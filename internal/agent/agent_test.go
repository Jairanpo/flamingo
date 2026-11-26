package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jairanpo/flamingo/internal/config"
	"github.com/Jairanpo/flamingo/internal/ticket"
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

// fakeServer plays the Flamingo server: a folder, and a record of what the
// agent reports.
type fakeServer struct {
	mu        sync.Mutex
	dir       string
	inventory []InventoryFile
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
	return mux
}

func newAgent(t *testing.T) (*Agent, *fakeServer, http.Handler) {
	t.Helper()
	fs := &fakeServer{dir: t.TempDir()}
	srv := httptest.NewServer(fs.handler(t))
	t.Cleanup(srv.Close)
	cfg := &config.Agent{ServerURL: srv.URL, DeviceName: "desk",
		Download: config.AgentDownload{MaxHeight: 1080, Workers: 1}}
	a, err := New(cfg, "tok", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return a, fs, a.Handler()
}

// pass is a good ticket for the test agent's computer.
func pass() string {
	return ticket.Sign(ticket.KeyOf("tok"), ticket.Claims{Device: "desk", User: 1, Expiry: time.Now().Add(time.Minute).Unix()})
}

// send makes a request as the Flamingo page would, with tk as its ticket.
func send(h http.Handler, method, path, tk string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8421"+path, nil)
	if tk != "" {
		r.Header.Set(TicketHeader, tk)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestOldAddressGoesToServer(t *testing.T) {
	a, _, h := newAgent(t)
	w := send(h, "GET", "/?source=PLx", "", nil)
	if w.Code != http.StatusFound || w.Header().Get("Location") != a.Cfg.ServerURL+"/?source=PLx" {
		t.Errorf("old page = %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := send(h, "GET", "/api/state", "", nil); w.Code != 404 {
		t.Errorf("server api through the agent = %d", w.Code)
	}
}

func TestSyncAndMedia(t *testing.T) {
	a, fs, h := newAgent(t)
	touch(t, filepath.Join(fs.dir, "C", "x_[aaaaaaaaaa1].mp4"), "0123456789")
	a.sync(context.Background(), false)
	if len(fs.inventory) != 1 || fs.inventory[0].ID != "aaaaaaaaaa1" {
		t.Fatalf("inventory = %+v", fs.inventory)
	}

	if w := send(h, "GET", "/media/aaaaaaaaaa1", "", nil); w.Code != 401 {
		t.Errorf("media without a ticket = %d", w.Code)
	}
	w := send(h, "GET", "/media/aaaaaaaaaa1", pass(), map[string]string{"Range": "bytes=2-4"})
	if w.Code != http.StatusPartialContent || w.Body.String() != "234" {
		t.Errorf("range = %d %q", w.Code, w.Body.String())
	}

	// Removed outside the app: /media says so and the server hears of it.
	os.Remove(filepath.Join(fs.dir, "C", "x_[aaaaaaaaaa1].mp4"))
	if w := send(h, "GET", "/media/aaaaaaaaaa1", pass(), nil); w.Code != 404 {
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

	page := map[string]string{"Origin": a.Cfg.ServerURL}
	if w := send(h, "POST", "/api/remove/aaaaaaaaaa1", pass(),
		map[string]string{"Origin": "http://evil.example"}); w.Code != 403 {
		t.Errorf("cross-site remove = %d", w.Code)
	}
	if w := send(h, "POST", "/api/remove/aaaaaaaaaa1", pass(), page); w.Code != 200 {
		t.Fatalf("remove = %d %s", w.Code, w.Body.String())
	}
	left, _ := filepath.Glob(filepath.Join(fs.dir, "C", "*"))
	if len(left) != 0 || len(fs.inventory) != 0 {
		t.Errorf("left %v, inventory %+v", left, fs.inventory)
	}

	a.dl.queue = make(chan request, 1) // no workers: the card stays queued
	a.dl.once.Do(func() {})
	a.dl.Enqueue("aaaaaaaaaa2", "Two", true, 0)
	w := send(h, "GET", "/api/jobs", pass(), nil)
	var out struct{ Jobs []map[string]any }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.Jobs) != 1 || out.Jobs[0]["title"] != "Two" {
		t.Errorf("jobs = %s", w.Body.String())
	}
}

// A page served by the server itself calls the agent from the server's
// address, with a ticket instead of the agent's cookie.
func TestTicketsFromTheServersPage(t *testing.T) {
	a, fs, h := newAgent(t)
	touch(t, filepath.Join(fs.dir, "C", "x_[aaaaaaaaaa1].mp4"), "0123456789")
	a.sync(context.Background(), false)
	page := strings.TrimRight(a.Cfg.ServerURL, "/") // http://127.0.0.1:<port>
	local := strings.Replace(page, "127.0.0.1", "localhost", 1)
	good := ticket.Sign(ticket.KeyOf("tok"), ticket.Claims{Device: "desk", User: 1, Expiry: time.Now().Add(time.Minute).Unix()})
	other := ticket.Sign(ticket.KeyOf("tok"), ticket.Claims{Device: "laptop", User: 1, Expiry: time.Now().Add(time.Minute).Unix()})
	old := ticket.Sign(ticket.KeyOf("tok"), ticket.Claims{Device: "desk", User: 1, Expiry: time.Now().Add(-time.Second).Unix()})
	forged := ticket.Sign(ticket.KeyOf("not-tok"), ticket.Claims{Device: "desk", User: 1, Expiry: time.Now().Add(time.Minute).Unix()})

	// Hello: readable by the server's page (either local name), not others.
	w := send(h, "GET", "/api/hello", "", map[string]string{"Origin": local})
	if w.Code != 200 || w.Header().Get("Access-Control-Allow-Origin") != local || !strings.Contains(w.Body.String(), `"desk"`) {
		t.Errorf("hello = %d %q %s", w.Code, w.Header().Get("Access-Control-Allow-Origin"), w.Body.String())
	}
	if w := send(h, "GET", "/api/hello", "", map[string]string{"Origin": "http://evil.example"}); w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("another site may read hello")
	}

	// Preflight: allowed for the page, with Chrome's local-network consent.
	w = send(h, "OPTIONS", "/api/get/aaaaaaaaaa2", "", map[string]string{"Origin": page,
		"Access-Control-Request-Method": "POST", "Access-Control-Request-Private-Network": "true"})
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Private-Network") != "true" ||
		!strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), TicketHeader) {
		t.Errorf("preflight = %d %v", w.Code, w.Header())
	}
	if w := send(h, "OPTIONS", "/api/get/aaaaaaaaaa2", "", map[string]string{"Origin": "http://evil.example",
		"Access-Control-Request-Method": "POST"}); w.Code != 403 {
		t.Errorf("evil preflight = %d", w.Code)
	}

	// Media by ticket in the address; refused when the ticket is wrong.
	for name, tk := range map[string]string{"other computer": other, "expired": old, "forged": forged} {
		if w := send(h, "GET", "/media/aaaaaaaaaa1?t="+tk, "", nil); w.Code != 401 {
			t.Errorf("%s ticket = %d", name, w.Code)
		}
	}
	if w := send(h, "GET", "/media/aaaaaaaaaa1?t="+good, "", map[string]string{"Range": "bytes=0-1"}); w.Code != 206 || w.Body.String() != "01" {
		t.Errorf("media by ticket = %d %q", w.Code, w.Body.String())
	}

	// A download from the page, ticket in the header; another site's page
	// is refused even with a good ticket.
	a.dl.queue = make(chan request, 1)
	a.dl.once.Do(func() {})
	if w := send(h, "POST", "/api/get/aaaaaaaaaa2", "", map[string]string{"Origin": "http://evil.example", TicketHeader: good}); w.Code != 403 {
		t.Errorf("evil get = %d", w.Code)
	}
	if w := send(h, "POST", "/api/get/aaaaaaaaaa2", "", map[string]string{"Origin": page, TicketHeader: good}); w.Code != 200 {
		t.Errorf("get by ticket = %d %s", w.Code, w.Body.String())
	}
	if w := send(h, "POST", "/api/sync", "", map[string]string{"Origin": page, TicketHeader: good}); w.Code != 200 {
		t.Errorf("sync = %d", w.Code)
	}
	if w := send(h, "POST", "/api/sync", "", map[string]string{"Origin": page}); w.Code != 401 {
		t.Errorf("sync without a ticket = %d", w.Code)
	}
}

func TestPageOrigins(t *testing.T) {
	u, _ := url.Parse("http://127.0.0.1:8430")
	got := pageOrigins(u, []string{"https://Flamingo.Example.com/", "nonsense"})
	for _, o := range []string{"http://127.0.0.1:8430", "http://localhost:8430", "http://[::1]:8430", "https://flamingo.example.com"} {
		if !got[o] {
			t.Errorf("missing %s in %v", o, got)
		}
	}
	if len(got) != 4 {
		t.Errorf("extra origins: %v", got)
	}
	u, _ = url.Parse("https://flamingo.example.com")
	if got := pageOrigins(u, nil); len(got) != 1 {
		t.Errorf("cloud server = %v", got)
	}
}
