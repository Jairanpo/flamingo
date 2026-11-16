package catalog

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Job is one card on the page: a source being added. The JSON matches the
// Python app's job cards.
type Job struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Kind     string   `json:"kind"`  // "source"
	State    string   `json:"state"` // queued | running | done | error
	Fraction *float64 `json:"fraction"`
	Detail   string   `json:"detail"`
	Error    *string  `json:"error"`
	Result   *Result  `json:"result"`
}

func (j *Job) live() bool { return j.State == "queued" || j.State == "running" }

type task struct {
	user    int32
	job     *Job
	ref     string
	section string
}

// Queue runs Add jobs one at a time. One worker, not several: each is a
// handful of requests that finish in seconds, and two at once would only
// race over the same catalog rows. Each user sees only their own cards.
// Cards live in memory; a restart forgets them, as the Python app did.
type Queue struct {
	Cat     *Catalog
	Timeout time.Duration // per job; 0 means 15 minutes

	once  sync.Once
	mu    sync.Mutex
	jobs  map[int32][]*Job
	tasks chan task
}

func (q *Queue) start() {
	q.once.Do(func() {
		q.jobs = map[int32][]*Job{}
		q.tasks = make(chan task, 1024)
		go q.work()
	})
}

// Enqueue adds a source for a user, or hands back the card already doing
// it: the key is what was typed, so a double-click joins the running job.
func (q *Queue) Enqueue(user int32, ref, section string) Job {
	q.start()
	key := "src:" + strings.ToLower(strings.TrimSpace(ref))
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range q.jobs[user] {
		if j.ID == key && j.live() {
			return *j
		}
	}
	q.dropLocked(user, key)
	j := &Job{ID: key, Title: strings.TrimSpace(ref), Kind: "source", State: "queued"}
	q.jobs[user] = append(q.jobs[user], j)
	select {
	case q.tasks <- task{user, j, ref, section}:
	default:
		msg := "too many sources waiting; try again in a minute"
		j.State, j.Detail, j.Error = "error", "failed", &msg
	}
	return *j
}

func (q *Queue) dropLocked(user int32, key string) bool {
	list := q.jobs[user]
	for i, j := range list {
		if j.ID == key && !j.live() {
			q.jobs[user] = append(list[:i:i], list[i+1:]...)
			return true
		}
	}
	return false
}

// Snapshot is the user's cards, oldest first.
func (q *Queue) Snapshot(user int32) []Job {
	q.start()
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Job, 0, len(q.jobs[user]))
	for _, j := range q.jobs[user] {
		out = append(out, *j)
	}
	return out
}

// Forget drops one finished card. Running ones stay; the page hides them.
func (q *Queue) Forget(user int32, id string) bool {
	q.start()
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.dropLocked(user, id)
}

// ForgetFinished drops every finished card of the user's.
func (q *Queue) ForgetFinished(user int32) {
	q.start()
	q.mu.Lock()
	defer q.mu.Unlock()
	keep := q.jobs[user][:0]
	for _, j := range q.jobs[user] {
		if j.live() {
			keep = append(keep, j)
		}
	}
	q.jobs[user] = keep
}

func (q *Queue) set(f func()) {
	q.mu.Lock()
	defer q.mu.Unlock()
	f()
}

func (q *Queue) work() {
	for t := range q.tasks {
		j := t.job
		q.set(func() { j.State, j.Detail = "running", "resolving…" })
		timeout := q.Timeout
		if timeout == 0 {
			timeout = 15 * time.Minute
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		res, err := q.Cat.Add(ctx, t.user, t.ref, t.section, func(d string) {
			q.set(func() { j.Detail = d })
		})
		cancel()
		q.set(func() {
			if err != nil {
				msg := err.Error()
				j.State, j.Detail, j.Error = "error", "failed", &msg
				return
			}
			one := 1.0
			// Now that it has a name, the card stops calling it by
			// whatever was typed into the box.
			j.Title, j.Result, j.State, j.Fraction = res.Name, &res, "done", &one
			if res.New > 0 {
				j.Detail = fmt.Sprintf("%d catalogued · %d new", res.Total, res.New)
			} else {
				j.Detail = fmt.Sprintf("%d catalogued · nothing new", res.Total)
			}
		})
	}
}
