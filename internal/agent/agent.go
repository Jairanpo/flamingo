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
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Jairanpo/flamingo/internal/config"
	"github.com/Jairanpo/flamingo/internal/ytdlp"
)

// DeviceHeader carries this computer's token to the server; it must match
// the server's name for it.
const DeviceHeader = "X-Flamingo-Device-Token"

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
	proxy  *httputil.ReverseProxy
	index  Index
	dl     *Downloader

	mu       sync.Mutex
	folder   string // where downloads go, from the server; "" until known
	reported []InventoryFile
	lastSent time.Time
	syncMu   sync.Mutex

	users userCache
}

func New(cfg *config.Agent, token string, log *slog.Logger) (*Agent, error) {
	u, err := url.Parse(cfg.ServerURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("server_url %q is not a full address (http://host:port)", cfg.ServerURL)
	}
	a := &Agent{Cfg: cfg, Token: token, Log: log, server: u,
		client: &http.Client{Timeout: 30 * time.Second}}
	a.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			// The server checks a form's Origin against the Host it was
			// sent to: keep the browser's, 127.0.0.1:8421.
			pr.Out.Host = pr.In.Host
			// Only this agent vouches for the computer; never the browser.
			pr.Out.Header.Del(DeviceHeader)
			pr.Out.Header.Set(DeviceHeader, token)
		},
		// Streams relay video for minutes; a flush per write keeps them live.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			a.Log.Warn("server unreachable", "path", r.URL.Path, "err", err)
			http.Error(w, "the Flamingo server is not answering", http.StatusBadGateway)
		},
	}
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

// Run serves the page and keeps the server's list of this computer's files
// current until ctx ends.
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
	mux.HandleFunc("POST /api/get/{vid}", a.signedIn(a.apiGet))
	mux.HandleFunc("POST /api/remove/{vid}", a.signedIn(a.apiRemove))
	mux.HandleFunc("GET /media/{vid}", a.signedIn(a.media))
	mux.HandleFunc("GET /api/subs/{vid}", a.signedIn(a.apiSubs))
	mux.HandleFunc("GET /subs/{vid}/{lang}", a.signedIn(a.subs))
	mux.HandleFunc("GET /api/jobs", a.signedIn(a.apiJobs))
	mux.HandleFunc("POST /api/jobs/clear", a.signedIn(a.apiJobsClear))
	// A folder changed in the app: look in the new place before answering,
	// so the page's next refresh already shows what is there.
	mux.HandleFunc("POST /api/downloads/device", a.thenSync)
	mux.HandleFunc("POST /api/downloads/platform", a.thenSync)
	mux.Handle("/", a.proxy)
	return sameOrigin(mux)
}

func (a *Agent) thenSync(w http.ResponseWriter, r *http.Request) {
	rec := httptest.NewRecorder()
	a.proxy.ServeHTTP(rec, r)
	if rec.Code == http.StatusOK {
		a.sync(r.Context(), true)
	}
	for k, v := range rec.Header() {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(rec.Body.Bytes())
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

// userCache remembers which session cookies the server accepted, so local
// routes (a seek in a video is many requests) don't each ask the server.
type userCache struct {
	mu sync.Mutex
	ok map[string]time.Time
}

func (c *userCache) fresh(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.ok[key]
	return ok && time.Since(t) < time.Minute
}

func (c *userCache) remember(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ok == nil || len(c.ok) > 256 {
		c.ok = map[string]time.Time{}
	}
	c.ok[key] = time.Now()
}

// signedIn lets a request reach this computer's files only when the server
// says its cookie belongs to a signed-in user.
func (a *Agent) signedIn(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie := r.Header.Get("Cookie")
		if cookie == "" || !a.users.fresh(cookie) {
			req, _ := http.NewRequestWithContext(r.Context(), "GET", a.Cfg.ServerURL+"/api/me", nil)
			req.Header.Set("Cookie", cookie)
			req.Header.Set(DeviceHeader, a.Token)
			resp, err := a.client.Do(req)
			if err != nil {
				fail(w, http.StatusBadGateway, "the Flamingo server is not answering")
				return
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				if strings.HasPrefix(r.URL.Path, "/api/") {
					fail(w, http.StatusUnauthorized, "sign in first")
				} else {
					http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
				}
				return
			}
			a.users.remember(cookie)
		}
		next(w, r)
	}
}

// sameOrigin refuses state-changing requests from another site's page, as
// the server does: this port is reachable by any page the browser opens.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if o, err := url.Parse(origin); err != nil || o.Host != r.Host {
				http.Error(w, "cross-site request refused", http.StatusForbidden)
				return
			}
		} else if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
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

// apiJobs is the server's cards (sources being added) plus this
// computer's downloads, in one list.
func (a *Agent) apiJobs(w http.ResponseWriter, r *http.Request) {
	var out struct {
		Jobs []any `json:"jobs"`
	}
	req, _ := http.NewRequestWithContext(r.Context(), "GET", a.Cfg.ServerURL+"/api/jobs", nil)
	req.Header.Set("Cookie", r.Header.Get("Cookie"))
	req.Header.Set(DeviceHeader, a.Token)
	if resp, err := a.client.Do(req); err == nil {
		if resp.StatusCode == http.StatusOK {
			_ = json.NewDecoder(resp.Body).Decode(&out)
		}
		resp.Body.Close()
	}
	if out.Jobs == nil {
		out.Jobs = []any{}
	}
	for _, j := range a.dl.Snapshot() {
		out.Jobs = append(out.Jobs, j)
	}
	writeJSON(w, http.StatusOK, out)
}

// apiJobsClear drops finished download cards here, then lets the server
// drop its own.
func (a *Agent) apiJobsClear(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(io.LimitReader(r.Body, 8<<10))
	var in struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(b, &in)
	a.dl.Forget(strings.TrimSpace(in.ID))
	r.Body = io.NopCloser(bytes.NewReader(b))
	r.ContentLength = int64(len(b))
	a.proxy.ServeHTTP(w, r)
}
