package router

import (
	"sync"
	"time"
)

// State of a circuit for a single target.
type BreakerState int

const (
	StateClosed   BreakerState = iota // requests flow normally
	StateOpen                         // requests are blocked until cooldown elapses
	StateHalfOpen                     // a single probe is allowed
)

func (s BreakerState) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	}
	return "unknown"
}

// BreakerConfig controls when a target trips and how long it stays open.
type BreakerConfig struct {
	OpenAfterFailures   int           // consecutive failures before tripping closed → open
	CooldownRateLimit   time.Duration // applied for ClassRateLimit
	CooldownServer      time.Duration // applied for ClassServer
	CooldownNetwork     time.Duration // applied for ClassNetwork
}

// Breaker is a per-target circuit breaker with three states (closed / open /
// half-open). Open cooldown depends on the failure class — rate-limit responses
// recover faster than network outages. While half-open, only one probe at a time
// is allowed; the outcome decides whether to fully close or fall back to open.
type Breaker struct {
	mu     sync.Mutex
	cfg    BreakerConfig
	states map[string]*targetState
}

type targetState struct {
	state            BreakerState
	consecutiveFails int
	openedAt         time.Time
	cooldown         time.Duration
	lastError        string
	lastClass        ErrorClass
	halfOpenInflight bool
}

// HealthSnapshot is a JSON-friendly view of a target's current breaker state.
type HealthSnapshot struct {
	ID               string  `json:"id"`
	State            string  `json:"state"`
	ConsecutiveFails int     `json:"consecutive_failures"`
	OpenedAt         *string `json:"opened_at,omitempty"`
	CooldownEndsAt   *string `json:"cooldown_ends_at,omitempty"`
	CooldownMs       int64   `json:"cooldown_ms,omitempty"`
	LastError        string  `json:"last_error,omitempty"`
	LastClass        string  `json:"last_class,omitempty"`
}

func NewBreaker(cfg BreakerConfig) *Breaker {
	return &Breaker{cfg: cfg, states: make(map[string]*targetState)}
}

// SetConfig swaps the breaker configuration. Existing target states are kept;
// future cooldowns will use the new durations.
func (b *Breaker) SetConfig(cfg BreakerConfig) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cfg = cfg
}

func (b *Breaker) getOrInit(id string) *targetState {
	st, ok := b.states[id]
	if !ok {
		st = &targetState{state: StateClosed}
		b.states[id] = st
	}
	return st
}

// Allow returns true if a request to the target may proceed. When the breaker
// transitions to half-open, the first caller is granted the probe slot; further
// callers are denied until that probe records its outcome.
func (b *Breaker) Allow(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.getOrInit(id)
	switch st.state {
	case StateClosed:
		return true
	case StateOpen:
		if time.Since(st.openedAt) >= st.cooldown {
			st.state = StateHalfOpen
			st.halfOpenInflight = true
			return true
		}
		return false
	case StateHalfOpen:
		if st.halfOpenInflight {
			return false
		}
		st.halfOpenInflight = true
		return true
	}
	return false
}

// Record reports the outcome of a request previously allowed via Allow.
// Successful outcomes close the circuit; transient failures (5xx/429/network)
// trip or extend it. Client (4xx) errors don't affect circuit state.
func (b *Breaker) Record(id string, class ErrorClass, errMsg string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.getOrInit(id)
	st.halfOpenInflight = false
	st.lastClass = class
	st.lastError = errMsg

	if class == ClassOK {
		st.state = StateClosed
		st.consecutiveFails = 0
		st.openedAt = time.Time{}
		st.cooldown = 0
		st.lastError = ""
		return
	}
	if class == ClassClient || class == ClassClientFailover {
		// Don't trip circuit; the user's request is invalid (or shape-incompatible
		// with this target), the upstream itself is fine.
		return
	}

	st.consecutiveFails++
	cooldown := b.cooldownFor(class)
	if st.state == StateHalfOpen || st.consecutiveFails >= b.cfg.OpenAfterFailures {
		st.state = StateOpen
		st.openedAt = time.Now()
		st.cooldown = cooldown
	}
}

// MarkCooldown forces a target into open state for the configured cooldown of
// the given class. Used when an external signal (e.g., Retry-After header)
// dictates a specific wait duration.
func (b *Breaker) MarkCooldownFor(id string, class ErrorClass, override time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.getOrInit(id)
	st.halfOpenInflight = false
	st.state = StateOpen
	st.openedAt = time.Now()
	if override > 0 {
		st.cooldown = override
	} else {
		st.cooldown = b.cooldownFor(class)
	}
	st.lastClass = class
}

func (b *Breaker) cooldownFor(class ErrorClass) time.Duration {
	switch class {
	case ClassRateLimit:
		return b.cfg.CooldownRateLimit
	case ClassServer:
		return b.cfg.CooldownServer
	case ClassNetwork:
		return b.cfg.CooldownNetwork
	}
	return b.cfg.CooldownServer
}

// Snapshot returns the current state of every tracked target, ordered by ID.
func (b *Breaker) Snapshot() []HealthSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]HealthSnapshot, 0, len(b.states))
	for id, st := range b.states {
		snap := HealthSnapshot{
			ID:               id,
			State:            st.state.String(),
			ConsecutiveFails: st.consecutiveFails,
			LastError:        st.lastError,
		}
		if st.lastClass != ClassOK {
			snap.LastClass = st.lastClass.String()
		}
		if !st.openedAt.IsZero() {
			t := st.openedAt.UTC().Format(time.RFC3339)
			snap.OpenedAt = &t
			ends := st.openedAt.Add(st.cooldown).UTC().Format(time.RFC3339)
			snap.CooldownEndsAt = &ends
			snap.CooldownMs = st.cooldown.Milliseconds()
		}
		out = append(out, snap)
	}
	return out
}

// Clear forces a target back to closed (e.g., after manual intervention).
func (b *Breaker) Clear(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.states, id)
}
