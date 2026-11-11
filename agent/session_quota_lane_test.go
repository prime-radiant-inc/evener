package agent

import (
	"testing"

	"primeradiant.com/evener/llm"
)

// A session with a coordinator carries it, its provider instance, and its
// lane into every model-call policy. Delegates get the yielding lane; a nil
// coordinator leaves the policy alone (today's per-session wall budget).
func TestApplyQuotaToPolicy(t *testing.T) {
	coord := llm.NewQuotaCoordinator(t.TempDir())

	root := newSession(t, withConfig(SessionConfig{QuotaCoordinator: coord}))
	profile := root.currentProfile()
	got := root.applyQuotaToPolicy(llm.DefaultRetryPolicy(), profile)
	if got.Quota != coord {
		t.Fatal("root policy did not carry the session's coordinator")
	}
	if got.QuotaKey != profile.ID() {
		t.Fatalf("QuotaKey = %q, want provider instance %q", got.QuotaKey, profile.ID())
	}
	if got.QuotaLane != llm.QuotaLaneRoot {
		t.Fatalf("root lane = %v, want %v", got.QuotaLane, llm.QuotaLaneRoot)
	}

	childCfg := SessionConfig{QuotaCoordinator: coord}
	childCfg.spawn.parentSessionID = "parent-session"
	child := newSession(t, withConfig(childCfg))
	childProfile := child.currentProfile()
	got = child.applyQuotaToPolicy(llm.DefaultRetryPolicy(), childProfile)
	if got.QuotaLane != llm.QuotaLaneDelegate {
		t.Fatalf("delegate lane = %v, want %v", got.QuotaLane, llm.QuotaLaneDelegate)
	}
	if got.QuotaKey != childProfile.ID() {
		t.Fatalf("delegate QuotaKey = %q, want %q", got.QuotaKey, childProfile.ID())
	}

	bare := newSession(t)
	got = bare.applyQuotaToPolicy(llm.DefaultRetryPolicy(), bare.currentProfile())
	if got.Quota != nil || got.QuotaKey != "" {
		t.Fatalf("policy without a session coordinator = %+v, want the per-session wall budget", got)
	}
}
