package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Jairanpo/flamingo/internal/config"
	"github.com/Jairanpo/flamingo/internal/ticket"
	"github.com/Jairanpo/flamingo/internal/ytdlp"
)

// DeviceHeader carries this computer's token to the server; it must match
// the server's name for it.
const DeviceHeader = "X-Flamingo-Device-Token"

// TicketHeader carries the pass a page got from the server (package
// ticket). A video or subtitle address, which cannot carry a header, puts
// it in ?t= instead.
const TicketHeader = "X-Flamingo-Ticket"

func init() {
	_ = mime.AddExtensionType(".m4a", "audio/mp4")
	_ = mime.AddExtensionType(".vtt", "text/vtt; charset=utf-8")
}

var vidRE = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// Agent is one computer's helper.
type Agent struct {
	Cfg   *config.Agent
	Token string
	Log   *slog.Logger

	server *url.URL
	client *http.Client
	index  Index
	dl     *Downloader

	mu       sync.Mutex
	folder   string // where downloads go, from the server; "" until known
	reported []InventoryFile
	lastSent time.Time
	syncMu   sync.Mutex

	// origins are the page addresses allowed to call this agent from
	// another address: the server's own, and any in page_origins.
	origins map[string]bool
	key     []byte // checks tickets: sha256 of the token
}

func New(cfg *config.Agent, token string, log *slog.Logger) (*Agent, error) {
	u, err := url.Parse(cfg.ServerURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("server_url %q is not a full address (http://host:port)", cfg.ServerURL)
	}
	a := &Agent{Cfg: cfg, Token: token, Log: log, server: u,
		client:  &http.Client{Timeout: 30 * time.Second},
		origins: pageOrigins(u, cfg.PageOrigins),
		key:     ticket.KeyOf(token)}
	a.dl = &Downloader{
		YT:       ytdlp.Runner{Path: cfg.Ytdlp.Path, ExtraArgs: cfg.Ytdlp.ExtraArgs},
		Index:    &a.index,
		SubLangs: cfg.Download.SubLangs,
		Folder:   a.mediaDir,
		Changed:  func() { a.sync(context.Background(), true) },
	}
	return a, nil
}

// mediaDir is where downloads go now, as a path on this computer.
func (a *Agent) mediaDir() (string, error) {
	a.mu.Lock()
	dir := a.folder
	a.mu.Unlock()
	if dir == "" {
		return "", errors.New("the download folder is not known yet: is the server reachable?")
	}
	return dir, nil
}

// Run answers the Flamingo page and keeps the server's list of this
// computer's files current until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	a.dl.Start(ctx, a.Cfg.Download.Workers)
	go a.loop(ctx)
	srv := &http.Server{Addr: a.Cfg.Listen, Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	a.Log.Info("agent listening", "addr", a.Cfg.Listen, "server", a.Cfg.ServerURL, "device", a.Cfg.DeviceName)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	}
}

