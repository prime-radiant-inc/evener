package agent

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// retainingSession is a session whose transcript keeps the line of the record
// matching text and then fails its write: the shape every caller below has to
// read as "the record exists", because every returning reader will find it.
func retainingSession(t *testing.T, name, match string) (*Session, *retainMarkerWriteFS) {
	t.Helper()
	s := newRetainedWriteSession(t)
	fs := retainTranscriptWrites(t, s, match)
	go func() {
		for range s.Events() {
		}
	}()
	return s, fs
}

// retainingSessionWatched is retainingSession with the event bus recorded
// rather than discarded, for the sites whose post-write effect IS an event.
// The returned function closes the session and reports everything it emitted.
func retainingSessionWatched(t *testing.T, match string) (*Session, *retainMarkerWriteFS, func() []events.SessionEvent) {
	t.Helper()
	s := newRetainedWriteSession(t)
	fs := retainTranscriptWrites(t, s, match)
	seen, mu, done := collectEvents(s)
	return s, fs, func() []events.SessionEvent {
		s.Close()
		<-done
		mu.Lock()
		defer mu.Unlock()
		return *seen
	}
}

func newRetainedWriteSession(t *testing.T) *Session {
	t.Helper()
	return newSession(t,
		withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: t.TempDir()}),
		withoutGitSnapshot(),
	)
}

// retainTranscriptWrites replaces the session's transcript with one that lets
// the line of the record matching text land and then fails its write.
func retainTranscriptWrites(t *testing.T, s *Session, match string) *retainMarkerWriteFS {
	t.Helper()
	if err := s.closeAttachedTranscript(); err != nil {
		t.Fatalf("close default transcript: %v", err)
	}
	fs := &retainMarkerWriteFS{Fs: afero.NewOsFs(), match: []byte(match)}
	writer, err := transcript.NewWriterWithFS(fs, transcriptPath(s.stateDir, s.id), transcript.Header{SessionID: s.id})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	writer.SyncInterval = 0 // every append syncs, so the matched record's own append fails
	s.attachTranscript(writer)
	return fs
}

func countTurnsWithText(t *testing.T, s *Session, text string) int {
	t.Helper()
	n := 0
	for _, turn := range currentHistory(t, s) {
		if turn.Message.Text() == text {
			n++
		}
	}
	return n
}

// The client-mutation recovery turn: a write that kept its entry left the
// restore reporting failure, so a session whose own transcript holds the
// recovery record could not be loaded at all — and the retry that followed
// wrote the record a second time.
func TestRetainedWrite_ClientMutationRecoveryCompletes(t *testing.T) {
	t.Parallel()
	const input = "the input a client mutation failed on"
	s, fs := retainingSession(t, "client-mutation", input)
	pending := appwire.PendingMutation{
		Method:         clientMutationMethodStart,
		TurnID:         "turn_cm_1",
		Input:          []appwire.InputItem{{Type: "text", Text: input}},
		ExecutionState: "failureRecording",
	}
	err := s.recordClientMutationFailure("cm-1", pending, clientMutationFailure{Message: "boom"}, false)
	if !fs.failed.Load() {
		t.Fatal("test setup: no write was retained")
	}
	if err != nil {
		t.Fatalf("recovery reported failure for a record the transcript holds: %v", err)
	}
	if got := countTurnsWithText(t, s, input); got != 1 {
		t.Fatalf("the recovery turn appears %d times in history, want once", got)
	}
}

// A skill carrier turn is the only copy of a skill's instructions, and the
// obligation it settles is settled once the record exists. Reporting failure
// leaves that obligation pending, and the next delivery sends the same body
// again.
func TestRetainedWrite_SkillCarrierIsRecorded(t *testing.T) {
	t.Parallel()
	const body = "SKILL BODY retained by its write"
	s, fs := retainingSession(t, "skill-carrier", body)
	turn := schema.NewTurn(schema.TurnSystem, llm.User(body))
	if err := s.recordSkillCarrierDurably(turn, turn); err != nil {
		t.Fatalf("carrier reported failure for a record the transcript holds: %v", err)
	}
	if !fs.failed.Load() {
		t.Fatal("test setup: no write was retained")
	}
	if got := countTurnsWithText(t, s, body); got != 1 {
		t.Fatalf("the carrier appears %d times in history, want once", got)
	}
}

