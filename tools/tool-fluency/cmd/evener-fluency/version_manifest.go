package main

// version_manifest.go lets matrix build its own evener binaries from a file
// mapping prompt-version labels to git refs, instead of requiring a caller to
// hand-build each one and pass it with --version LABEL=BIN. A manifest run
// resolves each ref to a commit, refuses when the repository is dirty (a run
// must record exactly which commits it measured, which an uncommitted change
// makes ambiguous) or when a ref does not resolve, and builds each commit
// into a cache directory keyed by its sha, so the same commit is never built
// twice across labels or across runs.

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// versionManifest maps a prompt-version label to the git ref that names its
// commit.
type versionManifest struct {
	Schema   int               `yaml:"schema"`
	Versions map[string]string `yaml:"versions"`
}

// loadVersionManifest decodes path strictly: an unknown field or an empty
// version map is an error, so a typo'd manifest fails at load time rather
// than silently running an empty or partial matrix.
func loadVersionManifest(path string) (versionManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return versionManifest{}, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var m versionManifest
	if err := dec.Decode(&m); err != nil {
		return versionManifest{}, fmt.Errorf("%s: %w", path, err)
	}
	if len(m.Versions) == 0 {
		return versionManifest{}, fmt.Errorf("%s: no versions", path)
	}
	return m, nil
}

// runGit runs a git subcommand against repo and returns its combined output,
// wrapping a failure with the exact command and output that failed. ctx comes
// from the caller, the way the runner's other exec.CommandContext calls do,
// rather than each git call inventing its own background context.
func runGit(ctx context.Context, repo string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// worktreeLocks holds one mutex per repo path. `git worktree add` and
// `remove` scan and edit every admin directory under .git/worktrees/, so they
// fail ("failed to read .git/worktrees/<name>/commondir", or a vanished admin
// directory) when they overlap on one repo and see another call's half-written
// or half-removed entry. Only this process runs them for a repo, so a
// process-level lock is enough.
var worktreeLocks sync.Map // repo path -> *sync.Mutex

func lockWorktrees(repo string) (unlock func()) {
	m, _ := worktreeLocks.LoadOrStore(filepath.Clean(repo), &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// addDetachedWorktree checks out sha into a new detached worktree at wt.
func addDetachedWorktree(ctx context.Context, repo, wt, sha string) error {
	defer lockWorktrees(repo)()
	_, err := runGit(ctx, repo, "worktree", "add", "--detach", "--force", wt, sha)
	return err
}

// removeWorktree deletes the worktree at wt, ignoring failure: it is cleanup.
func removeWorktree(ctx context.Context, repo, wt string) {
	defer lockWorktrees(repo)()
	_, _ = runGit(ctx, repo, "worktree", "remove", "--force", wt)
}

// resolveManifestCommit resolves ref to a commit sha in repo. A ref that does
// not resolve names both the label and the ref in its error, so a manifest
// with one bad entry fails loudly instead of silently building the rest.
func resolveManifestCommit(ctx context.Context, repo, label, ref string) (string, error) {
	out, err := runGit(ctx, repo, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("version %q: ref %q does not resolve to a commit in %s: %w", label, ref, repo, err)
	}
	return strings.TrimSpace(out), nil
}

// checkRepoClean refuses a repository with uncommitted changes: a
// version-manifest build must record exactly which commits it measured, and a
// dirty working tree makes it ambiguous whether the caller meant a committed
// ref or their pending edits.
func checkRepoClean(ctx context.Context, repo string) error {
	out, err := runGit(ctx, repo, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "" {
		return fmt.Errorf("%s has uncommitted changes; a version-manifest build must record exactly which commits it measured, so commit or set them aside first", repo)
	}
	return nil
}

// buildVersionFromManifest builds pkg at commit sha in repo and returns the
// binary's path, reusing an earlier build for the same (sha, pkg) pair
// instead of rebuilding. The cache is keyed on both: a run with a different
// --build-package against the same --version-cache must never see the
// binary an earlier run built for a different package.
//
// It never touches repo's own working tree: it builds in a uniquely named
// temporary detached worktree, which it removes when the build finishes.
// Concurrent calls for the same (sha, pkg) never share that worktree path
// (one's cleanup could otherwise delete another's in-flight checkout), and
// each writes its build to its own temp file before renaming it into the
// shared cache path, so a half-written build can never look like a cache
// hit; if two calls race, they simply both build and the later rename wins.
func buildVersionFromManifest(ctx context.Context, repo, cacheDir, sha, pkg string) (string, error) {
	destDir := filepath.Join(cacheDir, sha, safeName(pkg))
	bin := filepath.Join(destDir, "evener")
	if info, err := os.Stat(bin); err == nil && !info.IsDir() {
		return bin, nil
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}

	holder, err := os.MkdirTemp(cacheDir, "wt-"+sha+"-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(holder) }()
	// git names a worktree's admin directory under .git/worktrees/ after the
	// basename of its path, so a constant basename makes concurrent calls race
	// on the same admin directory: two `git worktree add` both creating
	// .git/worktrees/worktree corrupt each other's read of its commondir
	// ("failed to read .git/worktrees/worktree/commondir"). The holder already
	// carries a per-call unique suffix from MkdirTemp, so reuse it as the
	// basename to keep each call's admin directory unique.
	wt := filepath.Join(holder, filepath.Base(holder))
	if err := addDetachedWorktree(ctx, repo, wt, sha); err != nil {
		return "", err
	}
	defer removeWorktree(ctx, repo, wt)

	// os.CreateTemp allocates a unique name atomically, so two concurrent
	// builds can never collide on it; the file itself is then removed so
	// `go build` creates it fresh, with the executable permissions a build
	// output needs. Reusing the file os.CreateTemp made (mode 0600) would
	// leave the binary non-executable: opening an existing file for write
	// does not change its mode.
	tmpFile, err := os.CreateTemp(destDir, "evener-*.tmp")
	if err != nil {
		return "", err
	}
	tmpPath := tmpFile.Name()
	closeErr := tmpFile.Close()
	removeErr := os.Remove(tmpPath)
	if closeErr != nil {
		return "", closeErr
	}
	if removeErr != nil {
		return "", removeErr
	}
	defer func() { _ = os.Remove(tmpPath) }() // no-op once renamed into bin

	cmd := exec.CommandContext(ctx, "go", "build", "-o", tmpPath, pkg)
	cmd.Dir = wt
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build %s at %s: %w", pkg, sha, err)
	}
	if err := os.Rename(tmpPath, bin); err != nil {
		return "", err
	}
	return bin, nil
}

// versionsFromManifest resolves and builds every labeled version in m against
// repo, in a deterministic label order, failing loudly on the first dirty
// repo or unresolved ref rather than building some versions and skipping
// others silently.
func versionsFromManifest(ctx context.Context, repo, cacheDir, pkg string, m versionManifest) ([]matrixVersion, error) {
	if err := checkRepoClean(ctx, repo); err != nil {
		return nil, err
	}
	labels := slices.Sorted(maps.Keys(m.Versions))
	out := make([]matrixVersion, 0, len(labels))
	for _, label := range labels {
		ref := m.Versions[label]
		sha, err := resolveManifestCommit(ctx, repo, label, ref)
		if err != nil {
			return nil, err
		}
		bin, err := buildVersionFromManifest(ctx, repo, cacheDir, sha, pkg)
		if err != nil {
			return nil, fmt.Errorf("version %q (%s@%s): %w", label, ref, sha, err)
		}
		out = append(out, matrixVersion{Label: label, Bin: bin})
	}
	return out, nil
}
