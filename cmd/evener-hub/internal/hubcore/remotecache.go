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
	// generations maps each registered source to the identity generation its
	// current registration carries, assigned from nextGeneration — the same
	// shape hostreg's round-4 fix chose for the registry: one cache-wide
	// monotonic counter, per-name state only for live names. A refresh walk
	// captures a source's generation before it starts reading and
	// StoreWalkSnapshot rejects the source's rows when the generation moved
	// underneath the walk, so neither a remove — the entry drops, and an
	// absent source mismatches every capture — nor a remove/re-add — the
	// re-registration assigns a strictly newer generation — can publish the
	// old registration's rows under the re-added name (the round-7 M1
	// finding). Entries exist only for registered sources: removal deletes
	// rather than tombstones, so churn over distinct names cannot grow the
	// map or the publish-time scan (the round-7 M2 finding).
	generations    map[string]uint64
	nextGeneration uint64
}

// SetOnChange installs the post-commit content-change hook. The callback is
// intentionally invoked outside the cache lock so consumers may capture the
// source immediately without lock inversion.
func (c *RemoteThreadCache) SetOnChange(fn func()) { c.mu.Lock(); c.onChange = fn; c.mu.Unlock() }

func (c *RemoteThreadCache) Store(threads []appwire.Thread) {
	c.StoreSnapshot(threads, true)
}

// StoreSnapshot stores rows with compatibility source inference. Production's
// refresh path uses StoreWalkSnapshot so source ownership, row quality, and
// the walk's captured identity generations are published together with the
// same atomic generation.
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
//
// This is the walk-metadata-free publish: nothing is filtered, because with no
// captured generations there is no evidence a source's identity moved under
// the rows. The production refresh path publishes through StoreWalkSnapshot,
// which carries the generations the walk captured and drops rows a removal or
// remove/re-add has made stale.
func (c *RemoteThreadCache) StoreSnapshotData(snapshot RemoteThreadSnapshot) {
	c.publish(snapshot, nil)
}

// StoreWalkSnapshot publishes one refresh walk's result the way the background
// refresher does: sourceGenerations is the per-source identity map the walk
// captured before it started reading (SourceGenerations). Under the publish
// lock, every row and per-source entry the walk attributed to a source whose
// current generation differs from the captured one — or whose registration is
// gone altogether, removal being what makes a source absent — is dropped: a
// walk that started before a remove cannot republish the removed host's rows
// when it finishes (the round-6 M2 finding), and a walk that started before a
// remove/re-add cannot publish the old registration's rows under the re-added
// name (the round-7 M1 finding). A source the walk captured under its current
// generation publishes normally, and so does a source the walk never captured:
// it registered after the capture, so its rows belong to the current
// registration.
func (c *RemoteThreadCache) StoreWalkSnapshot(snapshot RemoteThreadSnapshot, sourceGenerations map[string]uint64) {
	c.publish(snapshot, sourceGenerations)
}

func (c *RemoteThreadCache) publish(snapshot RemoteThreadSnapshot, captured map[string]uint64) {
	c.mu.Lock()
	snapshot = c.withoutStaleSources(snapshot, captured)
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

// RegisterSource records sourceID's registration and assigns it the next
// identity generation. The hub's host manager calls it whenever a host's
// source registers — sidecar entries at startup and every runtime add — so the
// cache knows which registration of the name a walk is walking: RemoveSource
// deletes the entry, a re-add assigns a strictly newer generation, and
// StoreWalkSnapshot compares the two to reject a walk captured under a stale
// registration. Without the comparison, a refresh that started before a
// remove/re-add published the old host's rows under the new host's identity
// (the round-7 M1 finding). Registering a name that is already registered
// refreshes its generation too: the caller cannot observe the interleaving,
// and a stale walk's rows must not slide in through it. An empty ID does
// nothing.
func (c *RemoteThreadCache) RegisterSource(sourceID string) {
	if sourceID == "" {
		return
	}
	c.mu.Lock()
	c.nextGeneration++
	if c.generations == nil {
		c.generations = make(map[string]uint64)
	}
	c.generations[sourceID] = c.nextGeneration
	c.mu.Unlock()
}

// SourceGenerations returns a copy of the per-source identity generations, the
// snapshot a refresh walk captures before it starts reading. The walk hands
// the copy back to StoreWalkSnapshot, which compares it against the cache's
// current generations under the publish lock; walk-free publishers
// (StoreSnapshotData) never need it.
func (c *RemoteThreadCache) SourceGenerations() map[string]uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	generations := make(map[string]uint64, len(c.generations))
	maps.Copy(generations, c.generations)
	return generations
}

