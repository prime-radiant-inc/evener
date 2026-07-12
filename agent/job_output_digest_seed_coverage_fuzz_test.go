//go:build serffuzz

package agent

import (
	"errors"
	"math"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzJobOutputDigestSeedCoverage keeps a deterministic seed that exercises
// every reachable statement in the byte- and line-oriented digest helpers.
func FuzzJobOutputDigestSeedCoverage(f *testing.F) {
	f.Add([]byte("seed"))
	f.Fuzz(func(t *testing.T, data []byte) {
		assertJobOutputDigestContracts(t, data)
	})
}

func assertJobOutputDigestContracts(t *testing.T, data []byte) {
	t.Helper()

	continuations := []byte{0x80, 0x81}
	if got := trimTrailingPartialRune(continuations); string(got) != string(continuations) {
		t.Fatalf("all-continuation trailing slice changed: %x", got)
	}
	if got := trimTrailingPartialRune([]byte{'a', 0xe2, 0x82}); string(got) != "a" {
		t.Fatalf("incomplete trailing rune = %x, want 61", got)
	}
	if got := trimTrailingPartialRune([]byte("ok")); string(got) != "ok" {
		t.Fatalf("complete trailing rune changed: %q", got)
	}
	if got := trimLeadingPartialRune(append(append([]byte{}, continuations...), 'x')); string(got) != "x" {
		t.Fatalf("leading continuation bytes not trimmed: %x", got)
	}

	clamped := assembleOutputDigest([]byte("head"), []byte("tail"), 1, 0)
	if !strings.HasPrefix(clamped, "head\n") || !strings.Contains(clamped, "0 B elided") {
		t.Fatalf("clamped digest = %q", clamped)
	}
	dropped := assembleOutputDigest(nil, nil, 1, 1)
	if !strings.Contains(dropped, "permanently dropped") {
		t.Fatalf("dropped digest = %q", dropped)
	}
	for n, want := range map[int64]string{
		0:                         "0 B",
		1024:                      "1.0 KB",
		1024 * 1024:               "1.0 MB",
		1024 * 1024 * 1024:        "1.0 GB",
		1024 * 1024 * 1024 * 1024: "1.0 TB",
		math.MaxInt64:             "8388608.0 TB",
	} {
		if got := humanBytes(n); got != want {
			t.Fatalf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}

	short := string(data)
	if got := shellInlineDigest(short, int64(len(short)), 0); !utf8.ValidString(got) && utf8.ValidString(short) {
		t.Fatalf("short digest invalidated UTF-8: %x", got)
	}
	longRune := strings.Repeat("x", shellDigestHalfBytes-1) + "€" + strings.Repeat("y", shellDigestHalfBytes)
	if got := shellInlineDigest(longRune, int64(len(longRune)), 0); !utf8.ValidString(got) {
		t.Fatalf("long digest contains partial UTF-8: %x", got)
	}
	longLines := strings.Repeat("a\n", shellDigestHalfBytes+1)
	if got := shellInlineDigest(longLines, int64(len(longLines)), 0); !strings.Contains(got, "elided") {
		t.Fatalf("long line digest lacks marker: %q", got)
	}

	errHead := errors.New("head")
	if _, err := readJobOutputDigest(seedDigestReader(jobReadOutputSnapshot{}, jobReadOutputSnapshot{}, errHead, nil), 1, 1); !errors.Is(err, errHead) {
		t.Fatalf("head error = %v", err)
	}
	whole := jobReadOutputSnapshot{Content: "one"}
	if got, err := readJobOutputDigest(seedDigestReader(whole, jobReadOutputSnapshot{}, nil, nil), 2, 2); err != nil || got.Content != whole.Content || got.Truncated != whole.Truncated {
		t.Fatalf("whole read = %#v, %v", got, err)
	}
	overlap := jobReadOutputSnapshot{Content: "a\nb\n", TotalBytes: 4}
	if got, err := readJobOutputDigest(seedDigestReader(overlap, jobReadOutputSnapshot{}, nil, nil), 1, 1); err != nil || got.Content != overlap.Content || got.Truncated != overlap.Truncated {
		t.Fatalf("overlap read = %#v, %v", got, err)
	}
	separate := jobReadOutputSnapshot{Content: "a\nb\nc\n", TotalBytes: 6}
	if got, err := readJobOutputDigest(seedDigestReader(separate, jobReadOutputSnapshot{}, nil, nil), 1, 1); err != nil || !got.Truncated {
		t.Fatalf("single-buffer digest = %#v, %v", got, err)
	}
	head := jobReadOutputSnapshot{Content: "a\nb\n", Truncated: true}
	tail := jobReadOutputSnapshot{Content: "y\nz\n", TotalBytes: 8, DroppedBytes: 1}
	if got, err := readJobOutputDigest(seedDigestReader(head, tail, nil, nil), 1, 1); err != nil || !got.Truncated || !strings.Contains(got.Content, "z") {
		t.Fatalf("two-buffer digest = %#v, %v", got, err)
	}
	errTail := errors.New("tail")
	if _, err := readJobOutputDigest(seedDigestReader(head, tail, nil, errTail), 1, 1); !errors.Is(err, errTail) {
		t.Fatalf("tail error = %v", err)
	}

	if got, lines, before, after := midLineBytes([]byte("a\nb"), 0, 1); string(got) != "a\n" || lines != 1 || before || !after {
		t.Fatalf("clamped mid window = (%q,%d,%v,%v)", got, lines, before, after)
	}
	if got, lines, before, after := midLineBytes([]byte("a"), 2, 0); got != nil || lines != 0 || !before || after {
		t.Fatalf("empty mid window = (%q,%d,%v,%v)", got, lines, before, after)
	}
	if got, lines, before, after := midLineBytes([]byte("a"), 3, 1); got != nil || lines != 0 || !before || after {
		t.Fatalf("past-end mid window = (%q,%d,%v,%v)", got, lines, before, after)
	}
	if got, lines, before, after := midLineBytes(nil, 1, 1); got != nil || lines != 0 || before || after {
		t.Fatalf("nil mid window = (%q,%d,%v,%v)", got, lines, before, after)
	}

	for _, tc := range []struct {
		b string
		n int
	}{
		{"", 1}, {"a", 0}, {"a", 1}, {"a\n", 1}, {"a\nb", 1},
	} {
		firstLineBytes([]byte(tc.b), tc.n)
		lastLineBytes([]byte(tc.b), tc.n)
	}
}

func seedDigestReader(head, tail jobReadOutputSnapshot, headErr, tailErr error) func(int, bool) (jobReadOutputSnapshot, error) {
	return func(_ int, fromHead bool) (jobReadOutputSnapshot, error) {
		if fromHead {
			return head, headErr
		}
		return tail, tailErr
	}
}
