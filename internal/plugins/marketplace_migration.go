package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// An older evener could record a marketplace under a name the store refuses
// today: one carrying '@', which splitKey reads as the plugin/marketplace
// separator, so its installs key as some shorter marketplace's; one of the
// two scratch names, whose directory the next fetch clears; or one that is
// not a single non-traversing path component, from which no store directory
// can be derived. The store renames such an entry the first time it takes
// its lock (lockStore), so that under the lock every recorded name is valid
// and no operation has to ask which evener wrote the name.
//
// One rename moves an owned clone and writes two files, so a process
// that exits in the middle of one leaves a store half renamed. What names the
// rename in flight is a marker every automatic rename writes before changing anything
// and removes once both files record the new name (renameMarker): the next
// lock holder finishes exactly the rename the marker names, whether that
// rename had a clone of its own to move or only the two files to write,
// and without one there is no rename in flight, so a removed marketplace's
// residue under the same derived name is never mistaken for one. The merge
// that folds an alias into its family's existing record needs no new marker:
// the recorded exact-source family and the refused alias survive until both
// logical writes finish. Older merge markers still recover physical moves.

// loadMigratedMarketplaces reads the marketplaces file without the store lock
// and hands back what a lock holder would see. A name the store refuses, or a
// marker a run left in flight, means the file on disk is not what a lock
// holder would see, so this takes the lock — which migrates and recovers —
// and reads back what that left; a second reader that arrives meanwhile finds
// nothing left to do once it holds the lock. On a migrated store with no
// marker nothing is locked, so a listing never queues behind a fetch.
//
// A refused name means no lock holder has migrated this store yet (lockStore).
// A marker alone (renameMarkerPending) means one did and stopped between a
// rename's last write and dropping the marker, so every recorded name is one
// the store accepts while the rename is still in flight. Both are what the
// lock's recovery finishes, so both send this through it.
//
// The barrier is what a listing runs behind rather than a value it needs: the
// marketplace listing returns it, and the plugin listing takes the lock only
// to be sure the registry it reads next was keyed under valid names. Either
// way the lock is released before the caller reads anything else.
//
// That branch is the one write a listing makes, which is why it takes the
// caller's context rather than a background one: the hub runs a listing
// inline on the connection's serial worker, so a disconnected client's wait
// on the lock has to stop with the client. The same branch creates the
// store's .lock file in a store that had none, and a listing that finds
// neither a refused name nor a marker never reaches it.
func (m *Manager) loadMigratedMarketplaces(ctx context.Context, acquire lockAcquirer) (Marketplaces, error) {
	mk, err := m.loadMarketplaces()
	if err != nil {
		return mk, err
	}
	if len(refusedMarketplaceNames(mk)) == 0 && !m.renameMarkerPending() {
		return mk, nil
	}
	release, err := m.lockStore(ctx, acquire, 30*time.Second)
	if err != nil {
		return nil, err
	}
	defer release()
	return m.loadMarketplaces()
}

// renameMarkerPending reports whether the store holds a marker, the file that
// names the rename or merge a run left in flight (renameMarker). A marker that
// cannot be read counts as pending: it sits where one a run left would, and
// only the lock's recovery (recoverMarkedRename) can say what it names or fail
// the reading over it. A store that finished every operation has no marker and
// this costs one read of a name that is not there.
func (m *Manager) renameMarkerPending() bool {
	marker, err := m.loadRenameMarker()
	return err != nil || marker != nil
}

// migrateMarketplaceNames finishes the rename an interrupted run left in
// flight (recoverMarkedRename), then renames every recorded marketplace whose
// name validNameComponent refuses, in the order that leaves each of them its
// own directories and its own keys (migrationOrder), saying on stderr what
// each became.
// An entry whose name is an alias of one that migrated first — both naming
// the one source and deriving the one clone and the one cache — merges into
// the record that rename made instead (migratedUnderAnAlias). Each entry
// is saved on its own, so a failure leaves the entries before it migrated and
// the failing one as it was found, and the error names it. The caller must
// hold the store lock.
func (m *Manager) migrateMarketplaceNames() error {
	if err := m.recoverMarkedRename(); err != nil {
		return err
	}
	mk, err := m.loadMarketplaces()
	if err != nil {
		return err
	}
	names := refusedMarketplaceNames(mk)
	if len(names) == 0 {
		return nil
	}
	names, err = m.migrationOrder(names)
	if err != nil {
		return err
	}
	reg, err := m.loadRegistry()
	if err != nil {
		return err
	}
	// The name each rename of this run recorded, against the alias family it
	// belongs to: the two directories the refused name derived and the source
	// its record names. A merge records none.
	owners := registryKeyOwners(reg, mk)
	recordedThisRun := map[recordedAlias]string{}
	// The families earlier runs recorded, so an alias still waiting for the
	// marketplace they migrated knows which logical record owns the family. An
	// entry whose destination the store no longer records names nothing left to
	// merge into, so loading drops it.
	rec, err := m.loadMigrationRecord()
	if err != nil {
		return err
	}
	before := len(rec.Renames)
	kept := make([]recordedRename, 0, before)
	for _, r := range rec.Renames {
		if _, recorded := mk[r.To]; !recorded {
			continue
		}
		// The directories the rename recorded rather than ones recomputed from
		// From: From's directories have moved since, so resolving them again
		// would answer with the lexical path.
		if r.Clone == "" || r.Cache == "" {
			continue
		}
		recordedThisRun[recordedAlias{dirs: marketplaceDirs{clone: r.Clone, cache: r.Cache}, src: r.Source}] = r.To
		kept = append(kept, r)
	}
	rec.Renames = kept
	if len(kept) != before {
		if err := m.saveMigrationRecord(rec); err != nil {
			return err
		}
	}
	for _, name := range names {
		ref := mk[name]
		dirs, err := m.marketplaceDirsKey(name)
		if err != nil {
			return fmt.Errorf("renaming marketplace %q, recorded under a name the store no longer accepts: %w", name, err)
		}
		key := recordedAlias{dirs: dirs, src: ref.Source}
		alias, err := m.migratedUnderAnAlias(dirs, ref, mk, reg, recordedThisRun)
		if err != nil {
			return fmt.Errorf("renaming marketplace %q, recorded under a name the store no longer accepts: %w", name, err)
		}
		if alias {
			// The family records the actual logical destination, including any
			// collision suffix. Retained caches still occupy their old paths.
			into := recordedThisRun[key]
			if reg, err = m.mergeIntoMigrated(mk, owners, reg, name, into, false); err != nil {
				return fmt.Errorf("merging marketplace %q, recorded under a name the store no longer accepts, into %q: %w", name, into, err)
			}
			_, _ = fmt.Fprintf(m.stderr(), "warning: marketplace %q was recorded under a name the store no longer accepts, and names the same marketplace as %q; merged its plugins into that record and dropped the duplicate\n", name, into)
			continue
		}
		newName, err := m.freeMarketplaceName(migratedMarketplaceName(name), mk, reg)
		if err != nil {
			return fmt.Errorf("renaming marketplace %q, recorded under a name the store no longer accepts: %w", name, err)
		}
		// Written down before the rename, so a process that stops here still
		// leaves the family for the next run the way the marker leaves it the
		// rename itself.
		rec.Renames = append(rec.Renames, recordedRename{
			From: name, To: newName, Source: ref.Source,
			Clone: dirs.clone, Cache: dirs.cache,
		})
		if err := m.saveMigrationRecord(rec); err != nil {
			return fmt.Errorf("recording the migration of marketplace %q, recorded under a name the store no longer accepts: %w", name, err)
		}
		if reg, err = m.migrateMarketplaceName(mk, owners, reg, name, newName); err != nil {
			return fmt.Errorf("renaming marketplace %q, recorded under a name the store no longer accepts, to %q: %w", name, newName, err)
		}
		recordedThisRun[key] = newName
		_, _ = fmt.Fprintf(m.stderr(), "warning: marketplace %q was recorded under a name the store no longer accepts; renamed it to %q\n", name, newName)
	}
	return nil
}

