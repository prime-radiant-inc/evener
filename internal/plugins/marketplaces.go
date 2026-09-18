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

// saveMarketplaces is the shared boundary every caller (ensureFetched,
// AddMarketplace, RefreshMarketplace, RemoveMarketplace, EditMarketplace,
// saveRename, SeedDefaultMarketplaces) writes known_marketplaces.json
// through, so scrubbing here once - atomicWriteFile's own error can name
// this machine's absolute plugin-store path directly - covers every present
// and future caller instead of relying on each one to wrap it. Callers that
// need the marketplace name in the message (saveFailed/
// storeChangeRollbackFailed) wrap this already-scrubbed error with it; no
// path can reach the wire either way.
func (m *Manager) saveMarketplaces(mk Marketplaces) error {
	path, err := m.storePath(marketplacesFileName)
	if err != nil {
		return err
	}
	body, err := marketplaceMarshalIndent(mk, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling marketplaces: %w", err)
	}
	if err := marketplaceAtomicWriteFile(path, append(body, '\n'), 0o644); err != nil {
		_, _ = fmt.Fprintf(m.stderr(), "warning: saving %s failed: %v\n", marketplacesFileName, err)
		return fmt.Errorf("saving %s failed; see the hub's log for detail", marketplacesFileName)
	}
	m.markStoreChanged(StoreChanged{Marketplaces: true})
	return nil
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
			// The fetch clears this directory before cloning into it, so a
			// failure leaves a partial clone under the marketplace's name
			// while the entry records no location. Nothing else removes that
			// directory, so the failed fetch does — as AddMarketplace does
			// with its staging directory.
			_ = marketplaceRemoveAll(installLoc)
			return MarketplaceRef{}, err
		}
	}
	ref.InstallLocation = installLoc
	ref.LastUpdated = m.now().UTC()
	mk[name] = ref
	if err := m.saveMarketplaces(mk); err != nil {
		return MarketplaceRef{}, m.saveFailed(name, marketplacesFileName, err)
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
			if rollbackErr := marketplaceRemoveAll(installLoc); rollbackErr != nil {
				return MarketplaceRef{}, m.storeChangeRollbackFailed(name, err, rollbackErr)
			}
		}
		return MarketplaceRef{}, m.saveFailed(name, marketplacesFileName, err)
	}
	return ref, nil
}

// saveFailed scrubs a save failure's absolute plugin-store path -
// atomicWriteFile's own error text names its destination and temp file
// directly - before it reaches the RPC caller, logging the raw error
// server-side first. fileName is the store file saveErr's own caller was
// writing, not assumed: saveRename can fail saving either
// known_marketplaces.json or installed_plugins.json, and the wire error
// needs to name whichever one it was. saveErr wrapping errStoreBetweenNames
// (saveRename's own rollback-also-failed case) keeps that identity through
// %w - the sentinel's own text carries no path - so a caller can still
// errors.Is against it.
func (m *Manager) saveFailed(name, fileName string, saveErr error) error {
	_, _ = fmt.Fprintf(m.stderr(), "warning: saving marketplace %q failed: %v\n", name, saveErr)
	if errors.Is(saveErr, errStoreBetweenNames) {
		return fmt.Errorf("marketplace %q: saving %s failed; see the hub's log for detail: %w", name, fileName, errStoreBetweenNames)
	}
	return fmt.Errorf("marketplace %q: saving %s failed; see the hub's log for detail", name, fileName)
}

