package agent

import (
	"fmt"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// The diagnostic must distinguish a durable pending entry from a finished warm
// run, and must not hang behind the very locks a failed run may hold.
func TestStableSupervisionFailureSnapshotCapturesPendingAttention(t *testing.T) {
	t.Parallel()
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	// A served root can recheck after quiescence and arm a retry for the
	// diagnostic append, so leave its wake consumer unstarted.
	sub := warmStableDelegateUnservedRoot(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	// Keep admission refused while inspecting the warm run's finished channel.
	if !sub.trySetDisposeGate() {
		t.Fatal("gate quiescent child during pending diagnostic")
	}
	t.Cleanup(sub.clearDisposeGate)
	const attentionID = "attention:diagnostic-pending"
	if appended, err := sub.sess.appendDelegateNotificationDurably(attentionID, "diagnostic-sentinel"); err != nil || !appended {
		t.Fatalf("append diagnostic attention = %t, %v", appended, err)
	}
	// The child's initial-prompt session namer can still be running after
	// the run quiesces, taking the session lock as it names and saves the
	// session, and the snapshot records session_lock_busy rather than wait.
	// Hold the lock here as the namer would, so the uncontended keys below
	// are read once it is free.
	sub.sess.mu.Lock()
	time.AfterFunc(heldSnapshotLockForRetry, sub.sess.mu.Unlock)
	snapshot := uncontendedStableSupervisionSnapshot(t, root, sub, fixture.adapter)
	if !reflect.DeepEqual(snapshot["pending_attention_ids"], []string{attentionID}) || snapshot["current_run_open"] != false || snapshot["done_closed"] != true {
		t.Fatalf("snapshot omitted strict pending predicate or finished warm channel: %#v", snapshot)
	}
	if !reflect.DeepEqual(snapshot["blocking_predicates"], []string{"pending_attention_ids"}) {
		t.Fatalf("snapshot blocking predicates = %#v, want only durable pending attention", snapshot["blocking_predicates"])
	}
	// Receiver-scoped retry owns scheduling, not a second inventory of IDs.
	// Pending membership above comes from the canonical transcript fold.
	for _, key := range []string{"notification_retry_state", "attention_callbacks", "attention_reservations", "controller_finalizing", "running", "driving", "subagent_finalizing", "done_present", "attention_resolutions", "attention_resume_generations", "settlement_claims", "pending_notifications", "notify_callback"} {
		if _, ok := snapshot[key]; !ok {
			t.Errorf("snapshot omitted %q", key)
		}
	}
	if snapshot["provider_cursor"] != 1 || snapshot["provider_steps"] != 1 {
		t.Fatalf("snapshot omitted scripted provider cursor: %#v", snapshot)
	}
	requests, ok := snapshot["provider_requests"].([]llm.Request)
	if !ok || len(requests) != 1 || !requestMessagesContainText(requests[0].Messages, "warm retained runtime") {
		t.Fatalf("snapshot omitted actual provider request: %#v", snapshot)
	}
	if stacks, ok := snapshot["goroutine_stacks"].(string); !ok || !strings.Contains(stacks, "TestStableSupervisionFailureSnapshotCapturesPendingAttention") {
		t.Fatalf("snapshot omitted relevant goroutine stacks: %#v", snapshot)
	}

	t.Run("contended locks remain observable without blocking", func(t *testing.T) {
		sub.mu.Lock()
		defer sub.mu.Unlock()
		root.delegateController.mu.Lock()
		defer root.delegateController.mu.Unlock()
		sub.sess.attentionMu.Lock()
		defer sub.sess.attentionMu.Unlock()
		sub.sess.mu.Lock()
		defer sub.sess.mu.Unlock()
		sub.sess.pendingJobNotifsMu.Lock()
		defer sub.sess.pendingJobNotifsMu.Unlock()
		fixture.adapter.mu.Lock()
		defer fixture.adapter.mu.Unlock()
		snapshot := stableSupervisionFailureSnapshot(root, sub, fixture.adapter)
		for _, key := range []string{"subagent_lock_busy", "controller_lock_busy", "attention_lock_busy", "session_lock_busy", "notification_lock_busy", "provider_lock_busy"} {
			if snapshot[key] != true {
				t.Errorf("snapshot[%q] = %#v, want contended lock", key, snapshot[key])
			}
		}
	})
	for _, tc := range []struct {
		name, marker, field string
		hold                func() func()
	}{
		{"subagent", "subagent_lock_busy", "running", func() func() { sub.mu.Lock(); return sub.mu.Unlock }},
		{"controller", "controller_lock_busy", "attention_reservations", func() func() { root.delegateController.mu.Lock(); return root.delegateController.mu.Unlock }},
		{"attention", "attention_lock_busy", "attention_callbacks", func() func() { sub.sess.attentionMu.Lock(); return sub.sess.attentionMu.Unlock }},
		{"session", "session_lock_busy", "notification_retry_state", func() func() { sub.sess.mu.Lock(); return sub.sess.mu.Unlock }},
		{"notification", "notification_lock_busy", "pending_notifications", func() func() { sub.sess.pendingJobNotifsMu.Lock(); return sub.sess.pendingJobNotifsMu.Unlock }},
		{"provider", "provider_lock_busy", "provider_requests", func() func() { fixture.adapter.mu.Lock(); return fixture.adapter.mu.Unlock }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := tc.hold()
			defer release()
			done := make(chan map[string]any, 1)
			go func() { done <- stableSupervisionFailureSnapshot(root, sub, fixture.adapter) }()
			select {
			case snapshot := <-done:
				if snapshot[tc.marker] != true {
					t.Fatalf("snapshot omitted actual busy source %s: %#v", tc.name, snapshot)
				}
				if _, ok := snapshot[tc.field]; ok {
					t.Fatalf("snapshot read %s through held %s lock", tc.field, tc.name)
				}
				if tc.name == "attention" {
					if _, ok := snapshot["notification_retry_state"]; !ok {
						t.Fatal("attention lock incorrectly hid session-owned retry state")
					}
				}
				if tc.name == "session" && snapshot["attention_lock_busy"] == true {
					t.Fatal("session lock incorrectly attributed to attention source")
				}
			case <-time.After(stableSupervisionReportDeadline): // TRIPWIRE: a best-effort snapshot must return while this exact lock stays held.
				t.Fatalf("snapshot blocked on held %s lock", tc.name)
			}
		})
	}
}

// Dropping a populated reservation, or flattening its admission flags, must
// lose evidence here even though the previous warm run is already quiescent.
func TestStableSupervisionFailureSnapshotCapturesAttentionReservation(t *testing.T) {
	t.Parallel()
	var root *Session
	response := llm.Assistant("reservation-response-sentinel")
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(req llm.Request) llm.Response {
			assertStableSupervisionPresentingLease(t, root, fixture.delegateID, 2)
			observationRequest(t, req, []string{"reservation-sentinel"}, nil)
			return llm.Response{Message: response}
		},
	}
	root = restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	const attentionID = "attention:diagnostic-reservation"
	if appended, err := sub.sess.appendDelegateNotificationDurably(attentionID, "reservation-sentinel"); err != nil || !appended {
		t.Fatalf("append reserved attention = %t, %v", appended, err)
	}
	reservation, err := root.delegateController.ReserveAttention(sub.sess, attentionID)
	if err != nil {
		t.Fatalf("reserve attention: %v", err)
	}
	t.Cleanup(func() { abortStableDelegateStartReservation(root, fixture.delegateID) })
	snapshot := uncontendedStableSupervisionSnapshot(t, root, sub, fixture.adapter)
	wantReservation := []map[string]any{{"generation": uint64(2), "attention_id": attentionID, "admitted": false, "resolution_ready": false}}
	if !reflect.DeepEqual(snapshot["attention_reservations"], wantReservation) || !reflect.DeepEqual(snapshot["pending_attention_ids"], []string{attentionID}) {
		t.Fatalf("unaccepted reservation diagnostics = %#v", snapshot)
	}
	if !reflect.DeepEqual(snapshot["blocking_predicates"], []string{"pending_attention_ids", "attention_reservations"}) {
		t.Fatalf("unaccepted reservation blockers = %#v", snapshot["blocking_predicates"])
	}
	if err := sub.sess.acceptDelegateAttention(reservation); err != nil {
		t.Fatalf("accept attention: %v", err)
	}
	snapshot = uncontendedStableSupervisionSnapshot(t, root, sub, fixture.adapter)
	wantReservation = []map[string]any{{"generation": uint64(2), "attention_id": attentionID, "admitted": true, "resolution_ready": true}}
	if !reflect.DeepEqual(snapshot["attention_reservations"], wantReservation) || !reflect.DeepEqual(snapshot["blocking_predicates"], []string{"pending_attention_ids", "attention_reservations"}) {
		t.Fatalf("accepted reservation diagnostics = %#v", snapshot)
	}
	if !reflect.DeepEqual(snapshot["pending_attention_ids"], []string{attentionID}) ||
		!reflect.DeepEqual(snapshot["attention_resolutions"], map[string]delegateAttentionResolution{}) ||
		!reflect.DeepEqual(snapshot["attention_resume_generations"], map[string]uint64{}) {
		t.Fatalf("acceptance incorrectly claimed observation = %#v", snapshot)
	}
	assertAttentionResponseReceipts(t, sub.sess, attentionID, 0)
	if got := supervisionRequestCount(fixture.adapter); got != 1 {
		t.Fatalf("requests before launch = %d, want warm only", got)
	}
	started, err := root.delegateController.CommitStart(reservation)
	if err != nil {
		t.Fatalf("commit attention: %v", err)
	}
	if err := root.launchAcceptedDelegateAttention(sub, started); err != nil {
		t.Fatalf("launch attention: %v", err)
	}
	waitForStableSupervisionRun(t, root, fixture.childID, fixture.adapter)
	snapshot = uncontendedStableSupervisionSnapshot(t, root, sub, fixture.adapter)
	if !reflect.DeepEqual(snapshot["attention_reservations"], []map[string]any{}) || !reflect.DeepEqual(snapshot["blocking_predicates"], []string{}) || snapshot["generation"] != uint64(2) {
		t.Fatalf("settled reservation diagnostics = %#v", snapshot)
	}
	if got := supervisionRequestCount(fixture.adapter); got != 2 {
		t.Fatalf("requests after launch = %d, want exactly one attention request", got)
	}
	assertStableSupervisionAttentionReceipt(t, root, sub, attentionID, 2, response)
}

