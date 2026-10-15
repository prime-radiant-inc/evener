//go:build unix

package transcriptindex

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

// A genuine lock fault must not masquerade as contention (#4497 r9): the
// refusing handle's operations translate ErrLockBusy into "another writer
// holds the lock", which the hub answers as rebuilding-or-transient and the
// message-search refresher skips silently every cycle — so a fault folded
// into ErrLockBusy is undiagnosable forever. The handle's lock fd dies behind
// its back: flock answers EBADF, which no contention ever produces.
func TestALockFaultOnARefusingHandleIsAnOrdinaryErrorNotContention(t *testing.T) {
	path, dir := openCommittedSidecar(t, fixture{name: "faulted", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}})
	x := openRefusing(t, path, dir)
	x.mu.Lock()
	fd := int(x.lock.Fd())
	x.mu.Unlock()
	if err := unix.Close(fd); err != nil {
		t.Fatal(err)
	}
	err := x.CatchUp()
	if errors.Is(err, ErrLockBusy) {
		t.Fatalf("a lock fault masqueraded as contention: %v", err)
	}
	if err == nil {
		t.Fatal("the dead lock fd did not fail the refusing catch-up")
	}
	if !errors.Is(err, unix.EBADF) {
		t.Fatalf("err = %v, want the lock fault kept ordinary and wrapped", err)
	}
}

// The contention errno alone is contention: the refresher's silent skip and
// the hub's in-flight consult stay keyed to exactly the busy case, while
// every other locking errno (a dead fd, an exhausted lock table, an I/O
// fault) surfaces as itself.
func TestIsLockContentionClassifiesOnlyTheContentionErrno(t *testing.T) {
	if !isLockContention(unix.EWOULDBLOCK) {
		t.Fatal("EWOULDBLOCK is the contention errno")
	}
	for _, errno := range []unix.Errno{unix.EBADF, unix.ENOLCK, unix.EIO} {
		if isLockContention(errno) {
			t.Fatalf("%v classified as contention", errno)
		}
	}
}
