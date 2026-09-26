package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// TestAskUser_RejectedInterruptUsesDurableBoundary covers a clean rollback of
// the tool-results record that posted ask_user. The rejected interrupt marker
// must settle from the transcript, so neither the in-memory phantom result nor
// its ask_pending side effect can survive the boundary.
func TestAskUser_RejectedInterruptUsesDurableBoundary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// TRIPWIRE: scripted in-process provider and local transcript I/O; 30s is
	// far above the expected completion time and only guards a genuine hang.
	parentCtx, parentCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer parentCancel()
	turnCtx, cancelTurn := context.WithCancel(parentCtx)
	defer cancelTurn()

	ask := askUserCall("ask1", askUserArgsValid())
	cancelCall := llm.ToolCallData{ID: "cancel1", Name: "cancel_tool", Arguments: json.RawMessage(`{}`), Type: "function"}
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask, cancelCall) },
		},
	})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	markerFailure := errors.New("interrupt marker rejected after clean rollback")
	toolResultsFailure := errors.New("tool results record rejected")
	var fs *environmentSyncFailureFS
	sess.RegisterTool("cancel_tool", "cancels after ask_user posts",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(context.Context, any) (any, error) {
			fs.mu.Lock()
			fs.writeFailure = toolResultsFailure
			fs.failure = markerFailure
			fs.mu.Unlock()
			cancelTurn()
			return "canceling", nil
		})
	fs = attachEnvironmentFailureFS(t, sess)

	_, processErr := sess.ProcessInput(turnCtx, "which db should we use?", nil)
	if !errors.Is(processErr, context.Canceled) || !errors.Is(processErr, markerFailure) {
		t.Fatalf("ProcessInput error = %v, want cancellation and rejected marker", processErr)
	}
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("live ask pending count after rejected marker = %d, want 0", got)
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("live state after rejected marker = %q, want %q", got, SessionIdle)
	}

	meta := sess.Meta()
	sess.Close()
	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.askPendingCount(); got != 0 {
		t.Fatalf("restored ask pending count = %d, want 0", got)
	}
	if got := restored.State(); got != SessionIdle {
		t.Fatalf("restored state = %q, want %q", got, SessionIdle)
	}
}

