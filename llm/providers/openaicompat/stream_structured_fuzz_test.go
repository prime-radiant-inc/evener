package openaicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
)

// ccGen turns a fuzz input into a deterministic stream of small choices and
// accumulates a valid Chat Completions SSE event sequence. It is the inverse of
// decodeStream: rather than emitting the minimal wire bytes (as the difftest
// encoder does), it walks the decoder's whole event vocabulary — a role-only
// opening delta, reasoning_content deltas, content deltas, parallel tool_calls
// addressed by index with arguments split across many frames (the start frame
// carrying id+name, subsequent frames carrying argument fragments only), a
// finish_reason chunk, a separate usage chunk (the stream_options shape), and the
// terminal [DONE] — so the metamorphic oracle explores the STRUCTURED stream
// space instead of bytes that almost always die at the first SSE/JSON parse.
//
// Determinism: every choice comes from the byte source (no maps for ORDER, no
// rand, no time). JSON is built with json.Marshal over map[string]any, which
// sorts object keys, so the same input always yields byte-identical SSE.
type ccGen struct {
	b   []byte
	i   int
	out bytes.Buffer
}

func (g *ccGen) next() byte {
	if g.i >= len(g.b) {
		return 0
	}
	v := g.b[g.i]
	g.i++
	return v
}

// intn returns a value in [0,n). n<=0 yields 0.
func (g *ccGen) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(g.next()) % n
}

func (g *ccGen) boolean() bool { return g.next()%2 == 0 }

// ccAlphabet includes a space, a double-quote and a newline so generated strings
// exercise the decoder's JSON unescaping on the way back in.
var ccAlphabet = []rune{'a', 'b', 'c', ' ', '"', '\n'}

func (g *ccGen) text(maxLen int) string {
	n := g.intn(maxLen + 1)
	r := make([]rune, n)
	for i := range r {
		r[i] = ccAlphabet[g.intn(len(ccAlphabet))]
	}
	return string(r)
}

// name returns a short non-empty lowercase tool name.
func (g *ccGen) name() string {
	n := 1 + g.intn(4)
	r := make([]byte, n)
	for i := range r {
		r[i] = byte('a' + g.intn(6))
	}
	return string(r)
}

// frame appends one `data: <json>\n\n` SSE frame. Marshalling a map guarantees
// valid JSON and deterministic key order.
func (g *ccGen) frame(payload map[string]any) {
	data, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("structured chat gen: marshal frame: %v", err))
	}
	g.out.WriteString("data: ")
	g.out.Write(data)
	g.out.WriteString("\n\n")
}

// rawFrame appends one `data: <s>\n\n` SSE frame whose payload is not JSON (used
// for the terminal [DONE] sentinel).
func (g *ccGen) rawFrame(s string) {
	g.out.WriteString("data: ")
	g.out.WriteString(s)
	g.out.WriteString("\n\n")
}

