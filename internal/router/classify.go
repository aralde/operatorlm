package router

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrorClass categorizes failures so the breaker and retry policy can react
// differently. The class drives both the cooldown duration and whether a
// retry should be attempted.
type ErrorClass int

const (
	ClassOK           ErrorClass = iota // 2xx
	ClassClient                         // 4xx (non-retryable: 400/401/403/404/422/...)
	ClassRateLimit                      // 429 + 408/425
	ClassServer                         // 5xx
	ClassNetwork                        // dial errors, TLS errors, EOF, context deadline
)

func (c ErrorClass) String() string {
	switch c {
	case ClassOK:
		return "ok"
	case ClassClient:
		return "client"
	case ClassRateLimit:
		return "ratelimit"
	case ClassServer:
		return "server"
	case ClassNetwork:
		return "network"
	}
	return "unknown"
}

// Retryable returns true when this class warrants a retry on the same target.
// Client (4xx) errors are NOT retryable since the request itself is invalid.
func (c ErrorClass) Retryable() bool {
	return c == ClassRateLimit || c == ClassServer || c == ClassNetwork
}

// ClassifyStatus maps an HTTP status to a class.
func ClassifyStatus(status int) ErrorClass {
	switch {
	case status >= 200 && status < 400:
		return ClassOK
	case status == http.StatusRequestTimeout, status == http.StatusTooManyRequests, status == 425:
		return ClassRateLimit
	case status >= 500:
		return ClassServer
	case status >= 400:
		return ClassClient
	}
	return ClassNetwork
}

// ClassifyError maps a transport-level error to a class.
func ClassifyError(err error) ErrorClass {
	if err == nil {
		return ClassOK
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ClassNetwork
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return ClassNetwork
	}
	return ClassNetwork
}

// ParseRetryAfter returns the Duration encoded by a Retry-After response header,
// supporting both delta-seconds and HTTP-date forms. Returns 0 if absent or invalid.
func ParseRetryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}
	return 0
}
