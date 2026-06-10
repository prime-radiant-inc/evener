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
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestConfigureWatchRequiresCondition(t *testing.T) {
	jm := newTestJM(t)
	_, err := jm.configureWatch(watchArgs{Target: "caller"})
	if err == nil {
		t.Fatal("a watch with no condition and clear=false must error")
	}
}

func TestConfigureWatchRejectsNegativeProgressInterval(t *testing.T) {
	jm := newTestJM(t)
	_, err := jm.configureWatch(watchArgs{Target: "caller", ProgressIntervalMS: -1})
	if err == nil {
		t.Fatal("negative progress interval must error")
	}
	if !strings.Contains(err.Error(), "progress_interval_ms must be non-negative") {
		t.Fatalf("error = %v, want progress_interval_ms validation", err)
	}
}

func TestConfigureWatchClampsProgressInterval(t *testing.T) {
	jm := newTestJM(t)
	t.Cleanup(func() { _ = jm.close() })
	res, err := jm.configureWatch(watchArgs{Target: "caller", ProgressIntervalMS: 10})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if res.ProgressIntervalMS != minWatchProgressIntervalMS {
		t.Fatalf("progress interval = %d, want %d", res.ProgressIntervalMS, minWatchProgressIntervalMS)
	}
}

func TestConfigureWatchTargetNotFound(t *testing.T) {
	jm := newTestJM(t)
	_, err := jm.configureWatch(watchArgs{Target: "job_does_not_exist", OutputMatch: "ready"})
	if err == nil {
		t.Fatal("an unknown concrete job target must error (target_not_found)")
	}
}

func TestJobWatchMainAliasTargetFailsTargetNotFound(t *testing.T) {
	jm := newTestJM(t)

	_, err := jm.configureWatch(watchArgs{Target: "main"})

	if err == nil || !strings.Contains(err.Error(), "target_not_found") {
		t.Fatalf("error = %v, want target_not_found", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", jm.watchCount())
	}
}

func TestJobWatchWatchedTargetWithoutContextFails(t *testing.T) {
	jm := newTestJM(t)

	_, err := jm.configureWatch(watchArgs{Target: "watched"})

	if err == nil || !strings.Contains(err.Error(), "target_not_found") {
		t.Fatalf("error = %v, want target_not_found", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", jm.watchCount())
	}
}

func TestConfigureWatchRejectsOutputMatchOnSessionTargets(t *testing.T) {
	jm := newTestJM(t)
	for _, target := range []string{"caller", "*"} {
		t.Run(target, func(t *testing.T) {
			_, err := jm.configureWatch(watchArgs{Target: target, OutputMatch: "ready"})
			if err == nil {
				t.Fatal("session target output_match watch must error")
			}
			if !strings.Contains(err.Error(), "output_match requires a concrete job target") {
				t.Fatalf("error = %v, want output_match concrete-target validation", err)
			}
		})
	}
}

func TestJobWatchSendToMainAliasFailsTargetNotFound(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})

	_, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "main", Message: "observe"},
	})

	if err == nil || !strings.Contains(err.Error(), "target_not_found") {
		t.Fatalf("error = %v, want target_not_found", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", jm.watchCount())
	}
}

func TestJobWatchSendToKnownShellJobFailsTargetNotMessageable(t *testing.T) {
	jm := newTestJM(t)
	watched, _ := jm.createShell(createShellOpts{Command: "watched"})
	observer, _ := jm.createShell(createShellOpts{Command: "observer"})

	_, err := jm.configureWatch(watchArgs{
		Target:      watched.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: observer.JobID, Message: "observe"},
	})

	if err == nil || !strings.Contains(err.Error(), "target_not_messageable") {
		t.Fatalf("error = %v, want target_not_messageable", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", jm.watchCount())
	}
}

func TestJobWatchSendToWatchedKnownShellJobFailsTargetNotMessageable(t *testing.T) {
	jm := newTestJM(t)
	watched, _ := jm.createShell(createShellOpts{Command: "watched"})

	_, err := jm.configureWatch(watchArgs{
		Target:      watched.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "watched", Message: "observe"},
	})

	if err == nil || !strings.Contains(err.Error(), "target_not_messageable") {
		t.Fatalf("error = %v, want target_not_messageable", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", jm.watchCount())
	}
}

func TestConfigureWatchRejectsTerminalizingConcreteJob(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})

	jm.mu.Lock()
	jm.running[rec.JobID].finalize = &finalizeAttempt{done: make(chan struct{})}
	jm.mu.Unlock()

	_, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if err == nil {
		t.Fatal("a terminalizing concrete job must not accept new watches")
	}
	if !strings.Contains(err.Error(), "target_not_found") {
		t.Fatalf("error = %v, want target_not_found", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("terminalizing job watch was registered; count = %d", jm.watchCount())
	}
}

func TestJobWatchRejectsConcreteJobWithoutRunningRuntime(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})

	jm.mu.Lock()
	delete(jm.running, rec.JobID)
	jm.mu.Unlock()

	_, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if err == nil {
		t.Fatal("a concrete job without a running runtime must not accept new watches")
	}
	if !strings.Contains(err.Error(), "target_not_found") {
		t.Fatalf("error = %v, want target_not_found", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("inert concrete job watch was registered; count = %d", jm.watchCount())
	}
}

func TestConfigureWatchIdempotentAndReplace(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	first, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if first.ReplacedExisting {
		t.Error("first watch must not report replaced_existing")
	}

	same, _ := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if same.ReplacedExisting {
		t.Error("identical re-config must be idempotent, not a replacement")
	}

	diff, _ := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "blocked"})
	if !diff.ReplacedExisting {
		t.Error("changed config on the same key must report replaced_existing")
	}
}

func TestClearWatchRemovesIt(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, _ = jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if jm.watchCount() != 0 {
		t.Errorf("clear must remove the watch; count = %d", jm.watchCount())
	}
}

func TestEventWatchFiresAndNotifiesCaller(t *testing.T) {
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	if _, err := jm.configureWatch(watchArgs{Target: "caller", Events: []string{"assistant.message"}}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.onSessionEvent(events.EventAssistantTextEnd, nil)

	if len(notified) != 1 {
		t.Fatalf("an assistant.message event must notify the caller once, got %d", len(notified))
	}
}

func TestEventWatchTriggerEveryNth(t *testing.T) {
	jm := newTestJM(t)
	var fires int
	jm.enqueue = func(jobNotification) { fires++ }

	_, err := jm.configureWatch(watchArgs{
		Target:       "caller",
		Events:       []string{"assistant.message"},
		TriggerEvent: "assistant.message",
		TriggerEvery: 3,
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	for i := 0; i < 7; i++ {
		jm.onSessionEvent(events.EventAssistantTextEnd, nil)
	}
	if fires != 2 {
		t.Errorf("trigger.every=3 over 7 events should fire twice, got %d", fires)
	}
}

func TestEventWatchIgnoresUnwatchedKind(t *testing.T) {
	jm := newTestJM(t)
	var fires int
	jm.enqueue = func(jobNotification) { fires++ }
	_, _ = jm.configureWatch(watchArgs{Target: "caller", Events: []string{"assistant.message"}})
	jm.onSessionEvent(events.EventToolCallEnd, nil)
	if fires != 0 {
		t.Errorf("an unwatched event kind must not fire; fires = %d", fires)
	}
}

func TestEventWatchIgnoresWatchOriginatedSubagentEvents(t *testing.T) {
	jm := newTestJM(t)
	var sent []sendMessageArgs
	jm.send = func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}
	if _, err := jm.configureWatch(watchArgs{
		Target: "caller",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_obs", JobType: "delegate", Status: "completed", FromWatch: true})
	if len(sent) != 0 {
		t.Fatalf("watch-originated job event retriggered watch send: %#v", sent)
	}

	jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_worker", JobType: "delegate", Status: "completed"})
	if len(sent) != 1 {
		t.Fatalf("ordinary job event must trigger one watch send, got %d", len(sent))
	}
}

func TestWatchOriginSuppressesDelegateLifecycleWatchSends(t *testing.T) {
	jm := newTestJM(t)
	var sent []sendMessageArgs
	jm.send = func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}
	if _, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "watched", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	jm.onSessionEvent(events.EventJobStarted, events.JobStartedData{JobID: "job_obs", JobType: "delegate", Status: "running", FromWatch: true})
	jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_obs", JobType: "delegate", Status: "completed", FromWatch: true})

	if len(sent) != 0 {
		t.Fatalf("watch-originated delegate lifecycle events triggered watch sends: %#v", sent)
	}
}

func TestConcreteJobEventWatchSendsFrame(t *testing.T) {
	jm := newTestJM(t)
	var sent []sendMessageArgs
	jm.send = func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, err := jm.configureWatch(watchArgs{
		Target: rec.JobID,
		Events: []string{"assistant.tool"},
		Send:   &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.onSessionEvent(events.EventToolCallEnd, nil)

	if len(sent) != 1 {
		t.Fatalf("concrete job event watch must send once, got %d", len(sent))
	}
	if sent[0].Target != "job_obs" {
		t.Fatalf("delivery target = %q, want job_obs", sent[0].Target)
	}
	if !strings.Contains(sent[0].Message, "observe") ||
		!strings.Contains(sent[0].Message, rec.JobID) ||
		!strings.Contains(sent[0].Message, "event: TOOL_CALL_END") {
		t.Fatalf("delivery frame = %q, want configured message, job id, and trigger", sent[0].Message)
	}
}

func TestOutputMatchWatchFiresOnAppendedBytes(t *testing.T) {
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "(?i)ready"}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("booting\nserver READY\n")); err != nil {
		t.Fatalf("append: %v", err)
	}

	if len(notified) != 1 {
		t.Fatalf("output_match must fire once on the matching appended line, got %d", len(notified))
	}
}

func TestConcreteWatchExpiresOnTerminal(t *testing.T) {
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, _ = jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"})
	if jm.watchCount() != 1 {
		t.Fatalf("watch not registered")
	}
	code := 0
	jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code)
	if jm.watchCount() != 0 {
		t.Errorf("a concrete-job watch must expire when the job goes terminal; count = %d", jm.watchCount())
	}
}

func TestSessionWatchSurvivesAJobTerminal(t *testing.T) {
	jm := newTestJM(t)
	jm.enqueue = func(jobNotification) {}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, _ = jm.configureWatch(watchArgs{Target: "caller", Events: []string{"assistant.message"}})
	code := 0
	jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code)
	if jm.watchCount() != 1 {
		t.Errorf("a session-alias watch must survive a job going terminal; count = %d", jm.watchCount())
	}
}

func TestConcreteWatchFlushesBeforeTerminalNotification(t *testing.T) {
	jm := newTestJM(t)
	var order []string
	jm.enqueue = func(n jobNotification) {
		if n.Status == jobNotificationEventWatch {
			order = append(order, "watch")
		} else {
			order = append(order, "terminal")
		}
	}

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready")); err != nil {
		t.Fatalf("append: %v", err)
	}

	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if strings.Join(order, ",") != "watch,terminal" {
		t.Fatalf("notification order = %v, want watch before terminal", order)
	}
}

