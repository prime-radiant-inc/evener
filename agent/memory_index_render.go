package agent

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

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
	return memoryStampDate(p.ModTime)
}

// sortedMemoryPages orders pages newest first, ties by path.
func sortedMemoryPages(pages []memoryPage) []memoryPage {
	sorted := slices.Clone(pages)
	slices.SortStableFunc(sorted, func(a, b memoryPage) int {
		return cmp.Or(strings.Compare(memoryPageSortDate(b), memoryPageSortDate(a)), strings.Compare(a.Path, b.Path))
	})
	return sorted
}

// memoryTitleEscaper escapes a title for its index link, so the title's
// closing "](" is the line's first unescaped one (memoryIndexLineFor).
var memoryTitleEscaper = strings.NewReplacer(`\`, `\\`, `]`, `\]`)

// memoryIndexLine is one page's index line.
func memoryIndexLine(p memoryPage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- [%s](%s) — %s", memoryTitleEscaper.Replace(p.Title), p.Path, p.Description)
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

// memoryIndexLineFor reports whether line is the index line of the page at
// rel: its leading link "- [Title](rel) — ". The title ends at its first
// unescaped "](" (memoryIndexLine escapes "\\" and "]" in titles), and the
// path is matched whole from there, so neither a link inside the title nor
// one quoted in the description is read as this line's page.
func memoryIndexLineFor(line, rel string) bool {
	title, ok := strings.CutPrefix(line, "- [")
	if !ok {
		return false
	}
	link := "](" + rel + ") — "
	for i := 0; i < len(title); i++ {
		switch {
		case title[i] == '\\':
			i++
		case strings.HasPrefix(title[i:], "]("):
			return strings.HasPrefix(title[i:], link)
		}
	}
	return false
}

// patchMemoryIndex is index's page lines with the line of the page at rel
// replaced by line, or dropped when line is "": "" when no page line is left.
func patchMemoryIndex(index, rel, line string) string {
	var kept []string
	for _, existing := range memoryIndexLines(index) {
		if !memoryIndexLineFor(existing, rel) {
			kept = append(kept, existing+"\n")
		}
	}
	if line != "" {
		kept = append(kept, line+"\n")
	}
	return strings.Join(kept, "")
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

// memoryTagListBudget bounds, in bytes, the tags that a capped projection's
// header and its "Not shown" line each list. A scope can have
// hundreds of tags; listed in full they would push every page line out of
// the projection. Each list keeps its most-used tags and counts the rest, so
// the two together take about an eighth of the cap however many or long the
// tags are. 512 bytes holds about 25 typical tags.
const memoryTagListBudget = 512

// topMemoryTags is the most-used tags in counts (ties by name), most-used
// first, that fit memoryTagListBudget when each is written as part(tag)
// followed by ", ", and how many tags it leaves out.
func topMemoryTags(counts map[string]int, part func(tag string) string) (top []string, more int) {
	tags := slices.SortedFunc(maps.Keys(counts), func(a, b string) int {
		return cmp.Or(cmp.Compare(counts[b], counts[a]), strings.Compare(a, b))
	})
	size := 0
	for i, tag := range tags {
		size += len(part(tag)) + len(", ")
		if size > memoryTagListBudget {
			return tags[:i], len(tags) - i
		}
	}
	return tags, 0
}

// memoryTagParts is tags written as part(tag), then how many more tags a
// capped list leaves out, when it leaves any.
func memoryTagParts(tags []string, part func(tag string) string, more int) []string {
	parts := make([]string, 0, len(tags)+1)
	for _, tag := range tags {
		parts = append(parts, part(tag))
	}
	if more > 0 {
		parts = append(parts, "and "+pluralizedUnit(more, "more tag"))
	}
	return parts
}

// memoryTagsHeaderLine lists tags with their page counts, alphabetically, as
// a line, or is "" when no page has tags. The whole index lists every tag. A
// capped header, for a projection the whole index does not fit, lists only
// the most-used tags within memoryTagListBudget, then how many more there
// are.
func memoryTagsHeaderLine(pages []memoryPage, capped bool) string {
	counts, _ := memoryTagCounts(pages)
	if len(counts) == 0 {
		return ""
	}
	part := func(tag string) string { return fmt.Sprintf("%s (%d)", tag, counts[tag]) }
	tags, more := slices.Sorted(maps.Keys(counts)), 0
	if capped {
		tags, more = topMemoryTags(counts, part)
		slices.Sort(tags)
	}
	return "Tags: " + strings.Join(memoryTagParts(tags, part, more), ", ") + "\n"
}

// memoryNotShownLine closes a projection that leaves rest out: how many pages,
// then their most-used tags with counts, most common first, within
// memoryTagListBudget, then how many more tags there are, then the untagged
// count.
func memoryNotShownLine(rest []memoryPage) string {
	counts, untagged := memoryTagCounts(rest)
	part := func(tag string) string { return fmt.Sprintf("%s %d", tag, counts[tag]) }
	tags, more := topMemoryTags(counts, part)
	parts := memoryTagParts(tags, part, more)
	if untagged > 0 {
		parts = append(parts, fmt.Sprintf("untagged %d", untagged))
	}
	return fmt.Sprintf("Not shown: %s (%s).", pluralizedUnit(len(rest), "page"), strings.Join(parts, ", "))
}

// memoryPageLines is a scope's pages newest first and one index line per
// page.
func memoryPageLines(pages []memoryPage) (sorted []memoryPage, lines []string) {
	sorted = sortedMemoryPages(pages)
	lines = make([]string, len(sorted))
	for i, p := range sorted {
		lines[i] = memoryIndexLine(p) + "\n"
	}
	return sorted, lines
}

// renderMemoryIndex is a scope's whole generated index: the tag header, then
// one line per page, newest first. It is "" for a scope with no pages.
func renderMemoryIndex(pages []memoryPage) string {
	sorted, lines := memoryPageLines(pages)
	return memoryTagsHeaderLine(sorted, false) + strings.Join(lines, "")
}

// projectMemoryIndex is the index as projected into context within limit
// bytes: the whole index when it fits; otherwise the capped tag header, the
// newest lines that fit, and a closing line counting the pages left out. full
// is the whole index, rendered in the same pass.
func projectMemoryIndex(pages []memoryPage, limit int) (content, full string, truncated bool) {
	sorted, lines := memoryPageLines(pages)
	full = memoryTagsHeaderLine(sorted, false) + strings.Join(lines, "")
	if len(full) <= limit {
		return full, full, false
	}
	prefix := memoryTagsHeaderLine(sorted, true)
	sizes := make([]int, len(sorted)+1) // sizes[k]: prefix plus the first k lines
	sizes[0] = len(prefix)
	for i, line := range lines {
		sizes[i+1] = sizes[i] + len(line)
	}
	if sizes[len(sorted)] <= limit {
		return prefix + strings.Join(lines, ""), full, false
	}
	// Only a k whose first k lines fit can also hold the closing line.
	k := 0
	for k < len(sorted) && sizes[k+1] <= limit {
		k++
	}
	for ; k >= 0; k-- {
		closing := memoryNotShownLine(sorted[k:]) + "\n"
		if sizes[k]+len(closing) <= limit {
			return prefix + strings.Join(lines[:k], "") + closing, full, true
		}
	}
	return runetrim.Cut(prefix+memoryNotShownLine(sorted)+"\n", limit), full, true
}
