package anthropic

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

// This file (anti-collision token: areq_) adds four fuzz targets that harden the
// Anthropic request/response path without touching any existing test file:
//
//   - FuzzAreqToAnthropicMessages  — the unified-Message -> Anthropic-wire
//     translation (request.go toAnthropicMessages).
//   - FuzzAreqClampEffort          — reasoning-effort clamping (response.go).
//   - FuzzAreqParseUsage           — usage token accounting (response.go).
//   - FuzzAreqDecodeStreamGrammar  — grammar-driven adversarial SSE that reaches
//     decodeStream's deeper assembly branches (adapter.go).
//
// Every top-level identifier is prefixed `areq`/`Areq` so parallel lanes editing
// package anthropic cannot collide. The stream target deliberately reuses the
// existing package helpers accumulateAnthropicSSE / sameAnthropicResponse rather
// than redeclaring them.

// ---------------------------------------------------------------------------
// toAnthropicMessages
// ---------------------------------------------------------------------------

// areqPart builds one fuzzed content part. kindSel selects the content shape,
// routing the fuzzer's bytes into the bug-prone spots the translator handles
// (tool-call argument JSON, image sources, thinking, web-search raw payloads,
// tool results). imgPath is a real file inside the test sandbox; imgPath+".gone"
// is a guaranteed-missing sibling used to exercise the os.ReadFile error branch.
func areqPart(kindSel byte, s1, s2 string, blob []byte, imgPath string) (llm.ContentPart, bool) {
	switch kindSel % 22 {
	case 0:
		return llm.ContentPart{Kind: llm.ContentText, Text: s1}, true
	case 1:
		return llm.ContentPart{Kind: llm.ContentImage, Image: &llm.ImageData{Data: blob, MediaType: "image/png"}}, true
	case 2:
		return llm.ContentPart{Kind: llm.ContentImage, Image: &llm.ImageData{URL: imgPath}}, true
	case 3:
		return llm.ContentPart{Kind: llm.ContentImage, Image: &llm.ImageData{URL: imgPath + ".gone"}}, true
	case 4:
		return llm.ContentPart{Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://example.test/pic.png"}}, true
	case 5:
		return llm.ContentPart{Kind: llm.ContentImage, Image: nil}, true
	case 6:
		return llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: s1, Name: s2, Arguments: json.RawMessage(blob), Type: "function"}}, true
	case 7:
		return llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: nil}, true
	case 8:
		return llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: s2, Signature: s1}}, true
	case 9:
		return llm.ContentPart{Kind: llm.ContentThinking, Thinking: nil}, true
	case 10:
		return llm.ContentPart{Kind: llm.ContentRedThinking, Thinking: &llm.ThinkingData{Text: s2, Redacted: true}}, true
	case 11:
		return llm.ContentPart{Kind: llm.ContentRedThinking, Thinking: nil}, true
	case 12:
		return llm.ContentPart{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Raw: json.RawMessage(blob)}}, true
	case 13:
		return llm.ContentPart{Kind: llm.ContentWebSearch, WebSearch: nil}, true
	case 14:
		return llm.ContentPart{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Raw: nil}}, true
	case 15:
		return llm.ContentPart{Kind: llm.ContentAudio, Audio: &llm.AudioData{Data: blob}}, true
	case 16:
		return llm.ContentPart{Kind: llm.ContentDocument, Document: &llm.DocumentData{Data: blob}}, true
	case 17:
		return llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: s1, Content: s2}}, true
	case 18:
		return llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: s1, Content: s2, ImageData: blob, ImageMediaType: "image/png"}}, true
	case 19:
		return llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: s1, Content: s2, IsError: true}}, true
	case 20:
		return llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: s1, Content: map[string]any{"k": s2}}}, true
	default:
		// An empty / unknown-kind part exercises the per-role default (ignore).
		return llm.ContentPart{Kind: llm.ContentKind(s2)}, true
	}
}