// The no-action path keeps its real ordinary settlement claim until
// FinishNoAction, so the observer sees populated evidence rather than a mock.
func TestStableSupervisionFailureSnapshotCapturesSettlementClaim(t *testing.T) {
	t.Parallel()
	var root *Session
	response := llm.Assistant("settlement-response-sentinel")
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(req llm.Request) llm.Response {
			assertStableSupervisionPresentingLease(t, root, fixture.delegateID, 2)
			observationRequest(t, req, []string{"settlement-sentinel"}, nil)
			return llm.Response{Message: response}
		},
	}
	root = restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	snapshots := make(chan map[string]any, 1)
	updateSessionTestConfig(sub.sess, func(cfg *testConfig) {
		cfg.subagentAfterFinalStatePublish = func(got *subagent) {
			// The snapshot omits the keys any busy lock guards, and these
			// assertions need the controller-guarded settlement and binding
			// keys while the claim is live. Take the controller lock as an
			// incidental holder would, then read once it is free; the hook
			// blocks the finalization tail, so the claim outlives the retry.
			// TryLock: a lock already held leaves the first attempt busy
			// without waiting on a holder the test did not schedule.
			if root.delegateController.mu.TryLock() {
				time.AfterFunc(heldSnapshotLockForRetry, root.delegateController.mu.Unlock)
			}
			snapshots <- stableSupervisionSnapshotWithoutBusyLock(root, got, fixture.adapter)
		}
	})
	const attentionID = "attention:diagnostic-settlement"
	armStableSupervisionAttention(t, sub, attentionID, "settlement-sentinel")
	waitForStableSupervisionRun(t, root, fixture.childID, fixture.adapter)
	var snapshot map[string]any
	select {
	case snapshot = <-snapshots:
	default:
		t.Fatal("real no-action final state observer was not reached")
	}
	if busy := supervisionSnapshotBusyLockNames(snapshot); len(busy) != 0 {
		t.Fatalf("settlement snapshot locks still busy: %v", busy)
	}
	wantClaim := []map[string]any{{"generation": uint64(2), "ready": true, "mode": delegateSettlementOrdinary}}
	if !reflect.DeepEqual(snapshot["settlement_claims"], wantClaim) || snapshot["settlement_claimed"] != true ||
		snapshot["binding_generation"] != uint64(2) || snapshot["binding_ready"] != true || snapshot["current_run_open"] != true {
		t.Fatalf("live settlement claim diagnostics = %#v", snapshot)
	}
	if snapshot["completion_requirement"] != delegateCompletionAttentionOnly || snapshot["completion_outcome"] != delegateCompletionOutcomeAttentionNoAction || snapshot["terminal_seen"] != false {
		t.Fatalf("no-action settlement evidence = %#v", snapshot)
	}
	if !reflect.DeepEqual(snapshot["attention_resolutions"], map[string]delegateAttentionResolution{attentionID: delegateAttentionConsumed}) ||
		!reflect.DeepEqual(snapshot["blocking_predicates"], []string{"subagent_finalizing", "current_run_open", "completion_channel"}) {
		t.Fatalf("settlement transcript or blockers = %#v", snapshot)
	}
	assertStableSupervisionAttentionReceipt(t, root, sub, attentionID, 2, response)
	snapshot = uncontendedStableSupervisionSnapshot(t, root, sub, fixture.adapter)
	if !reflect.DeepEqual(snapshot["settlement_claims"], []map[string]any{}) || !reflect.DeepEqual(snapshot["blocking_predicates"], []string{}) {
		t.Fatalf("settled claim diagnostics = %#v", snapshot)
	}
}

