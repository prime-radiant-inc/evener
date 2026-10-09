package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// ListMemoryScopePages reads a scope directory's pages as the generated index
// does, for tools outside the agent such as the memory lab.
func TestListMemoryScopePages(t *testing.T) {
	t.Parallel()
	scope := filepath.Join(t.TempDir(), "scope")
	for rel, body := range map[string]string{
		"a.md":      "---\ndescription: alpha   one\ntags: [Coupons]\nupdated: 2026-03-02\nby: seed-fixture\n---\n# A\n",
		"bad.md":    "---\ndescription: Cart rule 7: whole numbers\n---\n# Bad\n",
		"MEMORY.md": "root index is never a page\n",
		"crlf.md":   "---\r\ndescription: d\r\n---\r\n",
		"plain.md":  "---\ntags: [x]\n---\nno description\n",
	} {
		path := filepath.Join(scope, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ListMemoryScopePages(scope)
	if err != nil {
		t.Fatal(err)
	}
	want := []MemoryScopePage{
		{Path: "a.md", Description: "alpha one", HasDescription: true, Frontmatter: true, Tags: []string{"coupons"}, Updated: "2026-03-02", By: "seed-fixture"},
		{Path: "bad.md", Description: "Bad " + memoryNoDescription, Frontmatter: true, Unreadable: true, Tags: []string{}},
		{Path: "crlf.md", Description: "--- " + memoryNoDescription, Tags: []string{}},
		{Path: "plain.md", Description: "no description " + memoryNoDescription, Frontmatter: true, Tags: []string{"x"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	// A page with no tags encodes them as an empty list, never null.
	if raw, err := json.Marshal(got[1].Tags); err != nil || string(raw) != "[]" {
		t.Fatalf("bad.md tags encoded as %s (%v), want []", raw, err)
	}
}

// Listing never creates a scope, and a path that isn't a directory is an error.
func TestListMemoryScopePagesNeedsAnExistingDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	absent := filepath.Join(dir, "absent")
	if _, err := ListMemoryScopePages(absent); err == nil {
		t.Fatal("want an error for a missing scope directory")
	}
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Fatalf("listing created the missing scope: stat err=%v", err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListMemoryScopePages(file); err == nil {
		t.Fatal("want an error for a scope that is a file")
	}
}

// A relative scope directory is read relative to the working directory.
func TestListMemoryScopePagesReadsARelativeDirectory(t *testing.T) {
	parent := t.TempDir()
	scope := filepath.Join(parent, "scope")
	if err := os.MkdirAll(scope, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scope, "a.md"), []byte("---\ndescription: d\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(parent)
	got, err := ListMemoryScopePages("scope")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != "a.md" {
		t.Fatalf("got %+v, want a.md", got)
	}
}
