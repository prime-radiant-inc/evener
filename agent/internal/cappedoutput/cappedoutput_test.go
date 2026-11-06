package cappedoutput

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestBufferKeepsHeadAndTailAroundTheMarker pins the single capture
// sink's shape: the exact stream at or under the cap, else the first and last
// windows around an omission marker that counts every byte it does not return.
func TestBufferKeepsHeadAndTailAroundTheMarker(t *testing.T) {
	t.Parallel()
	exact := []byte("short stream")
	b := New(64)
	if _, err := b.Write(exact); err != nil {
		t.Fatal(err)
	}
	if b.Truncated() {
		t.Fatal("under-cap capture reports truncated")
	}
	if got := b.String(); got != string(exact) {
		t.Fatalf("under-cap capture = %q, want the exact stream", got)
	}

	// A 4-byte cap on a 10-byte stream keeps the first two and the last two
	// bytes around a marker disclosing the six it dropped.
	b = New(4)
	if _, err := b.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	if !b.Truncated() {
		t.Fatal("past-cap capture reports untruncated")
	}
	want := "01" + fmt.Sprintf(OmissionMarker, 6) + "89"
	if got := b.String(); got != want {
		t.Fatalf("past-cap capture = %q, want %q", got, want)
	}
}

// TestBufferBoundsMemoryPastTheCap pins the memory bound: however
// much streams through, the capture holds the cap plus the marker, never the
// stream. Chunked writes exercise the rolling tail window across boundaries.
func TestBufferBoundsMemoryPastTheCap(t *testing.T) {
	t.Parallel()
	const capBytes = 64
	b := New(capBytes)
	chunk := []byte("0123456789abcdef") // 16 bytes
	for range 1000 {                    // 16000 bytes total
		if _, err := b.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if !b.Truncated() {
		t.Fatal("a 16000-byte stream under a 64-byte cap reports untruncated")
	}
	if len(b.buf)+len(b.head)+len(b.tail) > capBytes {
		t.Fatalf("capture retained %d bytes, want at most the %d-byte cap", len(b.buf)+len(b.head)+len(b.tail), capBytes)
	}
	if got := b.String(); len(got) > capBytes+len(fmt.Sprintf(OmissionMarker, 0))+16 {
		t.Fatalf("capped capture rendered %d bytes, want the retained windows around the marker", len(got))
	}
}

// retainedBufBytes is the memory a Buffer holds right now: the under-cap
// buffer while the stream fits, else the frozen head plus the tail window.
func retainedBufBytes(b *Buffer) int {
	return len(b.buf) + len(b.head) + len(b.tail)
}

// TestBufferCutEdgesNeverSplitARune pins the rune trims on both cut edges: a
// cap whose head cut lands mid-rune and whose tail cut lands on the rune's
// other half must drop the straddling bytes rather than render a replacement
// character, and the omission count must still be every byte not present.
func TestBufferCutEdgesNeverSplitARune(t *testing.T) {
	t.Parallel()
	const capBytes = 103
	// 150 two-byte runes then a one-byte rune: the head cut at byte 51 lands
	// on the first half of a rune and the tail cut at byte 249 on its second
	// half, so both trimmed edges drop exactly one byte.
	content := strings.Repeat("é", 150) + "x" // 301 bytes
	buf := New(capBytes)
	if _, err := buf.Write([]byte(content)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !buf.Truncated() {
		t.Fatalf("capture of %d bytes at cap %d reports untruncated", len(content), capBytes)
	}
	got := buf.String()
	if !utf8.ValidString(got) {
		t.Fatalf("capture is not valid UTF-8 after the rune trims: %q", got)
	}
	if !strings.HasPrefix(got, strings.Repeat("é", 25)) {
		t.Fatalf("capture lost the trimmed head window; starts with %q", got[:min(len(got), 12)])
	}
	if !strings.HasSuffix(got, strings.Repeat("é", 25)+"x") {
		t.Fatalf("capture lost the trimmed tail window; ends with %q", got[max(0, len(got)-12):])
	}
	if want := fmt.Sprintf(OmissionMarker, 200); !strings.Contains(got, want) {
		t.Fatalf("capture = %q, want the two trimmed edges' 200 bytes reported omitted", got)
	}
}

// TestBufferInvalidUTF8StaysBoundedAndUntrimmable covers the fuzz
// seed shapes with bytes that are not valid UTF-8 at all: the trims must not
// panic, and the capture must still honour the cap.
func TestBufferInvalidUTF8StaysBoundedAndUntrimmable(t *testing.T) {
	t.Parallel()
	const capBytes = 8
	buf := New(capBytes)
	if _, err := buf.Write(bytes.Repeat([]byte{0x80}, 20)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := buf.Write([]byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !buf.Truncated() {
		t.Fatal("a 21-byte stream under an 8-byte cap reports untruncated")
	}
	if got := retainedBufBytes(buf); got > capBytes {
		t.Fatalf("retained %d bytes at cap %d, want the capture to never exceed the cap", got, capBytes)
	}
	if got := len(buf.String()); got > capBytes+len(fmt.Sprintf(OmissionMarker, 0)) {
		t.Fatalf("capture rendered %d bytes, want at most the cap plus the marker", got)
	}
}

// TestBufferWritePatternIndependence pins ring order and tail-window
// seeding across chunk boundaries: any chunking of the same stream must render
// exactly the single-write capture. Main's memory test writes only uniform
// 16-byte chunks; this covers the boundary-adjacent chunk sizes where a
// mis-ordered ring or mis-seeded tail shows up.
func TestBufferWritePatternIndependence(t *testing.T) {
	t.Parallel()
	const capBytes = 4096
	const total = 100 * 1024
	content := strings.Repeat("0123456789", total/10)
	ref := New(capBytes)
	if _, err := ref.Write([]byte(content)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	want := ref.String()
	if !ref.Truncated() {
		t.Fatalf("capture of %d bytes at cap %d reports untruncated", total, capBytes)
	}
	if got := retainedBufBytes(ref); got != capBytes {
		t.Fatalf("retained %d bytes after a %d-byte stream, want exactly the %d-byte cap", got, total, capBytes)
	}
	if wantMarker := fmt.Sprintf(OmissionMarker, total-capBytes); !strings.Contains(want, wantMarker) {
		t.Fatalf("reference capture lacks the %d-omitted marker", total-capBytes)
	}
	for _, chunkLen := range []int{1, 7, 2047, 4095, 4097, 12288, capBytes / 2} {
		t.Run(fmt.Sprintf("chunks of %d", chunkLen), func(t *testing.T) {
			t.Parallel()
			buf := New(capBytes)
			for off := 0; off < total; off += chunkLen {
				end := min(off+chunkLen, total)
				if _, err := buf.Write([]byte(content[off:end])); err != nil {
					t.Fatalf("Write: %v", err)
				}
			}
			if got := buf.String(); got != want {
				t.Fatalf("capture in chunks of %d = %d bytes, want the single-write capture's %d", chunkLen, len(got), len(want))
			}
		})
	}
}

// TestBufferExactlyAtCapIsByteIdentical pins the boundary the under-cap test
// (a tiny stream well under the cap) does not: a stream of exactly cap bytes is not truncated and passes through byte for byte.
func TestBufferExactlyAtCapIsByteIdentical(t *testing.T) {
	t.Parallel()
	const capBytes = 100
	content := strings.Repeat("Z", capBytes)
	buf := New(capBytes)
	if _, err := buf.Write([]byte(content)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if buf.Truncated() {
		t.Fatal("a stream of exactly cap bytes reports truncated")
	}
	if got := buf.String(); got != content {
		t.Fatalf("capture at exactly the cap = %d bytes, want the exact %d-byte stream", len(got), capBytes)
	}
	if got := retainedBufBytes(buf); got != capBytes {
		t.Fatalf("retained %d bytes, want the whole %d-byte stream", got, capBytes)
	}

	// One byte more trips the cap with exactly one byte omitted.
	buf = New(capBytes)
	if _, err := buf.Write([]byte(content + "Y")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !buf.Truncated() {
		t.Fatal("a stream one byte past the cap reports untruncated")
	}
	if want := fmt.Sprintf(OmissionMarker, 1); !strings.Contains(buf.String(), want) {
		t.Fatalf("capture = %q, want the single byte past the cap reported omitted", buf.String())
	}
}

// TestBufferNonPositiveCapFallsBackToProductionDefault pins New's fallback: a
// non-positive cap keeps the bounded production default instead of silently
// removing the bound.
func TestBufferNonPositiveCapFallsBackToProductionDefault(t *testing.T) {
	t.Parallel()
	for _, capBytes := range []int{0, -1} {
		t.Run(fmt.Sprintf("cap %d", capBytes), func(t *testing.T) {
			t.Parallel()
			buf := New(capBytes)
			overflow := make([]byte, DefaultCapBytes+1)
			if _, err := buf.Write(overflow); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if !buf.Truncated() {
				t.Fatalf("a %d-byte stream past the %d-byte production cap reports untruncated", len(overflow), DefaultCapBytes)
			}
			if got := retainedBufBytes(buf); got != DefaultCapBytes {
				t.Fatalf("retained %d bytes, want the %d-byte production cap", got, DefaultCapBytes)
			}
			if want := fmt.Sprintf(OmissionMarker, 1); !strings.Contains(buf.String(), want) {
				t.Fatalf("capture lacks the single-omitted-byte marker for the production cap")
			}
		})
	}
}
