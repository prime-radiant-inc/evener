//go:build darwin

package execenv

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// TestDarwinStartTokenReadsKernelStartTime pins the Darwin instance proof
// against the current process: the token comes from the kernel's kinfo_proc,
// not from a wall clock this process could forge.
func TestDarwinStartTokenReadsKernelStartTime(t *testing.T) {
	token, err := darwinStartToken(os.Getpid())
	if err != nil {
		t.Fatalf("darwinStartToken(self): %v", err)
	}
	if token == "" {
		t.Fatal("the kernel start token is empty")
	}
	other, err := darwinStartToken(os.Getpid())
	if err != nil || other != token {
		t.Fatalf("darwinStartToken is not stable: %q then %q (%v)", token, other, err)
	}
	if _, err := darwinStartToken(0); !errors.Is(err, ErrBoundaryMemberGone) {
		t.Fatalf("darwinStartToken(0) = %v, want ErrBoundaryMemberGone", err)
	}
}

// TestDarwinBoundaryMembersExcludeTheLauncher pins the owner rule: the
// launcher's own pid equals the boundary pair, so counting it would make a live
// launcher's boundary never enumerate empty.
func TestDarwinBoundaryMembersExcludeTheLauncher(t *testing.T) {
	pid := os.Getpid()
	pgid, err := unix.Getpgid(pid)
	if err != nil {
		t.Fatalf("Getpgid: %v", err)
	}
	session, err := unix.Getsid(pid)
	if err != nil {
		t.Fatalf("Getsid: %v", err)
	}
	members, err := darwinBoundaryMembers(pgid, session)
	if err != nil {
		t.Fatalf("darwinBoundaryMembers: %v", err)
	}
	for _, member := range members {
		if member.PID == pid {
			t.Fatal("the boundary owner counted itself as a member")
		}
	}
}