// storeChangeRollbackFailed reports that an operation on marketplace name
// failed (cause) and the rollback that tried to undo it also failed
// (rollbackErr), leaving the store changed rather than back as it was found
// - the shape AddMarketplace and EditMarketplace's own fail closure both hit.
// Both cause's and rollbackErr's own text can carry this
// machine's absolute plugin-store path (atomicWriteFile, os.RemoveAll and
// os.Rename all name it directly), so both go to the hub's log instead of
// the RPC caller. cause wrapping errStoreBetweenNames (EditMarketplace's
// rename reaching this branch: saveRename's own between-names failure, whose
// directory rollback then also failed) keeps that identity through %w - the
// sentinel's own text carries no path.
func (m *Manager) storeChangeRollbackFailed(name string, cause, rollbackErr error) error {
	_, _ = fmt.Fprintf(m.stderr(), "warning: marketplace %q: %v; rolling back failed too: %v\n", name, cause, rollbackErr)
	if errors.Is(cause, errStoreBetweenNames) {
		return fmt.Errorf("marketplace %q's change could not be rolled back; see the hub's log for detail: %w", name, errStoreBetweenNames)
	}
	return fmt.Errorf("marketplace %q's change could not be rolled back; see the hub's log for detail", name)
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
	if _, ok := mk[name]; !ok {
		return fmt.Errorf("marketplace %q: %w", name, ErrMarketplaceNotFound)
	}
	// A directory source's install location is its own path, so a directory
	// under the store's canonical name is normally a stale clone — the one a
	// git->directory re-source failed to remove, say. Sweep it whatever the
	// recorded kind, or it outlives the marketplace under a name nothing
	// records and blocks that name for a later rename. The records that must
	// not be swept are directory sources that live at or beneath the clone.
	clone := m.marketplaceDir(name)
	protect, err := m.sweepDestroysSource(mk, clone)
	if err != nil {
		return err
	}
	if !protect {
		if err := marketplaceRemoveAll(clone); err != nil {
			_, _ = fmt.Fprintf(m.stderr(), "warning: removing marketplace clone %s: %v\n", clone, err)
		}
	}
	delete(mk, name)
	return m.saveMarketplaces(mk)
}

// sweepDestroysSource reports whether removing (or renaming away) the canonical
// clone directory clone would delete or break any registered marketplace's
// directory source — the one being removed or renamed, or any other a legacy or
// hand-seeded store recorded against the same path. The store refuses a
// directory source inside itself today, but refuseSourceInStore is enforced
// only by AddMarketplace and EditMarketplace, and the name migration neither
// re-checks it nor rewrites Source.Path, so a record predating that rule (or
// seeded by hand) survives every later write; a sweep must not be what finally
// deletes a live source.
//
// The model is what the sweep actually deletes: the tree at the clone path. A
// source below clone goes with it — even through a symlink, since removing the
// clone deletes the link — so the source is compared against the clone both as
// recorded and after symlinks are resolved. The clone's ancestors are resolved
// because the store root or its marketplaces directory can be a symlink and a
// legacy record can name the physical path, but a final symlink at the clone
// itself is not followed: RemoveAll removes the link, not its target. An
// ancestor of clone is not deleted by RemoveAll and must not be protected, or
// its stale clone would survive and hold the name.
func (m *Manager) sweepDestroysSource(mk Marketplaces, clone string) (bool, error) {
	// Nothing at the clone path makes the sweep a no-op, so there is nothing to
	// protect and no reason to let an unreadable record somewhere else fail the
	// whole removal or rename.
	haveClone, err := pathPresentNoFollow(clone)
	if err != nil {
		_, _ = fmt.Fprintf(m.stderr(), "warning: checking marketplace clone %s: %v\n", clone, err)
		return true, nil
	}
	if !haveClone {
		return false, nil
	}
	absClone, err := filepath.Abs(clone)
	if err != nil {
		_, _ = fmt.Fprintf(m.stderr(), "warning: resolving marketplace clone %s: %v\n", clone, err)
		return true, nil
	}
	resolvedClone, err := resolveAncestors(clone)
	if err != nil {
		_, _ = fmt.Fprintf(m.stderr(), "warning: resolving marketplace clone %s: %v\n", clone, err)
		return true, nil
	}
	underClone := func(path string) bool {
		return pathWithinDir(absClone, path) || pathWithinDir(resolvedClone, path)
	}
	for _, ref := range mk {
		if ref.Source.Kind != SourceDirectory || ref.Source.Path == "" {
			continue
		}
		touches, err := sourceTouchesClone(ref.Source.Path, underClone, 0)
		if err != nil {
			// A source the walk cannot inspect is one the sweep might still be
			// the thing that deletes: protect it rather than fail the operation
			// or run the sweep blind.
			_, _ = fmt.Fprintf(m.stderr(), "warning: checking directory source %s: %v\n", ref.Source.Path, err)
			return true, nil
		}
		if touches {
			return true, nil
		}
	}
	return false, nil
}

