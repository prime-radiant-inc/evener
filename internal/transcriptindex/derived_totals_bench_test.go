package transcriptindex

import (
	"path/filepath"
	"strconv"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// BenchmarkDerivedTotals measures the past-thread read's derived-totals call,
// which runs once per request. It measures repeated calls on a settled index,
// which is exactly the path a reader takes: the memo answers every call after
// the first.
func BenchmarkDerivedTotals(b *testing.B) {
	header := transcript.Header{SessionID: "s", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"}
	usageTurn := func(turn schema.Turn, input, output int) schema.Turn {
		turn.Usage = llm.Usage{InputTokens: input, OutputTokens: output, TotalTokens: input + output}
		return turn
	}
	// Fixture setup is outside the measured region.
	const turns = 2000
	lines := make([]fixtureLine, 0, turns*2)
	for i := range turns {
		id := "t" + strconv.Itoa(i)
		lines = append(lines,
			entryLine(opens(id, schema.TurnSpanExecution, user("q"))),
			entryLine(usageTurn(opens(id, schema.TurnSpanExecution, user("q")), 100, 10)))
	}
	fx := fixture{name: "derived-bench", header: header, lines: lines}
	path := writeFixture(b, fx)

	x, err := Open(path, filepath.Join(b.TempDir(), "index"))
	if err != nil {
		b.Fatal(err)
	}
	defer x.Close() //nolint:errcheck // benchmark handle
	// The first read populates the memo, outside the measured region: the
	// fixture setup must not be charged to the measured call.
	if _, _, err := x.DerivedTotals(0); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, _, err := x.DerivedTotals(0); err != nil {
			b.Fatal(err)
		}
	}
}
