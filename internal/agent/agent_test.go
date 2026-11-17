package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Jairanpo/flamingo/internal/config"
)

func touch(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIndexScan(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "Chan", "2024-01-01_-_One_[aaaaaaaaaa1].mp4"), "video!")
	touch(t, filepath.Join(dir, "Chan", "2024-01-01_-_One_[aaaaaaaaaa1].en.vtt"), "WEBVTT")
	touch(t, filepath.Join(dir, "Chan", "2024-01-01_-_One_[aaaaaaaaaa1].m4a"), "sound, bigger than the video")
	touch(t, filepath.Join(dir, "Other", "Two_[aaaaaaaaaa2].m4a"), "sound")
	touch(t, filepath.Join(dir, "Other", "Three_[aaaaaaaaaa3].mp4.part"), "half")
	touch(t, filepath.Join(dir, "notes.txt"), "x")

	var x Index
	if err := x.Scan(dir); err != nil {
		t.Fatal(err)
	}
	if f, ok := x.Get("aaaaaaaaaa1"); !ok || f.Audio || !strings.HasSuffix(f.Path, ".mp4") {
		t.Errorf("v1 = %+v %v, want the mp4 over the m4a", f, ok)
	}
	if f, ok := x.Get("aaaaaaaaaa2"); !ok || !f.Audio {
		t.Errorf("v2 = %+v %v, want audio", f, ok)
	}
	if _, ok := x.Get("aaaaaaaaaa3"); ok {
		t.Error("a .part file counted as downloaded")
	}
	if l := x.Langs("aaaaaaaaaa1"); len(l) != 1 || l[0] != "en" {
		t.Errorf("langs = %v", l)
	}
	inv := x.Inventory()
	if len(inv) != 2 || inv[0].ID != "aaaaaaaaaa1" || *inv[0].Size != 6 {
		t.Errorf("inventory = %+v", inv)
	}
	// A folder that does not exist yet is simply empty.
	if err := x.Scan(filepath.Join(dir, "nope")); err != nil || len(x.Inventory()) != 0 {
		t.Errorf("missing folder: %v %v", err, x.Inventory())
	}
}

func TestDownloaderForget(t *testing.T) {
	d := &Downloader{}
	d.queue = make(chan request, 4)
	d.once.Do(func() {})
	a := d.Enqueue("aaaaaaaaaa1", "One", false, 720)
	if a.State != "queued" || a.Title != "One" || a.Kind != "download" {
		t.Fatalf("job = %+v", a)
	}
	if again := d.Enqueue("aaaaaaaaaa1", "One", false, 720); len(d.Snapshot()) != 1 || again.ID != a.ID {
		t.Fatal("a second click started a second job")
	}
	if d.Forget("") {
		t.Error("a queued job was forgotten")
	}
	d.jobs[0].State = "done"
	if !d.Forget("aaaaaaaaaa1") || len(d.Snapshot()) != 0 {
		t.Error("a finished job was kept")
	}
}

// fakeServer plays the Flamingo server: one signed-in cookie, a folder,
// and a record of what the agent sends.
type fakeServer struct {
	mu        sync.Mutex
	dir       string
	inventory []InventoryFile
	seenToken []string
}

func (f *fakeServer) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/agent/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(DeviceHeader) != "tok" || r.URL.Query().Get("os") == "" {
			http.Error(w, "no", 401)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"media_dir": f.dir})
	})
	mux.HandleFunc("PUT /api/agent/inventory", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Files []InventoryFile }
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.inventory = in.Files
		f.mu.Unlock()
		_, _ = io.WriteString(w, "{}")
	})
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		if c, _ := r.Cookie("flamingo_session"); c == nil || c.Value != "good" {
			http.Error(w, `{"error":"sign in first"}`, 401)
			return
		}
		_, _ = io.WriteString(w, `{"name":"ana"}`)
	})
	mux.HandleFunc("GET /api/jobs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"jobs":[{"id":"src:@x","kind":"source","state":"done"}]}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seenToken = append(f.seenToken, strings.Join(r.Header.Values(DeviceHeader), ","))
		f.mu.Unlock()
		_, _ = io.WriteString(w, "page for "+r.Host)
	})
	return mux
}

