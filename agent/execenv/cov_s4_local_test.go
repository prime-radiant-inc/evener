package execenv

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/serf/fuzz/fault"
)

// --- Glob (was 0%) -------------------------------------------------------

func TestGlob_DefaultBase_NewestFirstTiesByPath(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)

	// Three .go files: two share a mtime (tie -> lexical), one is newer.
	for _, name := range []string{"aaa.go", "bbb.go", "zzz.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	older := time.Now().Add(-2 * time.Hour)
	newer := time.Now().Add(-1 * time.Hour)
	for _, name := range []string{"aaa.go", "bbb.go"} {
		if err := os.Chtimes(filepath.Join(dir, name), older, older); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(filepath.Join(dir, "zzz.go"), newer, newer); err != nil {
		t.Fatal(err)
	}

	got, err := env.Glob("*.go", "")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	want := []string{
		filepath.Join(dir, "zzz.go"), // newest
		filepath.Join(dir, "aaa.go"), // tie, lexical first
		filepath.Join(dir, "bbb.go"),
	}
	if len(got) != len(want) {
		t.Fatalf("Glob returned %d results, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Glob order[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestGlob_RelativeAndAbsoluteBase(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "c.txt"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(dir)

	rel, err := env.Glob("*.txt", "sub")
	if err != nil {
		t.Fatalf("Glob relative base: %v", err)
	}
	if len(rel) != 1 || rel[0] != filepath.Join(sub, "c.txt") {
		t.Fatalf("Glob relative base = %v, want [%s]", rel, filepath.Join(sub, "c.txt"))
	}

	abs, err := env.Glob("*.txt", sub)
	if err != nil {
		t.Fatalf("Glob absolute base: %v", err)
	}
	if len(abs) != 1 || abs[0] != filepath.Join(sub, "c.txt") {
		t.Fatalf("Glob absolute base = %v, want [%s]", abs, filepath.Join(sub, "c.txt"))
	}
}

func TestGlob_DoublestarPattern(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "sub", "d")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "c.txt"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "e.txt"), []byte("e"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(dir)

	got, err := env.Glob("**/*.txt", "")
	if err != nil {
		t.Fatalf("Glob doublestar: %v", err)
	}
	set := map[string]bool{}
	for _, m := range got {
		set[m] = true
	}
	for _, want := range []string{
		filepath.Join(dir, "sub", "c.txt"),
		filepath.Join(nested, "e.txt"),
	} {
		if !set[want] {
			t.Fatalf("doublestar glob missing %q; got %v", want, got)
		}
	}
}

func TestGlob_InvalidPattern(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	if _, err := env.Glob("[", ""); err == nil {
		t.Fatal("expected error for malformed glob pattern")
	}
}

// --- Grep native fallback via the exec seam ------------------------------

// grepNativeEnv installs a fault-free RootDir with the ripgrep lookup forced to
// fail, so Grep(...) takes the native-Go fallback path.
func grepNativeEnv(t *testing.T, seed map[string]string) *LocalExecutionEnvironment {
	t.Helper()
	orig := execLookPath
	t.Cleanup(func() { execLookPath = orig })
	execLookPath = func(string) (string, error) { return "", errors.New("ripgrep absent") }

	dir := t.TempDir()
	for name, content := range seed {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return NewLocalExecutionEnvironment(dir)
}

func TestGrepFallback_ContentMode(t *testing.T) {
	env := grepNativeEnv(t, map[string]string{"a.txt": "alpha needle\nbeta\n"})
	out, err := env.Grep("needle", "", "", false, 100, "")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if !strings.Contains(out, "a.txt:1:alpha needle") {
		t.Fatalf("content mode = %q, want file:line:text", out)
	}
}

func TestGrepFallback_FilesWithMatchesMode(t *testing.T) {
	env := grepNativeEnv(t, map[string]string{
		"a.txt": "needle here\n",
		"b.txt": "no match\n",
	})
	out, err := env.Grep("needle", "", "", false, 100, "files_with_matches")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if !strings.Contains(out, "a.txt") || strings.Contains(out, "b.txt") {
		t.Fatalf("files_with_matches = %q, want only a.txt", out)
	}
	if strings.Contains(out, "needle here") {
		t.Fatalf("files_with_matches should not include line content: %q", out)
	}
}

func TestGrepFallback_CountMode_Sorted(t *testing.T) {
	env := grepNativeEnv(t, map[string]string{
		"a.txt": "hit\nhit\n",
		"b.txt": "hit\n",
	})
	out, err := env.Grep("hit", "", "", false, 100, "count")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if out != "a.txt:2\nb.txt:1" {
		t.Fatalf("count mode = %q, want sorted \"a.txt:2\\nb.txt:1\"", out)
	}
}

func TestGrepFallback_CaseInsensitive(t *testing.T) {
	env := grepNativeEnv(t, map[string]string{"a.txt": "HELLO World\n"})
	out, err := env.Grep("hello", "", "", true, 100, "")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if !strings.Contains(out, "HELLO World") {
		t.Fatalf("case-insensitive match failed: %q", out)
	}
}

func TestGrepFallback_GlobFilter(t *testing.T) {
	env := grepNativeEnv(t, map[string]string{
		"keep.go":  "needle\n",
		"skip.txt": "needle\n",
	})
	out, err := env.Grep("needle", "", "*.go", false, 100, "")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if !strings.Contains(out, "keep.go") || strings.Contains(out, "skip.txt") {
		t.Fatalf("glob filter = %q, want only keep.go", out)
	}
}

func TestGrepFallback_MaxResultsCap(t *testing.T) {
	var content strings.Builder
	for i := 0; i < 20; i++ {
		content.WriteString("needle\n")
	}
	env := grepNativeEnv(t, map[string]string{"many.txt": content.String()})
	out, err := env.Grep("needle", "", "", false, 3, "")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("maxResults cap = %d lines, want 3: %q", len(lines), out)
	}
}

func TestGrepFallback_InvalidRegex(t *testing.T) {
	env := grepNativeEnv(t, map[string]string{"a.txt": "x\n"})
	_, err := env.Grep("[invalid", "", "", false, 100, "")
	if err == nil || !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("expected 'invalid regex' error, got %v", err)
	}
}

func TestGrepFallback_SkipsBinaryAndHidden(t *testing.T) {
	env := grepNativeEnv(t, map[string]string{
		"text.txt":        "needle plain\n",
		"blob.bin":        "needle\x00binary\n",
		".hidden.txt":     "needle hidden file\n",
		".dir/nested.txt": "needle hidden dir\n",
	})
	out, err := env.Grep("needle", "", "", false, 100, "")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if !strings.Contains(out, "text.txt") {
		t.Fatalf("should match text.txt: %q", out)
	}
	for _, skip := range []string{"blob.bin", ".hidden.txt", "nested.txt"} {
		if strings.Contains(out, skip) {
			t.Fatalf("should skip %q, got %q", skip, out)
		}
	}
}

// --- filteredEnv / filteredEnvWithPolicy ---------------------------------

func TestFilteredEnv_DenyAndAllowExtras(t *testing.T) {
	t.Setenv("FOO_SECRET", "s")
	t.Setenv("NORMAL_VAR", "ok")
	env := filteredEnv(map[string]string{
		"EXTRA_TOKEN": "leak", // denied extra
		"EXTRA_OK":    "keep", // allowed extra
	})
	m := envToMap(env)
	if _, ok := m["FOO_SECRET"]; ok {
		t.Fatal("FOO_SECRET should be denied")
	}
	if _, ok := m["EXTRA_TOKEN"]; ok {
		t.Fatal("EXTRA_TOKEN should be denied")
	}
	if m["NORMAL_VAR"] != "ok" {
		t.Fatal("NORMAL_VAR should pass through")
	}
	if m["EXTRA_OK"] != "keep" {
		t.Fatal("EXTRA_OK extra should pass through")
	}
}

func TestFilteredEnvWithPolicy_All(t *testing.T) {
	t.Setenv("SOME_SECRET", "sekret")
	env := filteredEnvWithPolicy(EnvPolicyAll, map[string]string{"X": "1"})
	m := envToMap(env)
	if m["SOME_SECRET"] != "sekret" {
		t.Fatal("EnvPolicyAll should include sensitive parent vars")
	}
	if m["X"] != "1" {
		t.Fatal("EnvPolicyAll should include extra vars")
	}
}

func TestFilteredEnvWithPolicy_None(t *testing.T) {
	t.Setenv("SOME_MARKER", "present")
	env := filteredEnvWithPolicy(EnvPolicyNone, map[string]string{"X": "1"})
	m := envToMap(env)
	if _, ok := m["SOME_MARKER"]; ok {
		t.Fatal("EnvPolicyNone should not inherit parent vars")
	}
	if m["X"] != "1" {
		t.Fatal("EnvPolicyNone should include only extras")
	}
	if len(m) != 1 {
		t.Fatalf("EnvPolicyNone should carry only the one extra; got %d vars", len(m))
	}
}

func TestFilteredEnvWithPolicy_CoreOnly(t *testing.T) {
	t.Setenv("SOME_MARKER", "present")
	t.Setenv("PATH", "/usr/bin")
	env := filteredEnvWithPolicy(EnvPolicyCoreOnly, map[string]string{"X": "1"})
	m := envToMap(env)
	if _, ok := m["PATH"]; !ok {
		t.Fatal("CoreOnly should include PATH")
	}
	if _, ok := m["SOME_MARKER"]; ok {
		t.Fatal("CoreOnly should exclude arbitrary parent vars")
	}
	if m["X"] != "1" {
		t.Fatal("CoreOnly should include extras")
	}
}

func TestFilteredEnvWithPolicy_Default_FiltersSensitive(t *testing.T) {
	t.Setenv("DEF_SECRET", "s")
	t.Setenv("DEF_NORMAL", "ok")
	env := filteredEnvWithPolicy(EnvPolicyDefault, nil)
	m := envToMap(env)
	if _, ok := m["DEF_SECRET"]; ok {
		t.Fatal("Default policy should filter sensitive vars")
	}
	if m["DEF_NORMAL"] != "ok" {
		t.Fatal("Default policy should keep normal vars")
	}
}

func envToMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			m[k] = v
		}
	}
	return m
}

