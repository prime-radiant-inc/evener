package difftest

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

// This file is the W4 CONFORMANCE golden-replay oracle (research §W4, the
// bounded/data-independent half). Where FuzzCrossProviderDifferential generates
// a synthetic logical response and asserts the four adapters AGREE, this suite
// replays a committed corpus of REAL provider wire bytes through each provider's
// own decoder and asserts the decode still matches the committed golden. A
// divergence is one of two things: provider wire-format DRIFT (the API changed
// shape) or a decoder REGRESSION (our code changed what it produces). Either way
// it must be looked at, not silenced.
//
// It is NOT a fuzz target — it is a deterministic Test func that runs in the
// normal `make test` gate (the llm module's `go test ./...`), exactly like
// TestDifferentialSanity. The goldens are machine-regenerable with
// `-update-goldens`, mirroring appwire/golden_test.go's convention.
//
// PROVENANCE. Every fixture's raw SSE bytes are copied verbatim from an existing
// in-repo provider test (cited per fixture in conformanceFixtures). These are
// the hand-authored, real-shaped wire fragments the repo already trusts as its
// decoder fixtures; this suite has no network access and fabricates nothing. The
// `source` field names the exact test the bytes were lifted from.

// updateConformanceGoldens rewrites the conformance snapshot instead of checking
// it. Wired to `make fuzz-goldens` (the llm-module line the parent adds);
// the check side runs under plain `make test`.
var updateConformanceGoldens = flag.Bool("update-goldens", false,
	"rewrite the difftest conformance golden snapshot from the current decoders")

// conformanceGoldenDir holds the committed conformance snapshot.
const conformanceGoldenDir = "testdata/golden"

// sseFrame is one SSE event. event is empty for providers whose wire format
// emits only `data:` lines (Google Gemini, OpenAI-compatible chat completions);
// it is set for the event-named streams (Anthropic messages, OpenAI Responses).
type sseFrame struct {
	event string
	data  string
}

// sseFixture is one committed corpus item: a named blob of real provider wire
// bytes plus the adapter that must decode it. source records where the bytes
// were copied from (provenance — see the file header).
type sseFixture struct {
	name     string
	provider string
	source   string
	frames   []sseFrame
}

// raw assembles the fixture's frames into the exact SSE byte stream a provider
// server emits — `event: <e>\n` (when named) followed by `data: <d>\n\n` —
// reproducing the `write()` helper every source test uses. This is the raw wire
// input fed to the real adapter decoder.
func (f sseFixture) raw() []byte {
	var b strings.Builder
	for _, fr := range f.frames {
		if fr.event != "" {
			b.WriteString("event: " + fr.event + "\n")
		}
		b.WriteString("data: " + fr.data + "\n\n")
	}
	return []byte(b.String())
}

// conformanceRecord is one fixture's canonical decode outcome. It captures every
// deterministic field the decoder reconstructs: visible text, reasoning text,
// the normalized finish class plus its raw wire reason, the usage triple, and
// each tool call's name + canonical-JSON arguments. Non-deterministic or
// provider-identity fields (Gemini's minted ULID tool-call ids, Response.ID /
// Model, Raw payloads) are deliberately excluded so the golden regenerates
// byte-stably — the same exclusions the differential oracle's allow-list makes.
type conformanceRecord struct {
	Name         string     `json:"name"`
	Provider     string     `json:"provider"`
	Source       string     `json:"source"`
	Text         string     `json:"text,omitempty"`
	Reasoning    string     `json:"reasoning,omitempty"`
	Finish       string     `json:"finish"`
	FinishRaw    string     `json:"finish_raw,omitempty"`
	InputTokens  int        `json:"input_tokens"`
	OutputTokens int        `json:"output_tokens"`
	TotalTokens  int        `json:"total_tokens"`
	Tools        []toolProj `json:"tools,omitempty"`
}

// recordFromResponse projects a decoded response into the canonical record. It
// reuses canonJSON (the differential oracle's argument normalizer) so tool-call
// argument key order / whitespace never reads as drift.
func recordFromResponse(fix sseFixture, r *llm.Response) conformanceRecord {
	rec := conformanceRecord{
		Name:         fix.name,
		Provider:     fix.provider,
		Source:       fix.source,
		Text:         r.Text(),
		Reasoning:    r.ReasoningText(),
		Finish:       r.Finish.Reason,
		FinishRaw:    r.Finish.Raw,
		InputTokens:  r.Usage.InputTokens,
		OutputTokens: r.Usage.OutputTokens,
		TotalTokens:  r.Usage.TotalTokens,
	}
	for _, tc := range r.ToolCalls() {
		rec.Tools = append(rec.Tools, toolProj{Name: tc.Name, Args: canonJSON(tc.Arguments)})
	}
	return rec
}

