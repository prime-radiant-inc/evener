package agent

import (
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// TestDelegationAllowancePersistsAcrossResume verifies that a delegate's
// delegation_allowance survives the production restore-from-descriptor path:
// a terminal delegate whose runtime was lost is reconstructed from its durable
// DelegateRestoreDescriptor.
//
// The persistence path under test:
// DelegateRestoreDescriptor.DelegationAllowance (durable on disk in jobs.jsonl)
// → spawnConfig.delegationAllowance (job_delegate.go restoreTerminalDelegateChild)
// → s.delegationAllowance (session_init.go).
//
// This drives the real reconstruction code, not a manual copy: it seeds a
// stopped delegate record carrying allowance > 0, persists it to disk, reloads
// it, then reconstructs the child via restoreTerminalDelegateChild and asserts
// the resumed session received the granted allowance. The test fails if the
// descriptor stops carrying allowance into the reconstructed session.
func TestDelegationAllowancePersistsAcrossResume(t *testing.T) {
	t.Parallel()
	const grantedAllowance = 1

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	s := newDelegateRestorePreflightSession(t, c)

	// Seed a terminal (runtime-lost) delegate whose durable restore descriptor
	// carries a non-zero allowance, then persist that descriptor to disk.
	rec := seedStoppedDelegateRestoreRecord(t, s)
	rec.DelegateRestore.DelegationAllowance = grantedAllowance
	replaceStoredDelegateRecord(t, s, rec)
	markStoredDelegateResumable(t, s, rec)

	// Reload from disk and confirm the durable descriptor carries the allowance.
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	if got := rec.DelegateRestore.DelegationAllowance; got != grantedAllowance {
		t.Fatalf("durable descriptor DelegationAllowance = %d, want %d", got, grantedAllowance)
	}
	childID := rec.DelegateRestore.ChildSessionID

	// Reconstruct the child through the production restore path. This reads
	// desc.DelegationAllowance into the child's spawnConfig and onto the
	// reconstructed session.
	preflight := requireDelegateRestorePreflight(t, s, rec)
	sub, err := s.restoreTerminalDelegateChild(rec, childID, preflight)
	if err != nil {
		t.Fatalf("restoreTerminalDelegateChild: %v", err)
	}
	if sub == nil || sub.sess == nil {
		t.Fatalf("reconstructed child = %+v, want retained runtime", sub)
	}

	if got, want := sub.sess.delegationAllowance, grantedAllowance; got != want {
		t.Fatalf("reconstructed session delegationAllowance = %d, want %d", got, want)
	}
}

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
			name:             "explicit 2 gives 2",
			maxSubagentDepth: 2,
			wantAllowance:    2,
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