// recoverMarkedRename finishes the operation named by the existing marker.
// Clones keep their separate rename/rollback policy, while published caches
// never move. Logical keys follow the new name, valid InstallPaths stay, and
// reconcileMigrationPaths handles missing paths left by the older move-based
// writer. A taken rename destination is refused, not overwritten. A marker
// whose source record is already gone needs only removal. The caller holds
// the store lock.
func (m *Manager) recoverMarkedRename() error {
	marker, err := m.loadRenameMarker()
	if err != nil || marker == nil {
		return err
	}
	if why := marker.namesNoRename(); why != nil {
		_, _ = fmt.Fprintf(m.stderr(), "warning: %s in the plugin store names no rename to finish (%v); dropped it\n", renameMarkerFileName, why)
		m.removeRenameMarker()
		return nil
	}
	mk, err := m.loadMarketplaces()
	if err != nil {
		return err
	}
	ref, recorded := mk[marker.From]
	if !recorded {
		m.removeRenameMarker()
		return nil
	}
	if marker.Merge {
		return m.finishMarkedMerge(mk, *marker)
	}
	fail := func(err error) error {
		return m.migrationFailed(marker.From, marker.To, err)
	}
	if _, taken := mk[marker.To]; taken && marker.To != marker.From {
		path, err := m.storePath(renameMarkerFileName)
		if err != nil {
			return fail(err)
		}
		return fail(fmt.Errorf("another marketplace is recorded as %q; rename that one, or delete %s to abandon the unfinished rename", marker.To, path))
	}
	reg, err := m.loadRegistry()
	if err != nil {
		return fail(err)
	}
	registryAsFound := reg
	owners := registryKeyOwners(reg, mk)
	itsOwn, _, err := m.marketplaceDirsAreItsOwn(mk, reg, marker.From)
	if err != nil {
		return fail(err)
	}
	var undo []func() error
	if itsOwn {
		// What the entry recorded before the rename, because the rename clears
		// it: a location is only followed where the entry recorded one, so an
		// entry an interrupted fetch left with a clone at the canonical path
		// but no recorded location stays unfetched, exactly as the normal
		// rename leaves it — recording that clone would have a later Browse or
		// Install parse a partial clone instead of clearing and re-cloning.
		recordedLocation := ref.InstallLocation
		if ref, undo, err = m.moveMarketplaceClone(marker.From, marker.To, ref, mk); err != nil {
			return fail(err)
		}
		if ref.Source.Kind != SourceDirectory && recordedLocation != "" {
			// The rename's own rule, asked of the clone at the name the
			// rename was taking it to: an entry whose clone made the move
			// records it, and one with no clone there is unfetched, for the
			// next fetch to clone under the new name. A directory source is
			// referenced where the user put it, however far the rename got.
			clone := m.marketplaceDir(marker.To)
			haveClone, err := pathPresent(clone)
			if err != nil {
				return fail(err)
			}
			ref.InstallLocation = ""
			if haveClone {
				ref.InstallLocation = clone
			}
		}
	} else if ref.Source.Kind != SourceDirectory {
		// Another record owns the derived directories. Leave them alone and
		// leave a git-backed entry unfetched under its new logical name.
		ref.InstallLocation = ""
	}
	reg, err = m.reconcileMigrationPaths(mk, reg, owners, *marker)
	if err != nil {
		return fail(errors.Join(err, runUndo(undo)))
	}
	reg = rekeyRegistry(reg, owners, marker.From, marker.To, "", "")

	if err := m.saveRename(mk, marker.From, marker.To, ref, reg, registryAsFound); err != nil {
		undoErr := runUndo(undo)
		if undoErr == nil && !errors.Is(err, errStoreBetweenNames) {
			// Dropping the marker is safe only where the store is back at the
			// old name, and a nil undoErr does not say that here: the marker
			// exists because an earlier run stopped mid-rename, so the
			// directories may already sit under the destination, and this
			// attempt then finds nothing to move and nothing to undo. Whatever
			// is still under the destination is what the marker is keeping, so
			// anything there leaves it in place.
			moved, statErr := m.dirsUnder(marker.To)
			if statErr != nil {
				return fail(statErr)
			}
			if !moved {
				m.removeRenameMarker()
				return fail(err)
			}
		}
		return fail(errors.Join(err, undoErr))
	}
	m.removeRenameMarker()
	_, _ = fmt.Fprintf(m.stderr(), "warning: marketplace %q was being renamed to %q when an earlier run stopped; finished the rename\n", marker.From, marker.To)
	return nil
}

