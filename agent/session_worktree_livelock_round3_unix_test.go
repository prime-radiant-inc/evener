//go:build unix

package agent

// Round-3 regressions for the dead-owner lane reclamation (issue #5471):
//
//   - the resume gate (finding 1): a resumed root must not adopt its lanes
//     before its livelock acquisition lands, because a prune may hold the
//     root's reclamation claim across exactly that window;
//   - the claim-before-eligibility ordering (finding 2): the dead-owner claim
//     is taken before any eligibility rung, so a commit that lands between
//     the listing and the claim is seen by the disposability check, and a
//     retirement that parks in the same window is seen by the revalidation;
//   - the park's precedence (finding 3): a committed retirement's durable
//     marker outranks the stale dead rendezvous entry an earlier crash left;
//   - the park's join with in-flight acquisition attempts (finding 4): an
//     attempt paused across a retirement leaves the root reading parked,
//     never dead.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/rendezvous"
)

// sessionAtWithID builds an additional session over the SAME scripted git
// boundary, main repo and state dir as r, with a caller-chosen session id: a
// resumed owner's replacement process, for tests that drive the real resume
// paths against a lane the previous runtime owned.
func (r *scriptedLaneRepo) sessionAtWithID(t *testing.T, sid string) *scriptedLaneRepo {
	t.Helper()
	cfg := worktreeTestSessionConfig()
	cfg.StateDir = r.stateDir
	cfg.clock = r.s.cfg.clock
	cfg.spawn.sessionID = sid
	cfg.testOnly.environmentInfo = scriptedEnvironmentInfo
	cfg.testOnly.worktreeGitRunner = func(context.Context, execenv.ExecutionEnvironment) worktree.GitRunner {
		return r.git.run
	}
	s := newSession(t, withDir(r.mainRoot), withConfig(cfg))
	s.stateDir = r.stateDir
	s.mu.Lock()
	s.worktreeGitVersionOK = true
	s.mu.Unlock()
	return &scriptedLaneRepo{t: t, s: s, git: r.git, mainRoot: r.mainRoot, stateDir: r.stateDir, clock: r.clock}
}

