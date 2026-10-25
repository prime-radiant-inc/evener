package google

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

func TestGeminiStreamSyntheticToolCallIDUsesIdentifierDomain(t *testing.T) {
	sse := `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"q":"x"}}}]}}]}` + "\n\n" +
		`data: {"candidates":[{"finishReason":"STOP"}]}` + "\n\n"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(sse))}
	s := llm.NewChanStream(cancel)
	go decodeGenerateContentStream(ctx, cancel, resp, s, llm.Request{Model: "test"}, "gemini-test", "http://test", llm.APILogCredentialMaterial{}, nil)
	var got string
	for ev := range s.Events() {
		if ev.ToolCall != nil {
			got = ev.ToolCall.ID
		}
	}
	if err := identifier.ValidateSyntheticCallID(got); err != nil {
		t.Fatalf("stream synthetic ID %q: %v", got, err)
	}
}

func TestGeminiSyntheticToolCallIDUsesIdentifierDomain(t *testing.T) {
	r := fromGeminiResponse(map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{map[string]any{
				"functionCall": map[string]any{"name": "lookup", "args": map[string]any{"q": "x"}},
			}}},
		}},
	}, "test")
	calls := r.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(calls))
	}
	if err := identifier.ValidateSyntheticCallID(calls[0].ID); err != nil {
		t.Fatalf("synthetic ID %q: %v", calls[0].ID, err)
	}
}

// TestParseUsageExcludesCachedContent pins llm.Usage's cache-miss invariant for
// Gemini, whose promptTokenCount includes cached content. InputTokens must hold
// only the uncached remainder and CacheReadTokens the cached count, so for
// conformant payloads adding the two recovers promptTokenCount exactly once.
func TestParseUsageExcludesCachedContent(t *testing.T) {
	tests := []struct {
		name            string
		usage           map[string]any
		wantInput       int
		wantOutput      int
		wantTotal       int
		wantCached      *int
		wantReasoning   *int
		assertPromptSum bool
	}{
		{
			name: "cached content is excluded from input tokens",
			usage: map[string]any{
				"promptTokenCount":        100,
				"cachedContentTokenCount": 40,
				"candidatesTokenCount":    10,
				"totalTokenCount":         117,
				"thoughtsTokenCount":      7,
			},
			wantInput:       60,
			wantOutput:      10,
			wantTotal:       117,
			wantCached:      new(40),
			wantReasoning:   new(7),
			assertPromptSum: true,
		},
		{
			name: "absent cached content leaves input unchanged",
			usage: map[string]any{
				"promptTokenCount":     100,
				"candidatesTokenCount": 10,
				"totalTokenCount":      110,
			},
			wantInput:       100,
			wantOutput:      10,
			wantTotal:       110,
			assertPromptSum: true,
		},
		{
			name: "zero cached content leaves input unchanged",
			usage: map[string]any{
				"promptTokenCount":        100,
				"cachedContentTokenCount": 0,
				"candidatesTokenCount":    10,
				"totalTokenCount":         110,
			},
			wantInput:       100,
			wantOutput:      10,
			wantTotal:       110,
			assertPromptSum: true,
		},
		{
			// Google defines cachedContentTokenCount as a subset of
			// promptTokenCount, so this payload is synthetic; it pins the
			// defensive clamp only, and its prompt-sum invariant does not
			// apply because the input itself violates the premise.
			name: "synthetic malformed payload clamps cached content above prompt at zero",
			usage: map[string]any{
				"promptTokenCount":        10,
				"cachedContentTokenCount": 40,
				"candidatesTokenCount":    5,
				"totalTokenCount":         55,
			},
			wantInput:  0,
			wantOutput: 5,
			wantTotal:  55,
			wantCached: new(40),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseUsage(tt.usage)
			if got.InputTokens != tt.wantInput {
				t.Errorf("InputTokens = %d, want %d", got.InputTokens, tt.wantInput)
			}
			if got.OutputTokens != tt.wantOutput {
				t.Errorf("OutputTokens = %d, want %d", got.OutputTokens, tt.wantOutput)
			}
			if got.TotalTokens != tt.wantTotal {
				t.Errorf("TotalTokens = %d, want %d", got.TotalTokens, tt.wantTotal)
			}
			assertOptionalInt(t, "CacheReadTokens", got.CacheReadTokens, tt.wantCached)
			assertOptionalInt(t, "ReasoningTokens", got.ReasoningTokens, tt.wantReasoning)
			if tt.assertPromptSum {
				rawPrompt := llm.IntFromAny(tt.usage["promptTokenCount"])
				cached := 0
				if got.CacheReadTokens != nil {
					cached = *got.CacheReadTokens
				}
				if got.InputTokens+cached != rawPrompt {
					t.Errorf("InputTokens + CacheReadTokens = %d, want promptTokenCount %d", got.InputTokens+cached, rawPrompt)
				}
			}
		})
	}
}

// assertOptionalInt compares an optional usage count without dereferencing a
// nil observed or expected pointer.
func assertOptionalInt(t *testing.T, field string, got, want *int) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("%s = %d, want nil", field, *got)
	case want != nil && got == nil:
		t.Errorf("%s = nil, want %d", field, *want)
	case want != nil && *got != *want:
		t.Errorf("%s = %d, want %d", field, *got, *want)
	}
}
