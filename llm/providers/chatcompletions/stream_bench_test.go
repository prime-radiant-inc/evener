package chatcompletions

import (
	"strings"
	"testing"
)

// BenchmarkStreamToolCallArgs measures a stream whose tool-call arguments
// arrive across many chunks, driven through the real stream decoder over a
// recorded body. Every chunk that carries arguments used to be JSON-decoded a
// second time to recover them; this is the stream shape that cost paid for.
func BenchmarkStreamToolCallArgs(b *testing.B) {
	const chunks = 128
	body := []byte(benchArgsSSE(chunks))
	b.ReportAllocs()
	for b.Loop() {
		resp, err := replayRecordedStream(body, "m")
		if err != nil {
			b.Fatalf("decode: %v", err)
		}
		if calls := resp.ToolCalls(); len(calls) != 1 || len(calls[0].Arguments) == 0 {
			b.Fatalf("assembled %d tool calls with %d argument bytes, want 1 non-empty", len(calls), len(calls[0].Arguments))
		}
	}
}

// benchArgsFragments splits a realistic tool-call arguments payload into
// count fragments at rune boundaries, so every fragment is a valid JSON string
// value and the concatenation is the original payload.
func benchArgsFragments(count int) []string {
	content := `{"path":"` + strings.Repeat("abcdefgh", 128) + `","limit":10,"u":"\u00e9"}`
	units := argContentUnits(content)
	frags := make([]string, 0, count)
	step := max(1, len(units)/count)
	for i := 0; i < len(units); i += step {
		frags = append(frags, strings.Join(units[i:min(i+step, len(units))], ""))
	}
	return frags
}

// benchArgsSSE builds a recorded SSE body carrying frags, one chunk per
// fragment, followed by a finish chunk and [DONE].
func benchArgsSSE(count int) string {
	var b strings.Builder
	for _, frag := range benchArgsFragments(count) {
		b.WriteString(`data: {"id":"c1","model":"m-wire","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"write_file","arguments":`)
		b.Write(jsonStringToken(frag))
		b.WriteString("}}]}}]}\n\n")
	}
	b.WriteString("data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}
