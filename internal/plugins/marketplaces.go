package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var (
	marketplaceReadFile        = os.ReadFile
	marketplaceMarshalIndent   = json.MarshalIndent
	marketplaceAtomicWriteFile = atomicWriteFile
	marketplaceGitClone        = gitClone
	marketplaceGitSparseClone  = gitSparseClone
	marketplaceGitPull         = gitPull
	marketplaceRemoveAll       = os.RemoveAll
	marketplaceRemove          = os.Remove
	marketplaceRename          = os.Rename
	marketplaceMkdirAll        = os.MkdirAll
	marketplaceAcquireLock     = acquireLock
	marketplaceStat            = os.Stat
	marketplaceLstat           = os.Lstat
)

// The two fixed scratch directories under the marketplaces dir: a fetch lands
// in staging before anything it would replace is touched, and the clone being
// replaced is renamed aside until the store files say it is no longer needed.
// Named once because a rename onto either of them is refused by name.
const (
	stagingCloneName = ".staging"
	asideCloneName   = ".old"
)

type MarketplaceRef struct {
	Source          Source    `json:"source"`
	InstallLocation string    `json:"installLocation"` //nolint:tagliatelle // matches Claude Code plugin/marketplace JSON schema
	LastUpdated     time.Time `json:"lastUpdated"`     //nolint:tagliatelle // matches Claude Code plugin/marketplace JSON schema
}

type Marketplaces map[string]MarketplaceRef

// catalogRoot is the directory holding .claude-plugin/marketplace.json for a
// registered marketplace. For a git-subdir source the manifest lives in the
// subdir under the clone root; otherwise it is InstallLocation itself.
func (m *Manager) catalogRoot(ref MarketplaceRef) string {
	if ref.Source.Kind == SourceGitSubdir {
		return filepath.Join(ref.InstallLocation, ref.Source.Path)
	}
	return ref.InstallLocation
}

