package hubcore

import "sync"

// ResumeLocks hands out one mutex per session id so concurrent resume attempts
// for the same session serialize. Both the REST send path and the RPC
// auto-resume path share a single instance (via WebConfig.ResumeLocks) so a
// resume triggered on one transport blocks a racing resume on the other,
// preventing two daemons from being spawned for one exited session (kata sm1a).
type ResumeLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewResumeLocks returns an empty registry ready for use.
func NewResumeLocks() *ResumeLocks {
	return &ResumeLocks{locks: map[string]*sync.Mutex{}}
}

// For returns the mutex for sessionID, creating it on first use. Repeated calls
// with the same id return the same mutex, so callers serialize against each
// other regardless of which path (REST or RPC) they came in on.
func (r *ResumeLocks) For(sessionID string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.locks[sessionID]
	if !ok {
		m = &sync.Mutex{}
		r.locks[sessionID] = m
	}
	return m
}
