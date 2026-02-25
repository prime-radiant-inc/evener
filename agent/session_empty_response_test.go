package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// emptyResponse returns a response with no text and no tool calls,
// simulating gpt-5.3-codex's null-content behavior.
func emptyResponse() llm.Response {
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant}}
}

func TestEmptyResponse_RetriesWithSteering(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	result := communicateCall("c1", "result", "final answer")

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Round 0: model returns empty response.
			func(req llm.Request) llm.Response { return emptyResponse() },
			// Round 1: after steering nudge, model resumes with communicate(result).
			func(req llm.Request) llm.Response {
				// Verify a steering message was injected into the conversation.
				lastMsg := req.Messages[len(req.Messages)-1]
				if lastMsg.Role != llm.RoleUser {
					t.Errorf("expected user-role steering message after empty response, got role %q", lastMsg.Role)
				}
				foundSteering := false
				for _, p := range lastMsg.Content {
					if p.Kind == llm.ContentText && strings.Contains(p.Text, "continue") {
						foundSteering = true
					}
				}
				if !foundSteering {
					t.Errorf("expected steering message containing 'continue', got: %+v", lastMsg.Content)
				}
				return toolCallResponse(result)
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.3-codex"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "do the task")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "final answer" {
		t.Fatalf("ProcessInput returned %q, want %q", out, "final answer")
	}
	sess.Close()

	// Two LLM requests: empty response + retry with steering.
	if got := len(f.Requests()); got != 2 {
		t.Fatalf("requests: got %d want 2", got)
	}
}

func TestEmptyResponse_ExhaustsRetries(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return emptyResponse() }, // retry 1
			func(req llm.Request) llm.Response { return emptyResponse() }, // retry 2
			func(req llm.Request) llm.Response { return emptyResponse() }, // retry 3
			func(req llm.Request) llm.Response { return emptyResponse() }, // exhausted — should not reach
			func(req llm.Request) llm.Response {
				t.Fatalf("should not reach 5th request")
				return llm.Response{}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.3-codex"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "do the task")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	// After exhausting retries, should return empty string.
	if strings.TrimSpace(out) != "" {
		t.Fatalf("expected empty output after exhausted retries, got %q", out)
	}
	sess.Close()

	// 1 initial + 3 retries = 4 total requests (the 4th empty exits).
	// Actually: first empty triggers retry 1, second empty triggers retry 2,
	// third empty triggers retry 3, fourth empty exits = 4 requests.
	if got := len(f.Requests()); got != 4 {
		t.Fatalf("requests: got %d want 4", got)
	}
}

func TestEmptyResponse_ResetsOnProgress(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	shellCall := func(id string) llm.ToolCallData {
		raw, _ := json.Marshal(map[string]any{
			"command":     "echo ok",
			"description": "filler",
		})
		return llm.ToolCallData{ID: id, Name: "exec_command", Arguments: raw, Type: "function"}
	}
	result := communicateCall("c1", "result", "done")

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Round 0: empty (triggers retry 1).
			func(req llm.Request) llm.Response { return emptyResponse() },
			// Round 0 retry: model recovers with tool call.
			func(req llm.Request) llm.Response { return toolCallResponse(shellCall("s1")) },
			// Round 1: empty again (should be fresh retry counter).
			func(req llm.Request) llm.Response { return emptyResponse() },
			// Round 1 retry: model recovers and submits result.
			func(req llm.Request) llm.Response { return toolCallResponse(result) },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.3-codex"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "do the task")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "done" {
		t.Fatalf("ProcessInput returned %q, want %q", out, "done")
	}
	sess.Close()

	if got := len(f.Requests()); got != 4 {
		t.Fatalf("requests: got %d want 4", got)
	}
}

func TestBareText_RedirectsToCommunicate(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	result := communicateCall("c1", "result", "final answer")

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Round 0: model returns bare text (no tools) — should be redirected.
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("here is my answer")}
			},
			// Round 1: after steering, model uses communicate(result).
			func(req llm.Request) llm.Response {
				// Verify a steering message was injected.
				lastMsg := req.Messages[len(req.Messages)-1]
				if lastMsg.Role != llm.RoleUser {
					t.Errorf("expected user-role steering after bare text, got %q", lastMsg.Role)
				}
				foundSteering := false
				for _, p := range lastMsg.Content {
					if p.Kind == llm.ContentText && strings.Contains(p.Text, "communicate") {
						foundSteering = true
					}
				}
				if !foundSteering {
					t.Errorf("expected steering mentioning communicate, got: %+v", lastMsg.Content)
				}
				return toolCallResponse(result)
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.3-codex"), NewLocalExecutionEnvironment(dir), SessionConfig{NonInteractive: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "do the task")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "final answer" {
		t.Fatalf("out: %q, want %q", out, "final answer")
	}
	sess.Close()

	if got := len(f.Requests()); got != 2 {
		t.Fatalf("requests: got %d want 2", got)
	}
}

func TestBareText_ExhaustsRetries(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("text 1")} },
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("text 2")} },
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("text 3")} },
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("text 4")} },
			func(req llm.Request) llm.Response {
				t.Fatalf("should not reach 5th request")
				return llm.Response{}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.3-codex"), NewLocalExecutionEnvironment(dir), SessionConfig{NonInteractive: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "do the task")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	// After exhausting retries, should return the last bare text.
	if strings.TrimSpace(out) != "text 4" {
		t.Fatalf("expected last bare text, got %q", out)
	}
	sess.Close()

	// 1 initial + 3 retries = 4 total.
	if got := len(f.Requests()); got != 4 {
		t.Fatalf("requests: got %d want 4", got)
	}
}
