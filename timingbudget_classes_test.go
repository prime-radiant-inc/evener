package evener_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// classTestFunc matches a top-level Go test definition: "func Name(t *testing.T)".
var classTestFunc = regexp.MustCompile(`^func (\w+)\(t \*testing\.T\)`)

// classMap reads testing-budget.json's perTestCeilingClasses as class name ->
// member test names.
func classMap(t *testing.T) map[string][]string {
	t.Helper()
	raw, err := os.ReadFile("testing-budget.json")
	if err != nil {
		t.Fatalf("read testing-budget.json: %v", err)
	}
	var budget struct {
		Classes map[string]struct {
			Tests []string `json:"tests"`
		} `json:"perTestCeilingClasses"` //nolint:tagliatelle // testing-budget.json's key, camelCase beside perTestCeilingSeconds
	}
	if err := json.Unmarshal(raw, &budget); err != nil {
		t.Fatalf("parse testing-budget.json: %v", err)
	}
	classes := make(map[string][]string, len(budget.Classes))
	for name, class := range budget.Classes {
		classes[name] = class.Tests
	}
	return classes
}

// classMapNames reads testing-budget.json's perTestCeilingClasses member names.
func classMapNames(t *testing.T) []string {
	t.Helper()
	var names []string
	for _, tests := range classMap(t) {
		names = append(names, tests...)
	}
	if len(names) == 0 {
		t.Fatal("testing-budget.json's perTestCeilingClasses names no members to check")
	}
	return names
}

// classDeclarations runs the audit's real git grep over the tracked *_test.go
// files and returns, per class-member name, the set of files defining it.
//
// The audit forces git's output shape on the command line: a developer's
// grep.column setting would turn "<file>:<line>:<content>" into
// "<file>:<line>:<column>:<content>", and color.grep=always wraps the line in
// ANSI escapes (color.ui=false does not override the more specific color.grep),
// so either way every declaration would look missing. --no-color and the -c
// overrides outrank a config file, and -n pins line numbers on regardless of
// grep.lineNumber. extraEnv is the test seam that proves that override.
func classDeclarations(t *testing.T, names, extraEnv []string) map[string]map[string]bool {
	t.Helper()
	var alternates []string
	for _, name := range names {
		alternates = append(alternates, regexp.QuoteMeta(name))
	}
	pattern := `^func (` + strings.Join(alternates, "|") + `)\(t \*testing\.T\)`
	cmd := exec.Command("git",
		"-c", "grep.column=false",
		"-c", "grep.break=false",
		"-c", "grep.heading=false",
		"grep", "--no-color", "-n", "-E", pattern, "--", "*_test.go")
	cmd.Env = append([]string{"LC_ALL=C"}, append(os.Environ(), extraEnv...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git grep for the class-map test names: %v", err)
	}

	defined := map[string]map[string]bool{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		// git grep -n prints "<file>:<line>:<content>"; the content can itself
		// hold colons, so split off only the first two fields and key the
		// definition by FILE, not by line number (two files can share a line).
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		file, decl := parts[0], parts[2]
		m := classTestFunc.FindStringSubmatch(decl)
		if m == nil {
			continue
		}
		if defined[m[1]] == nil {
			defined[m[1]] = map[string]bool{}
		}
		defined[m[1]][file] = true
	}
	return defined
}

// TestTimingBudgetClassMapNamesMatchTheTree pins testing-budget.json's
// perTestCeilingClasses against the tree it classifies (#4601). The map is
// hand-edited and membership is by bare test name, so two silent failure modes
// are possible and both are caught here:
//
//   - a name that is a typo or a stale rename matches no test, so that test
//     quietly falls back to the warn-only flat ceiling and its enforcement is
//     lost with no signal;
//   - a name defined in more than one package would classify tests in a package
//     the class never meant, enforcing the wrong number against them.
//
// git grep is a real external tool, not a faked one (the copy-only audits read
// git the same way): it pins each name to the single *_test.go that defines it.
func TestTimingBudgetClassMapNamesMatchTheTree(t *testing.T) {
	names := classMapNames(t)
	defined := classDeclarations(t, names, nil)
	for _, name := range names {
		files := defined[name]
		switch {
		case len(files) == 0:
			t.Errorf("testing-budget.json's perTestCeilingClasses lists %s, which no *_test.go defines; "+
				"a renamed or typo'd name silently drops that test's enforcement to the warn-only flat ceiling", name)
		case len(files) > 1:
			var paths []string
			for path := range files {
				paths = append(paths, path)
			}
			t.Errorf("testing-budget.json's perTestCeilingClasses lists %s, defined in %d files (%s); "+
				"membership is by bare name, so this classifies every package's test of that name", name, len(files), strings.Join(paths, ", "))
		}
	}
}

// TestTimingBudgetClassMapAuditIgnoresGrepFormattingConfig pins RoboRev's
// findings on PR #4610: the audit must not read a developer's grep output
// formatting. With grep.column=true a plain `git grep -n` prints
// "<file>:<line>:<column>:<content>"; with color.grep=always it wraps the line
// in ANSI escapes (color.ui=false does not override the more specific
// color.grep). Either makes the file:line:content parser reject the line and
// report every class member as missing, reddening a valid checkout. The audit
// disables both on the command line, which outranks a config file, so it finds
// the same declarations with the settings on as off.
func TestTimingBudgetClassMapAuditIgnoresGrepFormattingConfig(t *testing.T) {
	names := classMapNames(t)
	want := classDeclarations(t, names, nil)
	if len(want) != len(names) {
		t.Fatalf("fixture check: the audit found %d of %d class names with no formatting config", len(want), len(names))
	}
	first := names[0]
	if len(want[first]) != 1 {
		t.Fatalf("fixture check: %s should be defined in exactly one file, got %v", first, want[first])
	}

	cases := map[string]string{
		"column":         "[grep]\n\tcolumn = true\n",
		"color.grep":     "[color]\n\tgrep = always\n",
		"column + color": "[grep]\n\tcolumn = true\n[color]\n\tgrep = always\n",
	}
	for name, cfgBody := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := filepath.Join(t.TempDir(), "gitconfig")
			writeAuditScriptFixture(t, cfg, cfgBody)
			got := classDeclarations(t, names, []string{"GIT_CONFIG_GLOBAL=" + cfg, "GIT_CONFIG_NOSYSTEM=1"})
			if len(got) != len(want) {
				t.Fatalf("with %s set the audit found %d of %d names; it is reading git's formatted output", name, len(got), len(want))
			}
			if len(got[first]) != 1 {
				t.Fatalf("with %s set, %s resolved to %v, want exactly one file", name, first, got[first])
			}
		})
	}
}

