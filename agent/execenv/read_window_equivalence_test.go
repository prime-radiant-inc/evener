package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// read_file's offset/limit read takes a windowed path, while
// ReadFileAndBytesCtx still loads the whole file. These tests hold the two to
// the same answer, and they name nothing introduced with the windowed read: they
// compile and pass against the pre-change code, where both entries took the
// whole-file path. That is the point of the file — a differential can only show
// a regression, never invent the contract.

// readWindowScanFloor is the fixture size above which a bounded read is
// expected to scan rather than load the file. It is a literal, not derived from
// the implementation's buffer size, so this file keeps compiling against the
// pre-change code; it only has to be comfortably above the windowed read's
// whole-file threshold.
const readWindowScanFloor = 200 << 10

// readWindowCase is one file body and one requested window.
type readWindowCase struct {
	name string
	body string
	off  *int
	lim  *int
	// wantRows is the exact numbered-row prefix rendered for the unpadded body
	// (empty for empty/past-EOF notices, which assert structured meta fields).
	wantRows   string
	wantStart  int
	wantEnd    int
	wantTotal  int
	wantPastEF bool
	exactWhole bool // true when unbounded whole file (no footer)
}

// readFileWindowGolden pins the rendered window for the edge cases issue #6427
// names: line numbering, CRLF, a missing trailing newline, bytes that are not
// UTF-8, blank lines, an empty file, an offset past the end, and the
// no-offset/no-limit defaults.
var readFileWindowGolden = []readWindowCase{
	{name: "empty file", body: "", wantStart: 0, wantEnd: 0, wantTotal: 0},
	{name: "only newline", body: "\n", wantRows: "1\t\n", wantStart: 1, wantEnd: 1, wantTotal: 1, exactWhole: true},
	{name: "single line LF", body: "a\n", wantRows: "1\ta\n", wantStart: 1, wantEnd: 1, wantTotal: 1, exactWhole: true},
	{name: "single line no newline", body: "a", wantRows: "1\ta\n", wantStart: 1, wantEnd: 1, wantTotal: 1, exactWhole: true},
	{name: "single line CRLF", body: "a\r\n", wantRows: "1\ta\n", wantStart: 1, wantEnd: 1, wantTotal: 1, exactWhole: true},
	{name: "lone CR at end", body: "a\r", wantRows: "1\ta\r\n", wantStart: 1, wantEnd: 1, wantTotal: 1, exactWhole: true},
	{name: "CR mid line", body: "a\rb\nc", wantRows: "1\ta\rb\n2\tc\n", wantStart: 1, wantEnd: 2, wantTotal: 2, exactWhole: true},
	{name: "CRLF two rows", body: "a\r\nb\r\n", wantRows: "1\ta\n2\tb\n", wantStart: 1, wantEnd: 2, wantTotal: 2, exactWhole: true},
	{name: "CRLF window", body: "a\r\nb\r\nc\r\n", off: new(2), lim: new(1), wantRows: "2\tb\n", wantStart: 2, wantEnd: 2, wantTotal: 3},
	{name: "trailing blank lines", body: "a\n\n\n", wantRows: "1\ta\n2\t\n3\t\n", wantStart: 1, wantEnd: 3, wantTotal: 3, exactWhole: true},
	{name: "mixed endings", body: "a\r\nb\nc\r\n", wantRows: "1\ta\n2\tb\n3\tc\n", wantStart: 1, wantEnd: 3, wantTotal: 3, exactWhole: true},
	{name: "no trailing newline offset 2", body: "a\nb", off: new(2), lim: new(10), wantRows: "2\tb\n", wantStart: 2, wantEnd: 2, wantTotal: 2},
	{name: "offset past single line", body: "a\n", off: new(2), lim: new(10), wantStart: 0, wantEnd: 0, wantTotal: 1, wantPastEF: true},
	{name: "offset past many lines", body: "a\nb\nc\n", off: new(9), lim: new(10), wantStart: 0, wantEnd: 0, wantTotal: 3, wantPastEF: true},
	{name: "window from the middle", body: "1\n2\n3\n4\n5\n", off: new(3), lim: new(2), wantRows: "3\t3\n4\t4\n", wantStart: 3, wantEnd: 4, wantTotal: 5},
	{name: "limit longer than the file", body: "1\n2\n", off: new(1), lim: new(100), wantRows: "1\t1\n2\t2\n", wantStart: 1, wantEnd: 2, wantTotal: 2},
	{name: "non-UTF-8 bytes", body: "caf\xe9 \xff\xfe line\nsecond \x80 line\n", wantRows: "1\tcaf\xe9 \xff\xfe line\n2\tsecond \x80 line\n", wantStart: 1, wantEnd: 2, wantTotal: 2, exactWhole: true},
	{name: "non-UTF-8 unterminated tail", body: "ok\n\xff\xfe\xfd", off: new(2), lim: new(1), wantRows: "2\t\xff\xfe\xfd\n", wantStart: 2, wantEnd: 2, wantTotal: 2},
	{name: "blank first line", body: "\nsecond\nthird\n", wantRows: "1\t\n2\tsecond\n3\tthird\n", wantStart: 1, wantEnd: 3, wantTotal: 3, exactWhole: true},
	{name: "CRLF without final terminator", body: "a\r\nb\r", wantRows: "1\ta\n2\tb\r\n", wantStart: 1, wantEnd: 2, wantTotal: 2, exactWhole: true},
	{name: "offset only", body: "a\nb\nc\n", off: new(2), wantRows: "2\tb\n3\tc\n", wantStart: 2, wantEnd: 3, wantTotal: 3},
	{name: "limit only", body: "a\nb\nc\n", lim: new(2), wantRows: "1\ta\n2\tb\n", wantStart: 1, wantEnd: 2, wantTotal: 3},
	{name: "offset zero is the default", body: "a\nb\n", off: new(0), wantRows: "1\ta\n2\tb\n", wantStart: 1, wantEnd: 2, wantTotal: 2, exactWhole: true},
	{name: "limit zero is the default", body: "a\nb\n", lim: new(0), wantRows: "1\ta\n2\tb\n", wantStart: 1, wantEnd: 2, wantTotal: 2, exactWhole: true},
}

