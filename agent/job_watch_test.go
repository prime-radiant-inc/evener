package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/jobstore"
)

// onSessionEventKD drives the jobManager's session-event entry point with a kind
// and data, wrapping them in a SessionEvent envelope the way Session.emit does.
// Tests that need to set provenance on the event build a full events.SessionEvent
// literal and call jm.onSessionEvent directly instead.
func onSessionEventKD(jm *jobManager, kind events.EventKind, data events.EventData) {
	jm.onSessionEvent(events.SessionEvent{Kind: kind, SessionID: jm.sessionID, Data: data})
}

// drainAndAccept advances watch delivery the way the live loop does: one drain
// pass (delegate targets deliver + caller pendings re-token) followed by one
// notification accept (caller tokens render by key and settle). Use it in
// Session-based tests to drive a full delivery cycle; pure-jm tests assert on
// pending state instead (the new observable contract at the jobManager level).
func drainAndAccept(t *testing.T, s *Session) {
	t.Helper()
	if err := s.drainPendingWatchSends(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	s.acceptNotificationInput(context.Background()) // ok to no-op on empty queue
}

// drainWatchSendsVia delivers every pending non-caller watch send through the
// drain's delivery primitive (deliverPendingWatchSend), capturing the delivery
// args via the supplied sender — the way the live drain calls s.sendDelegateMessage.
// Caller-targeted sends route to notification tokens, not this primitive, so they
// are skipped (mirroring drainJobManagerWatchSends). Per-delivery errors are
// returned joined (the live drain logs them at the boundary and continues), so
// crash-recovery tests can assert the resulting state. Pure-jm tests use this to
// observe a delegate-targeted delivery after recording pending intent.
func drainWatchSendsVia(t *testing.T, jm *jobManager, send sendMessageFunc) error {
	t.Helper()
	var errs []error
	for _, d := range jm.pendingWatchSendDeliveries(nil) {
		if d.state.Key.ResolvedSendTo == runtimeMessageAliasCaller {
			continue
		}
		if _, err := jm.deliverPendingWatchSend(context.Background(), d.cfg, d.state, true, send); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// deliverWatchSendVia records a fired delivery as pending and delivers it through
// the drain's primitive with the supplied sender. It mirrors the (now-deleted)
// jobManager.deliverWatchSend, but takes the sender as a parameter — the structural
// point of the mailbox design is that delivery is never reachable from a jobManager
// field, only from explicit loop-owned (or, in tests, explicit) delivery.
func deliverWatchSendVia(t *testing.T, jm *jobManager, d watchSendDelivery, send sendMessageFunc) error {
	t.Helper()
	state, cfg, ok, err := jm.recordWatchSend(d)
	if err != nil || !ok {
		return err
	}
	_, err = jm.deliverPendingWatchSend(context.Background(), cfg, state, false, send)
	return err
}

// installWatchBelowValidation installs a watch directly into jm.watches the way
// configureWatch does AFTER newWatchConfig succeeds, but WITHOUT the validation
// layer (target/send checks and the feedback-loop guard). It exists so tests can
// exercise the live firing+delivery path (onSessionEvent -> recordWatchSendsAnd
// Kick) for caller-self watch shapes that the create-path guard now rejects.
// newWatchConfig itself runs no loop guard, so this install is legal below
// validation. The install sequence mirrors configureWatch: build cfg, lock,
// initProgressStop, assign, unlock, startProgressTimer (the timer no-ops for
// events-only configs where progressIntervalMS == 0).
func installWatchBelowValidation(t *testing.T, jm *jobManager, a watchArgs) {
	t.Helper()
	if a.Send != nil {
		a.Send.To = strings.TrimSpace(a.Send.To)
	}
	cfg, err := newWatchConfig(a, jm.now())
	if err != nil {
		t.Fatalf("newWatchConfig(%+v): %v", a, err)
	}
	sendTo := ""
	if a.Send != nil {
		sendTo = a.Send.To
	}
	key := watchKey{VisibleSessionID: jm.sessionID, Target: a.Target, SendTo: sendTo}
	jm.mu.Lock()
	stop := cfg.initProgressStop()
	jm.watches[key] = cfg
	jm.mu.Unlock()
	jm.startProgressTimer(key, cfg, stop)
}

func captureWatchSendDelivery(t *testing.T, jm *jobManager, jobID, trigger string) watchSendDelivery {
	t.Helper()
	root := events.SessionEvent{SessionID: jm.sessionID, Provenance: jobProvenanceForWatch(jm, jobID)}
	jm.mu.Lock()
	var delivery watchSendDelivery
	for _, cfg := range jm.watches {
		if cfg.target == jobID {
			delivery = jm.watchSendSnapshot(cfg, jobID, trigger, root)
			break
		}
	}
	jm.mu.Unlock()
	if delivery.cfg == nil {
		t.Fatalf("watch for %s not found", jobID)
	}
	return jm.snapshotWatchSendFrame(delivery)
}

func captureWatchSendDeliveryForKey(t *testing.T, jm *jobManager, key watchKey, watchedIdentity, trigger string) watchSendDelivery {
	t.Helper()
	root := events.SessionEvent{SessionID: jm.sessionID, Provenance: jobProvenanceForWatch(jm, watchedIdentity)}
	jm.mu.Lock()
	cfg := jm.watches[key]
	var delivery watchSendDelivery
	if cfg != nil {
		delivery = jm.watchSendSnapshot(cfg, watchedIdentity, trigger, root)
	}
	jm.mu.Unlock()
	if delivery.cfg == nil {
		t.Fatalf("watch for %+v not found", key)
	}
	return jm.snapshotWatchSendFrame(delivery)
}

func setupConcretePendingWatchSend(t *testing.T, jm *jobManager) (*jobstore.JobRecord, watchSendDelivery) {
	t.Helper()
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "dlg_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	feedJob(jm, rec.JobID, []byte("ready one\n"))
	delivery := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: ready two")
	return rec, delivery
}

func waitForTestSignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func waitForTestError(t *testing.T, ch <-chan error, label string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return nil
	}
}

func createRunningDelegateWatchTarget(t *testing.T, jm *jobManager) *jobstore.JobRecord {
	t.Helper()
	rec, err := jm.createShell(createShellOpts{Command: "delegate-output"})
	if err != nil {
		t.Fatalf("create watch target: %v", err)
	}
	jm.mu.Lock()
	run := jm.running[rec.JobID]
	run.rec.Type = jobstore.JobDelegate
	run.rec.TranscriptRef = encodeRef("", "child-"+rec.JobID)
	rec = cloneJobRecord(run.rec)
	jm.mu.Unlock()
	return rec
}

func loadWatchSendRecord(t *testing.T, jm *jobManager) jobstore.WatchSendRecord {
	t.Helper()
	return jobstore.FoldWatchSends(loadJobStoreEvents(t, jm))
}

func restoredWatchSendDelegateEvents(sessionID, jobID string, now time.Time, resumable *bool, sendTo string) []jobstore.Event {
	endedAt := now.Add(time.Second)
	events := []jobstore.Event{
		{
			Kind:             jobstore.EventJobStarted,
			TS:               now,
			JobID:            jobID,
			Type:             jobstore.JobDelegate,
			OwnerSessionID:   sessionID,
			VisibleToSession: sessionID,
			StartedAt:        &now,
		},
		{
			Kind:          jobstore.EventJobSessionAssigned,
			TS:            now,
			JobID:         jobID,
			TranscriptRef: encodeRef("", "child_"+jobID),
			Resumable:     resumable,
		},
		{
			Kind:        jobstore.EventJobFinished,
			TS:          endedAt,
			JobID:       jobID,
			Status:      jobstore.StatusCompleted,
			Reason:      "exit_zero",
			EndedAt:     &endedAt,
			TerminalGen: "term_" + jobID,
		},
	}
	return append(events, restoredWatchSendPendingEvents(sessionID, jobID, sendTo, endedAt)...)
}

func restoredWatchSendPendingEvents(sessionID, watchedJobID, sendTo string, now time.Time) []jobstore.Event {
	return []jobstore.Event{{
		Kind: jobstore.EventWatchSendPending,
		TS:   now,
		WatchSend: &jobstore.WatchSendState{
			Key: jobstore.WatchSendKey{
				VisibleSessionID:        sessionID,
				WatchTarget:             watchedJobID,
				ResolvedWatchedIdentity: watchedJobID,
				ResolvedSendTo:          sendTo,
				WatchGeneration:         "watch_restore_generation",
			},
			DeliveryID:      "delivery_restore_pending",
			UpdateSeq:       1,
			Message:         "restored observe",
			Frame:           "restored observe\n\ndelivery_id: delivery_restore_pending",
			TriggerIdentity: watchedJobID,
			TriggerReason:   "output_match: ready",
			CreatedAt:       now,
			UpdatedAt:       now,
		},
	}}
}

func loadJobStoreEvents(t *testing.T, jm *jobManager) []jobstore.Event {
	t.Helper()
	b, err := os.ReadFile(jm.dir + "/jobs.jsonl")
	if err != nil {
		t.Fatalf("read jobs.jsonl: %v", err)
	}
	var events []jobstore.Event
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e jobstore.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("parse event %q: %v", line, err)
		}
		events = append(events, e)
	}
	return events
}

