package difftest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/anthropic"
	"primeradiant.com/serf/llm/providers/google"
	"primeradiant.com/serf/llm/providers/openai"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

// This file holds serf's per-provider STREAM-vs-NON-STREAM differential oracle.
//
// Both real decode bugs found during the fuzzing campaign were of one shape: a
// provider's NON-streaming decoder was correct while its STREAMING decoder was
// wrong (anthropic read the finish reason from the wrong place; gemini dropped
// usage when it rode the finish chunk). The cross-provider oracle
// (FuzzCrossProviderDifferential) cannot see that class directly — it only
// drives the streaming path of every adapter, so a stream/non-stream skew that
// is identical across providers would slip through. This oracle targets that
// axis head-on: for ONE canonical logical response it decodes the SAME logical
// content through (a) the provider's real streaming decoder and (b) the
// provider's real non-streaming decoder, then asserts the two llm.Response
// values agree modulo the documented allow-list.
//
// For each provider the two decoders are genuinely independent functions:
//   - anthropic:    decodeStream            vs fromAnthropicResponse
//   - google:       decodeStream            vs fromGeminiResponse
//   - openaicompat: decodeStream            vs fromChatCompletionResponse
//   - openai:       decodeResponsesStream   vs fromResponses — NOTE: the openai
//     streaming path rebuilds its final Response by calling fromResponses on the
//     response.completed object, so this pair is near-tautological. It is kept
//     only as a guard that the StreamAccumulator passes the finish Response
//     through unchanged; it carries far less signal than the other three.

// jsonProvider couples a real adapter with both wire encoders (SSE for the
// streaming decoder, a single JSON body for the non-streaming decoder) and the
// adapter's own httptest server. The same server serves both paths: the body is
// swapped under its mutex before each drive, and the two drives run sequentially
// per provider so there is never an overlapping read.
type jsonProvider struct {
	name       string
	encodeSSE  func(logicalResponse) []byte
	encodeJSON func(logicalResponse) []byte
	stream     func(ctx context.Context, req llm.Request) (llm.Stream, error)
	complete   func(ctx context.Context, req llm.Request) (llm.Response, error)
	srv        *sseServer
}

func (p jsonProvider) driveStreaming(lr logicalResponse) (*llm.Response, error) {
	return driveStream(p.stream, p.encodeSSE(lr), p.srv)
}