// The compatibility resolution generation is not a controller lease. Join the
// actual presenting request's live binding, the journal start, and the raw
// successful assistant response instead of reading that compatibility field.
func assertStableSupervisionPresentingLease(t *testing.T, root *Session, delegateID string, generation uint64) {
	t.Helper()
	c := root.delegateController
	c.mu.Lock()
	agg, live := c.durable[delegateID], c.live[delegateID]
	matched := agg != nil && agg.Generation == generation && agg.CurrentRunOpen &&
		live != nil && live.binding != nil && live.binding.lease == (delegateLease{delegateID: delegateID, generation: generation})
	c.mu.Unlock()
	if !matched {
		t.Errorf("presenting request did not own delegate %s generation %d", delegateID, generation)
	}
}

func assertStableSupervisionAttentionReceipt(t *testing.T, root *Session, sub *subagent, id string, generation uint64, responses ...llm.Message) {
	t.Helper()
	events, err := root.delegateController.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	starts := []uint64{}
	for _, event := range events {
		if event.DelegateID == sub.sess.owningDelegateID && event.RunStarted != nil && event.RunStarted.Trigger == delegatestore.TriggerAttention {
			starts = append(starts, event.RunStarted.Generation)
		}
	}
	if !reflect.DeepEqual(starts, []uint64{generation}) {
		t.Fatalf("attention journal starts = %v, want exactly [%d]", starts, generation)
	}
	receipts, resolutions := 0, 0
	rounds := map[string]bool{}
	for _, entry := range readAttentionTranscriptEntries(t, sub.sess.TranscriptPath()) {
		if slices.Contains(entry.Turn.ObservedNotificationIDs, id) {
			if receipts >= len(responses) || entry.Turn.Kind != schema.TurnAssistant || entry.Turn.RoundID == "" || rounds[entry.Turn.RoundID] || !reflect.DeepEqual(entry.Turn.Message, responses[receipts]) || !reflect.DeepEqual(entry.Turn.ObservedNotificationIDs, []string{id}) {
				t.Fatalf("wrong successful presenting response for %s: %#v", id, entry.Turn)
			}
			rounds[entry.Turn.RoundID] = true
			receipts++
		}
		if resolution := entry.Turn.AttentionResolution; resolution != nil && resolution.AttentionID == id {
			resolutions++
			if resolution.Disposition != string(delegateAttentionConsumed) {
				t.Fatalf("wrong derived resolution: %#v", resolution)
			}
		}
	}
	if receipts != len(responses) || resolutions != 1 {
		t.Fatalf("attention %s: raw successful receipts=%d want %d, derived resolutions=%d want 1", id, receipts, len(responses), resolutions)
	}
	fold, err := readDelegateAttentionFold(sub.sess.TranscriptPath(), sub.sess.id)
	if err != nil || slices.Contains(fold.pendingIDs(), id) || fold.resolutions[id] != delegateAttentionConsumed {
		t.Fatalf("attention not resolved after receipt: pending=%v resolutions=%v err=%v", fold.pendingIDs(), fold.resolutions, err)
	}
}

