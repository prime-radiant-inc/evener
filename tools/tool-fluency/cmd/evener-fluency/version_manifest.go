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
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

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
// wrapping a failure with the exact command and output that failed.
func runGit(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// resolveManifestCommit resolves ref to a commit sha in repo. A ref that does
// not resolve names both the label and the ref in its error, so a manifest
// with one bad entry fails loudly instead of silently building the rest.
func resolveManifestCommit(repo, label, ref string) (string, error) {
	out, err := runGit(repo, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("version %q: ref %q does not resolve to a commit in %s: %w", label, ref, repo, err)
	}
	return strings.TrimSpace(out), nil
}

// checkRepoClean refuses a repository with uncommitted changes: a
// version-manifest build must record exactly which commits it measured, and a
// dirty working tree makes it ambiguous whether the caller meant a committed
// ref or their pending edits.
func checkRepoClean(repo string) error {
	out, err := runGit(repo, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "" {
		return fmt.Errorf("%s has uncommitted changes; a version-manifest build must record exactly which commits it measured, so commit or set them aside first", repo)
	}
	return nil
}

// buildVersionFromManifest builds pkg at commit sha in repo and returns the
// binary's path, reusing an earlier build for the same sha instead of
// rebuilding. It never touches repo's own working tree: it builds in a
// temporary detached worktree, which it removes when the build finishes.
func buildVersionFromManifest(repo, cacheDir, sha, pkg string) (string, error) {
	bin := filepath.Join(cacheDir, sha, "evener")
	if info, err := os.Stat(bin); err == nil && !info.IsDir() {
		return bin, nil
	}
	wt := filepath.Join(cacheDir, "wt-"+sha)
	if err := os.RemoveAll(wt); err != nil {
		return "", err
	}
	if _, err := runGit(repo, "worktree", "add", "--detach", "--force", wt, sha); err != nil {
		return "", err
	}
	defer func() {
		_, _ = runGit(repo, "worktree", "remove", "--force", wt)
	}()
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return "", err
	}
	cmd := exec.Command("go", "build", "-o", bin, pkg)
	cmd.Dir = wt
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build %s at %s: %w", pkg, sha, err)
	}
	return bin, nil
}

// versionsFromManifest resolves and builds every labeled version in m against
// repo, in a deterministic label order, failing loudly on the first dirty
// repo or unresolved ref rather than building some versions and skipping
// others silently.
func versionsFromManifest(repo, cacheDir, pkg string, m versionManifest) ([]matrixVersion, error) {
	if err := checkRepoClean(repo); err != nil {
		return nil, err
	}
	labels := slices.Sorted(maps.Keys(m.Versions))
	out := make([]matrixVersion, 0, len(labels))
	for _, label := range labels {
		ref := m.Versions[label]
		sha, err := resolveManifestCommit(repo, label, ref)
		if err != nil {
			return nil, err
		}
		bin, err := buildVersionFromManifest(repo, cacheDir, sha, pkg)
		if err != nil {
			return nil, fmt.Errorf("version %q (%s@%s): %w", label, ref, sha, err)
		}
		out = append(out, matrixVersion{Label: label, Bin: bin})
	}
	return out, nil
}
