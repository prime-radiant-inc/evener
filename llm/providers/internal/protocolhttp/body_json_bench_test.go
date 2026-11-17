package protocolhttp

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// benchBodyTurns is the history size #6409 quotes its cost at.
const benchBodyTurns = 800

// benchBodyResultBytes is the tool result size per turn #6409 measures with.
const benchBodyResultBytes = 2 << 10

// benchPrepareRes is a resolved anthropic row whose body is a large map tree,
// which is the shape Prepare marshals on every attempt.
func benchPrepareRes() registry.Resolved {
	return registry.Resolved{
		Instance: "inst", Protocol: registry.ProtocolAnthropic, ModelID: "m", WireID: "claude-x-wire",
		Transport: registry.Transport{Auth: registry.AuthNone, BaseURL: "https://api.example.test", Endpoint: "/messages"},
		Caps:      registry.Caps{Fields: registry.Baseline(registry.ProtocolAnthropic)},
	}
}

// benchPrepareBody builds the map tree an anthropic Messages body holds at
// turns of history with resultBytes of tool output per turn.
func benchPrepareBody(turns, resultBytes int) map[string]any {
	result := strings.Repeat("x", resultBytes)
	msgs := make([]map[string]any, 0, 2*turns)
	for range turns {
		msgs = append(msgs,
			map[string]any{"role": "assistant", "content": []map[string]any{
				{"type": "tool_use", "id": "toolu_01", "name": "grep", "input": map[string]any{"pattern": "func main"}},
			}},
			map[string]any{"role": "user", "content": []map[string]any{
				{"type": "tool_result", "tool_use_id": "toolu_01", "content": result},
			}},
		)
	}
	return map[string]any{
		"max_tokens":    32000,
		"messages":      msgs,
		"cache_control": map[string]any{"type": "ephemeral"},
		"model":         "claude-x-wire",
		"system":        []map[string]any{{"type": "text", "text": "sys prompt"}},
		"tools": []map[string]any{
			{"name": "grep", "description": "search", "input_schema": map[string]any{"type": "object"}},
		},
	}
}

// BenchmarkPrepareLargeHistory measures one Prepare over a body that grows
// with history, the pass #6409 reports as the largest addressable allocation
// in a round.
func BenchmarkPrepareLargeHistory(b *testing.B) {
	for _, turns := range []int{50, 200, benchBodyTurns} {
		b.Run(map[int]string{50: "50turns", 200: "200turns", benchBodyTurns: "800turns"}[turns], func(b *testing.B) {
			res := benchPrepareRes()
			body := benchPrepareBody(turns, benchBodyResultBytes)
			call := &Call{Method: http.MethodPost, URL: URL(res, res.Transport.Endpoint), Body: body, Res: res}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := Prepare(context.Background(), call); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkMarshalBodyLargeHistory measures the encoder alone, so a change in
// Prepare's other steps cannot hide one in the encoder or the reverse.
func BenchmarkMarshalBodyLargeHistory(b *testing.B) {
	body := benchPrepareBody(benchBodyTurns, benchBodyResultBytes)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		out, err := MarshalBody(body)
		if err != nil {
			b.Fatal(err)
		}
		b.SetBytes(int64(len(out)))
	}
}