// TestFoldTail_CanceledAskPairRidingTailDoesNotResurrectAfterRestore pins the
// #2057 review's Medium, confirmed by this test before the fix: a canceled
// ask round records its pairs after a mid-flight fold's snapshot, and its
// tool-results write fails cleanly. recordTurn used to keep that pair in
// persistedAppendLog, so the released fold's rewrite tail re-appended the
// persisted pair after its markers — including ask_user's non-error posted
// ack — and a cold restore resurrected the canceled ask as pending (the
// live/restore divergence the boundary work exists to eliminate). recordTurn
// now drops a pair whose write recorded nothing, so the rewrite tail carries
// only the round's successfully recorded pairs, and the restore stays idle.
func TestFoldTail_CanceledAskPairRidingTailDoesNotResurrectAfterRestore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// TRIPWIRE: scripted in-process providers and local transcript I/O; 30s is
	// far above the expected completion time and only guards a genuine hang.
	parentCtx, parentCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer parentCancel()

	ask := askUserCall("ask1", askUserArgsValid())
	proceed := make(chan struct{})
	var proceedOnce sync.Once
	closeProceed := func() { proceedOnce.Do(func() { close(proceed) }) }
	// compactDone stays nil until the fold goroutine launches, so the
	// teardown below skips the wait for a worker that was never started.
	var compactDone chan struct{}

	// The parked summarizer response holds the fold between its snapshot and
	// its publication until the canceled round's records are in place.
	entered := make(chan struct{})
	var summaryCalls atomic.Int32
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	c.Register(&agenttest.ScriptedAdapter{Provider: "fold-tail-cheap", Responder: func(llm.Request) llm.Response {
		if summaryCalls.Add(1) == 1 {
			close(entered)
			<-proceed
		}
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
	}})
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "fold-tail-cheap/model")
	sess, err := NewSession(c, profile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	// Ordered teardown, registered after the Close defer so LIFO runs it
	// first: the fold parks inside the scripted summarizer at <-proceed,
	// and a t.Fatal taken while it is parked must release it and let the
	// fold goroutine finish before the deferred Close tears the session
	// down around it. The parked summarizer holds no locks (unlike the
	// tombstone test's parked write), so this is goroutine hygiene rather
	// than deadlock prevention.
	defer func() {
		closeProceed()
		if compactDone != nil {
			<-compactDone
		}
	}()

	seedNumberedSessionHistory(t, sess, 12) // > PreserveRecentTurns(6): forces an actual fold

	compactErr := make(chan error, 1)
	compactDone = make(chan struct{})
	go func() {
		defer close(compactDone)
		compactErr <- sess.Compact(parentCtx) // parks inside the summarizer, past its snapshot
	}()
	select {
	case <-entered: // the fold is mid-flight; nothing is locked
	case <-parentCtx.Done():
		t.Fatal("the fold never reached its parked summarizer")
	}

	// The canceled ask round's records, all appended after the fold's
	// snapshot: the round's opening user input and assistant ask turn (their
	// writes succeed), then the canceled tool-results pair whose durable
	// write fails cleanly. recordTurn keeps that pair in the pair log, which
	// is exactly the state the review claims a stale fold re-appends.
	userTurn := schema.NewTurn(schema.TurnUserInput, llm.User("which db should we use?"))
	sess.recordTurn(userTurn, userTurn)
	assistantTurn := schema.NewTurn(schema.TurnAssistant, llm.Message{
		Role:    llm.RoleAssistant,
		Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &ask}},
	})
	sess.recordTurn(assistantTurn, assistantTurn)

	toolResultsFailure := errors.New("tool results record rejected")
	fs := attachEnvironmentFailureFS(t, sess)
	fs.mu.Lock()
	fs.writeFailure = toolResultsFailure
	fs.mu.Unlock()
	askAck := tool.ExecResult{ToolName: "ask_user", CallID: "ask1", Output: "posted", FullOutput: "posted"}
	sess.appendCanceledToolResults([]llm.ToolCallData{ask}, []tool.ExecResult{askAck}, context.Canceled)

	closeProceed()
	if err := <-compactErr; err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Setup checks: the tail still carries the round's successfully recorded
	// pairs (the ask call rides with the assistant turn), but ask_user's
	// posted ack must NOT ride -- a canceled pair whose write recorded
	// nothing cannot re-enter the durable transcript through the rewrite.
	data, err := readTranscriptFull(transcriptPath(sess.stateDir, sess.id), "")
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	resumed := ResumeHistory(data.Entries)
	if !resumedHistoryCarriesAsk(resumed, "ask1") {
		t.Fatal("test setup: the assistant ask turn is missing from the post-marker resumed history -- the rewrite tail stopped carrying recorded pairs")
	}
	if resumedHistoryCarriesPostedAskResult(resumed, "ask1") {
		t.Fatal("ask_user's canceled ack rode the rewrite tail: the failed results pair re-entered the durable transcript after the fold markers")
	}

	meta := sess.Meta()
	sess.Close()
	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.askPendingCount(); got != 0 {
		t.Fatalf("restored ask pending count after fold tail = %d, want 0 (the canceled pair resurrected through the rewrite tail)", got)
	}
	if got := restored.State(); got != SessionIdle {
		t.Fatalf("restored state after fold tail = %q, want %q", got, SessionIdle)
	}
}