func TestWatchSendDeliversFrameToTarget(t *testing.T) {
	jm := newTestJM(t)
	var sent []sendMessageArgs
	jm.send = func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "(?i)ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "saw ready", IncludeFrame: true},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.feedJobOutput(rec.JobID, []byte("server READY\n"))

	if len(sent) != 1 {
		t.Fatalf("a send watch must deliver once, got %d", len(sent))
	}
	if sent[0].Target != "job_obs" {
		t.Errorf("delivery target = %q, want job_obs", sent[0].Target)
	}
	if !sent[0].Background || !sent[0].BackgroundSet || !sent[0].FromWatch {
		t.Errorf("delivery args = %+v, want background watch send", sent[0])
	}
	if !strings.Contains(sent[0].Message, "saw ready") {
		t.Errorf("delivery must carry the configured message + frame; got %q", sent[0].Message)
	}
	if !strings.Contains(sent[0].Message, "output_match: server READY") {
		t.Errorf("delivery frame must carry the match trigger; got %q", sent[0].Message)
	}
}

func TestWatchSendBusyKeepsPendingAndEmitsNoDiagnostic(t *testing.T) {
	jm := newTestJM(t)
	var sent []sendMessageArgs
	jm.send = func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return busyWatchSendResult()
	}
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.feedJobOutput(rec.JobID, []byte("server ready\n"))

	if len(sent) != 1 {
		t.Fatalf("send attempts = %d, want 1", len(sent))
	}
	if len(notified) != 0 {
		t.Fatalf("busy send emitted diagnostics: %+v", notified)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending after busy send = %d, want 1: %+v", len(pending), pending)
	}
}

func TestWatchSendRetryAfterIdleDeliversLatestCoalescedFrame(t *testing.T) {
	jm := newTestJM(t)
	target := createRunningDelegateWatchTarget(t, jm)
	busy := true
	var delivered []sendMessageArgs
	jm.send = func(_ context.Context, a sendMessageArgs) sendMessageResult {
		if busy {
			return busyWatchSendResult()
		}
		delivered = append(delivered, a)
		return sendMessageResult{}
	}

	source, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      source.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: target.JobID, Message: "observe", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.feedJobOutput(source.JobID, []byte("first ready\n"))
	jm.feedJobOutput(source.JobID, []byte("second ready\n"))
	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("pending before retry = %d, want 1: %+v", len(pending), pending)
	}
	for _, state := range pending {
		if state.CoalescedCount != 1 {
			t.Fatalf("coalesced_count = %d, want 1", state.CoalescedCount)
		}
		if !strings.Contains(state.Frame, "second ready") || strings.Contains(state.Frame, "first ready") {
			t.Fatalf("pending frame = %q, want latest coalesced frame only", state.Frame)
		}
	}

	busy = false
	code := 0
	if err := jm.finalize(target.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize target: %v", err)
	}

	if len(delivered) != 1 {
		t.Fatalf("retry delivered sends = %d, want 1", len(delivered))
	}
	if !strings.Contains(delivered[0].Message, "second ready") || strings.Contains(delivered[0].Message, "first ready") {
		t.Fatalf("retry message = %q, want latest coalesced frame", delivered[0].Message)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after retry = %+v, want none", pending)
	}
}

func TestWatchSendDeliveredAppendedOnlyAfterSendSucceeds(t *testing.T) {
	jm := newTestJM(t)
	var eventsBeforeSendReturn []jobstore.EventKind
	var eventKinds []jobstore.EventKind
	realAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		eventKinds = append(eventKinds, e.Kind)
		return realAppend(e)
	}
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		eventsBeforeSendReturn = append(eventsBeforeSendReturn, eventKinds...)
		return sendMessageResult{}
	}

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.feedJobOutput(rec.JobID, []byte("server ready\n"))

	if containsEventKind(eventsBeforeSendReturn, jobstore.EventWatchSendDelivered) {
		t.Fatalf("delivered event was appended before send returned: %v", eventsBeforeSendReturn)
	}
	if !eventKindOrder(eventKinds, jobstore.EventWatchSendPending, jobstore.EventWatchSendDelivered) {
		t.Fatalf("event order = %v, want pending before delivered after send", eventKinds)
	}
}

func TestWatchSendCrashAfterSuccessBeforeDeliveredRetriesSameDeliveryID(t *testing.T) {
	stateDir := t.TempDir()
	jm, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	var sent []sendMessageArgs
	jm.send = func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}
	realAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchSendDelivered {
			return errors.New("crash before delivered marker")
		}
		return realAppend(e)
	}

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.feedJobOutput(rec.JobID, []byte("server ready\n"))
	if len(sent) != 1 {
		t.Fatalf("initial sends = %d, want 1", len(sent))
	}
	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("pending after failed delivered marker = %d, want 1", len(pending))
	}
	var deliveryID string
	for _, state := range pending {
		deliveryID = state.DeliveryID
	}
	if deliveryID == "" || !strings.Contains(sent[0].Message, "delivery_id: "+deliveryID) {
		t.Fatalf("initial frame %q missing delivery_id %q", sent[0].Message, deliveryID)
	}
	if err := jm.store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	reopened, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("reopen job manager: %v", err)
	}
	defer reopened.store.Close()
	var retried []sendMessageArgs
	reopened.send = func(_ context.Context, a sendMessageArgs) sendMessageResult {
		retried = append(retried, a)
		return sendMessageResult{}
	}
	if err := reopened.retryPendingWatchSendsForTarget(context.Background(), "job_obs"); err != nil {
		t.Fatalf("retry pending: %v", err)
	}

	if len(retried) != 1 {
		t.Fatalf("retry sends = %d, want 1", len(retried))
	}
	if !strings.Contains(retried[0].Message, "delivery_id: "+deliveryID) {
		t.Fatalf("retry frame %q missing same delivery_id %q", retried[0].Message, deliveryID)
	}
}

func TestWatchSendRestoreRetriesPendingBeforeTerminalNotifications(t *testing.T) {
	stateDir := t.TempDir()
	sessionID := "01KTESTWATCHRESTORE0000000000"
	jobID := "job_restore_idle"
	now := time.Unix(1000, 0).UTC()
	endedAt := now.Add(time.Second)
	resumable := true

	if err := os.MkdirAll(jobsDir(stateDir, sessionID), 0o755); err != nil {
		t.Fatalf("mkdir jobs dir: %v", err)
	}
	st, err := jobstore.Open(jobsDir(stateDir, sessionID) + "/jobs.jsonl")
	if err != nil {
		t.Fatalf("open job store: %v", err)
	}
	for _, event := range []jobstore.Event{
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
			TranscriptRef: encodeRef("", "child_restore_idle"),
			Resumable:     &resumable,
		},
		{
			Kind:        jobstore.EventJobFinished,
			TS:          endedAt,
			JobID:       jobID,
			Status:      jobstore.StatusCompleted,
			Reason:      "exit_zero",
			EndedAt:     &endedAt,
			TerminalGen: "term_restore_idle",
		},
		{
			Kind: jobstore.EventWatchSendPending,
			TS:   endedAt,
			WatchSend: &jobstore.WatchSendState{
				Key: jobstore.WatchSendKey{
					VisibleSessionID:        sessionID,
					WatchTarget:             jobID,
					ResolvedWatchedIdentity: jobID,
					ResolvedSendTo:          runtimeMessageAliasCaller,
					WatchGeneration:         "watch_restore_generation",
				},
				DeliveryID:      "delivery_restore_pending",
				UpdateSeq:       1,
				Message:         "restored observe",
				Frame:           "restored observe\n\ndelivery_id: delivery_restore_pending",
				TriggerIdentity: jobID,
				TriggerReason:   "output_match: ready",
				CreatedAt:       endedAt,
				UpdatedAt:       endedAt,
			},
		},
	} {
		if err := st.Append(event); err != nil {
			t.Fatalf("append %s: %v", event.Kind, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close job store: %v", err)
	}

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	meta := schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    (SessionConfig{NoProjectPrompts: true}).toSnapshot(),
		CreatedAt: now,
		UpdatedAt: now,
	}
	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("restore session: %v", err)
	}
	defer restored.Close()

	queue := restored.SteeringQueueSnapshot()
	if len(queue) != 1 {
		t.Fatalf("steering queue = %+v, want restored pending watch send", queue)
	}
	if !strings.Contains(queue[0].Text, "restored observe") ||
		!strings.Contains(queue[0].Text, "delivery_id: delivery_restore_pending") {
		t.Fatalf("restored watch send text = %q, want stored frame with delivery id", queue[0].Text)
	}
	if pending := loadWatchSendRecord(t, restored.jobManager).Pending; len(pending) != 0 {
		t.Fatalf("pending after restore retry = %+v, want none", pending)
	}

	events := loadJobStoreEvents(t, restored.jobManager)
	deliveredSeq := int64(0)
	notificationSeq := int64(0)
	for _, event := range events {
		switch event.Kind {
		case jobstore.EventWatchSendDelivered:
			deliveredSeq = event.Seq
		case jobstore.EventJobNotificationPending:
			notificationSeq = event.Seq
		}
	}
	if deliveredSeq == 0 || notificationSeq == 0 || deliveredSeq > notificationSeq {
		t.Fatalf("event order delivered=%d notification_pending=%d, want delivered before notification pending", deliveredSeq, notificationSeq)
	}
}

