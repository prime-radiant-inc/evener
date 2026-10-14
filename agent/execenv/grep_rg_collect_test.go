package execenv

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestRgCollectorBoundsPendingBytesAndResumes is MEDIUM 4: the collector is now
// the search's stdout sink, so until a delimiter arrives every write grew c.buf
// — the budget is only checked once a complete record accumulates, so an
// oversized or unterminated record defeated the capture's memory bound. The
// pending buffer must stay bounded, disclose, and resume with the NEXT record.
func TestRgCollectorBoundsPendingBytesAndResumes(t *testing.T) {
	t.Parallel()
	const budget = 1024
	c := newRgRecordCollector("/base", false, 4, budget)
	chunk := []byte(strings.Repeat("a", 256))
	for range 40 { // 10 KiB of pathless bytes, no delimiter
		if _, err := c.Write(chunk); err != nil {
			t.Fatal(err)
		}
		if len(c.buf) > 2*budget {
			t.Fatalf("pending buffer grew to %d bytes, want it bounded near the %d-byte budget", len(c.buf), budget)
		}
	}
	if !c.truncated() {
		t.Fatal("an oversized unterminated record did not disclose truncation")
	}
	// Finish the oversized record, then deliver a well-formed one: the record
	// after an oversized one must still be admitted.
	c.Write([]byte("\x001:hit\n"))
	c.Write([]byte("/base/good.txt\x001:hit\n"))
	c.flush()
	if !strings.Contains(c.retained(), "good.txt") {
		t.Fatalf("the record after an oversized one was swallowed: %q", c.retained())
	}
}

// TestRgCollectorByteBudgetIsPathPrioritized is MEDIUM 1: the byte budget must
// apply the same path priority the block-count limit does, or a byte overflow
// restores the emission-order dependence the collector exists to remove. Two
// records whose combined size exceeds the budget must retain the path-earliest
// one whichever order they arrive in.
func TestRgCollectorByteBudgetIsPathPrioritized(t *testing.T) {
	t.Parallel()
	pad := strings.Repeat("p", 512)
	make := func(name string) []byte { return []byte("/base/" + name + "\x001:hit" + pad + "\n") }
	run := func(first, second string) string {
		c := newRgRecordCollector("/base", false, 4, 700) // holds one record, not two
		c.Write(make(first))
		c.Write(make(second))
		c.flush()
		return c.retained()
	}
	ascending := run("f01.txt", "f02.txt")
	descending := run("f02.txt", "f01.txt")
	if ascending != descending {
		t.Fatalf("byte-budget retention depends on arrival order:\n ascending  = %q\n descending = %q", ascending, descending)
	}
	if !strings.Contains(ascending, "f01.txt") {
		t.Fatalf("the path-earliest record was not retained: %q", ascending)
	}
}

// TestRgCollectorFramesANewlinePathSplitAcrossWrites is MEDIUM 2: a chunk
// boundary inside a newline-bearing pathname (before its NUL) made the partial
// path read as a pathless line appended to the PREVIOUS file's block, so the
// record landed in the wrong block and could sort or retain wrongly. A split
// stream must frame and order exactly as the same stream in one write.
func TestRgCollectorFramesANewlinePathSplitAcrossWrites(t *testing.T) {
	t.Parallel()
	// The second record's pathname holds a newline and sorts BEFORE the first.
	const whole = "/base/zzz.txt\x001:first\n/base/we\nird.txt\x002:second\n"
	c := newRgRecordCollector("/base", false, 8, 1<<20)
	c.Write([]byte(whole))

	split := newRgRecordCollector("/base", false, 8, 1<<20)
	split.Write([]byte("/base/zzz.txt\x001:first\n/base/we\n"))
	split.Write([]byte("ird.txt\x002:second\n"))

	if got, want := split.retained(), c.retained(); got != want {
		t.Fatalf("a pathname split across writes mis-framed:\n split  = %q\n single = %q", got, want)
	}
	if !strings.HasPrefix(split.retained(), "/base/we\nird.txt\x002:second\n") {
		t.Fatalf("the newline-bearing pathname did not sort first: %q", split.retained())
	}
}

// TestRgCollectorPathOrderMatchesTheFormatter is MEDIUM 3: the collector orders
// paths the way ripgrepOutputLines does, including the platform path separator,
// so a record the collector retains is the one the formatter would have put
// first. (On unix the literal '/' and filepath.Separator are the same, so this
// pins the consistency; the divergence it guards is Windows-only.)
func TestRgCollectorPathOrderMatchesTheFormatter(t *testing.T) {
	t.Parallel()
	dir := "/base"
	sep := string(filepath.Separator)
	stream := dir + sep + "ctx.txt\x001:a\n" +
		dir + sep + "ctx0.txt\x001:b\n" +
		dir + sep + "ctx" + sep + "m.txt\x001:c\n" +
		dir + sep + `ctx\m.txt` + "\x001:d\n"
	c := newRgRecordCollector(dir, false, 8, 1<<20)
	c.Write([]byte(stream))
	c.flush()
	viaCollector := ripgrepOutputLines(c.retained(), dir, false, false)
	direct := ripgrepOutputLines(stream, dir, false, false)
	if !slices.Equal(viaCollector, direct) {
		t.Fatalf("collector order disagrees with the formatter:\n collector = %q\n formatter = %q", viaCollector, direct)
	}
	// A directory's files sort before a sibling file sharing its prefix.
	if rgPathOrder("ctx"+sep+"m.txt", "ctx.txt") >= 0 {
		t.Fatalf("rgPathOrder did not put the directory entry first with separator %q", sep)
	}
}
