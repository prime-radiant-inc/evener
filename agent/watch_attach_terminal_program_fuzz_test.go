//go:build evenerfuzz

package agent

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// FuzzWatchAttachTerminalProgram exercises the level-triggered output-match
// paths that run while a watch is attached to existing output and while a job
// becomes terminal. It uses test-owned source and receiver storage: shell
// records are only durable fixtures and never launch a process or model.
//
// The program's oracles assert the delivery cardinality that distinguishes an
// attach scan from replay, and exact canonical custody/source receipts for a
// terminal catch-up send. It intentionally leaves configuration validation, event
// evaluation, and general pending-send settlement to their dedicated programs.
func FuzzWatchAttachTerminalProgram(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		{0},
		{1, 2, 3},
		{255, 1, 254, 2},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &watpReader{data: data}
		watpAttachNoSend(t, r)
		watpAttachSidecar(t, r)
		watpTerminalCatchup(t, r)
		watpTerminalFlush(t, r)
	})
}

type watpReader struct {
	data []byte
	pos  int
}

func (r *watpReader) next() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

func (r *watpReader) word() string {
	return []string{"alpha", "beta", "gamma", "delta"}[int(r.next())%4]
}

func watpNewJM(t *testing.T) *jobManager {
	t.Helper()
	receiver := &Session{id: testOwnerSessionID, stateDir: t.TempDir()}
	writer, err := transcript.NewWriter(transcriptPath(receiver.stateDir, receiver.ID()), transcript.Header{SessionID: receiver.ID()})
	if err != nil {
		t.Fatal(err)
	}
	receiver.attachTranscript(writer)
	t.Cleanup(func() { _ = receiver.closeAttachedTranscript() })
	jm, err := newJobManager(receiver.stateDir, receiver.ID(), receiver.enqueueJobNotificationAndNotify)
	if err != nil {
		t.Fatal(err)
	}
	receiver.jobManager, jm.retirementOwner = jm, receiver
	freezeClock(jm)
	t.Cleanup(func() {
		jm.abandonRunningJobs()
		_ = jm.close()
	})
	return jm
}

func watpCreateOutputJob(t *testing.T, jm *jobManager, output string) *jobstore.JobRecord {
	t.Helper()
	rec, err := jm.createShell(createShellOpts{Command: "watch attach fixture"})
	if err != nil {
		t.Fatalf("create fixture shell: %v", err)
	}
	run := jm.running[rec.JobID]
	if run == nil || run.output == nil {
		t.Fatalf("fixture job %q has no running output", rec.JobID)
	}
	if _, err := jm.appendJobOutput(rec.JobID, run.output, []byte(output)); err != nil {
		t.Fatalf("append fixture output: %v", err)
	}
	return rec
}

func watpAttachNoSend(t *testing.T, r *watpReader) {
	t.Helper()
	jm := watpNewJM(t)

	label := r.word()
	rec := watpCreateOutputJob(t, jm, "noise\nneedle "+label+"\nneedle final-"+label+"\n")
	result, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "needle"})
	if err != nil {
		t.Fatalf("configure attach watch: %v", err)
	}
	if !result.Watching || !result.Fired {
		t.Fatalf("attach result = %+v, want live one-shot fire", result)
	}
	notifications := watpAcceptedFrames(t, jm, true)
	if len(notifications) != 1 || !strings.Contains(notifications[0].TriggerReason, "needle final-"+label) {
		t.Fatalf("attach callbacks=%+v", notifications)
	}

	// The attach scan is level-triggered, not replay. A new complete match fires
	// once more through the live feed path, while the retained matches do not.
	run := jm.running[rec.JobID]
	if run == nil || run.output == nil {
		t.Fatalf("live fixture job %q has no output", rec.JobID)
	}
	if _, err := jm.appendJobOutput(rec.JobID, run.output, []byte("needle live-"+label+"\n")); err != nil {
		t.Fatalf("append live fixture output: %v", err)
	}
	notifications = watpAcceptedFrames(t, jm, true)
	if len(notifications) != 2 || !strings.Contains(notifications[1].TriggerReason, "needle live-"+label) {
		t.Fatalf("live feed notifications = %+v, want exactly one new match", notifications)
	}
}

func watpAttachSidecar(t *testing.T, r *watpReader) {
	t.Helper()
	jm := watpNewJM(t)
	label := r.word()
	rec := watpCreateOutputJob(t, jm, "before\nneedle sidecar-"+label+"\n")
	result, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "needle",
		Send:        &watchSendArgs{To: runtimeMessageAliasCaller, Message: "observe " + label},
	})
	if err != nil {
		t.Fatalf("configure sidecar attach watch: %v", err)
	}
	if !result.Watching || !result.Fired {
		t.Fatalf("sidecar attach result = %+v, want live one-shot fire", result)
	}
	pending := watpAcceptedFrames(t, jm, false)
	if len(pending) != 1 || pending[0].Key.ResolvedSendTo != runtimeMessageAliasCaller || !strings.Contains(pending[0].TriggerReason, "needle sidecar-"+label) {
		t.Fatalf("sidecar attach pending = %+v", pending)
	}
}

