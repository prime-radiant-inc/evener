package tomlcheck

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- predicate tests -------------------------------------------------------

func TestIsSnakeCase(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"model", true},
		{"name", true},
		{"working_dir", true},
		{"reasoning_effort", true},
		{"max_subagent_depth", true},
		{"a_b_c", true},
		{"id", true},
		{"workingDir", false},
		{"working-dir", false},
		{"WorkingDir", false},
		{"WORKING", false},
		{"_working", false},
		{"working_", false},
		{"working__dir", false},
		{"working dir", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := isSnakeCase(c.in); got != c.ok {
				t.Fatalf("isSnakeCase(%q) = %v, want %v", c.in, got, c.ok)
			}
		})
	}
}

func TestToSnakeCase(t *testing.T) {
	if got := toSnakeCase("workingDir"); got != "working_dir" {
		t.Errorf("toSnakeCase(workingDir) = %q", got)
	}
	if got := toSnakeCase("working-dir"); got != "working_dir" {
		t.Errorf("toSnakeCase(working-dir) = %q", got)
	}
	if got := toSnakeCase("MCPConfigs"); got != "mcp_configs" {
		t.Errorf("toSnakeCase(MCPConfigs) = %q", got)
	}
	if got := toSnakeCase("reasoningEffort"); got != "reasoning_effort" {
		t.Errorf("toSnakeCase(reasoningEffort) = %q", got)
	}
	if got := toSnakeCase("WorkingDir"); got != "working_dir" {
		t.Errorf("toSnakeCase(WorkingDir) = %q", got)
	}
}

func TestViolationString(t *testing.T) {
	v := Violation{File: "a/b.toml", Line: 42, Message: "boom"}
	if got := v.String(); got != "a/b.toml:42: boom" {
		t.Fatalf("String()=%q", got)
	}
}

// --- TOML checker tests ----------------------------------------------------

func TestCheckTOMLFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.toml")
	body := `# top-level config
schema = 1
working_dir = "/tmp"
working-dir = "/tmp"
camelCase = true

[good_section]
inner_key = 1

[bad-section] # trailing comment
some-key = 2
bad_section.foo-key = 3
bad-section.good_key = 4

# evener:naming-ignore: kata.toml legacy
some-other-key = 3
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	violations, err := checkTOMLFile(path, "x.toml")
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(violations))
	for _, v := range violations {
		got = append(got, v.Message)
	}
	expectContains := []string{
		`toml key "working-dir"`,
		`toml key "camelCase"`,
		`toml table key "bad-section"`,
		`toml key "some-key"`,
		`toml key "foo-key"`,
		`toml key "bad-section"`,
	}
	for _, w := range expectContains {
		found := false
		for _, m := range got {
			if strings.Contains(m, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing %q; got: %v", w, got)
		}
	}
	mustNot := []string{
		`toml key "schema"`,
		`toml key "working_dir"`,
		`toml table key "good_section"`,
		`toml key "inner_key"`,
		`toml key "some-other-key"`, // covered by ignore marker
	}
	for _, w := range mustNot {
		for _, m := range got {
			if strings.Contains(m, w) {
				t.Errorf("unexpected violation %q", m)
			}
		}
	}
	// Verify suggestion text is snake_case.
	wantSuggest := []string{
		`suggest "working_dir"`,
		`suggest "camel_case"`,
		`suggest "bad_section"`,
	}
	for _, w := range wantSuggest {
		found := false
		for _, m := range got {
			if strings.Contains(m, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing snake_case suggestion %q; got: %v", w, got)
		}
	}
}

// A triple-quoted TOML string must not be linted line-by-line, and quoted
// table keys are accepted verbatim.
func TestCheckTOMLFileMultilineAndQuotedKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.toml")
	body := "prompt = \"\"\"\n" +
		"badKey = should-not-be-linted\n" +
		"\"\"\"\n" +
		"\n" +
		"[\"quoted.key.with.dots\"]\n" +
		"inner_key = 1\n" +
		"\n" +
		"real_key = 2\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	vs, err := checkTOMLFile(path, "x.toml")
	if err != nil {
		t.Fatalf("checkTOMLFile: %v", err)
	}
	for _, v := range vs {
		if strings.Contains(v.Message, "badKey") {
			t.Fatalf("content inside a triple-quoted string was linted: %+v", vs)
		}
		if strings.Contains(v.Message, "quoted.key") {
			t.Fatalf("quoted table key should be accepted verbatim: %+v", vs)
		}
	}
}

func TestCheckTOMLFileMissingFile(t *testing.T) {
	if _, err := checkTOMLFile(filepath.Join(t.TempDir(), "nope.toml"), "nope.toml"); err == nil {
		t.Fatal("expected error reading a missing toml file")
	}
}

// The ignore marker resets on a blank line, and a line that is neither a
// key nor a table header is left alone.
func TestCheckTOMLFileBlankResetAndNonKey(t *testing.T) {
	d := t.TempDir()
	toml := "# ignore resets on blank\n\nnot a key\n"
	p := filepath.Join(d, "x.toml")
	if err := os.WriteFile(p, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := checkTOMLFile(p, "x.toml"); err != nil {
		t.Fatal(err)
	}
}

// --- walker / runner tests -------------------------------------------------

func TestRun_SkipsExcludedPaths(t *testing.T) {
	root := t.TempDir()
	bad := "bad-key = 1\n"

	for _, dir := range []string{
		"inspo/cfg",
		"vendor/foo",
		"internal/x/testdata",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(dir), "x.toml"), []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A real file in scope.
	if err := os.MkdirAll(filepath.Join(root, "internal", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "x", "x.toml"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}

	vs, err := scanTOML(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 {
		t.Fatalf("want 1 violation, got %d: %v", len(vs), vs)
	}
	if !strings.HasPrefix(vs[0].File, "internal/x/x.toml") {
		t.Errorf("expected violation from internal/x/x.toml, got %s", vs[0].File)
	}
}

// Run in verbose mode over a tree containing a TOML violation exercises the
// verbose logging path.
func TestRunVerbose(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal", "x"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tomlSrc := "bad-key = 1\ngood_key = 2\n"
	if err := os.WriteFile(filepath.Join(root, "internal", "x", "x.toml"), []byte(tomlSrc), 0o644); err != nil {
		t.Fatalf("seed toml: %v", err)
	}

	vs, err := scanTOML(root, true)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	sawTOML := false
	for _, v := range vs {
		if strings.Contains(v.Message, "bad-key") {
			sawTOML = true
		}
	}
	if !sawTOML {
		t.Fatalf("expected a toml violation, got %+v", vs)
	}
}

func TestRunFilesystemFailuresAndSorting(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := scanTOML(missing, false); err == nil {
		t.Fatal("missing root")
	}
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "bad.toml"), []byte("badKey = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "worse.toml"), []byte("badKey = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "plain.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := scanTOML(d, true)
	if err != nil || len(got) != 2 || got[0].File > got[1].File {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestRunInjectedWalkerFailures(t *testing.T) {
	oldWalk, oldRel, oldTOML := filepathWalkDir, filepathRel, tomlFileChecker
	t.Cleanup(func() { filepathWalkDir, filepathRel, tomlFileChecker = oldWalk, oldRel, oldTOML })
	boom := errors.New("boom")
	filepathWalkDir = func(_ string, fn fs.WalkDirFunc) error { return fn("x", nil, boom) }
	if _, err := scanTOML(".", false); !errors.Is(err, boom) {
		t.Fatalf("walk callback err = %v", err)
	}
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := fs.FileInfoToDirEntry(info)
	filepathWalkDir = func(_ string, fn fs.WalkDirFunc) error { return fn("x", dir, nil) }
	filepathRel = func(string, string) (string, error) { return "", boom }
	if _, err := scanTOML(".", false); !errors.Is(err, boom) {
		t.Fatalf("rel err = %v", err)
	}
	filepathRel = oldRel
	filePath := filepath.Join(t.TempDir(), "x.toml")
	if err := os.WriteFile(filePath, []byte("k = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	file := fs.FileInfoToDirEntry(fileInfo)
	filepathWalkDir = func(_ string, fn fs.WalkDirFunc) error { return fn(filePath, file, nil) }
	tomlFileChecker = func(string, string) ([]Violation, error) { return nil, boom }
	if _, err := scanTOML(filepath.Dir(filePath), false); !errors.Is(err, boom) {
		t.Fatalf("toml err = %v", err)
	}
	filepathWalkDir = func(string, fs.WalkDirFunc) error { return boom }
	if _, err := scanTOML(".", false); !errors.Is(err, boom) {
		t.Fatalf("walk err = %v", err)
	}
}

func TestRunExcludedFileAndSameFileSort(t *testing.T) {
	oldWalk, oldRel, oldTOML := filepathWalkDir, filepathRel, tomlFileChecker
	t.Cleanup(func() { filepathWalkDir, filepathRel, tomlFileChecker = oldWalk, oldRel, oldTOML })
	p := filepath.Join(t.TempDir(), "x.toml")
	if err := os.WriteFile(p, []byte("k = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	entry := fs.FileInfoToDirEntry(info)
	filepathWalkDir = func(_ string, fn fs.WalkDirFunc) error { return fn(p, entry, nil) }
	filepathRel = func(string, string) (string, error) { return "vendor/x.toml", nil }
	called := false
	tomlFileChecker = func(string, string) ([]Violation, error) { called = true; return nil, nil }
	if _, err := scanTOML(".", false); err != nil || called {
		t.Fatalf("excluded called=%v err=%v", called, err)
	}
	filepathRel = func(string, string) (string, error) { return "x.toml", nil }
	tomlFileChecker = func(string, string) ([]Violation, error) {
		return []Violation{{File: "x.toml", Line: 9}, {File: "x.toml", Line: 1}}, nil
	}
	got, err := scanTOML(".", false)
	if err != nil || got[0].Line != 1 {
		t.Fatalf("sort=%v err=%v", got, err)
	}
}

// Hidden directories below the top level are skipped by the walker.
func TestIsExcludedHiddenSegment(t *testing.T) {
	if !isExcluded("internal/.hidden/x.toml") {
		t.Fatal("a nested hidden dir segment should be excluded")
	}
	// .github is the documented exception.
	if isExcluded(".github/workflows/ci.toml") {
		t.Fatal(".github must not be excluded")
	}
	if isExcluded("internal/x/x.toml") {
		t.Fatal("ordinary path should not be excluded")
	}
}

func TestRunTOMLResultsAndMain(t *testing.T) {
	oldAbs, oldRun := filepathAbs, tomlRun
	t.Cleanup(func() { filepathAbs, tomlRun = oldAbs, oldRun })
	var out, errOut bytes.Buffer
	if got := runTOML([]string{"--bad"}, &out, &errOut); got != 2 {
		t.Fatalf("flags = %d", got)
	}
	filepathAbs = func(string) (string, error) { return "", errors.New("abs") }
	if got := runTOML(nil, &out, &errOut); got != 2 {
		t.Fatalf("abs = %d", got)
	}
	filepathAbs = func(s string) (string, error) { return s, nil }
	tomlRun = func(string, bool) ([]Violation, error) { return nil, errors.New("walk") }
	if got := runTOML(nil, &out, &errOut); got != 2 {
		t.Fatalf("walk = %d", got)
	}
	tomlRun = func(string, bool) ([]Violation, error) { return []Violation{{File: "x", Line: 2, Message: "bad"}}, nil }
	if got := runTOML([]string{"-v"}, &out, &errOut); got != 1 || !strings.Contains(out.String(), "x:2: bad") {
		t.Fatalf("violation = %d %q", got, out.String())
	}
	tomlRun = func(string, bool) ([]Violation, error) { return nil, nil }
	out.Reset()
	errOut.Reset()
	exit := Run(nil, nil, &out, &errOut)
	if exit != 0 {
		t.Fatalf("exit = %d", exit)
	}
}
