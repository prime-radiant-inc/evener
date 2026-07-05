package plugins

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Gc sweeps cacheDir() (cache/<marketplace>/<plugin>/<sha>/) for materialized
// plugin dirs that no registry entry's InstallPath references, and removes
// them. It returns the removed paths.
//
// Superseded dirs accumulate because Upgrade never deletes (design doc
// §7/§12): a live session holds an absolute path into its materialized dir,
// so a background auto-upgrade must leave the old dir in place. Gc is the
// separate, dumb sweep that reclaims them — a snapshot diff against the
// registry, not live-session refcounting. It is only safe to call when no
// session could be actively starting against a dir about to be removed: on
// hub start (before any session exists) or on demand via `serf plugin gc`
// when the user is idle. Gc itself does not enforce that; it runs under the
// same flock as every other mutation, so it never races an install/upgrade.
func (m *Manager) Gc() ([]string, error) {
	release, err := acquireLock(m.lockPath(), 30*time.Second)
	if err != nil {
		return nil, err
	}
	defer release()

	reg, err := LoadRegistry(m.registryPath())
	if err != nil {
		return nil, err
	}
	referenced := make(map[string]bool, len(reg.Plugins))
	for _, entries := range reg.Plugins {
		for _, e := range entries {
			referenced[filepath.Clean(e.InstallPath)] = true
		}
	}

	marketplaceEntries, err := os.ReadDir(m.cacheDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", m.cacheDir(), err)
	}

	// Non-nil even when nothing is swept: callers (cmd/serf/plugincmd.go's
	// `gc --json`) JSON-encode this directly, and a nil slice would encode as
	// `null` instead of `[]`.
	removed := []string{}
	for _, mktEnt := range marketplaceEntries {
		if !mktEnt.IsDir() {
			continue
		}
		mktDir := filepath.Join(m.cacheDir(), mktEnt.Name())
		pluginEntries, err := os.ReadDir(mktDir)
		if err != nil {
			continue // best-effort: a transient read error on one marketplace shouldn't abort the sweep
		}
		for _, plugEnt := range pluginEntries {
			if !plugEnt.IsDir() {
				continue
			}
			pluginDir := filepath.Join(mktDir, plugEnt.Name())
			shaEntries, err := os.ReadDir(pluginDir)
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
				if err := os.RemoveAll(shaDir); err != nil {
					return removed, fmt.Errorf("removing %s: %w", shaDir, err)
				}
				removed = append(removed, shaDir)
			}
		}
	}
	sort.Strings(removed)
	return removed, nil
}
