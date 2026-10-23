//go:build linux

package sshconn

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestRemoteSelectedLockRejectsNonregular(t *testing.T) {
	for _, phase := range []string{"initial-symlink", "initial-only", "directory", "missing", "before-read", "before-read-only", "before-acceptance"} {
		t.Run(phase, func(t *testing.T) {
			base := t.TempDir()
			rootA, rootB := filepath.Join(base, "root A"), filepath.Join(base, "root B")
			for _, root := range []string{rootA, rootB} {
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
			}
			lockA, lockB := filepath.Join(rootA, "hub.lock"), filepath.Join(rootB, "hub.lock")
			const contents = "original owner lock\n"
			if err := os.WriteFile(lockA, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			holder, err := os.OpenFile(lockA, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = holder.Close() })
			if err := unix.Flock(int(holder.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			before, err := holder.Stat()
			if err != nil {
				t.Fatal(err)
			}
			switch phase {
			case "initial-symlink":
				err = os.Symlink(lockA, lockB)
			case "initial-only":
				// Symlink only for the initial stat. If the initial guard is
				// removed, the wrapper restores a regular hard link so the later
				// guards cannot be what refuses.
				err = os.Symlink(lockA, lockB)
			case "directory":
				err = os.Mkdir(lockB, 0700)
			case "missing":
			case "before-read-only":
				// Regular for the initial stat, symlink only for the pre-read
				// guard, regular again before acceptance.
				err = os.Link(lockA, lockB)
			default:
				// Replacing this hard link with a symlink preserves the followed
				// device/inode, so the existing identity pin alone cannot refuse it.
				err = os.Link(lockA, lockB)
			}
			if err != nil {
				t.Fatal(err)
			}
			// These functions inject a concurrent pathname replacement, not a
			// different stat identity or synthetic /proc/locks observation.
			marker := filepath.Join(base, "phase-marker")
			const prefix = `
stat() {
 command stat "$@" || return
 if [ "$phase" = before-read ]; then
  rm -- "$lockB" && ln -s -- "$lockA" "$lockB" || return
 fi
 if [ "$phase" = initial-only ]; then
  rm -- "$lockB" && ln -- "$lockA" "$lockB" || return
 fi
 if [ "$phase" = before-read-only ] && [ ! -e "$marker" ]; then
  : > "$marker" || return
  rm -- "$lockB" && ln -s -- "$lockA" "$lockB" || return
 fi
}
awk() {
 command awk "$@" || return
 if [ "$phase" = before-acceptance ]; then
  rm -- "$lockB" && ln -s -- "$lockA" "$lockB" || return
 fi
 if [ "$phase" = before-read-only ]; then
  rm -- "$lockB" && ln -- "$lockA" "$lockB" || return
 fi
}
`
			calls := 0
			fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, _ io.Reader) ([]byte, error) {
				calls++
				cmd := exec.CommandContext(ctx, "/bin/sh", "-c", prefix+strings.Join(argv[12:], " "))
				cmd.Env = append(selectedLockToolEnv(t), "phase="+phase, "lockA="+lockA, "lockB="+lockB, "marker="+marker)
				return cmd.Output()
			}}
			m := newTestManager(t, testRegistry(t), fr, Options{})
			out, probeErr := m.ownerRead(t.Context(), hostreg.Host{Name: "primary-lock", SSH: "fixture"}, selectedLockRemote(rootB))
			var exit *exec.ExitError
			if !errors.Is(probeErr, errOwnerEvidence) || !errors.As(probeErr, &exit) || exit.ExitCode() == 0 || len(out) != 0 || calls != 1 {
				t.Errorf("nonregular selected lock: output=%q error=%v calls=%d", out, probeErr, calls)
			}
			after, statErr := os.Stat(lockA)
			data, readErr := os.ReadFile(lockA)
			if statErr != nil || readErr != nil || !os.SameFile(before, after) || string(data) != contents {
				t.Fatalf("observer changed original lock: stat=%v read=%v contents=%q", statErr, readErr, data)
			}
			contender, err := os.OpenFile(lockA, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer contender.Close()
			if err := unix.Flock(int(contender.Fd()), unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
				t.Fatalf("original owner's exclusive lock was lost: %v", err)
			}
		})
	}
}
