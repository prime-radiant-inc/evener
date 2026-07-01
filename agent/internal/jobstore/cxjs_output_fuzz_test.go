package jobstore

import (
	"bufio"
	"bytes"
	"errors"
	"regexp"
	"strings"
	"testing"

	"primeradiant.com/serf/fuzz/oracle"
)

// This file fuzzes grepReaderLimit and its nearby line helpers in output.go —
// the bounded, line-by-line grep engine underneath OutputStore.Grep. It drives
// fuzzed log bytes and a fuzzed regexp/limit/line-cap through the reader core
// without touching disk, plus a fault reader that forces the read-error branch.
//
// New top-level identifiers are prefixed with the lane token "cxjs_" so this
// file never collides with a sibling fuzz lane in the same package.

// cxjs_grepPatterns is a small fixed set of safe, linear-time (RE2) patterns the
// fuzzer selects among when the fuzzed pattern bytes fail to compile, so the
// engine is always exercised against a valid regexp.
var cxjs_grepPatterns = []*regexp.Regexp{
	regexp.MustCompile(`ready`),
	regexp.MustCompile(`(?i)error`),
	regexp.MustCompile(`^`),
	regexp.MustCompile(`\d+`),
	regexp.MustCompile(`^done$`),
	regexp.MustCompile(`.`),
}

// cxjs_pickRegexp compiles the fuzzed pattern bytes (bounded), falling back to a
// deterministic member of the fixed set when they are empty or invalid.
func cxjs_pickRegexp(pat []byte, sel uint8) (*regexp.Regexp, bool) {
	if len(pat) > 0 && len(pat) <= 48 {
		if re, err := regexp.Compile(string(pat)); err == nil {
			return re, true
		}
	}
	return cxjs_grepPatterns[int(sel)%len(cxjs_grepPatterns)], true
}

// cxjs_errReader yields its bytes across Read calls, then returns a non-EOF
// sentinel error so grepReaderLimit takes the read-error branch. It honors the
// io.Reader contract: it returns available bytes with a nil error, and only
// returns the error once the buffer is drained.
type cxjs_errReader struct {
	b []byte
	i int
}

var errCxjsReadFault = errors.New("cxjs: injected read fault")

