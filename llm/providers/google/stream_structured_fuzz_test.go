package google

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
)

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

func (g *gemGen) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(g.next()) % n
}

func (g *gemGen) boolean() bool { return g.next()%2 == 0 }

var gemAlphabet = []rune{'a', 'b', 'c', ' ', '"', '\n'}

func (g *gemGen) text(maxLen int) string {
	n := g.intn(maxLen + 1)
	r := make([]rune, n)
	for i := range r {
		r[i] = gemAlphabet[g.intn(len(gemAlphabet))]
	}
	return string(r)
}

func (g *gemGen) name() string {
	n := 1 + g.intn(4)
	r := make([]byte, n)
	for i := range r {
		r[i] = byte('a' + g.intn(6))
	}
	return string(r)
}

func (g *gemGen) frame(event string, payload map[string]any) {
	data, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("structured interactions gen: marshal frame: %v", err))
	}
	g.out.WriteString("event: ")
	g.out.WriteString(event)
	g.out.WriteString("\ndata: ")
	g.out.Write(data)
	g.out.WriteString("\n\n")
}

func (g *gemGen) argsObj() map[string]any {
	obj := map[string]any{}
	for k := 0; k <= g.intn(3); k++ {
		obj[fmt.Sprintf("k%d", k)] = g.text(4)
	}
	return obj
}

func (g *gemGen) usage() map[string]any {
	in := g.intn(120)
	out := g.intn(120)
	u := map[string]any{
		"total_input_tokens":  in,
		"total_output_tokens": out,
		"total_tokens":        in + out,
	}
	if g.boolean() {
		u["total_cached_tokens"] = g.intn(50)
	}
	if g.boolean() {
		u["total_thought_tokens"] = g.intn(50)
	}
	return u
}

func (g *gemGen) status(hasTools bool) string {
	if hasTools && g.boolean() {
		return "requires_action"
	}
	switch g.intn(4) {
	case 1:
		return "incomplete"
	case 2:
		return "cancelled"
	case 3:
		return "requires_continuation"
	default:
		return "completed"
	}
}

func structuredInteractionsSSE(raw []byte) []byte {
	g := &gemGen{b: raw}
	g.frame("interaction.created", map[string]any{
		"event_type": "interaction.created",
		"interaction": map[string]any{
			"id":     "int_fuzz",
			"model":  "gemini-3.8-flash",
			"status": "in_progress",
		},
	})

	idx := 0
	hasTools := false
	nSteps := g.intn(4)
	for range nSteps {
		switch g.intn(4) {
		case 0: // thought
			g.frame("step.start", map[string]any{
				"event_type": "step.start",
				"index":      idx,
				"step":       map[string]any{"type": "thought"},
			})
			if txt := g.text(5); txt != "" {
				g.frame("step.delta", map[string]any{
					"event_type": "step.delta",
					"index":      idx,
					"delta":      map[string]any{"type": "thought_summary", "content": map[string]any{"type": "text", "text": txt}},
				})
			}
			if g.boolean() {
				g.frame("step.delta", map[string]any{
					"event_type": "step.delta",
					"index":      idx,
					"delta":      map[string]any{"type": "thought_signature", "signature": "sig_" + g.name()},
				})
			}
			g.frame("step.stop", map[string]any{"event_type": "step.stop", "index": idx})
		case 1: // model_output
			g.frame("step.start", map[string]any{
				"event_type": "step.start",
				"index":      idx,
				"step":       map[string]any{"type": "model_output"},
			})
			for c := g.intn(3); c > 0; c-- {
				g.frame("step.delta", map[string]any{
					"event_type": "step.delta",
					"index":      idx,
					"delta":      map[string]any{"type": "text", "text": g.text(5)},
				})
			}
			g.frame("step.stop", map[string]any{"event_type": "step.stop", "index": idx, "usage": g.usage()})
		case 2: // function_call
			hasTools = true
			argsBytes, _ := json.Marshal(g.argsObj())
			g.frame("step.start", map[string]any{
				"event_type": "step.start",
				"index":      idx,
				"step": map[string]any{
					"type":      "function_call",
					"id":        fmt.Sprintf("call_%d", idx),
					"name":      "t" + g.name(),
					"arguments": map[string]any{},
				},
			})
			if g.boolean() {
				g.frame("step.delta", map[string]any{
					"event_type": "step.delta",
					"index":      idx,
					"delta":      map[string]any{"type": "arguments_delta", "arguments": string(argsBytes)},
				})
			}
			g.frame("step.stop", map[string]any{"event_type": "step.stop", "index": idx})
		default: // google_search_call + result
			cid := fmt.Sprintf("gs_%d", idx)
			g.frame("step.start", map[string]any{
				"event_type": "step.start",
				"index":      idx,
				"step":       map[string]any{"type": "google_search_call", "id": cid, "arguments": map[string]any{"queries": []any{g.text(4)}}},
			})
			g.frame("step.stop", map[string]any{"event_type": "step.stop", "index": idx})
			idx++
			g.frame("step.start", map[string]any{
				"event_type": "step.start",
				"index":      idx,
				"step":       map[string]any{"type": "google_search_result", "call_id": cid, "result": []any{map[string]any{"url": "https://example.test"}}},
			})
			g.frame("step.stop", map[string]any{"event_type": "step.stop", "index": idx})
		}
		idx++
	}

	if g.next()%8 != 7 {
		g.frame("interaction.completed", map[string]any{
			"event_type": "interaction.completed",
			"interaction": map[string]any{
				"id":     "int_fuzz",
				"status": g.status(hasTools),
				"usage":  g.usage(),
			},
		})
	}
	return g.out.Bytes()
}

