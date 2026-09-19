package hubcore

import (
	"maps"
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
	// removed holds the source IDs RemoveSource pruned, so a refresh that was
	// walking while the remove committed cannot republish their rows when it
	// finishes (the round-6 M2 finding): StoreSnapshotData filters every
	// removed source's rows and per-source entry out at publish time.
	// RestoreSource clears an entry when the source registers again, so a
	// remove/re-add churn cannot leave the re-added name permanently
	// unpublishable.
	removed map[string]struct{}
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
	snapshot = c.withoutRemovedSources(snapshot)
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

// RemoveSource drops every cached row the snapshot walk attributed to
// sourceID, and the source's own per-source snapshot with them, so a host
// removed at runtime cannot keep rendering its last-refreshed sessions as
// live until the refresher's next tick rewrites the cache (the round-5 M2
// finding: the source registry no longer resolves the removed ID, so
// sourceOnline fail-opens and the stale rows read as live). The removal is
// also recorded, so a refresh that was walking while the remove committed
// cannot republish the source's rows when it finishes (the round-6 M2
// finding): StoreSnapshotData filters recorded sources out at publish time,
// and RestoreSource lifts the record when the source registers again. Row
// ownership follows the same rule StoreSnapshot's source inference uses: the
// row's Source when it carries one, else its parsed ref. An empty ID does
// nothing; a source the cache holds no rows for takes only the removal
// record — the snapshot rewrite stays a no-op prune that publishes no
// generation, mirroring StoreSnapshotData's no-op discipline.
func (c *RemoteThreadCache) RemoveSource(sourceID string) {
	if sourceID == "" {
		return
	}
	c.mu.Lock()
	// The record comes before the rows check: the cache may hold nothing for
	// the source right now, but the in-flight refresh captured its rows
	// before the remove and publishes after — the record is what stops that
	// republish, not the prune.
	if c.removed == nil {
		c.removed = make(map[string]struct{})
	}
	c.removed[sourceID] = struct{}{}
	previous := normalizeRemoteThreadSnapshot(c.snapshot)
	threads := make([]appwire.Thread, 0, len(previous.Threads))
	for _, thread := range previous.Threads {
		if remoteThreadOwnedBySource(thread, sourceID) {
			continue
		}
		threads = append(threads, thread)
	}
	_, hadSource := previous.Sources[sourceID]
	if !hadSource && len(threads) == len(previous.Threads) {
		c.mu.Unlock()
		return
	}
	delete(previous.Sources, sourceID)
	snapshot := RemoteThreadSnapshot{
		Threads:  threads,
		Complete: previous.Complete,
		Sources:  previous.Sources,
	}
	snapshot.Generation = c.snapshot.Generation + 1
	c.snapshot = cloneRemoteThreadSnapshot(snapshot)
	onChange := c.onChange
	c.mu.Unlock()
	if onChange != nil {
		onChange()
	}
}

// RestoreSource lifts sourceID's removal record, so the source's rows may
// publish again: RemoveSource records the ID not only to prune the current
// snapshot but to hold an in-flight refresh back from republishing it
// (round-6 M2), and a source that registers again — the remove/re-add churn —
// must clear that hold, or the re-added host's sessions would never render
// again. A restore for an ID that was never removed is a no-op. No snapshot
// content changes: the next refresh publish brings the source's fresh rows.
func (c *RemoteThreadCache) RestoreSource(sourceID string) {
	if sourceID == "" {
		return
	}
	c.mu.Lock()
	delete(c.removed, sourceID)
	c.mu.Unlock()
}

// withoutRemovedSources strips every removed source's rows and per-source
// snapshot entry from the incoming publish. RemoveSource pruned the removed
// sources' rows from the current snapshot, but a refresh that captured its
// walk before the remove can finish and publish afterwards, repopulating rows
// the removal committed to dropping (the round-6 M2 finding). The filter uses
// RemoveSource's own ownership rule, so what a prune drops cannot come back
// in through a publish. Callers hold mu.
func (c *RemoteThreadCache) withoutRemovedSources(snapshot RemoteThreadSnapshot) RemoteThreadSnapshot {
	if len(c.removed) == 0 {
		return snapshot
	}
	threads := make([]appwire.Thread, 0, len(snapshot.Threads))
	for _, thread := range snapshot.Threads {
		owned := false
		for sourceID := range c.removed {
			if remoteThreadOwnedBySource(thread, sourceID) {
				owned = true
				break
			}
		}
		if !owned {
			threads = append(threads, thread)
		}
	}
	snapshot.Threads = threads
	// Sources stays the caller's map — the publish must not mutate the
	// snapshot it was handed — so the removed entries drop from a copy, made
	// only when there is something to drop.
	var sources map[string]RemoteSourceSnapshot
	for sourceID := range c.removed {
		if _, ok := snapshot.Sources[sourceID]; !ok {
			continue
		}
		if sources == nil {
			sources = make(map[string]RemoteSourceSnapshot, len(snapshot.Sources))
			maps.Copy(sources, snapshot.Sources)
		}
		delete(sources, sourceID)
	}
	if sources != nil {
		snapshot.Sources = sources
	}
	return snapshot
}

// remoteThreadOwnedBySource reports whether the snapshot walk attributed
// thread to sourceID — the row's own Source when it carries one, else its
// parsed ref — exactly the resolution inferRemoteSources uses, so
// RemoveSource drops the rows a stored snapshot grouped under the ID.
func remoteThreadOwnedBySource(thread appwire.Thread, sourceID string) bool {
	if thread.Source != "" {
		return thread.Source == sourceID
	}
	ref, err := appwire.ParseRef(thread.Evener.Ref)
	if err != nil {
		return false
	}
	return ref.SourceID == sourceID
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
		Threads:    cloneThreads(snapshot.Threads),
		Complete:   snapshot.Complete,
		Generation: snapshot.Generation,
	}
	if snapshot.Sources != nil {
		out.Sources = make(map[string]RemoteSourceSnapshot, len(snapshot.Sources))
		for id, source := range snapshot.Sources {
			out.Sources[id] = RemoteSourceSnapshot{
				Threads:       cloneThreads(source.Threads),
				Complete:      source.Complete,
				IncompleteIDs: append([]string(nil), source.IncompleteIDs...),
			}
		}
	}
	return out
}

func cloneThreads(threads []appwire.Thread) []appwire.Thread {
	if threads == nil {
		return nil
	}
	out := make([]appwire.Thread, len(threads))
	for i := range threads {
		out[i] = appwire.CloneThread(threads[i])
	}
	return out
}
