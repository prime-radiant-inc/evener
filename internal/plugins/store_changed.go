package plugins

// StoreChanged reports what a lockStore session left the plugin store's two
// persisted files representing: installed_plugins.json (Plugins) and
// known_marketplaces.json (Marketplaces). A flag is set when that file's write
// lands, and the flag of the file that records a directory is also set when a
// rename's rollback could not put that directory back (#1800) — the clone's
// location is in known_marketplaces.json (Marketplaces), a plugin cache's
// installs in installed_plugins.json (Plugins) — so the store is left between
// the two names even though no file was written. The zero value means neither
// file was written and no rename was left half done.
type StoreChanged struct {
	Plugins      bool
	Marketplaces bool
}

// OnStoreChanged installs fn as the callback lockStore's release invokes,
// once per lock session, with whatever store file that session wrote or rename
// it left between the two names — never when it neither wrote a file nor left a
// rename half done. This reports writes that landed, not a session's net
// effect: a write that lands and is then restored still counts, because it is
// reported the instant it lands and a concurrent reader could have seen it.
// This is the one hook a
// caller of Install, AddMarketplace, ListMarketplaces, and every other method
// that takes the store lock can rely on to learn a write happened, instead of
// threading its own success signal back by hand: the flag is set by the write
// primitive itself (saveRegistry, saveMarketplaces), immediately after that
// primitive's own write succeeds — or by a rename's own undo when it cannot put
// a directory back (renameUndo) — so it cannot be forgotten by whatever caller
// wraps the primitive or by an early return on a later failure.
//
// Meant to be installed once, right after NewManager, by whoever constructs
// the Manager — the hub, wiring it to a broadcast. A Manager with none
// installed just accumulates and discards: reportingRelease's call into
// takeStoreChanged is always safe, callback or not.
func (m *Manager) OnStoreChanged(fn func(StoreChanged)) {
	m.storeChangedMu.Lock()
	defer m.storeChangedMu.Unlock()
	m.onStoreChanged = fn
}

// markStoreChanged accumulates changed onto the Manager's current lock
// session. The two write primitives (saveRegistry, saveMarketplaces) call it
// right after each one's own write succeeds, and a rename's own undo calls it
// (renameUndo) when it cannot put a moved directory back, marking that
// directory's file — Marketplaces for the clone, Plugins for the cache — since
// the store is then left between the two names with no file written.
// moveMarketplace and swapInClone do not mark a move that succeeds, since a
// directory neither List nor ListMarketplaces reads has not observably changed.
func (m *Manager) markStoreChanged(changed StoreChanged) {
	m.storeChangedMu.Lock()
	defer m.storeChangedMu.Unlock()
	m.pendingStoreChanged.Plugins = m.pendingStoreChanged.Plugins || changed.Plugins
	m.pendingStoreChanged.Marketplaces = m.pendingStoreChanged.Marketplaces || changed.Marketplaces
	if changed.Marketplaces {
		m.forgetChecks()
	}
}

// takeStoreChanged takes and clears the Manager's current lock session, and
// reads the installed OnStoreChanged callback, both under one lock — for
// lockStore's release to report once the lock itself is let go. Guarded by
// storeChangedMu rather than relying on the file lock: flock's mutual
// exclusion has no Go happens-before edge, and some of this package's own
// lockAcquirer test seams stub out the file lock entirely.
func (m *Manager) takeStoreChanged() (StoreChanged, func(StoreChanged)) {
	m.storeChangedMu.Lock()
	defer m.storeChangedMu.Unlock()
	changed := m.pendingStoreChanged
	m.pendingStoreChanged = StoreChanged{}
	return changed, m.onStoreChanged
}
