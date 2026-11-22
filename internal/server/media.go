package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/Jairanpo/flamingo/internal/catalog"
	"github.com/Jairanpo/flamingo/internal/ytdlp"
)

// Pictures and video bytes the page needs from YouTube, fetched here so the
// page itself never reaches out to Google.

var rangeRE = regexp.MustCompile(`^bytes=(\d*)-(\d*)$`)

const browserUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

// placeholder stands in for a thumbnail YouTube would not give.
const placeholder = `<svg xmlns="http://www.w3.org/2000/svg" width="320" height="180">` +
	`<rect width="320" height="180" fill="#06182f"/>` +
	`<text x="160" y="96" fill="#545677" font-family="sans-serif" font-size="14"` +
	` text-anchor="middle">no thumbnail</text></svg>`

func (s *Server) routesMedia(mux *http.ServeMux) {
	mux.HandleFunc("GET /thumb/{vid}", s.requireUser(s.thumb))
	mux.HandleFunc("GET /api/chapters/{vid}", s.requireUser(s.apiChapters))
	mux.HandleFunc("GET /api/description/{vid}", s.requireUser(s.apiDescription))
	mux.HandleFunc("GET /api/stream/{vid}", s.requireUser(s.apiStream))
	mux.HandleFunc("GET /stream/{vid}/audio", s.requireUser(s.stream))
	mux.HandleFunc("GET /stream/{vid}/video/{height}", s.requireUser(s.stream))
}

// --- thumbnails ---------------------------------------------------------

// thumb is cached on first view; every later view is a local file. Thumbs
// are shared: a picture is the same for everyone.
func (s *Server) thumb(w http.ResponseWriter, r *http.Request) {
	vid := r.PathValue("vid")
	if !vidRE.MatchString(vid) {
		fail(w, http.StatusBadRequest, "bad id")
		return
	}
	dest := filepath.Join(s.cfg.Storage.ThumbsDir, vid+".jpg")
	if fi, err := os.Stat(dest); err != nil || fi.Size() == 0 {
		if err := s.fetchThumb(r.Context(), vid, dest); err != nil {
			s.log.Debug("thumb", "vid", vid, "err", err)
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = io.WriteString(w, placeholder)
			return
		}
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=604800")
	http.ServeFile(w, r, dest)
}

func (s *Server) fetchThumb(ctx context.Context, vid, dest string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf(s.thumbURL, vid), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", browserUA)
	resp, err := s.web.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("thumbnail: %s", resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	// Written aside and renamed, so a half-fetched file is never served.
	tmp, err := os.CreateTemp(filepath.Dir(dest), vid+".*.part")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, 4<<20))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("thumbnail: empty")
	}
	return os.Rename(tmp.Name(), dest)
}

// --- chapters -----------------------------------------------------------

// apiChapters is looked up once per video, the first time its player opens.
func (s *Server) apiChapters(w http.ResponseWriter, r *http.Request) {
	vid := r.PathValue("vid")
	if !vidRE.MatchString(vid) {
		fail(w, http.StatusBadRequest, "bad id")
		return
	}
	chs, err := s.cat.Chapters(r.Context(), vid)
	if errors.Is(err, catalog.ErrUnknownVideo) {
		fail(w, http.StatusNotFound, "unknown video")
		return
	}
	if err != nil {
		s.failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chapters": chs})
}

// apiDescription fills in what a quick sync left out, the first time a
// video's player opens without one.
func (s *Server) apiDescription(w http.ResponseWriter, r *http.Request) {
	vid := r.PathValue("vid")
	if !vidRE.MatchString(vid) {
		fail(w, http.StatusBadRequest, "bad id")
		return
	}
	d, err := s.cat.Description(r.Context(), vid)
	if errors.Is(err, catalog.ErrUnknownVideo) {
		fail(w, http.StatusNotFound, "unknown video")
		return
	}
	if err != nil {
		s.failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"description": d})
}

// --- streaming without downloading ---------------------------------------
//
// The page plays a stream through MediaSource: it reads each track's index,
// then asks for the byte ranges it needs. Every one of those requests comes
// here and is relayed to YouTube.

// Streamer resolves stream addresses; ytdlp.Runner is the real one.
type Streamer interface {
	Stream(ctx context.Context, vid string, maxHeight int) (*ytdlp.Stream, error)
}

// streams remembers resolved addresses so a seek does not cost a yt-dlp
// run. The signed URLs live about six hours; five keeps a margin. One lock
// per video stops the page's parallel video and audio requests from
// resolving the same thing twice.
type streams struct {
	yt        Streamer
	maxHeight int
	ttl       time.Duration

	mu    sync.Mutex
	locks map[string]*sync.Mutex
	hits  map[string]streamHit
}

