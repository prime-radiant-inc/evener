package agent

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// These tests cover issue #4528: the manual retire's wait pre-check must see
// the obligations whose read used to block (a running job, a watch, a journal
// only NotifyPending record, and the attention registry), so a retire that
// lands beside one refuses at once instead of waiting out the namer (#4055's
// red/green shape). A source that cannot be read without waiting contributes
// no evidence and keeps today's wait.

// assertManualRetireRefusedAtOnce requires the retire to refuse without
// parking its timer on the namer, and the refusal snapshot to name category.
func assertManualRetireRefusedAtOnce(t *testing.T, c *RetirementController, clk *agenttest.FakeClock, category, sessionID string) {
	t.Helper()
	done := startManualClaim(c)
	select {
	case got := <-done:
		if got.err != nil || got.claim != nil {
			t.Fatalf("manual retire beside %s = claim %v, %v; want a refusal", category, got.claim, got.err)
		}
		if !hasRetirementBlockerFor(got.snapshot.Blockers, category, sessionID) {
			t.Fatalf("refusal snapshot lost the %s obligation: %+v", category, got.snapshot.Blockers)
		}
	// TRIPWIRE: a refusal returns at once; 30s only fires if the retire waited
	// on the fake clock for the namer.
	case <-time.After(30 * time.Second):
		t.Fatalf("manual retire waited for the namer beside %s", category)
	}
	if n := clk.BlockedCount(); n != 0 {
		t.Fatalf("manual retire parked %d timer(s) beside %s", n, category)
	}
}

// assertManualRetireWaitsThenClaims requires the next retire to wait for the
// namer (the obligation that previously skipped the wait is gone) and to claim
// once the namer is released.
func assertManualRetireWaitsThenClaims(t *testing.T, c *RetirementController, clk *agenttest.FakeClock, release func()) {
	t.Helper()
	done := startManualClaim(c)
	awaitManualClaimWaiting(t, clk, done)
	release()
	got := <-done
	if got.err != nil || got.claim == nil {
		t.Fatalf("manual retire after the obligation settled = %+v, %v; want a claim", got.snapshot, got.err)
	}
	if err := c.Abort(got.claim, ""); err != nil {
		t.Fatal(err)
	}
}

// startRunningRetirementShell registers a real job-manager run without spawning
// a process (createShell only appends job_started and records the run), and
// settles it at cleanup without an owner notification.
func startRunningRetirementShell(t *testing.T, root *Session) *jobstore.JobRecord {
	t.Helper()
	rec, err := root.jobManager.createShell(createShellOpts{Command: "sleep 300", Description: "original running job"})
	if err != nil {
		t.Fatalf("start running shell: %v", err)
	}
	t.Cleanup(func() {
		if err := finalizeRetirementShell(root, rec.JobID); err != nil {
			t.Errorf("finalize fixture shell: %v", err)
		}
	})
	return rec
}

// finalizeRetirementShell settles a fixture run with no notification: there is
// no owner result to deliver in these tests, and a NotifyPending record would
// itself be evidence the transition assertions must not see.
func finalizeRetirementShell(root *Session, jobID string) error {
	return root.jobManager.finalizeWithRunNoNotification(jobID, func(*runningJob) (jobstore.Status, string, *int, error) {
		return jobstore.StatusCompleted, "fixture_cleanup", nil, nil
	})
}

func appendRetirementJournalEvents(t *testing.T, root *Session, events ...jobstore.Event) {
	t.Helper()
	for _, event := range events {
		if err := root.jobManager.store.Append(event); err != nil {
			t.Fatalf("append job journal event %s: %v", event.Kind, err)
		}
	}
}

// seedJournalPendingJob writes a terminal job whose notification survives only
// in the journal, the state rematerializeDurablePendings exists to recover.
func seedJournalPendingJob(t *testing.T, root *Session, jobID, generation string) {
	t.Helper()
	now := time.Now()
	startedAt := now
	appendRetirementJournalEvents(t, root,
		jobstore.Event{Kind: jobstore.EventJobStarted, TS: now, JobID: jobID, Type: jobstore.JobShell, OwnerSessionID: root.ID(), VisibleToSession: root.ID(), StartedAt: &startedAt},
		jobstore.Event{Kind: jobstore.EventJobFinished, TS: now, JobID: jobID, Status: jobstore.StatusCompleted, Reason: "exit_zero", TerminalGen: generation},
		jobstore.Event{Kind: jobstore.EventJobNotificationPending, TS: now, JobID: jobID, TerminalGen: generation},
	)
}

