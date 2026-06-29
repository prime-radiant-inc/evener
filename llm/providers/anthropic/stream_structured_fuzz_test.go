package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
)

// anthGen turns a fuzz input into a deterministic stream of small choices and
// accumulates a valid Anthropic messages SSE event sequence. It is the inverse
// of decodeStream: rather than emitting the minimal wire bytes (as the difftest
// encoder does), it walks the decoder's whole event vocabulary — message_start
// with usage, an arbitrary run of content blocks (text, thinking,
// redacted_thinking, tool_use with input_json_delta split across frames,
// server_tool_use, web_search_tool_result), interleaved so the reasoning
// section-break path fires, signature deltas, a message_delta whose stop_reason
// nests under "delta" (end_turn / max_tokens / stop_sequence / tool_use) plus
// usage, and message_stop — so the metamorphic oracle explores the STRUCTURED
// stream space instead of bytes that almost always die at the first SSE/JSON
// parse.
//
// Determinism: every choice comes from the byte source (no maps for ORDER, no
// rand, no time). JSON is built with json.Marshal over map[string]any, which
// sorts object keys, so the same input always yields byte-identical SSE.
type anthGen struct {
	b   []byte
	i   int
	out bytes.Buffer
}

func (g *anthGen) next() byte {
	if g.i >= len(g.b) {
		return 0
	}
	v := g.b[g.i]
	g.i++
	return v
}

// intn returns a value in [0,n). n<=0 yields 0.
func (g *anthGen) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(g.next()) % n
}

func (g *anthGen) boolean() bool { return g.next()%2 == 0 }

// anthAlphabet includes a space, a double-quote and a newline so generated
// strings exercise the decoder's JSON unescaping on the way back in.
var anthAlphabet = []rune{'a', 'b', 'c', ' ', '"', '\n'}

func (g *anthGen) text(maxLen int) string {
	n := g.intn(maxLen + 1)
	r := make([]rune, n)
	for i := range r {
		r[i] = anthAlphabet[g.intn(len(anthAlphabet))]
	}
	return string(r)
}

// name returns a short non-empty lowercase tool name.
func (g *anthGen) name() string {
	n := 1 + g.intn(4)
	r := make([]byte, n)
	for i := range r {
		r[i] = byte('a' + g.intn(6))
	}
	return string(r)
}

// frame appends one `event: <event>\ndata: <json>\n\n` SSE frame. The Anthropic
// decoder switches on the SSE event name, so every frame carries one. Marshalling
// a map guarantees valid JSON and deterministic key order.
func (g *anthGen) frame(event string, payload map[string]any) {
	data, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("structured anthropic gen: marshal frame: %v", err))
	}
	g.out.WriteString("event: ")
	g.out.WriteString(event)
	g.out.WriteString("\ndata: ")
	g.out.Write(data)
	g.out.WriteString("\n\n")
}

// split breaks s into 1..3 non-empty pieces at source-chosen boundaries so tool
// arguments arrive across multiple input_json_delta frames (the real wire shape).
func (g *anthGen) split(s string) []string {
	if s == "" {
		return []string{""}
	}
	parts := 1 + g.intn(3)
	if parts > len(s) {
		parts = len(s)
	}
	out := make([]string, 0, parts)
	rem := s
	for p := 0; p < parts-1 && len(rem) > 1; p++ {
		cut := 1 + g.intn(len(rem)-1)
		out = append(out, rem[:cut])
		rem = rem[cut:]
	}
	out = append(out, rem)
	return out
}

// argsJSON builds a small, valid JSON object string for a tool call.
func (g *anthGen) argsJSON() string {
	obj := map[string]any{}
	for k := 0; k <= g.intn(3); k++ {
		obj[fmt.Sprintf("k%d", k)] = g.text(4)
	}
	b, err := json.Marshal(obj)
	if err != nil {
		panic(fmt.Sprintf("structured anthropic gen: marshal args: %v", err))
	}
	return string(b)
}

// blockKinds is the content-block vocabulary the decoder understands. The order
// is fixed so a given byte always selects the same kind (determinism).
var blockKinds = []string{"text", "thinking", "redacted_thinking", "tool_use", "server_tool_use", "web_search_tool_result"}