// TestReadFileWindowGoldenRows pins the exact rows for every golden case on the
// unpadded body, and checks that the whole-file entry returns the same text and
// the same file bytes.
func TestReadFileWindowGoldenRows(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	for _, tc := range readFileWindowGolden {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "fixture.txt")
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			got, meta, err := env.ReadFileRichCtx(t.Context(), path, tc.off, tc.lim)
			if err != nil {
				t.Fatalf("offset/limit read: %v", err)
			}
			if got == "" {
				t.Fatalf("read returned empty string; empty/past-EOF should return non-empty notice")
			}
			if meta.StartLine != tc.wantStart || meta.EndLine != tc.wantEnd || meta.TotalLines != tc.wantTotal || meta.PastEOF != tc.wantPastEF {
				t.Fatalf("meta = %+v, want start=%d end=%d total=%d pastEOF=%v", meta, tc.wantStart, tc.wantEnd, tc.wantTotal, tc.wantPastEF)
			}
			if tc.exactWhole {
				if got != tc.wantRows {
					t.Fatalf("whole read = %q, want %q", got, tc.wantRows)
				}
			} else if tc.wantRows != "" && !strings.HasPrefix(got, tc.wantRows) {
				t.Fatalf("bounded read = %q, want prefix %q", got, tc.wantRows)
			}
			whole, raw, wholeMeta, err := env.ReadFileRichAndBytesCtx(t.Context(), path, tc.off, tc.lim)
			if err != nil {
				t.Fatalf("whole-file read: %v", err)
			}
			if whole != got {
				t.Fatalf("whole-file read = %q, offset/limit read = %q", whole, got)
			}
			if wholeMeta != meta {
				t.Fatalf("whole-file meta = %+v, offset/limit meta = %+v", wholeMeta, meta)
			}
			if string(raw) != tc.body {
				t.Fatalf("whole-file read returned %q, want the file's bytes %q", raw, tc.body)
			}
		})
	}
}

