package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Jairanpo/yt/internal/config"
	"github.com/Jairanpo/yt/internal/db"
)

// Needs a disposable Postgres, e.g.
//
//	YT_TEST_DATABASE_URL=postgres://yt:pw@localhost:5432/yt_test go test ./...
//
// Without one the test is skipped. It migrates up, checks /healthz, then
// migrates back down so the database is left empty.
func TestHealthzAgainstPostgres(t *testing.T) {
	url := os.Getenv("YT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("YT_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	mig, err := db.NewMigrator(pool)
	if err != nil {
		t.Fatal(err)
	}
	defer mig.Close()
	if _, err := mig.Up(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for {
			if _, err := mig.Down(ctx); err != nil {
				return
			}
		}
	})

	s := New(&config.Server{}, pool, mig, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != 200 || body["db"] != "ok" || body["schema_version"] != float64(1) {
		t.Fatalf("healthz = %d %v", resp.StatusCode, body)
	}

	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM instance").Scan(&n); err != nil || n != 1 {
		t.Fatalf("instance rows = %d, %v", n, err)
	}
}
