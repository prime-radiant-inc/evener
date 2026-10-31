//go:build unix

package agent

// Round-6 regression for livelock mint hygiene (issue #5471's reporting PR,
// RoboRev finding 4): a construction that fails AFTER initSessionState —
// which minted the root's process livelock as its last step — must give the
// lock back in its failure cleanup, or the session that never came to life
// reads as live until the process exits. Both constructors are pinned
// through their existing late-failure path (an unknown context strategy
// fails after init), and the probe asserts the released state: the file
// exists (the mint created it) but nothing holds it.

import (
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// requireLivelockReleasedAfterFailedConstruction asserts the reporting
// contract's post-failure state: exists (the mint created the file) but not
// held (the failure cleanup released it), so a later probe answers
// dead-looking rather than false-live.
func requireLivelockReleasedAfterFailedConstruction(t *testing.T, stateDir, sid string) {
	t.Helper()
	held, exists, err := schema.SessionLivelockHeld(stateDir, sid)
	if err != nil {
		t.Fatalf("probe livelock for %s: %v", sid, err)
	}
	if !exists {
		t.Fatalf("livelock file for %s missing; the mint must have created it", sid)
	}
	if held {
		t.Fatal("a construction that failed after init left its minted livelock held: false-live evidence until process exit")
	}
}

// TestNewSessionFailureReleasesMintedLivelock: NewSession mints inside
// initSessionState and then fails on an unknown context strategy; the
// initComplete-guarded cleanup must release the livelock.
func TestNewSessionFailureReleasesMintedLivelock(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sid := identifier.MustNewSessionID()
	cfg := SessionConfig{StateDir: stateDir, ContextStrategy: "r6-bogus-strategy"}
	cfg.spawn.sessionID = sid

	sess, err := NewSession(llm.NewClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err == nil {
		sess.Close()
		t.Fatal("expected NewSession to fail on an unknown context strategy")
	}
	if sess != nil {
		t.Fatal("expected a nil session on error")
	}
	requireLivelockReleasedAfterFailedConstruction(t, stateDir, sid)
}

// TestRestoreSessionFailureReleasesMintedLivelock is the restore twin: the
// mint happens inside the restored root's initSessionState, and the
// restore-replay failure below it must release through the
// restoreComplete-guarded cleanup.
func TestRestoreSessionFailureReleasesMintedLivelock(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sid := identifier.MustNewSessionID()
	snap := SessionConfig{ContextStrategy: "r6-bogus-strategy"}.toSnapshot()
	meta := schema.SessionMeta{ID: sid, ProfileID: "openai", Model: "gpt-5.2", Config: snap}

	sess, err := RestoreSessionFromMetaWithConfig(
		w3init_restoreClient(), NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()), meta,
		RestoreSessionConfig{StateDir: stateDir},
	)
	if err == nil {
		sess.Close()
		t.Fatal("expected the restore to fail on an unknown context strategy")
	}
	if sess != nil {
		t.Fatal("expected a nil session on error")
	}
	requireLivelockReleasedAfterFailedConstruction(t, stateDir, sid)
}
