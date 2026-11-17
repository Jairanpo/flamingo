// Package toggl talks to Toggl Track's API v9, enough of it to run one
// timer. The page never calls Toggl itself: it asks this server, which
// knows each person's token.
package toggl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const DefaultAPI = "https://api.track.toggl.com/api/v9"

// projectTTL: projects change rarely, and a name is resolved to an id on
// every start; a short memory saves a click a round trip.
const projectTTL = 5 * time.Minute

// Error is something the person needs to read, not a stack trace.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// Entry is a time entry, as Toggl returns it.
type Entry struct {
	ID          int64    `json:"id"`
	Description string   `json:"description"`
	ProjectID   *int64   `json:"project_id"`
	WorkspaceID int64    `json:"workspace_id"`
	Tags        []string `json:"tags"`
	Start       string   `json:"start"`
}

// Client is shared by every user; caches are kept per token.
type Client struct {
	API  string // DefaultAPI when empty
	HTTP *http.Client

	mu    sync.Mutex
	cache map[string]*tokenCache
}

type tokenCache struct {
	workspace  int64
	projects   map[string]int64
	projectsAt time.Time
}

// Account is one person's way in.
type Account struct {
	Token     string
	Workspace int64 // 0: their default workspace
}

func (c *Client) api() string {
	if c.API == "" {
		return DefaultAPI
	}
	return c.API
}

func (c *Client) http() *http.Client {
	if c.HTTP == nil {
		return &http.Client{Timeout: 15 * time.Second}
	}
	return c.HTTP
}

func (c *Client) tc(tok string) *tokenCache {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cache == nil {
		c.cache = map[string]*tokenCache{}
	}
	if c.cache[tok] == nil {
		c.cache[tok] = &tokenCache{}
	}
	return c.cache[tok]
}

func (c *Client) call(ctx context.Context, a Account, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.api()+path, rd)
	if err != nil {
		return err
	}
	// The token is the user name and the literal word api_token the
	// password: that is Toggl's scheme, not a placeholder.
	req.SetBasicAuth(a.Token, "api_token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http().Do(req)
	if err != nil {
		return &Error{"could not reach Toggl: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return &Error{"Toggl rejected the API token — paste it again in your account (your name, top right)"}
	case resp.StatusCode >= 300:
		detail := strings.TrimSpace(string(raw))
		if len(detail) > 300 {
			detail = detail[:300]
		}
		if detail == "" {
			detail = resp.Status
		}
		return &Error{fmt.Sprintf("Toggl said %d: %s", resp.StatusCode, detail)}
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Check asks Toggl who the token belongs to, so a mistyped one is caught
// when it is entered rather than on the first timer.
func (c *Client) Check(ctx context.Context, a Account) error {
	var me struct {
		ID int64 `json:"id"`
	}
	return c.call(ctx, a, "GET", "/me", nil, &me)
}

// Workspace is the one to file entries under: the account's own, or the
// person's default in Toggl.
func (c *Client) Workspace(ctx context.Context, a Account) (int64, error) {
	if a.Workspace != 0 {
		return a.Workspace, nil
	}
	tc := c.tc(a.Token)
	c.mu.Lock()
	w := tc.workspace
	c.mu.Unlock()
	if w != 0 {
		return w, nil
	}
	var me struct {
		DefaultWorkspaceID int64 `json:"default_workspace_id"`
	}
	if err := c.call(ctx, a, "GET", "/me", nil, &me); err != nil {
		return 0, err
	}
	if me.DefaultWorkspaceID == 0 {
		return 0, &Error{"your Toggl account reports no default workspace; enter a workspace id in your account (your name, top right)"}
	}
	c.mu.Lock()
	tc.workspace = me.DefaultWorkspaceID
	c.mu.Unlock()
	return me.DefaultWorkspaceID, nil
}

// projectID resolves a project name, ignoring case; unknown is 0, not fatal.
func (c *Client) projectID(ctx context.Context, a Account, wid int64, name string) (int64, error) {
	if name == "" {
		return 0, nil
	}
	tc := c.tc(a.Token)
	c.mu.Lock()
	projects, at := tc.projects, tc.projectsAt
	c.mu.Unlock()
	if projects == nil || time.Since(at) > projectTTL {
		var rows []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		if err := c.call(ctx, a, "GET", fmt.Sprintf("/workspaces/%d/projects", wid), nil, &rows); err != nil {
			return 0, err
		}
		projects = map[string]int64{}
		for _, r := range rows {
			projects[strings.ToLower(r.Name)] = r.ID
		}
		c.mu.Lock()
		tc.projects, tc.projectsAt = projects, time.Now()
		c.mu.Unlock()
	}
	return projects[strings.ToLower(name)], nil
}

// Current is the running entry, or nil.
func (c *Client) Current(ctx context.Context, a Account) (*Entry, error) {
	var e *Entry
	if err := c.call(ctx, a, "GET", "/me/time_entries/current", nil, &e); err != nil {
		return nil, err
	}
	return e, nil
}

// Start starts a timer now. A project Toggl does not know is left off, and
// projectMissing says so.
func (c *Client) Start(ctx context.Context, a Account, description, project string, tags []string) (e *Entry, projectMissing bool, err error) {
	wid, err := c.Workspace(ctx, a)
	if err != nil {
		return nil, false, err
	}
	pid, err := c.projectID(ctx, a, wid, project)
	if err != nil {
		return nil, false, err
	}
	var pidv *int64
	if pid != 0 {
		pidv = &pid
	}
	if tags == nil {
		tags = []string{}
	}
	err = c.call(ctx, a, "POST", fmt.Sprintf("/workspaces/%d/time_entries", wid), map[string]any{
		"created_with": "flamingo",
		"workspace_id": wid,
		"description":  description,
		"project_id":   pidv,
		"tags":         tags,
		"duration":     -1, // negative: still running
		"start":        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"stop":         nil,
	}, &e)
	return e, project != "" && pid == 0, err
}

// Stop stops an entry; wid 0 means the account's workspace.
func (c *Client) Stop(ctx context.Context, a Account, entryID, wid int64) error {
	if wid == 0 {
		var err error
		if wid, err = c.Workspace(ctx, a); err != nil {
			return err
		}
	}
	return c.call(ctx, a, "PATCH", fmt.Sprintf("/workspaces/%d/time_entries/%d/stop", wid, entryID), nil, nil)
}

// AsJSON is the part of an entry the page uses; nil is {}.
func AsJSON(e *Entry) map[string]any {
	if e == nil {
		return map[string]any{}
	}
	tags := e.Tags
	if tags == nil {
		tags = []string{}
	}
	return map[string]any{
		"id": e.ID, "description": e.Description, "project_id": e.ProjectID,
		"workspace_id": e.WorkspaceID, "tags": tags, "start": e.Start,
	}
}

// IsTogglError reports whether err is one to show the person.
func IsTogglError(err error) bool {
	var te *Error
	return errors.As(err, &te)
}
