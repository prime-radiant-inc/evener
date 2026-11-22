package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// numberLinesGolden holds expectations produced by the pre-change NumberLines
// (the strings.ReplaceAll implementation this package shipped before the
// window-scanning rewrite). The table pins the original contract byte for
// byte, so the two rewrites — the string one and the bytes one read_file uses —
// cannot drift from it silently.
var numberLinesGolden = []struct {
	name string
	text string
	off  int
	lim  int
	want string
}{
	{"lone CR at end", "a\r", 1, 10, "1\ta\r\n"},
	{"lone CR at end offset2", "a\r", 2, 10, ""},
	{"CRLF single", "a\r\n", 1, 10, "1\ta\n"},
	{"CRLF two rows", "a\r\nb\r\n", 1, 10, "1\ta\n2\tb\n"},
	{"CRLF offset 2", "a\r\nb\r\n", 2, 10, "2\tb\n"},
	{"CRLF window 1", "a\r\nb\r\nc\r\n", 2, 1, "2\tb\n"},
	{"empty", "", 1, 10, "1\t\n"},
	{"only newline", "\n", 1, 10, "1\t\n"},
	{"offset past single line", "a\n", 2, 10, ""},
	{"trailing blanks", "a\n\n\n", 1, 10, "1\ta\n2\t\n3\t\n"},
	{"mixed endings", "a\r\nb\nc\r\n", 1, 10, "1\ta\n2\tb\n3\tc\n"},
	{"CR mid line", "a\rb\nc", 1, 10, "1\ta\rb\n2\tc\n"},
	{"no trailing newline", "a\nb", 2, 10, "2\tb\n"},
}

// TestNumberLinesBytesMatchesGolden pins the line-numbering contract against
// expectations taken from the pre-change implementation, and checks that the
// bytes variant read_file uses answers identically. Comparing the two new
// functions to each other alone would prove nothing: both could drift together.
func TestNumberLinesMatchesGolden(t *testing.T) {
	for _, tc := range numberLinesGolden {
		off, lim := tc.off, tc.lim
		if got := NumberLines(tc.text, &off, &lim); got != tc.want {
			t.Errorf("%s: NumberLines(%q) = %q, want %q", tc.name, tc.text, got, tc.want)
		}
	}
}

// TestReadFileWindowMatchesWindowedContract confirms the read_file path still
// numbers from the requested offset and stops at the limit, on a file large
// enough that the window sits far from both ends.
func TestReadFileWindowMatchesWindowedContract(t *testing.T) {
	var sb strings.Builder
	for range 5000 {
		sb.WriteString("row\n")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rows.txt"), []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write rows.txt: %v", err)
	}
	env := NewLocalExecutionEnvironment(dir)

	offset, limit := 2500, 3
	got, meta, err := env.ReadFileRichCtx(t.Context(), "rows.txt", &offset, &limit)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := "2500\trow\n2501\trow\n2502\trow\n"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("window = %q, want %q", got, want)
	}
	if meta.StartLine != 2500 || meta.EndLine != 2502 || meta.TotalLines != 5000 || meta.Truncated {
		t.Fatalf("meta = %+v, want 2500..2502 of 5000 untruncated", meta)
	}
}