// TestFoldTail_FailedPairTombstoneKeepsSnapshotPositions pins the High the
// local review raised against a removal-based fix: a fold can snapshot the
// pair log while a recordTurn write is still in flight, because the write runs
// with s.mu released. If a failing write REMOVED its pair, every later pair
// would shift into the dropped position, and a fold holding that snapshot
// would slice a successfully recorded turn out of its rewrite tail -- that
// turn's only post-marker representation, silently lost on restart. The
// tombstone holds the position instead and the publication filters it, so a
// pair recorded after the failed one still rides the tail of a fold
// snapshotted mid-write.
func TestFoldTail_FailedPairTombstoneKeepsSnapshotPositions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// TRIPWIRE: scripted in-process providers and local transcript I/O; 30s is
	// far above the expected completion time and only guards a genuine hang.
	parentCtx, parentCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer parentCancel()

	ask := askUserCall("ask1", askUserArgsValid())
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var proceedOnce sync.Once
	closeProceed := func() { proceedOnce.Do(func() { close(proceed) }) }
	writeFailed := make(chan struct{})
	resumeWrite := make(chan struct{})
	var resumeOnce sync.Once
	resumeTheWrite := func() { resumeOnce.Do(func() { close(resumeWrite) }) }
	// resultsDone and compactDone stay nil until their worker goroutines
	// launch, so the teardown below skips the wait for a worker that was
	// never started.
	var resultsDone, compactDone chan struct{}
	var summaryCalls atomic.Int32
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	c.Register(&agenttest.ScriptedAdapter{Provider: "fold-tail-cheap", Responder: func(llm.Request) llm.Response {
		if summaryCalls.Add(1) == 1 {
			close(entered)
			<-proceed
		}
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
	}})
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "fold-tail-cheap/model")
	sess, err := NewSession(c, profile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	// Ordered teardown, registered after the Close defer so LIFO runs it
	// first: the canceled-results goroutine parks holding attentionMu, and
	// sess.Close cannot take that lock until resumeWrite is closed. A
	// t.Cleanup release would run only after the defers -- on any t.Fatal
	// taken while the write is parked, the deferred Close would wait forever
	// and the 30-second guard could never fire.
	defer func() {
		closeProceed()
		resumeTheWrite()
		if resultsDone != nil {
			<-resultsDone
		}
		if compactDone != nil {
			<-compactDone
		}
	}()
	seedNumberedSessionHistory(t, sess, 12)

	userTurn := schema.NewTurn(schema.TurnUserInput, llm.User("which db should we use?"))
	sess.recordTurn(userTurn, userTurn)
	assistantTurn := schema.NewTurn(schema.TurnAssistant, llm.Message{
		Role:    llm.RoleAssistant,
		Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &ask}},
	})
	sess.recordTurn(assistantTurn, assistantTurn)

	// The canceled results record parks inside its failed write -- s.mu
	// released, attentionMu held -- which is exactly where a fold's snapshot
	// can observe the logged pair mid-write.
	toolResultsFailure := errors.New("tool results record rejected")
	fs := attachEnvironmentFailureFS(t, sess)
	fs.mu.Lock()
	fs.writeFailure = toolResultsFailure
	fs.onWriteFailure = func() {
		close(writeFailed)
		<-resumeWrite
	}
	fs.mu.Unlock()
	resultsDone = make(chan struct{})
	askAck := tool.ExecResult{ToolName: "ask_user", CallID: "ask1", Output: "posted", FullOutput: "posted"}
	go func() {
		defer close(resultsDone)
		sess.appendCanceledToolResults([]llm.ToolCallData{ask}, []tool.ExecResult{askAck}, context.Canceled)
	}()
	select {
	case <-writeFailed:
	case <-parentCtx.Done():
		t.Fatal("the canceled results write never reached its failure hook")
	}

	// The fold snapshots now: the pair log holds the user, assistant, and
	// mid-write results pairs, so this snapshot's rewrite boundary is that
	// results pair's position.
	compactErr := make(chan error, 1)
	compactDone = make(chan struct{})
	go func() {
		defer close(compactDone)
		compactErr <- sess.Compact(parentCtx)
	}()
	select {
	case <-entered:
	case <-parentCtx.Done():
		t.Fatal("the fold never reached its parked summarizer")
	}

	// Release the write: it fails cleanly, the results pair becomes a
	// tombstone in place, and a successful pair then logs after it.
	resumeTheWrite()
	<-resultsDone
	recoveryTurn := schema.NewTurn(schema.TurnUserInput, llm.User("recovery after failed write"))
	sess.recordTurn(recoveryTurn, recoveryTurn)

	closeProceed()
	if err := <-compactErr; err != nil {
		t.Fatalf("Compact: %v", err)
	}

	data, err := readTranscriptFull(transcriptPath(sess.stateDir, sess.id), "")
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	resumed := ResumeHistory(data.Entries)
	if indexOfTurnText(resumed, "recovery after failed write") < 0 {
		t.Fatal("the pair recorded after the failed one is missing from the post-marker resumed history: its position shifted below the mid-write snapshot's rewrite boundary")
	}
	if resumedHistoryCarriesPostedAskResult(resumed, "ask1") {
		t.Fatal("ask_user's canceled ack rode the rewrite tail: the failed results pair re-entered the durable transcript after the fold markers")
	}

	meta := sess.Meta()
	sess.Close()
	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.askPendingCount(); got != 0 {
		t.Fatalf("restored ask pending count after fold tail = %d, want 0", got)
	}
	if got := restored.State(); got != SessionIdle {
		t.Fatalf("restored state after fold tail = %q, want %q", got, SessionIdle)
	}
}

