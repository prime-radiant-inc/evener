//go:build unix

package execenv

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"primeradiant.com/evener/agent/sandbox"
)

// A carve-out root replaced in place while an operation holds the layer keeps
// its old descriptor open and usable for that operation, serves the
// replacement to new reads, and closes the old descriptor on the final
// release, neither early nor never.
func TestStaleCarveOutFdStaysOpenWhileHeldAndClosesOnFinalRelease(t *testing.T) {
	home := realTempDir(t)
	worktree := filepath.Join(home, "project")
	skills := filepath.Join(home, ".config", "evener", "skills")
	skill := filepath.Join(skills, "mine", "SKILL.md")
	for _, dir := range []string{worktree, filepath.Dir(skill)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(skill, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	host := sbTestHost(home)
	host.EvenerContentRoots = []string{skills}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted}, host, worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	t.Cleanup(env.Cleanup)
	if _, err := env.ReadFile(skill, nil, nil); err != nil {
		t.Fatalf("first read: %v", err)
	}
	env.sbMu.Lock()
	sfs := env.sbfs
	sfs.acquire() // an operation that holds the layer across the replacement
	env.sbMu.Unlock()
	held := true
	t.Cleanup(func() {
		if held {
			sfs.release()
		}
	})
	if len(rp.UnmaskedRoots) == 0 {
		t.Fatalf("fixture: no carve-out root: %v", rp.UnmaskedRoots)
	}
	root := rp.UnmaskedRoots[0]
	oldFd, err := sfs.rootFd(root)
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
	if err := os.MkdirAll(filepath.Dir(skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := env.ReadFile(skill, nil, nil); err != nil || !strings.Contains(got, "new") {
		t.Fatalf("a read after the replacement must see the new content: %q %v", got, err)
	}

	sfs.mu.Lock()
	staleHeld := slices.Contains(sfs.staleFds, oldFd)
	sfs.mu.Unlock()
	if !staleHeld {
		t.Fatalf("the replaced root's descriptor must wait in staleFds while the layer is held")
	}
	fd, err := unix.Openat(oldFd, filepath.Join("mine", "SKILL.md"), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("the held old descriptor must stay usable: %v", err)
	}
	buf := make([]byte, 16)
	n, _ := unix.Read(fd, buf)
	_ = unix.Close(fd)
	if !strings.Contains(string(buf[:n]), "old") {
		t.Errorf("the held old descriptor must still reach the old tree, read %q", buf[:n])
	}

	held = false
	sfs.release()
	var after unix.Stat_t
	if err := unix.Fstat(oldFd, &after); err == nil && after.Ino == before.Ino && after.Dev == before.Dev {
		t.Errorf("the old descriptor must be closed on the final release")
	}
	sfs.mu.Lock()
	left := len(sfs.staleFds)
	sfs.mu.Unlock()
	if left != 0 {
		t.Errorf("staleFds must be empty after the final release, %d left", left)
	}
}
