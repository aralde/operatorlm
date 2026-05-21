package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/aralde/operatorlm/internal/audit"
	"github.com/aralde/operatorlm/internal/providers"
	"github.com/aralde/operatorlm/internal/router"
)

// httpClient is the long-lived client used for all upstream requests.
// Connect / TLS / response-header timeouts are set on the Transport;
// the per-attempt total timeout is enforced via context.WithTimeout in dispatch.
var httpClient = &http.Client{
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
	},
}

// captureWriter mirrors response bytes into a bounded buffer so the audit log
// can see what the client received without blocking the streaming pipeline.
type captureWriter struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
	cap    int
}

func (c *captureWriter) WriteHeader(status int) {
	c.status = status
	c.ResponseWriter.WriteHeader(status)
}

func (c *captureWriter) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if remaining := c.cap - c.buf.Len(); remaining > 0 {
		n := len(p)
		if n > remaining {
			n = remaining
		}
		c.buf.Write(p[:n])
	}
	return c.ResponseWriter.Write(p)
}

func (c *captureWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// dispatch is the request entry point for both /v1/chat/completions and
// /v1/images/generations. It coordinates resolve → retry-loop → fallback-alias
// → audit-record. The control flow:
//
//  1. Resolve the model to a list of attempts (alias router or single slug).
//  2. For each attempt, ask the breaker if it's currently allowed; if not, skip.
//  3. Enforce the per-target rpm; if exceeded, skip.
//  4. Run the attempt with retries (exponential backoff + jitter).
//  5. On success, write the response and return.
//  6. On non-retryable client errors (4xx), return as-is — no fallback.
//  7. When all attempts are exhausted, jump to the configured default fallback
//     alias (once) before giving up.
//
// Streaming caveat: retry/fallback can only happen *before* the first byte has
// been flushed to the client. Once we start writing chunks, any subsequent
// upstream error is propagated as a stream cut.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, kind providers.Kind, body []byte, model string, stream bool) {
	rc := s.cfg.GetReliability()

	// Total request budget across retries and fallbacks.
	ctx := r.Context()
	if rc.TotalTimeoutMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(rc.TotalTimeoutMs)*time.Millisecond)
		defer cancel()
	}

	start := time.Now()
	rec := s.beginRecord(r, body, model, stream)

	var cw *captureWriter
	respWriter := w
	if s.audit != nil && s.audit.Enabled() {
		cw = &captureWriter{ResponseWriter: w, cap: s.audit.MaxResponseBody()}
		respWriter = cw
	}

	finish := func(status int, errMsg string, attemptID string, upstream *audit.UpstreamInfo) {
		if rec == nil {
			return
		}
		rec.Status = status
		rec.Error = errMsg
		rec.Attempt = attemptID
		rec.Upstream = upstream
		rec.DurationMs = time.Since(start).Milliseconds()
		if cw != nil {
			rec.ResponseHeaders = headerMap(cw.Header(), s.audit.Redact())
			rec.ResponseBody = audit.TruncateJSON(cw.buf.Bytes(), s.audit.MaxResponseBody())
		}
		s.audit.Write(*rec)
	}

	tried := map[string]bool{} // visited alias names to avoid recursive fallback loops
	tried[model] = true

	status, errMsg, attemptID, upstream, ok := s.tryDispatch(ctx, respWriter, r, kind, body, model, stream, tried)
	if !ok && rc.DefaultFallbackAlias != "" && !tried[rc.DefaultFallbackAlias] {
		log.Printf("dispatch model=%s exhausted, falling back to alias=%s", model, rc.DefaultFallbackAlias)
		status, errMsg, attemptID, upstream, ok = s.tryDispatch(ctx, respWriter, r, kind, body, rc.DefaultFallbackAlias, stream, tried)
	}
	if !ok {
		http.Error(respWriter, errMsg, status)
	}
	finish(status, errMsg, attemptID, upstream)
}

