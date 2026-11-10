package evener_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/internal/devtool/repoaudit"
)

// disposableHubHeading anchors the recipe audit to the one section that
// promises an isolated hub, so an unrelated fenced block cannot satisfy it.
const disposableHubHeading = "## A Disposable Hub Needs Its Own HOME"

// markdownFencedBlock returns the lines of the first fenced block after the
// given heading. It fails the test when the heading is missing, when there is
// no fence, when the fence is not a shell block, or when the fence never
// closes — each of those is a doc/structure change the audit has to notice
// rather than silently scan nothing.
func markdownFencedBlock(t *testing.T, path, heading string) []string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(string(body), "\n")

	headingAt := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == heading {
			headingAt = i
			break
		}
	}
	if headingAt < 0 {
		t.Fatalf("%s no longer carries the heading %q", path, heading)
	}

	fences, err := repoaudit.MarkdownFences(string(body))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	for _, f := range fences {
		if f.LineNo <= headingAt+1 {
			continue // before the heading, in 1-based line numbers
		}
		switch f.Lang {
		case "sh", "bash", "shell":
			return f.Lines
		default:
			t.Fatalf("%s's first block after %q is a %q block, want a shell block", path, heading, f.Lang)
		}
	}
	t.Fatalf("%s has no fenced block after %q", path, heading)
	return nil
}

// hostileAmbient is the ambient environment the recipes must shed: the
// state/config/cache roots, the state-dir and run-dir overrides, the hub
// rendezvous settings (token, address), and the provider and
// credential config paths. The provider paths matter most — they outrank
// $HOME/.config/evener, so leaving them set makes an "isolated" hub read the
// operator's real providers and credentials.
//
// This map is the single source of truth for the audit: the hostile env, the
// values reported back by the recipe, and the variables asserted unset all
// walk these names, so a variable cannot silently drop out of one of them.
func hostileAmbient(sentinel string) map[string]string {
	return map[string]string{
		"HOME":                      filepath.Join(sentinel, "real-home"),
		"XDG_STATE_HOME":            filepath.Join(sentinel, "real-state"),
		"XDG_CONFIG_HOME":           filepath.Join(sentinel, "real-config"),
		"XDG_CACHE_HOME":            filepath.Join(sentinel, "real-cache"),
		"EVENER_STATE_DIR":          filepath.Join(sentinel, "state-dir"),
		"EVENER_RUN_DIR":            filepath.Join(sentinel, "run-dir"),
		"EVENER_HUB_TOKEN":          "real-hub-token",
		"EVENER_HUB_ADDR":           "127.0.0.1:1",
		"EVENER_HUB_SPAWNED":        "1",
		"EVENER_PROVIDERS_CONFIG":   filepath.Join(sentinel, "providers.toml"),
		"EVENER_CREDENTIALS_CONFIG": filepath.Join(sentinel, "credentials.toml"),
	}
}

func sortedNames(env map[string]string) []string {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// reportScript prints every name in env as `NAME=value` or `NAME=<unset>`, so
// the caller can read back the environment a recipe left behind without the
// recipe itself having to cooperate.
func reportScript(env map[string]string) string {
	return `
report() {
	if [ "${!1+x}" = x ]; then
		printf '%s=%s\n' "$1" "${!1}"
	else
		printf '%s=<unset>\n' "$1"
	fi
}
for v in ` + strings.Join(sortedNames(env), " ") + `; do report "$v"; done
`
}

// scaffoldRepo returns a scratch "repo root" holding a copy of the real
// isolation helper and a fake evener. copyRepositoryFile copies the shipped
// helper, so the audit exercises the real function rather than a stub;
// writeExecutable takes the fork lock the parallel suite needs before
// anything execs the fake.
func scaffoldRepo(t *testing.T) string {
	t.Helper()
	repoRoot := t.TempDir()
	copyRepositoryFile(t, ".", repoRoot, "scripts/lib/e2e-lib.sh", 0o644)
	writeExecutable(t, filepath.Join(repoRoot, "evener"), "#!/usr/bin/env bash\nexit 0\n")
	return repoRoot
}

// runRecipe executes script in repoRoot under env plus the hostile ambient
// values and a scratch TMPDIR, and parses reportScript's output back into a
// map. It fails the test on any non-zero exit, because a recipe that cannot
// run to completion is not the isolation the section promises.
func runRecipe(t *testing.T, repoRoot, script, tmp string, ambient map[string]string, extraEnv ...string) (map[string]string, []byte) {
	t.Helper()
	// The documented recipes run under bash (they source e2e-lib.sh), and
	// cmdutil.StateRootFromLookup resolves USERPROFILE rather than HOME on
	// Windows, so this host is not the one these recipes describe.
	if runtime.GOOS == "windows" {
		t.Skip("the documented recipes are bash; Windows has no /bin/sh to run them")
	}
	env := append([]string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + tmp}, extraEnv...)
	for name, value := range ambient {
		env = append(env, name+"="+value)
	}
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = repoRoot
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running the recipe failed: %v\n%s", err, out)
	}
	got := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if ok {
			got[name] = value
		}
	}
	return got, out
}

