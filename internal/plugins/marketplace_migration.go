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
// One rename moves up to two directories and writes two files, so a process
// that exits in the middle of one leaves a store half renamed. What names the
// rename in flight is a marker every rename writes before it changes anything
// and removes once both files record the new name (renameMarker): the next
// lock holder finishes exactly the rename the marker names, whether that
// rename had directories of its own to move or only the two files to write,
// and without one there is no rename in flight, so a removed marketplace's
// residue under the same derived name is never mistaken for one. The merge
// that folds a second record of one marketplace into the first writes the
// same marker, saying that is what it names: it writes two files too, and the
// keys its first write moves look exactly like that residue to a run with no
// marker to read.

// loadMigratedMarketplaces reads the marketplaces file without the store lock
// and hands back what a lock holder would see. A name the store refuses means
// no lock holder has migrated this store yet (lockStore), so this takes the
// lock — which migrates — and reads back what that left; a second reader that
// arrives meanwhile finds nothing left to do once it holds the lock. On a
// migrated store nothing is locked, so a listing never queues behind a fetch.
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
// nothing to migrate never reaches it.
func (m *Manager) loadMigratedMarketplaces(ctx context.Context, acquire lockAcquirer) (Marketplaces, error) {
	mk, err := m.loadMarketplaces()
	if err != nil || len(refusedMarketplaceNames(mk)) == 0 {
		return mk, err
	}
	release, err := m.lockStore(ctx, acquire, 30*time.Second)
	if err != nil {
		return nil, err
	}
	defer release()
	return m.loadMarketplaces()
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
	// The name each rename of this run recorded, against the two directories
	// the refused name derived (marketplaceDirs), which are the directories an
	// alias of that name derives too. A merge records none.
	recordedThisRun := map[marketplaceDirs]string{}
	for _, name := range names {
		dirs, err := m.marketplaceDirsKey(name)
		if err != nil {
			return fmt.Errorf("renaming marketplace %q, recorded under a name the store no longer accepts: %w", name, err)
		}
		alias, err := m.migratedUnderAnAlias(dirs, mk[name], mk, reg, recordedThisRun)
		if err != nil {
			return fmt.Errorf("renaming marketplace %q, recorded under a name the store no longer accepts: %w", name, err)
		}
		if alias {
			// The record it merges into is where that rename put the
			// marketplace, which is the derived base only when the base was
			// free: the clone and the cache both names derive moved with it.
			into := recordedThisRun[dirs]
			if reg, err = m.mergeIntoMigrated(mk, reg, name, into); err != nil {
				return fmt.Errorf("merging marketplace %q, recorded under a name the store no longer accepts, into %q: %w", name, into, err)
			}
			_, _ = fmt.Fprintf(m.stderr(), "warning: marketplace %q was recorded under a name the store no longer accepts, and names the same marketplace as %q; merged its plugins into that record and dropped the duplicate\n", name, into)
			continue
		}
		newName, err := m.freeMarketplaceName(migratedMarketplaceName(name), mk, reg)
		if err != nil {
			return fmt.Errorf("renaming marketplace %q, recorded under a name the store no longer accepts: %w", name, err)
		}
		if reg, err = m.migrateMarketplaceName(mk, reg, name, newName); err != nil {
			return fmt.Errorf("renaming marketplace %q, recorded under a name the store no longer accepts, to %q: %w", name, newName, err)
		}
		recordedThisRun[dirs] = newName
		_, _ = fmt.Fprintf(m.stderr(), "warning: marketplace %q was recorded under a name the store no longer accepts; renamed it to %q\n", name, newName)
	}
	return nil
}

