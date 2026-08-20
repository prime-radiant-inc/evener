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

// TestEveryRuleIsPhony is the mirror of TestEveryPhonyTargetHasARule. That
// test catches a .PHONY name whose recipe vanished; this one catches a rule
// that was never declared .PHONY, where a file of the same name at the repo
// root silently turns the target into a no-op. fuzz-drive was in exactly that
// state when this test was written.
func TestEveryRuleIsPhony(t *testing.T) {
	t.Parallel()
	phony, rules := makefilePhonyAndRuleNames(t)
	if len(phony) == 0 || len(rules) == 0 {
		t.Fatalf("parsed %d .PHONY names and %d rules; the parser is broken, not the Makefile", len(phony), len(rules))
	}
	isPhony := make(map[string]bool, len(phony))
	for _, name := range phony {
		isPhony[name] = true
	}
	var missing []string
	for _, name := range rules {
		if !isPhony[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("these rules are not .PHONY, so a file of the same name at the repo root "+
			"makes `make <target>` a silent no-op. Add them to a .PHONY line:\n  %s",
			strings.Join(missing, "\n  "))
	}
}

// makefileRulesWithoutRecipes returns rule names whose rule line has no
// prerequisites and is followed by no recipe line, along with the total
// number of rule declarations examined across the source files. GNU make
// treats such a target as satisfied: it prints "Nothing to be done for `x'"
// and exits 0. A gate in that state reports green while executing nothing,
// which is the lint-fuzz-registry failure mode that
// TestEveryPhonyTargetHasARule was written for — except that test cannot see
// it, because its parser records a rule from the `name:` line alone and
// never looks at the indented recipe beneath.
//
// The prerequisite check matters: `build: build-runtime` and
// `lint: $(LINT_TARGETS)` also have no recipe of their own, but they are
// alias targets whose prerequisites do the real work — make runs those
// before reporting "Nothing to be done", so nothing is skipped. Requiring an
// empty prerequisite list is also what excludes target-specific variable
// lines like `install-home: PREFIX := $(HOME)/.local`, since the assignment
// text lands in the same position a prerequisite list would.
//
// total counts every rule declaration seen, not just the empty ones, so a
// caller can tell "the source files hold no rules" (parser lost its subject)
// apart from "the source files hold rules and all of them have recipes"
// (the audit is clean). Without that distinction, an accidentally empty
// source-file list — a real risk once the source is a glob over `make/*.mk`
// instead of a single hardcoded `Makefile` — would make this audit pass
// vacuously instead of failing loudly.
func makefileRulesWithoutRecipes(t *testing.T) (empty []string, total int) {
	t.Helper()
	for _, path := range []string{"Makefile"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		lines := strings.Split(string(raw), "\n")
		for i, line := range lines {
			name, rest, ok := strings.Cut(line, ":")
			if !ok || strings.HasPrefix(rest, "=") || strings.ContainsAny(name, " \t") || name == "" {
				continue
			}
			if line == "" || line[0] == '\t' || line[0] == '#' || line[0] == ' ' {
				continue
			}
			if strings.HasPrefix(name, ".") {
				continue
			}
			total++
			if strings.TrimSpace(rest) != "" {
				continue
			}
			hasRecipe := false
			for _, next := range lines[i+1:] {
				if next == "" || strings.HasPrefix(next, "#") {
					continue
				}
				hasRecipe = strings.HasPrefix(next, "\t")
				break
			}
			if !hasRecipe {
				empty = append(empty, name)
			}
		}
	}
	sort.Strings(empty)
	return empty, total
}

// TestEveryRuleHasARecipe fails on a rule line with no recipe beneath it.
func TestEveryRuleHasARecipe(t *testing.T) {
	t.Parallel()
	empty, total := makefileRulesWithoutRecipes(t)
	if total == 0 {
		t.Fatalf("parsed 0 rule declarations; the parser is broken, not the Makefile")
	}
	if len(empty) > 0 {
		t.Fatalf("these rules have no recipe, so `make <target>` prints "+
			"\"Nothing to be done\" and exits 0 while checking nothing:\n  %s",
			strings.Join(empty, "\n  "))
	}
}
