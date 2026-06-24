package agent

import "testing"

// TestSessionConfigDefaultSubagentDepth pins the default subagent depth at 2: a
// root session derives its delegation allowance from MaxSubagentDepth, so the
// default of 2 lets a delegate itself delegate one level (grant allowance 1).
func TestSessionConfigDefaultSubagentDepth(t *testing.T) {
	t.Parallel()
	var c SessionConfig
	c.applyDefaults()
	if c.MaxSubagentDepth != 2 {
		t.Fatalf("default MaxSubagentDepth = %d, want 2", c.MaxSubagentDepth)
	}

	// An explicit value is preserved, not overwritten by the default.
	explicit := SessionConfig{MaxSubagentDepth: 5}
	explicit.applyDefaults()
	if explicit.MaxSubagentDepth != 5 {
		t.Fatalf("explicit MaxSubagentDepth = %d, want 5 preserved", explicit.MaxSubagentDepth)
	}
}
