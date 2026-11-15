package repoaudit

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"primeradiant.com/evener/internal/maketargetsdoc/makefile"
)

// ruleLineName reports whether line declares a make rule, and if so returns the
// target name and everything after the colon.
//
// It calls the generator's own predicate, so the two cannot disagree about what
// a rule is. The extra step here is excluding a leading-dot directive (.PHONY,
// .DEFAULT_GOAL): RuleShape accepts those as rule-shaped because ParseFamily
// has to attach a block to one and reject it, while these audits treat a
// directive as neither a target nor a rule. Every Makefile audit reads rule
// lines through this one predicate, so they cannot drift apart.
func ruleLineName(line string) (name, rest string, ok bool) {
	name, rest, ok = makefile.RuleShape(line)
	if !ok || strings.HasPrefix(name, ".") {
		return "", "", false
	}
	return name, rest, true
}

// eachMakefileSource reads every file in MakefileSources and hands each body
// to visit, so every Makefile audit reads the same files the same way.
func eachMakefileSource(visit func(path string, raw []byte)) error {
	paths, err := MakefileSources()
	if err != nil {
		return err
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		visit(path, raw)
	}
	return nil
}

// readMakefileSources is eachMakefileSource for a check, which cannot go on
// without the files.
func readMakefileSources(a *audit, visit func(path string, raw []byte)) {
	if err := eachMakefileSource(visit); err != nil {
		a.Fatal(err)
	}
}