// TestManualRetireDoesNotWaitForARunningJobBesideTheNamer is #4528's running
// job case: the run is in-memory owner state, so the pre-check can read it
// without touching the journal.
func TestManualRetireDoesNotWaitForARunningJobBesideTheNamer(t *testing.T) {
	t.Parallel()
	root, c, clk, release := blockedNamerSession(t)
	rec := startRunningRetirementShell(t, root)

	assertManualRetireRefusedAtOnce(t, c, clk, "job", root.ID())

	// Transition back to useful operation: once the run is settled, the
	// evidence is gone and the retire waits for the namer as #3921 requires.
	if err := finalizeRetirementShell(root, rec.JobID); err != nil {
		t.Fatal(err)
	}
	assertManualRetireWaitsThenClaims(t, c, clk, release)
}

// TestManualRetireDoesNotWaitForAnActiveWatchBesideTheNamer is #4528's watch
// case: watches are in-memory counters too.
func TestManualRetireDoesNotWaitForAnActiveWatchBesideTheNamer(t *testing.T) {
	t.Parallel()
	root, c, clk, release := blockedNamerSession(t)
	registered, err := root.jobManager.configureWatch(watchArgs{Target: "caller", Source: "self", AfterSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = root.jobManager.clearWatchByID(registered.WatchID)
	})

	assertManualRetireRefusedAtOnce(t, c, clk, "watch", root.ID())

	if _, err := root.jobManager.clearWatchByID(registered.WatchID); err != nil {
		t.Fatal(err)
	}
	assertManualRetireWaitsThenClaims(t, c, clk, release)
}

// TestManualRetireDoesNotWaitForAJournalOnlyPendingNotificationBesideTheNamer
// is #4528's journal case: pendingJobNotifs does not mirror this record, so the
// read has to see the journal itself.
func TestManualRetireDoesNotWaitForAJournalOnlyPendingNotificationBesideTheNamer(t *testing.T) {
	t.Parallel()
	root, c, clk, release := blockedNamerSession(t)
	const jobID = "job-4528-journal"
	const generation = "gen-4528-journal"
	seedJournalPendingJob(t, root, jobID, generation)
	if pending := root.peekNotifications(); pending != 0 {
		t.Fatalf("fixture journal pending also reached the in-memory queue: %d", pending)
	}

	assertManualRetireRefusedAtOnce(t, c, clk, "notification", root.ID())

	// The delivery marker settles the durable notification; the retire then
	// waits for the namer again.
	appendRetirementJournalEvents(t, root, jobstore.Event{Kind: jobstore.EventJobNotificationDelivered, TS: time.Now(), JobID: jobID, TerminalGen: generation})
	assertManualRetireWaitsThenClaims(t, c, clk, release)
}

// TestManualRetireDoesNotWaitForPendingAttentionBesideTheNamer is #4528's
// attention case. The registry is set directly, as the attention tests do; the
// read under attentionMu is the production predicate.
func TestManualRetireDoesNotWaitForPendingAttentionBesideTheNamer(t *testing.T) {
	t.Parallel()
	root, c, clk, release := blockedNamerSession(t)
	const attentionID = "attention-4528"
	root.attentionMu.Lock()
	if root.rootAttentionWakeIDs == nil {
		root.rootAttentionWakeIDs = make(map[string]struct{})
	}
	root.rootAttentionWakeIDs[attentionID] = struct{}{}
	root.attentionMu.Unlock()

	assertManualRetireRefusedAtOnce(t, c, clk, "notification", root.ID())

	root.attentionMu.Lock()
	delete(root.rootAttentionWakeIDs, attentionID)
	root.attentionMu.Unlock()
	assertManualRetireWaitsThenClaims(t, c, clk, release)
}

// TestManualRetireKeepsWaitingWhenAttentionIsBusy pins the fail-closed branch:
// attentionMu is held across durable transcript work, so a busy registry
// reports no evidence and the retire keeps today's wait rather than skipping
// it on an unreadable source.
func TestManualRetireKeepsWaitingWhenAttentionIsBusy(t *testing.T) {
	t.Parallel()
	root, c, clk, release := blockedNamerSession(t)
	root.attentionMu.Lock()
	unlock := sync.OnceFunc(root.attentionMu.Unlock)
	defer unlock()
	assertManualRetireWaitsThenClaims(t, c, clk, func() {
		unlock()
		release()
	})
}

