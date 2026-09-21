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
	// shape hostreg's registry chose: one cache-wide monotonic counter,
	// per-name state only for live names. A refresh walk captures a source's
	// generation immediately before it reads that source, and
	// StoreWalkSnapshot rejects the source's rows when the generation moved
	// underneath the read, so neither a remove — the entry drops, and an
	// absent source mismatches every capture — nor a remove/re-add — the
	// re-registration assigns a strictly newer generation — can publish the
	// old registration's rows under the re-added name. Entries exist only
	// for registered sources: removal deletes rather than tombstones, so
	// churn over distinct names cannot grow the map or the publish-time
	// scan.
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
// This is the walk-metadata-free publish: nothing is filtered, because it
// carries no capture at all — with no captured generations there is no
// evidence a source's identity moved under the rows. The production refresh
// path publishes through StoreWalkSnapshot, which carries the generations the
// walk captured and drops rows a removal or remove/re-add has made stale.
func (c *RemoteThreadCache) StoreSnapshotData(snapshot RemoteThreadSnapshot) {
	c.publish(snapshot, nil)
}

// StoreWalkSnapshot publishes one refresh walk's result the way the background
// refresher does: sourceGenerations is the per-source identity map the walk
// captured at READ time — each source's generation taken immediately before
// the walk read it (SourceGeneration). Under the publish lock, every row and
// per-source entry the walk attributed to a source whose current generation
// differs from the one it was read under is dropped: a walk that read a source
// before a remove cannot republish the removed host's rows when it finishes,
// and a walk that read before a remove/re-add cannot publish the old
// registration's rows under the re-added name.
//
// A source the walk did not capture is dropped unconditionally. The
// production walk captures every source it reads, so an uncaptured row owner
// means no registration is known to own the rows: the source was already
// removed when the walk read it (its registry snapshot lagged the removal),
// or the publish names a source the walk never read. "Currently live" cannot
// prove ownership: an add → read → remove → re-add inside one walk leaves the
// rows belonging to the first registration, which the churn replaced before
// the publish. The immediacy a live-check would serve is carried by the
// read-time capture instead: a source that registers mid-walk is captured
// when the walk reads it, under the registration that owns the rows, and
// still publishes on that tick.
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
// source registers — sidecar entries at startup and every runtime add — and
// the source registry's construction does it for configured hosts, so the
// cache knows which registration of the name a walk is walking: RemoveSource
// deletes the entry, a re-add assigns a strictly newer generation, and
// StoreWalkSnapshot compares the two to reject a walk captured under a stale
// registration. Without the comparison, a refresh that started before a
// remove/re-add published the old host's rows under the new host's identity.
// Registering a name that is already registered refreshes its generation
// too: the caller cannot observe the interleaving, and a stale walk's rows
// must not slide in through it. Callers pair it with the registration's
// visibility: the generation is assigned before the source becomes
// enumerable, so a walk that reads a source always finds a generation to
// capture at read time — an uncaptured source was already gone when the walk
// read it, so the publish drops its rows as unowned. An empty ID does
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

// SourceGenerations returns a copy of the per-source identity generations.
// The background walk captures each source's generation at read time via
// SourceGeneration instead — a copy taken before the walk started reading
// could not tell a mid-walk remove/re-add from a fresh registration — so
// this whole-map view serves callers that need every registration at once.
func (c *RemoteThreadCache) SourceGenerations() map[string]uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	generations := make(map[string]uint64, len(c.generations))
	maps.Copy(generations, c.generations)
	return generations
}

// SourceGeneration returns sourceID's current identity generation and whether
// the source is registered. The background walk captures a source's generation
// with this immediately before it reads the source, so the publish compares
// the registration that owned the read against the one live at the publish: a
// remove or remove/re-add between the read and the publish moves the
// generation, and the read's rows drop instead of publishing under a
// registration that no longer owns them. A source absent here was removed —
// or never registered — so a walk that reads one
// anyway must leave it uncaptured and let the publish drop its rows as
// unowned.
func (c *RemoteThreadCache) SourceGeneration(sourceID string) (uint64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	generation, ok := c.generations[sourceID]
	return generation, ok
}

