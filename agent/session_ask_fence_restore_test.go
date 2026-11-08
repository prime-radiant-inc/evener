package agent

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// canonicalAskToolState builds the ToolState the live ask_user exec records,
// so a restored ask keeps its call identity instead of degrading to the
// legacy text-only path (questionsFromAskCalls only mints TurnID/CallID pairs
// from the canonical state).
func canonicalAskToolState(t *testing.T, args map[string]any) []byte {
	t.Helper()
	normalized, err := normalizeAskArgs(args)
	if err != nil {
		t.Fatalf("normalize ask args: %v", err)
	}
	parsed, err := parseAskQuestions(normalized)
	if err != nil {
		t.Fatalf("parse ask questions: %v", err)
	}
	state, err := json.Marshal(schema.AskUserState{Version: 1, Questions: projectAskUserQuestions(normalized, parsed)})
	if err != nil {
		t.Fatalf("marshal ask state: %v", err)
	}
	return state
}

// Round-7 S6, pinned as a red: the fence's specified check placement is on the
// wrong side of the durable append. Both clear sites run AFTER the answer turn
// is durable (processOneInput appends at agent/session_lifecycle.go:2332 and
// clears at :2344; the resolving steer appends then clears via
// clearAskPendingForResolvingSteer), and on restore
// turnResolvesAskBoundary resolves ANY durable TurnUserInput
// (agent/session_tools_ask.go). So a mismatch the fence refuses at a clear site
// can refuse the clear but cannot undo the durable answer — a crash then
// restores the ask as resolved, and spec 4's "never admitted as an answer" is
// unachievable from that placement.
//
// This red reproduces the exact durable state a refused clear-site check
// leaves — the answer turn durably appended, the live clear never run — by
// running the same admission append the real path runs
// (appendTurnAfterTranscriptWrite with the client-mutation transcript writer,
// session_lifecycle.go:2332-2336) minus the clear, then restoring. It asserts
// the promised outcome and fails today because restore's boundary scan treats
// any durable user input as resolving, regardless of whether the answer was
// admitted.
//
// This red is the refutation of the clear-site placement; when the comparison
// moves before the durable append (the admission transaction), a refused
// answer never becomes durable and this state is unreachable — the implementer
// lane retires this red with the machinery and inherits the scoped-restore
// agreement tests in its place (see specs/manager-tools/fence-spike.md).
func TestAskFenceRefusedAnswerMustNotResolveAcrossRestore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newSession(t, withConfig(minimalTestSessionConfig(t)), withStateDir(dir))

	// An ask round, as the live ask path records it: the assistant call and
	// its non-error ask_user result.
	call := askUserCall("ask-call-1", askUserArgsValid())
	s.appendTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant,
		Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}})
	askTurnID := s.history[len(s.history)-1].TurnID
	s.appendTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool,
		Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
			Name: "ask_user", ToolCallID: call.ID, Content: "ack", ToolState: canonicalAskToolState(t, askUserArgsValid())}}}})

	// The refused answer: durable, naming the ask it was composed against
	// (the admission path samples AnswerToTurnIDs the same way,
	// session_lifecycle.go:2331), never admitted, never cleared.
	answer := schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser,
		Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "delayed human answer"}}})
	answer.AnswerToTurnIDs = []string{askTurnID}
	if err := s.appendTurnAfterTranscriptWrite(answer,
		func() error { return s.appendClientMutationTranscriptLocked(answer) },
		func() { s.history = append(s.history, answer) },
	); err != nil {
		t.Fatalf("append refused answer: %v", err)
	}

	// The crash-restart: the daemon dies with the answer durable and the ask
	// unresolved; restore re-derives the pending set from the transcript.
	meta, closing := s.Meta(), s.stateDir
	s.Close()
	restored := restoreAskSession(t, meta, closing)

	// Promised (spec 4): the refused answer was "never admitted as an
	// answer", so the ask must still be pending after the crash. RED today:
	// restore resolves it because the boundary scan accepts any durable
	// TurnUserInput.
	if !restored.HasPendingAsk() {
		t.Fatalf("restore resolved an ask whose answer the fence refused: a durable user input must not resolve an unadmitted answer")
	}
	if got := restored.PendingAskCalls(); got == nil || len(got.Calls) != 1 {
		t.Fatalf("restored pending set = %+v, want the one refused ask's call", got)
	}
}

// TestAskRestoreKeepsAsksPostedAfterADurableAnswer pins the restore scan's
// recency scoping, which the redesigned clear must agree with. Walking
// backward, deriveRestoredAskPending accumulates ask rounds it passes and
// a durable user input survives restore even though a live whole-set clear at
// the input's consumption would have wiped it. The spike's scoped clear
// (clearing exactly the calls the answer was admitted against) makes the live
// session agree with this restore behavior instead of leaving the crash
// window resurrecting questions the live clear removed. This test passes
// today; it is the restore half of the scoped semantics the implementer lane
// inherits, and the anti-regression for the restore changes.
func TestAskRestoreKeepsAsksPostedAfterADurableAnswer(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newSession(t, withConfig(minimalTestSessionConfig(t)), withStateDir(dir))

	call1 := askUserCall("ask-call-early", askUserArgsValid())
	s.appendTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant,
		Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call1}}})
	earlyTurnID := s.history[len(s.history)-1].TurnID
	s.appendTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool,
		Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
			Name: "ask_user", ToolCallID: call1.ID, Content: "ack", ToolState: canonicalAskToolState(t, askUserArgsValid())}}}})

	// A durable answer naming the ask it resolves.
	answer := schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser,
		Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "answer to the early ask"}}})
	answer.AnswerToTurnIDs = []string{earlyTurnID}
	if err := s.appendTurnAfterTranscriptWrite(answer,
		func() error { return s.appendClientMutationTranscriptLocked(answer) },
		func() { s.history = append(s.history, answer) },
	); err != nil {
		t.Fatalf("append answer: %v", err)
	}

	// A later ask, posted after the answer.
	call2 := askUserCall("ask-call-late", askUserArgsValid())
	s.appendTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant,
		Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call2}}})
	s.appendTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool,
		Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
			Name: "ask_user", ToolCallID: call2.ID, Content: "ack", ToolState: canonicalAskToolState(t, askUserArgsValid())}}}})

	meta, closing := s.Meta(), s.stateDir
	s.Close()
	restored := restoreAskSession(t, meta, closing)

	if !restored.HasPendingAsk() {
		t.Fatal("restore dropped the ask posted after the durable answer — the restore scan is recency-scoped and must keep it")
	}
	got := restored.PendingAskCalls()
	if got == nil || len(got.Calls) != 1 || got.Calls[0].CallID != "ask-call-late" {
		t.Fatalf("restored pending set = %+v, want exactly the late ask's call", got)
	}
}
