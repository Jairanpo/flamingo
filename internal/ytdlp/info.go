package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Chapter is one section a creator marked in a video, in seconds.
type Chapter struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Title string  `json:"title"`
}

// Track is one stream file YouTube serves: where to read it, the headers
// the request needs, and the MIME type MediaSource is told.
type Track struct {
	URL     string
	Headers map[string]string
	Mime    string
}

// Stream is where to read a video from without downloading it. Videos maps
// each height on offer to its track; Default is the tallest one within the
// height asked for. The URLs are signed and expire after a few hours.
type Stream struct {
	Duration    *int
	Chapters    []Chapter
	Description string
	Audio       Track
	Videos      map[int]Track
	Default     int
}

// ErrNoStream means the video exists but offers nothing the page can play.
var ErrNoStream = errors.New("no separate H.264 + AAC streams to play")

func videoURL(vid string) string { return "https://www.youtube.com/watch?v=" + vid }

// Details is what one video's page says beyond the listing: its chapters
// (often none) and the author's description, links and all.
type Details struct {
	Chapters    []Chapter
	Description string
}

// Details asks YouTube for one video's chapters and description.
func (r Runner) Details(ctx context.Context, vid string) (Details, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	lines, stderr, err := r.lines(ctx, "--dump-json", "--skip-download", "--no-playlist", videoURL(vid))
	if err != nil {
		return Details{}, err
	}
	if len(lines) == 0 {
		return Details{}, fmt.Errorf("yt-dlp %s: %s", vid, trimErr(stderr))
	}
	return Details{Chapters: chapters(lines[0]), Description: s(lines[0], "description")}, nil
}

func chapters(info map[string]any) []Chapter {
	raw, _ := info["chapters"].([]any)
	out := make([]Chapter, 0, len(raw))
	for _, c := range raw {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		start, _ := m["start_time"].(float64)
		end, _ := m["end_time"].(float64)
		title, _ := m["title"].(string)
		out = append(out, Chapter{Start: round2(start), End: round2(end), Title: title})
	}
	return out
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// streamSelector only makes yt-dlp fail when nothing streamable exists: one
// run lists every format, so changing quality later costs nothing. YouTube
// no longer serves picture and sound in one file, so a stream is always a
// pair the page stitches together. Plain https H.264 + AAC is what every
// browser's MediaSource takes, and those files carry the byte index that
// makes seeking possible.
const streamSelector = "bv*[ext=mp4][vcodec^=avc1][protocol=https]+ba[ext=m4a][protocol=https]"

// Stream resolves where to read a video from. maxHeight picks the default
// quality; every height on offer is still listed.
func (r Runner) Stream(ctx context.Context, vid string, maxHeight int) (*Stream, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	lines, stderr, err := r.lines(ctx, "-j", "--no-playlist", "-f", streamSelector, videoURL(vid))
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		msg := trimErr(stderr)
		if msg == "" {
			msg = "no streamable format"
		}
		return nil, fmt.Errorf("yt-dlp %s: %s", vid, msg)
	}
	return pickStream(lines[0], maxHeight)
}

type format struct {
	URL          string            `json:"url"`
	Headers      map[string]string `json:"http_headers"`
	Ext          string            `json:"ext"`
	Protocol     string            `json:"protocol"`
	VCodec       *string           `json:"vcodec"`
	ACodec       *string           `json:"acodec"`
	Height       *int              `json:"height"`
	DynamicRange *string           `json:"dynamic_range"`
	FPS          *float64          `json:"fps"`
	TBR          *float64          `json:"tbr"`
}

func none(s *string) bool { return s == nil || *s == "none" }

func (f format) track(kind string) Track {
	codec := f.VCodec
	if kind == "audio" {
		codec = f.ACodec
	}
	c := ""
	if codec != nil {
		c = *codec
	}
	return Track{URL: f.URL, Headers: f.Headers, Mime: fmt.Sprintf(`%s/mp4; codecs="%s"`, kind, c)}
}

// rank orders files of the same height: H.264 over AV1, normal colour over
// HDR (some videos have AV1 only in HDR), then the richest.
type rank struct {
	avc, sdr bool
	fps, tbr float64
}

func (a rank) less(b rank) bool {
	if a.avc != b.avc {
		return !a.avc
	}
	if a.sdr != b.sdr {
		return !a.sdr
	}
	if a.fps != b.fps {
		return a.fps < b.fps
	}
	return a.tbr < b.tbr
}

func pickStream(info map[string]any, maxHeight int) (*Stream, error) {
	b, _ := json.Marshal(info)
	var parsed struct {
		Duration  *float64 `json:"duration"`
		Formats   []format `json:"formats"`
		Requested []format `json:"requested_formats"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		return nil, fmt.Errorf("unreadable yt-dlp output: %w", err)
	}
	// H.264 stops at 1080p; above that YouTube has AV1 (and VP9 in webm,
	// whose index the page cannot read). AV1 in mp4 has the same index, so
	// it fills the taller heights, but only where H.264 is missing: it is
	// much harder on older machines.
	type pick struct {
		r rank
		f format
	}
	best := map[int]pick{}
	for _, f := range parsed.Formats {
		if f.VCodec == nil || f.Height == nil || f.Ext != "mp4" || f.Protocol != "https" || !none(f.ACodec) {
			continue
		}
		vc := *f.VCodec
		if !strings.HasPrefix(vc, "avc1") && !strings.HasPrefix(vc, "av01") {
			continue
		}
		rk := rank{avc: strings.HasPrefix(vc, "avc1"), sdr: f.DynamicRange == nil || *f.DynamicRange == "SDR"}
		if f.FPS != nil {
			rk.fps = *f.FPS
		}
		if f.TBR != nil {
			rk.tbr = *f.TBR
		}
		if cur, ok := best[*f.Height]; !ok || cur.r.less(rk) {
			best[*f.Height] = pick{rk, f}
		}
	}
	var audio *format
	for i, f := range parsed.Requested {
		if none(f.VCodec) {
			audio = &parsed.Requested[i]
			break
		}
	}
	if len(best) == 0 || audio == nil {
		return nil, ErrNoStream
	}
	st := &Stream{Chapters: chapters(info), Description: s(info, "description"),
		Audio: audio.track("audio"), Videos: map[int]Track{}}
	if parsed.Duration != nil {
		d := int(*parsed.Duration)
		st.Duration = &d
	}
	heights := make([]int, 0, len(best))
	for h, p := range best {
		st.Videos[h] = p.f.track("video")
		heights = append(heights, h)
	}
	sort.Ints(heights)
	st.Default = heights[0]
	for _, h := range heights {
		if h <= maxHeight {
			st.Default = h
		}
	}
	return st, nil
}
