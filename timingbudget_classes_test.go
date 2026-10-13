package evener_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// classTestFunc matches a top-level Go test definition: "func Name(t *testing.T)".
var classTestFunc = regexp.MustCompile(`^func (\w+)\(t \*testing\.T\)`)

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
	var names []string
	for _, class := range budget.Classes {
		names = append(names, class.Tests...)
	}
	if len(names) == 0 {
		t.Fatal("testing-budget.json's perTestCeilingClasses names no members to check")
	}

	var alternates []string
	for _, name := range names {
		alternates = append(alternates, regexp.QuoteMeta(name))
	}
	pattern := `^func (` + strings.Join(alternates, "|") + `)\(t \*testing\.T\)`
	cmd := exec.Command("git", "grep", "-n", "-E", pattern, "--", "*_test.go")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
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
