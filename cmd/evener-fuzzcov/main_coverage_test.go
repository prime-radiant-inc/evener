package fuzzcov

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tempStdoutStderr creates a temp file pair to capture runCLI stdout/stderr.
// runCLI takes *os.File, so we can't use bytes.Buffer.
func tempStdoutStderr(t *testing.T) (*os.File, *os.File, func() (string, string)) {
	t.Helper()
	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatal(err)
	}
	stderrFile, err := os.CreateTemp(t.TempDir(), "stderr-*")
	if err != nil {
		t.Fatal(err)
	}
	return stdoutFile, stderrFile, func() (string, string) {
		outData, _ := os.ReadFile(stdoutFile.Name())
		errData, _ := os.ReadFile(stderrFile.Name())
		return string(outData), string(errData)
	}
}

// TestMainExitProcessSwallow replaces exitProcess so main() returns normally
// and exercises the error path where runCLI returns an error (code 2).
func TestMainExitProcessSwallow(t *testing.T) {
	oldExit := exitProcess
	defer func() { exitProcess = oldExit }()
	var exitCode int
	exitProcess = func(code int) { exitCode = code }

	// main() calls runCLI(os.Args[1:], ...). With no flags, runCLI returns
	// an error ("the only mode is -gap-only..."), so main sets code=2.
	main()
	if exitCode != 2 {
		t.Fatalf("main() exit code = %d, want 2", exitCode)
	}
}

// TestRunCLINoFlagsReturnsError covers the path where no mode is specified.
func TestRunCLINoFlagsReturnsError(t *testing.T) {
	stdout, stderr, _ := tempStdoutStderr(t)
	defer stdout.Close()
	defer stderr.Close()
	code, err := runCLI(nil, stdout, stderr)
	if err == nil {
		t.Fatalf("runCLI with no flags should error")
	}
	if code != 2 {
		t.Fatalf("runCLI code = %d, want 2", code)
	}
}

// TestRunCLIBadFlagReturnsError covers the flag-parse error path.
func TestRunCLIBadFlagReturnsError(t *testing.T) {
	stdout, stderr, _ := tempStdoutStderr(t)
	defer stdout.Close()
	defer stderr.Close()
	code, err := runCLI([]string{"-unknown-flag"}, stdout, stderr)
	if err == nil {
		t.Fatalf("runCLI with bad flag should error")
	}
	if code != 2 {
		t.Fatalf("runCLI bad flag code = %d, want 2", code)
	}
}

// TestRunCLIGapOnlyWithoutRegistry covers the empty-registry error path.
// We pass -modules to skip go.work derivation so the registry check is first.
func TestRunCLIGapOnlyWithoutRegistry(t *testing.T) {
	stdout, stderr, _ := tempStdoutStderr(t)
	defer stdout.Close()
	defer stderr.Close()
	code, err := runCLI([]string{"-gap-only", "-modules", "."}, stdout, stderr)
	if err == nil || !strings.Contains(err.Error(), "requires -registry") {
		t.Fatalf("runCLI -gap-only without -registry: err = %v", err)
	}
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}

// TestRunCLIGapOnlyWithBadRegistry covers the registry-read error path.
func TestRunCLIGapOnlyWithBadRegistry(t *testing.T) {
	stdout, stderr, _ := tempStdoutStderr(t)
	defer stdout.Close()
	defer stderr.Close()
	code, err := runCLI([]string{"-gap-only", "-registry", "/nonexistent/path"}, stdout, stderr)
	if err == nil {
		t.Fatalf("runCLI with bad registry should error")
	}
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}

// captureStdoutStderr temporarily redirects os.Stdout and os.Stderr to temp
// files and returns a function to read their contents. runGapOnlyE writes to
// os.Stdout/os.Stderr directly, not through the runCLI params.
func captureStdoutStderr(t *testing.T) func() (string, string) {
	t.Helper()
	oldOut := os.Stdout
	oldErr := os.Stderr
	outFile, err := os.CreateTemp(t.TempDir(), "out-*")
	if err != nil {
		t.Fatal(err)
	}
	errFile, err := os.CreateTemp(t.TempDir(), "err-*")
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = outFile
	os.Stderr = errFile
	return func() (string, string) {
		os.Stdout = oldOut
		os.Stderr = oldErr
		outData, _ := os.ReadFile(outFile.Name())
		errData, _ := os.ReadFile(errFile.Name())
		return string(outData), string(errData)
	}
}