// checkOrUpdateConformanceGolden compares records against
// testdata/golden/<name>.json, or rewrites it under -update-goldens. The file is
// pretty-printed so a drift diff reads line-by-line in review. Mirrors
// appwire/golden_test.go's checkOrUpdateGolden.
func checkOrUpdateConformanceGolden(t *testing.T, name string, records []conformanceRecord) {
	t.Helper()
	want, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden: %v", err)
	}
	want = append(want, '\n')
	path := filepath.Join(conformanceGoldenDir, name+".json")

	if *updateConformanceGoldens {
		if err := os.MkdirAll(conformanceGoldenDir, 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run `make fuzz-goldens` to create it)", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("conformance golden drift for %s.\n"+
			"A decoder changed what it produces for the committed real-traffic corpus, OR the\n"+
			"corpus was edited. If the change is INTENDED, run `make fuzz-goldens` and commit the\n"+
			"updated snapshot; otherwise it is provider wire-format drift or a decoder regression.\n"+
			"--- committed snapshot ---\n%s\n--- current decoders ---\n%s",
			name, got, want)
	}
}

// TestConformanceGolden replays every committed real-wire fixture through its
// provider's REAL adapter decoder (the same public Stream path the differential
// oracle drives) and snapshots the decoded result. Drift = provider wire-format
// change or decoder regression; investigate, do not weaken.
func TestConformanceGolden(t *testing.T) {
	ps, cleanup := providers()
	defer cleanup()

	driveByName := make(map[string]func([]byte) (*llm.Response, error), len(ps))
	for _, p := range ps {
		driveByName[p.name] = p.drive
	}

	records := make([]conformanceRecord, 0, len(conformanceFixtures))
	for _, fix := range conformanceFixtures {
		drive, ok := driveByName[fix.provider]
		if !ok {
			t.Fatalf("fixture %q names unknown provider %q", fix.name, fix.provider)
		}
		resp, err := drive(fix.raw())
		if err != nil {
			t.Fatalf("fixture %q (%s) failed to decode through %s: %v\n  raw=%q",
				fix.name, fix.source, fix.provider, err, fix.raw())
		}
		if resp == nil {
			t.Fatalf("fixture %q (%s) produced no response (stream never completed)\n  raw=%q",
				fix.name, fix.source, fix.raw())
		}
		records = append(records, recordFromResponse(fix, resp))
	}

	checkOrUpdateConformanceGolden(t, "conformance", records)
}

// TestConformanceFixturesWellFormed guards the corpus itself: every fixture must
// name a known provider and carry at least one frame. It keeps a malformed
// corpus from masquerading as a decode regression.
func TestConformanceFixturesWellFormed(t *testing.T) {
	known := map[string]bool{"anthropic": true, "google": true, "openai": true, "openaicompat": true}
	seen := map[string]bool{}
	for _, fix := range conformanceFixtures {
		if !known[fix.provider] {
			t.Errorf("fixture %q: unknown provider %q", fix.name, fix.provider)
		}
		if len(fix.frames) == 0 {
			t.Errorf("fixture %q: no frames", fix.name)
		}
		if fix.source == "" {
			t.Errorf("fixture %q: missing provenance (source)", fix.name)
		}
		if seen[fix.name] {
			t.Errorf("duplicate fixture name %q", fix.name)
		}
		seen[fix.name] = true
	}
}

