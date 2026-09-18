package plugins

// StoreChanged reports which of the plugin store's two persisted files a
// lockStore session's writes actually landed in: installed_plugins.json
// (Plugins) and known_marketplaces.json (Marketplaces). The zero value means
// neither was written.
type StoreChanged struct {
	Plugins      bool
	Marketplaces bool
}

// OnStoreChanged installs fn as the callback lockStore's release invokes,
// once per lock session, with whatever that session's writes actually
// changed — never when a session wrote nothing. This is the one hook a
// caller of Install, AddMarketplace, ListMarketplaces, and every other method
// that takes the store lock can rely on to learn a write happened, instead of
// threading its own success signal back by hand: the flag is set by the write
// primitive itself (saveRegistry, saveMarketplaces), immediately after that
// primitive's own write succeeds, so it cannot be forgotten by whatever
// caller wraps the primitive or by an early return on a later failure.
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
// session. Only the two write primitives (saveRegistry, saveMarketplaces)
// call it, right after each one's own write succeeds; moveMarketplace and
// swapInClone never do, since they move directories that neither List nor
// ListMarketplaces reads.
func (m *Manager) markStoreChanged(changed StoreChanged) {
	m.storeChangedMu.Lock()
	defer m.storeChangedMu.Unlock()
	m.pendingStoreChanged.Plugins = m.pendingStoreChanged.Plugins || changed.Plugins
	m.pendingStoreChanged.Marketplaces = m.pendingStoreChanged.Marketplaces || changed.Marketplaces
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