// loadMarketplaces and saveMarketplaces are the only ways this package reaches
// known_marketplaces.json. Both derive the path through storePath, so
// ListMarketplaces — which reads without the store lock — refuses an
// unresolved root instead of handing back the working directory's file.
func (m *Manager) loadMarketplaces() (Marketplaces, error) {
	path, err := m.storePath(marketplacesFileName)
	if err != nil {
		return nil, err
	}
	data, err := marketplaceReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Marketplaces{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var mk Marketplaces
	if err := json.Unmarshal(data, &mk); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if mk == nil {
		mk = Marketplaces{}
	}
	return mk, nil
}

func (m *Manager) saveMarketplaces(mk Marketplaces) error {
	path, err := m.storePath(marketplacesFileName)
	if err != nil {
		return err
	}
	body, err := marketplaceMarshalIndent(mk, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling marketplaces: %w", err)
	}
	return marketplaceAtomicWriteFile(path, append(body, '\n'), 0o644)
}

// fetchMarketplaceContainer clones/references src into destDir and returns the
// directory that contains .claude-plugin/marketplace.json.
func (m *Manager) fetchMarketplaceContainer(ctx context.Context, src Source, destDir string) (string, error) {
	switch src.Kind {
	case SourceDirectory:
		return src.Path, nil // referenced in place
	case SourceGitHub:
		url := "https://github.com/" + src.Repo + ".git"
		if err := marketplaceGitClone(ctx, url, destDir, src.Ref, src.Sha); err != nil {
			return "", err
		}
		return destDir, nil
	case SourceURL:
		if err := marketplaceGitClone(ctx, src.URL, destDir, src.Ref, src.Sha); err != nil {
			return "", err
		}
		return destDir, nil
	case SourceGitSubdir:
		if err := marketplaceGitSparseClone(ctx, src.URL, destDir, src.Path, src.Ref, src.Sha); err != nil {
			return "", err
		}
		return filepath.Join(destDir, src.Path), nil
	default:
		return "", fmt.Errorf("unsupported marketplace source %q", src.Kind)
	}
}

// ensureFetched clones a registered-but-unfetched marketplace (empty
// InstallLocation) into its store dir and backfills InstallLocation. A directory
// source is always "fetched" (referenced in place). Safe to call repeatedly.
//
// The caller must already hold m.lockPath() — as Browse and catalogPlugin's
// callers (Install/Upgrade) do — so this does not lock internally. flock(2)
// locks are per open-file-description, not per-process, so a second internal
// acquireLock here would self-deadlock (spin until its own 30s timeout) when
// reached from Install/Upgrade, which already hold that same lock.
func (m *Manager) ensureFetched(ctx context.Context, name string) (MarketplaceRef, error) {
	mk, err := m.loadMarketplaces()
	if err != nil {
		return MarketplaceRef{}, err
	}
	ref, ok := mk[name]
	if !ok {
		return MarketplaceRef{}, fmt.Errorf("marketplace %q: %w", name, ErrMarketplaceNotFound)
	}
	if ref.InstallLocation != "" {
		return ref, nil
	}
	installLoc := ref.Source.Path // directory source: referenced in place
	if ref.Source.Kind != SourceDirectory {
		installLoc = m.marketplaceDir(name)
		_ = marketplaceRemoveAll(installLoc)
		if _, err := m.fetchMarketplaceContainer(ctx, ref.Source, installLoc); err != nil {
			return MarketplaceRef{}, err
		}
	}
	ref.InstallLocation = installLoc
	ref.LastUpdated = m.now().UTC()
	mk[name] = ref
	if err := m.saveMarketplaces(mk); err != nil {
		return MarketplaceRef{}, err
	}
	return ref, nil
}

// AddMarketplace fetches src, reads its marketplace.json for the name (unless
// name is given), and records it. Returns the stored ref.
func (m *Manager) AddMarketplace(ctx context.Context, name string, src Source) (MarketplaceRef, error) {
	release, err := m.lockStore(ctx, marketplaceAcquireLock, 30*time.Second)
	if err != nil {
		return MarketplaceRef{}, err
	}
	defer release()

	if src.Kind == SourceDirectory {
		// Before the staging clear below, which would destroy a source that
		// sits there — and before the fetch, which is what would leave this
		// marketplace registered against the store's own doomed files. The
		// name this would be recorded under is still unknown here, and the
		// rule does not need it.
		if err := m.refuseSourceInStore(src.Path); err != nil {
			return MarketplaceRef{}, err
		}
	}

	// The store lock is held throughout, so the file cannot change between
	// here and the save.
	mk, err := m.loadMarketplaces()
	if err != nil {
		return MarketplaceRef{}, err
	}

	// Fetch into a staging dir first so a bad marketplace never half-registers.
	staging := m.marketplaceDir(stagingCloneName)
	_ = marketplaceRemoveAll(staging)
	root, err := m.fetchMarketplaceContainer(ctx, src, staging)
	if err != nil {
		_ = marketplaceRemoveAll(staging)
		return MarketplaceRef{}, err
	}
	cat, err := ParseCatalog(root)
	if err != nil {
		_ = marketplaceRemoveAll(staging)
		return MarketplaceRef{}, fmt.Errorf("reading marketplace.json: %w", err)
	}
	if name == "" {
		name = cat.Name
	}
	if name == "" {
		_ = marketplaceRemoveAll(staging)
		return MarketplaceRef{}, errors.New("marketplace has no name and none was given")
	}
	if err := validNameComponent("marketplace", name); err != nil {
		_ = marketplaceRemoveAll(staging)
		return MarketplaceRef{}, err
	}

	installLoc := src.Path // directory source: in place
	if src.Kind != SourceDirectory {
		installLoc = m.marketplaceDir(name)
		old, err := m.swapInClone(staging, installLoc)
		if err != nil {
			_ = marketplaceRemoveAll(staging)
			return MarketplaceRef{}, err
		}
		if old != "" {
			_ = marketplaceRemoveAll(old)
		}
	} else {
		_ = marketplaceRemoveAll(staging)
	}

	ref := MarketplaceRef{Source: src, InstallLocation: installLoc, LastUpdated: m.now().UTC()}
	mk[name] = ref
	if err := m.saveMarketplaces(mk); err != nil {
		if src.Kind != SourceDirectory {
			_ = marketplaceRemoveAll(installLoc)
		}
		return MarketplaceRef{}, err
	}
	return ref, nil
}

// ListMarketplaces returns every registered marketplace, read from behind the
// migration barrier (loadMigratedMarketplaces) so that the names it hands back
// are the ones the store accepts today.
func (m *Manager) ListMarketplaces(ctx context.Context) (Marketplaces, error) {
	return m.loadMigratedMarketplaces(ctx, marketplaceAcquireLock)
}

func (m *Manager) RemoveMarketplace(ctx context.Context, name string) error {
	release, err := m.lockStore(ctx, marketplaceAcquireLock, 30*time.Second)
	if err != nil {
		return err
	}
	defer release()
	mk, err := m.loadMarketplaces()
	if err != nil {
		return err
	}
	ref, ok := mk[name]
	if !ok {
		return fmt.Errorf("marketplace %q: %w", name, ErrMarketplaceNotFound)
	}
	if ref.Source.Kind != SourceDirectory {
		if err := marketplaceRemoveAll(m.marketplaceDir(name)); err != nil {
			_, _ = fmt.Fprintf(m.stderr(), "warning: removing marketplace clone %s: %v\n", m.marketplaceDir(name), err)
		}
	}
	delete(mk, name)
	return m.saveMarketplaces(mk)
}

// EditMarketplace renames a registered marketplace and/or replaces its
// source (spec 2026-09-07 §3). The order is chosen so the one step that can
// take a long time or fail for reasons outside the store — fetching the new
// source — happens before anything on disk moves, and a failure at any later
// step, the marketplaces file's own save included, puts back everything the
// edit moved: the renamed directories, the re-keyed registry file, and the
// contents of a clone the new source was swapped into — the swap sets the old
// contents aside and they stay there until both files are saved. Restoring
// only the directory's NAME would leave the new source's files wearing the old
// one, which every later refresh would then serve under the old source's name,
// because RefreshMarketplace pulls the clone's own origin.
//
//  1. fetch a changed source into staging and parse its catalog (Add's own
//     staging discipline: a bad source never half-registers);
//  2. rename the clone directory and the plugin cache directory, and re-key
//     every <plugin>@old registry entry (its install path lives under the
//     renamed cache);
//  3. swap the staged clone into the (possibly renamed) install location, or
//     point a directory source at its path;
//  4. save the installed registry, then the marketplaces file.
//
// A same-name, same-source call is a no-op that returns the current ref.
// A git-backed marketplace's plugins are materialized under the cache; a
// directory-source marketplace's relative plugins are referenced in place
// inside it. A re-source moves neither, beyond the re-key a rename implies.
func (m *Manager) EditMarketplace(ctx context.Context, name, newName string, src *Source) (MarketplaceRef, error) {
	release, err := m.lockStore(ctx, marketplaceAcquireLock, 30*time.Second)
	if err != nil {
		return MarketplaceRef{}, err
	}
	defer release()

	mk, err := m.loadMarketplaces()
	if err != nil {
		return MarketplaceRef{}, err
	}
	ref, ok := mk[name]
	if !ok {
		return MarketplaceRef{}, fmt.Errorf("marketplace %q: %w", name, ErrMarketplaceNotFound)
	}
	renaming := newName != "" && newName != name
	resourcing := src != nil && *src != ref.Source
	if !renaming && !resourcing {
		return ref, nil
	}
	if renaming {
		if err := validNameComponent("marketplace", newName); err != nil {
			return MarketplaceRef{}, err
		}
		if _, taken := mk[newName]; taken {
			return MarketplaceRef{}, fmt.Errorf("marketplace %q: %w", newName, ErrMarketplaceExists)
		}
	}
	if resourcing {
		if src.Kind == SourceDirectory {
			// Before the staging directory is cleared, which is itself one of
			// the paths this refuses.
			if err := m.refuseSourceInStore(src.Path); err != nil {
				return MarketplaceRef{}, fmt.Errorf("marketplace %q: %w", name, err)
			}
		}
	}
	reg, err := m.loadRegistry()
	if err != nil {
		return MarketplaceRef{}, err
	}
	if renaming {
		// With the other refusals, not beside the rename it guards: nothing
		// the fetch does can put a leftover under the new name, so a rename
		// this check is going to refuse need not pay for a clone first.
		if err := m.refuseLeftoversUnder(newName, reg); err != nil {
			return MarketplaceRef{}, err
		}
	}

	// 1. The network step, before anything on disk moves.
	staging := m.marketplaceDir(stagingCloneName)
	if resourcing {
		_ = marketplaceRemoveAll(staging)
		root, err := m.fetchMarketplaceContainer(ctx, *src, staging)
		if err != nil {
			_ = marketplaceRemoveAll(staging)
			return MarketplaceRef{}, err
		}
		if _, err := ParseCatalog(root); err != nil {
			_ = marketplaceRemoveAll(staging)
			return MarketplaceRef{}, fmt.Errorf("reading marketplace.json: %w", err)
		}
	}
	// From here until the files are saved, a failure runs undo in reverse
	// and sweeps staging. Every step reports what it could not put back: a
	// rollback that fails leaves a directory under a name nothing records,
	// and the error the edit returns is the only place that can say which.
	var undo []func() error
	// swappedIn holds the install location once swapInClone has replaced its
	// contents with the new source's, and asideClone the directory the swap
	// renamed those old contents to. The old clone outlives the swap for
	// exactly this window, because a name alone cannot undo it: the new
	// source's files renamed back to the old name would be served under the old
	// source's name by every later refresh (RefreshMarketplace pulls the
	// clone's own origin). So a failure removes the swapped-in clone and
	// renames the old contents back into it; only once both files are saved is
	// the aside copy no longer a rollback source, and it goes with staging.
	swappedIn, asideClone := "", ""
	undoSwap := func() error {
		if swappedIn == "" {
			return nil
		}
		if err := marketplaceRemoveAll(swappedIn); err != nil {
			return fmt.Errorf("removing the swapped-in clone %s: %w", swappedIn, err)
		}
		if asideClone == "" {
			return nil
		}
		return restoreRename("old clone", asideClone, swappedIn)
	}
	fail := func(err error) (MarketplaceRef, error) {
		// Before undo, which moves the install location back under its old
		// name: the contents have to be in it first.
		errs := []error{err, undoSwap(), runUndo(undo)}
		_ = marketplaceRemoveAll(staging)
		return MarketplaceRef{}, errors.Join(errs...)
	}

	// 2. Rename on disk and in the registry. The registry as the edit found it
	// is kept because the re-key below replaces it wholesale and a later
	// failure has to write it back.
	target := name
	registryAsFound := reg
	if renaming {
		target = newName
		ref, reg, undo, err = m.moveMarketplace(name, newName, ref, reg, mk)
		if err != nil {
			return fail(err)
		}
	}

	// 3. Apply the new source into the install location. An old clone that a
	// directory source makes redundant goes only after the files say so.
	var afterSave []func()
	if resourcing {
		if src.Kind == SourceDirectory {
			if ref.Source.Kind != SourceDirectory {
				clone := m.marketplaceDir(target)
				afterSave = append(afterSave, func() { _ = marketplaceRemoveAll(clone) })
			}
			_ = marketplaceRemoveAll(staging)
			ref.InstallLocation = src.Path
		} else {
			dest := m.marketplaceDir(target)
			aside, err := m.swapInClone(staging, dest)
			if err != nil {
				return fail(err)
			}
			swappedIn, asideClone = dest, aside
			if aside != "" {
				afterSave = append(afterSave, func() { _ = marketplaceRemoveAll(aside) })
			}
			ref.InstallLocation = dest
		}
		ref.Source = *src
		// LastUpdated tracks how fresh the catalog on disk is, so only the
		// branch that fetched one moves it; a rename moves no content.
		ref.LastUpdated = m.now().UTC()
	}

	// 4. Save both files. A failed save is what the undo above is for: the
	// file that survives it is the old one, and directories left under the
	// new name would be orphaned by the next refresh, which reclones the
	// recorded source at the recorded path.
	if renaming {
		if err := m.saveRename(mk, name, newName, ref, reg, registryAsFound); err != nil {
			return fail(err)
		}
	} else {
		mk[name] = ref
		if err := m.saveMarketplaces(mk); err != nil {
			return fail(err)
		}
	}
	for _, fn := range afterSave {
		fn()
	}
	return ref, nil
}

// restoreRename puts one of an edit's renames back, naming both paths when it
// cannot: the directory is then left under a name no store file records, and
// nothing but this error says where it is.
func restoreRename(what, from, to string) error {
	if err := marketplaceRename(from, to); err != nil {
		return fmt.Errorf("restoring %s %s to %s: %w", what, from, to, err)
	}
	return nil
}

// runUndo runs a rename's undo steps in reverse and joins what they could not
// put back.
func runUndo(undo []func() error) error {
	var errs []error
	for _, fn := range slices.Backward(undo) {
		errs = append(errs, fn())
	}
	return errors.Join(errs...)
}

// moveMarketplace renames a marketplace on disk and in the registry: the
// clone directory, when the source is not a directory and one is there; the
// plugin cache directory, when one is there; and every <plugin>@name registry
// entry, whose install path follows the cache. Neither store file is written.
// On success it returns the ref and registry as they are to be recorded, and
// the steps that put the directories back should a later step fail; a failure
// puts back what it had moved itself and reports what it could not.
func (m *Manager) moveMarketplace(name, newName string, ref MarketplaceRef, reg Registry, mk Marketplaces) (MarketplaceRef, Registry, []func() error, error) {
	var undo []func() error
	fail := func(err error) (MarketplaceRef, Registry, []func() error, error) {
		if undoErr := runUndo(undo); undoErr != nil {
			return MarketplaceRef{}, Registry{}, nil, errors.Join(err, undoErr, errRenameRollbackIncomplete)
		}
		return MarketplaceRef{}, Registry{}, nil, err
	}
	if ref.Source.Kind != SourceDirectory {
		oldDir, newDir := m.marketplaceDir(name), m.marketplaceDir(newName)
		haveClone, err := pathPresent(oldDir)
		if err != nil {
			return fail(err)
		}
		// Whatever the entry records: a lazy fetch clears and refills this
		// directory before it writes an install location, so a fetch that
		// failed leaves one behind that only the move takes with the name.
		if haveClone {
			if err := marketplaceRename(oldDir, newDir); err != nil {
				return fail(fmt.Errorf("renaming marketplace clone: %w", err))
			}
			undo = append(undo, func() error { return restoreRename("marketplace clone", newDir, oldDir) })
		}
		// A recorded location says where the clone is, so it follows the
		// clone that moved; with none to move the entry is unfetched, and the
		// next fetch clears and clones under the new name as it would have
		// under the old. An entry that recorded no location keeps none,
		// whatever a failed fetch left at the canonical path.
		if ref.InstallLocation != "" {
			ref.InstallLocation = ""
			if haveClone {
				ref.InstallLocation = newDir
			}
		}
	}
	oldCache, newCache := filepath.Join(m.cacheDir(), name), filepath.Join(m.cacheDir(), newName)
	haveCache, err := pathPresent(oldCache)
	if err != nil {
		return fail(err)
	}
	if haveCache {
		if err := marketplaceRename(oldCache, newCache); err != nil {
			return fail(fmt.Errorf("renaming plugin cache: %w", err))
		}
		undo = append(undo, func() error { return restoreRename("plugin cache", newCache, oldCache) })
	}
	return ref, rekeyRegistry(reg, mk, name, newName, oldCache, newCache), undo, nil
}

// errStoreBetweenNames marks the one rename failure that leaves the store
// between the old name and the new one rather than back at either: the
// marketplaces file records the old name while the registry keys the
// marketplace's plugins under the new one. Only saveRename can tell that
// half-state from the two it rolls back to, so a caller whose rename wrote a
// marker keeps it for that state (migrateMarketplaceName).
var errStoreBetweenNames = errors.New("the store is left between the two names")

// errRenameRollbackIncomplete marks a move helper's failure that could not put
// every directory back where it found it. A move writes neither store file, so
// a failure whose undo succeeded leaves the store at the old name with nothing
// left to resume; only one that could not leaves it between the two names. A
// rename that wrote a marker keeps it for this state alone
// (migrateMarketplaceName).
var errRenameRollbackIncomplete = errors.New("a failed move could not be put back completely")

// saveRename records a rename in both store files, the registry first: a
// marketplaces file naming a marketplace whose plugins are still keyed under
// the old name is the worse of the two half-states, and evener-doctor reports
// the other one — which now outlives a failed save only if the restore below
// fails too. When the marketplaces file's save fails, registryAsFound is
// written back, because the re-keyed entries name install paths under a cache
// directory the caller is about to rename back. That restore is itself a
// write that can fail, and only then is the store left inconsistent, so the
// error says so and carries errStoreBetweenNames, which is how a rename that
// wrote a marker knows the marker is still needed.
func (m *Manager) saveRename(mk Marketplaces, name, newName string, ref MarketplaceRef, reg, registryAsFound Registry) error {
	if err := m.saveRegistry(reg); err != nil {
		return err
	}
	delete(mk, name)
	mk[newName] = ref
	if err := m.saveMarketplaces(mk); err != nil {
		if restoreErr := m.saveRegistry(registryAsFound); restoreErr != nil {
			return fmt.Errorf("marketplace %q: saving %s failed (%w); restoring %s failed (%w), so %w: it still keys this marketplace's plugins under %q", name, marketplacesFileName, err, registryFileName, restoreErr, errStoreBetweenNames, newName)
		}
		return fmt.Errorf("marketplace %q not renamed: saving %s failed, so the store is back as it was: %w", name, marketplacesFileName, err)
	}
	return nil
}

// refuseLeftoversUnder rejects a rename onto a name that a removed
// marketplace's residue still occupies. Removing a marketplace drops its clone
// and its registration but never its plugin cache or its registry entries, and
// a failed edit can strand a clone, so all three can outlive the marketplace
// that made it. Renaming onto any of them would bury it under a live
// marketplace's name — the directories silently, the registry entries as ghost
// installs that shadow the ones being re-keyed — so each is named and refused
// before anything moves. This runs on every rename, not only when the
// marketplace being renamed has something of its own to move: an install-free
// one moves neither a cache nor a registry entry and would otherwise sail past.
//
// Each refusal carries ErrMarketplaceExists, because that is what it is: the
// name is taken, just not by anything the marketplaces file records. Clearing
// the residue it names is the caller's move, not the store's failure.
func (m *Manager) refuseLeftoversUnder(newName string, reg Registry) error {
	// Where an os.Rename LinkError would have said only "file exists".
	cache := filepath.Join(m.cacheDir(), newName)
	haveCache, err := pathPresent(cache)
	if err != nil {
		return err
	}
	if haveCache {
		return fmt.Errorf("plugin cache %s already exists; a removed marketplace left it behind and it must be deleted before %q can be reused: %w", cache, newName, ErrMarketplaceExists)
	}
	clone := m.marketplaceDir(newName)
	haveClone, err := pathPresent(clone)
	if err != nil {
		return err
	}
	if haveClone {
		return fmt.Errorf("marketplace clone %s already exists; a removed marketplace left it behind and it must be deleted before %q can be reused: %w", clone, newName, ErrMarketplaceExists)
	}
	// A recorded name never carries '@' once the store is migrated
	// (lockStore), so a key ending in "@<newName>" is keyed under newName
	// itself — never the renamed marketplace's own, which key under its old
	// name — and is residue. Mid-migration a name not yet renamed can still
	// share the suffix; the migration reads that as taken and moves on to the
	// next numbered name.
	suffix := "@" + newName
	for key := range reg.Plugins {
		if strings.HasSuffix(key, suffix) {
			return fmt.Errorf("registry entry %s already exists; a removed marketplace left it behind and it must be removed before %q can be reused: %w", key, newName, ErrMarketplaceExists)
		}
	}
	return nil
}

// rekeyRegistry moves every <plugin>@oldName entry to <plugin>@newName. An
// install path under oldCache follows the cache directory to newCache; with
// no oldCache, no cache directory moved and every path stays. Entries for
// other marketplaces, and paths outside the cache, are untouched. A
// <plugin>@newName key taken by an orphan — RemoveMarketplace drops a
// marketplace's registration but not its registry entries — is refused before
// this runs, by refuseLeftoversUnder. Should one reach here anyway, the copy
// pass runs first and the moved entries overwrite it, dropping the orphan
// rather than letting map order decide whether a ghost replaces a live install.
// rekeyRegistry moves every <plugin>@oldName entry to <plugin>@newName, where
// oldName is the longest recorded name the key ends in. An
// install path under oldCache follows the cache directory to newCache; with
// no oldCache, no cache directory moved and every path stays. Entries for
// other marketplaces, and paths outside the cache, are untouched. A
// <plugin>@newName key taken by an orphan — RemoveMarketplace drops a
// marketplace's registration but not its registry entries — is refused before
// this runs, by refuseLeftoversUnder. Should one reach here anyway, the copy
// pass runs first and the moved entries overwrite it, dropping the orphan
// rather than letting map order decide whether a ghost replaces a live install.
func rekeyRegistry(reg Registry, mk Marketplaces, oldName, newName, oldCache, newCache string) Registry {
	// The marketplace a key belongs to is the longest recorded name it ends in
	// as "@<name>". A plugin's own '@' sits before that one, so wid@get@acme is
	// wid@get in acme — but a key can end in more than one recorded name at
	// once: "<plugin>@x@y@z" ends in "@y@z" and in "@z". The longest wins, so
	// migrating "z" leaves a marketplace recorded as "x@y@z" its keys, which
	// migrating it later would otherwise no longer find.
	owner := func(key string) (string, bool) {
		best := ""
		for name := range mk {
			if strings.HasSuffix(key, "@"+name) && len(name) > len(best) {
				best = name
			}
		}
		return best, best != ""
	}
	out := Registry{Version: reg.Version, Plugins: make(map[string][]InstallEntry, len(reg.Plugins))}
	for key, entries := range reg.Plugins {
		if name, ok := owner(key); !ok || name != oldName {
			out.Plugins[key] = entries
		}
	}
	for key, entries := range reg.Plugins {
		if name, ok := owner(key); !ok || name != oldName {
			continue
		}
		plugin := strings.TrimSuffix(key, "@"+oldName)
		moved := make([]InstallEntry, 0, len(entries))
		for _, e := range entries {
			if oldCache != "" {
				if rel, under := pathUnder(oldCache, e.InstallPath); under {
					e.InstallPath = filepath.Join(newCache, rel)
				}
			}
			moved = append(moved, e)
		}
		out.Plugins[registryKey(plugin, newName)] = moved
	}
	return out
}

// pathPresent reports whether path is there, and refuses to guess: only a
// missing path counts as absent, so any other stat error — no permission on a
// store directory, an I/O error — is the caller's to return. A caller that
// read one as "nothing here" would skip moving a directory it could not see,
// bury a leftover it could not check, or install over a clone it never
// inspected, and commit the store files as though it had.
//
// Stat follows links, so a dangling symlink reports its missing target rather
// than itself; the Lstat fallback is what sees the link. The name is occupied
// either way — a rename onto it fails, and renaming it away moves the link,
// not what it once pointed at.
func pathPresent(path string) (bool, error) {
	if _, err := marketplaceStat(path); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("checking %s: %w", path, err)
		}
		if _, err := marketplaceLstat(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return false, nil
			}
			return false, fmt.Errorf("checking %s: %w", path, err)
		}
	}
	return true, nil
}

