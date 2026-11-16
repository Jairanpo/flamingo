package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Jairanpo/flamingo/internal/config"
	"github.com/Jairanpo/flamingo/internal/ytdlp"
)

// fakeStreams hands out URLs on a fake YouTube; each resolve bumps the
// generation, and only the newest generation's URL is accepted.
type fakeStreams struct {
	base     string
	resolves atomic.Int32
}

func (f *fakeStreams) Stream(_ context.Context, vid string, maxHeight int) (*ytdlp.Stream, error) {
	n := f.resolves.Add(1)
	gen := string(rune('0' + n))
	return &ytdlp.Stream{
		Audio:   ytdlp.Track{URL: f.base + "/a?gen=" + gen, Mime: "audio/mp4"},
		Videos:  map[int]ytdlp.Track{720: {URL: f.base + "/v720?gen=" + gen, Headers: map[string]string{"X-Need": "yes"}}},
		Default: 720,
	}, nil
}

func bare(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Server{Storage: config.Storage{ThumbsDir: t.TempDir()}, Ytdlp: config.Ytdlp{StreamMaxHeight: 1080}}
	return New(cfg, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func call(s *Server, h http.HandlerFunc, path string, values map[string]string, header map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	for k, v := range values {
		r.SetPathValue(k, v)
	}
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func TestStreamRelay(t *testing.T) {
	var newest atomic.Int32
	yt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("gen") != string(rune('0'+newest.Load())) {
			w.WriteHeader(http.StatusForbidden) // an expired signature
			return
		}
		if r.URL.Path == "/v720" && r.Header.Get("X-Need") != "yes" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Range", "bytes 0-4/100")
		w.Header().Set("Content-Length", "5")
		w.Header().Set("Set-Cookie", "leak=1")
		w.WriteHeader(http.StatusPartialContent)
		io.WriteString(w, "bytes")
	}))
	defer yt.Close()

	s := bare(t)
	fs := &fakeStreams{base: yt.URL}
	s.streams = newStreams(fs, 1080)
	rng := map[string]string{"Range": "bytes=0-4"}

	newest.Store(1)
	w := call(s, s.stream, "/stream/aaaaaaaaaa1/video/720", map[string]string{"vid": tV1, "height": "720"}, rng)
	if w.Code != 206 || w.Body.String() != "bytes" || w.Header().Get("Content-Range") != "bytes 0-4/100" ||
		w.Header().Get("Content-Type") != "video/mp4" || w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("video = %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	// Cached: audio does not resolve again.
	w = call(s, s.stream, "/stream/aaaaaaaaaa1/audio", map[string]string{"vid": tV1}, rng)
	if w.Code != 206 || w.Header().Get("Content-Type") != "audio/mp4" || fs.resolves.Load() != 1 {
		t.Fatalf("audio = %d, resolves %d", w.Code, fs.resolves.Load())
	}
	// The signature expires: one fresh resolve, then it plays.
	newest.Store(2)
	w = call(s, s.stream, "/stream/aaaaaaaaaa1/audio", map[string]string{"vid": tV1}, rng)
	if w.Code != 206 || fs.resolves.Load() != 2 {
		t.Fatalf("after expiry = %d, resolves %d", w.Code, fs.resolves.Load())
	}
	// Still refused after a fresh resolve: an error, not a loop.
	newest.Store(9)
	w = call(s, s.stream, "/stream/aaaaaaaaaa1/audio", map[string]string{"vid": tV1}, rng)
	if w.Code != http.StatusBadGateway || fs.resolves.Load() != 3 {
		t.Fatalf("refused = %d, resolves %d", w.Code, fs.resolves.Load())
	}

	for _, c := range []struct {
		vals   map[string]string
		header map[string]string
		code   int
	}{
		{map[string]string{"vid": tV1, "height": "480"}, rng, http.StatusNotFound},
		{map[string]string{"vid": tV1, "height": "x"}, rng, http.StatusBadRequest},
		{map[string]string{"vid": "bad"}, rng, http.StatusBadRequest},
		{map[string]string{"vid": tV1}, nil, http.StatusBadRequest},
		{map[string]string{"vid": tV1}, map[string]string{"Range": "bytes=-"}, http.StatusBadRequest},
		{map[string]string{"vid": tV1}, map[string]string{"Range": "bytes=0-1,5-6"}, http.StatusBadRequest},
	} {
		newest.Store(fs.resolves.Load())
		if w := call(s, s.stream, "/stream/x", c.vals, c.header); w.Code != c.code {
			t.Errorf("%v %v = %d, want %d", c.vals, c.header, w.Code, c.code)
		}
	}
}

func TestThumb(t *testing.T) {
	var hits atomic.Int32
	img := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if strings.Contains(r.URL.Path, tV2) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte("JPEGDATA"))
	}))
	defer img.Close()
	s := bare(t)
	s.thumbURL = img.URL + "/vi/%s/mqdefault.jpg"

	for i := 0; i < 2; i++ {
		w := call(s, s.thumb, "/thumb/"+tV1, map[string]string{"vid": tV1}, nil)
		if w.Code != 200 || w.Body.String() != "JPEGDATA" || w.Header().Get("Content-Type") != "image/jpeg" {
			t.Fatalf("thumb = %d %q %v", w.Code, w.Body.String(), w.Header())
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("fetched %d times, want once", hits.Load())
	}
	if b, _ := os.ReadFile(filepath.Join(s.cfg.Storage.ThumbsDir, tV1+".jpg")); string(b) != "JPEGDATA" {
		t.Fatalf("cached = %q", b)
	}

	w := call(s, s.thumb, "/thumb/"+tV2, map[string]string{"vid": tV2}, nil)
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "<svg") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("placeholder = %d %v", w.Code, w.Header())
	}
	entries, _ := os.ReadDir(s.cfg.Storage.ThumbsDir)
	if len(entries) != 1 {
		t.Fatalf("left behind %d files", len(entries))
	}
	if w := call(s, s.thumb, "/thumb/x", map[string]string{"vid": "../etc/passwd"}, nil); w.Code != 400 {
		t.Fatalf("bad id = %d", w.Code)
	}
}
