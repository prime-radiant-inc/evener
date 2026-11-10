//go:build linux

package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// localShellRunner executes a remote probe through the local /bin/sh. When
// omitGlobal is set, it rewrites the global /proc/locks read to a successful
// empty observation, the shape concurrent lock churn produced in real runs,
// without touching the candidate's own fdinfo read.
func localShellRunner(t *testing.T, omitGlobal bool) *fakeRunner {
	t.Helper()
	env := append(selectedLockToolEnv(t), "PATH=/usr/bin:/bin")
	return &fakeRunner{runFn: func(ctx context.Context, argv []string, _ io.Reader) ([]byte, error) {
		remote := strings.Join(argv[12:], " ")
		prefix := ""
		if omitGlobal && !strings.Contains(remote, "/fdinfo/") {
			prefix = `awk() { [ "$#" -eq 4 ] && [ "$4" = /proc/locks ] && return 0; command awk "$@"; }; `
		}
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", prefix+remote)
		cmd.Env = env
		return cmd.Output()
	}}
}

func heldSelectedLock(t *testing.T, root string) string {
	t.Helper()
	lockPath := filepath.Join(root, "hub.lock")
	const sentinel = "test-owned lock bytes\n"
	if err := os.WriteFile(lockPath, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(lockPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		_ = lock.Close()
	})
	return sentinel
}

// TestRemoteSelectedLockCandidateProof covers the completed-but-empty global
// /proc/locks read: the per-process candidate proof must still name the holder,
// must reject a live process that does not hold the lock, and must leave both
// the lock bytes and the inode untouched.
func TestRemoteSelectedLockCandidateProof(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "selected root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := heldSelectedLock(t, root)
	lockPath := filepath.Join(root, "hub.lock")
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	nonHolder := exec.Command("/bin/sleep", "30")
	if err := nonHolder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = nonHolder.Process.Kill()
		_, _ = nonHolder.Process.Wait()
	})

	host := hostreg.Host{Name: "injected", SSH: "fixture"}
	fr := localShellRunner(t, true)
	m := newTestManager(t, testRegistry(t), fr, Options{})
	pid := fmt.Sprintf("%d", os.Getpid())

	// Guard: the injected global read ran to completion and observed nothing.
	if got, err := m.selectedLockPID(t.Context(), host, root); got != "" || !errors.Is(err, errNoUniqueLockOwner) {
		t.Fatalf("global probe = (%q, %v), want the injected empty observation", got, err)
	}
	// The candidate's own fdinfo is independent of that global list.
	if got, err := m.selectedLockPIDPreferring(t.Context(), host, root, pid); err != nil || got != pid {
		t.Fatalf("candidate proof = (%q, %v), want %s", got, err, pid)
	}
	// A live candidate that holds no lock is not authority.
	if got, err := m.selectedLockPIDPreferring(t.Context(), host, root, fmt.Sprintf("%d", nonHolder.Process.Pid)); err == nil || got != "" {
		t.Fatalf("non-holder candidate = (%q, %v), want refusal", got, err)
	}
	// A hostile candidate is refused before building any remote command.
	beforeHostile := len(fr.recordedRuns())
	if got, err := m.selectedLockPIDPreferring(t.Context(), host, root, "4242; marker"); err == nil || got != "" {
		t.Fatalf("hostile candidate = (%q, %v), want refusal", got, err)
	}
	if runs := len(fr.recordedRuns()); runs != beforeHostile+1 {
		t.Fatalf("hostile candidate issued %d remote commands, want only the global probe", runs-beforeHostile)
	}

	after, statErr := os.Stat(lockPath)
	contents, readErr := os.ReadFile(lockPath)
	if statErr != nil || readErr != nil || string(contents) != sentinel || !os.SameFile(before, after) {
		t.Fatalf("candidate proof mutated the lock: stat=%v read=%v bytes=%q", statErr, readErr, contents)
	}
}

