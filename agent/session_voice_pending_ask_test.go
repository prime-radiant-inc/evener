package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestVoicePendingAsk_ExecutedCanonicalState(t *testing.T) {
	t.Parallel()
	original := askUserArgsValid()
	executed := askUserArgsValid()
	questions := executed["questions"].([]any)
	first := questions[0].(map[string]any)
	first["question"] = strings.Repeat("executed-719 ", 40)
	first["header"] = "header-719"
	first["why"] = "why-719"
	first["if_unanswered"] = "fallback-719"
	first["multi_select"] = true
	first["options"] = []any{map[string]any{"label": "choice-2", "detail": "detail-2", "recommended": true}, map[string]any{"label": "choice-1", "detail": "detail-1"}}
	executed["questions"] = append(questions, map[string]any{"question": "question-2", "options": first["options"]})
	hookJSON, err := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "updatedInput": executed}})
	if err != nil {
		t.Fatal(err)
	}
	s := newSession(t, withConfig(minimalTestSessionConfig(t)), withStateDir(t.TempDir()), withSteps(func(llm.Request) llm.Response {
		return agenttest.ToolCallResponse(askUserCall("canonical-call", original), askUserCall("canonical-call-2", original))
	}))
	runner := hooks.NewRunner(nil, "")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{Matcher: "AskUserQuestion", Type: "command", Command: "printf '%s' '" + string(hookJSON) + "'", Timeout: 5})
	s.hookRunner = runner
	if _, err := s.ProcessInput(tripwireCtx(t), "input-719", nil); err != nil {
		t.Fatal(err)
	}
	if len(s.PendingAskArguments()) != 2 {
		t.Fatal("validated ask did not execute")
	}
	executedQuestions, err := ParseAskUserCallArguments(s.PendingAskArguments()[0])
	if err != nil {
		t.Fatal(err)
	}
	if executedQuestions[0].Question != first["question"] {
		t.Fatal("hook replacement did not reach Exec")
	}
	want := []schema.AskUserQuestion{
		{Header: "header-719", Question: first["question"].(string), MultiSelect: true, Why: "why-719", IfUnanswered: "fallback-719", Options: []schema.AskUserOption{{Label: "choice-2", Detail: "detail-2", Recommended: true}, {Label: "choice-1", Detail: "detail-1"}}},
		{Question: "question-2", Options: []schema.AskUserOption{{Label: "choice-2", Detail: "detail-2", Recommended: true}, {Label: "choice-1", Detail: "detail-1"}}},
	}
	_, refs := s.PendingAskSnapshot()
	if refs.Version != 1 || refs.InstanceID != s.ID() || refs.Revision == 0 || len(refs.Calls) != 2 || refs.Calls[0].CallID != "canonical-call" || refs.Calls[1].CallID != "canonical-call-2" {
		t.Fatalf("live refs=%+v", refs)
	}
	found := 0
	var origin *schema.ConversationOutputProvenance
	for _, entry := range readPersistedTranscriptEntries(t, s.stateDir, s.ID()) {
		if entry.Turn.Kind == schema.TurnAssistant {
			origin = entry.Turn.ConversationProvenance
			if origin == nil || origin.RequestID == "" || origin.FrontierID == "" || origin.RunGeneration == 0 || entry.Turn.TurnID != refs.Calls[0].TurnID {
				t.Fatalf("ask lacks actual request origin: %+v", entry.Turn)
			}
			for _, call := range assistantToolCalls(entry.Turn.Message) {
				originalQuestions, err := ParseAskUserCallArguments(call.Arguments)
				if err != nil {
					t.Fatal(err)
				}
				if reflect.DeepEqual(originalQuestions, want) {
					t.Fatal("test failed to distinguish original from executed content")
				}
			}
		}
		if entry.Turn.Kind != schema.TurnToolResults {
			continue
		}
		for _, part := range entry.Turn.Message.Content {
			if part.ToolResult == nil || part.ToolResult.Name != "ask_user" {
				continue
			}
			found++
			if part.ToolResult.IsError {
				t.Fatal("ask execution failed")
			}
			var state struct {
				Version   int                      `json:"version"`
				Questions []schema.AskUserQuestion `json:"questions"`
			}
			if err := json.Unmarshal(part.ToolResult.ToolState, &state); err != nil {
				t.Fatalf("successful validated ask has no canonical persisted state: %v", err)
			}
			if state.Version != 1 || !reflect.DeepEqual(state.Questions, want) {
				t.Fatalf("canonical state lost executed public fields: %+v", state)
			}
		}
	}
	if found != 2 {
		t.Fatal("successful result not persisted")
	}
	meta := s.Meta()
	dir := s.stateDir
	s.Close()
	restored := restoreAskSession(t, meta, dir)
	if got, _ := restored.PendingAskSnapshot(); got == nil || got.Count != 2*len(want) {
		t.Fatalf("restore lost executed batch: %+v", got)
	}
	restored.mu.Lock()
	pending := append([]askQuestion(nil), restored.askPending...)
	restored.mu.Unlock()
	_, restoredState := restored.PendingAskSnapshot()
	if !reflect.DeepEqual(restoredState.Calls, refs.Calls) {
		t.Fatalf("restored refs=%+v want %+v", restoredState, refs)
	}
	for _, entry := range readPersistedTranscriptEntries(t, dir, restored.ID()) {
		if entry.Turn.Kind == schema.TurnAssistant && !reflect.DeepEqual(entry.Turn.ConversationProvenance, origin) {
			t.Fatal("restore changed origin")
		}
	}
	for i, q := range pending {
		if q.Question != want[i%len(want)].Question {
			t.Fatal("restore used original instead of executed question")
		}
	}
}

