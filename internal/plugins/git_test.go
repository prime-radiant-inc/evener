package plugins

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

func TestFetchPluginSource_RejectsExtTransport(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
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

// waitForFile polls until path exists or the deadline passes.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("file %s never appeared", path)
}

// TestGit_CancellationSendsSIGTERM pins the graceful-cancellation contract of
// the git() helper: on context cancellation the child must receive SIGTERM —
// git's signal handlers remove its own lock files (.git/index.lock etc.) on
// termination — rather than exec's default SIGKILL, which strands those lock
// files and wedges persistent clones. A fake `git` first in PATH traps TERM
// and records its delivery.
func TestGit_CancellationSendsSIGTERM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal-delivery test needs a POSIX shell")
	}
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	termed := filepath.Join(dir, "termed")
	script := "#!/bin/sh\n" +
		"trap 'touch " + termed + "; exit 143' TERM\n" +
		"touch " + started + "\n" +
		// Sleep in short interruptible slices; redirect so the background
		// sleep never holds git()'s output pipe open past the shell's exit.
		"i=0; while [ $i -lt 300 ]; do sleep 0.1 >/dev/null 2>&1 & wait $!; i=$((i+1)); done\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := git(ctx, "", "fetch")
		errCh <- err
	}()
	waitForFile(t, started)
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("git() returned nil after cancellation; want error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("git() did not return after cancellation")
	}
	waitForFile(t, termed)
}
