package hubcore

import (
	"reflect"
	"sync"

	"primeradiant.com/evener/appwire"
)

// RemoteSourceSnapshot is the source-owned portion of one remote navigation
// snapshot. Threads are the source's last complete page walk, Complete says
// whether that walk reached a terminal page, and IncompleteIDs identifies
// malformed or conflicting rows that must not authorize a favorite decision.
type RemoteSourceSnapshot struct {
	Threads       []appwire.Thread
	Complete      bool
	IncompleteIDs []string
}

// RemoteThreadSnapshot is one atomically published remote navigation unit.
// The cache assigns Generation when it stores the unit; readers receive
// defensive copies of every slice and map-backed value.
type RemoteThreadSnapshot struct {
	Threads    []appwire.Thread
	Complete   bool
	Sources    map[string]RemoteSourceSnapshot
	Generation uint64
}

// RemoteThreadCache holds the most recent remote-source thread snapshot so a
// tree render never blocks on a network hop. A background refresher stores one
// complete unit and the tree read path consumes one complete unit.
type RemoteThreadCache struct {
	mu       sync.RWMutex
	snapshot RemoteThreadSnapshot
	onChange func()
}

// SetOnChange installs the post-commit content-change hook. The callback is
// intentionally invoked outside the cache lock so consumers may capture the
// source immediately without lock inversion.
func (c *RemoteThreadCache) SetOnChange(fn func()) { c.mu.Lock(); c.onChange = fn; c.mu.Unlock() }

func (c *RemoteThreadCache) Store(threads []appwire.Thread) {
	c.StoreSnapshot(threads, true)
}

// StoreSnapshot stores rows with compatibility source inference. Production's
// refresh path uses StoreSnapshotData so source ownership and row quality are
// published together with the same atomic generation.
func (c *RemoteThreadCache) StoreSnapshot(threads []appwire.Thread, complete bool) {
	c.StoreSnapshotData(RemoteThreadSnapshot{
		Threads:  threads,
		Complete: complete,
		Sources:  inferRemoteSources(threads, complete),
	})
}

// StoreSnapshotData atomically publishes all remote snapshot metadata as one
// generation. The caller's generation is ignored because the cache owns the
// monotonic sequence used by tree memoization.
func (c *RemoteThreadCache) StoreSnapshotData(snapshot RemoteThreadSnapshot) {
	c.mu.Lock()
	previous := c.snapshot
	previous = normalizeRemoteThreadSnapshot(previous)
	snapshot = normalizeRemoteThreadSnapshot(snapshot)
	if reflect.DeepEqual(previous.Threads, snapshot.Threads) && previous.Complete == snapshot.Complete && reflect.DeepEqual(previous.Sources, snapshot.Sources) {
		c.mu.Unlock()
		return
	}
	snapshot.Generation = c.snapshot.Generation + 1
	c.snapshot = cloneRemoteThreadSnapshot(snapshot)
	onChange := c.onChange
	c.mu.Unlock()
	if onChange != nil {
		onChange()
	}
}

func normalizeRemoteThreadSnapshot(snapshot RemoteThreadSnapshot) RemoteThreadSnapshot {
	if snapshot.Threads == nil {
		snapshot.Threads = []appwire.Thread{}
	}
	if snapshot.Sources == nil {
		snapshot.Sources = map[string]RemoteSourceSnapshot{}
	}
	for id, source := range snapshot.Sources {
		if source.Threads == nil {
			source.Threads = []appwire.Thread{}
		}
		if source.IncompleteIDs == nil {
			source.IncompleteIDs = []string{}
		}
		snapshot.Sources[id] = source
	}
	return snapshot
}

func (c *RemoteThreadCache) Get() []appwire.Thread {
	return c.Snapshot().Threads
}

func (c *RemoteThreadCache) Snapshot() RemoteThreadSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneRemoteThreadSnapshot(c.snapshot)
}

// Generation returns the revision marker without cloning the retained
// snapshot. Callers that only need invalidation identity should use this
// method rather than Snapshot.
func (c *RemoteThreadCache) Generation() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshot.Generation
}

func inferRemoteSources(threads []appwire.Thread, complete bool) map[string]RemoteSourceSnapshot {
	sources := make(map[string]RemoteSourceSnapshot)
	for _, thread := range threads {
		sourceID := thread.Source
		if sourceID == "" {
			if ref, err := appwire.ParseRef(thread.Evener.Ref); err == nil {
				sourceID = ref.SourceID
			}
		}
		if sourceID == "" {
			continue
		}
		source := sources[sourceID]
		source.Threads = append(source.Threads, thread)
		source.Complete = complete
		sources[sourceID] = source
	}
	return sources
}

func cloneRemoteThreadSnapshot(snapshot RemoteThreadSnapshot) RemoteThreadSnapshot {
	out := RemoteThreadSnapshot{
		Threads:    append([]appwire.Thread(nil), snapshot.Threads...),
		Complete:   snapshot.Complete,
		Generation: snapshot.Generation,
	}
	if snapshot.Sources != nil {
		out.Sources = make(map[string]RemoteSourceSnapshot, len(snapshot.Sources))
		for id, source := range snapshot.Sources {
			out.Sources[id] = RemoteSourceSnapshot{
				Threads:       append([]appwire.Thread(nil), source.Threads...),
				Complete:      source.Complete,
				IncompleteIDs: append([]string(nil), source.IncompleteIDs...),
			}
		}
	}
	return out
}