// resumedHistoryCarriesPostedAskResult reports whether any turn in the
// resumed history carries a completed, non-error tool RESULT for the given
// call ID. Resume-time orphan repair splices an all-error placeholder for a
// call with no results, which is fine: the derivation rules skip those.
func resumedHistoryCarriesPostedAskResult(turns []schema.Turn, callID string) bool {
	for _, turn := range turns {
		for _, part := range turn.Message.Content {
			if part.ToolResult != nil && part.ToolResult.ToolCallID == callID && !part.ToolResult.IsError {
				return true
			}
		}
	}
	return false
}

// resumedHistoryCarriesAsk reports whether any turn in the resumed history
// still carries the ask_user tool call or its result for the given call ID.
func resumedHistoryCarriesAsk(turns []schema.Turn, callID string) bool {
	for _, turn := range turns {
		for _, part := range turn.Message.Content {
			if part.ToolCall != nil && part.ToolCall.ID == callID {
				return true
			}
			if part.ToolResult != nil && part.ToolResult.ToolCallID == callID {
				return true
			}
		}
	}
	return false
}

// TestDeriveRestoredAskPending_FailedAskPairNeverCollects pins the derivation
// rule that makes the #2057 phantom impossible for every fold interleaving:
// pending questions rebuild exclusively from a tool-results turn carrying a
// completed, non-error ask_user result (a posted question awaiting its
// answer). A failed ask leaves either no results turn at all (its record
// rolled back) or an error placeholder, and the assistant turn alone never
// collects -- so no transcript a rewrite tail can produce resurrects a failed
// ask as pending. Both shapes a failed ask pair can leave behind a fold's
// markers must derive idle and empty.
func TestDeriveRestoredAskPending_FailedAskPairNeverCollects(t *testing.T) {
	t.Parallel()
	ask := askUserCall("ask1", askUserArgsValid())
	askTurn := schema.NewTurn(schema.TurnAssistant, llm.Message{
		Role:    llm.RoleAssistant,
		Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &ask}},
	})
	base := []schema.Turn{
		schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")),
		schema.NewTurn(schema.TurnUserInput, llm.User("which db should we use?")),
		askTurn,
	}
	errorResults := schema.NewTurn(schema.TurnToolResults, llm.Message{
		Role: llm.RoleTool,
		Content: []llm.ContentPart{{
			Kind:       llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{ToolCallID: "ask1", Name: "ask_user", Content: "interrupted", IsError: true},
		}},
	})

	for name, history := range map[string][]schema.Turn{
		"no-results-turn":   base,
		"error-placeholder": append(append([]schema.Turn{}, base...), errorResults),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			pending, isAskRound := deriveRestoredAskPending(history, 0, nil)
			if len(pending) != 0 || isAskRound {
				t.Fatalf("deriveRestoredAskPending = %d pending (isAskRound=%v), want 0: a failed ask has no completed non-error ask_user result to collect from", len(pending), isAskRound)
			}
			if got := deriveRestoredState(history, 0, nil); got != SessionIdle {
				t.Fatalf("deriveRestoredState = %q, want %q", got, SessionIdle)
			}
		})
	}
}

