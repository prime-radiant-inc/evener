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

// assistantReasoningDetails returns the reasoning_details array emitted on the
// (single) assistant message of a built request body.
func assistantReasoningDetails(t *testing.T, req llm.Request) []map[string]any {
	t.Helper()
	body, err := buildRequestBody(req, false, ModelCompat{})
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	msgs := body["messages"].([]map[string]any)
	for _, m := range msgs {
		if m["role"] != "assistant" {
			continue
		}
		rd, ok := m["reasoning_details"]
		if !ok {
			return nil
		}
		details, ok := rd.([]map[string]any)
		if !ok {
			t.Fatalf("reasoning_details not []map[string]any: %T", rd)
		}
		return details
	}
	t.Fatal("no assistant message in body")
	return nil
}

// Encrypted reasoning_details (OpenRouter Gemini/o-series) arrive as opaque
// {type,id,data} items with no text. They must be captured on the thinking
// part's EncryptedContent so the reasoning chain survives replay, and must
// replay back into the reasoning_details array.
func TestStream_EncryptedReasoningDetails_RoundTrip(t *testing.T) {
	a := streamChunks(t, []string{
		`{"model":"m","choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.encrypted","id":"rc_1","data":"OPAQUE"}]},"finish_reason":null}]}`,
		`{"model":"m","choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":null}]}`,
		`{"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	})
	_, final := collectThinking(t, a)
	parts := thinkingParts(final)
	if len(parts) != 1 {
		t.Fatalf("thinking parts = %d, want 1 (encrypted-only must still make a thinking part)", len(parts))
	}
	if parts[0].EncryptedContent == "" {
		t.Fatalf("encrypted content dropped; thinking = %+v", parts[0])
	}
	if !strings.Contains(parts[0].EncryptedContent, "OPAQUE") || !strings.Contains(parts[0].EncryptedContent, "reasoning.encrypted") {
		t.Fatalf("encrypted content = %q", parts[0].EncryptedContent)
	}

	// Replay: the encrypted item must return in reasoning_details.
	req := llm.Request{Model: "m", Messages: []llm.Message{
		llm.User("q"),
		{Role: llm.RoleAssistant, Content: final.Message.Content},
		llm.User("q2"),
	}}
	details := assistantReasoningDetails(t, req)
	if len(details) != 1 {
		t.Fatalf("reasoning_details = %v, want 1 encrypted item", details)
	}
	if details[0]["type"] != "reasoning.encrypted" || details[0]["id"] != "rc_1" || details[0]["data"] != "OPAQUE" {
		t.Fatalf("replayed encrypted detail = %v", details[0])
	}
}

// A turn can carry both a reasoning.text item and a reasoning.encrypted item in
// the same reasoning_details array. The text lands on Text, the encrypted on
// EncryptedContent, and both replay back into reasoning_details.
func TestStream_MixedTextAndEncryptedReasoningDetails(t *testing.T) {
	a := streamChunks(t, []string{
		`{"model":"m","choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.text","text":"deep thought","format":"unknown","index":0},{"type":"reasoning.encrypted","id":"rc_1","data":"OPAQUE"}]},"finish_reason":null}]}`,
		`{"model":"m","choices":[{"index":0,"delta":{"content":"a"},"finish_reason":null}]}`,
		`{"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	})
	_, final := collectThinking(t, a)
	parts := thinkingParts(final)
	if len(parts) != 1 {
		t.Fatalf("thinking parts = %d, want 1", len(parts))
	}
	if parts[0].Text != "deep thought" {
		t.Errorf("thinking text = %q, want %q", parts[0].Text, "deep thought")
	}
	if !strings.Contains(parts[0].EncryptedContent, "OPAQUE") {
		t.Errorf("encrypted content = %q", parts[0].EncryptedContent)
	}
}