// seedColdResumableIsolationLane is seedStableIsolationLane with the complete
// cold-resume evidence a replacement process's bootstrap reconcile requires:
// a resolved profile and model on the descriptor, plus the child session's
// meta and transcript. Without them the fold closes the delegate's
// resumability at the resume's bootstrap (missing_delegate_resume_metadata),
// and there is no owned lane for a resumed session to adopt.
func (r *scriptedLaneRepo) seedColdResumableIsolationLane(t *testing.T) (delegateID, lanePath string) {
	t.Helper()
	delegateID = r.s.delegateController.newDelegateID()
	lanePath, _, _, _, _, err := r.s.createDelegateWorktree(context.Background(), delegateID, "")
	if err != nil {
		t.Fatalf("create stable delegate worktree: %v", err)
	}
	childID := "child-" + delegateID
	descriptor := delegatestore.Descriptor{
		ChildSessionID:    childID,
		TranscriptRef:     encodeRef("", childID),
		OwnerSessionID:    r.s.id,
		VisibleSessionID:  r.s.id,
		Task:              "stable worktree fixture",
		AgentType:         "default",
		ResolvedProfileID: "fixture-profile",
		ResolvedModel:     "fixture-model",
		ToolNameCeiling:   []string{"communicate"},
		WorkingDir:        lanePath,
		LocalEnvPolicy:    "default",
		Isolation:         "worktree",
		Resumable:         true,
	}
	seedOwnerSessionMeta(t, r.stateDir, childID, true, r.s.id)
	header, err := json.Marshal(transcript.Header{
		Kind:            "header",
		FormatVersion:   2,
		SessionID:       childID,
		ParentSessionID: r.s.id,
		Task:            "stable worktree fixture",
		CreatedAt:       time.Unix(5, 0).UTC(),
		ProfileID:       "fixture-profile",
		Model:           "fixture-model",
		Depth:           1,
	})
	if err != nil {
		t.Fatalf("marshal transcript header: %v", err)
	}
	if err := os.WriteFile(schema.SessionTranscriptPath(r.stateDir, childID), append(header, '\n'), 0o644); err != nil {
		t.Fatalf("write child transcript: %v", err)
	}
	lease := delegateLease{delegateID: delegateID, generation: 1}
	r.s.delegateController.mu.Lock()
	_, err = r.s.delegateController.appendLocked(
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateCreated,
			DelegateID: delegateID,
			Created:    &delegatestore.DelegateCreated{Descriptor: descriptor},
		},
		delegateControllerRunStartedEvent(delegateID, 1, delegatestore.TriggerInitial, time.Unix(10, 0).UTC()),
	)
	if err == nil {
		r.s.delegateController.live[delegateID] = &delegateLiveState{binding: &delegateRuntimeBinding{
			lease:  lease,
			cancel: func() {},
			ready:  true,
		}}
		r.s.delegateController.turnsInUse++
	}
	r.s.delegateController.mu.Unlock()
	if err != nil {
		t.Fatalf("seed stable delegate: %v", err)
	}
	plans, err := r.s.delegateController.FinishGeneration(lease, delegateFinish{
		outcome: delegatestore.OutcomeFailed,
		reason:  "fixture complete",
		endedAt: time.Unix(20, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("finish stable delegate: %v", err)
	}
	for _, plan := range plans.deliveries {
		token, admitted, deliveryErr := r.s.delegateController.BeginDelivery(plan)
		if deliveryErr != nil || !admitted {
			t.Fatalf("begin fixture delivery: admitted=%t err=%v", admitted, deliveryErr)
		}
		if _, deliveryErr := r.s.delegateController.CompleteDelivery(token, true); deliveryErr != nil {
			t.Fatalf("acknowledge fixture delivery: %v", deliveryErr)
		}
	}
	return delegateID, lanePath
}

// TestWorktreeResumeWaitsForLivelockBeforeAdoptingLanes (review round 3,
// finding 1): an actual resume while prune holds the root claim. The owner's
// previous runtime retired non-terminally (lanes preserved), and a prune now
// holds the root's livelock claim — the resumed owner's replacement process
// cannot take the livelock, so it must not adopt its lanes either: an
// adoption racing the claim's removal is exactly the resumed-owner lane loss
// the review found. Once the claim releases, the acquisition lands and the
// re-lock runs; the park marker the resume inherited is cleared, making the
// owner live again.
func TestWorktreeResumeWaitsForLivelockBeforeAdoptingLanes(t *testing.T) {
	t.Parallel()
	sid := identifier.MustNewSessionID()
	cfg := worktreeTestSessionConfig()
	cfg.spawn.sessionID = sid
	r := newScriptedLaneRepoWithConfig(t, cfg)
	if r.s.ID() != sid {
		t.Fatalf("fixture session id = %q, want the pre-selected %q", r.s.ID(), sid)
	}
	_, path := r.seedColdResumableIsolationLane(t)
	r.unlockLane(t, path) // the clean-close state a resume must re-lock

	// The owner's runtime retires non-terminally: process-local resources
	// release, the lanes it owns stay for the resume.
	_ = r.s.releaseRuntime(context.Background(), closeOptions{}, releaseRetirement)

	// A prune holds the root claim: the dead-owner reclamation's exclusive
	// flock on the root's livelock.
	releaseClaim := holdLivelock(t, r.stateDir, sid)

	// The resume: a replacement process on the same session id, whose
	// non-blocking acquisition contends with the claim, exactly as a resume
	// overlapping the reclamation does.
	resumed := r.sessionAtWithID(t, sid)
	resumed.s.mu.Lock()
	signal := resumed.s.livelockAcquired
	resumed.s.mu.Unlock()
	if signal == nil {
		t.Fatal("the resumed session's livelock acquisition was not pending under the claim")
	}

	// The resume's lane re-lock (the restore tail's post-init step) runs —
	// and must not adopt anything while the claim is held.
	go resumed.s.resumeReLockOwnLanes()

	// Negative wait: an ungated re-lock would have taken the lane by now (the
	// scripted git boundary answers synchronously); a gated one cannot, since
	// the acquisition this gate waits on is denied by the claim's flock.
	time.Sleep(50 * time.Millisecond)
	if _, locked, _ := r.laneLocked(t, path); locked {
		t.Fatal("resume adopted its lane while a prune held the root's livelock claim")
	}

	// The claim releases: the acquisition lands, the gate opens, the re-lock
	// adopts the lane.
	releaseClaim()
	waitForCondition(t, livelockContentionWaitBudget, "the resume re-lock after the claim released", func() bool {
		_, locked, _ := r.laneLocked(t, path)
		return locked
	})

	// The resume is live again, not parked: its later crash must leave honest
	// residue, not a preserved-forever root.
	if parked, err := schema.SessionLivelockParked(r.stateDir, sid); err != nil || parked {
		t.Fatalf("park marker after resume = (present=%v, err=%v), want cleared", parked, err)
	}
}

// TestWorktreePrune_ClaimPrecedesEligibilitySeesMidPassCommit (review round 3,
// finding 2): the owner resumes between the listing and the claim and commits
// to the lane branch. With the claim taken BEFORE the eligibility rungs, the
// disposability check reads the branch under the claim and refuses the now
// diverged tip; with the claim taken after, the pass would collect the lane on
// the listing's stale verdict and force-delete the resumed owner's new commit.
func TestWorktreePrune_ClaimPrecedesEligibilitySeesMidPassCommit(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	seedOwnerSessionMeta(t, r.stateDir, deadOwnerRootSID, false, "")
	seedDeadLivelock(t, r.stateDir, deadOwnerRootSID)
	path, _ := r.addDelegateLaneFixture(t, deadOwnerDlgID, deadOwnerRootSID)
	marker := "evener:dlg:" + deadOwnerDlgID + ":" + deadOwnerRootSID

	r.s.worktreeBeforeDeadOwnerLivelockClaim = func(lanePath string) {
		// The owner resumes here — after the listing's dead verdict, before
		// the claim — and commits to the lane branch.
		wtGit(t, lanePath,
			"-c", "user.email=owner@example.com", "-c", "user.name=Owner",
			"commit", "--allow-empty", "-m", "resumed-owner-work")
	}

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if e := findPruneEntry(t, pruneEntries(t, out, "removed"), deadOwnerDlgID); e != nil {
		t.Fatalf("prune collected a lane whose branch changed after the listing: %+v", e)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), deadOwnerDlgID)
	if e == nil {
		t.Fatalf("%s not reported skipped: %+v", deadOwnerDlgID, out)
	}
	if reason, _ := e["reason"].(string); strings.Contains(reason, "owner resumed since listing") || strings.Contains(reason, "owner retired since listing") {
		t.Errorf("reason = %q: the owner did not resume or retire, its branch merely changed", reason)
	}
	if !r.delegateLaneLockedViaPorcelain(t, path, marker) {
		t.Error("the resumed owner's lane lost its lock")
	}
	if !branchExistsInRepo(t, r.mainRoot, deadOwnerDlgID) {
		t.Error("the resumed owner's new commit was force-deleted")
	}
}

