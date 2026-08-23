package evener_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
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

// ruleLineName reports whether line declares a make rule, and if so returns the
// target name and everything after the colon.
//
// A rule line starts at column zero with `name:`. Recipe lines begin with a tab;
// `:=` and friends are assignments, not rules; a name carrying whitespace is a
// multi-target line this repo does not use; a leading `.` is a directive like
// .PHONY or .DEFAULT_GOAL, not a target.
//
// This predicate lives in one place because it did not used to: five audits
// each carried their own copy, and they had already drifted — only four of the
// five excluded leading-dot directives, so a `.NOTPARALLEL:` line would have
// counted as a real rule in one reading and not in the others. Nothing in the
// tree triggered it, which is exactly how a hand-copied predicate hides a
// divergence until the day it does.
//
// internal/maketargetsdoc/parse.go's ruleShape mirrors this deliberately and
// says so; that copy is unavoidable, because the generator is package main and
// cannot be imported here.
func ruleLineName(line string) (name, rest string, ok bool) {
	if line == "" || line[0] == '\t' || line[0] == '#' || line[0] == ' ' {
		return "", "", false
	}
	name, rest, ok = strings.Cut(line, ":")
	if !ok || strings.HasPrefix(rest, "=") || strings.ContainsAny(name, " \t") || name == "" {
		return "", "", false
	}
	if strings.HasPrefix(name, ".") {
		return "", "", false
	}
	return name, rest, true
}