func TestStableSupervisionFailureSnapshotCapturesArmRetryIDs(t *testing.T) {
	t.Parallel()
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(req llm.Request) llm.Response {
			observationRequest(t, req, []string{"arm-retry-sentinel-attention:diagnostic-z", "arm-retry-sentinel-attention:diagnostic-a"}, nil)
			return llm.Response{Message: llm.Assistant("no action")}
		},
	}
	clock := agenttest.NewFakeClockAt(time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC))
	root := restoreSupervisionRoot(t, fixture, clock)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	sub.sess.mu.Lock()
	notify := sub.sess.notifyFunc
	sub.sess.mu.Unlock()
	sub.sess.SetNotifyFunc(nil)
	if !sub.trySetDisposeGate() {
		t.Fatal("gate quiescent child during arm retry")
	}
	defer sub.clearDisposeGate()
	for _, id := range []string{"attention:diagnostic-z", "attention:diagnostic-a"} {
		if err := sub.sess.enqueueNotification(notification{ID: id, Message: llm.User("arm-retry-sentinel-" + id)}); err != nil {
			t.Fatalf("enqueue retry notification %q: %v", id, err)
		}
	}
	// Make the real filesystem read fail without changing or truncating the
	// writer's bytes. One receiver-scoped retry owns both durable messages.
	path := transcriptPath(root.stateDir, fixture.childID)
	backup := path + ".diagnostic-arm-backup"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		if restoreErr := os.Rename(backup, path); restoreErr != nil {
			t.Errorf("restore transcript: %v", restoreErr)
		}
		t.Fatal(err)
	}
	restore := func() {
		t.Helper()
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(backup, path); err != nil {
			t.Fatal(err)
		}
	}
	// Restore before root.Close even when an assertion fails during forcing.
	restored := false
	t.Cleanup(func() {
		if !restored {
			restore()
		}
	})
	if err := sub.sess.requestNotificationWake(); err == nil {
		t.Fatal("receiver fold read against directory transcript succeeded")
	}
	if err := sub.sess.requestNotificationWake(); err != nil {
		t.Fatalf("coalesced receiver retry repeated the failed fold read: %v", err)
	}
	restore()
	restored = true
	snapshot := uncontendedStableSupervisionSnapshot(t, root, sub, fixture.adapter)
	if snapshot["notification_retry"] != true ||
		!reflect.DeepEqual(snapshot["pending_attention_ids"], []string{"attention:diagnostic-z", "attention:diagnostic-a"}) ||
		!reflect.DeepEqual(snapshot["blocking_predicates"], []string{"notification_retry", "pending_attention_ids"}) {
		t.Fatalf("populated arm retry diagnostics = %#v", snapshot)
	}
	clock.Advance(jobNotificationRetryInitialDelay)
	clock.Drain()
	snapshot = uncontendedStableSupervisionSnapshot(t, root, sub, fixture.adapter)
	if snapshot["notification_retry"] != false || snapshot["needs_attention"] != true || snapshot["generation"] != uint64(1) {
		t.Fatalf("successful retry diagnostics = %#v", snapshot)
	}
	sub.clearDisposeGate()
	sub.sess.SetNotifyFunc(notify)
	waitForStableSupervisionRun(t, root, fixture.childID, fixture.adapter)
}

