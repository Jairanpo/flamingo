package ytdlp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Listing channels, playlists and hand-picked videos: a port of
// sync_source, _sync_picks and browse in ytlocal/ytdlp.py.

// ErrNothing: the listing came back empty. The message carries yt-dlp's
// own complaint, which is usually the real reason.
var ErrNothing = errors.New("nothing found")

// Meta says what a listing was of.
type Meta struct {
	Kind      string // "channel" | "playlist" | "picks"
	ID        string // channel id, playlist id; for picks, the channel id
	Name      string // channel name or playlist title
	Owner     string // a playlist's channel name
	Handle    string // "@name" or ""
	ChannelID string // the channel it hangs under; "" when YouTube hid it
	Section   string // "" | "live" | "course" (course only from Browse)
}

// Entry is one video slot in a listing.
type Entry struct {
	ID          string
	Title       string // "" when the video is private, deleted or blocked
	Description string
	Duration    *int
	Published   string // YYYY-MM-DD or ""
	MonthOnly   bool   // flat listings only know roughly when
	ChannelID   string // the uploader, when the listing says
	ChannelName string
	Position    int
}

func (e Entry) Unavailable() bool { return e.Title == "" }

var jsOnce sync.Once
var jsArgs []string

// jsRuntime: YouTube hides its video addresses behind a JavaScript puzzle.
// yt-dlp only looks for deno by itself, so point it at node when that is
// what this machine has.
func jsRuntime() []string {
	jsOnce.Do(func() {
		if _, err := exec.LookPath("deno"); err == nil {
			return
		}
		if node, err := exec.LookPath("node"); err == nil {
			jsArgs = []string{"--js-runtimes", "node:" + node}
		}
	})
	return jsArgs
}

func (r Runner) path() string {
	if r.Path == "" {
		return "yt-dlp"
	}
	return r.Path
}

func (r Runner) base() []string {
	args := []string{"--ignore-config", "--no-warnings"}
	has := false
	for _, a := range r.ExtraArgs {
		has = has || a == "--js-runtimes"
	}
	if !has {
		args = append(args, jsRuntime()...)
	}
	return append(args, r.ExtraArgs...)
}

// lines runs yt-dlp and decodes each JSON line it prints. A failing exit is
// not an error by itself: with --ignore-errors yt-dlp exits non-zero over a
// single bad entry, so the caller judges by what came back.
func (r Runner) lines(ctx context.Context, args ...string) ([]map[string]any, string, error) {
	cmd := r.command(ctx, append(r.base(), args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, "", fmt.Errorf("yt-dlp stopped: %w", ctx.Err())
	}
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		return nil, "", fmt.Errorf("could not run yt-dlp (%s): %w", r.path(), err)
	}
	var out []map[string]any
	sc := bufio.NewScanner(&stdout)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		m := map[string]any{}
		if json.Unmarshal(line, &m) == nil {
			out = append(out, m)
		}
	}
	return out, stderr.String(), nil
}

func (r Runner) flat(ctx context.Context, url string, end int, extra ...string) ([]map[string]any, string, error) {
	args := []string{"--flat-playlist", "--dump-json", "--ignore-errors"}
	args = append(args, extra...)
	if end > 0 {
		args = append(args, "--playlist-end", fmt.Sprint(end))
	}
	return r.lines(ctx, append(args, url)...)
}

