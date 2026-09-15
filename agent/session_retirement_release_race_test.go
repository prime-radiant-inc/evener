package agent

import (
	"sync"
	"testing"
)

// TestRetirementReleaseFaultSeamIsRaceFree is the regression for the
// retirement-release fault seam being an unsynchronized package global read on
// a production path and written by tests. The release path can run on a
// goroutine outliving the test that set the seam, so a plain read racing a
// plain write is a data race under the Go memory model; this repository runs
// -race. Driving the production reader concurrently with writers must be
// race-free.
func TestRetirementReleaseFaultSeamIsRaceFree(t *testing.T) {
	defer setRetirementReleaseFault(nil)()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			setRetirementReleaseFault(func(string) error { return nil })
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = retirementReleaseFailure("race-probe")
		}
	}()
	wg.Wait()
}
