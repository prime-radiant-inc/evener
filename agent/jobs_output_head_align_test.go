package agent

import (
	"os"
	"path/filepath"
	"testing"
	"unicode/utf8"
)

func writeOutputFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "out.log")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write output fixture: %v", err)
	}
	return path
}

// A head window that ends mid-rune stops at the last whole rune instead of
// trailing orphaned continuation bytes, so a digest built from the head never
// closes on a replacement character.
func TestHeadOutputFileAlignsMidRuneWindowEnd(t *testing.T) {
	t.Parallel()
	// Two 4-byte emoji: a 6-byte window ends 2 bytes into the second one.
	path := writeOutputFixture(t, "😀😀")

	out, total, truncated, err := headOutputFile(path, 6, 8)
	if err != nil {
		t.Fatalf("headOutputFile: %v", err)
	}
	if !utf8.ValidString(out) {
		t.Fatalf("head is not valid UTF-8: %x", out)
	}
	if out != "😀" || total != 8 || !truncated {
		t.Fatalf("head = (%q, %d, %v), want (😀, 8, true)", out, total, truncated)
	}
}

// A window already on a rune boundary is returned whole, ASCII is byte-identical
// at every window size, and a window narrower than the rune it lands in is EMPTY
// rather than a lone replacement character.
func TestHeadOutputFileWindowEdges(t *testing.T) {
	t.Parallel()
	path := writeOutputFixture(t, "😀😀")

	out, total, truncated, err := headOutputFile(path, 4, 8)
	if err != nil {
		t.Fatalf("headOutputFile: %v", err)
	}
	if out != "😀" || total != 8 || !truncated {
		t.Fatalf("aligned head = (%q, %d, %v), want (😀, 8, true)", out, total, truncated)
	}

	out, _, _, err = headOutputFile(writeOutputFixture(t, "😀"), 2, 4)
	if err != nil {
		t.Fatalf("headOutputFile: %v", err)
	}
	if out != "" {
		t.Fatalf("narrow head = %q, want empty", out)
	}

	const ascii = "abcdefghij"
	asciiPath := writeOutputFixture(t, ascii)
	for n := 0; n <= len(ascii)+2; n++ {
		want := ascii
		if n < len(ascii) {
			want = ascii[:n]
		}
		out, _, _, err := headOutputFile(asciiPath, n, int64(len(ascii)))
		if err != nil {
			t.Fatalf("headOutputFile(%d): %v", n, err)
		}
		if out != want {
			t.Fatalf("head(%d) = %q, want %q", n, out, want)
		}
	}
}

// Job output is not required to be UTF-8, and only the window's own cut is
// realigned: a whole retained file comes back byte for byte even when its last
// bytes are an incomplete sequence, because we never cut there.
func TestHeadOutputFileKeepsWholeFileIntact(t *testing.T) {
	t.Parallel()
	content := []byte{'a', 'b', 0xF0, 0x9F}
	path := writeOutputFixture(t, string(content))

	out, total, truncated, err := headOutputFile(path, 10, 4)
	if err != nil {
		t.Fatalf("headOutputFile: %v", err)
	}
	if out != string(content) || total != 4 || truncated {
		t.Fatalf("head = (%x, %d, %v), want (%x, 4, false)", out, total, truncated, content)
	}
}
