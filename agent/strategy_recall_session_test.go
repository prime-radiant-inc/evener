package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

func TestRecallStrategy_Integration_ViaTool(t *testing.T) {
	// Test the full flow: Session creates the recall strategy, the recall tool
	// saves the session and runs the sub-agent.
	dir := t.TempDir()

	c := llm.NewClient()
	llmCallIndex := 0
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Main agent round 1: model calls the recall tool.
			func(req llm.Request) llm.Response {
				llmCallIndex++
				return llm.Response{
					Model: "gpt-5.2",
					Finish: llm.FinishReason{
						Reason: llm.FinishReasonToolCalls,
					},
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
								ID:        "call_recall",
								Name:      "recall",
								Arguments: []byte(`{"question": "What was discussed earlier?"}`),
							}},
						},
					},
				}
			},
			// Sub-agent round 1: returns a direct text answer (no tool calls).
			func(req llm.Request) llm.Response {
				llmCallIndex++
				// Verify this is the sub-agent by checking the system prompt.
				sysMsg := req.Messages[0].Text()
				if !strings.Contains(sysMsg, "transcript search") {
					t.Errorf("expected sub-agent system prompt to mention transcript search, got: %s", sysMsg[:min(100, len(sysMsg))])
				}
				return wrapCommunicateResponse(llm.Response{
					Model: "gpt-5.2",
					Finish: llm.FinishReason{
						Reason: llm.FinishReasonStop,
					},
					Message: llm.Assistant("Earlier, the user said hello."),
				})
			},
			// Main agent round 2: after getting recall result, return final answer.
			func(req llm.Request) llm.Response {
				llmCallIndex++
				return wrapCommunicateResponse(llm.Response{
					Model:   "gpt-5.2",
					Finish:  llm.FinishReason{Reason: llm.FinishReasonStop},
					Message: llm.Assistant("Based on my recall, the user said hello earlier."),
				})
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		ContextStrategy: "recall",
		StateDir:        dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Verify the recall tool is registered.
	recallDef := sess.reg.Get("recall")
	if recallDef == nil {
		t.Fatal("expected recall tool to be registered")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := sess.ProcessInput(ctx, "What was said earlier?", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if out == "" {
		t.Fatal("expected non-empty output")
	}

	// Should have used 3 LLM calls: main->recall tool, sub-agent, main->final
	if llmCallIndex != 3 {
		t.Errorf("expected 3 LLM calls, got %d", llmCallIndex)
	}
}
