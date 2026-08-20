package evener_test

import (
	"os"
	"sort"
	"strings"
	"testing"
)

// makefilePhonyAndRuleNames returns the names declared across every .PHONY
// line and the targets that actually carry a rule. A rule line starts at
// column zero with `name:`; `:=` assignments (LINT_TARGETS := …) and recipe
// lines (which begin with a tab) are not rules.
func makefilePhonyAndRuleNames(t *testing.T) (phony, rules []string) {
	t.Helper()
	raw, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatalf("reading Makefile: %v", err)
	}
	seen := map[string]bool{}
	for line := range strings.Lines(string(raw)) {
		line = strings.TrimRight(line, "\n")
		if after, ok := strings.CutPrefix(line, ".PHONY:"); ok {
			phony = append(phony, strings.Fields(after)...)
			continue
		}
		if line == "" || line[0] == '\t' || line[0] == '#' || line[0] == ' ' {
			continue
		}
		name, rest, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(rest, "=") || strings.ContainsAny(name, " \t") || name == "" {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		rules = append(rules, name)
	}
	return phony, rules
}

// TestEveryPhonyTargetHasARule is the tripwire for a gate that is listed,
// reports green, and executes nothing.
//
// A .PHONY name with no rule is not an error to make: it prints "Nothing to be
// done" and exits 0. So a target can be named in .PHONY and in LINT_TARGETS,
// be run by `make lint` on every commit and in CI, and check nothing at all,
// with no output anywhere saying so. That is what happened to
// lint-fuzz-registry: a rebase conflict resolution kept both mentions of the
// name and dropped the recipe between them, and `make lint` went on reporting
// PASS across eight modules while running zero fuzz-registry checking. The
// only way to see it was to introduce real registry drift and notice the gate
// did not care.
//
// Nothing else in the suite can catch this. The defect is the ABSENCE of a
// line, so no exact-text pin covers it, and the gate that would have caught
// the drift is the gate that stopped running.
func TestEveryPhonyTargetHasARule(t *testing.T) {
	t.Parallel()
	phony, rules := makefilePhonyAndRuleNames(t)
	if len(phony) == 0 || len(rules) == 0 {
		t.Fatalf("parsed %d .PHONY names and %d rules; the parser is broken, not the Makefile", len(phony), len(rules))
	}
	hasRule := make(map[string]bool, len(rules))
	for _, name := range rules {
		hasRule[name] = true
	}
	var missing []string
	for _, name := range phony {
		if !hasRule[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("these .PHONY targets have no rule, so `make <target>` silently does nothing and "+
			"exits 0 — a gate named here and in LINT_TARGETS reports green while executing "+
			"nothing, which is how lint-fuzz-registry's recipe went missing. Restore the "+
			"recipe, or drop the name:\n  %s", strings.Join(missing, "\n  "))
	}
}

// TestEveryLintTargetIsPhonyAndHasARule narrows the rule above onto the lint
// family, because LINT_TARGETS is the list `make lint` expands and the one a
// reader treats as the inventory of what the gate proves. docs/testing.md's
// gate matrix describes this list item by item.
func TestEveryLintTargetIsPhonyAndHasARule(t *testing.T) {
	t.Parallel()
	phony, rules := makefilePhonyAndRuleNames(t)
	raw, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatalf("reading Makefile: %v", err)
	}
	var targets []string
	for line := range strings.Lines(string(raw)) {
		if after, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "LINT_TARGETS :="); ok {
			targets = strings.Fields(after)
			break
		}
	}
	if len(targets) == 0 {
		t.Fatal("no LINT_TARGETS assignment found; `make lint` has no family list to expand")
	}
	isPhony := make(map[string]bool, len(phony))
	for _, name := range phony {
		isPhony[name] = true
	}
	hasRule := make(map[string]bool, len(rules))
	for _, name := range rules {
		hasRule[name] = true
	}
	for _, target := range targets {
		if !hasRule[target] {
			t.Errorf("LINT_TARGETS names %q, which has no rule: `make lint` runs it and it does nothing", target)
		}
		if !isPhony[target] {
			t.Errorf("LINT_TARGETS names %q, which is not .PHONY: a file of that name would skip the check", target)
		}
	}
}