func TestStableSupervisionFailureSnapshotCapturesNotificationPayload(t *testing.T) {
	t.Parallel()
	var root *Session
	presented := make(chan llm.Request, 1)
	response := llm.Assistant("notification acknowledged")
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(req llm.Request) llm.Response {
			assertStableSupervisionPresentingLease(t, root, fixture.delegateID, 2)
			presented <- req
			return llm.Response{Message: response}
		},
	}
	root = restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	if !sub.trySetDisposeGate() {
		t.Fatal("gate quiescent child during notification delivery")
	}
	t.Cleanup(sub.clearDisposeGate)
	// Exercise the real source lifecycle and its canonical acceptance. Merely
	// forwarding source events populates the retired legacy queue, not this rail.
	jm := sub.sess.jobManager
	record, err := jm.createShell(createShellOpts{Command: "diagnostic source", Description: "notification-label", Intent: "notification-intent"})
	if err != nil {
		t.Fatal(err)
	}
	jobID := record.JobID
	jm.mu.Lock()
	run := jm.running[jobID]
	jm.mu.Unlock()
	output := strings.Repeat("x", 42)
	if n, err := jm.appendJobOutput(jobID, run.output, []byte(output)); err != nil || n != 42 {
		t.Fatalf("append diagnostic output = %d, %v", n, err)
	}
	code := 7
	if err := jm.finishJob(run, jobstore.StatusFailed, "exit_nonzero", &code); err != nil {
		t.Fatalf("finish real source: %v", err)
	}
	if !supervisionChannelClosed(run.done) {
		t.Fatal("source completion did not finish durable notification custody")
	}
	records, err := jm.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	source := records[jobID]
	if source == nil || source.TerminalGen == "" || source.NotifyState != jobstore.NotifyDelivered || source.OutputBytes != 42 || source.ExitCode == nil || *source.ExitCode != code {
		t.Fatalf("durable source custody = %#v", source)
	}
	generation := source.TerminalGen
	attentionID := "shell:" + jobID + ":" + generation
	snapshot := uncontendedStableSupervisionSnapshot(t, root, sub, fixture.adapter)
	payloads, ok := snapshot["pending_attention_payloads"].(map[string]llm.Message)
	if !ok || len(payloads) != 1 || !reflect.DeepEqual(snapshot["pending_attention_ids"], []string{attentionID}) || snapshot["generation"] != uint64(1) || snapshot["done_closed"] != true || snapshot["provider_cursor"] != 1 {
		t.Fatalf("gated canonical notification diagnostics = %#v", snapshot)
	}
	want := map[string]string{
		"job_id": jobID, "event": "failed", "job_type": "shell", "description": "notification-label",
		"status": "failed", "reason": "exit_nonzero", "intent": "notification-intent",
		"output_bytes": "42", "exit_code": "7", "transcript_ref": "job:" + jobID,
	}
	drainRequestNotice(t, llm.Request{Messages: []llm.Message{payloads[attentionID]}}, want)
	if !strings.Contains(payloads[attentionID].Text(), output) || snapshot["notify_callback"] == "0x0" || snapshot["notify_callback"] == nil {
		t.Fatalf("canonical notification lost output or callback: %#v", payloads)
	}
	assertAttentionResponseReceipts(t, sub.sess, attentionID, 0)
	if stableSupervisionRefusal(sub) == "" {
		t.Fatal("unobserved source incorrectly appeared quiescent")
	}
	// Admission is still refused to the producer. Releasing its gate must let
	// the real producer wake/retry reach a NEW request, without a manual kick.
	sub.clearDisposeGate()
	var req llm.Request
	select {
	case req = <-presented:
	case <-time.After(supervisionSnapshotUncontendedTripwire): // TRIPWIRE: the real canonical producer owes this request after the admission gate opens.
		t.Fatalf("real producer did not present %s: %#v", attentionID, stableSupervisionFailureSnapshot(root, sub, fixture.adapter))
	}
	drainRequestNotice(t, req, want)
	if !requestMessagesContainText(req.Messages, output) {
		t.Fatal("presenting request omitted complete shell output")
	}
	presentations := 0
	for _, message := range req.Messages {
		if reflect.DeepEqual(message, payloads[attentionID]) {
			presentations++
		}
	}
	if presentations != 1 {
		t.Fatalf("complete canonical payload presented %d times, want exactly one", presentations)
	}
	waitForStableSupervisionRun(t, root, fixture.childID, fixture.adapter)
	snapshot = uncontendedStableSupervisionSnapshot(t, root, sub, fixture.adapter)
	if !reflect.DeepEqual(snapshot["pending_attention_ids"], []string{}) || snapshot["generation"] != uint64(2) || snapshot["provider_cursor"] != 2 {
		t.Fatalf("drained notification diagnostics = %#v", snapshot)
	}
	if notifications, ok := snapshot["pending_notifications"].([]jobNotification); !ok || len(notifications) != 0 {
		t.Fatalf("canonical delivery left legacy notifications: %#v", snapshot)
	}
	assertStableSupervisionAttentionReceipt(t, root, sub, attentionID, 2, response)
	accepted := 0
	for _, entry := range readAttentionTranscriptEntries(t, sub.sess.TranscriptPath()) {
		if entry.Turn.AttentionID == attentionID {
			accepted++
			if !reflect.DeepEqual(entry.Turn.Message, payloads[attentionID]) {
				t.Fatal("accepted source payload changed")
			}
		}
	}
	if accepted != 1 || supervisionRequestCount(fixture.adapter) != 2 {
		t.Fatalf("source accepted %d times, provider requests %d, want one source and warm + attention", accepted, supervisionRequestCount(fixture.adapter))
	}
	records, err = jm.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if record := records[jobID]; record == nil || record.NotifyState != jobstore.NotifyDelivered || record.TerminalGen != generation {
		t.Fatalf("durable notification delivery = %#v", record)
	}
}