func TestWatchSendRestoreRetriesConcreteTerminalResumableDelegateWithoutRetainedChild(t *testing.T) {
	stateDir := t.TempDir()
	sessionID := "S1"
	jobID := "job_restore_delegate"
	now := time.Unix(1000, 0).UTC()
	resumable := true

	jm, err := newJobManager(stateDir, sessionID, func(jobNotification) {})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	for _, event := range restoredWatchSendDelegateEvents(sessionID, jobID, now, &resumable, jobID) {
		if err := jm.appendEvent(event); err != nil {
			t.Fatalf("append %s: %v", event.Kind, err)
		}
	}
	if err := jm.store.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	reopened, err := newJobManager(stateDir, sessionID, func(jobNotification) {})
	if err != nil {
		t.Fatalf("reopen job manager: %v", err)
	}
	defer reopened.store.Close()
	var sent []sendMessageArgs
	reopened.send = func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}
	s := &Session{
		id:         sessionID,
		jobManager: reopened,
		subagents:  newSubagentManager(nil),
	}

	if err := s.retryRestoredPendingWatchSends(context.Background()); err != nil {
		t.Fatalf("retry restored pending: %v", err)
	}
	if err := reopened.armPendingTerminalNotifications(); err != nil {
		t.Fatalf("arm terminal notifications: %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("restored concrete delegate sends = %d, want 1", len(sent))
	}
	if sent[0].Target != jobID {
		t.Fatalf("send target = %q, want %q", sent[0].Target, jobID)
	}
	if pending := loadWatchSendRecord(t, reopened).Pending; len(pending) != 0 {
		t.Fatalf("pending after restore retry = %+v, want none", pending)
	}
	events := loadJobStoreEvents(t, reopened)
	deliveredSeq := int64(0)
	notificationSeq := int64(0)
	for _, event := range events {
		switch event.Kind {
		case jobstore.EventWatchSendDelivered:
			deliveredSeq = event.Seq
		case jobstore.EventJobNotificationPending:
			notificationSeq = event.Seq
		}
	}
	if deliveredSeq == 0 || notificationSeq == 0 || deliveredSeq > notificationSeq {
		t.Fatalf("event order delivered=%d notification_pending=%d, want delivered before notification pending", deliveredSeq, notificationSeq)
	}
}

func TestWatchSendRestoreDropsHardFailureTargetsOnce(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sendTo   string
		events   func(string, time.Time) []jobstore.Event
		wantText string
	}{
		{
			name:     "unknown",
			sendTo:   "job_missing",
			events:   func(string, time.Time) []jobstore.Event { return nil },
			wantText: "target_not_found",
		},
		{
			name:   "non_messageable",
			sendTo: "job_shell",
			events: func(sessionID string, now time.Time) []jobstore.Event {
				return []jobstore.Event{{
					Kind:             jobstore.EventJobStarted,
					TS:               now,
					JobID:            "job_shell",
					Type:             jobstore.JobShell,
					OwnerSessionID:   sessionID,
					VisibleToSession: sessionID,
					StartedAt:        &now,
				}}
			},
			wantText: "target_not_messageable",
		},
		{
			name:   "non_resumable",
			sendTo: "job_not_resumable",
			events: func(sessionID string, now time.Time) []jobstore.Event {
				resumable := false
				return restoredWatchSendDelegateEvents(sessionID, "job_not_resumable", now, &resumable, "job_not_resumable")[:3]
			},
			wantText: "target_not_resumable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			sessionID := "S1"
			now := time.Unix(1000, 0).UTC()
			var notified []jobNotification
			jm, err := newJobManager(stateDir, sessionID, func(n jobNotification) { notified = append(notified, n) })
			if err != nil {
				t.Fatalf("new job manager: %v", err)
			}
			for _, event := range tc.events(sessionID, now) {
				if err := jm.appendEvent(event); err != nil {
					t.Fatalf("append %s: %v", event.Kind, err)
				}
			}
			for _, event := range restoredWatchSendPendingEvents(sessionID, "job_watched", tc.sendTo, now) {
				if err := jm.appendEvent(event); err != nil {
					t.Fatalf("append %s: %v", event.Kind, err)
				}
			}
			if err := jm.store.Close(); err != nil {
				t.Fatalf("close seed store: %v", err)
			}
			reopened, err := newJobManager(stateDir, sessionID, func(n jobNotification) { notified = append(notified, n) })
			if err != nil {
				t.Fatalf("reopen job manager: %v", err)
			}
			defer reopened.store.Close()
			reopened.send = func(context.Context, sendMessageArgs) sendMessageResult {
				t.Fatal("hard-failure restored target should not call send")
				return sendMessageResult{}
			}
			s := &Session{
				id:         sessionID,
				jobManager: reopened,
				subagents:  newSubagentManager(nil),
			}

			if err := s.retryRestoredPendingWatchSends(context.Background()); err != nil {
				t.Fatalf("first retry restored pending: %v", err)
			}
			if err := s.retryRestoredPendingWatchSends(context.Background()); err != nil {
				t.Fatalf("second retry restored pending: %v", err)
			}

			if pending := loadWatchSendRecord(t, reopened).Pending; len(pending) != 0 {
				t.Fatalf("pending after hard failure retry = %+v, want none", pending)
			}
			if len(notified) != 1 {
				t.Fatalf("diagnostics = %d, want exactly 1: %+v", len(notified), notified)
			}
			if !strings.Contains(notified[0].Reason, "delivery_id=delivery_restore_pending") ||
				!strings.Contains(notified[0].Reason, tc.wantText) {
				t.Fatalf("diagnostic reason = %q, want delivery id and %q", notified[0].Reason, tc.wantText)
			}
		})
	}
}

func TestWatchSendHardFailureDropsPendingAndDiagnosesOnceAcrossRestores(t *testing.T) {
	stateDir := t.TempDir()
	var notified []jobNotification
	jm, err := newJobManager(stateDir, "S1", func(n jobNotification) { notified = append(notified, n) })
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return hardWatchSendResult(errors.New("target_not_messageable"))
	}

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.feedJobOutput(rec.JobID, []byte("server ready\n"))
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after hard failure = %+v, want none", pending)
	}
	if len(notified) != 1 {
		t.Fatalf("diagnostics after hard failure = %d, want 1: %+v", len(notified), notified)
	}
	if !strings.Contains(notified[0].Reason, "delivery_id=") {
		t.Fatalf("diagnostic reason = %q, want delivery id", notified[0].Reason)
	}
	if err := jm.store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	reopened, err := newJobManager(stateDir, "S1", func(n jobNotification) { notified = append(notified, n) })
	if err != nil {
		t.Fatalf("first reopen: %v", err)
	}
	reopened.send = jm.send
	if err := reopened.retryPendingWatchSendsForTarget(context.Background(), "job_obs"); err != nil {
		t.Fatalf("first retry: %v", err)
	}
	if err := reopened.store.Close(); err != nil {
		t.Fatalf("close reopened store: %v", err)
	}
	second, err := newJobManager(stateDir, "S1", func(n jobNotification) { notified = append(notified, n) })
	if err != nil {
		t.Fatalf("second reopen: %v", err)
	}
	defer second.store.Close()
	second.send = jm.send
	if err := second.retryPendingWatchSendsForTarget(context.Background(), "job_obs"); err != nil {
		t.Fatalf("second retry: %v", err)
	}

	if len(notified) != 1 {
		t.Fatalf("diagnostics across restores = %d, want exactly 1: %+v", len(notified), notified)
	}
}

func TestWatchSendTerminalOrderingSendsFinalFrameBeforeTerminalNotification(t *testing.T) {
	jm := newTestJM(t)
	var order []string
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		order = append(order, "send")
		return sendMessageResult{}
	}
	jm.enqueue = func(n jobNotification) {
		if n.Status != jobNotificationEventWatch {
			order = append(order, "terminal")
		}
	}

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready")); err != nil {
		t.Fatalf("append output: %v", err)
	}
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if strings.Join(order, ",") != "send,terminal" {
		t.Fatalf("order = %v, want send before terminal", order)
	}
}