// Delegate steering: the claim it belongs to has to complete, or the delegate
// is told its message was never delivered while every reader can see that it
// was.
func TestRetainedWrite_DelegateSteeringCompletesItsClaim(t *testing.T) {
	t.Parallel()
	const message = "steering a delegate through a retained write"
	s, fs := retainingSession(t, "delegate-steering", message)
	entry, err := s.appendDelegateSteeringDurablyWithMetadata(message, "q_1_retained", "user", "")
	if err != nil {
		t.Fatalf("steering reported failure for a record the transcript holds: %v", err)
	}
	if !fs.failed.Load() {
		t.Fatal("test setup: no write was retained")
	}
	if entry.entryID != "q_1_retained" {
		t.Fatalf("steering entry = %#v, want the one the transcript holds", entry)
	}
	if got := countTurnsWithText(t, s, message); got != 1 {
		t.Fatalf("the steering turn appears %d times in history, want once", got)
	}
}

// A failure turn that reached the transcript must not put its steering back on
// the queue: the next drain would write the same failure again and announce it
// a second time.
func TestRetainedWrite_FailedSteeringSelectionIsNotRequeued(t *testing.T) {
	t.Parallel()
	const cause = "the selection that failed"
	s, fs := retainingSession(t, "failed-steering", cause)
	s.recordFailedSteeringSelection(steeringMessage{Text: "steer me", ClientMutationID: "cm-steer-1", StableTurnID: "turn_steer_1"}, retainedSelectionError{})
	if !fs.failed.Load() {
		t.Fatal("test setup: no write was retained")
	}
	s.mu.Lock()
	queued := len(s.steeringQueue)
	s.mu.Unlock()
	if queued != 0 {
		t.Fatalf("steering queue holds %d messages, want none: the failure record is in the transcript", queued)
	}
	if got := countTurnsWithText(t, s, cause); got != 1 {
		t.Fatalf("the failure turn appears %d times in history, want once", got)
	}
}

type retainedSelectionError struct{}

func (retainedSelectionError) Error() string { return "the selection that failed" }

// The round's tool results carry the delegate delivery commits inside the
// persisted turn. A write that kept its entry leaves that turn readable, so
// aborting the commits would leave the durable record claiming deliveries the
// delegate store rolled back — a reader coming back sees work the store says
// never happened.
func TestRetainedWrite_DeliveryCommitsCompleteWithTheirRound(t *testing.T) {
	t.Parallel()
	inline, _ := newDelegateControllerTestHarness(t, 2, 1)
	const delegateID = "dlg_inline_a"
	seedDelegateControllerIdle(t, inline, delegateID, "")
	lease, waiter := startDelegateDeliveryGeneration(t, inline, delegateID, true)
	plan := finishDelegateDeliveryGeneration(t, inline, lease, "inline").deliveries[0]
	if _, err := deliverDelegatePacket(plan, nil); err != nil {
		t.Fatalf("handoff inline delivery: %v", err)
	}
	resolution := <-waiter.resolution
	if resolution.commit == nil {
		t.Fatal("inline handoff returned no delivery commit")
	}

	const answer = "done one"
	fs := &retainMarkerWriteFS{Fs: afero.NewOsFs(), match: []byte(answer)}
	writer, err := transcript.NewWriterWithFS(fs, transcriptPath(inline.stateDir, "root-session"), transcript.Header{SessionID: "root-session"})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	writer.SyncInterval = 0
	root := &Session{id: "root-session", stateDir: inline.stateDir, state: SessionIdle, delegateController: inline, events: make(chan events.SessionEvent, 64)}
	root.attachTranscript(writer)
	inline.rootRuntime = root

	result := llm.ToolResultNamed("inline-call-1", "delegate_send", answer, false)
	commits := []delegateToolCallDeliveryCommit{{toolCallID: "inline-call-1", commit: resolution.commit}}
	writeErr := root.appendToolResultsWithDeliveryCommitsDurably(result, result, commits, nil)
	if !fs.failed.Load() {
		t.Fatal("test setup: no write was retained")
	}
	if writeErr != nil {
		t.Fatalf("returned error = %v, want none: the durable append settles a retained entry itself", writeErr)
	}
	if got := len(inline.durable[delegateID].PendingDeliveries); got != 0 {
		t.Fatalf("pending deliveries for %s = %d, want the round's commits completed: the turn that names them is in the transcript", delegateID, got)
	}
}