// assertIsolated checks that the environment a recipe left behind is the
// throwaway HOME and nothing else: every ambient redirect variable is unset,
// HOME is under the scratch TMPDIR, and — resolved through the product's own
// cmdutil.StateRootFromLookup rather than a re-derived path — the hub's state
// root lands under that scratch HOME and not under the ambient
// XDG_STATE_HOME.
func assertIsolated(t *testing.T, got map[string]string, ambient map[string]string, tmp string, out []byte) {
	t.Helper()
	for _, name := range sortedNames(ambient) {
		if name == "HOME" {
			continue
		}
		if v := got[name]; v != "<unset>" {
			t.Errorf("the recipe left %s=%s set, so the \"isolated\" hub can follow it to "+
				"the operator's real %s instead of the throwaway HOME:\n%s",
				name, v, strings.TrimPrefix(name, "EVENER_"), out)
		}
	}

	home := got["HOME"]
	if home == "" || home == "<unset>" {
		t.Fatalf("the recipe left HOME unset or empty; the hub would resolve the real "+
			"home directory. Reported environment:\n%s", out)
	}
	if home == ambient["HOME"] {
		t.Fatalf("the recipe kept the ambient HOME %q; the hub is not isolated:\n%s", home, out)
	}
	if !within(t, home, tmp) {
		t.Fatalf("the recipe's HOME %q is not under the scratch TMPDIR %q:\n%s", home, tmp, out)
	}

	// Resolve the state root the way the product does, from the recipe's own
	// post-isolation environment. A rebuilt HOME/.local/state/evener path
	// would only re-state the HOME check above and could not show that
	// XDG_STATE_HOME stopped redirecting.
	lookup := func(name string) (string, bool) {
		v, ok := got[name]
		if !ok || v == "<unset>" {
			return "", false
		}
		return v, true
	}
	stateRoot, err := cmdutil.StateRootFromLookup(runtime.GOOS, lookup)
	if err != nil {
		t.Fatalf("the hub's state root does not resolve from the recipe's environment: %v", err)
	}
	if !within(t, stateRoot, tmp) {
		t.Errorf("the hub's state root %q resolves outside the scratch TMPDIR %q", stateRoot, tmp)
	}
	if within(t, stateRoot, ambient["XDG_STATE_HOME"]) {
		t.Errorf("the hub's state root %q resolves under the ambient XDG_STATE_HOME %q — the "+
			"\"disposable\" hub would claim the real state root", stateRoot, ambient["XDG_STATE_HOME"])
	}
}

// within reports whether child is parent or lives beneath it, resolving
// symlinks first so a macOS /var vs /private/var split cannot make an
// isolated layout look outside its TMPDIR.
func within(t *testing.T, child, parent string) bool {
	t.Helper()
	resolve := func(p string) string {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return resolved
		}
		return p
	}
	rel, err := filepath.Rel(resolve(parent), resolve(child))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

// TestDisposableHubRecipeIsolatesTheHubEnvironment runs the recipe the testing
// docs present as the blessed disposable hub and inspects the environment the
// hub would inherit.
//
// The defect (#2040): the recipe was a bare `HOME=$(mktemp -d) ./evener hub …`
// with no unsets. cmdutil.DefaultStateRoot prefers XDG_STATE_HOME over $HOME, so
// an exported XDG_STATE_HOME kept the "disposable" hub on the real state root;
// EVENER_PROVIDERS_CONFIG/EVENER_CREDENTIALS_CONFIG outrank
// $HOME/.config/evener, so the hub forwarded the operator's real providers and
// credentials to every spawned daemon — a paid network call out of a fixture
// whose whole point is that neither happens.
//
// The recipe is executed, not pattern-matched: it runs verbatim in a scratch
// directory against a fake ./evener that reports its environment, so the check
// tracks what a reader copying the doc actually gets. A bare HOME assignment
// with XDG_STATE_HOME exported fails here.
func TestDisposableHubRecipeIsolatesTheHubEnvironment(t *testing.T) {
	t.Parallel()

	block := markdownFencedBlock(t, "docs/developing-evener/testing.md", disposableHubHeading)
	recipe := strings.Join(block, "\n")
	if !repoaudit.HasCommand(recipe, "e2e_isolate_home") {
		t.Fatalf("the disposable-hub recipe no longer routes through e2e_isolate_home; "+
			"a bare HOME=$(mktemp -d) leaves XDG_STATE_HOME and the provider/credential "+
			"config paths in force:\n%s", recipe)
	}
	if !hasHubLaunch(recipe) {
		t.Fatalf("the recipe block no longer launches the hub; the extraction or the doc "+
			"changed:\n%s", recipe)
	}

	sentinel := t.TempDir()
	ambient := hostileAmbient(sentinel)
	tmp := t.TempDir()
	got, out := runRecipe(t, scaffoldRepo(t), recipe+reportScript(ambient), tmp, ambient)
	assertIsolated(t, got, ambient, tmp, out)
}

