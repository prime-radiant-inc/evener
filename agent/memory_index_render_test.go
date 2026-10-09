package agent

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRenderMemoryIndex(t *testing.T) {
	t.Parallel()
	pages := []memoryPage{
		{Path: "vitest/loader.md", Title: "Vitest read-only loader", Description: "read-only Vitest needs --configLoader runner", HasDescription: true, Tags: []string{"vitest"}, Updated: "2026-10-07"},
		{Path: "cents.md", Title: "Integer cents", Description: "Money is integer cents, never floats", HasDescription: true, Tags: []string{"money", "formatting"}, Updated: "2026-10-08"},
		{Path: "b.md", Title: "b", Description: "unstamped, older mtime", HasDescription: true, ModTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Path: "a.md", Title: "a", Description: "unstamped, same day as loader", HasDescription: true, ModTime: time.Date(2026, 10, 7, 23, 0, 0, 0, time.UTC)},
		{Path: "bad.md", Title: "Bad", Description: "Bad " + memoryNoDescription, Unreadable: true, Updated: "2026-01-01"},
	}
	want := "Tags: formatting (1), money (1), vitest (1)\n" +
		"- [Integer cents](cents.md) — Money is integer cents, never floats [money, formatting] (updated 2026-10-08)\n" +
		"- [a](a.md) — unstamped, same day as loader\n" +
		"- [Vitest read-only loader](vitest/loader.md) — read-only Vitest needs --configLoader runner [vitest] (updated 2026-10-07)\n" +
		"- [b](b.md) — unstamped, older mtime\n" +
		"- [Bad](bad.md) — Bad (no description) (frontmatter unreadable) (updated 2026-01-01)\n"
	if got := renderMemoryIndex(pages); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := renderMemoryIndex(nil); got != "" {
		t.Fatalf("empty scope rendered %q", got)
	}
	untagged := []memoryPage{{Path: "x.md", Title: "x", Description: "d", HasDescription: true}}
	if got := renderMemoryIndex(untagged); strings.HasPrefix(got, "Tags:") {
		t.Fatalf("header present with no tags: %q", got)
	}
}

func overflowPages(n int) []memoryPage {
	var pages []memoryPage
	for i := range n {
		tags := []string{"vitest"}
		switch {
		case i%3 == 0:
			tags = []string{"indexeddb", "vitest"}
		case i%5 == 0:
			tags = nil
		}
		pages = append(pages, memoryPage{
			Path: fmt.Sprintf("p%03d.md", i), Title: fmt.Sprintf("Page %03d", i),
			Description: "opaque description " + strings.Repeat("x", 60), HasDescription: true,
			Tags: tags, Updated: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format(time.DateOnly),
		})
	}
	return pages
}

func TestProjectMemoryIndexFitsWhole(t *testing.T) {
	t.Parallel()
	pages := overflowPages(3)
	content, full, truncated := projectMemoryIndex(pages, memoryProjectionCap)
	if truncated || content != renderMemoryIndex(pages) || full != content {
		t.Fatalf("truncated=%t content=%q full=%q", truncated, content, full)
	}
}

func TestProjectMemoryIndexKeepsNewestAndCountsTheRest(t *testing.T) {
	t.Parallel()
	pages := overflowPages(200)
	content, full, truncated := projectMemoryIndex(pages, memoryProjectionCap)
	if !truncated || len(content) > memoryProjectionCap || full != renderMemoryIndex(pages) {
		t.Fatalf("truncated=%t len=%d, full is the whole rendering=%t", truncated, len(content), full == renderMemoryIndex(pages))
	}
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	fullLines := strings.Split(strings.TrimSuffix(full, "\n"), "\n")
	shown := len(lines) - 1 // header ... last line is Not shown
	for i := range shown {
		if lines[i] != fullLines[i] {
			t.Fatalf("line %d %q is not the full index's %q", i, lines[i], fullLines[i])
		}
	}
	notShown := 200 - (shown - 1) // minus the header
	last := lines[len(lines)-1]
	sorted := sortedMemoryPages(pages)
	if want := memoryNotShownLine(sorted[200-notShown:]); last != want {
		t.Fatalf("last line %q want %q", last, want)
	}
	if !strings.HasPrefix(last, fmt.Sprintf("Not shown: %d pages (vitest ", notShown)) || !strings.Contains(last, "untagged") {
		t.Fatalf("last line %q", last)
	}
	// One more line would not have fit: the projection with the next page
	// shown and the rest counted is over the budget.
	k := 200 - notShown
	longer := strings.Join(fullLines[:shown+1], "\n") + "\n" + memoryNotShownLine(sorted[k+1:]) + "\n"
	if len(longer) <= memoryProjectionCap {
		t.Fatalf("showing page %d too would still fit (%d bytes)", k, len(longer))
	}
}

func TestMemoryNotShownLine(t *testing.T) {
	t.Parallel()
	got := memoryNotShownLine([]memoryPage{
		{Tags: []string{"vitest", "indexeddb"}}, {Tags: []string{"vitest"}}, {Tags: []string{"indexeddb"}},
		{Tags: []string{"vitest"}}, {}, {}, {Tags: []string{"alpha"}},
	})
	want := `Not shown: 7 pages (vitest 3, indexeddb 2, alpha 1, untagged 2).`
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if one := memoryNotShownLine([]memoryPage{{}}); !strings.HasPrefix(one, "Not shown: 1 page (untagged 1).") {
		t.Fatalf("singular: %q", one)
	}
}