// emitTextBlock streams a text content block: start, 0..K text deltas (some
// empty, exercising the early-return guard), stop.
func (g *anthGen) emitTextBlock(idx int) {
	g.frame("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         idx,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
	for d := g.intn(4); d > 0; d-- {
		g.frame("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": idx,
			"delta": map[string]any{"type": "text_delta", "text": g.text(5)},
		})
	}
	g.frame("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
}

// emitThinkingBlock streams a thinking (or redacted_thinking) block: start
// (sometimes with initial inline thinking), 0..K thinking deltas, an optional
// signature delta, stop.
func (g *anthGen) emitThinkingBlock(idx int, redacted bool) {
	typ := "thinking"
	cb := map[string]any{"type": typ, "thinking": ""}
	if redacted {
		typ = "redacted_thinking"
		cb = map[string]any{"type": typ, "data": ""}
	}
	if g.boolean() { // sometimes seed initial thinking in the start block
		cb["thinking"] = g.text(4)
	}
	g.frame("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         idx,
		"content_block": cb,
	})
	for d := g.intn(4); d > 0; d-- {
		g.frame("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": idx,
			"delta": map[string]any{"type": "thinking_delta", "thinking": g.text(5)},
		})
	}
	if g.boolean() {
		g.frame("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": idx,
			"delta": map[string]any{"type": "signature_delta", "signature": g.text(4)},
		})
	}
	g.frame("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
}

// emitToolBlock streams a tool_use (or server_tool_use) block: start with id +
// name, input_json_delta frames carrying the arguments split across the wire, an
// optional final stop. The toolID is occasionally blank so the decoder's
// id-guarded start/delta/stop branches are exercised.
func (g *anthGen) emitToolBlock(idx int, server bool) {
	typ := "tool_use"
	if server {
		typ = "server_tool_use"
	}
	toolID := fmt.Sprintf("toolu_%d", idx)
	if g.intn(4) == 0 { // sometimes a malformed start with no id
		toolID = ""
	}
	args := g.argsJSON()
	cb := map[string]any{"type": typ, "id": toolID, "name": "t" + g.name(), "input": map[string]any{}}
	g.frame("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         idx,
		"content_block": cb,
	})
	for _, chunk := range g.split(args) {
		if chunk == "" {
			continue
		}
		g.frame("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": idx,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": chunk},
		})
	}
	g.frame("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
}

// emitWebSearchResult streams a web_search_tool_result block: a single start
// frame carrying a raw result payload, then stop. The decoder stores the raw
// start and surfaces it as a WebSearch part at message_stop.
func (g *anthGen) emitWebSearchResult(idx int) {
	g.frame("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": idx,
		"content_block": map[string]any{
			"type":        "web_search_tool_result",
			"tool_use_id": fmt.Sprintf("srvtoolu_%d", idx),
			"content":     []any{map[string]any{"type": "web_search_result", "title": g.text(4)}},
		},
	})
	g.frame("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
}

// structuredAnthropicSSE builds a valid-but-adversarial Anthropic messages SSE
// event sequence from fuzz bytes. The returned bytes are always parseable SSE
// whose frames carry well-formed JSON, so the decoder reaches its content,
// tool-mapping, reasoning, and finish logic instead of the malformed-JSON
// passthrough.
func structuredAnthropicSSE(raw []byte) []byte {
	g := &anthGen{b: raw}

	in := g.intn(120)
	g.frame("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":    "msg_struct",
			"model": "fuzz-model",
			"usage": map[string]any{"input_tokens": in, "output_tokens": 0},
		},
	})

	sawTool := false
	nBlocks := g.intn(5)
	for idx := 0; idx < nBlocks; idx++ {
		switch blockKinds[g.intn(len(blockKinds))] {
		case "text":
			g.emitTextBlock(idx)
		case "thinking":
			g.emitThinkingBlock(idx, false)
		case "redacted_thinking":
			g.emitThinkingBlock(idx, true)
		case "tool_use":
			g.emitToolBlock(idx, false)
			sawTool = true
		case "server_tool_use":
			g.emitToolBlock(idx, true)
		case "web_search_tool_result":
			g.emitWebSearchResult(idx)
		}
	}

	// message_delta: stop_reason nests under "delta" (the bug-fixed path) plus a
	// usage update. Sometimes omit it entirely so finish defaults to "stop".
	if g.boolean() {
		stop := "end_turn"
		switch g.intn(4) {
		case 1:
			stop = "max_tokens"
		case 2:
			stop = "stop_sequence"
		case 3:
			stop = "tool_use"
		}
		if sawTool {
			stop = "tool_use"
		}
		g.frame("message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": stop},
			"usage": map[string]any{"output_tokens": g.intn(120)},
		})
	}

	// Finish: usually message_stop (a completed stream); occasionally truncate so
	// the metamorphic oracle holds on a mid-stream cutoff too. The truncate token
	// is a live nonzero value so an exhausted byte source (which reads back zeros)
	// produces a completed stream rather than always truncating.
	if g.next()%8 != 7 {
		g.frame("message_stop", map[string]any{"type": "message_stop"})
	}

	return g.out.Bytes()
}