// TestSharedHubHandoffRecipeIsolatesTheHubEnvironment executes the sibling
// hand-off recipe in agentic-testing.md — the one that re-establishes
// isolation in a fresh shell before a card reuses another card's hub. It is
// run the same way as the disposable-hub recipe, so a bare-HOME regression
// there fails here rather than passing on a prose mention.
func TestSharedHubHandoffRecipeIsolatesTheHubEnvironment(t *testing.T) {
	t.Parallel()

	block := findFencedBlockContaining(t, "docs/developing-evener/agentic-testing.md", "EVENER_E2E_RUN")
	recipe := strings.Join(block, "\n")
	if !repoaudit.HasCommand(recipe, "e2e_isolate_home") {
		t.Fatalf("the sibling hand-off recipe no longer routes through e2e_isolate_home:\n%s", recipe)
	}

	// The hand-off reads files the owning card wrote ($run/hub.log, $run/hub.pid)
	// and the token the isolated HOME now holds. Provide them so the block runs
	// to completion; the pid is this shell's own, which is alive for the
	// duration of the block's `kill -0` check.
	repoRoot := scaffoldRepo(t)
	sentinel := t.TempDir()
	ambient := hostileAmbient(sentinel)
	tmp := t.TempDir()
	runDir := filepath.Join(tmp, "shared-run")
	if err := os.MkdirAll(filepath.Join(runDir, "home", ".local", "state", "evener"), 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "hub.log"), []byte("listening on 127.0.0.1:54321\n"), 0o644); err != nil {
		t.Fatalf("write hub.log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "home", ".local", "state", "evener", "auth-token"), []byte("isolated-token\n"), 0o600); err != nil {
		t.Fatalf("write auth-token: %v", err)
	}
	prelude := "printf '%s' \"$$\" > \"$run/hub.pid\"\n"
	extra := []string{"run=" + runDir, "EVENER_E2E_RUN=" + runDir}

	// Report TOKEN too, so the recipe's own auth-token path is exercised: a
	// regression back to the legacy "$HOME/.evener/auth-token" makes the cat
	// fail, leaving TOKEN empty while the rest of the recipe still completes.
	tokenReport := "\nprintf 'TOKEN=%s\\n' \"$TOKEN\"\n"
	got, out := runRecipe(t, repoRoot, prelude+recipe+tokenReport+reportScript(ambient), tmp, ambient, extra...)
	assertIsolated(t, got, ambient, tmp, out)
	if got["TOKEN"] != "isolated-token" {
		t.Errorf("the hand-off recipe read TOKEN=%q, want the isolated token %q from "+
			"$HOME/.local/state/evener/auth-token; a legacy path read would leave it empty:\n%s",
			got["TOKEN"], "isolated-token", out)
	}
}

// findFencedBlockContaining returns the first fenced block in path whose text
// contains needle, or fails the test.
func findFencedBlockContaining(t *testing.T, path, needle string) []string {
	t.Helper()
	fences, err := repoaudit.ReadMarkdownFences(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, fence := range fences {
		if strings.Contains(strings.Join(fence.Lines, "\n"), needle) {
			return fence.Lines
		}
	}
	t.Fatalf("%s has no fenced block containing %q", path, needle)
	return nil
}

// hasHubLaunch reports whether a non-comment line starts a disposable hub,
// accepting any binary spelling (./evener, $run/evener) as long as the line
// names the hub subcommand and passes it -addr.
func hasHubLaunch(text string) bool {
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, "hub") && strings.Contains(trimmed, "-addr") {
			return true
		}
	}
	return false
}
