package server

import (
	"sync"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appoverlay"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm/registry"
)

// threadHistories owns every thread's history on this server: the root's and
// each delegate's, with one index handle cache and one notice budget shared
// across all of them.
type threadHistories struct {
	// mu serializes changes to the registry. A lookup (get) takes no lock:
	// recorded-entry hooks look histories up under a transcript append lock,
	// where only a history's queue mutex may be taken.
	mu      sync.Mutex
	cache   *transcriptindex.Cache
	budget  *appoverlay.Budget
	threads sync.Map // thread id -> *threadHistory
	// root is the served root thread, the owner of every descendant history
	// ensureDescendant admits; detachExcept sets it.
	root string
	// lastEpoch is the epoch each detached thread's history ended at: a
	// history recreated for the thread starts there, so a thread's epochs
	// never go backwards while this process runs.
	lastEpoch map[string]uint64
}

// newThreadHistories returns an empty registry backed by a cache of the
// given capacity and a notice budget of the given byte limit.
func newThreadHistories(cacheCapacity, budgetBytes int) *threadHistories {
	return &threadHistories{
		cache:     transcriptindex.NewCache(cacheCapacity),
		budget:    appoverlay.NewBudget(budgetBytes),
		lastEpoch: map[string]uint64{},
	}
}

// ensure returns threadID's history, creating it over path with a fresh
// overlay charged against the registry's shared budget if this is the first
// call for threadID; a later call for an already-registered threadID
// returns the existing history and ignores every other argument.
// recordedLength seeds the new history's projection with the length already
// recorded before any entry reaches its hook (a transcript.Writer's
// in-memory RecordedLength(), never a stat), so a read before the first
// hooked entry covers those bytes directly instead of waiting for a
// republish; pass 0 when the history's hook is wired before anything is
// recorded. ensure does no file I/O itself: the cache opens a handle lazily,
// on the first projection or read, so it is safe to call inside a
// projection commit. epoch is the resync epoch the new history starts at (or
// the epoch an earlier history of the thread ended at, if higher), and
// bootGeneration the daemon's boot generation it publishes under.
func (r *threadHistories) ensure(
	threadID, ref, path string,
	recordedLength int64,
	epoch uint64,
	bootGeneration string,
	publish func(appwire.HistoryUpdatedParams) error,
	resync func(epoch uint64),
	cost func(model string) *registry.Cost,
) *threadHistory {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ensureLocked(threadID, ref, path, recordedLength, epoch, bootGeneration, publish, resync, cost)
}

// ensureDescendant is ensure for a descendant of the tree rooted at owner,
// with nothing recorded before its hook: it returns nil, creating nothing,
// when owner is no longer the served root. The check and the creation share
// the registry lock with detachExcept, so a hook of a replaced tree that read
// its root's identity before the replacement cannot slip a history in after
// it.
func (r *threadHistories) ensureDescendant(
	owner, threadID, ref, path, bootGeneration string,
	publish func(appwire.HistoryUpdatedParams) error,
	resync func(epoch uint64),
	cost func(model string) *registry.Cost,
) *threadHistory {
	r.mu.Lock()
	defer r.mu.Unlock()
	if owner != r.root {
		return nil
	}
	return r.ensureLocked(threadID, ref, path, 0, 0, bootGeneration, publish, resync, cost)
}

// ensureLocked is ensure's body. Callers hold r.mu.
func (r *threadHistories) ensureLocked(
	threadID, ref, path string,
	recordedLength int64,
	epoch uint64,
	bootGeneration string,
	publish func(appwire.HistoryUpdatedParams) error,
	resync func(epoch uint64),
	cost func(model string) *registry.Cost,
) *threadHistory {
	if h := r.get(threadID); h != nil {
		return h
	}
	epoch = max(epoch, r.lastEpoch[threadID])
	h := newThreadHistory(threadHistoryConfig{
		threadID:       threadID,
		ref:            ref,
		path:           path,
		cache:          r.cache,
		overlay:        appoverlay.New(r.budget),
		publish:        publish,
		resync:         resync,
		cost:           cost,
		recordedLength: recordedLength,
		epoch:          epoch,
		bootGeneration: bootGeneration,
	})
	r.threads.Store(threadID, h)
	return h
}

// get returns threadID's history, or nil if none is registered. It takes no
// lock.
func (r *threadHistories) get(threadID string) *threadHistory {
	if h, ok := r.threads.Load(threadID); ok {
		return h.(*threadHistory)
	}
	return nil
}

// drop closes threadID's history and its overlay and removes it from the
// registry; a no-op if threadID is not registered. The history's close can
// block on its projection goroutine, so it and the overlay close run after
// the registry's own lock is released.
func (r *threadHistories) drop(threadID string) {
	if h := r.detach(threadID); h != nil {
		closeHistories([]*threadHistory{h})
	}
}

// detach removes threadID's history from the registry and returns it
// unclosed, or nil if none is registered (see detachExcept on why).
func (r *threadHistories) detach(threadID string) *threadHistory {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.detachLocked(threadID)
}

// detachLocked removes threadID's history and retires it: it takes no more
// entries and recovers no more (its epoch is final), and a history recreated
// for the thread starts at that epoch. Callers hold r.mu.
func (r *threadHistories) detachLocked(threadID string) *threadHistory {
	loaded, ok := r.threads.LoadAndDelete(threadID)
	if !ok {
		return nil
	}
	h := loaded.(*threadHistory)
	r.lastEpoch[threadID] = max(r.lastEpoch[threadID], h.retire())
	return h
}

// detachExcept makes keep the served root and removes every other history
// from the registry, returning them unclosed. Closing waits on each history's
// projection goroutine, which may be waiting on a projection commit, so a
// caller inside a commit detaches there and closes after the commit with
// closeHistories.
func (r *threadHistories) detachExcept(keep string) []*threadHistory {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.root = keep
	var detached []*threadHistory
	r.threads.Range(func(key, _ any) bool {
		if threadID := key.(string); threadID != keep {
			detached = append(detached, r.detachLocked(threadID))
		}
		return true
	})
	return detached
}

// close closes every registered history and its overlay, then the shared
// cache, and leaves the registry empty.
func (r *threadHistories) close() {
	closeHistories(r.detachExcept(""))
	_ = r.cache.Close()
}

// closeHistories closes each history and then its overlay. The caller holds
// nothing a history's publish or resync needs.
func closeHistories(histories []*threadHistory) {
	for _, h := range histories {
		h.close()
		h.overlay.Close()
	}
}
