package agent

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

func TestConversationProvenance_RealFoldPreservesCoveredInputs(t *testing.T) {
	t.Parallel()
	s := newScriptedSummaryCompactSession(t, "conversation-fold-cheap", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nOriginal request remains relevant.\n[END SUMMARY]")}
	}, withConfig(minimalTestSessionConfig(t)), withStateDir(t.TempDir()))
	s.client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(llm.Request) llm.Response {
		call := communicateCall("before-fold", "before fold")
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}
	}}})
	if _, err := s.ProcessInput(context.Background(), "original-fold-input-612", nil); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	original := slices.Clone(s.conversation.inputs)
	s.mu.Unlock()
	if len(original) != 1 {
		t.Fatalf("original consumed inputs = %v", original)
	}
	forceRealRoundSummaryFold(t, s, 0.5, 0.85)
	var rt events.RoundTimings
	if _, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 2, &rt); err != nil {
		t.Fatal(err)
	}
	entries := readPersistedTranscriptEntries(t, s.stateDir, s.ID())
	marker := false
	for _, e := range entries {
		if e.Turn.Kind == schema.TurnSummary {
			marker = true
		}
	}
	if !marker {
		t.Fatal("real summarizer did not commit a summary")
	}
	s.mu.Lock()
	coverage := slices.Clone(s.conversation.preparedInputs)
	s.mu.Unlock()
	if !slices.Equal(coverage, original) {
		t.Fatalf("actual post-fold request lost original input coverage: got=%v want=%v", coverage, original)
	}
}

func TestConversationProvenance_FoldThenRestorePreservesEligibility(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newScriptedSummaryCompactSession(t, "conversation-restored-fold", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nFour original requests remain relevant.\n[END SUMMARY]")}
	}, withConfig(minimalTestSessionConfig(t)), withStateDir(dir))
	response := func(llm.Request) llm.Response {
		call := communicateCall("fold-result", "result")
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}
	}
	s.client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{response, response, response, response}})
	for i := range 4 {
		if _, err := s.ProcessInput(context.Background(), fmt.Sprintf("actual-fold-input-%d", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	original := s.ConversationProvenance().AdmittedInputs
	if len(original) != 4 {
		t.Fatalf("real admissions=%v", original)
	}
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "", nil }
	s.contextMgr.CheckpointThreshold = 2
	s.contextMgr.SummarizeThreshold = 0.5
	s.contextMgr.PreserveRecentTurns = 0
	forcePressureAbove(t, s, 0.85)
	var timings events.RoundTimings
	if _, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 5, &timings); err != nil {
		t.Fatal(err)
	}
	marker := false
	for _, entry := range readPersistedTranscriptEntries(t, dir, s.ID()) {
		marker = marker || entry.Turn.Kind == schema.TurnSummary
	}
	if !marker {
		t.Fatal("real admitted history was not summarized")
	}
	s.Close()
	meta, err := schema.LoadSessionMeta(dir, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{response}})
	restored, err := RestoreSessionFromMetaWithConfig(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err := restored.ProcessInput(context.Background(), "post-fold-restore-input", nil); err != nil {
		t.Fatal(err)
	}
	state := restored.ConversationProvenance()
	ids, ok := appwire.ResolveConversationFrontier(state, state.ConsumedFrontierID)
	if !ok || len(ids) != 5 {
		t.Fatalf("restored actual request lost summarized coverage: state=%+v ids=%v", state, ids)
	}
	for _, id := range original {
		if !slices.Contains(ids, id) {
			t.Fatalf("restored request lost original admission %s", id)
		}
	}
	var output *appwire.ConversationOutputProvenance
	for index, entry := range readPersistedTranscriptEntries(t, dir, s.ID()) {
		if item, ok := apptranscript.CommunicateItem(entry.Turn.TurnID, index, entry.Turn); ok {
			output = item.ConversationProvenance
		}
	}
	if !appwire.ConversationOutputEligible(state, output) {
		t.Fatal("restored committed communicate was not eligible after consuming all inputs")
	}
}
