package google

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
)

// gemGen turns a fuzz input into a deterministic stream of small choices and
// accumulates a valid Gemini streamGenerateContent (alt=sse) event sequence. It
// is the inverse of decodeStream: rather than emitting the minimal wire bytes (as
// the difftest encoder does), it walks the decoder's whole event vocabulary —
// candidates[].content.parts holding thought / text / functionCall parts (a
// functionCall optionally carrying a thoughtSignature), groundingMetadata web
// search results, a separate-chunk usageMetadata, and a finishReason chunk that
// (the bug-fixed shape) commonly carries usageMetadata on the SAME chunk and may
// also carry trailing content parts — so the metamorphic oracle explores the
// STRUCTURED stream space instead of bytes that almost always die at the first
// SSE/JSON parse.
//
// Determinism: every choice comes from the byte source (no maps for ORDER, no
// rand, no time). JSON is built with json.Marshal over map[string]any, which
// sorts object keys, so the same input always yields byte-identical SSE.
type gemGen struct {
	b   []byte
	i   int
	out bytes.Buffer
}

func (g *gemGen) next() byte {
	if g.i >= len(g.b) {
		return 0
	}
	v := g.b[g.i]
	g.i++
	return v
}

// intn returns a value in [0,n). n<=0 yields 0.
func (g *gemGen) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(g.next()) % n
}

func (g *gemGen) boolean() bool { return g.next()%2 == 0 }

// gemAlphabet includes a space, a double-quote and a newline so generated
// strings exercise the decoder's JSON unescaping on the way back in.
var gemAlphabet = []rune{'a', 'b', 'c', ' ', '"', '\n'}

func (g *gemGen) text(maxLen int) string {
	n := g.intn(maxLen + 1)
	r := make([]rune, n)
	for i := range r {
		r[i] = gemAlphabet[g.intn(len(gemAlphabet))]
	}
	return string(r)
}

// name returns a short non-empty lowercase tool name.
func (g *gemGen) name() string {
	n := 1 + g.intn(4)
	r := make([]byte, n)
	for i := range r {
		r[i] = byte('a' + g.intn(6))
	}
	return string(r)
}

// frame appends one `data: <json>\n\n` SSE frame. Gemini's alt=sse stream is a
// sequence of data-only frames carrying GenerateContentResponse JSON. Marshalling
// a map guarantees valid JSON and deterministic key order.
func (g *gemGen) frame(payload map[string]any) {
	data, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("structured gemini gen: marshal frame: %v", err))
	}
	g.out.WriteString("data: ")
	g.out.Write(data)
	g.out.WriteString("\n\n")
}

// argsObj builds a small JSON object (as a map, the Gemini functionCall.args wire
// shape) for a tool call.
func (g *gemGen) argsObj() map[string]any {
	obj := map[string]any{}
	for k := 0; k <= g.intn(3); k++ {
		obj[fmt.Sprintf("k%d", k)] = g.text(4)
	}
	return obj
}

// usage builds a usageMetadata object covering the prompt / candidates / total
// counts plus the optional cached-content and thoughts token breakdowns.
func (g *gemGen) usage() map[string]any {
	in := g.intn(120)
	out := g.intn(120)
	u := map[string]any{
		"promptTokenCount":     in,
		"candidatesTokenCount": out,
		"totalTokenCount":      in + out,
	}
	if g.boolean() {
		u["cachedContentTokenCount"] = g.intn(50)
	}
	if g.boolean() {
		u["thoughtsTokenCount"] = g.intn(50)
	}
	return u
}

// finishReason returns one of the Gemini finish-reason strings the decoder
// normalizes.
func (g *gemGen) finishReason() string {
	switch g.intn(4) {
	case 1:
		return "MAX_TOKENS"
	case 2:
		return "SAFETY"
	case 3:
		return "RECITATION"
	default:
		return "STOP"
	}
}

// contentParts builds 1..3 candidate parts of mixed kind: a thought part (which
// the decoder must classify before text because both carry a "text" field), a
// plain text part, or a functionCall part that sometimes carries a
// thoughtSignature. Some text is empty so the decoder's empty-part guards fire.
func (g *gemGen) contentParts() []any {
	n := 1 + g.intn(3)
	parts := make([]any, 0, n)
	for i := 0; i < n; i++ {
		switch g.intn(3) {
		case 0:
			parts = append(parts, map[string]any{"thought": true, "text": g.text(5)})
		case 1:
			parts = append(parts, map[string]any{"text": g.text(5)})
		default:
			p := map[string]any{"functionCall": map[string]any{"name": "t" + g.name(), "args": g.argsObj()}}
			if g.boolean() {
				p["thoughtSignature"] = g.text(4)
			}
			parts = append(parts, p)
		}
	}
	return parts
}

