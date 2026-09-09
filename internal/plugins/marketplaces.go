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
	marketplaceRename          = os.Rename
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
		// The clone lands at <marketplaces>/<name>, joined from a recorded
		// name, and the clear below is what empties whatever is there. An
		// entry already fetched derives nothing and is left readable.
		if err := refuseRecordedName(name); err != nil {
			return MarketplaceRef{}, err
		}
		if err := m.refuseScratchNamedClone(name); err != nil {
			return MarketplaceRef{}, err
		}
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

	// Read before the fetch rather than after it, because the scratch
	// directories the fetch and the swap below clear can be a registered
	// marketplace's own clone. The store lock is held throughout, so the file
	// cannot change between here and the save.
	mk, err := m.loadMarketplaces()
	if err != nil {
		return MarketplaceRef{}, err
	}
	if err := m.refuseScratchNamesOccupied(mk); err != nil {
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

// ListMarketplaces reads the marketplaces file without the store lock, so a
// listing never queues behind a fetch. A name the store refuses means no lock
// holder has migrated this store yet (lockStore), so the listing takes the
// lock — which migrates — and reads what that left; a second lister that
// arrives meanwhile finds nothing left to do once it holds the lock.
func (m *Manager) ListMarketplaces() (Marketplaces, error) {
	mk, err := m.loadMarketplaces()
	if err != nil || len(refusedMarketplaceNames(mk)) == 0 {
		return mk, err
	}
	release, err := m.lockStore(context.Background(), marketplaceAcquireLock, 30*time.Second)
	if err != nil {
		return nil, err
	}
	defer release()
	return m.loadMarketplaces()
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
	// The clone this deletes is <marketplaces>/<name>, joined from a name
	// nothing gated on the way in.
	if err := refuseRecordedName(name); err != nil {
		return err
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
	// Both halves of an edit derive store paths from the recorded name — the
	// clone and cache directories a rename moves, the clone a re-source swaps
	// into — and only the rename TARGET was ever validated.
	if err := refuseRecordedName(name); err != nil {
		return MarketplaceRef{}, err
	}
	if renaming {
		// The scratch names and '@' are refused here too, by the shared
		// validator: only the rename target is checked, so a marketplace an
		// older evener already recorded under such a name can still be
		// renamed away.
		if err := validNameComponent("marketplace", newName); err != nil {
			return MarketplaceRef{}, err
		}
		if _, taken := mk[newName]; taken {
			return MarketplaceRef{}, fmt.Errorf("marketplace %q: %w", newName, ErrMarketplaceExists)
		}
	}
	if resourcing {
		// A marketplace an older evener recorded under one of the scratch
		// names owns the directory this edit fetches into or renames aside, so
		// the fetch below would destroy its clone.
		if err := m.refuseScratchNamedClone(name); err != nil {
			return MarketplaceRef{}, err
		}
		// And the same clone is destroyed when some OTHER marketplace is the
		// one being re-sourced.
		if err := m.refuseScratchNamesOccupied(mk); err != nil {
			return MarketplaceRef{}, err
		}
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
		if err := m.refuseLeftoversUnder(name, newName, mk, reg); err != nil {
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
		// Only the fetch puts anything in the staging directory, and only a
		// re-source fetches. A rename that swept it anyway would take the
		// clone of a marketplace an older evener registered as .staging — and
		// the rename is that entry's only way off it.
		if resourcing {
			_ = marketplaceRemoveAll(staging)
		}
		return MarketplaceRef{}, errors.Join(errs...)
	}

	// 2. Rename on disk and in the registry. The registry as the edit found it
	// is kept because the re-key below replaces it wholesale and a later
	// failure has to write it back.
	target := name
	registryAsFound := reg
	if renaming {
		target = newName
		ref, reg, undo, err = m.moveMarketplace(mk, name, newName, ref, reg)
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
func (m *Manager) moveMarketplace(mk Marketplaces, name, newName string, ref MarketplaceRef, reg Registry) (MarketplaceRef, Registry, []func() error, error) {
	var undo []func() error
	fail := func(err error) (MarketplaceRef, Registry, []func() error, error) {
		return MarketplaceRef{}, Registry{}, nil, errors.Join(err, runUndo(undo))
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
		// The location moves only if there was one; an entry the store has
		// not fetched stays unfetched, and the next fetch clears and
		// refetches under the new name as it would have under the old.
		if ref.InstallLocation != "" {
			ref.InstallLocation = newDir
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

// saveRename records a rename in both store files, the registry first: a
// marketplaces file naming a marketplace whose plugins are still keyed under
// the old name is the worse of the two half-states, and evener-doctor reports
// the other one — which now outlives a failed save only if the restore below
// fails too. When the marketplaces file's save fails, registryAsFound is
// written back, because the re-keyed entries name install paths under a cache
// directory the caller is about to rename back. That restore is itself a
// write that can fail, and only then is the store left inconsistent, so the
// error says so.
func (m *Manager) saveRename(mk Marketplaces, name, newName string, ref MarketplaceRef, reg, registryAsFound Registry) error {
	if err := m.saveRegistry(reg); err != nil {
		return err
	}
	delete(mk, name)
	mk[newName] = ref
	if err := m.saveMarketplaces(mk); err != nil {
		if restoreErr := m.saveRegistry(registryAsFound); restoreErr != nil {
			return fmt.Errorf("marketplace %q: saving %s failed (%w); restoring %s failed (%w), so it still keys this marketplace's plugins under %q", name, marketplacesFileName, err, registryFileName, restoreErr, newName)
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
func (m *Manager) refuseLeftoversUnder(oldName, newName string, mk Marketplaces, reg Registry) error {
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
	// A marketplace name may itself carry '@', so this marketplace's own
	// entries can already end in "@<newName>" — renaming foo@baz to baz leaves
	// widget@foo@baz ending in "@baz". An entry rekeyRegistry is about to move,
	// matched on the same exact "@<oldName>" suffix it uses, is nothing to
	// refuse: it goes with the rename and leaves the key it had empty. And a
	// key some longer recorded name claims is that marketplace's — renaming
	// acme to baz while foo@baz is registered leaves widget@foo@baz under the
	// suffix too — which rekeyRegistry's copy pass leaves exactly where it is,
	// by the same rule, so it is not residue. Its move pass is the other half:
	// it writes <plugin>@<newName> for every key it moves, and that
	// destination can be the key just spared, burying a live install as surely
	// as renaming onto an orphan does. So a claimed key is refused when a
	// moving key lands on it, and only then.
	own, suffix := "@"+oldName, "@"+newName
	for key := range reg.Plugins {
		plugin, ends := strings.CutSuffix(key, suffix)
		if !ends {
			continue
		}
		if claimedByALongerName(mk, key, newName) {
			// The "@<oldName>" suffix is a way of moving away, not of being
			// spared: a key under it is skipped because rekeyRegistry moves
			// it, and one a longer recorded name claims stays where it is —
			// renaming foo@baz to baz with bar@foo@baz registered leaves
			// widget@bar@foo@baz under both suffixes and moves nothing of it.
			if strings.HasSuffix(key, own) && !claimedByALongerName(mk, key, oldName) {
				continue
			}
			// Tested with the move pass's own predicate, so the two stay in
			// step: it moves a key ending in "@<oldName>" that no recorded
			// name longer than it claims.
			from := registryKey(plugin, oldName)
			if _, moving := reg.Plugins[from]; !moving || claimedByALongerName(mk, from, oldName) {
				continue
			}
			return fmt.Errorf("registry entry %s belongs to a registered marketplace and renaming %q to %q would re-key %s onto it: %w", key, oldName, newName, from, ErrMarketplaceExists)
		}
		return fmt.Errorf("registry entry %s already exists; a removed marketplace left it behind and it must be removed before %q can be reused: %w", key, newName, ErrMarketplaceExists)
	}
	return nil
}

// refuseScratchNamedClone rejects an operation that would clear or fetch the
// clone of a marketplace an older evener recorded under one of the store's
// scratch names. <marketplaces>/.staging and <marketplaces>/.old are the
// fetch's and the swap's own directories, so for such an entry that directory
// is its clone: a re-source fetches over it, and a clone of an entry nothing
// has fetched yet empties it and then leaves the new clone where the next
// fetch sweeps it. Renaming the entry away is the way out and stays allowed —
// a rename neither stages nor fetches.
func (m *Manager) refuseScratchNamedClone(name string) error {
	if name == stagingCloneName || name == asideCloneName {
		return fmt.Errorf("marketplace %q is registered at %s, one of the store's scratch directories, which this operation clears; it must be renamed first: %w", name, m.marketplaceDir(name), ErrInvalidName)
	}
	return nil
}

// refuseScratchNamesOccupied rejects an operation that is about to use the
// store's scratch directories while a registered marketplace's clone is one of
// them. An older evener let a marketplace record itself as .staging or .old,
// and for such an entry the scratch names are not scratch at all: the fetch's
// clear and the swap's delete take its clone, whichever marketplace the
// operation is actually about, and take it even when that operation then
// fails. So every fetch, swap and staged reclone stops while such an entry is
// registered. Renaming it away is the way out and stays allowed — a rename
// stages nothing.
func (m *Manager) refuseScratchNamesOccupied(mk Marketplaces) error {
	for _, scratch := range []string{stagingCloneName, asideCloneName} {
		if _, occupied := mk[scratch]; occupied {
			return fmt.Errorf("marketplace %q is registered at %s, one of the store's scratch directories, which this operation clears; it must be renamed first: %w", scratch, m.marketplaceDir(scratch), ErrInvalidName)
		}
	}
	return nil
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

// resolveForContainment canonicalises a path for pathWithinDir: absolute
// always, and physical when the path exists, so a symlinked store root cannot
// hide a containment that the filesystem would honour. A path that does not
// exist keeps its absolute form — there is nothing on disk for a later step to
// destroy.
func resolveForContainment(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", path, err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
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

// rekeyRegistry moves every <plugin>@oldName entry to <plugin>@newName and
// rewrites the install paths that lived under the renamed cache directory;
// entries for other marketplaces, and paths outside the cache, are untouched.
// A <plugin>@newName key taken by an orphan — RemoveMarketplace drops a
// marketplace's registration but not its registry entries — is refused before
// this runs, by refuseLeftoversUnder. Should one reach here anyway, the copy
// pass runs first and the moved entries overwrite it, dropping the orphan
// rather than letting map order decide whether a ghost replaces a live install.
func rekeyRegistry(reg Registry, mk Marketplaces, oldName, newName, oldCache, newCache string) Registry {
	// A marketplace name may itself carry '@', so only the exact "@<oldName>"
	// suffix identifies this marketplace's entries; splitKey's last-'@' parse
	// would read a marketplace named foo@bar as bar and move nothing. The
	// suffix alone is not the whole rule, though: it also ends every key of a
	// marketplace recorded as <something>@<oldName>, whose entries are that
	// marketplace's. A key belongs to the longest recorded name it ends with,
	// so one of those keeps its key, its entry and its install path here — and
	// its cache, which lives under cache/<that name>, is outside oldCache, so
	// the filepath.Rel guard below leaves the path alone in any case.
	suffix := "@" + oldName
	out := Registry{Version: reg.Version, Plugins: make(map[string][]InstallEntry, len(reg.Plugins))}
	for key, entries := range reg.Plugins {
		if !strings.HasSuffix(key, suffix) || claimedByALongerName(mk, key, oldName) {
			out.Plugins[key] = entries
		}
	}
	for key, entries := range reg.Plugins {
		plugin, ok := strings.CutSuffix(key, suffix)
		if !ok || claimedByALongerName(mk, key, oldName) {
			continue
		}
		moved := make([]InstallEntry, 0, len(entries))
		for _, e := range entries {
			rel, err := filepath.Rel(oldCache, e.InstallPath)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				e.InstallPath = filepath.Join(newCache, rel)
			}
			moved = append(moved, e)
		}
		out.Plugins[registryKey(plugin, newName)] = moved
	}
	return out
}

// claimedByALongerName reports whether a key already known to end in
// "@<oldName>" is some other recorded marketplace's. Only a marketplace named
// <something>@<oldName> can also end it, and that longer name is the more
// specific claim on the key.
//
// Nothing in the key itself can settle it: with both foo@bar and bar recorded,
// widget@foo@bar is equally plugin "widget" from foo@bar and plugin
// "widget@foo" from bar, because a plugin name may carry '@' too. The longer
// recorded name wins, so a marketplace named for the whole suffix keeps the
// entries that name it. Recorded is the operative word: an orphan a removed
// <something>@<oldName> marketplace left in the registry has no name to claim
// it, so it moves with this rename — its key only, since its install path is
// outside the renamed cache.
func claimedByALongerName(mk Marketplaces, key, oldName string) bool {
	for name := range mk {
		if len(name) > len(oldName) && strings.HasSuffix(key, "@"+name) {
			return true
		}
	}
	return false
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
	// The clone a refresh clones into or reclones is <marketplaces>/<name>,
	// joined from a name nothing gated on the way in.
	if err := refuseRecordedName(name); err != nil {
		return err
	}
	if ref.Source.Kind != SourceDirectory {
		if ref.InstallLocation == "" {
			// Never fetched (seeded pointer): clone now — that is the refresh.
			// The clone lands at <marketplaces>/<name>, and the clear below is
			// what empties whatever is there.
			if err := m.refuseScratchNamedClone(name); err != nil {
				return err
			}
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
			// The reclone stages through the scratch directories, unlike the
			// pull it is falling back from.
			if scratchErr := m.refuseScratchNamesOccupied(mk); scratchErr != nil {
				return fmt.Errorf("refreshing marketplace %q: git pull failed (%w); a staged reclone cannot run: %w", name, pullErr, scratchErr)
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
