package tool

import (
	"fmt"
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
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

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
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("  hello world  \n  goodbye\t\n"), 0o644); err != nil {
		t.Fatal(err)
	}

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
	if err := os.WriteFile(filepath.Join(dir, "f.py"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

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

func TestApplyPatch_SearchesWholeOldBlock(t *testing.T) {
	dir := t.TempDir()
	content := strings.Join([]string{
		"func first() {",
		"\tmodel := \"openai/gpt-5\"",
		"\treturn model",
		"}",
		"",
		"func second() {",
		"\tmodel := \"openai/gpt-5\"",
		"\tconfig := loadConfig()",
		"}",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	patch := "*** Begin Patch\n*** Update File: f.go\n@@\n \tmodel := \"openai/gpt-5\"\n \tconfig := loadConfig()\n+\tfastCheapModel := \"openai/gpt-5-mini\"\n }\n*** End Patch\n"
	_, err := ApplyPatch(dir, patch)
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "f.go"))
	text := string(got)
	if strings.Contains(text, "func first() {\n\tmodel := \"openai/gpt-5\"\n\tfastCheapModel") {
		t.Fatalf("patch applied to first matching anchor instead of whole block:\n%s", text)
	}
	if !strings.Contains(text, "func second() {\n\tmodel := \"openai/gpt-5\"\n\tconfig := loadConfig()\n\tfastCheapModel := \"openai/gpt-5-mini\"\n}") {
		t.Fatalf("patch did not apply to whole-block match:\n%s", text)
	}
}

func TestApplyPatch_HintCanBeFirstOldLine(t *testing.T) {
	dir := t.TempDir()
	content := strings.Join([]string{
		"func target() {",
		"\treturn oldValue",
		"}",
		"",
		"func unrelated() {",
		"\treturn oldValue",
		"}",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	patch := "*** Begin Patch\n*** Update File: f.go\n@@ func unrelated() {\n func unrelated() {\n-\treturn oldValue\n+\treturn newValue\n }\n*** End Patch\n"
	_, err := ApplyPatch(dir, patch)
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "f.go"))
	text := string(got)
	if strings.Contains(text, "func target() {\n\treturn newValue\n}") {
		t.Fatalf("patch applied to earlier block instead of hinted block:\n%s", text)
	}
	if !strings.Contains(text, "func unrelated() {\n\treturn newValue\n}") {
		t.Fatalf("patch did not update hinted block:\n%s", text)
	}
}

func TestApplyPatch_SequenceSearchMatchesOpenAILeniency(t *testing.T) {
	dir := t.TempDir()
	content := "func f() {\n\toldCall()   \n\treturn\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	patch := "*** Begin Patch\n*** Update File: f.go\n@@\n \toldCall()\n \treturn\n+\t// done\n*** End Patch\n"
	_, err := ApplyPatch(dir, patch)
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "f.go"))
	if !strings.Contains(string(got), "\toldCall()   \n\treturn\n\t// done\n") {
		t.Fatalf("patch did not apply with trailing-whitespace leniency:\n%s", string(got))
	}
}

func TestApplyPatch_FuzzyMatchPreservesOriginalContextLines(t *testing.T) {
	dir := t.TempDir()
	content := "func f() {\n\toldCall()   \n\treturn\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	patch := "*** Begin Patch\n*** Update File: f.go\n@@\n \toldCall()\n-\treturn\n+\treturn nil\n }\n*** End Patch\n"
	_, err := ApplyPatch(dir, patch)
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "f.go"))
	if !strings.Contains(string(got), "\toldCall()   \n\treturn nil\n") {
		t.Fatalf("fuzzy context line was not preserved from original file:\n%s", string(got))
	}
	if strings.Contains(string(got), "\toldCall()\n\treturn nil\n") {
		t.Fatalf("fuzzy context line was rewritten from patch text:\n%s", string(got))
	}
}

func TestApplyPatch_SequenceSearchMatchesInternalWhitespaceFuzzily(t *testing.T) {
	dir := t.TempDir()
	content := "func f() {\n\tconst value    = 1\n\treturn value\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	patch := "*** Begin Patch\n*** Update File: f.go\n@@\n \tconst value = 1\n-\treturn value\n+\treturn value + 1\n }\n*** End Patch\n"
	_, err := ApplyPatch(dir, patch)
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "f.go"))
	if !strings.Contains(string(got), "\tconst value    = 1\n\treturn value + 1\n") {
		t.Fatalf("patch did not apply while preserving internally fuzzy context:\n%s", string(got))
	}
}

