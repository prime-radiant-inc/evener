package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestNormalizeAskArgs_BatchFormPassthrough covers the batch form (Case 1,
// line 109-110) which is already covered; here for completeness.
func TestNormalizeAskArgs_BatchFormPassthrough(t *testing.T) {
	args := askUserArgsValid()
	out, err := normalizeAskArgs(args)
	if err != nil {
		t.Fatalf("normalizeAskArgs batch form: %v", err)
	}
	if _, ok := out["questions"]; !ok {
		t.Fatal("batch form missing questions")
	}
}

// TestNormalizeAskArgs_ShorthandWithAllOptional covers the shorthand wrapping
// path with all optional fields present (lines 116-131).
func TestNormalizeAskArgs_ShorthandWithAllOptional(t *testing.T) {
	args := map[string]any{
		"question":      "Which option?",
		"options":       []any{map[string]any{"label": "A", "detail": "x"}},
		"why":           "need a decision",
		"if_unanswered": "proceed anyway",
		"multi_select":  true,
		"header":        "Choice",
		"extra_field":   "preserved",
	}
	out, err := normalizeAskArgs(args)
	if err != nil {
		t.Fatalf("normalizeAskArgs shorthand: %v", err)
	}
	questions, ok := out["questions"].([]any)
	if !ok || len(questions) != 1 {
		t.Fatalf("expected 1 question, got %#v", out["questions"])
	}
	q := questions[0].(map[string]any)
	// Verify all optional fields were wrapped.
	for _, key := range []string{"question", "options", "why", "if_unanswered", "multi_select", "header"} {
		if _, ok := q[key]; !ok {
			t.Fatalf("wrapped question missing %q: %#v", key, q)
		}
	}
	// Verify extra fields are preserved.
	if out["extra_field"] != "preserved" {
		t.Fatalf("extra field not preserved: %#v", out["extra_field"])
	}
	// Verify shorthand keys are removed from the top level.
	for _, key := range []string{"question", "options", "why", "if_unanswered", "multi_select", "header"} {
		if _, ok := out[key]; ok {
			t.Fatalf("top-level still has %q", key)
		}
	}
}

// TestNormalizeAskArgs_ShorthandEmptyOptional covers the shorthand wrapping
// path where optional fields are empty/zero and should be skipped (lines
// 120, 123, 126, 129 — the `!= ""` and `== true` guards).
func TestNormalizeAskArgs_ShorthandEmptyOptional(t *testing.T) {
	args := map[string]any{
		"question":      "Which option?",
		"options":       []any{map[string]any{"label": "A", "detail": "x"}},
		"why":           "",    // empty, should be skipped
		"if_unanswered": "",    // empty, should be skipped
		"multi_select":  false, // false, should be skipped
		"header":        "",    // empty, should be skipped
	}
	out, err := normalizeAskArgs(args)
	if err != nil {
		t.Fatalf("normalizeAskArgs: %v", err)
	}
	questions := out["questions"].([]any)
	q := questions[0].(map[string]any)
	for _, key := range []string{"why", "if_unanswered", "multi_select", "header"} {
		if _, ok := q[key]; ok {
			t.Fatalf("empty optional field %q should not be wrapped: %#v", key, q)
		}
	}
}

// TestNormalizeAskArgs_ShorthandOnlyQuestionAndOptions covers the minimal
// shorthand form with only question + options, no optional fields (lines
// 116-119, 134-141).
func TestNormalizeAskArgs_ShorthandOnlyQuestionAndOptions(t *testing.T) {
	args := map[string]any{
		"question": "Which option?",
		"options":  []any{map[string]any{"label": "A"}},
	}
	out, err := normalizeAskArgs(args)
	if err != nil {
		t.Fatalf("normalizeAskArgs: %v", err)
	}
	questions := out["questions"].([]any)
	if len(questions) != 1 {
		t.Fatalf("expected 1 question, got %d", len(questions))
	}
	q := questions[0].(map[string]any)
	if q["question"] != "Which option?" {
		t.Fatalf("question = %#v", q["question"])
	}
}

// TestNormalizeAskArgs_BothFormsError covers the error when both questions
// and question are present (line 147-149).
func TestNormalizeAskArgs_BothFormsError(t *testing.T) {
	args := map[string]any{
		"questions": []any{map[string]any{}},
		"question":  "Which?",
		"options":   []any{map[string]any{}},
	}
	_, err := normalizeAskArgs(args)
	if err == nil {
		t.Fatal("expected error for both forms")
	}
	if !strings.Contains(err.Error(), "both") {
		t.Fatalf("error = %v, want both forms message", err)
	}
}

// TestNormalizeAskArgs_QuestionWithoutOptionsError covers the error when
// question is present but options is missing (line 153-155).
func TestNormalizeAskArgs_QuestionWithoutOptionsError(t *testing.T) {
	args := map[string]any{
		"question": "Which?",
	}
	_, err := normalizeAskArgs(args)
	if err == nil {
		t.Fatal("expected error for question without options")
	}
	if !strings.Contains(err.Error(), "options") {
		t.Fatalf("error = %v, want options required message", err)
	}
}

// TestNormalizeAskArgs_NeitherFormError covers the error when neither
// questions nor question+options is present (line 158-160).
func TestNormalizeAskArgs_NeitherFormError(t *testing.T) {
	args := map[string]any{"unrelated": "field"}
	_, err := normalizeAskArgs(args)
	if err == nil {
		t.Fatal("expected error for no form")
	}
	if !strings.Contains(err.Error(), "questions") {
		t.Fatalf("error = %v, want questions required message", err)
	}
}

// TestNormalizeAskArgs_EmptyArgs covers the empty-args case.
func TestNormalizeAskArgs_EmptyArgs(t *testing.T) {
	_, err := normalizeAskArgs(map[string]any{})
	if err == nil {
		t.Fatal("expected error for empty args")
	}
}

// TestNormalizeAskArgs_QuestionsWithOptionsOnly covers the case where
// questions is absent, question is absent, but options is present (falls
// through to the "neither form" error at line 158-160).
func TestNormalizeAskArgs_OptionsOnlyWithoutQuestion(t *testing.T) {
	args := map[string]any{
		"options": []any{map[string]any{"label": "A"}},
	}
	_, err := normalizeAskArgs(args)
	if err == nil {
		t.Fatal("expected error for options without question")
	}
}

// TestParseAskQuestions_EmptyQuestions covers parsing with no questions.
func TestParseAskQuestions_EmptyQuestions(t *testing.T) {
	args := map[string]any{"questions": []any{}}
	parsed, err := parseAskQuestions(args)
	if err != nil {
		t.Fatalf("parseAskQuestions empty: %v", err)
	}
	if len(parsed) != 0 {
		t.Fatalf("expected 0 questions, got %d", len(parsed))
	}
}

// TestParseAskQuestions_NilQuestions covers parsing with missing questions key.
func TestParseAskQuestions_NilQuestions(t *testing.T) {
	parsed, err := parseAskQuestions(map[string]any{})
	if err != nil {
		t.Fatalf("parseAskQuestions nil: %v", err)
	}
	if len(parsed) != 0 {
		t.Fatalf("expected 0 questions, got %d", len(parsed))
	}
}

// TestParseAskQuestions_QuestionWithoutHeader covers a question that has no
// header field (line 202 — the `_, ok` falls to empty string).
func TestParseAskQuestions_QuestionWithoutHeader(t *testing.T) {
	args := map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Which?",
				"options":  []any{map[string]any{"label": "A"}},
			},
		},
	}
	parsed, err := parseAskQuestions(args)
	if err != nil {
		t.Fatalf("parseAskQuestions: %v", err)
	}
	if len(parsed) != 1 || parsed[0].Header != "" {
		t.Fatalf("expected empty header, got %#v", parsed)
	}
}