func countDelegateStartedEvents(t *testing.T, jm *jobManager, delegateID string) int {
	t.Helper()
	var count int
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.Kind == jobstore.EventJobStarted && event.DelegateID == delegateID {
			count++
		}
	}
	return count
}

func runtimeWatchSendPending(t *testing.T, jm *jobManager) map[jobstore.WatchSendKey]*jobstore.WatchSendState {
	t.Helper()
	out := make(map[jobstore.WatchSendKey]*jobstore.WatchSendState)
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for _, cfg := range jm.watches {
		for key, state := range cfg.pending {
			copied := *state
			out[key] = &copied
		}
	}
	for cfg := range jm.terminalFlush {
		for key, state := range cfg.pending {
			copied := *state
			out[key] = &copied
		}
	}
	return out
}

func seedCommonWatchSendTargets(t *testing.T, jm *jobManager) {
	t.Helper()
	seedWatchSendDelegateTarget(t, jm, "dlg_obs")
}

func seedWatchSendDelegateTarget(t *testing.T, jm *jobManager, target string) {
	t.Helper()
	delegateID := target
	jobID := target
	if strings.HasPrefix(target, "job_") {
		delegateID = "dlg_" + strings.TrimPrefix(target, "job_")
	} else if strings.HasPrefix(target, "dlg_") {
		jobID = "job_" + strings.TrimPrefix(target, "dlg_")
	}
	childID := "child_" + jobID
	delegates, err := jm.store.LoadDelegates()
	if err != nil {
		t.Fatalf("load delegates before seeding watch-send target: %v", err)
	}
	now := jm.now()
	if delegates[delegateID] == nil {
		if err := jm.appendEvent(jobstore.Event{
			Kind:       jobstore.EventDelegateCreated,
			TS:         now,
			DelegateID: delegateID,
			Delegate: &jobstore.DelegateEvent{
				ChildSessionID:   childID,
				TranscriptRef:    encodeRef("", childID),
				OwnerSessionID:   jm.sessionID,
				VisibleSessionID: jm.sessionID,
				Generation:       jobstore.NewDelegateGeneration(),
				Resumable:        true,
			},
		}); err != nil {
			t.Fatalf("seed watch-send delegate %q: %v", delegateID, err)
		}
	}
	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load jobs before seeding watch-send target: %v", err)
	}
	if rec := recs[jobID]; rec != nil {
		return
	}
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               now,
		JobID:            jobID,
		Type:             jobstore.JobDelegate,
		Status:           jobstore.StatusRunning,
		OwnerSessionID:   jm.sessionID,
		VisibleToSession: jm.sessionID,
		DelegateID:       delegateID,
		// Production delegates carry their transcript ref in the job_started
		// event (attachDelegateJobWithRestore); grant minting resolves the
		// observer's child session id from it.
		TranscriptRef: encodeRef("", childID),
		StartedAt:     &now,
	}); err != nil {
		t.Fatalf("seed watch-send delegate target %q: %v", jobID, err)
	}
}

