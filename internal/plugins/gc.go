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

// referencedInstallPaths is the set of materialized directories named by the
// registry. Doctor uses it to identify retained published revisions; a session
// may still reference one after the registry moves to a newer revision.
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

// Gc preserves every published plugin cache revision, including revisions no
// longer named by the registry: sessions can retain their exact absolute paths
// across upgrades and resumes, even in another state root. Superseded revisions
// therefore accumulate on disk. Gc sweeps marketplacesDir() for clone dirs no recorded
// marketplace's InstallLocation names — the residue a failed removal, add or
// edit whose directory cleanup failed leaves behind. It returns the removed
// paths.
//
// Gc holds the same store lock as installation and upgrade, and still checks
// registry and marketplace bookkeeping before sweeping clones. Explicit
// uninstall and marketplace removal keep their separate deletion semantics.
func (m *Manager) Gc(ctx context.Context) ([]string, error) {
	release, err := m.lockStore(ctx, gcAcquireLock, 30*time.Second)
	if err != nil {
		return nil, err
	}
	defer release()

	if _, err := m.loadRegistry(); err != nil {
		return nil, err
	}

	// Validate both store files before mutating any clone directories.
	mk, err := m.loadMarketplaces()
	if err != nil {
		return nil, err
	}

	// Non-nil even when nothing is swept: callers (cmd/evener/plugincmd.go's
	// `gc --json`) JSON-encode this directly, and a nil slice would encode as
	// `null` instead of `[]`.
	removed := []string{}
	// A copy of the marketplaces file a fetch would have cleared is not a
	// clone, and neither scratch name is. A recorded directory source at or
	// beneath a candidate is live data the sweep must keep, the same protection
	// RemoveMarketplace's own clone sweep applies. A read failure here is
	// returned rather than reported as a successful clone sweep.
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

	// Reclaim publication staging a killed fetch left behind. Best effort: the
	// store lock Gc holds is the same one every updated publisher takes, so no
	// publisher can be filling a container while this runs; only a finished or
	// dead invocation's container can be old enough to match. A failure here
	// leaves residue for a later sweep rather than failing the clone sweep.
	m.reclaimPublicationStaging()

	sort.Strings(removed)
	return removed, nil
}

// reclaimPublicationStaging removes abandoned .staging- containers beneath
// each publishing/<marketplace>/<plugin> directory.
func (m *Manager) reclaimPublicationStaging() {
	marketplaces, err := gcReadDir(filepath.Join(m.Root, publishingDirName))
	if err != nil {
		return
	}
	for _, marketplace := range marketplaces {
		if !marketplace.IsDir() {
			continue
		}
		plugins, err := gcReadDir(filepath.Join(m.Root, publishingDirName, marketplace.Name()))
		if err != nil {
			continue
		}
		for _, plugin := range plugins {
			if !plugin.IsDir() {
				continue
			}
			dir := filepath.Join(m.Root, publishingDirName, marketplace.Name(), plugin.Name())
			for _, staging := range m.abandonedStagingWithPrefix(dir, publicationStagingPrefix) {
				_ = gcRemoveAll(staging)
			}
		}
	}
}
