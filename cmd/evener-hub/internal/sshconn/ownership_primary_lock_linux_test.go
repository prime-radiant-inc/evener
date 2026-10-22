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
	for _, phase := range []string{"initial-symlink", "directory", "missing", "before-read", "before-acceptance"} {
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
			case "directory":
				err = os.Mkdir(lockB, 0700)
			case "missing":
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
			const prefix = `
stat() {
 command stat "$@" || return
 if [ "$phase" = before-read ]; then
  rm -- "$lockB" && ln -s -- "$lockA" "$lockB" || return
 fi
}
awk() {
 command awk "$@" || return
 if [ "$phase" = before-acceptance ]; then
  rm -- "$lockB" && ln -s -- "$lockA" "$lockB" || return
 fi
}
`
			calls := 0
			fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, _ io.Reader) ([]byte, error) {
				calls++
				cmd := exec.CommandContext(ctx, "/bin/sh", "-c", prefix+strings.Join(argv[12:], " "))
				cmd.Env = append(selectedLockToolEnv(t), "phase="+phase, "lockA="+lockA, "lockB="+lockB)
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
