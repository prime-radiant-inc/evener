package agent

import (
	"bytes"
	"testing"
)

// The job-output digest renderer slices line windows out of a byte buffer with
// three helpers in job_output_digest.go:
//
//   - firstLineBytes(b, n)     — the first n lines.
//   - lastLineBytes(b, n)      — the last n lines.
//   - midLineBytes(b, from, n) — the n lines starting at 1-based `from`.
//
// firstLineBytes is a specialized re-implementation of the window midLineBytes
// computes at from==1: for n >= 1 the two MUST return the same slice, the same
// line count, and the same "are there more lines after the window" flag
// (firstLineBytes.more == midLineBytes.after). They are separate code with
// separate scanning loops (forward index walk vs the windowed walk), so a change
// to one can silently drift from the other — corrupting the head shown in every
// truncated job/shell output digest. The existing job_output_digest_test.go
// pins example rows for firstLineBytes/lastLineBytes but never cross-checks them
// against the general midLineBytes primitive, and midLineBytes has no direct
// test at all. This is that differential, plus a windowing-covers-everything
// invariant for midLineBytes itself.

// FuzzLineWindowExtractors drives the line-window helpers from one fuzzed buffer
// and window size and asserts:
//
//   - first-window differential (n >= 1): firstLineBytes(b, n) equals
//     midLineBytes(b, 1, n) on slice bytes, line count, and the more/after flag.
//   - midLineBytes tiling coverage (n >= 1): walking midLineBytes(b, from, n)
//     forward by the lines each window actually returns reconstructs b exactly
//     (every byte once, in order), terminates when `after` clears, and the
//     `before` flag is true exactly when from > 1 on a non-empty buffer.
//
// ALLOW-LIST — NOT treated as divergence:
//   - n <= 0 (degenerate window): firstLineBytes returns more=(len(b)>0) while
//     midLineBytes returns after=false for count<1; both yield an empty slice.
//     A non-positive window is never requested by the digest code (head/tail line
//     counts are always positive), so the differential is asserted only for
//     n >= 1, matching the helpers' real call sites.
func FuzzLineWindowExtractors(f *testing.F) {
	seeds := [][]byte{
		nil,
		[]byte(""),
		[]byte("a"),
		[]byte("a\n"),
		[]byte("a\nb\nc"),
		[]byte("a\nb\nc\n"),
		[]byte("\n\n\n"),
		[]byte("line1\nline2\nline3\nline4\n"),
		bytes.Repeat([]byte("x\n"), 50),
	}
	for _, s := range seeds {
		// Pack a window size into the first byte's low bits so the fuzzer can
		// steer n; the rest is the buffer.
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Derive a window size in [0, 7] from the trailing byte and use the rest
		// as the buffer, so one fuzz input drives both dimensions.
		var b []byte
		n := 1
		if len(data) > 0 {
			n = int(data[len(data)-1]) % 8
			b = data[:len(data)-1]
		}

		if n >= 1 {
			fs, fl, fmore := firstLineBytes(b, n)
			ms, ml, mbefore, mafter := midLineBytes(b, 1, n)
			if !bytes.Equal(fs, ms) || fl != ml || fmore != mafter {
				t.Fatalf("firstLineBytes vs midLineBytes(_,1,_) diverged for b=%q n=%d:\n  first=(%q,%d,more=%v)\n  mid  =(%q,%d,after=%v)",
					b, n, fs, fl, fmore, ms, ml, mafter)
			}
			if mbefore {
				t.Fatalf("midLineBytes(_,1,_) reported before=true (b=%q n=%d): there is nothing before line 1", b, n)
			}
		}

		assertMidLineTilingCoversAll(t, b, max(n, 1))
	})
}

// assertMidLineTilingCoversAll walks midLineBytes(b, from, n) forward, advancing
// `from` by the number of lines each window actually returned, and asserts the
// concatenated windows reproduce b exactly, that paging stops when `after`
// clears, and that `before` is set exactly when from > 1 over a non-empty buffer.
func assertMidLineTilingCoversAll(t *testing.T, b []byte, n int) {
	t.Helper()
	var got []byte
	from := 1
	// Each window yields >= 1 line until exhaustion; #lines bounds the loop.
	guard := bytes.Count(b, []byte("\n")) + 3
	for i := 0; ; i++ {
		if i > guard {
			t.Fatalf("midLineBytes tiling did not terminate within %d windows (b=%q n=%d)", guard, b, n)
		}
		slice, lines, before, after := midLineBytes(b, from, n)
		wantBefore := from > 1 && len(b) > 0
		if before != wantBefore {
			t.Fatalf("midLineBytes before flag = %v, want %v (b=%q from=%d n=%d)", before, wantBefore, b, from, n)
		}
		if lines == 0 {
			// No lines at this offset: must be at/after the end and report no more.
			if after {
				t.Fatalf("midLineBytes returned 0 lines but after=true (b=%q from=%d n=%d)", b, from, n)
			}
			break
		}
		got = append(got, slice...)
		if !after {
			break
		}
		from += lines
	}
	if !bytes.Equal(got, b) {
		t.Fatalf("midLineBytes tiling did not reconstruct the buffer:\n  got =%q\n  want=%q\n  (n=%d)", got, b, n)
	}
}

// TestLineWindowExtractorsSanity is a fast, explicit seed check (no fuzzing): a
// fixed multi-line buffer must satisfy both the first-window differential and
// the midLineBytes tiling invariant.
func TestLineWindowExtractorsSanity(t *testing.T) {
	b := []byte("alpha\nbravo\ncharlie\ndelta")
	for _, n := range []int{1, 2, 3, 4, 10} {
		fs, fl, fmore := firstLineBytes(b, n)
		ms, ml, _, mafter := midLineBytes(b, 1, n)
		if !bytes.Equal(fs, ms) || fl != ml || fmore != mafter {
			t.Fatalf("n=%d: first=(%q,%d,%v) mid=(%q,%d,%v)", n, fs, fl, fmore, ms, ml, mafter)
		}
		assertMidLineTilingCoversAll(t, b, n)
	}
}
