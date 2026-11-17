package execenv

import (
	"strings"
	"testing"
)

// TestScanLineWindowMatchesNumberLinesBytes is the scanner's own differential:
// every fixture is rendered twice, once by the scanner and once by
// NumberLinesBytes over the whole fixture, and the two must agree byte for
// byte. This file is the only one that names symbols introduced with the
// windowed read, so the contract checks in read_window_equivalence_test.go stay
// runnable against the pre-change code.
//
// Not parallel: readWindowWholeBelow is package-wide.
func TestScanLineWindowMatchesNumberLinesBytes(t *testing.T) {
	orig := readWindowWholeBelow
	readWindowWholeBelow = 0
	t.Cleanup(func() { readWindowWholeBelow = orig })

	fixtures := []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"only newline", "\n"},
		{"only CRLF", "\r\n"},
		{"single line no newline", "a"},
		{"single line LF", "a\n"},
		{"single line CRLF", "a\r\n"},
		{"lone CR at end", "a\r"},
		{"CR mid line", "a\rb\nc"},
		{"CR then CRLF", "a\r\r\n"},
		{"empty CRLF line at end", "a\r\n\r\n"},
		{"trailing blank lines", "a\n\n\n"},
		{"two rows CRLF", "a\r\nb\r\n"},
		{"mixed endings", "a\r\nb\nc\r\n"},
		{"no trailing newline", "a\nb"},
		{"CRLF without final terminator", "a\r\nb\r"},
		{"non-UTF-8 bytes", "caf\xe9 \xff\xfe line\nsecond \x80 line\n"},
		{"non-UTF-8 last line", "ok\n\xff\xfe\xfd"},
		{"blank first line", "\nsecond\nthird\n"},
		{"long line over one chunk", strings.Repeat("x", 3*readWindowChunk) + "\ntail\n"},
		{"many lines", strings.Repeat("row\n", 500)},
		{"line exactly a chunk", strings.Repeat("y", readWindowChunk) + "\nz\n"},
		{"CRLF splits on the chunk edge", strings.Repeat("x", readWindowChunk-1) + "\r\nsecond\n"},
	}
	windows := []struct{ offset, limit int }{
		{1, 1}, {1, 2}, {1, 10}, {1, DefaultReadLines},
		{2, 1}, {2, 10}, {3, 2},
		{1, 500}, {500, 1}, {501, 3}, {999, 1}, {1000, 1}, {1001, 1},
	}
	for _, f := range fixtures {
		for _, w := range windows {
			off, lim := w.offset, w.limit
			want := NumberLinesBytes([]byte(f.body), &off, &lim)
			got, err := scanLineWindow(t.Context(), strings.NewReader(f.body), nil, w.offset, w.limit)
			if err != nil {
				t.Errorf("%s offset=%d limit=%d: scanner error %v", f.name, w.offset, w.limit, err)
				continue
			}
			if got != want {
				t.Errorf("%s offset=%d limit=%d:\n scanner = %q\n want    = %q", f.name, w.offset, w.limit, got, want)
			}
		}
	}
}

// TestScanLineWindowHeadIsScannedFirst pins that the head bytes a windowed read
// consumes before the scanner are continued from, not treated as a separate
// input: a fixture whose first bytes arrive as head renders the same window.
func TestScanLineWindowHeadIsScannedFirst(t *testing.T) {
	t.Parallel()
	body := "first\nsecond\r\nthird"
	off, lim := 1, 3
	want := NumberLinesBytes([]byte(body), &off, &lim)
	head := body[:4] // "firs", splitting the first line
	got, err := scanLineWindow(t.Context(), strings.NewReader(body[len(head):]), []byte(head), off, lim)
	if err != nil {
		t.Fatalf("scanner with head: %v", err)
	}
	if got != want {
		t.Fatalf("scanner with head = %q, want %q", got, want)
	}
}

// TestScanLineWindowRefusesANULByteItScans pins where the binary refusal sits:
// on the bytes the scan consumes. A NUL the read never reaches — one past the
// window's last line — is not read, so it cannot refuse; a NUL inside what the
// scan walks does.
func TestScanLineWindowRefusesANULByteItScans(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		body string
		off  int
		lim  int
	}{
		{"NUL in the first line", "bad\x00first\nsecond\n", 1, 2},
		{"NUL in the last line read", "first\nsecond\nbad\x00tail\n", 1, 3},
		{"NUL in a skipped line", "skip\x00me\nsecond\nthird\n", 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := scanLineWindow(t.Context(), strings.NewReader(tc.body), nil, tc.off, tc.lim); err == nil {
				t.Fatalf("scanner accepted a scanned NUL: %q", tc.body)
			}
		})
	}
	// A NUL past the window is never scanned, since the scan stops at the
	// window's last line. This is the window-scoped refusal the package
	// documents at readWindowWholeBelow.
	body := "first\nsecond\nthird\n" + strings.Repeat("text\n", 10) + "bad\x00tail\n"
	if _, err := scanLineWindow(t.Context(), strings.NewReader(body), nil, 1, 2); err != nil {
		t.Fatalf("scanner refused a window that ends before the file's NUL: %v", err)
	}
}