// finishMarkedMerge finishes a marked alias merge into an existing record.
// It preserves valid paths and uses the same source/ownership checks as a
// normal alias merge. The caller must hold the store lock.
func (m *Manager) finishMarkedMerge(mk Marketplaces, marker renameMarker) error {
	fail := func(err error) error {
		return m.migrationFailed(marker.From, marker.To, err)
	}
	if _, into := mk[marker.To]; !into {
		path, err := m.storePath(renameMarkerFileName)
		if err != nil {
			return fail(err)
		}
		return fail(fmt.Errorf("no marketplace is recorded as %q to merge into; delete %s to abandon the unfinished merge", marker.To, path))
	}
	reg, err := m.loadRegistry()
	if err != nil {
		return fail(err)
	}
	owners := registryKeyOwners(reg, mk)
	// Keep this existing recovery marker until both logical files record the
	// merge. Fresh merges never create one.
	if _, err := m.mergeIntoMigrated(mk, owners, reg, marker.From, marker.To, true); err != nil {
		return fail(err)
	}
	_, _ = fmt.Fprintf(m.stderr(), "warning: marketplace %q was being merged into %q when an earlier run stopped; finished the merge\n", marker.From, marker.To)
	return nil
}

// reconcileMigrationPaths reads only. A marker from the older move-based
// migration can leave a missing recorded path whose exact destination exists.
// Existing paths always win, even when both namespaces contain files. Neither
// missing files nor a new logical name authorize moving or replacing a tree.
func (m *Manager) reconcileMigrationPaths(mk Marketplaces, reg Registry, owners map[string]string, marker renameMarker) (Registry, error) {
	itsOwn, _, err := m.marketplaceDirsAreItsOwn(mk, reg, marker.From)
	if err != nil {
		return Registry{}, err
	}
	oldCache := filepath.Join(m.cacheDir(), marker.From)
	newCache := filepath.Join(m.cacheDir(), marker.To)
	// When another marketplace owns the derived directories, the older writer
	// moved only plugin directories owned exclusively by this record.
	moves := m.pluginCacheMoves(reg, marker.From, marker.To, owners)
	// Only these registry/source lists are immutable. Filesystem ownership
	// evidence is still checked in its original order for every candidate.
	var foreignEntries [][]InstallEntry
	for key, entries := range reg.Plugins {
		if owners[key] != marker.From && owners[key] != marker.To {
			foreignEntries = append(foreignEntries, entries)
		}
	}
	protectionPaths := marketplaceProtectionPaths(mk)
	canReconcile := func(path string) (string, bool, error) {
		rel, under := pathUnder(oldCache, path)
		if !under || !nameCanHaveDirectories(marker.From) {
			return "", false, nil
		}
		if !pathWithinDir(resolveBestEffort(oldCache), resolveBestEffort(path)) {
			return "", false, nil
		}
		if !itsOwn && !slices.ContainsFunc(moves, func(move pluginCacheMove) bool {
			_, under := pathUnder(move.from, path)
			return under
		}) {
			return "", false, nil
		}
		// Physical containment and ownership still apply after a crash. In
		// particular a symlink out, or another record using the destination,
		// is not evidence of this marker's move.
		for _, name := range []string{marker.From, marker.To} {
			inStore, err := m.marketplaceDirsInStore(name)
			if err != nil || !inStore {
				return "", false, err
			}
		}
		for _, entries := range foreignEntries {
			if installedUnder(newCache, entries) {
				return "", false, nil
			}
		}
		// Cleanup treats inspection errors as protection and continues. Recovery
		// must retain its marker and fail instead of committing unknown ownership.
		present, err := pathPresentNoFollow(newCache)
		if err != nil || !present {
			return "", false, err
		}
		for _, source := range protectionPaths {
			protect, err := m.sourceTouchesPath(source, newCache)
			if err != nil || protect {
				return "", false, err
			}
		}
		destination := filepath.Join(newCache, rel)
		present, err = pathPresent(destination)
		if err == nil && present && !pathWithinDir(resolveBestEffort(newCache), resolveBestEffort(destination)) {
			return "", false, nil
		}
		return destination, present, err
	}
	out := Registry{Version: reg.Version, Plugins: make(map[string][]InstallEntry, len(reg.Plugins))}
	for key, entries := range reg.Plugins {
		out.Plugins[key] = slices.Clone(entries)
		if owner, ok := owners[key]; !ok || (owner != marker.From && owner != marker.To) {
			continue
		}
		for i, entry := range entries {
			present, err := pathPresent(entry.InstallPath)
			if err != nil {
				return Registry{}, err
			}
			if present {
				continue
			}
			destination, moved, err := canReconcile(entry.InstallPath)
			if err != nil {
				return Registry{}, err
			}
			if moved {
				out.Plugins[key][i].InstallPath = destination
			}
		}
	}
	return out, nil
}

// renameMarker names an automatic rename in flight, or an older interrupted
// alias merge (Merge). Fresh merges use idempotent logical writes without a
// marker, so they cannot manufacture physical-move recovery evidence. Rename
// and old-marker recovery retain their existing clone rollback and journal
// rules. Explicit edits write none and refuse interrupted-operation leftovers.
type renameMarker struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Merge bool   `json:"merge"`
}

// namesNoRename says what is wrong with a marker that names no rename this
// store left in flight, or nil where it names one. A rename names the
// marketplace it renames and the name it takes, so a marker missing either
// was hand-written or is some other writer's file under the marker's name:
// there is nothing to finish, and finishing it anyway would move a clone onto
// whatever path the missing name derives. The destination a rename takes is
// one migratedMarketplaceName derived, so it is a name the store accepts;
// finishing a marker naming one the store refuses would put a clone where no
// operation could derive it — outside the marketplaces directory, for a
// traversing name.
func (marker renameMarker) namesNoRename() error {
	if marker.From == "" || marker.To == "" {
		return fmt.Errorf("a rename names the marketplace it renames and the name it takes, and this names %q and %q", marker.From, marker.To)
	}
	if pendingMigration(marker.To) {
		return fmt.Errorf("a rename takes a name the store can hold as a directory, and this names %q", marker.To)
	}
	return nil
}

// loadRenameMarker reads the marker the store holds, or nil where there is
// none. A marker that cannot be parsed fails the migration like the store
// files it sits beside: it names a rename that is half made, and no operation
// should run over one.
func (m *Manager) loadRenameMarker() (*renameMarker, error) {
	path, err := m.storePath(renameMarkerFileName)
	if err != nil {
		return nil, err
	}
	data, err := marketplaceReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, m.storeFileFailed(err, "reading %s", renameMarkerFileName)
	}
	var marker renameMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return nil, m.storeFileFailed(err, "parsing %s", renameMarkerFileName)
	}
	return &marker, nil
}