// RemoveSource drops every cached row the snapshot walk attributed to
// sourceID, and the source's own per-source snapshot with them, so a host
// removed at runtime cannot keep rendering its last-refreshed sessions as
// live until the refresher's next tick rewrites the cache (the round-5 M2
// finding: the source registry no longer resolves the removed ID, so
// sourceOnline fail-opens and the stale rows read as live).
//
// The removal also deletes the source's registration generation: a refresh that
// was walking while the remove committed holds a capture of the old
// generation, and StoreWalkSnapshot rejects every captured source whose
// registration is gone — an absent source mismatches any capture — so the late
// publish cannot republish the removed host's rows (the round-6 M2 finding).
// Unlike the round-6 tombstone this replaces, no per-name state survives the
// removal, so the cache's bookkeeping stays bounded by the live source set
// (the round-7 M2 finding). Row ownership follows the same rule
// StoreSnapshot's source inference uses: the row's Source when it carries one,
// else its parsed ref. An empty ID does nothing; a source the cache holds no
// rows for loses only its generation — the snapshot rewrite stays a no-op
// prune that publishes no generation, mirroring publish's no-op discipline.
func (c *RemoteThreadCache) RemoveSource(sourceID string) {
	if sourceID == "" {
		return
	}
	c.mu.Lock()
	delete(c.generations, sourceID)
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

// withoutStaleSources strips every stale source's rows and per-source entry
// from the incoming publish. A source is stale when the walk captured its
// generation and the cache's current generation for it differs — the source
// was removed after the walk started (its registration, and with it the
// generation entry, is gone: an absent source mismatches every capture) or
// removed and re-added (the re-registration assigned a strictly newer
// generation). RemoveSource pruned the removed sources' rows from the current
// snapshot, but a refresh that captured its walk before the remove can finish
// and publish afterwards, repopulating rows the removal committed to dropping
// (the round-6 M2 finding) — or, across a remove/re-add, rows from the old
// host under the new host's identity (the round-7 M1 finding). The filter
// uses RemoveSource's own ownership rule, so what a prune drops cannot come
// back in through a publish. Sources the walk did not capture publish
// unfiltered: they registered after the capture, so their rows belong to the
// current registration. Callers hold mu.
func (c *RemoteThreadCache) withoutStaleSources(snapshot RemoteThreadSnapshot, captured map[string]uint64) RemoteThreadSnapshot {
	if len(captured) == 0 {
		return snapshot
	}
	sourceStale := func(sourceID string) bool {
		generation, walked := captured[sourceID]
		return walked && c.generations[sourceID] != generation
	}
	threads := make([]appwire.Thread, 0, len(snapshot.Threads))
	for _, thread := range snapshot.Threads {
		if !remoteThreadSourceStale(thread, sourceStale) {
			threads = append(threads, thread)
		}
	}
	snapshot.Threads = threads
	// Sources stays the caller's map — the publish must not mutate the
	// snapshot it was handed — so stale entries drop from a copy, made only
	// when there is something to drop.
	var sources map[string]RemoteSourceSnapshot
	for sourceID := range snapshot.Sources {
		if !sourceStale(sourceID) {
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

// remoteThreadSourceStale reports whether the snapshot walk attributed thread
// to a source whose registration moved since the walk captured it — the row's
// own Source when it carries one, else its parsed ref, exactly the resolution
// inferRemoteSources uses, so the staleness test and the ownership rule cannot
// disagree about which source owns a row.
func remoteThreadSourceStale(thread appwire.Thread, sourceStale func(string) bool) bool {
	if thread.Source != "" {
		return sourceStale(thread.Source)
	}
	ref, err := appwire.ParseRef(thread.Evener.Ref)
	if err != nil {
		return false
	}
	return sourceStale(ref.SourceID)
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