// --- injectLocalVenvPath -------------------------------------------------

func TestInjectLocalVenvPath_PrependsBin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bin dir name differs on windows")
	}
	root := t.TempDir()
	venvBin := filepath.Join(root, ".venv", "bin")
	if err := os.MkdirAll(venvBin, 0o755); err != nil {
		t.Fatal(err)
	}
	env := injectLocalVenvPath([]string{"PATH=/usr/bin"}, []string{root})
	m := envToMap(env)
	if !strings.HasPrefix(m["PATH"], venvBin+string(os.PathListSeparator)) {
		t.Fatalf("venv bin not prepended: PATH=%q", m["PATH"])
	}
}

func TestInjectLocalVenvPath_AppendsWhenNoExistingPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bin dir name differs on windows")
	}
	root := t.TempDir()
	venvBin := filepath.Join(root, ".venv", "bin")
	if err := os.MkdirAll(venvBin, 0o755); err != nil {
		t.Fatal(err)
	}
	// env with no PATH entry at all: the venv dir becomes the whole PATH.
	env := injectLocalVenvPath([]string{"HOME=/home/x"}, []string{root})
	m := envToMap(env)
	if m["PATH"] != venvBin {
		t.Fatalf("PATH = %q, want %q", m["PATH"], venvBin)
	}
}