// writeRenameMarker records the rename or the merge that is about to be made,
// with the atomic write the store files get: a marker torn in half would name
// an operation nobody made.
func (m *Manager) writeRenameMarker(marker renameMarker) error {
	path, err := m.storePath(renameMarkerFileName)
	if err != nil {
		return err
	}
	body, err := marketplaceMarshalIndent(marker, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling the marker recording marketplace %q as %q: %w", marker.From, marker.To, err)
	}
	if err := marketplaceAtomicWriteFile(path, append(body, '\n'), 0o644); err != nil {
		return m.storeFileFailed(err, "writing %s", renameMarkerFileName)
	}
	return nil
}

// removeRenameMarker drops the marker once the rename it names is recorded or
// undone, or where it names no rename at all. Removing a marker that is not
// there is no failure: what the caller needs is that none survives. Nor is
// one that cannot be removed, which is why this reports rather than returns:
// every marker this drops names a rename nothing is left to make, and failing
// the lock acquisition over a file that means nothing would stop every store
// operation until the user deleted it by hand. The next lock holder tries
// again.
func (m *Manager) removeRenameMarker() {
	path, err := m.storePath(renameMarkerFileName)
	if err != nil {
		_, _ = fmt.Fprintf(m.stderr(), "warning: could not remove the plugin store's %s: %v\n", renameMarkerFileName, err)
		return
	}
	if err := marketplaceRemoveAll(path); err != nil {
		_, _ = fmt.Fprintf(m.stderr(), "warning: could not remove %s, which names no rename left to make: %v\n", path, err)
	}
}

// markerLeftForRecovery names the marker a failed rename or merge leaves
// behind, for the error that failure returns: the store may be between the two
// names, and what brings it to one is the next lock holder's recovery, so the
// user is told which file records the operation that is still to be finished.
func (m *Manager) markerLeftForRecovery(what string) error {
	path, err := m.storePath(renameMarkerFileName)
	if err != nil {
		return err
	}
	return fmt.Errorf("%s still records the %s, which the next store operation finishes", path, what)
}

// migrationRecord is the store's record of the renames the migration has made,
// each against the alias family it belongs to. The marker beside one rename
// lives only as long as that rename; this outlives the run, because a
// marketplace's other names can still be recorded in a later one — "a/./b"
// migrated now leaves "a/b" deriving the same directories with nothing to move,
// and only this says which existing marketplace those directories became.
type migrationRecord struct {
	Renames []recordedRename `json:"renames"`
}

// recordedRename is one rename the migration made: the name it moved from, the
// name it took, and the source the moved record names, which is what keeps one
// marketplace's aliases apart from another's when both derive the one pair.
//
// Clone and Cache are the directories the name derived, resolved while they
// still existed. They are stored rather than recomputed from From because From's
// directories have moved by the next run: resolving them again answers with the
// lexical path, which is not what a name reaching the same directory through a
// symlink derives, so the family would not be recognised.
type recordedRename struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Source Source `json:"source"`
	Clone  string `json:"clone"`
	Cache  string `json:"cache"`
}

// loadMigrationRecord reads the record the store holds, or the empty one where
// there is none. A record that cannot be parsed fails the migration like the
// store files it sits beside: it names families whose aliases would otherwise
// be migrated a second time.
func (m *Manager) loadMigrationRecord() (migrationRecord, error) {
	path, err := m.storePath(migrationRecordFileName)
	if err != nil {
		return migrationRecord{}, err
	}
	data, err := marketplaceReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return migrationRecord{}, nil
		}
		return migrationRecord{}, m.storeFileFailed(err, "reading %s", migrationRecordFileName)
	}
	var rec migrationRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return migrationRecord{}, m.storeFileFailed(err, "parsing %s", migrationRecordFileName)
	}
	return rec, nil
}

// saveMigrationRecord writes the record with the atomic write the store files
// get, and removes the file where the record names no family at all.
func (m *Manager) saveMigrationRecord(rec migrationRecord) error {
	if len(rec.Renames) == 0 {
		m.removeMigrationRecord()
		return nil
	}
	path, err := m.storePath(migrationRecordFileName)
	if err != nil {
		return err
	}
	body, err := marketplaceMarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling %s: %w", migrationRecordFileName, err)
	}
	if err := marketplaceAtomicWriteFile(path, append(body, '\n'), 0o644); err != nil {
		return m.storeFileFailed(err, "writing %s", migrationRecordFileName)
	}
	return nil
}

// removeMigrationRecord drops the record once it names no family the store can
// still hold. Like removeRenameMarker this reports rather than returns: every
// entry it drops names a destination the store no longer records, so a file left
// behind cannot fail an operation that would otherwise have worked.
func (m *Manager) removeMigrationRecord() {
	path, err := m.storePath(migrationRecordFileName)
	if err != nil {
		_, _ = fmt.Fprintf(m.stderr(), "warning: could not remove the plugin store's %s: %v\n", migrationRecordFileName, err)
		return
	}
	if err := marketplaceRemoveAll(path); err != nil {
		_, _ = fmt.Fprintf(m.stderr(), "warning: could not remove %s, which names no migration left to finish: %v\n", path, err)
	}
}

// pendingMigration reports whether a recorded name is one the migration will
// rename: the store's own rules refuse it, or no filesystem holds it as the
// directories it derives. It is the one predicate the migration, doctor and the
// marker check all ask, so none of them can disagree about which names are
// about to change — a name doctor called healthy is one the next lock holder
// would rename.
func pendingMigration(name string) bool {
	return validNameComponent("marketplace", name) != nil || !nameCanHaveDirectories(name)
}

// refusedMarketplaceNames is every recorded name the store will not take as it
// stands — done by pendingMigration — longest first and otherwise sorted. It
// is the seed order the migration
// sorts (migrationOrder): a name has to migrate before another only where one
// of the two relations there says so, and this decides between the names
// neither relation constrains. Longest first because that is the order the
// "@<name>" re-keying wants of them — only a longer name can share a shorter
// one's suffix — so a store where no relation binds anything migrates in it.
func refusedMarketplaceNames(mk Marketplaces) []string {
	var names []string
	for name := range mk {
		if pendingMigration(name) {
			names = append(names, name)
		}
	}
	slices.SortFunc(names, func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return strings.Compare(a, b)
	})
	return names
}

