package tool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/sandbox"
)

// testMutator exercises an unrestricted off environment against real files.
func testMutator(dir string) execenv.FileMutator {
	return execenv.NewLocalExecutionEnvironment(dir)
}

// confinedPatchMutator contains untrusted patch paths inside the test fixture.
func confinedPatchMutator(t *testing.T, dir string) execenv.FileMutator {
	t.Helper()
	env := execenv.NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)
	env.Sandbox = &sandbox.ResolvedPolicy{
		Mode:     sandbox.ModeRestricted,
		FileTool: sandbox.AccessScope{Read: sandbox.ReadWorktreeOnly, ReadRoots: []string{dir}, WriteRoots: []string{dir}},
	}
	return env
}

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
	out, err := ApplyPatch(t.Context(), testMutator(dir), patch)
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	wantLines := []string{
		"A b.txt",
		"M a.txt",
		"R b.txt -> c.txt",
		"D d.txt",
	}
	for _, want := range wantLines {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
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

// TestApplyPatch_DeleteFailureNotReportedAsApplied pins issue #2376: when the
// filesystem removal genuinely fails (injected read-only boundary → EPERM),
// apply_patch must return an error, not report the deleted path as applied, and
// must leave the target on disk.
func TestApplyPatch_DeleteFailureNotReportedAsApplied(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(target, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fm := execenv.NewLocalExecutionEnvironment(dir).SetFs(afero.NewReadOnlyFs(afero.NewOsFs()))

	out, err := ApplyPatch(t.Context(), fm, "*** Begin Patch\n*** Delete File: keep.txt\n*** End Patch\n")
	if err == nil {
		t.Fatalf("ApplyPatch reported success after a failed remove: %q", out)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Errorf("a failed delete must leave the file in place: %v", statErr)
	}
}

func TestApplyPatch_SummaryPrefixesMoveToSamePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "same.txt"), []byte("line1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := `*** Begin Patch
*** Update File: same.txt
*** Move to: same.txt
@@
 line1
+line2
*** End Patch
`
	out, err := ApplyPatch(t.Context(), testMutator(dir), patch)
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	if !strings.Contains(out, "M same.txt") {
		t.Fatalf("expected 'M same.txt' when Move to names the same path, got:\n%s", out)
	}
	if strings.Contains(out, "->") {
		t.Fatalf("unexpected rename arrow when Move to names same path:\n%s", out)
	}
}

func TestApplyPatch_EndOfFileMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	patch := "*** Begin Patch\n*** Update File: f.txt\n@@ line1\n line1\n-line2\n+replaced\n line3\n*** End of File\n*** End Patch\n"
	_, err := ApplyPatch(t.Context(), testMutator(dir), patch)
	if err != nil {
		t.Fatalf("ApplyPatch with End of File marker: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if !strings.Contains(string(got), "replaced") {
		t.Fatal("patch not applied")
	}
	if strings.Contains(string(got), "line2") {
		t.Fatal("replaced line 'line2' should have been removed by patch")
	}
}

func TestApplyPatch_FuzzyWhitespaceMatching(t *testing.T) {
	dir := t.TempDir()
	// File has trailing spaces and tabs mixed in
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("  hello world  \n  goodbye\t\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	patch := "*** Begin Patch\n*** Update File: f.txt\n@@ hello\n hello world\n-goodbye\n+farewell\n*** End Patch\n"
	_, err := ApplyPatch(t.Context(), testMutator(dir), patch)
	if err != nil {
		t.Fatalf("ApplyPatch with whitespace mismatch: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if !strings.Contains(string(got), "farewell") {
		t.Fatal("fuzzy match patch not applied")
	}
	if strings.Contains(string(got), "goodbye") {
		t.Fatal("replaced line 'goodbye' should have been removed by patch")
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
	_, err := ApplyPatch(t.Context(), testMutator(dir), patch)
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
	_, err := ApplyPatch(t.Context(), testMutator(dir), patch)
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
	_, err := ApplyPatch(t.Context(), testMutator(dir), patch)
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
	_, err := ApplyPatch(t.Context(), testMutator(dir), patch)
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
	_, err := ApplyPatch(t.Context(), testMutator(dir), patch)
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
	_, err := ApplyPatch(t.Context(), testMutator(dir), patch)
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
	_, err := ApplyPatch(t.Context(), testMutator(dir), patch)
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

	result, err := ApplyPatch(t.Context(), testMutator(dir), patch)
	if err != nil {
		t.Fatalf("expected patch to apply with Unicode fuzzy matching: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "test.go"))
	if !strings.Contains(string(got), "goodbye world") {
		t.Errorf("expected result to contain 'goodbye world', got:\n%s\nresult: %s", string(got), result)
	}
	if strings.Contains(string(got), "hello world") {
		t.Errorf("original line 'hello world' should have been removed by patch, got:\n%s", string(got))
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
	_, err := ApplyPatch(t.Context(), testMutator(dir), patch)
	if err == nil {
		t.Fatal("expected patch mismatch")
	}
	assertPatchMismatch(t, err, 1, "\tmodel := \"openai/gpt-5\"", "\tif err != nil {", 8)
}

func TestApplyPatch_ContextMismatchEndsWithRecoveryPointer(t *testing.T) {
	dir := t.TempDir()
	content := strings.Join([]string{
		"func first() {",
		"\tmodel := \"openai/gpt-5\"",
		"\treturn model",
		"}",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	patch := "*** Begin Patch\n*** Update File: f.go\n@@\n \tmodel := \"openai/gpt-5\"\n-\treturn nonexistent\n+\treturn model\n*** End Patch\n"
	_, err := ApplyPatch(t.Context(), testMutator(dir), patch)
	if err == nil {
		t.Fatal("expected patch mismatch")
	}
	msg := err.Error()
	const wantSuffix = "the file has changed since the content this patch was built from; re-read the target region with read_file, then rebuild the patch"
	if !strings.HasSuffix(msg, wantSuffix) {
		t.Fatalf("error does not end with recovery pointer:\n%s", msg)
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
	_, err := ApplyPatch(t.Context(), testMutator(dir), patch)
	if err == nil {
		t.Fatal("expected patch mismatch")
	}
	assertPatchMismatch(t, err, 1, "\tclose(done)", "\tdefer sess.Close()", 6)
}

func TestApplyPatch_ConfinedRejectsPathTraversalAndAbsolutePaths(t *testing.T) {
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
		if _, err := ApplyPatch(t.Context(), confinedPatchMutator(t, dir), p); err == nil {
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
	touched, err := ApplyPatch(t.Context(), testMutator(dir), patch)
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

func TestApplyPatch_ConfinedAbsolutePathOutsideRootDir(t *testing.T) {
	dir := t.TempDir()
	// An absolute path that is NOT under rootDir — should still be rejected.
	patch := `*** Begin Patch
*** Add File: /etc/shadow
+nope
*** End Patch
`
	if _, err := ApplyPatch(t.Context(), confinedPatchMutator(t, dir), patch); err == nil {
		t.Fatal("absolute path outside rootDir should be rejected")
	}
}

func TestApplyPatch_SandboxOffOutsideWorkingDirectory(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "source.txt")
	target := filepath.Join(base, "target.txt")
	fm := testMutator(root)
	patches := []string{
		"*** Begin Patch\n*** Add File: ../source.txt\n+original\n*** End Patch\n",
		fmt.Sprintf("*** Begin Patch\n*** Update File: %s\n*** Move to: %s\n@@\n-original\n+updated\n*** End Patch\n", source, target),
	}
	for _, patch := range patches {
		if _, err := ApplyPatch(t.Context(), fm, patch); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "updated\n" {
		t.Fatalf("patched file = %q, %v", got, err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("moved source = %v", err)
	}
	if _, err := ApplyPatch(t.Context(), fm, fmt.Sprintf("*** Begin Patch\n*** Delete File: %s\n*** End Patch\n", target)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("deleted target = %v", err)
	}
}

func TestApplyPatchKeepsBareBlankContextLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("first\n\nsecond\nthird\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The blank context line is written with no leading space.
	patch := "*** Begin Patch\n*** Update File: a.go\n@@\n first\n\n-second\n+SECOND\n third\n*** End Patch\n"
	if _, err := ApplyPatch(t.Context(), testMutator(dir), patch); err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "first\n\nSECOND\nthird\n"; string(got) != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestApplyPatchTrailingEmptyLineIsNotContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("one\ntwo\n\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Blank lines after the last hunk line are separators, not context: if
	// they counted, the hunk would demand a blank line after "two" that the
	// patch never described and the replacement would grow an extra one.
	patch := "*** Begin Patch\n*** Update File: a.go\n@@\n one\n-two\n+TWO\n\n\n*** End Patch\n"
	if _, err := ApplyPatch(t.Context(), testMutator(dir), patch); err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "one\nTWO\n\nthree\n"; string(got) != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestApplyPatchMismatchReportsFirstDifferingLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("alpha\nbeta\ngamma\ndelta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: a.go\n@@\n alpha\n beta\n-GAMMA\n+gamma2\n delta\n*** End Patch\n"
	_, err := ApplyPatch(t.Context(), testMutator(dir), patch)
	var mm *PatchMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("err = %v, want *PatchMismatchError", err)
	}
	if mm.HunkLine != 2 {
		t.Errorf("HunkLine = %d, want 2", mm.HunkLine)
	}
	if mm.Want != "GAMMA" || mm.Got != "gamma" {
		t.Errorf("Want/Got = %q/%q, want GAMMA/gamma", mm.Want, mm.Got)
	}
	if mm.Line != 3 {
		t.Errorf("Line = %d, want 3", mm.Line)
	}
}

func assertPatchMismatch(t *testing.T, err error, hunkLine int, want, got string, line int) {
	t.Helper()
	var mm *PatchMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("err = %v, want *PatchMismatchError", err)
	}
	if mm.HunkLine != hunkLine || mm.Want != want || mm.Got != got || mm.Line != line {
		t.Fatalf("mismatch = {HunkLine:%d Want:%q Got:%q Line:%d}, want {%d %q %q %d}",
			mm.HunkLine, mm.Want, mm.Got, mm.Line, hunkLine, want, got, line)
	}
}

func TestApplyPatchBlankLineAfterHunkMarkerIsContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("x\n\nfunc f() {\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: a.go\n@@\n\n func f() {\n+\tbody()\n }\n*** End Patch\n"
	if _, err := ApplyPatch(t.Context(), testMutator(dir), patch); err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	got, _ := os.ReadFile(path)
	if want := "x\n\nfunc f() {\n\tbody()\n}\n"; string(got) != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

// A hunk whose only old line is a bare blank anchors on the first blank line
// at or after the current position, exactly as a " " context line does.
func TestApplyPatchBlankOnlyHunkAnchorsOnBlankLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("a\n\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: a.go\n@@\n\n+new\n*** End Patch\n"
	if _, err := ApplyPatch(t.Context(), testMutator(dir), patch); err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	got, _ := os.ReadFile(path)
	if want := "a\n\nnew\nb\n"; string(got) != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestApplyPatchMismatchAtEndOfFileReportsEOF(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: a.go\n@@\n alpha\n beta\n-gamma\n+delta\n*** End Patch\n"
	_, err := ApplyPatch(t.Context(), testMutator(dir), patch)
	var mm *PatchMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("err = %v, want *PatchMismatchError", err)
	}
	if mm.HunkLine != 2 || mm.Want != "gamma" || !mm.GotEOF {
		t.Errorf("mismatch = {HunkLine:%d Want:%q GotEOF:%v}, want {2 gamma true}", mm.HunkLine, mm.Want, mm.GotEOF)
	}
}

func TestApplyPatch_BlankLineTolerantUniqueFallback(t *testing.T) {
	t.Parallel()

	t.Run("missing blank line preserves file blank line", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "rank.js")
		orig := "function a() {\n  return 1\n}\n\nfunction boundaryRank(x) {\n  return x\n}\n"
		if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n*** Update File: rank.js\n@@\n }\n-function boundaryRank(x) {\n+function boundaryRankNew(x) {\n*** End Patch\n"
		if _, err := ApplyPatch(t.Context(), testMutator(dir), patch); err != nil {
			t.Fatalf("ApplyPatch failed: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := "function a() {\n  return 1\n}\n\nfunction boundaryRankNew(x) {\n  return x\n}\n"
		if string(got) != want {
			t.Fatalf("got:\n%q\nwant:\n%q", string(got), want)
		}
	})

	t.Run("extra blank line in context does not invent stray blank line", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "rank.js")
		orig := "function a() {\n  return 1\n}\nfunction boundaryRank(x) {\n  return x\n}\n"
		if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n*** Update File: rank.js\n@@\n }\n \n-function boundaryRank(x) {\n+function boundaryRankNew(x) {\n*** End Patch\n"
		if _, err := ApplyPatch(t.Context(), testMutator(dir), patch); err != nil {
			t.Fatalf("ApplyPatch failed: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := "function a() {\n  return 1\n}\nfunction boundaryRankNew(x) {\n  return x\n}\n"
		if string(got) != want {
			t.Fatalf("got:\n%q\nwant:\n%q", string(got), want)
		}
	})

	t.Run("insertion after context with missing blank line preserves all lines", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "ins.txt")
		orig := "a\n\nb\n"
		if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n*** Update File: ins.txt\n@@\n a\n+mid\n b\n*** End Patch\n"
		if _, err := ApplyPatch(t.Context(), testMutator(dir), patch); err != nil {
			t.Fatalf("ApplyPatch failed: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "a\n\nmid\nb\n" && string(got) != "a\nmid\n\nb\n" {
			t.Fatalf("got:\n%q", string(got))
		}
	})

	t.Run("missing blank line with tabs vs spaces preserves file indentation and blank line", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "tabs.go")
		orig := "func a() {\n\t}\n\n\tfmt.Println(1)\n}\n"
		if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n*** Update File: tabs.go\n@@\n   }\n-  fmt.Println(1)\n+\tfmt.Println(2)\n*** End Patch\n"
		if _, err := ApplyPatch(t.Context(), testMutator(dir), patch); err != nil {
			t.Fatalf("ApplyPatch failed: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := "func a() {\n\t}\n\n\tfmt.Println(2)\n}\n"
		if string(got) != want {
			t.Fatalf("got:\n%q\nwant:\n%q", string(got), want)
		}
	})

	t.Run("multi-line deletion spanning internal blank line drops internal blank line", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "del.txt")
		orig := "top\nold1\n\nold2\nbot\n"
		if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n*** Update File: del.txt\n@@\n top\n-old1\n-old2\n bot\n*** End Patch\n"
		if _, err := ApplyPatch(t.Context(), testMutator(dir), patch); err != nil {
			t.Fatalf("ApplyPatch failed: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := "top\nbot\n"
		if string(got) != want {
			t.Fatalf("got:\n%q\nwant:\n%q", string(got), want)
		}
	})

	t.Run("replacement pairs separated by file blank line preserve blank line", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "rep.txt")
		orig := "old1\n\nold2\n"
		if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n*** Update File: rep.txt\n@@\n-old1\n+new1\n-old2\n+new2\n*** End Patch\n"
		if _, err := ApplyPatch(t.Context(), testMutator(dir), patch); err != nil {
			t.Fatalf("ApplyPatch failed: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := "new1\n\nnew2\n"
		if string(got) != want {
			t.Fatalf("got:\n%q\nwant:\n%q", string(got), want)
		}
	})

	t.Run("replacement before context preserves trailing blank line after replacement", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "after.txt")
		orig := "old1\n\nkeep\n"
		if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n*** Update File: after.txt\n@@\n-old1\n+new1\n keep\n*** End Patch\n"
		if _, err := ApplyPatch(t.Context(), testMutator(dir), patch); err != nil {
			t.Fatalf("ApplyPatch failed: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := "new1\n\nkeep\n"
		if string(got) != want {
			t.Fatalf("got:\n%q\nwant:\n%q", string(got), want)
		}
	})

	t.Run("ambiguous duplicate fails with PatchMismatchError and leaves file untouched", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "dup.js")
		orig := "}\n\nfunction f() {\n  return 1\n}\n\nfunction f() {\n  return 2\n}\n"
		if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n*** Update File: dup.js\n@@\n }\n-function f() {\n+function g() {\n*** End Patch\n"
		_, err := ApplyPatch(t.Context(), testMutator(dir), patch)
		if _, ok := errors.AsType[*PatchMismatchError](err); !ok {
			t.Fatalf("err = %v, want *PatchMismatchError", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != orig {
			t.Fatalf("file mutated on ambiguous fallback:\n%q", string(got))
		}
	})

	t.Run("hint disambiguates unique tail match and refuses ambiguous tail without falling back", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "hint.js")
		orig := "// first\n}\n\nfunction f() {\n  return 1\n}\n\n// second\n}\n\nfunction f() {\n  return 2\n}\n"
		if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n*** Update File: hint.js\n@@ // second\n }\n-function f() {\n+function g() {\n*** End Patch\n"
		if _, err := ApplyPatch(t.Context(), testMutator(dir), patch); err != nil {
			t.Fatalf("hint-disambiguated ApplyPatch failed: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := "// first\n}\n\nfunction f() {\n  return 1\n}\n\n// second\n}\n\nfunction g() {\n  return 2\n}\n"
		if string(got) != want {
			t.Fatalf("got:\n%q\nwant:\n%q", string(got), want)
		}

		ambigPath := filepath.Join(dir, "hint_ambig.js")
		ambigOrig := "// start\n}\n\nfunction f() {\n  return 1\n}\n\nfunction f() {\n  return 2\n}\n"
		if err := os.WriteFile(ambigPath, []byte(ambigOrig), 0o644); err != nil {
			t.Fatal(err)
		}
		ambigPatch := "*** Begin Patch\n*** Update File: hint_ambig.js\n@@ // start\n }\n-function f() {\n+function g() {\n*** End Patch\n"
		_, err = ApplyPatch(t.Context(), testMutator(dir), ambigPatch)
		if _, ok := errors.AsType[*PatchMismatchError](err); !ok {
			t.Fatalf("err = %v, want *PatchMismatchError", err)
		}
		got, err = os.ReadFile(ambigPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != ambigOrig {
			t.Fatalf("file mutated on ambiguous hinted fallback:\n%q", string(got))
		}
	})

	t.Run("explicit deleted blank line disables fallback", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "delblank.txt")
		orig := "alpha\nbeta\n"
		if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n*** Update File: delblank.txt\n@@\n alpha\n-\n beta\n*** End Patch\n"
		_, err := ApplyPatch(t.Context(), testMutator(dir), patch)
		if _, ok := errors.AsType[*PatchMismatchError](err); !ok {
			t.Fatalf("err = %v, want *PatchMismatchError", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != orig {
			t.Fatalf("file mutated:\n%q", string(got))
		}
	})

	t.Run("single non-blank line or boundary blank context disables fallback", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "single.txt")
		orig := "top\nalpha\n\nbeta\n"
		if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		cases := []string{
			// Leading blank context line + single non-blank line.
			"*** Begin Patch\n*** Update File: single.txt\n@@\n \n-top\n+TOP\n*** End Patch\n",
			// Leading blank context line with 2 non-blank lines.
			"*** Begin Patch\n*** Update File: single.txt\n@@\n \n alpha\n-beta\n+BETA\n*** End Patch\n",
			// Trailing blank context line with 2 non-blank lines.
			"*** Begin Patch\n*** Update File: single.txt\n@@\n alpha\n-beta\n+BETA\n \n*** End Patch\n",
		}
		for _, patch := range cases {
			_, err := ApplyPatch(t.Context(), testMutator(dir), patch)
			if _, ok := errors.AsType[*PatchMismatchError](err); !ok {
				t.Fatalf("patch %q: err = %v, want *PatchMismatchError", patch, err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != orig {
				t.Fatalf("file mutated for patch %q:\n%q", patch, string(got))
			}
		}
	})

	t.Run("ambiguous at tightest fuzzy tier refuses without loosening", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "tiers.txt")
		orig := "  alpha\n\n  beta\n\n\talpha\n\n\tbeta\n"
		if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n*** Update File: tiers.txt\n@@\n alpha\n-beta\n+BETA\n*** End Patch\n"
		_, err := ApplyPatch(t.Context(), testMutator(dir), patch)
		if _, ok := errors.AsType[*PatchMismatchError](err); !ok {
			t.Fatalf("err = %v, want *PatchMismatchError", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != orig {
			t.Fatalf("file mutated:\n%q", string(got))
		}
	})

	t.Run("hint past unique match falls back to pos and multi-hunk continues after matched end", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "multi.js")
		orig := "}\n\nfunction first() {\n  return 1\n}\n// later anchor\nfunction second() {\n  return 2\n}\n"
		if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n*** Update File: multi.js\n@@ // later anchor\n }\n-function first() {\n+function firstRenamed() {\n@@\n function second() {\n-  return 2\n+  return 3\n*** End Patch\n"
		if _, err := ApplyPatch(t.Context(), testMutator(dir), patch); err != nil {
			t.Fatalf("ApplyPatch failed: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := "}\n\nfunction firstRenamed() {\n  return 1\n}\n// later anchor\nfunction second() {\n  return 3\n}\n"
		if string(got) != want {
			t.Fatalf("got:\n%q\nwant:\n%q", string(got), want)
		}
	})

	t.Run("explicit blank context between deletions preserves file blank line", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "para.txt")
		orig := "p1a\np1b\n\np2a\np2b\n\nend\n"
		if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		// Omitted blank line before "end" activates blank-line fallback; the
		// explicit blank context between -p1b and -p2a must keep the file gap.
		patch := "*** Begin Patch\n*** Update File: para.txt\n@@\n p1a\n-p1b\n \n-p2a\n p2b\n end\n*** End Patch\n"
		if _, err := ApplyPatch(t.Context(), testMutator(dir), patch); err != nil {
			t.Fatalf("ApplyPatch failed: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := "p1a\n\np2b\n\nend\n"
		if string(got) != want {
			t.Fatalf("got:\n%q\nwant:\n%q", string(got), want)
		}

		// Multiple blank context lines in the patch must not duplicate a single file blank line.
		dupPath := filepath.Join(dir, "para_dup.txt")
		if err := os.WriteFile(dupPath, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		dupPatch := "*** Begin Patch\n*** Update File: para_dup.txt\n@@\n p1a\n-p1b\n \n \n-p2a\n p2b\n end\n*** End Patch\n"
		if _, err := ApplyPatch(t.Context(), testMutator(dir), dupPatch); err != nil {
			t.Fatalf("ApplyPatch failed: %v", err)
		}
		got, err = os.ReadFile(dupPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("got:\n%q\nwant:\n%q", string(got), want)
		}
	})

	t.Run("extra blank context between deletions does not invent blank line", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "nobrk.txt")
		orig := "p1a\np1b\np2a\np2b\n\nend\n"
		if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n*** Update File: nobrk.txt\n@@\n p1a\n-p1b\n \n-p2a\n p2b\n end\n*** End Patch\n"
		if _, err := ApplyPatch(t.Context(), testMutator(dir), patch); err != nil {
			t.Fatalf("ApplyPatch failed: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := "p1a\np2b\n\nend\n"
		if string(got) != want {
			t.Fatalf("got:\n%q\nwant:\n%q", string(got), want)
		}
	})

	t.Run("additions around explicit blank context between deletions keep patch order", func(t *testing.T) {
		t.Parallel()
		orig := "top\ndrop1\n\ndrop2\n\nbot\n"
		cases := []struct {
			name  string
			patch string
			want  string
		}{
			{
				name:  "blank context before addition",
				patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n top\n-drop1\n \n+added\n-drop2\n bot\n*** End Patch\n",
				want:  "top\n\nadded\n\nbot\n",
			},
			{
				name:  "addition before blank context",
				patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n top\n-drop1\n+added\n \n-drop2\n bot\n*** End Patch\n",
				want:  "top\nadded\n\n\nbot\n",
			},
			{
				name:  "additions on both sides of blank context",
				patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n top\n-drop1\n+added1\n \n+added2\n-drop2\n bot\n*** End Patch\n",
				want:  "top\nadded1\n\nadded2\n\nbot\n",
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				path := filepath.Join(dir, "f.txt")
				if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
					t.Fatal(err)
				}
				if _, err := ApplyPatch(t.Context(), testMutator(dir), tc.patch); err != nil {
					t.Fatalf("ApplyPatch failed: %v", err)
				}
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != tc.want {
					t.Fatalf("got:\n%q\nwant:\n%q", string(got), tc.want)
				}
			})
		}
	})
}
