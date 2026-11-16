// Package server is the central HTTP service.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Jairanpo/yt/internal/auth"
	"github.com/Jairanpo/yt/internal/config"
	"github.com/Jairanpo/yt/internal/db"
)

type Server struct {
	cfg     *config.Server
	pool    *pgxpool.Pool
	mig     *db.Migrator
	log     *slog.Logger
	auth    *auth.Store
	limiter *auth.Limiter
}

func New(cfg *config.Server, pool *pgxpool.Pool, mig *db.Migrator, log *slog.Logger) *Server {
	return &Server{
		cfg: cfg, pool: pool, mig: mig, log: log,
		auth:    &auth.Store{Pool: pool, SessionTTL: cfg.Auth.SessionTTL},
		limiter: auth.NewLimiter(),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.Handle("GET /assets/", http.FileServerFS(webFS))
	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)

	mux.HandleFunc("GET /", s.requireUser(s.home))
	mux.HandleFunc("GET /api/me", s.requireUser(s.me))
	s.routesAPI(mux)
	return securityHeaders(sameOrigin(mux))
}

// healthz answers 200 when the database is reachable, 503 when it is not.
// Compose and any later load balancer use it to decide if the server is up.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	body := map[string]any{"status": "ok", "db": "ok"}
	code := http.StatusOK
	if err := s.pool.Ping(ctx); err != nil {
		body["status"], body["db"] = "degraded", err.Error()
		code = http.StatusServiceUnavailable
	} else if v, err := s.mig.Version(ctx); err == nil {
		body["schema_version"] = v
	}
	writeJSON(w, code, body)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Run serves until ctx is cancelled, then gives open requests a few seconds
// to finish.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	s.log.Info("listening", "addr", s.cfg.Listen)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shut); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