// TestWorktreePrune_ParkedSinceListingSupersedesDeadVerdict (review round 3,
// finding 2): the owner resumes after the listing and RETIRES before the
// claim. The park commits durably in that window, and the revalidation under
// the claim must read it — the stale death verdict that authorized the claim
// cannot collect a retired root's preserved lanes, and the claim's own claim
// protocol cannot resurrect the park: the marker is the park, not the file's
// absence.
func TestWorktreePrune_ParkedSinceListingSupersedesDeadVerdict(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	seedOwnerSessionMeta(t, r.stateDir, deadOwnerRootSID, false, "")
	seedDeadLivelock(t, r.stateDir, deadOwnerRootSID)
	path, _ := r.addDelegateLaneFixture(t, deadOwnerDlgID, deadOwnerRootSID)
	marker := "evener:dlg:" + deadOwnerDlgID + ":" + deadOwnerRootSID

	// The raw marker file, not the schema helper: this must simulate exactly
	// what the committed retirement wrote, byte for byte, in the window.
	parkPath := filepath.Join(r.stateDir, "sessions", deadOwnerRootSID+".livelock.parked")
	r.s.worktreeBeforeDeadOwnerLivelockClaim = func(string) {
		if err := os.WriteFile(parkPath, []byte("2026-10-30T00:00:00Z\n"), 0o600); err != nil {
			t.Errorf("write park marker at the seam: %v", err)
		}
	}

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if e := findPruneEntry(t, pruneEntries(t, out, "removed"), deadOwnerDlgID); e != nil {
		t.Fatalf("prune collected a lane whose owner retired (parked) after the listing: %+v", e)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), deadOwnerDlgID)
	if e == nil {
		t.Fatalf("%s not reported skipped: %+v", deadOwnerDlgID, out)
	}
	if reason, _ := e["reason"].(string); reason != "owner retired since listing" {
		t.Errorf("reason = %q, want owner retired since listing", reason)
	}
	if !r.delegateLaneLockedViaPorcelain(t, path, marker) {
		t.Error("the retired owner's preserved lane lost its lock")
	}
	if !branchExistsInRepo(t, r.mainRoot, deadOwnerDlgID) {
		t.Error("the retired owner's preserved lane branch was deleted")
	}
}