// migrationOrder is the order the refused names migrate in: the seed order
// refusedMarketplaceNames made, sorted so that a name either of two relations
// puts before another migrates before it.
//
// The directory relation is that a name whose clone resolves strictly inside
// another's has to move first, or the other's move carries it away: "a/b/.."
// resolves to <marketplaces>/a, the directory holding "a/b"'s clone, so
// moving it first would take that clone and that plugin cache off under a
// name deriving neither. It is also what marketplaceDirsAreItsOwn counts on
// when it passes over another refused name as an owner: a name whose
// directories are inside this one's has already left, so a directory still
// under the name is the name's own to move. The clone answers for the pair —
// the cache a name derives sits at the same depth under the cache directory.
//
// The key relation is that a name ending in "@<other>" has to re-key first,
// or the other's exact "@<other>" suffix cuts that much off its keys and
// lands its plugins under the other's new name: "../..@a/b" re-keys before
// "a/b".
//
// Neither relation orders the other's pairs. A name resolving outside the
// store can be the longer, shallower one ("../..@a/b" beside "a/b"), and a
// deeper name need not end in the shallower one's name at all, so the order
// has to come from both together.
//
// The directory relation binds only where the inner name has a directory of
// the store's to lose (dirInStore), which is what makes the two satisfiable
// together for every pair a store can hold. A name ending in "@<other>"
// carries the other's trailing components, so it can only resolve above the
// other's clone where that clone climbed out of the store first — and neither
// the marketplaces directory itself nor anything above it is a directory some
// rename moves. Binding the relation there would order such a pair against
// its keys for no directory at all, and the loser's keys would go with the
// winner's "@<other>" suffix.
func (m *Manager) migrationOrder(names []string) ([]string, error) {
	marketplaces, err := resolveForContainment(m.marketplacesDir())
	if err != nil {
		return nil, err
	}
	cache, err := resolveForContainment(m.cacheDir())
	if err != nil {
		return nil, err
	}
	clone := make(map[string]string, len(names))
	cached := make(map[string]string, len(names))
	for _, name := range names {
		_, derived, err := namedDir(m.marketplacesDir(), name)
		if err != nil {
			return nil, err
		}
		clone[name] = derived
		_, derivedCache, err := namedDir(m.cacheDir(), name)
		if err != nil {
			return nil, err
		}
		cached[name] = derivedCache
	}
	// A name goes before another where either directory of its own sits inside
	// one of that other's. Both are asked because either can nest alone: a
	// symlink under one of the store's directories nests a cache inside
	// another's while the clones are siblings, and the outer cache's move would
	// carry the inner one's files away just as the clone's would.
	nestedIn := func(a, b string) bool {
		if dirInStore(marketplaces, clone[a]) && clone[a] != clone[b] && pathWithinDir(clone[b], clone[a]) {
			return true
		}
		return dirInStore(cache, cached[a]) && cached[a] != cached[b] && pathWithinDir(cached[b], cached[a])
	}
	goesBefore := func(a, b string) bool {
		return nestedIn(a, b) || (strings.HasSuffix(a, "@"+b) && !nestedIn(b, a))
	}
	order := make([]string, 0, len(names))
	left := slices.Clone(names)
	for len(left) > 0 {
		// The first name left that nothing left has to go before. Three or
		// more names can constrain each other in a ring, which no order
		// satisfies; then the seed order's first goes and the rest follow it,
		// so a store like that migrates rather than looping here.
		next := 0
		for i, name := range left {
			if !slices.ContainsFunc(left, func(other string) bool { return other != name && goesBefore(other, name) }) {
				next = i
				break
			}
		}
		order = append(order, left[next])
		left = slices.Delete(left, next, next+1)
	}
	return order, nil
}

// maxComponentBytes is the longest a single filesystem component may be. NAME_MAX
// is 255 on the filesystems the store sits on, and numberedNameReserve is what
// the "-2", "-3", … a taken name's replacement can add, so a derived name within
// maxMigratedNameBytes still fits once freeMarketplaceName numbers it.
const (
	maxComponentBytes    = 255
	numberedNameReserve  = len("-999999")
	maxMigratedNameBytes = maxComponentBytes - numberedNameReserve
)

// migratedMarketplaceName derives the name a refused one is renamed to: path
// separators split it and the "." and ".." components go, the rest joined
// by '-'; every '@' becomes '-'; a scratch name loses its dot; and a name with
// nothing left, one the store still refuses, or one that no filesystem will take
// as a path component becomes "marketplace". The last is not hypothetical: the
// derived name becomes a directory name, and a legacy name can derive one longer
// than a component may be, or carrying a byte no filename can carry, which would
// otherwise fail the probe that looks for it and leave every store operation
// failing rather than migrating the name away.
func migratedMarketplaceName(name string) string {
	var parts []string
	for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part != "." && part != ".." {
			parts = append(parts, part)
		}
	}
	derived := strings.ReplaceAll(strings.Join(parts, "-"), "@", "-")
	if derived == stagingCloneName || derived == asideCloneName {
		derived = derived[1:]
	}
	if !usableMarketplaceName(derived) {
		return "marketplace"
	}
	return derived
}

// usableMarketplaceName reports whether name can stand as one store path
// component: a component the filesystem takes at all, no control character, the
// store's own rules for a name, and short enough that the numbered replacement a
// collision appends still fits. The length bound is maxMigratedNameBytes rather
// than maxComponentBytes, because freeMarketplaceName may add "-2", "-3", …
func usableMarketplaceName(name string) bool {
	if !pathComponentName(name) || len(name) > maxMigratedNameBytes {
		return false
	}
	if strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return false
	}
	return validNameComponent("marketplace", name) == nil
}

// pathComponentName reports whether name can stand as one filesystem component
// at all: no byte a filename cannot carry, and no longer than a component may be.
func pathComponentName(name string) bool {
	return len(name) <= maxComponentBytes && !strings.ContainsRune(name, 0)
}

