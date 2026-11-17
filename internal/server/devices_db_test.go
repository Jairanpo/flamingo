package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// agentCall is a call the agent makes itself, with its token and no cookie.
func agentCall(t *testing.T, base, token, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, base+path, rd)
	if token != "" {
		req.Header.Set(deviceHeader, token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestDevicesAndFolders(t *testing.T) {
	ts, pool, s := testServer(t)
	ctx := context.Background()
	seed(t, pool)
	s.auth.CreateUser(ctx, "ana", "", "secret-pass", true)
	bobU, _ := s.auth.CreateUser(ctx, "bob", "", "secret-pass", false)
	if _, err := pool.Exec(ctx, `INSERT INTO user_playlists (user_id, play_id) VALUES ($1, $2)`, bobU.ID, tPl); err != nil {
		t.Fatal(err)
	}

	// Pairing: a wrong password gets nothing; the right one a token.
	code, _ := agentCall(t, ts.URL, "", "POST", "/api/agent/pair",
		map[string]string{"user": "bob", "password": "nope", "device": "laptop", "os": "darwin"})
	eq(t, "bad pair", code, 401)
	code, got := agentCall(t, ts.URL, "", "POST", "/api/agent/pair",
		map[string]string{"user": "bob", "password": "secret-pass", "device": "bad name!", "os": "darwin"})
	eq(t, "bad name", code, 400)
	code, got = agentCall(t, ts.URL, "", "POST", "/api/agent/pair",
		map[string]string{"user": "bob", "password": "secret-pass", "device": "laptop", "os": "darwin"})
	tok, _ := got["token"].(string)
	if code != 200 || tok == "" {
		t.Fatalf("pair = %d %v", code, got)
	}

	// The agent's folder: the macOS default, until told otherwise.
	code, _ = agentCall(t, ts.URL, "", "GET", "/api/agent/config", nil)
	eq(t, "config without token", code, 401)
	code, got = agentCall(t, ts.URL, tok, "GET", "/api/agent/config?os=darwin", nil)
	eq(t, "config", []any{code, got["device"], got["platform"], got["media_dir"], got["own"]},
		[]any{200, "laptop", "MACOS", "~/Movies/Flamingo", false})

	// The agent reports what is on disk; videos not in the catalog are skipped.
	code, got = agentCall(t, ts.URL, tok, "PUT", "/api/agent/inventory", map[string]any{"files": []map[string]any{
		{"id": tV2, "size": 5, "audio": true}, {"id": "zzzzzzzzzzz", "size": 1}, {"id": tV2, "size": 9}}})
	eq(t, "inventory", []any{code, got["files"], got["known"]}, []any{200, 2, 1})

	// Bob's page through the laptop's agent sees v2 on disk, as sound only;
	// through no agent, nothing is on disk.
	bob := signIn(t, ts.URL, "bob")
	bob.token = tok
	var v2 map[string]any
	for _, v := range bob.state("")["videos"].([]any) {
		if m := v.(map[string]any); m["id"] == tV2 {
			v2 = m
		}
	}
	eq(t, "v2 via laptop", []any{v2["have"], v2["audio"]}, []any{true, true})
	bob.token = ""
	for _, v := range bob.state("")["videos"].([]any) {
		if m := v.(map[string]any); m["have"] == true {
			t.Errorf("on disk without an agent: %v", m["id"])
		}
	}
	_, page := get(t, bob.http, ts.URL+"/")
	if !strings.Contains(page, `data-device=""`) {
		t.Error("the page offered downloads without an agent")
	}

	// Another report replaces the list.
	agentCall(t, ts.URL, tok, "PUT", "/api/agent/inventory", map[string]any{"files": []any{}})
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM device_videos d JOIN devices USING (devi_id) WHERE devi_name = 'laptop'`).Scan(&n)
	eq(t, "emptied", n, 0)

	// Folders: only an admin moves a platform's default…
	bob.token = tok
	code, _ = bob.do("POST", "/api/downloads/platform", map[string]string{"code": "MACOS", "media_dir": "/x"})
	eq(t, "member sets default", code, 403)
	ana := signIn(t, ts.URL, "ana")
	code, _ = ana.do("POST", "/api/downloads/platform", map[string]string{"code": "MACOS", "media_dir": " ~/Media/F "})
	eq(t, "admin sets default", code, 200)
	code, _ = ana.do("POST", "/api/downloads/platform", map[string]string{"code": "MACOS", "media_dir": "  "})
	eq(t, "blank default", code, 400)
	_, got = agentCall(t, ts.URL, tok, "GET", "/api/agent/config", nil)
	eq(t, "new default", got["media_dir"], "~/Media/F")

	// …and anyone on a computer can give it its own folder, or drop it.
	code, got = bob.do("POST", "/api/downloads/device", map[string]string{"media_dir": "/Volumes/ext/F"})
	eq(t, "own folder", []any{code, got["media_dir"], got["own"]}, []any{200, "/Volumes/ext/F", true})
	code, got = bob.do("GET", "/api/downloads", nil)
	eq(t, "sheet", []any{code, len(got["platforms"].([]any)), got["admin"], got["device"].(map[string]any)["media_dir"]},
		[]any{200, 3, false, "/Volumes/ext/F"})
	code, got = bob.do("POST", "/api/downloads/device", map[string]string{"media_dir": ""})
	eq(t, "back to default", []any{code, got["media_dir"], got["own"]}, []any{200, "~/Media/F", false})
	bob.token = ""
	code, _ = bob.do("POST", "/api/downloads/device", map[string]string{"media_dir": "/x"})
	eq(t, "own folder without agent", code, 400)

	// Pairing the name again replaces the key; the old one stops working.
	_, got = agentCall(t, ts.URL, "", "POST", "/api/agent/pair",
		map[string]string{"user": "bob", "password": "secret-pass", "device": "laptop", "os": "darwin"})
	code, _ = agentCall(t, ts.URL, tok, "GET", "/api/agent/config", nil)
	eq(t, "old token", code, 401)
	code, _ = agentCall(t, ts.URL, got["token"].(string), "GET", "/api/agent/config", nil)
	eq(t, "new token", code, 200)
}
