package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jairanpo/yt/internal/auth"
	"github.com/Jairanpo/yt/internal/config"
	"github.com/Jairanpo/yt/internal/db"
)

func testServer(t *testing.T) (*httptest.Server, *pgxpool.Pool, *Server) {
	t.Helper()
	url := os.Getenv("YT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("YT_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	mig, err := db.NewMigrator(pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mig.Close() })
	reset := func() {
		for {
			if _, err := mig.Down(ctx); err != nil {
				return
			}
		}
	}
	reset()
	if _, err := mig.Up(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reset)

	cfg := &config.Server{Auth: config.Auth{SessionTTL: time.Hour}}
	s := New(cfg, pool, mig, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, pool, s
}

// browser is a client with cookies that does not follow redirects, so each
// step's answer can be checked.
func browser(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func post(t *testing.T, c *http.Client, u string, form url.Values, origin string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", u, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func get(t *testing.T, c *http.Client, u string) (*http.Response, string) {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func TestLoginFlow(t *testing.T) {
	ts, _, s := testServer(t)
	ctx := context.Background()
	if _, err := s.auth.CreateUser(ctx, "Ana", "Ana B.", "secret-pass", false); err != nil {
		t.Fatal(err)
	}
	c := browser(t)

	// Signed out: pages redirect to login, the API says 401.
	if r, _ := get(t, c, ts.URL+"/"); r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/login?next=%2F" {
		t.Fatalf("/ signed out: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if r, _ := get(t, c, ts.URL+"/api/me"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/api/me signed out: %d", r.StatusCode)
	}
	if r, body := get(t, c, ts.URL+"/login"); r.StatusCode != 200 || !strings.Contains(body, `name="password"`) {
		t.Fatalf("login page: %d", r.StatusCode)
	}
	if csp := func() string { r, _ := get(t, c, ts.URL+"/login"); return r.Header.Get("Content-Security-Policy") }(); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("CSP missing: %q", csp)
	}

	// Wrong password, then unknown user: same answer.
	if r := post(t, c, ts.URL+"/login", url.Values{"name": {"ana"}, "password": {"nope-nope"}}, ""); r.StatusCode != 401 {
		t.Fatalf("wrong password: %d", r.StatusCode)
	}
	if r := post(t, c, ts.URL+"/login", url.Values{"name": {"bob"}, "password": {"secret-pass"}}, ""); r.StatusCode != 401 {
		t.Fatalf("unknown user: %d", r.StatusCode)
	}

	// Another site's form can't sign anyone in or out.
	if r := post(t, c, ts.URL+"/login", url.Values{"name": {"ana"}, "password": {"secret-pass"}}, "https://evil.example"); r.StatusCode != 403 {
		t.Fatalf("cross-site login: %d", r.StatusCode)
	}

	// Right password (name in any case); an outside "next" is ignored.
	r := post(t, c, ts.URL+"/login", url.Values{"name": {" ANA "}, "password": {"secret-pass"}, "next": {"//evil.example/x"}}, ts.URL)
	if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/" {
		t.Fatalf("login: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	var ck *http.Cookie
	for _, x := range r.Cookies() {
		if x.Name == sessionCookie {
			ck = x
		}
	}
	if ck == nil || !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie: %+v", ck)
	}

	resp, body := get(t, c, ts.URL+"/api/me")
	var me map[string]any
	_ = json.Unmarshal([]byte(body), &me)
	if resp.StatusCode != 200 || me["name"] != "ana" || me["display_name"] != "Ana B." || me["admin"] != true {
		t.Fatalf("/api/me: %d %v", resp.StatusCode, me)
	}
	if r, body := get(t, c, ts.URL+"/"); r.StatusCode != 200 || !strings.Contains(body, "Ana B.") {
		t.Fatalf("home: %d", r.StatusCode)
	}

	// Signing out ends the session on the server, not only in the browser.
	stolen := ck.Value
	if r := post(t, c, ts.URL+"/logout", nil, ts.URL); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout: %d", r.StatusCode)
	}
	if u, _, err := s.auth.Session(ctx, stolen); err != auth.ErrNoSuchUser || u != nil {
		t.Fatalf("session survived logout: %v", err)
	}
	if r, _ := get(t, c, ts.URL+"/api/me"); r.StatusCode != 401 {
		t.Fatalf("after logout: %d", r.StatusCode)
	}
}

