package hub

import "testing"

// keeperCloser is implemented by every store whose connection now outlives a
// single call, so a test that creates one must release it.
type keeperCloser interface {
	Close() error
}

// closeOnCleanup registers a store's shared handle for release when the test
// ends. Stores used to open and close a connection per call, so leaving one
// open leaked nothing; a retained handle holds a SQLite connection, file
// descriptors and a database/sql opener goroutine. Registering at creation is
// what keeps a scenario that fails or skips from leaking: the cleanup runs
// either way.
//
// It takes testing.TB rather than *testing.T because a few fixtures here are
// shared with a benchmark and receive whichever of *testing.T or *testing.B
// their caller holds.
func closeOnCleanup[T keeperCloser](t testing.TB, store T) T {
	t.Helper()
	t.Cleanup(func() { _ = store.Close() })
	return store
}
