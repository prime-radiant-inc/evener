package main

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	return captureFD(t, &os.Stdout, fn)
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns what it wrote.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	return captureFD(t, &os.Stderr, fn)
}

func captureFD(t *testing.T, fd **os.File, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := *fd
	*fd = w
	defer func() { *fd = orig }()

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

func TestMatchSignature(t *testing.T) {
	cases := []struct {
		src, want string
	}{
		{"package p\nfunc (x T) UnmarshalJSON(b []byte) error { return nil }\n", "func (x T) UnmarshalJSON"},
		{"package p\nfunc ParseThing(s string) {}\n", "func Parse"},
		{"package p\nvar _ = json.Unmarshal(b, &v)\n", "json.Unmarshal"},
		{"package p\nfunc jsonDecode() {}\n", "func jsonDecode"},
		{"package p\nfunc plain() {}\n", ""},
	}
	for _, c := range cases {
		if got := matchSignature([]byte(c.src)); got != c.want {
			t.Errorf("matchSignature(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}

func TestFuzzedPackages(t *testing.T) {
	merged := map[string]block{
		"m/a/x.go:1": {file: "m/a/x.go", start: 1, stmts: 1, count: 1},
		"m/a/y.go:2": {file: "m/a/y.go", start: 2, stmts: 1, count: 0}, // uncovered
		"m/b/z.go:3": {file: "m/b/z.go", start: 3, stmts: 1, count: 1},
	}
	got := fuzzedPackages(merged)
	want := map[string]bool{"m/a": true, "m/b": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fuzzedPackages = %v, want %v", got, want)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate short = %q", got)
	}
	got := truncate("abcdefghij", 5)
	if len([]rune(got)) != 5 {
		t.Errorf("truncate width = %q (len %d)", got, len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncate should end with ellipsis, got %q", got)
	}
}

func TestScanUniverse(t *testing.T) {
	repo := t.TempDir()
	mod := filepath.Join(repo, "m")
	// A parse package, a plain package, a testdata dir, and a nested module.
	mustWrite(t, filepath.Join(mod, "go.mod"), "module example.com/m\n\ngo 1.25\n")
	mustWrite(t, filepath.Join(mod, "parsepkg", "p.go"), "package parsepkg\nfunc ParseX() {}\n")
	mustWrite(t, filepath.Join(mod, "plainpkg", "q.go"), "package plainpkg\nfunc Plain() {}\n")
	mustWrite(t, filepath.Join(mod, "parsepkg", "p_test.go"), "package parsepkg\nfunc ParseIgnoredInTest() {}\n")
	mustWrite(t, filepath.Join(mod, "testdata", "t.go"), "package td\nfunc ParseShouldBeSkipped() {}\n")
	mustWrite(t, filepath.Join(mod, "nested", "go.mod"), "module example.com/nested\n\ngo 1.25\n")
	mustWrite(t, filepath.Join(mod, "nested", "n.go"), "package nested\nfunc ParseNestedSkipped() {}\n")

	modulePaths, err := readModulePaths(repo, []string{"m"})
	if err != nil {
		t.Fatal(err)
	}
	universe, err := scanUniverse(repo, modulePaths)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := universe["example.com/m/parsepkg"]; !ok {
		t.Fatalf("expected parsepkg in universe, got %v", universe)
	}
	if _, ok := universe["example.com/m/plainpkg"]; ok {
		t.Errorf("plainpkg should not be a parse package")
	}
	for imp := range universe {
		if strings.Contains(imp, "testdata") || strings.Contains(imp, "nested") {
			t.Errorf("universe should skip testdata and nested modules, got %q", imp)
		}
	}
}

func TestReadManifest(t *testing.T) {
	p := filepath.Join(t.TempDir(), "manifest.tsv")
	mustWrite(t, p, "# comment\n\n"+
		"llm\t.\tFuzzParseSSE\t\tsse.go\t/tmp/a.cov\n"+
		".\t./appwire\tFuzzWireTypes\t\t\t/tmp/b.cov\n")
	got, err := readManifest(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []target{
		{module: "llm", pkg: ".", name: "FuzzParseSSE", coverpkg: "", focus: "sse.go", profile: "/tmp/a.cov"},
		{module: ".", pkg: "./appwire", name: "FuzzWireTypes", coverpkg: "", focus: "", profile: "/tmp/b.cov"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("readManifest =\n%+v\nwant\n%+v", got, want)
	}
}

func TestReadManifestRejectsShortLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "manifest.tsv")
	mustWrite(t, p, "llm\t.\tFuzzX\n")
	if _, err := readManifest(p); err == nil {
		t.Fatal("expected error for a line without 6 fields")
	}
}

func TestCheckExit(t *testing.T) {
	// Clean: no regression, no gaps.
	results := []result{{name: "FuzzA", focusPct: 90, floor: 88}}
	if code := checkExit(results, nil, 0.5); code != 0 {
		t.Fatalf("clean run should exit 0, got %d", code)
	}

	// Regression beyond the tolerance band.
	reg := []result{{name: "FuzzA", focusPct: 80, floor: 88}}
	out := captureStderr(t, func() {
		if code := checkExit(reg, nil, 0.5); code != 1 {
			t.Errorf("regression should exit 1, got %d", code)
		}
	})
	if !strings.Contains(out, "REGRESSION FuzzA") {
		t.Errorf("expected REGRESSION message, got %q", out)
	}

	// Within tolerance band: not a regression.
	within := []result{{name: "FuzzA", focusPct: 87.6, floor: 88}}
	if code := checkExit(within, nil, 0.5); code != 0 {
		t.Errorf("within-tolerance should exit 0, got %d", code)
	}

	// Gap breach.
	gaps := [][2]string{{"m/x", "func Parse"}}
	out = captureStderr(t, func() {
		if code := checkExit(nil, gaps, 0.5); code != 1 {
			t.Errorf("gap breach should exit 1, got %d", code)
		}
	})
	if !strings.Contains(out, "GAP BREACH") {
		t.Errorf("expected GAP BREACH message, got %q", out)
	}
}

func TestPrintReportMarks(t *testing.T) {
	results := []result{
		{name: "FuzzBelow", focusLabel: "a.go", focusPct: 70, floor: 88, pkgPct: 71},   // !
		{name: "FuzzAbove", focusLabel: "b.go", focusPct: 95, floor: 88, pkgPct: 96},   // ^
		{name: "FuzzAt", focusLabel: strings.Repeat("x", 60), focusPct: 88, floor: 88}, // = and truncation
	}
	out := captureStdout(t, func() {
		printReport(results, [][2]string{{"m/gap", "func Parse"}})
	})
	if !strings.Contains(out, "FUZZ SURFACE COVERAGE") {
		t.Errorf("missing header: %q", out)
	}
	if !strings.Contains(out, "GAP MAP") || !strings.Contains(out, "m/gap") {
		t.Errorf("missing gap section: %q", out)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("expected a truncated label with an ellipsis: %q", out)
	}

	// No-gap branch prints the "none" line.
	out = captureStdout(t, func() {
		printReport(results, nil)
	})
	if !strings.Contains(out, "ZERO fuzz coverage: none") {
		t.Errorf("expected no-gap line, got %q", out)
	}
}

func TestParseBlockErrors(t *testing.T) {
	bad := []string{
		"onlytwo fields",
		"file:1.1,2.2 notanint 1",
		"file:1.1,2.2 1 notanint",
		"noposition 1 1",
		"file:norange 1 1",
		"file:notaline.1,2.2 1 1",
	}
	for _, line := range bad {
		if _, err := parseBlock(line); err == nil {
			t.Errorf("parseBlock(%q) expected error", line)
		}
	}
}

func TestParseProfileErrors(t *testing.T) {
	dir := t.TempDir()

	if _, err := parseProfile(filepath.Join(dir, "missing.cov")); err == nil {
		t.Error("expected error opening a missing profile")
	}

	empty := filepath.Join(dir, "empty.cov")
	mustWrite(t, empty, "")
	if _, err := parseProfile(empty); err == nil {
		t.Error("expected error for empty profile")
	}

	badblock := filepath.Join(dir, "badblock.cov")
	mustWrite(t, badblock, "mode: set\ngarbage line here\n")
	if _, err := parseProfile(badblock); err == nil {
		t.Error("expected error for a malformed block line")
	}
}

func TestFuncLineRangeErrors(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "s.go")
	mustWrite(t, src, "package p\nfunc Present() {}\n")

	if _, _, err := funcLineRange(src, "Missing"); err == nil {
		t.Error("expected error for a missing function")
	}
	if _, _, err := funcLineRange(filepath.Join(dir, "nope.go"), "X"); err == nil {
		t.Error("expected parse error for a missing source file")
	}
	lo, hi, err := funcLineRange(src, "Present")
	if err != nil || lo != 2 || hi != 2 {
		t.Errorf("funcLineRange(Present) = %d,%d,%v want 2,2,nil", lo, hi, err)
	}
}

func TestReadModulePathsErrors(t *testing.T) {
	if _, err := readModulePaths(t.TempDir(), []string{"nomodule"}); err == nil {
		t.Error("expected error for a module dir without go.mod")
	}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "m", "go.mod"), "go 1.25\n") // no module line
	if _, err := readModulePaths(dir, []string{"m"}); err == nil {
		t.Error("expected error for a go.mod with no module path")
	}
}

func TestReadFloorsMissingAndMalformed(t *testing.T) {
	// Missing file is not an error; returns empty map.
	got, err := readFloors(filepath.Join(t.TempDir(), "none.txt"))
	if err != nil || len(got) != 0 {
		t.Fatalf("readFloors(missing) = %v, %v", got, err)
	}

	dir := t.TempDir()
	badN := filepath.Join(dir, "badn.txt")
	mustWrite(t, badN, "FuzzA notanumber\n")
	if _, err := readFloors(badN); err == nil {
		t.Error("expected error for a non-numeric floor")
	}
	badFields := filepath.Join(dir, "badf.txt")
	mustWrite(t, badFields, "FuzzA\n")
	if _, err := readFloors(badFields); err == nil {
		t.Error("expected error for a one-field floor line")
	}
}

func TestRunGapOnly(t *testing.T) {
	repo := t.TempDir()
	mod := filepath.Join(repo, "m")
	mustWrite(t, filepath.Join(mod, "go.mod"), "module example.com/m\n\ngo 1.25\n")
	// Two parse packages; one is registered, one is ignored -> no breach.
	mustWrite(t, filepath.Join(mod, "covered", "c.go"), "package covered\nfunc ParseC() {}\n")
	mustWrite(t, filepath.Join(mod, "skipped", "s.go"), "package skipped\nfunc ParseS() {}\n")

	registry := filepath.Join(repo, "registry.txt")
	mustWrite(t, registry, "native:m:./covered:FuzzCovered\n")
	ignore := filepath.Join(repo, "ignore.txt")
	mustWrite(t, ignore, "example.com/m/skipped  # dev-only tool\n")

	var code int
	out := captureStdout(t, func() {
		code = runGapOnly(registry, repo, []string{"m"}, ignore)
	})
	if code != 0 {
		t.Fatalf("expected exit 0 when all packages covered/ignored, got %d (%q)", code, out)
	}
	if !strings.Contains(out, "all") {
		t.Errorf("expected success summary, got %q", out)
	}

	// Now drop the ignore-list: the skipped package becomes a breach.
	emptyIgnore := filepath.Join(repo, "empty.txt")
	mustWrite(t, emptyIgnore, "# nothing ignored\n")
	errOut := captureStderr(t, func() {
		code = runGapOnly(registry, repo, []string{"m"}, emptyIgnore)
	})
	if code != 1 {
		t.Fatalf("expected exit 1 on an unignored gap, got %d", code)
	}
	if !strings.Contains(errOut, "GAP BREACH") || !strings.Contains(errOut, "example.com/m/skipped") {
		t.Errorf("expected gap breach naming skipped pkg, got %q", errOut)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
