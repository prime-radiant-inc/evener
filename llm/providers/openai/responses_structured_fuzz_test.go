package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// respGen turns a fuzz input into a deterministic stream of small choices and
// accumulates a valid OpenAI Responses SSE event sequence. It is the inverse of
// decodeResponsesStream: rather than emitting the minimal wire bytes (as the
// difftest encoder does), it walks the decoder's whole event vocabulary —
// reasoning parts, multiple text deltas via either the "delta" or "text" field,
// tool calls addressed by call_id / item_id / both, arguments split across many
// function_call_arguments.delta frames, a separate .done, output_item.done for
// the end-of-text branch, and a completed/incomplete/truncated finish — so the
// metamorphic oracle explores the STRUCTURED stream space instead of bytes that
// almost always die at the first SSE/JSON parse.
//
// Determinism: every choice comes from the byte source (no maps for ORDER, no
// rand, no time). JSON is built with json.Marshal over map[string]any, which
// sorts object keys, so the same input always yields byte-identical SSE.
type respGen struct {
	b   []byte
	i   int
	out bytes.Buffer
}

func (g *respGen) next() byte {
	if g.i >= len(g.b) {
		return 0
	}
	v := g.b[g.i]
	g.i++
	return v
}

// intn returns a value in [0,n). n<=0 yields 0.
func (g *respGen) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(g.next()) % n
}

func (g *respGen) boolean() bool { return g.next()%2 == 0 }

// respAlphabet includes a space, a double-quote and a newline so generated
// strings exercise the decoder's JSON unescaping on the way back in.
var respAlphabet = []rune{'a', 'b', 'c', ' ', '"', '\n'}

func (g *respGen) text(maxLen int) string {
	n := g.intn(maxLen + 1)
	r := make([]rune, n)
	for i := range r {
		r[i] = respAlphabet[g.intn(len(respAlphabet))]
	}
	return string(r)
}

// name returns a short non-empty lowercase tool name.
func (g *respGen) name() string {
	n := 1 + g.intn(4)
	r := make([]byte, n)
	for i := range r {
		r[i] = byte('a' + g.intn(6))
	}
	return string(r)
}

// frame appends one `data: <json>\n\n` SSE frame. Marshalling a map guarantees
// valid JSON and deterministic key order.
func (g *respGen) frame(payload map[string]any) {
	data, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("structured responses gen: marshal frame: %v", err))
	}
	g.out.WriteString("data: ")
	g.out.Write(data)
	g.out.WriteString("\n\n")
}