// TestParseAskQuestions_OneRecommended covers the happy path with one
// recommended option (line 194-195).
func TestParseAskQuestions_OneRecommended(t *testing.T) {
	args := map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Which?",
				"options": []any{
					map[string]any{"label": "A", "recommended": true},
					map[string]any{"label": "B"},
				},
			},
		},
	}
	parsed, err := parseAskQuestions(args)
	if err != nil {
		t.Fatalf("parseAskQuestions: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("expected 1 question, got %d", len(parsed))
	}
}

// askToolCallContent builds llm.ContentPart entries for tool calls.
func askToolCallContent(id, name string, args map[string]any) llm.ContentPart {
	raw, _ := json.Marshal(args)
	return llm.ContentPart{
		Kind:     llm.ContentToolCall,
		ToolCall: &llm.ToolCallData{ID: id, Name: name, Arguments: raw, Type: "function"},
	}
}

func askToolResultContent(id, name string, isError bool) llm.ContentPart {
	return llm.ContentPart{
		Kind:       llm.ContentToolResult,
		ToolResult: &llm.ToolResultData{ToolCallID: id, Name: name, IsError: isError},
	}
}

// TestQuestionsFromAskCalls covers the backward scan and call filtering
// (lines 395-421).
func TestQuestionsFromAskCalls(t *testing.T) {
	// Build a history with an assistant turn carrying two ask_user calls
	// (one with good args, one with bad JSON), followed by a tool-results
	// turn.
	goodArgs := askUserArgsValid()
	assistantTurn := schema.Turn{
		Kind: schema.TurnAssistant,
		Message: llm.Message{
			Content: []llm.ContentPart{
				askToolCallContent("call_good", "ask_user", goodArgs),
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_bad", Name: "ask_user", Arguments: json.RawMessage(`not valid json`), Type: "function"}},
				askToolCallContent("call_other", "communicate", map[string]any{}),
			},
		},
	}
	toolResultsTurn := schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{
			Content: []llm.ContentPart{
				askToolResultContent("call_good", "ask_user", false),
				askToolResultContent("call_bad", "ask_user", false),
				askToolResultContent("call_other", "communicate", false),
			},
		},
	}
	history := []schema.Turn{assistantTurn, toolResultsTurn}

	wantCallIDs := map[string]bool{"call_good": true, "call_bad": true}
	out := questionsFromAskCalls(history, 1, wantCallIDs)
	// Only the good call should produce questions (1 question).
	if len(out) != 1 {
		t.Fatalf("expected 1 question from good call, got %d: %+v", len(out), out)
	}
}

