package ytdlp

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// What the Add and Browse boxes accept, and how it becomes a URL yt-dlp can
// page through. A port of the ref helpers in ytlocal/ytdlp.py and server.py.

var (
	videoIDRE    = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	channelIDRE  = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)
	playlistIDRE = regexp.MustCompile(`^(PL|OLAK|UU|UL|FL|RD)[A-Za-z0-9_-]{4,}$`)
	handleRE     = regexp.MustCompile(`^@?[A-Za-z0-9_.\-]{2,120}$`)
	tabRE        = regexp.MustCompile(`/(videos|streams|shorts|playlists|courses)$`)
	listRE       = regexp.MustCompile(`[?&]list=([A-Za-z0-9_-]+)`)
	handleInRE   = regexp.MustCompile(`@([\w.\-]+)`)
	// One video, by any of the links YouTube hands out for it.
	videoURLRE = regexp.MustCompile(`(?:youtube\.com/(?:watch\?(?:.*&)?v=|shorts/|live/|embed/)|youtu\.be/)([A-Za-z0-9_-]{11})`)
)

// ytHosts is everything this library tracks. A page that forbids outbound
// requests of its own has no business handing yt-dlp an arbitrary URL.
var ytHosts = map[string]bool{
	"youtube.com": true, "www.youtube.com": true, "m.youtube.com": true,
	"music.youtube.com": true, "youtu.be": true, "www.youtu.be": true,
}

// ErrRef is wrapped by every "that is not something I can track" answer;
// the message is meant for the person who typed it.
var ErrRef = errors.New("bad reference")

type refError string

func (e refError) Error() string { return string(e) }
func (e refError) Unwrap() error { return ErrRef }

// IsVideoID, IsChannelID and IsPlaylistID check YouTube's id shapes.
func IsVideoID(s string) bool    { return videoIDRE.MatchString(s) }
func IsChannelID(s string) bool  { return channelIDRE.MatchString(s) }
func IsPlaylistID(s string) bool { return playlistIDRE.MatchString(s) }

func checkRef(raw, what string) (string, error) {
	ref := strings.TrimSpace(raw)
	if ref == "" {
		return "", refError(what)
	}
	if len(ref) > 400 {
		return "", refError("that is too long to be a channel or playlist")
	}
	if strings.Contains(ref, "://") || strings.HasPrefix(strings.ToLower(ref), "www.") {
		u := ref
		if !strings.Contains(u, "://") {
			u = "https://" + u
		}
		host := ""
		if p, err := url.Parse(u); err == nil {
			host = strings.ToLower(p.Hostname())
		}
		if !ytHosts[host] {
			if host == "" {
				host = "that"
			}
			return "", refError(host + " is not YouTube — this library only tracks youtube.com")
		}
		return ref, nil
	}
	if !handleRE.MatchString(ref) {
		return "", refError(what)
	}
	return ref, nil
}

// CleanSourceRef checks what the Add box was given: a handle, a bare id, a
// YouTube URL or a video link.
func CleanSourceRef(raw string) (string, error) {
	ref, err := checkRef(raw, "paste a channel handle (@name), a channel URL, "+
		"a playlist URL, a PL… id or a video link")
	if err != nil {
		return "", err
	}
	if IsIndex(SourceURL(ref)) {
		// The tab lists playlists, not videos: syncing it would find nothing.
		return "", refError("that is a creator's playlist index, not one source — use Browse to pick from it")
	}
	return ref, nil
}

// CleanCreatorRef checks what Browse was given: a creator, whose tabs can
// then be listed.
func CleanCreatorRef(raw string) (string, error) {
	ref, err := checkRef(raw, "paste a channel handle (@name) or a channel URL "+
		"to see its courses, live streams and playlists")
	if err != nil {
		return "", err
	}
	if strings.Contains(ref, "list=") || hasAnyPrefix(ref, "PL", "OLAK", "UU", "FL", "RD") {
		return "", refError("that is one playlist, not a creator — Add tracks it directly")
	}
	return ref, nil
}

