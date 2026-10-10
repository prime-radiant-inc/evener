package hub

import (
	"context"
	"slices"
	"sort"
	"sync"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

// noticeInterval is how often the hub re-derives its notices to announce a
// change it did not cause: an access token that expired with no refresh token,
// a refresh the issuer rejected, a plugin directory that went missing.
const noticeInterval = 5 * time.Second

// hubNotices derives the Board's notices (S11, spec 7.1): hub-level problems
// that block sessions. It reads the answers the phone's fallback reads, so a
// notice and the screen its action opens agree: the evener/auth/list rows, the
// navigation manifest's sources, and the evener/plugin/list rows.
type hubNotices struct {
	refreshProviders func() bool
	auth             func() (appwire.AuthListResponse, error)
	plugins          func(context.Context) (appwire.PluginListResponse, error)
	sources          *appsource.Registry
	roster           *hubcore.Roster
	remote           *hubcore.RemoteThreadCache

	// mu guards last, the notices read last answered with.
	mu   sync.Mutex
	last []appwire.HubNotice
}

// read is the hub's notices now, as both evener/notices/list and the watcher
// report them. A kind whose read failed keeps the notices the last read gave
// for it: a failed read is no news, so neither a client that asks during a
// transient failure nor the watcher is told a problem resolved when it was
// only unreadable.
//
// The lock spans the whole derive-and-merge sequence, not just the merge: the
// RPC handler and the watcher goroutine can call read concurrently, and
// derive does real I/O (the auth and plugin reads), so a narrower lock would
// let a slower, staler derive finish after a faster, newer one and overwrite
// it in last.
func (n *hubNotices) read(ctx context.Context) []appwire.HubNotice {
	n.mu.Lock()
	defer n.mu.Unlock()
	next, failed := n.derive(ctx)
	n.last = keepFailedNotices(next, n.last, failed)
	return n.last
}

// derive returns the notices the hub can read now, sign-ins first, then hosts,
// then plugins. A kind whose read failed is named in failed, so read can keep
// that kind's last notices instead of reporting them resolved.
func (n *hubNotices) derive(ctx context.Context) (notices []appwire.HubNotice, failed map[string]bool) {
	notices = []appwire.HubNotice{}
	failed = map[string]bool{}
	signIns, err := n.signInNotices()
	if err != nil {
		failed[appwire.NoticeKindSignInRequired] = true
	}
	notices = append(notices, signIns...)
	notices = append(notices, n.hostNotices()...)
	plugins, err := n.pluginNotices(ctx)
	if err != nil {
		failed[appwire.NoticeKindPluginBroken] = true
	}
	return append(notices, plugins...), failed
}

// signInNotices names every provider instance whose evener/auth/list row says
// it needs signing in again, with the live sessions whose current model runs
// on it.
func (n *hubNotices) signInNotices() ([]appwire.HubNotice, error) {
	if n.auth == nil {
		return nil, nil
	}
	list, err := n.auth()
	if err != nil {
		return nil, err
	}
	var sessions map[string]int
	var notices []appwire.HubNotice
	for _, provider := range list.Providers {
		if !provider.NeedsLogin {
			continue
		}
		if sessions == nil {
			sessions = n.liveSessionsByProfile()
		}
		notices = append(notices, appwire.HubNotice{
			ID:               appwire.NoticeKindSignInRequired + ":" + provider.Provider,
			Kind:             appwire.NoticeKindSignInRequired,
			Subject:          provider.Provider,
			AffectedSessions: sessions[provider.Provider],
		})
	}
	sort.Slice(notices, func(i, j int) bool { return notices[i].Subject < notices[j].Subject })
	return notices, nil
}

// liveSessionsByProfile counts this hub's live top-level sessions by the
// provider instance their current model runs on. A crashed session's process
// is gone, so it runs on nothing.
func (n *hubNotices) liveSessionsByProfile() map[string]int {
	counts := map[string]int{}
	if n.roster == nil {
		return counts
	}
	for _, entry := range n.roster.List() {
		if entry.Crashed || entry.SessionID == "" || entry.Profile == "" {
			continue
		}
		counts[entry.Profile]++
	}
	return counts
}

// hostNotices names every registered host that is offline, as the navigation
// manifest's sources report it, with its sessions that were live when the hub
// last reached it.
func (n *hubNotices) hostNotices() []appwire.HubNotice {
	if n.sources == nil {
		return nil
	}
	var sessions map[string]int
	var notices []appwire.HubNotice
	for _, source := range n.sources.All() {
		if source.ID() == "local" || sourceIsOnline(source) {
			continue
		}
		if sessions == nil {
			sessions = n.lastSeenLiveSessionsBySource()
		}
		notices = append(notices, appwire.HubNotice{
			ID:               appwire.NoticeKindHostOffline + ":" + source.ID(),
			Kind:             appwire.NoticeKindHostOffline,
			Subject:          source.ID(),
			AffectedSessions: sessions[source.ID()],
		})
	}
	return notices
}

// lastSeenLiveSessionsBySource counts each remote source's top-level rows that
// were live in the hub's last remote snapshot: an offline host keeps its
// last-known rows there.
func (n *hubNotices) lastSeenLiveSessionsBySource() map[string]int {
	counts := map[string]int{}
	if n.remote == nil {
		return counts
	}
	for _, thread := range n.remote.Snapshot().Threads {
		if thread.Evener.Kind == "subagent" || !appThreadTreeLive(thread) {
			continue
		}
		counts[thread.Source]++
	}
	return counts
}

// pluginNotices names every installed plugin evener/plugin/list reports
// broken, in that list's order (plugin, then marketplace).
func (n *hubNotices) pluginNotices(ctx context.Context) ([]appwire.HubNotice, error) {
	if n.plugins == nil {
		return nil, nil
	}
	list, err := n.plugins(ctx)
	if err != nil {
		return nil, err
	}
	var notices []appwire.HubNotice
	for _, plugin := range list.Plugins {
		if !plugin.Broken {
			continue
		}
		notices = append(notices, appwire.HubNotice{
			ID:          appwire.NoticeKindPluginBroken + ":" + plugin.Plugin + "@" + plugin.Marketplace,
			Kind:        appwire.NoticeKindPluginBroken,
			Subject:     plugin.Plugin,
			Marketplace: plugin.Marketplace,
		})
	}
	return notices, nil
}

func registerNoticesHandler(server *appserver.Server, notices *hubNotices) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerNoticesList, func(ctx context.Context, _ appwire.EmptyParams) (appwire.NoticesListResponse, error) {
		return appwire.NoticesListResponse{Notices: notices.read(ctx)}, nil
	})
}

