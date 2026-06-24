package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/llm"
)

func TestEmptyResponse_RetriesWithSteering(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	result := agenttest.CommunicateCall("c1", "final answer")

	f := &agenttest.FakeAdapter{
		Provider: "openai",
		Steps: []func(req llm.Request) llm.Response{
			// Round 0: model returns empty response.
			func(req llm.Request) llm.Response { return agenttest.EmptyResponse() },
			// Round 1: after steering nudge, model resumes with communicate.
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
				return agenttest.ToolCallResponse(result)
			},
		},
	}
	c.Register(f)

	sess, err := agent.NewSession(c, agent.NewOpenAIProfile("gpt-5.3-codex"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "do the task", nil)
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
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	f := &agenttest.FakeAdapter{
		Provider: "openai",
		Steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return agenttest.EmptyResponse() }, // retry 1
			func(req llm.Request) llm.Response { return agenttest.EmptyResponse() }, // retry 2
			func(req llm.Request) llm.Response { return agenttest.EmptyResponse() }, // retry 3
			func(req llm.Request) llm.Response { return agenttest.EmptyResponse() }, // exhausted — should not reach
			func(req llm.Request) llm.Response {
				t.Fatalf("should not reach 5th request")
				return llm.Response{}
			},
		},
	}
	c.Register(f)

	sess, err := agent.NewSession(c, agent.NewOpenAIProfile("gpt-5.3-codex"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "do the task", nil)
	if err == nil {
		t.Fatalf("expected empty-response contract error, got output %q", out)
	}
	if out != "" {
		t.Fatalf("expected empty output on error, got %q", out)
	}
	if !strings.Contains(err.Error(), "empty response") {
		t.Fatalf("unexpected error: %v", err)
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
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	shellCall := func(id string) llm.ToolCallData {
		raw, _ := json.Marshal(map[string]any{
			"command":     "echo ok",
			"description": "filler",
		})
		return llm.ToolCallData{ID: id, Name: "exec_command", Arguments: raw, Type: "function"}
	}
	result := agenttest.CommunicateCall("c1", "done")

	f := &agenttest.FakeAdapter{
		Provider: "openai",
		Steps: []func(req llm.Request) llm.Response{
			// Round 0: empty (triggers retry 1).
			func(req llm.Request) llm.Response { return agenttest.EmptyResponse() },
			// Round 0 retry: model recovers with tool call.
			func(req llm.Request) llm.Response { return agenttest.ToolCallResponse(shellCall("s1")) },
			// Round 1: empty again (should be fresh retry counter).
			func(req llm.Request) llm.Response { return agenttest.EmptyResponse() },
			// Round 1 retry: model recovers and submits result.
			func(req llm.Request) llm.Response { return agenttest.ToolCallResponse(result) },
		},
	}
	c.Register(f)

	sess, err := agent.NewSession(c, agent.NewOpenAIProfile("gpt-5.3-codex"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "do the task", nil)
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

func TestEmptyResponse_DoesNotConsumeToolRounds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	shellCall := func(id string) llm.ToolCallData {
		raw, _ := json.Marshal(map[string]any{
			"command":     "echo ok",
			"description": "filler",
		})
		return llm.ToolCallData{ID: id, Name: "exec_command", Arguments: raw, Type: "function"}
	}
	result := agenttest.CommunicateCall("c1", "done")

	// With MaxToolRoundsPerInput=3, agent gets 3 real tool rounds.
	// Two empty responses should NOT consume rounds, leaving all 3 for real work.
	f := &agenttest.FakeAdapter{
		Provider: "openai",
		Steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return agenttest.ToolCallResponse(shellCall("s1")) }, // round 0
			func(req llm.Request) llm.Response { return agenttest.EmptyResponse() },                   // empty — not a round
			func(req llm.Request) llm.Response { return agenttest.ToolCallResponse(shellCall("s2")) }, // round 1
			func(req llm.Request) llm.Response { return agenttest.EmptyResponse() },                   // empty — not a round
			func(req llm.Request) llm.Response { return agenttest.ToolCallResponse(result) },          // round 2 (submit)
		},
	}
	c.Register(f)

	sess, err := agent.NewSession(c, agent.NewOpenAIProfile("gpt-5.3-codex"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{
		MaxToolRoundsPerInput: 3,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "do the task", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "done" {
		t.Fatalf("ProcessInput returned %q, want %q", out, "done")
	}
	sess.Close()

	// All 5 requests should have been made (3 real + 2 empty).
	if got := len(f.Requests()); got != 5 {
		t.Fatalf("requests: got %d want 5", got)
	}
}

func TestBareText_DoesNotConsumeToolRounds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	shellCall := func(id string) llm.ToolCallData {
		raw, _ := json.Marshal(map[string]any{
			"command":     "echo ok",
			"description": "filler",
		})
		return llm.ToolCallData{ID: id, Name: "exec_command", Arguments: raw, Type: "function"}
	}
	result := agenttest.CommunicateCall("c1", "done")

	// With MaxToolRoundsPerInput=3, bare text retries should not consume rounds.
	f := &agenttest.FakeAdapter{
		Provider: "openai",
		Steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return agenttest.ToolCallResponse(shellCall("s1")) },  // round 0
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("oops")} }, // bare text — not a round
			func(req llm.Request) llm.Response { return agenttest.ToolCallResponse(shellCall("s2")) },  // round 1
			func(req llm.Request) llm.Response { return agenttest.ToolCallResponse(result) },           // round 2 (submit)
		},
	}
	c.Register(f)

	sess, err := agent.NewSession(c, agent.NewOpenAIProfile("gpt-5.3-codex"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{
		MaxToolRoundsPerInput: 3,
		NonInteractive:        true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "do the task", nil)
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
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	result := agenttest.CommunicateCall("c1", "final answer")

	f := &agenttest.FakeAdapter{
		Provider: "openai",
		Steps: []func(req llm.Request) llm.Response{
			// Round 0: model returns bare text (no tools) — should be redirected.
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("here is my answer")}
			},
			// Round 1: after steering, model uses communicate.
			func(req llm.Request) llm.Response {
				if req.ToolChoice == nil || req.ToolChoice.Mode != "required" {
					t.Errorf("expected bare-text retry to require a tool call, got %#v", req.ToolChoice)
				}
				// Verify a steering message was injected.
				lastMsg := req.Messages[len(req.Messages)-1]
				if lastMsg.Role != llm.RoleUser {
					t.Errorf("expected user-role steering after bare text, got %q", lastMsg.Role)
				}
				foundSteering := false
				for _, p := range lastMsg.Content {
					if p.Kind == llm.ContentText && strings.Contains(p.Text, "call communicate now") {
						foundSteering = true
					}
				}
				if !foundSteering {
					t.Errorf("expected steering to allow immediate communicate, got: %+v", lastMsg.Content)
				}
				return agenttest.ToolCallResponse(result)
			},
		},
	}
	c.Register(f)

	sess, err := agent.NewSession(c, agent.NewOpenAIProfile("gpt-5.3-codex"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{NonInteractive: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "do the task", nil)
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
	if req := f.Requests()[0]; req.ToolChoice == nil || req.ToolChoice.Mode != "required" {
		t.Fatalf("initial request should be required, got %#v", req.ToolChoice)
	}
}

func TestBareText_ExhaustsRetries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	f := &agenttest.FakeAdapter{
		Provider: "openai",
		Steps: []func(req llm.Request) llm.Response{
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

	sess, err := agent.NewSession(c, agent.NewOpenAIProfile("gpt-5.3-codex"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{NonInteractive: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "do the task", nil)
	if err == nil {
		t.Fatalf("expected bare-text contract error, got output %q", out)
	}
	if out != "" {
		t.Fatalf("expected empty output on error, got %q", out)
	}
	if !strings.Contains(err.Error(), "bare text without calling communicate") {
		t.Fatalf("unexpected error: %v", err)
	}
	sess.Close()

	// 1 initial + 3 retries = 4 total.
	if got := len(f.Requests()); got != 4 {
		t.Fatalf("requests: got %d want 4", got)
	}
}

// TestEmptyResponse_PhasePreservedInHistory verifies that when a response has
// empty text but a phase annotation (e.g., "final_answer"), the turn is still
// appended to history. This ensures the model sees its previous phase in context
// so it can course-correct instead of repeating the empty final_answer.
func TestEmptyResponse_PhasePreservedInHistory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	result := agenttest.CommunicateCall("c1", "final answer")

	// emptyResponseWithPhase simulates gpt-5.3-codex emitting an empty text
	// response with phase="final_answer".
	emptyWithPhase := llm.Response{
		Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "", Phase: "final_answer"},
			},
		},
	}

	f := &agenttest.FakeAdapter{
		Provider: "openai",
		Steps: []func(req llm.Request) llm.Response{
			// Round 0: model returns empty text with final_answer phase.
			func(req llm.Request) llm.Response { return emptyWithPhase },
			// Round 1: after steering, model should have the empty final_answer
			// in its history. Verify by checking message count increased.
			func(req llm.Request) llm.Response {
				// The empty final_answer turn should be in the messages.
				foundPhase := false
				for _, m := range req.Messages {
					if m.Role == llm.RoleAssistant {
						for _, p := range m.Content {
							if p.Phase == "final_answer" {
								foundPhase = true
							}
						}
					}
				}
				if !foundPhase {
					t.Errorf("expected empty final_answer phase in history, not found in request messages")
				}
				return agenttest.ToolCallResponse(result)
			},
		},
	}
	c.Register(f)

	sess, err := agent.NewSession(c, agent.NewOpenAIProfile("gpt-5.3-codex"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "do the task", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != "final answer" {
		t.Fatalf("ProcessInput returned %q, want %q", out, "final answer")
	}
	sess.Close()
}