func hasAnyPrefix(s string, p ...string) bool {
	for _, x := range p {
		if strings.HasPrefix(s, x) {
			return true
		}
	}
	return false
}

// ChannelBase is a channel URL with no tab on the end, from a handle, name
// or URL.
func ChannelBase(ref string) string {
	ref = strings.TrimRight(strings.TrimSpace(ref), "/")
	if !strings.HasPrefix(ref, "http") {
		if !strings.HasPrefix(ref, "@") {
			ref = "@" + ref
		}
		ref = "https://www.youtube.com/" + ref
	}
	return tabRE.ReplaceAllString(ref, "")
}

// VideoID is the one video a link points at, or "". A link that also names
// a playlist is taken as that playlist.
func VideoID(ref string) string {
	if strings.Contains(ref, "list=") {
		return ""
	}
	if m := videoURLRE.FindStringSubmatch(strings.TrimSpace(ref)); m != nil {
		return m[1]
	}
	return ""
}

// PicksURL is how a picked video is asked for: YouTube's own address that
// plays a set of videos in a row.
const PicksURL = "https://www.youtube.com/watch_videos?video_ids="

func IsPicks(u string) bool { return strings.HasPrefix(u, PicksURL) }

// PickedIDs are the video ids in a picks URL.
func PickedIDs(u string) []string {
	if !IsPicks(u) {
		return nil
	}
	var out []string
	for _, v := range strings.Split(u[len(PicksURL):], ",") {
		if IsVideoID(v) {
			out = append(out, v)
		}
	}
	return out
}

// SourceURL normalises whatever was typed into a URL to page through.
func SourceURL(ref string) string {
	ref = strings.TrimRight(strings.TrimSpace(ref), "/")
	switch {
	case IsPicks(ref):
		return ref
	case VideoID(ref) != "":
		return PicksURL + VideoID(ref)
	case hasAnyPrefix(ref, "PL", "UU", "OLAK") && !strings.Contains(ref, "/"):
		return "https://www.youtube.com/playlist?list=" + ref
	case strings.Contains(ref, "list="):
		return ref
	}
	// A tab named on purpose is kept, including /playlists and /courses,
	// which the caller turns away.
	if m := tabRE.FindString(ref); m != "" {
		return ChannelBase(ref) + m
	}
	return ChannelBase(ref) + "/videos"
}

// IsIndex: the tab lists playlists rather than videos.
func IsIndex(u string) bool {
	return strings.HasSuffix(u, "/playlists") || strings.HasSuffix(u, "/courses")
}

// URLKind is "playlist" or "channel".
func URLKind(u string) string {
	if strings.Contains(u, "list=") || IsPicks(u) {
		return "playlist"
	}
	return "channel"
}

// ListID is the playlist id in a URL, or "".
func ListID(u string) string {
	if m := listRE.FindStringSubmatch(u); m != nil {
		return m[1]
	}
	return ""
}

// LivePlaylistID is YouTube's hidden playlist of a channel's past streams
// (UULV + the rest of its UC id), the same list the Live tab shows.
func LivePlaylistID(channelID string) string {
	if strings.HasPrefix(channelID, "UC") && len(channelID) > 2 {
		return "UULV" + channelID[2:]
	}
	return ""
}

// ChannelOfLive is the inverse of LivePlaylistID.
func ChannelOfLive(playlistID string) string {
	if strings.HasPrefix(playlistID, "UULV") {
		return "UC" + playlistID[4:]
	}
	return ""
}

// ChannelURL is the uploads tab of a channel id.
func ChannelURL(channelID string) string {
	return "https://www.youtube.com/channel/" + channelID + "/videos"
}

// PlaylistURL is the page of a playlist id.
func PlaylistURL(id string) string {
	return "https://www.youtube.com/playlist?list=" + id
}
