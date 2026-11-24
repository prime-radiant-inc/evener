//go:build linux || darwin

package execenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	gitignore "github.com/sabhiram/go-gitignore"
	"primeradiant.com/evener/agent/sandbox"
)

func TestWalkProjectFilesSecurityAndDenylist(t *testing.T) {
	t.Parallel()

	t.Run("excludes git dir and prunes gitignored subtrees before listing", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		mustWriteProjectFile(t, filepath.Join(root, ".gitignore"), "ignored_unreadable/\nignored_huge/\n*.secret\n")
		mustWriteProjectFile(t, filepath.Join(root, "keep.go"), "package main\n")
		mustWriteProjectFile(t, filepath.Join(root, "drop.secret"), "nope\n")
		mustWriteProjectFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
		mustWriteProjectFile(t, filepath.Join(root, ".git", "objects", "pack"), "data\n")
		mustWriteProjectFile(t, filepath.Join(root, ".github", "workflows", "ci.yml"), "name: ci\n")

		// An ignored unreadable directory must be pruned before listing so chmod 0000
		// does not error or leak entries. Skip chmod check when running as root.
		ignoredUnreadable := filepath.Join(root, "ignored_unreadable")
		mustWriteProjectFile(t, filepath.Join(ignoredUnreadable, "hidden.txt"), "secret\n")
		if os.Geteuid() != 0 {
			if err := os.Chmod(ignoredUnreadable, 0o000); err != nil {
				t.Fatalf("chmod ignored_unreadable: %v", err)
			}
			t.Cleanup(func() { _ = os.Chmod(ignoredUnreadable, 0o755) })
		}

		// An ignored subtree with more subdirectories than MaxDirListings must be
		// pruned before listing so it never consumes the directory listing budget.
		for i := range 12 {
			mustWriteProjectFile(t, filepath.Join(root, "ignored_huge", fmt.Sprintf("sub%02d", i), "file.txt"), "x\n")
		}

		res, err := WalkProjectFiles(t.Context(), root, ProjectFilesOptions{
			IncludeDotfiles: true,
			Limits: ProjectFilesLimits{
				MaxDirListings: 4,
			},
		})
		if err != nil {
			t.Fatalf("WalkProjectFiles: %v", err)
		}
		if res.Truncated {
			t.Fatalf("WalkProjectFiles unexpectedly truncated with ignored subtrees pruned: %+v", res.Entries)
		}

		got := projectEntryMap(res.Entries)
		if _, ok := got["keep.go"]; !ok {
			t.Fatalf("missing keep.go in %v", res.Entries)
		}
		if !got[".github"] || !got[".github/workflows"] {
			t.Fatalf("expected non-.git dotdirs when IncludeDotfiles=true, got %v", res.Entries)
		}
		if _, ok := got[".github/workflows/ci.yml"]; !ok {
			t.Fatalf("missing .github/workflows/ci.yml in %v", res.Entries)
		}
		for _, forbidden := range []string{
			".git",
			".git/HEAD",
			".git/objects",
			"drop.secret",
			"ignored_unreadable",
			"ignored_unreadable/hidden.txt",
			"ignored_huge",
			"ignored_huge/sub00",
		} {
			if _, ok := got[forbidden]; ok {
				t.Fatalf("forbidden path %q appeared in walk results: %v", forbidden, res.Entries)
			}
		}
	})

	t.Run("nested .gitignore negation re-includes directory and selected files", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		mustWriteProjectFile(t, filepath.Join(root, ".gitignore"), "results/\n__pycache__/\n")
		mustWriteProjectFile(t, filepath.Join(root, "results", "root_ignored.json"), "{}\n")
		const nestedDir = "mobile-native/modules/evener-voice/tools/wake-word"
		mustWriteProjectFile(
			t,
			filepath.Join(root, nestedDir, ".gitignore"),
			"__pycache__/\n!results/\nresults/*\n!results/*.json\n",
		)
		mustWriteProjectFile(t, filepath.Join(root, nestedDir, "results", "calibration.json"), "{}\n")
		mustWriteProjectFile(t, filepath.Join(root, nestedDir, "results", "scratch.bin"), "bin\n")
		mustWriteProjectFile(t, filepath.Join(root, nestedDir, "__pycache__", "x.pyc"), "pyc\n")

		res, err := WalkProjectFiles(t.Context(), root, ProjectFilesOptions{})
		if err != nil {
			t.Fatalf("WalkProjectFiles: %v", err)
		}
		got := projectEntryMap(res.Entries)
		for _, want := range []string{
			nestedDir + "/results",
			nestedDir + "/results/calibration.json",
		} {
			if _, ok := got[want]; !ok {
				t.Fatalf("expected re-included %q in walk results, got %v", want, res.Entries)
			}
		}
		for _, forbidden := range []string{
			"results",
			"results/root_ignored.json",
			nestedDir + "/results/scratch.bin",
			nestedDir + "/__pycache__",
			nestedDir + "/__pycache__/x.pyc",
		} {
			if _, ok := got[forbidden]; ok {
				t.Fatalf("expected ignored %q to stay excluded, got %v", forbidden, res.Entries)
			}
		}
	})

	t.Run("skips DefaultDenylist descendants and masked root even with dotfiles enabled", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		mustWriteProjectFile(t, filepath.Join(home, "workspace", "main.go"), "package main\n")
		mustWriteProjectFile(t, filepath.Join(home, ".config", "allowed.toml"), "ok = true\n")
		mustWriteProjectFile(t, filepath.Join(home, ".docker", "daemon.json"), "{}\n")

		// Seed every home credential entry from sandbox.DefaultDenylist(home).
		mustWriteProjectFile(t, filepath.Join(home, ".ssh", "id_ed25519"), "key\n")
		mustWriteProjectFile(t, filepath.Join(home, ".aws", "credentials"), "key\n")
		mustWriteProjectFile(t, filepath.Join(home, ".config", "gcloud", "creds.db"), "db\n")
		mustWriteProjectFile(t, filepath.Join(home, ".netrc"), "login\n")
		mustWriteProjectFile(t, filepath.Join(home, ".config", "evener", "credentials.toml"), "secret\n")
		mustWriteProjectFile(t, filepath.Join(home, ".gnupg", "secring.gpg"), "gpg\n")
		mustWriteProjectFile(t, filepath.Join(home, ".docker", "config.json"), "{}\n")
		mustWriteProjectFile(t, filepath.Join(home, ".kube", "config"), "kube\n")
		mustWriteProjectFile(t, filepath.Join(home, ".git-credentials"), "cred\n")

		// Walking $HOME itself with IncludeDotfiles=true must expose non-masked
		// dotfiles while omitting every DefaultDenylist path and descendant.
		res, err := WalkProjectFiles(t.Context(), home, ProjectFilesOptions{
			IncludeDotfiles: true,
			HomeDir:         home,
		})
		if err != nil {
			t.Fatalf("WalkProjectFiles(home): %v", err)
		}
		got := projectEntryMap(res.Entries)
		for _, want := range []string{
			"workspace/main.go",
			".config",
			".config/allowed.toml",
			".docker",
			".docker/daemon.json",
		} {
			if _, ok := got[want]; !ok {
				t.Fatalf("missing expected unmasked entry %q in %v", want, res.Entries)
			}
		}
		for _, masked := range []string{
			".ssh",
			".ssh/id_ed25519",
			".aws",
			".aws/credentials",
			".config/gcloud",
			".config/gcloud/creds.db",
			".netrc",
			".config/evener",
			".config/evener/credentials.toml",
			".gnupg",
			".gnupg/secring.gpg",
			".docker/config.json",
			".kube",
			".kube/config",
			".git-credentials",
		} {
			if _, ok := got[masked]; ok {
				t.Fatalf("masked denylist entry %q leaked in %v", masked, res.Entries)
			}
		}

		// Walking a masked root directly must return empty immediately.
		maskedRootRes, err := WalkProjectFiles(t.Context(), filepath.Join(home, ".ssh"), ProjectFilesOptions{
			IncludeDotfiles: true,
			HomeDir:         home,
		})
		if err != nil {
			t.Fatalf("WalkProjectFiles(masked root): %v", err)
		}
		if len(maskedRootRes.Entries) != 0 || maskedRootRes.Truncated {
			t.Fatalf("masked root walk = %+v, want empty non-truncated", maskedRootRes)
		}

		// Verify canonicalization catches StripDataVolumeAlias + masked root alias.
		aliasList := sandbox.DefaultDenylist(home)
		if len(aliasList) == 0 {
			t.Fatal("DefaultDenylist returned empty")
		}
	})

	t.Run("skips file symlinks directory symlinks and symlink cycles", func(t *testing.T) {
		t.Parallel()
		outside := t.TempDir()
		mustWriteProjectFile(t, filepath.Join(outside, "secret.txt"), "outside\n")
		mustWriteProjectFile(t, filepath.Join(outside, "dir", "nested.txt"), "outside\n")

		root := t.TempDir()
		mustWriteProjectFile(t, filepath.Join(root, "real.go"), "package real\n")
		mustWriteProjectFile(t, filepath.Join(root, "sub", "child.go"), "package sub\n")

		if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "escape_file.txt")); err != nil {
			t.Fatalf("symlink file: %v", err)
		}
		if err := os.Symlink(filepath.Join(outside, "dir"), filepath.Join(root, "escape_dir")); err != nil {
			t.Fatalf("symlink dir: %v", err)
		}
		if err := os.Symlink(root, filepath.Join(root, "sub", "cycle_root")); err != nil {
			t.Fatalf("symlink cycle: %v", err)
		}
		if err := os.Symlink(filepath.Join(root, "real.go"), filepath.Join(root, "inroot_link.go")); err != nil {
			t.Fatalf("symlink in-root: %v", err)
		}

		res, err := WalkProjectFiles(t.Context(), root, ProjectFilesOptions{IncludeDotfiles: true})
		if err != nil {
			t.Fatalf("WalkProjectFiles: %v", err)
		}
		if res.Truncated {
			t.Fatalf("unexpected truncation: %+v", res)
		}
		got := projectEntryMap(res.Entries)
		if _, ok := got["real.go"]; !ok {
			t.Fatalf("missing real.go in %v", res.Entries)
		}
		if _, ok := got["sub/child.go"]; !ok {
			t.Fatalf("missing sub/child.go in %v", res.Entries)
		}
		for _, forbidden := range []string{
			"escape_file.txt",
			"escape_dir",
			"escape_dir/nested.txt",
			"sub/cycle_root",
			"sub/cycle_root/real.go",
			"inroot_link.go",
		} {
			if _, ok := got[forbidden]; ok {
				t.Fatalf("symlink entry %q must be skipped, got %v", forbidden, res.Entries)
			}
		}
	})
}

