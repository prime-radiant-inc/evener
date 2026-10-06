package plugins

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
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

// updateCheckRefreshBudget bounds the marketplace refreshes that open a check
// (refreshForCheck), so slow marketplaces leave the remote checks most of
// updateCheckDeadline. A variable so tests can shorten it.
var updateCheckRefreshBudget = 40 * time.Second

var errUpdateCheckRefreshBudget = errors.New("not refreshed within the update check's time for refreshing marketplaces")

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
// call. A relative source is answered from the marketplace's local clone,
// also without a network call; a directory source is never asked. Every
// fetched marketplace is refreshed first (refreshForCheck), so the catalogs
// the check reads, and the clones relative sources are answered from, are
// current; one that cannot be refreshed is warned about and checked as it
// stands.
// A remote or catalog that cannot be read, or a remote still unanswered at
// updateCheckDeadline, is warned about and flags nothing. A cancelled check
// returns ctx's error and keeps the previous answers, unless its refresh
// changed a marketplace, which retires them as any marketplace write does. Of
// overlapping checks only the newest publishes, and a marketplace write
// retires every answer (forgetChecks).
func (m *Manager) CheckUpdates(ctx context.Context) error {
	// The deadline covers the store lock wait as well as the remotes, so a
	// pending migration's lock cannot push the check past it.
	checkCtx, cancel := context.WithTimeoutCause(ctx, updateCheckDeadline, errUpdateCheckDeadline)
	defer cancel()
	refreshWarnings := m.refreshForCheck(checkCtx)
	if err := ctx.Err(); err != nil {
		return err
	}
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
	warnings := slices.Concat(refreshWarnings, catalogWarnings, remoteWarnings)
	slices.Sort(warnings)
	for _, w := range warnings {
		_, _ = fmt.Fprintf(m.stderr(), "warning: %s\n", w)
	}
	m.publishCheck(gen, heads)
	return nil
}

// refreshForCheck refreshes every fetched git marketplace before a check and
// answers a warning for each it could not refresh; a refresh failure never
// fails the check. The refreshes run one at a time, so the budget ends at
// one clear place for the next check to resume from. Each gets
// updateCheckTimeout, all of them together updateCheckRefreshBudget, and a
// check whose budget runs out starts the next check's refreshes at the first
// marketplace it cut off or left, so slow marketplaces cannot starve the ones
// after them on every check. A directory marketplace is read in place, a never-fetched
// one is left to an explicit refresh, and one pinned to a tag or a commit
// (its clone has a detached HEAD) cannot be fast-forwarded, so none of those
// is refreshed.
func (m *Manager) refreshForCheck(ctx context.Context) []string {
	mk, err := m.ListMarketplaces(ctx)
	if err != nil {
		return []string{fmt.Sprintf("listing marketplaces to refresh: %v", err)}
	}
	budgetCtx, cancel := context.WithTimeoutCause(ctx, updateCheckRefreshBudget, errUpdateCheckRefreshBudget)
	defer cancel()
	names := slices.Sorted(maps.Keys(mk))
	m.remoteHeadsMu.Lock()
	resumeAt := m.checkRefreshResumeAt
	m.remoteHeadsMu.Unlock()
	start, _ := slices.BinarySearch(names, resumeAt)
	names = slices.Concat(names[start:], names[:start])
	var warnings []string
	firstLeft, started := "", 0
	for _, name := range names {
		ref := mk[name]
		if ref.Source.Kind == SourceDirectory || ref.InstallLocation == "" || !cloneOnBranch(ref.InstallLocation) {
			continue
		}
		// Once the budget, the deadline or the caller has ended the phase,
		// no further refresh starts; each one left is warned about.
		if cause := context.Cause(budgetCtx); cause != nil {
			if firstLeft == "" {
				firstLeft = name
			}
			warnings = append(warnings, fmt.Sprintf("refreshing marketplace %q before checking for updates: %v", name, cause))
			continue
		}
		started++
		refreshCtx, cancelRefresh := context.WithTimeout(budgetCtx, updateCheckTimeout)
		err := m.fastForwardMarketplace(refreshCtx, name, ref.InstallLocation)
		// A refresh cut off by a limit is warned about by that limit, which
		// says more than the error of the git it killed; any other failure
		// keeps its own error.
		if err != nil && refreshCtx.Err() != nil {
			if cause := context.Cause(budgetCtx); cause != nil {
				err = cause
				// Cut off by the phase's end, it goes first next time, unless
				// it began the phase: it had the whole budget and would only
				// take it again.
				if started > 1 && firstLeft == "" {
					firstLeft = name
				}
			}
		}
		cancelRefresh()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("refreshing marketplace %q before checking for updates: %v", name, err))
		}
	}
	m.remoteHeadsMu.Lock()
	m.checkRefreshResumeAt = firstLeft
	m.remoteHeadsMu.Unlock()
	return warnings
}

// fastForwardMarketplace fetches the clone at dir with the store lock free,
// so a plugin operation started meanwhile does not wait behind the network,
// then takes the lock to fast-forward it and stamp its LastUpdated
// (stampRefreshed: one that moves nothing reports no store change). A fetch
// writes only under .git, so nothing reading the clone's files sees it, and
// it holds the clone's own lock (lockClone), as the fast-forward does too,
// against other git work in the clone and its removal or move. A blobless
// git-subdir clone still downloads the changed files' contents as it
// fast-forwards, under the store lock, bounded by the refresh's timeout.
// Unlike RefreshMarketplace, a failure is not repaired by recloning, which
// would hold the lock across a download; an explicit refresh does that.
func (m *Manager) fastForwardMarketplace(ctx context.Context, name, dir string) error {
	if err := m.withClone(ctx, dir, func() error { return marketplaceGitFetch(ctx, dir) }); err != nil {
		return err
	}
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
	if !ok || ref.InstallLocation != dir || !cloneOnBranch(dir) {
		// Removed, moved or re-sourced to a pin while fetching: there is
		// nothing to fast-forward.
		return nil
	}
	releaseClone, err := m.lockClone(ctx, dir)
	if err != nil {
		return err
	}
	defer releaseClone()
	before, err := marketplaceGitHeadSHA(ctx, dir)
	if err != nil {
		return err
	}
	if err := marketplaceGitFastForward(ctx, dir); err != nil {
		return err
	}
	after, err := marketplaceGitHeadSHA(ctx, dir)
	if err != nil {
		return err
	}
	return m.stampRefreshed(mk, name, ref, after != before)
}

// cloneOnBranch reports whether the clone at dir has a branch checked out,
// reading its HEAD file rather than running git, so it answers after the
// check's budget has ended too. A clone that cannot be read counts as on a
// branch, so the refresh tries it and warns of what fails.
func cloneOnBranch(dir string) bool {
	head, err := os.ReadFile(filepath.Join(dir, ".git", "HEAD"))
	return err != nil || strings.HasPrefix(string(head), "ref: ")
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