// nameCanHaveDirectories reports whether any directory the store derives from a
// recorded name could exist: the name has to be free of NULs, and every
// component it splits into has to stand as a filesystem component. A name that
// cannot have them has none to move, so nothing is probed for it, and probing is
// what fails the whole migration.
//
// The whole name's length is deliberately not the question: two legal 200-byte
// components are a real clone and cache even though the name is 401 bytes, and
// those directories have to move. Control characters are not excluded here even
// though usableMarketplaceName excludes them from a name the migration invents:
// a filesystem that takes them holds their directories, and those move like any
// other.
func nameCanHaveDirectories(name string) bool {
	if strings.ContainsRune(name, 0) {
		return false
	}
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' })
	for _, part := range parts {
		if len(part) > maxComponentBytes {
			return false
		}
	}
	return true
}

// freeMarketplaceName is base, or the first of base-2, base-3, … that nothing
// occupies: no recorded marketplace, and none of the residue a rename onto
// it would bury (refuseLeftoversUnder).
func (m *Manager) freeMarketplaceName(base string, mk Marketplaces, reg Registry) (string, error) {
	for n := 1; ; n++ {
		candidate := base
		if n > 1 {
			candidate = fmt.Sprintf("%s-%d", base, n)
		}
		if _, recorded := mk[candidate]; recorded {
			continue
		}
		err := m.refuseLeftoversUnder(candidate, reg)
		if errors.Is(err, ErrMarketplaceExists) {
			continue
		}
		if err != nil {
			return "", err
		}
		return candidate, nil
	}
}

// marketplaceDirs is the clone directory and the plugin cache a recorded name
// derives, the pair two records of one marketplace share. Deriving the same
// new name is not that: "a/./b" and "a\b" both derive "a-b", from directories
// of their own. So it is the pair a run holds each rename it made against,
// under which an alias of a migrated name finds that rename and a marketplace
// that only shares the derived name has a slot of its own. Sharing the pair is
// where the question of one marketplace or two starts, not where it ends —
// the two records have to name the one source too (migratedUnderAnAlias).
type marketplaceDirs struct {
	clone, cache string
}

// recordedAlias is the key one marketplace's alias family is looked up and
// recorded under: the two directories a refused name derives and the source its
// record names. The directories alone are not enough. A record naming a
// different source takes a numbered name for the same pair without ending
// either source's aliases, so the pair's entry would be overwritten, and a later
// true alias would look up the stranger, fail the source test, and split into a
// third record instead of merging.
type recordedAlias struct {
	dirs marketplaceDirs
	src  Source
}

// marketplaceDirsKey is the pair a recorded name derives, resolved the way the
// ownership checks resolve it (namedDir): two refused names can reach the one
// clone and the one cache through a symlink inside the store, and they derive
// the same physical directories, so the alias map has to see them as one
// marketplace even though their lexical paths differ. Resolving both is what
// makes "link/x" and "real/x" one record, which is what the alias check is for.
func (m *Manager) marketplaceDirsKey(name string) (marketplaceDirs, error) {
	_, clone, err := namedDir(m.marketplacesDir(), name)
	if err != nil {
		return marketplaceDirs{}, err
	}
	_, cache, err := namedDir(m.cacheDir(), name)
	if err != nil {
		return marketplaceDirs{}, err
	}
	return marketplaceDirs{clone: clone, cache: cache}, nil
}

// migratedUnderAnAlias uses the recorded physical family and exact source,
// not old-cache absence: retained revisions intentionally still live there.
// A git record with a surviving clone, or one never fetched, has not lost its
// own clone to that family's rename and remains a separate marketplace.
// Directory sources are identified by their source and recorded family alone.
// The destination must still hold marketplace data or keys, so stale records
// cannot absorb a new source into an empty name.
func (m *Manager) migratedUnderAnAlias(dirs marketplaceDirs, ref MarketplaceRef, mk Marketplaces, reg Registry, recordedThisRun map[recordedAlias]string) (bool, error) {
	into, madeHere := recordedThisRun[recordedAlias{dirs: dirs, src: ref.Source}]
	if !madeHere {
		return false, nil
	}
	if mk[into].Source != ref.Source {
		return false, nil
	}
	if ref.Source.Kind != SourceDirectory && ref.InstallLocation == "" {
		return false, nil
	}
	gone := []string{dirs.clone}
	// A directory source's install location is the user's own source path and
	// says nothing about what the store holds.
	if ref.Source.Kind != SourceDirectory && ref.InstallLocation != "" {
		gone = append(gone, ref.InstallLocation)
	}
	for _, dir := range gone {
		present, err := pathPresent(dir)
		if err != nil {
			return false, err
		}
		if present {
			return false, nil
		}
	}
	// Whether anything is under the name that rename took is what
	// refuseLeftoversUnder asks of a rename's destination — a clone, a plugin
	// cache, a registry key — read here as what it moved there. With nothing
	// of the marketplace under either name there is nothing to merge, and the
	// duplicate record takes a numbered name of its own.
	err := m.refuseLeftoversUnder(into, reg)
	if errors.Is(err, ErrMarketplaceExists) {
		return true, nil
	}
	return false, err
}

// mergeIntoMigrated folds an alias into its recorded family's marketplace.
// Only logical ownership changes. Existing InstallPaths stay, including paths
// in the retained original cache. Only finishMarkedMerge sets recovering: an
// older physical-move marker may reconcile missing paths. Fresh merges write
// no marker: the existing alias-family record and refused source name make
// registry-first logical writes idempotent, including a split-write retry.
func (m *Manager) mergeIntoMigrated(mk Marketplaces, owners map[string]string, reg Registry, name, recorded string, recovering bool) (Registry, error) {
	registryAsFound := reg
	if mk[name].Source != mk[recorded].Source {
		return registryAsFound, fmt.Errorf("marketplaces %q and %q name different sources: %w", name, recorded, ErrMarketplaceExists)
	}
	if recovering {
		var err error
		reg, err = m.reconcileMigrationPaths(mk, reg, owners, renameMarker{From: name, To: recorded, Merge: true})
		if err != nil {
			return registryAsFound, err
		}
	}
	// A fresh logical merge must not create evidence of an older cache move.
	// Until the final marketplaces write, the refused alias remains recorded
	// and its exact source/family identifies the same destination on retry.
	// Re-keying again preserves InstallPaths, even if the first registry
	// write succeeded. An existing recovery marker stays until both writes do.
	existing := reg
	reg = rekeyRegistry(reg, owners, name, recorded, "", "")
	// Keep the existing installed selection when both aliases name the same
	// plugin. Both physical trees remain available to existing sessions.
	for key, entries := range existing.Plugins {
		if owner, ok := owners[key]; ok && owner == recorded {
			reg.Plugins[key] = entries
		}
	}
	// The recorded marketplace is written back as it stands, so what this
	// save records is the duplicate's removal and the keys that moved.
	if err := m.saveRename(mk, name, recorded, mk[recorded], reg, registryAsFound); err != nil {
		if recovering {
			return registryAsFound, m.migrationFailed(name, recorded, errors.Join(err, m.markerLeftForRecovery("merge")))
		}
		return registryAsFound, m.migrationFailed(name, recorded, err)
	}
	m.removeRenameMarker()
	return reg, nil
}

