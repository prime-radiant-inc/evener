package plugins

import (
	"context"
	"errors"
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

// updateCheckDeadline bounds a whole check, however many remotes hang. It
// stays under the clients' PLUGIN_UPDATE_CHECK_TIMEOUT_MS (in
// appwire-client/typescript/state/extensions/plugins.ts) with room for git's
// WaitDelay after the cut-off and the listing that follows, so a client gets
// the answer instead of giving up on a check the hub is still running. A
// variable so tests can shorten it.
var updateCheckDeadline = 100 * time.Second

var errUpdateCheckDeadline = errors.New("not answered within the update check's overall time limit")

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
// call. A relative source is answered from the refreshed marketplace clone,
// also without a network call, and a directory source is never asked.
// A remote or catalog that cannot be read, or a remote still unanswered at
// updateCheckDeadline, is warned about and flags nothing. A cancelled check
// returns ctx's error and keeps the previous answers. Of
// overlapping checks only the newest publishes, and a marketplace write
// retires every answer (forgetChecks).
func (m *Manager) CheckUpdates(ctx context.Context) error {
	// The deadline covers the store lock wait as well as the remotes, so a
	// pending migration's lock cannot push the check past it.
	checkCtx, cancel := context.WithTimeoutCause(ctx, updateCheckDeadline, errUpdateCheckDeadline)
	defer cancel()
	mk, err := m.loadMigratedMarketplaces(checkCtx, installAcquireLock)
	if err != nil {
		return err
	}
	// Begun after the load, whose migration may itself write the
	// marketplaces and so retire any check already begun.
	gen := m.beginCheck()
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
		if marketplace == "" {
			// A registry key with no '@' names no marketplace, so no catalog
			// says where this plugin comes from.
			catalogWarnings = append(catalogWarnings, fmt.Sprintf("plugin %q is installed with no marketplace; not checking it for updates", plugin))
			continue
		}
		src, ok := m.upgradeSource(mk, catalogs, &catalogWarnings, marketplace, plugin)
		var lookup func() (string, error)
		switch ref := mk[marketplace]; {
		case !ok || usedInPlace(src, ref):
			continue
		case src.Rel:
			// A plugin in its marketplace's own repo is checked against the
			// refreshed clone, without a network call: Upgrade copies its
			// folder again when the clone's tree at that folder changed.
			root := m.catalogRoot(ref)
			lookup = func() (string, error) { return sourcePathTree(checkCtx, root, src.Path) }
		case gitRemoteURL(src) != "":
			lookup = func() (string, error) { return remoteHead(checkCtx, src) }
		default:
			continue
		}
		g.Go(func() error {
			head, err := lookup()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				// Past the deadline git's own error (a kill, or a git that
				// never started) says less than the deadline does.
				if cause := context.Cause(checkCtx); errors.Is(cause, errUpdateCheckDeadline) {
					err = cause
				}
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
	m.publishCheck(gen, heads)
	return nil
}

// upgradeSource is plugin's source in marketplace's local catalog, parsing each
// catalog once into catalogs. ok is false when the catalog cannot be read or
// no longer lists the plugin, where an Upgrade would fail too.
func (m *Manager) upgradeSource(mk Marketplaces, catalogs map[string]Catalog, warnings *[]string, marketplace, plugin string) (Source, bool) {
	cat, parsed := catalogs[marketplace]
	if !parsed {
		ref, known := mk[marketplace]
		switch {
		case !known:
			// Installed from a marketplace no longer registered: there is no
			// catalog to ask, and Upgrade would fail the same way.
			*warnings = append(*warnings, fmt.Sprintf("plugins from marketplace %q are installed but it is not registered; not checking them for updates", marketplace))
		case ref.InstallLocation == "":
			// A seeded or re-keyed marketplace can be recorded before its
			// clone exists; its empty InstallLocation would read a relative
			// path.
		default:
			var err error
			if cat, err = ParseCatalog(m.catalogRoot(ref)); err != nil {
				*warnings = append(*warnings, fmt.Sprintf("reading marketplace.json for %s: %v", marketplace, err))
			}
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

// beginCheck starts a check generation. Only the newest generation may
// publish, so a slow check that finishes after a newer one cannot replace its
// answers, and a marketplace change (forgetChecks) retires a check already
// reading the catalog it replaced.
func (m *Manager) beginCheck() uint64 {
	m.remoteHeadsMu.Lock()
	defer m.remoteHeadsMu.Unlock()
	m.checkGeneration++
	return m.checkGeneration
}

// publishCheck stores heads as the current answers if gen is still the newest
// check generation, and drops them otherwise.
func (m *Manager) publishCheck(gen uint64, heads map[string]checkedHead) {
	m.remoteHeadsMu.Lock()
	defer m.remoteHeadsMu.Unlock()
	if gen == m.checkGeneration {
		m.remoteHeads = heads
	}
}

// forgetChecks drops every answer and retires any check in flight. A
// marketplace write can change a catalog's source, ref or pin, after which an
// answer checked against the old catalog no longer says what Upgrade would do.
func (m *Manager) forgetChecks() {
	m.remoteHeadsMu.Lock()
	defer m.remoteHeadsMu.Unlock()
	m.checkGeneration++
	m.remoteHeads = nil
}