func TestWatchSendTerminalPendingPersistenceFailureRetriesFinalization(t *testing.T) {
	jm := newTestJM(t)
	var sent []sendMessageArgs
	jm.send = func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready")); err != nil {
		t.Fatalf("append output: %v", err)
	}
	appendErr := errors.New("pending append failed")
	realAppend := jm.appendEvent
	blocked := true
	jm.appendEvent = func(e jobstore.Event) error {
		if blocked && e.Kind == jobstore.EventWatchSendPending {
			return appendErr
		}
		return realAppend(e)
	}

	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); !errors.Is(err, appendErr) {
		t.Fatalf("finalize err = %v, want pending append failure", err)
	}
	if len(sent) != 0 {
		t.Fatalf("final watch send delivered despite failed pending persistence: %#v", sent)
	}
	jobs := jm.list(listFilter{})
	if len(jobs) != 1 || jobs[0].Status != jobstore.StatusCompleted {
		t.Fatalf("job state after failed finalization = %+v, want terminal retained", jobs)
	}
	if jobs[0].NotifyState != "" && jobs[0].NotifyState != jobstore.NotifyNotArmed {
		t.Fatalf("notify state after failed finalization = %q, want not armed", jobs[0].NotifyState)
	}

	blocked = false
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("retry finalize: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("final watch send after retry = %d, want 1", len(sent))
	}
	if !strings.Contains(sent[0].Message, "output_match: server ready") {
		t.Fatalf("retried final watch frame = %q, want original final trigger", sent[0].Message)
	}
	jobs = jm.list(listFilter{})
	if len(jobs) != 1 || jobs[0].NotifyState != jobstore.NotifyPending {
		t.Fatalf("job state after retry finalization = %+v, want notification pending", jobs)
	}
}

func TestWatchSendToWatchedDeliversFrameToConcreteTarget(t *testing.T) {
	jm := newTestJM(t)
	var sent []sendMessageArgs
	jm.send = func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}

	rec := createRunningDelegateWatchTarget(t, jm)
	_, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "(?i)ready",
		Send:        &watchSendArgs{To: "watched", Message: "saw ready"},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.feedJobOutput(rec.JobID, []byte("server READY\n"))

	if len(sent) != 1 {
		t.Fatalf("a send watch must deliver once, got %d", len(sent))
	}
	if sent[0].Target != rec.JobID {
		t.Fatalf("delivery target = %q, want watched job %q", sent[0].Target, rec.JobID)
	}
}

func TestWatchSendToWatchedWildcardJobNotificationDeliversConcreteTarget(t *testing.T) {
	jm := newTestJM(t)
	var sent []sendMessageArgs
	jm.send = func(_ context.Context, a sendMessageArgs) sendMessageResult {
		sent = append(sent, a)
		return sendMessageResult{}
	}

	_, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "watched", Message: "observe"},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_delegate", JobType: "delegate", Status: "completed"})

	if len(sent) != 1 {
		t.Fatalf("wildcard job notification must deliver once, got %d", len(sent))
	}
	if sent[0].Target != "job_delegate" {
		t.Fatalf("delivery target = %q, want concrete watched job", sent[0].Target)
	}
}

func TestWatchSendPendingSnapshotCoalescesAndDoesNotRereadOutput(t *testing.T) {
	jm := newTestJM(t)
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "(?i)ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "saw ready", IncludeFrame: true, IncludeExcerpt: true},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("first READY\ninitial excerpt\n")); err != nil {
		t.Fatalf("append first output: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("second READY\nlatest excerpt\n")); err != nil {
		t.Fatalf("append second output: %v", err)
	}
	if _, err := jm.running[rec.JobID].output.Append([]byte("do not reread\n")); err != nil {
		t.Fatalf("append later output: %v", err)
	}

	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("pending count = %d, want 1: %+v", len(pending), pending)
	}
	var state *jobstore.WatchSendState
	for _, pendingState := range pending {
		state = pendingState
	}
	if state.CoalescedCount != 1 {
		t.Fatalf("coalesced_count = %d, want 1", state.CoalescedCount)
	}
	if !strings.Contains(state.Frame, "second READY") || !strings.Contains(state.Frame, "latest excerpt") {
		t.Fatalf("pending frame did not snapshot latest trigger/output: %q", state.Frame)
	}
	if strings.Contains(state.Frame, "do not reread") {
		t.Fatalf("pending frame reread later output: %q", state.Frame)
	}
}

func TestWatchSendPendingUsesTriggerTimeFrameSnapshot(t *testing.T) {
	jm := newTestJM(t)
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true, IncludeExcerpt: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.running[rec.JobID].output.Append([]byte("server ready\ninitial excerpt\n")); err != nil {
		t.Fatalf("append trigger output: %v", err)
	}
	delivery := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: server ready")
	if _, err := jm.running[rec.JobID].output.Append([]byte("later output must not be snapshotted\n")); err != nil {
		t.Fatalf("append later output: %v", err)
	}

	jm.deliverWatchSend(context.Background(), delivery)

	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("pending count = %d, want 1: %+v", len(pending), pending)
	}
	for _, state := range pending {
		if !strings.Contains(state.Frame, "initial excerpt") {
			t.Fatalf("pending frame = %q, want trigger-time excerpt", state.Frame)
		}
		if strings.Contains(state.Frame, "later output must not be snapshotted") {
			t.Fatalf("pending frame reread output after trigger: %q", state.Frame)
		}
	}
}

func TestWatchSendGenerationChangesAfterRestoreAndReplacementDropsOldPending(t *testing.T) {
	stateDir := t.TempDir()
	jm, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "(?i)ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "first generation", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure first watch: %v", err)
	}
	jm.feedJobOutput(rec.JobID, []byte("first READY\n"))
	firstPending := loadWatchSendRecord(t, jm).Pending
	if len(firstPending) != 1 {
		t.Fatalf("first pending count = %d, want 1", len(firstPending))
	}
	var firstKey jobstore.WatchSendKey
	for key := range firstPending {
		firstKey = key
	}
	if err := jm.store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	reopened, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("reopen job manager: %v", err)
	}
	reopened.now = func() time.Time { return time.Unix(1001, 0).UTC() }
	reopened.send = jm.send
	output, err := jobstore.OpenOutput(reopened.outputPathForJob(rec, rec.JobID), maxJobOutputRetentionBytes)
	if err != nil {
		t.Fatalf("reopen output: %v", err)
	}
	reopened.running[rec.JobID] = &runningJob{rec: rec, output: output, done: make(chan struct{})}
	t.Cleanup(func() { _ = output.Close() })
	if _, err := reopened.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "(?i)ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "second generation", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure second watch: %v", err)
	}
	reopened.feedJobOutput(rec.JobID, []byte("second READY\n"))

	pending := loadWatchSendRecord(t, reopened).Pending
	if len(pending) != 1 {
		t.Fatalf("pending count after restore replacement = %d, want 1: %+v", len(pending), pending)
	}
	if _, ok := pending[firstKey]; ok {
		t.Fatalf("old restored pending key survived replacement cleanup: %+v", pending)
	}
	for key, state := range pending {
		if key.WatchGeneration == firstKey.WatchGeneration {
			t.Fatalf("watch generation reused after restore: %q", key.WatchGeneration)
		}
		if !strings.Contains(state.Frame, "second READY") {
			t.Fatalf("new pending frame = %q, want second trigger", state.Frame)
		}
		return
	}
	t.Fatal("new pending key not found")
}

func TestWatchSendRestoreLoadsPendingStateForFutureRetry(t *testing.T) {
	stateDir := t.TempDir()
	jm, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true, IncludeExcerpt: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready\nstored excerpt\n")); err != nil {
		t.Fatalf("append output: %v", err)
	}
	delivery := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: server ready")
	jm.deliverWatchSend(context.Background(), delivery)
	folded := loadWatchSendRecord(t, jm).Pending
	if len(folded) != 1 {
		t.Fatalf("folded pending before restore = %d, want 1", len(folded))
	}
	var wantFrame string
	for _, state := range folded {
		wantFrame = state.Frame
	}
	if err := jm.store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	reopened, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("reopen job manager: %v", err)
	}
	t.Cleanup(func() { _ = reopened.store.Close() })

	restored := runtimeWatchSendPending(t, reopened)
	if len(restored) != 1 {
		t.Fatalf("runtime pending after restore = %d, want 1: %+v", len(restored), restored)
	}
	for _, state := range restored {
		if state.Frame != wantFrame {
			t.Fatalf("restored frame = %q, want stored frame %q", state.Frame, wantFrame)
		}
		if !strings.Contains(state.Frame, "stored excerpt") {
			t.Fatalf("restored frame = %q, want stored payload", state.Frame)
		}
	}
}

func TestWatchSendRestoreClearDropsPendingState(t *testing.T) {
	stateDir := t.TempDir()
	jm, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.feedJobOutput(rec.JobID, []byte("server ready\n"))
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending before restore = %d, want 1", len(pending))
	}
	if err := jm.store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	reopened, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("reopen job manager: %v", err)
	}
	t.Cleanup(func() { _ = reopened.store.Close() })
	if restored := runtimeWatchSendPending(t, reopened); len(restored) != 1 {
		t.Fatalf("runtime pending after restore = %d, want 1", len(restored))
	}

	if _, err := reopened.configureWatch(watchArgs{Target: rec.JobID, Clear: true}); err != nil {
		t.Fatalf("clear restored pending: %v", err)
	}

	if pending := loadWatchSendRecord(t, reopened).Pending; len(pending) != 0 {
		t.Fatalf("folded pending after restore clear = %+v, want none", pending)
	}
}

