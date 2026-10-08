package agent

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"primeradiant.com/evener/agent/internal/runetrim"
)

// memoryProjectionCap bounds, in bytes, the index a scope projects into context.
const memoryProjectionCap = 8192

// memoryPageSortDate is the date a page sorts by: its stamp, else its file's
// modification date.
func memoryPageSortDate(p memoryPage) string {
	if p.Updated != "" {
		return p.Updated
	}
	return p.ModTime.UTC().Format(time.DateOnly)
}

// sortedMemoryPages orders pages newest first, ties by path.
func sortedMemoryPages(pages []memoryPage) []memoryPage {
	sorted := slices.Clone(pages)
	slices.SortStableFunc(sorted, func(a, b memoryPage) int {
		return cmp.Or(strings.Compare(memoryPageSortDate(b), memoryPageSortDate(a)), strings.Compare(a.Path, b.Path))
	})
	return sorted
}

// memoryIndexLine is one page's index line.
func memoryIndexLine(p memoryPage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- [%s](%s) — %s", p.Title, p.Path, p.Description)
	if p.Unreadable {
		b.WriteString(" " + memoryFrontmatterUnreadable)
	}
	if len(p.Tags) > 0 {
		b.WriteString(" [" + strings.Join(p.Tags, ", ") + "]")
	}
	if p.Updated != "" {
		b.WriteString(" (updated " + p.Updated + ")")
	}
	return b.String()
}

// memoryTagCounts counts pages per tag; a page counts once under each tag.
func memoryTagCounts(pages []memoryPage) (counts map[string]int, untagged int) {
	counts = make(map[string]int)
	for _, p := range pages {
		if len(p.Tags) == 0 {
			untagged++
		}
		for _, tag := range p.Tags {
			counts[tag]++
		}
	}
	return counts, untagged
}

// memoryTagsHeader lists every tag with its page count, alphabetically, or
// is "" when no page has tags.
func memoryTagsHeader(pages []memoryPage) string {
	counts, _ := memoryTagCounts(pages)
	if len(counts) == 0 {
		return ""
	}
	var parts []string
	for _, tag := range slices.Sorted(maps.Keys(counts)) {
		parts = append(parts, fmt.Sprintf("%s (%d)", tag, counts[tag]))
	}
	return "Tags: " + strings.Join(parts, ", ")
}

// memoryNotShownLine closes a projection that leaves rest out: how many pages
// and their tags, most common first, then the routes to them.
func memoryNotShownLine(rest []memoryPage) string {
	counts, untagged := memoryTagCounts(rest)
	tags := slices.SortedFunc(maps.Keys(counts), func(a, b string) int {
		return cmp.Or(cmp.Compare(counts[b], counts[a]), strings.Compare(a, b))
	})
	var parts []string
	for _, tag := range tags {
		parts = append(parts, fmt.Sprintf("%s %d", tag, counts[tag]))
	}
	if untagged > 0 {
		parts = append(parts, fmt.Sprintf("untagged %d", untagged))
	}
	noun := "pages"
	if len(rest) == 1 {
		noun = "page"
	}
	return fmt.Sprintf("Not shown: %d %s (%s). Read the full index with memory_read(\"MEMORY.md\"), or find a tag's pages with memory_search.", len(rest), noun, strings.Join(parts, ", "))
}

// renderMemoryIndex is a scope's whole generated index: the tag header, then
// one line per page, newest first. It is "" for a scope with no pages.
func renderMemoryIndex(pages []memoryPage) string {
	var b strings.Builder
	sorted := sortedMemoryPages(pages)
	if header := memoryTagsHeader(sorted); header != "" {
		b.WriteString(header + "\n")
	}
	for _, p := range sorted {
		b.WriteString(memoryIndexLine(p) + "\n")
	}
	return b.String()
}

// projectMemoryIndex is the index as projected into context within limit
// bytes: the whole index when it fits; otherwise the header, the newest lines
// that fit, and a closing line counting the pages left out.
func projectMemoryIndex(pages []memoryPage, limit int) (string, bool) {
	if full := renderMemoryIndex(pages); len(full) <= limit {
		return full, false
	}
	sorted := sortedMemoryPages(pages)
	prefix := ""
	if header := memoryTagsHeader(sorted); header != "" {
		prefix = header + "\n"
	}
	lines := make([]string, len(sorted))
	sizes := make([]int, len(sorted)+1) // sizes[k]: prefix plus the first k lines
	sizes[0] = len(prefix)
	for i, p := range sorted {
		lines[i] = memoryIndexLine(p) + "\n"
		sizes[i+1] = sizes[i] + len(lines[i])
	}
	for k := range slices.Backward(sorted) {
		closing := memoryNotShownLine(sorted[k:]) + "\n"
		if sizes[k]+len(closing) <= limit {
			return prefix + strings.Join(lines[:k], "") + closing, true
		}
	}
	return runetrim.Cut(prefix+memoryNotShownLine(sorted)+"\n", limit), true
}
