package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Jairanpo/flamingo/internal/auth"
	"github.com/Jairanpo/flamingo/internal/ticket"
)

// Computers and their download folders.
//
// The agent on each computer holds a device token, given to it once when it
// is paired. It sends the token on its own calls and on every page request
// it passes through, so the server knows which computer "on disk" means.
// A browser talking to the server directly sends none, and sees streaming
// only.

// deviceHeader carries the agent's token. The agent strips any copy that
// comes from the browser before adding its own.
const deviceHeader = "X-Flamingo-Device-Token"

var deviceNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// goosToPlatform maps the agent's runtime.GOOS to a platforms row.
var goosToPlatform = map[string]string{"linux": "LINUX", "darwin": "MACOS", "windows": "WINDOWS"}

type device struct {
	ID   int32
	Name string
}

func tokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// deviceOf names the computer a request came through, or nil.
func (s *Server) deviceOf(r *http.Request) (*device, error) {
	token := strings.TrimSpace(r.Header.Get(deviceHeader))
	if token == "" {
		return nil, nil
	}
	var d device
	err := s.pool.QueryRow(r.Context(), `SELECT devi_id, devi_name FROM devices WHERE devi_token_hash = $1`,
		tokenHash(token)).Scan(&d.ID, &d.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// pageDeviceHeader names the computer a page found its agent on. Unlike the
// token it proves nothing by itself, so it only counts for a computer the
// signed-in user paired. A page on another site cannot send it: the server
// allows no cross-site requests.
const pageDeviceHeader = "X-Flamingo-Device"

// pageDevice is the computer a signed-in page is on: the one whose agent
// passed the request along (its token), or the one the page names.
func (s *Server) pageDevice(r *http.Request) (*device, error) {
	d, err := s.deviceOf(r)
	if d != nil || err != nil {
		return d, err
	}
	name := strings.TrimSpace(r.Header.Get(pageDeviceHeader))
	u := CurrentUser(r)
	if name == "" || u == nil {
		return nil, nil
	}
	var pd device
	err = s.pool.QueryRow(r.Context(), `
		SELECT devi_id, devi_name FROM devices
		 WHERE devi_name = $1 AND user_id = $2 AND devi_token_hash IS NOT NULL`,
		name, u.ID).Scan(&pd.ID, &pd.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &pd, nil
}

func (s *Server) routesDevices(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/agent/pair", s.apiPair)
	mux.HandleFunc("POST /api/devices/pair-code", s.requireUser(s.apiPairCode))
	mux.HandleFunc("GET /api/devices/ticket", s.requireUser(s.apiTicket))
	mux.HandleFunc("GET /api/agent/config", s.requireDevice(s.apiAgentConfig))
	mux.HandleFunc("PUT /api/agent/inventory", s.requireDevice(s.apiInventory))
	mux.HandleFunc("GET /api/downloads", s.requireUser(s.apiDownloads))
	mux.HandleFunc("POST /api/downloads/platform", s.requireUser(s.apiSetPlatformDir))
	mux.HandleFunc("POST /api/downloads/device", s.requireUser(s.apiSetDeviceDir))
}

// requireDevice lets through only calls that carry a known device token:
// the agent's own calls, never a browser's.
func (s *Server) requireDevice(next func(http.ResponseWriter, *http.Request, *device)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d, err := s.deviceOf(r)
		if err != nil {
			s.failErr(w, err)
			return
		}
		if d == nil {
			fail(w, http.StatusUnauthorized, "unknown device; pair it again with: flamingo agent pair")
			return
		}
		next(w, r, d)
	}
}

// --- pairing ------------------------------------------------------------

// apiPair signs a computer in for good: {device, os} plus either a user's
// own {user, password} or a one-time {code} from the app gives back the
// token the agent keeps. Pairing a name again replaces its token, so a
// reinstalled computer keeps its downloads.
func (s *Server) apiPair(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var in struct{ User, Password, Code, Device, OS string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "bad request")
		return
	}
	code := normCode(in.Code)
	name := strings.ToLower(strings.TrimSpace(in.User))
	if code != "" {
		// Codes are guessed against no account in particular: count the
		// tries per address under one shared name.
		name = "\x00pairing-code"
	}
	addr, now := clientAddr(r), time.Now()
	if blocked, wait := s.limiter.Blocked(addr, name, now); blocked {
		fail(w, http.StatusTooManyRequests, "too many tries; wait "+wait.Round(time.Minute).String())
		return
	}
	if !deviceNameRE.MatchString(in.Device) {
		fail(w, http.StatusBadRequest, "a device name is letters, digits, dot, dash and underscore")
		return
	}
	var u struct {
		ID   int32
		Name string
	}
	if code != "" {
		// Used once: the DELETE both checks and spends it.
		err := s.pool.QueryRow(r.Context(), `
			DELETE FROM pairing_codes pc USING users us
			 WHERE pc.paco_code_hash = $1 AND pc.paco_expires_at > now() AND us.user_id = pc.user_id
			RETURNING us.user_id, us.user_name`, tokenHash(code)).Scan(&u.ID, &u.Name)
		if errors.Is(err, pgx.ErrNoRows) {
			s.limiter.Failed(addr, name, now)
			fail(w, http.StatusUnauthorized, "that code is wrong or ran out; make a new one in the app")
			return
		}
		if err != nil {
			s.failErr(w, err)
			return
		}
	} else {
		au, err := s.auth.Authenticate(r.Context(), name, in.Password)
		if errors.Is(err, auth.ErrBadLogin) {
			s.limiter.Failed(addr, name, now)
			fail(w, http.StatusUnauthorized, "wrong user name or password")
			return
		}
		if err != nil {
			s.failErr(w, err)
			return
		}
		u.ID, u.Name = au.ID, au.Name
	}
	s.limiter.Succeeded(addr, name)

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		s.failErr(w, err)
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	plat := goosToPlatform[in.OS] // "" (unknown) leaves it for the first config call
	_, err := s.pool.Exec(r.Context(), `
		INSERT INTO devices (devi_name, devi_token_hash, user_id, plat_code)
		VALUES ($1, $2, $3, NULLIF($4, ''))
		ON CONFLICT (devi_name) DO UPDATE
		   SET devi_token_hash = EXCLUDED.devi_token_hash, user_id = EXCLUDED.user_id,
		       plat_code = COALESCE(EXCLUDED.plat_code, devices.plat_code)`,
		in.Device, tokenHash(token), u.ID, plat)
	if err != nil {
		s.failErr(w, err)
		return
	}
	s.log.Info("device paired", "device", in.Device, "user", u.Name, "os", in.OS)
	writeJSON(w, http.StatusOK, map[string]any{"device": in.Device, "token": token})
}

// --- pairing from the app, and tickets ----------------------------------

const (
	pairCodeTTL   = 5 * time.Minute
	pairCodeChars = "ABCDEFGHJKMNPQRSTUVWXYZ23456789" // no 0/O, 1/I/L
)

// normCode makes a typed code comparable: capitals, no dash or spaces.
func normCode(c string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(c) {
		if strings.ContainsRune(pairCodeChars, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// apiPairCode hands the page a one-time code for pairing a computer to this
// account, shown as XXXX-XXXX.
func (s *Server) apiPairCode(w http.ResponseWriter, r *http.Request) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		s.failErr(w, err)
		return
	}
	code := make([]byte, len(raw))
	for i, b := range raw {
		code[i] = pairCodeChars[int(b)%len(pairCodeChars)]
	}
	u := CurrentUser(r)
	expires := time.Now().Add(pairCodeTTL)
	if _, err := s.pool.Exec(r.Context(), `
		WITH swept AS (DELETE FROM pairing_codes WHERE paco_expires_at <= now())
		INSERT INTO pairing_codes (paco_code_hash, user_id, paco_expires_at) VALUES ($1, $2, $3)`,
		tokenHash(string(code)), u.ID, expires); err != nil {
		s.failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code": string(code[:4]) + "-" + string(code[4:]), "expires": expires.Unix()})
}

// apiTicket writes the page a pass for one of this user's computers
// (?device=name), which that computer's agent checks on its own.
func (s *Server) apiTicket(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	name := strings.TrimSpace(r.URL.Query().Get("device"))
	var key []byte
	err := s.pool.QueryRow(r.Context(), `
		SELECT devi_token_hash FROM devices
		 WHERE devi_name = $1 AND user_id = $2 AND devi_token_hash IS NOT NULL`,
		name, u.ID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(w, http.StatusNotFound, "no such computer paired to your account")
		return
	}
	if err != nil {
		s.failErr(w, err)
		return
	}
	exp := time.Now().Add(ticket.TTL).Unix()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"ticket":  ticket.Sign(key, ticket.Claims{Device: name, User: u.ID, Expiry: exp}),
		"expires": exp,
	})
}

// --- the agent's own calls ------------------------------------------------

type folder struct {
	Device   string  `json:"device"`
	Platform *string `json:"platform"`
	// MediaDir is where downloads go: the computer's own folder, or else its
	// platform's. "~" means the agent user's home.
	MediaDir string `json:"media_dir"`
	Own      bool   `json:"own"` // the computer has its own folder
}

func (s *Server) folderOf(ctx context.Context, id int32) (folder, error) {
	var f folder
	var own *string
	var plat *string
	err := s.pool.QueryRow(ctx, `
		SELECT d.devi_name, d.plat_code, d.devi_media_dir, p.plat_media_dir
		  FROM devices d LEFT JOIN platforms p USING (plat_code)
		 WHERE d.devi_id = $1`, id).Scan(&f.Device, &f.Platform, &own, &plat)
	if err != nil {
		return f, err
	}
	switch {
	case own != nil:
		f.MediaDir, f.Own = *own, true
	case plat != nil:
		f.MediaDir = *plat
	default:
		// Not known yet what this computer runs: Linux's folder, the most
		// common answer in this house.
		err = s.pool.QueryRow(ctx, `SELECT plat_media_dir FROM platforms WHERE plat_code = 'LINUX'`).Scan(&f.MediaDir)
	}
	return f, err
}

// apiAgentConfig answers where this computer keeps its files. The agent
// asks on start and every few minutes, saying what it runs on (?os=), so a
// folder changed in the app reaches it without a restart.
func (s *Server) apiAgentConfig(w http.ResponseWriter, r *http.Request, d *device) {
	plat := goosToPlatform[r.URL.Query().Get("os")]
	if _, err := s.pool.Exec(r.Context(), `
		UPDATE devices SET devi_last_seen_at = now(), plat_code = COALESCE(NULLIF($2, ''), plat_code)
		 WHERE devi_id = $1`, d.ID, plat); err != nil {
		s.failErr(w, err)
		return
	}
	f, err := s.folderOf(r.Context(), d.ID)
	if err != nil {
		s.failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

type inventoryFile struct {
	ID    string `json:"id"`
	Size  *int64 `json:"size"`
	Audio bool   `json:"audio"`
}

// apiInventory replaces this computer's list of downloads with what its
// agent found on disk. Files for videos not in the catalog are skipped.
func (s *Server) apiInventory(w http.ResponseWriter, r *http.Request, d *device) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	var in struct {
		Files []inventoryFile `json:"files"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "bad inventory")
		return
	}
	seen := map[string]bool{}
	files := in.Files[:0]
	for _, f := range in.Files {
		if vidRE.MatchString(f.ID) && !seen[f.ID] {
			seen[f.ID] = true
			files = append(files, f)
		}
	}
	raw, _ := json.Marshal(files)
	// Both halves in one statement: the rows deleted and the rows written
	// never overlap, and the list is never seen half replaced.
	tag, err := s.pool.Exec(r.Context(), `
		WITH f AS (SELECT * FROM jsonb_to_recordset($2::jsonb) AS x(id text, size bigint, audio boolean)),
		gone AS (DELETE FROM device_videos dv
		          WHERE dv.devi_id = $1 AND NOT EXISTS (SELECT 1 FROM f WHERE f.id = dv.vid_id))
		INSERT INTO device_videos (devi_id, vid_id, dvvi_size_bytes, dvvi_downloaded_at, dvvi_audio_only)
		SELECT $1, f.id, f.size, now(), f.audio FROM f JOIN videos v ON v.vid_id = f.id
		ON CONFLICT (devi_id, vid_id) DO UPDATE
		   SET dvvi_size_bytes = EXCLUDED.dvvi_size_bytes, dvvi_audio_only = EXCLUDED.dvvi_audio_only`,
		d.ID, string(raw))
	if err != nil {
		s.failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": len(files), "known": tag.RowsAffected()})
}

// --- folder settings, from the page ----------------------------------------

type platformJSON struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	MediaDir string `json:"media_dir"`
}

// apiDownloads is the Downloads sheet: every platform's folder, and this
// computer's when the page came through an agent.
func (s *Server) apiDownloads(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pool.Query(r.Context(),
		`SELECT plat_code, plat_name, plat_media_dir FROM platforms ORDER BY plat_name`)
	if err != nil {
		s.failErr(w, err)
		return
	}
	plats, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (platformJSON, error) {
		var p platformJSON
		return p, row.Scan(&p.Code, &p.Name, &p.MediaDir)
	})
	if err != nil {
		s.failErr(w, err)
		return
	}
	out := map[string]any{"platforms": plats, "device": nil, "admin": CurrentUser(r).IsAdmin()}
	d, err := s.pageDevice(r)
	if err != nil {
		s.failErr(w, err)
		return
	}
	if d != nil {
		f, err := s.folderOf(r.Context(), d.ID)
		if err != nil {
			s.failErr(w, err)
			return
		}
		out["device"] = f
	}
	writeJSON(w, http.StatusOK, out)
}

func cleanDir(s string) string { return strings.TrimSpace(s) }

// apiSetPlatformDir changes a platform's default folder. It moves where
// every computer on that system looks, so only an admin may.
func (s *Server) apiSetPlatformDir(w http.ResponseWriter, r *http.Request) {
	if !CurrentUser(r).IsAdmin() {
		fail(w, http.StatusForbidden, "only an admin can change the default folders")
		return
	}
	b := body(r)
	dir := cleanDir(str(b, "media_dir"))
	if dir == "" || len(dir) > 512 {
		fail(w, http.StatusBadRequest, "a folder is required")
		return
	}
	tag, err := s.pool.Exec(r.Context(), `UPDATE platforms SET plat_media_dir = $2 WHERE plat_code = $1`,
		str(b, "code"), dir)
	if err != nil {
		s.failErr(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		fail(w, http.StatusNotFound, "no such platform")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// apiSetDeviceDir gives this computer its own folder; an empty one goes
// back to its platform's.
func (s *Server) apiSetDeviceDir(w http.ResponseWriter, r *http.Request) {
	d, err := s.pageDevice(r)
	if err != nil {
		s.failErr(w, err)
		return
	}
	if d == nil {
		fail(w, http.StatusBadRequest, "open Flamingo through this computer's agent to set its folder")
		return
	}
	dir := cleanDir(str(body(r), "media_dir"))
	if len(dir) > 512 {
		fail(w, http.StatusBadRequest, "that folder name is too long")
		return
	}
	if _, err := s.pool.Exec(r.Context(), `UPDATE devices SET devi_media_dir = NULLIF($2, '') WHERE devi_id = $1`,
		d.ID, dir); err != nil {
		s.failErr(w, err)
		return
	}
	f, err := s.folderOf(r.Context(), d.ID)
	if err != nil {
		s.failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}