// TestRestoredFailureBoundaryPublishesBeforeDoorRelease pins the ordering that
// keeps a concurrent compaction from landing between transcript restoration and
// the live state transition. The hook runs after both state and askPending have
// been published; attentionMu must still exclude a competing transcript writer.
func TestRestoredFailureBoundaryPublishesBeforeDoorRelease(t *testing.T) {
	t.Parallel()
	sess := newTestSessionForEnvctx(t)
	defer sess.Close()

	sess.mu.Lock()
	sess.state = SessionProcessing
	sess.mu.Unlock()

	checked := false
	sess.cfg.testOnly.beforeRestoredFailureBoundaryDoorRelease = func() {
		checked = true
		if got := sess.State(); got != SessionIdle {
			t.Fatalf("published state = %q, want %q before attentionMu release", got, SessionIdle)
		}
		if sess.attentionMu.TryLock() {
			sess.attentionMu.Unlock()
			t.Fatal("attentionMu was available before restored state publication door release")
		}
	}

	sess.finishProcessingAtRestoredFailureBoundary(context.Background())
	if !checked {
		t.Fatal("restored failure boundary release hook did not run")
	}
}

// TestRestoredFailureBoundaryMapsCompactedForkDivergence exercises the same
// post-compaction coordinate mapping used by restore. A child forked after the
// inherited prefix can compact before its own pending ask is interrupted; the
// raw fork index is then larger than the resumed history and must not hide the
// child's journal provenance for a legacy human-note steer.
func TestRestoredFailureBoundaryMapsCompactedForkDivergence(t *testing.T) {
	t.Parallel()
	sess := newTestSessionForEnvctx(t)
	defer sess.Close()
	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	const noteID = "cm-compacted-fork-note"
	if err := sess.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		req := testClientMutationRequest(t, clientMutationMethodNotesHumanSet, noteID, struct{ Note string }{Note: "child note"})
		snapshot.Journal[noteID] = clientMutationRecord{
			ClientMutationID:  req.ClientMutationID,
			Method:            req.Method,
			Payload:           req.Payload,
			PayloadHash:       req.PayloadHash,
			OperationState:    clientMutationOperationTerminal,
			ExecutionState:    "incorporated",
			ProjectionState:   appwire.MutationProjectionReflected,
			AttemptGeneration: 1,
		}
		return nil
	}); err != nil {
		t.Fatalf("seed note provenance: %v", err)
	}

	ask := askUserCall("ask-compacted", askUserArgsValid())
	turns := make([]schema.Turn, 0, 15)
	for range 10 {
		turns = append(turns, schema.NewTurn(schema.TurnSystem, llm.User("inherited context")))
	}
	turns = append(turns,
		schema.NewTurn(schema.TurnSummary, llm.User("compacted context")),
		schema.NewTurn(schema.TurnUserInput, llm.User("which datastore?")),
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &ask}},
		}),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("ask-compacted", "ask_user", "ack", false)),
	)
	note := schema.NewTurn(schema.TurnSteering, llm.User("updated the project note"))
	note.SteeringSource = events.SteeringSourceUser
	note.ClientMutationID = noteID
	turns = append(turns, note)
	for _, turn := range turns {
		if err := sess.writeTranscript(turn); err != nil {
			t.Fatalf("write transcript turn %s: %v", turn.Kind, err)
		}
	}

	// Ten inherited entries precede the marker, so the full-transcript fork
	// boundary is 11 while the resumed history starts at entry 10.
	sess.fork.divergence = 11
	sess.mu.Lock()
	sess.state = SessionProcessing
	sess.mu.Unlock()

	sess.finishProcessingAtRestoredFailureBoundary(context.Background())
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pending asks after compacted fork restore = %d, want 1", got)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state after compacted fork restore = %q, want %q", got, SessionAwaiting)
	}
}

