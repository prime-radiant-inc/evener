//go:build linux || darwin

package rendezvous

import (
	"sync"
	"syscall"
	"testing"
)

// TestOwnershipFlockSeamIsRaceFree is the regression for the flock seam being
// an unsynchronized package global read on every rendezvous write/remove and
// written by tests. A writer or remover can run on a goroutine outliving the
// test that set the seam, so a plain read racing a plain write is a data race
// under the Go memory model; this repository runs -race. Driving the production
// lock path concurrently with writers must be race-free.
func TestOwnershipFlockSeamIsRaceFree(t *testing.T) {
	defer setOwnershipFlock(syscall.Flock)()

	dir := t.TempDir()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 200 {
			setOwnershipFlock(func(int, int) error { return nil })
		}
	}()
	go func() {
		defer wg.Done()
		for range 200 {
			if err := withOwnershipLock(dir, 9911, func() error { return nil }); err != nil {
				t.Errorf("withOwnershipLock: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}