// TestRemoteSelectedLockCandidateRefusesAmbiguousOrFailedGlobalProbe pins the
// boundary: the candidate proof supplements only a completed empty read. A
// transport failure, a changed identity, or a malformed/ambiguous owner keeps
// its existing refusal and must not issue the candidate probe.
func TestRemoteSelectedLockCandidateRefusesAmbiguousOrFailedGlobalProbe(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  []byte
		err  error
	}{
		{name: "multiple owners", out: []byte("4242\n5151\n")},
		{name: "malformed owner", out: []byte("bogus\n")},
		{name: "zero owner", out: []byte("0\n")},
		{name: "transport failure", err: errors.New("injected transport failure")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runs := 0
			fr := &fakeRunner{runFn: func(context.Context, []string, io.Reader) ([]byte, error) {
				runs++
				return tc.out, tc.err
			}}
			m := newTestManager(t, testRegistry(t), fr, Options{})
			host := hostreg.Host{Name: "injected", SSH: "fixture"}
			got, err := m.selectedLockPIDPreferring(t.Context(), host, "/srv/state", "4242")
			if got != "" || !errors.Is(err, errOwnerEvidence) {
				t.Fatalf("preferring = (%q, %v), want owner evidence refusal", got, err)
			}
			if runs != 1 {
				t.Fatalf("candidate proof ran after a non-empty/failed global probe: %d commands", runs)
			}
		})
	}
}

// TestRemoteSelectedLockCandidateProofUnderChurn holds a real exclusive flock
// while unrelated locks change and proves the candidate proof still names the
// holder on every read. The global list is deliberately under the churn that
// can omit or duplicate a row; this proof never reads it.
func TestRemoteSelectedLockCandidateProofUnderChurn(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "selected root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	heldSelectedLock(t, root)

	const churnLocks = 64
	churn := make([]*os.File, 0, churnLocks)
	for i := range churnLocks {
		f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("%d.lock", i)), os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
		churn = append(churn, f)
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	for worker := range 2 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for {
				for i := worker; i < len(churn); i += 2 {
					select {
					case <-done:
						return
					default:
					}
					fd := int(churn[i].Fd())
					_ = unix.Flock(fd, unix.LOCK_UN)
					_ = unix.Flock(fd, unix.LOCK_EX)
				}
				runtime.Gosched()
			}
		}(worker)
	}
	// A descriptor opening and closing between the glob and its fdinfo read is
	// the vanish the production probe must tolerate; the lock churn alone only
	// toggles existing descriptors.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			f, err := os.CreateTemp(dir, "descriptor-churn")
			if err != nil {
				return
			}
			name := f.Name()
			_ = f.Close()
			_ = os.Remove(name)
		}
	}()
	t.Cleanup(func() {
		close(done)
		wg.Wait()
		for _, f := range churn {
			_ = f.Close()
		}
	})

	host := hostreg.Host{Name: "injected", SSH: "fixture"}
	m := newTestManager(t, testRegistry(t), localShellRunner(t, false), Options{})
	pid := fmt.Sprintf("%d", os.Getpid())
	for i := 0; i < 20; i++ {
		got, err := m.selectedLockCandidatePID(t.Context(), host, root, pid)
		if err != nil || got != pid {
			t.Fatalf("churn proof %d = (%q, %v), want %s", i, got, err, pid)
		}
	}
}

// TestRemoteSelectedLockCandidateToleratesVanishedDescriptor pins the
// pipefail case: grep returns 2 for a descriptor that closed mid-read while
// still printing the held row. The grouped `|| true` keeps the probe alive.
func TestRemoteSelectedLockCandidateToleratesVanishedDescriptor(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "selected root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	heldSelectedLock(t, root)
	pid := fmt.Sprintf("%d", os.Getpid())
	script := `grep() { command grep "$@"; return 2; }; ` + selectedLockCandidateRemote(root, pid)
	cmd := exec.Command("/bin/bash", "-o", "pipefail", "-c", script)
	cmd.Env = append(selectedLockToolEnv(t), "PATH=/usr/bin:/bin")
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != pid {
		t.Fatalf("candidate probe = (%q, %v), want %s despite grep exit 2", out, err, pid)
	}
}
