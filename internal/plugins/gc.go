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

// referencedClonePaths is the set of marketplace clone directories the
// marketplaces file names — the ones that are live rather than orphaned. Gc
// removes what is not in it. Each install location has its ancestors resolved,
// so a store reached through a symlinked marketplaces directory compares like
// with like: a record naming the physical path still matches the lexical path a
// directory listing derives.
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

// Gc sweeps cacheDir() (cache/<marketplace>/<plugin>/<sha>/) for materialized
// plugin dirs that no registry entry's InstallPath references, and removes
// them. It also sweeps marketplacesDir() for clone dirs that no recorded
// marketplace's InstallLocation names — the residue a failed removal, add or
// edit whose directory cleanup failed leaves behind. It returns the removed
// paths.
//
// Superseded dirs accumulate because Upgrade never deletes (design doc
// §7/§12): a live session holds an absolute path into its materialized dir,
// so a background auto-upgrade must leave the old dir in place. Gc is the
// separate, dumb sweep that reclaims them — a snapshot diff against the
// registry, not live-session refcounting. It is only safe to call when no
// session could be actively starting against a dir about to be removed: on
// hub start (before any session exists) or on demand via `evener plugin gc`
// when the user is idle. Gc itself does not enforce that; it runs under the
// same flock as every other mutation, so it never races an install/upgrade.
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
	referenced := referencedInstallPaths(reg)

	// Both store files are read before either sweep mutates disk, so a corrupt
	// marketplaces file aborts Gc with nothing removed rather than after the
	// cache sweep has already deleted directories.
	mk, err := m.loadMarketplaces()
	if err != nil {
		return nil, err
	}

	// Non-nil even when nothing is swept: callers (cmd/evener/plugincmd.go's
	// `gc --json`) JSON-encode this directly, and a nil slice would encode as
	// `null` instead of `[]`.
	removed := []string{}
	if marketplaceEntries, err := gcReadDir(m.cacheDir()); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("reading %s: %w", m.cacheDir(), err)
		}
	} else {
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
	}

	// A copy of the marketplaces file a fetch would have cleared is not a
	// clone, and neither scratch name is. A recorded directory source at or
	// beneath a candidate is live data the sweep must keep, the same protection
	// RemoveMarketplace's own clone sweep applies. A read failure here is
	// returned — after the cache removals already made — rather than reported
	// as a success that skipped the clone sweep.
	if cloneEnts, err := gcReadDir(m.marketplacesDir()); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return removed, fmt.Errorf("reading marketplaces directory: %w", err)
		}
	} else {
		ownedClones, err := referencedClonePaths(mk)
		if err != nil {
			return removed, err
		}
		protectedSources := marketplaceProtectionPaths(mk)
		for _, ent := range cloneEnts {
			// A symlink occupies the name as a directory does — a failed
			// cleanup can leave one where the clone was — and RemoveAll removes
			// the link, never its target, so it is a candidate too.
			if isScratchCloneName(ent.Name()) || (!ent.IsDir() && ent.Type()&fs.ModeSymlink == 0) {
				continue
			}
			dir := filepath.Join(m.marketplacesDir(), ent.Name())
			// Ancestors resolved but a final symlink not followed, so a
			// leftover link is compared as the name it occupies, as
			// sweepDestroysSource does.
			resolved, err := resolveAncestors(dir)
			if err != nil {
				return removed, err
			}
			if ownedClones[resolved] {
				continue
			}
			if present, protect := m.sweepDestroysSource(protectedSources, dir); !present || protect {
				continue
			}
			if err := gcRemoveAll(dir); err != nil {
				return removed, fmt.Errorf("removing %s: %w", dir, err)
			}
			removed = append(removed, dir)
		}
	}

	sort.Strings(removed)
	return removed, nil
}
