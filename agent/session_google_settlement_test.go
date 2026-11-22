package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func registerProbeTool(t *testing.T, sess *Session, counter *atomic.Int32) {
	t.Helper()
	err := sess.reg.Register(tool.RegisteredTool{
		Definition: llm.ToolDefinition{
			Name:        "probe_tool",
			Description: "test probe tool",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			counter.Add(1)
			return "executed", nil
		},
	})
	if err != nil {
		t.Fatalf("registerProbeTool: %v", err)
	}
}

func TestSessionGoogleSettlement_NonAuthorizingStatusesSuppressToolsAndSettleOnce(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		finish llm.FinishReason
	}{
		{name: "incomplete", finish: llm.FinishReason{Reason: llm.FinishReasonLength, Raw: "incomplete"}},
		{name: "budget_exceeded", finish: llm.FinishReason{Reason: llm.FinishReasonLength, Raw: "budget_exceeded"}},
		{name: "requires_continuation", finish: llm.FinishReason{Reason: llm.FinishReasonLength, Raw: "requires_continuation"}},
		{name: "cancelled", finish: llm.FinishReason{Reason: llm.FinishReasonOther, Raw: "cancelled"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stateDir := t.TempDir()
			interactionID := "int_" + tc.name + "_123"
			var modelCalls atomic.Int32
			var toolCalls atomic.Int32

			fa := newFakeAdapter("google", func(req llm.Request) llm.Response {
				modelCalls.Add(1)
				return llm.Response{
					ID:    interactionID,
					Model: "gemini-3-pro",
					Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{
								GoogleInteraction: true,
								GoogleInstance:    "google",
								GoogleModel:       "gemini-3-pro",
								Signature:         "sig_partial_" + tc.name,
								Summary:           []string{"partial reasoning"},
							}},
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
								ID:        "call_probe_1",
								Name:      "probe_tool",
								Arguments: json.RawMessage(`{}`),
								Type:      "function",
							}},
						},
					},
					Finish: tc.finish,
				}
			})

			c := llm.NewClient()
			c.Register(fa)
			sess, err := NewSession(c, withTestSessionNamer(c, newGeminiProfile("gemini-3-pro")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
				StateDir: stateDir,
				testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
			})
			if err != nil {
				t.Fatalf("NewSession: %v", err)
			}
			defer sess.Close()
			registerProbeTool(t, sess, &toolCalls)

			var collected []events.SessionEvent
			doneDrain := make(chan struct{})
			go func() {
				defer close(doneDrain)
				for ev := range sess.Events() {
					collected = append(collected, ev)
				}
			}()

			_, runErr := sess.ProcessInput(context.Background(), "run tool", nil)
			if runErr == nil {
				t.Fatalf("expected non-nil error for non-authorizing status %q", tc.name)
			}
			if got := modelCalls.Load(); got != 1 {
				t.Fatalf("model calls = %d, want 1", got)
			}
			if got := toolCalls.Load(); got != 0 {
				t.Fatalf("tool handler calls = %d, want 0", got)
			}
			if got := sess.State(); got != SessionIdle {
				t.Fatalf("State = %q, want %q", got, SessionIdle)
			}
			if got := sess.WireState(); got != appwire.ThreadStatusSystemError {
				t.Fatalf("WireState = %q, want %q", got, appwire.ThreadStatusSystemError)
			}

			sess.Close()
			<-doneDrain

			// Verify EventError surfaced interaction ID and raw status.
			var sawError bool
			for _, ev := range collected {
				if ev.Kind == events.EventError {
					if data, ok := ev.Data.(events.ErrorData); ok {
						sawError = true
						if !strings.Contains(data.Error, interactionID) || !strings.Contains(data.Error, tc.finish.Raw) {
							t.Fatalf("EventError.Error = %q, want both %q and %q", data.Error, interactionID, tc.finish.Raw)
						}
					}
				}
			}
			if !sawError {
				t.Fatal("missing EventError on failure settlement")
			}

			// Verify .transcript.jsonl persisted TurnAssistant with surviving ContentThinking
			// (skipHistory=false even with empty text) and zero ContentToolCall parts, plus TurnFailure.
			entries := readPersistedTranscriptEntries(t, stateDir, sess.ID())
			var sawAssistant, sawThinking, sawFailure bool
			for _, entry := range entries {
				switch entry.Turn.Kind {
				case schema.TurnAssistant:
					sawAssistant = true
					for _, p := range entry.Turn.Message.Content {
						if p.Kind == llm.ContentToolCall {
							t.Fatalf("persisted TurnAssistant still contained ContentToolCall: %+v", p.ToolCall)
						}
						if p.Kind == llm.ContentThinking && p.Thinking != nil && p.Thinking.Signature == "sig_partial_"+tc.name {
							sawThinking = true
						}
					}
				case schema.TurnFailure:
					sawFailure = true
					if entry.Turn.Error == nil || !strings.Contains(entry.Turn.Error.Message, interactionID) || !strings.Contains(entry.Turn.Error.Message, tc.finish.Raw) {
						t.Fatalf("TurnFailure = %+v, want interaction ID %q and status %q", entry.Turn.Error, interactionID, tc.finish.Raw)
					}
				}
			}
			if !sawAssistant || !sawThinking {
				t.Fatalf("surviving thinking not persisted in TurnAssistant: sawAssistant=%v sawThinking=%v", sawAssistant, sawThinking)
			}
			if !sawFailure {
				t.Fatal("missing TurnFailure in transcript")
			}
		})
	}
}

