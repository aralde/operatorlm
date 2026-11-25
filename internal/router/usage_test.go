package router

import (
	"testing"

	"github.com/aralde/operatorlm/internal/config"
)

func TestUsageGroupsTargetsByAlias(t *testing.T) {
	u := NewUsage()
	p := config.Provider{Name: "groq"}
	a0 := Attempt{Provider: p, KeyName: "default", UpstreamModel: "llama", AliasName: "fast", TargetIdx: 0}
	a1 := Attempt{Provider: config.Provider{Name: "openrouter"}, KeyName: "work", UpstreamModel: "llama", AliasName: "fast", TargetIdx: 1}
	direct := Attempt{Provider: p, KeyName: "default", UpstreamModel: "llama"}

	u.Record(a0, 10, 2)
	u.Record(a0, 5, 1)
	u.Record(a1, 100, 20)
	u.Record(direct, 1, 1)

	snap := u.Snapshot()
	if len(snap.Aliases) != 2 {
		t.Fatalf("aliases: %+v", snap.Aliases)
	}
	if d := snap.Aliases[0]; d.Alias != "" || d.Requests != 1 || d.PromptTokens != 1 {
		t.Fatalf("direct: %+v", d)
	}
	if f := snap.Aliases[1]; f.Alias != "fast" || f.Requests != 3 || f.PromptTokens != 115 || f.CompletionTokens != 23 {
		t.Fatalf("fast: %+v", f)
	}
	if len(snap.Targets) != 3 {
		t.Fatalf("targets: %+v", snap.Targets)
	}
	if t0 := snap.Targets[0]; t0.ID != "alias:fast/#0" || t0.Requests != 2 || t0.PromptTokens != 15 || t0.CompletionTokens != 3 {
		t.Fatalf("target 0: %+v", t0)
	}
	if t1 := snap.Targets[1]; t1.Key != "work" || t1.Provider != "openrouter" {
		t.Fatalf("target 1: %+v", t1)
	}
}
