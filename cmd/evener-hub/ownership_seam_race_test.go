package hub

import (
	"sync"
	"testing"
)

// TestRetireOwnershipCapabilitySeamIsRaceFree is the regression for the
// ownership-capability seam being an unsynchronized package global read on the
// daemon-list path and written by tests. A goroutine that outlives the test
// that set the seam can read it concurrently, so a plain read racing a plain
// write is a data race under the Go memory model; this repository runs -race.
// Driving the production reader concurrently with writers must be race-free.
func TestRetireOwnershipCapabilitySeamIsRaceFree(t *testing.T) {
	defer setRetireOwnershipCapability(nil)()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			setRetireOwnershipCapability(func() bool { return true })
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = strongOwnershipAvailable()
		}
	}()
	wg.Wait()
}
