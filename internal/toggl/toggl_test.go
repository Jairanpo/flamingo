package toggl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeToggl answers the few calls the client makes.
func fakeToggl(t *testing.T, running *Entry) (*httptest.Server, *atomic.Int32, *map[string]any) {
	var projectCalls atomic.Int32
	started := map[string]any{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		if pass != "api_token" || user == "bad" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/me":
			json.NewEncoder(w).Encode(map[string]any{"default_workspace_id": 77})
		case r.Method == "GET" && r.URL.Path == "/me/time_entries/current":
			json.NewEncoder(w).Encode(running)
		case r.Method == "GET" && r.URL.Path == "/workspaces/77/projects":
			projectCalls.Add(1)
			json.NewEncoder(w).Encode([]map[string]any{{"id": 5, "name": "Learning"}})
		case r.Method == "POST" && r.URL.Path == "/workspaces/77/time_entries":
			json.NewDecoder(r.Body).Decode(&started)
			json.NewEncoder(w).Encode(map[string]any{"id": 9, "workspace_id": 77,
				"description": started["description"], "project_id": started["project_id"], "tags": started["tags"]})
		case r.Method == "PATCH" && r.URL.Path == "/workspaces/77/time_entries/9/stop":
			w.Write([]byte(`{}`))
		case r.Method == "PATCH":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`"Time entry not found"`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, &projectCalls, &started
}

func TestStartStop(t *testing.T) {
	ts, projectCalls, started := fakeToggl(t, nil)
	c := &Client{API: ts.URL}
	a := Account{Token: "tok"}
	ctx := context.Background()

	e, missing, err := c.Start(ctx, a, "Alpha", "learning", []string{"Youtube"})
	if err != nil || missing || e.ID != 9 || *e.ProjectID != 5 {
		t.Fatalf("start = %+v %v %v", e, missing, err)
	}
	if (*started)["duration"] != float64(-1) || (*started)["created_with"] != "flamingo" {
		t.Fatalf("sent %v", *started)
	}
	// Unknown project: left off, reported; the project list is cached.
	e, missing, err = c.Start(ctx, a, "Alpha", "Nope", nil)
	if err != nil || !missing || e.ProjectID != nil {
		t.Fatalf("start unknown project = %+v %v %v", e, missing, err)
	}
	if got := (*started)["tags"]; got == nil {
		t.Fatal("tags must be [] not null")
	}
	if projectCalls.Load() != 1 {
		t.Fatalf("project list fetched %d times", projectCalls.Load())
	}

	if err := c.Stop(ctx, a, 9, 0); err != nil {
		t.Fatal(err)
	}
	err = c.Stop(ctx, a, 10, 77)
	if !IsTogglError(err) || !strings.Contains(err.Error(), "404") {
		t.Fatalf("stop missing = %v", err)
	}
	cur, err := c.Current(ctx, a)
	if err != nil || cur != nil {
		t.Fatalf("current = %v %v", cur, err)
	}
	if got := AsJSON(nil); len(got) != 0 {
		t.Fatalf("AsJSON(nil) = %v", got)
	}
}

func TestBadToken(t *testing.T) {
	ts, _, _ := fakeToggl(t, nil)
	c := &Client{API: ts.URL}
	_, err := c.Current(context.Background(), Account{Token: "bad"})
	if !IsTogglError(err) || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("err = %v", err)
	}
	_, err = (&Client{API: "http://127.0.0.1:1"}).Current(context.Background(), Account{Token: "x"})
	if !IsTogglError(err) || !strings.Contains(err.Error(), "could not reach") {
		t.Fatalf("unreachable = %v", err)
	}
}

func TestExplicitWorkspace(t *testing.T) {
	c := &Client{API: "http://unused"}
	w, err := c.Workspace(context.Background(), Account{Token: "x", Workspace: 3})
	if err != nil || w != 3 {
		t.Fatalf("w = %d %v", w, err)
	}
}
