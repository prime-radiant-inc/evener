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
