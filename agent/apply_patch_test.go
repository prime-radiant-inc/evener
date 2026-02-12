package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatch_AddUpdateMoveDelete(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "d.txt"), []byte("delete me\n"), 0o644)

	patch := `*** Begin Patch
*** Add File: b.txt
+hello
+world
*** Update File: a.txt
@@
 one
-two
+TWO
 three
*** Update File: b.txt
*** Move to: c.txt
@@
 hello
 world
*** Delete File: d.txt
*** End Patch
`
	out, err := ApplyPatch(dir, patch)
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	if !strings.Contains(out, "b.txt") || !strings.Contains(out, "a.txt") || !strings.Contains(out, "c.txt") {
		t.Fatalf("output: %q", out)
	}

	// a.txt updated
	ab, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatalf("read a.txt: %v", err)
	}
	if strings.TrimSpace(string(ab)) != "one\nTWO\nthree" {
		t.Fatalf("a.txt: %q", string(ab))
	}
	// b.txt moved to c.txt
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); err == nil {
		t.Fatalf("expected b.txt to be moved away")
	}
	b, err := os.ReadFile(filepath.Join(dir, "c.txt"))
	if err != nil {
		t.Fatalf("read c.txt: %v", err)
	}
	if strings.TrimSpace(string(b)) != "hello\nworld" {
		t.Fatalf("c.txt: %q", string(b))
	}

	// d.txt deleted
	if _, err := os.Stat(filepath.Join(dir, "d.txt")); err == nil {
		t.Fatalf("expected d.txt to be deleted")
	}
}

func TestApplyPatch_EndOfFileMarker(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("line1\nline2\nline3\n"), 0o644)

	patch := "*** Begin Patch\n*** Update File: f.txt\n@@ line1\n line1\n-line2\n+replaced\n line3\n*** End of File\n*** End Patch\n"
	_, err := ApplyPatch(dir, patch)
	if err != nil {
		t.Fatalf("ApplyPatch with End of File marker: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if !strings.Contains(string(got), "replaced") {
		t.Fatal("patch not applied")
	}
}

func TestApplyPatch_FuzzyWhitespaceMatching(t *testing.T) {
	dir := t.TempDir()
	// File has trailing spaces and tabs mixed in
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("  hello world  \n  goodbye\t\n"), 0o644)

	patch := "*** Begin Patch\n*** Update File: f.txt\n@@ hello\n hello world\n-goodbye\n+farewell\n*** End Patch\n"
	_, err := ApplyPatch(dir, patch)
	if err != nil {
		t.Fatalf("ApplyPatch with whitespace mismatch: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if !strings.Contains(string(got), "farewell") {
		t.Fatal("fuzzy match patch not applied")
	}
}

func TestApplyPatch_ContextHintDisambiguates(t *testing.T) {
	dir := t.TempDir()
	content := "func foo():\n    return 1\n\nfunc bar():\n    return 1\n"
	os.WriteFile(filepath.Join(dir, "f.py"), []byte(content), 0o644)

	// Both functions have "    return 1" — the @@ hint disambiguates
	patch := "*** Begin Patch\n*** Update File: f.py\n@@ func bar():\n-    return 1\n+    return 2\n*** End Patch\n"
	_, err := ApplyPatch(dir, patch)
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "f.py"))
	lines := strings.Split(string(got), "\n")
	// foo should still return 1, bar should return 2
	if !strings.Contains(lines[1], "return 1") {
		t.Fatal("foo's return was modified")
	}
	if !strings.Contains(lines[4], "return 2") {
		t.Fatal("bar's return was not modified")
	}
}

func TestApplyPatch_MultiHunkSingleFile(t *testing.T) {
	dir := t.TempDir()
	content := "alpha\nbeta\ngamma\ndelta\nepsilon\n"
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte(content), 0o644)

	patch := "*** Begin Patch\n*** Update File: f.txt\n@@ alpha\n alpha\n-beta\n+BETA\n@@ delta\n delta\n-epsilon\n+EPSILON\n*** End Patch\n"
	_, err := ApplyPatch(dir, patch)
	if err != nil {
		t.Fatalf("ApplyPatch multi-hunk: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	s := string(got)
	if !strings.Contains(s, "BETA") {
		t.Fatal("first hunk not applied")
	}
	if !strings.Contains(s, "EPSILON") {
		t.Fatal("second hunk not applied")
	}
	if !strings.Contains(s, "gamma") {
		t.Fatal("unchanged line gamma should be preserved")
	}
}

func TestApplyPatch_FuzzyMatch_UnicodeQuotes(t *testing.T) {
	// File uses straight quotes, patch uses curly quotes.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.go"), []byte("fmt.Println(\"hello world\")\n"), 0o644)

	// Build a v4a-style patch that uses curly quotes in the delete line.
	patch := "*** Begin Patch\n*** Update File: test.go\n@@\n-fmt.Println(\u201Chello world\u201D)\n+fmt.Println(\"goodbye world\")\n*** End Patch\n"

	result, err := ApplyPatch(dir, patch)
	if err != nil {
		t.Fatalf("expected patch to apply with Unicode fuzzy matching: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "test.go"))
	if !strings.Contains(string(got), "goodbye world") {
		t.Errorf("expected result to contain 'goodbye world', got:\n%s\nresult: %s", string(got), result)
	}
}

func TestApplyPatch_RejectsPathTraversalAndAbsolutePaths(t *testing.T) {
	dir := t.TempDir()
	cases := []string{
		`*** Begin Patch
*** Add File: ../x.txt
+nope
*** End Patch
`,
		`*** Begin Patch
*** Add File: /abs.txt
+nope
*** End Patch
`,
	}
	for _, p := range cases {
		if _, err := ApplyPatch(dir, p); err == nil {
			t.Fatalf("expected error for patch:\n%s", p)
		}
	}
}