// resolveAncestors canonicalizes every component of path except the last, so a
// symlinked store root or marketplaces directory is followed but a final
// symlink is not — RemoveAll and rename act on the link itself.
func resolveAncestors(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", path, err)
	}
	dir, base := filepath.Split(abs)
	if dir == "" {
		return abs, nil
	}
	resolved, err := resolveForContainment(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, base), nil
}

// maxSymlinkHops bounds the source-link walk so a symlink cycle cannot spin.
const maxSymlinkHops = 64

// sourceTouchesClone reports whether deleting the tree at the clone would delete
// or break the recorded source: whether the source's absolute path, its fully
// resolved target, or any symlink met while resolving it sits at or beneath the
// clone. The walk follows each link's own target in turn, because a chain can
// leave the clone and come back — a source reached through a link the clone
// holds is broken even when its final target is elsewhere, and EvalSymlinks
// alone would hide both that hop and a second link standing between them.
func sourceTouchesClone(source string, underClone func(string) bool, depth int) (bool, error) {
	if depth > maxSymlinkHops {
		// Deeper than any sane chain: assume the sweep holds a link the source
		// needs and protect.
		return true, nil
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		return false, fmt.Errorf("resolving %s: %w", source, err)
	}
	if underClone(abs) {
		return true, nil
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil && underClone(resolved) {
		return true, nil
	}
	root := filepath.VolumeName(abs) + string(filepath.Separator)
	current := root
	for comp := range strings.SplitSeq(strings.TrimPrefix(abs, root), string(filepath.Separator)) {
		if comp == "" {
			continue
		}
		next := filepath.Join(current, comp)
		info, err := marketplaceLstat(next)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || pathCannotExist(err) {
				// A component that is not there cannot be destroyed.
				return false, nil
			}
			return false, fmt.Errorf("checking %s: %w", next, err)
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			current = next
			continue
		}
		target, err := os.Readlink(next)
		if err != nil {
			return false, fmt.Errorf("reading link %s: %w", next, err)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(next), target)
		}
		// The link itself, and the path it names, both go if the clone holds
		// them — the final target alone would miss a hop back out of the clone.
		if underClone(next) || underClone(target) {
			return true, nil
		}
		// The target is a path in its own right: its own links can lead back
		// into the clone even where the recorded path's components do not.
		touches, err := sourceTouchesClone(target, underClone, depth+1)
		if err != nil {
			return false, err
		}
		if touches {
			return true, nil
		}
		current = filepath.Clean(target)
	}
	return false, nil
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
		swapErr, undoErr := undoSwap(), runUndo(undo)
		_ = marketplaceRemoveAll(staging)
		if swapErr == nil && undoErr == nil {
			return MarketplaceRef{}, err
		}
		// The rollback could not put every directory back, so the store is
		// left changed rather than back as it was found. swapErr/undoErr are
		// undoSwap/runUndo's own renames and removes, which can carry this
		// machine's absolute plugin-store path - storeChangeRollbackFailed's
		// log is the only place that says which.
		return MarketplaceRef{}, m.storeChangeRollbackFailed(name, err, errors.Join(swapErr, undoErr))
	}

	// 2. Rename on disk and in the registry. The registry as the edit found it
	// is kept because the re-key below replaces it wholesale and a later
	// failure has to write it back.
	target := name
	registryAsFound := reg
	if renaming {
		target = newName
		ref, reg, undo, err = m.moveMarketplace(name, newName, ref, mk, reg, registryKeyOwners(reg, mk))
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
		// saveRename already returns a scrubbed error, naming whichever file
		// (known_marketplaces.json or installed_plugins.json) actually
		// failed, before fail decides whether the outer rollback also needs
		// reporting.
		if err := m.saveRename(mk, name, newName, ref, reg, registryAsFound); err != nil {
			return fail(err)
		}
	} else {
		mk[name] = ref
		if err := m.saveMarketplaces(mk); err != nil {
			return fail(m.saveFailed(name, marketplacesFileName, err))
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

// moveMarketplace renames a marketplace on disk and in the registry: the clone
// directory, when one is there — moved for a non-directory source, swept for a
// directory source, which never lives there; the plugin cache directory, when
// one is there; and every <plugin>@name registry entry, whose install path
// follows the cache. Neither store file is written. On success it returns the
// ref and registry as they are to be recorded, and the steps that put the
// directories back should a later step fail; a failure puts back what it had
// moved itself and reports what it could not.
func (m *Manager) moveMarketplace(name, newName string, ref MarketplaceRef, mk Marketplaces, reg Registry, owners map[string]string) (MarketplaceRef, Registry, []func() error, error) {
	var undo []func() error
	fail := func(err error) (MarketplaceRef, Registry, []func() error, error) {
		if undoErr := runUndo(undo); undoErr != nil {
			return MarketplaceRef{}, Registry{}, nil, errors.Join(err, undoErr, errRenameRollbackIncomplete)
		}
		return MarketplaceRef{}, Registry{}, nil, err
	}
	oldDir, newDir := m.marketplaceDir(name), m.marketplaceDir(newName)
	if ref.Source.Kind != SourceDirectory {
		// The clone move follows a final symlink: pathPresent stats through it,
		// so the move refuses rather than blindly renaming an entry it cannot
		// read (TestEditMarketplace_TreatsOnlyAMissingPathAsAbsent).
		haveClone, err := pathPresent(oldDir)
		if err != nil {
			return fail(err)
		}
		// Whatever the entry records: a lazy fetch clears and refills this
		// directory before it writes an install location, so a fetch killed
		// mid-clone leaves one behind that only the move takes with the name.
		if haveClone {
			// The move takes the directory with the name. If another
			// marketplace's directory source names it, that move would strip
			// the source out from under a live record — as a hand-seeded or
			// pre-refuseSourceInStore store can arrange — so refuse instead.
			displaced, err := m.sweepDestroysSource(mk, oldDir)
			if err != nil {
				return fail(err)
			}
			if displaced {
				return fail(fmt.Errorf("renaming marketplace clone %s would move a directory source another marketplace records", oldDir))
			}
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
	} else {
		// A directory source never lives under the clone directory, so one
		// there is residue — the clone a git->directory re-source failed to
		// remove. Sweep it rather than moving it: moving it would park the
		// same unrecorded directory under the new name, which is still a name
		// nothing records it under. Nothing references it, so there is nothing
		// to put back and no undo step to add — unless some record's directory
		// source lives at or beneath it, which keeps the sweep from deleting a
		// live source. Presence is the entry's own, not a stat through a final
		// symlink: sweeping is what removes it either way, so an unreadable
		// link must still be cleared rather than block a directory rename.
		protect, err := m.sweepDestroysSource(mk, oldDir)
		if err != nil {
			return fail(err)
		}
		if !protect {
			if err := marketplaceRemoveAll(oldDir); err != nil {
				return fail(fmt.Errorf("removing stale marketplace clone %s: %w", oldDir, err))
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
	return ref, rekeyRegistry(reg, owners, name, newName, oldCache, newCache), undo, nil
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
//
// The returned error is already scrubbed through saveFailed, naming whichever
// store file (known_marketplaces.json or installed_plugins.json) actually
// failed instead of the raw *fs.PathError, so no caller — EditMarketplace or
// a migration recovery reached from ListMarketplaces/List, which can surface
// its error over RPC just as directly — can forget to scrub before the
// absolute plugin-store path in the raw error reaches an RPC caller.
func (m *Manager) saveRename(mk Marketplaces, name, newName string, ref MarketplaceRef, reg, registryAsFound Registry) error {
	if err := m.saveRegistry(reg); err != nil {
		return m.saveFailed(name, registryFileName, err)
	}
	delete(mk, name)
	mk[newName] = ref
	if err := m.saveMarketplaces(mk); err != nil {
		if restoreErr := m.saveRegistry(registryAsFound); restoreErr != nil {
			return m.saveFailed(name, marketplacesFileName, fmt.Errorf("saving %s failed (%w); restoring %s failed (%w), so %w: it still keys this marketplace's plugins under %q", marketplacesFileName, err, registryFileName, restoreErr, errStoreBetweenNames, newName))
		}
		return m.saveFailed(name, marketplacesFileName, err)
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

// registryKeyOwner names the marketplace a registry key belongs to: the longest
// recorded name the key ends in as "@<name>". A plugin's own '@' sits before
// that one, so wid@get@acme is wid@get in acme — but a key can end in more than
// one recorded name at once: "<plugin>@x@y@z" ends in "@y@z" and in "@z". The
// longest wins, so migrating "z" leaves a marketplace recorded as "x@y@z" its
// keys, which migrating it later would otherwise no longer find. Whether a name
// matched is answered apart from the name itself, because the empty name is a
// recorded name a key ends in: "widget@" is widget in the marketplace called
// "".
func registryKeyOwner(key string, mk Marketplaces) (string, bool) {
	best, found := "", false
	for name := range mk {
		if strings.HasSuffix(key, "@"+name) && (!found || len(name) > len(best)) {
			best, found = name, true
		}
	}
	return best, found
}

// registryKeyOwners names, for every key in the registry, the marketplace it
// belongs to as the store was found. Ownership is taken once, before any
// rename: a rename rewrites keys, and the names it writes are recorded, so
// recomputing ownership later would let a key rewritten for one marketplace be
// claimed by a name the migration itself had just made — which is not a name
// the key was ever keyed under.
func registryKeyOwners(reg Registry, mk Marketplaces) map[string]string {
	owners := make(map[string]string, len(reg.Plugins))
	for key := range reg.Plugins {
		if owner, ok := registryKeyOwner(key, mk); ok {
			owners[key] = owner
		}
	}
	return owners
}

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
func rekeyRegistry(reg Registry, owners map[string]string, oldName, newName, oldCache, newCache string) Registry {
	out := Registry{Version: reg.Version, Plugins: make(map[string][]InstallEntry, len(reg.Plugins))}
	for key, entries := range reg.Plugins {
		if owner, ok := owners[key]; !ok || owner != oldName {
			out.Plugins[key] = entries
		}
	}
	for key, entries := range reg.Plugins {
		if owner, ok := owners[key]; !ok || owner != oldName {
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
		movedKey := registryKey(plugin, newName)
		out.Plugins[movedKey] = moved
		// The key is new, so its owner is the name it was just keyed under:
		// recorded, so the steps after this one in the same pass agree on it.
		owners[movedKey] = newName
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
		// A path the filesystem will not consider at all holds nothing, so it
		// counts as absent rather than failing the operation that asked: the
		// migration has to be able to look at a legacy name's directories in
		// order to rename it, and failing here would leave every lock-taking
		// operation failing on the store instead.
		if !errors.Is(err, fs.ErrNotExist) && !pathCannotExist(err) {
			return false, fmt.Errorf("checking %s: %w", path, err)
		}
		if _, err := marketplaceLstat(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) || pathCannotExist(err) {
				return false, nil
			}
			return false, fmt.Errorf("checking %s: %w", path, err)
		}
	}
	return true, nil
}

// pathPresentNoFollow reports whether a directory entry exists at path without
// resolving a final symlink — the entry RemoveAll and rename act on. It is what
// a sweep needs: a clone link whose target is unreadable (a self-loop, a
// permission wall) is still an entry the sweep must clear, where pathPresent's
// stat-through-the-link would error and strand it. Only a missing entry is
// absent; any other lstat error is the caller's to return.
func pathPresentNoFollow(path string) (bool, error) {
	if _, err := marketplaceLstat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) || pathCannotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("checking %s: %w", path, err)
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
				// As in ensureFetched: the fetch cleared this directory
				// before cloning into it, and a failure would leave a
				// partial clone under the marketplace's name.
				_ = marketplaceRemoveAll(installLoc)
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
	if err := m.saveMarketplaces(mk); err != nil {
		return m.saveFailed(name, marketplacesFileName, err)
	}
	return nil
}
