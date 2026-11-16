package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Jairanpo/yt/internal/ytdlp"
)

// Resolver looks up channels the SQLite file doesn't fully know. A nil
// Resolver means offline: what can't be found out is left unknown.
type Resolver interface {
	PlaylistOwner(ctx context.Context, playlistID string) (ytdlp.Owner, error)
	Channel(ctx context.Context, channelID string) (ytdlp.Owner, error)
}

const picksPrefix = "PICKS:"

// plan is what the import will write, already translated to the new schema.
type plan struct {
	channels  map[string]*channelRow
	playlists []playlistRow
	videos    []videoRow
	plVideos  []plVideoRow
	chapters  []chapterRow
	// videos whose chapter rows are replaced (looked up, even if none)
	chapterVids []string

	defaultSort string
	userChans   []userSourceRow
	userPlists  []userSourceRow
	picks       []pickRow
	userVideos  []userVideoRow
	collections []collectionRow
	marks       []markRow
	topics      map[string][]string // topic name → channel ids
	rules       map[string]*ruleRow // channel id → rule
	downloads   []downloadRow

	warnings []string
}

type channelRow struct {
	ID, Name  string
	Handle    *string
	SyncedAt  *time.Time
	CreatedAt *time.Time
}

type playlistRow struct {
	ID, Section, Title string
	ChanID             *string
	SyncedAt           *time.Time
	CreatedAt          time.Time
}

type videoRow struct {
	ID, Title         string
	ChanID            *string
	Description       *string
	Duration          *int64
	PublishedOn       *time.Time
	MonthOnly         bool
	ChannelPos        *int
	UnavailableSince  *time.Time
	ChaptersCheckedAt *time.Time
	CreatedAt         time.Time
}

type plVideoRow struct {
	PlistID, VidID string
	Position       int
}

type chapterRow struct {
	VidID      string
	Start, End float64
	Title      string
}

type userSourceRow struct {
	ID        string // chan_id or play_id
	Shelved   bool
	PinnedAt  *time.Time
	Sort      *string
	CreatedAt time.Time
}

type pickRow struct {
	userSourceRow // ID is the chan_id
	Videos        []plVideoRow
}

type userVideoRow struct {
	VidID                    string
	Watched, Starred, Hidden bool
	WatchedAt                *time.Time
	Progress                 float64
}

type collectionRow struct {
	Name      string
	Sort      *string
	CreatedAt time.Time
	Items     []collectionItem
}

type markRow struct {
	VidID     string
	Seconds   float64
	Note      *string
	CreatedAt time.Time
}

type ruleRow struct {
	source    string // the toggl_map name it came from
	Name      *string
	Project   *string
	Tags      []string
	CreatedAt time.Time
}

type downloadRow struct {
	VidID        string
	Size         *int64
	DownloadedAt time.Time
	AudioOnly    bool
}

// What an audio-only fetch leaves on disk (the Python app's AUDIO_EXTS).
var audioExts = []string{".m4a", ".mp3", ".opus", ".ogg", ".aac", ".flac", ".wav"}

func isAudio(path string) bool {
	for _, e := range audioExts {
		if strings.HasSuffix(path, e) {
			return true
		}
	}
	return false
}

func (p *plan) warn(format string, a ...any) {
	p.warnings = append(p.warnings, fmt.Sprintf(format, a...))
}