func appendDelegateTargetEvents(t *testing.T, jm *jobManager, delegateID string, resumable *bool) {
	t.Helper()
	jobID := "job_" + strings.TrimPrefix(delegateID, "dlg_")
	childID := "child_" + jobID
	now := jm.now()
	started := now.Add(time.Millisecond)
	events := []jobstore.Event{
		{
			Kind:       jobstore.EventDelegateCreated,
			TS:         now,
			DelegateID: delegateID,
			Delegate: &jobstore.DelegateEvent{
				ChildSessionID:   childID,
				TranscriptRef:    encodeRef("", childID),
				OwnerSessionID:   jm.sessionID,
				VisibleSessionID: jm.sessionID,
				Generation:       jobstore.NewDelegateGeneration(),
				Resumable:        true,
			},
		},
		{
			Kind:             jobstore.EventJobStarted,
			TS:               started,
			JobID:            jobID,
			Type:             jobstore.JobDelegate,
			DelegateID:       delegateID,
			OwnerSessionID:   jm.sessionID,
			VisibleToSession: jm.sessionID,
			TranscriptRef:    encodeRef("", childID),
			StartedAt:        &started,
		},
	}
	if resumable != nil {
		events = append(events, jobstore.Event{
			Kind:          jobstore.EventJobSessionAssigned,
			TS:            now.Add(2 * time.Millisecond),
			JobID:         jobID,
			TranscriptRef: encodeRef("", childID),
			Resumable:     resumable,
		})
	}
	ended := now.Add(3 * time.Millisecond)
	events = append(events, jobstore.Event{
		Kind:    jobstore.EventJobFinished,
		TS:      ended,
		JobID:   jobID,
		Status:  jobstore.StatusCompleted,
		EndedAt: &ended,
	})
	for _, event := range events {
		if err := jm.appendEvent(event); err != nil {
			t.Fatalf("append %s: %v", event.Kind, err)
		}
	}
}