func s(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func duration(m map[string]any) *int {
	if f, ok := m["duration"].(float64); ok && f > 0 {
		d := int(f)
		return &d
	}
	return nil
}

func date(raw string) string {
	if len(raw) == 8 && strings.Trim(raw, "0123456789") == "" {
		return raw[0:4] + "-" + raw[4:6] + "-" + raw[6:8]
	}
	return ""
}

func handleOf(m map[string]any) string {
	for _, k := range []string{"playlist_uploader_id", "uploader_id", "channel_url",
		"uploader_url", "playlist_webpage_url"} {
		if h := handleInRE.FindStringSubmatch(s(m, k)); h != nil {
			return "@" + h[1]
		}
	}
	return ""
}

func trimErr(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if len(stderr) > 800 {
		stderr = stderr[:800]
	}
	return stderr
}

// List catalogues a channel's uploads, a playlist, or (for a picks URL)
// the videos named in it. limit > 0 stops after that many entries.
//
// Flat mode costs one request per ~100 videos, which is what makes a whole
// back catalogue cheap. Entries keep the listing's order: newest first for
// a channel, the author's order for a playlist.
func (r Runner) List(ctx context.Context, url string, limit int) (Meta, []Entry, error) {
	if IsPicks(url) {
		return r.picks(ctx, PickedIDs(url))
	}
	kind := URLKind(url)
	rows, stderr, err := r.flat(ctx, url, limit, "--extractor-args", "youtubetab:approximate_date")
	if err != nil {
		return Meta{}, nil, err
	}
	var meta Meta
	var entries []Entry
	for _, e := range rows {
		vid := s(e, "id")
		if !IsVideoID(vid) {
			continue
		}
		if meta.Kind == "" {
			// Flat mode puts the list's identity on the playlist_* keys. For a
			// playlist, playlist_channel_id is the owning channel.
			if kind == "playlist" {
				pid := s(e, "playlist_id")
				if pid == "" {
					pid = ListID(url)
				}
				meta = Meta{Kind: "playlist", ID: pid,
					Name:      s(e, "playlist_title", "playlist"),
					Owner:     s(e, "playlist_channel", "playlist_uploader", "channel"),
					Handle:    handleOf(e),
					ChannelID: s(e, "playlist_channel_id")}
				if live := ChannelOfLive(pid); live != "" {
					meta.Section = "live"
					if meta.ChannelID == "" {
						meta.ChannelID = live
					}
				}
			} else {
				cid := s(e, "channel_id", "playlist_channel_id", "playlist_id")
				name := s(e, "channel", "playlist_channel", "playlist_uploader", "uploader")
				meta = Meta{Kind: "channel", ID: cid, Name: name, Handle: handleOf(e), ChannelID: cid}
				if strings.HasSuffix(url, "/streams") && LivePlaylistID(cid) != "" {
					// The Live tab shares the channel's id; it is filed as
					// the channel's live playlist instead.
					meta = Meta{Kind: "playlist", ID: LivePlaylistID(cid), Name: "Live streams",
						Owner: name, Handle: meta.Handle, ChannelID: cid, Section: "live"}
				}
			}
		}
		d := date(s(e, "upload_date"))
		entries = append(entries, Entry{
			ID: vid, Title: s(e, "title"), Description: s(e, "description"),
			Duration: duration(e), Published: d, MonthOnly: d != "",
			ChannelID: s(e, "channel_id"), ChannelName: s(e, "channel", "uploader"),
			Position: len(entries),
		})
	}
	if len(entries) == 0 {
		return Meta{}, nil, fmt.Errorf("%w: no videos found at %s\n%s", ErrNothing, url, trimErr(stderr))
	}
	if meta.ID == "" {
		meta.ID = ListID(url)
	}
	if meta.ID == "" {
		return Meta{}, nil, fmt.Errorf("%w: YouTube did not say what %s is", ErrNothing, url)
	}
	return meta, entries, nil
}

// picks reads full metadata for hand-picked videos: a request per video
// rather than per hundred, which buys exact dates. They are filed under the
// first one's channel.
func (r Runner) picks(ctx context.Context, ids []string) (Meta, []Entry, error) {
	if len(ids) == 0 {
		return Meta{}, nil, fmt.Errorf("%w: no video ids", ErrNothing)
	}
	args := []string{"--dump-json", "--skip-download", "--no-playlist", "--ignore-errors",
		"--ignore-no-formats-error"}
	for _, v := range ids {
		args = append(args, "https://www.youtube.com/watch?v="+v)
	}
	rows, stderr, err := r.lines(ctx, args...)
	if err != nil {
		return Meta{}, nil, err
	}
	infos := map[string]map[string]any{}
	var first map[string]any
	for _, row := range rows {
		id := s(row, "id")
		if first == nil {
			first = row
		}
		infos[id] = row
	}
	if first == nil {
		return Meta{}, nil, fmt.Errorf("%w: could not read %s\n%s", ErrNothing,
			strings.Join(ids, ", "), trimErr(stderr))
	}
	cid := s(first, "channel_id")
	if cid == "" {
		cid = r.channelIDOf(ctx, s(first, "uploader_id"))
	}
	if cid == "" {
		return Meta{}, nil, fmt.Errorf("%w: YouTube did not say whose video %s is", ErrNothing, s(first, "id"))
	}
	meta := Meta{Kind: "picks", ID: cid, Name: "Picked videos",
		Owner: s(first, "channel", "uploader"), Handle: handleOf(first),
		ChannelID: cid, Section: "picks"}
	var entries []Entry
	for pos, vid := range ids {
		info, ok := infos[vid]
		if !ok {
			// Gone private or deleted since: keep the slot, as a playlist does.
			entries = append(entries, Entry{ID: vid, Position: pos})
			continue
		}
		entries = append(entries, Entry{
			ID: vid, Title: s(info, "title"), Description: s(info, "description"),
			Duration: duration(info), Published: date(s(info, "upload_date")),
			ChannelID: s(info, "channel_id"), ChannelName: s(info, "channel", "uploader"),
			Position: pos,
		})
	}
	return meta, entries, nil
}

// channelIDOf finds a channel's UC id from its @handle, for when a video's
// page left it out.
func (r Runner) channelIDOf(ctx context.Context, handle string) string {
	if handle == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	rows, _, err := r.flat(ctx, ChannelBase(handle)+"/videos", 1)
	if err != nil {
		return ""
	}
	for _, e := range rows {
		if id := s(e, "playlist_channel_id", "channel_id"); id != "" {
			return id
		}
	}
	return ""
}

// BrowseItem is one thing a creator has that can be tracked as a playlist.
type BrowseItem struct {
	ID      string `json:"id"`
	Section string `json:"section"` // course | live | playlist
	Title   string `json:"title"`
}

var sections = map[string]int{"course": 0, "live": 1, "playlist": 2}

// Browse lists a creator's courses, live streams and playlists. The tabs
// are listed side by side, so it takes about as long as the slowest one.
// The Home tab is read too, for playlists featured there but missing from
// the Playlists tab, and the Playlists page itself for "shows", which
// yt-dlp skips.
func (r Runner) Browse(ctx context.Context, ref string) (owner string, found []BrowseItem, err error) {
	base := ChannelBase(ref)
	type tab struct {
		url string
		end int
	}
	tabs := map[string]tab{"course": {base + "/courses", 0}, "playlist": {base + "/playlists", 0},
		"featured": {base + "/featured", 0}, "live": {base + "/streams", 1}}
	type result struct {
		rows   []map[string]any
		stderr string
		err    error
	}
	got := map[string]result{}
	var shows []BrowseItem
	var mu sync.Mutex
	var wg sync.WaitGroup
	for k, t := range tabs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, stderr, err := r.flat(ctx, t.url, t.end)
			mu.Lock()
			got[k] = result{rows, stderr, err}
			mu.Unlock()
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		shows = showLists(ctx, base+"/playlists")
	}()
	wg.Wait()
	for _, res := range got {
		if res.err != nil {
			return "", nil, res.err
		}
	}

	channelID := ""
	for _, k := range []string{"course", "playlist", "featured", "live"} {
		for _, e := range got[k].rows {
			// On these tabs the entry's own channel keys say "View full
			// playlist"; the owner is on the playlist_* keys.
			if owner == "" {
				owner = s(e, "playlist_channel", "playlist_uploader")
				if owner == "" {
					if t := s(e, "playlist_title"); t != "" {
						owner = t
						if i := strings.LastIndex(t, " - "); i >= 0 {
							owner = t[:i]
						}
					}
				}
			}
			if channelID == "" {
				channelID = s(e, "playlist_channel_id")
				if pid := s(e, "playlist_id"); channelID == "" && strings.HasPrefix(pid, "UC") {
					channelID = pid
				}
			}
		}
	}

	seen := map[string]bool{}
	add := func(id, section, title string) {
		if id == "" || !IsPlaylistID(id) || seen[id] {
			return
		}
		seen[id] = true
		if title == "" {
			title = id
		}
		found = append(found, BrowseItem{ID: id, Section: section, Title: title})
	}
	for _, k := range []string{"course", "playlist", "featured"} {
		section := k
		if k == "featured" {
			section = "playlist"
		}
		for _, e := range got[k].rows {
			pid := s(e, "id")
			if pid == "" {
				pid = ListID(s(e, "url"))
			}
			// The Home tab's "Videos" and "Popular" shelves are the channel's
			// own UU lists, which the channel already covers.
			if k == "featured" && !hasAnyPrefix(pid, "PL", "OLAK") {
				continue
			}
			add(pid, section, s(e, "title"))
		}
		if k == "playlist" {
			for _, sh := range shows {
				add(sh.ID, "playlist", sh.Title)
			}
		}
	}
	if live := LivePlaylistID(channelID); live != "" {
		for _, e := range got["live"].rows {
			if IsVideoID(s(e, "id")) {
				found = append(found, BrowseItem{ID: live, Section: "live", Title: "Live streams"})
				break
			}
		}
	}
	if len(found) == 0 {
		return "", nil, fmt.Errorf("%w: no courses, live streams or playlists found at %s\n%s",
			ErrNothing, base, trimErr(got["playlist"].stderr))
	}
	sort.SliceStable(found, func(i, j int) bool { return sections[found[i].Section] < sections[found[j].Section] })
	return owner, found, nil
}

const userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

var initialDataRE = regexp.MustCompile(`(?s)var ytInitialData = (\{.*?\});</script>`)

// showLists finds the playlists a channel files as "shows" (Driver61 has
// five), which yt-dlp's tab listing skips. Best effort: a failed fetch or a
// changed page gives nothing.
func showLists(ctx context.Context, url string) []BrowseItem {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "en")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	html, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil
	}
	m := initialDataRE.FindSubmatch(html)
	if m == nil {
		return nil
	}
	var data any
	if json.Unmarshal(m[1], &data) != nil {
		return nil
	}
	var found []BrowseItem
	var walk func(any)
	walk = func(o any) {
		switch v := o.(type) {
		case map[string]any:
			if lv, ok := v["lockupViewModel"].(map[string]any); ok &&
				s(lv, "contentType") == "LOCKUP_CONTENT_TYPE_SHOW" {
				pid := s(lv, "contentId")
				title := pid
				if md, ok := lv["metadata"].(map[string]any); ok {
					if lm, ok := md["lockupMetadataViewModel"].(map[string]any); ok {
						if t, ok := lm["title"].(map[string]any); ok && s(t, "content") != "" {
							title = s(t, "content")
						}
					}
				}
				if IsPlaylistID(pid) {
					found = append(found, BrowseItem{ID: pid, Section: "playlist", Title: title})
				}
			}
			for _, x := range v {
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(data)
	return found
}
