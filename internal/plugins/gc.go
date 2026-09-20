package plugins

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

var (
	gcAcquireLock = acquireLock
	gcReadDir     = os.ReadDir
	gcRemoveAll   = os.RemoveAll
)

// referencedInstallPaths is the set of materialized directories the registry
// names — the ones that are live rather than orphaned. Gc removes what is not
// in it and Doctor reports what is not in it, and the two have to agree: a
// directory Doctor calls orphaned that Gc would keep sends the user to a
// command that does nothing.
func referencedInstallPaths(reg Registry) map[string]bool {
	referenced := make(map[string]bool, len(reg.Plugins))
	for _, entries := range reg.Plugins {
		for _, e := range entries {
			referenced[filepath.Clean(e.InstallPath)] = true
		}
	}
	return referenced
}

// Gc sweeps cacheDir() (cache/<marketplace>/<plugin>/<sha>/) for materialized
// plugin dirs that no registry entry's InstallPath references, and removes
// them. It also sweeps marketplacesDir() for clone dirs that no recorded
// marketplace's InstallLocation references — the residue a removal, add or
// edit whose directory cleanup failed leaves behind. It returns the removed
// paths.
//
// Superseded dirs accumulate because Upgrade never deletes (design doc
// §7/§12): a live session holds an absolute path into its materialized dir,
// so a background auto-upgrade must leave the old dir in place. Gc is the
// separate, dumb sweep that reclaims them — a snapshot diff against the
// registry and the marketplaces file, not live-session refcounting. It is only
// safe to call when no session could be actively starting against a dir about
// to be removed: on hub start (before any session exists) or on demand via
// `evener plugin gc` when the user is idle. Gc itself does not enforce that; it
// runs under the same flock as every other mutation, so it never races an
// install, upgrade or marketplace fetch.
func (m *Manager) Gc(ctx context.Context) ([]string, error) {
	release, err := m.lockStore(ctx, gcAcquireLock, 30*time.Second)
	if err != nil {
		return nil, err
	}
	defer release()

	reg, err := m.loadRegistry()
	if err != nil {
		return nil, err
	}

	// The marketplaces file — and, just below, the retained-clone record — is
	// read before either sweep mutates disk, so a corrupt one aborts Gc with
	// nothing removed rather than after the cache sweep has already deleted
	// directories.
	mk, err := m.loadMarketplaces()
	if err != nil {
		return nil, err
	}

	// The store's exception list: clone paths a removal deliberately spared.
	retained, err := m.loadRetainedClones()
	if err != nil {
		return nil, err
	}

	// Non-nil even when nothing is swept: callers (cmd/evener/plugincmd.go's
	// `gc --json`) JSON-encode this directly, and a nil slice would encode as
	// `null` instead of `[]`. sweepOrphanCacheDirs supplies the non-nil slice.
	removed, err := m.sweepOrphanCacheDirs(referencedInstallPaths(reg))
	if err != nil {
		return removed, err
	}

	clones, err := m.sweepOrphanedClones(mk, reg, retained)
	removed = append(removed, clones...)
	if err != nil {
		return removed, err
	}

	sort.Strings(removed)
	return removed, nil
}

// sweepOrphanCacheDirs removes the materialized plugin dirs under cacheDir()
// that referenced — the InstallPaths the registry names — does not. It returns
// a non-nil empty slice when there is nothing to remove.
func (m *Manager) sweepOrphanCacheDirs(referenced map[string]bool) ([]string, error) {
	marketplaceEntries, err := gcReadDir(m.cacheDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", m.cacheDir(), err)
	}

	removed := []string{}
	for _, mktEnt := range marketplaceEntries {
		if !mktEnt.IsDir() {
			continue
		}
		mktDir := filepath.Join(m.cacheDir(), mktEnt.Name())
		pluginEntries, err := gcReadDir(mktDir)
		if err != nil {
			continue // best-effort: a transient read error on one marketplace shouldn't abort the sweep
		}
		for _, plugEnt := range pluginEntries {
			if !plugEnt.IsDir() {
				continue
			}
			pluginDir := filepath.Join(mktDir, plugEnt.Name())
			shaEntries, err := gcReadDir(pluginDir)
			if err != nil {
				continue
			}
			for _, shaEnt := range shaEntries {
				if !shaEnt.IsDir() {
					continue
				}
				shaDir := filepath.Join(pluginDir, shaEnt.Name())
				if referenced[filepath.Clean(shaDir)] {
					continue
				}
				if err := gcRemoveAll(shaDir); err != nil {
					return removed, fmt.Errorf("removing %s: %w", shaDir, err)
				}
				removed = append(removed, shaDir)
			}
		}
	}
	return removed, nil
}

