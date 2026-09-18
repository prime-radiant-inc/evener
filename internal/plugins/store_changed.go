package plugins

// StoreChanged reports which of the plugin store's two persisted files a
// lockStore session's writes actually landed in: installed_plugins.json
// (Plugins) and known_marketplaces.json (Marketplaces). The zero value means
// neither was written.
type StoreChanged struct {
	Plugins      bool
	Marketplaces bool
}

// any reports whether either file changed.
func (c StoreChanged) any() bool {
	return c.Plugins || c.Marketplaces
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
// installed just accumulates and discards: reportStoreChanged is always safe
// to call.
func (m *Manager) OnStoreChanged(fn func(StoreChanged)) {
	m.onStoreChanged = fn
}

// markStoreChanged accumulates changed onto the Manager's current lock
// session. Called by a write primitive right after its own write succeeds —
// saveRegistry after installSaveRegistry returns nil, saveMarketplaces after
// marketplaceAtomicWriteFile returns nil — never before, and never by the
// function that calls the primitive: recoverMarkedRename,
// migrateMarketplaceNames, and every rename/merge helper in
// marketplace_migration.go call down into saveRegistry/saveMarketplaces
// (directly or through saveRename) for every write they make, so none of
// them has to compute or forward its own answer.
//
// Only the two file-writing primitives mark the flag. moveMarketplace and
// swapInClone move directories that neither List nor ListMarketplaces ever
// reads — those two calls answer entirely from the JSON files — and both are
// invoked as one step of a larger rename/edit that rolls the directory move
// back (runUndo, undoSwap) if the step that follows it fails. Marking the
// flag at either of them would report a change that a concurrent reader
// could never have observed and that this very session's own rollback may
// immediately undo, exactly the false "something changed" this mechanism
// exists to rule out. Marking it only where a file write actually lands is
// safe even when that same session later overwrites or restores the file
// again (saveRename's restore of registryAsFound after a failed
// saveMarketplaces, for one): List's own registry read is unlocked, so a
// transient write in between is a state a concurrent read could really have
// seen, and reporting it is the conservative, correct answer — never the
// reverse.
func (m *Manager) markStoreChanged(changed StoreChanged) {
	m.pendingStoreChanged.Plugins = m.pendingStoreChanged.Plugins || changed.Plugins
	m.pendingStoreChanged.Marketplaces = m.pendingStoreChanged.Marketplaces || changed.Marketplaces
}

// captureStoreChanged takes and clears the Manager's current lock session,
// for lockStore's release to report once the lock itself is let go. Called
// while the lock is still held — not after — so that the next acquisition's
// own reset (lockStore) cannot run concurrently with this read: with no lock
// left between them, two goroutines touching the same unsynchronized field
// would be a plain data race, and List's registry read (which never takes
// this lock at all) is proof this field's session boundary really depends on
// the flock, not on anything else serializing callers.
func (m *Manager) captureStoreChanged() StoreChanged {
	changed := m.pendingStoreChanged
	m.pendingStoreChanged = StoreChanged{}
	return changed
}
