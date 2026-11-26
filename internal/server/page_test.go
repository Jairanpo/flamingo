package server

import (
	"bytes"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Jairanpo/flamingo/internal/auth"
)

func TestLibraryPage(t *testing.T) {
	var b bytes.Buffer
	data := struct{ User *auth.User }{&auth.User{Name: "ana", DisplayName: `Ana <script>x</script>`}}
	if err := pages.ExecuteTemplate(&b, "library.html", data); err != nil {
		t.Fatal(err)
	}
	html := b.String()
	for _, want := range []string{
		`<title>Flamingo</title>`,
		`<script src="/assets/app.js"></script>`,
		`href="/assets/app.css"`,
		`action="/logout"`,
		`Ana &lt;script&gt;x&lt;/script&gt;`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	// Nothing inline for the CSP to refuse.
	if strings.Contains(html, "<script>") || strings.Contains(html, "<style>") || strings.Contains(html, " onclick=") {
		t.Error("inline script or style in the page")
	}
}

func TestAssetsServed(t *testing.T) {
	s := bare(t)
	h := s.Handler()
	for path, want := range map[string]string{
		"/assets/app.js":             "ON_DEVICE",
		"/assets/app.css":            "--flamingo:#EB9FEF",
		"/assets/auth.css":           "--flamingo:#EB9FEF",
		"/assets/favicon.svg":        "<svg",
		"/assets/fonts/outfit.woff2": "wOF2",
		"/assets/fonts/nunito.woff2": "wOF2",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		body, _ := io.ReadAll(w.Body)
		if w.Code != 200 || !strings.Contains(string(body), want) {
			t.Errorf("%s = %d, lacks %q", path, w.Code, want)
		}
		if strings.HasSuffix(path, ".woff2") && w.Header().Get("Content-Type") != "font/woff2" {
			t.Errorf("%s type = %s", path, w.Header().Get("Content-Type"))
		}
		if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") ||
			!strings.Contains(csp, "media-src 'self' blob: http://127.0.0.1:8421") ||
			!strings.Contains(csp, "connect-src 'self' http://127.0.0.1:8421") {
			t.Errorf("csp = %s", csp)
		}
	}
}

// A redeployed stylesheet must reach browsers that already hold the old one.
func TestAssetsRevalidate(t *testing.T) {
	h := bare(t).Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/assets/app.css", nil))
	tag := w.Header().Get("ETag")
	if tag == "" || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("etag %q, cache-control %q", tag, w.Header().Get("Cache-Control"))
	}
	r := httptest.NewRequest("GET", "/assets/app.css", nil)
	r.Header.Set("If-None-Match", tag)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 304 {
		t.Errorf("unchanged asset = %d, want 304", w.Code)
	}
}