func TestVoicePendingAsk_FreeTextRetryClearsOnce(t *testing.T) {
	t.Parallel()
	s, adapter := newDurableAdmissionAskSessionWithAdapter(t, SessionConfig{})
	seedDurableAdmissionAsk(t, s)
	_, before := s.PendingAskSnapshot()
	if len(before.Calls) != 1 {
		t.Fatalf("ask not owned: %+v", before)
	}
	adapter.appendSteps(func(req llm.Request) llm.Response {
		if !conversationRequestContains(req, "free-text-729 neither option, keep order 2 then 1") {
			t.Fatal("free text did not reach provider unchanged")
		}
		return agenttest.CommunicateResponse(true, "output-729")
	})
	params := appwire.TurnStartParams{ClientMutationID: "answer-729", Input: []appwire.InputItem{{Type: "text", Text: "free-text-729 neither option, keep order 2 then 1"}}}
	if _, err := s.AcceptClientMutationStart(params); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := s.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claim=%v err=%v", ok, err)
	}
	if _, err := s.ProcessInput(withQueuedClientMutation(tripwireCtx(t), claimed), params.Input[0].Text, nil); err != nil {
		t.Fatal(err)
	}
	_, after := s.PendingAskSnapshot()
	if after == nil || len(after.Calls) != 0 || after.Revision <= before.Revision {
		t.Fatalf("answer did not clear owned calls: %+v", after)
	}
	if _, err := s.AcceptClientMutationStart(params); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.claimClientMutationStart(); err != nil || ok {
		t.Fatalf("completed retry became runnable again: claimed=%v err=%v", ok, err)
	}
	if _, got := s.PendingAskSnapshot(); got.Revision != after.Revision {
		t.Fatalf("retry cleared twice: before=%+v after=%+v", after, got)
	}
	answers := 0
	for _, entry := range readPersistedTranscriptEntries(t, s.stateDir, s.ID()) {
		if entry.Turn.ClientMutationID == params.ClientMutationID && entry.Turn.Kind == schema.TurnUserInput {
			answers++
			if entry.Turn.Message.Text() != params.Input[0].Text || !reflect.DeepEqual(entry.Turn.AnswerToTurnIDs, []string{before.Calls[0].TurnID}) {
				t.Fatalf("answer changed payload or attribution: %+v", entry.Turn)
			}
		}
	}
	if answers != 1 || len(adapter.Requests()) != 2 {
		t.Fatalf("answers=%d provider requests=%d", answers, len(adapter.Requests()))
	}
	meta, dir := s.Meta(), s.stateDir
	s.Close()
	restored := restoreAskSession(t, meta, dir)
	if _, got := restored.PendingAskSnapshot(); got == nil || len(got.Calls) != 0 || restored.HasPendingAsk() {
		t.Fatalf("resolved ask resurrected: %+v", got)
	}
}