func busyWatchSendResult() sendMessageResult {
	return sendMessageResult{
		WatchSendDeliveryClass:    watchSendBusy,
		WatchSendDeliveryClassSet: true,
		Err:                       errors.New("busy"),
	}
}

func hardWatchSendResult(err error) sendMessageResult {
	return sendMessageResult{
		WatchSendDeliveryClass:    watchSendHardFailure,
		WatchSendDeliveryClassSet: true,
		Err:                       err,
	}
}

func containsEventKind(kinds []jobstore.EventKind, want jobstore.EventKind) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}

func eventKindOrder(kinds []jobstore.EventKind, before, after jobstore.EventKind) bool {
	beforeIndex := -1
	afterIndex := -1
	for i, kind := range kinds {
		if kind == before && beforeIndex == -1 {
			beforeIndex = i
		}
		if kind == after && afterIndex == -1 {
			afterIndex = i
		}
	}
	return beforeIndex >= 0 && afterIndex >= 0 && beforeIndex < afterIndex
}

func installCallerSendWatchWithPending(t *testing.T, jm *jobManager) *watchConfig {
	t.Helper()
	// This is the feedback-loop shape (caller target, communicate,
	// send.to=caller) that configureWatch now rejects (TestValidateWatchDeliveryLoop
	// asserts the rejection). Install below validation to exercise the caller-send
	// pending/busy-delivery mechanics this helper's callers depend on.
	installWatchBelowValidation(t, jm, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"communicate"},
		Send:   &watchSendArgs{To: runtimeMessageAliasCaller, Message: "ping"},
	})
	onSessionEventKD(jm, events.EventCommunicate, nil)
	key := watchKey{
		VisibleSessionID: jm.sessionID,
		Target:           runtimeMessageAliasCaller,
		SendTo:           runtimeMessageAliasCaller,
	}
	cfg := jm.watches[key]
	if cfg == nil {
		t.Fatal("installCallerSendWatchWithPending: watch config not found")
	}
	if len(cfg.pendingOrder) == 0 {
		t.Fatal("installCallerSendWatchWithPending: no pending send after busy delivery")
	}
	return cfg
}