// tryDispatch resolves and runs attempts for a single model identifier.
// Returns ok=true when a response was written successfully (any 2xx) or a
// non-retryable error was forwarded (4xx). Returns ok=false if all attempts
// were exhausted with retryable failures.
func (s *Server) tryDispatch(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	kind providers.Kind,
	body []byte,
	model string,
	stream bool,
	tried map[string]bool,
) (status int, errMsg string, attemptID string, upstream *audit.UpstreamInfo, ok bool) {
	tried[model] = true
	policy := s.rt.Policy()

	attempts, err := s.rt.Resolve(model)
	if err != nil {
		return http.StatusBadRequest, err.Error(), "", nil, false
	}

	var lastStatus int
	var lastBody []byte
	var lastID string
	var lastUpstream *audit.UpstreamInfo

	for _, att := range attempts {
		if !s.rt.Breaker().Allow(att.ID()) {
			log.Printf("dispatch model=%s attempt=%s skipped: breaker open", model, att.ID())
			continue
		}
		if att.RPM > 0 && !s.rt.RateLimiter().Allow(att.ID(), att.RPM) {
			log.Printf("dispatch model=%s attempt=%s skipped: rpm exceeded", model, att.ID())
			s.rt.Breaker().Record(att.ID(), router.ClassOK, "") // release the half-open slot if any
			continue
		}

		prov, regOK := s.reg.ByName(att.Provider.Name)
		if !regOK {
			s.rt.Breaker().Record(att.ID(), router.ClassOK, "")
			lastStatus = http.StatusInternalServerError
			lastBody = []byte(fmt.Sprintf("provider %q not in registry", att.Provider.Name))
			lastID = att.ID()
			continue
		}

		// Retry loop on this single target ----------------------------
		var (
			retryAfter time.Duration
			tStatus    int
			tBody      []byte
			tClass     router.ErrorClass
			tUpstream  *audit.UpstreamInfo
			delivered  bool
		)
		for try := 0; try <= policy.MaxRetries; try++ {
			if try > 0 {
				delay := policy.Delay(try-1, retryAfter)
				log.Printf("dispatch model=%s attempt=%s retry=%d delay=%s", model, att.ID(), try, delay)
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					tStatus = http.StatusGatewayTimeout
					tBody = []byte(ctx.Err().Error())
					tClass = router.ClassNetwork
					goto endTries
				}
			}
			retryAfter = 0

			tStatus, tBody, tClass, tUpstream, delivered = s.runOnce(ctx, w, r, prov, kind, body, att, stream)
			if delivered {
				// Final answer (2xx success or 4xx non-retryable).
				s.rt.Breaker().Record(att.ID(), tClass, "")
				if tClass == router.ClassOK {
					s.rt.RateLimiter().Record(att.ID())
				}
				return tStatus, "", att.ID(), tUpstream, true
			}
			if !tClass.Retryable() {
				goto endTries
			}
			// extract Retry-After if present in upstream response (encoded in tBody header? not yet)
		}
	endTries:
		s.rt.Breaker().Record(att.ID(), tClass, string(tBody))
		lastStatus = tStatus
		lastBody = tBody
		lastID = att.ID()
		lastUpstream = tUpstream
	}

	if lastStatus == 0 {
		lastStatus = http.StatusBadGateway
		lastBody = []byte("all attempts failed")
	}
	return lastStatus, fmt.Sprintf("all attempts failed; last status %d: %s", lastStatus, string(lastBody)), lastID, lastUpstream, false
}