func TestWalkProjectFilesGitWorktreeAncestorIgnores(t *testing.T) {
	t.Parallel()

	t.Run("subdirectory of .git directory repo honors root and intermediate .gitignore plus nested negations", func(t *testing.T) {
		t.Parallel()
		repoRoot := t.TempDir()
		mustWriteProjectFile(t, filepath.Join(repoRoot, ".git", "HEAD"), "ref: refs/heads/main\n")
		mustWriteProjectFile(
			t,
			filepath.Join(repoRoot, ".gitignore"),
			"node_modules/\nresults/\n/docs-site/anchored_only/\nignored_in_root/\n",
		)
		mustWriteProjectFile(t, filepath.Join(repoRoot, "apps", ".gitignore"), "dist/\n")

		docsRoot := filepath.Join(repoRoot, "docs-site")
		mustWriteProjectFile(t, filepath.Join(docsRoot, ".gitignore"), "!ignored_in_root/\n")
		mustWriteProjectFile(t, filepath.Join(docsRoot, "node_modules", "pkg", "index.js"), "module.exports = {}\n")
		mustWriteProjectFile(t, filepath.Join(docsRoot, "anchored_only", "secret.txt"), "secret\n")
		mustWriteProjectFile(t, filepath.Join(docsRoot, "results", "dropped.txt"), "drop\n")
		mustWriteProjectFile(t, filepath.Join(docsRoot, "ignored_in_root", "kept.txt"), "keep\n")
		mustWriteProjectFile(t, filepath.Join(docsRoot, "src", "app.ts"), "export {};\n")
		mustWriteProjectFile(t, filepath.Join(docsRoot, "sub", ".gitignore"), "!results/\nresults/*\n!results/*.json\n")
		mustWriteProjectFile(t, filepath.Join(docsRoot, "sub", "results", "calibration.json"), "{}\n")
		mustWriteProjectFile(t, filepath.Join(docsRoot, "sub", "results", "scratch.bin"), "bin\n")

		res, err := WalkProjectFiles(t.Context(), docsRoot, ProjectFilesOptions{})
		if err != nil {
			t.Fatalf("WalkProjectFiles(docsRoot): %v", err)
		}
		got := projectEntryMap(res.Entries)
		for _, want := range []string{
			"src/app.ts",
			"ignored_in_root/kept.txt",
			"sub/results",
			"sub/results/calibration.json",
		} {
			if _, ok := got[want]; !ok {
				t.Fatalf("expected %q in docsRoot walk, got %v", want, res.Entries)
			}
		}
		for _, forbidden := range []string{
			"node_modules",
			"node_modules/pkg/index.js",
			"anchored_only",
			"anchored_only/secret.txt",
			"results",
			"results/dropped.txt",
			"sub/results/scratch.bin",
		} {
			if _, ok := got[forbidden]; ok {
				t.Fatalf("forbidden gitignored path %q appeared in docsRoot walk: %v", forbidden, res.Entries)
			}
		}
		for _, entry := range res.Entries {
			if strings.HasPrefix(entry.Path, "docs-site/") || strings.HasPrefix(entry.Path, "../") {
				t.Fatalf("entry %q is not relative to docsRoot", entry.Path)
			}
		}

		// Intermediate ancestor (.gitignore in apps/) excludes dist/ inside apps/web/.
		webRoot := filepath.Join(repoRoot, "apps", "web")
		mustWriteProjectFile(t, filepath.Join(webRoot, "dist", "bundle.js"), "var x;\n")
		mustWriteProjectFile(t, filepath.Join(webRoot, "node_modules", "dep", "index.js"), "var y;\n")
		mustWriteProjectFile(t, filepath.Join(webRoot, "src", "main.ts"), "export {};\n")

		webRes, err := WalkProjectFiles(t.Context(), webRoot, ProjectFilesOptions{})
		if err != nil {
			t.Fatalf("WalkProjectFiles(webRoot): %v", err)
		}
		webGot := projectEntryMap(webRes.Entries)
		if _, ok := webGot["src/main.ts"]; !ok {
			t.Fatalf("expected src/main.ts in webRoot walk, got %v", webRes.Entries)
		}
		for _, forbidden := range []string{"dist", "dist/bundle.js", "node_modules", "node_modules/dep/index.js"} {
			if _, ok := webGot[forbidden]; ok {
				t.Fatalf("forbidden path %q leaked into webRoot walk: %v", forbidden, webRes.Entries)
			}
		}
	})

	t.Run("subdirectory of linked .git pointer worktree honors root .gitignore", func(t *testing.T) {
		t.Parallel()
		wtRoot := t.TempDir()
		mustWriteProjectFile(t, filepath.Join(wtRoot, ".git"), "gitdir: /tmp/main/.git/worktrees/wt\n")
		mustWriteProjectFile(t, filepath.Join(wtRoot, ".gitignore"), "node_modules/\n")
		subRoot := filepath.Join(wtRoot, "packages", "web")
		mustWriteProjectFile(t, filepath.Join(subRoot, "node_modules", "dep", "a.js"), "x\n")
		mustWriteProjectFile(t, filepath.Join(subRoot, "index.ts"), "export const ok = true;\n")

		res, err := WalkProjectFiles(t.Context(), subRoot, ProjectFilesOptions{})
		if err != nil {
			t.Fatalf("WalkProjectFiles(linked worktree subdir): %v", err)
		}
		got := projectEntryMap(res.Entries)
		if _, ok := got["index.ts"]; !ok {
			t.Fatalf("expected index.ts in linked worktree subdir walk, got %v", res.Entries)
		}
		if _, ok := got["node_modules"]; ok {
			t.Fatalf("expected node_modules excluded via linked worktree ancestor .gitignore, got %v", res.Entries)
		}
	})

	t.Run("standalone non-git directory walks cleanly without ancestor gitignore", func(t *testing.T) {
		t.Parallel()
		standalone := t.TempDir()
		mustWriteProjectFile(t, filepath.Join(standalone, "node_modules", "pkg", "index.js"), "x\n")
		mustWriteProjectFile(t, filepath.Join(standalone, "main.go"), "package main\n")

		res, err := WalkProjectFiles(t.Context(), standalone, ProjectFilesOptions{})
		if err != nil {
			t.Fatalf("WalkProjectFiles(standalone): %v", err)
		}
		got := projectEntryMap(res.Entries)
		if _, ok := got["main.go"]; !ok || !got["node_modules"] {
			t.Fatalf("standalone non-git walk = %v, want both main.go and node_modules", res.Entries)
		}
	})
}

