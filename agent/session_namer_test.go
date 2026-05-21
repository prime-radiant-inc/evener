package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

func TestNameSession_UsesCheapModelAndStructuredOutput(t *testing.T) {
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-4.1-nano")
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				if req.Model != "gpt-4.1-nano" {
					t.Fatalf("model = %q, want cheap model", req.Model)
				}
				if req.Provider != "openai" {
					t.Fatalf("provider = %q, want openai", req.Provider)
				}
				if req.ResponseFormat == nil || req.ResponseFormat.Type != "json_schema" {
					t.Fatalf("ResponseFormat = %#v, want json_schema", req.ResponseFormat)
				}
				if req.MaxTokens == nil || *req.MaxTokens > 100 {
					t.Fatalf("MaxTokens = %#v, want small cap", req.MaxTokens)
				}
				if len(req.Tools) != 0 {
					t.Fatalf("Tools len = %d, want 0", len(req.Tools))
				}
				joined := messagesText(req.Messages)
				if !strings.Contains(joined, "initial user prompt") || !strings.Contains(joined, "fix the flaky test") {
					t.Fatalf("prompt did not include expected source text: %q", joined)
				}
				return llm.Response{
					Message: llm.Assistant(`{"name":"Fix Flaky Test"}`),
					Usage:   llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
				}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	got, err := NameSession(context.Background(), client, profile, sessionNameSourcePrompt, "fix the flaky test in agent/session_test.go")
	if err != nil {
		t.Fatalf("NameSession: %v", err)
	}
	if got.Name != "Fix Flaky Test" {
		t.Fatalf("Name = %q, want Fix Flaky Test", got.Name)
	}
	if got.Source != sessionNameSourcePrompt {
		t.Fatalf("Source = %q, want prompt", got.Source)
	}
	if got.Usage.TotalTokens != 15 {
		t.Fatalf("Usage.TotalTokens = %d, want 15", got.Usage.TotalTokens)
	}
}

func TestNameSession_FallsBackToActiveModel(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				if req.Model != "gpt-5.2" {
					t.Fatalf("model = %q, want active model", req.Model)
				}
				return llm.Response{Message: llm.Assistant(`{"name":"Review System Prompt"}`)}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	got, err := NameSession(context.Background(), client, NewOpenAIProfile("gpt-5.2"), sessionNameSourcePrompt, "review the system prompt")
	if err != nil {
		t.Fatalf("NameSession: %v", err)
	}
	if got.Name != "Review System Prompt" {
		t.Fatalf("Name = %q, want Review System Prompt", got.Name)
	}
}

func TestSessionNamerEnabledRequiresConfiguredCheapModel(t *testing.T) {
	if sessionNamerEnabled(NewOpenAIProfile("gpt-5.2")) {
		t.Fatal("session namer should not auto-enable from active model")
	}
	if !sessionNamerEnabled(WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-4.1-nano")) {
		t.Fatal("session namer should enable when cheap model is configured")
	}
}

func TestNameSession_SanitizesGeneratedName(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant(`{"name":"  \"Fix Parser Bug!!!\"  "}`)}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	got, err := NameSession(context.Background(), client, NewOpenAIProfile("gpt-5.2"), sessionNameSourceCompaction, "[CONTEXT SUMMARY] parser failures")
	if err != nil {
		t.Fatalf("NameSession: %v", err)
	}
	if got.Name != "Fix Parser Bug" {
		t.Fatalf("Name = %q, want Fix Parser Bug", got.Name)
	}
	if got.Source != sessionNameSourceCompaction {
		t.Fatalf("Source = %q, want compaction", got.Source)
	}
}

func TestNameSession_RejectsEmptySourceText(t *testing.T) {
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	_, err := NameSession(context.Background(), client, NewOpenAIProfile("gpt-5.2"), sessionNameSourcePrompt, "   ")
	if err == nil || !strings.Contains(err.Error(), "source text is empty") {
		t.Fatalf("err = %v, want source text error", err)
	}
}

func messagesText(messages []llm.Message) string {
	var b strings.Builder
	for _, m := range messages {
		b.WriteString(m.Text())
		b.WriteByte('\n')
	}
	return b.String()
}
