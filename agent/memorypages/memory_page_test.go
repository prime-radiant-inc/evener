package memorypages

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
)

// listMemoryPages lists env's scope with no cancellation.
func listMemoryPages(env *execenv.LocalExecutionEnvironment) ([]Page, error) {
	return ListPagesCtx(context.Background(), env)
}

func TestParseMemoryPage(t *testing.T) {
	t.Parallel()
	mod := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, rel, raw string
		want           Page
	}{
		{"full frontmatter", "cents.md",
			"---\ndescription: Money is integer cents, never floats\ntags: [Money, formatting, money]\nevidence: file:shop/price.go:Format\nupdated: 2026-10-08\nby: s1\nextra: kept\n---\n# Integer cents\nPrices are cents.\n",
			Page{Path: "cents.md", Description: "Money is integer cents, never floats", HasDescription: true, Tags: []string{"money", "formatting"}, Updated: "2026-10-08", By: "s1", Frontmatter: true, ModTime: mod}},
		{"multi-line description collapses", "a.md",
			"---\ndescription: |-\n  line one\n  line two\n---\nbody\n",
			Page{Path: "a.md", Description: "line one line two", HasDescription: true, Frontmatter: true, ModTime: mod}},
		{"single string tag, whitespace runs", "a.md",
			"---\ndescription: d\ntags: \"  Read Only   Vitest \"\n---\n",
			Page{Path: "a.md", Description: "d", HasDescription: true, Tags: []string{"read-only-vitest"}, Frontmatter: true, ModTime: mod}},
		{"non-string tags formatted, nested dropped, empty dropped", "a.md",
			"---\ndescription: d\ntags: [2026, \"\", {x: 1}, true]\n---\n",
			Page{Path: "a.md", Description: "d", HasDescription: true, Tags: []string{"2026", "true"}, Frontmatter: true, ModTime: mod}},
		{"string updated", "a.md",
			"---\ndescription: d\nupdated: \"2026-10-07\"\n---\n",
			Page{Path: "a.md", Description: "d", HasDescription: true, Updated: "2026-10-07", Frontmatter: true, ModTime: mod}},
		{"non-date updated ignored", "a.md",
			"---\ndescription: d\nupdated: yesterday\n---\n",
			Page{Path: "a.md", Description: "d", HasDescription: true, Frontmatter: true, ModTime: mod}},
		{"when field", "a.md",
			"---\ndescription: d\nwhen: Writing a commit message in this repo\n---\n",
			Page{Path: "a.md", Description: "d", HasDescription: true, When: "Writing a commit message in this repo", Frontmatter: true, ModTime: mod}},
		{"multi-line when collapses", "a.md",
			"---\ndescription: d\nwhen: |-\n  before changing\n  Total\n---\n",
			Page{Path: "a.md", Description: "d", HasDescription: true, When: "before changing Total", Frontmatter: true, ModTime: mod}},
		{"non-string when counts as missing", "a.md",
			"---\ndescription: d\nwhen: [a, b]\n---\n",
			Page{Path: "a.md", Description: "d", HasDescription: true, Frontmatter: true, ModTime: mod}},
		{"empty when counts as missing", "a.md",
			"---\ndescription: d\nwhen: \"  \"\n---\n",
			Page{Path: "a.md", Description: "d", HasDescription: true, Frontmatter: true, ModTime: mod}},
		{"a page with a when and no description carries no marker", "a.md",
			"---\nwhen: Pricing a cart\n---\n# Cart pricing\nbody\n",
			Page{Path: "a.md", Description: "Cart pricing", When: "Pricing a cart", Frontmatter: true, ModTime: mod}},
		{"a page with a when and no heading or body carries no marker", "a.md",
			"---\nwhen: Pricing a cart\n---\n",
			Page{Path: "a.md", When: "Pricing a cart", Frontmatter: true, ModTime: mod}},
		{"unreadable frontmatter drops when", "bad.md",
			"---\ndescription: Fix: use cents\nwhen: w\n---\n# Cents rule\n",
			Page{Path: "bad.md", Description: "Cents rule " + NoDescription, Unreadable: true, Frontmatter: true, ModTime: mod}},
		{"missing description falls back to heading", "sub/loader.md",
			"# Vitest read-only loader\n\nUse --configLoader runner.\n",
			Page{Path: "sub/loader.md", Description: "Vitest read-only loader " + NoDescription, ModTime: mod}},
		{"non-string description counts as missing", "a.md",
			"---\ndescription: 42\n---\nfirst line\n",
			Page{Path: "a.md", Description: "first line " + NoDescription, Frontmatter: true, ModTime: mod}},
		{"fallback to first non-blank line", "a.md",
			"\n\n  first real line  \nsecond\n",
			Page{Path: "a.md", Description: "first real line " + NoDescription, ModTime: mod}},
		{"empty page", "empty.md", "",
			Page{Path: "empty.md", Description: NoDescription, ModTime: mod}},
		{"a null frontmatter block is still frontmatter", "a.md",
			"---\n~\n---\n# T\n",
			Page{Path: "a.md", Description: "T " + NoDescription, Frontmatter: true, ModTime: mod}},
		{"an empty frontmatter block is still frontmatter", "a.md",
			"---\n---\n# T\n",
			Page{Path: "a.md", Description: "T " + NoDescription, Frontmatter: true, ModTime: mod}},
		{"unparseable frontmatter", "bad.md",
			"---\ndescription: Fix: use cents\n---\n# Cents rule\n",
			Page{Path: "bad.md", Description: "Cents rule " + NoDescription, Unreadable: true, Frontmatter: true, ModTime: mod}},
		{"heading inside a fence is skipped", "a.md",
			"```\n# not a heading\n```\n## Real title\n",
			Page{Path: "a.md", Description: "Real title " + NoDescription, ModTime: mod}},
		{"tilde fence needs a matching close", "a.md",
			"~~~\n```\n# still fenced\n~~~\n# After\n",
			Page{Path: "a.md", Description: "After " + NoDescription, ModTime: mod}},
		{"indented heading", "a.md",
			"  ## Loader notes\n",
			Page{Path: "a.md", Description: "Loader notes " + NoDescription, ModTime: mod}},
		{"four-space indent is code", "a.md",
			"    # code\n# Real\n",
			Page{Path: "a.md", Description: "Real " + NoDescription, ModTime: mod}},
		{"tab indent is code", "a.md",
			"\t# code\n \t# code too\n# Real\n",
			Page{Path: "a.md", Description: "Real " + NoDescription, ModTime: mod}},
		{"long fence not closed by a short one", "a.md",
			"````\n```\n# not a heading\n````\n# After\n",
			Page{Path: "a.md", Description: "After " + NoDescription, ModTime: mod}},
		{"backtick in a backtick fence's info string opens no fence", "a.md",
			"```go`x\n# Heading\n",
			Page{Path: "a.md", Description: "Heading " + NoDescription, ModTime: mod}},
		{"backtick in a tilde fence's info string still opens it", "a.md",
			"~~~go`x\n# fenced\n~~~\n# After\n",
			Page{Path: "a.md", Description: "After " + NoDescription, ModTime: mod}},
		{"closing hashes trimmed", "a.md",
			"## Title ##\n",
			Page{Path: "a.md", Description: "Title " + NoDescription, ModTime: mod}},
		{"hash without space stays", "a.md",
			"# C#\n",
			Page{Path: "a.md", Description: "C# " + NoDescription, ModTime: mod}},
		{"a carriage return ends a heading", "a.md",
			"# A\rB\n",
			Page{Path: "a.md", Description: "A " + NoDescription, ModTime: mod}},
		{"carriage-return-only line endings", "a.md",
			"\r# Title\rbody\r",
			Page{Path: "a.md", Description: "Title " + NoDescription, ModTime: mod}},
		{"CRLF line endings", "a.md",
			"# Title\r\nbody\r\n",
			Page{Path: "a.md", Description: "Title " + NoDescription, ModTime: mod}},
		{"CRLF frontmatter", "a.md",
			"---\r\ndescription: d\r\ntags: [x]\r\n---\r\n# T\r\n",
			Page{Path: "a.md", Description: "d", HasDescription: true, Tags: []string{"x"}, Frontmatter: true, ModTime: mod}},
		{"carriage-return-only frontmatter", "a.md",
			"---\rdescription: d\r---\r# T\r",
			Page{Path: "a.md", Description: "d", HasDescription: true, Frontmatter: true, ModTime: mod}},
		{"a carriage return ends the fallback line", "a.md",
			"first\rsecond\n",
			Page{Path: "a.md", Description: "first " + NoDescription, ModTime: mod}},
		{"whitespace runs in a heading collapse", "a.md",
			"# A \t\v\f B\n",
			Page{Path: "a.md", Description: "A B " + NoDescription, ModTime: mod}},
		{"whitespace runs in the fallback line collapse", "a.md",
			"one\t\vtwo\n",
			Page{Path: "a.md", Description: "one two " + NoDescription, ModTime: mod}},
		{"non-markdown file", "notes.txt", "anything",
			Page{Path: "notes.txt", Description: "notes.txt", ModTime: mod}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParsePage(tc.rel, []byte(tc.raw), mod)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// The fallback description is cut to 120 characters (runes, not bytes).
func TestParseMemoryPageFallbackCut(t *testing.T) {
	t.Parallel()
	line := strings.Repeat("é", 130)
	got := ParsePage("a.md", []byte(line+"\n"), time.Time{})
	want := string([]rune(line)[:120]) + " " + NoDescription
	if got.Description != want {
		t.Fatalf("got %q want %q", got.Description, want)
	}
}

func TestListMemoryPages(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	env, err := execenv.NewConfinedFileEnvironment(root, "memory")
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()
	scope := filepath.Join(root, "memory")
	for rel, body := range map[string]string{
		"a.md":                     "---\ndescription: alpha\n---\n",
		"sub/b.md":                 "# Bravo\n",
		"sub/MEMORY.md":            "nested index is a page\n",
		"empty.md":                 "",
		"notes.txt":                "plain",
		"MEMORY.md":                "root index is never a page\n",
		".MEMORY.md.pre-generated": "backup\n",
		".hidden/c.md":             "hidden\n",
		"sub/.draft.md":            "hidden\n",
	} {
		path := filepath.Join(scope, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(scope, "a.md"), filepath.Join(scope, "link.md")); err != nil {
		t.Fatal(err)
	}
	pages, err := listMemoryPages(env)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, p := range pages {
		paths = append(paths, p.Path)
	}
	slices.Sort(paths)
	want := []string{"a.md", "empty.md", "notes.txt", "sub/MEMORY.md", "sub/b.md"}
	if !slices.Equal(paths, want) {
		t.Fatalf("pages=%v want %v", paths, want)
	}
	for _, p := range pages {
		if p.ModTime.IsZero() {
			t.Fatalf("%s has no ModTime", p.Path)
		}
	}
}

// The root index is generated on every filesystem, so its name is excluded
// whatever the case; a nested file of that name is an ordinary page.
func TestIsMemoryPagePathExcludesTheRootIndexInAnyCase(t *testing.T) {
	t.Parallel()
	for rel, want := range map[string]bool{
		"MEMORY.md":     false,
		"memory.md":     false,
		"Memory.MD":     false,
		"sub/MEMORY.md": true,
		"sub/memory.md": true,
		"memory.md.txt": true,
		"notes.md":      true,
	} {
		if got := IsPagePath(rel); got != want {
			t.Errorf("IsPagePath(%q)=%v, want %v", rel, got, want)
		}
	}
}

// A name whose case differs from the only listed name matching it resolves
// to that name; an exact match wins, and an ambiguous or missing one does not
// resolve.
func TestMatchMemoryNameCase(t *testing.T) {
	t.Parallel()
	listed := map[string]bool{"notes.md": true, "a.md": true, "A.md": true, "Exact.md": true, "exact.md": true}
	for _, tc := range []struct {
		link, want string
		ok         bool
	}{
		{"notes.md", "notes.md", true},
		{"Notes.md", "notes.md", true},
		{"Exact.md", "Exact.md", true},
		{"a.MD", "", false},
		{"missing.md", "", false},
	} {
		if got, ok := matchMemoryNameCase(tc.link, maps.Keys(listed)); got != tc.want || ok != tc.ok {
			t.Fatalf("%s: got %q, %t; want %q, %t", tc.link, got, ok, tc.want, tc.ok)
		}
	}
}

// Each segment of a path takes the spelling its directory lists, matched
// ignoring case on any filesystem; a segment its directory doesn't list, and
// everything below it, is kept as given.
func TestListedMemoryPagePath(t *testing.T) {
	t.Parallel()
	env, scope := newMemoryMigrateScope(t)
	if err := os.MkdirAll(filepath.Join(scope, "Notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"fact.md", filepath.Join("Notes", "x.md")} {
		if err := os.WriteFile(filepath.Join(scope, name), []byte("body\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{
		"fact.md": "fact.md", "Notes/x.md": "Notes/x.md",
		"Fact.md": "fact.md", "notes/X.md": "Notes/x.md",
		"new.md": "new.md", "New/y.md": "New/y.md",
	}
	for rel, listed := range want {
		if got := ListedPagePath(env, rel); got != listed {
			t.Fatalf("%s: got %q, want %q", rel, got, listed)
		}
	}
}
