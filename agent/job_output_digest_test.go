package agent

import (
	"fmt"
	"strings"
	"testing"
)

func TestShellInlineDigestWholeLines(t *testing.T) {
	t.Parallel()
	// Lines whose boundaries do not align with the per-side byte budget, so a naive
	// byte cut would leave a partial line fragment at the head's end / tail's start.
	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, fmt.Sprintf("line%03d: %s", i, strings.Repeat("x", 23)))
	}
	full := strings.Join(lines, "\n") + "\n"
	orig := map[string]bool{}
	for _, l := range lines {
		orig[l] = true
	}

	d := shellInlineDigest(full, int64(len(full)), 0)

	// Split into the head (before the elision marker) and tail (after it). Every
	// rendered line must be a complete original line — no mid-line fragments.
	head, rest, _ := strings.Cut(d, "…[")
	_, tail, _ := strings.Cut(rest, "]…\n")
	for _, section := range []string{head, tail} {
		for _, l := range strings.Split(strings.Trim(section, "\n"), "\n") {
			if l == "" {
				continue
			}
			if !orig[l] {
				t.Fatalf("digest contains a non-whole line fragment: %q\nfull digest:\n%s", l, d)
			}
		}
	}
}

func TestAssembleOutputDigest(t *testing.T) {
	t.Parallel()
	head := []byte("h1\nh2\n")
	tail := []byte("t1\nt2\n")

	// Retained, nothing evicted: marker states EXACT elided bytes (total-head-tail)
	// plus a recovery hint, and never a fabricated line-count estimate. The store is
	// byte-oriented and does not track total lines, so any line figure would be a
	// guess that can exceed the true count.
	got := assembleOutputDigest(head, tail, 1000, 0)
	if !strings.HasPrefix(got, "h1\nh2\n") || !strings.HasSuffix(got, "t1\nt2\n") {
		t.Fatalf("digest must bracket head and tail:\n%s", got)
	}
	if !strings.Contains(got, "elided") || !strings.Contains(got, "read_transcript") || !strings.Contains(got, "transcript_ref") {
		t.Fatalf("digest must carry an elision marker + recovery hint:\n%s", got)
	}
	if !strings.Contains(got, "988 B") {
		t.Fatalf("digest must state exact elided bytes (1000-6-6=988):\n%s", got)
	}
	if strings.Contains(got, "~") {
		t.Fatalf("digest must not fabricate a line-count estimate:\n%s", got)
	}
	if strings.Contains(got, "permanently dropped") {
		t.Fatalf("no eviction here; marker must not claim permanent loss:\n%s", got)
	}

	// Evicted: marker must flag permanent loss past the cap, still with no estimate.
	got = assembleOutputDigest(head, tail, 9_000_000, 1_000_000)
	if !strings.Contains(got, "permanently dropped") {
		t.Fatalf("evicted digest must flag permanent loss:\n%s", got)
	}
	if strings.Contains(got, "~") {
		t.Fatalf("evicted digest must not fabricate a line-count estimate:\n%s", got)
	}
}

func TestFirstLineBytes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		in       string
		n        int
		want     string
		wantN    int
		wantMore bool
	}{
		{"two of five", "l1\nl2\nl3\nl4\nl5\n", 2, "l1\nl2\n", 2, true},
		{"all when n exceeds", "l1\nl2\n", 5, "l1\nl2\n", 2, false},
		{"no trailing newline", "l1\nl2", 1, "l1\n", 1, true},
		{"single partial line", "only", 3, "only", 1, false},
		{"empty", "", 2, "", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, n, more := firstLineBytes([]byte(c.in), c.n)
			if string(got) != c.want || n != c.wantN || more != c.wantMore {
				t.Fatalf("firstLineBytes(%q,%d) = (%q,%d,%v), want (%q,%d,%v)", c.in, c.n, got, n, more, c.want, c.wantN, c.wantMore)
			}
		})
	}
}

func TestLastLineBytes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		in       string
		n        int
		want     string
		wantN    int
		wantMore bool
	}{
		{"two of five", "l1\nl2\nl3\nl4\nl5\n", 2, "l4\nl5\n", 2, true},
		{"all when n exceeds", "l1\nl2\n", 5, "l1\nl2\n", 2, false},
		{"no trailing newline", "l1\nl2", 1, "l2", 1, true},
		{"single partial line", "only", 3, "only", 1, false},
		{"empty", "", 2, "", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, n, more := lastLineBytes([]byte(c.in), c.n)
			if string(got) != c.want || n != c.wantN || more != c.wantMore {
				t.Fatalf("lastLineBytes(%q,%d) = (%q,%d,%v), want (%q,%d,%v)", c.in, c.n, got, n, more, c.want, c.wantN, c.wantMore)
			}
		})
	}
}
