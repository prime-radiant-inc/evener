package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestScanLineWindowMatchesNumberLinesBytes is the scanner's own differential:
// every fixture is rendered twice, once by the scanner and once by
// renderTextReadRich over the whole fixture, and the two must agree byte for
// byte (and meta for meta).
func TestScanLineWindowMatchesNumberLinesBytes(t *testing.T) {
	t.Parallel()
	fixtures := readWindowScannerFixtures()
	windows := readWindowScannerWindows()
	for _, f := range fixtures {
		for _, w := range windows {
			off, lim := w.offset, w.limit
			want, wantMeta, werr := renderTextReadRich("fixture.txt", []byte(f.body), &off, &lim)
			got, gotMeta, err := scanLineWindowRich(t.Context(), strings.NewReader(f.body), int64(len(f.body)), "fixture.txt", nil, &off, &lim)
			if err != nil {
				t.Errorf("%s offset=%d limit=%d: scanner error %v (whole err=%v)", f.name, w.offset, w.limit, err, werr)
				continue
			}
			if got != want {
				t.Errorf("%s offset=%d limit=%d:\n scanner = %q\n want    = %q", f.name, w.offset, w.limit, got, want)
			}
			if gotMeta != wantMeta {
				t.Errorf("%s offset=%d limit=%d:\n scanner meta = %+v\n want meta    = %+v", f.name, w.offset, w.limit, gotMeta, wantMeta)
			}
		}
	}
}

// readWindowScannerFixtures are the bodies the scanner differential runs over.
// They live in a helper so the ReadFileCtx differential below drives exactly the
// same set with the threshold at 0.
func readWindowScannerFixtures() []struct {
	name string
	body string
} {
	return []struct {
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
}

// readWindowScannerWindows are the windows the scanner differential requests.
func readWindowScannerWindows() []struct{ offset, limit int } {
	return []struct{ offset, limit int }{
		{1, 1}, {1, 2}, {1, 10}, {1, DefaultReadLines},
		{2, 1}, {2, 10}, {3, 2},
		{1, 500}, {500, 1}, {501, 3}, {999, 1}, {1000, 1}, {1001, 1},
	}
}

// TestReadFileWindowForcesTheScanOnSmallFixtures is where readWindowWholeBelow's
// override earns its keep. The variable is what keeps a small file on the
// whole-file path, so setting it to 0 makes every fixture scan — the same
// fixtures and windows as the scanner differential above, but through
// ReadFileCtx, which no other small-fixture test reaches.
//
// Not parallel: readWindowWholeBelow is package-wide.
func TestReadFileWindowForcesTheScanOnSmallFixtures(t *testing.T) {
	orig := readWindowWholeBelow
	readWindowWholeBelow = 0
	t.Cleanup(func() { readWindowWholeBelow = orig })

	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	for _, f := range readWindowScannerFixtures() {
		path := filepath.Join(t.TempDir(), "fixture.txt")
		if err := os.WriteFile(path, []byte(f.body), 0o644); err != nil {
			t.Fatalf("%s: write fixture: %v", f.name, err)
		}
		for _, w := range readWindowScannerWindows() {
			off, lim := w.offset, w.limit
			want, _, wantMeta, werr := env.ReadFileRichAndBytesCtx(t.Context(), path, &off, &lim)
			got, gotMeta, err := env.ReadFileRichCtx(t.Context(), path, &off, &lim)
			if err != nil {
				t.Errorf("%s offset=%d limit=%d: ReadFileRichCtx error %v (whole err=%v)", f.name, w.offset, w.limit, err, werr)
				continue
			}
			if got != want {
				t.Errorf("%s offset=%d limit=%d: scanned entry diverged\n got  = %q\n want = %q", f.name, w.offset, w.limit, got, want)
			}
			if gotMeta != wantMeta {
				t.Errorf("%s offset=%d limit=%d: scanned meta diverged\n got  = %+v\n want = %+v", f.name, w.offset, w.limit, gotMeta, wantMeta)
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
	want, wantMeta, _ := renderTextReadRich("fixture.txt", []byte(body), &off, &lim)
	head := body[:4] // "firs", splitting the first line
	got, gotMeta, err := scanLineWindowRich(t.Context(), strings.NewReader(body[len(head):]), int64(len(body)), "fixture.txt", []byte(head), &off, &lim)
	if err != nil {
		t.Fatalf("scanner with head: %v", err)
	}
	if got != want || gotMeta != wantMeta {
		t.Fatalf("scanner with head = (%q, %+v), want (%q, %+v)", got, gotMeta, want, wantMeta)
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
			if _, _, err := scanLineWindowRich(t.Context(), strings.NewReader(tc.body), int64(len(tc.body)), "fixture.txt", nil, &tc.off, &tc.lim); err == nil {
				t.Fatalf("scanner accepted a scanned NUL: %q", tc.body)
			}
		})
	}
	// A NUL past the window is never scanned, since the scan stops at the
	// window's last line. This is the window-scoped refusal the package
	// documents at readWindowWholeBelow.
	body := "first\nsecond\nthird\n" + strings.Repeat("text\n", 10) + "bad\x00tail\n"
	if _, _, err := scanLineWindowRich(t.Context(), strings.NewReader(body), int64(len(body)), "fixture.txt", nil, new(1), new(2)); err != nil {
		t.Fatalf("scanner refused a window that ends before the file's NUL: %v", err)
	}
}