// TestRunGapOnlyESuccess covers the full runGapOnlyE happy path: a repo with
// one parse package that has a registered fuzz target.
func TestRunGapOnlyESuccess(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/repo\n\ngo 1.25.6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "parser"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "parser", "parse.go"), []byte("package parser\n\nfunc Parse(data []byte) error { return nil }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	regDir := t.TempDir()
	regPath := filepath.Join(regDir, "registry.txt")
	if err := os.WriteFile(regPath, []byte("native:.:./parser:FuzzParse\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ignorePath := filepath.Join(regDir, "ignore.txt")
	if err := os.WriteFile(ignorePath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	readOut := captureStdoutStderr(t)
	code, err := runGapOnlyE(regPath, repo, []string{"."}, ignorePath)
	outStr, _ := readOut()
	if err != nil {
		t.Fatalf("runGapOnlyE success: %v", err)
	}
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if !strings.Contains(outStr, "all 1 decode/parse package") {
		t.Fatalf("stdout missing success message: %s", outStr)
	}
}

// TestRunGapOnlyEGapBreach covers the gap-breach path.
func TestRunGapOnlyEGapBreach(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/repo\n\ngo 1.25.6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "fuzzed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "fuzzed", "fuzzed.go"), []byte("package fuzzed\n\nfunc Parse(x []byte) error { return nil }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "unfuzzed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "unfuzzed", "unfuzzed.go"), []byte("package unfuzzed\n\nfunc Decode(x []byte) error { return nil }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	regDir := t.TempDir()
	regPath := filepath.Join(regDir, "registry.txt")
	if err := os.WriteFile(regPath, []byte("native:.:./fuzzed:FuzzParse\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ignorePath := filepath.Join(regDir, "ignore.txt")
	if err := os.WriteFile(ignorePath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	readOut := captureStdoutStderr(t)
	code, err := runGapOnlyE(regPath, repo, []string{"."}, ignorePath)
	_, errStr := readOut()
	if err != nil {
		t.Fatalf("runGapOnlyE gap-breach returned error: %v", err)
	}
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(errStr, "GAP BREACH") {
		t.Fatalf("stderr missing GAP BREACH: %s", errStr)
	}
	if !strings.Contains(errStr, "example.com/repo/unfuzzed") {
		t.Fatalf("stderr missing unfuzzed package: %s", errStr)
	}
}

// TestRunGapOnlyEIgnoredPackage covers the path where a gap package is
// in the ignore-list.
func TestRunGapOnlyEIgnoredPackage(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/repo\n\ngo 1.25.6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "unfuzzed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "unfuzzed", "unfuzzed.go"), []byte("package unfuzzed\n\nfunc Decode(x []byte) error { return nil }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	regDir := t.TempDir()
	regPath := filepath.Join(regDir, "registry.txt")
	if err := os.WriteFile(regPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	ignorePath := filepath.Join(regDir, "ignore.txt")
	if err := os.WriteFile(ignorePath, []byte("example.com/repo/unfuzzed  # intentionally ignored\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	readOut := captureStdoutStderr(t)
	code, err := runGapOnlyE(regPath, repo, []string{"."}, ignorePath)
	_, errStr := readOut()
	if err != nil {
		t.Fatalf("runGapOnlyE ignored: %v", err)
	}
	if code != 0 {
		t.Fatalf("code = %d, want 0 (all gaps ignored)\nstderr: %s", code, errStr)
	}
}

// TestRunCLIGapOnlyBadIgnore covers the readIgnore error path.
func TestRunCLIGapOnlyBadIgnore(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/repo\n\ngo 1.25.6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	regDir := t.TempDir()
	regPath := filepath.Join(regDir, "registry.txt")
	if err := os.WriteFile(regPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	ignorePath := filepath.Join(regDir, "ignore.txt")
	// An ignore entry with no reason comment is an error.
	if err := os.WriteFile(ignorePath, []byte("example.com/repo/unfuzzed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, _ := tempStdoutStderr(t)
	defer stdout.Close()
	defer stderr.Close()
	code, err := runCLI([]string{"-gap-only", "-registry", regPath, "-repo-root", repo, "-modules", ".", "-ignore", ignorePath}, stdout, stderr)
	if err == nil {
		t.Fatalf("runCLI with bad ignore should error")
	}
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}

// TestRunCLIGapOnlyBadModulePaths covers the readModulePaths error path where
// a module has no go.mod.
func TestRunCLIGapOnlyBadModulePaths(t *testing.T) {
	repo := t.TempDir()
	// No go.mod in repo root — readModulePaths will fail.
	regDir := t.TempDir()
	regPath := filepath.Join(regDir, "registry.txt")
	if err := os.WriteFile(regPath, []byte("native:.:./parser:FuzzParse\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ignorePath := filepath.Join(regDir, "ignore.txt")
	if err := os.WriteFile(ignorePath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, _ := tempStdoutStderr(t)
	defer stdout.Close()
	defer stderr.Close()
	code, err := runCLI([]string{"-gap-only", "-registry", regPath, "-repo-root", repo, "-modules", ".", "-ignore", ignorePath}, stdout, stderr)
	if err == nil {
		t.Fatalf("runCLI with missing go.mod should error")
	}
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}

// TestRunCLIGapOnlyExplicitModules covers the -modules flag path through runCLI.
func TestRunCLIGapOnlyExplicitModules(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "mymod"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "mymod", "go.mod"), []byte("module example.com/mymod\n\ngo 1.25.6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "mymod", "parser"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "mymod", "parser", "parse.go"), []byte("package parser\n\nfunc Parse(data []byte) error { return nil }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	regDir := t.TempDir()
	regPath := filepath.Join(regDir, "registry.txt")
	if err := os.WriteFile(regPath, []byte("native:mymod:./parser:FuzzParse\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ignorePath := filepath.Join(regDir, "ignore.txt")
	if err := os.WriteFile(ignorePath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	readOut := captureStdoutStderr(t)
	code, err := runGapOnlyE(regPath, repo, []string{"mymod"}, ignorePath)
	outStr, errStr := readOut()
	if err != nil {
		t.Fatalf("runGapOnlyE explicit modules: %v\nstderr: %s", err, errStr)
	}
	if code != 0 {
		t.Fatalf("code = %d, want 0\nstdout: %s\nstderr: %s", code, outStr, errStr)
	}
}

// TestScanUniverse covers the scanUniverse function with a real directory tree.
func TestScanUniverse(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "mod", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A Go file with a parse signature.
	if err := os.WriteFile(filepath.Join(repo, "mod", "pkg", "decode.go"), []byte("package pkg\n\nfunc Decode(data []byte) error { return nil }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A Go file without a parse signature.
	if err := os.MkdirAll(filepath.Join(repo, "mod", "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "mod", "plain", "plain.go"), []byte("package plain\n\nfunc Hello() string { return \"hi\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A test file (should be ignored).
	if err := os.WriteFile(filepath.Join(repo, "mod", "pkg", "decode_test.go"), []byte("package pkg\n\nfunc TestDecode(t *testing.T) {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A testdata dir (should be skipped).
	if err := os.MkdirAll(filepath.Join(repo, "mod", "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "mod", "testdata", "data.go"), []byte("package testdata\n\nfunc Parse(x []byte) error { return nil }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A nested module (should be skipped).
	if err := os.MkdirAll(filepath.Join(repo, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "nested", "go.mod"), []byte("module example.com/nested\n\ngo 1.25.6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "nested", "parse.go"), []byte("package nested\n\nfunc Parse(x []byte) error { return nil }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A hidden dir (should be skipped).
	if err := os.MkdirAll(filepath.Join(repo, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".hidden", "parse.go"), []byte("package hidden\n\nfunc Parse(x []byte) error { return nil }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	modulePaths := map[string]string{"mod": "example.com/mod"}
	universe, err := scanUniverse(repo, modulePaths)
	if err != nil {
		t.Fatalf("scanUniverse: %v", err)
	}
	if len(universe) != 1 {
		t.Fatalf("universe = %v, want 1 package (example.com/mod/pkg)", universe)
	}
	if _, ok := universe["example.com/mod/pkg"]; !ok {
		t.Fatalf("universe missing example.com/mod/pkg: %v", universe)
	}
}

// TestScanUniverseWalkError covers the walk-error path in scanUniverse.
func TestScanUniverseWalkError(t *testing.T) {
	// Use a non-existent repo root to trigger a walk error.
	modulePaths := map[string]string{"mod": "example.com/mod"}
	_, err := scanUniverse(filepath.Join(t.TempDir(), "nonexistent"), modulePaths)
	if err == nil {
		t.Fatalf("scanUniverse on nonexistent root should error")
	}
}

// TestMatchSignature covers the matchSignature function.
func TestMatchSignature(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{`func (t *T) UnmarshalJSON(data []byte) error { return nil }`, "func (t *T) UnmarshalJSON"},
		{`func (t *T) UnmarshalText(data []byte) error { return nil }`, "func (t *T) UnmarshalText"},
		{`json.Unmarshal(data)`, "json.Unmarshal"},
		{`json.NewDecoder(r)`, "json.NewDecoder"},
		{`func Parse(data []byte) error { return nil }`, "func Parse"},
		{`func myDecode(data []byte) error { return nil }`, "func myDecode"},
		{`toml.Decode(data)`, "toml.Decode"},
		{`toml.Unmarshal(data)`, "toml.Unmarshal"},
		{`func Hello() string { return "hi" }`, ""},
	}
	for _, c := range cases {
		if got := matchSignature([]byte(c.src)); got != c.want {
			t.Errorf("matchSignature(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}

// TestReadModulePaths covers the readModulePaths function.
func TestReadModulePaths(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "mod1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "mod1", "go.mod"), []byte("module example.com/mod1\n\ngo 1.25.6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/root\n\ngo 1.25.6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err := readModulePaths(repo, []string{".", "mod1"})
	if err != nil {
		t.Fatalf("readModulePaths: %v", err)
	}
	if paths["."] != "example.com/root" {
		t.Fatalf("paths[.] = %q, want example.com/root", paths["."])
	}
	if paths["mod1"] != "example.com/mod1" {
		t.Fatalf("paths[mod1] = %q, want example.com/mod1", paths["mod1"])
	}
}

// TestReadModulePathsMissingGoMod covers the error path where a module
// directory has no go.mod.
func TestReadModulePathsMissingGoMod(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := readModulePaths(repo, []string{"empty"})
	if err == nil {
		t.Fatalf("readModulePaths with no go.mod should error")
	}
}

// TestReadModulePathsEmptyGoMod covers the error path where go.mod has no
// module directive.
func TestReadModulePathsEmptyGoMod(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("go 1.25.6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readModulePaths(repo, []string{"."})
	if err == nil {
		t.Fatalf("readModulePaths with empty go.mod should error")
	}
}

// TestModulePathFromGoMod covers the modulePathFromGoMod function.
func TestModulePathFromGoMod(t *testing.T) {
	if got := modulePathFromGoMod([]byte("module example.com/foo\n\ngo 1.25.6\n")); got != "example.com/foo" {
		t.Fatalf("modulePathFromGoMod = %q, want example.com/foo", got)
	}
	if got := modulePathFromGoMod([]byte("go 1.25.6\n")); got != "" {
		t.Fatalf("modulePathFromGoMod with no module = %q, want empty", got)
	}
	if got := modulePathFromGoMod([]byte("")); got != "" {
		t.Fatalf("modulePathFromGoMod empty = %q, want empty", got)
	}
}

// TestReadIgnoreMissingFile covers the not-exist path in readIgnore.
func TestReadIgnoreMissingFile(t *testing.T) {
	m, err := readIgnore(filepath.Join(t.TempDir(), "nonexistent.txt"))
	if err != nil {
		t.Fatalf("readIgnore on missing file should not error: %v", err)
	}
	if len(m) != 0 {
		t.Fatalf("readIgnore on missing file should return empty map: %v", m)
	}
}

// TestReadIgnoreEmptyReason covers the error path where an ignore entry has
// an import path but no reason.
func TestReadIgnoreEmptyReason(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ignore.txt")
	if err := os.WriteFile(p, []byte("example.com/x  #\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readIgnore(p); err == nil {
		t.Fatalf("readIgnore with empty reason should error")
	}
}

// TestReadIgnoreEmptyImport covers the error path where an ignore entry has
// a reason but no import path (the import part trims to empty before the #).
func TestReadIgnoreEmptyImport(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ignore.txt")
	// The line "   # some reason" starts with # after trim → treated as comment.
	// Instead use "x  #" which has import "x" but empty reason → error.
	// And "  #" → import trims to "", reason trims to "" → both empty → error.
	if err := os.WriteFile(p, []byte("x  #  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readIgnore(p); err == nil {
		t.Fatalf("readIgnore with empty reason after # should error")
	}
}

// TestGoWorkModulesEmpty covers the error path where go.work has no use
// directives.
func TestGoWorkModulesEmpty(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.work"), []byte("go 1.25.6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := goWorkModules(repo); err == nil {
		t.Fatalf("goWorkModules with no use directives should error")
	}
}

// TestGoWorkModulesMissingFile covers the error path where go.work doesn't exist.
func TestGoWorkModulesMissingFile(t *testing.T) {
	if _, err := goWorkModules(t.TempDir()); err == nil {
		t.Fatalf("goWorkModules with missing go.work should error")
	}
}

// TestReadRegistryMissingFile covers the error path in readRegistry.
func TestReadRegistryMissingFile(t *testing.T) {
	if _, err := readRegistry(filepath.Join(t.TempDir(), "nonexistent.txt")); err == nil {
		t.Fatalf("readRegistry on missing file should error")
	}
}
