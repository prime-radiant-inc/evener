package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// manifestFixtureRepo creates a git repository with two commits on main, each
// building a distinguishable "./cmd/fake" binary, and a branch "other" at the
// first commit. It also carries a second package, "./cmd/fake2", at both
// commits, so tests can prove that building the same commit for two different
// packages does not share a cache entry. It returns the repo path and the
// commit shas in order. Tests build these tiny packages rather than the real
// evener binary, so they stay fast and offline.
func manifestFixtureRepo(t *testing.T) (repo string, firstSHA, secondSHA string) {
	t.Helper()
	repo = t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main", "--template=")
	run("config", "user.name", "Fixture")
	run("config", "user.email", "fixture@evener.test")
	run("config", "commit.gpgsign", "false")
	mustWrite(t, filepath.Join(repo, "go.mod"), "module example.com/fixture\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(repo, "cmd", "fake", "main.go"), "package main\n\nfunc main() { println(\"one\") }\n")
	mustWrite(t, filepath.Join(repo, "cmd", "fake2", "main.go"), "package main\n\nfunc main() { println(\"other-package\") }\n")
	run("add", "-A")
	run("commit", "-q", "-m", "one")
	firstSHA = run("rev-parse", "HEAD")
	run("branch", "other")
	mustWrite(t, filepath.Join(repo, "cmd", "fake", "main.go"), "package main\n\nfunc main() { println(\"two\") }\n")
	run("add", "-A")
	run("commit", "-q", "-m", "two")
	secondSHA = run("rev-parse", "HEAD")
	return repo, firstSHA, secondSHA
}

func TestLoadVersionManifestDecodesLabelsToRefs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "versions.yaml")
	mustWrite(t, path, "schema: 1\nversions:\n  v0: main\n  v1-A: other\n")
	m, err := loadVersionManifest(path)
	if err != nil {
		t.Fatalf("loadVersionManifest: %v", err)
	}
	if m.Versions["v0"] != "main" || m.Versions["v1-A"] != "other" {
		t.Errorf("versions = %+v", m.Versions)
	}
}

func TestLoadVersionManifestRefusesEmptyOrMalformed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.yaml")
	mustWrite(t, empty, "schema: 1\nversions: {}\n")
	if _, err := loadVersionManifest(empty); err == nil {
		t.Error("loadVersionManifest(empty) = nil, want a refusal")
	}
	badField := filepath.Join(dir, "bad.yaml")
	mustWrite(t, badField, "schema: 1\nversions:\n  v0: main\nnotafield: true\n")
	if _, err := loadVersionManifest(badField); err == nil {
		t.Error("loadVersionManifest(unknown field) = nil, want a refusal")
	}
}

func TestResolveManifestCommitResolvesRefsAndRefusesMissingOnes(t *testing.T) {
	t.Parallel()
	repo, firstSHA, secondSHA := manifestFixtureRepo(t)
	if got, err := resolveManifestCommit(context.Background(), repo, "v0", "main"); err != nil || got != secondSHA {
		t.Errorf("resolveManifestCommit(main) = %q, %v; want %q", got, err, secondSHA)
	}
	if got, err := resolveManifestCommit(context.Background(), repo, "v1-A", "other"); err != nil || got != firstSHA {
		t.Errorf("resolveManifestCommit(other) = %q, %v; want %q", got, err, firstSHA)
	}
	_, err := resolveManifestCommit(context.Background(), repo, "v2", "does-not-exist")
	if err == nil || !strings.Contains(err.Error(), "v2") || !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("resolveManifestCommit(missing) = %v, want a refusal naming the label and ref", err)
	}
}

func TestCheckRepoCleanRefusesUncommittedChanges(t *testing.T) {
	t.Parallel()
	repo, _, _ := manifestFixtureRepo(t)
	if err := checkRepoClean(context.Background(), repo); err != nil {
		t.Fatalf("checkRepoClean(clean repo) = %v, want nil", err)
	}
	mustWrite(t, filepath.Join(repo, "cmd", "fake", "main.go"), "package main\n\nfunc main() { println(\"dirty\") }\n")
	err := checkRepoClean(context.Background(), repo)
	if err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("checkRepoClean(dirty repo) = %v, want a refusal naming uncommitted changes", err)
	}
}

