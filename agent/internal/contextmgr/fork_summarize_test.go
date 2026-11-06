package contextmgr

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/internal/sessionlog"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
)

// stubSummarizeAdapter (the minimal llm provider stub) lives in
// testhelpers_test.go, shared with the strategy tests.

// A reply that fails JSON parsing is this surface's rejected reply: the same
// route is tried again before the caller is told the fork summary failed.
func TestForkSummarizeRetriesRejectedReply(t *testing.T) {
	entry := sessionlog.SessionLogEntry{Action: "shell", Summary: "Ran the build.", Outcome: "success", FilesTouched: []string{"main.go"}}
	entryJSON, _ := json.Marshal(entry)
	var calls atomic.Int32
	adapter := &stubSummarizeAdapter{
		name: "openai",
		respFn: func(llm.Request) (llm.Response, error) {
			if calls.Add(1) == 1 {
				return llm.Response{Message: llm.Assistant("not json")}, nil
			}
			return llm.Response{Message: llm.Assistant(string(entryJSON))}, nil
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	turns := []schema.Turn{
		{Kind: schema.TurnToolResults, Message: llm.ToolResultNamed("c1", "shell", "PASS", false)},
	}

	got, err := forkSummarize(context.Background(), cheapmodel.New(client), NewOpenAIProfile("gpt-5.2"), turns, 3)
	if err != nil {
		t.Fatalf("forkSummarize: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want the rejected attempt plus one retry", calls.Load())
	}
	if got.Turn != 3 || got.Action != "shell" {
		t.Fatalf("entry = %+v, want the retried reply's entry", got)
	}
}

// A model that keeps producing unparseable replies spends the same re-roll
// budget as the conversation surface before the error reaches the strategy,
// and the budget-exhausted error still wraps the JSON parse cause.
func TestForkSummarizeExhaustsThreeAttempts(t *testing.T) {
	var calls atomic.Int32
	adapter := &stubSummarizeAdapter{
		name: "openai",
		respFn: func(llm.Request) (llm.Response, error) {
			calls.Add(1)
			return llm.Response{Message: llm.Assistant("still not json")}, nil
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	turns := []schema.Turn{
		{Kind: schema.TurnToolResults, Message: llm.ToolResultNamed("c1", "shell", "PASS", false)},
	}

	_, err := forkSummarize(context.Background(), cheapmodel.New(client), NewOpenAIProfile("gpt-5.2"), turns, 1)
	if err == nil {
		t.Fatal("forkSummarize stored an unparseable reply")
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want three attempts", calls.Load())
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("exhausted-budget error lost the parse cause: %v", err)
	}
}

// The fork summary's re-rolls settle as ONE logical attempt group, like the
// conversation surface's: three transport successes and one settlement.
func TestForkSummarizeReRollsSettleAsOneAttemptGroup(t *testing.T) {
	client, logger, path := contextManagerAttemptClient(t, &contextManagerAttemptAdapter{name: "openai", text: "not json"})
	turns := []schema.Turn{
		{Kind: schema.TurnToolResults, Message: llm.ToolResultNamed("c1", "shell", "PASS", false)},
	}

	_, err := forkSummarize(context.Background(), cheapmodel.New(client), NewOpenAIProfile("gpt-5.2"), turns, 1)
	if err == nil {
		t.Fatal("forkSummarize stored an unparseable reply")
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	assertContextManagerLogicalGroup(t, readContextManagerAttemptRecords(t, path), apilog.AttemptSuccess, 3)
}

// Cancellation between attempts stops the fork summary and reports the
// cancellation, not the parse failure of the last reply.
func TestForkSummarizeRetriesStopOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	adapter := &stubSummarizeAdapter{
		name: "openai",
		respFn: func(llm.Request) (llm.Response, error) {
			calls.Add(1)
			cancel()
			return llm.Response{Message: llm.Assistant("not json")}, nil
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	turns := []schema.Turn{
		{Kind: schema.TurnToolResults, Message: llm.ToolResultNamed("c1", "shell", "PASS", false)},
	}

	_, err := forkSummarize(ctx, cheapmodel.New(client), NewOpenAIProfile("gpt-5.2"), turns, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("forkSummarize err = %v, want the cancellation", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want no retry after cancellation", calls.Load())
	}
}

func TestForkSummarize_RoutesToCheapProvider(t *testing.T) {
	// A configured cross-provider cheap model must route the summarize side call
	// to the cheap provider, not the active provider.
	entry := sessionlog.SessionLogEntry{Action: "shell", Summary: "ok", Outcome: "success"}
	entryJSON, _ := json.Marshal(entry)

	mainAdapter := &stubSummarizeAdapter{
		name: "openai",
		respFn: func(req llm.Request) (llm.Response, error) {
			t.Errorf("summarize routed to active provider %q, want cheap provider anthropic", req.Provider)
			return llm.Response{Message: llm.Assistant(string(entryJSON))}, nil
		},
	}
	cheapAdapter := &stubSummarizeAdapter{
		name: "anthropic",
		respFn: func(req llm.Request) (llm.Response, error) {
			if req.Model != "claude-haiku-4-5-20251001" {
				t.Errorf("model = %q, want claude-haiku-4-5-20251001", req.Model)
			}
			return llm.Response{Message: llm.Assistant(string(entryJSON))}, nil
		},
	}
	client := llm.NewClient()
	client.Register(mainAdapter)
	client.Register(cheapAdapter)

	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "anthropic/claude-haiku-4-5-20251001")
	turns := []schema.Turn{
		{Kind: schema.TurnToolResults, Message: llm.ToolResultNamed("c1", "shell", "PASS", false)},
	}

	if _, err := forkSummarize(context.Background(), cheapmodel.New(client), profile, turns, 1); err != nil {
		t.Fatalf("forkSummarize: %v", err)
	}
	if cheapAdapter.lastReq.Provider != "anthropic" {
		t.Fatalf("cheap adapter saw provider %q, want anthropic", cheapAdapter.lastReq.Provider)
	}
}

func TestForkSummarize_Success(t *testing.T) {
	entry := sessionlog.SessionLogEntry{
		Action:       "shell",
		Summary:      "Ran go test ./... and all tests passed.",
		Outcome:      "success",
		FilesTouched: []string{"main.go"},
	}
	entryJSON, _ := json.Marshal(entry)

	adapter := &stubSummarizeAdapter{
		name: "openai",
		respFn: func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant(string(entryJSON))}, nil
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := NewOpenAIProfile("gpt-5.2")
	turns := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID:        "c1",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"go test ./..."}`),
				}},
			},
		}},
		{Kind: schema.TurnToolResults, Message: llm.ToolResultNamed("c1", "shell", "PASS", false)},
	}

	got, err := forkSummarize(context.Background(), cheapmodel.New(client), profile, turns, 7)
	if err != nil {
		t.Fatalf("forkSummarize: %v", err)
	}

	if got.Turn != 7 {
		t.Errorf("Turn = %d, want 7", got.Turn)
	}
	if got.Action != "shell" {
		t.Errorf("Action = %q, want %q", got.Action, "shell")
	}
	if got.Outcome != "success" {
		t.Errorf("Outcome = %q, want %q", got.Outcome, "success")
	}
	if got.Summary != entry.Summary {
		t.Errorf("Summary = %q, want %q", got.Summary, entry.Summary)
	}
	if len(got.FilesTouched) != 1 || got.FilesTouched[0] != "main.go" {
		t.Errorf("FilesTouched = %v, want [main.go]", got.FilesTouched)
	}
}

func TestForkSummarize_Failure(t *testing.T) {
	entry := sessionlog.SessionLogEntry{
		Action:       "shell",
		Summary:      "Ran tests but compilation failed.",
		Outcome:      "failure",
		FilesTouched: []string{"main.go"},
		Failures:     []string{"./main.go:42:5: undefined: foo"},
	}
	entryJSON, _ := json.Marshal(entry)

	adapter := &stubSummarizeAdapter{
		name: "openai",
		respFn: func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant(string(entryJSON))}, nil
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := NewOpenAIProfile("gpt-5.2")
	turns := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID:        "c1",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"go build ./..."}`),
				}},
			},
		}},
		{Kind: schema.TurnToolResults, Message: llm.ToolResultNamed("c1", "shell", "./main.go:42:5: undefined: foo", true)},
	}

	got, err := forkSummarize(context.Background(), cheapmodel.New(client), profile, turns, 12)
	if err != nil {
		t.Fatalf("forkSummarize: %v", err)
	}

	if got.Outcome != "failure" {
		t.Errorf("Outcome = %q, want %q", got.Outcome, "failure")
	}
	if len(got.Failures) != 1 {
		t.Fatalf("Failures len = %d, want 1", len(got.Failures))
	}
	if got.Failures[0] != "./main.go:42:5: undefined: foo" {
		t.Errorf("Failures[0] = %q, want exact error text", got.Failures[0])
	}
	if got.Turn != 12 {
		t.Errorf("Turn = %d, want 12", got.Turn)
	}
}