// migrateMarketplaceName changes one refused logical name under the store
// lock. Only an owned clone moves; published caches and InstallPaths stay.
// A clone outside the store or owned by another record stays untouched and a
// git-backed entry becomes unfetched, while directory sources keep their live
// location. Registry and marketplace writes retain the existing journal and
// rollback order.
func (m *Manager) migrateMarketplaceName(mk Marketplaces, owners map[string]string, reg Registry, name, newName string) (Registry, error) {
	ref, registryAsFound := mk[name], reg
	itsOwn, _, err := m.marketplaceDirsAreItsOwn(mk, reg, name)
	if err != nil {
		return registryAsFound, err
	}
	// The marker names this rename before any of it happens, so a run that
	// exits partway through leaves the next one the two names rather than a
	// store state to read them out of. The branch that moves nothing writes
	// one too: its two file writes can be interrupted between, and the keys
	// the first of them leaves under the new name are what a marker-less run
	// reads as a removed marketplace's residue.
	if err := m.writeRenameMarker(renameMarker{From: name, To: newName}); err != nil {
		return registryAsFound, err
	}
	var undo []func() error
	if itsOwn {
		if ref, undo, err = m.moveMarketplaceClone(name, newName, ref, mk); err != nil {
			m.removeRenameMarker()
			return registryAsFound, m.migrationFailed(name, newName, err)
		}
	} else {
		// Derived directories outside the store or held by another record
		// are not this rename's clone to move. All published caches stay.
		if ref.Source.Kind != SourceDirectory {
			ref.InstallLocation = ""
		}
	}
	reg = rekeyRegistry(reg, owners, name, newName, "", "")

	if err := m.saveRename(mk, name, newName, ref, reg, registryAsFound); err != nil {
		// A marker means the store may be between the two names, so only a
		// rollback that provably brought it to one may drop it: the registry
		// back as it was found (errStoreBetweenNames) and every directory back
		// under the old name. Where any part of the rollback failed the rename
		// is still half made, so the marker stays and the error names it, and
		// the next lock holder finishes the rename from whatever is left.
		undoErr := runUndo(undo)
		if undoErr == nil && !errors.Is(err, errStoreBetweenNames) {
			m.removeRenameMarker()
			return registryAsFound, err
		}
		return registryAsFound, m.migrationFailed(name, newName, errors.Join(err, undoErr, m.markerLeftForRecovery("rename")))
	}
	m.removeRenameMarker()
	return reg, nil
}

// dirsUnder reports whether either directory a name derives is in the store. A
// recovery asks it of the marker's destination: directories there are what an
// earlier run already moved, and the marker is the only record left of whose
// they are.
func (m *Manager) dirsUnder(name string) (bool, error) {
	for _, dir := range []string{m.marketplaceDir(name), filepath.Join(m.cacheDir(), name)} {
		present, err := pathPresent(dir)
		if err != nil {
			return false, err
		}
		if present {
			return true, nil
		}
	}
	return false, nil
}

// pluginCacheMove describes the exact move an older writer could have made
// for an entry whose derived directories another marketplace owns:
// cache/<old>/<plugin> to cache/<new>/<plugin>.
type pluginCacheMove struct{ plugin, from, to string }

// pluginCacheMoves describes the older writer's per-plugin moves for recovery,
// without performing them. Only exclusively owned plugin directories qualify.
//
// A directory another key installs into is the owner's rather than this
// entry's: a name cleaning to the owner's own cache ("./x" beside "x") derives
// the owner's plugin directories themselves, and one of them can hold the one
// plugin both records name. Moving it would leave the owner's entry pointing
// where the files no longer are, and it already sits at the depth Gc reads, so
// it stays and this entry's key goes on naming it there.
func (m *Manager) pluginCacheMoves(reg Registry, name, newName string, owners map[string]string) []pluginCacheMove {
	oldCache, newCache := filepath.Join(m.cacheDir(), name), filepath.Join(m.cacheDir(), newName)
	var moves []pluginCacheMove
	for key, entries := range reg.Plugins {
		// The same ownership rule rekeyRegistry uses: a key belongs to the
		// longest recorded name it ends in. Selecting by the exact suffix would
		// move a cache a longer name's key names without moving that key.
		if owner, ok := owners[key]; !ok || owner != name {
			continue
		}
		plugin := strings.TrimSuffix(key, "@"+name)
		if unsafePathComponent(plugin) {
			// A recorded key whose plugin is not one path component names no
			// cache this rename may build a path from.
			continue
		}
		from := filepath.Join(oldCache, plugin)
		if !installedUnder(from, entries) || anotherKeyInstallsUnder(reg, key, from) {
			continue
		}
		moves = append(moves, pluginCacheMove{plugin: plugin, from: from, to: filepath.Join(newCache, plugin)})
	}
	// Map order decides nothing: this is the order the moves are made in and,
	// reversed, the order a failed rename puts them back in.
	slices.SortFunc(moves, func(a, b pluginCacheMove) int { return strings.Compare(a.plugin, b.plugin) })
	return moves
}

// installedUnder reports whether any of entries is installed under dir. Both
// sides are resolved, because this asks where files are, not what the store
// wrote: dir is derived from a name while the entry names a path the store
// recorded, and a symlink under the cache can make them the same directory
// without making them the same string. resolveBestEffort answers with the path
// itself where it is not there, so a recorded path that no longer exists
// compares as itself — and there is nothing under it to move either way.
func installedUnder(dir string, entries []InstallEntry) bool {
	resolved := resolveBestEffort(dir)
	return slices.ContainsFunc(entries, func(entry InstallEntry) bool {
		return pathWithinDir(resolved, resolveBestEffort(entry.InstallPath))
	})
}