// TestRestoredFailureBoundaryShiftsDivergencePastRepairs is the one shape
// that exercises the repair-insertion shift in the door's divergence
// mapping: a compacted forked child whose inherited prefix contains an
// orphaned tool call. Repair splices an all-error synthetic right after
// the orphan, so the raw resumed boundary must shift past the insertion
// or the synthetic lands child-side and the boundary no longer splits
// inherited from child-owned turns where journal provenance expects it.
func TestRestoredFailureBoundaryShiftsDivergencePastRepairs(t *testing.T) {
	t.Parallel()
	sess := newTestSessionForEnvctx(t)
	defer sess.Close()
	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	const noteID = "cm-repair-shift-note"
	if err := sess.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		req := testClientMutationRequest(t, clientMutationMethodNotesHumanSet, noteID, struct{ Note string }{Note: "child note"})
		snapshot.Journal[noteID] = clientMutationRecord{
			ClientMutationID:  req.ClientMutationID,
			Method:            req.Method,
			Payload:           req.Payload,
			PayloadHash:       req.PayloadHash,
			OperationState:    clientMutationOperationTerminal,
			ExecutionState:    "incorporated",
			ProjectionState:   appwire.MutationProjectionReflected,
			AttemptGeneration: 1,
		}
		return nil
	}); err != nil {
		t.Fatalf("seed note provenance: %v", err)
	}

	ask := askUserCall("ask-child", askUserArgsValid())
	orphan := llm.ToolCallData{ID: "orphan-read", Name: "read_notes", Arguments: json.RawMessage(`{}`), Type: "function"}
	note := schema.NewTurn(schema.TurnSteering, llm.User("updated the project note"))
	note.SteeringSource = events.SteeringSourceUser
	note.ClientMutationID = noteID
	turns := make([]schema.Turn, 0, 20)
	for range 10 {
		turns = append(turns, schema.NewTurn(schema.TurnSystem, llm.User("inherited context")))
	}
	turns = append(turns,
		schema.NewTurn(schema.TurnSummary, llm.User("compacted context")),
		schema.NewTurn(schema.TurnUserInput, llm.User("which datastore?")),
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &orphan}},
		}),
		schema.NewTurn(schema.TurnUserInput, llm.User("inherited follow-up")),
		note,
		schema.NewTurn(schema.TurnUserInput, llm.User("child question")),
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &ask}},
		}),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("ask-child", "ask_user", "ack", false)),
	)
	for _, turn := range turns {
		if err := sess.writeTranscript(turn); err != nil {
			t.Fatalf("write transcript turn %s: %v", turn.Kind, err)
		}
	}

	// The first child-owned turn is the note at full-transcript index 14;
	// repair inserts the orphan's synthetic at resumed index 3, so the raw
	// resumed boundary (4) must shift past the insertion to 5: the boundary
	// stays aligned with the post-repair history's geometry — the synthetic
	// lands inherited-side, the note and the child's ask round child-side —
	// which is what journal provenance consumers of the boundary expect.
	// This test's state/pending assertions are themselves invariant to the
	// shift (real turns move with the boundary; the all-error synthetic is
	// inert in the derivations — red-checked by disabling the shift); the
	// shift's observable is pinned by
	// TestRestoredFailureBoundaryDivergenceShiftDecidesSteerProvenance.
	sess.fork.divergence = 14
	sess.mu.Lock()
	sess.state = SessionProcessing
	sess.mu.Unlock()

	sess.finishProcessingAtRestoredFailureBoundary(context.Background())
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pending asks after repair-shifted fork restore = %d, want 1", got)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state after repair-shifted fork restore = %q, want %q", got, SessionAwaiting)
	}
}