// split breaks s into 1..3 non-empty pieces at source-chosen boundaries so tool
// arguments arrive across multiple delta frames (the real wire shape).
func (g *ccGen) split(s string) []string {
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
func (g *ccGen) argsJSON() string {
	obj := map[string]any{}
	for k := 0; k <= g.intn(3); k++ {
		obj[fmt.Sprintf("k%d", k)] = g.text(4)
	}
	b, err := json.Marshal(obj)
	if err != nil {
		panic(fmt.Sprintf("structured chat gen: marshal args: %v", err))
	}
	return string(b)
}

// usage builds a usage object covering the prompt / completion / total counts plus
// the optional cached-input and reasoning-token breakdowns the decoder reads.
func (g *ccGen) usage() map[string]any {
	in := g.intn(120)
	out := g.intn(120)
	cached := g.intn(in + 1)
	u := map[string]any{
		"prompt_tokens":     in,
		"completion_tokens": out,
		"total_tokens":      in + out,
	}
	if g.boolean() {
		u["prompt_tokens_details"] = map[string]any{"cached_tokens": cached}
	}
	if g.boolean() {
		u["completion_tokens_details"] = map[string]any{"reasoning_tokens": g.intn(50)}
	}
	return u
}

// deltaChunk wraps a single choices[0] delta payload in a chat completion chunk.
func deltaChunk(model string, delta map[string]any) map[string]any {
	return map[string]any{
		"model":   model,
		"choices": []any{map[string]any{"index": 0, "delta": delta}},
	}
}

// genTool is one fully-realized tool call, retained so its argument fragments can
// be streamed round-robin with the other tools (the real parallel-call shape).
type genTool struct {
	idx    int
	chunks []string
}

// structuredChatSSE builds a valid-but-adversarial Chat Completions SSE event
// sequence from fuzz bytes. The returned bytes are always parseable SSE whose
// frames carry well-formed JSON, so the decoder reaches its content, reasoning,
// tool-mapping, and finish logic instead of the malformed-chunk skip.
func structuredChatSSE(raw []byte) []byte {
	g := &ccGen{b: raw}
	model := "m" + g.name()

	// Optional opening role-only delta.
	if g.boolean() {
		g.frame(deltaChunk(model, map[string]any{"role": "assistant"}))
	}

	// 0..3 reasoning_content deltas (some empty, exercising the start guard).
	for d := g.intn(4); d > 0; d-- {
		g.frame(deltaChunk(model, map[string]any{"reasoning_content": g.text(5)}))
	}

	// 0..3 content deltas.
	for d := g.intn(4); d > 0; d-- {
		g.frame(deltaChunk(model, map[string]any{"content": g.text(5)}))
	}

	// Tools: 0..3 parallel calls, each opened with id+name then fed argument
	// fragments round-robin across the calls so the index-keyed accumulator and
	// its deterministic end-of-stream sort are exercised.
	nTools := g.intn(4)
	var tools []genTool
	for t := 0; t < nTools; t++ {
		g.frame(deltaChunk(model, map[string]any{
			"tool_calls": []any{map[string]any{
				"index":    t,
				"id":       fmt.Sprintf("call_%d", t),
				"type":     "function",
				"function": map[string]any{"name": "t" + g.name(), "arguments": ""},
			}},
		}))
		tools = append(tools, genTool{idx: t, chunks: g.split(g.argsJSON())})
	}
	maxRounds := 0
	for _, tc := range tools {
		if len(tc.chunks) > maxRounds {
			maxRounds = len(tc.chunks)
		}
	}
	for r := 0; r < maxRounds; r++ {
		for _, tc := range tools {
			if r >= len(tc.chunks) || tc.chunks[r] == "" {
				continue
			}
			g.frame(deltaChunk(model, map[string]any{
				"tool_calls": []any{map[string]any{
					"index":    tc.idx,
					"function": map[string]any{"arguments": tc.chunks[r]},
				}},
			}))
		}
	}

	// Finish: a finish_reason chunk, an optional separate usage chunk (the
	// stream_options.include_usage shape, whose choices array is empty), and the
	// terminal [DONE]. Occasionally truncate so the metamorphic oracle holds on a
	// mid-stream cutoff too. The truncate token is a live nonzero value so an
	// exhausted byte source (which reads back zeros) produces a completed stream
	// rather than always truncating.
	if g.next()%8 != 7 {
		finish := "stop"
		switch g.intn(4) {
		case 1:
			finish = "length"
		case 2:
			finish = "tool_calls"
		case 3:
			finish = "content_filter"
		}
		if nTools > 0 {
			finish = "tool_calls"
		}
		g.frame(map[string]any{
			"model":   model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}},
		})
		if g.boolean() {
			g.frame(map[string]any{"model": model, "choices": []any{}, "usage": g.usage()})
		}
		g.rawFrame("[DONE]")
	}

	return g.out.Bytes()
}

