package hub

import (
	"sync"
	"testing"
)

// concurrentCase is one case of a multi-case E2E test: the subtest name and the
// journey it runs.
type concurrentCase struct {
	name string
	run  func(t *testing.T)
}

// runCasesConcurrently runs every case as its own subtest, all at once, and
// waits for all of them before returning.
//
// The subtests are deliberately not t.Parallel: a test with parallel subtests
// returns from its own function before they run, so its reported duration --
// the number `evener dev hub-shards` surveys and the timing ratchet records --
// would be a few milliseconds while the cases cost seconds each. Starting each
// case's own t.Run from its own goroutine keeps every case on its own
// *testing.T (so a case's t.Fatal stops that case and not the batch, and the
// test-goroutine vet check stays clean) while the parent's duration is the wall
// of the whole batch.
func runCasesConcurrently(t *testing.T, cases []concurrentCase) {
	t.Helper()
	var wg sync.WaitGroup
	for _, c := range cases {
		wg.Go(func() { t.Run(c.name, c.run) })
	}
	wg.Wait()
}
