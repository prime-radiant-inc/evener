package transcriptindex

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
)

// turnWindowSession writes turns execution turns into path and returns it.
func writeTurnWindowSessionFile(t testing.TB, dir string, turns int) string {
	t.Helper()
	lines := make([]fixtureLine, 0, 2*turns)
	for i := range turns {
		id := fmt.Sprintf("turn_%d", i)
		lines = append(lines,
			entryLine(opens(id, schema.TurnSpanExecution, user(fmt.Sprintf("question %d", i)))),
			entryLine(inTurn(id, assistant(text(fmt.Sprintf("answer %d", i))))),
			entryLine(completion(id, schema.TurnCompleted, i, 1)),
		)
	}
	fx := fixture{header: transcript.Header{SessionID: "turn_window_bench"}, lines: lines}
	header, encoded := fx.encode(t)
	path := filepath.Join(dir, "session.transcript.jsonl")
	if err := os.WriteFile(path, append(header, joinLines(encoded)...), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// BenchmarkResolveTurnWindow times one turn-addressed read of a single turn,
// the constant-sized window the audit's issue names, on sessions whose turn
// counts differ by 8x. The reported allocs and bytes show whether the read's
// cost tracks the window or the session.
func BenchmarkResolveTurnWindow(b *testing.B) {
	for _, turns := range []int{500, 4_000} {
		b.Run(fmt.Sprintf("turns=%d", turns), func(b *testing.B) {
			dir := b.TempDir()
			path := writeTurnWindowSessionFile(b, dir, turns)
			x := openIndex(b, path, filepath.Join(dir, "index"))
			b.Cleanup(func() { _ = x.Close() })
			selector := appwire.ThreadTurnWindow{Turn: fmt.Sprintf("turn_%d", turns/2)}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := x.ResolveTurnWindow(selector); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