func TestBuildVersionFromManifestBuildsAndCachesByCommit(t *testing.T) {
	t.Parallel()
	repo, firstSHA, secondSHA := manifestFixtureRepo(t)
	cache := t.TempDir()
	bin, err := buildVersionFromManifest(context.Background(), repo, cache, secondSHA, "./cmd/fake")
	if err != nil {
		t.Fatalf("buildVersionFromManifest: %v", err)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("built binary: %v", err)
	}
	firstMod, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	// A second build of the same commit reuses the cached binary instead of
	// rebuilding: its mtime does not move.
	again, err := buildVersionFromManifest(context.Background(), repo, cache, secondSHA, "./cmd/fake")
	if err != nil {
		t.Fatalf("buildVersionFromManifest (again): %v", err)
	}
	secondMod, err := os.Stat(again)
	if err != nil {
		t.Fatal(err)
	}
	if again != bin || !secondMod.ModTime().Equal(firstMod.ModTime()) {
		t.Errorf("buildVersionFromManifest rebuilt an already-cached commit: %s vs %s", bin, again)
	}
	// A different commit gets its own cached binary.
	otherBin, err := buildVersionFromManifest(context.Background(), repo, cache, firstSHA, "./cmd/fake")
	if err != nil {
		t.Fatalf("buildVersionFromManifest (other commit): %v", err)
	}
	if otherBin == bin {
		t.Errorf("two different commits shared a cached binary at %s", bin)
	}
	// The build leaves no worktree registered against the repo.
	cmd := exec.Command("git", "-C", repo, "worktree", "list")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git worktree list: %v: %s", err, out)
	}
	if strings.Count(strings.TrimSpace(string(out)), "\n") != 0 {
		t.Errorf("worktree list = %s, want only the repo's own primary worktree", out)
	}
}

// TestBuildVersionFromManifestCachesSeparatelyPerPackage: the cache is keyed
// on the commit AND the package. Two different packages built at the same
// commit must land at different paths and produce different binaries, or a
// later run with a different --build-package would silently reuse whatever
// an earlier run built for a different one.
func TestBuildVersionFromManifestCachesSeparatelyPerPackage(t *testing.T) {
	t.Parallel()
	repo, _, secondSHA := manifestFixtureRepo(t)
	cache := t.TempDir()
	binFake, err := buildVersionFromManifest(context.Background(), repo, cache, secondSHA, "./cmd/fake")
	if err != nil {
		t.Fatalf("buildVersionFromManifest(./cmd/fake): %v", err)
	}
	binFake2, err := buildVersionFromManifest(context.Background(), repo, cache, secondSHA, "./cmd/fake2")
	if err != nil {
		t.Fatalf("buildVersionFromManifest(./cmd/fake2): %v", err)
	}
	if binFake == binFake2 {
		t.Fatalf("two different packages at the same commit shared a cached binary at %s", binFake)
	}
	outFake, err := exec.Command(binFake).CombinedOutput()
	if err != nil {
		t.Fatalf("run %s: %v: %s", binFake, err, outFake)
	}
	outFake2, err := exec.Command(binFake2).CombinedOutput()
	if err != nil {
		t.Fatalf("run %s: %v: %s", binFake2, err, outFake2)
	}
	if strings.TrimSpace(string(outFake)) == strings.TrimSpace(string(outFake2)) {
		t.Errorf("./cmd/fake and ./cmd/fake2 produced the same output %q, want each package's own binary", outFake)
	}
}

// TestBuildVersionFromManifestConcurrentBuildsOfSameCommitBothSucceed: several
// concurrent builds of the same commit must not race over a shared worktree
// path (one's cleanup deleting the other's in-flight checkout) or over a
// shared final binary path (one seeing the other's half-written file as a
// cache hit). Both builds should succeed, the cached binary should run, and
// the repository should be left with no leaked worktree.
func TestBuildVersionFromManifestConcurrentBuildsOfSameCommitBothSucceed(t *testing.T) {
	t.Parallel()
	repo, _, secondSHA := manifestFixtureRepo(t)
	cache := t.TempDir()
	const n = 8
	bins := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bins[i], errs[i] = buildVersionFromManifest(context.Background(), repo, cache, secondSHA, "./cmd/fake")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("build %d: %v", i, err)
		}
	}
	if bins[0] != bins[1] {
		t.Fatalf("concurrent builds of the same commit = %v, want the same cached path", bins)
	}
	if out, err := exec.Command(bins[0]).CombinedOutput(); err != nil {
		t.Fatalf("run %s: %v: %s", bins[0], err, out)
	}
	out, err := exec.Command("git", "-C", repo, "worktree", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("git worktree list: %v: %s", err, out)
	}
	if strings.Count(strings.TrimSpace(string(out)), "\n") != 0 {
		t.Errorf("worktree list = %s, want only the repo's own primary worktree", out)
	}
}

