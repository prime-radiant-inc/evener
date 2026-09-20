package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
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
	for i := 0; i < 10; i++ {
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