// installCallerSendWatchWithCurrentFrame installs a caller-send watch, drives it
// to updateSeq 2 (one busy fire creates pending @1, a second coalesces to @2),
// then stamps a deterministic Frame on the single pending entry so render-by-key
// assertions can match exact frame text. Returns the cfg, the pending key, and
// the pending entry's DeliveryID.
func installCallerSendWatchWithCurrentFrame(t *testing.T, jm *jobManager, frame string) (*watchConfig, jobstore.WatchSendKey, string) {
	t.Helper()
	cfg := installCallerSendWatchWithPending(t, jm)
	onSessionEventKD(jm, events.EventCommunicate, nil) // bump updateSeq 1 -> 2
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if len(cfg.pendingOrder) != 1 {
		t.Fatalf("want exactly one pending entry, got %d", len(cfg.pendingOrder))
	}
	key := cfg.pendingOrder[0]
	state := cfg.pending[key]
	if state == nil {
		t.Fatal("pending entry missing for key")
	}
	if state.UpdateSeq != 2 {
		t.Fatalf("pending updateSeq = %d, want 2 after two fires", state.UpdateSeq)
	}
	state.Frame = frame
	return cfg, key, state.DeliveryID
}

func blankRuntimePendingDelegateGenerationForTest(t *testing.T, jm *jobManager) {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for _, cfg := range jm.watches {
		for _, state := range cfg.pending {
			state.DelegateGeneration = ""
		}
	}
	for cfg := range jm.terminalFlush {
		for _, state := range cfg.pending {
			state.DelegateGeneration = ""
		}
	}
}

// The v2 re-route this section once tested — the parent's drain re-tokening a
// child's caller-targeted pending onto the parent's rail — is deleted in T15.
// Its replacement, TestDrainDoesNotReRouteChildCallerPendings, lives in
// job_delegate_drivedown_test.go and pins the new behavior: a mid-owner caller
// send renders in the mid's own drive turn, never on the parent's rail.

// terminalShellWithOutput creates a shell job, writes output to it, finalizes it
// completed, and returns the (now store-only) job_id. After finalize the job has
// been removed from jm.running, so a watch attached afterward must resolve its
// terminal status from the store and scan retained output via grepOutput.
func terminalShellWithOutput(t *testing.T, jm *jobManager, output string) string {
	t.Helper()
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	if output != "" {
		if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte(output)); err != nil {
			t.Fatalf("append output: %v", err)
		}
	}
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	jm.mu.Lock()
	_, stillRunning := jm.running[rec.JobID]
	jm.mu.Unlock()
	if stillRunning {
		t.Fatalf("job %q still in jm.running after finalize; terminal catch-up tests assume store-only", rec.JobID)
	}
	return rec.JobID
}

// TestTerminalCatchupNoSendFiresNotification covers spec §7.1 "Terminal target":
// an output_match-only watch on a terminal job whose retained output already
// contains a match performs a one-shot catch-up — fires exactly one notification,
// installs no live watch, and reports terminal_catchup with the terminal status.
// --- observer read grants (spec §5.1) ---