func TestForkSummarize_MalformedJSON(t *testing.T) {
	adapter := &stubSummarizeAdapter{
		name: "openai",
		respFn: func(req llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Assistant("this is not JSON at all")}, nil
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := NewOpenAIProfile("gpt-5.2")
	turns := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Assistant("I'll read the file")},
	}

	_, err := forkSummarize(context.Background(), cheapmodel.New(client), profile, turns, 1)
	if err == nil {
		t.Fatal("expected error for malformed JSON response, got nil")
	}
}

func TestForkSummarize_ExtractsAction(t *testing.T) {
	// The LLM returns JSON where action matches the tool call name.
	adapter := &stubSummarizeAdapter{
		name: "openai",
		respFn: func(req llm.Request) (llm.Response, error) {
			entry := sessionlog.SessionLogEntry{
				Action:  "edit_file",
				Summary: "Edited main.go to fix typo.",
				Outcome: "success",
			}
			b, _ := json.Marshal(entry)
			return llm.Response{Message: llm.Assistant(string(b))}, nil
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := NewOpenAIProfile("gpt-5.2")
	turns := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID:        "c1",
					Name:      "edit_file",
					Arguments: json.RawMessage(`{"file_path":"main.go","old_string":"tpyo","new_string":"typo"}`),
				}},
			},
		}},
		{Kind: schema.TurnToolResults, Message: llm.ToolResultNamed("c1", "edit_file", "OK", false)},
	}

	got, err := forkSummarize(context.Background(), cheapmodel.New(client), profile, turns, 3)
	if err != nil {
		t.Fatalf("forkSummarize: %v", err)
	}

	if got.Action != "edit_file" {
		t.Errorf("Action = %q, want %q", got.Action, "edit_file")
	}
}