// heldSnapshotLockForRetry is how long a test holds a runtime lock so the
// retrying readers' first attempt finds it busy. It is not a deadline: the
// reader waits until the lock is free, so any length passes; it only has to
// outlast that first attempt for the retry to run.
const heldSnapshotLockForRetry = 50 * time.Millisecond

// supervisionSnapshotUncontendedTripwire bounds how long a reader retries a
// failure snapshot while a lock it wants stays busy.
// TRIPWIRE: incidental holders keep a lock for milliseconds; 30s only fires on
// a lock that is genuinely leaked, not scheduler contention under a loaded suite.
const supervisionSnapshotUncontendedTripwire = 30 * time.Second

// supervisionSnapshotBusyLockNames lists the "<name>_lock_busy" keys a failure
// snapshot recorded for the locks it could not take.
func supervisionSnapshotBusyLockNames(snapshot map[string]any) []string {
	busy := []string{}
	for key := range snapshot {
		if strings.HasSuffix(key, "_lock_busy") {
			busy = append(busy, key)
		}
	}
	return busy
}

// stableSupervisionSnapshotWithoutBusyLock retries the best-effort failure
// snapshot until it reads no lock busy, so a caller that checks the keys a busy
// lock omits reads them uncontended. It runs on the child's run goroutine (the
// subagentAfterFinalStatePublish hook), which must not touch *testing.T, so it
// bounds itself with a tripwire and returns its last snapshot, leaving any busy
// keys for the test goroutine to report.
func stableSupervisionSnapshotWithoutBusyLock(root *Session, sub *subagent, adapter *fakeAdapter) map[string]any {
	deadline := time.Now().Add(supervisionSnapshotUncontendedTripwire)
	for {
		snapshot := stableSupervisionFailureSnapshot(root, sub, adapter)
		if len(supervisionSnapshotBusyLockNames(snapshot)) == 0 || !time.Now().Before(deadline) {
			return snapshot
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// uncontendedStableSupervisionSnapshot is stableSupervisionFailureSnapshot
// taken once no lock it reads is busy. The snapshot never waits for a lock, and
// paths outside the run, such as the child's session namer, can hold one briefly
// after it quiesces, so a test that checks its uncontended keys retries it
// instead.
func uncontendedStableSupervisionSnapshot(t *testing.T, root *Session, sub *subagent, adapter *fakeAdapter) map[string]any {
	t.Helper()
	snapshot := stableSupervisionSnapshotWithoutBusyLock(root, sub, adapter)
	if busy := supervisionSnapshotBusyLockNames(snapshot); len(busy) != 0 {
		t.Fatalf("supervision snapshot locks still busy after %s: %v", supervisionSnapshotUncontendedTripwire, busy)
	}
	return snapshot
}

func stableSupervisionFailureSnapshot(root *Session, sub *subagent, adapter *fakeAdapter) map[string]any {
	// This is a best-effort sequential snapshot, not an atomic lifecycle view.
	// Never wait for a runtime lock or hold one while reading another source.
	snapshot := map[string]any{"child_id": sub.sess.id, "delegate_id": sub.sess.owningDelegateID}
	if sub.mu.TryLock() {
		snapshot["running"], snapshot["driving"], snapshot["subagent_finalizing"] = sub.running, sub.driving, sub.finalizing
		snapshot["closed"], snapshot["settlement_claimed"] = sub.closed, sub.settlementClaimed
		snapshot["done_present"] = sub.done != nil
		snapshot["done_closed"] = supervisionChannelClosed(sub.done)
		sub.mu.Unlock()
	} else {
		snapshot["subagent_lock_busy"] = true
	}
	sess := sub.sess
	if sess.attentionMu.TryLock() {
		snapshot["attention_callbacks"] = sess.attentionCallbacks
		snapshot["attention_cursor_pending_ids"] = sess.attentionFoldCursor.fold.pendingIDs()
		sess.attentionMu.Unlock()
	} else {
		snapshot["attention_lock_busy"] = true
	}
	if sess.mu.TryLock() {
		snapshot["notification_retry"] = sess.notificationSchedule.retry.active
		snapshot["notification_retry_state"] = sess.notificationSchedule.retry
		snapshot["notify_callback"] = fmt.Sprintf("%p", sess.notifyFunc)
		snapshot["turn_started_at"] = sess.turnStartedAt
		sess.mu.Unlock()
	} else {
		snapshot["session_lock_busy"] = true
	}
	if sess.pendingJobNotifsMu.TryLock() {
		snapshot["pending_notifications"] = append([]jobNotification(nil), sess.pendingJobNotifs...)
		sess.pendingJobNotifsMu.Unlock()
	} else {
		snapshot["notification_lock_busy"] = true
	}
	fold, err := readDelegateAttentionFold(transcriptPath(root.stateDir, sess.id), sess.id)
	if err != nil {
		snapshot["attention_fold_error"] = err.Error()
	} else {
		snapshot["pending_attention_ids"] = fold.pendingIDs()
		payloads := map[string]llm.Message{}
		for _, id := range fold.pendingIDs() {
			payloads[id] = fold.content[id]
		}
		snapshot["pending_attention_payloads"] = payloads
		snapshot["attention_resolutions"] = fold.resolutions
		snapshot["attention_resume_generations"] = fold.resumeGenerations
	}
	c := root.delegateController
	if c.mu.TryLock() {
		if aggregate := c.durable[sess.owningDelegateID]; aggregate != nil {
			snapshot["generation"], snapshot["phase"] = aggregate.Generation, aggregate.Phase
			snapshot["current_run_open"], snapshot["needs_attention"] = aggregate.CurrentRunOpen, aggregate.NeedsAttention
			snapshot["pending_deliveries"] = len(aggregate.PendingDeliveries)
		}
		if live := c.live[sess.owningDelegateID]; live != nil {
			snapshot["controller_finalizing"] = live.finalizing != nil
			if live.finalizing != nil {
				snapshot["finalizing_generation"] = live.finalizing.generation
				snapshot["finalization_released"] = supervisionChannelClosed(live.finalizing.released)
			}
			if binding := live.binding; binding != nil {
				snapshot["binding_generation"], snapshot["binding_ready"] = binding.lease.generation, binding.ready
				if evidence := binding.evidence; evidence != nil {
					snapshot["completion_requirement"], snapshot["completion_outcome"], snapshot["terminal_seen"] = evidence.requirement, evidence.outcome, evidence.terminalSeen
				}
			}
		}
		reservations := []map[string]any{}
		for _, record := range c.reservations {
			if record.delegateID == sess.owningDelegateID && record.trigger == delegatestore.TriggerAttention && record.runtime == sess && record.attentionID != "" {
				reservations = append(reservations, map[string]any{"generation": record.generation, "attention_id": record.attentionID, "admitted": record.attentionAdmitted, "resolution_ready": record.attentionResolutionReady})
			}
		}
		snapshot["attention_reservations"] = reservations
		claims := []map[string]any{}
		for _, claim := range c.settlementClaims {
			if claim.lease.delegateID == sess.owningDelegateID {
				claims = append(claims, map[string]any{"generation": claim.lease.generation, "ready": supervisionChannelClosed(claim.ready), "mode": claim.mode})
			}
		}
		snapshot["settlement_claims"] = claims
		c.mu.Unlock()
	} else {
		snapshot["controller_lock_busy"] = true
	}
	if adapter != nil {
		// fakeAdapter holds this mutex across a scripted provider step. Blocking
		// Requests() here would hide a provider stuck inside that step.
		if adapter.mu.TryLock() {
			snapshot["provider_cursor"], snapshot["provider_steps"] = adapter.i, len(adapter.steps)
			snapshot["provider_requests"] = append([]llm.Request(nil), adapter.requests...)
			adapter.mu.Unlock()
		} else {
			snapshot["provider_lock_busy"] = true
		}
	}
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	stacks := []string{}
	for stack := range strings.SplitSeq(string(buf[:n]), "\n\n") {
		if strings.Contains(stack, "primeradiant.com/evener/agent.") {
			stacks = append(stacks, stack)
		}
	}
	snapshot["goroutine_stacks"] = strings.Join(stacks, "\n\n")
	snapshot["goroutine_stacks_truncated"] = n == len(buf)
	blockers := []string{}
	for _, key := range []string{"running", "driving", "subagent_finalizing", "current_run_open", "controller_finalizing", "notification_retry"} {
		if snapshot[key] == true {
			blockers = append(blockers, key)
		}
	}
	for _, key := range []string{"pending_attention_ids"} {
		if ids, ok := snapshot[key].([]string); ok && len(ids) != 0 {
			blockers = append(blockers, key)
		}
	}
	if records, ok := snapshot["attention_reservations"].([]map[string]any); ok && len(records) != 0 {
		blockers = append(blockers, "attention_reservations")
	}
	if snapshot["done_present"] == false || snapshot["done_closed"] == false {
		blockers = append(blockers, "completion_channel")
	}
	if snapshot["attention_fold_error"] != nil {
		blockers = append(blockers, "attention_fold_error")
	}
	snapshot["blocking_predicates"] = blockers
	return snapshot
}

func supervisionChannelClosed(ch <-chan struct{}) bool {
	return channelClosed(ch)
}
