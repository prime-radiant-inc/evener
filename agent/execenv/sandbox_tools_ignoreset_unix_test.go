//go:build linux || darwin

package execenv

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"primeradiant.com/evener/agent/sandbox"
)

// TestLoadIgnoreSetSkipsMaskedSubtree: loadIgnoreSet, driven with the same
// secureDirFS and masking skip predicate sandboxFS.glob/grepNative wire it
// with, never descends into (and so never reads a .gitignore from) a masked
// subtree — proving I3: prior to this fix, loadIgnoreSet had no way to skip
// masked paths at all, so a sandboxed session's glob/grep would read
// .gitignore files inside a policy-masked directory (e.g. ~/.ssh, an AWS
// credentials dir). A non-masked .gitignore elsewhere under the same base
// must still be picked up, so this also proves the fix doesn't over-skip.
func TestLoadIgnoreSetSkipsMaskedSubtree(t *testing.T) {
	t.Parallel()
	env, home, _ := sandboxedEnvWithDenylist(t, sandbox.ModeReadOnly, filepath.Join("~", "vault"))
	vault := filepath.Join(home, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, ".gitignore"), []byte("*.secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sfs := env.sandbox()
	if sfs == nil {
		t.Fatal("expected a sandboxed environment")
	}
	// sandbox() hands back a held layer; the ignore-set work below runs on it,
	// so the release goes at the end of the operation, not here.
	defer sfs.release()
	baseFd, canonical, err := sfs.openReadBaseFd("glob", home)
	if err != nil {
		t.Fatalf("openReadBaseFd: %v", err)
	}
	defer func() { _ = unix.Close(baseFd) }()

	// One budget for both, as the production callers do: the filesystem charges
	// entries and live holdings against the same budget discovery charges
	// listings and rules to, so splitting them here would hide a regression in
	// that sharing.
	budget := newGlobBudget("glob")
	fsys := &secureDirFS{baseFd: baseFd, basePath: canonical, fs: sfs, budget: budget, ctx: t.Context()}
	set, err := loadIgnoreSet(t.Context(), fsys, func(relPath string) bool {
		return sfs.underMasked(filepath.Join(canonical, relPath))
	}, budget, wholeBaseIgnoreScope())
	if err != nil {
		t.Fatal(err)
	}

	for _, d := range set.dirs {
		if d.rel == "vault" || strings.HasPrefix(d.rel, "vault/") {
			t.Errorf("loadIgnoreSet read a .gitignore inside the masked vault subtree: rel=%q", d.rel)
		}
	}
	found := false
	for _, d := range set.dirs {
		if d.rel == "." {
			found = true
		}
	}
	if !found {
		t.Error("loadIgnoreSet should still load the non-masked base .gitignore")
	}
}

// TestSandboxedGlobReusesIgnoreMatchersAcrossCalls is the sandboxed half of the
// entry-point proof: the sandboxed glob walks a different filesystem (a
// secureDirFS over a held fd) and reaches the shared cache through globCached,
// so the cache must be asserted through env.Glob rather than through the
// helper, which is handed a cache directly and therefore stayed green while no
// call site passed one.
func TestSandboxedGlobReusesIgnoreMatchersAcrossCalls(t *testing.T) {
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)
	if err := os.WriteFile(filepath.Join(worktree, ".gitignore"), []byte("*.log\n!keep.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "a.log"), []byte("log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "keep.log"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	count, reset := countEntryPointCompiles(t)

	glob := func() []string {
		matches, _, err := env.GlobWithBudget(t.Context(), "**/*", worktree, false, NewGlobBudget())
		if err != nil {
			t.Fatalf("sandboxed GlobWithBudget: %v", err)
		}
		return matches
	}
	first := glob()
	if firstCompiles := count(); firstCompiles == 0 {
		t.Fatal("the first sandboxed glob compiled no rules file, so this test proves nothing")
	}
	reset()
	second := glob()
	if got := count(); got != 0 {
		t.Fatalf("the second sandboxed glob compiled %d rules file(s), want 0: the environment's cache is not reaching the sandboxed glob's ignore load", got)
	}
	if !slices.Equal(first, second) {
		t.Fatalf("the two sandboxed globs disagreed:\nfirst  = %v\nsecond = %v", first, second)
	}
	for _, m := range second {
		if filepath.Base(m) == "a.log" {
			t.Fatalf("the cached sandboxed pass reported an ignored file: %v", second)
		}
	}
	if !slices.ContainsFunc(second, func(p string) bool { return filepath.Base(p) == "keep.log" }) {
		t.Fatalf("the cached sandboxed pass dropped the negated file: %v", second)
	}
}

// TestNilEnvironmentReadsIgnoreRulesUncached pins the fallback the cache has to
// keep: only a nil environment answers a nil cache, so its searches must still
// read and compile rules instead of panicking or matching nothing. A
// struct-literal environment takes the other path — ignoreMatcherCacheFor fills
// its cache in on first use — which the second half of this test asserts.
func TestNilEnvironmentReadsIgnoreRulesUncached(t *testing.T) {
	t.Parallel()
	if cache := (*LocalExecutionEnvironment)(nil).ignoreMatcherCacheFor(); cache != nil {
		t.Fatal("a nil environment answered a cache, so its callers would thread a non-nil cache into a search that has no environment behind it")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.log"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A struct literal carries no initialized cache; the accessor fills it in.
	env := &LocalExecutionEnvironment{RootDir: root}
	if env.ignoreMatcherCacheFor() == nil {
		t.Fatal("a struct-literal environment's accessor answered nil, want a cache so its searches can reuse matchers")
	}
	matches, _, err := env.GlobWithBudget(t.Context(), "**/*", root, false, NewGlobBudget())
	if err != nil {
		t.Fatalf("struct-literal env GlobWithBudget: %v", err)
	}
	for _, m := range matches {
		if filepath.Base(m) == "a.log" {
			t.Fatalf("the struct-literal environment did not apply its rules file: %v", matches)
		}
	}
	if !slices.ContainsFunc(matches, func(p string) bool { return filepath.Base(p) == "a.go" }) {
		t.Fatalf("the struct-literal environment matched nothing it should have: %v", matches)
	}
}
