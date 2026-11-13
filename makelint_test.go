package evener_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"primeradiant.com/evener/internal/devtool/repoaudit"
)

// makefileRecipes is repoaudit.MakefileRecipes for a test.
func makefileRecipes(t *testing.T) map[string]string {
	t.Helper()
	recipes, err := repoaudit.MakefileRecipes()
	if err != nil {
		t.Fatal(err)
	}
	return recipes
}

// lintTargets is repoaudit.LintTargets for a test.
func lintTargets(t *testing.T) []string {
	t.Helper()
	targets, err := repoaudit.LintTargets()
	if err != nil {
		t.Fatal(err)
	}
	return targets
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
	"lint-naming":     "./evener-dev tomlcheck",
	"lint-gofmt":      "/bin/gofmt\" -l",
	"lint-evenerfuzz": "-tags evenerfuzz",
	"lint-eval":       "-tags eval ",
	"lint-internal":   "./evener-dev internalcheck",
	// The merged sweep runs golangci-lint directly from the repo root, so
	// the darwin cross-pass — the only GOOS=darwin invocation in any gate —
	// is what distinguishes this recipe.
	"lint-golangci":           "GOOS=darwin golangci-lint run",
	"lint-generated":          "docs/appwire-protocol.md",
	"lint-fuzz-registry":      "scripts/fuzz/fuzz-registry-check.sh",
	"lint-package-imports":    "scripts/sdk/package-import-paths-check.sh",
	"lint-go-module-consumer": "scripts/gate/go-module-consumer-check.sh",
	"lint-js-toolchain-pins":  "scripts/sdk/js-toolchain-pins-check.mjs",
	"lint-tracked-file-size":  "scripts/ops/tracked-file-size-check.sh",
	"lint-repo-audit":         "./evener-dev dev repo-audit",
	"secret-scan":             "scripts/ops/gitleaks-scan.sh repo",
}

// makeTargetsDryRun runs `make -n -k <targets...>` from the repository root
// and returns what it printed. It is the one audit input in this file that
// comes from make itself rather than from Makefile text: every other
// assertion here is a textual proxy, and a proxy cannot tell whether `lint`
// still depends on the list it reads.
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
// output at lint-generated and every gate after it reads as missing. For a
// single .PHONY lane target, -k is inert.
//
// Empty output is the vacuity failure. It means make never reached the point
// of printing a recipe, so every assertion built on the output would hold
// against nothing; it fails loudly here instead.
func makeTargetsDryRun(t *testing.T, makePath string, targets ...string) string {
	t.Helper()
	cmd := exec.Command(makePath, append([]string{"-n", "-k"}, targets...)...)
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
		t.Fatalf("`%s -n -k %s` printed no recipe lines (%v), so this audit has no subject and "+
			"would hold against nothing. That is a broken Makefile, not a clean one.\nstderr:\n%s",
			makePath, strings.Join(targets, " "), runErr, stderr.String())
	}
	return stdout.String()
}

// TestMakeLintRunsEveryGateInLintTargets asks make what `make lint` actually
// expands to.
//
// Every Makefile audit in `make lint-repo-audit` reads Makefile TEXT. That is a proxy
// for what make does, and it has a blind spot exactly where this branch's
// original defect lives: nothing in the text says `lint`'s prerequisite list is
// still `$(LINT_TARGETS)`. Rewrite the rule as `lint: lint-naming` and the gate
// drops from nine checks to one with every textual audit still green, while
// LINT_TARGETS goes on being validated as a list nothing expands. Move a gate's
// rule into another family file and drop it from LINT_TARGETS and
// everyLintingRuleIsInLintTargets stops seeing it too, because that audit
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

	targets := lintTargets(t)
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

	dryRun := makeTargetsDryRun(t, makePath, "lint")
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
// everyGeneratedRegionIsInTheStalenessDiff covers the other half — that the
// diff names every doc carrying a generated region. It checks that the recipe
// contains each path as text, which stays true whatever the recipe does with
// them, so on its own it cannot tell a working gate from a hollow one.
func TestMakeLintGeneratedRegeneratesAndDiffsHEAD(t *testing.T) {
	t.Parallel()
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skipf("make is not on PATH (%v), so nothing can ask it what `make lint` expands to", err)
	}
	dryRun := makeTargetsDryRun(t, makePath, "lint")

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

// mergedSweepModules is the hand-maintained module list as package patterns
// from the repository root: the root module as ./... and each sibling module
// directory as ./<dir>/... . It is deliberately written out rather than read
// from go.work, for the reason lintGateCommands gives about LINT_TARGETS: a
// derived oracle could not see a module dropped from the Makefile's
// FUZZ_GO_MODULES. Its guarantee is therefore about that list, not about
// go.work — a module added to go.work's use list but not to GO_MODULES stays
// invisible to this test.
var mergedSweepModules = []string{
	"./...", "./agent/...", "./llm/...", "./auth/...", "./envvars/...",
	"./invariant/...", "./identifier/...", "./execsupport/...", "./fuzz/...",
}