func TestWatchSendRestoreClearDropsWatchedTargetedPendingState(t *testing.T) {
	stateDir := t.TempDir()
	jm, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	rec := createRunningDelegateWatchTarget(t, jm)
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "watched", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.feedJobOutput(rec.JobID, []byte("server ready\n"))
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending before restore = %d, want 1", len(pending))
	}
	if err := jm.store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	reopened, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("reopen job manager: %v", err)
	}
	t.Cleanup(func() { _ = reopened.store.Close() })
	if restored := runtimeWatchSendPending(t, reopened); len(restored) != 1 {
		t.Fatalf("runtime pending after restore = %d, want 1", len(restored))
	}

	if _, err := reopened.configureWatch(watchArgs{Target: rec.JobID, Send: &watchSendArgs{To: "watched"}, Clear: true}); err != nil {
		t.Fatalf("clear restored watched pending: %v", err)
	}

	if pending := loadWatchSendRecord(t, reopened).Pending; len(pending) != 0 {
		t.Fatalf("folded pending after restore watched clear = %+v, want none", pending)
	}
}

func TestWatchSendRestoreReconfigureDropsWatchedPendingState(t *testing.T) {
	stateDir := t.TempDir()
	jm, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	rec := createRunningDelegateWatchTarget(t, jm)
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "watched", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure first watch: %v", err)
	}
	jm.feedJobOutput(rec.JobID, []byte("server ready\n"))
	firstPending := loadWatchSendRecord(t, jm).Pending
	if len(firstPending) != 1 {
		t.Fatalf("pending before restore = %d, want 1", len(firstPending))
	}
	var firstKey jobstore.WatchSendKey
	for key := range firstPending {
		firstKey = key
	}
	if err := jm.store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	reopened, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("reopen job manager: %v", err)
	}
	reopened.now = func() time.Time { return time.Unix(1001, 0).UTC() }
	reopened.send = jm.send
	output, err := jobstore.OpenOutput(reopened.outputPathForJob(rec, rec.JobID), maxJobOutputRetentionBytes)
	if err != nil {
		t.Fatalf("reopen output: %v", err)
	}
	reopened.running[rec.JobID] = &runningJob{rec: rec, output: output, done: make(chan struct{}), durableStarted: true}
	t.Cleanup(func() { _ = output.Close() })
	t.Cleanup(func() { _ = reopened.store.Close() })
	if restored := runtimeWatchSendPending(t, reopened); len(restored) != 1 {
		t.Fatalf("runtime pending after restore = %d, want 1", len(restored))
	}

	if _, err := reopened.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "blocked",
		Send:        &watchSendArgs{To: "watched", Message: "replacement"},
	}); err != nil {
		t.Fatalf("reconfigure watched pending: %v", err)
	}

	pending := loadWatchSendRecord(t, reopened).Pending
	if _, ok := pending[firstKey]; ok {
		t.Fatalf("old restored watched pending survived replacement cleanup: %+v", pending)
	}
	if len(pending) != 0 {
		t.Fatalf("pending after watched replacement = %+v, want none before new trigger", pending)
	}
}

func TestWatchSendClearDropsPending(t *testing.T) {
	jm := newTestJM(t)
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.feedJobOutput(rec.JobID, []byte("ready\n"))
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending before clear = %d, want 1", len(pending))
	}

	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after clear = %+v, want none", pending)
	}
}

func TestWatchSendWatchedTargetPruneDropsPending(t *testing.T) {
	jm := newTestJM(t)
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.feedJobOutput(rec.JobID, []byte("ready\n"))
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending before prune = %d, want 1", len(pending))
	}

	jm.abandonRunningJob(rec.JobID)

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after watched-target prune = %+v, want none", pending)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count after watched-target prune = %d, want 0", jm.watchCount())
	}
}

func TestWatchSendPruneAppendFailureKeepsPendingReachable(t *testing.T) {
	jm := newTestJM(t)
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.feedJobOutput(rec.JobID, []byte("ready\n"))
	key := watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: "job_obs"}
	jm.mu.Lock()
	cfg := jm.watches[key]
	jm.mu.Unlock()
	if cfg == nil || len(cfg.pending) != 1 {
		t.Fatalf("pending before prune = %+v, want one", cfg)
	}
	realAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchSendDropped {
			return errors.New("append dropped failed")
		}
		return realAppend(e)
	}

	jm.abandonRunningJob(rec.JobID)

	if got := len(cfg.pending); got != 1 {
		t.Fatalf("in-memory pending after failed prune append = %d, want 1", got)
	}
	jm.mu.Lock()
	reachable := jm.watches[key] == cfg
	if !reachable && jm.terminalFlush != nil {
		reachable = jm.terminalFlush[cfg]
	}
	jm.mu.Unlock()
	if !reachable {
		t.Fatal("pending watch config was unreachable after failed prune append")
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("folded pending after failed prune append = %d, want 1", len(pending))
	}

	jm.appendEvent = realAppend
	if err := jm.close(); err != nil {
		t.Fatalf("retry cleanup through close: %v", err)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("folded pending after retry cleanup = %d, want 0", len(pending))
	}
}

func TestWatchSendTerminalFlushPersistsAlreadyFiredPending(t *testing.T) {
	jm := newTestJM(t)
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready")); err != nil {
		t.Fatalf("append output: %v", err)
	}

	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("pending after terminal flush = %d, want 1: %+v", len(pending), pending)
	}
	for _, state := range pending {
		if !strings.Contains(state.Frame, "output_match: server ready") {
			t.Fatalf("pending frame = %q, want flushed trigger", state.Frame)
		}
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count after terminal expiry = %d, want 0", jm.watchCount())
	}
}

func TestWatchSendTerminalFlushCloseDropsPending(t *testing.T) {
	jm := newTestJM(t)
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready")); err != nil {
		t.Fatalf("append output: %v", err)
	}
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending before close = %d, want 1", len(pending))
	}

	if err := jm.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after close = %+v, want none", pending)
	}
}

func TestWatchSendTerminalFlushConfigureClearDropsPending(t *testing.T) {
	jm := newTestJM(t)
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready")); err != nil {
		t.Fatalf("append output: %v", err)
	}
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count after terminal expiry = %d, want 0", jm.watchCount())
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending before configure clear = %d, want 1", len(pending))
	}
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "ready"}); err == nil || !strings.Contains(err.Error(), "target_not_found") {
		t.Fatalf("terminal concrete watch registration error = %v, want target_not_found", err)
	}

	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true}); err != nil {
		t.Fatalf("configure clear terminal-flushed pending: %v", err)
	}

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after configure clear = %+v, want none", pending)
	}
}

func TestWatchSendTerminalExpiryWithoutPendingDoesNotRetainDetachedConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		args watchArgs
	}{
		{
			name: "notification only",
			args: watchArgs{OutputMatch: "ready"},
		},
		{
			name: "send without flushed match",
			args: watchArgs{OutputMatch: "ready", Send: &watchSendArgs{To: "job_obs", Message: "observe"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jm := newTestJM(t)
			rec, _ := jm.createShell(createShellOpts{Command: "x"})
			tc.args.Target = rec.JobID
			if _, err := jm.configureWatch(tc.args); err != nil {
				t.Fatalf("configure: %v", err)
			}

			code := 0
			if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
				t.Fatalf("finalize: %v", err)
			}

			jm.mu.Lock()
			detached := len(jm.terminalFlush)
			jm.mu.Unlock()
			if detached != 0 {
				t.Fatalf("detached terminal flush configs = %d, want 0", detached)
			}
			if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true}); err == nil || !strings.Contains(err.Error(), "target_not_found") {
				t.Fatalf("clear expired watch without pending = %v, want target_not_found", err)
			}
		})
	}
}

func TestWatchSendTerminalExpiryWithInflightSendRemainsClearable(t *testing.T) {
	jm := newTestJM(t)
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready")); err != nil {
		t.Fatalf("append output: %v", err)
	}

	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	jm.mu.Lock()
	detached := len(jm.terminalFlush)
	jm.mu.Unlock()
	if detached != 1 {
		t.Fatalf("detached terminal flush configs = %d, want 1", detached)
	}
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, Send: &watchSendArgs{To: "job_obs"}, Clear: true}); err != nil {
		t.Fatalf("clear terminal-flushed send: %v", err)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after clear = %+v, want none", pending)
	}
}

func TestWatchSendClearNormalizesSendTarget(t *testing.T) {
	for _, tc := range []struct {
		name        string
		configured  string
		clearTarget string
	}{
		{name: "configured untrimmed", configured: " job_obs ", clearTarget: "job_obs"},
		{name: "clear untrimmed", configured: "job_obs", clearTarget: " job_obs "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jm := newTestJM(t)
			rec, _ := jm.createShell(createShellOpts{Command: "x"})
			if _, err := jm.configureWatch(watchArgs{
				Target:      rec.JobID,
				OutputMatch: "ready",
				Send:        &watchSendArgs{To: tc.configured, Message: "observe"},
			}); err != nil {
				t.Fatalf("configure: %v", err)
			}
			if _, err := jm.configureWatch(watchArgs{
				Target: rec.JobID,
				Send:   &watchSendArgs{To: tc.clearTarget},
				Clear:  true,
			}); err != nil {
				t.Fatalf("clear: %v", err)
			}
			if jm.watchCount() != 0 {
				t.Fatalf("watch count after clear = %d, want 0", jm.watchCount())
			}
		})
	}
}

