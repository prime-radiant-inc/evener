package agent

import (
	"fmt"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
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
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	const attentionID = "attention:diagnostic-pending"
	if appended, err := sub.sess.appendDelegateNotificationDurably(attentionID, "diagnostic-sentinel"); err != nil || !appended {
		t.Fatalf("append diagnostic attention = %t, %v", appended, err)
	}
	// Incidental paths take the session lock briefly even with no run open,
	// and the snapshot records session_lock_busy rather than wait. Hold it
	// here as such a path would, so the uncontended keys below are read once
	// it is free.
	sub.sess.mu.Lock()
	released := make(chan struct{})
	go func() {
		defer close(released)
		time.Sleep(50 * time.Millisecond)
		sub.sess.mu.Unlock()
	}()
	snapshot := uncontendedStableSupervisionSnapshot(t, root, sub, fixture.adapter)
	<-released
	if !reflect.DeepEqual(snapshot["pending_attention_ids"], []string{attentionID}) || snapshot["current_run_open"] != false || snapshot["done_closed"] != true {
		t.Fatalf("snapshot omitted strict pending predicate or finished warm channel: %#v", snapshot)
	}
	if !reflect.DeepEqual(snapshot["blocking_predicates"], []string{"pending_attention_ids"}) {
		t.Fatalf("snapshot blocking predicates = %#v, want only durable pending attention", snapshot["blocking_predicates"])
	}
	for _, key := range []string{"arm_retry_ids", "attention_reservations", "controller_finalizing", "running", "driving", "subagent_finalizing", "done_present", "attention_resolutions", "attention_resume_generations", "settlement_claims", "pending_notifications", "notify_callback"} {
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
}

// Dropping a populated reservation, or flattening its admission flags, must
// lose evidence here even though the previous warm run is already quiescent.
func TestStableSupervisionFailureSnapshotCapturesAttentionReservation(t *testing.T) {
	t.Parallel()
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("no action")} },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
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
	if !reflect.DeepEqual(snapshot["attention_reservations"], wantReservation) || !reflect.DeepEqual(snapshot["blocking_predicates"], []string{"attention_reservations"}) {
		t.Fatalf("accepted reservation diagnostics = %#v", snapshot)
	}
	if !reflect.DeepEqual(snapshot["attention_resolutions"], map[string]delegateAttentionResolution{attentionID: delegateAttentionConsumed}) ||
		!reflect.DeepEqual(snapshot["attention_resume_generations"], map[string]uint64{attentionID: 2}) {
		t.Fatalf("accepted reservation transcript identity = %#v", snapshot)
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
}

// The no-action path keeps its real ordinary settlement claim until
// FinishNoAction, so the observer sees populated evidence rather than a mock.
func TestStableSupervisionFailureSnapshotCapturesSettlementClaim(t *testing.T) {
	t.Parallel()
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("no action")} },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	snapshots := make(chan map[string]any, 1)
	updateSessionTestConfig(sub.sess, func(cfg *testConfig) {
		cfg.subagentAfterFinalStatePublish = func(got *subagent) {
			snapshots <- stableSupervisionFailureSnapshot(root, got, fixture.adapter)
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
	wantClaim := []map[string]any{{"generation": uint64(2), "ready": true, "mode": delegateSettlementOrdinary}}
	if !reflect.DeepEqual(snapshot["settlement_claims"], wantClaim) || snapshot["settlement_claimed"] != true ||
		snapshot["binding_generation"] != uint64(2) || snapshot["binding_ready"] != true || snapshot["current_run_open"] != true {
		t.Fatalf("live settlement claim diagnostics = %#v", snapshot)
	}
	if snapshot["completion_requirement"] != delegateCompletionAttentionOnly || snapshot["completion_outcome"] != delegateCompletionOutcomeAttentionNoAction || snapshot["terminal_seen"] != false {
		t.Fatalf("no-action settlement evidence = %#v", snapshot)
	}
	if !reflect.DeepEqual(snapshot["attention_resolutions"], map[string]delegateAttentionResolution{attentionID: delegateAttentionConsumed}) ||
		!reflect.DeepEqual(snapshot["attention_resume_generations"], map[string]uint64{attentionID: 2}) ||
		!reflect.DeepEqual(snapshot["blocking_predicates"], []string{"subagent_finalizing", "current_run_open", "completion_channel"}) {
		t.Fatalf("settlement transcript or blockers = %#v", snapshot)
	}
	snapshot = uncontendedStableSupervisionSnapshot(t, root, sub, fixture.adapter)
	if !reflect.DeepEqual(snapshot["settlement_claims"], []map[string]any{}) || !reflect.DeepEqual(snapshot["blocking_predicates"], []string{}) {
		t.Fatalf("settled claim diagnostics = %#v", snapshot)
	}
}

func TestStableSupervisionFailureSnapshotCapturesArmRetryIDs(t *testing.T) {
	t.Parallel()
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("no action")} },
	}
	clock := agenttest.NewFakeClockAt(time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC))
	root := restoreSupervisionRoot(t, fixture, clock)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	if !sub.trySetDisposeGate() {
		t.Fatal("gate quiescent child during arm retry")
	}
	defer sub.clearDisposeGate()
	for _, id := range []string{"attention:diagnostic-z", "attention:diagnostic-a"} {
		if appended, err := sub.sess.appendDelegateNotificationDurably(id, "arm-retry-sentinel"); err != nil || !appended {
			t.Fatalf("append retry attention %q = %t, %v", id, appended, err)
		}
	}
	// Make the real filesystem read fail without changing or truncating the
	// writer's bytes. The fake clock keeps both admitted retries observable.
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
	for _, id := range []string{"attention:diagnostic-z", "attention:diagnostic-a"} {
		if err := sub.sess.armDelegateAttention(id); err == nil {
			t.Fatalf("arm %q against directory transcript succeeded", id)
		}
	}
	restore()
	restored = true
	snapshot := uncontendedStableSupervisionSnapshot(t, root, sub, fixture.adapter)
	if !reflect.DeepEqual(snapshot["arm_retry_ids"], []string{"attention:diagnostic-a", "attention:diagnostic-z"}) ||
		!reflect.DeepEqual(snapshot["pending_attention_ids"], []string{"attention:diagnostic-z", "attention:diagnostic-a"}) ||
		!reflect.DeepEqual(snapshot["blocking_predicates"], []string{"arm_retry_ids", "pending_attention_ids"}) {
		t.Fatalf("populated arm retry diagnostics = %#v", snapshot)
	}
	clock.Advance(jobNotificationRetryInitialDelay)
	clock.Drain()
	snapshot = uncontendedStableSupervisionSnapshot(t, root, sub, fixture.adapter)
	if !reflect.DeepEqual(snapshot["arm_retry_ids"], []string{}) || snapshot["needs_attention"] != true || snapshot["generation"] != uint64(1) {
		t.Fatalf("successful retry diagnostics = %#v", snapshot)
	}
	sub.clearDisposeGate()
	sub.sess.notify()
	waitForStableSupervisionRun(t, root, fixture.childID, fixture.adapter)
}

