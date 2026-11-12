package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	changes, err := applyPatchChanges(context.Background(), testMutator(dir), patch)
	if err != nil {
		t.Fatalf("applyPatchChanges: %v", err)
	}
	// Issue #6192: each operation reports what it did, and a move names its
	// source and destination instead of listing them as two plain paths.
	want := []patchChange{
		{kind: patchAdded, path: "b.txt"},
		{kind: patchUpdated, path: "a.txt"},
		{kind: patchMoved, path: "c.txt", from: "b.txt"},
		{kind: patchDeleted, path: "d.txt"},
	}
	if !slices.Equal(changes, want) {
		t.Fatalf("changes = %+v, want %+v", changes, want)
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

// TestApplyPatch_ResultHasOneLinePerOperation pins the result's shape: one
// line after the header for each operation, so a move occupies a single line
// that names both of its paths.
func TestApplyPatch_ResultHasOneLinePerOperation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gone.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Add File: new.txt\n+fresh\n" +
		"*** Update File: old.txt\n*** Move to: moved.txt\n@@\n keep\n" +
		"*** Delete File: gone.txt\n" +
		"*** End Patch\n"

	out, err := ApplyPatch(testMutator(dir), patch)
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 4 {
		t.Fatalf("result has %d lines, want a header and one line per operation: %q", len(lines), out)
	}
	for i, paths := range [][]string{{"new.txt"}, {"old.txt", "moved.txt"}, {"gone.txt"}} {
		for _, path := range paths {
			if !strings.Contains(lines[i+1], path) {
				t.Errorf("result line %d = %q, want it to name %s", i+1, lines[i+1], path)
			}
		}
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

	out, err := ApplyPatch(fm, "*** Begin Patch\n*** Delete File: keep.txt\n*** End Patch\n")
	if err == nil {
		t.Fatalf("ApplyPatch reported success after a failed remove: %q", out)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Errorf("a failed delete must leave the file in place: %v", statErr)
	}
}

func TestApplyPatch_EndOfFileMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	patch := "*** Begin Patch\n*** Update File: f.txt\n@@ line1\n line1\n-line2\n+replaced\n line3\n*** End of File\n*** End Patch\n"
	_, err := ApplyPatch(testMutator(dir), patch)
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
	_, err := ApplyPatch(testMutator(dir), patch)
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
	_, err := ApplyPatch(testMutator(dir), patch)
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
	_, err := ApplyPatch(testMutator(dir), patch)
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
	_, err := ApplyPatch(testMutator(dir), patch)
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
	_, err := ApplyPatch(testMutator(dir), patch)
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
	_, err := ApplyPatch(testMutator(dir), patch)
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
	_, err := ApplyPatch(testMutator(dir), patch)
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
	_, err := ApplyPatch(testMutator(dir), patch)
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

	result, err := ApplyPatch(testMutator(dir), patch)
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
	_, err := ApplyPatch(testMutator(dir), patch)
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
	_, err := ApplyPatch(testMutator(dir), patch)
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
	_, err := ApplyPatch(testMutator(dir), patch)
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
		if _, err := ApplyPatch(confinedPatchMutator(t, dir), p); err == nil {
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
	touched, err := ApplyPatch(testMutator(dir), patch)
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
	if _, err := ApplyPatch(confinedPatchMutator(t, dir), patch); err == nil {
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
		if _, err := ApplyPatch(fm, patch); err != nil {
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
	if _, err := ApplyPatch(fm, fmt.Sprintf("*** Begin Patch\n*** Delete File: %s\n*** End Patch\n", target)); err != nil {
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
	if _, err := ApplyPatch(testMutator(dir), patch); err != nil {
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
	if _, err := ApplyPatch(testMutator(dir), patch); err != nil {
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
	_, err := ApplyPatch(testMutator(dir), patch)
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
	if _, err := ApplyPatch(testMutator(dir), patch); err != nil {
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
	if _, err := ApplyPatch(testMutator(dir), patch); err != nil {
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
	_, err := ApplyPatch(testMutator(dir), patch)
	var mm *PatchMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("err = %v, want *PatchMismatchError", err)
	}
	if mm.HunkLine != 2 || mm.Want != "gamma" || !mm.GotEOF {
		t.Errorf("mismatch = {HunkLine:%d Want:%q GotEOF:%v}, want {2 gamma true}", mm.HunkLine, mm.Want, mm.GotEOF)
	}
}