// runNoticeWatcher announces every change in the hub's notices: on each tick it
// reads them again and, when they differ from the last announced set,
// broadcasts evener/notices/changed with the new list. When the sign-in
// notices change it first broadcasts the no-data evener/auth/updated, so a
// providers pane refreshes a sign-in whose state changed with no write through
// this hub.
func runNoticeWatcher(ctx context.Context, ticks <-chan time.Time, read func(context.Context) []appwire.HubNotice, broadcaster hostNotificationBroadcaster) {
	announced := read(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			next := read(ctx)
			if slices.Equal(next, announced) {
				continue
			}
			if !slices.Equal(signInSubjects(next), signInSubjects(announced)) {
				broadcaster.BroadcastAll(appwire.NotifyEvenerAuthUpdated, appwire.EvenerAuthUpdatedParams{})
			}
			announced = next
			broadcaster.BroadcastAll(appwire.NotifyEvenerNoticesChanged, appwire.NoticesListResponse{Notices: next})
		}
	}
}

// keepFailedNotices puts back, for each kind whose read failed, the notices of
// that kind the last announcement carried, in the derivation's kind order.
func keepFailedNotices(next, last []appwire.HubNotice, failed map[string]bool) []appwire.HubNotice {
	if len(failed) == 0 {
		return next
	}
	merged := []appwire.HubNotice{}
	for _, kind := range []string{appwire.NoticeKindSignInRequired, appwire.NoticeKindHostOffline, appwire.NoticeKindPluginBroken} {
		from := next
		if failed[kind] {
			from = last
		}
		for _, notice := range from {
			if notice.Kind == kind {
				merged = append(merged, notice)
			}
		}
	}
	return merged
}

// signInSubjects is the provider instances the sign-in notices name.
func signInSubjects(notices []appwire.HubNotice) []string {
	var subjects []string
	for _, notice := range notices {
		if notice.Kind == appwire.NoticeKindSignInRequired {
			subjects = append(subjects, notice.Subject)
		}
	}
	return subjects
}

// watchRead observes user provider edits on the existing server-lifetime notice
// watcher, including while no browser is subscribed or the file cannot load.
func (n *hubNotices) watchRead(ctx context.Context, broadcaster hostNotificationBroadcaster) []appwire.HubNotice {
	if n.refreshProviders != nil && n.refreshProviders() {
		broadcaster.BroadcastAll(appwire.NotifyEvenerAuthUpdated, appwire.EvenerAuthUpdatedParams{})
	}
	return n.read(ctx)
}
