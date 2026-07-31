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

// A window that already begins on a rune boundary is returned whole: alignment
// costs a window nothing when there is nothing to align.
func TestTailOutputFileWindowOnRuneBoundaryUnchanged(t *testing.T) {
	t.Parallel()
	path := w2dlg_writeFile(t, "😀😀")

	out, total, truncated, err := tailOutputFile(path, 4, 8)
	if err != nil {
		t.Fatalf("tailOutputFile: %v", err)
	}
	if out != "😀" || total != 8 || !truncated {
		t.Fatalf("tail = (%q, %d, %v), want (😀, 8, true)", out, total, truncated)
	}
	if got := jobOutputTailFrom(out, total, truncated).RetainedStart; got != 4 {
		t.Fatalf("retainedStart = %d, want 4", got)
	}
}

// Pure ASCII output is byte-identical at every window size: every byte is a rune
// start, so no window can lose one.
func TestTailOutputFileASCIIWindowsByteIdentical(t *testing.T) {
	t.Parallel()
	const content = "abcdefghij"
	path := w2dlg_writeFile(t, content)

	for n := 0; n <= len(content)+2; n++ {
		want := content
		if n < len(content) {
			want = content[len(content)-n:]
		}
		out, _, _, err := tailOutputFile(path, n, int64(len(content)))
		if err != nil {
			t.Fatalf("tailOutputFile(%d): %v", n, err)
		}
		if out != want {
			t.Fatalf("tail(%d) = %q, want %q", n, out, want)
		}
	}
}

// A window narrower than the rune it lands in yields an EMPTY tail with honest
// offsets, rather than a lone replacement character: retainedStart equals the
// total, so the caption reads "0 of 4 bytes" and the content agrees.
func TestTailOutputFileWindowNarrowerThanRuneIsEmpty(t *testing.T) {
	t.Parallel()
	path := w2dlg_writeFile(t, "😀")

	out, total, truncated, err := tailOutputFile(path, 2, 4)
	if err != nil {
		t.Fatalf("tailOutputFile: %v", err)
	}
	if out != "" || total != 4 || !truncated {
		t.Fatalf("tail = (%q, %d, %v), want (\"\", 4, true)", out, total, truncated)
	}
	projected := jobOutputTailFrom(out, total, truncated)
	if projected.RetainedStart != 4 || projected.TotalBytes-projected.RetainedStart != 0 {
		t.Fatalf("projection = %+v, want retainedStart 4 describing 0 returned bytes", projected)
	}
}

// Binary output is legal here, and alignment must not censor it. It skips
// CONTINUATION bytes at the window start and nothing else: a window opening on a
// byte that is invalid UTF-8 but not a continuation byte passes through whole, a
// whole retained file that happens to start with continuation bytes is never
// cut (we did not make that cut), and a run of continuation bytes longer than
// any rune could leave loses only the three a 4-byte rune could account for.
func TestTailOutputFileKeepsInvalidUTF8(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content []byte
		bytes   int
		want    []byte
	}{
		{
			name:    "invalid lead byte at the cut",
			content: []byte{'a', 'b', 0xFF, 0xFE, 'c'},
			bytes:   3,
			want:    []byte{0xFF, 0xFE, 'c'},
		},
		{
			name:    "whole file fits, continuation bytes first",
			content: []byte{0x80, 0x81, 0x82, 0x83},
			bytes:   10,
			want:    []byte{0x80, 0x81, 0x82, 0x83},
		},
		{
			name:    "continuation run longer than a rune keeps the rest",
			content: []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80},
			bytes:   6,
			want:    []byte{0x80, 0x80, 0x80},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := w2dlg_writeFile(t, string(tc.content))
			total := int64(len(tc.content))

			out, gotTotal, truncated, err := tailOutputFile(path, tc.bytes, total)
			if err != nil {
				t.Fatalf("tailOutputFile: %v", err)
			}
			if out != string(tc.want) {
				t.Fatalf("tail = %x, want %x", out, tc.want)
			}
			projected := jobOutputTailFrom(out, gotTotal, truncated)
			if projected.TotalBytes-projected.RetainedStart != int64(len(out)) {
				t.Fatalf("projection = %+v, want the caption to count the %d returned bytes", projected, len(out))
			}
		})
	}
}
