package agent

import (
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// TestDelegationAllowancePersistsAcrossResume verifies that the
// delegation_allowance value set on a child's spawnConfig is written to the
// transcript header and survives a restore round-trip.
//
// The persistence path: spawnConfig.delegationAllowance → Header.DelegationAllowance
// (written at NewSession) → RestoreSessionConfig.spawn.delegationAllowance (injected
// from the header in the restore path) → s.delegationAllowance.
//
// Red today: neither the field nor the header slot exist.
func TestDelegationAllowancePersistsAcrossResume(t *testing.T) {
	stateDir := t.TempDir()
	workDir := t.TempDir()
	c := llm.NewClient()

	// Build a child session with delegation_allowance = 2 via spawnConfig.
	cfg := SessionConfig{
		StateDir:         stateDir,
		NoProjectPrompts: true,
	}
	cfg.spawn.depth = 1
	cfg.spawn.parentSessionID = "parent-session"
	cfg.spawn.delegationAllowance = 2

	child, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	childID := child.ID()

	// Flush and close the transcript so it's persisted to disk.
	if child.transcript != nil {
		if err := child.transcript.Close(); err != nil {
			t.Fatalf("close child transcript: %v", err)
		}
		child.transcript = nil
	}
	child.Close()

	// Read the transcript header back and verify the allowance was written.
	tpath := filepath.Join(stateDir, sessionsSubdir, childID+".transcript.jsonl")
	header, _, _, readErr := readTranscript(tpath)
	if readErr != nil {
		t.Fatalf("readTranscript: %v", readErr)
	}
	if got, want := header.DelegationAllowance, 2; got != want {
		t.Fatalf("transcript header DelegationAllowance = %d, want %d", got, want)
	}

	// Restore from the persisted session, injecting the allowance (simulating
	// what job_delegate.go does after reading the transcript header).
	meta, err := schema.LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	restoreCfg := RestoreSessionConfig{
		StateDir: stateDir,
		spawn: spawnConfig{
			depth:               1,
			parentSessionID:     "parent-session",
			delegationAllowance: header.DelegationAllowance,
		},
	}
	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), meta, restoreCfg)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer restored.Close()

	if got, want := restored.delegationAllowance, 2; got != want {
		t.Fatalf("restored session delegationAllowance = %d, want %d", got, want)
	}
}

// TestRootAllowanceFromConfig verifies that a root session derives its
// delegationAllowance from MaxSubagentDepth (spec §1: "Root allowance = config").
// Zero MaxSubagentDepth defaults to 1.
//
// Red today: the delegationAllowance field does not exist.
func TestRootAllowanceFromConfig(t *testing.T) {
	c := llm.NewClient()

	tests := []struct {
		name             string
		maxSubagentDepth int
		wantAllowance    int
	}{
		{
			name:             "default (zero) gives 1",
			maxSubagentDepth: 0,
			wantAllowance:    1,
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
