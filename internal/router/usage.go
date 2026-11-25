package router

import (
	"sort"
	"sync"
)

// TargetUsage is the accumulated token count for one attempt ID.
type TargetUsage struct {
	ID               string `json:"id"`
	Alias            string `json:"alias"`
	Provider         string `json:"provider"`
	Key              string `json:"key"`
	UpstreamModel    string `json:"upstream_model"`
	Requests         int64  `json:"requests"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
}

// AliasUsage sums TargetUsage over every target of one alias. Direct slug
// routing is grouped under an empty alias name.
type AliasUsage struct {
	Alias            string `json:"alias"`
	Requests         int64  `json:"requests"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
}

type UsageSnapshot struct {
	Aliases []AliasUsage  `json:"aliases"`
	Targets []TargetUsage `json:"targets"`
}

// Usage keeps in-memory token counters per attempt. Nothing is persisted.
type Usage struct {
	mu      sync.Mutex
	targets map[string]*TargetUsage
}

func NewUsage() *Usage {
	return &Usage{targets: map[string]*TargetUsage{}}
}

func (u *Usage) Record(att Attempt, prompt, completion int64) {
	id := att.ID()
	u.mu.Lock()
	defer u.mu.Unlock()
	t, ok := u.targets[id]
	if !ok {
		t = &TargetUsage{
			ID:            id,
			Alias:         att.AliasName,
			Provider:      att.Provider.Name,
			Key:           att.KeyName,
			UpstreamModel: att.UpstreamModel,
		}
		u.targets[id] = t
	}
	t.Requests++
	t.PromptTokens += prompt
	t.CompletionTokens += completion
}

func (u *Usage) Snapshot() UsageSnapshot {
	u.mu.Lock()
	defer u.mu.Unlock()
	snap := UsageSnapshot{Aliases: []AliasUsage{}, Targets: make([]TargetUsage, 0, len(u.targets))}
	byAlias := map[string]*AliasUsage{}
	for _, t := range u.targets {
		snap.Targets = append(snap.Targets, *t)
		a, ok := byAlias[t.Alias]
		if !ok {
			a = &AliasUsage{Alias: t.Alias}
			byAlias[t.Alias] = a
		}
		a.Requests += t.Requests
		a.PromptTokens += t.PromptTokens
		a.CompletionTokens += t.CompletionTokens
	}
	for _, a := range byAlias {
		snap.Aliases = append(snap.Aliases, *a)
	}
	sort.Slice(snap.Aliases, func(i, j int) bool { return snap.Aliases[i].Alias < snap.Aliases[j].Alias })
	sort.Slice(snap.Targets, func(i, j int) bool { return snap.Targets[i].ID < snap.Targets[j].ID })
	return snap
}