// TestReadFileWindowAgreesWithWholeFileReadOnALargeFile re-runs every golden
// case on a body padded past readWindowScanFloor, so the same windows are
// answered from a file big enough to scan, and compares the offset/limit entry
// with the whole-file entry. The pad lands after the rows the case is about, so
// every window that stops before the file's end answers exactly as it did on the
// unpadded body — a case whose body had no trailing newline simply merges into
// the pad, and the differential still holds because both entries read the same
// file. The exact rows are pinned by TestReadFileWindowGoldenRows instead.
func TestReadFileWindowAgreesWithWholeFileReadOnALargeFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	const padRow = "pad row after the interesting part\n"
	for _, tc := range readFileWindowGolden {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := tc.body + strings.Repeat(padRow, readWindowScanFloor/len(padRow))
			path := filepath.Join(t.TempDir(), "fixture.txt")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			got, err := env.ReadFileCtx(t.Context(), path, tc.off, tc.lim)
			if err != nil {
				t.Fatalf("offset/limit read: %v", err)
			}
			whole, raw, err := env.ReadFileAndBytesCtx(t.Context(), path, tc.off, tc.lim)
			if err != nil {
				t.Fatalf("whole-file read: %v", err)
			}
			if got != whole {
				t.Fatalf("offset/limit read = %q, whole-file read = %q", got, whole)
			}
			if len(raw) != len(body) {
				t.Fatalf("whole-file read returned %d bytes, want the file's %d", len(raw), len(body))
			}
		})
	}
}

// TestReadFileWindowAgreesAcrossScanBufferEdges drives the differential over
// bodies positioned around a scan buffer's edge, where a line, a newline or a
// CR/LF pair straddles it. A scanner that mishandled a boundary would agree
// with the whole-file read on short fixtures and diverge here. The sizes are
// written against readWindowScanFloor because this file cannot name the
// implementation's buffer.
func TestReadFileWindowAgreesAcrossScanBufferEdges(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	pad := func(n int) string { return strings.Repeat("x", n) }
	bodies := map[string]string{
		"newline after a long line":       pad(readWindowScanFloor) + "\nsecond\nthird\n",
		"CRLF after a long line":          pad(readWindowScanFloor) + "\r\nsecond\n",
		"newline at the floor":            pad(readWindowScanFloor-1) + "\nsecond\n",
		"many short rows over the floor":  strings.Repeat("row\n", readWindowScanFloor/4),
		"long row then rows":              pad(readWindowScanFloor-1) + "\r\n" + "second\n",
		"unterminated long tail":          "first\n" + pad(readWindowScanFloor),
		"CRLF and bare CR over the floor": strings.Repeat("ab\r\ncd\r", readWindowScanFloor/8),
	}
	windows := []struct {
		name string
		off  int
		lim  int
	}{
		{"first rows", 1, 3},
		{"middle", 5, 3},
		{"whole default", 1, DefaultReadLines},
		{"past the end", 1 << 20, 3},
	}
	for bodyName, body := range bodies {
		path := filepath.Join(t.TempDir(), "boundary.txt")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", bodyName, err)
		}
		for _, w := range windows {
			off, lim := w.off, w.lim
			want, _, err := env.ReadFileAndBytesCtx(t.Context(), path, &off, &lim)
			if err != nil {
				t.Fatalf("%s / %s: whole-file read: %v", bodyName, w.name, err)
			}
			got, err := env.ReadFileCtx(t.Context(), path, &off, &lim)
			if err != nil {
				t.Fatalf("%s / %s: offset/limit read: %v", bodyName, w.name, err)
			}
			if got != want {
				t.Fatalf("%s / %s: offset/limit read diverged\n got  = %q\n want = %q",
					bodyName, w.name, truncateForReport(got), truncateForReport(want))
			}
		}
	}
}

