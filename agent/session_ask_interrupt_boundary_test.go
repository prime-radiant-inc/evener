package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
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

// TestAskUser_RejectedAskAppendedDuringFoldTailDoesNotResurrectAfterRestore
// closes the concurrent window the #2057 review flagged: a failed ask pair
// appended after a fold's snapshot riding the publication's rewrite tail.
// The fold parks inside its scripted summarizer strictly after the
// history/pair-log snapshot; the ask turn records its pair and settles the
// rejected boundary inside that blocked interval. The settlement's state
// publication is serialized against fold publications -- it consumes the
// pending pair log and bumps the history revision atomically -- so the
// released fold's first publish is fenced and its retry re-snapshots past
// the pair, folding it away together with its opening user input. A cold
// restore must still derive idle with no pending ask. The reviewer's other
// interleaving (the fold publishing the pair through the tail ahead of the
// settlement) is pinned impossible by the derivation rule the companion
// test below covers.
func TestAskUser_RejectedAskAppendedDuringFoldTailDoesNotResurrectAfterRestore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// TRIPWIRE: scripted in-process providers and local transcript I/O; 30s is
	// far above the expected completion time and only guards a genuine hang.
	parentCtx, parentCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer parentCancel()
	turnCtx, cancelTurn := context.WithCancel(parentCtx)
	defer cancelTurn()

	ask := askUserCall("ask1", askUserArgsValid())
	cancelCall := llm.ToolCallData{ID: "cancel1", Name: "cancel_tool", Arguments: json.RawMessage(`{}`), Type: "function"}
	proceed := make(chan struct{})

	// The parked summarizer response holds the fold between its snapshot and
	// its publication until the canceled round's handler releases it.
	entered := make(chan struct{})
	var summaryCalls atomic.Int32
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask, cancelCall) },
		},
	})
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

	seedNumberedSessionHistory(t, sess, 12) // > PreserveRecentTurns(6): forces an actual fold

	compactErr := make(chan error, 1)
	go func() {
		compactErr <- sess.Compact(parentCtx) // parks inside the summarizer, past its snapshot
	}()
	<-entered // the fold is mid-flight; nothing is locked

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

	close(proceed)
	if err := <-compactErr; err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Setup check: the concurrent scenario really happened -- the transcript
	// carries both the fold's markers and the ask turn. Whether the pair
	// landed inside the re-snapshot's fold (settlement won the publication
	// race) or after the markers through the rewrite tail (fold won) varies
	// by run; the restore contract below must hold for both.
	data, err := readTranscriptFull(transcriptPath(sess.stateDir, sess.id))
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	var hasFold, hasAsk bool
	for _, entry := range data.Entries {
		if entry.Turn.Kind == schema.TurnCheckpoint || entry.Turn.Kind == schema.TurnSummary {
			hasFold = true
		}
		if resumedHistoryCarriesAsk([]schema.Turn{entry.Turn}, "ask1") {
			hasAsk = true
		}
	}
	if !hasFold || !hasAsk {
		t.Fatalf("test setup: transcript carries fold=%v ask=%v, want both", hasFold, hasAsk)
	}

	meta := sess.Meta()
	sess.Close()
	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.askPendingCount(); got != 0 {
		t.Fatalf("restored ask pending count after concurrent fold tail = %d, want 0 (the failed pair resurrected through the rewrite tail)", got)
	}
	if got := restored.State(); got != SessionIdle {
		t.Fatalf("restored state after concurrent fold tail = %q, want %q", got, SessionIdle)
	}
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
