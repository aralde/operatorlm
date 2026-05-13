package router

import (
	"math/rand"
	"time"
)

// RetryPolicy controls how many times a single target is retried before the
// router moves on to the next one. Delays use full jitter on top of an
// exponential ceiling — see https://aws.amazon.com/blogs/architecture/exponential-backoff-and-jitter/
type RetryPolicy struct {
	MaxRetries int
	BaseMs     int
	CapMs      int
}

// Delay returns the wait time for the n-th retry (0-indexed). When the upstream
// signaled Retry-After, that value takes precedence — adding jitter on top so
// concurrent clients don't unblock at the exact same instant.
func (p RetryPolicy) Delay(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter + time.Duration(rand.Intn(250))*time.Millisecond
	}
	if p.BaseMs <= 0 {
		p.BaseMs = 500
	}
	if p.CapMs <= 0 {
		p.CapMs = 10000
	}
	exp := p.BaseMs << attempt
	if exp <= 0 || exp > p.CapMs {
		exp = p.CapMs
	}
	jittered := rand.Intn(exp + 1)
	return time.Duration(jittered) * time.Millisecond
}
