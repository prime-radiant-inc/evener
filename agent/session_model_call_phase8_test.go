package agent

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/tool"
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

func TestSession_PersistsImageToolResultFromExecResult(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		AgentName: "explorer", // skip vision side-channel; this test only covers tool-result persistence.
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	const toolName = "image_fixture"
	imageBytes := []byte("fake-png-bytes")
	if err := sess.reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name:        toolName,
			Description: "returns deterministic image bytes",
			Parameters:  map[string]any{"type": "object"},
		}},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			return tool.ImageResult{Text: "read image", Data: imageBytes, MediaType: "image/png"}, nil
		},
	}); err != nil {
		t.Fatalf("Register image fixture tool: %v", err)
	}

	call := llm.ToolCallData{
		ID:        "call_img",
		Name:      toolName,
		Arguments: json.RawMessage(`{}`),
		Type:      "function",
	}
	res := sess.execTool(context.Background(), call)
	if len(res.ImageData) == 0 || res.ImageMediaType != "image/png" {
		t.Fatalf("execTool image data/media=%q/%q, want image/png with bytes", res.ImageMediaType, res.ImageData)
	}
	if err := sess.persistToolResults(context.Background(), []llm.ToolCallData{call}, []tool.ExecResult{res}); err != nil {
		t.Fatalf("persistToolResults: %v", err)
	}

	sess.mu.Lock()
	history := append([]schema.Turn(nil), sess.history...)
	sess.mu.Unlock()
	got, ok := findToolResultInHistory(history, "call_img")
	if !ok {
		t.Fatalf("persisted history missing tool result for call_img: %s", turnKinds(history))
	}
	if got.ImageMediaType != "image/png" || len(got.ImageData) == 0 {
		t.Fatalf("persisted image data/media=%q/%q, want image/png with bytes", got.ImageMediaType, got.ImageData)
	}
}
