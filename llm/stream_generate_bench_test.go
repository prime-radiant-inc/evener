package llm

import (
	"context"
	"testing"
)

// benchmarkStreamDeltas is the delta count one benchmark op streams, so ns/op
// and B/op read directly as the cost of 1000 deltas (the unit issue #6411
// quotes).
const benchmarkStreamDeltas = 1000

// BenchmarkStreamGeneratePartial measures the high-level streaming path over
// 1000 text deltas. before #6411 the per-delta partial publication rebuilt the
// whole accumulated response, making one op quadratic in the delta count.
//
//	no-reader: the streaming path alone, no PartialResponse observer.
//	read-each: a consumer that asks for the partial after every event.
func BenchmarkStreamGeneratePartial(b *testing.B) {
	evs, want := partialStreamEvents(benchmarkStreamDeltas)
	prompt := "hi"

	for _, tc := range []struct {
		name     string
		readEach bool
	}{
		{name: "no-reader"},
		{name: "read-each", readEach: true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				c, _ := newScriptedStreamClient(replayEvents(evs...))
				res, err := StreamGenerate(context.Background(), GenerateOptions{
					Client:   c,
					Model:    "m",
					Provider: "openai",
					Prompt:   &prompt,
				})
				if err != nil {
					b.Fatalf("StreamGenerate: %v", err)
				}
				for ev := range res.Events() {
					if !tc.readEach {
						continue
					}
					_ = ev
					_ = res.PartialResponse()
				}
				resp, err := res.Response()
				if err != nil {
					b.Fatalf("Response: %v", err)
				}
				if resp.Text() != want {
					b.Fatalf("final text mismatch")
				}
			}
		})
	}
}
