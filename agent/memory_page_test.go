package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
)

func TestParseMemoryPage(t *testing.T) {
	t.Parallel()
	mod := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, rel, raw string
		want           memoryPage
	}{
		{"full frontmatter", "cents.md",
			"---\ndescription: Money is integer cents, never floats\ntags: [Money, formatting, money]\nevidence: file:shop/price.go:Format\nupdated: 2026-10-08\nby: s1\nextra: kept\n---\n# Integer cents\nPrices are cents.\n",
			memoryPage{Path: "cents.md", Title: "Integer cents", Description: "Money is integer cents, never floats", HasDescription: true, Tags: []string{"money", "formatting"}, Updated: "2026-10-08", ModTime: mod}},
		{"multi-line description collapses", "a.md",
			"---\ndescription: |-\n  line one\n  line two\n---\nbody\n",
			memoryPage{Path: "a.md", Title: "a", Description: "line one line two", HasDescription: true, ModTime: mod}},
		{"single string tag, whitespace runs", "a.md",
			"---\ndescription: d\ntags: \"  Read Only   Vitest \"\n---\n",
			memoryPage{Path: "a.md", Title: "a", Description: "d", HasDescription: true, Tags: []string{"read-only-vitest"}, ModTime: mod}},
		{"non-string tags formatted, nested dropped, empty dropped", "a.md",
			"---\ndescription: d\ntags: [2026, \"\", {x: 1}, true]\n---\n",
			memoryPage{Path: "a.md", Title: "a", Description: "d", HasDescription: true, Tags: []string{"2026", "true"}, ModTime: mod}},
		{"string updated", "a.md",
			"---\ndescription: d\nupdated: \"2026-10-07\"\n---\n",
			memoryPage{Path: "a.md", Title: "a", Description: "d", HasDescription: true, Updated: "2026-10-07", ModTime: mod}},
		{"non-date updated ignored", "a.md",
			"---\ndescription: d\nupdated: yesterday\n---\n",
			memoryPage{Path: "a.md", Title: "a", Description: "d", HasDescription: true, ModTime: mod}},
		{"missing description falls back to heading", "sub/loader.md",
			"# Vitest read-only loader\n\nUse --configLoader runner.\n",
			memoryPage{Path: "sub/loader.md", Title: "Vitest read-only loader", Description: "Vitest read-only loader " + memoryNoDescription, ModTime: mod}},
		{"non-string description counts as missing", "a.md",
			"---\ndescription: 42\n---\nfirst line\n",
			memoryPage{Path: "a.md", Title: "a", Description: "first line " + memoryNoDescription, ModTime: mod}},
		{"fallback to first non-blank line", "a.md",
			"\n\n  first real line  \nsecond\n",
			memoryPage{Path: "a.md", Title: "a", Description: "first real line " + memoryNoDescription, ModTime: mod}},
		{"empty page", "empty.md", "",
			memoryPage{Path: "empty.md", Title: "empty", Description: memoryNoDescription, ModTime: mod}},
		{"unparseable frontmatter", "bad.md",
			"---\ndescription: Fix: use cents\n---\n# Cents rule\n",
			memoryPage{Path: "bad.md", Title: "Cents rule", Description: "Cents rule " + memoryNoDescription, Unreadable: true, ModTime: mod}},
		{"non-markdown file", "notes.txt", "anything",
			memoryPage{Path: "notes.txt", Title: "notes", Description: "notes.txt", ModTime: mod}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseMemoryPage(tc.rel, []byte(tc.raw), mod)
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
	got := parseMemoryPage("a.md", []byte(line+"\n"), time.Time{})
	want := string([]rune(line)[:120]) + " " + memoryNoDescription
	if got.Description != want {
		t.Fatalf("got %q want %q", got.Description, want)
	}
}

func TestListMemoryPages(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	env, err := execenv.NewConfinedFileEnvironment(root, filepath.Join("memory", "personal"))
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()
	scope := filepath.Join(root, "memory", "personal")
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