// TestManualRetireKeepsWaitingWhenJobStateIsBusy is the same fail-closed
// branch for jm.mu, which is held across durable store reads. The fixture
// journal is deliberately empty, so the counter read is the only job evidence
// and its contention is what the wait proves.
func TestManualRetireKeepsWaitingWhenJobStateIsBusy(t *testing.T) {
	t.Parallel()
	root, c, clk, release := blockedNamerSession(t)
	root.jobManager.mu.Lock()
	unlock := sync.OnceFunc(root.jobManager.mu.Unlock)
	defer unlock()
	assertManualRetireWaitsThenClaims(t, c, clk, func() {
		unlock()
		release()
	})
}

// TestManualRetireSnapshotCautionsWhenAttentionCannotBeRead proves the busy
// registry is still reported as an unknown, possibly-held source in the
// diagnostic snapshot. The namer lease already refuses the claim, so the
// placeholder gates nothing.
func TestManualRetireSnapshotCautionsWhenAttentionCannotBeRead(t *testing.T) {
	t.Parallel()
	root, c, _, _ := blockedNamerSession(t)
	root.attentionMu.Lock()
	unlock := sync.OnceFunc(root.attentionMu.Unlock)
	defer unlock()
	claim, snapshot, err := c.TryClaim(true)
	unlock()
	if claim != nil {
		_ = c.Abort(claim, "")
		t.Fatalf("claim passed a held attention registry: %+v", snapshot)
	}
	if err != nil {
		t.Fatal(err)
	}
	if !hasRetirementBlockerFor(snapshot.Blockers, "notification", root.ID()) {
		t.Fatalf("refusal snapshot lost the unreadable attention caution: %+v", snapshot.Blockers)
	}
}

// TestManualRetireSnapshotCautionsWhenJobStateIsBusy is the same caution for a
// busy jm.mu. The namer lease already refuses the claim, so the placeholder
// gates nothing.
func TestManualRetireSnapshotCautionsWhenJobStateIsBusy(t *testing.T) {
	t.Parallel()
	root, c, _, _ := blockedNamerSession(t)
	root.jobManager.mu.Lock()
	unlock := sync.OnceFunc(root.jobManager.mu.Unlock)
	defer unlock()
	claim, snapshot, err := c.TryClaim(true)
	unlock()
	if claim != nil {
		_ = c.Abort(claim, "")
		t.Fatalf("claim passed a held job manager: %+v", snapshot)
	}
	if err != nil {
		t.Fatal(err)
	}
	if !hasRetirementBlockerFor(snapshot.Blockers, "job", root.ID()) {
		t.Fatalf("refusal snapshot lost the unreadable job-state caution: %+v", snapshot.Blockers)
	}
}

// TestRetirementNonBlockingJournalPendingBoundedRead pins the bounded journal
// read itself: it sees only this session's complete Pending records, and an
// empty, incomplete or oversized journal contributes no evidence, keeping the
// retire's wait.
func TestRetirementNonBlockingJournalPendingBoundedRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	jm := &jobManager{dir: dir}
	path := filepath.Join(dir, "jobs.jsonl")
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if jm.retirementNonBlockingJournalPending(sessionID) {
		t.Fatal("empty journal reported evidence")
	}

	store, err := jobstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	startedAt := now
	for _, event := range []jobstore.Event{
		{Kind: jobstore.EventJobStarted, TS: now, JobID: "job-bounded", Type: jobstore.JobShell, OwnerSessionID: sessionID, StartedAt: &startedAt},
		{Kind: jobstore.EventJobFinished, TS: now, JobID: "job-bounded", Status: jobstore.StatusCompleted, TerminalGen: "gen-bounded"},
		{Kind: jobstore.EventJobNotificationPending, TS: now, JobID: "job-bounded", TerminalGen: "gen-bounded"},
	} {
		if err := store.Append(event); err != nil {
			t.Fatalf("append journal event %s: %v", event.Kind, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if !jm.retirementNonBlockingJournalPending(sessionID) {
		t.Fatal("journal-only Pending record was not read")
	}
	if jm.retirementNonBlockingJournalPending("another-session") {
		t.Fatal("another session's Pending record counted as evidence")
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"kind":"job_notification_delivered","job_id":"job-bounded","terminal_generation":"gen-bounded"`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if jm.retirementNonBlockingJournalPending(sessionID) {
		t.Fatal("an incomplete trailing line was read as a settled journal")
	}

	if err := os.WriteFile(path, make([]byte, retirementNonBlockingJournalMaxBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if jm.retirementNonBlockingJournalPending(sessionID) {
		t.Fatal("an oversized journal reported evidence")
	}
}