func TestSessionGoogleSettlement_ContentFilterSuppressesToolsAndFallsThroughToNoToolCalls(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	var modelCalls atomic.Int32
	var toolCalls atomic.Int32

	fa := newFakeAdapter("google",
		func(req llm.Request) llm.Response {
			modelCalls.Add(1)
			return llm.Response{
				ID:    "int_cf_1",
				Model: "gemini-3-pro",
				Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
							ID:        "call_cf_1",
							Name:      "probe_tool",
							Arguments: json.RawMessage(`{}`),
							Type:      "function",
						}},
					},
				},
				Finish: llm.FinishReason{Reason: llm.FinishReasonContentFilter, Raw: "safety"},
			}
		},
		func(req llm.Request) llm.Response {
			modelCalls.Add(1)
			return agenttest.CommunicateResponse(true, "recovered after content filter")
		},
	)

	c := llm.NewClient()
	c.Register(fa)
	sess, err := NewSession(c, withTestSessionNamer(c, newGeminiProfile("gemini-3-pro")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		StateDir: stateDir,
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	registerProbeTool(t, sess, &toolCalls)

	out, runErr := sess.ProcessInput(context.Background(), "trigger safety", nil)
	if runErr != nil {
		t.Fatalf("ProcessInput: %v", runErr)
	}
	if out != "recovered after content filter" {
		t.Fatalf("output = %q, want recovered message", out)
	}
	if got := toolCalls.Load(); got != 0 {
		t.Fatalf("tool handler calls = %d, want 0", got)
	}
	if got := modelCalls.Load(); got != 2 {
		t.Fatalf("model calls = %d, want 2 (fell through to handleNoToolCalls recovery)", got)
	}

	entries := readPersistedTranscriptEntries(t, stateDir, sess.ID())
	for _, entry := range entries {
		if entry.Turn.Kind == schema.TurnAssistant {
			for _, p := range entry.Turn.Message.Content {
				if p.Kind == llm.ContentToolCall && p.ToolCall != nil && p.ToolCall.ID == "call_cf_1" {
					t.Fatal("content_filter response persisted suppressed ContentToolCall")
				}
			}
		}
	}
}

func TestSessionGoogleSettlement_StreamingResetsCommunicatePreviewOnNonAuthorizingStatus(t *testing.T) {
	t.Parallel()

	call := communicateCall("comm_preview_1", "partial message")
	client := llm.NewClient()
	client.Register(&streamingAdapter{
		name: "google",
		streamScript: func(st *llm.ChanStream) {
			st.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
			st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: call.ID, Name: call.Name, Type: "function"}})
			st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &llm.ToolCallData{ID: call.ID, Arguments: []byte(`{"message":"partial`)}})
			st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &call})
			finish := llm.FinishReason{Reason: llm.FinishReasonLength, Raw: "incomplete"}
			st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
		},
	})
	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(client, withTestSessionNamer(client, newGeminiProfile("gemini-3-pro")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		LLMRetryPolicy: &policy,
		testOnly:       testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	recorder := startPreviewEventRecorder(sess)

	_, runErr := sess.ProcessInput(context.Background(), "stream incomplete", nil)
	if runErr == nil {
		t.Fatal("expected error for streamed incomplete Google status")
	}
	evs := recorder.stop(sess)

	var sawPreviewStart, sawPreviewReset bool
	for _, ev := range evs {
		switch ev.Kind {
		case events.EventCommunicatePreviewStart:
			if d, ok := ev.Data.(events.CommunicatePreviewStartData); ok && d.CallID == call.ID {
				sawPreviewStart = true
			}
		case events.EventCommunicatePreviewReset:
			if d, ok := ev.Data.(events.CommunicatePreviewResetData); ok && d.CallID == call.ID {
				sawPreviewReset = true
			}
		case events.EventToolCallStart, events.EventToolCallEnd:
			t.Fatalf("unexpected tool execution event on incomplete status: %+v", ev)
		}
	}
	if !sawPreviewStart || !sawPreviewReset {
		t.Fatalf("expected streamed communicate preview start and reset, got start=%v reset=%v", sawPreviewStart, sawPreviewReset)
	}
}