func TestWatchSendTerminalFlushWatchedTargetedClearDropsPending(t *testing.T) {
	jm := newTestJM(t)
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	rec := createRunningDelegateWatchTarget(t, jm)
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "watched", Message: "observe", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready")); err != nil {
		t.Fatalf("append output: %v", err)
	}
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("pending after terminal flush = %d, want 1: %+v", len(pending), pending)
	}
	for key := range pending {
		if key.ResolvedSendTo != rec.JobID {
			t.Fatalf("pending resolved send target = %q, want watched job %q", key.ResolvedSendTo, rec.JobID)
		}
	}

	if _, err := jm.clearWatch(watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: "watched"}); err != nil {
		t.Fatalf("clear terminal-flushed watched pending: %v", err)
	}

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after terminal watched clear = %+v, want none", pending)
	}
}

func TestWatchSendTerminalFlushClearBeforeFailedSendDoesNotPersistPending(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	cleared := false
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		if !cleared {
			cleared = true
			if _, err := jm.clearWatch(watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: "job_obs"}); err != nil {
				t.Fatalf("clear terminal-flushed watch: %v", err)
			}
		}
		return sendMessageResult{Err: errors.New("busy")}
	}
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("server ready")); err != nil {
		t.Fatalf("append output: %v", err)
	}
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if !cleared {
		t.Fatal("send callback did not clear watch")
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after terminal flush clear = %+v, want none", pending)
	}
}

func TestWatchSendTerminalExpiryCloseDropsExistingPending(t *testing.T) {
	jm := newTestJM(t)
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.feedJobOutput(rec.JobID, []byte("server ready\n"))
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending before terminal expiry = %d, want 1", len(pending))
	}
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count after terminal expiry = %d, want 0", jm.watchCount())
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending after terminal expiry = %d, want 1", len(pending))
	}

	if err := jm.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after close = %+v, want none", pending)
	}
}

func TestWatchSendStaleDeliveryClearedDuringSendDoesNotPersistPending(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true}); err != nil {
			t.Fatalf("clear during send: %v", err)
		}
		return sendMessageResult{Err: errors.New("busy")}
	}
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	delivery := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: ready")

	jm.deliverWatchSend(context.Background(), delivery)

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("stale delivery cleared during send persisted pending = %+v", pending)
	}
}

func TestWatchSendStaleDeliveryReplacedDuringSendDoesNotPersistPending(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		if _, err := jm.configureWatch(watchArgs{
			Target:      rec.JobID,
			OutputMatch: "blocked",
			Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
		}); err != nil {
			t.Fatalf("replace during send: %v", err)
		}
		return sendMessageResult{Err: errors.New("busy")}
	}
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	delivery := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: ready")

	jm.deliverWatchSend(context.Background(), delivery)

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("stale delivery replaced during send persisted pending = %+v", pending)
	}
}

func TestWatchSendPendingDeliveredRemovesBeforeNextFailure(t *testing.T) {
	jm := newTestJM(t)
	failSend := true
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		if failSend {
			return sendMessageResult{Err: errors.New("busy")}
		}
		return sendMessageResult{}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	jm.feedJobOutput(rec.JobID, []byte("ready one\n"))
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending after first failure = %d, want 1", len(pending))
	}
	failSend = false
	jm.feedJobOutput(rec.JobID, []byte("ready two\n"))
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after delivered = %+v, want none", pending)
	}
	failSend = true
	jm.feedJobOutput(rec.JobID, []byte("ready three\n"))
	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("pending after second failure = %d, want 1", len(pending))
	}
	for _, state := range pending {
		if state.CoalescedCount != 0 {
			t.Fatalf("coalesced_count after delivered cleanup = %d, want 0", state.CoalescedCount)
		}
	}
}

func TestWatchSendOverlapOlderDeliveredDoesNotRemoveNewerPending(t *testing.T) {
	jm := newTestJM(t)
	sendErr := errors.New("busy")
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: sendErr}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	first := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: first ready")
	second := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: second ready")

	jm.deliverWatchSend(context.Background(), second)
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("pending after second failure = %d, want 1", len(pending))
	}
	if got := len(second.cfg.pending); got != 1 {
		t.Fatalf("in-memory pending after second failure = %d, want 1", got)
	}

	sendErr = nil
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{}
	}
	jm.deliverWatchSend(context.Background(), first)

	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("folded pending after older delivered = %d, want 1: %+v", len(pending), pending)
	}
	for _, state := range pending {
		if !strings.Contains(state.Frame, "second ready") {
			t.Fatalf("pending frame = %q, want newer trigger", state.Frame)
		}
	}
	if got := len(second.cfg.pending); got != 1 {
		t.Fatalf("in-memory pending after older delivered = %d, want newer pending retained", got)
	}
}

func TestWatchSendOverlapOlderFailedDoesNotOverwriteNewerPending(t *testing.T) {
	jm := newTestJM(t)
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	first := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: first ready")
	second := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: second ready")

	jm.deliverWatchSend(context.Background(), second)
	jm.deliverWatchSend(context.Background(), first)

	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != 1 {
		t.Fatalf("folded pending after older failed delivery = %d, want 1: %+v", len(pending), pending)
	}
	for _, state := range pending {
		if !strings.Contains(state.Frame, "second ready") {
			t.Fatalf("pending frame = %q, want newer trigger", state.Frame)
		}
		if state.CoalescedCount != 0 {
			t.Fatalf("coalesced_count = %d, want 0 for ignored older delivery", state.CoalescedCount)
		}
	}
	if got := len(second.cfg.pending); got != 1 {
		t.Fatalf("in-memory pending after older failed delivery = %d, want 1", got)
	}
	for _, state := range second.cfg.pending {
		if !strings.Contains(state.Frame, "second ready") {
			t.Fatalf("in-memory pending frame = %q, want newer trigger", state.Frame)
		}
	}
}

func TestWatchSendStaleFailedDeliveryAfterNewerDeliveredDoesNotPersistPending(t *testing.T) {
	jm := newTestJM(t)
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	first := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: first ready")
	second := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: second ready")

	jm.deliverWatchSend(context.Background(), second)
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("pending after newer delivered = %d, want 0", len(pending))
	}

	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	jm.deliverWatchSend(context.Background(), first)

	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("folded pending after stale failed delivery = %+v, want none", pending)
	}
	if got := len(first.cfg.pending); got != 0 {
		t.Fatalf("in-memory pending after stale failed delivery = %d, want 0", got)
	}
}

func TestWatchSendTeardownRejectsInFlightFailedDeliveryDuringDroppedAppend(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *jobManager) (watchSendDelivery, func() error)
	}{
		{
			name: "clear",
			setup: func(t *testing.T, jm *jobManager) (watchSendDelivery, func() error) {
				t.Helper()
				rec, delivery := setupConcretePendingWatchSend(t, jm)
				return delivery, func() error {
					_, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true})
					return err
				}
			},
		},
		{
			name: "replace",
			setup: func(t *testing.T, jm *jobManager) (watchSendDelivery, func() error) {
				t.Helper()
				rec, delivery := setupConcretePendingWatchSend(t, jm)
				return delivery, func() error {
					_, err := jm.configureWatch(watchArgs{
						Target:      rec.JobID,
						OutputMatch: "blocked",
						Send:        &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
					})
					return err
				}
			},
		},
		{
			name: "prune",
			setup: func(t *testing.T, jm *jobManager) (watchSendDelivery, func() error) {
				t.Helper()
				rec, delivery := setupConcretePendingWatchSend(t, jm)
				return delivery, func() error {
					jm.abandonRunningJob(rec.JobID)
					return nil
				}
			},
		},
		{
			name: "close",
			setup: func(t *testing.T, jm *jobManager) (watchSendDelivery, func() error) {
				t.Helper()
				if _, err := jm.configureWatch(watchArgs{
					Target: "*",
					Events: []string{"job.notification"},
					Send:   &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
				}); err != nil {
					t.Fatalf("configure: %v", err)
				}
				jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_trigger_one", JobType: "delegate", Status: "completed"})
				key := watchKey{VisibleSessionID: jm.sessionID, Target: "*", SendTo: "job_obs"}
				delivery := captureWatchSendDeliveryForKey(t, jm, key, "job_trigger_two", "job.notification")
				return delivery, jm.close
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jm := newTestJM(t)
			jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
				return sendMessageResult{Err: errors.New("busy")}
			}
			delivery, teardown := tc.setup(t, jm)
			if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
				t.Fatalf("pending before teardown = %d, want 1", len(pending))
			}
			dropStarted := make(chan struct{})
			releaseDrop := make(chan struct{})
			realAppend := jm.appendEvent
			blocked := false
			jm.appendEvent = func(e jobstore.Event) error {
				if e.Kind == jobstore.EventWatchSendDropped && !blocked {
					blocked = true
					close(dropStarted)
					<-releaseDrop
				}
				return realAppend(e)
			}

			errCh := make(chan error, 1)
			go func() { errCh <- teardown() }()
			waitForTestSignal(t, dropStarted, "dropped append")

			jm.deliverWatchSend(context.Background(), delivery)
			close(releaseDrop)
			if err := waitForTestError(t, errCh, "teardown"); err != nil {
				t.Fatalf("teardown: %v", err)
			}

			if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
				t.Fatalf("pending after teardown = %+v, want none", pending)
			}
			if got := len(delivery.cfg.pending); got != 0 {
				t.Fatalf("in-memory pending after teardown = %d, want 0", got)
			}
		})
	}
}

