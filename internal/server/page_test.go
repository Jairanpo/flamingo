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
	data := struct {
		User   *auth.User
		Device string
	}{&auth.User{Name: "ana", DisplayName: `Ana <script>x</script>`}, `desk"><b`}
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
		`data-device="desk&#34;&gt;&lt;b"`,
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
		"/assets/app.js":   "ON_DEVICE",
		"/assets/app.css":  "--accent:#EB9FEF",
		"/assets/auth.css": "--accent:#EB9FEF",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		body, _ := io.ReadAll(w.Body)
		if w.Code != 200 || !strings.Contains(string(body), want) {
			t.Errorf("%s = %d, lacks %q", path, w.Code, want)
		}
		if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") ||
			!strings.Contains(csp, "media-src 'self' blob:") {
			t.Errorf("csp = %s", csp)
		}
	}
}