// TestQuestionsFromAskCalls_NoAssistantTurn covers the case where no
// preceding assistant turn is found (line 420-421).
func TestQuestionsFromAskCalls_NoAssistantTurn(t *testing.T) {
	toolResultsTurn := schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{
			Content: []llm.ContentPart{
				askToolResultContent("call1", "ask_user", false),
			},
		},
	}
	// History has only the tool-results turn, no preceding assistant.
	history := []schema.Turn{toolResultsTurn}
	out := questionsFromAskCalls(history, 0, map[string]bool{"call1": true})
	if out != nil {
		t.Fatalf("expected nil, got %+v", out)
	}
}

// TestQuestionsFromAskCalls_NonMatchingName covers the skip for calls whose
// name is not ask_user (line 401-402).
func TestQuestionsFromAskCalls_NonMatchingName(t *testing.T) {
	assistantTurn := schema.Turn{
		Kind: schema.TurnAssistant,
		Message: llm.Message{
			Content: []llm.ContentPart{
				askToolCallContent("call1", "communicate", map[string]any{}),
			},
		},
	}
	toolResultsTurn := schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{
			Content: []llm.ContentPart{
				askToolResultContent("call1", "ask_user", false),
			},
		},
	}
	history := []schema.Turn{assistantTurn, toolResultsTurn}
	out := questionsFromAskCalls(history, 1, map[string]bool{"call1": true})
	if len(out) != 0 {
		t.Fatalf("expected 0 questions, got %d", len(out))
	}
}

// TestQuestionsFromAskCalls_NormalizeError covers the case where
// normalizeAskArgs fails on a call's arguments (line 409-411).
func TestQuestionsFromAskCalls_NormalizeError(t *testing.T) {
	// Args with an invalid shape (both questions and question present).
	badArgs := map[string]any{
		"questions": []any{map[string]any{}},
		"question":  "Which?",
	}
	assistantTurn := schema.Turn{
		Kind: schema.TurnAssistant,
		Message: llm.Message{
			Content: []llm.ContentPart{
				askToolCallContent("call1", "ask_user", badArgs),
			},
		},
	}
	toolResultsTurn := schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{
			Content: []llm.ContentPart{
				askToolResultContent("call1", "ask_user", false),
			},
		},
	}
	history := []schema.Turn{assistantTurn, toolResultsTurn}
	out := questionsFromAskCalls(history, 1, map[string]bool{"call1": true})
	if len(out) != 0 {
		t.Fatalf("expected 0 questions from normalize error, got %d", len(out))
	}
}

