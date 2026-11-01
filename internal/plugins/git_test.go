package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/gittest"
)

// makeGitRepo initialises a git repo at dir with one file and returns its HEAD sha.
func makeGitRepo(t *testing.T, dir string, file, content string) string {
	t.Helper()
	skipWithoutGit(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, dir, "add", ".")
	gittest.Run(t, dir, "commit", "-q", "-m", "init")
	return gittest.Run(t, dir, "rev-parse", "HEAD")
}

func TestGitClone_CopiesRepoAtSha(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	sha := makeGitRepo(t, src, "plugin.txt", "hello")

	dst := filepath.Join(t.TempDir(), "dst")
	if err := gitClone(context.Background(), src, dst, "", sha); err != nil {
		t.Fatalf("gitClone: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "plugin.txt"))
	if err != nil || string(b) != "hello" {
		t.Fatalf("cloned file = %q, err %v", b, err)
	}
	got, err := gitHeadSHA(context.Background(), dst)
	if err != nil || got != sha {
		t.Fatalf("HEAD = %q err %v, want %q", got, err, sha)
	}
}

func TestGitRefNamesBranch_IgnoresATagInTheBranchNamespace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "src")
	makeGitRepo(t, dir, "file.txt", "hello")
	gittest.Run(t, dir, "tag", "refs/heads/x")

	branch, err := gitRefNamesBranch(context.Background(), dir, "x")
	if err != nil {
		t.Fatal(err)
	}
	if branch {
		t.Fatal("a tag named refs/heads/x is not a local branch")
	}
}

func TestGitClone_ShaPinIsNotShadowedByATag(t *testing.T) {
	skipWithoutGit(t)
	src := filepath.Join(t.TempDir(), "src")
	first := makeGitRepo(t, src, "file.txt", "v1")
	short := first[:7]
	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, src, "commit", "-qam", "v2")
	gittest.Run(t, src, "tag", short)

	dst := filepath.Join(t.TempDir(), "dst")
	if err := gitClone(context.Background(), src, dst, "", short); err != nil {
		t.Fatalf("gitClone: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v1" {
		t.Fatalf("cloned file = %q, want the pinned commit's v1", got)
	}
}

func TestGitSparseClone_ShaPinIsNotShadowedByATag(t *testing.T) {
	skipWithoutGit(t)
	src := filepath.Join(t.TempDir(), "src")
	makeGitRepo(t, src, "README.md", "root")
	if err := os.MkdirAll(filepath.Join(src, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "plugins", "plugin.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, src, "add", "plugins")
	gittest.Run(t, src, "commit", "-qm", "v1")
	first := gittest.Run(t, src, "rev-parse", "HEAD")
	short := first[:7]
	if err := os.WriteFile(filepath.Join(src, "plugins", "plugin.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, src, "commit", "-qam", "v2")
	gittest.Run(t, src, "tag", short)

	dst := filepath.Join(t.TempDir(), "dst")
	if err := gitSparseClone(context.Background(), src, dst, "plugins", "", short); err != nil {
		t.Fatalf("gitSparseClone: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "plugins", "plugin.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v1" {
		t.Fatalf("cloned file = %q, want the pinned commit's v1", got)
	}
}

func TestGitClone_RejectsFlagLikeArgs(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "dst")
	if err := gitClone(context.Background(), "--upload-pack=evil", dst, "", ""); err == nil {
		t.Fatal("gitClone accepted a flag-like url; want rejection")
	}
	if err := gitClone(context.Background(), "https://example/x.git", dst, "-x", ""); err == nil {
		t.Fatal("gitClone accepted a flag-like ref; want rejection")
	}
}

func TestFetchPluginSource_RejectsExtTransport(t *testing.T) {
	skipWithoutGit(t)
	marker := filepath.Join(t.TempDir(), "PWNED")
	dst := filepath.Join(t.TempDir(), "out")
	url := "ext::sh -c \"touch " + marker + "\""
	if _, err := fetchPluginSource(context.Background(), Source{Kind: SourceURL, URL: url}, "", dst); err == nil {
		t.Fatal("ext:: transport was allowed (should be blocked)")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("ext:: command executed — RCE not blocked")
	}
}