// An encrypted detail can stream in a chunk before its tool call finishes; it
// must still be captured (carrier is the thinking part, not the tool call, so
// arrival order is irrelevant).
func TestStream_EncryptedDetailBeforeToolCall(t *testing.T) {
	a := streamChunks(t, []string{
		`{"model":"m","choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.encrypted","id":"call_1","data":"OPAQUE"}]},"finish_reason":null}]}`,
		`{"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":null}]}`,
		`{"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	})
	_, final := collectThinking(t, a)
	parts := thinkingParts(final)
	if len(parts) != 1 || parts[0].EncryptedContent == "" {
		t.Fatalf("encrypted detail not captured before tool call; parts = %+v", parts)
	}
	if !strings.Contains(parts[0].EncryptedContent, "OPAQUE") {
		t.Errorf("encrypted content = %q", parts[0].EncryptedContent)
	}
}

// Replay composes text (MiniMax reasoning_details) with encrypted details:
// the text item comes first, encrypted after.
func TestReplay_MixedTextAndEncryptedReasoningDetails(t *testing.T) {
	encrypted := `[{"type":"reasoning.encrypted","id":"rc_1","data":"OPAQUE"}]`
	req := llm.Request{
		Model: "m",
		ProviderOptions: map[string]any{
			"openai-compatible": map[string]any{"reasoning": map[string]any{"enabled": true}},
		},
		Messages: []llm.Message{
			llm.User("q"),
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "deep thought", EncryptedContent: encrypted}},
				{Kind: llm.ContentText, Text: "a"},
			}},
			llm.User("q2"),
		},
	}
	details := assistantReasoningDetails(t, req)
	if len(details) != 2 {
		t.Fatalf("reasoning_details = %v, want [text, encrypted]", details)
	}
	if details[0]["type"] != "reasoning.text" || details[0]["text"] != "deep thought" {
		t.Errorf("details[0] = %v, want reasoning.text", details[0])
	}
	if details[1]["type"] != "reasoning.encrypted" || details[1]["data"] != "OPAQUE" {
		t.Errorf("details[1] = %v, want reasoning.encrypted", details[1])
	}
}

// Encrypted details must replay even without the useReasoningDetails flag:
// OpenRouter Gemini/o-series require the encrypted block returned regardless.
func TestReplay_EncryptedOnlyWithoutReasoningFlag(t *testing.T) {
	encrypted := `[{"type":"reasoning.encrypted","id":"rc_1","data":"OPAQUE"}]`
	req := llm.Request{Model: "m", Messages: []llm.Message{
		llm.User("q"),
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{EncryptedContent: encrypted}},
			{Kind: llm.ContentText, Text: "a"},
		}},
		llm.User("q2"),
	}}
	details := assistantReasoningDetails(t, req)
	if len(details) != 1 || details[0]["type"] != "reasoning.encrypted" {
		t.Fatalf("reasoning_details = %v, want single encrypted item", details)
	}
}

// Moonshot/Kimi may report usage on choices[0].usage instead of the top-level
// chunk usage; the stream loop must pick it up.
func TestStream_ChoiceLevelUsageFallback(t *testing.T) {
	a := streamChunks(t, []string{
		`{"model":"m","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		`{"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop","usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}]}`,
	})
	_, final := collectThinking(t, a)
	if final.Usage.InputTokens != 7 || final.Usage.OutputTokens != 2 {
		t.Fatalf("choice-level usage not picked up: %+v", final.Usage)
	}
}

// When both top-level and choice-level usage are present, top-level wins.
func TestStream_TopLevelUsageWinsOverChoice(t *testing.T) {
	a := streamChunks(t, []string{
		`{"model":"m","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		`{"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop","usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`,
	})
	_, final := collectThinking(t, a)
	if final.Usage.InputTokens != 7 || final.Usage.OutputTokens != 2 {
		t.Fatalf("top-level usage should win: %+v", final.Usage)
	}
}

// A foreign provider's opaque EncryptedContent (an OpenAI Responses blob on a
// cross-provider transcript) must not replay into reasoning_details.
func TestReplay_ForeignEncryptedContentSkipped(t *testing.T) {
	req := llm.Request{Model: "m", Messages: []llm.Message{
		llm.User("q"),
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{
				Text:             "pondered",
				EncryptedContent: "gAAAAABopaqueOpenAIResponsesBlob",
			}},
			{Kind: llm.ContentText, Text: "a"},
		}},
		llm.User("q2"),
	}}
	body, err := buildRequestBody(req, false, ModelCompat{})
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	msgs := body["messages"].([]map[string]any)
	assistant := msgs[1]
	if _, ok := assistant["reasoning_details"]; ok {
		t.Errorf("foreign EncryptedContent replayed into reasoning_details: %v", assistant)
	}
	if got := assistant["reasoning_content"]; got != "pondered" {
		t.Errorf("thinking text should still replay normally, got %v", got)
	}
}

// Top-level usage takes strict precedence across the WHOLE stream: a later
// choice-level usage chunk must not overwrite earlier top-level numbers
// (matching the non-stream path's precedence).
func TestStream_TopLevelUsageWinsAcrossChunks(t *testing.T) {
	a := streamChunks(t, []string{
		`{"model":"m","choices":[{"index":0,"delta":{"content":"a"},"finish_reason":null}],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}`,
		`{"model":"m","choices":[{"index":0,"delta":{"content":"b"},"finish_reason":null,"usage":{"prompt_tokens":99,"completion_tokens":99,"total_tokens":198}}]}`,
		`{"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	})
	_, final := collectThinking(t, a)
	if final.Usage.InputTokens != 10 {
		t.Fatalf("InputTokens = %d, want the earlier top-level 10, not the later choice-level 99", final.Usage.InputTokens)
	}
}

// OpenRouter's Anthropic route delivers the thinking SIGNATURE as a
// reasoning.text item (streamed: a trailing text-less item; non-stream: text
// and signature merged on one item). Dropping it breaks multi-turn reasoning
// continuation, so it must survive into EncryptedContent…
func TestStream_SignatureReasoningDetailSurvives(t *testing.T) {
	a := streamChunks(t, []string{
		`{"model":"m","choices":[{"index":0,"delta":{"reasoning":"think","reasoning_details":[{"type":"reasoning.text","text":"think","format":"anthropic-claude-v1","index":0}]},"finish_reason":null}]}`,
		`{"model":"m","choices":[{"index":0,"delta":{"reasoning_details":[{"type":"reasoning.text","signature":"SIGBLOB","format":"anthropic-claude-v1","index":0}]},"finish_reason":null}]}`,
		`{"model":"m","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}`,
	})
	_, final := collectThinking(t, a)
	var enc string
	for _, td := range thinkingParts(final) {
		enc = td.EncryptedContent
	}
	if !strings.Contains(enc, "SIGBLOB") {
		t.Fatalf("signature item dropped: EncryptedContent = %q", enc)
	}
}

// …and replay as OpenRouter's canonical merged shape: ONE reasoning.text item
// carrying the accumulated text plus the signature/format — not a synthetic
// text item alongside a text-less signature item.
func TestToChatMessages_MergesTextIntoSignatureItem(t *testing.T) {
	msgs := []llm.Message{{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{{
			Kind: llm.ContentThinking,
			Thinking: &llm.ThinkingData{
				Text:             "think",
				Signature:        "reasoning",
				EncryptedContent: `[{"type":"reasoning.text","signature":"SIGBLOB","format":"anthropic-claude-v1","index":0}]`,
			},
		}, {Kind: llm.ContentText, Text: "hi"}},
	}}
	out, err := toChatMessages(msgs, ModelCompat{}, false)
	if err != nil {
		t.Fatalf("toChatMessages: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("messages = %d, want 1", len(out))
	}
	details, _ := out[0]["reasoning_details"].([]map[string]any)
	if len(details) != 1 {
		t.Fatalf("reasoning_details = %#v, want exactly one merged item", out[0]["reasoning_details"])
	}
	d := details[0]
	if d["text"] != "think" || d["signature"] != "SIGBLOB" || d["format"] != "anthropic-claude-v1" {
		t.Fatalf("merged item = %#v, want text+signature+format together", d)
	}
}

// Anthropic-routed OpenRouter models silently return NO reasoning when
// tool_choice forces tool use (direct Anthropic 400s on the combo; OpenRouter
// degrades silently — live-bisected 2026-07-02). With the quirk, an active
// reasoning request downgrades a forcing tool_choice to "auto"; without
// reasoning the forcing stays.
func TestBuildRequestBody_ToolChoiceAutoUnderReasoning(t *testing.T) {
	quirks := QuirksPreset("openrouter")
	effort := "high"
	req := llm.Request{
		Model:           "anthropic/claude-sonnet-4.5",
		Messages:        []llm.Message{llm.User("hi")},
		ReasoningEffort: &effort,
		ToolChoice:      &llm.ToolChoice{Mode: "required"},
	}
	body, err := buildRequestBody(req, true, ModelCompat{Quirks: quirks})
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	if got := body["tool_choice"]; got != "auto" {
		t.Fatalf("tool_choice = %#v, want auto under reasoning", got)
	}

	req.ReasoningEffort = nil
	body, err = buildRequestBody(req, true, ModelCompat{Quirks: quirks})
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	if got := body["tool_choice"]; got != "required" {
		t.Fatalf("tool_choice = %#v, want required without reasoning", got)
	}
}
