package jobstore

import (
	"bytes"
	"os"
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

// The retention cap evicts whole runes: the retained file's FIRST byte is the
// file's own, and read-time alignment deliberately does not paper over it, so
// the pruner must not leave the cut inside a rune.
func TestOutputStorePruneCutsOnRuneBoundary(t *testing.T) {
	// Two 4-byte emoji under a 6-byte cap: the raw cut lands 2 bytes into the
	// first one, so the retained file would open on a continuation byte.
	path := filepath.Join(t.TempDir(), "job_prune.log")
	o, err := OpenOutputNoSync(path, 6)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = o.Close() }()
	appendOutput(t, o, "😀😀")

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pruned file: %v", err)
	}
	if !utf8.Valid(raw) {
		t.Fatalf("pruned file is not valid UTF-8: %x", raw)
	}
	if string(raw) != "😀" {
		t.Fatalf("pruned file = %x, want %x", raw, "😀")
	}
	// The evicted bytes stay accounted for: retainedStart names the lifetime
	// offset of the file's first byte, so it plus the file's size is the total.
	if start, total := o.RetainedStart(), o.Len(); start != 4 || start+int64(len(raw)) != total {
		t.Fatalf("retainedStart = %d with total %d over %d retained bytes, want 4", start, total, len(raw))
	}
}

// Reopening an oversized file prunes it to the cap the same way, and the same
// rune boundary applies: the cut is the store's, not the file's.
func TestOutputStorePruneOnOpenCutsOnRuneBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job_reopen.log")
	if err := os.WriteFile(path, []byte("😀😀"), 0o644); err != nil {
		t.Fatalf("write oversized file: %v", err)
	}
	o, err := OpenOutputNoSync(path, 6)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = o.Close() }()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pruned file: %v", err)
	}
	if string(raw) != "😀" {
		t.Fatalf("pruned file = %x, want %x", raw, "😀")
	}
	if start := o.RetainedStart(); start != 4 {
		t.Fatalf("retainedStart = %d, want 4", start)
	}
}

// The pruner drops only the bytes its own cut orphaned. Output that is not
// UTF-8 keeps everything past the three bytes a 4-byte rune could account for,
// and a cap that lands on a rune boundary retains the cap exactly.
func TestOutputStorePruneKeepsInvalidUTF8(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		cap     int64
		want    []byte
	}{
		{
			name:    "cut already on a boundary",
			content: []byte("😀😀"),
			cap:     4,
			want:    []byte("😀"),
		},
		{
			name:    "continuation run longer than a rune keeps the rest",
			content: []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80},
			cap:     6,
			want:    []byte{0x80, 0x80, 0x80},
		},
		{
			name:    "invalid lead byte at the cut",
			content: []byte{'a', 'b', 0xFF, 0xFE, 'c'},
			cap:     3,
			want:    []byte{0xFF, 0xFE, 'c'},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "job_prune.log")
			o, err := OpenOutputNoSync(path, tc.cap)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer func() { _ = o.Close() }()
			appendOutput(t, o, string(tc.content))

			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read pruned file: %v", err)
			}
			if !bytes.Equal(raw, tc.want) {
				t.Fatalf("pruned file = %x, want %x", raw, tc.want)
			}
			if start := o.RetainedStart(); start+int64(len(raw)) != o.Len() {
				t.Fatalf("retainedStart %d + %d retained bytes != total %d", start, len(raw), o.Len())
			}
		})
	}
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
