package router

import (
	"sync"
	"time"
)

// RateLimiter tracks request timestamps per attempt-ID over a sliding 60s window.
type RateLimiter struct {
	mu      sync.Mutex
	windows map[string][]time.Time
	window  time.Duration
}

func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		windows: make(map[string][]time.Time),
		window:  60 * time.Second,
	}
}

// Allow checks whether a new request would fit under the given rpm limit
// without recording it. Use Record after a successful upstream call.
// limit <= 0 means unlimited.
func (r *RateLimiter) Allow(id string, limit int) bool {
	if limit <= 0 {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evictLocked(id)
	return len(r.windows[id]) < limit
}

// Record adds a timestamp for this attempt.
func (r *RateLimiter) Record(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.windows[id] = append(r.windows[id], time.Now())
}

func (r *RateLimiter) evictLocked(id string) {
	cutoff := time.Now().Add(-r.window)
	stamps := r.windows[id]
	i := 0
	for ; i < len(stamps); i++ {
		if stamps[i].After(cutoff) {
			break
		}
	}
	if i > 0 {
		r.windows[id] = stamps[i:]
	}
}
