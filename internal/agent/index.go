// Package agent is the helper that runs on each computer: it shows the
// page through 127.0.0.1, passes the page's calls on to the server, and
// downloads, keeps and plays this computer's files.
package agent

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
)

// File is one download found on disk.
type File struct {
	Path  string
	Size  int64
	Audio bool
}

// Every file the agent writes ends in "[<id>].<ext>", and subtitles in
// "[<id>].<lang>.vtt". That is how a file is found again, wherever in the
// folder it sits and whatever it was called.
var (
	mediaRE = regexp.MustCompile(`\[([A-Za-z0-9_-]{11})\]\.(mp4|webm|mkv|mov|m4a|mp3|opus|ogg|aac)$`)
	subRE   = regexp.MustCompile(`\[([A-Za-z0-9_-]{11})\]\.([A-Za-z0-9_-]{1,20})\.vtt$`)
)

var audioExt = map[string]bool{"m4a": true, "mp3": true, "opus": true, "ogg": true, "aac": true}

// Index is what the download folder holds, by video id.
type Index struct {
	mu    sync.RWMutex
	dir   string
	files map[string]File
	subs  map[string]map[string]string // id → lang → path
}

// Scan reads the folder again. A missing folder is an empty one.
func (x *Index) Scan(dir string) error {
	files := map[string]File{}
	subs := map[string]map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir && os.IsNotExist(err) {
				return filepath.SkipAll
			}
			return nil // an unreadable corner is skipped, not fatal
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if m := subRE.FindStringSubmatch(name); m != nil {
			if subs[m[1]] == nil {
				subs[m[1]] = map[string]string{}
			}
			subs[m[1]][m[2]] = p
			return nil
		}
		m := mediaRE.FindStringSubmatch(name)
		if m == nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		f := File{Path: p, Size: info.Size(), Audio: audioExt[m[2]]}
		// A picture beats sound alone; between two of a kind, the bigger.
		if old, ok := files[m[1]]; ok && (old.Audio == f.Audio && old.Size >= f.Size || !old.Audio && f.Audio) {
			return nil
		}
		files[m[1]] = f
		return nil
	})
	if err != nil {
		return err
	}
	x.mu.Lock()
	x.dir, x.files, x.subs = dir, files, subs
	x.mu.Unlock()
	return nil
}

func (x *Index) Dir() string {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.dir
}

func (x *Index) Get(id string) (File, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	f, ok := x.files[id]
	return f, ok
}

// Langs lists the subtitles on disk for a video.
func (x *Index) Langs(id string) []string {
	x.mu.RLock()
	defer x.mu.RUnlock()
	var out []string
	for l := range x.subs[id] {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

func (x *Index) Sub(id, lang string) (string, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	p, ok := x.subs[id][lang]
	return p, ok
}

// Paths is every file kept for a video: the media and its subtitles.
func (x *Index) Paths(id string) []string {
	x.mu.RLock()
	defer x.mu.RUnlock()
	var out []string
	if f, ok := x.files[id]; ok {
		out = append(out, f.Path)
	}
	for _, p := range x.subs[id] {
		out = append(out, p)
	}
	return out
}

// Inventory is what the server is told: one entry per video, sorted so two
// reports of the same folder compare equal.
func (x *Index) Inventory() []InventoryFile {
	x.mu.RLock()
	defer x.mu.RUnlock()
	out := make([]InventoryFile, 0, len(x.files))
	for id, f := range x.files {
		size := f.Size
		out = append(out, InventoryFile{ID: id, Size: &size, Audio: f.Audio})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

type InventoryFile struct {
	ID    string `json:"id"`
	Size  *int64 `json:"size"`
	Audio bool   `json:"audio"`
}