// split breaks s into 1..3 non-empty pieces at source-chosen boundaries so tool
// arguments arrive across multiple deltas (the real wire shape).
func (g *respGen) split(s string) []string {
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

// genTool is one fully-realized tool call, retained so the completed event's
// output array can mirror the streamed deltas.
type genTool struct {
	callID string
	itemID string
	name   string
	args   string
}

// structuredResponsesSSE builds a valid-but-adversarial OpenAI Responses SSE
// event sequence from fuzz bytes. The returned bytes are always parseable SSE
// whose frames carry well-formed JSON, so the decoder reaches its content,
// tool-mapping, and finish logic instead of the malformed-JSON passthrough.
func structuredResponsesSSE(raw []byte) []byte {
	g := &respGen{b: raw}

	// Reasoning: 0..2 summary parts, each with 0..2 (sometimes empty) deltas.
	nParts := g.intn(3)
	for p := 0; p < nParts; p++ {
		g.frame(map[string]any{"type": "response.reasoning_summary_part.added"})
		for d := g.intn(3); d > 0; d-- {
			g.frame(map[string]any{"type": "response.reasoning_summary_text.delta", "delta": g.text(5)})
		}
	}

	// Text: 0..3 deltas via "delta", "text", or an empty frame (early-return guard).
	var textBuf strings.Builder
	nText := g.intn(4)
	for d := 0; d < nText; d++ {
		delta := g.text(5)
		switch g.intn(3) {
		case 0:
			g.frame(map[string]any{"type": "response.output_text.delta", "delta": delta})
			textBuf.WriteString(delta)
		case 1:
			g.frame(map[string]any{"type": "response.output_text.delta", "text": delta})
			textBuf.WriteString(delta)
		default:
			g.frame(map[string]any{"type": "response.output_text.delta"})
		}
	}

	// Tools: 0..3 calls, each exercising a different id-addressing mode.
	var tools []genTool
	nTools := g.intn(4)
	for t := 0; t < nTools; t++ {
		tc := genTool{
			callID: fmt.Sprintf("call_%d", t),
			itemID: fmt.Sprintf("fc_%d", t),
			name:   "t" + g.name(),
			args:   g.argsJSON(),
		}
		mode := g.intn(3) // 0=call_id only, 1=item_id only, 2=both
		address := func(m map[string]any, allowItem bool) {
			switch mode {
			case 0:
				m["call_id"] = tc.callID
			case 1:
				m["item_id"] = tc.itemID
			default:
				if allowItem && g.boolean() {
					m["item_id"] = tc.itemID
				} else {
					m["call_id"] = tc.callID
				}
			}
		}

		if !g.boolean() { // sometimes skip output_item.added to test the delta-without-added path
			item := map[string]any{"type": "function_call", "name": tc.name}
			if mode != 1 {
				item["call_id"] = tc.callID
			}
			if mode != 0 {
				item["id"] = tc.itemID
			}
			g.frame(map[string]any{"type": "response.output_item.added", "item": item})
		}

		for _, chunk := range g.split(tc.args) {
			delta := map[string]any{"type": "response.function_call_arguments.delta", "delta": chunk}
			address(delta, true)
			if g.boolean() { // occasionally re-assert the name on a delta
				delta["name"] = tc.name
			}
			g.frame(delta)
		}

		if g.boolean() {
			done := map[string]any{"type": "response.function_call_arguments.done", "arguments": tc.args}
			address(done, true)
			g.frame(done)
		}

		tools = append(tools, tc)
	}

	// End-of-text via a non-function output_item.done (best-effort TextEnd branch).
	if nText > 0 && g.boolean() {
		g.frame(map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message"}})
	}

	// Finish: completed, completed-but-incomplete, or a truncated stream.
	switch g.intn(4) {
	case 0, 1:
		g.frame(completedFrame(textBuf.String(), tools, g, false))
	case 2:
		g.frame(completedFrame(textBuf.String(), tools, g, true))
	default:
		// No completion: a genuinely truncated stream (mid-stream error / empty
		// fallback). Still valid SSE; the metamorphic oracle must hold.
	}

	return g.out.Bytes()
}

// argsJSON builds a small, valid JSON object string for a tool call.
func (g *respGen) argsJSON() string {
	obj := map[string]any{}
	for k := 0; k <= g.intn(3); k++ {
		obj[fmt.Sprintf("k%d", k)] = g.text(4)
	}
	b, err := json.Marshal(obj)
	if err != nil {
		panic(fmt.Sprintf("structured responses gen: marshal args: %v", err))
	}
	return string(b)
}

// completedFrame builds the response.completed payload whose output array mirrors
// the streamed content, so the StreamAccumulator's preferred final Response is
// internally consistent. incomplete marks the max_output_tokens (length) finish.
func completedFrame(text string, tools []genTool, g *respGen, incomplete bool) map[string]any {
	var output []any
	if text != "" {
		output = append(output, map[string]any{
			"type":    "message",
			"content": []any{map[string]any{"type": "output_text", "text": text}},
		})
	}
	for _, tc := range tools {
		output = append(output, map[string]any{
			"type":      "function_call",
			"call_id":   tc.callID,
			"id":        tc.itemID,
			"name":      tc.name,
			"arguments": tc.args,
		})
	}
	in, out := g.intn(120), g.intn(120)
	resp := map[string]any{
		"id":     "resp_struct",
		"model":  "fuzz-model",
		"output": output,
		"usage": map[string]any{
			"input_tokens":  in,
			"output_tokens": out,
			"total_tokens":  in + out,
		},
	}
	if incomplete {
		resp["status"] = "incomplete"
		resp["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	} else {
		resp["status"] = "completed"
	}
	return map[string]any{"type": "response.completed", "response": resp}
}

// FuzzOpenAIResponsesStructured drives the REAL Responses SSE decoder with
// structure-aware, valid-but-adversarial event sequences and asserts the SAME
// metamorphic property as FuzzOpenAIResponsesMetamorphic: a semantics-preserving
// transform (re-chunking, interstitial SSE comments) must not change the
// accumulated llm.Response. Where the raw-byte metamorphic target mostly dies at
// the first parse, this target reaches the decoder's content/tool/finish logic
// on nearly every input (see TestStructuredResponsesReachesDeeper). A divergence
// here is a real decoder bug (state that leaks across read boundaries or
// mishandles benign framing), not a test artifact.
func FuzzOpenAIResponsesStructured(f *testing.F) {
	seeds := [][]byte{
		{},
		{0x00},
		{0x00, 0x01, 0x02, 0x03, 0x04, 0x05},
		{0x01, 0x01, 0x61, 0x62, 0x63, 0x64}, // one tool, split args
		{0x02, 0x02, 0x61, 0x62, 0x07, 0x09, 0x0a, 0x0b, 0x0c}, // two tools, mixed addressing
		{0x02, 0x00, 0x00, 0x10, 0x20, 0x30, 0x40, 0x50, 0x60}, // reasoning + text + tools
		{0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8},       // truncated finish
	}
	for _, s := range seeds {
		f.Add(s)
	}

	a := &Adapter{BaseURL: "http://fuzz.local"}

	f.Fuzz(func(t *testing.T, raw []byte) {
		sse := structuredResponsesSSE(raw)

		base, baseErr := accumulateResponsesSSE(a, sse, false) // Oracle (floor): never panics.

		rechunked, reErr := accumulateResponsesSSE(a, sse, true)
		if !sameAccumulatedResponse(base, baseErr, rechunked, reErr) {
			t.Fatalf("re-chunk boundary changed the accumulated response:\n base=%+v (err=%v)\n one-byte=%+v (err=%v)\n sse=%q",
				base, baseErr, rechunked, reErr, sse)
		}

		commented := bytes.ReplaceAll(sse, []byte("\n\n"), []byte("\n\n: fuzz-keepalive\n\n"))
		withComments, cErr := accumulateResponsesSSE(a, commented, false)
		if !sameAccumulatedResponse(base, baseErr, withComments, cErr) {
			t.Fatalf("interstitial SSE comments changed the accumulated response:\n base=%+v (err=%v)\n commented=%+v (err=%v)\n sse=%q",
				base, baseErr, withComments, cErr, sse)
		}
	})
}

// TestStructuredResponsesReachesDeeper is the evidence (and a regression guard)
// that the structure-aware generator explores deeper decoder states than feeding
// the same bytes raw. Over a fixed-seed Monte Carlo, it measures the fraction of
// inputs whose stream reaches response.completed (a non-nil accumulated
// Response). Raw bytes interpreted as SSE essentially never form a valid
// completed frame; the structured generator does so on most inputs.
func TestStructuredResponsesReachesDeeper(t *testing.T) {
	a := &Adapter{BaseURL: "http://fuzz.local"}
	const iters = 2000

	rng := rand.New(rand.NewSource(1)) //nolint:gosec // deterministic test fixture, not security
	var rawCompleted, structCompleted int
	for n := 0; n < iters; n++ {
		raw := make([]byte, rng.Intn(64))
		_, _ = rng.Read(raw)

		if resp, _ := accumulateResponsesSSE(a, raw, false); resp != nil {
			rawCompleted++
		}
		if resp, _ := accumulateResponsesSSE(a, structuredResponsesSSE(raw), false); resp != nil {
			structCompleted++
		}
	}

	rawRate := float64(rawCompleted) / iters
	structRate := float64(structCompleted) / iters
	t.Logf("reached response.completed: raw=%.1f%% (%d/%d)  structured=%.1f%% (%d/%d)",
		rawRate*100, rawCompleted, iters, structRate*100, structCompleted, iters)

	if rawRate > 0.05 {
		t.Fatalf("raw-byte completion rate unexpectedly high (%.1f%%); the comparison is meaningless", rawRate*100)
	}
	if structRate < 0.5 {
		t.Fatalf("structured generator reached response.completed only %.1f%% of the time; expected >50%%", structRate*100)
	}
}
