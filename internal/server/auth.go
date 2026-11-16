package server

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Jairanpo/yt/internal/auth"
)

const sessionCookie = "yt_session"

//go:embed templates/*.html assets/*
var webFS embed.FS

var pages = template.Must(template.ParseFS(webFS, "templates/*.html"))

type ctxKey struct{}

// CurrentUser is the signed-in user for a request that passed requireUser.
func CurrentUser(r *http.Request) *auth.User {
	u, _ := r.Context().Value(ctxKey{}).(*auth.User)
	return u
}

func (s *Server) secureCookies() bool { return strings.HasPrefix(s.cfg.PublicURL, "https://") }

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteStrictMode,
	})
}

// userFrom reads the session cookie. Errors other than "not signed in" are
// returned so a database outage doesn't look like being signed out.
func (s *Server) userFrom(w http.ResponseWriter, r *http.Request) (*auth.User, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, auth.ErrNoSuchUser
	}
	u, renewed, err := s.auth.Session(r.Context(), c.Value)
	if err != nil {
		return nil, err
	}
	if renewed != nil {
		s.setSessionCookie(w, c.Value, *renewed)
	}
	return u, nil
}

// requireUser lets signed-in requests through. Others get a 401 on /api/
// and are sent to the login page everywhere else.
func (s *Server) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := s.userFrom(w, r)
		if errors.Is(err, auth.ErrNoSuchUser) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in first"})
				return
			}
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		if err != nil {
			s.log.Error("session lookup", "err", err)
			http.Error(w, "the database is not answering", http.StatusServiceUnavailable)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	}
}

// sameOrigin refuses state-changing requests sent by another site's page
// (cross-site request forgery). The session cookie is SameSite=Strict as
// well; this is the second lock on the same door.
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

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// safeNext keeps the after-login redirect on this site.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") ||
		strings.HasPrefix(next, "/login") {
		return "/"
	}
	return next
}

func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type loginPage struct {
	Name, Next, Error string
}

func (s *Server) render(w http.ResponseWriter, code int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := pages.ExecuteTemplate(w, name, data); err != nil {
		s.log.Error("render", "page", name, "err", err)
	}
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	if u, err := s.userFrom(w, r); err == nil && u != nil {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	s.render(w, http.StatusOK, "login.html", loginPage{Next: safeNext(r.URL.Query().Get("next"))})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.PostForm.Get("name")))
	page := loginPage{Name: name, Next: safeNext(r.PostForm.Get("next"))}
	addr, now := clientAddr(r), time.Now()

	if blocked, wait := s.limiter.Blocked(addr, name, now); blocked {
		page.Error = "Too many tries. Wait " + wait.Round(time.Minute).String() + " and try again."
		s.render(w, http.StatusTooManyRequests, "login.html", page)
		return
	}
	u, err := s.auth.Authenticate(r.Context(), name, r.PostForm.Get("password"))
	if errors.Is(err, auth.ErrBadLogin) {
		s.limiter.Failed(addr, name, now)
		s.log.Info("login failed", "name", name, "addr", addr)
		page.Error = "Wrong user name or password."
		s.render(w, http.StatusUnauthorized, "login.html", page)
		return
	}
	if err != nil {
		s.log.Error("login", "err", err)
		page.Error = "Something went wrong on the server. Try again in a moment."
		s.render(w, http.StatusInternalServerError, "login.html", page)
		return
	}
	s.limiter.Succeeded(addr, name)
	token, expires, err := s.auth.StartSession(r.Context(), u.ID, r.UserAgent())
	if err != nil {
		s.log.Error("start session", "err", err)
		page.Error = "Something went wrong on the server. Try again in a moment."
		s.render(w, http.StatusInternalServerError, "login.html", page)
		return
	}
	s.log.Info("login", "user", u.Name, "addr", addr)
	s.setSessionCookie(w, token, expires)
	http.Redirect(w, r, page.Next, http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.auth.EndSession(r.Context(), c.Value); err != nil {
			s.log.Error("end session", "err", err)
		}
	}
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.render(w, http.StatusOK, "home.html", CurrentUser(r))
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"name": u.Name, "display_name": u.DisplayName, "admin": u.IsAdmin(),
	})
}