type streamHit struct {
	at time.Time
	st *ytdlp.Stream
}

func newStreams(yt Streamer, maxHeight int) *streams {
	return &streams{yt: yt, maxHeight: maxHeight, ttl: 5 * time.Hour,
		locks: map[string]*sync.Mutex{}, hits: map[string]streamHit{}}
}

func (c *streams) get(ctx context.Context, vid string, fresh bool) (*ytdlp.Stream, error) {
	c.mu.Lock()
	l := c.locks[vid]
	if l == nil {
		l = &sync.Mutex{}
		c.locks[vid] = l
	}
	c.mu.Unlock()

	l.Lock()
	defer l.Unlock()
	c.mu.Lock()
	hit, ok := c.hits[vid]
	c.mu.Unlock()
	if !fresh && ok && time.Since(hit.at) < c.ttl {
		return hit.st, nil
	}
	st, err := c.yt.Stream(ctx, vid, c.maxHeight)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	// Drop what has expired so the map does not grow without end.
	for k, h := range c.hits {
		if time.Since(h.at) >= c.ttl {
			delete(c.hits, k)
			delete(c.locks, k)
		}
	}
	c.hits[vid] = streamHit{time.Now(), st}
	c.mu.Unlock()
	return st, nil
}

func (s *Server) apiStream(w http.ResponseWriter, r *http.Request) {
	vid := r.PathValue("vid")
	if !vidRE.MatchString(vid) {
		fail(w, http.StatusBadRequest, "bad id")
		return
	}
	st, err := s.streams.get(r.Context(), vid, false)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	// What the lookup learned fills the catalog's gaps; a video not in the
	// catalog is left alone.
	if err := s.cat.SetChapters(r.Context(), vid, st.Chapters); err != nil {
		s.log.Warn("stream: chapters", "vid", vid, "err", err)
	}
	if err := s.cat.FillDuration(r.Context(), vid, st.Duration); err != nil {
		s.log.Warn("stream: duration", "vid", vid, "err", err)
	}
	if err := s.cat.FillDescription(r.Context(), vid, st.Description); err != nil {
		s.log.Warn("stream: description", "vid", vid, "err", err)
	}
	videos := map[string]string{}
	for h, t := range st.Videos {
		videos[strconv.Itoa(h)] = t.Mime
	}
	chs := st.Chapters
	if chs == nil {
		chs = []ytdlp.Chapter{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"duration": st.Duration, "chapters": chs, "videos": videos,
		"default": st.Default, "audio": st.Audio.Mime,
	})
}

// stream relays one byte range of /stream/<id>/audio or
// /stream/<id>/video/<height>.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	vid, hs := r.PathValue("vid"), r.PathValue("height")
	kind := "audio"
	height := 0
	if hs != "" {
		kind = "video"
		h, err := strconv.Atoi(hs)
		if err != nil || h <= 0 {
			fail(w, http.StatusBadRequest, "bad stream")
			return
		}
		height = h
	}
	if !vidRE.MatchString(vid) {
		fail(w, http.StatusBadRequest, "bad stream")
		return
	}
	rng := r.Header.Get("Range")
	if !rangeRE.MatchString(rng) || rng == "bytes=-" {
		fail(w, http.StatusBadRequest, "a byte range is required")
		return
	}
	var resp *http.Response
	for _, fresh := range []bool{false, true} {
		st, err := s.streams.get(r.Context(), vid, fresh)
		if err != nil {
			fail(w, http.StatusBadGateway, err.Error())
			return
		}
		track, ok := st.Audio, true
		if kind == "video" {
			track, ok = st.Videos[height]
		}
		if !ok {
			fail(w, http.StatusNotFound, "no such quality")
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), "GET", track.URL, nil)
		if err != nil {
			fail(w, http.StatusBadGateway, err.Error())
			return
		}
		for k, v := range track.Headers {
			req.Header.Set(k, v)
		}
		req.Header.Set("Range", rng)
		resp, err = s.relay.Do(req)
		if err != nil {
			if r.Context().Err() != nil {
				return // the browser seeked away or closed the tab
			}
			fail(w, http.StatusBadGateway, "could not reach YouTube")
			return
		}
		if resp.StatusCode < 300 {
			break
		}
		resp.Body.Close()
		// 403 is what an expired signature looks like: resolve again once.
		if resp.StatusCode != http.StatusForbidden || fresh {
			fail(w, http.StatusBadGateway, fmt.Sprintf("YouTube answered %d", resp.StatusCode))
			return
		}
	}
	defer resp.Body.Close()
	h := w.Header()
	h.Set("Content-Type", kind+"/mp4")
	for _, k := range []string{"Content-Length", "Content-Range"} {
		if v := resp.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, resp.Body)
}