func TestForkSummarize_UsesCheapModel(t *testing.T) {
	// Configure an explicit cheap model so the test is independent of provider
	// defaults. If forkSummarize routes to the main model ("gpt-5.2") instead
	// of the configured cheap model, the assertion will catch it.
	const cheapModel = "gpt-test-cheap"
	adapter := &stubSummarizeAdapter{
		name: "openai",
		respFn: func(req llm.Request) (llm.Response, error) {
			if req.Model != cheapModel {
				t.Errorf("expected cheap model %q, got %q", cheapModel, req.Model)
			}
			entry := sessionlog.SessionLogEntry{
				Action:  "shell",
				Summary: "test",
				Outcome: "success",
			}
			b, _ := json.Marshal(entry)
			return llm.Response{Message: llm.Assistant(string(b))}, nil
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), cheapModel)
	turns := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	_, err := forkSummarize(context.Background(), cheapmodel.New(client), profile, turns, 1)
	if err != nil {
		t.Fatalf("forkSummarize: %v", err)
	}
}

func TestForkSummarize_LLMError(t *testing.T) {
	adapter := &stubSummarizeAdapter{
		name: "openai",
		respFn: func(req llm.Request) (llm.Response, error) {
			return llm.Response{}, errors.New("rate limited")
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := NewOpenAIProfile("gpt-5.2")
	turns := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Assistant("hello")},
	}

	_, err := forkSummarize(context.Background(), cheapmodel.New(client), profile, turns, 1)
	if err == nil {
		t.Fatal("expected error when LLM returns error, got nil")
	}
}