// TestWorktreeRetirementSupersedesStaleCrashRendezvousEntry (review round 3,
// finding 3): crash → resume → retirement → foreign prune. The earlier crash
// left a rendezvous roster entry with a dead pid that the replacement process
// never owned and so never removed; the successful resume then retires. The
// park's durable marker must outrank that stale entry, or the foreign prune
// reads the retired root as dead and deletes the lanes its resumable delegates
// still require.
func TestWorktreeRetirementSupersedesStaleCrashRendezvousEntry(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	rv := t.TempDir()
	r.s.worktreeRendezvousDir = rv
	sid := identifier.MustNewSessionID()
	if _, err := rendezvous.Write(rv, rendezvous.Entry{
		PID: reapedChildPID(t), SessionID: sid, ThreadID: sid,
	}); err != nil {
		t.Fatalf("write the earlier crash's roster entry: %v", err)
	}
	seedOwnerSessionMeta(t, r.stateDir, sid, false, "")
	own := newSession(t, withStateDir(r.stateDir), withConfig(retiredOwnerSessionConfig(sid)))
	path, _ := r.addDelegateLaneFixture(t, deadOwnerDlgID, sid)
	marker := "evener:dlg:" + deadOwnerDlgID + ":" + sid

	// The successful resume holds its livelock (the stale dead entry cannot
	// outrank it), then retires non-terminally.
	held, exists, err := schema.SessionLivelockHeld(r.stateDir, sid)
	if err != nil || !exists || !held {
		t.Fatalf("owner livelock after resume = (held=%v, exists=%v, err=%v), want held", held, exists, err)
	}
	_ = own.releaseRuntime(context.Background(), closeOptions{}, releaseRetirement)

	parked, err := schema.SessionLivelockParked(r.stateDir, sid)
	if err != nil || !parked {
		t.Fatalf("retirement left no park marker (parked=%v, err=%v)", parked, err)
	}

	out, err := r.pruneOp(t)
	if err != nil {
		t.Fatalf("prune after retirement: %v", err)
	}
	if e := findPruneEntry(t, pruneEntries(t, out, "removed"), deadOwnerDlgID); e != nil {
		t.Fatalf("foreign prune collected a retired root's lane on a stale crash entry: %+v", e)
	}
	e := findPruneEntry(t, pruneEntries(t, out, "skipped"), deadOwnerDlgID)
	if e == nil {
		t.Fatalf("%s not reported skipped: %+v", deadOwnerDlgID, out)
	}
	if reason, _ := e["reason"].(string); !strings.Contains(reason, "unverifiable") {
		t.Errorf("reason = %q, want the owner's liveness unverifiable (parked), not dead", reason)
	}
	if !r.delegateLaneLockedViaPorcelain(t, path, marker) {
		t.Error("the retired root's preserved lane lost its lock")
	}
	if !branchExistsInRepo(t, r.mainRoot, deadOwnerDlgID) {
		t.Error("the retired root's preserved lane branch was deleted")
	}
}

