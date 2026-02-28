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

	result := submitResultCall("c1", "final answer")

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Round 0: model returns empty response.
			func(req llm.Request) llm.Response { return emptyResponse() },
			// Round 1: after steering nudge, model resumes with submit_result.
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
	result := submitResultCall("c1", "done")

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Empty (triggers retry 1, does not consume a round).
			func(req llm.Request) llm.Response { return emptyResponse() },
			// Model recovers with tool call (round 0).
			func(req llm.Request) llm.Response { return toolCallResponse(shellCall("s1")) },
			// Empty again (fresh retry counter, does not consume a round).
			func(req llm.Request) llm.Response { return emptyResponse() },
			// Model recovers and submits result (round 1).
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

func TestBareText_RedirectsToSubmitResult(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	result := submitResultCall("c1", "final answer")

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Round 0: model returns bare text (no tools) — should be redirected.
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("here is my answer")}
			},
			// Round 1: after steering, model uses submit_result.
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
					t.Errorf("expected steering mentioning submit_result, got: %+v", lastMsg.Content)
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

// TestEmptyResponse_DoesNotConsumeRounds verifies that empty-response retries
// do not count against MaxToolRoundsPerInput. With a budget of 2 rounds,
// 3 empty responses interspersed with 2 real rounds should succeed. If empties
// consumed rounds, the loop would exit prematurely at the turn limit.
func TestEmptyResponse_DoesNotConsumeRounds(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	shellCall := func(id string) llm.ToolCallData {
		raw, _ := json.Marshal(map[string]any{
			"command":     "echo ok",
			"description": "filler",
		})
		return llm.ToolCallData{ID: id, Name: "exec_command", Arguments: raw, Type: "function"}
	}
	result := submitResultCall("c1", "done")

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Empty (should not consume a round).
			func(req llm.Request) llm.Response { return emptyResponse() },
			// Recovery: real tool call (round 0).
			func(req llm.Request) llm.Response { return toolCallResponse(shellCall("s1")) },
			// Empty again (should not consume a round).
			func(req llm.Request) llm.Response { return emptyResponse() },
			// Empty again (should not consume a round).
			func(req llm.Request) llm.Response { return emptyResponse() },
			// Recovery: submit result (round 1 — within budget).
			func(req llm.Request) llm.Response { return toolCallResponse(result) },
		},
	}
	c.Register(f)

	// MaxToolRoundsPerInput=2: only rounds 0 and 1.
	// Without the fix: empty→round0 + shell→round1 + empty→round2(exit!) — never reaches submit.
	// With the fix: empty(no count) + shell→round0 + empty(no count) + empty(no count) + submit→round1→exits via resultDelivered.
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.3-codex"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 2,
	})
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

	if got := len(f.Requests()); got != 5 {
		t.Fatalf("requests: got %d want 5", got)
	}
}

// TestBareText_DoesNotConsumeRounds verifies that bare-text retries do not
// count against MaxToolRoundsPerInput. With a budget of 1 round, 2 bare text
// responses (both recovered) + 1 real tool round should succeed.
func TestBareText_DoesNotConsumeRounds(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	result := submitResultCall("c1", "done")

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Bare text (should not consume a round).
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("some bare text")}
			},
			// Another bare text (should not consume a round).
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("more bare text")}
			},
			// Recovery: submit result (round 0).
			func(req llm.Request) llm.Response { return toolCallResponse(result) },
		},
	}
	c.Register(f)

	// MaxToolRoundsPerInput=1: only 1 real round allowed.
	// Without the fix: bare→round0 + bare→round1(exit!) — never reaches submit.
	// With the fix: bare(no count) + bare(no count) + submit→round0→exits.
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.3-codex"), NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxToolRoundsPerInput: 1,
		NonInteractive:        true,
	})
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

	if got := len(f.Requests()); got != 3 {
		t.Fatalf("requests: got %d want 3", got)
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