func TestVoicePendingAsk_RejectedCallsHaveNoCanonicalState(t *testing.T) {
	t.Parallel()
	for _, denied := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid", true: "denied"}[denied], func(t *testing.T) {
			args := askUserArgsValid()
			if !denied {
				args["questions"].([]any)[0].(map[string]any)["options"] = []any{map[string]any{"label": "same", "detail": "a"}, map[string]any{"label": "same", "detail": "b"}}
			}
			s := newSession(t, withConfig(minimalTestSessionConfig(t)), withStateDir(t.TempDir()), withSteps(func(llm.Request) llm.Response { return agenttest.ToolCallResponse(askUserCall("rejected", args)) }, func(llm.Request) llm.Response { return agenttest.CommunicateResponse(true, "done") }))
			if denied {
				runner := hooks.NewRunner(nil, "")
				runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{Matcher: "AskUserQuestion", Type: "command", Command: `printf '%s' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny"}}'`, Timeout: 5})
				s.hookRunner = runner
			}
			if _, err := s.ProcessInput(tripwireCtx(t), "input-93", nil); err != nil {
				t.Fatal(err)
			}
			result, ok := findToolResultInHistory(s.history, "rejected")
			if !ok || !result.IsError || len(result.ToolState) != 0 {
				t.Fatalf("rejected call exposed canonical state: %+v", result)
			}
			if _, pending := s.PendingAskSnapshot(); s.HasPendingAsk() || len(pending.Calls) != 0 {
				t.Fatal("rejected ask acquired pending ownership")
			}
		})
	}
}

