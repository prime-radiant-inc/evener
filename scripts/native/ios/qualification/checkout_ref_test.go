//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckoutRefChoosesHeadOrMerge drives the workflow's checkout selector
// against a real repository: a head that predates the harness falls back to
// the merge ref that contains it, and a head that carries it is tested as
// pushed. The local fixture is the same object shape a pull_request merge ref
// has, with the head as one of the merge commit's parents.
func TestCheckoutRefChoosesHeadOrMerge(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL="+os.DevNull,
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=fixture",
			"GIT_AUTHOR_EMAIL=fixture@example.invalid",
			"GIT_COMMITTER_NAME=fixture",
			"GIT_COMMITTER_EMAIL=fixture@example.invalid",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(path, data string) {
		t.Helper()
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(message string) string {
		t.Helper()
		git("add", "-A")
		git("commit", "-qm", message)
		return git("rev-parse", "HEAD")
	}

	git("init", "-q")
	git("checkout", "-q", "-b", "main")
	write("mobile-native/README", "root\n")
	root := commit("root without the qualification harness")

	write("scripts/native/ios/build-simulator.sh", "#!/bin/sh\n")
	write("scripts/native/ios/qualification/policy.go", "package main\n")
	write("scripts/native/ios/run-ui-tests.sh", "#!/bin/sh\n")
	base := commit("add the qualification harness")

	// A stale PR branch from before the harness, touching an unrelated native
	// file. It carries the older worktree script but predates the qualification
	// directory the workflow's policy step calls, the failure #5993 recorded.
	// The merge ref has both that change and main's harness.
	git("checkout", "-q", "-b", "stale", root)
	write("mobile-native/stale.txt", "stale\n")
	write("scripts/native/ios/build-simulator.sh", "#!/bin/sh\n")
	stale := commit("stale native change")
	git("checkout", "-q", "main")
	git("merge", "--no-ff", "-q", "-m", "merge stale", "stale")
	staleMerge := git("rev-parse", "HEAD")

	// A current PR branch that carries the harness itself.
	git("checkout", "-q", "-b", "current", base)
	write("mobile-native/current.txt", "current\n")
	current := commit("current native change")
	git("checkout", "-q", "main")
	git("merge", "--no-ff", "-q", "-m", "merge current", "current")
	currentMerge := git("rev-parse", "HEAD")

	script, err := filepath.Abs("checkout-ref.sh")
	if err != nil {
		t.Fatal(err)
	}
	selectRef := func(head, merge string) (string, string) {
		t.Helper()
		cmd := exec.Command("bash", script, head, merge,
			"scripts/native/ios/build-simulator.sh",
			"scripts/native/ios/qualification",
			"scripts/native/ios/run-ui-tests.sh",
		)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("checkout-ref %s %s: %v\n%s", head, merge, err, out)
		}
		ref, reason := "", ""
		for _, line := range strings.Split(string(out), "\n") {
			switch {
			case strings.HasPrefix(line, "ref="):
				ref = strings.TrimPrefix(line, "ref=")
			case strings.HasPrefix(line, "reason="):
				reason = strings.TrimPrefix(line, "reason=")
			}
		}
		return ref, reason
	}

	ref, reason := selectRef(stale, staleMerge)
	if ref != staleMerge {
		t.Fatalf("stale head selected %s, want the merge ref %s", ref, staleMerge)
	}
	if !strings.Contains(reason, "scripts/native/ios/qualification") {
		t.Fatalf("stale head reason %q does not name the missing harness path", reason)
	}
	ref, _ = selectRef(current, currentMerge)
	if ref != current {
		t.Fatalf("current head selected %s, want the PR head %s", ref, current)
	}

	// The selector must refuse a commit it cannot read, not silently pick one.
	cmd := exec.Command("bash", script, strings.Repeat("0", 40), currentMerge, "scripts/native/ios/qualification")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("accepted an unknown commit:\n%s", out)
	}
}