// The assistant turn is the round's own content. A write that kept its entry
// leaves that content readable, so aborting before the text-end event would
// leave the live stream with an assistant turn it never closed while the
// transcript holds the whole thing.
func TestRetainedWrite_AssistantTextEndFollowsItsRound(t *testing.T) {
	t.Parallel()
	const answer = "the assistant answer a retained write kept"
	s, fs, finish := retainingSessionWatched(t, answer)
	resp := llm.Response{Message: llm.Assistant(answer), Model: "test-model", Provider: "test-provider"}
	if err := s.emitAssistantResponse(context.Background(), resp, sessionModelResponse{}, answer, false, ModelAttemptMetadata{}); err != nil {
		t.Fatalf("the round aborted over a record the transcript holds: %v", err)
	}
	if !fs.failed.Load() {
		t.Fatal("test setup: no write was retained")
	}
	if got := countTurnsWithText(t, s, answer); got != 1 {
		t.Fatalf("the assistant turn appears %d times in history, want once", got)
	}
	for _, event := range finish() {
		if event.Kind == events.EventAssistantTextEnd {
			return
		}
	}
	t.Fatal("no assistant text end was emitted for a round whose turn is in the transcript")
}

// A salvaged draft that reached the transcript is the draft a delegating
// parent's result should point at. Reporting failure leaves the salvage
// unmarked, so the parent recommends nothing while the child's transcript
// holds the partial answer.
func TestRetainedWrite_SalvagedTurnIsMarkedPersisted(t *testing.T) {
	t.Parallel()
	const salvaged = "the partial draft a retained write kept"
	s, fs := retainingSession(t, "salvage", salvaged)
	s.mu.Lock()
	s.totalRounds = 1
	s.mu.Unlock()
	if err := s.persistSalvagedTurn(salvaged, "test-model", "test-provider"); err != nil {
		t.Fatalf("salvage reported failure for a record the transcript holds: %v", err)
	}
	if !fs.failed.Load() {
		t.Fatal("test setup: no write was retained")
	}
	if !s.hasSalvageFromFinalRound() {
		t.Fatal("the salvaged turn is in the transcript but the session did not latch it")
	}
	if got := countTurnsWithText(t, s, salvaged); got != 1 {
		t.Fatalf("the salvaged turn appears %d times in history, want once", got)
	}
}

// A notification reminder that reached the transcript must not put its
// notifications back on the queue: the next wake would deliver the same
// reminder again over a turn that already recorded it.
func TestRetainedWrite_DeliveredNotificationsAreNotRequeued(t *testing.T) {
	t.Parallel()
	const jobID = "job_retained_reminder"
	s, fs := retainingSession(t, "notification", jobID)
	appendPendingJobNotificationRecordWithProvenance(t, s.jobManager, s.ID(), jobID, nil)
	s.enqueueJobNotification(jobNotification{
		JobID:   jobID,
		JobType: string(jobstore.JobShell),
		Status:  string(jobstore.StatusCompleted),
	})
	if proceed := s.acceptNotificationInput(context.Background(), "turn_notification_retained"); !proceed {
		t.Fatal("the wake stood down over a reminder the transcript holds")
	}
	if !fs.failed.Load() {
		t.Fatal("test setup: no write was retained")
	}
	s.pendingJobNotifsMu.Lock()
	pending := len(s.pendingJobNotifs)
	s.pendingJobNotifsMu.Unlock()
	if pending != 0 {
		t.Fatalf("pending job notifications = %d, want none: their reminder is in the transcript", pending)
	}
}

// A delegate's seed input is the child's whole reason to run. The seed is read
// back from the child's own transcript, which holds the retained line, so
// reporting failure would refuse to start a delegate whose input is already
// recorded.
func TestRetainedWrite_DelegatePreseedSucceeds(t *testing.T) {
	t.Parallel()
	const input = "the delegate input a retained write kept"
	child, fs := retainingSession(t, "preseed", input)
	if err := (delegateRuntime{}).preseedInput(child, input, transcriptPath(child.stateDir, child.id)); err != nil {
		t.Fatalf("preseed reported failure for a record the transcript holds: %v", err)
	}
	if !fs.failed.Load() {
		t.Fatal("test setup: no write was retained")
	}
	if got := countTurnsWithText(t, child, input); got != 1 {
		t.Fatalf("the seed turn appears %d times in history, want once", got)
	}
}