// sweepOrphanedClones removes the directories under marketplacesDir() that no
// recorded marketplace's InstallLocation names. The caller holds the store
// lock, so no fetch can be mid-clone against one of them.
//
// A directory source that sits at or beneath a candidate is live data the
// sweep must not delete, the same protection RemoveMarketplace's own clone
// sweep applies — a hand-seeded or pre-rule record can still point into the
// store. The two scratch names are the store's own, not leftovers: a fetch
// stages into .staging, and .old holds the clone a failed swap could not put
// back, which can be the only local copy.
//
// A failed cleanup can leave a symlink where the clone was, and a symlink
// occupies the name exactly as a directory does — it blocks the rename that
// reports "it must be deleted". It is swept too: RemoveAll removes the link,
// never its target, and sweepDestroysSource already compares sources against
// the link without following it.
func (m *Manager) sweepOrphanedClones(mk Marketplaces, reg Registry, retained []string) ([]string, error) {
	owned, err := referencedClonePaths(mk)
	if err != nil {
		return nil, err
	}
	entries, err := gcReadDir(m.marketplacesDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		// Best-effort, as for one marketplace's cache subdirectory: the cache
		// sweep above is Gc's primary work, and a transient error on the
		// secondary walk must not undo its report. The raw error names the
		// store path, so it goes to the log rather than the returned error.
		_, _ = fmt.Fprintf(m.stderr(), "warning: reading %s: %v\n", m.marketplacesDir(), err)
		return nil, nil
	}
	protected := cloneProtectionPaths(mk, reg, retained)
	var removed []string
	for _, ent := range entries {
		if isScratchCloneName(ent.Name()) || (!ent.IsDir() && ent.Type()&fs.ModeSymlink == 0) {
			continue
		}
		dir := filepath.Join(m.marketplacesDir(), ent.Name())
		// Ancestors are resolved but a final symlink is not, so a leftover
		// link is compared as the name it occupies rather than as whatever it
		// points at — the same view sweepDestroysSource takes of it.
		resolved, err := resolveAncestors(dir)
		if err != nil {
			_, _ = fmt.Fprintf(m.stderr(), "warning: resolving marketplace clone %s: %v\n", dir, err)
			continue
		}
		if owned[resolved] {
			continue
		}
		// present is checked too: an entry removed between ReadDir and here is
		// nothing to sweep, and RemoveAll on a missing path would report it as
		// removed.
		if present, protect := m.sweepDestroysSource(protected, dir); !present || protect {
			continue
		}
		if err := gcRemoveAll(dir); err != nil {
			// The raw *fs.PathError names this machine's store path, so it goes
			// to the log; the returned error names only the marketplace.
			_, _ = fmt.Fprintf(m.stderr(), "warning: removing orphaned marketplace clone %s: %v\n", dir, err)
			return removed, fmt.Errorf("removing the orphaned %q marketplace clone failed; see the hub's log for detail", ent.Name())
		}
		removed = append(removed, dir)
	}
	return removed, nil
}

// cloneProtectionPaths names every recorded path that removing a clone would
// destroy or break: each marketplace's directory source, each *other*
// marketplace's install location, and every registry InstallPath.
// marketplaceProtectionPaths alone is not enough for a clone sweep:
// RemoveMarketplace drops a marketplace's registration but deliberately leaves
// its plugins' registry entries — and their install paths — behind, so a
// legacy record whose install lies inside the clone would otherwise be broken
// by the sweep that reclaims the clone. A hand-seeded store can likewise point
// another record's location into a clone.
//
// exclude names the records being removed: their install location is the clone
// the caller is reclaiming, so it must not protect it. A directory source is
// always protected, the excluded record's included, because a legacy record can
// source from the clone its own name derives.
func cloneProtectionPaths(mk Marketplaces, reg Registry, retained []string, exclude ...string) []string {
	skip := make(map[string]bool, len(exclude))
	for _, n := range exclude {
		skip[n] = true
	}
	protect := marketplaceProtectionPaths(mk)
	for name, ref := range mk {
		if skip[name] {
			continue
		}
		if ref.InstallLocation != "" {
			protect = append(protect, ref.InstallLocation)
		}
	}
	for _, entries := range reg.Plugins {
		for _, e := range entries {
			if e.InstallPath != "" {
				protect = append(protect, e.InstallPath)
			}
		}
	}
	// The exception record's retained clones: directories a removal spared
	// because a recorded source sits at or beneath them, whose record is gone.
	protect = append(protect, retained...)
	return protect
}

// referencedClonePaths is the set of marketplace clone directories the
// marketplaces file names — the ones that are live rather than orphaned.
// Gc removes what is not in it and Doctor reports what is not in it, and the
// two have to agree. Each recorded install location has its ancestors resolved
// (but a final symlink not followed), so a store reached through a symlink
// compares like with like and a residue link is compared as the name it
// occupies.
func referencedClonePaths(mk Marketplaces) (map[string]bool, error) {
	owned := make(map[string]bool, len(mk))
	for _, ref := range mk {
		if ref.InstallLocation == "" {
			continue
		}
		resolved, err := resolveAncestors(ref.InstallLocation)
		if err != nil {
			return nil, err
		}
		owned[resolved] = true
	}
	return owned, nil
}

// isScratchCloneName reports whether name is one of the two fixed scratch
// directories under the marketplaces directory. Neither is a marketplace's
// clone, so neither is a leftover the sweep may remove.
func isScratchCloneName(name string) bool {
	return name == stagingCloneName || name == asideCloneName
}