// TestQuestionsFromAskCalls_ParseError covers the case where parseAskQuestions
// fails on a call's arguments (line 413-415).
func TestQuestionsFromAskCalls_ParseError(t *testing.T) {
	// Valid shape but duplicate labels — parseAskQuestions will reject it.
	dupArgs := askUserArgsDuplicateLabels()
	assistantTurn := schema.Turn{
		Kind: schema.TurnAssistant,
		Message: llm.Message{
			Content: []llm.ContentPart{
				askToolCallContent("call1", "ask_user", dupArgs),
			},
		},
	}
	toolResultsTurn := schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{
			Content: []llm.ContentPart{
				askToolResultContent("call1", "ask_user", false),
			},
		},
	}
	history := []schema.Turn{assistantTurn, toolResultsTurn}
	out := questionsFromAskCalls(history, 1, map[string]bool{"call1": true})
	if len(out) != 0 {
		t.Fatalf("expected 0 questions from parse error, got %d", len(out))
	}
}

// TestQuestionsFromAskCalls_SkipsNonAssistantTurns covers the backward
// scan past non-TurnAssistant turns (line 396-397).
func TestQuestionsFromAskCalls_SkipsNonAssistantTurns(t *testing.T) {
	goodArgs := askUserArgsValid()
	assistantTurn := schema.Turn{
		Kind: schema.TurnAssistant,
		Message: llm.Message{
			Content: []llm.ContentPart{
				askToolCallContent("call1", "ask_user", goodArgs),
			},
		},
	}
	// Insert a non-assistant turn between assistant and tool-results.
	bookkeepingTurn := schema.Turn{Kind: schema.TurnSystem}
	toolResultsTurn := schema.Turn{
		Kind: schema.TurnToolResults,
		Message: llm.Message{
			Content: []llm.ContentPart{
				askToolResultContent("call1", "ask_user", false),
			},
		},
	}
	history := []schema.Turn{assistantTurn, bookkeepingTurn, toolResultsTurn}
	out := questionsFromAskCalls(history, 2, map[string]bool{"call1": true})
	if len(out) != 1 {
		t.Fatalf("expected 1 question, got %d", len(out))
	}
}

// TestRegisterAskTool_AbortError covers the deps.abort error path (line
// 219-220) by registering the ask tool with a deps that returns an error
// from abort.
func TestRegisterAskTool_AbortError(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	reg := tool.NewRegistry()
	deps := &toolDeps{
		abort: func(ctx context.Context) error {
			return errAbort("aborted")
		},
	}
	registerAskTool(reg, sess, deps)
	defs := reg.Definitions()
	if !hasToolDef(defs, "ask_user") {
		t.Fatal("ask_user not registered")
	}
	// Execute the ask_user tool to trigger the abort error.
	res := reg.ExecuteCall(context.Background(), execenv.NewLocalExecutionEnvironment(dir), askUserCall("c1", askUserArgsValid()))
	if !res.IsError {
		t.Fatalf("expected abort error, got result=%+v", res)
	}
}

// TestRegisterAskTool_NonInteractive covers the NonInteractive guard (line
// 222-223).
func TestRegisterAskTool_NonInteractive(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{NonInteractive: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	reg := tool.NewRegistry()
	deps := &toolDeps{
		abort: func(ctx context.Context) error { return nil },
	}
	registerAskTool(reg, sess, deps)
	res := reg.ExecuteCall(context.Background(), execenv.NewLocalExecutionEnvironment(dir), askUserCall("c1", askUserArgsValid()))
	if !res.IsError {
		t.Fatal("expected unavailable error for non-interactive session")
	}
}

// TestMinimalExampleQuestionsArray covers the example serialization.
func TestMinimalExampleQuestionsArray(t *testing.T) {
	got := minimalExampleQuestionsArray()
	if !strings.Contains(got, "questions") || !strings.Contains(got, "question") || !strings.Contains(got, "options") {
		t.Fatalf("minimal example missing expected fields: %s", got)
	}
}

// errAbort is a simple error type for the abort test.
type errAbort string

func (e errAbort) Error() string { return string(e) }