// conformanceFixtures is the committed real-wire corpus. Every fixture's bytes
// are copied verbatim from the cited in-repo provider test (see file header).
// To add a fixture: copy real wire bytes from a provider test, cite the source,
// then run `make fuzz-goldens` to extend the snapshot.
var conformanceFixtures = []sseFixture{
	// ---- Anthropic (messages event stream) ----
	{
		name:     "anthropic_text_full",
		provider: "anthropic",
		source:   "llm/providers/anthropic/adapter_test.go TestStream_CapturesIDAndModel",
		frames: []sseFrame{
			{"message_start", `{"type":"message_start","message":{"id":"msg_123","model":"claude-3-5-sonnet-20241022","usage":{"input_tokens":10}}}`},
			{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`},
			{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`},
			{"message_stop", `{"type":"message_stop"}`},
		},
	},
	{
		name:     "anthropic_thinking_tooluse",
		provider: "anthropic",
		source:   "llm/providers/anthropic/adapter_test.go TestAdapter_Stream_TranslatesToolUseAndThinkingBlocks",
		frames: []sseFrame{
			{"content_block_start", `{"index":0,"content_block":{"type":"thinking","signature":"sig1"}}`},
			{"content_block_delta", `{"index":0,"delta":{"type":"thinking_delta","thinking":"Plan"}}`},
			{"content_block_stop", `{"index":0}`},
			{"content_block_start", `{"index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather"}}`},
			{"content_block_delta", `{"index":1,"delta":{"type":"input_json_delta","partial_json":"{\"n\":1}"}}`},
			{"content_block_stop", `{"index":1}`},
			{"message_delta", `{"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":2}}`},
			{"message_stop", `{}`},
		},
	},
	{
		name:     "anthropic_reasoning_sectionbreak",
		provider: "anthropic",
		source:   "llm/providers/anthropic/adapter_test.go TestAdapter_Stream_EmitsReasoningDeltas_SectionBreakBetweenBlocks",
		frames: []sseFrame{
			{"content_block_start", `{"index":0,"content_block":{"type":"thinking"}}`},
			{"content_block_delta", `{"index":0,"delta":{"type":"thinking_delta","thinking":"Let me "}}`},
			{"content_block_delta", `{"index":0,"delta":{"type":"thinking_delta","thinking":"think."}}`},
			{"content_block_stop", `{"index":0}`},
			{"content_block_start", `{"index":1,"content_block":{"type":"thinking"}}`},
			{"content_block_delta", `{"index":1,"delta":{"type":"thinking_delta","thinking":"Then verify."}}`},
			{"content_block_stop", `{"index":1}`},
			{"content_block_start", `{"index":2,"content_block":{"type":"text"}}`},
			{"content_block_delta", `{"index":2,"delta":{"type":"text_delta","text":"Answer"}}`},
			{"content_block_stop", `{"index":2}`},
			{"message_delta", `{"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`},
			{"message_stop", `{}`},
		},
	},

	// ---- Google Gemini (streamGenerateContent, alt=sse; data: only) ----
	{
		name:     "google_text_finish",
		provider: "google",
		source:   "llm/providers/google/adapter_test.go TestAdapter_Stream_YieldsTextDeltasAndFinish",
		frames: []sseFrame{
			{"", `{"candidates":[{"content":{"parts":[{"text":"Hel"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`},
			{"", `{"candidates":[{"content":{"parts":[{"text":"lo"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`},
		},
	},
	{
		name:     "google_functioncall",
		provider: "google",
		source:   "llm/providers/google/adapter_test.go TestAdapter_Stream_TranslatesFunctionCalls",
		frames: []sseFrame{
			{"", `{"candidates":[{"content":{"parts":[{"thoughtSignature":"sig-1","functionCall":{"name":"get_weather","args":{"n":1}}}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`},
			{"", `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`},
		},
	},
	{
		name:     "google_usage_on_finish_chunk",
		provider: "google",
		source:   "llm/providers/google/usage_finish_chunk_test.go TestStream_UsageOnFinishChunk",
		frames: []sseFrame{
			{"", `{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33}}`},
		},
	},

	// ---- OpenAI Responses API (event stream) ----
	{
		name:     "openai_text",
		provider: "openai",
		source:   "llm/providers/openai/adapter_test.go TestAdapter_Stream_YieldsTextDeltasAndFinish",
		frames: []sseFrame{
			{"response.output_text.delta", `{"type":"response.output_text.delta","delta":"Hel"}`},
			{"response.output_text.delta", `{"type":"response.output_text.delta","delta":"lo"}`},
			{"response.completed", `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.2","output":[{"type":"message","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`},
		},
	},
	{
		name:     "openai_reasoning_summary",
		provider: "openai",
		source:   "llm/providers/openai/adapter_test.go TestAdapter_Stream_EmitsReasoningSummaryDeltas",
		frames: []sseFrame{
			{"response.reasoning_summary_part.added", `{"type":"response.reasoning_summary_part.added","summary_index":0}`},
			{"response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","delta":"Let me "}`},
			{"response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","delta":"think."}`},
			{"response.reasoning_summary_part.added", `{"type":"response.reasoning_summary_part.added","summary_index":1}`},
			{"response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","delta":"Then verify."}`},
			{"response.output_text.delta", `{"type":"response.output_text.delta","delta":"Answer"}`},
			{"response.completed", `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.5","output":[{"type":"message","content":[{"type":"output_text","text":"Answer"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`},
		},
	},
	{
		name:     "openai_toolcall",
		provider: "openai",
		source:   "llm/providers/openai/adapter_test.go TestAdapter_Stream_TranslatesToolCalls",
		frames: []sseFrame{
			{"response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","call_id":"call_1","name":"get_weather","delta":"{\"n\":1}"}`},
			{"response.output_item.done", `{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"n\":1}"}}`},
			{"response.completed", `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.2","output":[{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"n\":1}"}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`},
		},
	},

	// ---- OpenAI-compatible Chat Completions (data: only, [DONE]-terminated) ----
	{
		name:     "openaicompat_text",
		provider: "openaicompat",
		source:   "llm/providers/openaicompat/adapter_test.go TestAdapter_Stream_YieldsTextDeltasAndFinish",
		frames: []sseFrame{
			{"", `{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`},
			{"", `{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]}`},
			{"", `{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}`},
			{"", `{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`},
			{"", "[DONE]"},
		},
	},
	{
		name:     "openaicompat_toolcall",
		provider: "openaicompat",
		source:   "llm/providers/openaicompat/adapter_test.go TestAdapter_Stream_ToolCalls",
		frames: []sseFrame{
			{"", `{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}`},
			{"", `{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"ci"}}]},"finish_reason":null}]}`},
			{"", `{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ty\":\"SF\"}"}}]},"finish_reason":null}]}`},
			{"", `{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`},
			{"", "[DONE]"},
		},
	},
}
