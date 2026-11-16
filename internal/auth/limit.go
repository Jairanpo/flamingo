package auth

import (
	"sync"
	"time"
)

// Limiter slows down password guessing. It counts failed logins in a
// sliding window, both for one name from one address and for an address
// overall; a success clears that name's count. In memory only: a restart
// forgets, which is fine for a home server.
type Limiter struct {
	Window     time.Duration
	PerName    int // failures allowed for one name from one address
	PerAddress int // failures allowed from one address, any names

	mu   sync.Mutex
	fail map[string][]time.Time
}

func NewLimiter() *Limiter {
	return &Limiter{Window: 15 * time.Minute, PerName: 5, PerAddress: 20, fail: map[string][]time.Time{}}
}

func (l *Limiter) recent(key string, now time.Time) []time.Time {
	ts := l.fail[key]
	i := 0
	for i < len(ts) && now.Sub(ts[i]) > l.Window {
		i++
	}
	ts = ts[i:]
	if len(ts) == 0 {
		delete(l.fail, key)
	} else {
		l.fail[key] = ts
	}
	return ts
}

// Blocked reports whether another attempt should be refused, and for how long.
func (l *Limiter) Blocked(addr, name string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range []struct {
		key   string
		limit int
	}{{addr + "|" + name, l.PerName}, {addr, l.PerAddress}} {
		if ts := l.recent(k.key, now); len(ts) >= k.limit {
			return true, l.Window - now.Sub(ts[0])
		}
	}
	return false, 0
}

func (l *Limiter) Failed(addr, name string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range []string{addr + "|" + name, addr} {
		l.fail[k] = append(l.recent(k, now), now)
	}
}

func (l *Limiter) Succeeded(addr, name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fail, addr+"|"+name)
}
