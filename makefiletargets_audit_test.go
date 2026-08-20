package evener_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// makefileSourcePaths returns every file that contributes rules to the build:
// the root Makefile plus the family files it includes. Tests that audit the
// Makefile must read all of them — reading only the root would silently stop
// auditing most of the repo's targets the moment they moved into make/.
//
// t is testing.TB rather than *testing.T because FuzzInstallScript's
// installedBins helper (install_fuzz_test.go) reads the Makefile from a
// *testing.F, which is not a *testing.T but does satisfy testing.TB.
func makefileSourcePaths(t testing.TB) []string {
	t.Helper()
	paths := []string{"Makefile"}
	family, err := filepath.Glob("make/*.mk")
	if err != nil {
		t.Fatalf("globbing make/*.mk: %v", err)
	}
	// filepath.Glob returns (nil, nil) when the pattern matches nothing, so a
	// renamed or missing make/ directory would silently narrow every audit
	// built on this helper down to the rule-free root Makefile and leave them
	// all passing vacuously. Refuse that instead.
	if len(family) == 0 {
		t.Fatal("make/*.mk matched no files; every Makefile audit would pass vacuously against the rule-free root Makefile. Did make/ move?")
	}
	sort.Strings(family)
	return append(paths, family...)
}

// copyMakefileSources copies the root Makefile and every make/*.mk family
// file from repoRoot into fixtureRoot, so a fixture that runs `make
// <target>` reaches the split rules the anchored include pulls in. Copying
// only "Makefile" reaches zero rules post-split — TestRootMakefileHasNoRules
// keeps the root file rule-free — and `make <target>` there does nothing.
func copyMakefileSources(t *testing.T, repoRoot, fixtureRoot string) {
	t.Helper()
	for _, rel := range makefileSourcePaths(t) {
		copyRepositoryFile(t, repoRoot, fixtureRoot, rel, 0o644)
	}
}

// TestMakefileSourcePathsIncludesRootAndFamilies pins the shape every other
// Makefile-reading test relies on: the root file first, then the family
// files in sorted order, so a caller can always assume index 0 is "Makefile".
func TestMakefileSourcePathsIncludesRootAndFamilies(t *testing.T) {
	t.Parallel()
	paths := makefileSourcePaths(t)
	if len(paths) == 0 || paths[0] != "Makefile" {
		t.Fatalf("expected Makefile first, got %v", paths)
	}
	for _, p := range paths[1:] {
		if !strings.HasPrefix(p, "make/") || !strings.HasSuffix(p, ".mk") {
			t.Errorf("unexpected source path %q", p)
		}
	}
}