// TestLintSweepsAreMergedPerBuildRegime asks make what each heavy lint lane
// expands to and asserts the merged shape: one invocation per build regime
// (host, GOOS=darwin, GOOS=windows, each build tag) covering every workspace
// module, instead of the old one-invocation-per-module loops.
//
// The shape is what this pins, not the speed: golangci-lint compiles only the
// files its GOOS selects, so the host and darwin sweeps can never be one pass —
// each regime needs its own invocation. What CAN be merged is the module loop
// inside a regime, because go.work resolves cross-module package patterns from
// the root. Collapsing 9+ invocations per regime into 1 keeps the per-module
// runner (cmd/evener-dev module-lint) and its LINT_PARALLEL knob from
// shadowing golangci's own concurrency.
//
// The assertions run against `make -n` output rather than recipe text for the
// reason makeTargetsDryRun gives: run_quiet_lint wraps recipes in `if ( … )`, so
// a textual read of make/linting.mk sees the loop bodies, not what runs. Each
// gate is dry-run alone (-k not needed: the wrapper prints its recipe even
// when an earlier prerequisite would fail), and the dry run prints the recipe
// once per lane.
func TestLintSweepsAreMergedPerBuildRegime(t *testing.T) {
	t.Parallel()
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skipf("make is not on PATH (%v), so nothing can ask make what the lint lanes expand to", err)
	}

	// The three lanes are .PHONY with no prerequisites at HEAD, so -n
	// prints every recipe line without executing anything; a prerequisite
	// added later must stay .PHONY or -n would run its recipe.
	dryRunOf := func(target string) string {
		t.Helper()
		return makeTargetsDryRun(t, makePath, target)
	}

	// Each lane's cross-GOOS regimes are what the linux-drop regression of
	// the first merge commit proved nobody else pins: every gate stayed green
	// while lint-evenerfuzz lost the GOOS=linux passes that two evenerfuzz
	// && linux sources depend on. CI runs on Linux only, where those
	// branches never execute, so a repeat deletion ships silently without
	// this table. The invocation counts pin one-invocation-per-regime: a
	// revert to per-module loops multiplies the counts, and a dropped
	// regime subtracts from them.
	laneRegimes := map[string]struct {
		vetInvocations   []string
		golangciRuns     []string
		golangciRunCount int
	}{
		"lint-evenerfuzz": {
			vetInvocations: []string{
				"go vet -tags evenerfuzz",
				"GOOS=windows go vet -tags evenerfuzz",
				"GOOS=linux go vet -tags evenerfuzz",
			},
			golangciRuns: []string{
				"golangci-lint run --allow-parallel-runners --build-tags evenerfuzz --enable-only tagliatelle",
			},
			golangciRunCount: 2, // the host pass plus the GOOS=linux pass
		},
		"lint-eval": {
			vetInvocations: []string{
				"go vet -tags eval",
				"GOOS=linux go vet -tags eval",
			},
			golangciRuns: []string{
				"golangci-lint run --allow-parallel-runners --build-tags eval --enable-only tagliatelle",
			},
			golangciRunCount: 2, // the host pass plus the GOOS=linux pass
		},
		"lint-golangci": {
			golangciRuns: []string{
				"golangci-lint run --allow-parallel-runners",
				"GOOS=darwin golangci-lint run --allow-parallel-runners",
				"golangci-lint run --allow-parallel-runners --config .golangci-appwire.yml",
			},
			golangciRunCount: 3, // host + darwin + appwire
		},
	}

	for _, lane := range []string{"lint-evenerfuzz", "lint-eval", "lint-golangci"} {
		dryRun := dryRunOf(lane)
		regimes := laneRegimes[lane]

		for _, invocation := range append(regimes.vetInvocations, regimes.golangciRuns...) {
			if !strings.Contains(dryRun, invocation) {
				t.Errorf("%s lost the invocation %q; the merged lane must run every build regime it covers", lane, invocation)
			}
		}
		if got := strings.Count(dryRun, "golangci-lint run"); got != regimes.golangciRunCount {
			t.Errorf("%s runs %d golangci-lint invocations, want %d: one per build regime. A higher count means a per-module loop returned; a lower one means a regime was dropped.", lane, got, regimes.golangciRunCount)
		}

		// (a) every workspace module appears in the lane's sweep patterns.
		for _, pattern := range mergedSweepModules {
			if !strings.Contains(dryRun, pattern) {
				t.Errorf("%s does not sweep %s; the merged recipe must name every go.work module in each regime's invocation", lane, pattern)
			}
		}

		// (b) every golangci-lint invocation in the lane runs under the
		// per-worktree cache (the #290 cross-checkout poisoning guard). The
		// recipe exports GOLANGCI_LINT_CACHE once, shell-wide, before the
		// first invocation, so every later invocation in the same recipe
		// inherits it; asserting the export exists whenever any invocation
		// does is the check that can fail.
		exports := strings.Count(dryRun, "GOLANGCI_LINT_CACHE=")
		invocations := strings.Count(dryRun, "golangci-lint run")
		if exports == 0 && invocations > 0 {
			t.Errorf("%s runs golangci-lint without exporting GOLANGCI_LINT_CACHE; the per-worktree cache is the #290 poisoning guard", lane)
		}

		// (c) the per-module runner is gone from the recipe.
		if strings.Contains(dryRun, "module-lint") {
			t.Errorf("%s still runs the per-module module-lint runner; the merge replaced it with direct single invocations", lane)
		}
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
		"appwire-client/typescript/types.gen.ts",
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