// makefilePhonyAndRuleNames returns the names declared across every .PHONY
// line and the targets that actually carry a rule, accumulated across every
// file in makefileSourcePaths. A rule line starts at column zero with
// `name:`; `:=` assignments (LINT_TARGETS := …) and recipe lines (which
// begin with a tab) are not rules.
func makefilePhonyAndRuleNames(a *audit) (phony, rules []string) {
	seen := map[string]bool{}
	readMakefileSources(a, func(_ string, raw []byte) {
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

// everyPhonyTargetHasARule is the tripwire for a gate that is listed,
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
func everyPhonyTargetHasARule(a *audit) {
	phony, rules := makefilePhonyAndRuleNames(a)
	if len(phony) == 0 || len(rules) == 0 {
		a.Fatalf("parsed %d .PHONY names and %d rules; the parser is broken, not the Makefile", len(phony), len(rules))
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
		a.Fatalf("these .PHONY targets have no rule, so `make <target>` silently does nothing and "+
			"exits 0 — a gate named here and in LINT_TARGETS reports green while executing "+
			"nothing, which is how lint-fuzz-registry's recipe went missing. Restore the "+
			"recipe, or drop the name:\n  %s", strings.Join(missing, "\n  "))
	}
}

// LintTargets returns the FIRST "LINT_TARGETS :=" assignment's values across
// MakefileSources — the same list both everyLintTargetIsPhonyAndHasARule and
// everyLintingRuleIsInLintTargets validate, and the one `make lint` actually
// expands because exactlyOneLintTargetsDefinition holds there to be exactly
// one definition. It returns nil when no assignment exists.
func LintTargets() ([]string, error) {
	var targets []string
	found := false
	err := eachMakefileSource(func(_ string, raw []byte) {
		if found {
			return
		}
		for line := range strings.Lines(string(raw)) {
			if after, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "LINT_TARGETS :="); ok {
				targets, found = strings.Fields(after), true
				return
			}
		}
	})
	return targets, err
}

// lintTargets is LintTargets for a check, which cannot go on without the
// list.
func lintTargets(a *audit) []string {
	targets, err := LintTargets()
	if err != nil {
		a.Fatal(err)
	}
	return targets
}

// everyLintTargetIsPhonyAndHasARule narrows the rule above onto the lint
// family, because LINT_TARGETS is the list `make lint` expands and the one a
// reader treats as the inventory of what the gate proves.
// docs/developing-evener/linting.md names that list in its opening paragraph
// and documents each member in its generated target table.
//
// It reads the FIRST LINT_TARGETS definition across the source files, which
// is only the list `make lint` actually expands because
// exactlyOneLintTargetsDefinition holds there to be exactly one.
func everyLintTargetIsPhonyAndHasARule(a *audit) {
	phony, rules := makefilePhonyAndRuleNames(a)
	targets := lintTargets(a)
	if len(targets) == 0 {
		a.Fatal("no LINT_TARGETS assignment found; `make lint` has no family list to expand")
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
			a.Errorf("LINT_TARGETS names %q, which has no rule: `make lint` runs it and it does nothing", target)
		}
		if !isPhony[target] {
			a.Errorf("LINT_TARGETS names %q, which is not .PHONY: a file of that name would skip the check", target)
		}
	}
}

const lintCacheCleanExemptionComment = "# lint-cache-clean is an intentional local cleanup action; keep it out of LINT_TARGETS so required lint runs can reuse the cache they are checking."

// lint-cache-sweep is the second local cleanup action: it reclaims caches of
// disposed worktrees, and a required lint run must never depend on (or be
// gated behind) deleting the very caches it warms. Same narrow shape as
// lint-cache-clean's exemption: this exact comment beside this one rule.
const lintCacheSweepExemptionComment = "# lint-cache-sweep is the second intentional local cleanup action (reclaiming disposed worktrees' caches); keep it out of LINT_TARGETS like lint-cache-clean above."

// makefileLintingRuleNames returns every rule name declared in
// make/linting.mk, excluding the `lint` alias itself (the target
// LINT_TARGETS feeds, not a member of it), the one explicitly documented local
// cleanup action, and directive lines like .PHONY. The cleanup exception is
// deliberately narrow and requires the exact adjacent comment next to that
// one rule; it is not a general allowlist for disconnected lint rules.
func makefileLintingRuleNames(a *audit) []string {
	raw, err := os.ReadFile("make/linting.mk")
	if err != nil {
		a.Fatalf("reading make/linting.mk: %v", err)
	}
	var names []string
	var previous string
	for line := range strings.Lines(string(raw)) {
		line = strings.TrimRight(line, "\n")
		name, _, ok := ruleLineName(line)
		if !ok || name == "lint" {
			previous = line
			continue
		}
		if name == "lint-cache-clean" && strings.TrimSpace(previous) == lintCacheCleanExemptionComment {
			previous = line
			continue
		}
		if name == "lint-cache-sweep" && strings.TrimSpace(previous) == lintCacheSweepExemptionComment {
			previous = line
			continue
		}
		names = append(names, name)
		previous = line
	}
	return names
}

// everyLintingRuleIsInLintTargets is the mirror
// everyLintTargetIsPhonyAndHasARule does not cover: that check walks
// LINT_TARGETS outward to confirm every member still has a working rule.
// This one walks make/linting.mk's rules inward to confirm every one of
// them is still a LINT_TARGETS member. A rule dropped from LINT_TARGETS
// still exists, still has a working recipe, and still runs standing alone,
// while `make help`, linting.md's generated table and its prose go on
// asserting that `make lint` runs it — but the required gate no longer
// calls it, and every other Makefile audit stays green.
//
// Every make/linting.mk rule is required except the one explicitly
// documented local cleanup action above. That narrow exception is
// recognized only for lint-cache-clean and only with its exact adjacent
// comment; a future lint-family rule must not gain a silent skip here.
func everyLintingRuleIsInLintTargets(a *audit) {
	rules := makefileLintingRuleNames(a)
	if len(rules) == 0 {
		a.Fatal("parsed 0 rules from make/linting.mk; the parser is broken, not the Makefile")
	}
	targets := lintTargets(a)
	if len(targets) == 0 {
		a.Fatal("no LINT_TARGETS assignment found; `make lint` has no family list to expand")
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
		a.Fatalf("these make/linting.mk rules are not in LINT_TARGETS, so `make lint` never calls "+
			"them even though they still work standing alone — PR #273's failure mode, mirrored: "+
			"a lint-shaped rule that quietly stopped being part of the required gate. Add each to "+
			"LINT_TARGETS, or if one is genuinely not gate material, say why in a comment beside "+
			"it:\n  %s", strings.Join(missing, "\n  "))
	}
}

// everyRuleIsPhony is the mirror of everyPhonyTargetHasARule. That
// check catches a .PHONY name whose recipe vanished; this one catches a rule
// that was never declared .PHONY, where a file of the same name at the repo
// root silently turns the target into a no-op.
func everyRuleIsPhony(a *audit) {
	phony, rules := makefilePhonyAndRuleNames(a)
	if len(phony) == 0 || len(rules) == 0 {
		a.Fatalf("parsed %d .PHONY names and %d rules; the parser is broken, not the Makefile", len(phony), len(rules))
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
		a.Fatalf("these rules are not .PHONY, so a file of the same name at the repo root "+
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
// everyPhonyTargetHasARule was written for — except that test cannot see
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
func makefileRulesWithoutRecipes(a *audit) (empty []string, total int) {
	for _, path := range makefileSourcePaths(a) {
		raw, err := os.ReadFile(path)
		if err != nil {
			a.Fatalf("reading %s: %v", path, err)
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
// to be done"), so this audit has to read it as recipe-less.
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

// everyRuleHasARecipe fails on a rule line with no recipe beneath it.
func everyRuleHasARecipe(a *audit) {
	empty, total := makefileRulesWithoutRecipes(a)
	if total == 0 {
		a.Fatalf("parsed 0 rule declarations; the parser is broken, not the Makefile")
	}
	if len(empty) > 0 {
		a.Fatalf("these rules have no recipe, so `make <target>` prints "+
			"\"Nothing to be done\" and exits 0 while checking nothing:\n  %s",
			strings.Join(empty, "\n  "))
	}
}

// rootMakefileHasNoRules keeps every rule in a family file. The generator
// reads make/*.mk only, so a rule left in the root would be annotated by the
// annotation audit and then have nowhere to be published — documented in the
// Makefile and invisible in the docs.
func rootMakefileHasNoRules(a *audit) {
	raw, err := os.ReadFile("Makefile")
	if err != nil {
		a.Fatalf("reading Makefile: %v", err)
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
		a.Fatalf("these rules are in the root Makefile; move them into a make/*.mk "+
			"family file so the generator can publish them:\n  %s", strings.Join(found, "\n  "))
	}
}

// lintTargetsDefinition matches a column-zero LINT_TARGETS assignment in any
// of make's spellings. everyLintTargetIsPhonyAndHasARule searches for the
// narrower literal "LINT_TARGETS :=", so this pattern is deliberately wider:
// a second definition in any spelling changes what `make lint` expands, and
// the audit that validates the list must not be reading a different one.
var lintTargetsDefinition = regexp.MustCompile(`^LINT_TARGETS\s*(:=|\+=|\?=|!=|=)`)

// exactlyOneLintTargetsDefinition keeps LINT_TARGETS single-valued across
// the whole source set.
//
// everyLintTargetIsPhonyAndHasARule validates the FIRST definition it
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
func exactlyOneLintTargetsDefinition(a *audit) {
	var found []string
	for _, path := range makefileSourcePaths(a) {
		raw, err := os.ReadFile(path)
		if err != nil {
			a.Fatalf("reading %s: %v", path, err)
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
		a.Fatalf("expected exactly one LINT_TARGETS definition, found %d — make uses the last "+
			"and everyLintTargetIsPhonyAndHasARule validates the first, so anything but one "+
			"lets `make lint` expand a list no audit reads:\n  %s",
			len(found), strings.Join(found, "\n  "))
	}
}

// lintGeneratedRecipe returns make/linting.mk's lint-generated recipe as one
// string: every tab-indented line beneath the rule, joined. Reading the
// recipe text rather than expanding it through make keeps this a static
// audit that costs nothing and cannot itself fail for environmental reasons.
func lintGeneratedRecipe(a *audit) string {
	raw, err := os.ReadFile("make/linting.mk")
	if err != nil {
		a.Fatalf("reading make/linting.mk: %v", err)
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
		a.Fatal("found no recipe beneath lint-generated: in make/linting.mk; this audit has lost its subject")
	}
	return strings.Join(recipe, "\n")
}

// everyGeneratedRegionIsInTheStalenessDiff closes the one direction the
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
func everyGeneratedRegionIsInTheStalenessDiff(a *audit) {
	recipe := lintGeneratedRecipe(a)

	docPaths, err := filepath.Glob("docs/developing-evener/*.md")
	if err != nil {
		a.Fatalf("globbing docs/developing-evener/*.md: %v", err)
	}
	var marked, missing []string
	for _, docPath := range docPaths {
		raw, err := os.ReadFile(docPath)
		if err != nil {
			a.Fatalf("reading %s: %v", docPath, err)
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
		a.Fatal("no doc under docs/developing-evener/ carries a generated region; the parser is broken, not the docs")
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		a.Fatalf("these docs carry a generated region that lint-generated never inspects, so "+
			"`make generate` rewrites them and the gate reports green over whatever they "+
			"drift into. Add them to lint-generated's staleness list in make/linting.mk:\n  %s",
			strings.Join(missing, "\n  "))
	}
}

// MakefileRecipes returns every rule's recipe across makefileSourcePaths,
// keyed by target name: the tab-indented lines beneath the rule line, joined.
// A target declared more than once accumulates every recipe line it carries.
//
// Any line that is neither a rule nor tab-indented ends the current recipe, so
// a `define`d block's tab-indented body cannot attach itself to whatever rule
// happened to come before it.
func MakefileRecipes() (map[string]string, error) {
	collected := map[string][]string{}
	err := eachMakefileSource(func(_ string, raw []byte) {
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
	if err != nil {
		return nil, err
	}
	recipes := make(map[string]string, len(collected))
	for name, lines := range collected {
		recipes[name] = strings.Join(lines, "\n")
	}
	return recipes, nil
}

// makefileRecipes is MakefileRecipes for a check, which cannot go on without
// the recipes.
func makefileRecipes(a *audit) map[string]string {
	recipes, err := MakefileRecipes()
	if err != nil {
		a.Fatal(err)
	}
	return recipes
}

// frontendLintRunsFromTheFrontendDirectory pins the one static check whose
// tool does not live at the repository root to a command that resolves it
// correctly, and pins the chain that reaches it.
//
// No `biome` binary is installed at the root (the frontend's pinned
// @biomejs/biome lives in cmd/evener-hub/frontend/node_modules, and
// mobile-native carries its own copy for the native tree), so `npx biome` from
// the repository root finds no local package and downloads the unrelated
// `biome@0.3.3`, which ignores its arguments and exits 0 (issue #1406). A
// root-scoped invocation therefore reports green while checking nothing.
//
// The check runs from `make test-web`, through evener-dev's web-checks, which
// invokes the frontend's own `npm run lint` from the frontend directory. This
// audit pins that whole chain rather than just the recipe: the frontend script
// stays the single definition of the two enforced scopes (src and
// appwire-client/typescript), and the web gate stays the only caller, so no
// root-scoped `npx biome` can creep back in.
func frontendLintRunsFromTheFrontendDirectory(a *audit) {
	recipes := makefileRecipes(a)
	testWeb, ok := recipes["test-web"]
	if !ok {
		a.Fatalf("no test-web rule, so nothing runs the frontend's Biome scopes")
	}
	if strings.Contains(testWeb, "npx") {
		a.Errorf("test-web shells out to npx (%q); from the repository root that resolves the "+
			"unrelated biome@0.3.3 and exits 0", strings.TrimSpace(testWeb))
	}
	webChecksRunNpmLint(a)
}

// webChecksRunNpmLint holds evener-dev's web-checks (cmd/evener-dev/webchecks.go)
// to running a "lint" check as the frontend's own `npm run lint`, so the
// frontend script stays the single definition of the lint scopes and a
// package.json edit cannot shrink the gate silently.
func webChecksRunNpmLint(a *audit) {
	raw, err := os.ReadFile("cmd/evener-dev/webchecks.go")
	if err != nil {
		a.Fatalf("reading cmd/evener-dev/webchecks.go: %v", err)
	}
	if !strings.Contains(string(raw), `var webChecks = []string{"typecheck", "test", "lint"}`) {
		a.Fatalf("webchecks.go no longer runs a lint check; the frontend Biome scopes have no caller")
	}
	if !strings.Contains(string(raw), `argv := []string{"npm", "run", check}`) {
		a.Fatalf("webchecks.go no longer runs each check as `npm run <check>`, so the frontend " +
			"script is no longer the single definition of the lint scopes")
	}
}

// lintGolangciLintsDarwinOffDarwin pins the cross-GOOS pass that closed
// #2928. golangci-lint compiles only the files the run's GOOS selects, and
// Linux CI's host pass selects no _darwin.go file, so a darwin-only finding
// reaches main unlinted and first fails `make lint-golangci` for a macOS
// contributor — agent/execenv/process_boundary_darwin.go reached main with
// five findings that way (#2847). lint-golangci must therefore run a second
// golangci-lint sweep with GOOS=darwin whenever the host is not darwin.
func lintGolangciLintsDarwinOffDarwin(a *audit) {
	recipes := makefileRecipes(a)
	recipe, ok := recipes["lint-golangci"]
	if !ok {
		a.Fatal("make/linting.mk has no lint-golangci rule, so `make lint` lints no module")
	}
	if !strings.Contains(recipe, "golangci-lint run") {
		a.Fatalf("lint-golangci's recipe holds no golangci-lint invocation, so this audit has lost "+
			"its subject. Recipe:\n%s", strings.TrimSpace(recipe))
	}
	if strings.Contains(recipe, "module-lint") {
		a.Fatalf("lint-golangci still dispatches through the retired module-lint runner; the merged "+
			"recipe invokes golangci-lint directly. Recipe:\n%s", strings.TrimSpace(recipe))
	}
	if !strings.Contains(recipe, "GOOS=darwin") {
		a.Fatalf("lint-golangci runs no GOOS=darwin pass, so CI (Linux) never compiles "+
			"a _darwin.go file and a darwin-only finding reaches main unlinted (#2928). Recipe:\n%s",
			strings.TrimSpace(recipe))
	}
	if !strings.Contains(recipe, "host_goos") || !strings.Contains(recipe, "!= darwin") {
		a.Fatalf("lint-golangci's darwin pass is not gated on the host GOOS, so it can run on a "+
			"darwin host too and double-analyse there. Recipe:\n%s", strings.TrimSpace(recipe))
	}
}