// resolveBestEffort resolves path for a comparison about disk, and answers with
// the path itself where it cannot: resolveForContainment only fails on
// filepath.Abs, and a path that is not there keeps its absolute form.
func resolveBestEffort(path string) string {
	if resolved, err := resolveForContainment(path); err == nil {
		return resolved
	}
	return path
}

// anotherKeyInstallsUnder reports whether a registry key other than own has an
// install under dir, physically: see installedUnder for why the comparison is
// resolved rather than lexical. This is the guard that keeps a rename from
// taking a plugin directory another marketplace's record still names, and a
// cache symlink is exactly how two names reach one directory.
func anotherKeyInstallsUnder(reg Registry, own, dir string) bool {
	for key, entries := range reg.Plugins {
		if key != own && installedUnder(dir, entries) {
			return true
		}
	}
	return false
}

// marketplaceDirsAreItsOwn reports whether the two directories a recorded name
// derives are this marketplace's to move: inside the store, and none of them
// another recorded marketplace's. Ownership rather than containment is what the
// move has to ask, because a refused name can alias another entry's
// directories — "a/b" recorded beside a valid "a" derives a subdirectory of a's
// clone and the cache directory of a's plugin "b", and "./x" beside "x" cleans
// to x's own two — and moving those would leave that marketplace recording a
// clone that is no longer there and its plugins pointing at moved paths. When
// they are not its own, the marketplace that owns them is named too; a name
// whose own directories fall outside the store is nobody's, so that name is
// then empty.
func (m *Manager) marketplaceDirsAreItsOwn(mk Marketplaces, reg Registry, name string) (bool, string, error) {
	inStore, err := m.marketplaceDirsInStore(name)
	if err != nil || !inStore {
		return false, "", err
	}
	for _, dir := range []string{m.marketplacesDir(), m.cacheDir()} {
		resolvedDir, derived, err := namedDir(dir, name)
		if err != nil {
			return false, "", err
		}
		for other := range mk {
			if other == name {
				continue
			}
			// A refused name is migrating too, and one whose directories
			// are inside this one's goes first (migrationOrder), so a
			// refused name still holding a directory around this one has
			// yet to take it away:
			// "a@b/c" deferring to "a@b" would re-key where it stands and
			// then find its directories gone, moved away under "a-b" with
			// the rest of "a@b"'s.
			if validNameComponent("marketplace", other) != nil {
				continue
			}
			_, othersDir, err := namedDir(dir, other)
			if err != nil {
				return false, "", err
			}
			// A name whose own directory resolves outside the store — a
			// clone that is a symlink out — owns nothing here, whatever
			// that directory contains.
			if !dirInStore(resolvedDir, othersDir) || !pathWithinDir(othersDir, derived) {
				continue
			}
			held, err := m.otherHoldsDir(other, mk[other], reg, mk, dir, othersDir)
			if err != nil {
				return false, "", err
			}
			if held {
				return false, other, nil
			}
		}
	}
	return true, "", nil
}

// otherHoldsDir reports whether the store holds anything of the marketplace
// recorded as other at dir: under the marketplaces directory, the clone the
// record points at; under the cache, an install of a key that names it. A name
// nothing has fetched holds nothing there — its record points at no clone and
// none of its keys has an install — so it owns nothing, however its name sits
// around another marketplace's directories.
//
// That distinction is what keeps a nested name's directories: a fetch clears and
// refills the directory it clones into, so a clone left beneath an unfetched
// parent is destroyed by the parent's first fetch.
func (m *Manager) otherHoldsDir(other string, ref MarketplaceRef, reg Registry, mk Marketplaces, dir, othersDir string) (bool, error) {
	if dir == m.marketplacesDir() {
		if ref.InstallLocation == "" {
			return false, nil
		}
		resolved, err := resolveForContainment(ref.InstallLocation)
		if err != nil {
			return false, err
		}
		return pathWithinDir(resolved, othersDir), nil
	}
	for key, entries := range reg.Plugins {
		if owner, ok := registryKeyOwner(key, mk); !ok || owner != other {
			continue
		}
		if installedUnder(othersDir, entries) {
			return true, nil
		}
	}
	return false, nil
}

// marketplaceDirsInStore reports whether both directories a recorded name
// derives — the clone under the marketplaces directory and the plugin cache
// under the cache directory — really sit inside them. Both have to be, and the
// first that is not answers for the pair: a name whose clone resolves outside
// the store while its cache resolves inside is possible, and moves neither, so
// a git-backed one is left unfetched with its in-store cache behind it, under a
// name no later operation derives.
func (m *Manager) marketplaceDirsInStore(name string) (bool, error) {
	if !nameCanHaveDirectories(name) {
		// No filesystem can hold a directory under this name, so the store has
		// none and there is nothing to probe: answering "outside" sends the
		// entry down the re-key path, which is all a name with no directories
		// has left to do.
		return false, nil
	}
	for _, dir := range []string{m.marketplacesDir(), m.cacheDir()} {
		resolvedDir, derived, err := namedDir(dir, name)
		if err != nil {
			return false, err
		}
		if !dirInStore(resolvedDir, derived) {
			return false, nil
		}
	}
	return true, nil
}

// namedDir resolves the directory a recorded name derives under dir, and dir
// itself, for comparison. The name is joined onto the resolved directory, and
// the join resolved in turn, so that a store reached through a symlink compares
// like with like whether or not the derived path exists, and a symlink inside
// the store cannot hide an escape.
//
// dir is the marketplaces or the cache directory, and containment is judged
// under it wherever it resolves rather than under the resolved store root.
// Wherever the user pointed those two, they are the directories every
// marketplace operation already clones into, removes from and renames within,
// so requiring them under the root would refuse a store deliberately put on
// another volume. Making either one a symlink needs write access to the store
// root, and the escape a symlink under them could still hide is the one the
// resolved join above closes.
func namedDir(dir, name string) (resolvedDir, derived string, err error) {
	resolvedDir, err = resolveForContainment(dir)
	if err != nil {
		return "", "", err
	}
	derived, err = resolveForContainment(filepath.Join(resolvedDir, name))
	if err != nil {
		return "", "", err
	}
	return resolvedDir, derived, nil
}

// dirInStore reports whether a derived directory is one of dir's own. A name
// that joins to dir itself (".", "") names no marketplace directory, so it is
// out with the names that land outside.
func dirInStore(dir, derived string) bool {
	return derived != dir && pathWithinDir(dir, derived)
}