func (r *cxjs_errReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, errCxjsReadFault
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

// cxjs_effMaxLine mirrors grepReaderLimit's normalization of maxLineBytes so the
// oracle can bound returned line lengths correctly.
func cxjs_effMaxLine(limitBytes, maxLineBytes int) int {
	if maxLineBytes <= 0 || maxLineBytes > limitBytes {
		return limitBytes
	}
	return maxLineBytes
}

// FuzzCxjsGrepReaderLimit drives grepReaderLimit over fuzzed log bytes with a
// fuzzed regexp, byte budget, match cap, and per-line cap.
//
// Oracles (never a bare no-panic):
//   - determinism: identical bytes + params → identical matches.
//   - bounded output: total matched-line bytes <= limitBytes, and the match
//     count honors maxMatches when positive.
//   - every returned line actually matches the regexp, contains no newline, and
//     is no longer than the effective per-line cap.
//   - offset consistency: byte offsets are non-decreasing and within the input,
//     and the line reconstructed at each offset equals the returned line.
func FuzzCxjsGrepReaderLimit(f *testing.F) {
	f.Add([]byte("ready one\nnot me\nready two\n"), []byte("ready"), uint8(0), 4096, 0, 4096)
	f.Add([]byte("error: boom\r\nok\r\nerror: again\r\n"), []byte("(?i)error"), uint8(1), 64, 2, 32)
	f.Add([]byte("a\nbb\nccc\ndddd"), []byte("^"), uint8(2), 3, 0, 0)
	f.Add([]byte(""), []byte(""), uint8(0), 16, 1, -1)
	f.Add([]byte(strings.Repeat("x", 200)+"\nshort\n"), []byte("short"), uint8(0), 100, 0, 8)
	f.Add([]byte("no newline at eof ready"), []byte("ready"), uint8(0), 4096, 0, 4096)

	f.Fuzz(func(t *testing.T, data, pat []byte, patSel uint8, limitBytes, maxMatches, maxLineBytes int) {
		// Bound the numeric knobs so the fuzzer can't drive gigantic allocations
		// or plateau on identical huge budgets. grepReaderLimit's callers
		// (GrepLimitLineBytes / GrepFileLimitAt) guarantee limitBytes >= 1 before
		// reaching it, so the harness honors that precondition. maxMatches and
		// maxLineBytes still span their guard values (0, negative, > limit).
		limitBytes = cxjs_boundInt(limitBytes, 1, 1<<16)
		maxMatches = cxjs_boundInt(maxMatches, -4, 4096)
		maxLineBytes = cxjs_boundInt(maxLineBytes, -4, 1<<16)
		if len(data) > 1<<16 {
			data = data[:1<<16]
		}

		re, _ := cxjs_pickRegexp(pat, patSel)

		grep := func() ([]Match, error) {
			return grepReaderLimit(bufio.NewReader(bytes.NewReader(data)), re, limitBytes, maxMatches, maxLineBytes)
		}

		matches, err := grep()

		// The byte-reader never errors (only EOF), so a non-nil error here would
		// be a real bug from the engine itself.
		if err != nil {
			t.Fatalf("grepReaderLimit over a byte reader errored: %v", err)
		}

		// Determinism.
		matches2, err2 := grep()
		if err2 != nil || !oracle.DeepEqual(matches, matches2) {
			t.Fatalf("grepReaderLimit not deterministic: err2=%v", err2)
		}

		effMax := cxjs_effMaxLine(limitBytes, maxLineBytes)
		total := 0
		prevOffset := int64(-1)
		for _, m := range matches {
			if !re.MatchString(m.Line) {
				t.Fatalf("returned line does not match regexp: %q", m.Line)
			}
			if strings.ContainsRune(m.Line, '\n') {
				t.Fatalf("returned line contains a newline: %q", m.Line)
			}
			if len(m.Line) > effMax {
				t.Fatalf("returned line %d bytes exceeds effective cap %d", len(m.Line), effMax)
			}
			if m.ByteOffset < 0 || m.ByteOffset > int64(len(data)) {
				t.Fatalf("offset %d out of bounds [0,%d]", m.ByteOffset, len(data))
			}
			if m.ByteOffset <= prevOffset {
				t.Fatalf("offsets not strictly increasing: %d after %d", m.ByteOffset, prevOffset)
			}
			prevOffset = m.ByteOffset
			if got := cxjs_lineAt(data, m.ByteOffset); got != m.Line {
				t.Fatalf("line at offset %d is %q, want %q", m.ByteOffset, got, m.Line)
			}
			total += len(m.Line)
		}
		if total > limitBytes {
			t.Fatalf("total matched bytes %d exceeds limit %d", total, limitBytes)
		}
		if maxMatches > 0 && len(matches) > maxMatches {
			t.Fatalf("returned %d matches exceeds cap %d", len(matches), maxMatches)
		}

		// Fault path: a reader that fails after draining its bytes must never
		// panic and must behave deterministically. When grep reads past the
		// buffered bytes (no early budget/match stop) it surfaces the injected
		// error; when it stops early it returns the same matches as the clean
		// run. Either way the outcome is stable and any error is the injected one.
		faultGrep := func() ([]Match, error) {
			return grepReaderLimit(bufio.NewReader(&cxjs_errReader{b: data}), re, limitBytes, maxMatches, maxLineBytes)
		}
		fm, ferr := faultGrep()
		if ferr != nil && !errors.Is(ferr, errCxjsReadFault) {
			t.Fatalf("unexpected fault error: %v", ferr)
		}
		if ferr == nil && !oracle.DeepEqual(fm, matches) {
			t.Fatalf("fault reader that stopped early disagreed with the clean run")
		}
		fm2, ferr2 := faultGrep()
		if (ferr == nil) != (ferr2 == nil) || (ferr == nil && !oracle.DeepEqual(fm, fm2)) {
			t.Fatalf("fault path nondeterministic")
		}

		cxjs_checkLineContentLen(t, data)
	})
}

// cxjs_checkLineContentLen pins logicalLineContentLen: an empty fragment leaves
// the accumulated length unchanged, and for a single fragment the content length
// matches an independent computation that strips one trailing "\n", "\r\n", or
// "\r".
func cxjs_checkLineContentLen(t *testing.T, frag []byte) {
	if got := logicalLineContentLen(frag, nil); got != len(frag) {
		t.Fatalf("logicalLineContentLen(line, nil)=%d, want %d", got, len(frag))
	}
	oracle.AgreesWith(t,
		func(b []byte) int { return logicalLineContentLen(nil, b) },
		cxjs_naiveContentLen,
		frag,
		func(a, b int) bool { return a == b },
	)
}

// cxjs_naiveContentLen is the reference: the byte count excluding one trailing
// line terminator ("\n", "\r\n", or a bare "\r").
func cxjs_naiveContentLen(b []byte) int {
	n := len(b)
	if n == 0 {
		return 0
	}
	if b[n-1] == '\n' {
		n--
		if n > 0 && b[n-1] == '\r' {
			n--
		}
		return n
	}
	if b[n-1] == '\r' {
		return n - 1
	}
	return n
}

// cxjs_lineAt reconstructs the logical line beginning at off in data: the bytes
// up to the next '\n', with a single trailing "\r\n"/"\n"/"\r" stripped.
func cxjs_lineAt(data []byte, off int64) string {
	if off < 0 || off > int64(len(data)) {
		return ""
	}
	rest := data[off:]
	if i := bytes.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[:i+1]
	}
	line := rest
	if len(line) > 0 && line[len(line)-1] == '\n' {
		line = line[:len(line)-1]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
	}
	return string(line)
}

func cxjs_boundInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