// recoverMarkedRename finishes the rename a marker names, which is the one a
// run that exited partway through had in flight: whatever of the clone and
// the cache is still under the old name moves, whatever keys are still under
// it are re-keyed, and the entry is recorded under the new name. A step that
// rename already took is one this finds nothing left to do for, and a rename
// whose directories are another marketplace's moves nothing here either:
// marketplaceDirsAreItsOwn asks about the recorded names, so the store
// answers it after the crash the way it answered the rename before it.
//
// A marker naming an old name nothing records is a run that exited between
// the marketplaces write and the marker's removal — the rename is recorded,
// so only the marker is left to drop — and one naming no rename at all
// (namesNoRename) goes the same way. A marker naming a merge goes to
// finishMarkedMerge, which moves no directories and folds into a record that
// is there by design. A destination another marketplace is recorded under is
// the one thing this refuses rather than finishes: that record was made
// between the crash and now, what sits under the name is a mix of what the
// rename moved there and what the user put there, and nothing left in the
// store tells the two apart, so the acquisition fails naming both names and
// the marker. The caller must hold the store lock.
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
		return fmt.Errorf("finishing the rename of marketplace %q to %q, which an earlier run left unfinished: %w", marker.From, marker.To, err)
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
	itsOwn, owner, err := m.marketplaceDirsAreItsOwn(mk, marker.From)
	if err != nil {
		return fail(err)
	}
	var undo []func() error
	if itsOwn {
		if ref, reg, undo, err = m.moveMarketplace(marker.From, marker.To, ref, reg); err != nil {
			return fail(err)
		}
		if ref.Source.Kind != SourceDirectory {
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
	} else {
		// The rename left the other marketplace's directories where they are
		// and moved only the plugin caches the entry's own keys reference, so
		// finishing it repeats those moves — each presence-checked, so one
		// the rename already made is skipped — and makes the same two writes:
		// whatever keys are still under the old name follow their install
		// paths to the new one, and a git-backed entry is left unfetched, for
		// the next fetch to clone under the new name.
		if ref.Source.Kind != SourceDirectory {
			ref.InstallLocation = ""
		}
		if owner == "" {
			reg = rekeyRegistry(reg, marker.From, marker.To, "", "")
		} else if reg, undo, err = m.movePluginCachesToNewName(reg, marker.From, marker.To); err != nil {
			return fail(err)
		}
	}
	if err := m.saveRename(mk, marker.From, marker.To, ref, reg, registryAsFound); err != nil {
		undoErr := runUndo(undo)
		if undoErr == nil && !errors.Is(err, errStoreBetweenNames) {
			// Everything the recovery moved is back and the registry is as it
			// was found, so the store is at the old name with nothing left to
			// finish: the marker goes, and the next lock holder migrates the
			// refused name afresh rather than retrying a destination that may
			// since have been taken.
			m.removeRenameMarker()
			return fail(err)
		}
		return fail(errors.Join(err, undoErr))
	}
	m.removeRenameMarker()
	_, _ = fmt.Fprintf(m.stderr(), "warning: marketplace %q was being renamed to %q when an earlier run stopped; finished the rename\n", marker.From, marker.To)
	return nil
}