func TestWalkProjectFilesDotfilesAndBFSBudgets(t *testing.T) {
	t.Parallel()

	t.Run("dotfiles excluded by default and included when requested", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		mustWriteProjectFile(t, filepath.Join(root, "src", "app.go"), "package src\n")
		mustWriteProjectFile(t, filepath.Join(root, ".env"), "A=1\n")
		mustWriteProjectFile(t, filepath.Join(root, ".github", "workflows", "test.yml"), "on: push\n")
		mustWriteProjectFile(t, filepath.Join(root, ".git", "config"), "[core]\n")

		withoutDots, err := WalkProjectFiles(t.Context(), root, ProjectFilesOptions{IncludeDotfiles: false})
		if err != nil {
			t.Fatalf("WalkProjectFiles(IncludeDotfiles=false): %v", err)
		}
		gotWithout := projectEntryMap(withoutDots.Entries)
		if _, ok := gotWithout["src/app.go"]; !ok {
			t.Fatalf("missing src/app.go in %v", withoutDots.Entries)
		}
		for _, hidden := range []string{".env", ".github", ".github/workflows/test.yml", ".git", ".git/config"} {
			if _, ok := gotWithout[hidden]; ok {
				t.Fatalf("dotfile %q must stay hidden when IncludeDotfiles=false", hidden)
			}
		}

		withDots, err := WalkProjectFiles(t.Context(), root, ProjectFilesOptions{IncludeDotfiles: true})
		if err != nil {
			t.Fatalf("WalkProjectFiles(IncludeDotfiles=true): %v", err)
		}
		gotWith := projectEntryMap(withDots.Entries)
		for _, want := range []string{".env", ".github", ".github/workflows", ".github/workflows/test.yml", "src/app.go"} {
			if _, ok := gotWith[want]; !ok {
				t.Fatalf("missing %q when IncludeDotfiles=true in %v", want, withDots.Entries)
			}
		}
		if _, ok := gotWith[".git"]; ok {
			t.Fatalf(".git must stay excluded even when IncludeDotfiles=true")
		}
	})

	t.Run("breadth-first order preserves root entries when MaxPaths truncates deep subtree", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		// Alphabetically first directory has a deep subtree that would starve zzz_root.txt in DFS.
		for i := range 6 {
			mustWriteProjectFile(t, filepath.Join(root, "aaa", fmt.Sprintf("lvl%d", i), "deep.txt"), "d\n")
		}
		mustWriteProjectFile(t, filepath.Join(root, "zzz_root.txt"), "top\n")

		res, err := WalkProjectFiles(t.Context(), root, ProjectFilesOptions{
			Limits: ProjectFilesLimits{MaxPaths: 4},
		})
		if err != nil {
			t.Fatalf("WalkProjectFiles: %v", err)
		}
		if !res.Truncated {
			t.Fatalf("expected Truncated=true for MaxPaths=4, got false (%v)", res.Entries)
		}
		if len(res.Entries) != 4 {
			t.Fatalf("got %d entries, want 4: %v", len(res.Entries), res.Entries)
		}
		got := projectEntryMap(res.Entries)
		if _, ok := got["zzz_root.txt"]; !ok {
			t.Fatalf("BFS truncation lost root-level zzz_root.txt: %v", res.Entries)
		}
	})

	t.Run("MaxDirListings stops cleanly with Truncated=true", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		for i := range 5 {
			mustWriteProjectFile(t, filepath.Join(root, fmt.Sprintf("d%d", i), "f.txt"), "x\n")
		}

		res, err := WalkProjectFiles(t.Context(), root, ProjectFilesOptions{
			Limits: ProjectFilesLimits{MaxDirListings: 2},
		})
		if err != nil {
			t.Fatalf("WalkProjectFiles: %v", err)
		}
		if !res.Truncated {
			t.Fatalf("expected Truncated=true when MaxDirListings=2")
		}
		if len(res.Entries) == 0 {
			t.Fatalf("expected shallow entries before truncation")
		}
	})

	t.Run("MaxDirEntries stops cleanly with Truncated=true", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		for i := range 6 {
			mustWriteProjectFile(t, filepath.Join(root, fmt.Sprintf("f%02d.txt", i)), "x\n")
		}

		res, err := WalkProjectFiles(t.Context(), root, ProjectFilesOptions{
			Limits: ProjectFilesLimits{MaxDirEntries: 3},
		})
		if err != nil {
			t.Fatalf("WalkProjectFiles: %v", err)
		}
		if !res.Truncated {
			t.Fatalf("expected Truncated=true when MaxDirEntries=3")
		}
	})

	t.Run("MaxLiveEntries stops cleanly with Truncated=true", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		for i := range 6 {
			mustWriteProjectFile(t, filepath.Join(root, fmt.Sprintf("f%02d.txt", i)), "x\n")
		}

		res, err := WalkProjectFiles(t.Context(), root, ProjectFilesOptions{
			Limits: ProjectFilesLimits{MaxDirEntries: 20, MaxLiveEntries: 3},
		})
		if err != nil {
			t.Fatalf("WalkProjectFiles: %v", err)
		}
		if !res.Truncated {
			t.Fatalf("expected Truncated=true when MaxLiveEntries=3")
		}
	})

	t.Run("MaxIgnoreTotalBytes stops cleanly with Truncated=true", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		// Each .gitignore incurs globIgnoreFileOverheadBytes (512) + path + source + rule*2048.
		mustWriteProjectFile(t, filepath.Join(root, ".gitignore"), "*.tmp\n*.bak\n")
		mustWriteProjectFile(t, filepath.Join(root, "keep.go"), "package main\n")

		res, err := WalkProjectFiles(t.Context(), root, ProjectFilesOptions{
			Limits: ProjectFilesLimits{MaxIgnoreTotalBytes: 1024},
		})
		if err != nil {
			t.Fatalf("WalkProjectFiles: %v", err)
		}
		if !res.Truncated {
			t.Fatalf("expected Truncated=true when MaxIgnoreTotalBytes=1024")
		}
	})

	t.Run("DeadlineExceeded returns Truncated=true while Canceled returns error", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		mustWriteProjectFile(t, filepath.Join(root, "a.go"), "package a\n")

		expiredCtx, cancelExpired := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		defer cancelExpired()

		res, err := WalkProjectFiles(expiredCtx, root, ProjectFilesOptions{})
		if err != nil {
			t.Fatalf("DeadlineExceeded should not return error, got %v", err)
		}
		if !res.Truncated {
			t.Fatalf("expected Truncated=true on DeadlineExceeded")
		}

		canceledCtx, cancelNow := context.WithCancel(t.Context())
		cancelNow()

		_, cerr := WalkProjectFiles(canceledCtx, root, ProjectFilesOptions{})
		if !errors.Is(cerr, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", cerr)
		}
	})
}

