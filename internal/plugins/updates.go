package plugins

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// Each remote is asked with its own timeout, a few at a time, so one
// unreachable host costs a check about updateCheckTimeout rather than git's
// own, much longer, network timeouts.
const (
	updateCheckTimeout     = 20 * time.Second
	updateCheckConcurrency = 4
)

// checkedHead is one plugin's CheckUpdates answer: the commit an Upgrade would
// install, and the commit installed when the check read the registry. The
// answer holds only while that install is still the one in the registry, so
// any change to it (an Upgrade, a reinstall, an upgrade from another process)
// retires the answer without anyone having to clear it.
type checkedHead struct {
	head, installed string
}

// CheckUpdates asks the remote of every installed git-backed plugin for the
// commit an Upgrade would install now, and remembers the answers so List
// reports UpdateAvailable until the next check or a change to that install.
// The source asked is the one the marketplace's local catalog names, the one
// Upgrade fetches. A source pinned to a sha is answered without a network
// call, and a relative or directory source is never asked: it has no remote.
// A remote or catalog that cannot be read is warned about and flags nothing.
// A cancelled check returns ctx's error and keeps the previous answers.
func (m *Manager) CheckUpdates(ctx context.Context) error {
	mk, err := m.loadMigratedMarketplaces(ctx, installAcquireLock)
	if err != nil {
		return err
	}
	reg, err := m.loadRegistry()
	if err != nil {
		return err
	}
	// The loop's catalog warnings and the goroutines' remote warnings are
	// kept apart, so only the latter need mu.
	var catalogWarnings, remoteWarnings []string
	catalogs := map[string]Catalog{}
	var mu sync.Mutex
	heads := map[string]checkedHead{}
	var g errgroup.Group
	g.SetLimit(updateCheckConcurrency)
	for key, entries := range reg.Plugins {
		if len(entries) == 0 {
			continue
		}
		installed := entries[0].GitCommitSha
		plugin, marketplace := splitKey(key)
		src, ok := m.upgradeSource(mk, catalogs, &catalogWarnings, marketplace, plugin)
		if !ok || gitRemoteURL(src) == "" {
			continue
		}
		g.Go(func() error {
			head, err := remoteHead(ctx, src)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				remoteWarnings = append(remoteWarnings, fmt.Sprintf("checking %s for updates: %v", key, err))
			} else {
				heads[key] = checkedHead{head: head, installed: installed}
			}
			return nil
		})
	}
	_ = g.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	warnings := slices.Concat(catalogWarnings, remoteWarnings)
	slices.Sort(warnings)
	for _, w := range warnings {
		_, _ = fmt.Fprintf(m.stderr(), "warning: %s\n", w)
	}
	m.remoteHeadsMu.Lock()
	m.remoteHeads = heads
	m.remoteHeadsMu.Unlock()
	return nil
}

// upgradeSource is plugin's source in marketplace's local catalog, parsing each
// catalog once into catalogs. ok is false when the catalog cannot be read or
// no longer lists the plugin, where an Upgrade would fail too.
func (m *Manager) upgradeSource(mk Marketplaces, catalogs map[string]Catalog, warnings *[]string, marketplace, plugin string) (Source, bool) {
	cat, parsed := catalogs[marketplace]
	if !parsed {
		ref, known := mk[marketplace]
		if !known {
			return Source{}, false
		}
		var err error
		if cat, err = ParseCatalog(m.catalogRoot(ref)); err != nil {
			*warnings = append(*warnings, fmt.Sprintf("reading marketplace.json for %s: %v", marketplace, err))
		}
		catalogs[marketplace] = cat
	}
	p, ok := cat.plugin(plugin)
	return p.Source, ok
}

func remoteHead(ctx context.Context, src Source) (string, error) {
	if src.Sha != "" {
		return src.Sha, nil
	}
	ctx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()
	return gitRemoteHead(ctx, gitRemoteURL(src), src.Ref)
}

// updateAvailable reports whether the last CheckUpdates found, for an install
// still at installed, a commit other than installed.
func (m *Manager) updateAvailable(key, installed string) bool {
	m.remoteHeadsMu.Lock()
	checked := m.remoteHeads[key]
	m.remoteHeadsMu.Unlock()
	return checked.head != "" && checked.installed == installed && !sameCommit(checked.head, installed)
}

// sameCommit compares commit ids the way git reads them: case-insensitively,
// with an abbreviated id (a catalog's sha pin may be one) naming the commit it
// prefixes.
func sameCommit(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	return a != "" && b != "" && (strings.HasPrefix(a, b) || strings.HasPrefix(b, a))
}