func TestInjectLocalVenvPath_EmptyInputsUnchanged(t *testing.T) {
	if got := injectLocalVenvPath(nil, []string{"/x"}); got != nil {
		t.Fatalf("empty env should be unchanged, got %v", got)
	}
	in := []string{"PATH=/usr/bin"}
	if got := injectLocalVenvPath(in, nil); len(got) != 1 || got[0] != "PATH=/usr/bin" {
		t.Fatalf("empty roots should be unchanged, got %v", got)
	}
}

func TestInjectLocalVenvPath_NoVenvUnchanged(t *testing.T) {
	root := t.TempDir() // no .venv/venv dir
	in := []string{"PATH=/usr/bin"}
	got := injectLocalVenvPath(in, []string{root})
	if len(got) != 1 || got[0] != "PATH=/usr/bin" {
		t.Fatalf("root without venv should leave PATH unchanged, got %v", got)
	}
}

func TestInjectLocalVenvPath_DedupesWhenAlreadyPresent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bin dir name differs on windows")
	}
	root := t.TempDir()
	venvBin := filepath.Join(root, ".venv", "bin")
	if err := os.MkdirAll(venvBin, 0o755); err != nil {
		t.Fatal(err)
	}
	// PATH already contains the venv bin -> must not be prepended again.
	orig := venvBin + string(os.PathListSeparator) + "/usr/bin"
	got := injectLocalVenvPath([]string{"PATH=" + orig}, []string{root})
	m := envToMap(got)
	if m["PATH"] != orig {
		t.Fatalf("PATH with venv already present should be unchanged: got %q want %q", m["PATH"], orig)
	}
}