func TestWatchSendAppendFailureDuringClearKeepsPendingInMemory(t *testing.T) {
	jm := newTestJM(t)
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.feedJobOutput(rec.JobID, []byte("ready\n"))
	key := watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: "job_obs"}
	jm.mu.Lock()
	cfg := jm.watches[key]
	jm.mu.Unlock()
	if cfg == nil || len(cfg.pending) != 1 {
		t.Fatalf("pending before clear = %+v, want one", cfg)
	}
	realAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchSendDropped {
			return errors.New("append dropped failed")
		}
		return realAppend(e)
	}

	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true}); err == nil {
		t.Fatal("clear succeeded, want append failure")
	}

	if got := len(cfg.pending); got != 1 {
		t.Fatalf("in-memory pending after failed clear append = %d, want 1", got)
	}
	jm.mu.Lock()
	reachable := jm.watches[key] == cfg
	jm.mu.Unlock()
	if !reachable {
		t.Fatal("watch config with pending was detached after failed clear append")
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("folded pending after failed clear append = %d, want 1", len(pending))
	}

	jm.appendEvent = realAppend
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true}); err != nil {
		t.Fatalf("retry clear: %v", err)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("folded pending after retry clear = %d, want 0", len(pending))
	}
	if jm.watchCount() != 0 {
		t.Fatalf("watch count after retry clear = %d, want 0", jm.watchCount())
	}
}

func TestWatchSendPartialDroppedAppendReconcilesSuccessfulPrefix(t *testing.T) {
	jm := newTestJM(t)
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	if _, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_trigger_one", JobType: "delegate", Status: "completed"})
	jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_trigger_two", JobType: "delegate", Status: "completed"})
	key := watchKey{VisibleSessionID: jm.sessionID, Target: "*", SendTo: "job_obs"}
	jm.mu.Lock()
	cfg := jm.watches[key]
	jm.mu.Unlock()
	if cfg == nil || len(cfg.pending) != 2 {
		t.Fatalf("pending before clear = %+v, want two", cfg)
	}
	realAppend := jm.appendEvent
	var dropped int
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchSendDropped {
			dropped++
			if dropped == 2 {
				return errors.New("append dropped failed")
			}
		}
		return realAppend(e)
	}

	if _, err := jm.configureWatch(watchArgs{Target: "*", Clear: true}); err == nil {
		t.Fatal("clear succeeded, want partial append failure")
	}
	if got := len(cfg.pending); got != 1 {
		t.Fatalf("in-memory pending after partial dropped append = %d, want 1", got)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("folded pending after partial dropped append = %d, want 1", len(pending))
	}
	jm.mu.Lock()
	reachable := jm.watches[key] == cfg
	rejecting := cfg.rejectingDelivery
	jm.mu.Unlock()
	if !reachable {
		t.Fatal("watch config with remaining pending was detached after partial dropped append")
	}
	if rejecting {
		t.Fatal("watch config stayed rejecting after failed dropped append")
	}

	jm.appendEvent = realAppend
	if _, err := jm.configureWatch(watchArgs{Target: "*", Clear: true}); err != nil {
		t.Fatalf("retry clear: %v", err)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("folded pending after retry clear = %d, want 0", len(pending))
	}
}

func TestWatchSendAppendFailureDuringReplaceLeavesOldWatchReachable(t *testing.T) {
	jm := newTestJM(t)
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.feedJobOutput(rec.JobID, []byte("ready\n"))
	key := watchKey{VisibleSessionID: jm.sessionID, Target: rec.JobID, SendTo: "job_obs"}
	jm.mu.Lock()
	oldCfg := jm.watches[key]
	jm.mu.Unlock()
	if oldCfg == nil || len(oldCfg.pending) != 1 {
		t.Fatalf("pending before replace = %+v, want one", oldCfg)
	}
	realAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchSendDropped {
			return errors.New("append dropped failed")
		}
		return realAppend(e)
	}

	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "blocked",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err == nil {
		t.Fatal("replace succeeded, want append failure")
	}

	jm.mu.Lock()
	stillReachable := jm.watches[key] == oldCfg
	jm.mu.Unlock()
	if !stillReachable {
		t.Fatal("old watch config was replaced after failed drop append")
	}
	if got := len(oldCfg.pending); got != 1 {
		t.Fatalf("old pending after failed replace append = %d, want 1", got)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("folded pending after failed replace append = %d, want 1", len(pending))
	}

	jm.appendEvent = realAppend
	res, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "blocked",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	})
	if err != nil {
		t.Fatalf("retry replace: %v", err)
	}
	if !res.ReplacedExisting {
		t.Fatal("retry replace did not report replaced_existing")
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("folded pending after retry replace = %d, want 0", len(pending))
	}
}

func TestWatchSendAppendFailureDuringCloseKeepsPendingReachable(t *testing.T) {
	jm := newTestJM(t)
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	if _, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_trigger", JobType: "delegate", Status: "completed"})
	key := watchKey{VisibleSessionID: jm.sessionID, Target: "*", SendTo: "job_obs"}
	jm.mu.Lock()
	cfg := jm.watches[key]
	jm.mu.Unlock()
	if cfg == nil || len(cfg.pending) != 1 {
		t.Fatalf("pending before close = %+v, want one", cfg)
	}
	realAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchSendDropped {
			return errors.New("append dropped failed")
		}
		return realAppend(e)
	}

	if err := jm.close(); err == nil {
		t.Fatal("close succeeded, want append failure")
	}
	if _, err := jm.store.Load(); err != nil {
		t.Fatalf("store after failed close = %v, want still open", err)
	}
	if got := len(cfg.pending); got != 1 {
		t.Fatalf("in-memory pending after failed close append = %d, want 1", got)
	}
	jm.mu.Lock()
	reachable := jm.watches[key] == cfg
	if !reachable && jm.terminalFlush != nil {
		reachable = jm.terminalFlush[cfg]
	}
	jm.mu.Unlock()
	if !reachable {
		t.Fatal("pending watch config was unreachable after failed close append")
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("folded pending after failed close append = %d, want 1", len(pending))
	}
	jm.mu.Lock()
	closing := jm.closing
	jm.mu.Unlock()
	if closing {
		t.Fatal("job manager remained closing after failed close append")
	}
	later, err := jm.createShell(createShellOpts{Command: "after failed close"})
	if err != nil {
		t.Fatalf("create after failed close: %v", err)
	}
	code := 0
	if err := jm.finalize(later.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize after failed close: %v", err)
	}

	jm.appendEvent = realAppend
	if err := jm.close(); err != nil {
		t.Fatalf("retry close: %v", err)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 0 {
		t.Fatalf("folded pending after retry close = %d, want 0", len(pending))
	}
}

func TestWatchSendAppendFailureDuringDeliveredKeepsPendingInMemory(t *testing.T) {
	jm := newTestJM(t)
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.feedJobOutput(rec.JobID, []byte("ready one\n"))
	delivery := captureWatchSendDelivery(t, jm, rec.JobID, "output_match: ready two")
	if got := len(delivery.cfg.pending); got != 1 {
		t.Fatalf("pending before delivered = %d, want 1", got)
	}
	realAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchSendDelivered {
			return errors.New("append delivered failed")
		}
		return realAppend(e)
	}
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{}
	}

	jm.deliverWatchSend(context.Background(), delivery)

	if got := len(delivery.cfg.pending); got != 1 {
		t.Fatalf("in-memory pending after failed delivered append = %d, want 1", got)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != 1 {
		t.Fatalf("folded pending after failed delivered append = %d, want 1", len(pending))
	}
}

func TestWatchSendSettledTombstonesAreBounded(t *testing.T) {
	jm := newTestJM(t)
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{}
	}
	if _, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	for i := 0; i < defaultWatchSendPendingCap+5; i++ {
		jobID := "job_trigger_" + string(rune('A'+i))
		jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: jobID, JobType: "delegate", Status: "completed"})
	}
	jm.mu.Lock()
	var cfg *watchConfig
	for _, watch := range jm.watches {
		cfg = watch
	}
	settled := len(cfg.settledUpdateSeq)
	jm.mu.Unlock()
	if settled > defaultWatchSendPendingCap {
		t.Fatalf("settled tombstones = %d, want <= %d", settled, defaultWatchSendPendingCap)
	}
}

func TestWatchSendAppendFailureDuringEvictionKeepsMemoryAndDurableConsistent(t *testing.T) {
	jm := newTestJM(t)
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }
	if _, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	for i := 0; i < defaultWatchSendPendingCap; i++ {
		jobID := "job_trigger_" + string(rune('A'+i))
		jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: jobID, JobType: "delegate", Status: "completed"})
	}
	jm.mu.Lock()
	var cfg *watchConfig
	for _, watch := range jm.watches {
		cfg = watch
	}
	jm.mu.Unlock()
	if cfg == nil || len(cfg.pending) != defaultWatchSendPendingCap {
		t.Fatalf("pending before eviction = %+v, want cap", cfg)
	}

	realAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchSendEvicted {
			return errors.New("append evicted failed")
		}
		return realAppend(e)
	}
	jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_trigger_over_cap", JobType: "delegate", Status: "completed"})

	if got := len(cfg.pending); got != defaultWatchSendPendingCap {
		t.Fatalf("in-memory pending after failed eviction append = %d, want %d", got, defaultWatchSendPendingCap)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != defaultWatchSendPendingCap {
		t.Fatalf("folded pending after failed eviction append = %d, want %d", len(pending), defaultWatchSendPendingCap)
	} else {
		for key := range pending {
			if key.ResolvedWatchedIdentity == "job_trigger_over_cap" {
				t.Fatalf("pending over cap was durable after failed eviction append: %+v", pending)
			}
		}
	}
	for _, n := range notified {
		if strings.Contains(n.Reason, "watch send evicted") {
			t.Fatalf("eviction diagnostic emitted before durable evicted append succeeded: %+v", n)
		}
	}

	jm.appendEvent = realAppend
	jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: "job_trigger_retry_cleanup", JobType: "delegate", Status: "completed"})
	if got := len(cfg.pending); got != defaultWatchSendPendingCap {
		t.Fatalf("in-memory pending after retry eviction = %d, want %d", got, defaultWatchSendPendingCap)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != defaultWatchSendPendingCap {
		t.Fatalf("folded pending after retry eviction = %d, want %d", len(pending), defaultWatchSendPendingCap)
	}
}