// Not parallel: swaps package-level compileIgnoreLines seam to count .gitignore compilations.
func TestWalkProjectFilesIgnoreCacheReuse(t *testing.T) {
	root := t.TempDir()
	mustWriteProjectFile(t, filepath.Join(root, ".gitignore"), "*.log\n")
	mustWriteProjectFile(t, filepath.Join(root, "sub", ".gitignore"), "*.tmp\n")
	mustWriteProjectFile(t, filepath.Join(root, "app.go"), "package app\n")
	mustWriteProjectFile(t, filepath.Join(root, "sub", "mod.go"), "package sub\n")
	mustWriteProjectFile(t, filepath.Join(root, "sub", "skip.tmp"), "skip\n")

	var compiles int
	orig := compileIgnoreLines
	compileIgnoreLines = func(lines ...string) *gitignore.GitIgnore {
		compiles++
		return orig(lines...)
	}
	t.Cleanup(func() { compileIgnoreLines = orig })

	cache := NewProjectFilesIgnoreCache()

	res1, err := WalkProjectFiles(t.Context(), root, ProjectFilesOptions{IgnoreCache: cache})
	if err != nil {
		t.Fatalf("first walk: %v", err)
	}
	if compiles != 2 {
		t.Fatalf("first walk compiled %d matchers, want 2", compiles)
	}
	if slices.ContainsFunc(res1.Entries, func(e ProjectFileEntry) bool { return strings.HasSuffix(e.Path, ".tmp") }) {
		t.Fatalf("expected *.tmp to be ignored in %v", res1.Entries)
	}

	compiles = 0
	if _, err := WalkProjectFiles(t.Context(), root, ProjectFilesOptions{IgnoreCache: cache}); err != nil {
		t.Fatalf("second walk: %v", err)
	}
	if compiles != 0 {
		t.Fatalf("second walk compiled %d matchers, want 0 (cached)", compiles)
	}

	// Modify sub/.gitignore and advance its mtime so the cache invalidates only that file.
	subIgnore := filepath.Join(root, "sub", ".gitignore")
	mustWriteProjectFile(t, subIgnore, "*.tmp\n*.bak\n")
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(subIgnore, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	compiles = 0
	if _, err := WalkProjectFiles(t.Context(), root, ProjectFilesOptions{IgnoreCache: cache}); err != nil {
		t.Fatalf("third walk: %v", err)
	}
	if compiles != 1 {
		t.Fatalf("third walk after editing sub/.gitignore compiled %d matchers, want 1", compiles)
	}
}

// Not parallel: swaps package-level compileIgnoreLines seam to count ancestor .gitignore compilations.
func TestWalkProjectFilesAncestorIgnoreCacheAndSymlink(t *testing.T) {
	outside := t.TempDir()
	mustWriteProjectFile(t, filepath.Join(outside, "evil.gitignore"), "*\n")

	repoRoot := t.TempDir()
	mustWriteProjectFile(t, filepath.Join(repoRoot, ".git", "HEAD"), "ref: refs/heads/main\n")
	mustWriteProjectFile(t, filepath.Join(repoRoot, ".gitignore"), "node_modules/\n")
	mustWriteProjectFile(t, filepath.Join(repoRoot, "apps", ".gitignore"), "dist/\n")
	mustWriteProjectFile(t, filepath.Join(repoRoot, "docs-site", "node_modules", "a.js"), "x\n")
	mustWriteProjectFile(t, filepath.Join(repoRoot, "docs-site", "page.md"), "# docs\n")
	mustWriteProjectFile(t, filepath.Join(repoRoot, "apps", "web", "dist", "out.js"), "x\n")
	mustWriteProjectFile(t, filepath.Join(repoRoot, "apps", "web", "app.ts"), "export {};\n")

	var compiles int
	orig := compileIgnoreLines
	compileIgnoreLines = func(lines ...string) *gitignore.GitIgnore {
		compiles++
		return orig(lines...)
	}
	t.Cleanup(func() { compileIgnoreLines = orig })

	cache := NewProjectFilesIgnoreCache()
	if _, err := WalkProjectFiles(t.Context(), filepath.Join(repoRoot, "docs-site"), ProjectFilesOptions{IgnoreCache: cache}); err != nil {
		t.Fatalf("first docs-site walk: %v", err)
	}
	if compiles != 1 {
		t.Fatalf("first docs-site walk compiled %d matchers, want 1 (repoRoot/.gitignore)", compiles)
	}

	compiles = 0
	if _, err := WalkProjectFiles(t.Context(), filepath.Join(repoRoot, "docs-site"), ProjectFilesOptions{IgnoreCache: cache}); err != nil {
		t.Fatalf("second docs-site walk: %v", err)
	}
	if compiles != 0 {
		t.Fatalf("second docs-site walk compiled %d matchers, want 0 (cached)", compiles)
	}

	if _, err := WalkProjectFiles(t.Context(), filepath.Join(repoRoot, "apps", "web"), ProjectFilesOptions{IgnoreCache: cache}); err != nil {
		t.Fatalf("apps/web walk: %v", err)
	}
	if compiles != 1 {
		t.Fatalf("apps/web walk compiled %d matchers, want 1 (only apps/.gitignore; repoRoot/.gitignore cached)", compiles)
	}

	// Symlinked ancestor .gitignore outside repo must be ignored safely without error.
	symRepo := t.TempDir()
	mustWriteProjectFile(t, filepath.Join(symRepo, ".git", "HEAD"), "ref: refs/heads/main\n")
	if err := os.Symlink(filepath.Join(outside, "evil.gitignore"), filepath.Join(symRepo, ".gitignore")); err != nil {
		t.Fatalf("symlink .gitignore: %v", err)
	}
	mustWriteProjectFile(t, filepath.Join(symRepo, "sub", "keep.go"), "package sub\n")
	res, err := WalkProjectFiles(t.Context(), filepath.Join(symRepo, "sub"), ProjectFilesOptions{})
	if err != nil {
		t.Fatalf("walk under symlinked ancestor .gitignore: %v", err)
	}
	if _, ok := projectEntryMap(res.Entries)["keep.go"]; !ok {
		t.Fatalf("expected keep.go when ancestor .gitignore is symlink, got %v", res.Entries)
	}
}

func mustWriteProjectFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func projectEntryMap(entries []ProjectFileEntry) map[string]bool {
	out := make(map[string]bool, len(entries))
	for _, e := range entries {
		out[e.Path] = e.IsDir
	}
	return out
}
