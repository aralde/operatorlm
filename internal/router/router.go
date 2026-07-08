package router

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aralde/operatorlm/internal/config"
)

// Attempt describes a single concrete upstream call to try.
type Attempt struct {
	Provider      config.Provider // resolved provider
	KeyRef        string          // resolved api_key_ref to use (slot or default)
	KeyName       string          // human-readable: "default" or slot name
	UpstreamModel string          // the actual model name to send upstream
	AliasName     string          // empty if direct slug routing; alias name otherwise
	TargetIdx     int             // index of target within alias (for ratelimit/breaker ID)
	RPM           int             // 0 means no limit
	// MaxOutputTokens, when > 0, caps the request body's max_tokens fields
	// for this attempt. Populated from AliasTarget.MaxOutputTokens; 0 for
	// direct slug routing.
	MaxOutputTokens int
}

// ID is a stable identifier used as key in breaker / rate-limit maps.
func (a Attempt) ID() string {
	if a.AliasName != "" {
		return fmt.Sprintf("alias:%s/#%d", a.AliasName, a.TargetIdx)
	}
	return fmt.Sprintf("slug:%s/%s/%s", a.Provider.Name, a.KeyName, a.UpstreamModel)
}

// Router resolves a request's `model` field to an ordered list of attempts.
type Router struct {
	cfg     *config.Config
	rl      *RateLimiter
	breaker *Breaker

	mu     sync.RWMutex
	policy RetryPolicy
}

func New(cfg *config.Config) *Router {
	r := &Router{cfg: cfg, rl: NewRateLimiter()}
	r.breaker = NewBreaker(BreakerConfig{}) // configured below
	r.Reconfigure(cfg.GetReliability())
	return r
}

// Reconfigure applies a new ReliabilityConfig at runtime.
func (r *Router) Reconfigure(rc config.ReliabilityConfig) {
	rc = rc.WithDefaults()
	r.mu.Lock()
	r.policy = RetryPolicy{
		MaxRetries: rc.MaxRetries,
		BaseMs:     rc.BackoffBaseMs,
		CapMs:      rc.BackoffCapMs,
	}
	r.mu.Unlock()
	r.breaker.SetConfig(BreakerConfig{
		OpenAfterFailures: rc.OpenAfterFailures,
		CooldownRateLimit: time.Duration(rc.CooldownRateLimitMs) * time.Millisecond,
		CooldownServer:    time.Duration(rc.CooldownServerMs) * time.Millisecond,
		CooldownNetwork:   time.Duration(rc.CooldownNetworkMs) * time.Millisecond,
	})
}

func (r *Router) RateLimiter() *RateLimiter { return r.rl }
func (r *Router) Breaker() *Breaker          { return r.breaker }

func (r *Router) Policy() RetryPolicy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.policy
}

// Resolve returns the list of attempts to try for the given model name,
// in priority order (alias-aware). Returns an error if the model doesn't
// match any alias or provider prefix, or if a target's references are broken.
func (r *Router) Resolve(model string) ([]Attempt, error) {
	if a, ok := r.cfg.FindAlias(model); ok {
		if a.Disabled {
			return nil, fmt.Errorf("alias %q is disabled", model)
		}
		return r.resolveAlias(a)
	}
	for _, p := range r.cfg.Snapshot() {
		if p.Disabled {
			continue
		}
		if p.Prefix == "" || !strings.HasPrefix(model, p.Prefix) {
			continue
		}
		upstream := strings.TrimPrefix(model, p.Prefix)
		if len(p.Models) > 0 && !contains(p.Models, upstream) {
			return nil, fmt.Errorf("model %q not enabled for provider %q", upstream, p.Name)
		}
		return []Attempt{{
			Provider:      p,
			KeyRef:        p.APIKeyRef,
			KeyName:       "default",
			UpstreamModel: upstream,
		}}, nil
	}

	// Built-in local engine: models live on disk (not in [[providers]]), so
	// resolve the local prefix directly. The engine validates that the model
	// file exists when it tries to start.
	if lm := r.cfg.GetLocalModels(); lm.Enabled && lm.Prefix != "" && strings.HasPrefix(model, lm.Prefix) {
		return []Attempt{{
			Provider: config.Provider{
				Name:   "local",
				Type:   "llamacpp",
				Prefix: lm.Prefix,
			},
			KeyName:       "default",
			UpstreamModel: strings.TrimPrefix(model, lm.Prefix),
		}}, nil
	}

	return nil, fmt.Errorf("no provider or alias matches model %q", model)
}

func (r *Router) resolveAlias(a config.Alias) ([]Attempt, error) {
	if len(a.Targets) == 0 {
		return nil, fmt.Errorf("alias %q has no targets", a.Name)
	}
	type withIdx struct {
		t   config.AliasTarget
		idx int
	}
	all := make([]withIdx, 0, len(a.Targets))
	for i, t := range a.Targets {
		all = append(all, withIdx{t, i})
	}

	strat := a.Strategy
	if strat == "" {
		strat = "order"
	}
	switch strat {
	case "order":
		sort.SliceStable(all, func(i, j int) bool {
			if all[i].t.Order != all[j].t.Order {
				return all[i].t.Order < all[j].t.Order
			}
			return all[i].idx < all[j].idx
		})
	default:
		return nil, fmt.Errorf("unsupported strategy %q", strat)
	}

	out := make([]Attempt, 0, len(all))
	for _, w := range all {
		t := w.t
		p, ok := r.cfg.FindProvider(t.Provider)
		if !ok {
			return nil, fmt.Errorf("alias %q target #%d: provider %q not found", a.Name, w.idx, t.Provider)
		}
		keyRef := p.KeyRef(t.Key)
		if keyRef == "" && p.Type != "llama-server" && p.Type != "llamacpp" {
			return nil, fmt.Errorf("alias %q target #%d: key %q not found in provider %q", a.Name, w.idx, t.Key, t.Provider)
		}
		keyName := t.Key
		if keyName == "" {
			keyName = "default"
		}
		out = append(out, Attempt{
			Provider:        p,
			KeyRef:          keyRef,
			KeyName:         keyName,
			UpstreamModel:   t.UpstreamModel,
			AliasName:       a.Name,
			TargetIdx:       w.idx,
			RPM:             t.RPM,
			MaxOutputTokens: t.MaxOutputTokens,
		})
	}
	_ = rand.Intn
	return out, nil
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