// RemoveSource drops every cached row the snapshot walk attributed to
// sourceID, and the source's own per-source snapshot with them, so a host
// removed at runtime cannot keep rendering its last-refreshed sessions as
// live until the refresher's next tick rewrites the cache — the source
// registry no longer resolves the removed ID, so sourceOnline fail-opens
// and the stale rows would read as live.
//
// The removal also deletes the source's registration generation: a refresh that
// was walking while the remove committed holds a capture of the old
// generation, and StoreWalkSnapshot rejects every captured source whose
// registration is gone — an absent source mismatches any capture — so the late
// publish cannot republish the removed host's rows. No per-name state
// survives the removal, so the cache's bookkeeping stays bounded by the live
// source set. Row ownership follows the same rule
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
// — or, across a remove/re-add, rows from the old host under the new host's
// identity. The filter uses RemoveSource's own ownership rule, so what a
// prune drops cannot come back in through a publish.
//
// A source the walk did not capture is stale unconditionally. The
// production walk captures every source it reads (immediately before the
// read), so an uncaptured row owner means no registration is known to own
// the rows: the source was already removed when the walk read it — its
// registry snapshot lagged the removal — or the publish names a source the
// walk never read. A source can register after the walk's registry
// enumeration, be read by the walk, and be removed and re-added before the
// publish; the first registration's rows must not publish under the
// re-added identity, so "live at publish time" proves nothing about who
// owns them. The immediacy a live-check would serve moves to the read-time
// capture: a source that registers mid-walk is captured when the walk reads
// it and still publishes on that tick. Callers hold mu.
func (c *RemoteThreadCache) withoutStaleSources(snapshot RemoteThreadSnapshot, captured map[string]uint64) RemoteThreadSnapshot {
	if captured == nil {
		// The walk-free publish (StoreSnapshotData) carries no capture at
		// all: with nothing captured there is no evidence a source's identity
		// moved under the rows, so nothing is filtered. A walk's empty
		// capture is a map — SourceGenerations returns one, never nil — and
		// still filters.
		return snapshot
	}
	sourceStale := func(sourceID string) bool {
		generation, walked := captured[sourceID]
		if !walked {
			// No read-time capture claims the source's rows: the walk read it
			// after its registration was already gone, or the publish names a
			// source the walk never read. Either way no registration is known
			// to own the rows — and "currently live" is not ownership: an
			// add → read → remove → re-add sequence leaves the rows with the
			// replaced registration — so they cannot publish.
			return true
		}
		return c.generations[sourceID] != generation
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

// remoteThreadSourceID resolves the source a snapshot walk attributed thread
// to: the row's own Source when it carries one, else its parsed ref's
// source ID. It is the one definition of row ownership — the staleness test
// and the ownership rule below and the source inference
// (inferRemoteSources) all resolve through it, so they cannot disagree
// about which source owns a row. ok is false when neither carries a usable
// source (a ref that fails to parse).
func remoteThreadSourceID(thread appwire.Thread) (string, bool) {
	if thread.Source != "" {
		return thread.Source, true
	}
	ref, err := appwire.ParseRef(thread.Evener.Ref)
	if err != nil {
		return "", false
	}
	return ref.SourceID, true
}

// remoteThreadSourceStale reports whether the snapshot walk attributed thread
// to a source whose registration moved since the walk captured it.
func remoteThreadSourceStale(thread appwire.Thread, sourceStale func(string) bool) bool {
	sourceID, ok := remoteThreadSourceID(thread)
	if !ok {
		return false
	}
	return sourceStale(sourceID)
}

// remoteThreadOwnedBySource reports whether the snapshot walk attributed
// thread to sourceID, so RemoveSource drops the rows a stored snapshot
// grouped under the ID.
func remoteThreadOwnedBySource(thread appwire.Thread, sourceID string) bool {
	owner, ok := remoteThreadSourceID(thread)
	if !ok {
		return false
	}
	return owner == sourceID
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
		sourceID, _ := remoteThreadSourceID(thread)
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
