package plugins

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// Each remote is asked with its own timeout, a few at a time, so one slow or
// unreachable host delays a check by at most updateCheckTimeout.
const (
	updateCheckTimeout     = 20 * time.Second
	updateCheckConcurrency = 4
)

// CheckUpdates asks the remote of every installed git-backed plugin for the
// commit an Upgrade would install now, and remembers the answers so List
// reports UpdateAvailable until the next check or that plugin's upgrade. The
// source asked is the one the marketplace's local catalog names, the one
// Upgrade fetches. A source pinned to a sha is answered without a network
// call, and a relative or directory source is never asked: it has no remote.
// A remote that cannot be asked is warned about and flags nothing.
func (m *Manager) CheckUpdates(ctx context.Context) error {
	mk, err := m.loadMigratedMarketplaces(ctx, installAcquireLock)
	if err != nil {
		return err
	}
	reg, err := m.loadRegistry()
	if err != nil {
		return err
	}
	catalogs := map[string]Catalog{}
	var mu sync.Mutex
	heads := map[string]string{}
	var g errgroup.Group
	g.SetLimit(updateCheckConcurrency)
	for key := range reg.Plugins {
		plugin, marketplace := splitKey(key)
		src, ok := m.upgradeSource(mk, catalogs, marketplace, plugin)
		if !ok || gitRemoteURL(src) == "" {
			continue
		}
		g.Go(func() error {
			head, err := remoteHead(ctx, src)
			if err != nil {
				_, _ = fmt.Fprintf(m.stderr(), "warning: checking %s for updates: %v\n", key, err)
				return nil
			}
			mu.Lock()
			heads[key] = head
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	m.remoteHeadsMu.Lock()
	m.remoteHeads = heads
	m.remoteHeadsMu.Unlock()
	return nil
}

// upgradeSource is plugin's source in marketplace's local catalog, parsing each
// catalog once into catalogs. ok is false when the catalog cannot be read or
// no longer lists the plugin, where an Upgrade would fail too.
func (m *Manager) upgradeSource(mk Marketplaces, catalogs map[string]Catalog, marketplace, plugin string) (Source, bool) {
	cat, parsed := catalogs[marketplace]
	if !parsed {
		ref, known := mk[marketplace]
		if !known {
			return Source{}, false
		}
		cat, _ = ParseCatalog(m.catalogRoot(ref))
		catalogs[marketplace] = cat
	}
	for _, p := range cat.Plugins {
		if p.Name == plugin {
			return p.Source, true
		}
	}
	return Source{}, false
}

func remoteHead(ctx context.Context, src Source) (string, error) {
	if src.Sha != "" {
		return src.Sha, nil
	}
	ctx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()
	return gitRemoteHead(ctx, gitRemoteURL(src), src.Ref)
}

// updateAvailable reports whether the last CheckUpdates found a commit for key
// other than installedSha.
func (m *Manager) updateAvailable(key, installedSha string) bool {
	m.remoteHeadsMu.Lock()
	defer m.remoteHeadsMu.Unlock()
	head := m.remoteHeads[key]
	return head != "" && head != installedSha
}

// forgetRemoteHead drops key's checked head once an Upgrade has fetched the
// plugin's current source, so a head the check saw before the upgrade cannot
// flag the plugin again.
func (m *Manager) forgetRemoteHead(key string) {
	m.remoteHeadsMu.Lock()
	defer m.remoteHeadsMu.Unlock()
	delete(m.remoteHeads, key)
}