// readMakefileSources reads every file in makefileSourcePaths and hands each
// body to visit. Six audits needed this loop and each had re-typed it, down to
// two different spellings of the read-error message.
func readMakefileSources(t testing.TB, visit func(path string, raw []byte)) {
	t.Helper()
	for _, path := range makefileSourcePaths(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		visit(path, raw)
	}
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
	readMakefileSources(t, func(_ string, raw []byte) {
		for line := range strings.Lines(string(raw)) {
			line = strings.TrimRight(line, "\n")
			if after, ok := strings.CutPrefix(line, ".PHONY:"); ok {
				phony = append(phony, strings.Fields(after)...)
				continue
			}
			name, _, ok := ruleLineName(line)
			if !ok || seen[name] {
				continue
			}
			seen[name] = true
			rules = append(rules, name)
		}
	})
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

// firstLintTargetsList returns the FIRST "LINT_TARGETS :=" assignment's
// values across makefileSourcePaths — the same list both
// TestEveryLintTargetIsPhonyAndHasARule and TestEveryLintingRuleIsInLintTargets
// validate, and the one `make lint` actually expands because
// TestExactlyOneLintTargetsDefinition holds there to be exactly one
// definition.
func firstLintTargetsList(t *testing.T) []string {
	t.Helper()
	for _, path := range makefileSourcePaths(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for line := range strings.Lines(string(raw)) {
			if after, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "LINT_TARGETS :="); ok {
				return strings.Fields(after)
			}
		}
	}
	return nil
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
	targets := firstLintTargetsList(t)
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

// makefileLintingRuleNames returns every rule name declared in
// make/linting.mk, excluding the `lint` alias itself (the target
// LINT_TARGETS feeds, not a member of it) and directive lines like
// .PHONY.
func makefileLintingRuleNames(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("make/linting.mk")
	if err != nil {
		t.Fatalf("reading make/linting.mk: %v", err)
	}
	var names []string
	for line := range strings.Lines(string(raw)) {
		line = strings.TrimRight(line, "\n")
		name, _, ok := ruleLineName(line)
		if !ok || name == "lint" {
			continue
		}
		names = append(names, name)
	}
	return names
}

// TestEveryLintingRuleIsInLintTargets is the mirror
// TestEveryLintTargetIsPhonyAndHasARule does not cover: that test walks
// LINT_TARGETS outward to confirm every member still has a working rule.
// This one walks make/linting.mk's rules inward to confirm every one of
// them is still a LINT_TARGETS member. Nothing enforced that direction
// before this test, and it is the direction PR #273's failure actually
// took reversed onto this branch: a reviewer proved it by dropping
// lint-generated from LINT_TARGETS, and all ten prior audits kept passing
// while `make help`, linting.md's generated table (`trigger: Required CI
// (via make lint)`), and linting.md's prose kept asserting it runs, and
// `make -n lint` held zero references to it. lint-generated still exists,
// still has a working recipe, and `make lint-generated` standing alone
// still runs it — but the required gate silently stopped calling it.
//
// Every one of make/linting.mk's rules was checked against `make -n lint`
// before this test was written, and each is genuinely required: none is
// exempt, so this test hardcodes no allowlist. If a future lint-family rule
// is deliberately NOT gate material, say so in a comment next to its rule
// rather than adding a silent skip here.
func TestEveryLintingRuleIsInLintTargets(t *testing.T) {
	t.Parallel()
	rules := makefileLintingRuleNames(t)
	if len(rules) == 0 {
		t.Fatal("parsed 0 rules from make/linting.mk; the parser is broken, not the Makefile")
	}
	targets := firstLintTargetsList(t)
	if len(targets) == 0 {
		t.Fatal("no LINT_TARGETS assignment found; `make lint` has no family list to expand")
	}
	inTargets := make(map[string]bool, len(targets))
	for _, target := range targets {
		inTargets[target] = true
	}
	var missing []string
	for _, rule := range rules {
		if !inTargets[rule] {
			missing = append(missing, rule)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("these make/linting.mk rules are not in LINT_TARGETS, so `make lint` never calls "+
			"them even though they still work standing alone — PR #273's failure mode, mirrored: "+
			"a lint-shaped rule that quietly stopped being part of the required gate. Add each to "+
			"LINT_TARGETS, or if one is genuinely not gate material, say why in a comment beside "+
			"it:\n  %s", strings.Join(missing, "\n  "))
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
		fileEmpty, fileTotal := rulesWithoutRecipesInLines(strings.Split(string(raw), "\n"))
		empty = append(empty, fileEmpty...)
		total += fileTotal
	}
	sort.Strings(empty)
	return empty, total
}

// rulesWithoutRecipesInLines is the scan makefileRulesWithoutRecipes runs
// against one source file's lines. It is factored out, pure, so the two
// hollow-gate shapes below can be pinned directly against synthetic content
// instead of by editing the real Makefile.
//
// Two shapes read as "has prerequisites" if the text after the rule's colon
// is tested for emptiness verbatim, when GNU make treats both as having
// none:
//
//   - A trailing "#…" comment. `lint-fuzz-registry: # TODO restore after
//     the rebase` leaves "rest" as " # TODO restore after the rebase" —
//     visibly non-empty — but make strips the comment before looking at the
//     prerequisite list, so the real list is empty.
//   - A bare ";". `lint-fuzz-registry: ;` leaves "rest" as " ;", also
//     non-empty by the same naive test, but a semicolon with nothing after
//     it introduces an EMPTY inline recipe, not a prerequisite.
//
// Both are the same hollow-gate shape this audit exists to catch: a rule
// that is phony, has a rule line, and does nothing. A rebase conflict
// resolution that leaves either shape behind passes make cleanly ("Nothing
// to be done") and, before this fix, passed this audit too.
func rulesWithoutRecipesInLines(lines []string) (empty []string, total int) {
	for i, line := range lines {
		name, rest, ok := ruleLineName(line)
		if !ok {
			continue
		}
		total++
		restNoComment, _, _ := strings.Cut(rest, "#")
		restTrimmed := strings.TrimSpace(restNoComment)
		if restTrimmed != "" && restTrimmed != ";" {
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

// TestEveryRuleHasARecipeCatchesTrailingComment pins the first hollow-gate
// shape rulesWithoutRecipesInLines must not read as "has prerequisites": a
// rule line whose only text after the colon is a "#…" comment. This is
// exactly the shape a rebase conflict resolution left behind on
// lint-fuzz-registry, and `make` runs it as "Nothing to be done" — a gate
// that reports green while checking nothing.
func TestEveryRuleHasARecipeCatchesTrailingComment(t *testing.T) {
	t.Parallel()
	lines := strings.Split("lint-fuzz-registry: # TODO restore after the rebase\n", "\n")
	empty, total := rulesWithoutRecipesInLines(lines)
	if total != 1 {
		t.Fatalf("expected 1 rule declaration parsed, got %d", total)
	}
	if !slices.Contains(empty, "lint-fuzz-registry") {
		t.Fatalf("expected lint-fuzz-registry to be reported as recipe-less (a trailing comment "+
			"is not a prerequisite), got %v", empty)
	}
}

// TestEveryRuleHasARecipeCatchesSemicolonOnlyRecipe pins the second
// hollow-gate shape: a rule line ending in a bare ";" with nothing after
// it. GNU make reads ";" as introducing an inline recipe, and an inline
// recipe with no text is empty — the same "Nothing to be done" outcome as
// no recipe at all — but the raw text after the colon (" ;") is non-empty,
// so a verbatim emptiness test misreads it as a prerequisite list.
func TestEveryRuleHasARecipeCatchesSemicolonOnlyRecipe(t *testing.T) {
	t.Parallel()
	lines := strings.Split("lint-fuzz-registry: ;\n", "\n")
	empty, total := rulesWithoutRecipesInLines(lines)
	if total != 1 {
		t.Fatalf("expected 1 rule declaration parsed, got %d", total)
	}
	if !slices.Contains(empty, "lint-fuzz-registry") {
		t.Fatalf("expected lint-fuzz-registry to be reported as recipe-less (a bare \";\" is an "+
			"empty inline recipe, not a prerequisite), got %v", empty)
	}
}

// TestEveryRuleHasARecipeAllowsRealSemicolonRecipe guards the fix above
// against overcorrecting: a rule whose inline recipe after ";" actually has
// content must NOT be reported as recipe-less.
func TestEveryRuleHasARecipeAllowsRealSemicolonRecipe(t *testing.T) {
	t.Parallel()
	lines := strings.Split("lint-fuzz-registry: ; echo hi\n", "\n")
	empty, total := rulesWithoutRecipesInLines(lines)
	if total != 1 {
		t.Fatalf("expected 1 rule declaration parsed, got %d", total)
	}
	if slices.Contains(empty, "lint-fuzz-registry") {
		t.Fatalf("lint-fuzz-registry has a real inline recipe after \";\" and should not be "+
			"reported as recipe-less, got %v", empty)
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
		name, _, ok := ruleLineName(line)
		if !ok {
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
// finds, scanning the root Makefile and then make/*.mk in sorted order. What
// `make lint` expands is neither the first definition nor the last: a rule's
// prerequisite list is expanded when the RULE IS READ, so `lint` is fixed to
// whatever LINT_TARGETS holds at make/linting.mk's `lint:` line, and a
// definition in a later-sorting file never reaches it at all. An
// earlier-sorting one — make/building.mk, say — is the case that splits audit
// and make apart: the audit validates that copy while `lint` still expands
// linting.mk's, so the audit reads a list nobody runs and `make lint` runs a
// list nobody audits, with every other Makefile audit green throughout. One
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
			name, rest, ok := ruleLineName(line)
			if !ok {
				continue
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

// lintGeneratedRecipe returns make/linting.mk's lint-generated recipe as one
// string: every tab-indented line beneath the rule, joined. Reading the
// recipe text rather than expanding it through make keeps this a static
// audit that costs nothing and cannot itself fail for environmental reasons.
func lintGeneratedRecipe(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("make/linting.mk")
	if err != nil {
		t.Fatalf("reading make/linting.mk: %v", err)
	}
	var recipe []string
	inRule := false
	for line := range strings.Lines(string(raw)) {
		line = strings.TrimRight(line, "\n")
		if strings.HasPrefix(line, "lint-generated:") {
			inRule = true
			continue
		}
		if !inRule {
			continue
		}
		if !strings.HasPrefix(line, "\t") {
			break
		}
		recipe = append(recipe, line)
	}
	if len(recipe) == 0 {
		t.Fatal("found no recipe beneath lint-generated: in make/linting.mk; this audit has lost its subject")
	}
	return strings.Join(recipe, "\n")
}

// TestEveryGeneratedRegionIsInTheStalenessDiff closes the one direction the
// generator cannot see for itself.
//
// internal/maketargetsdoc already guards both directions it can reach:
// generateOne errors on a make/*.mk with no stemToDoc entry, and
// checkOrphanRegions errors on a doc region naming a .mk that does not exist.
// Neither can see lint-generated's diff list, which is a hand-maintained
// literal inside a recipe. Add make/newfamily.mk plus a newfamily.md carrying
// a region and forget to widen that literal, and `make generate` faithfully
// regenerates the new doc while the gate never inspects it — green forever
// over stale content, which is the lint-fuzz-registry failure class this
// whole branch exists to install tripwires against.
//
// The assertion is deliberately textual: every doc under
// docs/developing-evener/ that carries a generated region must have its path
// appear in the recipe. A path in the recipe that carries no region is
// harmless — a comparison over a file nothing rewrites — so this checks the
// one direction that can actually go wrong.
func TestEveryGeneratedRegionIsInTheStalenessDiff(t *testing.T) {
	t.Parallel()
	recipe := lintGeneratedRecipe(t)

	docPaths, err := filepath.Glob("docs/developing-evener/*.md")
	if err != nil {
		t.Fatalf("globbing docs/developing-evener/*.md: %v", err)
	}
	var marked, missing []string
	for _, docPath := range docPaths {
		raw, err := os.ReadFile(docPath)
		if err != nil {
			t.Fatalf("reading %s: %v", docPath, err)
		}
		if !strings.Contains(string(raw), "BEGIN GENERATED: make targets") {
			continue
		}
		marked = append(marked, docPath)
		if !strings.Contains(recipe, filepath.ToSlash(docPath)) {
			missing = append(missing, docPath)
		}
	}
	if len(marked) == 0 {
		t.Fatal("no doc under docs/developing-evener/ carries a generated region; the parser is broken, not the docs")
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("these docs carry a generated region that lint-generated never inspects, so "+
			"`make generate` rewrites them and the gate reports green over whatever they "+
			"drift into. Add them to lint-generated's staleness list in make/linting.mk:\n  %s",
			strings.Join(missing, "\n  "))
	}
}

// lintGateCommands maps every gate `make lint` is required to run to a
// fragment of that gate's recipe. The fragment, not the target name, is what
// the two audits below look for in `make -n lint`'s output: run_quiet_lint
// wraps each recipe in `if ( … )`, so a dry run prints COMMANDS and never
// target names, and an audit keyed on names would match nothing and have to be
// written so that it could not fail.
//
// The list is deliberately written out rather than derived from LINT_TARGETS.
// Derived, it could not see the failure it exists to catch: drop a gate from
// LINT_TARGETS and the expectation drops with it, leaving the audit green over
// a shrunken gate — PR #273 again, with the audit joining in. Written out, the
// deletion has to be made here too, where a reviewer sees it in the diff. If
// you are reading this because the map and LINT_TARGETS disagree, the question
// is which of them is wrong, not which is easier to edit.
var lintGateCommands = map[string]string{
	"lint-naming":        "go run ./cmd/evener-dev/bin tomlcheck",
	"lint-gofmt":         "gofmt -l",
	"lint-evenerfuzz":    "-tags evenerfuzz",
	"lint-eval":          "-tags eval ",
	"lint-internal":      "go run ./cmd/evener-dev/bin internalcheck",
	"lint-golangci":      "module-lint",
	"lint-generated":     "docs/appwire-protocol.md",
	"lint-fuzz-registry": "scripts/fuzz/fuzz-registry-check.sh",
	"secret-scan":        "scripts/ops/gitleaks-scan.sh repo",
}

// makefileRecipes returns every rule's recipe across makefileSourcePaths,
// keyed by target name: the tab-indented lines beneath the rule line, joined.
// A target declared more than once accumulates every recipe line it carries.
//
// Any line that is neither a rule nor tab-indented ends the current recipe, so
// a `define`d block's tab-indented body cannot attach itself to whatever rule
// happened to come before it.
func makefileRecipes(t *testing.T) map[string]string {
	t.Helper()
	collected := map[string][]string{}
	readMakefileSources(t, func(_ string, raw []byte) {
		current := ""
		for line := range strings.Lines(string(raw)) {
			line = strings.TrimRight(line, "\n")
			if name, _, ok := ruleLineName(line); ok {
				current = name
				continue
			}
			if strings.HasPrefix(line, "\t") {
				if current != "" {
					collected[current] = append(collected[current], line)
				}
				continue
			}
			current = ""
		}
	})
	recipes := make(map[string]string, len(collected))
	for name, lines := range collected {
		recipes[name] = strings.Join(lines, "\n")
	}
	return recipes
}

// makeLintDryRun runs `make -n -k lint` from the repository root and returns
// what it printed. It is the one audit input in this file that comes from make
// itself rather than from Makefile text: every other assertion here is a
// textual proxy, and a proxy cannot tell whether `lint` still depends on the
// list it reads.
//
// -n prints recipe lines instead of running them, with one GNU make exception
// this audit has to account for: a line referencing $(MAKE) counts as
// recursion and IS executed, with -n handed down through MAKEFLAGS so the
// sub-make prints rather than runs. lint-generated's recipe is such a line, so
// its `git diff --exit-code HEAD` really runs, and the dry run's EXIT STATUS
// therefore reports whether this working tree's generated docs are fresh — not
// whether `make lint` is wired up. Hence two deliberate choices: the callers
// assert on the printed recipes and never on the exit status, and -k keeps
// make going past that failure, because without it a stale doc truncates the
// output at lint-generated and every gate after it reads as missing.
//
// Empty output is the vacuity failure. It means make never reached the point
// of printing a recipe, so every assertion built on the output would hold
// against nothing; it fails loudly here instead.
func makeLintDryRun(t *testing.T, makePath string) string {
	t.Helper()
	cmd := exec.Command(makePath, "-n", "-k", "lint")
	// A parent make (this suite runs under `make test`) exports its own flags,
	// job-server file descriptors and recursion depth. Inheriting them makes
	// the child warn about an unavailable job server and can change what it
	// prints, so the child starts from a clean slate.
	for _, env := range os.Environ() {
		name, _, _ := strings.Cut(env, "=")
		if name == "MAKEFLAGS" || name == "MFLAGS" || name == "MAKELEVEL" {
			continue
		}
		cmd.Env = append(cmd.Env, env)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatalf("`%s -n -k lint` printed no recipe lines (%v), so this audit has no subject and "+
			"would hold against nothing. That is a broken Makefile, not a clean one.\nstderr:\n%s",
			makePath, runErr, stderr.String())
	}
	return stdout.String()
}

// TestMakeLintRunsEveryGateInLintTargets asks make what `make lint` actually
// expands to.
//
// Every other Makefile audit in this file reads Makefile TEXT. That is a proxy
// for what make does, and it has a blind spot exactly where this branch's
// original defect lives: nothing in the text says `lint`'s prerequisite list is
// still `$(LINT_TARGETS)`. Rewrite the rule as `lint: lint-naming` and the gate
// drops from nine checks to one with every textual audit still green, while
// LINT_TARGETS goes on being validated as a list nothing expands. Move a gate's
// rule into another family file and drop it from LINT_TARGETS and
// TestEveryLintingRuleIsInLintTargets stops seeing it too, because that audit
// reads make/linting.mk alone — PR #273 rebuilt out of two edits that are
// individually harmless.
//
// So this one shells out. It asserts that the set of gates `make lint` really
// invokes is exactly LINT_TARGETS, matching on each gate's distinguishing
// command because the wrapper hides the names (see lintGateCommands).
//
// What it does not prove: that no EXTRA work rides along in `make lint`. The
// dry run's other lines are not attributable to a target without reimplementing
// make, and an extra check is not the failure mode this repository keeps
// suffering.
func TestMakeLintRunsEveryGateInLintTargets(t *testing.T) {
	t.Parallel()
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skipf("make is not on PATH (%v), so nothing can ask it what `make lint` expands to", err)
	}

	targets := firstLintTargetsList(t)
	if len(targets) == 0 {
		t.Fatal("no LINT_TARGETS assignment found; `make lint` has no family list to expand")
	}
	inTargets := make(map[string]bool, len(targets))
	for _, target := range targets {
		inTargets[target] = true
		if _, known := lintGateCommands[target]; !known {
			t.Errorf("LINT_TARGETS names %q, which lintGateCommands does not cover, so no audit "+
				"checks that `make lint` runs it. Add it there, pinned to the command that "+
				"distinguishes its recipe.", target)
		}
	}
	for name := range lintGateCommands {
		if !inTargets[name] {
			t.Errorf("lintGateCommands requires %q of `make lint`, but LINT_TARGETS no longer "+
				"names it. Either restore it to LINT_TARGETS, or delete it here and say in the "+
				"commit message why the gate stopped being required.", name)
		}
	}

	// The map's fragments are worth matching only if each really identifies its
	// own gate. A fragment absent from its recipe could never appear in the dry
	// run; a fragment shared with a sibling gate would go on matching after its
	// own gate stopped running.
	recipes := makefileRecipes(t)
	for name, fragment := range lintGateCommands {
		recipe, ok := recipes[name]
		if !ok {
			t.Errorf("lintGateCommands names %q, which has no recipe in any make source file", name)
			continue
		}
		if !strings.Contains(recipe, fragment) {
			t.Errorf("lintGateCommands pins %q to %q, which is no longer in that target's recipe, "+
				"so the dry-run check for it can never fail. Repoint it at a command the recipe "+
				"actually runs.", name, fragment)
		}
		for other, otherRecipe := range recipes {
			if other == name || lintGateCommands[other] == "" {
				continue
			}
			if strings.Contains(otherRecipe, fragment) {
				t.Errorf("lintGateCommands pins %q to %q, which also appears in gate %q's recipe, "+
					"so the check would keep matching after %s stopped running.",
					name, fragment, other, name)
			}
		}
	}

	dryRun := makeLintDryRun(t, makePath)
	var missing []string
	for name, fragment := range lintGateCommands {
		if !strings.Contains(dryRun, fragment) {
			missing = append(missing, fmt.Sprintf("%s (no %q in the dry run)", name, fragment))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("`make lint` does not run these gates. They are in LINT_TARGETS and their rules "+
			"still work standing alone, but make's own expansion of `lint` never reaches them — "+
			"the shape PR #273 shipped, where a required gate reported PASS while executing "+
			"nothing:\n  %s\n\nfull dry run:\n%s", strings.Join(missing, "\n  "), dryRun)
	}
}

// lintGeneratedInvokesGenerate matches a command that runs the `generate`
// TARGET: $(MAKE) expands to a make binary under some path or name (`make`,
// `gmake`, `/usr/bin/make`), and the recipe chains the diff onto it with `&&`
// so a generator that FAILS fails the gate instead of falling through to a
// clean comparison over outputs nothing rewrote.
//
// Requiring the command position — start of line, or after `(`, `;`, `&`, `|`
// — is what keeps this able to fail. lint-generated's own error message
// contains the words "make generate has already run", so a bare substring
// search would go on matching after the recipe was reverted to a hand-copied
// `go generate ./appwire/...`, which is the exact regression this pins.
var lintGeneratedInvokesGenerate = regexp.MustCompile(`(^|[(;&|])\s*[^\s;&|]*make\s+generate\s+&&`)

// lintGeneratedDiffNamesHEAD matches HEAD as a whole word among git diff's
// arguments.
var lintGeneratedDiffNamesHEAD = regexp.MustCompile(`\bHEAD\b`)

// TestMakeLintGeneratedRegeneratesAndDiffsHEAD pins the two properties that
// make lint-generated a staleness gate rather than a decoration, against what
// make actually expands its recipe to.
//
// It must run the `generate` TARGET, not a copy of the generate commands: a
// hand-copied list regenerates a subset, and the diff over the rest then
// compares committed output with itself forever. And it must diff against HEAD
// with --exit-code: a bare `git diff` compares the working tree with the INDEX,
// so staging a regeneration without committing it silenced the gate over
// exactly the committed content it claims to check (ruling R26), and without
// --exit-code a difference prints and exits zero.
//
// TestEveryGeneratedRegionIsInTheStalenessDiff covers the other half — that the
// diff names every doc carrying a generated region. It checks that the recipe
// contains each path as text, which stays true whatever the recipe does with
// them, so on its own it cannot tell a working gate from a hollow one.
func TestMakeLintGeneratedRegeneratesAndDiffsHEAD(t *testing.T) {
	t.Parallel()
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skipf("make is not on PATH (%v), so nothing can ask it what `make lint` expands to", err)
	}
	dryRun := makeLintDryRun(t, makePath)

	var line string
	for candidate := range strings.Lines(dryRun) {
		if strings.Contains(candidate, lintGateCommands["lint-generated"]) {
			line = strings.TrimRight(candidate, "\n")
			break
		}
	}
	if line == "" {
		t.Fatalf("`make lint`'s dry run holds no lint-generated recipe (nothing naming %q), so "+
			"this audit has no subject:\n%s", lintGateCommands["lint-generated"], dryRun)
	}

	if !lintGeneratedInvokesGenerate.MatchString(line) {
		t.Errorf("lint-generated does not run `$(MAKE) generate &&` ahead of its diff, so it "+
			"compares only whatever its own copy of the generate commands happened to rewrite — "+
			"green forever over everything else. Recipe as make expands it:\n%s", line)
	}

	_, afterDiff, found := strings.Cut(line, "git diff")
	if !found {
		t.Fatalf("lint-generated runs no `git diff`, so nothing compares the regenerated output "+
			"with what is committed:\n%s", line)
	}
	// Everything up to the `||` is the diff invocation itself. The message after
	// it mentions HEAD in prose, and must not be allowed to satisfy this.
	diffArgs, _, _ := strings.Cut(afterDiff, "||")
	if !strings.Contains(diffArgs, "--exit-code") {
		t.Errorf("lint-generated's `git diff` has no --exit-code, so a difference prints and the "+
			"gate still exits zero. Arguments as make expands them: git diff%s", diffArgs)
	}
	if !lintGeneratedDiffNamesHEAD.MatchString(diffArgs) {
		t.Errorf("lint-generated's `git diff` does not name HEAD, so it compares the working tree "+
			"with the INDEX and `git add` silences it over still-stale COMMITTED output (ruling "+
			"R26). Arguments as make expands them: git diff%s", diffArgs)
	}
}

// TestLintGeneratedRejectsOutputDeletedFromHEAD exercises the real Make recipe
// with real git. A generated path deleted in HEAD is recreated as untracked;
// `git diff HEAD -- <path>` alone does not report that file, so the gate must
// also require every expected output to remain tracked.
func TestLintGeneratedRejectsOutputDeletedFromHEAD(t *testing.T) {
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skipf("make is not on PATH: %v", err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git is not on PATH: %v", err)
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	fixture := t.TempDir()
	copyRepositoryFile(t, repoRoot, fixture, "Makefile", 0o644)
	copyRepositoryFile(t, repoRoot, fixture, "make/linting.mk", 0o644)

	generated := []string{
		"docs/appwire-protocol.md",
		"cmd/evener-hub/frontend/src/protocol/types.gen.ts",
		"docs/developing-evener/README.md",
		"docs/developing-evener/building.md",
		"docs/developing-evener/testing.md",
		"docs/developing-evener/linting.md",
		"docs/developing-evener/fuzzing.md",
		"docs/developing-evener/coverage.md",
	}
	for _, path := range generated {
		if err := os.MkdirAll(filepath.Join(fixture, filepath.Dir(path)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	makefile := filepath.Join(fixture, "Makefile")
	raw, err := os.ReadFile(makefile)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("\n.PHONY: generate\ngenerate:\n\t@touch "+strings.Join(generated, " ")+"\n")...)
	if err := os.WriteFile(makefile, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(path string, args ...string) string {
		t.Helper()
		cmd := exec.Command(path, args...)
		cmd.Dir = fixture
		out, runErr := cmd.CombinedOutput()
		if runErr != nil {
			t.Fatalf("%s %v: %v\n%s", path, args, runErr, out)
		}
		return string(out)
	}
	run(makePath, "generate")
	run(gitPath, "init", "-q")
	run(gitPath, "add", "--", "Makefile", "make/linting.mk")
	gitAddGenerated := append([]string{"add", "--"}, generated...)
	run(gitPath, gitAddGenerated...)
	run(gitPath, "-c", "user.name=Evener Test", "-c", "user.email=test@evener.invalid", "commit", "-qm", "baseline")

	deleted := generated[0]
	if err := os.Remove(filepath.Join(fixture, deleted)); err != nil {
		t.Fatal(err)
	}
	run(gitPath, "add", "-u", "--", deleted)
	run(gitPath, "-c", "user.name=Evener Test", "-c", "user.email=test@evener.invalid", "commit", "-qm", "delete generated output")

	cmd := exec.Command(makePath, "lint-generated")
	cmd.Dir = fixture
	out, runErr := cmd.CombinedOutput()
	if runErr == nil {
		t.Fatalf("lint-generated accepted %s after the generator recreated it untracked:\n%s", deleted, out)
	}
	if _, err := os.Stat(filepath.Join(fixture, deleted)); err != nil {
		t.Fatalf("fixture generator did not recreate %s: %v\n%s", deleted, err, out)
	}
}