// One page larger than the whole budget is never cut mid-line.
func TestProjectMemoryIndexHugeLine(t *testing.T) {
	t.Parallel()
	pages := []memoryPage{
		{Path: "huge.md", Title: "huge", Description: strings.Repeat("y", 9000), HasDescription: true, Tags: []string{"big"}, Updated: "2026-10-08"},
		{Path: "small.md", Title: "small", Description: "fits", HasDescription: true, Updated: "2026-10-01"},
	}
	content, _, truncated := projectMemoryIndex(pages, memoryProjectionCap)
	if !truncated || len(content) > memoryProjectionCap || strings.Contains(content, "yyy") {
		t.Fatalf("truncated=%t len=%d content=%.200q", truncated, len(content), content)
	}
	if !strings.HasSuffix(content, memoryNotShownLine(sortedMemoryPages(pages))+"\n") {
		t.Fatalf("content %q does not end with the Not shown line for both pages", content)
	}
}

// A tag header too large to fit is cut at the budget, still truncated.
func TestProjectMemoryIndexOversizedHeader(t *testing.T) {
	t.Parallel()
	var pages []memoryPage
	for i := range 600 {
		pages = append(pages, memoryPage{Path: fmt.Sprintf("p%03d.md", i), Title: "t", Description: "d", HasDescription: true, Tags: []string{fmt.Sprintf("tag-number-%03d", i)}})
	}
	content, _, truncated := projectMemoryIndex(pages, memoryProjectionCap)
	if !truncated || len(content) > memoryProjectionCap || !strings.HasPrefix(content, "Tags: ") {
		t.Fatalf("truncated=%t len=%d", truncated, len(content))
	}
}

// A path that a bare Markdown link destination can't hold (a space, a
// parenthesis, a leading "<") is written in angle brackets, with any angle
// bracket inside it escaped; any other path is written as it is.
func TestMemoryIndexLineLinkTarget(t *testing.T) {
	t.Parallel()
	for rel, want := range map[string]string{
		"notes/a.md":     "notes/a.md",
		"a<b>.md":        "a<b>.md",
		"my notes.md":    "<my notes.md>",
		"tab\there.md":   "<tab\there.md>",
		"cents (old).md": "<cents (old).md>",
		"half).md":       "<half).md>",
		"<x> y.md":       `<\<x\> y.md>`,
		"<lead.md":       `<\<lead.md>`,
		`a \> b.md`:      `<a \\\> b.md>`,
		`a\>b.md`:        `<a\\\>b.md>`,
	} {
		line := memoryIndexLine(memoryPage{Path: rel, Title: "T", Description: "d"})
		if wantLine := "- [T](" + want + ") — d"; line != wantLine {
			t.Fatalf("%q: got %q, want %q", rel, line, wantLine)
		}
	}
}

// Patching a page's line matches only each line's leading link: a description
// that quotes another page's link shape is not that page's line.
func TestPatchMemoryIndexMatchesTheLeadingLink(t *testing.T) {
	t.Parallel()
	other := "- [Other](other.md) — see [x](x.md) — for the rule"
	index := "Tags: a (1)\n- [X](x.md) — old\n" + other + "\n"
	got := patchMemoryIndex(index, "x.md", "- [X](x.md) — new")
	if want := "- [Other](other.md) — see [x](x.md) — for the rule\n- [X](x.md) — new\n"; got != want {
		t.Fatalf("patched %q, want %q", got, want)
	}
	if got := patchMemoryIndex(got, "x.md", ""); got != other+"\n" {
		t.Fatalf("delete patched %q", got)
	}
	if got := patchMemoryIndex(other+"\n", "other.md", ""); got != "" {
		t.Fatalf("patched away the last page line: %q", got)
	}
}

// An index line belongs to the page its leading link names, even when the
// title holds a link or ") — ", or the description quotes another page's link.
func TestMemoryIndexLineFor(t *testing.T) {
	t.Parallel()
	linked := memoryIndexLine(memoryPage{Path: "a.md", Title: `Use [Cents](money.md) ) — here \`, Description: "d"})
	for _, tc := range []struct {
		line, rel string
		want      bool
	}{
		{linked, "a.md", true},
		{linked, "money.md", false},
		{"- [A) — b](x.md) — desc", "x.md", true},
		{"- [X](x.md) — see [y](y.md) — note", "x.md", true},
		{"- [X](x.md) — see [y](y.md) — note", "y.md", false},
		{memoryIndexLine(memoryPage{Path: "s/p.md", Title: "T", Description: "d"}), "s/p.md", true},
		{"- [X](x.md.bak) — d", "x.md", false},
		{"Tags: a (1)", "a", false},
		{memoryIndexLine(memoryPage{Path: "my notes (old).md", Title: "T", Description: "d"}), "my notes (old).md", true},
		{memoryIndexLine(memoryPage{Path: "my notes (old).md", Title: "T", Description: "d"}), "old).md", false},
	} {
		if got := memoryIndexLineFor(tc.line, tc.rel); got != tc.want {
			t.Fatalf("%q for %q: got %t, want %t", tc.line, tc.rel, got, tc.want)
		}
	}
}
