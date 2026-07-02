package openaicompat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// streamChunks serves an SSE stream of the given chunk payloads and returns
// the adapter pointed at it.
func streamChunks(t *testing.T, chunks []string) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c) //nolint:errcheck
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: [DONE]\n\n") //nolint:errcheck
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)
	return &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
}

// collectThinking drains the stream and returns the reasoning deltas plus the
// final response's thinking parts.
func collectThinking(t *testing.T, a *Adapter) (deltas []string, final *llm.Response) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, err := a.Stream(ctx, llm.Request{Model: "m", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close() //nolint:errcheck
	for ev := range st.Events() {
		if ev.Type == llm.StreamEventReasoningDelta {
			deltas = append(deltas, ev.ReasoningDelta)
		}
		if ev.Type == llm.StreamEventFinish {
			final = ev.Response
		}
	}
	if final == nil {
		t.Fatal("no finish event")
	}
	return deltas, final
}

func thinkingParts(resp *llm.Response) []*llm.ThinkingData {
	var out []*llm.ThinkingData
	for _, p := range resp.Message.Content {
		if p.Kind == llm.ContentThinking && p.Thinking != nil {
			out = append(out, p.Thinking)
		}
	}
	return out
}

func TestStream_ReasoningFieldVariants(t *testing.T) {
	cases := []struct {
		name          string
		field         string
		wantSignature string
	}{
		{name: "reasoning_content", field: "reasoning_content", wantSignature: "reasoning_content"},
		{name: "reasoning (openrouter/chutes)", field: "reasoning", wantSignature: "reasoning"},
		{name: "reasoning_text", field: "reasoning_text", wantSignature: "reasoning_text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := streamChunks(t, []string{
				`{"model":"m","choices":[{"index":0,"delta":{"` + tc.field + `":"thinking "},"finish_reason":null}]}`,
				`{"model":"m","choices":[{"index":0,"delta":{"` + tc.field + `":"hard"},"finish_reason":null}]}`,
				`{"model":"m","choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":null}]}`,
				`{"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			})
			deltas, final := collectThinking(t, a)
			if got := strings.Join(deltas, ""); got != "thinking hard" {
				t.Errorf("reasoning deltas = %q, want %q", got, "thinking hard")
			}
			parts := thinkingParts(final)
			if len(parts) != 1 {
				t.Fatalf("thinking parts = %d, want 1", len(parts))
			}
			if parts[0].Text != "thinking hard" {
				t.Errorf("thinking text = %q", parts[0].Text)
			}
			if parts[0].Signature != tc.wantSignature {
				t.Errorf("thinking signature = %q, want %q", parts[0].Signature, tc.wantSignature)
			}
		})
	}
}

// A provider that duplicates the same content across reasoning_content and
// reasoning (chutes.ai does this) must not double the thinking text.
func TestStream_DuplicatedReasoningFieldsNotDoubled(t *testing.T) {
	a := streamChunks(t, []string{
		`{"model":"m","choices":[{"index":0,"delta":{"reasoning_content":"once","reasoning":"once"},"finish_reason":null}]}`,
		`{"model":"m","choices":[{"index":0,"delta":{"content":"a"},"finish_reason":null}]}`,
		`{"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	})
	deltas, final := collectThinking(t, a)
	if got := strings.Join(deltas, ""); got != "once" {
		t.Errorf("reasoning deltas = %q, want %q", got, "once")
	}
	parts := thinkingParts(final)
	if len(parts) != 1 || parts[0].Text != "once" {
		t.Fatalf("thinking = %+v, want single 'once'", parts)
	}
}

// Streamed reasoning_details (OpenRouter/MiniMax) must not be dropped.
func TestStream_ReasoningDetailsDeltas(t *testing.T) {
	a := streamChunks(t, []string{
		`{"model":"m","choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.text","text":"deep ","format":"unknown","index":0}]},"finish_reason":null}]}`,
		`{"model":"m","choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.text","text":"thought","format":"unknown","index":0}]},"finish_reason":null}]}`,
		`{"model":"m","choices":[{"index":0,"delta":{"content":"a"},"finish_reason":null}]}`,
		`{"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	})
	deltas, final := collectThinking(t, a)
	if got := strings.Join(deltas, ""); got != "deep thought" {
		t.Errorf("reasoning deltas = %q, want %q", got, "deep thought")
	}
	parts := thinkingParts(final)
	if len(parts) != 1 || parts[0].Text != "deep thought" {
		t.Fatalf("thinking = %+v, want 'deep thought'", parts)
	}
}

// Replay routes thinking back to the field it arrived on.
func TestReplay_ThinkingReturnsToSameField(t *testing.T) {
	mkReq := func(sig string) llm.Request {
		return llm.Request{Model: "m", Messages: []llm.Message{
			{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "q"}}},
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "pondered", Signature: sig}},
				{Kind: llm.ContentText, Text: "a"},
			}},
			{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "q2"}}},
		}}
	}
	cases := []struct {
		sig       string
		wantField string
	}{
		{sig: "reasoning", wantField: "reasoning"},
		{sig: "reasoning_text", wantField: "reasoning_text"},
		{sig: "reasoning_content", wantField: "reasoning_content"},
		// Unknown signatures (e.g. an Anthropic crypto blob on a cross-provider
		// transcript) fall back to reasoning_content.
		{sig: "EqQBCgIYAhIkNL", wantField: "reasoning_content"},
		{sig: "", wantField: "reasoning_content"},
	}
	for _, tc := range cases {
		t.Run("sig="+tc.sig, func(t *testing.T) {
			body, err := buildRequestBody(mkReq(tc.sig), false, ModelCompat{})
			if err != nil {
				t.Fatalf("buildRequestBody: %v", err)
			}
			msgs := body["messages"].([]map[string]any)
			assistant := msgs[1]
			if got := assistant[tc.wantField]; got != "pondered" {
				t.Errorf("assistant[%q] = %v, want pondered (full msg %v)", tc.wantField, got, assistant)
			}
			for _, f := range []string{"reasoning_content", "reasoning", "reasoning_text"} {
				if f == tc.wantField {
					continue
				}
				if _, ok := assistant[f]; ok {
					t.Errorf("assistant[%q] unexpectedly present", f)
				}
			}
		})
	}
}

// Non-stream responses parse the alternate reasoning fields too.
func TestComplete_ReasoningFieldVariants(t *testing.T) {
	cases := []struct {
		name          string
		field         string
		wantSignature string
	}{
		{name: "reasoning", field: "reasoning", wantSignature: "reasoning"},
		{name: "reasoning_text", field: "reasoning_text", wantSignature: "reasoning_text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"id":"1","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"a","%s":"hmm"},"finish_reason":"stop"}]}`, tc.field) //nolint:errcheck
			}))
			t.Cleanup(srv.Close)
			a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
			resp, err := a.Complete(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.User("hi")}})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			parts := thinkingParts(&resp)
			if len(parts) != 1 || parts[0].Text != "hmm" {
				t.Fatalf("thinking = %+v, want 'hmm'", parts)
			}
			if parts[0].Signature != tc.wantSignature {
				t.Errorf("signature = %q, want %q", parts[0].Signature, tc.wantSignature)
			}
		})
	}
}