// --- fuzzy edit matching -------------------------------------------------

func TestEditFile_FuzzyMultiLineWindow(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	original := "line one\n    indented two\n\tindented three\nline four\n"
	if _, err := env.WriteFile("m.txt", original); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// old_string spans two lines but with normalized (single-space) whitespace.
	old := "indented two\nindented three"
	res, err := env.EditFile("m.txt", old, "REPLACED", false)
	if err != nil {
		t.Fatalf("EditFile multi-line fuzzy: %v", err)
	}
	if !strings.Contains(res, "whitespace normalization") {
		t.Fatalf("expected whitespace-normalization note, got %q", res)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "m.txt"))
	if !strings.Contains(string(b), "REPLACED") {
		t.Fatalf("multi-line fuzzy replacement not applied: %q", string(b))
	}
}

func TestEditFile_GenuinelyAbsent_NoNearest(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	if _, err := env.WriteFile("z.txt", "aaaa\nbbbb\ncccc\n"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := env.EditFile("z.txt", "qqqqqqqqzzzzzzzz", "X", false)
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if strings.Contains(err.Error(), "nearest text") {
		t.Fatalf("should not surface a nearest region for a wholly-dissimilar string: %v", err)
	}
	if !strings.Contains(err.Error(), "old_string not found") {
		t.Fatalf("expected 'old_string not found', got %v", err)
	}
}

func TestEditFile_NonUnique_WithoutReplaceAll(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	if _, err := env.WriteFile("dup.txt", "dup\ndup\n"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := env.EditFile("dup.txt", "dup", "X", false)
	if err == nil || !strings.Contains(err.Error(), "not unique") {
		t.Fatalf("expected 'not unique' error, got %v", err)
	}
}

func TestEditFile_ReplaceAll(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	if _, err := env.WriteFile("all.txt", "x\nx\nx\n"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	res, err := env.EditFile("all.txt", "x", "Y", true)
	if err != nil {
		t.Fatalf("EditFile replaceAll: %v", err)
	}
	if !strings.Contains(res, "3 replacements") {
		t.Fatalf("expected 3 replacements, got %q", res)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "all.txt"))
	if string(b) != "Y\nY\nY\n" {
		t.Fatalf("replaceAll result = %q", string(b))
	}
}

func TestLineSimilarity_Cases(t *testing.T) {
	// Contains-case -> 1.0
	if got := lineSimilarity("the quick brown fox", "quick brown"); got != 1 {
		t.Fatalf("contains case = %v, want 1", got)
	}
	// Empty -> 0
	if got := lineSimilarity("", "abc"); got != 0 {
		t.Fatalf("empty a = %v, want 0", got)
	}
	if got := lineSimilarity("abc", ""); got != 0 {
		t.Fatalf("empty b = %v, want 0", got)
	}
	// Jaccard: {a,b,c} vs {c,d} -> intersection {c}=1, union=4 -> 0.25
	if got := lineSimilarity("a b c", "c d"); got != 0.25 {
		t.Fatalf("jaccard case = %v, want 0.25", got)
	}
	// Fully disjoint -> 0
	if got := lineSimilarity("a b", "c d"); got != 0 {
		t.Fatalf("disjoint case = %v, want 0", got)
	}
}

// --- WriteFile / EditFile fault arms -------------------------------------

// faultOnly returns a schedule that faults exactly operation index k and lets
// every other op proceed. trip() faults when the plan byte % 4 == 0, so 0x01
// is a pass and 0x00 is a fault.
func faultOnly(k int) *fault.Schedule {
	plan := bytes.Repeat([]byte{0x01}, 64)
	plan[k%64] = 0x00
	return fault.FromBytes(plan)
}

// faultFsEnv builds an env over a MemMapFs whose RootDir already exists (created
// before wrapping so the setup ops are not counted by the schedule).
func faultFsEnv(t *testing.T, plan *fault.Schedule, seed map[string]string) *LocalExecutionEnvironment {
	t.Helper()
	base := afero.NewMemMapFs()
	root := "/root"
	if err := base.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range seed {
		if err := afero.WriteFile(base, filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := NewLocalExecutionEnvironment(root).SetFs(fault.FS(base, plan))
	return env
}

func TestWriteFile_FaultArms(t *testing.T) {
	// Over a window of single-op faults, WriteFile's MkdirAll and underlying
	// write must both surface an error. Faulting the first op hits MkdirAll;
	// later ops hit the OpenFile/Write of the content write.
	firstErr := false
	laterErr := false
	for k := 0; k < 6; k++ {
		env := faultFsEnv(t, faultOnly(k), nil)
		_, err := env.WriteFile("out.txt", "hello")
		if k == 0 && err != nil {
			firstErr = true
		}
		if k > 0 && err != nil {
			laterErr = true
		}
	}
	if !firstErr {
		t.Fatal("faulting the first op (MkdirAll) should make WriteFile error")
	}
	if !laterErr {
		t.Fatal("faulting a later op (content write) should make WriteFile error")
	}

	// Sanity: with no faults WriteFile succeeds.
	env := faultFsEnv(t, fault.FromBytes(bytes.Repeat([]byte{0x01}, 8)), nil)
	if _, err := env.WriteFile("out.txt", "hello"); err != nil {
		t.Fatalf("fault-free WriteFile should succeed: %v", err)
	}
}

func TestEditFile_ReadFaultAndWriteFault(t *testing.T) {
	// Faulting the first op fails the read; faulting a later op (after the read
	// completes) fails the final write. Both are distinct error branches.
	seed := map[string]string{"e.txt": "needle\n"}

	// Read fault: first op is the ReadFile Open.
	envRead := faultFsEnv(t, faultOnly(0), seed)
	if _, err := envRead.EditFile("e.txt", "needle", "X", false); err == nil {
		t.Fatal("faulting the read op should make EditFile error")
	}

	// Write fault: scan later ops for one where the read succeeds but the write
	// fails. A successful read leaves the file unmodified only if the write also
	// fails, so an error at a later index exercises the final-write branch.
	writeErr := false
	for k := 1; k < 8; k++ {
		env := faultFsEnv(t, faultOnly(k), seed)
		if _, err := env.EditFile("e.txt", "needle", "X", false); err != nil {
			writeErr = true
			break
		}
	}
	if !writeErr {
		t.Fatal("faulting a later op should make EditFile's final write error")
	}
}

func TestResolveWrite_EscapeAndEmpty(t *testing.T) {
	env := faultFsEnv(t, nil, nil)
	if _, err := env.WriteFile("../escape.txt", "x"); err == nil ||
		!strings.Contains(err.Error(), "outside working directory") {
		t.Fatalf("escape write: expected 'outside working directory', got %v", err)
	}
	if _, err := env.WriteFile("", "x"); err == nil || !strings.Contains(err.Error(), "empty path") {
		t.Fatalf("empty path: expected 'empty path' error, got %v", err)
	}
	// EditFile shares resolveWrite.
	if _, err := env.EditFile("../escape.txt", "a", "b", false); err == nil ||
		!strings.Contains(err.Error(), "outside working directory") {
		t.Fatalf("escape edit: expected 'outside working directory', got %v", err)
	}
}

// --- ListDirectory -------------------------------------------------------

func TestListDirectory_NonExistentPath(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	if _, err := env.ListDirectory("does/not/exist", 1); err == nil {
		t.Fatal("expected error listing a non-existent path")
	}
}

func TestListDirectory_DeepRecursionAndFlags(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink/exec-bit semantics differ on Windows")
	}
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	// Nested three levels deep.
	if _, err := env.WriteFile("l1/l2/l3/leaf.txt", "leaf"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("run.sh", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	ents, err := env.ListDirectory("", 4)
	if err != nil {
		t.Fatalf("ListDirectory: %v", err)
	}
	byName := map[string]DirEntry{}
	for _, e := range ents {
		byName[e.Name] = e
	}
	leaf, ok := byName[filepath.Join("l1", "l2", "l3", "leaf.txt")]
	if !ok {
		t.Fatalf("deep recursion missed leaf; entries: %v", ents)
	}
	if leaf.Size != int64(len("leaf")) {
		t.Fatalf("leaf size = %d, want %d", leaf.Size, len("leaf"))
	}
	if e := byName["run.sh"]; !e.IsExec {
		t.Fatalf("run.sh should be exec: %+v", e)
	}
	if e := byName["link"]; !e.IsSymlink {
		t.Fatalf("link should be symlink: %+v", e)
	}
}

// --- pid<=0 guards -------------------------------------------------------

func TestProcessGroupGuards_NonPositivePID(t *testing.T) {
	// pid <= 0 must be a no-op (never signals the whole session process group).
	terminateProcessGroup(0)
	terminateProcessGroup(-1)
	killProcessGroup(0)
	killProcessGroup(-42)
}

// --- gitpath: DirsFromRootToCwd -----------------------------------------

func TestDirsFromRootToCwd(t *testing.T) {
	sep := string(filepath.Separator)
	root := filepath.Join(sep+"a", "b")

	// cwd == root -> just root.
	if got := DirsFromRootToCwd(root, root); len(got) != 1 || got[0] != filepath.Clean(root) {
		t.Fatalf("cwd==root = %v, want [%s]", got, filepath.Clean(root))
	}

	// Nested cwd -> full chain root..cwd inclusive.
	cwd := filepath.Join(root, "c", "d")
	got := DirsFromRootToCwd(root, cwd)
	want := []string{
		filepath.Clean(root),
		filepath.Join(root, "c"),
		filepath.Join(root, "c", "d"),
	}
	if len(got) != len(want) {
		t.Fatalf("nested chain = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chain[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// cwd outside root -> just cwd.
	outside := filepath.Join(sep+"a", "x")
	if got := DirsFromRootToCwd(root, outside); len(got) != 1 || got[0] != filepath.Clean(outside) {
		t.Fatalf("outside = %v, want [%s]", got, filepath.Clean(outside))
	}
}

// --- cheap one-liners ----------------------------------------------------

func TestPlatform(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	got := env.Platform()
	// The mapping is total; on this Linux host it must be "linux".
	want := "linux"
	switch runtime.GOOS {
	case "darwin":
		want = "darwin"
	case "windows":
		want = "windows"
	}
	if got != want {
		t.Fatalf("Platform() = %q, want %q", got, want)
	}
}

func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	if err := os.WriteFile(filepath.Join(dir, "here.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !env.FileExists("here.txt") {
		t.Fatal("FileExists should report true for an existing file")
	}
	if env.FileExists("nope.txt") {
		t.Fatal("FileExists should report false for a missing file")
	}
}

func TestDetectImageFormat_MagicBytes(t *testing.T) {
	// No recognized extension: identification is by magic bytes only.
	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	if got := detectImageFormat("blob.dat", png); got != "png" {
		t.Fatalf("png magic = %q, want png", got)
	}
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0x00}
	if got := detectImageFormat("blob.dat", jpeg); got != "jpeg" {
		t.Fatalf("jpeg magic = %q, want jpeg", got)
	}
	if got := detectImageFormat("blob.dat", []byte("GIF89a---")); got != "gif" {
		t.Fatalf("gif89a magic = %q, want gif", got)
	}
	if got := detectImageFormat("blob.dat", []byte("GIF87a---")); got != "gif" {
		t.Fatalf("gif87a magic = %q, want gif", got)
	}
	if got := detectImageFormat("blob.dat", []byte("not an image at all")); got != "" {
		t.Fatalf("non-image = %q, want empty", got)
	}
}

func TestShellEscape(t *testing.T) {
	if got := shellEscape(""); got != "''" {
		t.Fatalf("empty = %q, want ''", got)
	}
	if got := shellEscape("plainword"); got != "plainword" {
		t.Fatalf("plain word = %q, want plainword", got)
	}
	if got := shellEscape("a b"); got != "'a b'" {
		t.Fatalf("spaced = %q, want 'a b'", got)
	}
	// A word containing a single quote uses the '"'"' splice.
	if got := shellEscape("it's"); got != `'it'"'"'s'` {
		t.Fatalf("single-quote = %q, want %q", got, `'it'"'"'s'`)
	}
}
