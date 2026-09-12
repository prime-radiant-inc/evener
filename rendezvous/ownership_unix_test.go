//go:build linux || darwin

package rendezvous

import (
	"syscall"
	"testing"
	"time"
)

// flockProbe replaces ownershipFlock to observe and pace LOCK_EX
// acquisitions. The wrapper parks AFTER the real flock succeeds, so a
// goroutine observed via acquired is holding the kernel lock; a second
// goroutine that cannot reach acquired within the grace window is blocked in
// the kernel — exactly the mutual-exclusion property under test.
type flockProbe struct {
	acquired chan struct{}
	permit   chan struct{}
}

func installFlockProbe(t *testing.T) *flockProbe {
	t.Helper()
	probe := &flockProbe{
		acquired: make(chan struct{}, 8),
		permit:   make(chan struct{}),
	}
	real := ownershipFlock
	ownershipFlock = func(fd int, how int) error {
		err := real(fd, how)
		if err == nil && how == syscall.LOCK_EX {
			probe.acquired <- struct{}{}
			<-probe.permit
		}
		return err
	}
	t.Cleanup(func() { ownershipFlock = real })
	return probe
}

func (p *flockProbe) awaitAcquired(t *testing.T, what string) {
	t.Helper()
	select {
	case <-p.acquired:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s never acquired the ownership lock", what)
	}
}

// assertBlocked fails if a lock acquisition lands within the grace window —
// a grace, not a race (serve_shutdown_budget_test.go precedent).
func (p *flockProbe) assertBlocked(t *testing.T, what string) {
	t.Helper()
	select {
	case <-p.acquired:
		t.Fatalf("%s acquired the ownership lock while another operation held it", what)
	case <-time.After(250 * time.Millisecond):
	}
}

// TestOwnershipLockSerializesRemoveThenWrite is order A of the replacement
// race: a stale RemoveIfOwned holds the lock; a replacement's Write for the
// same PID must kernel-block until the remove finishes, then land its own
// entry. Without serialization the write could interleave into the remove's
// check-then-unlink window.
func TestOwnershipLockSerializesRemoveThenWrite(t *testing.T) {
	dir := t.TempDir()
	v1 := ownershipTestEntry(8100)
	v2 := v1
	v2.Address = "127.0.0.1:5200"
	v2.StartedAt = v1.StartedAt.Add(time.Hour)
	if _, err := Write(dir, v1); err != nil {
		t.Fatalf("Write(v1): %v", err)
	}
	probe := installFlockProbe(t)

	removeDone := make(chan error, 1)
	go func() { removeDone <- RemoveIfOwned(dir, v1) }()
	probe.awaitAcquired(t, "RemoveIfOwned(v1)")

	writeDone := make(chan error, 1)
	go func() {
		_, err := Write(dir, v2)
		writeDone <- err
	}()
	probe.assertBlocked(t, "Write(v2)")

	probe.permit <- struct{}{} // let the remove finish
	if err := <-removeDone; err != nil {
		t.Fatalf("RemoveIfOwned(v1): %v", err)
	}
	if got := ownershipEntries(t, dir); len(got) != 0 {
		t.Fatalf("entries after remove = %+v, want none", got)
	}

	probe.awaitAcquired(t, "Write(v2)")
	probe.permit <- struct{}{}
	if err := <-writeDone; err != nil {
		t.Fatalf("Write(v2): %v", err)
	}
	entries := ownershipEntries(t, dir)
	if len(entries) != 1 || entries[0].Address != v2.Address {
		t.Fatalf("entries after serialized write = %+v, want v2", entries)
	}
}

// TestOwnershipLockSerializesWriteThenRemove is order B: the replacement's
// Write holds the lock; a stale RemoveIfOwned must kernel-block, then read
// the NEW entry and refuse. An unprotected check-then-unlink would have
// deleted v2 the moment the write landed — this test catches that.
func TestOwnershipLockSerializesWriteThenRemove(t *testing.T) {
	dir := t.TempDir()
	v1 := ownershipTestEntry(8200)
	v2 := v1
	v2.Address = "127.0.0.1:5300"
	v2.StartedAt = v1.StartedAt.Add(time.Hour)
	if _, err := Write(dir, v1); err != nil {
		t.Fatalf("Write(v1): %v", err)
	}
	probe := installFlockProbe(t)

	writeDone := make(chan error, 1)
	go func() {
		_, err := Write(dir, v2)
		writeDone <- err
	}()
	probe.awaitAcquired(t, "Write(v2)")

	removeDone := make(chan error, 1)
	go func() { removeDone <- RemoveIfOwned(dir, v1) }()
	probe.assertBlocked(t, "RemoveIfOwned(stale v1)")

	probe.permit <- struct{}{} // let the replacement write land
	if err := <-writeDone; err != nil {
		t.Fatalf("Write(v2): %v", err)
	}

	probe.awaitAcquired(t, "RemoveIfOwned(stale v1)")
	probe.permit <- struct{}{}
	if err := <-removeDone; err == nil {
		t.Fatal("RemoveIfOwned accepted a stale identity after the replacement write")
	}
	entries := ownershipEntries(t, dir)
	if len(entries) != 1 || entries[0].Address != v2.Address {
		t.Fatalf("replacement entry destroyed by stale remove: %+v", entries)
	}
}
