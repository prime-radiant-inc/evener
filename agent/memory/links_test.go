package memory

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// Removing AST-based filtering would turn code/citations into false dangling links.
func TestLinksInlineCodeFencesAndExternalCitations(t *testing.T) {
	t.Parallel()
	source := "[A\\]](a.md#part) and [index](index.md)\n`[bad](missing.md)`\n```markdown\n[bad](missing.md)\n```\n    [bad](missing.md)\n\n[external](https://example.invalid/path.md) [citation](docs/source.go)\n![image](missing.md) [reference][ref] [ref]\n\n[ref]: missing.md\n"
	got, err := localLinks(source)
	if err != nil {
		t.Fatal(err)
	}
	want := []Reference{{Destination: "a.md#part", Line: 1}, {Destination: "index.md", Line: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
	got, err = localLinks("[*formatted*](a.md)\n[](b.md) [multi\nline](c.md)\n")
	if err != nil || !reflect.DeepEqual(got, []Reference{{Destination: "a.md", Line: 1}, {Destination: "b.md", Line: 2}, {Destination: "c.md", Line: 2}}) {
		t.Fatalf("locations %#v %v", got, err)
	}
}

func TestIdentifiersAndLocalDestinations(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"a", "build-cache-2", strings.Repeat("a", 64)} {
		r := ApplyRequest{OperationID: "valid", Pages: []PageOperation{{Kind: "put", PageID: id, Body: str("")}}, Index: str("- [A](" + id + ".md): S\n")}
		if _, _, err := buildBatch(Snapshot{}, r, Actor{}, at); err != nil {
			t.Errorf("valid %q: %v", id, err)
		}
	}
	for _, id := range []string{"index", "metadata", "receipts", "changes", "pending", "lock", "A", "a_b", "a.b", "%2f", "/abs", "a/b", "a\\b", "é", strings.Repeat("a", 65), "a--b", "a-", "\x00"} {
		_, _, err := buildBatch(Snapshot{}, ApplyRequest{OperationID: "bad", Pages: []PageOperation{{Kind: "put", PageID: id, Body: str("")}}}, Actor{}, at)
		assertCode(t, err, "invalid_input")
	}
	for _, dest := range []string{"../a.md", "./a.md", "/a.md", "dir/a.md", "dir\\a.md", "a%2fb.md", "%61.md", "a%2emd", "a.md?query", "a.MD", "metadata.md", "A.md", "file:///tmp/a.md", "a&#46;md", "a\\.md"} {
		_, err := localLinks("[X](" + dest + ")\n")
		if err == nil {
			t.Errorf("accepted destination %q", dest)
		} else {
			assertCode(t, err, "invalid_input")
		}
	}
	for _, dest := range []string{"a.md", "build-cache-2.md#section", "index.md#part", "https://example.invalid/a.md", "mailto:someone@example.invalid", "docs/source.go", "#anchor"} {
		if _, err := localLinks("[X](" + dest + ")\n"); err != nil {
			t.Errorf("valid destination %q: %v", dest, err)
		}
	}
	if _, err := localLinks("\xff"); err == nil {
		t.Fatal("accepted invalid UTF-8")
	}
}

func TestLinksWindowsPathsAndInertCitationPercent(t *testing.T) {
	t.Parallel()
	for _, dest := range []string{"C:/a.md", `C:\a.md`} {
		_, err := localLinks("[X](" + dest + ")")
		if err == nil {
			t.Errorf("accepted local absolute destination %q", dest)
		}
	}
	if _, err := localLinks("[citation](docs/percent%note.go)"); err != nil {
		t.Errorf("inert citation rejected: %v", err)
	}
}