// TestTimingBudgetRunnerBoundClassesAreUnclassified pins the 2026-10-14 ruling
// (issue #4615): four classes whose wall time tracks how busy the runner is, not
// the code, were removed from perTestCeilingClasses so their members fall back to
// the flat warn-only perTestCeilingSeconds. The kept classes are asserted too, so
// this fails on an emptied or renamed map and not only on a re-added class.
func TestTimingBudgetRunnerBoundClassesAreUnclassified(t *testing.T) {
	classes := classMap(t)
	for _, tc := range []struct {
		name   string
		wantIn bool
	}{
		{name: "dev-tool compile/CLI"},
		{name: "install"},
		{name: "transcript large-fixture"},
		{name: "gate-runner integration"},
		{name: "delegate-attention & idle", wantIn: true},
		{name: "deploy-host integration", wantIn: true},
		{name: "session-activity", wantIn: true},
		{name: "retirement", wantIn: true},
		{name: "eval harness", wantIn: true},
		{name: "plugin-git integration", wantIn: true},
		{name: "live-stack E2E", wantIn: true},
	} {
		_, ok := classes[tc.name]
		switch {
		case ok && !tc.wantIn:
			t.Errorf("testing-budget.json's perTestCeilingClasses still lists the runner-bound class %q; "+
				"the 2026-10-14 ruling (#4615) removed it so its members stay on the flat warn-only ceiling", tc.name)
		case !ok && tc.wantIn:
			t.Errorf("testing-budget.json's perTestCeilingClasses no longer lists the kept class %q", tc.name)
		}
	}
}
