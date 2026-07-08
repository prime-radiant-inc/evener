package agent

import (
	"bytes"
	"context"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestFallbackChain_ContinuationRejectionRetriesFullHistoryBeforeModelFallback(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	continuationErr := llm.ErrorFromHTTPStatus("openai", 404, "Previous response not found", map[string]any{
		"error": map[string]any{
			"code":    "previous_response_not_found",
			"message": "Previous response not found",
			"type":    "invalid_request_error",
		},
	}, nil)

	f := &agenttest.ModelTrackingAdapter{
		Provider: "openai",
		Respond: func(req llm.Request) (llm.Response, error) {
			switch len(req.Messages) {
			case 1:
				return llm.Response{}, continuationErr
			case 2:
				if req.Model != "primary" {
					t.Fatalf("same-endpoint recovery model = %q, want primary", req.Model)
				}
				return agenttest.FinalResponse("full-history recovery answered"), nil
			default:
				t.Fatalf("unexpected request messages: %+v", req.Messages)
				return llm.Response{}, nil
			}
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, NewOpenAIProfile("primary"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
		ModelFallbacks: []string{"fallback-b"},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	drainSessionEvents(sess)

	req := phase8DeltaRequest()
	_, usedReq, attempt, err := sess.callModelWithFallback(context.Background(), NewOpenAIProfile("primary"), req, "", 1)
	if err != nil {
		t.Fatalf("callModelWithFallback: %v", err)
	}
	if usedReq.HistoryMode != llm.HistoryModeFullHistoryFallback {
		t.Fatalf("used history mode = %q, want %q", usedReq.HistoryMode, llm.HistoryModeFullHistoryFallback)
	}
	if attempt.HistoryMode != llm.HistoryModeFullHistoryFallback {
		t.Fatalf("attempt history mode = %q, want %q", attempt.HistoryMode, llm.HistoryModeFullHistoryFallback)
	}
	requests := f.Requests()
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	if requests[0].HistoryMode != llm.HistoryModeResponsesDelta || requests[0].PreviousResponseID != "resp_phase8_anchor" {
		t.Fatalf("first request = %+v", requests[0])
	}
	if requests[1].HistoryMode != llm.HistoryModeFullHistoryFallback ||
		requests[1].PreviousResponseID != "" ||
		requests[1].Continuation != nil ||
		requestMessagesContainText(requests[1].Messages, "PHASE8_DELTA_ONLY_MARKER") ||
		!requestMessagesContainText(requests[1].Messages, "PHASE8_FULL_HISTORY_MARKER") {
		t.Fatalf("full-history retry request = %+v", requests[1])
	}
}

func TestFallbackChain_ContinuationRecoveryFailureThenModelFallback(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	continuationErr := llm.ErrorFromHTTPStatus("openai", 404, "Previous response not found", map[string]any{
		"error": map[string]any{"code": "previous_response_not_found", "message": "Previous response not found"},
	}, nil)
	recoveryErr := llm.ErrorFromHTTPStatus("openai", 403, "recovery denied", nil, nil)

	f := &agenttest.ModelTrackingAdapter{
		Provider: "openai",
		Respond: func(req llm.Request) (llm.Response, error) {
			switch req.Model {
			case "primary":
				if req.HistoryMode == llm.HistoryModeResponsesDelta {
					return llm.Response{}, continuationErr
				}
				if req.HistoryMode == llm.HistoryModeFullHistoryFallback {
					return llm.Response{}, recoveryErr
				}
			case "fallback-b":
				return agenttest.FinalResponse("fallback model answered"), nil
			}
			t.Fatalf("unexpected request: model=%q history_mode=%q", req.Model, req.HistoryMode)
			return llm.Response{}, nil
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, NewOpenAIProfile("primary"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
		ModelFallbacks: []string{"fallback-b"},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	drainSessionEvents(sess)

	_, usedReq, _, err := sess.callModelWithFallback(context.Background(), NewOpenAIProfile("primary"), phase8DeltaRequest(), "", 1)
	if err != nil {
		t.Fatalf("callModelWithFallback: %v", err)
	}
	if usedReq.Model != "fallback-b" || usedReq.HistoryMode != llm.HistoryModeFullHistory {
		t.Fatalf("used request = %+v", usedReq)
	}
	got := f.Models()
	want := []string{"primary", "primary", "fallback-b"}
	if len(got) != len(want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("models = %v, want %v", got, want)
		}
	}
	requests := f.Requests()
	if len(requests) != 3 {
		t.Fatalf("request count = %d, want 3", len(requests))
	}
	if requests[2].HistoryMode != llm.HistoryModeFullHistory ||
		requests[2].PreviousResponseID != "" ||
		requests[2].Continuation != nil ||
		requestMessagesContainText(requests[2].Messages, "PHASE8_DELTA_ONLY_MARKER") ||
		!requestMessagesContainText(requests[2].Messages, "PHASE8_FULL_HISTORY_MARKER") {
		t.Fatalf("model fallback request = %+v", requests[2])
	}
}

func TestFallbackChain_NonContinuationErrorSkipsFullHistoryRetry(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	invalidErr := llm.ErrorFromHTTPStatus("openai", 422, "input item is invalid", map[string]any{
		"error": map[string]any{"code": "invalid_request_error", "message": "input item is invalid"},
	}, nil)

	f := &agenttest.ModelTrackingAdapter{
		Provider: "openai",
		Respond: func(req llm.Request) (llm.Response, error) {
			switch req.Model {
			case "primary":
				return llm.Response{}, invalidErr
			case "fallback-b":
				return agenttest.FinalResponse("fallback model answered"), nil
			}
			t.Fatalf("unexpected model %q", req.Model)
			return llm.Response{}, nil
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, NewOpenAIProfile("primary"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
		ModelFallbacks: []string{"fallback-b"},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	drainSessionEvents(sess)

	_, _, _, err = sess.callModelWithFallback(context.Background(), NewOpenAIProfile("primary"), phase8DeltaRequest(), "", 1)
	if err != nil {
		t.Fatalf("callModelWithFallback: %v", err)
	}
	got := f.Models()
	want := []string{"primary", "fallback-b"}
	if len(got) != len(want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("models = %v, want %v", got, want)
		}
	}
	requests := f.Requests()
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	if requests[1].HistoryMode != llm.HistoryModeFullHistory ||
		requests[1].PreviousResponseID != "" ||
		requests[1].Continuation != nil ||
		requestMessagesContainText(requests[1].Messages, "PHASE8_DELTA_ONLY_MARKER") ||
		!requestMessagesContainText(requests[1].Messages, "PHASE8_FULL_HISTORY_MARKER") {
		t.Fatalf("model fallback request = %+v", requests[1])
	}
}

func phase8DeltaRequest() llm.Request {
	return llm.Request{
		Provider:           "openai",
		Model:              "primary",
		HistoryMode:        llm.HistoryModeResponsesDelta,
		PreviousResponseID: "resp_phase8_anchor",
		Continuation: &llm.ContinuationMetadata{
			PreviousResponseIDHash: "cont-handle-v1:response_id:phase8",
		},
		Messages: []llm.Message{
			llm.User("PHASE8_DELTA_ONLY_MARKER"),
		},
		FullHistoryFallbackMessages: []llm.Message{
			llm.User("PHASE8_FULL_HISTORY_MARKER"),
			llm.Assistant("prior assistant"),
		},
	}
}

func TestExpandHistory_ImageToolResultPreservesImageData(t *testing.T) {
	imageBytes := []byte("fake-png-bytes")
	history := []schema.Turn{
		schema.NewTurn(schema.TurnToolResults, llm.Message{
			Role: llm.RoleTool,
			Content: []llm.ContentPart{{
				Kind: llm.ContentToolResult,
				ToolResult: &llm.ToolResultData{
					ToolCallID:     "call_img",
					Name:           "read_file",
					Content:        "read image",
					ImageData:      imageBytes,
					ImageMediaType: "image/png",
				},
			}},
		}),
	}

	messages := expandHistory(history)
	if len(messages) != 1 {
		t.Fatalf("expandHistory returned %d messages, want 1", len(messages))
	}
	parts := messages[0].Content
	if len(parts) != 1 || parts[0].Kind != llm.ContentToolResult || parts[0].ToolResult == nil {
		t.Fatalf("expanded message parts=%+v, want one tool result", parts)
	}
	got := parts[0].ToolResult
	if got.ImageMediaType != "image/png" || !bytes.Equal(got.ImageData, imageBytes) {
		t.Fatalf("expanded image data/media=%q/%q, want image/png with bytes", got.ImageMediaType, got.ImageData)
	}
}
