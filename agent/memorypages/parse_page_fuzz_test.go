package memorypages

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/fuzz/edgeseeds"
)

// FuzzParsePage drives ParsePage, the decode seam from a page's bytes to its
// index entry. Input is an arbitrary Markdown page. Beyond no-panic it asserts
// what the generated index relies on: the description and when line are one
// line, the page's index line is one line, a page that claims its own
// description has a non-empty one, and parsing is deterministic.
func FuzzParsePage(f *testing.F) {
	seeds := []string{
		"---\ndescription: d\nwhen: w\ntags: [a, b]\nupdated: 2026-10-08\nby: s1\n---\n# Title\nbody\n",
		"---\nwhen: When code handles money\n---\n\n# Money\n",
		"# Heading only\n",
		"no heading, first line\nsecond\n",
		"---\ndescription: a: b\n---\nx\n",
		"---\ndescription: |\n  two\n  lines\n---\n",
		"```\n# fenced\n```\n# real\n",
		"",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	for _, s := range edgeseeds.FrontmatterYAML() {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		p := ParsePage("a.md", []byte(raw), time.Time{})
		if strings.ContainsAny(p.Description, "\r\n") || strings.ContainsAny(p.When, "\r\n") {
			t.Fatalf("multi-line description %q or when %q for %q", p.Description, p.When, raw)
		}
		if p.HasDescription && p.Description == "" {
			t.Fatalf("HasDescription with an empty description for %q", raw)
		}
		if line := IndexLine(p); strings.ContainsAny(line, "\r\n") {
			t.Fatalf("multi-line index line %q for %q", line, raw)
		}
		if again := ParsePage("a.md", []byte(raw), time.Time{}); !reflect.DeepEqual(p, again) {
			t.Fatalf("ParsePage not deterministic for %q: %+v vs %+v", raw, p, again)
		}
	})
}
