package agent

// Retirement vs dead-owner reclamation (issue #5471 review round 2, finding 3):
// idle retirement is non-terminal and deliberately preserves resumable
// delegates and their locked worktrees (docs/daemon-idle-retirement.md —
// "retirement does not sweep them away"). A retired root must therefore never
// read as a *dead* root to a foreign prune: retirement parks the process
// livelock durably (the file is removed, which answers unknown, never dead),
// so the retired owner's lanes survive until it resumes — and the resume
// re-takes the livelock, making the owner live again.

import (
	"context"
	"os"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
)

// retiredOwnerSessionConfig builds the config for a fixture session with a
// pre-selected session id in a shared state dir.
func retiredOwnerSessionConfig(sid string) SessionConfig {
	return SessionConfig{
		MaxSubagentDepth: 1,
		spawn:            spawnConfig{sessionID: sid},
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	}
}

// TestWorktreeRetirementParksLivelockAndKeepsLanes: a retired (non-terminal,
// resumable) root is not a dead root. Retirement removes the livelock file so
// a foreign prune answers "unverifiable" instead of dead, the delegate lanes
// it preserved stay locked, and a later resume of the same session id takes
// the livelock again — the owner is live once more.
func TestWorktreeRetirementParksLivelockAndKeepsLanes(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	retiredOwnerSID := identifier.MustNewSessionID()
	seedOwnerSessionMeta(t, r.stateDir, retiredOwnerSID, false, "")
	own := newSession(t, withStateDir(r.stateDir), withConfig(retiredOwnerSessionConfig(retiredOwnerSID)))

	held, exists, err := schema.SessionLivelockHeld(r.stateDir, retiredOwnerSID)
	if err != nil || !exists || !held {
		t.Fatalf("owner livelock before retirement = (held=%v, exists=%v, err=%v), want held", held, exists, err)
	}

	path, _ := r.addDelegateLaneFixture(t, deadOwnerDlgID, retiredOwnerSID)
	marker := "evener:dlg:" + deadOwnerDlgID + ":" + retiredOwnerSID

	// Retire the owner's runtime: the non-terminal release path idle
	// retirement takes (releaseRuntime under the retirement policy), which
	// preserves resumable delegates and their locked lanes for the resume.
	_ = own.releaseRuntime(context.Background(), closeOptions{}, releaseRetirement)

	// The park is durable evidence: no livelock file remains, so the retired
	// owner can never be read as a crashed-dead one.
	if _, statErr := os.Stat(schema.SessionLivelockPath(r.stateDir, retiredOwnerSID)); !os.IsNotExist(statErr) {
		t.Fatalf("retirement left a livelock file (stat err=%v): a free livelock reads as a dead owner", statErr)
	}

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune after retirement: %v", err)
	}
	if e := findPruneEntry(t, pruneEntries(t, out, "removed"), deadOwnerDlgID); e != nil {
		t.Fatalf("foreign prune collected a retired owner's preserved lane: %+v", e)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), deadOwnerDlgID)
	if e == nil {
		t.Fatalf("%s not reported skipped: %+v", deadOwnerDlgID, out)
	}
	if reason, _ := e["reason"].(string); !strings.Contains(reason, "locked") {
		t.Errorf("reason = %q, want a locked refusal: a retired owner is not a dead owner", reason)
	}
	if !r.delegateLaneLockedViaPorcelain(t, path, marker) {
		t.Error("retired owner's lane lost its lock to a foreign prune")
	}
	if !branchExistsInRepo(t, r.mainRoot, deadOwnerDlgID) {
		t.Error("retired owner's lane branch was deleted by a foreign prune")
	}

	// The resume re-represents the owner: a replacement session with the same
	// id takes the livelock again, so the tree is live and its lanes are
	// exactly as protected as before the retirement.
	_ = newSession(t, withStateDir(r.stateDir), withConfig(retiredOwnerSessionConfig(retiredOwnerSID)))
	held, exists, err = schema.SessionLivelockHeld(r.stateDir, retiredOwnerSID)
	if err != nil || !exists || !held {
		t.Fatalf("owner livelock after resume = (held=%v, exists=%v, err=%v), want held again", held, exists, err)
	}
}