// refuseSourceInStore rejects a directory source that names one of the two
// directories the store manages, or anything inside them. The marketplaces
// directory holds every marketplace's clone plus the two scratch names a fetch
// clears and a swap deletes, and the cache holds materialized plugins; all of
// it is rewritten by the next add, refresh or re-source, which never asks what
// some marketplace is sourced from. So a source pointed there is registered
// against files the store is about to destroy — the marketplace's own clone,
// which a re-source to a directory schedules for removal, is only the nearest
// case of it. An empty path names nothing at all and is refused with them.
//
// The rule is about the path alone, so it carries no marketplace name: an add
// has none to give until it has fetched the catalog this refuses to fetch. A
// caller that does know the name says so around the error.
func (m *Manager) refuseSourceInStore(path string) error {
	if path == "" {
		return fmt.Errorf("a directory source needs a path: %w", ErrMarketplaceSourceInStore)
	}
	candidate, err := resolveForContainment(path)
	if err != nil {
		return err
	}
	for _, dir := range []string{m.marketplacesDir(), m.cacheDir()} {
		resolved, err := resolveForContainment(dir)
		if err != nil {
			return err
		}
		if pathWithinDir(resolved, candidate) {
			return fmt.Errorf("%w (%s is inside %s)", ErrMarketplaceSourceInStore, path, dir)
		}
	}
	return nil
}