// truncateForReport keeps a divergence message readable when the bodies above
// run to tens of kilobytes.
func truncateForReport(s string) string {
	const keep = 160
	if len(s) <= keep {
		return s
	}
	return s[:keep] + "…"
}

// TestReadFileWindowAgreesOnAMixedFile walks a deterministic pseudo-random file
// — CRLF, bare CR, LF and bytes that are not UTF-8 mixed together — through
// many windows, so numbering and termination are checked at offsets nobody
// hand-picked.
func TestReadFileWindowAgreesOnAMixedFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	var sb strings.Builder
	seed := uint32(0x9e3779b9)
	for sb.Len() < readWindowScanFloor {
		seed ^= seed << 13
		seed ^= seed >> 17
		seed ^= seed << 5
		switch seed % 9 {
		case 0:
			sb.WriteString("\r\n")
		case 1:
			sb.WriteString("\n")
		case 2:
			sb.WriteString("\r")
		case 3:
			sb.WriteString("\xff")
		case 4:
			sb.WriteString("")
		default:
			sb.WriteString("some line text")
		}
	}
	path := filepath.Join(dir, "mixed.txt")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write mixed.txt: %v", err)
	}
	for _, w := range []struct{ off, lim int }{
		{1, 1}, {1, 2}, {1, 3}, {2, 1}, {3, 5}, {17, 4}, {1, 2000}, {50, 50}, {1000, 1},
	} {
		off, lim := w.off, w.lim
		want, _, werr := env.ReadFileAndBytesCtx(t.Context(), path, &off, &lim)
		got, gerr := env.ReadFileCtx(t.Context(), path, &off, &lim)
		if (werr == nil) != (gerr == nil) {
			t.Fatalf("offset=%d limit=%d: offset/limit err = %v, whole-file err = %v", w.off, w.lim, gerr, werr)
		}
		if werr != nil {
			continue
		}
		if got != want {
			t.Fatalf("offset=%d limit=%d: offset/limit read diverged\n got  = %q\n want = %q",
				w.off, w.lim, truncateForReport(got), truncateForReport(want))
		}
	}
}

// TestReadFileWindowKeepsTheTruncationInputIdentical pins that a large multi-line
// read stops cleanly at DefaultReadMaxBytes on a line boundary and agrees
// byte-for-byte (and meta-for-meta) between ReadFileRichCtx and
// ReadFileRichAndBytesCtx.
func TestReadFileWindowKeepsTheTruncationInputIdentical(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	body := strings.Repeat("some source line of moderate length\n", 5000)
	path := filepath.Join(dir, "many.txt")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write many.txt: %v", err)
	}
	off, lim := 1, DefaultReadLines
	want, _, wantMeta, err := env.ReadFileRichAndBytesCtx(t.Context(), path, &off, &lim)
	if err != nil {
		t.Fatalf("whole-file read: %v", err)
	}
	got, gotMeta, err := env.ReadFileRichCtx(t.Context(), path, &off, &lim)
	if err != nil {
		t.Fatalf("offset/limit read: %v", err)
	}
	if got != want {
		t.Fatalf("offset/limit read diverged from the whole-file read over %d rows", 5000)
	}
	if gotMeta != wantMeta {
		t.Fatalf("meta diverged: got=%+v want=%+v", gotMeta, wantMeta)
	}
	if !gotMeta.Truncated || gotMeta.TruncatedBy != "chars" || gotMeta.StartLine != 1 || gotMeta.EndLine >= DefaultReadLines || gotMeta.TotalLines != 5000 {
		t.Fatalf("gotMeta = %+v", gotMeta)
	}
	if len(got) > DefaultReadMaxBytes+512 {
		t.Fatalf("output len = %d, want <= %d", len(got), DefaultReadMaxBytes+512)
	}
}