func watpTerminalCatchup(t *testing.T, r *watpReader) {
	t.Helper()
	jm := watpNewJM(t)
	label := r.word()
	rec := watpCreateOutputJob(t, jm, "terminal needle-"+label)
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "fixture complete", nil); err != nil {
		t.Fatalf("finalize terminal catch-up fixture: %v", err)
	}

	result, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "needle"})
	if err != nil {
		t.Fatalf("configure terminal catch-up: %v", err)
	}
	if result.Watching || !result.TerminalCatchup || !result.Fired || result.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("terminal catch-up result = %+v", result)
	}
	notifications := watpAcceptedFrames(t, jm, true)
	if len(notifications) != 1 || !strings.Contains(notifications[0].TriggerReason, "terminal needle-"+label) {
		t.Fatalf("terminal catch-up notifications = %+v", notifications)
	}

	sendResult, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "needle",
		Send:        &watchSendArgs{To: runtimeMessageAliasCaller, Message: "terminal observe"},
	})
	if err != nil {
		t.Fatalf("configure terminal sidecar catch-up: %v", err)
	}
	if sendResult.Watching || !sendResult.TerminalCatchup || !sendResult.Fired {
		t.Fatalf("terminal sidecar catch-up result = %+v", sendResult)
	}
	pending := watpAcceptedFrames(t, jm, false)
	if len(pending) != 1 || pending[0].Key.ResolvedSendTo != runtimeMessageAliasCaller || pending[0].Key.WatchID == notifications[0].Key.WatchID || pending[0].DeliveryID == notifications[0].DeliveryID {
		t.Fatalf("terminal sidecar pending = %+v", pending)
	}
}

func watpTerminalFlush(t *testing.T, r *watpReader) {
	t.Helper()
	jm := watpNewJM(t)
	label := r.word()
	rec := watpCreateOutputJob(t, jm, "unterminated needle flush-"+label)
	result, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "needle"})
	if err != nil {
		t.Fatalf("configure terminal-flush watch: %v", err)
	}
	// The byte-window scanner (commit fac0f049f) does not wait for a line
	// terminator: the attach-time level scan sees the unterminated match and
	// fires immediately instead of parking it for the terminal flush.
	if !result.Watching || !result.Fired {
		t.Fatalf("unterminated attach result = %+v, want live attach fire", result)
	}
	notifications := watpAcceptedFrames(t, jm, true)
	if got := watpCountReason(notifications, "needle flush-"+label); got != 1 {
		t.Fatalf("attach notifications = %+v, want one match", notifications)
	}
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "fixture complete", nil); err != nil {
		t.Fatalf("finalize terminal-flush fixture: %v", err)
	}
	// The attach scan already delivered the match; the terminal flush must not
	// replay those bytes. Finalize still enqueues the ordinary job-finished
	// notification, so only the match cardinality is pinned.
	notifications = watpAcceptedFrames(t, jm, true)
	if got := watpCountReason(notifications, "needle flush-"+label); got != 1 {
		t.Fatalf("terminal flush notifications = %+v, want no match replay", notifications)
	}
	if jm.watchCount() != 0 {
		t.Fatalf("terminal flush retained a live watch: %d", jm.watchCount())
	}
}

func watpCountReason(notifications []jobstore.WatchSendState, want string) int {
	count := 0
	for _, notification := range notifications {
		if strings.Contains(notification.TriggerReason, want) {
			count++
		}
	}
	return count
}

// Inspect real source records, canonical custody and exact source receipts.
// Frame text here is opaque transport data, not an oracle for rendered wording.
func watpAcceptedFrames(t *testing.T, jm *jobManager, callback bool) []jobstore.WatchSendState {
	t.Helper()
	produced := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
	receipts := producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)
	entries := notificationRawEntries(t, transcriptPath(jm.retirementOwner.stateDir, jm.sessionID))
	var found []jobstore.WatchSendState
	for _, event := range produced {
		state := *event.WatchSend
		if state.CallbackFrame != callback {
			continue
		}
		matches, acks := 0, 0
		for _, entry := range entries {
			if entry.Turn.AttentionID == fmt.Sprintf("watch:%s:%d", state.DeliveryID, state.UpdateSeq) {
				matches++
				if callback && !reflect.DeepEqual(entry.Turn.Message, llm.UserMachinery(state.Frame)) {
					t.Fatal("complete watch message changed in transport")
				}
				if !callback && (state.Message == "" || !strings.Contains(entry.Turn.Message.Text(), state.Message)) {
					t.Fatal("sidecar user payload did not reach canonical custody")
				}
			}
		}
		for _, receipt := range receipts {
			if receipt.WatchSend.DeliveryID == state.DeliveryID && receipt.WatchSend.UpdateSeq == state.UpdateSeq {
				acks++
			}
		}
		if matches != 1 || acks != 1 {
			t.Fatalf("watch %s custody=%d receipts=%d want1/1", state.DeliveryID, matches, acks)
		}
		found = append(found, state)
	}
	if pending := jm.pendingWatchSendDeliveries(nil); len(pending) != 0 {
		t.Fatalf("source retained %d unacknowledged deliveries", len(pending))
	}
	return found
}