// areqRole maps the low bits of a script byte to a role, biased toward user and
// assistant (which carry the interesting content) while still reaching system,
// developer, tool, and an unknown role.
func areqRole(sel byte) llm.Role {
	switch sel % 8 {
	case 0:
		return llm.RoleSystem
	case 1:
		return llm.RoleDeveloper
	case 2, 6:
		return llm.RoleUser
	case 3, 7:
		return llm.RoleAssistant
	case 4:
		return llm.RoleTool
	default:
		return llm.Role("intern") // unknown role -> default ignore branch
	}
}

// areqMessages deterministically constructs a message slice from fuzz primitives.
// Consecutive script bytes that decode to the same role produce adjacent
// same-role messages, exercising the alternation-merge path in toAnthropicMessages.
func areqMessages(script []byte, s1, s2 string, blob []byte, imgPath string) []llm.Message {
	msgs := make([]llm.Message, 0, len(script))
	for _, b := range script {
		part, _ := areqPart(b>>3, s1, s2, blob, imgPath)
		msgs = append(msgs, llm.Message{Role: areqRole(b), Content: []llm.ContentPart{part}})
	}
	return msgs
}

// FuzzAreqToAnthropicMessages drives the unified-Message -> Anthropic-wire
// translation. Oracles:
//   - never panics (floor);
//   - determinism: translating the same messages twice yields an identical
//     (error, system, messages) triple;
//   - request-shape contract on success: every emitted message has role
//     user|assistant, carries a non-empty []map[string]any content, no two
//     adjacent messages share a role (Anthropic's alternation requirement), and
//     the whole system+messages structure marshals to valid JSON.
func FuzzAreqToAnthropicMessages(f *testing.F) {
	f.Add([]byte{0x00, 0x10, 0x18}, "call_1", "hi", []byte(`{"a":1}`))
	f.Add([]byte{0x02, 0x02, 0x33, 0x3b}, "", "think", []byte(`{}`))
	f.Add([]byte{0x7a, 0x7b, 0x8a, 0x9a}, "id", "s", []byte(`not json`))
	f.Add([]byte{0x1c, 0x24, 0x84, 0x8c, 0x94}, "sys", "", []byte(``))
	f.Add([]byte{0x14, 0x1d, 0x22}, "x", "y", []byte(`{"deep":{"x":[1,2,3]}}`))
	// Deep image / nil / web-search branches (bytes chosen so role|kind land on the
	// specific user/assistant content shapes): local-file image success, image nil,
	// image-data, tool-call nil, thinking nil, redacted-thinking nil, web-search
	// nil and empty-raw.
	f.Add([]byte{0x00, 0x12, 0x2a, 0x0b, 0x13, 0x3b, 0x4b, 0x5b, 0x6b, 0x73}, "id", "s", []byte(`{}`))
	// Error branches (each short-circuits, so kept in its own seed): local-file
	// image read failure (user/assistant) and unsupported audio/document parts.
	f.Add([]byte{0x1a}, "id", "s", []byte(``))
	f.Add([]byte{0x1b}, "id", "s", []byte(``))
	f.Add([]byte{0x7b}, "id", "s", []byte(``))
	f.Add([]byte{0x83}, "id", "s", []byte(``))

	// One real file inside the test sandbox lets the local-path image branch read
	// bytes without touching anything outside the sandbox; its ".gone" sibling is
	// never created, exercising the os.ReadFile error path.
	dir := f.TempDir()
	imgPath := filepath.Join(dir, "img.png")
	if err := os.WriteFile(imgPath, []byte("\x89PNG\r\n\x1a\nfuzzbytes"), 0o600); err != nil {
		f.Fatalf("seed image write: %v", err)
	}

	f.Fuzz(func(t *testing.T, script []byte, s1, s2 string, blob []byte) {
		msgs := areqMessages(script, s1, s2, blob, imgPath)

		sys1, out1, err1 := toAnthropicMessages(msgs)
		sys2, out2, err2 := toAnthropicMessages(areqMessages(script, s1, s2, blob, imgPath))

		// Determinism.
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("nondeterministic error: %v vs %v", err1, err2)
		}
		if err1 == nil {
			if sys1 != sys2 {
				t.Fatalf("nondeterministic system:\n a=%q\n b=%q", sys1, sys2)
			}
			if !reflect.DeepEqual(out1, out2) {
				t.Fatalf("nondeterministic messages:\n a=%#v\n b=%#v", out1, out2)
			}
		}
		if err1 != nil {
			return // a structured translation error (unsupported audio/document) is acceptable.
		}

		// Request-shape contract.
		prevRole := ""
		for i, m := range out1 {
			role, _ := m["role"].(string)
			if role != "user" && role != "assistant" {
				t.Fatalf("message %d has non-Anthropic role %q\nmsgs=%#v", i, role, out1)
			}
			if role == prevRole {
				t.Fatalf("adjacent messages %d/%d share role %q (alternation violated)\nmsgs=%#v", i-1, i, role, out1)
			}
			prevRole = role
			content, ok := m["content"].([]map[string]any)
			if !ok || len(content) == 0 {
				t.Fatalf("message %d has empty/mis-typed content %#v", i, m["content"])
			}
		}
		if _, err := json.Marshal(map[string]any{"system": sys1, "messages": out1}); err != nil {
			t.Fatalf("translated request does not marshal to JSON: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// clampEffort
// ---------------------------------------------------------------------------

var areqHierarchy = []string{"low", "medium", "high", "max"}

func areqHierIdx(s string) int {
	for i, h := range areqHierarchy {
		if h == s {
			return i
		}
	}
	return -1
}

// areqEffortLevels builds a supported-levels slice from fuzz bytes, mixing real
// hierarchy levels, blanks, and arbitrary junk so the clamp sees both known and
// unknown supported sets.
func areqEffortLevels(sel []byte, requested string) []string {
	if len(sel) == 0 {
		return nil
	}
	choices := []string{"low", "medium", "high", "max", "LOW", "High", "", "weird", requested}
	out := make([]string, 0, len(sel))
	for _, b := range sel {
		out = append(out, choices[int(b)%len(choices)])
	}
	return out
}

// FuzzAreqClampEffort drives reasoning-effort clamping. Oracles:
//   - never panics (floor);
//   - determinism;
//   - idempotence: clampEffort(clampEffort(x)) == clampEffort(x);
//   - empty supported set is a pass-through (returns the raw requested string);
//   - a directly-supported request is returned unchanged (normalized);
//   - membership: when the request is a known level and at least one supported
//     level is a known level, the result is itself one of the supported levels.
func FuzzAreqClampEffort(f *testing.F) {
	f.Add("high", []byte{0, 1})
	f.Add("  MAX ", []byte{0, 1, 2})
	f.Add("low", []byte{2, 3})
	f.Add("bogus", []byte{6, 7})
	f.Add("medium", []byte(nil))

	f.Fuzz(func(t *testing.T, requested string, sel []byte) {
		levels := areqEffortLevels(sel, requested)

		got := clampEffort(requested, levels)
		if got2 := clampEffort(requested, levels); got != got2 {
			t.Fatalf("nondeterministic clampEffort(%q,%v): %q vs %q", requested, levels, got, got2)
		}
		if reGot := clampEffort(got, levels); reGot != got {
			t.Fatalf("clampEffort not idempotent: clamp(%q)=%q, clamp(clamp)=%q (levels=%v)", requested, got, reGot, levels)
		}

		if len(levels) == 0 {
			if got != requested {
				t.Fatalf("empty supported levels must pass through: got %q want %q", got, requested)
			}
			return
		}

		norm := strings.ToLower(strings.TrimSpace(requested))
		// This oracle deliberately uses a narrower notion of "known"/"directly
		// supported" than clampEffort itself: it lowercases (but does not trim)
		// each supported level and only recognizes the low/medium/high/max
		// hierarchy, whereas clampEffort delegates to llm.ClampReasoningEffort,
		// which trims and also recognizes minimal/xhigh. That makes this check a
		// conservative under-approximation — it can only skip an assertion it
		// isn't sure about, never wrongly demand one — so it stays valid even
		// though it no longer mirrors clampEffort's comparisons exactly.
		supportedHasKnown := false
		directlySupported := false
		for _, l := range levels {
			if areqHierIdx(strings.ToLower(l)) >= 0 {
				supportedHasKnown = true
			}
			if strings.EqualFold(l, norm) {
				directlySupported = true
			}
		}
		if directlySupported && got != norm {
			t.Fatalf("directly-supported request %q changed to %q (levels=%v)", norm, got, levels)
		}
		if areqHierIdx(norm) >= 0 && supportedHasKnown {
			found := false
			for _, l := range levels {
				if strings.EqualFold(l, got) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("clamp result %q is not a supported level (requested=%q, levels=%v)", got, requested, levels)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// parseUsage
// ---------------------------------------------------------------------------

// areqNum encodes v in one of several JSON-decoded numeric shapes (json.Number,
// float64, int) plus a non-numeric poison value, matching what IntFromAny must
// tolerate off the wire.
func areqNum(v int, enc byte) any {
	switch enc % 4 {
	case 0:
		return json.Number(strconv.Itoa(v))
	case 1:
		return float64(v)
	case 2:
		return v
	default:
		return "not-a-number" // IntFromAny -> 0
	}
}

// FuzzAreqParseUsage drives the usage token accounting. Oracles:
//   - never panics (floor);
//   - determinism;
//   - preserved input/output counts: InputTokens/OutputTokens equal IntFromAny of
//     the raw fields;
//   - total invariant: TotalTokens == InputTokens + OutputTokens + every non-nil
//     cache component (the core preserved-count contract);
//   - presence/selection: cache pointers are set exactly when their source key is
//     present, and the cache_creation breakdown takes precedence over the flat
//     cache_creation_input_tokens fallback.
func FuzzAreqParseUsage(f *testing.F) {
	f.Add(10, 5, 3, 7, 2, byte(0), byte(0))
	f.Add(0, 0, 0, 0, 0, byte(1), byte(3))
	f.Add(-4, 1000000, 9, 0, 11, byte(2), byte(2))
	f.Add(1, 1, 1, 1, 1, byte(3), byte(1))

	f.Fuzz(func(t *testing.T, in, out, read, w5m, w1h int, enc, presence byte) {
		u := map[string]any{
			"input_tokens":  areqNum(in, enc),
			"output_tokens": areqNum(out, enc>>2),
		}
		hasRead := presence&1 == 1
		hasBreakdown := presence&2 == 2
		hasFlat := presence&4 == 4
		has5m := presence&8 == 8
		has1h := presence&16 == 16

		if hasRead {
			u["cache_read_input_tokens"] = areqNum(read, enc)
		}
		if hasBreakdown {
			bd := map[string]any{}
			if has5m {
				bd["ephemeral_5m_input_tokens"] = areqNum(w5m, enc)
			}
			if has1h {
				bd["ephemeral_1h_input_tokens"] = areqNum(w1h, enc>>1)
			}
			u["cache_creation"] = bd
		}
		if hasFlat {
			u["cache_creation_input_tokens"] = areqNum(w5m, enc>>3)
		}

		usage := parseUsage(u)
		if !reflect.DeepEqual(usage, parseUsage(u)) {
			t.Fatalf("nondeterministic parseUsage for %#v", u)
		}

		if usage.InputTokens != llm.IntFromAny(u["input_tokens"]) {
			t.Fatalf("InputTokens=%d not preserved from %v", usage.InputTokens, u["input_tokens"])
		}
		if usage.OutputTokens != llm.IntFromAny(u["output_tokens"]) {
			t.Fatalf("OutputTokens=%d not preserved from %v", usage.OutputTokens, u["output_tokens"])
		}

		want := usage.InputTokens + usage.OutputTokens
		if usage.CacheReadTokens != nil {
			want += *usage.CacheReadTokens
		}
		if usage.CacheWriteTokens != nil {
			want += *usage.CacheWriteTokens
		}
		if usage.CacheWrite1hTokens != nil {
			want += *usage.CacheWrite1hTokens
		}
		if usage.TotalTokens != want {
			t.Fatalf("TotalTokens=%d != sum of components %d (usage=%+v)", usage.TotalTokens, want, usage)
		}

		// Presence / selection contract.
		if (usage.CacheReadTokens != nil) != hasRead {
			t.Fatalf("CacheReadTokens presence=%v want %v", usage.CacheReadTokens != nil, hasRead)
		}
		want5mSet := (hasBreakdown && has5m) || (!hasBreakdown && hasFlat)
		if (usage.CacheWriteTokens != nil) != want5mSet {
			t.Fatalf("CacheWriteTokens presence=%v want %v (breakdown=%v flat=%v has5m=%v)", usage.CacheWriteTokens != nil, want5mSet, hasBreakdown, hasFlat, has5m)
		}
		want1hSet := hasBreakdown && has1h
		if (usage.CacheWrite1hTokens != nil) != want1hSet {
			t.Fatalf("CacheWrite1hTokens presence=%v want %v", usage.CacheWrite1hTokens != nil, want1hSet)
		}
	})
}

// ---------------------------------------------------------------------------
// decodeStream — grammar-driven adversarial SSE
// ---------------------------------------------------------------------------

// areqEvent renders one Anthropic SSE frame chosen by op. Producing well-formed
// frames (rather than random bytes) lets the fuzzer reliably reach the deeper
// assembly branches: deltas that arrive before their content_block_start, stops
// for never-started blocks, empty-argument tool calls, server_tool_use /
// web_search_tool_result assembly, index gaps, and multi-thinking section breaks.
func areqEvent(op byte, idx int, s1, s2 string, blob []byte) string {
	esc := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	frame := func(event, data string) string {
		return "event: " + event + "\ndata: " + data + "\n\n"
	}
	switch op % 18 {
	case 0:
		return frame("message_start", `{"type":"message_start","message":{"id":`+esc(s1)+`,"model":`+esc(s2)+`,"usage":{"input_tokens":7,"output_tokens":0}}}`)
	case 1:
		return frame("content_block_start", `{"type":"content_block_start","index":`+esc(idx)+`,"content_block":{"type":"text","text":""}}`)
	case 2:
		return frame("content_block_start", `{"type":"content_block_start","index":`+esc(idx)+`,"content_block":{"type":"tool_use","id":`+esc(s1)+`,"name":`+esc(s2)+`,"input":{}}}`)
	case 3:
		return frame("content_block_start", `{"type":"content_block_start","index":`+esc(idx)+`,"content_block":{"type":"thinking","thinking":`+esc(s1)+`,"signature":`+esc(s2)+`}}`)
	case 4:
		return frame("content_block_start", `{"type":"content_block_start","index":`+esc(idx)+`,"content_block":{"type":"redacted_thinking","data":`+esc(s1)+`}}`)
	case 5:
		return frame("content_block_start", `{"type":"content_block_start","index":`+esc(idx)+`,"content_block":{"type":"server_tool_use","id":`+esc(s1)+`,"name":"web_search","input":{"query":`+esc(s2)+`}}}`)
	case 6:
		return frame("content_block_start", `{"type":"content_block_start","index":`+esc(idx)+`,"content_block":{"type":"web_search_tool_result","content":[{"title":`+esc(s2)+`}]}}`)
	case 7:
		return frame("content_block_delta", `{"type":"content_block_delta","index":`+esc(idx)+`,"delta":{"type":"text_delta","text":`+esc(s1)+`}}`)
	case 8:
		return frame("content_block_delta", `{"type":"content_block_delta","index":`+esc(idx)+`,"delta":{"type":"input_json_delta","partial_json":`+esc(string(blob))+`}}`)
	case 9:
		return frame("content_block_delta", `{"type":"content_block_delta","index":`+esc(idx)+`,"delta":{"type":"thinking_delta","thinking":`+esc(s2)+`}}`)
	case 10:
		return frame("content_block_delta", `{"type":"content_block_delta","index":`+esc(idx)+`,"delta":{"type":"signature_delta","signature":`+esc(s1)+`}}`)
	case 11:
		return frame("content_block_stop", `{"type":"content_block_stop","index":`+esc(idx)+`}`)
	case 12:
		return frame("message_delta", `{"type":"message_delta","delta":{"stop_reason":`+esc(s1)+`},"usage":{"input_tokens":3,"output_tokens":9}}`)
	case 13:
		return frame("ping", `{"type":"ping"}`)
	case 14:
		return frame("message_stop", `{"type":"message_stop"}`)
	case 15:
		return frame("content_block_start", `{"type":"content_block_start","index":`+esc(idx)+`,"content_block":{"type":`+esc(s2)+`}}`)
	case 16:
		return ": interstitial comment\n\n"
	default:
		return frame(s2, esc(map[string]any{"type": s2, "index": idx}))
	}
}

// areqBuildSSE turns a script into a well-formed-ish SSE byte stream and always
// terminates it with a message_stop so the assembly path in decodeStream runs.
func areqBuildSSE(script []byte, s1, s2 string, blob []byte) []byte {
	var sb strings.Builder
	for i, b := range script {
		idx := int((b >> 5) & 0x3)
		if i%3 == 0 {
			idx += 4 // create index gaps so blocks[i] == nil is reached at assembly
		}
		sb.WriteString(areqEvent(b, idx, s1, s2, blob))
	}
	sb.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	return []byte(sb.String())
}

// FuzzAreqDecodeStreamGrammar drives decodeStream with grammar-generated
// adversarial SSE. Oracles (reusing the package's own accumulate/compare helpers):
//   - never panics and always terminates (floor);
//   - determinism: the same stream accumulates to an identical llm.Response;
//   - read-boundary invariance (metamorphic): delivering the exact same bytes one
//     at a time must not change the accumulated response.
func FuzzAreqDecodeStreamGrammar(f *testing.F) {
	f.Add([]byte{0, 2, 8, 11, 12}, "toolu_1", "shell", []byte(`{"cmd":"ls"}`))
	f.Add([]byte{0, 3, 9, 10, 11, 3, 9, 11}, "msg_1", "claude", []byte(`x`))
	f.Add([]byte{7, 8, 9, 11}, "id", "text", []byte(``))
	f.Add([]byte{5, 6, 11, 11}, "srv_1", "query text", []byte(`{}`))
	f.Add([]byte{2, 11}, "toolu_2", "noargs", []byte(``))

	a := &Adapter{BaseURL: "http://areq.local"}

	f.Fuzz(func(t *testing.T, script []byte, s1, s2 string, blob []byte) {
		sse := areqBuildSSE(script, s1, s2, blob)

		base, baseErr := accumulateAnthropicSSE(a, sse, false)
		again, againErr := accumulateAnthropicSSE(a, sse, false)
		if !sameAnthropicResponse(base, baseErr, again, againErr) {
			t.Fatalf("nondeterministic decode:\n a=%+v (err=%v)\n b=%+v (err=%v)\n sse=%q", base, baseErr, again, againErr, sse)
		}

		rechunked, reErr := accumulateAnthropicSSE(a, sse, true)
		if !sameAnthropicResponse(base, baseErr, rechunked, reErr) {
			t.Fatalf("re-chunk boundary changed accumulated response:\n whole=%+v (err=%v)\n one-byte=%+v (err=%v)\n sse=%q", base, baseErr, rechunked, reErr, sse)
		}
	})
}
