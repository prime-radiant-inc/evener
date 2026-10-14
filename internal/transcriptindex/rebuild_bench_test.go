package transcriptindex

import (
	"bufio"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

// syntheticTranscript writes a transcript in the shape of the production
// transcripts whose rebuild is slow (issue #4343): ~13KB average entries —
// assistant turns with thinking, text and several tool calls; tool-results
// entries whose outputs dominate the bytes — grouped as legacy adjacency turns
// with hook completions between them, with a new-format execution turn every
// nth turn and communicate calls throughout. It returns the path, the entry
// count and the entry-average size, so a benchmark can report per-entry cost.
func syntheticTranscript(tb testing.TB, turns int) (path string, entries int, size int64) {
	tb.Helper()
	filler := strings.Repeat("line of realistic tool output text; ", 256) // ~11KB
	random := rand.New(rand.NewPCG(1, 2))
	out, err := os.Create(filepath.Join(tb.TempDir(), "session.transcript.jsonl"))
	if err != nil {
		tb.Fatal(err)
	}
	defer func() { _ = out.Close() }()
	w := bufio.NewWriterSize(out, 1<<20)
	write := func(line []byte) {
		tb.Helper()
		if _, err := w.Write(line); err != nil {
			tb.Fatal(err)
		}
		entries++
		size += int64(len(line))
	}
	write(encodeHeader(tb, transcript.Header{SessionID: "th_synthetic", CreatedAt: fixtureClock, ProfileID: "openai", Model: "gpt-test"}))
	entry := func(turn schema.Turn) { write(encodeEntry(tb, entries+1, turn)) }
	turnID := func(i int) string { return fmt.Sprintf("turn_syn_%d", i) }
	for i := 1; i <= turns; i++ {
		question := at(user(fmt.Sprintf("do task %d: %s", i, filler[:300+random.IntN(400)])), i)
		switch {
		case i%13 == 0: // a new-format execution turn
			id := turnID(i)
			entry(opens(id, schema.TurnSpanExecution, question))
			c1, c2 := fmt.Sprintf("call_%d_a", i), fmt.Sprintf("call_%d_b", i)
			entry(inTurn(id, withModel(withUsage(at(assistant(
				thinking(filler[:900]),
				text(filler[:1200]),
				call(c1, "read_file", fmt.Sprintf(`{"path":"src_%d.go","intent":%q}`, i, filler[100:300])),
				call(c2, "grep", `{"pattern":"func","path":"."}`),
			), i*10), 10, 5, 3, 15), "gpt-test")))
			entry(inTurn(id, at(results(
				result(c1, "read_file", filler[:6000]),
				result(c2, "grep", filler[:2500]),
			), i*10+1)))
			entry(inTurn(id, withModel(withUsage(at(assistant(text(filler[:800])), i*10+2), 7, 2, 0, 9), "gpt-next")))
			entry(completion(id, schema.TurnCompleted, i*10+3, 4000))
		case i%3 == 0: // a legacy work turn with tool calls and a communicate
			c1, c2, c3 := fmt.Sprintf("call_%d_a", i), fmt.Sprintf("call_%d_b", i), fmt.Sprintf("call_%d_c", i)
			entry(question)
			entry(withUsage(at(assistant(
				thinking(filler[:1000]),
				text(filler[:1500]),
				call(c1, "read_file", `{"path":"x.go"}`),
				call(c2, "shell", `{"cmd":"make test"}`),
				call(c3, "communicate", `{"message":"status update"}`),
			), i), 10, 5, 3, 15))
			entry(at(results(
				result(c1, "read_file", filler[:8000]),
				result(c2, "shell", filler[:4000]),
				result(c3, "communicate", "delivered"),
			), i))
			entry(at(assistant(text(filler[:700])), i))
			entry(at(standalone(schema.TurnHookCompleted, "hooks ran"), i))
		case i%3 == 1: // a short legacy turn
			entry(question)
			entry(withModel(withUsage(at(assistant(text(filler[:2000])), i), 3, 2, 0, 5), "gpt-test"))
		default: // a legacy turn whose assistant defers a communicate
			c1 := fmt.Sprintf("call_%d_a", i)
			entry(question)
			entry(at(assistant(text(filler[:900]), callRaw(c1, "communicate", `{message: "working on it"}`)), i))
			entry(at(results(result(c1, "communicate", "delivered")), i))
			entry(at(assistant(text(filler[:600])), i))
		}
	}
	if err := w.Flush(); err != nil {
		tb.Fatal(err)
	}
	if err := out.Close(); err != nil {
		tb.Fatal(err)
	}
	return out.Name(), entries, size
}

// BenchmarkRebuild times a full Index.Rebuild of a synthetic transcript, at
// roughly the production shape, for two sizes. b.N rebuilds are timed end to
// end (a fresh build generation each), after Open has warmed the file cache
// with an identical untimed build.
func BenchmarkRebuild(b *testing.B) {
	for _, mb := range []int{50, 200} {
		b.Run(fmt.Sprintf("%dMB", mb), func(b *testing.B) {
			// The generator writes about 8.4KB per turn: 50MB is ~6,000
			// turns, 200MB ~24,000. The benchmark logs the exact size it got.
			turns := mb * 1_000_000 / 8_450
			path, entries, size := syntheticTranscript(b, turns)
			dir := b.TempDir()
			x := openIndex(b, path, filepath.Join(dir, "index"))
			b.Logf("transcript: %d bytes, %d entries (avg %d bytes/entry), %d turns",
				size, entries, size/int64(entries), x.turns.n)
			info, err := os.Stat(path)
			if err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for range b.N {
				if err := x.Rebuild(info.Size()); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(size)*float64(b.N)/b.Elapsed().Seconds()/(1<<20), "MB/s")
			b.ReportMetric(float64(entries)*float64(b.N)/b.Elapsed().Seconds(), "entries/s")
			b.ReportMetric(b.Elapsed().Seconds()/(float64(b.N)*float64(entries))*1000, "ms/entry")
		})
	}
}

// BenchmarkRealTranscriptRebuild times full rebuilds of real transcripts named
// by EVENER_TRANSCRIPT_INDEX_REAL (colon-separated paths), copied first like
// the opt-in real-transcript tests so the original is only read. Opt-in: a
// default test run must not spend minutes rebuilding a production-sized file.
func BenchmarkRealTranscriptRebuild(b *testing.B) {
	list := os.Getenv(realTranscriptsEnv)
	if list == "" {
		b.Skipf("set %s to a colon-separated list of transcript paths", realTranscriptsEnv)
	}
	for source := range strings.SplitSeq(list, ":") {
		b.Run(filepath.Base(source), func(b *testing.B) {
			b.Helper()
			path := copyRealTranscript(b, source)
			info, err := os.Stat(path)
			if err != nil {
				b.Fatal(err)
			}
			dir := b.TempDir()
			// Open's catch-up is itself a full build against an empty sidecar:
			// the production scenario. It is logged, not timed, and warms the
			// file cache for the timed rebuilds below.
			opened := time.Now()
			x := openIndex(b, path, filepath.Join(dir, "index"))
			b.Logf("cold build (Open) took %v for %d bytes, %d entries, %d turns, %d items",
				time.Since(opened), info.Size(), x.meta.Entries, x.turns.n, x.items.n)
			b.ResetTimer()
			for range b.N {
				if err := x.Rebuild(info.Size()); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			seconds := b.Elapsed().Seconds() / float64(b.N)
			b.ReportMetric(float64(info.Size())/seconds/(1<<20), "MB/s")
			b.ReportMetric(float64(x.meta.Entries)/seconds, "entries/s")
			b.ReportMetric(seconds/float64(x.meta.Entries)*1000, "ms/entry")
		})
	}
}

// TestRebuildPastTheBufferLimitMatchesTheReference pins the fallback where a
// record file outgrows the build's in-memory buffer and the table starts
// writing straight through: the records must come out exactly as an ordinary
// build's. Shrinks recordBufferLimit rather than writing a giant file.
func TestRebuildPastTheBufferLimitMatchesTheReference(t *testing.T) {
	previous := recordBufferLimit
	recordBufferLimit = 1024
	t.Cleanup(func() { recordBufferLimit = previous })
	sets := fixtures()
	path := writeFixture(t, sets[len(sets)-1]) // the fixture joining every set's lines
	x := openIndex(t, path, filepath.Join(t.TempDir(), "index"))
	assertAllWindows(t, x, path)
}

// copyRealTranscript copies one transcript into a temp dir, like the opt-in
// real-transcript tests do.
func copyRealTranscript(tb testing.TB, source string) string {
	tb.Helper()
	in, err := os.Open(source)
	if err != nil {
		tb.Fatal(err)
	}
	defer in.Close() //nolint:errcheck // read-only
	path := filepath.Join(tb.TempDir(), filepath.Base(source))
	out, err := os.Create(path)
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		tb.Fatal(err)
	}
	if err := out.Close(); err != nil {
		tb.Fatal(err)
	}
	return path
}
