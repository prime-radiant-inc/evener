package plugins

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeGitRepo initialises a git repo at dir with one file and returns its HEAD sha.
func makeGitRepo(t *testing.T, dir string, file, content string) string {
	t.Helper()
	if !gitAvailable() {
		t.Skip("git not available")
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	out, _ := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	return strings.TrimSpace(string(out))
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

func TestGitClone_RejectsFlagLikeArgs(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "dst")
	if err := gitClone(context.Background(), "--upload-pack=evil", dst, "", ""); err == nil {
		t.Fatal("gitClone accepted a flag-like url; want rejection")
	}
	if err := gitClone(context.Background(), "https://example/x.git", dst, "-x", ""); err == nil {
		t.Fatal("gitClone accepted a flag-like ref; want rejection")
	}
}