// resolveForContainment canonicalises a path for a comparison about disk:
// absolute always, and physical when the path exists, so a symlinked store root
// cannot hide a containment that the filesystem would honour. A path that does
// not exist is resolved through the nearest ancestor that does, with the rest
// appended: symlinks above a missing leaf are still how the filesystem would
// read the path, and a directory an earlier migration moved leaves its former
// path comparing as wherever the symlinks above it point rather than as its own
// lexical string. The result is still a path nothing is at, so there is nothing
// on disk for a later step to destroy.
func resolveForContainment(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", path, err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	missing := ""
	for dir := abs; ; {
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs, nil
		}
		missing = filepath.Join(filepath.Base(dir), missing)
		dir = parent
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, missing), nil
		}
	}
}

// pathUnder is where under dir a path sits, and whether it is under dir at
// all. Both are taken as the store recorded them — an install path against
// the cache directory it was joined from — so neither is resolved here, and a
// path that is dir itself is under it.
func pathUnder(dir, path string) (rel string, under bool) {
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// pathWithinDir reports whether candidate is dir itself or sits beneath it.
// Both must already be resolved.
func pathWithinDir(dir, candidate string) bool {
	rel, err := filepath.Rel(dir, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// recloneMarketplace replaces a marketplace clone whose git pull failed. The
// fresh clone is fully downloaded into a staging dir before the existing clone
// is touched, so the current clone — possibly wedged, but the only local copy
// — is never lost to a failed download; a failed reclone leaves it exactly as
// it was. The caller must hold m.lockPath(), which also serializes use of the
// shared staging/aside dirs.
func (m *Manager) recloneMarketplace(ctx context.Context, ref MarketplaceRef) error {
	staging := m.marketplaceDir(stagingCloneName)
	_ = marketplaceRemoveAll(staging)
	// After a successful swap the staging dir no longer exists, so this defer
	// only ever sweeps a leftover from a failed path.
	defer func() { _ = marketplaceRemoveAll(staging) }()
	if _, err := m.fetchMarketplaceContainer(ctx, ref.Source, staging); err != nil {
		return err
	}
	old, err := m.swapInClone(staging, ref.InstallLocation)
	if err != nil {
		return err
	}
	if old != "" {
		_ = marketplaceRemoveAll(old)
	}
	return nil
}

// swapInClone replaces dest with the fully-downloaded staging dir: rename any
// existing dest aside, then rename staging in. A failed swap restores dest, and
// no path removes the old clone before the new one is in place. The caller must
// hold m.lockPath().
//
// It returns the path the old clone was set aside under, or "" when dest held
// nothing. That directory is the caller's: it is the only copy of what dest
// held, so a caller whose work can still fail keeps it until the work is
// committed and every other caller removes it at once.
func (m *Manager) swapInClone(staging, dest string) (string, error) {
	// The aside name is this swap's own scratch, so a directory already there
	// is residue a caller's best-effort cleanup failed to remove. It has to go
	// first: renaming dest onto a non-empty directory fails, which would leave
	// the marketplace unable to re-source until someone deleted it by hand.
	old := m.marketplaceDir(asideCloneName)
	_ = marketplaceRemoveAll(old)
	occupied, err := pathPresent(dest)
	if err != nil {
		return "", err
	}
	movedAside := false
	if occupied {
		if err := marketplaceRename(dest, old); err != nil {
			return "", fmt.Errorf("moving old clone aside: %w", err)
		}
		movedAside = true
	}
	if err := marketplaceRename(staging, dest); err != nil {
		if movedAside {
			// Put the old clone back so dest keeps pointing at a real
			// directory. If even that fails, .old still holds the only
			// local copy — deliberately NOT swept — and the error says so.
			if restoreErr := marketplaceRename(old, dest); restoreErr != nil {
				return "", fmt.Errorf("installing fresh clone failed (%w); restoring old clone: %w", err, restoreErr)
			}
		}
		return "", fmt.Errorf("installing fresh clone: %w", err)
	}
	if !movedAside {
		return "", nil
	}
	return old, nil
}

func (m *Manager) RefreshMarketplace(ctx context.Context, name string) error {
	release, err := m.lockStore(ctx, marketplaceAcquireLock, 30*time.Second)
	if err != nil {
		return err
	}
	defer release()
	mk, err := m.loadMarketplaces()
	if err != nil {
		return err
	}
	ref, ok := mk[name]
	if !ok {
		return fmt.Errorf("marketplace %q: %w", name, ErrMarketplaceNotFound)
	}
	if ref.Source.Kind != SourceDirectory {
		if ref.InstallLocation == "" {
			// Never fetched (seeded pointer): clone now — that is the refresh.
			installLoc := m.marketplaceDir(name)
			_ = marketplaceRemoveAll(installLoc)
			if _, err := m.fetchMarketplaceContainer(ctx, ref.Source, installLoc); err != nil {
				return err
			}
			ref.InstallLocation = installLoc
		} else if pullErr := marketplaceGitPull(ctx, ref.InstallLocation); pullErr != nil {
			// A failed pull can mean the clone is wedged — e.g. a stale
			// .git/index.lock stranded by a killed git — and a plain retry
			// would then fail the same way forever. Self-heal with a staged
			// reclone; on failure it leaves the existing clone untouched.
			// When the pull failed because the request itself was canceled,
			// skip the doomed reclone and surface the cancellation directly.
			if ctx.Err() != nil {
				return pullErr
			}
			if recloneErr := m.recloneMarketplace(ctx, ref); recloneErr != nil {
				return fmt.Errorf("refreshing marketplace %q: git pull failed (%w); staged reclone failed: %w", name, pullErr, recloneErr)
			}
		}
	}
	ref.LastUpdated = m.now().UTC()
	mk[name] = ref
	return m.saveMarketplaces(mk)
}