// makefilePhonyAndRuleNames returns the names declared across every .PHONY
// line and the targets that actually carry a rule, accumulated across every
// file in makefileSourcePaths. A rule line starts at column zero with
// `name:`; `:=` assignments (LINT_TARGETS := …) and recipe lines (which
// begin with a tab) are not rules.
func makefilePhonyAndRuleNames(t *testing.T) (phony, rules []string) {
	t.Helper()
	seen := map[string]bool{}
	for _, path := range makefileSourcePaths(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
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
// reader treats as the inventory of what the gate proves.
// docs/developing-evener/linting.md names that list in its opening paragraph
// and documents each member in its generated target table.
//
// It reads the FIRST LINT_TARGETS definition across the source files, which
// is only the list `make lint` actually expands because
// TestExactlyOneLintTargetsDefinition holds there to be exactly one.
func TestEveryLintTargetIsPhonyAndHasARule(t *testing.T) {
	t.Parallel()
	phony, rules := makefilePhonyAndRuleNames(t)
	var targets []string
search:
	for _, path := range makefileSourcePaths(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for line := range strings.Lines(string(raw)) {
			if after, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "LINT_TARGETS :="); ok {
				targets = strings.Fields(after)
				break search
			}
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
	for _, path := range makefileSourcePaths(t) {
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

// TestRootMakefileHasNoRules keeps every rule in a family file. The generator
// reads make/*.mk only, so a rule left in the root would be annotated by the
// annotation audit and then have nowhere to be published — documented in the
// Makefile and invisible in the docs.
func TestRootMakefileHasNoRules(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatalf("reading Makefile: %v", err)
	}
	var found []string
	for line := range strings.Lines(string(raw)) {
		line = strings.TrimRight(line, "\n")
		if line == "" || line[0] == '\t' || line[0] == '#' || line[0] == ' ' {
			continue
		}
		name, rest, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(rest, "=") || strings.ContainsAny(name, " \t") || name == "" {
			continue
		}
		if strings.HasPrefix(name, ".") {
			continue
		}
		found = append(found, name)
	}
	if len(found) > 0 {
		t.Fatalf("these rules are in the root Makefile; move them into a make/*.mk "+
			"family file so the generator can publish them:\n  %s", strings.Join(found, "\n  "))
	}
}

// lintTargetsDefinition matches a column-zero LINT_TARGETS assignment in any
// of make's spellings. TestEveryLintTargetIsPhonyAndHasARule searches for the
// narrower literal "LINT_TARGETS :=", so this pattern is deliberately wider:
// a second definition in any spelling changes what `make lint` expands, and
// the audit that validates the list must not be reading a different one.
var lintTargetsDefinition = regexp.MustCompile(`^LINT_TARGETS\s*(:=|\+=|\?=|!=|=)`)

// TestExactlyOneLintTargetsDefinition keeps LINT_TARGETS single-valued across
// the whole source set.
//
// TestEveryLintTargetIsPhonyAndHasARule validates the FIRST definition it
// finds, scanning the root Makefile and then make/*.mk in sorted order; make
// itself uses the LAST one to win. A second definition in an earlier-sorting
// file — make/building.mk, say — therefore splits the two apart: the audit
// validates a list nobody runs while `make lint` expands a list nobody
// audits, and every other Makefile audit stays green throughout. One
// definition is the only state in which the audit's subject and make's are
// the same list.
func TestExactlyOneLintTargetsDefinition(t *testing.T) {
	t.Parallel()
	var found []string
	for _, path := range makefileSourcePaths(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		lineNo := 0
		for line := range strings.Lines(string(raw)) {
			lineNo++
			if lintTargetsDefinition.MatchString(strings.TrimRight(line, "\n")) {
				found = append(found, fmt.Sprintf("%s:%d", path, lineNo))
			}
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one LINT_TARGETS definition, found %d — make uses the last "+
			"and TestEveryLintTargetIsPhonyAndHasARule validates the first, so anything but one "+
			"lets `make lint` expand a list no audit reads:\n  %s",
			len(found), strings.Join(found, "\n  "))
	}
}

// annotationFieldKey matches a "## " line's content when it opens with the
// key-shaped prefix of a structured field (`proves:`, `trigger:`, and the
// rest). Such a line is not a summary. It mirrors internal/maketargetsdoc's
// fieldAttempt (parse.go), which is package main and so cannot be imported
// here; the two must agree on what a summary line looks like.
var annotationFieldKey = regexp.MustCompile(`^[a-z][a-z0-9-]*:`)

// annotationTargetSpecificVariable matches the remainder of a rule-shaped
// line when that remainder is a variable assignment rather than a
// prerequisite list — `install-home: PREFIX := $(HOME)/.local`. Such a line
// never carries the annotation; the block sits above the sibling line that
// carries the prerequisites and recipe. Mirrors the generator's
// targetSpecificVariable for the same reason as above.
var annotationTargetSpecificVariable = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\s*(:=|\+=|\?=|!=|=)`)

// makefileTargetsMissingSummaries returns every rule across
// makefileSourcePaths whose contiguous comment block carries no "##" summary
// line, plus the total number of annotatable rules examined.
//
// The block is read upward from the rule line through unbroken comment lines
// only: a blank line or any non-comment line between the block and its rule
// ends it, so a summary that has drifted away from the target it describes
// reads as missing rather than as attached to whatever now sits below it.
// Plain "#" rationale lines are transparent — real family files carry both,
// with the "##" block on either side of the "#" prose.
//
// total is returned so the caller can tell "no rules were found" (the parser
// lost its subject) apart from "every rule is annotated" (the audit is
// clean), the same distinction makefileRulesWithoutRecipes draws.
func makefileTargetsMissingSummaries(t *testing.T) (missing []string, total int) {
	t.Helper()
	for _, path := range makefileSourcePaths(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
		for i, line := range lines {
			if line == "" || line[0] == '\t' || line[0] == '#' || line[0] == ' ' {
				continue
			}
			name, rest, ok := strings.Cut(line, ":")
			if !ok || strings.HasPrefix(rest, "=") || strings.ContainsAny(name, " \t") || name == "" {
				continue
			}
			if strings.HasPrefix(name, ".") {
				continue // .PHONY and friends are directives, not rules.
			}
			if annotationTargetSpecificVariable.MatchString(strings.TrimSpace(rest)) {
				continue
			}
			total++
			if !hasSummaryAbove(lines[:i]) {
				missing = append(missing, fmt.Sprintf("%s:%d: %s", path, i+1, name))
			}
		}
	}
	return missing, total
}

// hasSummaryAbove reports whether the comment block immediately above a rule
// (i.e. at the end of before) contains at least one "##" summary line.
func hasSummaryAbove(before []string) bool {
	for _, line := range slices.Backward(before) {
		if !strings.HasPrefix(line, "#") {
			return false // blank or code: the block, if any, is not contiguous.
		}
		content, ok := strings.CutPrefix(line, "## ")
		if !ok {
			continue // plain "#" rationale, or a bare "##".
		}
		if strings.HasPrefix(content, " ") {
			continue // three-space continuation of the line above it.
		}
		if annotationFieldKey.MatchString(content) {
			continue // a structured field, not the summary.
		}
		return true
	}
	return false
}

// TestEveryTargetHasASummaryAnnotation is the drift tripwire for decision 2 of
// the Makefile/docs decomposition spec: every target carries a documentation
// annotation, with no exemption list. A target with no "##" summary is a
// target that appears in no doc and in `make help` with nothing beside it,
// which is how mutation-floor, fuzz-drive, coverage-gaps,
// coverage-gaps-selftest and test-install reached the point of having no
// target-level documentation anywhere in the repository.
//
// It keys on RULES rather than on .PHONY names, which is a strict superset:
// fuzz-drive carried a rule with no .PHONY declaration for months, and keying
// on .PHONY would have let a target dodge this audit by way of a second
// omission.
//
// The generator (internal/maketargetsdoc) refuses an unannotated rule too,
// but it reads make/*.mk only. This audit reads the root Makefile as well, so
// a rule that lands there — where the generator would never see it — is still
// caught. TestRootMakefileHasNoRules is what keeps that case hypothetical.
//
// Presence is all this proves. A summary can drift from what its recipe does
// and nothing here will say so; that is an accepted limit, not an oversight.
func TestEveryTargetHasASummaryAnnotation(t *testing.T) {
	t.Parallel()
	missing, total := makefileTargetsMissingSummaries(t)
	if total == 0 {
		t.Fatal("parsed 0 annotatable rules; the parser is broken, not the Makefile")
	}
	if len(missing) > 0 {
		t.Fatalf("these targets have no `## ` summary line directly above them, so they appear "+
			"in no doc and in `make help` with nothing beside them. Add a summary (see spec §2, "+
			"docs/superpowers/specs/2026-08-20-makefile-and-docs-decomposition-design.md):\n  %s",
			strings.Join(missing, "\n  "))
	}
}
