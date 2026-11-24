package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jairanpo/flamingo/internal/ticket"
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

// Pairing from the app with a one-time code, tickets for the agent, and a
// page that names its computer instead of coming through the agent.
func TestPairCodeAndTickets(t *testing.T) {
	ts, pool, s := testServer(t)
	ctx := context.Background()
	seed(t, pool)
	s.auth.CreateUser(ctx, "ana", "", "secret-pass", true)
	bobU, _ := s.auth.CreateUser(ctx, "bob", "", "secret-pass", false)
	if _, err := pool.Exec(ctx, `INSERT INTO user_playlists (user_id, play_id) VALUES ($1, $2)`, bobU.ID, tPl); err != nil {
		t.Fatal(err)
	}
	bob := signIn(t, ts.URL, "bob")
	bob.token = ""

	// A code only for the signed in.
	code, _ := agentCall(t, ts.URL, "", "POST", "/api/devices/pair-code", nil)
	eq(t, "code signed out", code, 401)
	code, got := bob.do("POST", "/api/devices/pair-code", nil)
	pc, _ := got["code"].(string)
	if code != 200 || len(pc) != 9 || pc[4] != '-' {
		t.Fatalf("pair code = %d %v", code, got)
	}

	// Wrong code: nothing. The right one, typed loosely, pairs to bob, once.
	code, _ = agentCall(t, ts.URL, "", "POST", "/api/agent/pair",
		map[string]string{"code": "AAAA-AAAA", "device": "laptop", "os": "linux"})
	eq(t, "wrong code", code, 401)
	code, got = agentCall(t, ts.URL, "", "POST", "/api/agent/pair",
		map[string]string{"code": " " + strings.ToLower(pc) + " ", "device": "laptop", "os": "linux"})
	tok, _ := got["token"].(string)
	if code != 200 || tok == "" {
		t.Fatalf("pair by code = %d %v", code, got)
	}
	var owner int32
	pool.QueryRow(ctx, `SELECT user_id FROM devices WHERE devi_name = 'laptop'`).Scan(&owner)
	eq(t, "paired to", owner, bobU.ID)
	code, _ = agentCall(t, ts.URL, "", "POST", "/api/agent/pair",
		map[string]string{"code": pc, "device": "laptop2", "os": "linux"})
	eq(t, "code reused", code, 401)

	// An expired code does nothing.
	_, got = bob.do("POST", "/api/devices/pair-code", nil)
	pool.Exec(ctx, `UPDATE pairing_codes SET paco_expires_at = now() - interval '1 second'`)
	code, _ = agentCall(t, ts.URL, "", "POST", "/api/agent/pair",
		map[string]string{"code": got["code"].(string), "device": "laptop3", "os": "linux"})
	eq(t, "expired code", code, 401)

	// Tickets: for bob's own computer, signed with its key.
	code, got = bob.do("GET", "/api/devices/ticket?device=laptop", nil)
	tk, _ := got["ticket"].(string)
	if code != 200 || tk == "" {
		t.Fatalf("ticket = %d %v", code, got)
	}
	c, err := ticket.Verify(ticket.KeyOf(tok), "laptop", tk, time.Now())
	if err != nil || c.User != bobU.ID {
		t.Fatalf("ticket check: %v %+v", err, c)
	}
	ana := signIn(t, ts.URL, "ana")
	ana.token = ""
	code, _ = ana.do("GET", "/api/devices/ticket?device=laptop", nil)
	eq(t, "ticket for someone else's computer", code, 404)
	code, _ = bob.do("GET", "/api/devices/ticket?device=nowhere", nil)
	eq(t, "ticket for no computer", code, 404)

	// The page names its computer: bob sees the laptop's files; ana naming
	// bob's laptop sees nothing on disk and no folder.
	agentCall(t, ts.URL, tok, "PUT", "/api/agent/inventory",
		map[string]any{"files": []map[string]any{{"id": tV2, "size": 5}}})
	have := func(c *client) bool {
		for _, v := range c.state("")["videos"].([]any) {
			if m := v.(map[string]any); m["id"] == tV2 {
				return m["have"] == true
			}
		}
		return false
	}
	eq(t, "no name", have(bob), false)
	bob.device = "laptop"
	eq(t, "named", have(bob), true)
	_, got = bob.do("GET", "/api/downloads", nil)
	if got["device"] == nil {
		t.Error("the sheet lost the named computer's folder")
	}
	code, _ = bob.do("POST", "/api/downloads/device", map[string]string{"media_dir": "/mnt/f"})
	eq(t, "folder for named computer", code, 200)
	ana.device = "laptop"
	_, got = ana.do("GET", "/api/downloads", nil)
	eq(t, "someone else's computer", got["device"], nil)
	code, _ = ana.do("POST", "/api/downloads/device", map[string]string{"media_dir": "/x"})
	eq(t, "folder on someone else's computer", code, 400)

	// The helper for each system: only the builds present, only by name.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "flamingo-linux-amd64"), []byte("ELF!"), 0o755)
	s.cfg.Storage.AgentsDir = dir
	_, got = bob.do("GET", "/api/devices/agents", nil)
	list, _ := got["agents"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["os"] != "linux" {
		t.Errorf("agents = %v", got)
	}
	r, body := get(t, bob.http, ts.URL+"/download/agent/flamingo-linux-amd64")
	eq(t, "download", []any{r.StatusCode, body}, []any{200, "ELF!"})
	r, _ = get(t, bob.http, ts.URL+"/download/agent/..%2Fsecret")
	eq(t, "other file", r.StatusCode, 404)
	r, _ = get(t, bob.http, ts.URL+"/download/agent/flamingo-windows-amd64.exe")
	eq(t, "missing build", r.StatusCode, 404)
}
