package execenv

import (
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The read_file and edit windows present a whole (already capped) file as
// lines. Splitting the whole file into per-line strings amplifies a
// newline-heavy file at the read cap into ~16 bytes of slice header for every
// line — gigabytes past the cap the read just enforced — so the windows are
// streamed instead. The boundary tables below pin byte-identical output; the
// allocation probes pin that no window materializes every line (issue #4556).

func TestNumberLinesWindowBoundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		text       string
		offsetLine *int
		limitLines *int
		want       string
	}{
		{name: "whole file", text: "a\nb\nc", want: "   1\ta\n   2\tb\n   3\tc\n"},
		{name: "offset past the first line", text: "a\nb\nc", offsetLine: new(2), want: "   2\tb\n   3\tc\n"},
		{name: "limit short of the end", text: "a\nb\nc", limitLines: new(2), want: "   1\ta\n   2\tb\n"},
		{name: "limit from an offset", text: "a\nb\nc", offsetLine: new(2), limitLines: new(1), want: "   2\tb\n"},
		{name: "offset past the end", text: "a\nb\nc", offsetLine: new(4), want: ""},
		{name: "final empty segment after a trailing newline", text: "a\n", want: "   1\ta\n   2\t\n"},
		{name: "offset lands on the final empty segment", text: "a\n", offsetLine: new(2), want: "   2\t\n"},
		{name: "offset past a trailing newline", text: "a\n", offsetLine: new(3), want: ""},
		{name: "empty file is one empty line", text: "", want: "   1\t\n"},
		{name: "crlf normalized before numbering", text: "a\r\nb", want: "   1\ta\n   2\tb\n"},
		{name: "interior empty line and trailing segment", text: "a\n\nb\n", want: "   1\ta\n   2\t\n   3\tb\n   4\t\n"},
		{name: "single line beyond a large limit", text: "a", limitLines: new(5), want: "   1\ta\n"},
		{name: "last line from an offset", text: "a\nb", offsetLine: new(2), limitLines: new(5), want: "   2\tb\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := NumberLines(tc.text, tc.offsetLine, tc.limitLines); got != tc.want {
				t.Fatalf("NumberLines(%q, %v, %v) = %q, want %q", tc.text, tc.offsetLine, tc.limitLines, got, tc.want)
			}
		})
	}
}

func TestFindFuzzyMatchWindowBoundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
		old     string
		want    string
	}{
		{name: "single-line match keeps the file's spacing", content: "  x  \nb", old: "x", want: "  x  "},
		{name: "multi-line window match keeps each line's spacing", content: "q\n  x\n", old: "q\n x", want: "q\n  x"},
		{name: "window spanning two whole lines", content: "p\nq\n", old: "p\nq", want: "p\nq"},
		{name: "single-line pass wins over the window pass", content: "m\nn\n", old: "n\n", want: "n"},
		{name: "no match", content: "a\nb\nc", old: "zzz", want: ""},
		{name: "window order is not the match", content: "a\nb", old: "b\na", want: ""},
		{name: "empty content", content: "", old: "x", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := findFuzzyMatch(tc.content, tc.old); got != tc.want {
				t.Fatalf("findFuzzyMatch(%q, %q) = %q, want %q", tc.content, tc.old, got, tc.want)
			}
		})
	}
}

func TestNearestFileRegionWindowBoundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
		old     string
		want    string
	}{
		{name: "window spans the matching line and the next", content: "one\ntwo\nthree", old: "two\nthree", want: "two\nthree"},
		{name: "window past the end keeps the trailing empty segment", content: "alpha\nneedle line\n", old: "needle line\nmore", want: "needle line\n"},
		{name: "nothing similar enough", content: "one\ntwo\nthree", old: "four five", want: ""},
		{name: "empty content", content: "", old: "x", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := nearestFileRegion(tc.content, tc.old); got != tc.want {
				t.Fatalf("nearestFileRegion(%q, %q) = %q, want %q", tc.content, tc.old, got, tc.want)
			}
		})
	}
}

// Not parallel: the allocation measurement must exclude sibling test activity.
// TestNumberLinesBoundsWindowAllocationOnALineHeavyFile pins the bound the
// reviewer's measurement demanded: numbering a line-heavy buffer near the read
// cap must not allocate per line for every line in it.
func TestNumberLinesBoundsWindowAllocationOnALineHeavyFile(t *testing.T) {
	text := strings.Repeat("\n", 32<<20)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	NumberLines(text, nil, nil)
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Fatalf("numbering a %d-byte line-heavy buffer allocated %d bytes, want a streamed window", len(text), allocated)
	}
}

// Not parallel: the allocation measurement must exclude sibling test activity.
// TestFindFuzzyMatchBoundsAllocationOnALineHeavyFile pins the same bound on
// edit_file's fuzzy scan.
func TestFindFuzzyMatchBoundsAllocationOnALineHeavyFile(t *testing.T) {
	content := strings.Repeat("\n", 32<<20)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	findFuzzyMatch(content, "absent")
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Fatalf("fuzzy-matching a %d-byte line-heavy buffer allocated %d bytes, want a streamed scan", len(content), allocated)
	}
}

// Not parallel: the allocation measurement must exclude sibling test activity.
// TestNearestFileRegionBoundsAllocationOnALineHeavyFile pins the same bound on
// edit_file's nearest-region diagnostic.
func TestNearestFileRegionBoundsAllocationOnALineHeavyFile(t *testing.T) {
	content := strings.Repeat("\n", 32<<20)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	nearestFileRegion(content, "absent thing")
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Fatalf("nearest-region over a %d-byte line-heavy buffer allocated %d bytes, want a streamed scan", len(content), allocated)
	}
}

// TestEachLineYieldsSplitSegments guards the streaming primitive itself: it
// must yield exactly the segments strings.Split(text, "\n") yields, including
// the final (possibly empty) one, and stop the moment visit says so.
func TestEachLineYieldsSplitSegments(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"", "a", "a\n", "\n", "a\nb", "a\nb\n", "\n\n", "a\r\nb"} {
		want := strings.Split(text, "\n")
		var got []string
		eachLine(text, func(line string) bool {
			got = append(got, line)
			return false
		})
		if !slices.Equal(got, want) {
			t.Fatalf("eachLine(%q) = %q, want %q", text, got, want)
		}
		got = got[:0]
		eachLine(text, func(line string) bool {
			got = append(got, line)
			return true
		})
		if len(got) != 1 {
			t.Fatalf("eachLine(%q) visited %d segments after stop, want 1", text, len(got))
		}
	}
}