// TestRestoredFailureBoundaryDivergenceShiftDecidesSteerProvenance pins the
// observable the repair-insertion shift exists for: an inherited steering
// turn carrying a client mutation ID that also exists in the child's
// journal must stay on the inherited side of the boundary, where the
// provenance scan cannot consult the child's journal (a child mutation may
// reuse a parent's ID). The kindless steer looks like a human-note carrier
// through the child's journal record but is an ordinary user steer by its
// text, so the two classifications disagree on whether it answers an
// earlier still-pending ask: inherited it resolves the boundary (the
// parent's steer answered the parent's ask, so only the child's own ask
// stays pending); misclassified child-side it masquerades as a
// non-resolving carrier and the inherited ask wrongly survives. The repair
// insertion before the steer is what moves the boundary past it — without
// the shift the steer falls child-side and this test fails with
// pending = 2.
func TestRestoredFailureBoundaryDivergenceShiftDecidesSteerProvenance(t *testing.T) {
	t.Parallel()
	sess := newTestSessionForEnvctx(t)
	defer sess.Close()
	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}

	// The child's journal carries a notes/human/set record under the same
	// client mutation ID the inherited steer references.
	const noteID = "cm-shared-note-id"
	if err := sess.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		req := testClientMutationRequest(t, clientMutationMethodNotesHumanSet, noteID, struct{ Note string }{Note: "child note"})
		snapshot.Journal[noteID] = clientMutationRecord{
			ClientMutationID:  req.ClientMutationID,
			Method:            req.Method,
			Payload:           req.Payload,
			PayloadHash:       req.PayloadHash,
			OperationState:    clientMutationOperationTerminal,
			ExecutionState:    "incorporated",
			ProjectionState:   appwire.MutationProjectionReflected,
			AttemptGeneration: 1,
		}
		return nil
	}); err != nil {
		t.Fatalf("seed note provenance: %v", err)
	}

	inheritedAsk := askUserCall("ask-inherited", askUserArgsValid())
	childAsk := askUserCall("ask-child", askUserArgsValid())
	orphan := llm.ToolCallData{ID: "orphan-read", Name: "read_notes", Arguments: json.RawMessage(`{}`), Type: "function"}
	// The kindless inherited steer: child-side it reads as a human-note
	// carrier through the journal record; inherited its ordinary user text
	// resolves the boundary.
	inheritedSteer := schema.NewTurn(schema.TurnSteering, llm.User("Postgres, let's use it"))
	inheritedSteer.SteeringSource = events.SteeringSourceUser
	inheritedSteer.ClientMutationID = noteID
	// The child's own round is a non-resolving human-note carrier, so the
	// scan walks past it to the inherited steer before anything resolves.
	childSteer := schema.NewTurn(schema.TurnSteering, llm.User("updated the project note"))
	childSteer.SteeringSource = events.SteeringSourceUser
	childSteer.SteeringKind = events.SteeringKindHumanNote
	turns := make([]schema.Turn, 0, 20)
	for range 10 {
		turns = append(turns, schema.NewTurn(schema.TurnSystem, llm.User("inherited context")))
	}
	turns = append(turns,
		schema.NewTurn(schema.TurnSummary, llm.User("compacted context")),
		schema.NewTurn(schema.TurnUserInput, llm.User("which datastore?")),
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &orphan}},
		}),
		schema.NewTurn(schema.TurnUserInput, llm.User("which queue?")),
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &inheritedAsk}},
		}),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("ask-inherited", "ask_user", "ack", false)),
		inheritedSteer,
		// Bookkeeping after the steer: steeringOriginBoundary treats the
		// turn at divergence-1 as child-side, so the steer must not be the
		// last turn before the fork point — a system turn absorbs that
		// position without resolving anything on either side of the scan.
		schema.NewTurn(schema.TurnSystem, llm.User("inherited bookkeeping")),
		childSteer,
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &childAsk}},
		}),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("ask-child", "ask_user", "ack", false)),
	)
	for _, turn := range turns {
		if err := sess.writeTranscript(turn); err != nil {
			t.Fatalf("write transcript turn %s: %v", turn.Kind, err)
		}
	}

	// The first child-owned turn is the child's carrier steer at
	// full-transcript index 18; repair inserts the orphan's synthetic at
	// resumed index 3, so the raw resumed boundary (8) must shift to 9 to
	// keep the inherited steer at 7 on the inherited side, where the scan
	// consults no journal and its ordinary user text resolves the
	// boundary.
	sess.fork.divergence = 18
	sess.mu.Lock()
	sess.state = SessionProcessing
	sess.mu.Unlock()

	sess.finishProcessingAtRestoredFailureBoundary(context.Background())
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("pending asks after steer-provenance boundary = %d, want 1 (the child's own ask; the inherited steer answered the parent's)", got)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state after steer-provenance boundary = %q, want %q", got, SessionAwaiting)
	}
}

