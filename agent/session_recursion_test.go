package agent

import (
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestRootAllowanceFromConfig verifies that a root session derives its
// delegationAllowance from MaxSubagentDepth (spec §1: "Root allowance = config").
// Zero MaxSubagentDepth defaults to 1.
//
// Red today: the delegationAllowance field does not exist.
func TestRootAllowanceFromConfig(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()

	tests := []struct {
		name             string
		maxSubagentDepth int
		wantAllowance    int
	}{
		{
			name:             "default (zero) gives 2",
			maxSubagentDepth: 0,
			wantAllowance:    2,
		},
		{
			name:             "explicit 1 gives 1",
			maxSubagentDepth: 1,
			wantAllowance:    1,
		},
		{
			name:             "explicit 2 gives 2",
			maxSubagentDepth: 2,
			wantAllowance:    2,
		},
		{
			name:             "explicit 3 gives 3",
			maxSubagentDepth: 3,
			wantAllowance:    3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
				MaxSubagentDepth: tc.maxSubagentDepth,
				NoProjectPrompts: true,
			})
			if err != nil {
				t.Fatalf("NewSession: %v", err)
			}
			defer sess.Close()

			if got := sess.delegationAllowance; got != tc.wantAllowance {
				t.Fatalf("root delegationAllowance = %d, want %d (MaxSubagentDepth=%d)", got, tc.wantAllowance, tc.maxSubagentDepth)
			}
		})
	}
}

// TestRestoredRootAllowanceFromConfig verifies that a RESUMED root session
// derives its delegationAllowance from MaxSubagentDepth, exactly like a fresh
// root (spec §1: "Root allowance = config"). Zero MaxSubagentDepth defaults to 2.
//
// Red today: RestoreSessionFromMetaWithConfig sets delegationAllowance from the
// zero-valued spawn carrier, so a restored root gets allowance 0 and every
// delegate spawn is rejected by the allowance gate.
func TestRestoredRootAllowanceFromConfig(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	tests := []struct {
		name             string
		maxSubagentDepth int
		wantAllowance    int
	}{
		{
			name:             "default (zero) gives 2",
			maxSubagentDepth: 0,
			wantAllowance:    2,
		},
		{
			name:             "explicit 2 gives 2",
			maxSubagentDepth: 2,
			wantAllowance:    2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			meta := schema.SessionMeta{
				ID:        "01TESTRESTOREROOT",
				ProfileID: "openai",
				Model:     "gpt-5.2",
				Config:    (SessionConfig{MaxSubagentDepth: tc.maxSubagentDepth, NoProjectPrompts: true}).toSnapshot(),
			}
			sess, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{StateDir: t.TempDir()})
			if err != nil {
				t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
			}
			defer sess.Close()

			if got := sess.delegationAllowance; got != tc.wantAllowance {
				t.Fatalf("restored root delegationAllowance = %d, want %d (MaxSubagentDepth=%d)", got, tc.wantAllowance, tc.maxSubagentDepth)
			}
		})
	}
}
