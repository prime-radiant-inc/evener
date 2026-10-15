//go:build linux || darwin

package execenv

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"primeradiant.com/evener/agent/sandbox"
)

// admitReadFD's streamed flag mirrors openRegularFollow's on the sandboxed
// arm: a regular file whose path names pseudo-filesystem content keeps
// O_NONBLOCK — the runtime poller owns the descriptor, so the capped read's
// deadline can interrupt a stream like /proc/kmsg that never yields — while
// an ordinary regular file gets the ordinary blocking descriptor back.
func TestAdmitReadFDKeepsNonblockingForStreamedRegulars(t *testing.T) {
	// Not parallel: stubPseudoFsRoots swaps the package's root list.
	root := t.TempDir()
	pseudo := filepath.Join(root, "pseudo")
	if err := os.MkdirAll(pseudo, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(pseudo, "keep.txt")
	if err := os.WriteFile(keep, []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(root, "plain.txt")
	if err := os.WriteFile(plain, []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stubPseudoFsRoots(t, pseudo)

	for _, tc := range []struct {
		name     string
		path     string
		streamed bool
		wantSet  bool
	}{
		{"pseudo-rooted regular file", keep, true, true},
		{"ordinary regular file", plain, false, false},
	} {
		fd, err := unix.Open(tc.path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		if err != nil {
			t.Fatalf("%s: open: %v", tc.name, err)
		}
		f, err := admitReadFD(fd, tc.path, false, tc.streamed)
		if err != nil {
			t.Fatalf("%s: admit: %v", tc.name, err)
		}
		set := fdNonblocking(t, f)
		_ = f.Close()
		if set != tc.wantSet {
			t.Fatalf("%s: O_NONBLOCK set = %v, want %v", tc.name, set, tc.wantSet)
		}
	}
}

// The sandboxed arm reads a named target under a (stubbed) pseudo root the
// same way: its matches come back, and keeping the nonblocking flag on a real
// disk file changes nothing about what the search finds.
func TestSandboxedGrepReadsAStubbedPseudoRootedRegularFile(t *testing.T) {
	// Not parallel: stubPseudoFsRoots swaps the package's root list.
	env, _, worktree := sandboxedEnv(t, sandbox.ModeRestricted)
	pseudo := filepath.Join(worktree, "pseudo")
	if err := os.MkdirAll(pseudo, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(pseudo, "keep.txt")
	if err := os.WriteFile(keep, []byte("needle here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stubPseudoFsRoots(t, pseudo)

	got, err := env.Grep(t.Context(), "needle", keep, "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	if want := "1:needle here"; got != want {
		t.Fatalf("sandboxed named pseudo-rooted regular = %q, want %q", got, want)
	}
}