func TestVoicePendingAsk_InternalFoldDuringHumanNote(t *testing.T) {
	t.Parallel()
	summarize := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nPending work retained.\n[END SUMMARY]")}
	}
	s := newScriptedSummaryCompactSession(t, "pending-fold-cheap", summarize, withConfig(minimalTestSessionConfig(t)), withStateDir(t.TempDir()))
	response := func(llm.Request) llm.Response { return agenttest.CommunicateResponse(true, "output-73") }
	s.client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{response, response, response, response, func(llm.Request) llm.Response {
		return agenttest.ToolCallResponse(askUserCall("before-fold", askUserArgsValid()))
	}, response}})
	for range 5 {
		if _, err := s.ProcessInput(tripwireCtx(t), "input-73", nil); err != nil {
			t.Fatal(err)
		}
	}
	_, refs := s.PendingAskSnapshot()
	if len(refs.Calls) != 1 {
		t.Fatal("pending ask setup failed")
	}
	original, ok := findToolResultInHistory(s.history, "before-fold")
	if !ok || len(original.ToolState) == 0 {
		t.Fatal("pending source has no canonical state")
	}
	originFor := func(history []schema.Turn) *schema.ConversationOutputProvenance {
		for _, turn := range history {
			for _, call := range assistantToolCalls(turn.Message) {
				if call.ID == "before-fold" {
					return turn.ConversationProvenance
				}
			}
		}
		return nil
	}
	origin := originFor(s.history)
	if origin == nil {
		t.Fatal("pending source has no recorded request provenance")
	}
	if err := s.Compact(tripwireCtx(t)); err == nil {
		t.Fatal("explicit compact accepted pending ask")
	}
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "", nil }
	s.contextMgr.CheckpointThreshold = 2
	s.contextMgr.SummarizeThreshold = 0.5
	s.contextMgr.PreserveRecentTurns = 0
	forcePressureAbove(t, s, 0.92)
	if _, err := s.SetHumanNote("note-73", "note-content-73"); err != nil {
		t.Fatal(err)
	}
	if _, ran, err := s.ProcessPendingUserInput(tripwireCtx(t), nil); err != nil || !ran {
		t.Fatalf("note round ran=%v err=%v", ran, err)
	}
	folded := false
	for _, entry := range readPersistedTranscriptEntries(t, s.stateDir, s.ID()) {
		folded = folded || entry.Turn.Kind == schema.TurnSummary
	}
	if !folded {
		t.Fatal("scenario never triggered real internal fold")
	}
	if _, pending := s.PendingAskSnapshot(); !reflect.DeepEqual(pending.Calls, refs.Calls) {
		t.Fatalf("live fold lost pending refs: %+v", pending)
	}
	if !reflect.DeepEqual(originFor(s.history), origin) {
		t.Fatal("live fold changed pending source provenance")
	}
	meta, dir := s.Meta(), s.stateDir
	s.Close()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{response}})
	client.Register(&agenttest.ScriptedAdapter{Provider: "pending-fold-cheap", Responder: summarize})
	restored, err := RestoreSessionFromMeta(client, withTestSessionNamer(client, WithCheapModel(NewOpenAIProfile("gpt-5.2"), "pending-fold-cheap/model")), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restored.Close)
	if _, pending := restored.PendingAskSnapshot(); !reflect.DeepEqual(pending.Calls, refs.Calls) {
		t.Fatalf("internal fold lost pending source across restore: got=%+v want=%+v", pending, refs)
	}
	result, ok := findToolResultInHistory(restored.history, "before-fold")
	if !ok || string(result.ToolState) != string(original.ToolState) {
		t.Fatal("fold restore lost canonical public content")
	}
	if !reflect.DeepEqual(originFor(restored.history), origin) {
		t.Fatal("fold restore changed pending source provenance")
	}
	if _, err := restored.ProcessInput(tripwireCtx(t), "answer-73 outside the listed options", nil); err != nil {
		t.Fatal(err)
	}
	if _, pending := restored.PendingAskSnapshot(); restored.HasPendingAsk() || len(pending.Calls) != 0 {
		t.Fatal("ordinary answer did not release pending tail")
	}
	countSummaries := func() int {
		count := 0
		for _, entry := range readPersistedTranscriptEntries(t, dir, restored.ID()) {
			if entry.Turn.Kind == schema.TurnSummary {
				count++
			}
		}
		return count
	}
	before := countSummaries()
	restored.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "", nil }
	restored.contextMgr.PreserveRecentTurns = 0
	if err := restored.Compact(tripwireCtx(t)); err != nil {
		t.Fatal(err)
	}
	if countSummaries() <= before {
		t.Fatal("post-answer compaction did not persist a new summary")
	}
	meta = restored.Meta()
	restored.Close()
	final := restoreAskSession(t, meta, dir)
	if _, pending := final.PendingAskSnapshot(); final.HasPendingAsk() || len(pending.Calls) != 0 {
		t.Fatal("post-answer fold resurrected resolved source")
	}
}

// Legacy fixture records deliberately omit both canonical state and request
// provenance. Ordinary text restoration stays available without voice identity.
func TestVoicePendingAsk_LegacyRestoreStaysTextOnly(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(minimalTestSessionConfig(t)), withStateDir(t.TempDir()))
	call := askUserCall("legacy-call", askUserArgsValid())
	s.appendTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}})
	s.appendTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{Name: "ask_user", ToolCallID: call.ID, Content: "legacy-ack"}}}})
	meta, dir := s.Meta(), s.stateDir
	s.Close()
	restored := restoreAskSession(t, meta, dir)
	if question, _ := restored.PendingAskSnapshot(); !restored.HasPendingAsk() || question == nil {
		t.Fatal("legacy ordinary question was lost")
	}
	if _, got := restored.PendingAskSnapshot(); got == nil || len(got.Calls) != 0 {
		t.Fatalf("legacy restore synthesized canonical identity: %+v", got)
	}
	for _, entry := range readPersistedTranscriptEntries(t, dir, restored.ID()) {
		if entry.Turn.ConversationProvenance != nil {
			t.Fatal("legacy origin synthesized")
		}
	}
	result, ok := findToolResultInHistory(restored.history, call.ID)
	if !ok || len(result.ToolState) != 0 {
		t.Fatalf("legacy content synthesized: %+v", result)
	}
}