// FuzzAnthropicStreamStructured drives the REAL Anthropic event-stream decoder
// with structure-aware, valid-but-adversarial event sequences and asserts the
// SAME metamorphic property as FuzzAnthropicStreamMetamorphic: a
// semantics-preserving transform (re-chunking, interstitial SSE comments) must
// not change the accumulated llm.Response. Where the raw-byte metamorphic target
// mostly dies at the first parse, this target reaches the decoder's
// content/tool/reasoning/finish logic on nearly every input (see
// TestStructuredAnthropicReachesDeeper). A divergence here is a real decoder bug
// (state that leaks across read boundaries or mishandles benign framing), not a
// test artifact. This decoder backs anthropic, kimi-anthropic, minimax, and
// openrouter-anthropic.
func FuzzAnthropicStreamStructured(f *testing.F) {
	seeds := [][]byte{
		{},
		{0x00},
		{0x00, 0x01, 0x02, 0x03, 0x04, 0x05},
		{0x01, 0x03, 0x61, 0x62, 0x63, 0x64}, // one block, tool args split
		{0x02, 0x01, 0x03, 0x61, 0x62, 0x07, 0x09, 0x0a, 0x0b}, // text + thinking
		{0x03, 0x01, 0x02, 0x03, 0x10, 0x20, 0x30, 0x40, 0x50}, // mixed block kinds
		{0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8},       // truncated finish
	}
	for _, s := range seeds {
		f.Add(s)
	}

	a := &Adapter{BaseURL: "http://fuzz.local"}

	f.Fuzz(func(t *testing.T, raw []byte) {
		sse := structuredAnthropicSSE(raw)

		base, baseErr := accumulateAnthropicSSE(a, sse, false) // Oracle (floor): never panics.

		rechunked, reErr := accumulateAnthropicSSE(a, sse, true)
		if !sameAnthropicResponse(base, baseErr, rechunked, reErr) {
			t.Fatalf("re-chunk boundary changed the accumulated response:\n base=%+v (err=%v)\n one-byte=%+v (err=%v)\n sse=%q",
				base, baseErr, rechunked, reErr, sse)
		}

		commented := bytes.ReplaceAll(sse, []byte("\n\n"), []byte("\n\n: fuzz-keepalive\n\n"))
		withComments, cErr := accumulateAnthropicSSE(a, commented, false)
		if !sameAnthropicResponse(base, baseErr, withComments, cErr) {
			t.Fatalf("interstitial SSE comments changed the accumulated response:\n base=%+v (err=%v)\n commented=%+v (err=%v)\n sse=%q",
				base, baseErr, withComments, cErr, sse)
		}
	})
}

// TestStructuredAnthropicReachesDeeper is the evidence (and a regression guard)
// that the structure-aware generator explores deeper decoder states than feeding
// the same bytes raw. Over a fixed-seed Monte Carlo, it measures the fraction of
// inputs whose stream reaches message_stop (a non-nil accumulated Response). Raw
// bytes interpreted as SSE essentially never form a valid completed stream; the
// structured generator does so on most inputs.
func TestStructuredAnthropicReachesDeeper(t *testing.T) {
	a := &Adapter{BaseURL: "http://fuzz.local"}
	const iters = 2000

	rng := rand.New(rand.NewSource(1)) //nolint:gosec // deterministic test fixture, not security
	var rawCompleted, structCompleted int
	for n := 0; n < iters; n++ {
		raw := make([]byte, rng.Intn(64))
		_, _ = rng.Read(raw)

		if resp, _ := accumulateAnthropicSSE(a, raw, false); resp != nil {
			rawCompleted++
		}
		if resp, _ := accumulateAnthropicSSE(a, structuredAnthropicSSE(raw), false); resp != nil {
			structCompleted++
		}
	}

	rawRate := float64(rawCompleted) / iters
	structRate := float64(structCompleted) / iters
	t.Logf("reached message_stop: raw=%.1f%% (%d/%d)  structured=%.1f%% (%d/%d)",
		rawRate*100, rawCompleted, iters, structRate*100, structCompleted, iters)

	if rawRate > 0.05 {
		t.Fatalf("raw-byte completion rate unexpectedly high (%.1f%%); the comparison is meaningless", rawRate*100)
	}
	if structRate < 0.5 {
		t.Fatalf("structured generator reached message_stop only %.1f%% of the time; expected >50%%", structRate*100)
	}
}