func TestApplyPatch_MultiHunkSingleFile(t *testing.T) {
	dir := t.TempDir()
	content := "alpha\nbeta\ngamma\ndelta\nepsilon\n"
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

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
	if err := os.WriteFile(filepath.Join(dir, "test.go"), []byte("fmt.Println(\"hello world\")\n"), 0o644); err != nil {
		t.Fatal(err)
	}

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

func TestApplyPatch_ContextMismatchReportsContextAndCandidates(t *testing.T) {
	dir := t.TempDir()
	content := strings.Join([]string{
		"func first() {",
		"\tmodel := \"openai/gpt-5\"",
		"\treturn model",
		"}",
		"",
		"func second() {",
		"\tgot, err := resolve()",
		"\tif err != nil {",
		"\t\treturn err",
		"\t}",
		"\tmodel := \"openai/gpt-5\"",
		"\treturn model",
		"}",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	patch := "*** Begin Patch\n*** Update File: f.go\n@@\n \tgot, err := resolve()\n \tmodel := \"openai/gpt-5\"\n+\tfastCheapModel := \"openai/gpt-5-mini\"\n*** End Patch\n"
	_, err := ApplyPatch(dir, patch)
	if err == nil {
		t.Fatal("expected patch mismatch")
	}
	msg := err.Error()
	for _, want := range []string{
		"apply_patch: expected lines not found in f.go at line 7",
		"wanted: \"\\tgot, err := resolve()\"",
		"got:    \"\\tgot, err := resolve()\"",
		"File context around line 7:",
		">  7 | \tgot, err := resolve()",
		"Expected old/context lines from patch:",
		"  \tgot, err := resolve()",
		"Potential locations for old/context block:",
		"candidate at line 7:",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error missing %q:\n%s", want, msg)
		}
	}
}

func TestApplyPatch_DeleteMismatchReportsFullBlockCandidate(t *testing.T) {
	dir := t.TempDir()
	content := strings.Join([]string{
		"func before() {",
		"\tclose(done)",
		"}",
		"",
		"func target() {",
		"\tdefer sess.Close()",
		"\tclose(done)",
		"}",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "hook_test.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	patch := "*** Begin Patch\n*** Update File: hook_test.go\n@@\n func target() {\n-\tclose(done)\n+\tclose(complete)\n }\n*** End Patch\n"
	_, err := ApplyPatch(dir, patch)
	if err == nil {
		t.Fatal("expected patch mismatch")
	}
	msg := err.Error()
	for _, want := range []string{
		"apply_patch: expected lines not found in hook_test.go at line 5",
		"wanted: \"func target() {\"",
		"got:    \"func target() {\"",
		"File context around line 5:",
		"Expected old/context lines from patch:",
		"  func target() {",
		"  \tclose(done)",
		"  }",
		"Potential locations for old/context block:",
		"candidate at line 5:",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error missing %q:\n%s", want, msg)
		}
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

func TestApplyPatch_AbsolutePathUnderRootDir(t *testing.T) {
	dir := t.TempDir()
	// Write a file at dir/hello.txt
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello\n"), 0644)

	// Patch using an absolute path that is under rootDir — should succeed.
	patch := fmt.Sprintf(`*** Begin Patch
*** Update File: %s/hello.txt
@@
-hello
+world
*** End Patch
`, dir)
	touched, err := ApplyPatch(dir, patch)
	if err != nil {
		t.Fatalf("absolute path under rootDir should succeed: %v", err)
	}
	if !strings.Contains(touched, "hello.txt") {
		t.Fatalf("unexpected touched files: %v", touched)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "hello.txt"))
	if string(got) != "world\n" {
		t.Fatalf("file content: %q", string(got))
	}
}

func TestApplyPatch_AbsolutePathOutsideRootDir(t *testing.T) {
	dir := t.TempDir()
	// An absolute path that is NOT under rootDir — should still be rejected.
	patch := `*** Begin Patch
*** Add File: /etc/shadow
+nope
*** End Patch
`
	if _, err := ApplyPatch(dir, patch); err == nil {
		t.Fatal("absolute path outside rootDir should be rejected")
	}
}