// runOnce performs a single upstream request with a per-attempt timeout.
// `delivered` reports whether the response was fully written to the client
// (success or non-retryable failure). When delivered=false, the caller may
// retry on the same target (if class.Retryable) or move to the next attempt.
func (s *Server) runOnce(
	ctx context.Context,
	w http.ResponseWriter,
	_ *http.Request,
	prov providers.Provider,
	kind providers.Kind,
	body []byte,
	att router.Attempt,
	stream bool,
) (status int, errBody []byte, class router.ErrorClass, upstream *audit.UpstreamInfo, delivered bool) {
	rcCfg := s.cfg.GetReliability()
	attemptCtx := ctx
	if rcCfg.PerAttemptTimeoutMs > 0 {
		var cancel context.CancelFunc
		attemptCtx, cancel = context.WithTimeout(ctx, time.Duration(rcCfg.PerAttemptTimeoutMs)*time.Millisecond)
		defer cancel()
	}

	effectiveBody := body
	if att.MaxOutputTokens > 0 {
		effectiveBody = providers.ClampOutputTokens(body, att.MaxOutputTokens)
	}
	req, err := prov.BuildRequest(attemptCtx, kind, effectiveBody, att, stream)
	if err != nil {
		return http.StatusInternalServerError, []byte(err.Error()), router.ClassClient, nil, false
	}
	upstream = s.upstreamInfo(req)

	resp, err := httpClient.Do(req)
	if err != nil {
		return http.StatusBadGateway, []byte(err.Error()), router.ClassifyError(err), upstream, false
	}
	defer resp.Body.Close()

	class = router.ClassifyStatus(resp.StatusCode)
	if upstream != nil {
		upstream.Status = resp.StatusCode
	}

	if class != router.ClassOK && class != router.ClassClient {
		// Retryable. Drain body and signal not-delivered.
		b, _ := io.ReadAll(resp.Body)
		log.Printf("dispatch model=? attempt=%s status=%d class=%s retryable", att.ID(), resp.StatusCode, class)
		return resp.StatusCode, b, class, upstream, false
	}

	log.Printf("dispatch attempt=%s status=%d class=%s", att.ID(), resp.StatusCode, class)
	if resp.StatusCode >= 400 {
		// Read body once: we may either forward it (true client error) or
		// reclassify it as "try the next target" if it's a token-limit /
		// context-window rejection.
		b, _ := io.ReadAll(resp.Body)
		if router.IsTokenLimitRejection(resp.StatusCode, b) {
			log.Printf("dispatch attempt=%s status=%d reclassified as client_failover", att.ID(), resp.StatusCode)
			return resp.StatusCode, b, router.ClassClientFailover, upstream, false
		}
		http.Error(w, string(b), resp.StatusCode)
		return resp.StatusCode, b, class, upstream, true
	}

	if writeErr := prov.WriteResponse(w, resp, att.UpstreamModel, stream); writeErr != nil {
		log.Printf("dispatch attempt=%s write error: %v", att.ID(), writeErr)
	}
	return resp.StatusCode, nil, router.ClassOK, upstream, true
}

// --- audit helpers ---

func (s *Server) auditEnabled() bool {
	return s.audit != nil && s.audit.Enabled()
}

func (s *Server) beginRecord(r *http.Request, body []byte, model string, stream bool) *audit.Record {
	if !s.auditEnabled() {
		return nil
	}
	redact := s.audit.Redact()
	maxReq := s.audit.MaxRequestBody()
	rec := &audit.Record{
		ID:             newID(),
		Timestamp:      time.Now().UTC(),
		Client:         r.RemoteAddr,
		UserAgent:      r.Header.Get("User-Agent"),
		Method:         r.Method,
		Path:           r.URL.Path,
		Model:          model,
		Stream:         stream,
		RequestHeaders: headerMap(r.Header, redact),
		RequestBody:    audit.TruncateJSON(body, maxReq),
	}
	return rec
}

func (s *Server) upstreamInfo(req *http.Request) *audit.UpstreamInfo {
	if !s.auditEnabled() {
		return nil
	}
	return &audit.UpstreamInfo{
		URL:     audit.RedactURL(req.URL.String(), s.audit.Redact()),
		Headers: headerMap(req.Header, s.audit.Redact()),
	}
}

func headerMap(h http.Header, redact bool) map[string]string {
	return audit.RedactHeaders(h, redact)
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// extractRequestMeta parses the OpenAI request body for the model and stream flag.
func extractRequestMeta(body []byte) (model string, stream bool, err error) {
	var env struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return "", false, err
	}
	return env.Model, env.Stream, nil
}