func newAgent(t *testing.T) (*Agent, *fakeServer, http.Handler) {
	t.Helper()
	fs := &fakeServer{dir: t.TempDir()}
	srv := httptest.NewServer(fs.handler(t))
	t.Cleanup(srv.Close)
	cfg := &config.Agent{ServerURL: srv.URL, Download: config.AgentDownload{MaxHeight: 1080, Workers: 1}}
	a, err := New(cfg, "tok", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return a, fs, a.Handler()
}

func send(h http.Handler, method, path, cookie string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8421"+path, nil)
	if cookie != "" {
		r.Header.Set("Cookie", "flamingo_session="+cookie)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestProxyVouchesForTheComputer(t *testing.T) {
	_, fs, h := newAgent(t)
	// A browser claiming to be some computer is ignored: only the agent's
	// own token goes through.
	w := send(h, "GET", "/", "good", map[string]string{DeviceHeader: "forged"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "127.0.0.1:8421") {
		t.Fatalf("page = %d %q (the browser's Host must reach the server)", w.Code, w.Body.String())
	}
	if len(fs.seenToken) != 1 || fs.seenToken[0] != "tok" {
		t.Errorf("server saw tokens %v, want just the agent's", fs.seenToken)
	}
}

func TestSyncAndMedia(t *testing.T) {
	a, fs, h := newAgent(t)
	touch(t, filepath.Join(fs.dir, "C", "x_[aaaaaaaaaa1].mp4"), "0123456789")
	a.sync(context.Background(), false)
	if len(fs.inventory) != 1 || fs.inventory[0].ID != "aaaaaaaaaa1" {
		t.Fatalf("inventory = %+v", fs.inventory)
	}

	if w := send(h, "GET", "/media/aaaaaaaaaa1", "", nil); w.Code != 401 && w.Code != http.StatusSeeOther {
		t.Errorf("media without a session = %d", w.Code)
	}
	w := send(h, "GET", "/media/aaaaaaaaaa1", "good", map[string]string{"Range": "bytes=2-4"})
	if w.Code != http.StatusPartialContent || w.Body.String() != "234" {
		t.Errorf("range = %d %q", w.Code, w.Body.String())
	}

	// Removed outside the app: /media says so and the server hears of it.
	os.Remove(filepath.Join(fs.dir, "C", "x_[aaaaaaaaaa1].mp4"))
	if w := send(h, "GET", "/media/aaaaaaaaaa1", "good", nil); w.Code != 404 {
		t.Errorf("missing file = %d", w.Code)
	}
	a.sync(context.Background(), false)
	if len(fs.inventory) != 0 {
		t.Errorf("inventory after delete = %+v", fs.inventory)
	}
}

func TestRemoveAndJobs(t *testing.T) {
	a, fs, h := newAgent(t)
	touch(t, filepath.Join(fs.dir, "C", "x_[aaaaaaaaaa1].mp4"), "v")
	touch(t, filepath.Join(fs.dir, "C", "x_[aaaaaaaaaa1].es.vtt"), "WEBVTT")
	a.sync(context.Background(), false)

	if w := send(h, "POST", "/api/remove/aaaaaaaaaa1", "good",
		map[string]string{"Origin": "http://evil.example"}); w.Code != 403 {
		t.Errorf("cross-site remove = %d", w.Code)
	}
	if w := send(h, "POST", "/api/remove/aaaaaaaaaa1", "good",
		map[string]string{"Origin": "http://127.0.0.1:8421"}); w.Code != 200 {
		t.Fatalf("remove = %d %s", w.Code, w.Body.String())
	}
	left, _ := filepath.Glob(filepath.Join(fs.dir, "C", "*"))
	if len(left) != 0 || len(fs.inventory) != 0 {
		t.Errorf("left %v, inventory %+v", left, fs.inventory)
	}

	a.dl.queue = make(chan request, 1) // no workers: the card stays queued
	a.dl.once.Do(func() {})
	a.dl.Enqueue("aaaaaaaaaa2", "Two", true, 0)
	w := send(h, "GET", "/api/jobs", "good", nil)
	var out struct{ Jobs []map[string]any }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.Jobs) != 2 || out.Jobs[0]["kind"] != "source" || out.Jobs[1]["title"] != "Two" {
		t.Errorf("jobs = %s", w.Body.String())
	}
}
