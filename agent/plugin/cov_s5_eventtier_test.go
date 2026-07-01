package plugin

import "testing"

// EventTier labels events serf fires as claude-compatible-subset and unknown
// events as the empty string.
func TestCov_EventTier(t *testing.T) {
	if got := EventTier(HookPreToolUse); got != "claude-compatible-subset" {
		t.Errorf("PreToolUse tier = %q, want claude-compatible-subset", got)
	}
	if got := EventTier(HookEvent("NotARealEvent")); got != "" {
		t.Errorf("unknown event tier = %q, want empty", got)
	}
}
