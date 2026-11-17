package agent

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Jairanpo/flamingo/internal/ytdlp"
)

// Job is a download card on the page. Its JSON matches the server's job
// cards, so the page shows both in one list.
type Job struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Kind     string   `json:"kind"`  // "download"
	State    string   `json:"state"` // queued | running | done | error
	Fraction *float64 `json:"fraction"`
	Detail   string   `json:"detail"`
	Error    *string  `json:"error"`
	Result   any      `json:"result"`
}

func (j *Job) live() bool { return j.State == "queued" || j.State == "running" }

type request struct {
	job    *Job
	audio  bool
	height int
}

// Downloader is a small worker pool. Two workers keep the line busy without
// hammering YouTube. Cards live in memory, as the Python app's did.
type Downloader struct {
	YT       ytdlp.Runner
	Index    *Index
	SubLangs string
	// Folder is where new files go right now (the server can change it).
	Folder func() (string, error)
	// Changed is called after a file appears or goes, to tell the server.
	Changed func()

	mu    sync.Mutex
	jobs  []*Job
	queue chan request
	once  sync.Once
}

func (d *Downloader) Start(ctx context.Context, workers int) {
	d.once.Do(func() {
		d.queue = make(chan request, 256)
		for range workers {
			go d.work(ctx)
		}
	})
}

// Enqueue asks for a video, or hands back the card already fetching it.
func (d *Downloader) Enqueue(id, title string, audio bool, height int) Job {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, j := range d.jobs {
		if j.ID == id && j.live() {
			return *j
		}
	}
	d.dropLocked(id)
	if title == "" {
		title = id
	}
	j := &Job{ID: id, Title: title, Kind: "download", State: "queued", Detail: "queued"}
	d.jobs = append(d.jobs, j)
	select {
	case d.queue <- request{j, audio, height}:
	default:
		msg := "too many downloads waiting; try again later"
		j.State, j.Detail, j.Error = "error", "failed", &msg
	}
	return *j
}

func (d *Downloader) dropLocked(id string) {
	out := d.jobs[:0]
	for _, j := range d.jobs {
		if j.ID != id {
			out = append(out, j)
		}
	}
	d.jobs = out
}

// Snapshot copies the cards for the page.
func (d *Downloader) Snapshot() []Job {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Job, len(d.jobs))
	for i, j := range d.jobs {
		out[i] = *j
	}
	return out
}

// Forget drops one finished card, or every finished one when id is "".
// Running cards stay; the page hides those itself.
func (d *Downloader) Forget(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	hit := false
	out := d.jobs[:0]
	for _, j := range d.jobs {
		if !j.live() && (id == "" || j.ID == id) {
			hit = true
			continue
		}
		out = append(out, j)
	}
	d.jobs = out
	return hit
}

func (d *Downloader) set(j *Job, fn func()) {
	d.mu.Lock()
	fn()
	d.mu.Unlock()
}

func (d *Downloader) work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-d.queue:
			d.run(ctx, req)
		}
	}
}

func (d *Downloader) run(ctx context.Context, req request) {
	j := req.job
	d.set(j, func() { j.State, j.Detail = "running", "starting" })
	fail := func(err error) {
		msg := err.Error()
		d.set(j, func() { j.State, j.Detail, j.Error = "error", "failed", &msg })
	}
	dir, err := d.Folder()
	if err != nil {
		fail(err)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(err)
		return
	}
	old, hadOld := d.Index.Get(j.ID)

	ctx, cancel := context.WithTimeout(ctx, 3*time.Hour)
	defer cancel()
	path, err := d.YT.Download(ctx, j.ID, dir, req.audio, req.height, func(f *float64, detail string) {
		d.set(j, func() { j.Fraction, j.Detail = f, detail })
	})
	if err != nil {
		fail(err)
		return
	}
	// Downloading again at another quality can land under another name:
	// the old copy goes, so the folder never holds two.
	if hadOld && filepath.Clean(old.Path) != filepath.Clean(path) {
		_ = os.Remove(old.Path)
	}
	if !req.audio && d.SubLangs != "" {
		d.set(j, func() { j.Detail = "fetching subtitles" })
		_ = d.YT.Subtitles(ctx, j.ID, dir, d.SubLangs) // best effort
	}
	if d.Changed != nil {
		d.Changed()
	}
	one := 1.0
	d.set(j, func() { j.State, j.Fraction, j.Detail = "done", &one, "done" })
}
