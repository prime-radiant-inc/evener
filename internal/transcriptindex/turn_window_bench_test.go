package transcriptindex

import (
	"path/filepath"
	"strconv"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
)

// BenchmarkResolveTurnWindow measures a turn-addressed past-thread read, which
// runs once per request. It resolves the newest turn on a settled index, so the
// selected window is one turn: whatever this costs above a single record is the
// scan resolveTurnWindow does to find it.
func BenchmarkResolveTurnWindow(b *testing.B) {
	header := transcript.Header{SessionID: "s", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"}
	const turns = 2000
	lines := make([]fixtureLine, 0, turns)
	for i := range turns {
		lines = append(lines, entryLine(opens("t"+strconv.Itoa(i), schema.TurnSpanExecution, user("q"))))
	}
	fx := fixture{name: "turn-window-bench", header: header, lines: lines}
	path := writeFixture(b, fx)

	x, err := Open(path, filepath.Join(b.TempDir(), "index"))
	if err != nil {
		b.Fatal(err)
	}
	defer x.Close() //nolint:errcheck // benchmark handle

	selector := appwire.ThreadTurnWindow{Turn: "t" + strconv.Itoa(turns-1)}
	// The first resolve settles the index, outside the measured region.
	if _, err := x.ResolveTurnWindow(selector); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := x.ResolveTurnWindow(selector); err != nil {
			b.Fatal(err)
		}
	}
}
