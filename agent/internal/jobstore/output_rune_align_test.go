package jobstore

import (
	"bytes"
	"path/filepath"
	"testing"
	"unicode/utf8"
)

// openAlignStore opens an unpruned output store holding content, for the
// window-alignment cases below.
func openAlignStore(t *testing.T, content string, capBytes int64) *OutputStore {
	t.Helper()
	o, err := OpenOutputNoSync(filepath.Join(t.TempDir(), "job_align.log"), capBytes)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = o.Close() })
	appendOutput(t, o, content)
	return o
}

// A tail window that lands mid-rune starts on the next rune boundary instead of
// on a UTF-8 continuation byte, so the live jobs panel never opens on a
// replacement character. The window SHRINKS to align, never reads further back:
// the panel derives the retained-start caption from the bytes it receives.
func TestOutputStoreTailAlignsMidRuneWindowStart(t *testing.T) {
	// Two 4-byte emoji: a 6-byte window opens 2 bytes into the first one.
	o := openAlignStore(t, "😀😀", 0)

	data, total, truncated, err := o.Tail(6)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if !utf8.Valid(data) {
		t.Fatalf("tail is not valid UTF-8: %x", data)
	}
	if string(data) != "😀" || total != 8 || !truncated {
		t.Fatalf("tail = (%q, %d, %v), want (😀, 8, true)", data, total, truncated)
	}
}

// A window already on a rune boundary is returned whole, and a window narrower
// than the rune it lands in is empty rather than a lone replacement character.
func TestOutputStoreTailWindowEdges(t *testing.T) {
	o := openAlignStore(t, "😀😀", 0)

	data, _, _, err := o.Tail(4)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if string(data) != "😀" {
		t.Fatalf("aligned window = %q, want 😀", data)
	}
	data, total, truncated, err := o.Tail(2)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if len(data) != 0 || total != 8 || !truncated {
		t.Fatalf("narrow window = (%x, %d, %v), want (empty, 8, true)", data, total, truncated)
	}
}

// A head window that ends mid-rune stops at the last whole rune instead of
// trailing orphaned continuation bytes, so a digest of a live job's output never
// closes on a replacement character.
func TestOutputStoreHeadAlignsMidRuneWindowEnd(t *testing.T) {
	// Two 4-byte emoji: a 6-byte window ends 2 bytes into the second one.
	o := openAlignStore(t, "😀😀", 0)

	data, total, truncated, err := o.Head(6)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if !utf8.Valid(data) {
		t.Fatalf("head is not valid UTF-8: %x", data)
	}
	if string(data) != "😀" || total != 8 || !truncated {
		t.Fatalf("head = (%q, %d, %v), want (😀, 8, true)", data, total, truncated)
	}
}

// A head window already on a rune boundary is returned whole, and a window
// narrower than the rune it lands in is empty rather than a lone replacement
// character. A window that covers the whole retained file is never realigned:
// its last byte is the file's own, so an incomplete trailing sequence survives.
func TestOutputStoreHeadWindowEdges(t *testing.T) {
	o := openAlignStore(t, "😀😀", 0)

	data, _, _, err := o.Head(4)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if string(data) != "😀" {
		t.Fatalf("aligned window = %q, want 😀", data)
	}
	data, _, _, err = o.Head(2)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("narrow window = %x, want empty", data)
	}

	partial := []byte{'a', 'b', 0xF0, 0x9F}
	whole := openAlignStore(t, string(partial), 0)
	data, _, truncated, err := whole.Head(10)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if !bytes.Equal(data, partial) || truncated {
		t.Fatalf("whole file = (%x, %v), want (%x, false)", data, truncated, partial)
	}
}

// Output is not required to be UTF-8, and alignment must not censor it. Only the
// window's own cut is realigned, and only continuation bytes are skipped: a whole
// retained file is returned byte for byte even when it opens on continuation
// bytes, and a window opening on an invalid-but-not-continuation byte keeps it.
func TestOutputStoreTailKeepsInvalidUTF8(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		bytes   int
		want    []byte
	}{
		{
			name:    "whole file, continuation bytes first",
			content: []byte{0x80, 0x81, 0x82, 0x83},
			bytes:   10,
			want:    []byte{0x80, 0x81, 0x82, 0x83},
		},
		{
			name:    "invalid lead byte at the cut",
			content: []byte{'a', 'b', 0xFF, 0xFE, 'c'},
			bytes:   3,
			want:    []byte{0xFF, 0xFE, 'c'},
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
			o := openAlignStore(t, string(tc.content), 0)

			data, _, _, err := o.Tail(tc.bytes)
			if err != nil {
				t.Fatalf("tail: %v", err)
			}
			if !bytes.Equal(data, tc.want) {
				t.Fatalf("tail = %x, want %x", data, tc.want)
			}
		})
	}
}
