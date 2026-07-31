package agent

import (
	"testing"
	"unicode/utf8"
)

// A tail window that lands mid-rune starts on the next rune boundary instead of
// on a UTF-8 continuation byte, and the reported offsets describe the bytes
// actually returned: the panel caption counts totalBytes - retainedStart, and
// jobOutputTailFrom derives retainedStart from the returned content, so the
// shrunken window and the caption stay in agreement.
func TestTailOutputFileAlignsMidRuneWindowStart(t *testing.T) {
	t.Parallel()
	// Two 4-byte emoji: a 6-byte window starts 2 bytes into the first one.
	path := w2dlg_writeFile(t, "😀😀")

	out, total, truncated, err := tailOutputFile(path, 6, 8)
	if err != nil {
		t.Fatalf("tailOutputFile: %v", err)
	}
	if !utf8.ValidString(out) {
		t.Fatalf("tail is not valid UTF-8: %x", out)
	}
	if r, _ := utf8.DecodeRuneInString(out); r == utf8.RuneError {
		t.Fatalf("tail starts with a replacement character: %x", out)
	}
	if out != "😀" || total != 8 || !truncated {
		t.Fatalf("tail = (%q, %d, %v), want (😀, 8, true)", out, total, truncated)
	}
	projected := jobOutputTailFrom(out, total, truncated)
	if projected.RetainedStart != 4 || projected.TotalBytes-projected.RetainedStart != int64(len(out)) {
		t.Fatalf("projection = %+v, want retainedStart 4 describing %d returned bytes", projected, len(out))
	}
}