// TestWorktreeAddAndRemoveOnOneRepoTolerateConcurrency: `git worktree add`
// scans every .git/worktrees/* admin directory, so it can read another
// call's half-written one and fail ("failed to read commondir"). Many
// concurrent adds and removes on one repo must all succeed.
func TestWorktreeAddAndRemoveOnOneRepoTolerateConcurrency(t *testing.T) {
	t.Parallel()
	repo, _, secondSHA := manifestFixtureRepo(t)
	base := t.TempDir()
	const n = 64
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			wt := filepath.Join(base, fmt.Sprintf("wt-%d", i), fmt.Sprintf("wt-%d", i))
			if errs[i] = addDetachedWorktree(context.Background(), repo, wt, secondSHA); errs[i] == nil {
				removeWorktree(context.Background(), repo, wt)
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("add %d: %v", i, err)
		}
	}
}

func TestVersionsFromManifestResolvesAndRefusesADirtyRepo(t *testing.T) {
	t.Parallel()
	repo, firstSHA, secondSHA := manifestFixtureRepo(t)
	cache := t.TempDir()
	m := versionManifest{Versions: map[string]string{"v0": "main", "v1-A": "other"}}
	versions, err := versionsFromManifest(context.Background(), repo, cache, "./cmd/fake", m)
	if err != nil {
		t.Fatalf("versionsFromManifest: %v", err)
	}
	got := map[string]string{}
	for _, v := range versions {
		got[v.Label] = v.Bin
	}
	if len(got) != 2 || got["v0"] == "" || got["v1-A"] == "" || got["v0"] == got["v1-A"] {
		t.Fatalf("versions = %+v, want v0 and v1-A with distinct binaries", got)
	}
	_ = firstSHA
	_ = secondSHA

	mustWrite(t, filepath.Join(repo, "untracked.txt"), "dirty\n")
	_, err = versionsFromManifest(context.Background(), repo, cache, "./cmd/fake", m)
	if err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("versionsFromManifest(dirty repo) = %v, want a refusal", err)
	}
}

func TestRunMatrixCommandNeedsVersionCacheWithVersionManifest(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	manifest := filepath.Join(out, "versions.yaml")
	mustWrite(t, manifest, "schema: 1\nversions:\n  v0: main\n")
	err := runMatrixCommand([]string{"--out", out, "--version-manifest", manifest, "--models", "m"})
	if err == nil || !strings.Contains(err.Error(), "--version-cache") {
		t.Fatalf("runMatrixCommand = %v, want a refusal naming --version-cache", err)
	}
}

// TestRunMatrixCommandRefusesOverlappingLabels: not parallel, since it
// replaces the package's suite runner.
func TestRunMatrixCommandRefusesOverlappingLabels(t *testing.T) {
	repo, _, _ := manifestFixtureRepo(t)
	out := t.TempDir()
	manifest := filepath.Join(out, "versions.yaml")
	mustWrite(t, manifest, "schema: 1\nversions:\n  v0: main\n")
	orig := runMatrixSuite
	t.Cleanup(func() { runMatrixSuite = orig })
	runMatrixSuite = func(cfg runConfig) error { return nil }
	err := runMatrixCommand([]string{"--out", out, "--version", "v0=/bin/true", "--version-manifest", manifest,
		"--version-cache", filepath.Join(out, "cache"), "--repo", repo, "--build-package", "./cmd/fake", "--models", "m"})
	if err == nil || !strings.Contains(err.Error(), "v0") {
		t.Fatalf("runMatrixCommand = %v, want a refusal naming the duplicate label v0", err)
	}
}

// TestRunMatrixCommandBuildsFromVersionManifest: not parallel, since it
// replaces the package's suite runner.
func TestRunMatrixCommandBuildsFromVersionManifest(t *testing.T) {
	repo, firstSHA, secondSHA := manifestFixtureRepo(t)
	out := t.TempDir()
	manifest := filepath.Join(out, "versions.yaml")
	mustWrite(t, manifest, "schema: 1\nversions:\n  v0: main\n  v1-A: other\n")
	var mu sync.Mutex
	var got []runConfig
	orig := runMatrixSuite
	t.Cleanup(func() { runMatrixSuite = orig })
	runMatrixSuite = func(cfg runConfig) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, cfg)
		return nil
	}
	err := runMatrixCommand([]string{"--out", out, "--version-manifest", manifest, "--version-cache", filepath.Join(out, "cache"),
		"--repo", repo, "--build-package", "./cmd/fake", "--models", "m"})
	if err != nil {
		t.Fatalf("runMatrixCommand: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ran %d configurations, want 2", len(got))
	}
	bins := map[string]string{}
	for _, cfg := range got {
		if strings.Contains(cfg.outDir, "v0") {
			bins["v0"] = cfg.evenerBin
		} else {
			bins["v1-A"] = cfg.evenerBin
		}
	}
	if bins["v0"] == "" || bins["v1-A"] == "" || bins["v0"] == bins["v1-A"] {
		t.Fatalf("bins = %+v, want two distinct built binaries", bins)
	}
	_ = firstSHA
	_ = secondSHA
}