// TestRestoredFailureBoundaryWarnsOnUnparseablePendingAsk pins the live
// boundary's malformed-ask diagnostic: the door derives pending from the
// transcript exactly as restore does, and an ask round whose questions
// cannot parse leaves the pending-ask holds inert. Restore warns about
// that (session_init.go), so the interrupt-rejection path must warn too.
func TestRestoredFailureBoundaryWarnsOnUnparseablePendingAsk(t *testing.T) {
	t.Parallel()
	sess := newTestSessionForEnvctx(t)
	defer sess.Close()

	// Durable arguments that fail parseAskQuestions's validation: two
	// recommended options in one question is an all-or-nothing violation,
	// so questionsFromAskCalls skips the call ("rebuild what parses") and
	// the round is recognized as pending while none of its questions parse.
	mangled := llm.ToolCallData{ID: "ask-mangled", Name: "ask_user", Arguments: json.RawMessage(`{"questions":[{"header":"H","question":"Q?","options":[{"label":"A","recommended":true},{"label":"B","recommended":true}]}]}`), Type: "function"}
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("which datastore?")),
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &mangled}},
		}),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("ask-mangled", "ask_user", "ack", false)),
	}
	collected := make(chan []string, 1)
	go func() {
		var msgs []string
		for ev := range sess.Events() {
			if ev.Kind != events.EventWarning {
				continue
			}
			if data, ok := ev.Data.(events.WarningData); ok {
				msgs = append(msgs, data.Message)
			}
		}
		collected <- msgs
	}()
	for _, turn := range turns {
		if err := sess.writeTranscript(turn); err != nil {
			t.Fatalf("write transcript turn %s: %v", turn.Kind, err)
		}
	}
	sess.mu.Lock()
	sess.state = SessionProcessing
	sess.mu.Unlock()

	sess.finishProcessingAtRestoredFailureBoundary(context.Background())
	if got := sess.askPendingCount(); got != 0 {
		t.Fatalf("unparseable ask round left pending asks = %d, want 0", got)
	}
	// A no-op boundary (already settled; the session is no longer
	// Processing) must not repeat the warning: the live pending set was
	// left untouched, so the holds still apply and a second warning would
	// be false.
	sess.finishProcessingAtRestoredFailureBoundary(context.Background())
	sess.Close()
	msgs := <-collected
	count := 0
	for _, msg := range msgs {
		if strings.HasPrefix(msg, "interrupt boundary: found a pending ask_user round") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("interrupt-boundary warning count = %d, want exactly 1 (one real settlement, one silent no-op)", count)
	}
}