func key(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// keepText stores text exactly as written; only blank text becomes NULL.
func keepText(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

func nonEmpty(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

var sections = map[string]string{"": "PLAYLIST", "course": "COURSE", "live": "LIVE"}

func sortOrder(s string) (string, bool) {
	switch s {
	case "default", "newest", "oldest", "added", "title", "longest", "shortest":
		return strings.ToUpper(s), true
	}
	return "", false
}

func buildPlan(ctx context.Context, s *snapshot, res Resolver, now time.Time) (*plan, error) {
	p := &plan{
		channels:    map[string]*channelRow{},
		topics:      map[string][]string{},
		rules:       map[string]*ruleRow{},
		defaultSort: "DEFAULT",
	}
	byID := map[string]*source{}
	for i := range s.sources {
		byID[s.sources[i].ID] = &s.sources[i]
	}

	// --- channels -------------------------------------------------------
	addChannel := func(id, name string, handle *string, synced, created *time.Time) {
		c, ok := p.channels[id]
		if !ok {
			c = &channelRow{ID: id, Name: strings.TrimSpace(name)}
			p.channels[id] = c
		}
		if c.Name == "" {
			c.Name = strings.TrimSpace(name)
		}
		if c.Handle == nil && handle != nil {
			c.Handle = handle
		}
		if synced != nil && (c.SyncedAt == nil || synced.After(*c.SyncedAt)) {
			c.SyncedAt = synced
		}
		if created != nil && (c.CreatedAt == nil || created.Before(*c.CreatedAt)) {
			c.CreatedAt = created
		}
	}
	for _, src := range s.sources {
		if src.Kind == "channel" {
			addChannel(src.ID, src.Name.String, nonEmpty(src.Handle.String), nullEpoch(src.LastSync), nullEpoch(src.AddedAt))
		}
	}

	// A playlist's owner: known by id, or asked of YouTube.
	plistChan := map[string]*string{}
	for _, src := range s.sources {
		if src.Kind == "channel" {
			continue
		}
		id := src.ChanID.String
		owner := src.Owner.String
		handle := nonEmpty(src.Handle.String)
		if id == "" && res != nil && !strings.HasPrefix(src.ID, picksPrefix) {
			o, err := res.PlaylistOwner(ctx, src.ID)
			if err != nil {
				p.warn("playlist %q: could not find its channel (%v)", src.Name.String, err)
			} else {
				id, owner = o.ChannelID, o.Name
				if o.Handle != "" {
					handle = &o.Handle
				}
			}
		}
		if id == "" {
			if res == nil {
				p.warn("playlist %q: channel unknown (offline)", src.Name.String)
			}
			continue
		}
		name := owner
		if strings.TrimSpace(name) == "" && handle != nil {
			name = *handle
		}
		addChannel(id, name, handle, nil, nil)
		plistChan[src.ID] = &id
	}

	// Names the old app keyed things by → channel ids. The creator name was
	// either a channel's own name or a playlist's owner as YouTube shows it.
	byName := map[string]map[string]bool{}
	nameTo := func(name, id string) {
		k := key(name)
		if k == "" {
			return
		}
		if byName[k] == nil {
			byName[k] = map[string]bool{}
		}
		byName[k][id] = true
	}
	for id, c := range p.channels {
		nameTo(c.Name, id)
	}
	for _, src := range s.sources {
		if id := plistChan[src.ID]; id != nil {
			nameTo(src.Owner.String, *id)
		}
	}
	lookup := func(name string) (string, error) {
		ids := byName[key(name)]
		switch len(ids) {
		case 0:
			return "", fmt.Errorf("no channel by that name")
		case 1:
			for id := range ids {
				return id, nil
			}
		}
		return "", fmt.Errorf("%d channels share that name", len(ids))
	}

	// Topics may name channels the file never stored; look those up too.
	for _, t := range s.topics {
		if strings.HasPrefix(t.Creator, "UC") {
			if _, ok := p.channels[t.Creator]; !ok && res != nil {
				o, err := res.Channel(ctx, t.Creator)
				if err != nil {
					p.warn("topic %q: channel %s not found (%v)", t.Topic, t.Creator, err)
					continue
				}
				var h *string
				if o.Handle != "" {
					h = &o.Handle
				}
				addChannel(o.ChannelID, o.Name, h, nil, nil)
			}
		}
	}

	// Two channels must not claim one handle; the first keeps it.
	ids := make([]string, 0, len(p.channels))
	for id := range p.channels {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	handles := map[string]string{}
	for _, id := range ids {
		c := p.channels[id]
		if c.Handle == nil {
			continue
		}
		if other, taken := handles[key(*c.Handle)]; taken {
			p.warn("channels %s and %s both claim %s; kept it on the first", other, id, *c.Handle)
			c.Handle = nil
			continue
		}
		handles[key(*c.Handle)] = id
		if c.Name == "" {
			c.Name = *c.Handle
		}
	}

	// --- playlists, user sources, picks ---------------------------------
	sortOf := map[string]*string{}
	for _, pr := range s.prefs {
		so, ok := sortOrder(pr.Sort)
		if !ok {
			p.warn("unknown sort %q for %s; skipped", pr.Sort, pr.Scope)
			continue
		}
		if pr.Scope == "global" {
			p.defaultSort = so
			continue
		}
		sortOf[pr.Scope] = &so
	}

	for _, src := range s.sources {
		us := userSourceRow{
			ID: src.ID, Shelved: src.Shelved, PinnedAt: nullEpoch(src.PinnedAt),
			Sort: sortOf["source:"+src.ID], CreatedAt: epoch(src.AddedAt.Int64),
		}
		switch {
		case src.Kind == "channel":
			p.userChans = append(p.userChans, us)
		case strings.HasPrefix(src.ID, picksPrefix):
			if src.ChanID.String == "" {
				p.warn("picks list %s has no channel; skipped", src.ID)
				continue
			}
			us.ID = src.ChanID.String
			p.picks = append(p.picks, pickRow{userSourceRow: us})
		default:
			sec, ok := sections[src.Section.String]
			if !ok {
				p.warn("playlist %q: unknown section %q, filed as a plain playlist", src.Name.String, src.Section.String)
				sec = "PLAYLIST"
			}
			p.playlists = append(p.playlists, playlistRow{
				ID: src.ID, Section: sec, Title: src.Name.String, ChanID: plistChan[src.ID],
				SyncedAt: nullEpoch(src.LastSync), CreatedAt: epoch(src.AddedAt.Int64),
			})
			p.userPlists = append(p.userPlists, us)
		}
	}

	// --- memberships ------------------------------------------------------
	uploader := map[string]string{}
	chanPos := map[string]int{}
	pickIndex := map[string]int{}
	for i, pk := range p.picks {
		pickIndex[picksPrefix+pk.ID] = i
	}
	for _, m := range s.memberships {
		src := byID[m.SourceID]
		if src == nil {
			continue
		}
		switch {
		case src.Kind == "channel":
			uploader[m.VideoID] = src.ID
			chanPos[m.VideoID] = m.Position
		case strings.HasPrefix(src.ID, picksPrefix):
			i := pickIndex[src.ID]
			p.picks[i].Videos = append(p.picks[i].Videos, plVideoRow{VidID: m.VideoID, Position: m.Position})
			if _, ok := uploader[m.VideoID]; !ok {
				uploader[m.VideoID] = p.picks[i].ID
			}
		default:
			p.plVideos = append(p.plVideos, plVideoRow{PlistID: src.ID, VidID: m.VideoID, Position: m.Position})
			// A channel's live list holds only that channel's own streams.
			if src.Section.String == "live" && plistChan[src.ID] != nil {
				if _, ok := uploader[m.VideoID]; !ok {
					uploader[m.VideoID] = *plistChan[src.ID]
				}
			}
		}
	}

	// --- videos and per-user video state -----------------------------------
	leftSource := 0
	for _, v := range s.videos {
		row := videoRow{
			ID: v.ID, Title: v.Title, Description: keepText(v.Description.String),
			MonthOnly: v.DateApprox, CreatedAt: epoch(v.FirstSeen),
		}
		if c, ok := uploader[v.ID]; ok {
			row.ChanID = &c
			if pos, ok := chanPos[v.ID]; ok {
				row.ChannelPos = &pos
			}
		}
		if v.Duration.Valid {
			row.Duration = &v.Duration.Int64
		}
		if v.UploadDate.String != "" {
			d, err := time.Parse("2006-01-02", v.UploadDate.String)
			if err != nil {
				p.warn("video %s: unreadable date %q; left empty", v.ID, v.UploadDate.String)
			} else {
				row.PublishedOn = &d
			}
		}
		if v.Unavailable {
			// The old app didn't record since when; the import is the
			// earliest time we can vouch for.
			row.UnavailableSince = &now
		}
		if v.Chapters.Valid {
			var chs []struct {
				Start float64  `json:"start"`
				End   *float64 `json:"end"`
				Title string   `json:"title"`
			}
			if err := json.Unmarshal([]byte(v.Chapters.String), &chs); err != nil {
				p.warn("video %s: unreadable chapters; left for a fresh lookup", v.ID)
			} else {
				row.ChaptersCheckedAt = &now
				p.chapterVids = append(p.chapterVids, v.ID)
				for _, c := range chs {
					end := c.Start
					if c.End != nil && *c.End >= c.Start {
						end = *c.End
					}
					p.chapters = append(p.chapters, chapterRow{VidID: v.ID, Start: c.Start, End: end, Title: c.Title})
				}
			}
		}
		if v.LeftSource.String != "" {
			leftSource++
		}
		p.videos = append(p.videos, row)

		// A download is something this user chose to keep, so it stays in their
		// library even when no source lists it any more.
		if v.Watched || v.Starred || v.Hidden == 1 || v.Progress > 0 || v.DownloadedPath.String != "" {
			uv := userVideoRow{VidID: v.ID, Watched: v.Watched, Starred: v.Starred,
				Hidden: v.Hidden == 1, Progress: max(v.Progress, 0)}
			if v.Watched {
				uv.WatchedAt = nullEpoch(v.WatchedAt)
			}
			p.userVideos = append(p.userVideos, uv)
		}
		if v.DownloadedPath.String != "" {
			d := downloadRow{VidID: v.ID, DownloadedAt: epoch(v.DownloadedAt.Int64),
				AudioOnly: isAudio(v.DownloadedPath.String)}
			if v.Filesize.Valid {
				d.Size = &v.Filesize.Int64
			}
			p.downloads = append(p.downloads, d)
		}
	}
	if leftSource > 0 {
		p.warn("%d videos remember the playlist sync took them out of; that history is not carried over", leftSource)
	}

	// --- collections, marks -------------------------------------------------
	items := map[int64][]collectionItem{}
	for _, it := range s.items {
		items[it.CollectionID] = append(items[it.CollectionID], it)
	}
	for _, c := range s.collections {
		p.collections = append(p.collections, collectionRow{
			Name: c.Name, Sort: sortOf[fmt.Sprintf("collection:%d", c.ID)],
			CreatedAt: epoch(c.CreatedAt), Items: items[c.ID],
		})
	}
	for _, m := range s.marks {
		p.marks = append(p.marks, markRow{VidID: m.VideoID, Seconds: max(m.T, 0),
			Note: nonEmpty(m.Note), CreatedAt: epoch(m.CreatedAt)})
	}

	// --- topics -------------------------------------------------------------
	for _, t := range s.topics {
		var id string
		if name, ok := strings.CutPrefix(t.Creator, "name:"); ok {
			var err error
			if id, err = lookup(name); err != nil {
				p.warn("topic %q for %q: %v; skipped", t.Topic, name, err)
				continue
			}
		} else if _, ok := p.channels[t.Creator]; ok {
			id = t.Creator
		} else {
			if res == nil {
				p.warn("topic %q: channel %s unknown (offline); skipped", t.Topic, t.Creator)
			}
			continue
		}
		p.topics[t.Topic] = append(p.topics[t.Topic], id)
	}
	for name, chans := range p.topics {
		p.topics[name] = dedupe(chans)
	}

	// --- Toggl rules ----------------------------------------------------------
	for _, m := range s.toggl {
		id, err := lookup(m.Channel)
		if err != nil {
			p.warn("Toggl settings for %q: %v; skipped", m.Channel, err)
			continue
		}
		r := &ruleRow{source: m.Channel, Name: nonEmpty(m.Name.String), Project: nonEmpty(m.Project.String),
			CreatedAt: epoch(m.SetAt)}
		seen := map[string]bool{}
		for _, tag := range strings.Split(m.Tags.String, ",") {
			tag = strings.TrimSpace(tag)
			if tag != "" && !seen[key(tag)] {
				seen[key(tag)] = true
				r.Tags = append(r.Tags, tag)
			}
		}
		if prev, dup := p.rules[id]; dup {
			// Two old names for one channel: the one that is the channel's
			// own name wins.
			keep, drop := prev, r
			if key(m.Channel) == key(p.channels[id].Name) {
				keep, drop = r, prev
			}
			p.warn("Toggl settings for %q and %q are the same channel; kept %q", prev.source, m.Channel, keep.source)
			_ = drop
			p.rules[id] = keep
			continue
		}
		p.rules[id] = r
	}
	return p, nil
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	out := xs[:0]
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