// TestWorktreeRetirementJoinsInFlightLivelockRetry (review round 3, finding 4):
// an acquisition attempt pauses across a retirement — it read the retry loop's
// stop flag before the park, and proceeds after the park has committed. The
// late attempt must join the park, not race it: the retired root keeps reading
// parked (marker beside a free-but-present livelock), never as a dead owner,
// whatever the attempt does to the flock.
func TestWorktreeRetirementJoinsInFlightLivelockRetry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sid := identifier.MustNewSessionID()
	if err := os.MkdirAll(schema.SessionsDir(dir), 0o755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	holder := holdLivelock(t, dir, sid)

	s := newSession(t, withStateDir(dir), withConfig(retiredOwnerSessionConfig(sid)))
	// Park the next retry-loop attempt at a deterministic point: after its
	// stop-flag read, before its acquisition.
	entered := make(chan struct{})
	unblock := make(chan struct{})
	var enterOnce sync.Once
	s.mu.Lock()
	s.testOnlyBeforeLivelockRetryAcquire = func() {
		enterOnce.Do(func() { close(entered) })
		<-unblock
	}
	s.mu.Unlock()
	waitForCondition(t, livelockContentionWaitBudget, "a retry attempt to pause at the seam", func() bool {
		return channelClosed(entered)
	})

	// Retirement parks while the attempt is paused.
	_ = s.releaseRuntime(context.Background(), closeOptions{}, releaseRetirement)

	// The contention clears and the paused attempt proceeds: it acquires the
	// root's livelock, sees the stop flag at its landing re-check, and
	// releases. The attempt is one wakeup of work; the settle below leaves
	// far more room than it needs (a fixed negative settle — there is no
	// completion signal to poll for on a path whose outcome is the ABSENCE of
	// any further action).
	holder()
	close(unblock)
	time.Sleep(250 * time.Millisecond)

	// Whatever the late attempt did, the retired root reads parked: the marker
	// is present, the file is free-but-present, and the oracle answers
	// unknown — never dead.
	held, exists, err := schema.SessionLivelockHeld(dir, sid)
	if err != nil || !exists || held {
		t.Fatalf("livelock after the late attempt settled = (held=%v, exists=%v, err=%v), want free-but-present", held, exists, err)
	}
	parked, err := schema.SessionLivelockParked(dir, sid)
	if err != nil || !parked {
		t.Fatalf("park marker after the late attempt = (present=%v, err=%v), want present", parked, err)
	}
	probe := newSession(t, withStateDir(dir), withConfig(retiredOwnerSessionConfig(identifier.MustNewSessionID())))
	verdict, root := probe.delegateLaneOwnerLiveness(worktree.Marker{DelegateID: deadOwnerDlgID, SessionID: sid})
	if verdict != ownerUnknown || root != sid {
		t.Fatalf("owner liveness after retirement joined a late acquisition attempt = %v/%q, want unknown (parked) for %q", verdict, root, sid)
	}
}