// driveComplete feeds the non-streaming JSON body to the adapter's real Complete
// method (the genuine end-to-end non-stream decode path, not an in-package
// shortcut).
func (p jsonProvider) driveComplete(lr logicalResponse) (*llm.Response, error) {
	p.srv.set(p.encodeJSON(lr))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := p.complete(ctx, llm.Request{Model: "diff-model", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// jsonProviders builds the four real adapters wired to their own httptest
// servers, each with both an SSE encoder and a JSON-body encoder. The caller
// must invoke the returned cleanup to release the servers.
func jsonProviders() ([]jsonProvider, func()) {
	anthSrv := newSSEServer("anthropic")
	googSrv := newSSEServer("google")
	oaiSrv := newSSEServer("openai")
	compatSrv := newSSEServer("openaicompat")

	anth := &anthropic.Adapter{BaseURL: anthSrv.srv.URL}
	goog := &google.Adapter{BaseURL: googSrv.srv.URL}
	oai := &openai.Adapter{BaseURL: oaiSrv.srv.URL}
	compat := &openaicompat.Adapter{BaseURL: compatSrv.srv.URL}

	ps := []jsonProvider{
		{
			name:       "anthropic",
			encodeSSE:  encodeAnthropic,
			encodeJSON: encodeAnthropicJSON,
			stream:     anth.Stream,
			complete:   anth.Complete,
			srv:        anthSrv,
		},
		{
			// Use the finish-chunk-usage SSE shape (not the separate-chunk
			// encodeGoogle): usage riding the finishReason chunk is the dominant
			// Gemini shape AND the exact wire skew the gemini usage-drop bug lived
			// in, so this is where the differential earns its keep for google.
			name:       "google",
			encodeSSE:  encodeGoogleStreamFinishUsage,
			encodeJSON: encodeGoogleJSON,
			stream:     goog.Stream,
			complete:   goog.Complete,
			srv:        googSrv,
		},
		{
			name:       "openaicompat",
			encodeSSE:  encodeOpenAICompat,
			encodeJSON: encodeOpenAICompatJSON,
			stream:     compat.Stream,
			complete:   compat.Complete,
			srv:        compatSrv,
		},
		{
			name:       "openai",
			encodeSSE:  encodeOpenAIResponses,
			encodeJSON: encodeOpenAIResponsesJSON,
			stream:     oai.Stream,
			complete:   oai.Complete,
			srv:        oaiSrv,
		},
	}
	cleanup := func() {
		anthSrv.close()
		googSrv.close()
		oaiSrv.close()
		compatSrv.close()
	}
	return ps, cleanup
}

// ---- Non-streaming JSON body encoders ----
//
// Each emits the single JSON response body that the provider's NON-streaming
// decoder reads, carrying the same logical content as the matching SSE encoder.
// Only the fields the non-stream decoder actually reads are emitted. Reasoning
// is encoded where the non-stream decoder accepts it, but it never enters the
// projection (project() ignores thinking), so its encoding is allow-listed.

// encodeAnthropicJSON renders a /v1/messages non-streaming message object:
// content is a typed-block array, stop_reason sits at the TOP level (the
// streaming wire nests it under message_delta.delta — the exact skew the
// anthropic finish-reason bug lived in).
func encodeAnthropicJSON(lr logicalResponse) []byte {
	var content []string
	if lr.Reasoning != "" {
		content = append(content, fmt.Sprintf(`{"type":"thinking","thinking":%s,"signature":""}`, qs(lr.Reasoning)))
	}
	if lr.Text != "" {
		content = append(content, fmt.Sprintf(`{"type":"text","text":%s}`, qs(lr.Text)))
	}
	for i, tc := range lr.Tools {
		content = append(content, fmt.Sprintf(
			`{"type":"tool_use","id":"toolu_%d","name":%s,"input":%s}`, i, qs(tc.Name), tc.argsJSON()))
	}
	stop := map[string]string{"stop": "end_turn", "length": "max_tokens", "tool_calls": "tool_use"}[lr.Finish]
	return []byte(fmt.Sprintf(
		`{"id":"msg_diff","type":"message","role":"assistant","model":"diff-model","content":[%s],"stop_reason":%s,"usage":{"input_tokens":%d,"output_tokens":%d}}`,
		strings.Join(content, ","), qs(stop), lr.InTok, lr.OutTok))
}

// encodeGoogleStreamFinishUsage renders the streamGenerateContent SSE where the
// usageMetadata rides the SAME chunk as finishReason (the dominant real Gemini
// shape). The content parts arrive in an earlier chunk; the final chunk carries
// finishReason + usageMetadata together. This deliberately differs from the
// cross-provider encodeGoogle (which puts usage in a separate earlier chunk):
// the usage-on-finish path is precisely where the gemini usage-drop bug lived,
// so the differential must drive it to guard the fix.
func encodeGoogleStreamFinishUsage(lr logicalResponse) []byte {
	var parts []string
	if lr.Reasoning != "" {
		parts = append(parts, fmt.Sprintf(`{"thought":true,"text":%s}`, qs(lr.Reasoning)))
	}
	if lr.Text != "" {
		parts = append(parts, fmt.Sprintf(`{"text":%s}`, qs(lr.Text)))
	}
	for _, tc := range lr.Tools {
		parts = append(parts, fmt.Sprintf(`{"functionCall":{"name":%s,"args":%s}}`, qs(tc.Name), tc.argsJSON()))
	}
	finish := map[string]string{"stop": "STOP", "length": "MAX_TOKENS", "tool_calls": "STOP"}[lr.Finish]

	var b strings.Builder
	fmt.Fprintf(&b, "data: {\"candidates\":[{\"content\":{\"parts\":[%s]}}]}\n\n", strings.Join(parts, ","))
	fmt.Fprintf(&b,
		"data: {\"candidates\":[{\"finishReason\":%s}],\"usageMetadata\":{\"promptTokenCount\":%d,\"candidatesTokenCount\":%d,\"totalTokenCount\":%d}}\n\n",
		qs(finish), lr.InTok, lr.OutTok, lr.totalTok())
	return []byte(b.String())
}

// encodeGoogleJSON renders a :generateContent non-streaming response: a single
// candidate whose content.parts carry the content, finishReason and
// usageMetadata at the top level (the streaming wire splits these across
// chunks — the skew the gemini usage-on-finish bug lived in).
func encodeGoogleJSON(lr logicalResponse) []byte {
	var parts []string
	if lr.Reasoning != "" {
		parts = append(parts, fmt.Sprintf(`{"thought":true,"text":%s}`, qs(lr.Reasoning)))
	}
	if lr.Text != "" {
		parts = append(parts, fmt.Sprintf(`{"text":%s}`, qs(lr.Text)))
	}
	for _, tc := range lr.Tools {
		parts = append(parts, fmt.Sprintf(`{"functionCall":{"name":%s,"args":%s}}`, qs(tc.Name), tc.argsJSON()))
	}
	finish := map[string]string{"stop": "STOP", "length": "MAX_TOKENS", "tool_calls": "STOP"}[lr.Finish]
	return []byte(fmt.Sprintf(
		`{"candidates":[{"content":{"parts":[%s]},"finishReason":%s}],"usageMetadata":{"promptTokenCount":%d,"candidatesTokenCount":%d,"totalTokenCount":%d}}`,
		strings.Join(parts, ","), qs(finish), lr.InTok, lr.OutTok, lr.totalTok()))
}

// encodeOpenAIResponsesJSON renders the non-streaming /v1/responses object. It
// is exactly the inner "response" object the streaming encoder wraps inside its
// response.completed frame, so both paths feed fromResponses the same bytes
// (see the near-tautology note at the top of this file).
func encodeOpenAIResponsesJSON(lr logicalResponse) []byte {
	var output []string
	if lr.Text != "" {
		output = append(output, fmt.Sprintf(`{"type":"message","content":[{"type":"output_text","text":%s}]}`, qs(lr.Text)))
	}
	for i, tc := range lr.Tools {
		output = append(output, fmt.Sprintf(
			`{"type":"function_call","call_id":"call_%d","id":"fc_%d","name":%s,"arguments":%s}`, i, i, qs(tc.Name), qs(tc.argsJSON())))
	}
	usage := fmt.Sprintf(`"usage":{"input_tokens":%d,"output_tokens":%d,"total_tokens":%d}`, lr.InTok, lr.OutTok, lr.totalTok())
	if lr.Finish == "length" {
		return []byte(fmt.Sprintf(
			`{"id":"resp_diff","model":"diff-model","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[%s],%s}`,
			strings.Join(output, ","), usage))
	}
	return []byte(fmt.Sprintf(
		`{"id":"resp_diff","model":"diff-model","status":"completed","output":[%s],%s}`,
		strings.Join(output, ","), usage))
}

// encodeOpenAICompatJSON renders a non-streaming /chat/completions object: the
// content lives under choices[0].message (the streaming wire carries it under
// choices[0].delta — independent struct shapes decoded by independent code).
func encodeOpenAICompatJSON(lr logicalResponse) []byte {
	msgFields := []string{
		`"role":"assistant"`,
		`"content":` + qs(lr.Text),
	}
	if lr.Reasoning != "" {
		msgFields = append(msgFields, `"reasoning_content":`+qs(lr.Reasoning))
	}
	if len(lr.Tools) > 0 {
		var tcs []string
		for i, tc := range lr.Tools {
			tcs = append(tcs, fmt.Sprintf(
				`{"id":"call_%d","type":"function","function":{"name":%s,"arguments":%s}}`, i, qs(tc.Name), qs(tc.argsJSON())))
		}
		msgFields = append(msgFields, fmt.Sprintf(`"tool_calls":[%s]`, strings.Join(tcs, ",")))
	}
	finish := map[string]string{"stop": "stop", "length": "length", "tool_calls": "tool_calls"}[lr.Finish]
	return []byte(fmt.Sprintf(
		`{"id":"c_diff","model":"diff-model","choices":[{"index":0,"message":{%s},"finish_reason":%s}],"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`,
		strings.Join(msgFields, ","), qs(finish), lr.InTok, lr.OutTok, lr.totalTok()))
}

// ALLOW-LIST — fields INTENTIONALLY excluded from the stream-vs-non-stream
// equivalence check. This oracle compares the SAME projection as the
// cross-provider oracle (text, tool name + canonical args, finish class, usage
// triple), so the same exclusions apply (Response.ID/Model/Provider, Raw
// payloads, FinishReason.Raw, tool call IDs/ItemID/Type, reasoning encoding,
// Usage pointer fields, framing metadata). One axis matters specifically here:
//
//   - reasoning ENCODING differs by path even WITHIN a provider (e.g. OpenAI's
//     stream emits reasoning_summary deltas while the non-stream output array
//     carries none), but project() never reads thinking content, so it is
//     excluded by construction — a difference there is not a finding.
//
// Everything the projection DOES compare must agree between a provider's two
// decoders; a disagreement there is a real stream/non-stream decode bug.

// streamVsNonStreamDivergence drives both decoders for every provider, projects,
// and returns a non-empty message describing the first stream/non-stream
// disagreement, or "" if all providers' two paths agree. A path that fails to
// decode (or yields no response) is itself reported as a divergence.
func streamVsNonStreamDivergence(t *testing.T, ps []jsonProvider, lr logicalResponse) string {
	t.Helper()
	for _, p := range ps {
		sresp, serr := p.driveStreaming(lr)
		if serr != nil {
			return fmt.Sprintf("provider %s streaming path failed to decode: %v\n  sse=%q", p.name, serr, p.encodeSSE(lr))
		}
		if sresp == nil {
			return fmt.Sprintf("provider %s streaming path produced no response\n  sse=%q", p.name, p.encodeSSE(lr))
		}
		cresp, cerr := p.driveComplete(lr)
		if cerr != nil {
			return fmt.Sprintf("provider %s non-streaming path failed to decode: %v\n  json=%q", p.name, cerr, p.encodeJSON(lr))
		}
		if cresp == nil {
			return fmt.Sprintf("provider %s non-streaming path produced no response\n  json=%q", p.name, p.encodeJSON(lr))
		}
		sp := project(sresp)
		cp := project(cresp)
		if !equalProjections(sp, cp) {
			return fmt.Sprintf(
				"provider %s stream-vs-non-stream decode divergence:\n  stream    %s\n  complete  %s\n  logical=%+v",
				p.name, sp.String(), cp.String(), lr)
		}
	}
	return ""
}

// FuzzStreamVsNonStreamDifferential is serf's per-provider stream/non-stream
// differential oracle. It generates one canonical logical response, encodes it
// into each provider's SSE wire AND its single-body JSON wire, decodes each back
// through the REAL adapter's streaming and non-streaming paths respectively, and
// asserts the two decoded responses are equivalent modulo the documented
// allow-list. A divergence is a genuine intra-provider stream/non-stream decode
// bug (the exact class of the two bugs already found) — do NOT weaken the check
// to hide one.
func FuzzStreamVsNonStreamDifferential(f *testing.F) {
	seeds := [][]byte{
		{},                                   // empty → minimal text "x", stop
		{0x00},                               // text-only path
		{0x01, 0x05, 0x41, 0x42},             // one tool call
		{0x02, 0x03, 0x61, 0x62, 0x07, 0x09}, // two tool calls
		{0x00, 0x10, 0x20, 0x30, 0x40, 0x50}, // text + reasoning + usage
		{0x01, 0x02, 0x63, 0x64, 0x65, 0x66, 0xAA, 0xBB},       // tool + reasoning
		{0x00, 0x00, 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06}, // length finish
	}
	for _, s := range seeds {
		f.Add(s)
	}

	ps, cleanup := jsonProviders()
	f.Cleanup(cleanup)

	f.Fuzz(func(t *testing.T, raw []byte) {
		lr := generate(raw)
		if msg := streamVsNonStreamDivergence(t, ps, lr); msg != "" {
			t.Fatal(msg)
		}
	})
}

// TestStreamVsNonStreamSanity is a fast, explicit seed check (no fuzzing): a
// fixed set of logical responses with text, reasoning, tool calls, length
// finishes and a usage triple must decode equivalently through each provider's
// streaming and non-streaming paths. It documents the oracle's intent and
// guards the encoders/drivers independently of the fuzz engine.
func TestStreamVsNonStreamSanity(t *testing.T) {
	ps, cleanup := jsonProviders()
	defer cleanup()

	cases := []logicalResponse{
		{Text: "hello world", Finish: "stop", InTok: 7, OutTok: 3},
		{Text: "stopped early", Finish: "length", InTok: 10, OutTok: 5},
		{Text: "thinking out loud", Reasoning: "ponder", Finish: "stop", InTok: 12, OutTok: 6},
		{Text: "", Finish: "tool_calls", InTok: 4, OutTok: 8, Tools: []logicalToolCall{
			{Name: "shell", Args: map[string]string{"cmd": "ls"}},
		}},
		{Text: "calling tools", Reasoning: "let me think", Finish: "tool_calls", InTok: 11, OutTok: 9, Tools: []logicalToolCall{
			{Name: "grep", Args: map[string]string{"q": "needle", "path": "."}},
			{Name: "shell", Args: map[string]string{"cmd": "pwd"}},
		}},
	}

	for i, lr := range cases {
		t.Run(fmt.Sprintf("case%d", i), func(t *testing.T) {
			if msg := streamVsNonStreamDivergence(t, ps, lr); msg != "" {
				t.Fatal(msg)
			}
		})
	}
}