func captureWatchSendDelivery(t *testing.T, jm *jobManager, jobID, trigger string) watchSendDelivery {
	t.Helper()
	jm.mu.Lock()
	var delivery watchSendDelivery
	for _, cfg := range jm.watches {
		if cfg.target == jobID {
			delivery = jm.watchSendSnapshot(cfg, jobID, trigger)
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
	jm.mu.Lock()
	cfg := jm.watches[key]
	var delivery watchSendDelivery
	if cfg != nil {
		delivery = jm.watchSendSnapshot(cfg, watchedIdentity, trigger)
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
		Send:        &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.feedJobOutput(rec.JobID, []byte("ready one\n"))
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

func TestWatchSendCapEvictsOldestPendingAndNotifies(t *testing.T) {
	jm := newTestJM(t)
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return sendMessageResult{Err: errors.New("busy")}
	}
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	if _, err := jm.configureWatch(watchArgs{
		Target: "*",
		Events: []string{"job.notification"},
		Send:   &watchSendArgs{To: "job_obs", Message: "observe", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	for i := 0; i < defaultWatchSendPendingCap+1; i++ {
		jobID := "job_trigger_" + string(rune('A'+i))
		jm.onSessionEvent(events.EventJobFinished, events.JobFinishedData{JobID: jobID, JobType: "delegate", Status: "completed"})
	}

	pending := loadWatchSendRecord(t, jm).Pending
	if len(pending) != defaultWatchSendPendingCap {
		t.Fatalf("pending count = %d, want %d", len(pending), defaultWatchSendPendingCap)
	}
	for key := range pending {
		if key.ResolvedWatchedIdentity == "job_trigger_A" {
			t.Fatalf("oldest pending key was not evicted: %+v", pending)
		}
	}
	var evictionDiagnostics int
	for _, n := range notified {
		if strings.Contains(n.Reason, "watch send evicted") {
			evictionDiagnostics++
			if !strings.Contains(n.Reason, "job_trigger_A") {
				t.Fatalf("diagnostic reason = %q, want evicted trigger", n.Reason)
			}
		}
	}
	if evictionDiagnostics != 1 {
		t.Fatalf("eviction diagnostic count = %d, want 1: %+v", evictionDiagnostics, notified)
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

func TestWatchSendToWatchedRejectsSessionEventsWithoutConcreteTarget(t *testing.T) {
	for _, eventName := range []string{"assistant.message", "assistant.tool", "communicate"} {
		t.Run(eventName, func(t *testing.T) {
			jm := newTestJM(t)
			var sent []sendMessageArgs
			jm.send = func(_ context.Context, a sendMessageArgs) sendMessageResult {
				sent = append(sent, a)
				return sendMessageResult{}
			}

			_, err := jm.configureWatch(watchArgs{
				Target: "*",
				Events: []string{eventName},
				Send:   &watchSendArgs{To: "watched", Message: "observe"},
			})

			if err == nil || !strings.Contains(err.Error(), "target_not_found") {
				t.Fatalf("error = %v, want target_not_found", err)
			}
			if jm.watchCount() != 0 {
				t.Fatalf("watch count = %d, want 0", jm.watchCount())
			}
			if len(sent) != 0 {
				t.Fatalf("rejected watched send delivered sends: %#v", sent)
			}
		})
	}
}

func TestWatchSendFailureNotifiesCaller(t *testing.T) {
	jm := newTestJM(t)
	sendErr := errors.New("target_not_messageable: job_obs")
	jm.send = func(context.Context, sendMessageArgs) sendMessageResult {
		return hardWatchSendResult(sendErr)
	}
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "saw ready"},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.feedJobOutput(rec.JobID, []byte("server ready\n"))

	if len(notified) != 1 {
		t.Fatalf("failed watch send must notify caller once, got %d", len(notified))
	}
	if notified[0].Status != jobNotificationEventWatch {
		t.Fatalf("notification status = %q, want watch", notified[0].Status)
	}
	if !strings.Contains(notified[0].Reason, "watch send failed") ||
		!strings.Contains(notified[0].Reason, "target_not_messageable") {
		t.Fatalf("notification reason = %q, want bounded send failure", notified[0].Reason)
	}
}

func TestWatchSendFrameIsBounded(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte(strings.Repeat("x", watchFrameMaxChars*2))); err != nil {
		t.Fatalf("append: %v", err)
	}

	frame := jm.buildWatchFrame(&watchConfig{
		send: &watchSendArgs{
			Message:        strings.Repeat("m", watchMessageMaxChars+100),
			IncludeFrame:   true,
			IncludeExcerpt: true,
		},
	}, rec.JobID, strings.Repeat("trigger", watchTriggerMaxChars), "delivery_test")

	if len([]rune(frame)) > watchFrameMaxChars {
		t.Fatalf("frame length = %d, want <= %d", len([]rune(frame)), watchFrameMaxChars)
	}
	if !strings.Contains(frame, "Watch frame") || !strings.Contains(frame, "excerpt:") {
		t.Fatalf("frame must include bounded metadata and excerpt; got %q", frame)
	}
}

func TestWatchSendExcerptWithoutFrameOmitsFrameMetadata(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte("ready excerpt\n")); err != nil {
		t.Fatalf("append: %v", err)
	}

	frame := jm.buildWatchFrame(&watchConfig{
		send: &watchSendArgs{
			Message:        "saw ready",
			IncludeExcerpt: true,
		},
	}, rec.JobID, "output_match: ready excerpt", "delivery_test")

	if !strings.Contains(frame, "saw ready") || !strings.Contains(frame, "ready excerpt") {
		t.Fatalf("excerpt-only delivery must include message and excerpt; got %q", frame)
	}
	if !strings.Contains(frame, "delivery_id: delivery_test") {
		t.Fatalf("excerpt-only delivery must include delivery id; got %q", frame)
	}
	if strings.Contains(frame, "Watch frame") || strings.Contains(frame, "trigger:") {
		t.Fatalf("excerpt-only delivery must not include frame metadata; got %q", frame)
	}
}

func TestWatchSendMessageOnlyIncludesDeliveryID(t *testing.T) {
	jm := newTestJM(t)

	frame := jm.buildWatchFrame(&watchConfig{
		send: &watchSendArgs{Message: "plain message"},
	}, "job_target", "output_match: ready", "delivery_message_only")

	if !strings.Contains(frame, "plain message") {
		t.Fatalf("message-only delivery must include message; got %q", frame)
	}
	if !strings.Contains(frame, "delivery_id: delivery_message_only") {
		t.Fatalf("message-only delivery must include delivery id; got %q", frame)
	}
	if strings.Contains(frame, "Watch frame") || strings.Contains(frame, "trigger:") {
		t.Fatalf("message-only delivery must not include full frame metadata; got %q", frame)
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

func TestProgressTimerFiresPeriodically(t *testing.T) {
	jm := newTestJM(t)
	fired := make(chan struct{}, 4)
	jm.enqueue = func(jobNotification) { fired <- struct{}{} }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, ProgressIntervalMS: 1000}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	select {
	case <-fired:
	case <-time.After(3 * time.Second):
		t.Fatal("progress timer did not fire within 3s")
	}
	_, _ = jm.configureWatch(watchArgs{Target: rec.JobID, Clear: true})
}

func TestProgressTimerStopsOnClose(t *testing.T) {
	jm := newTestJM(t)
	fired := make(chan struct{}, 16)
	jm.enqueue = func(jobNotification) { fired <- struct{}{} }

	if _, err := jm.configureWatch(watchArgs{Target: "caller", ProgressIntervalMS: minWatchProgressIntervalMS}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	select {
	case <-fired:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("progress timer did not fire before close")
	}
	if err := jm.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	for {
		select {
		case <-fired:
		default:
			goto drained
		}
	}

drained:
	if count := jm.watchCount(); count != 0 {
		t.Fatalf("close must remove watches; count = %d", count)
	}
	select {
	case <-fired:
		t.Fatal("progress timer fired after close")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestWatchEventKindNamesResolve(t *testing.T) {
	if len(WatchEventKindNames) != len(modelEventKinds) {
		t.Fatalf("WatchEventKindNames has %d names, modelEventKinds has %d", len(WatchEventKindNames), len(modelEventKinds))
	}
	for _, name := range WatchEventKindNames {
		if _, ok := modelEventKinds[name]; !ok {
			t.Errorf("WatchEventKindNames includes unresolved event kind %q", name)
		}
	}
}

var _ = jobstore.JobShell
