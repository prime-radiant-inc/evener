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

func TestWatchSendGenerationChangesAfterRestoreAndKeepsOldPending(t *testing.T) {
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
	if _, err := reopened.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "(?i)ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "second generation", IncludeFrame: true},
	}); err != nil {
		t.Fatalf("configure second watch: %v", err)
	}
	reopened.feedJobOutput(rec.JobID, []byte("second READY\n"))

	pending := loadWatchSendRecord(t, reopened).Pending
	if len(pending) != 2 {
		t.Fatalf("pending count after restore = %d, want 2: %+v", len(pending), pending)
	}
	if _, ok := pending[firstKey]; !ok {
		t.Fatalf("old pending key was overwritten or removed: %+v", pending)
	}
	for key, state := range pending {
		if key == firstKey {
			continue
		}
		if key.WatchGeneration == firstKey.WatchGeneration {
			t.Fatalf("watch generation reused after restore: %q", key.WatchGeneration)
		}
		if !strings.Contains(state.Frame, "second READY") {
			t.Fatalf("new pending frame = %q, want second trigger", state.Frame)
		}
		return
	}
	t.Fatal("second pending key not found")
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

	if got := len(cfg.pending); got != defaultWatchSendPendingCap+1 {
		t.Fatalf("in-memory pending after failed eviction append = %d, want %d", got, defaultWatchSendPendingCap+1)
	}
	if pending := loadWatchSendRecord(t, jm).Pending; len(pending) != defaultWatchSendPendingCap+1 {
		t.Fatalf("folded pending after failed eviction append = %d, want %d", len(pending), defaultWatchSendPendingCap+1)
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
	return jobstore.FoldWatchSends(events)
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
		return sendMessageResult{Err: sendErr}
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
	}, rec.JobID, strings.Repeat("trigger", watchTriggerMaxChars))

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
	}, rec.JobID, "output_match: ready excerpt")

	if !strings.Contains(frame, "saw ready") || !strings.Contains(frame, "ready excerpt") {
		t.Fatalf("excerpt-only delivery must include message and excerpt; got %q", frame)
	}
	if strings.Contains(frame, "Watch frame") || strings.Contains(frame, "trigger:") {
		t.Fatalf("excerpt-only delivery must not include frame metadata; got %q", frame)
	}
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
