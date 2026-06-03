package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/serf/agent/internal/sessionlog"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// stubSummarizeAdapter is a minimal ProviderAdapter stub for fork summarize tests.
type stubSummarizeAdapter struct {
	name    string
	respFn  func(req llm.Request) (llm.Response, error)
	lastReq llm.Request
}

func (a *stubSummarizeAdapter) Name() string { return a.name }
func (a *stubSummarizeAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	a.lastReq = req
	return a.respFn(req)
}
func (a *stubSummarizeAdapter) Stream(_ context.Context, _ llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
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

	got, err := forkSummarize(context.Background(), client, profile, turns, 7)
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

	got, err := forkSummarize(context.Background(), client, profile, turns, 12)
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

	_, err := forkSummarize(context.Background(), client, profile, turns, 1)
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

	got, err := forkSummarize(context.Background(), client, profile, turns, 3)
	if err != nil {
		t.Fatalf("forkSummarize: %v", err)
	}

	if got.Action != "edit_file" {
		t.Errorf("Action = %q, want %q", got.Action, "edit_file")
	}
}

func TestForkSummarize_UsesCheapModel(t *testing.T) {
	adapter := &stubSummarizeAdapter{
		name: "openai",
		respFn: func(req llm.Request) (llm.Response, error) {
			if req.Model != "gpt-4.1-nano" {
				t.Errorf("expected cheap model gpt-4.1-nano, got %q", req.Model)
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

	profile := NewOpenAIProfile("gpt-5.2")
	turns := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	_, err := forkSummarize(context.Background(), client, profile, turns, 1)
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

	_, err := forkSummarize(context.Background(), client, profile, turns, 1)
	if err == nil {
		t.Fatal("expected error when LLM returns error, got nil")
	}
}