func loadGrantTable(t *testing.T, jm *jobManager) map[string]map[string]bool {
	t.Helper()
	grants, err := jm.store.LoadGrants()
	if err != nil {
		t.Fatalf("load grants: %v", err)
	}
	return grants
}

func countWatchReadGrantEvents(t *testing.T, jm *jobManager) int {
	t.Helper()
	var n int
	for _, e := range loadJobStoreEvents(t, jm) {
		if e.Kind == jobstore.EventWatchReadGrant {
			n++
		}
	}
	return n
}

// --- granted cross-session reads (spec §5.1, consumption) ---

// grantReadWatchedOutput is the watched job's full retained output in the
// granted-read fixture; "ready" fires the sidecar watch.
const grantReadWatchedOutput = "alpha\nbravo ready\ncharlie\n"

// grantReadFixture is the minimal parent/observer pair for granted
// cross-session read tests: the parent jobManager owns a watched shell job
// plus the seeded observer delegate (job_obs -> child session
// "child_job_obs"), and the observer child session carries the
// parent-injected grant-lookup seam exactly the way spawnAgent and delegate
// restore wire it.
type grantReadFixture struct {
	parentStateDir string
	parent         *Session
	parentJM       *jobManager
	observer       *Session
	watched        string
}

func newGrantReadFixture(t *testing.T) *grantReadFixture {
	t.Helper()
	parentStateDir := t.TempDir()
	parentJM, err := newJobManager(parentStateDir, "PARENT", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new parent jobManager: %v", err)
	}
	observerJM, err := newJobManager(t.TempDir(), "child_job_obs", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new observer jobManager: %v", err)
	}
	t.Cleanup(func() {
		_ = parentJM.store.Close()
		_ = observerJM.store.Close()
	})

	parent := &Session{id: "PARENT", jobManager: parentJM, subagents: newSubagentManager(nil)}
	observer := &Session{id: "child_job_obs", jobManager: observerJM}
	observer.cfg.spawn.parentGrantedJobRead = parent.lookupGrantedJobRead

	seedCommonWatchSendTargets(t, parentJM)
	watched, err := parentJM.createShell(createShellOpts{Command: "server"})
	if err != nil {
		t.Fatalf("create watched job: %v", err)
	}
	// Canonical sidecar flow: the watch is created while the job runs (grant
	// minted at create), output fires it, the job finishes, and the observer
	// reads after the fact.
	if _, err := parentJM.configureWatch(watchArgs{
		Target:      watched.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "dlg_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure sidecar watch: %v", err)
	}
	if _, err := parentJM.appendJobOutput(watched.JobID, parentJM.running[watched.JobID].output, []byte(grantReadWatchedOutput)); err != nil {
		t.Fatalf("append watched output: %v", err)
	}
	code := 0
	if err := parentJM.finalize(watched.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize watched job: %v", err)
	}
	return &grantReadFixture{
		parentStateDir: parentStateDir,
		parent:         parent,
		parentJM:       parentJM,
		observer:       observer,
		watched:        watched.JobID,
	}
}

func observerReadOutput(t *testing.T, observer *Session, args map[string]any) (jobReadOutputTestResult, error) {
	t.Helper()
	out, err := jobReadOutputTool(context.Background(), observer, args, 20000)
	if err != nil {
		return jobReadOutputTestResult{}, err
	}
	var res jobReadOutputTestResult
	if err := json.Unmarshal(handlerJSON(t, out), &res); err != nil {
		t.Fatalf("unmarshal job_read_output: %v (output: %s)", err, out)
	}
	return res, nil
}

var _ = jobstore.JobShell

func onlyWatchConfigForTest(t *testing.T, jm *jobManager) *watchConfig {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if len(jm.watches) != 1 {
		t.Fatalf("watch count = %d, want 1", len(jm.watches))
	}
	for _, cfg := range jm.watches {
		return cfg
	}
	panic("unreachable")
}
