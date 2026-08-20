# Makefile and Developer-Docs Decomposition Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split the 522-line root Makefile into six family `.mk` files, give every target a machine-checked documentation annotation, generate the target reference into hand-written docs, and move all dev-facing docs under `docs/developing-evener/`.

**Architecture:** Three phases. Phase 1 splits the Makefile and updates every consumer that reads it, changing no target behaviour. Phase 2 creates `docs/developing-evener/`, splits `docs/testing.md` six ways, and sweeps links — leaving the Canonical Gate Matrix intact so the repo is never without a gate reference. Phase 3 adds the annotation parser, the generator, `make help`, the annotation audit, and only then dissolves the matrix into generated regions.

**Tech Stack:** GNU Make 3.81-compatible makefiles, Go 1.25 (workspace with 8 modules), `go generate`, existing `golangci-lint` gate.

**Spec:** `docs/superpowers/specs/2026-08-20-makefile-and-docs-decomposition-design.md`

## Global Constraints

- **GNU Make 3.81 compatibility is mandatory.** macOS ships 3.81; CI has 4.x. Use no feature newer than 3.81. Verify locally with `/usr/bin/make`.
- **The include must be anchored:** `include $(dir $(lastword $(MAKEFILE_LIST)))make/*.mk`. A bare relative include breaks `scripts/gate/merge-approval-gate-selftest.sh`.
- **No target behaviour changes.** Recipes move verbatim. The one deliberate product change is `agent/workspace_info.go`, and it exists to preserve today's behaviour across the split.
- **Every lint run in this worktree must isolate the cache:** `export GOLANGCI_LINT_CACHE=/private/tmp/claude-501/-Users-jesse-git-prime-radiant-evener/422cd0dd-aa5c-42ce-b71e-4a1c3b90a6be/scratchpad/wt-glcache`. A content-identical checkout at a second path poisons the shared golangci-lint cache (issue #290) and breaks the main checkout.
- **Family membership rule:** a target lives in the `.mk` named for the doc that explains it — not the CI job that runs it.
- **Six families:** `building`, `testing`, `linting`, `fuzzing`, `coverage`, `repo`. 66 targets (65 existing rules + `help`).
- **Commit after every task.** Never use `--no-verify`.

---

# Phase 1 — Makefile decomposition

## Task 1: Close the two reverse audit gaps, before any split

Two hollow-gate variants exist today that no audit catches: a rule with no `.PHONY` declaration (`fuzz-drive`), and a rule with an empty recipe. Writing these audits first means they are proven against the current Makefile before it moves.

**Files:**
- Modify: `makefiletargets_audit_test.go`
- Modify: `Makefile:1` (add `fuzz-drive` to `.PHONY`)

**Interfaces:**
- Consumes: `makefilePhonyAndRuleNames(t) (phony, rules []string)` — existing helper at `makefiletargets_audit_test.go:14`.
- Produces: nothing later tasks depend on.

- [ ] **Step 1: Write both failing tests**

Append to `makefiletargets_audit_test.go`:

```go
// TestEveryRuleIsPhony is the mirror of TestEveryPhonyTargetHasARule. That
// test catches a .PHONY name whose recipe vanished; this one catches a rule
// that was never declared .PHONY, where a file of the same name at the repo
// root silently turns the target into a no-op. fuzz-drive was in exactly that
// state when this test was written.
func TestEveryRuleIsPhony(t *testing.T) {
	t.Parallel()
	phony, rules := makefilePhonyAndRuleNames(t)
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
```

- [ ] **Step 2: Run it and watch it fail for the right reason**

```bash
go test . -run TestEveryRuleIsPhony -count=1
```

Expected: FAIL, naming exactly `fuzz-drive`. If it names anything else, stop and investigate before proceeding — the parser may be misreading a line.

- [ ] **Step 3: Fix the omission**

In `Makefile:1`, add `fuzz-drive` to the `.PHONY` list, immediately after `fuzz-continuous`.

- [ ] **Step 4: Confirm it passes**

```bash
go test . -run TestEveryRuleIsPhony -count=1
```

Expected: PASS.

- [ ] **Step 5: Write the empty-recipe audit**

This one passes immediately against the current tree — there are no empty recipes today. That is fine: its value is the tripwire, and Step 6 proves it can fire.

Add a second helper and test to `makefiletargets_audit_test.go`:

```go
// makefileRulesWithoutRecipes returns rule names whose rule line is followed by
// no recipe line. GNU make treats such a target as satisfied: it prints
// "Nothing to be done for `x'" and exits 0. A gate in that state reports green
// while executing nothing, which is the lint-fuzz-registry failure mode that
// TestEveryPhonyTargetHasARule was written for — except that test cannot see
// it, because its parser records a rule from the `name:` line alone and never
// looks at the indented recipe beneath.
func makefileRulesWithoutRecipes(t *testing.T) []string {
	t.Helper()
	var empty []string
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
	return empty
}

// TestEveryRuleHasARecipe fails on a rule line with no recipe beneath it.
func TestEveryRuleHasARecipe(t *testing.T) {
	t.Parallel()
	if empty := makefileRulesWithoutRecipes(t); len(empty) > 0 {
		t.Fatalf("these rules have no recipe, so `make <target>` prints "+
			"\"Nothing to be done\" and exits 0 while checking nothing:\n  %s",
			strings.Join(empty, "\n  "))
	}
}
```

Note: this references `makefileSourcePaths`, introduced in Task 2. For this task only, inline `[]string{"Makefile"}` in its place, and Task 2 replaces it.

- [ ] **Step 6: Prove the new audit can actually fire**

```bash
cp Makefile /tmp/Makefile.bak
# Delete the single recipe line of lint-fuzz-registry (line 515).
sed -i '' '515d' Makefile
go test . -run TestEveryRuleHasARecipe -count=1
```

Expected: FAIL, naming `lint-fuzz-registry`. Then restore:

```bash
cp /tmp/Makefile.bak Makefile && rm /tmp/Makefile.bak
go test . -run TestEveryRuleHasARecipe -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add makefiletargets_audit_test.go Makefile
git commit -m "test: catch rules that are not .PHONY and rules with no recipe

TestEveryPhonyTargetHasARule catches a .PHONY name whose rule vanished. Two
sibling failures were uncaught: a rule never declared .PHONY (fuzz-drive was
in that state), and a rule line with an empty recipe, which make reports as
success while running nothing."
```

---

## Task 2: Make every Makefile-reading test read the file set

**Files:**
- Modify: `makefiletargets_audit_test.go` (add `makefileSourcePaths`, rewire both existing tests and the Task 1 helper)
- Modify: `makefile_audit_test.go:51`
- Modify: `install_fuzz_test.go:25`

**Interfaces:**
- Produces: `makefileSourcePaths(t *testing.T) []string` — returns `Makefile` followed by sorted `make/*.mk`. Used by every Makefile-reading test from here on.

- [ ] **Step 1: Write a test for the helper itself**

Add to `makefiletargets_audit_test.go`:

```go
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
```

- [ ] **Step 2: Run it and watch it fail**

```bash
go test . -run TestMakefileSourcePathsIncludesRootAndFamilies -count=1
```

Expected: FAIL to compile — `undefined: makefileSourcePaths`.

- [ ] **Step 3: Implement the helper**

```go
// makefileSourcePaths returns every file that contributes rules to the build:
// the root Makefile plus the family files it includes. Tests that audit the
// Makefile must read all of them — reading only the root would silently stop
// auditing most of the repo's targets the moment they moved into make/.
func makefileSourcePaths(t *testing.T) []string {
	t.Helper()
	paths := []string{"Makefile"}
	family, err := filepath.Glob("make/*.mk")
	if err != nil {
		t.Fatalf("globbing make/*.mk: %v", err)
	}
	sort.Strings(family)
	return append(paths, family...)
}
```

Add `"path/filepath"` to the imports.

- [ ] **Step 4: Rewire the three existing readers**

In `makefiletargets_audit_test.go`, change `makefilePhonyAndRuleNames` to loop over `makefileSourcePaths(t)` instead of reading `"Makefile"`, accumulating `phony` and `rules` across all files. Change `TestEveryLintTargetIsPhonyAndHasARule`'s `LINT_TARGETS` search to scan the same set. Replace the Task 1 placeholder `[]string{"Makefile"}` with `makefileSourcePaths(t)`.

In `makefile_audit_test.go:51` and `install_fuzz_test.go:25`, replace the single `os.ReadFile("Makefile")` with a loop over `makefileSourcePaths(t)`, concatenating the bodies with `"\n"`.

- [ ] **Step 5: Confirm everything still passes with no `make/` directory yet**

```bash
go test . -run 'TestMakefileSourcePaths|TestEveryPhonyTargetHasARule|TestEveryLintTargetIsPhonyAndHasARule|TestEveryRuleIsPhony|TestEveryRuleHasARecipe|TestNoMakefileRecipeFeedsVariableToRecursiveDelete' -count=1
```

Expected: PASS. The glob returns nothing, so behaviour is unchanged — that is the point.

- [ ] **Step 6: Commit**

```bash
git add makefiletargets_audit_test.go makefile_audit_test.go install_fuzz_test.go
git commit -m "test: read the Makefile file set, not just the root file

No behaviour change today: make/*.mk does not exist yet, so the glob is empty
and every audit sees exactly what it saw before. Landing this before the split
means the audits never have a window where they silently cover less."
```

---

## Task 3: Teach `parseMakefileTargets` to follow includes

This is product code. `agent/workspace_info.go` builds the workspace prompt an agent sees; after the split it would report variable names instead of targets, and because `len(targets) > 0` still holds it emits wrong content rather than none.

It is already partly broken: the guard `strings.Contains(line, "=") && !strings.Contains(line, ":")` does not skip `LDFLAGS := …`, because `:=` contains both characters. `strings.Index(line, ":")` then matches the colon of `:=` and yields the target `LDFLAGS`.

**Files:**
- Modify: `agent/workspace_info.go:252-315`
- Test: `agent/workspace_info_include_test.go` (create)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `parseMakefileTargets(path string) []string` — unchanged signature, now follows `include` and rejects assignments.

- [ ] **Step 1: Write the failing test**

Create `agent/workspace_info_include_test.go`:

```go
package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestParseMakefileTargetsFollowsIncludes pins the split-Makefile shape: rules
// live in make/*.mk and the root holds only variables and the include line. A
// parser that reads the root alone returns variable names here, which is worse
// than returning nothing — the caller checks len(targets) > 0 and publishes
// the garbage.
func TestParseMakefileTargetsFollowsIncludes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "make"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Makefile", "LDFLAGS := -X main.X=1\nGO_MODULES := . agent\n\n.DEFAULT_GOAL := build\n\ninclude $(dir $(lastword $(MAKEFILE_LIST)))make/*.mk\n")
	write("make/building.mk", ".PHONY: build\n\nbuild:\n\t@echo build\n")
	write("make/testing.mk", ".PHONY: test vet\n\ntest:\n\t@echo test\n\nvet:\n\t@echo vet\n")

	got := parseMakefileTargets(filepath.Join(root, "Makefile"))

	for _, want := range []string{"build", "test", "vet"} {
		if !slices.Contains(got, want) {
			t.Errorf("missing target %q; got %v", want, got)
		}
	}
	for _, unwanted := range []string{"LDFLAGS", "GO_MODULES"} {
		if slices.Contains(got, unwanted) {
			t.Errorf("variable %q reported as a target; got %v", unwanted, got)
		}
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

```bash
cd agent && go test . -run TestParseMakefileTargetsFollowsIncludes -count=1
```

Expected: FAIL — three missing targets and two variables reported as targets.

- [ ] **Step 3: Implement**

In `agent/workspace_info.go`, replace the assignment guard and add include following. The function keeps its signature; extract the per-file scan into a helper so the include recursion is one level and cannot loop:

```go
// makefileAssignment matches a variable assignment operator. `:=` and `::=`
// contain a colon, so a naive "has = but no :" test treats `LDFLAGS := x` as a
// rule named LDFLAGS.
var makefileAssignment = regexp.MustCompile(`^[^:=#]*(::?=|\+=|\?=)`)

// makefileInclude matches an include line, with or without a $(dir $(lastword
// $(MAKEFILE_LIST))) prefix, capturing the path or glob.
var makefileInclude = regexp.MustCompile(`^-?include\s+(?:\$\(dir \$\(lastword \$\(MAKEFILE_LIST\)\)\))?(\S+)`)
```

Change `parseMakefileTargets` to collect into a shared `seen`/`targets` pair via a new `appendMakefileTargets(path string, targets *[]string, seen map[string]bool, depth int)`. In the scan loop:

- Before the existing assignment check, `if makefileAssignment.MatchString(line) { continue }` and delete the old `Contains("=") && !Contains(":")` guard.
- After the recipe-line skip, handle includes:

```go
if m := makefileInclude.FindStringSubmatch(line); m != nil && depth == 0 {
	pattern := m[1]
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(filepath.Dir(path), pattern)
	}
	matches, _ := filepath.Glob(pattern)
	sort.Strings(matches)
	for _, inc := range matches {
		appendMakefileTargets(inc, targets, seen, depth+1)
	}
	continue
}
```

Add `"regexp"` and `"sort"` to the imports. Keep every existing skip (pattern rules, `.`-prefixed specials, multi-target lines) exactly as it is.

- [ ] **Step 4: Confirm it passes, and that nothing else regressed**

```bash
cd agent && go test . -run 'TestParseMakefileTargets|Workspace' -count=1
```

Expected: PASS, including `agent/workspace_test.go`, `agent/cov_s3_workspace_test.go` and `agent/fuzz_ar_workspace_test.go`.

- [ ] **Step 5: Check the real repo, not just fixtures**

```bash
cd agent && go test . -run TestParseMakefileTargetsFollowsIncludes -count=1 -v
```

Then sanity-check against the live Makefile with a scratch program or by adding a temporary `t.Log(parseMakefileTargets("../Makefile"))`. `LDFLAGS` and `EVENER_INSTALL_BINS` must no longer appear. Remove the temporary log before committing.

- [ ] **Step 6: Commit**

```bash
git add agent/workspace_info.go agent/workspace_info_include_test.go
git commit -m "fix(agent): follow Makefile includes and stop reporting variables as targets

parseMakefileTargets read one file and treated 'LDFLAGS := x' as a rule, because
the assignment guard tested for '=' without ':' and := has both. Splitting the
Makefile into make/*.mk would have reduced the workspace prompt to nothing but
those false targets."
```

---

## Task 4: Split the Makefile

**Files:**
- Create: `make/building.mk`, `make/testing.mk`, `make/linting.mk`, `make/fuzzing.mk`, `make/coverage.mk`, `make/repo.mk`
- Modify: `Makefile` (reduce to variables + define + default goal + include)
- Modify: `makefiletargets_audit_test.go` (add `TestRootMakefileHasNoRules`)

**Interfaces:**
- Consumes: `makefileSourcePaths` from Task 2.
- Produces: the six family files. Phase 3's generator maps stem → doc against exactly these names.

- [ ] **Step 1: Write the root-has-no-rules audit first**

```go
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
```

- [ ] **Step 2: Run it and watch it fail against today's Makefile**

```bash
go test . -run TestRootMakefileHasNoRules -count=1
```

Expected: FAIL, listing all 65 rules.

- [ ] **Step 3: Move the rules, family by family**

Move each rule **verbatim** — recipe, preceding `#` comments, and blank-line spacing — into its family file per the spec's §1 table. Each family file opens with its own `.PHONY` line naming exactly its own targets.

Move the family-local variables too: `LDFLAGS`, `PREFIX`, `BINDIR`, `EVENER_SHARE_BINDIR`, `INSTALL_BUILD_DIR`, `EVENER_INSTALL_BINS`, `BUILD_CHANNEL` → `make/building.mk`; `LINT_TARGETS` → `make/linting.mk`; `FUZZ_SEED_REPLAY`, `FUZZ_GOWORK` → `make/fuzzing.mk`.

`make/repo.mk` gets `clean`, `generate`, `tools`, `refresh-model-catalog`. `help` is added in Phase 3, not now.

The root Makefile ends as:

```make
GO_MODULES := . agent llm auth envvars invariant identifier
FUZZ_GO_MODULES := $(GO_MODULES) fuzz
DEV_TOOLING_TEST_SCRIPTS := <unchanged>

define run_quiet_lint
<unchanged>
endef

.DEFAULT_GOAL := build

include $(dir $(lastword $(MAKEFILE_LIST)))make/*.mk
```

`.DEFAULT_GOAL` is mandatory: `build` is the default today only because it is the first rule in the file, and after the split "first rule wins" would resolve against alphabetical glob order.

- [ ] **Step 4: Verify make itself, before the tests**

```bash
/usr/bin/make --version | head -1     # confirm GNU Make 3.81
/usr/bin/make -n build | head -5      # default goal still build
/usr/bin/make -n lint  | head -5
cd /tmp && /usr/bin/make -f "$OLDPWD/Makefile" -n build | head -3
```

The last command is the `merge-approval-gate-selftest.sh` shape. Expected: it works. If it reports `make/*.mk: No such file or directory`, the include is not anchored — fix it before continuing.

- [ ] **Step 5: Run every Makefile audit**

```bash
go test . -run 'TestEveryPhonyTargetHasARule|TestEveryLintTargetIsPhonyAndHasARule|TestEveryRuleIsPhony|TestEveryRuleHasARecipe|TestRootMakefileHasNoRules|TestNoMakefileRecipeFeedsVariableToRecursiveDelete|TestMakefileSourcePaths' -count=1
```

Expected: PASS, all seven.

- [ ] **Step 6: Prove the rewritten audits did not get weaker**

Three planted regressions, each reverted immediately. Deleting a *recipe body* is **not** a valid plant for the first test — the parser records a rule from its `name:` line and never inspects the recipe.

```bash
# Plant 1: delete a whole rule line.
cp make/linting.mk /tmp/linting.mk.bak
grep -n '^lint-naming:' make/linting.mk       # note the line number, then delete it
sed -i '' '<N>d' make/linting.mk
go test . -run TestEveryPhonyTargetHasARule -count=1   # MUST FAIL naming lint-naming
cp /tmp/linting.mk.bak make/linting.mk

# Plant 2: empty a recipe body.
sed -i '' '/^lint-naming:/,+1{/^\t/d;}' make/linting.mk
go test . -run TestEveryRuleHasARecipe -count=1        # MUST FAIL naming lint-naming
cp /tmp/linting.mk.bak make/linting.mk

# Plant 3: remove a .PHONY entry.
sed -i '' 's/^\.PHONY: lint lint-naming/.PHONY: lint/' make/linting.mk
go test . -run TestEveryRuleIsPhony -count=1           # MUST FAIL naming lint-naming
cp /tmp/linting.mk.bak make/linting.mk && rm /tmp/linting.mk.bak
```

If any plant does **not** fail, the audit is weaker than before the split. Stop and fix it.

- [ ] **Step 7: Full gate**

```bash
export GOLANGCI_LINT_CACHE=/private/tmp/claude-501/-Users-jesse-git-prime-radiant-evener/422cd0dd-aa5c-42ce-b71e-4a1c3b90a6be/scratchpad/wt-glcache
make lint && make test && make test-dev-tooling
```

Expected: all green. `make test-dev-tooling` exercises `merge-approval-gate-selftest.sh`, which is the real check on the anchored include.

- [ ] **Step 8: Commit**

```bash
git add Makefile make/ makefiletargets_audit_test.go
git commit -m "make: split the Makefile into six family files under make/

Recipes move verbatim; no target behaviour changes. The include is anchored to
MAKEFILE_LIST rather than the cwd, because merge-approval-gate-selftest.sh runs
make -f <abs>/Makefile from a fixture directory where a relative include cannot
resolve. .DEFAULT_GOAL becomes explicit: build was the default only by virtue of
being the first rule, which after the split would be an accident of glob order."
```

---

## Task 5: Fix the fixture consumers

**Files:**
- Modify: `install_test.go:43-49`
- Modify: `runtime_pair_build_test.go:883`

**Interfaces:**
- Consumes: `make/*.mk` from Task 4.

- [ ] **Step 1: Run the fixture tests and watch them fail**

```bash
go test . -run 'TestInstall|BuildWeb' -count=1
```

Expected: FAIL with `make/*.mk: No such file or directory` — these fixtures copy `Makefile` into a temp root and run `make` there.

- [ ] **Step 2: Copy the family files alongside the root file**

In `install_test.go`, after the existing `os.WriteFile(filepath.Join(fixtureRoot, "Makefile"), ...)`:

```go
	// The root Makefile is only half the build now: every rule lives in
	// make/*.mk. A fixture with the root file alone fails at include time.
	if err := os.MkdirAll(filepath.Join(fixtureRoot, "make"), 0o755); err != nil {
		t.Fatalf("mkdir make: %v", err)
	}
	family, err := filepath.Glob(filepath.Join(repoRoot, "make", "*.mk"))
	if err != nil {
		t.Fatalf("globbing make/*.mk: %v", err)
	}
	if len(family) == 0 {
		t.Fatal("no make/*.mk found; the fixture would silently test a different Makefile")
	}
	for _, src := range family {
		copyRepositoryFile(t, repoRoot, fixtureRoot, filepath.Join("make", filepath.Base(src)), 0o644)
	}
```

Apply the same block in `runtime_pair_build_test.go`'s `newBuildWebFixture`, using `fixture.repoRoot` and `fixture.root`.

The `len(family) == 0` guard matters: without it, a future rename of the directory would leave the fixture copying nothing and testing a Makefile that no longer matches the repo's.

- [ ] **Step 3: Confirm they pass**

```bash
go test . -run 'TestInstall|BuildWeb' -count=1
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add install_test.go runtime_pair_build_test.go
git commit -m "test: copy make/*.mk into the Makefile fixtures

Both fixtures copy the root Makefile into a temp root and run make there. After
the split that root file is an include line and nothing else."
```

---

# Phase 2 — Documentation move

The Canonical Gate Matrix stays intact through this whole phase. It is dissolved in Task 14, in the same commit that generates its replacement, so the repo is never without a gate reference.

## Task 6: Create the directory and move the docs that keep their content

**Files:**
- Create: `docs/developing-evener/README.md`
- Move (via `git mv`): `docs/dev-checklist.md`, `docs/agentic-testing.md`, `docs/agent-test-serial-prefix.md`, `docs/performance-profiling.md`, `docs/environment.md`, `docs/worktrees.md`, `docs/conventions/`
- Modify: `docs/environment.md:83` (outbound link)
- Modify: `scenariosourcecite_audit_test.go:214`

**Interfaces:**
- Produces: `docs/developing-evener/` containing seven moved entries plus a README index.

- [ ] **Step 1: Move the seven entries**

```bash
mkdir -p docs/developing-evener
git mv docs/dev-checklist.md docs/agentic-testing.md docs/agent-test-serial-prefix.md \
       docs/performance-profiling.md docs/environment.md docs/worktrees.md \
       docs/conventions docs/developing-evener/
```

`docs/testing.md` and `docs/fuzzing.md` are **not** moved here — they are split in Tasks 7-9.

- [ ] **Step 2: Fix the one outbound link in the moved set**

`docs/developing-evener/environment.md:83` links to `](sandboxing.md#caches-are-contained-never-poisoned)`. `sandboxing.md` stays at `docs/`, so it becomes `](../sandboxing.md#caches-are-contained-never-poisoned)`.

- [ ] **Step 3: Unbreak the scenario audit**

`scenariosourcecite_audit_test.go:214` hardcodes the path and `:75` fatals on a read error, so the move breaks the test outright:

```go
	files = append(files, "docs/developing-evener/agentic-testing.md")
```

- [ ] **Step 4: Sweep the live inbound references**

```bash
for old in dev-checklist agentic-testing agent-test-serial-prefix performance-profiling environment worktrees; do
  rg -l "docs/$old\.md" --hidden -g '!.git' . \
    | rg -v 'docs/(superpowers|design)/(plans|notes|proofs|specs)/' \
    | xargs -r sed -i '' "s|docs/$old\.md|docs/developing-evener/$old.md|g"
done
rg -l "docs/conventions/" --hidden -g '!.git' . \
  | rg -v 'docs/(superpowers|design)/(plans|notes|proofs|specs)/' \
  | xargs -r sed -i '' "s|docs/conventions/|docs/developing-evener/conventions/|g"
```

Dated plans, notes and proofs are deliberately left pointing at the old paths (spec decision 6).

- [ ] **Step 5: Write the README index**

`docs/developing-evener/README.md` — a short index naming each doc and one line on what it covers. Its own `make targets` region is added in Phase 3; for now it ends with a `## Targets` heading and the two marker comments with nothing between them:

```markdown
## Targets

<!-- BEGIN GENERATED: make targets. Edit make/repo.mk, then run `make generate`. -->
<!-- END GENERATED -->
```

- [ ] **Step 6: Verify no live reference or relative link is broken**

```bash
go test . -run TestScenario -count=1
rg -n 'docs/(dev-checklist|agentic-testing|agent-test-serial-prefix|performance-profiling|environment|worktrees)\.md' --hidden -g '!.git' . \
  | rg -v 'docs/(superpowers|design)/(plans|notes|proofs|specs)/'
```

Expected: the test passes and the second command prints nothing.

- [ ] **Step 7: Commit**

```bash
git add -A docs/ scenariosourcecite_audit_test.go
git status   # confirm nothing unexpected is staged
git commit -m "docs: move the dev-facing docs under docs/developing-evener/

Seven entries move with their content unchanged. Live references are updated;
references inside dated plans, notes and proofs are left pointing at the old
paths, matching the precedent in 2026-08-19-infra-standardization-design.md.
scenariosourcecite_audit_test.go hardcoded agentic-testing.md and fatals on a
read error, so it moves with them."
```

---

## Task 7: Split `docs/testing.md`

**Files:**
- Create: `docs/developing-evener/building.md`, `docs/developing-evener/linting.md`, `docs/developing-evener/coverage.md`
- Create: `docs/developing-evener/testing.md` (from the surviving sections)
- Delete: `docs/testing.md`

**Interfaces:**
- Produces: four docs, each ending with an empty `BEGIN GENERATED` / `END GENERATED` marker pair under a `## Targets` heading.

- [ ] **Step 1: Route every section per the spec's §6 table**

The spec maps all eighteen `##` sections. Two rules that are easy to get wrong:

- `### Frontend setup boundary` (`:233`) is an H3 **inside** `## Post-Merge Gate` (`:173`). It stays with its parent in `testing.md`; `building.md` links to it rather than moving it out.
- `## The seqfuzz/schemafuzz Family Lives Only in make test-fuzz` (`:291`) and `## Proving a Type Survives a Round Trip` (`:400`) go to `fuzzing.md`, which Task 8 creates. Hold them aside now and hand them over in Task 8.

- [ ] **Step 2: Give `coverage.md` sole ownership of the two-track explanation**

`docs/testing.md:344-397` is the load-bearing coverage prose: the two tracks, why a default-gate `-cover` number is neither whole-repo coverage nor "how well is this tested", the `-run '^(Test|Example)'` filter hiding every `Fuzz*BehaviorProgram` target, and the EXECUTED-vs-TESTED distinction for `cmd/evener-hub/cov_*_test.go`. It moves to `coverage.md` **whole**. `fuzzing.md` links to it and must not restate it — duplicating it across two files is how it goes out of sync.

- [ ] **Step 3: Add the marker pair to each new doc**

Every one of the four gets a `## Targets` section with the two marker comments and nothing between them, naming its own family file (`make/building.mk`, `make/testing.mk`, `make/linting.mk`, `make/coverage.mk`).

- [ ] **Step 4: Move the gate matrix to `testing.md` untouched**

Copy `## Canonical Gate Matrix` (`:140-171`) into the new `testing.md` verbatim. It is dissolved in Task 14, not here.

- [ ] **Step 5: Check nothing was dropped**

```bash
git show HEAD:docs/testing.md | grep -c '^## '     # 18
cat docs/developing-evener/{testing,building,linting,coverage}.md | grep -c '^## '
```

The second count is the first plus the new `## Targets` headings, minus the two sections held for Task 8. Reconcile any difference before moving on — a silently dropped section is the main risk in this task.

- [ ] **Step 6: Commit**

```bash
git add -A docs/
git commit -m "docs: split testing.md into testing, building, linting and coverage

coverage.md takes sole ownership of the two-track explanation; the Frontend
setup boundary subsection stays with its parent Post-Merge Gate section rather
than being split away from it. The gate matrix is copied over unchanged and is
dissolved later, once there is generated output to replace it."
```

---

## Task 8: Build `fuzzing.md`

**Files:**
- Create: `docs/developing-evener/fuzzing.md` (from `docs/fuzzing.md` plus the two held sections)
- Delete: `docs/fuzzing.md`

- [ ] **Step 1: Move and merge**

```bash
git mv docs/fuzzing.md docs/developing-evener/fuzzing.md
```

Then append the two sections held from Task 7 (`The seqfuzz/schemafuzz Family…`, `Proving a Type Survives a Round Trip`).

- [ ] **Step 2: Fix all six outbound links**

`fuzzing.md` carries six relative links, each needing one more `../`:

| Line | Before | After |
| --- | --- | --- |
| 12, 100, 275 | `](../fuzz/README.md)` | `](../../fuzz/README.md)` |
| 13, 149 | `](skills/fuzzing-an-api-surface/SKILL.md)` | `](../skills/fuzzing-an-api-surface/SKILL.md)` |
| 14 | `](design/fuzzing-toolkit-design.md)` | `](../design/fuzzing-toolkit-design.md)` |

- [ ] **Step 3: Add the marker pair and the cross-link to coverage**

Add the `## Targets` section with markers naming `make/fuzzing.mk`. Where the moved seqfuzz section touches the two-track idea, replace the explanation with a link to `coverage.md`.

- [ ] **Step 4: Sweep inbound references and verify every relative link resolves**

```bash
rg -l "docs/fuzzing\.md" --hidden -g '!.git' . \
  | rg -v 'docs/(superpowers|design)/(plans|notes|proofs|specs)/' \
  | xargs -r sed -i '' 's|docs/fuzzing\.md|docs/developing-evener/fuzzing.md|g'

# Every relative markdown link in the new directory must point at a real file.
for f in docs/developing-evener/*.md; do
  grep -oE '\]\(([^)#]+)' "$f" | sed 's/^](//' | while read -r target; do
    case "$target" in http*|\#*) continue;; esac
    [ -e "docs/developing-evener/$target" ] || echo "BROKEN: $f -> $target"
  done
done
```

Expected: the loop prints nothing.

- [ ] **Step 5: Commit**

```bash
git add -A docs/
git commit -m "docs: move fuzzing.md into docs/developing-evener/ and absorb two sections

Six relative links each gain a ../ level; they were invisible to an
inbound-only link check. The two-track coverage explanation stays in
coverage.md and is linked, not restated."
```

---

## Task 9: Sweep `docs/testing.md`'s remaining inbound references

130 files referenced `docs/testing.md`, 91 of them dated plans that stay as they are. The other 39 need routing to whichever of the four new docs now holds the thing they were pointing at.

**Files:**
- Modify: ~39 files across `agent/`, `cmd/`, `scripts/`, `test/`, `internal/`, `tools/`, `fuzz/README.md`, `AGENTS.md`, `Makefile`, `testing-budget.json`

- [ ] **Step 1: List what still points at the old path**

```bash
rg -n "docs/testing\.md" --hidden -g '!.git' . \
  | rg -v 'docs/(superpowers|design)/(plans|notes|proofs|specs)/'
```

- [ ] **Step 2: Route each by what it cites**

This cannot be a blind `sed`: a reference to the gate matrix goes to `testing.md`, one about coverage floors goes to `coverage.md`, one about lint passes goes to `linting.md`. Read the surrounding line in each case.

- [ ] **Step 3: Verify**

```bash
rg -n "docs/testing\.md" --hidden -g '!.git' . \
  | rg -v 'docs/(superpowers|design)/(plans|notes|proofs|specs)/'
```

Expected: no output.

- [ ] **Step 4: Full gate**

```bash
export GOLANGCI_LINT_CACHE=/private/tmp/claude-501/-Users-jesse-git-prime-radiant-evener/422cd0dd-aa5c-42ce-b71e-4a1c3b90a6be/scratchpad/wt-glcache
make lint && make test && make test-dev-tooling
```

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "docs: route the remaining live testing.md references to their new homes

Each reference goes to whichever of the four docs now holds the material it
cites, so this is a read-and-route pass rather than a path substitution."
```

---

# Phase 3 — Annotations and the generator

## Task 10: The annotation parser

**Files:**
- Create: `internal/maketargetsdoc/parse.go`
- Test: `internal/maketargetsdoc/parse_test.go`

**Interfaces:**
- Produces:
  ```go
  type Target struct {
      Name       string
      Summary    string
      Proves     string
      Trigger    string
      Requires   string
      FailsWhen  string
  }
  func ParseFamily(src []byte) ([]Target, error)
  ```
  `ParseFamily` returns targets in file order. Tasks 11 and 12 both consume it.

- [ ] **Step 1: Write the failing tests**

Create `internal/maketargetsdoc/parse_test.go` with table cases covering: a summary-only target; a target with all four fields; a continuation line joining into a field; a multi-line summary; an unknown key (error); a `##` block above a target-specific variable line such as `install-home: PREFIX := $(HOME)/.local` (error); a `##` block separated from its rule by an intervening non-comment line (error); and a rule with no `##` block at all (error).

```go
func TestParseFamilySummaryOnly(t *testing.T) {
	got, err := ParseFamily([]byte("## Remove the built binaries.\nclean:\n\trm -f evener\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "clean" || got[0].Summary != "Remove the built binaries." {
		t.Fatalf("got %+v", got)
	}
}

func TestParseFamilyUnknownKeyIsAnError(t *testing.T) {
	_, err := ParseFamily([]byte("## Lint.\n## trigers: always\nlint:\n\t@true\n"))
	if err == nil {
		t.Fatal("expected an error for the misspelled key 'trigers'")
	}
}

func TestParseFamilyRejectsBlockAboveTargetSpecificVariable(t *testing.T) {
	src := "## Install into the home prefix.\ninstall-home: PREFIX := $(HOME)/.local\ninstall-home: install\n\t@true\n"
	if _, err := ParseFamily([]byte(src)); err == nil {
		t.Fatal("expected an error: the block sits above a variable assignment, not the rule")
	}
}
```

- [ ] **Step 2: Run and watch them fail**

```bash
cd internal/maketargetsdoc && go test ./... -count=1
```

Expected: FAIL to build — package does not exist.

- [ ] **Step 3: Implement `ParseFamily`**

Scan line by line. Accumulate a pending `##` block. On a column-zero `name:` line: if the remainder after the colon matches an assignment operator, error if a block is pending. Otherwise attach the block, error if it is empty. Any non-`##`, non-blank line clears a pending block and errors if one was pending. Reject unknown keys.

- [ ] **Step 4: Confirm all cases pass**

```bash
cd internal/maketargetsdoc && go test ./... -count=1 -v
```

- [ ] **Step 5: Commit**

```bash
git add internal/maketargetsdoc/
git commit -m "feat(maketargetsdoc): parse ## target annotations

Errors rather than guessing on the three shapes that would otherwise publish
something wrong: an unknown key, a block above a target-specific variable line,
and a block separated from its rule."
```

---

## Task 11: Render and rewrite the marked regions

**Files:**
- Create: `internal/maketargetsdoc/render.go`, `internal/maketargetsdoc/main.go`
- Test: `internal/maketargetsdoc/render_test.go`
- Create: `internal/maketargetsdoc/doc.go` (holds the `//go:generate` directive)

**Interfaces:**
- Consumes: `ParseFamily` from Task 10.
- Produces: `func Render(targets []Target) string` and `func RewriteRegion(doc []byte, family string, body string) ([]byte, error)`.

- [ ] **Step 1: Write the failing render tests**

Cover: targets with fields render as the wide table; summary-only targets render as the `Other targets` compact list; a family where **no** target has fields emits the compact list and **no** empty wide table; `RewriteRegion` replaces only between the markers and leaves surrounding prose byte-identical; a missing marker pair is an error; an unterminated region is an error.

- [ ] **Step 2: Run and watch them fail**

```bash
cd internal/maketargetsdoc && go test ./... -run Render -count=1
```

- [ ] **Step 3: Implement**

`main.go` walks `make/*.mk`, maps stem → `docs/developing-evener/<stem>.md` (with `repo` → `README.md`), parses, renders and rewrites. A `.mk` with no destination doc is an error, as is a doc whose region has no matching `.mk`.

- [ ] **Step 4: Confirm**

```bash
cd internal/maketargetsdoc && go test ./... -count=1
```

- [ ] **Step 5: Commit**

```bash
git add internal/maketargetsdoc/
git commit -m "feat(maketargetsdoc): render target tables into marked doc regions"
```

---

## Task 12: `make help`

**Files:**
- Modify: `internal/maketargetsdoc/main.go` (add `-mode help`)
- Modify: `make/repo.mk` (add the `help` target)
- Test: `internal/maketargetsdoc/help_test.go`

- [ ] **Step 1: Write the failing test** — `help` output groups by family and prints one line per target.
- [ ] **Step 2: Run and watch it fail.**
- [ ] **Step 3: Implement `-mode help`,** reusing `ParseFamily` so there is exactly one annotation parser.
- [ ] **Step 4: Add the target** to `make/repo.mk` with its own `##` summary, and to that file's `.PHONY`.
- [ ] **Step 5: Confirm** `make help` prints all 66 targets and `make` with no argument still builds.
- [ ] **Step 6: Commit.**

---

## Task 13: Annotate all 66 targets

One step per family so a reviewer can reject one family's prose without rejecting the rest.

**Files:** all six `make/*.mk`

- [ ] **Step 1: `make/repo.mk`** (5 targets). Note `clean` removes the built binaries from the repo root — it does **not** touch `.build`; check the recipe before writing the summary.
- [ ] **Step 2: `make/coverage.mk`** (5 targets).
- [ ] **Step 3: `make/building.mk`** (17 targets).
- [ ] **Step 4: `make/testing.mk`** (11 targets).
- [ ] **Step 5: `make/linting.mk`** (10 targets). Move `lint-evenerfuzz`'s 19-line comment into `linting.md` prose; the `##` block gets the table-cell version only.
- [ ] **Step 6: `make/fuzzing.mk`** (18 targets).
- [ ] **Step 7: Regenerate and eyeball** — `go run ./internal/maketargetsdoc && git diff docs/`.
- [ ] **Step 8: Commit** each family separately if the review cycle wants it; otherwise one commit.

---

## Task 14: Wire the gate, add the annotation audit, dissolve the matrix

This is the commit where the matrix goes, because this is the first commit where generated output exists to replace it.

**Files:**
- Modify: `make/repo.mk` (`generate`), `make/linting.mk` (`lint-generated`)
- Modify: `appwire/doc.go` or `internal/maketargetsdoc/doc.go` (`//go:generate`)
- Modify: `makefiletargets_audit_test.go` (add `TestEveryTargetHasASummaryAnnotation`; update the gate-matrix reference in its comment at `:85-86`)
- Modify: `docs/developing-evener/testing.md` (remove the matrix, add "Gates that are not make targets")

- [ ] **Step 1: Write the annotation audit** — every rule in `makefileSourcePaths(t)` has a `##` summary, keyed on rules, no exemptions.
- [ ] **Step 2: Run it** — it should pass, since Task 13 annotated everything. Then delete one `##` summary and confirm it fails. Restore.
- [ ] **Step 3: Rewire `lint-generated` to call `$(MAKE) generate`.**

```make
lint-generated:
	$(call run_quiet_lint,$(MAKE) generate && { git diff --exit-code -- docs/appwire-protocol.md cmd/evener-hub/frontend/src/protocol/types.gen.ts docs/developing-evener/README.md docs/developing-evener/building.md docs/developing-evener/testing.md docs/developing-evener/linting.md docs/developing-evener/fuzzing.md docs/developing-evener/coverage.md || { echo "generated outputs are stale; run 'make generate' and commit."; exit 1; }; })
```

The recipe previously carried its own copy of `go generate ./appwire/...`. Adding doc paths without widening what gets regenerated would have produced a gate that is green forever — the exact `lint-fuzz-registry` failure this whole change exists to prevent.

- [ ] **Step 4: Prove the gate can fail.**

```bash
export GOLANGCI_LINT_CACHE=/private/tmp/claude-501/-Users-jesse-git-prime-radiant-evener/422cd0dd-aa5c-42ce-b71e-4a1c3b90a6be/scratchpad/wt-glcache
make generate && git diff --exit-code    # clean
sed -i '' '0,/^## /s/^## .*/## Deliberately wrong summary./' make/coverage.mk
make lint                                 # MUST fail in lint-generated
git checkout -- make/coverage.mk && make generate
```

- [ ] **Step 5: Dissolve the matrix.** Delete `## Canonical Gate Matrix` from `testing.md`. Its five non-target rows become a hand-written `## Gates that are not make targets` section: `scripts/web/web-preflight.sh`, `ROOT_FULL=1 make test`, the `EVENER_LIVE_TESTS=1` opt-in block, the `EVENER_E2E_LIVE=1 scripts/coverage/e2e-cover.sh` block, and the "Launcher health checks … outside this repository's gates" row. Cells too large for a table — `make test-web-browser`'s determinism cell runs to several hundred words — become prose in the owning family doc, with the generated row carrying the one-line version. **This is the least mechanical work in the plan; do not rush it.**

- [ ] **Step 6: Full gate, then commit.**

```bash
make lint && make test && make test-dev-tooling
git add -A
git commit -m "make: generate the target reference and retire the gate matrix

lint-generated now calls \$(MAKE) generate instead of carrying its own copy of
go generate, so the six doc regions it diffs are actually regenerated first.
The Canonical Gate Matrix is replaced by generated per-family tables; its five
non-target rows become prose."
```

---

## Task 15: Final sweep

- [ ] **Step 1: Confirm every target is discoverable** — `make help | wc -l` covers 66 targets; the five originally-undocumented ones (`mutation-floor`, `fuzz-drive`, `coverage-gaps`, `coverage-gaps-selftest`, `test-install`) each appear in a doc.
- [ ] **Step 2: Confirm no relative link in `docs/developing-evener/` is broken** — rerun the loop from Task 8 Step 4.
- [ ] **Step 3: Full gate from a clean cache** — `golangci-lint cache clean` is **not** safe here; instead delete only the worktree-local cache directory and rerun `make lint`.
- [ ] **Step 4: Update `AGENTS.md`** if it points at any moved doc.
- [ ] **Step 5: Commit.**

---

## Self-review notes

**Spec coverage.** Every numbered change in the spec maps to a task: §1 → Tasks 4-5, §2 → Tasks 10, 13, §3 → Tasks 11, 14, §4 → Task 12, §5 → Tasks 1-3, 5, 14, §6 → Tasks 6-8, §7 → Tasks 6, 8, 9. All five audits named in the spec are created (Tasks 1, 4, 14).

**One deviation from the spec, deliberate.** The spec's commit plan dissolves the gate matrix in commit 2 and generates its replacement in commit 3, which leaves the repo with no gate reference in between. This plan keeps the matrix intact through Phase 2 and dissolves it in Task 14, in the same commit that generates the tables. Fold this back into the spec.

**Known soft spot.** Task 9 ("route ~39 references by what they cite") and Task 14 Step 5 (rewriting oversized matrix cells as prose) are the two steps that cannot be mechanised and have no test that proves them right. They are the most likely to need a second pass.