// finishMarkedMerge finishes the merge a marker names: whatever keys are
// still under the duplicate's name move to the record it folds into, their
// install paths rewritten onto that record's cache, and the duplicate's
// record goes (mergeIntoMigrated, which finds nothing left to do for a step
// the merge already took). The record it folds into is recorded by design —
// both records stand until the one save that drops the duplicate — so where a
// rename refuses a destination something else holds, this expects one and
// refuses a marker naming a record nothing holds, rather than merging into a
// record it would have to invent. The caller must hold the store lock.
func (m *Manager) finishMarkedMerge(mk Marketplaces, marker renameMarker) error {
	fail := func(err error) error {
		return fmt.Errorf("finishing the merge of marketplace %q into %q, which an earlier run left unfinished: %w", marker.From, marker.To, err)
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
	// The merge names itself in the marker again and drops it once both files
	// record it, so what is left to say here is that it was finished.
	if _, err := m.mergeIntoMigrated(mk, reg, marker.From, marker.To); err != nil {
		return fail(err)
	}
	_, _ = fmt.Fprintf(m.stderr(), "warning: marketplace %q was being merged into %q when an earlier run stopped; finished the merge\n", marker.From, marker.To)
	return nil
}

// renameMarker names the operation the migration has in flight: the rename of
// a marketplace to a name the store accepts, or the merge of a second record
// of that one marketplace into the record the rename made (Merge). Both write
// it before they change anything and remove it once both store files record
// the result, and the rename removes it once a failed save has put the store
// back at one of the two names — which the merge cannot do, having no
// directories to put back and so no rollback to judge, so its marker stays
// until the merge is finished. A marker on disk therefore means an operation
// that got somewhere in between, and recoverMarkedRename finishes it. Which
// of the two it names decides how: a rename may have directories of its own
// to move and refuses a destination another marketplace took, while a merge
// moves nothing and folds into a record that is there by design. The rename
// an edit makes writes none: it refuses the leftovers a stopped run left
// under the new name and says so, and the user retrying it is there to read
// that.
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
	return validNameComponent("marketplace", marker.To)
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
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var marker renameMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
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
	return marketplaceAtomicWriteFile(path, append(body, '\n'), 0o644)
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

// refusedMarketplaceNames is every recorded name validNameComponent refuses,
// longest first and otherwise sorted. It is the seed order the migration
// sorts (migrationOrder): a name has to migrate before another only where one
// of the two relations there says so, and this decides between the names
// neither relation constrains. Longest first because that is the order the
// "@<name>" re-keying wants of them — only a longer name can share a shorter
// one's suffix — so a store where no relation binds anything migrates in it.
func refusedMarketplaceNames(mk Marketplaces) []string {
	var names []string
	for name := range mk {
		if validNameComponent("marketplace", name) != nil {
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
	clone := make(map[string]string, len(names))
	for _, name := range names {
		_, derived, err := namedDir(m.marketplacesDir(), name)
		if err != nil {
			return nil, err
		}
		clone[name] = derived
	}
	nestedIn := func(a, b string) bool {
		return dirInStore(marketplaces, clone[a]) && clone[a] != clone[b] && pathWithinDir(clone[b], clone[a])
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

// migratedUnderAnAlias reports whether the marketplace the entry deriving dirs
// records is already migrated: an alias of its name — one of the several
// refused names deriving the one clone directory and the one plugin cache for
// the one source — went first, and this record is that marketplace's second.
//
// Naming the same source is half of what makes two records one marketplace,
// because the directories are only the other half: a directory source keeps
// neither of them in the store, so "a/./b" pointing at one directory and
// "a/b" pointing at another derive the one absent pair while naming two
// marketplaces. Two records deriving the same directories and naming
// different sources contradict each other, whatever kind the sources are, and
// keeping both under numbered names loses nothing — the second comes out
// unfetched, or, for a directory source, referenced at its own path — where a
// merge would drop its source and key its plugins under a marketplace that is
// not its own.
//
// The record an alias belongs to is the one this run made for a name deriving
// the same two directories (recordedThisRun, marketplaceDirs), under
// whatever name that rename took: residue under the derived base pushes the
// first of two aliases onto a numbered name, and the second follows it there,
// because the clone and the cache both names derive went with it. Nothing in
// the store tells a base name a rename left from one the user added — their
// sources differ either way, and a record outlives the process that made it —
// so anything else recorded there is a stranger, and merging into it would
// drop this entry's source and key its plugins under a marketplace that is
// not its own. A run that exits between two aliases
// leaves the user a duplicate record instead, which is the safer failure.
//
// Its own directories gone is the other half: they are what a rename moves,
// so a marketplace still holding either of them is one no rename has taken
// from here — an entry whose directories another marketplace owns keeps them
// and is nobody's alias. An entry the store fetched records where it put the
// clone, so that path answers with them. One it never fetched has neither
// directory to have lost, so nothing of it was ever moved and it is nobody's
// second record: what it has of its own is a source, which a merge would drop
// and a numbered name keeps.
func (m *Manager) migratedUnderAnAlias(dirs marketplaceDirs, ref MarketplaceRef, mk Marketplaces, reg Registry, recordedThisRun map[marketplaceDirs]string) (bool, error) {
	into, madeHere := recordedThisRun[dirs]
	if !madeHere {
		return false, nil
	}
	if mk[into].Source != ref.Source {
		return false, nil
	}
	if ref.Source.Kind != SourceDirectory && ref.InstallLocation == "" {
		return false, nil
	}
	gone := []string{dirs.clone, dirs.cache}
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

// mergeIntoMigrated folds a duplicate record into the marketplace already
// recorded under the name both of them derive: its keys move there, their
// install paths rewritten from its cache directory onto the recorded name's,
// where the rename that migrated first put the files, and its record goes.
// One record per marketplace is what the two names always described.
func (m *Manager) mergeIntoMigrated(mk Marketplaces, reg Registry, name, recorded string) (Registry, error) {
	registryAsFound := reg
	// The marker names this merge before any of it happens, for the reason a
	// rename's does: the two writes below can be interrupted between, and the
	// keys the first of them moves are what a run with no marker to read takes
	// for a removed marketplace's residue.
	if err := m.writeRenameMarker(renameMarker{From: name, To: recorded, Merge: true}); err != nil {
		return registryAsFound, err
	}
	oldCache, newCache := filepath.Join(m.cacheDir(), name), filepath.Join(m.cacheDir(), recorded)
	reg = rekeyRegistry(reg, name, recorded, oldCache, newCache)
	// Where both records hold the same plugin, the entry already keyed under
	// the recorded name stays: the cache moved under that name, so that entry
	// is the one naming an install path that is there.
	suffix := "@" + recorded
	for key, entries := range registryAsFound.Plugins {
		if strings.HasSuffix(key, suffix) {
			reg.Plugins[key] = entries
		}
	}
	// The recorded marketplace is written back as it stands, so what this
	// save records is the duplicate's removal and the keys that moved.
	if err := m.saveRename(mk, name, recorded, mk[recorded], reg, registryAsFound); err != nil {
		// A merge moves nothing, so there is no undo to judge and nothing to
		// tell a save that reached neither file from one that reached both:
		// the marker stays, and the next lock holder makes the merge from
		// whatever this left.
		return registryAsFound, errors.Join(err, m.markerLeftForRecovery("merge"))
	}
	m.removeRenameMarker()
	return reg, nil
}

// migrateMarketplaceName renames one recorded marketplace the way an edit
// does — directories, registry keys, both files, with the same rollback —
// and returns the registry as saved. Whether there is anything to move is
// decided by where the old name's directories are, not by the shape of the
// name: a name carrying a separator ("a/b") nests them inside the store,
// where they are this marketplace's clone and plugin cache and move like any
// other rename's, while a traversing name ("../../escape") joins them outside
// the store, where nothing is the store's to move and no install path
// changes. A non-directory source's InstallLocation is that outside path, so
// it goes: the entry is then unfetched, and the next fetch clones under the
// new name inside the store instead of pulling and swapping outside it. A
// directory source's install location is its own source path and stays.
// Directories another recorded marketplace owns go the same way as outside
// ones — see marketplaceDirsAreItsOwn — except that the entry's own plugin
// caches, which sit inside them, move under the new name
// (movePluginCachesToNewName).
func (m *Manager) migrateMarketplaceName(mk Marketplaces, reg Registry, name, newName string) (Registry, error) {
	ref, registryAsFound := mk[name], reg
	itsOwn, owner, err := m.marketplaceDirsAreItsOwn(mk, name)
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
		if ref, reg, undo, err = m.moveMarketplace(name, newName, ref, reg); err != nil {
			return registryAsFound, m.markerAfterFailedMove(err)
		}
	} else {
		// An entry whose directories another marketplace owns leaves that
		// marketplace every one of them and takes only its own plugins'
		// caches, which sit inside them one level below the layout Gc reads
		// (movePluginCachesToNewName). A name whose directories fall outside
		// the store has nothing in the store to take, so it only re-keys.
		if ref.Source.Kind != SourceDirectory {
			ref.InstallLocation = ""
		}
		if owner == "" {
			reg = rekeyRegistry(reg, name, newName, "", "")
		} else if reg, undo, err = m.movePluginCachesToNewName(reg, name, newName); err != nil {
			return registryAsFound, m.markerAfterFailedMove(err)
		}
	}
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
		return registryAsFound, errors.Join(err, undoErr, m.markerLeftForRecovery("rename"))
	}
	m.removeRenameMarker()
	return reg, nil
}

// markerAfterFailedMove decides the marker's fate after one of the move helpers
// failed. A helper that put every directory back (no
// errRenameRollbackIncomplete) leaves the store at the old name with nothing to
// resume, so the marker goes and the failure is reported as it stands; one that
// could not leaves the store between the two names, which is what the marker is
// for, so it stays and the error names it for the next lock holder.
func (m *Manager) markerAfterFailedMove(err error) error {
	if errors.Is(err, errRenameRollbackIncomplete) {
		return errors.Join(err, m.markerLeftForRecovery("rename"))
	}
	m.removeRenameMarker()
	return err
}

// pluginCacheMove is one plugin's cache directory moving with the name of an
// entry whose derived directories another marketplace owns:
// cache/<old>/<plugin> to cache/<new>/<plugin>.
type pluginCacheMove struct{ plugin, from, to string }

// movePluginCachesToNewName moves what such a rename has of its own. The
// marketplace that owns the directories keeps every one of them, but the
// entry's own installs sit at cache/<old>/<plugin>/<sha> — one level below the
// cache/<marketplace>/<plugin>/<sha> layout Gc reads, so Gc takes
// cache/<old>/<plugin> for an unreferenced sha directory and reclaims the
// installs the rename just carried over. Each plugin directory the entry's own
// keys reference moves to the same place under the new name, where Gc reads it
// as the install it is, and the paths follow. Everything else stays: the
// owner's own plugins, the clone the entry's name derives, which is the
// owner's too, and an install path outside the cache, which is a directory
// source's own and is referenced where the user put it.
//
// A directory that is not there has already moved — the run this is finishing
// made that rename and stopped before the registry write — so each move is
// presence-checked and the path rewritten either way, as a rename with no
// cache to move still records the new name.
func (m *Manager) movePluginCachesToNewName(reg Registry, name, newName string) (Registry, []func() error, error) {
	moves := m.pluginCacheMoves(reg, name, newName)
	if len(moves) == 0 {
		return rekeyRegistry(reg, name, newName, "", ""), nil, nil
	}
	var undo []func() error
	fail := func(err error) (Registry, []func() error, error) {
		if undoErr := runUndo(undo); undoErr != nil {
			return Registry{}, nil, errors.Join(err, undoErr, errRenameRollbackIncomplete)
		}
		return Registry{}, nil, err
	}
	newCache := filepath.Join(m.cacheDir(), newName)
	// Nothing occupies the new name (refuseLeftoversUnder), so the cache
	// directory is this rename's to create — and to remove again where the
	// rename rolls back, so a retry finds the name free instead of stepping
	// to the next numbered one. One the run this is finishing created is not
	// this rename's to remove.
	haveNewCache, err := pathPresent(newCache)
	if err != nil {
		return fail(err)
	}
	if !haveNewCache {
		if err := marketplaceMkdirAll(newCache, 0o755); err != nil {
			return fail(fmt.Errorf("creating plugin cache %s: %w", newCache, err))
		}
		undo = append(undo, func() error {
			// Empty once the moves above are undone; a directory a failed
			// restore left something in is reported rather than removed.
			if err := marketplaceRemove(newCache); err != nil {
				return fmt.Errorf("removing plugin cache %s: %w", newCache, err)
			}
			return nil
		})
	}
	for _, move := range moves {
		present, err := pathPresent(move.from)
		if err != nil {
			return fail(err)
		}
		if !present {
			continue
		}
		if err := marketplaceRename(move.from, move.to); err != nil {
			return fail(fmt.Errorf("renaming plugin cache: %w", err))
		}
		undo = append(undo, func() error { return restoreRename("plugin cache", move.to, move.from) })
	}
	reg = rekeyRegistry(reg, name, newName, "", "")
	for _, move := range moves {
		entries := reg.Plugins[registryKey(move.plugin, newName)]
		for i, entry := range entries {
			if rel, under := pathUnder(move.from, entry.InstallPath); under {
				entries[i].InstallPath = filepath.Join(move.to, rel)
			}
		}
	}
	return reg, undo, nil
}

// pluginCacheMoves is what the entry recorded under name has of its own inside
// the owner's cache: for each of its keys, the cache/<old>/<plugin> directory
// that key's installs sit under, against the same directory under the new
// name. A key with nothing installed under that directory has nothing there to
// move.
//
// A directory another key installs into is the owner's rather than this
// entry's: a name cleaning to the owner's own cache ("./x" beside "x") derives
// the owner's plugin directories themselves, and one of them can hold the one
// plugin both records name. Moving it would leave the owner's entry pointing
// where the files no longer are, and it already sits at the depth Gc reads, so
// it stays and this entry's key goes on naming it there.
func (m *Manager) pluginCacheMoves(reg Registry, name, newName string) []pluginCacheMove {
	oldCache, newCache := filepath.Join(m.cacheDir(), name), filepath.Join(m.cacheDir(), newName)
	suffix := "@" + name
	var moves []pluginCacheMove
	for key, entries := range reg.Plugins {
		plugin, ok := strings.CutSuffix(key, suffix)
		if !ok {
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

// installedUnder reports whether any of entries is installed under dir.
func installedUnder(dir string, entries []InstallEntry) bool {
	return slices.ContainsFunc(entries, func(entry InstallEntry) bool {
		_, under := pathUnder(dir, entry.InstallPath)
		return under
	})
}

// anotherKeyInstallsUnder reports whether a registry key other than own has an
// install under dir.
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
func (m *Manager) marketplaceDirsAreItsOwn(mk Marketplaces, name string) (bool, string, error) {
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
			if dirInStore(resolvedDir, othersDir) && pathWithinDir(othersDir, derived) {
				return false, other, nil
			}
		}
	}
	return true, "", nil
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