func TestStableSupervisionFailureSnapshotCapturesNotificationPayload(t *testing.T) {
	t.Parallel()
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
		func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("notification acknowledged")}
		},
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	if !sub.trySetDisposeGate() {
		t.Fatal("gate quiescent child during notification delivery")
	}
	defer sub.clearDisposeGate()
	started := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	ended := started.Add(time.Second)
	code := 7
	jm := sub.sess.jobManager
	for _, event := range []jobstore.Event{
		{Kind: jobstore.EventJobStarted, TS: started, JobID: "job_diagnostic", Type: jobstore.JobShell, OwnerSessionID: fixture.childID, StartedAt: &started, Description: "notification-label", Intent: "notification-intent"},
		{Kind: jobstore.EventJobFinished, TS: ended, JobID: "job_diagnostic", Status: jobstore.StatusFailed, Reason: "exit_nonzero", ExitCode: &code, EndedAt: &ended, OutputBytes: 42, TerminalGen: "diagnostic-generation"},
		{Kind: jobstore.EventJobNotificationPending, TS: ended, JobID: "job_diagnostic", TerminalGen: "diagnostic-generation"},
	} {
		if err := jm.forwardEvent(event); err != nil {
			t.Fatalf("route diagnostic job event %q: %v", event.Kind, err)
		}
	}
	snapshot := uncontendedStableSupervisionSnapshot(t, root, sub, fixture.adapter)
	want := []jobNotification{{
		Kind: jobNotificationKindTerminal, JobID: "job_diagnostic", JobType: "shell", Status: "failed", Reason: "exit_nonzero",
		Description: "notification-label", Intent: "notification-intent", TerminalGen: "diagnostic-generation", OutputBytes: 42, ExitCode: &code,
		TranscriptRef: "job:job_diagnostic",
	}}
	if !reflect.DeepEqual(snapshot["pending_notifications"], want) || snapshot["notify_callback"] == "0x0" || snapshot["notify_callback"] == nil {
		t.Fatalf("queued notification diagnostics = %#v", snapshot)
	}
	sub.clearDisposeGate()
	sub.sess.notify()
	waitForStableSupervisionRun(t, root, fixture.childID, fixture.adapter)
	snapshot = uncontendedStableSupervisionSnapshot(t, root, sub, fixture.adapter)
	if notifications, ok := snapshot["pending_notifications"].([]jobNotification); !ok || len(notifications) != 0 {
		t.Fatalf("drained notification diagnostics = %#v", snapshot)
	}
	records, err := jm.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if record := records["job_diagnostic"]; record == nil || record.NotifyState != jobstore.NotifyDelivered || record.TerminalGen != "diagnostic-generation" {
		t.Fatalf("durable notification delivery = %#v", record)
	}
}

// uncontendedStableSupervisionSnapshot is stableSupervisionFailureSnapshot
// taken once no lock it reads is busy. The snapshot never waits for a lock,
// so a test that checks its uncontended keys retries it instead.
func uncontendedStableSupervisionSnapshot(t *testing.T, root *Session, sub *subagent, adapter *fakeAdapter) map[string]any {
	t.Helper()
	var snapshot map[string]any
	waitForCondition(t, 5*time.Second, "a snapshot with no busy lock", func() bool {
		snapshot = stableSupervisionFailureSnapshot(root, sub, adapter)
		for key := range snapshot {
			if strings.HasSuffix(key, "_lock_busy") {
				return false
			}
		}
		return true
	})
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
		ids := keys(sess.delegateAttentionArmIDs)
		sort.Strings(ids)
		snapshot["arm_retry_ids"] = ids
		sess.attentionMu.Unlock()
	} else {
		snapshot["attention_lock_busy"] = true
	}
	if sess.mu.TryLock() {
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
	for _, key := range []string{"running", "driving", "subagent_finalizing", "current_run_open", "controller_finalizing"} {
		if snapshot[key] == true {
			blockers = append(blockers, key)
		}
	}
	for _, key := range []string{"arm_retry_ids", "pending_attention_ids"} {
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