func TestSessionsAndAccounts(t *testing.T) {
	_, pool, s := testServer(t)
	ctx := context.Background()
	st := s.auth

	admin, err := st.CreateUser(ctx, "ana", "", "secret-pass", false)
	if err != nil || !admin.IsAdmin() || admin.DisplayName != "ana" {
		t.Fatalf("first user: %+v %v", admin, err)
	}
	member, err := st.CreateUser(ctx, "bob", "Bob", "secret-pass", false)
	if err != nil || member.IsAdmin() {
		t.Fatalf("second user: %+v %v", member, err)
	}
	if _, err := st.CreateUser(ctx, "BOB", "", "secret-pass", false); err != auth.ErrUserExists {
		t.Errorf("duplicate: %v", err)
	}

	// An imported user has no password and can't sign in.
	if _, err := pool.Exec(ctx, `INSERT INTO users (user_name, user_display_name) VALUES ('imp', 'Imported')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Authenticate(ctx, "imp", ""); err != auth.ErrBadLogin {
		t.Errorf("passwordless login: %v", err)
	}

	// Expiry and renewal, on a fake clock.
	now := time.Now()
	st.Now = func() time.Time { return now }
	token, _, err := st.StartSession(ctx, member.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(20 * time.Minute)
	if _, renewed, err := st.Session(ctx, token); err != nil || renewed != nil {
		t.Errorf("early use should not renew: %v %v", renewed, err)
	}
	now = now.Add(20 * time.Minute) // 40 min in: past half of the hour
	if _, renewed, err := st.Session(ctx, token); err != nil || renewed == nil {
		t.Errorf("late use should renew: %v %v", renewed, err)
	}
	now = now.Add(50 * time.Minute) // still inside the renewed hour
	if _, _, err := st.Session(ctx, token); err != nil {
		t.Errorf("renewed session ended early: %v", err)
	}
	now = now.Add(2 * time.Hour)
	if _, _, err := st.Session(ctx, token); err != auth.ErrNoSuchUser {
		t.Errorf("expired session still works: %v", err)
	}
	st.Now = nil

	// A new password signs the user out everywhere.
	token, _, _ = st.StartSession(ctx, member.ID, "")
	if err := st.SetPassword(ctx, "bob", "another-pass"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Session(ctx, token); err != auth.ErrNoSuchUser {
		t.Errorf("session survived password change: %v", err)
	}
	if _, err := st.Authenticate(ctx, "bob", "another-pass"); err != nil {
		t.Errorf("new password: %v", err)
	}

	// The last admin can't be demoted or removed.
	if err := st.SetRole(ctx, "ana", false); err == nil {
		t.Error("demoted the last admin")
	}
	if err := st.DeleteUser(ctx, "ana"); err == nil {
		t.Error("removed the last admin")
	}
	if err := st.SetRole(ctx, "bob", true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRole(ctx, "ana", false); err != nil {
		t.Errorf("demote with another admin around: %v", err)
	}
	if err := st.DeleteUser(ctx, "nobody"); err != auth.ErrNoSuchUser {
		t.Errorf("remove unknown: %v", err)
	}
	if err := st.DeleteUser(ctx, "ana"); err != nil {
		t.Errorf("remove member: %v", err)
	}
}

func TestLoginRateLimit(t *testing.T) {
	ts, _, s := testServer(t)
	if _, err := s.auth.CreateUser(context.Background(), "ana", "", "secret-pass", false); err != nil {
		t.Fatal(err)
	}
	c := browser(t)
	for range s.limiter.PerName {
		post(t, c, ts.URL+"/login", url.Values{"name": {"ana"}, "password": {"wrong-pass"}}, "")
	}
	// Even the right password is refused while blocked.
	if r := post(t, c, ts.URL+"/login", url.Values{"name": {"ana"}, "password": {"secret-pass"}}, ""); r.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("after %d failures: %d", s.limiter.PerName, r.StatusCode)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"/":                    "/",
		"/library?x=1":         "/library?x=1",
		"//evil.example":       "/",
		"/\\evil.example":      "/",
		"https://evil.example": "/",
		"":                     "/",
		"/login?next=/":        "/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}
