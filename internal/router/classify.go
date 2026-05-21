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
	ClassOK             ErrorClass = iota // 2xx
	ClassClient                           // 4xx (non-retryable: 400/401/403/404/422/...)
	ClassRateLimit                        // 429 + 408/425
	ClassServer                           // 5xx
	ClassNetwork                          // dial errors, TLS errors, EOF, context deadline
	ClassClientFailover                   // 4xx whose payload signals "try a different target"
	//                                       (e.g. token-limit / context-window rejections).
	//                                       Not retryable on the same target, but the
	//                                       alias loop should advance to the next attempt.
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
	case ClassClientFailover:
		return "client_failover"
	}
	return "unknown"
}

// IsTokenLimitRejection inspects an upstream 4xx body and reports whether the
// rejection is about a request-shape parameter that would succeed on a
// different target — typically max_tokens / max_completion_tokens /
// max_output_tokens exceeding the model's cap, or the prompt exceeding the
// context window. Designed to be tolerant of provider-specific shapes:
// OpenAI-compatible providers (Groq, DeepSeek, xAI, Mistral, NVIDIA NIM,
// OpenRouter, Azure) all surface either an `error.param` field or a message
// containing the offending parameter name / "context window" phrasing.
func IsTokenLimitRejection(status int, body []byte) bool {
	if status < 400 || status >= 500 {
		return false
	}
	if len(body) == 0 {
		return false
	}
	lower := strings.ToLower(string(body))
	tokens := []string{
		`"param":"max_completion_tokens"`,
		`"param":"max_tokens"`,
		`"param":"max_output_tokens"`,
		`"param": "max_completion_tokens"`,
		`"param": "max_tokens"`,
		`"param": "max_output_tokens"`,
		"context_length_exceeded",
		"context window",
		"context_window",
		"maximum context length",
		"maximum value for `max_completion_tokens`",
		"maximum value for `max_tokens`",
		"maximum value for `max_output_tokens`",
	}
	for _, needle := range tokens {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
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
