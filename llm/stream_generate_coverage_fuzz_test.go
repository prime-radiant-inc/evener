package llm

import (
	"context"
	"errors"
	"testing"
)

type streamGenerateCoverageAdapter struct {
	stream func(context.Context, Request) (Stream, error)
}

func (*streamGenerateCoverageAdapter) Name() string { return "stream-coverage" }
func (*streamGenerateCoverageAdapter) Complete(context.Context, Request) (Response, error) {
	return Response{}, errors.New("unused")
}
func (a *streamGenerateCoverageAdapter) Stream(ctx context.Context, req Request) (Stream, error) {
	return a.stream(ctx, req)
}

func runStreamGenerateCoverage(t *testing.T, ctx context.Context, adapter *streamGenerateCoverageAdapter, tools []Tool) (*Response, error) {
	t.Helper()
	client := NewClient()
	client.Register(adapter)
	prompt := "coverage"
	result, err := StreamGenerate(ctx, GenerateOptions{Client: client, Provider: adapter.Name(), Model: "m", Prompt: &prompt, Tools: tools})
	if err != nil {
		return nil, err
	}
	for range result.Events() {
	}
	return result.Response()
}

// FuzzStreamGenerateCoverage replays the deterministic scripted-provider
// scenarios that distinguish the high-level streaming loop's control paths.
func FuzzStreamGenerateCoverage(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		TestStreamGenerate_RejectsPromptAndMessagesTogether(t)
		TestStreamGenerate_TimeoutPerStep_EmitsRequestTimeoutError(t)
		TestStreamGenerate_TimeoutTotal_EmitsRequestTimeoutError(t)
		TestStreamGenerate_SimpleStreaming_YieldsDeltasAndFinish(t)
		TestStreamGenerate_ToolLoop_EmitsStepFinishAndContinues(t)
		TestStreamGenerate_PassiveToolCall_StopsWithoutStepFinish(t)
		TestStreamGenerate_DoesNotRetryAfterPartialDataDelivered(t)
		TestStreamGenerate_RetriesStreamTruncation(t)
		TestStreamGenerate_RetriesErrorEventWithoutForwardingAttemptError(t)
		TestStreamGenerate_TruncationAfterPartialOutput_NoRetry(t)
		TestStreamGenerate_Cancellation_EmitsAbortError(t)
		TestStreamResult_TextStream_FiltersToTextDeltasOnly(t)
		TestStreamGenerate_ToolCallsWithStopFinish_DoesNotExecute(t)
		TestStreamGenerate_TotalUsage_AggregatesAcrossSteps(t)
		TestStreamGenerate_StopWhen_TerminatesToolLoopEarly(t)

		// Directly cover accessor states and verify returned responses are copies.
		var nilResult *StreamResult
		if got := nilResult.PartialResponse(); got != nil {
			t.Fatalf("nil PartialResponse = %#v", got)
		}
		if _, err := nilResult.Response(); err == nil {
			t.Fatal("nil Response unexpectedly succeeded")
		}
		done := make(chan struct{})
		close(done)
		result := &StreamResult{done: done, err: errors.New("sentinel")}
		if got := result.PartialResponse(); got != nil {
			t.Fatalf("empty PartialResponse = %#v", got)
		}
		if got, err := result.Response(); got != nil || !errors.Is(err, result.err) {
			t.Fatalf("empty Response = %#v, %v", got, err)
		}
		result.partial = &Response{ID: "partial"}
		partial := result.PartialResponse()
		partial.ID = "changed"
		if result.partial.ID != "partial" {
			t.Fatal("PartialResponse returned aliased state")
		}

		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		_, _ = runStreamGenerateCoverage(t, cancelled, &streamGenerateCoverageAdapter{stream: func(context.Context, Request) (Stream, error) {
			t.Fatal("adapter called with pre-cancelled context")
			return nil, nil
		}}, nil)

		_, _ = runStreamGenerateCoverage(t, context.Background(), &streamGenerateCoverageAdapter{stream: func(context.Context, Request) (Stream, error) {
			return nil, ErrorFromHTTPStatus("stream-coverage", 400, "permanent", nil, nil)
		}}, nil)

		// A nil-error ERROR event is informational and is forwarded before FINISH.
		_, _ = runStreamGenerateCoverage(t, context.Background(), &streamGenerateCoverageAdapter{stream: func(context.Context, Request) (Stream, error) {
			resp := Response{Model: "m", Provider: "stream-coverage", Finish: FinishReason{Reason: FinishReasonStop}}
			return newSliceStream(StreamEvent{Type: StreamEventError}, StreamEvent{Type: StreamEventFinish, Response: &resp}), nil
		}}, nil)

		// FINISH without content or a response must surface the missing-response error.
		oldAccumulatedResponse := streamGenerateAccumulatedResponse
		streamGenerateAccumulatedResponse = func(*StreamAccumulator) *Response { return nil }
		_, _ = runStreamGenerateCoverage(t, context.Background(), &streamGenerateCoverageAdapter{stream: func(context.Context, Request) (Stream, error) {
			return newSliceStream(StreamEvent{Type: StreamEventFinish}), nil
		}}, nil)
		streamGenerateAccumulatedResponse = oldAccumulatedResponse

		// Text accumulation supplies a response when FINISH omits its Response field.
		resp, err := runStreamGenerateCoverage(t, context.Background(), &streamGenerateCoverageAdapter{stream: func(context.Context, Request) (Stream, error) {
			finish := FinishReason{Reason: FinishReasonStop}
			return newSliceStream(
				StreamEvent{Type: StreamEventTextDelta, Delta: "accumulated"},
				StreamEvent{Type: StreamEventFinish, FinishReason: &finish},
			), nil
		}}, nil)
		if err != nil || resp == nil || resp.Text() != "accumulated" {
			t.Fatalf("accumulated response = %#v, %v", resp, err)
		}

		passive := Tool{Definition: ToolDefinition{Name: "passive", Parameters: map[string]any{"type": "object"}}}
		active := Tool{Definition: ToolDefinition{Name: "active", Parameters: map[string]any{"type": "object"}}, Execute: func(context.Context, any) (any, error) { return "unused", nil }}
		_, _ = runStreamGenerateCoverage(t, context.Background(), &streamGenerateCoverageAdapter{stream: func(context.Context, Request) (Stream, error) {
			call := ToolCallData{ID: "c", Name: "passive", Arguments: []byte(`{}`)}
			resp := Response{Model: "m", Provider: "stream-coverage", Message: Message{Role: RoleAssistant,
				Content: []ContentPart{{Kind: ContentToolCall, ToolCall: &call}}}, Finish: FinishReason{Reason: FinishReasonToolCalls}}
			return newSliceStream(StreamEvent{Type: StreamEventFinish, Response: &resp}), nil
		}}, []Tool{active, passive})
	})
}