func FuzzGeminiStreamStructured(f *testing.F) {
	seeds := [][]byte{
		{},
		{0x00},
		{0x00, 0x01, 0x02, 0x03, 0x04, 0x05},
		{0x01, 0x02, 0x61, 0x62, 0x63, 0x64},
		{0x02, 0x00, 0x02, 0x10, 0x20, 0x30, 0x40, 0x50},
		{0x03, 0x01, 0x01, 0x02, 0x10, 0x20, 0x30, 0x40, 0x50},
		{0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8},
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		sse := structuredInteractionsSSE(raw)

		base, baseErr := accumulateInteractionsSSE(sse, false)

		rechunked, reErr := accumulateInteractionsSSE(sse, true)
		if !sameInteractionsResponse(base, baseErr, rechunked, reErr) {
			t.Fatalf("re-chunk boundary changed the accumulated response:\n base=%+v (err=%v)\n one-byte=%+v (err=%v)\n sse=%q",
				base, baseErr, rechunked, reErr, sse)
		}

		commented := bytes.ReplaceAll(sse, []byte("\n\n"), []byte("\n\n: fuzz-keepalive\n\n"))
		withComments, cErr := accumulateInteractionsSSE(commented, false)
		if !sameInteractionsResponse(base, baseErr, withComments, cErr) {
			t.Fatalf("interstitial SSE comments changed the accumulated response:\n base=%+v (err=%v)\n commented=%+v (err=%v)\n sse=%q",
				base, baseErr, withComments, cErr, sse)
		}
	})
}

func TestStructuredGeminiReachesDeeper(t *testing.T) {
	t.Parallel()
	const iters = 2000

	rng := rand.New(rand.NewSource(1)) //nolint:gosec // deterministic test fixture, not security
	var rawCompleted, structCompleted int
	for range iters {
		raw := make([]byte, rng.Intn(64))
		_, _ = rng.Read(raw)

		if resp, _ := accumulateInteractionsSSE(raw, false); resp != nil {
			rawCompleted++
		}
		if resp, _ := accumulateInteractionsSSE(structuredInteractionsSSE(raw), false); resp != nil {
			structCompleted++
		}
	}

	rawRate := float64(rawCompleted) / iters
	structRate := float64(structCompleted) / iters
	t.Logf("reached interaction.completed: raw=%.1f%% (%d/%d)  structured=%.1f%% (%d/%d)",
		rawRate*100, rawCompleted, iters, structRate*100, structCompleted, iters)

	if rawRate > 0.05 {
		t.Fatalf("raw-byte completion rate unexpectedly high (%.1f%%); the comparison is meaningless", rawRate*100)
	}
	if structRate < 0.5 {
		t.Fatalf("structured generator reached interaction.completed only %.1f%% of the time; expected >50%%", structRate*100)
	}
}
