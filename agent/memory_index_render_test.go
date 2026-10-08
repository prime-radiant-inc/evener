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
	content, truncated := projectMemoryIndex(pages, memoryProjectionCap)
	if truncated || content != renderMemoryIndex(pages) {
		t.Fatalf("truncated=%t content=%q", truncated, content)
	}
}

func TestProjectMemoryIndexKeepsNewestAndCountsTheRest(t *testing.T) {
	t.Parallel()
	pages := overflowPages(200)
	full := renderMemoryIndex(pages)
	content, truncated := projectMemoryIndex(pages, memoryProjectionCap)
	if !truncated || len(content) > memoryProjectionCap {
		t.Fatalf("truncated=%t len=%d", truncated, len(content))
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

// Review Focus 2: one page larger than the whole budget is never cut mid-line.
func TestProjectMemoryIndexHugeLine(t *testing.T) {
	t.Parallel()
	pages := []memoryPage{
		{Path: "huge.md", Title: "huge", Description: strings.Repeat("y", 9000), HasDescription: true, Tags: []string{"big"}, Updated: "2026-10-08"},
		{Path: "small.md", Title: "small", Description: "fits", HasDescription: true, Updated: "2026-10-01"},
	}
	content, truncated := projectMemoryIndex(pages, memoryProjectionCap)
	if !truncated || len(content) > memoryProjectionCap || strings.Contains(content, "yyy") {
		t.Fatalf("truncated=%t len=%d content=%.200q", truncated, len(content), content)
	}
	if !strings.HasSuffix(content, memoryNotShownLine(sortedMemoryPages(pages))+"\n") {
		t.Fatalf("content %q does not end with the Not shown line for both pages", content)
	}
}

// Ruling 13: a header too large to fit is cut at the budget, still truncated.
func TestProjectMemoryIndexOversizedHeader(t *testing.T) {
	t.Parallel()
	var pages []memoryPage
	for i := range 600 {
		pages = append(pages, memoryPage{Path: fmt.Sprintf("p%03d.md", i), Title: "t", Description: "d", HasDescription: true, Tags: []string{fmt.Sprintf("tag-number-%03d", i)}})
	}
	content, truncated := projectMemoryIndex(pages, memoryProjectionCap)
	if !truncated || len(content) > memoryProjectionCap || !strings.HasPrefix(content, "Tags: ") {
		t.Fatalf("truncated=%t len=%d", truncated, len(content))
	}
}