// structuredGeminiSSE builds a valid-but-adversarial Gemini streamGenerateContent
// SSE event sequence from fuzz bytes. The returned bytes are always parseable SSE
// whose frames carry well-formed JSON, so the decoder reaches its content,
// tool-mapping, reasoning, grounding, and finish logic instead of the
// malformed-JSON passthrough.
func structuredGeminiSSE(raw []byte) []byte {
	g := &gemGen{b: raw}

	// 0..3 content chunks, each carrying a mix of thought/text/functionCall parts.
	for c := g.intn(4); c > 0; c-- {
		g.frame(map[string]any{
			"candidates": []any{map[string]any{
				"content": map[string]any{"parts": g.contentParts()},
			}},
		})
	}

	// Optional groundingMetadata chunk (web search results surface as a WebSearch
	// part).
	if g.boolean() {
		g.frame(map[string]any{
			"candidates": []any{map[string]any{
				"groundingMetadata": map[string]any{
					"webSearchQueries": []any{g.text(4), g.text(4)},
				},
			}},
		})
	}

	// Optional separate-chunk usageMetadata (usage arriving ahead of the finish).
	separateUsage := g.boolean()
	if separateUsage {
		g.frame(map[string]any{"usageMetadata": g.usage()})
	}

	// Finish: candidates[0].finishReason, which commonly rides the SAME chunk as
	// trailing content parts and usageMetadata (the dominant Gemini shape, and the
	// site of a real bug where same-chunk usage was dropped). Occasionally truncate
	// so the metamorphic oracle holds on a mid-stream cutoff too. The truncate
	// token is a live nonzero value so an exhausted byte source (which reads back
	// zeros) produces a completed stream rather than always truncating.
	if g.next()%8 != 7 {
		cand := map[string]any{"finishReason": g.finishReason()}
		if g.boolean() {
			cand["content"] = map[string]any{"parts": g.contentParts()}
		}
		chunk := map[string]any{"candidates": []any{cand}}
		// usageMetadata on the finish chunk (same-chunk shape) when there was no
		// separate usage chunk, or sometimes in addition to it (a re-stated total).
		if !separateUsage || g.boolean() {
			chunk["usageMetadata"] = g.usage()
		}
		g.frame(chunk)
	}

	return g.out.Bytes()
}

// FuzzGeminiStreamStructured drives the REAL Gemini streamGenerateContent SSE
// decoder with structure-aware, valid-but-adversarial event sequences and asserts
// the SAME metamorphic property as FuzzGeminiStreamMetamorphic: a
// semantics-preserving transform (re-chunking, interstitial SSE comments) must
// not change the accumulated llm.Response. Where the raw-byte metamorphic target
// mostly dies at the first parse, this target reaches the decoder's
// content/tool/reasoning/grounding/finish logic on nearly every input (see
// TestStructuredGeminiReachesDeeper). A divergence here is a real decoder bug
// (state that leaks across read boundaries or mishandles benign framing), not a
// test artifact. The synthetic per-call tool-call ID is normalized away (see
// normalizeGeminiResponse) since it is random by construction, not a function of
// the bytes.
func FuzzGeminiStreamStructured(f *testing.F) {
	seeds := [][]byte{
		{},
		{0x00},
		{0x00, 0x01, 0x02, 0x03, 0x04, 0x05},
		{0x01, 0x02, 0x61, 0x62, 0x63, 0x64}, // content + tool call
		{0x02, 0x00, 0x02, 0x10, 0x20, 0x30, 0x40, 0x50},       // thought + text + tool
		{0x03, 0x01, 0x01, 0x02, 0x10, 0x20, 0x30, 0x40, 0x50}, // grounding + separate usage
		{0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8},       // truncated finish
	}
	for _, s := range seeds {
		f.Add(s)
	}

	a := &Adapter{BaseURL: "http://fuzz.local"}

	f.Fuzz(func(t *testing.T, raw []byte) {
		sse := structuredGeminiSSE(raw)

		base, baseErr := accumulateGeminiSSE(a, sse, false) // Oracle (floor): never panics.

		rechunked, reErr := accumulateGeminiSSE(a, sse, true)
		if !sameGeminiResponse(base, baseErr, rechunked, reErr) {
			t.Fatalf("re-chunk boundary changed the accumulated response:\n base=%+v (err=%v)\n one-byte=%+v (err=%v)\n sse=%q",
				base, baseErr, rechunked, reErr, sse)
		}

		commented := bytes.ReplaceAll(sse, []byte("\n\n"), []byte("\n\n: fuzz-keepalive\n\n"))
		withComments, cErr := accumulateGeminiSSE(a, commented, false)
		if !sameGeminiResponse(base, baseErr, withComments, cErr) {
			t.Fatalf("interstitial SSE comments changed the accumulated response:\n base=%+v (err=%v)\n commented=%+v (err=%v)\n sse=%q",
				base, baseErr, withComments, cErr, sse)
		}
	})
}

// TestStructuredGeminiReachesDeeper is the evidence (and a regression guard) that
// the structure-aware generator explores deeper decoder states than feeding the
// same bytes raw. Over a fixed-seed Monte Carlo, it measures the fraction of
// inputs whose stream reaches a finishReason chunk (a non-nil accumulated
// Response). Raw bytes interpreted as SSE essentially never form a valid finish
// chunk; the structured generator does so on most inputs.
func TestStructuredGeminiReachesDeeper(t *testing.T) {
	a := &Adapter{BaseURL: "http://fuzz.local"}
	const iters = 2000

	rng := rand.New(rand.NewSource(1)) //nolint:gosec // deterministic test fixture, not security
	var rawCompleted, structCompleted int
	for n := 0; n < iters; n++ {
		raw := make([]byte, rng.Intn(64))
		_, _ = rng.Read(raw)

		if resp, _ := accumulateGeminiSSE(a, raw, false); resp != nil {
			rawCompleted++
		}
		if resp, _ := accumulateGeminiSSE(a, structuredGeminiSSE(raw), false); resp != nil {
			structCompleted++
		}
	}

	rawRate := float64(rawCompleted) / iters
	structRate := float64(structCompleted) / iters
	t.Logf("reached finishReason: raw=%.1f%% (%d/%d)  structured=%.1f%% (%d/%d)",
		rawRate*100, rawCompleted, iters, structRate*100, structCompleted, iters)

	if rawRate > 0.05 {
		t.Fatalf("raw-byte completion rate unexpectedly high (%.1f%%); the comparison is meaningless", rawRate*100)
	}
	if structRate < 0.5 {
		t.Fatalf("structured generator reached finishReason only %.1f%% of the time; expected >50%%", structRate*100)
	}
}