// loop asks the server where files go and tells it what is there: at once,
// then every two minutes. A folder changed in the app is picked up here.
func (a *Agent) loop(ctx context.Context) {
	t := time.NewTicker(2 * time.Minute)
	defer t.Stop()
	for {
		a.sync(ctx, false)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// sync fetches the folder, scans it, and reports the files when they
// changed (or every ten minutes, or when force is set).
func (a *Agent) sync(ctx context.Context, force bool) {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	var f struct {
		MediaDir string `json:"media_dir"`
	}
	if err := a.call(ctx, "GET", "/api/agent/config?os="+runtime.GOOS, nil, &f); err != nil {
		a.Log.Warn("asking the server for the download folder", "err", err)
		return
	}
	dir, err := config.ExpandHome(f.MediaDir)
	if err != nil {
		a.Log.Error("download folder", "err", err)
		return
	}
	a.mu.Lock()
	if a.folder != dir {
		a.Log.Info("download folder", "dir", dir)
	}
	a.folder = dir
	a.mu.Unlock()
	if err := a.index.Scan(dir); err != nil {
		a.Log.Warn("scanning the download folder", "dir", dir, "err", err)
		return
	}
	inv := a.index.Inventory()
	a.mu.Lock()
	same := slices.EqualFunc(inv, a.reported, func(x, y InventoryFile) bool {
		return x.ID == y.ID && x.Audio == y.Audio && *x.Size == *y.Size
	})
	stale := time.Since(a.lastSent) > 10*time.Minute
	a.mu.Unlock()
	if same && !stale && !force {
		return
	}
	if err := a.call(ctx, "PUT", "/api/agent/inventory", map[string]any{"files": inv}, nil); err != nil {
		a.Log.Warn("reporting downloads", "err", err)
		return
	}
	a.mu.Lock()
	a.reported, a.lastSent = inv, time.Now()
	a.mu.Unlock()
}

// call is the agent's own request to the server, signed with its token.
func (a *Agent) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.Cfg.ServerURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set(DeviceHeader, a.Token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return fmt.Errorf("%s %s: %s", method, path, e.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// --- the browser's side ---------------------------------------------------

func (a *Agent) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/get/{vid}", a.ticketed(a.apiGet))
	mux.HandleFunc("POST /api/remove/{vid}", a.ticketed(a.apiRemove))
	mux.HandleFunc("GET /media/{vid}", a.ticketed(a.media))
	mux.HandleFunc("GET /api/subs/{vid}", a.ticketed(a.apiSubs))
	mux.HandleFunc("GET /subs/{vid}/{lang}", a.ticketed(a.subs))
	mux.HandleFunc("GET /api/jobs", a.ticketed(a.apiJobs))
	mux.HandleFunc("POST /api/jobs/clear", a.ticketed(a.apiJobsClear))
	// Is there an agent here, and for which computer? Then, after a folder
	// change in the app, look again.
	mux.HandleFunc("GET /api/hello", a.apiHello)
	mux.HandleFunc("POST /api/sync", a.ticketed(a.apiSync))
	// The agent has no page of its own any more: an old bookmark to it
	// lands on the same view at the server.
	mux.HandleFunc("GET /", a.toServer)
	return a.cors(a.sameOrigin(mux))
}

func (a *Agent) toServer(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, a.Cfg.ServerURL+r.URL.RequestURI(), http.StatusFound)
}

// pageOrigins is the set of page addresses allowed in: the server's own,
// with localhost and 127.0.0.1 counting as one when it is on this machine,
// and any the config names.
func pageOrigins(server *url.URL, extra []string) map[string]bool {
	set := map[string]bool{}
	add := func(scheme, host string) { set[strings.ToLower(scheme+"://"+host)] = true }
	add(server.Scheme, server.Host)
	switch server.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		port := server.Port()
		for _, h := range []string{"localhost", "127.0.0.1", "[::1]"} {
			if port != "" {
				h += ":" + port
			}
			add(server.Scheme, h)
		}
	}
	for _, o := range extra {
		if u, err := url.Parse(strings.TrimSpace(o)); err == nil && u.Scheme != "" && u.Host != "" {
			add(u.Scheme, u.Host)
		}
	}
	return set
}

// cors lets the Flamingo page, served from the server's address, call this
// agent. Other sites get no answer they can read.
func (a *Agent) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allowed := origin != "" && a.origins[strings.ToLower(origin)]
		if allowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			if !allowed {
				http.Error(w, "cross-site request refused", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, "+TicketHeader)
			w.Header().Set("Access-Control-Max-Age", "600")
			// Chrome asks before a public page may reach this computer.
			if r.Header.Get("Access-Control-Request-Private-Network") == "true" {
				w.Header().Set("Access-Control-Allow-Private-Network", "true")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// apiHello tells a page there is an agent here, and for which computer.
func (a *Agent) apiHello(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"device": a.Cfg.DeviceName, "paired": true})
}

// apiSync looks in the download folder again and tells the server, so a
// page's next refresh shows what is there.
func (a *Agent) apiSync(w http.ResponseWriter, r *http.Request) {
	a.sync(r.Context(), true)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// ticketed lets a request reach this computer's files only with a ticket
// the server wrote for this computer (package ticket): in the
// X-Flamingo-Ticket header, or in ?t= for a video or subtitle address.
func (a *Agent) ticketed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tk := r.Header.Get(TicketHeader)
		if tk == "" {
			tk = r.URL.Query().Get("t")
		}
		if tk == "" {
			fail(w, http.StatusUnauthorized, "a ticket from the Flamingo page is needed")
			return
		}
		if _, err := ticket.Verify(a.key, a.Cfg.DeviceName, tk, time.Now()); err != nil {
			fail(w, http.StatusUnauthorized, "ticket refused: "+strings.TrimPrefix(err.Error(), "ticket: "))
			return
		}
		next(w, r)
	}
}

// sameOrigin refuses state-changing requests from any page but Flamingo's:
// this port is reachable by every page the browser opens. A program on
// this computer (no Origin, no Sec-Fetch-Site) is not a browser page.
func (a *Agent) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if !a.origins[strings.ToLower(origin)] {
				http.Error(w, "cross-site request refused", http.StatusForbidden)
				return
			}
		} else if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "none" {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Agent) apiGet(w http.ResponseWriter, r *http.Request) {
	vid := r.PathValue("vid")
	if !vidRE.MatchString(vid) {
		fail(w, http.StatusBadRequest, "bad id")
		return
	}
	var in struct {
		Audio  bool   `json:"audio"`
		Height *int   `json:"height"`
		Title  string `json:"title"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&in)
	h := a.Cfg.Download.MaxHeight
	if in.Height != nil && *in.Height >= 0 {
		h = *in.Height
	}
	title := strings.TrimSpace(in.Title)
	if len(title) > 200 {
		title = title[:200]
	}
	writeJSON(w, http.StatusOK, a.dl.Enqueue(vid, title, in.Audio, h))
}

// apiRemove deletes a video's file and its subtitles; the catalog keeps it.
func (a *Agent) apiRemove(w http.ResponseWriter, r *http.Request) {
	vid := r.PathValue("vid")
	if !vidRE.MatchString(vid) {
		fail(w, http.StatusBadRequest, "bad id")
		return
	}
	for _, p := range a.index.Paths(vid) {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	a.sync(r.Context(), true)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *Agent) media(w http.ResponseWriter, r *http.Request) {
	vid := r.PathValue("vid")
	f, ok := a.index.Get(vid)
	if ok {
		if _, err := os.Stat(f.Path); err != nil {
			ok = false
		}
	}
	if !ok {
		// Gone from disk since the last look: tell the server, so the card
		// offers Download again.
		go a.sync(context.Background(), true)
		fail(w, http.StatusNotFound, "this file is not on this computer")
		return
	}
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeFile(w, r, f.Path)
}

func (a *Agent) apiSubs(w http.ResponseWriter, r *http.Request) {
	langs := a.index.Langs(r.PathValue("vid"))
	if langs == nil {
		langs = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"langs": langs})
}

func (a *Agent) subs(w http.ResponseWriter, r *http.Request) {
	p, ok := a.index.Sub(r.PathValue("vid"), r.PathValue("lang"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	http.ServeFile(w, r, p)
}

// apiJobs is this computer's download cards; the page adds the server's.
func (a *Agent) apiJobs(w http.ResponseWriter, r *http.Request) {
	jobs := []any{}
	for _, j := range a.dl.Snapshot() {
		jobs = append(jobs, j)
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

// apiJobsClear drops a finished download card.
func (a *Agent) apiJobsClear(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&in)
	a.dl.Forget(strings.TrimSpace(in.ID))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