// FuzzOpenAICompatStreamStructured drives the REAL Chat Completions SSE decoder
// with structure-aware, valid-but-adversarial event sequences and asserts the
// SAME metamorphic property as FuzzOpenAICompatStreamMetamorphic: a
// semantics-preserving transform (re-chunking, interstitial SSE comments) must
// not change the accumulated llm.Response. Where the raw-byte metamorphic target
// mostly dies at the first parse, this target reaches the decoder's
// content/reasoning/tool/finish logic on nearly every input (see
// TestStructuredOpenAICompatReachesDeeper). A divergence here is a real decoder
// bug (state that leaks across read boundaries or mishandles benign framing), not
// a test artifact. This decoder backs openaicompat directly and (via wrapping)
// glm, kimi, openrouter, and ollama.
func FuzzOpenAICompatStreamStructured(f *testing.F) {
	seeds := [][]byte{
		{},
		{0x00},
		{0x00, 0x01, 0x02, 0x03, 0x04, 0x05},
		{0x01, 0x00, 0x01, 0x61, 0x62, 0x63, 0x64},             // text + one tool
		{0x02, 0x02, 0x00, 0x10, 0x20, 0x30, 0x40, 0x50},       // reasoning + two tools
		{0x03, 0x01, 0x02, 0x10, 0x20, 0x30, 0x40, 0x50, 0x60}, // mixed content + tools
		{0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8},       // truncated finish
	}
	for _, s := range seeds {
		f.Add(s)
	}

	a := &Adapter{BaseURL: "http://fuzz.local"}

	f.Fuzz(func(t *testing.T, raw []byte) {
		sse := structuredChatSSE(raw)

		base, baseErr := accumulateChatSSE(a, sse, false) // Oracle (floor): never panics.

		rechunked, reErr := accumulateChatSSE(a, sse, true)
		if !sameChatResponse(base, baseErr, rechunked, reErr) {
			t.Fatalf("re-chunk boundary changed the accumulated response:\n base=%+v (err=%v)\n one-byte=%+v (err=%v)\n sse=%q",
				base, baseErr, rechunked, reErr, sse)
		}

		commented := bytes.ReplaceAll(sse, []byte("\n\n"), []byte("\n\n: fuzz-keepalive\n\n"))
		withComments, cErr := accumulateChatSSE(a, commented, false)
		if !sameChatResponse(base, baseErr, withComments, cErr) {
			t.Fatalf("interstitial SSE comments changed the accumulated response:\n base=%+v (err=%v)\n commented=%+v (err=%v)\n sse=%q",
				base, baseErr, withComments, cErr, sse)
		}
	})
}

// TestStructuredOpenAICompatReachesDeeper is the evidence (and a regression
// guard) that the structure-aware generator explores deeper decoder states than
// feeding the same bytes raw. Over a fixed-seed Monte Carlo, it measures the
// fraction of inputs whose stream reaches [DONE] (a non-nil accumulated
// Response). Raw bytes interpreted as SSE essentially never form a valid
// completed stream; the structured generator does so on most inputs.
func TestStructuredOpenAICompatReachesDeeper(t *testing.T) {
	a := &Adapter{BaseURL: "http://fuzz.local"}
	const iters = 2000

	rng := rand.New(rand.NewSource(1)) //nolint:gosec // deterministic test fixture, not security
	var rawCompleted, structCompleted int
	for n := 0; n < iters; n++ {
		raw := make([]byte, rng.Intn(64))
		_, _ = rng.Read(raw)

		if resp, _ := accumulateChatSSE(a, raw, false); resp != nil {
			rawCompleted++
		}
		if resp, _ := accumulateChatSSE(a, structuredChatSSE(raw), false); resp != nil {
			structCompleted++
		}
	}

	rawRate := float64(rawCompleted) / iters
	structRate := float64(structCompleted) / iters
	t.Logf("reached [DONE]: raw=%.1f%% (%d/%d)  structured=%.1f%% (%d/%d)",
		rawRate*100, rawCompleted, iters, structRate*100, structCompleted, iters)

	if rawRate > 0.05 {
		t.Fatalf("raw-byte completion rate unexpectedly high (%.1f%%); the comparison is meaningless", rawRate*100)
	}
	if structRate < 0.5 {
		t.Fatalf("structured generator reached [DONE] only %.1f%% of the time; expected >50%%", structRate*100)
	}
}
