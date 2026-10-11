//go:build unix

package execenv

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"

	"primeradiant.com/evener/agent/sandbox"
)

// A carve-out root replaced in place while an operation holds the layer keeps
// its old descriptor open and usable for that operation, and the final
// release closes it, neither early nor never.
func TestStaleCarveOutFdStaysOpenWhileHeldAndClosesOnFinalRelease(t *testing.T) {
	home := realTempDir(t)
	worktree := filepath.Join(home, "project")
	skills := filepath.Join(home, ".config", "evener", "skills")
	skill := filepath.Join(skills, "mine", "SKILL.md")
	writeSkill := func(content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(skill, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkill("old\n")
	host := sbTestHost(home)
	host.EvenerContentRoots = []string{skills}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted}, host, worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(rp.UnmaskedRoots) == 0 {
		t.Fatalf("fixture: no carve-out root: %v", rp.UnmaskedRoots)
	}
	env := NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)

	sfs := env.sandbox() // an operation holding the layer across the replacement
	release := sync.OnceFunc(sfs.release)
	t.Cleanup(release)
	oldFd, err := sfs.rootFd(rp.UnmaskedRoots[0])
	if err != nil {
		t.Fatalf("rootFd: %v", err)
	}
	var before unix.Stat_t
	if err := unix.Fstat(oldFd, &before); err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(skills, skills+".old"); err != nil {
		t.Fatal(err)
	}
	writeSkill("new\n")
	// A new read drops the replaced root's descriptor from the cache.
	if _, err := env.ReadFile(skill, nil, nil); err != nil {
		t.Fatalf("read after the replacement: %v", err)
	}

	fd, err := unix.Openat(oldFd, filepath.Join("mine", "SKILL.md"), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("the held old descriptor must stay usable: %v", err)
	}
	f := os.NewFile(uintptr(fd), "old skill")
	got, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil || !strings.Contains(string(got), "old") {
		t.Errorf("the held old descriptor must still reach the old tree, read %q (%v)", got, err)
	}

	release()
	var after unix.Stat_t
	if err := unix.Fstat(oldFd, &after); err == nil && after.Ino == before.Ino && after.Dev == before.Dev {
		t.Errorf("the old descriptor must be closed on the final release")
	}
}
