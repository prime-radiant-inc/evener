package hub

import (
	"context"
	"fmt"
	"sync"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/execsupport/componentlog"
	"primeradiant.com/evener/internal/plugins"
)

var (
	hubRosterWatch = func(ctx context.Context, roster *hubcore.Roster) error { return roster.Watch(ctx) }
	hubTicker      = func(d time.Duration) (<-chan time.Time, func()) {
		t := time.NewTicker(d)
		return t.C, t.Stop
	}
	hubSeedDefaults = func(ctx context.Context, mgr *plugins.Manager) error {
		_, err := mgr.SeedDefaultMarketplaces(ctx)
		return err
	}
	hubPluginGC           = func(ctx context.Context, mgr *plugins.Manager) ([]string, error) { return mgr.Gc(ctx) }
	hubStartUpgradeDaemon = func(ctx context.Context, mgr *plugins.Manager, interval time.Duration) {
		startPluginAutoUpgradeDaemon(ctx, mgr, interval)
	}
	// Every background path below runs on web.cfg.PluginManager — the one wired
	// Manager the server's plugin surfaces also use — so the store they
	// maintain is the store the server serves (#1780). A Manager minted here
	// over plugins.DefaultRoot() would be a different root whenever the caller
	// pointed cfg.PluginRoot inside a sandbox/test temp root.
	hubStartUpgrade = func(ctx context.Context, cfg Config, web *WebServer) {
		mgr := web.cfg.PluginManager
		hubStartUpgradeDaemon(ctx, mgr, cfg.PluginAutoUpgradeInterval)
	}
)

func refreshPastOnStatus(past *hubcore.PastIndex) func(string) {
	return func(sessionID string) { past.RefreshOne(sessionID) }
}

func watchHubRoster(ctx context.Context, roster *hubcore.Roster) {
	if ctx.Err() != nil {
		return
	}
	log := componentlog.New("hub")
	if err := hubRosterWatch(ctx, roster); err != nil && ctx.Err() == nil {
		log.Info(fmt.Sprintf("roster watch: %v", err))
	}
}

// runHubAttention derives each live session's attention level and republishes
// the Manager roster from the same snapshot, then lets the attention watcher
// announce any transition. It is one derivation, so every caller that serves a
// wake — the shared snapshot wake, or the attention-only loop a test drives —
// publishes the identical snapshot.
func runHubAttention(ctx context.Context, web *WebServer, archive *hubcore.ArchiveStore, w *hubcore.AttentionWatcher) {
	decisions, _ := archive.Decisions()
	// Derive from the same remote-inclusive metas/live the navigation tree
	// uses: sessions on other hosts reach the hub only through the
	// remote-thread cache, so reading the roster alone left their
	// question/approval out of the broadcast (#2529).
	snapshot := web.navigationSnapshot(ctx)
	m, sum := hubDeriveNavigationAttention(snapshot.metas, snapshot.live, decisions)
	// Archiving clears human attention, not the Manager's live population.
	// Reuse top-level eligibility without an archive suppression decision.
	roster, _ := hubcore.DeriveAttention(snapshot.metas, snapshot.live, nil)
	web.navigation.roster.Publish(roster, rosterIdentities(snapshot))
	w.Tick(m, sum)
}

// rosterIdentities retains the workspace ref across thread/clear, when the
// live session ID changes but the caller's qualified workspace ref does not.
func rosterIdentities(snapshot navigationSnapshot) func(string) (rosterIdentity, bool) {
	identities := make(map[string]rosterIdentity, len(snapshot.live))
	for _, live := range snapshot.live {
		if live.SessionID == "" {
			continue
		}
		var ref string
		if live.SourceID == "" || live.SourceID == appwire.LocalSourceID {
			// The local source projects the probe's current ID as its thread ID.
			ref = appsource.LocalDaemonWorkspaceRef(appwire.LocalSourceID, live.Entry, live.SessionID)
		} else if live.ThreadID != "" {
			ref = (appwire.Ref{SourceID: live.SourceID, ThreadID: live.ThreadID}).String()
		}
		if ref != "" {
			instanceID := live.RosterInstanceID
			if instanceID == "" {
				instanceID = live.SessionID
			}
			identities[live.SessionID] = rosterIdentity{Ref: ref, InstanceID: instanceID}
		}
	}
	return func(id string) (rosterIdentity, bool) { identity, ok := identities[id]; return identity, ok }
}

// refreshHubMessageSearch keeps the message search index (S14) in step with
// the transcripts the past index lists: once now, then every interval (the
// past index's own rebuild interval). A refresh reads again only the
// transcripts that changed.
func refreshHubMessageSearch(ctx context.Context, index *hubcore.MessageSearch, past *hubcore.PastIndex, interval time.Duration) {
	if index == nil || ctx.Err() != nil {
		return
	}
	log := componentlog.New("hub")
	ticks, stop := hubTicker(interval)
	defer stop()
	refresh := func() {
		failures, err := index.Refresh(ctx, messageSearchSessions(past.All()))
		for _, failure := range failures {
			log.Info(fmt.Sprintf("message search: session %s: %v", failure.SessionID, failure.Err))
		}
		if err != nil && ctx.Err() == nil {
			log.Info(fmt.Sprintf("message search: %v", err))
		}
	}
	refresh()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			refresh()
		}
	}
}

// messageSearchSessions is every session the past index lists, with its
// transcript.
func messageSearchSessions(entries []hubcore.PastEntry) []hubcore.MessageSearchSession {
	sessions := make([]hubcore.MessageSearchSession, 0, len(entries))
	for _, entry := range entries {
		sessions = append(sessions, hubcore.MessageSearchSession{ID: entry.Meta.ID, TranscriptPath: pastTranscriptPath(entry)})
	}
	return sessions
}

// startHubSnapshotWatchers starts the hub's periodic snapshot watchers on the
// one wake that serves all of them (#6422).
func startHubSnapshotWatchers(start func(func()), ctx context.Context, poke <-chan struct{}, archive *hubcore.ArchiveStore, web *WebServer) {
	if ctx.Err() != nil {
		return
	}
	start(func() { watchHubSnapshotWake(ctx, poke, archive, web) })
}

// watchHubSnapshotWake is the hub's one periodic snapshot wake. Before it,
// attention (which republishes the Manager roster) and notices each ran their
// own 5 s ticker, so an idle hub paid two independent derivations of the same
// state, and the notices' credential listing rebuilt the whole instance listing
// for every curated provider on every one of its ticks (#6422).
//
// Both halves keep the cadence they had. The attention roster and
// evener/attention/changed are re-derived on each tick and on each poke; the
// notices are re-derived on the tick only. A poke is not a reason to re-read
// providers.toml under credMu, run the credential listing and list plugins:
// pokes fire on every roster change, remote-cache change and archive, favorite
// or pin decision, so pulsing notices on them would turn a fixed 5 s read into
// one read per event on a busy hub, which is more work than #6422 removed.
//
// The notices half runs on its own goroutine because its watcher owns the
// announced-set baseline it diffs against and does its own credential read;
// keeping that baseline single-owner is what makes the announcements
// well-ordered. Its wake is a coalesced pulse: a pulse that arrives while the
// previous derive still runs is dropped, which loses nothing, because the next
// tick re-derives from current state and announces whatever moved. The watcher
// is waited for before this returns, so a derive in flight cannot outlive the
// server and stores its callers close.
func watchHubSnapshotWake(ctx context.Context, poke <-chan struct{}, archive *hubcore.ArchiveStore, web *WebServer) {
	if ctx.Err() != nil {
		return
	}
	w := hubcore.NewAttentionWatcher(func(p appwire.AttentionChangedPayload) {
		web.appRPC.BroadcastAll(appwire.NotifyEvenerAttentionChanged, p)
	})
	ticks, stop := hubTicker(snapshotInterval)
	defer stop()
	noticeTicks := make(chan time.Time, 1)
	var notices sync.WaitGroup
	notices.Add(1)
	go func() {
		defer notices.Done()
		web.notices.runNoticeWatcher(ctx, noticeTicks, web.notices.watchRead)
	}()
	defer notices.Wait()
	run := func() { runHubAttention(ctx, web, archive, w) }
	pulseNotices := func() {
		select {
		case noticeTicks <- time.Time{}:
		default:
		}
	}
	run()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			run()
			pulseNotices()
		case <-poke:
			run()
		}
	}
}

// seedHubMarketplaces runs before the hub ever calls ListenAndServe (main.go
// binds hubListener but does not Serve it until well after this call), so no
// client could be connected when it runs — but it runs on web.cfg.PluginManager
// regardless: that Manager is wired to web's server, and a write no caller has
// to remember to pair with a broadcast is the point of OnStoreChanged (#1634),
// which only holds if nothing gets to stay a documented exception.
func seedHubMarketplaces(ctx context.Context, web *WebServer) {
	mgr := web.cfg.PluginManager
	if err := hubSeedDefaults(ctx, mgr); err != nil {
		hubLogFor(web.cfg).Info(fmt.Sprintf("warning: seeding default marketplaces: %v", err))
	}
}

func startHubPluginMaintenance(ctx context.Context, cfg Config, web *WebServer, startBackground func(func())) {
	if cfg.PluginAutoUpgrade {
		startBackground(func() { hubStartUpgrade(ctx, cfg, web) })
	}
	// Also runs before ListenAndServe (see seedHubMarketplaces), on the same
	// shared Manager for the same reason: Gc's own lockStore acquisition can run
	// migrateMarketplaceNames, which writes both store files finishing a
	// rename an earlier run left half done.
	gcMgr := web.cfg.PluginManager
	log := hubLogFor(web.cfg)
	if removed, err := hubPluginGC(ctx, gcMgr); err != nil {
		log.Info(fmt.Sprintf("plugin gc: %v", err))
	} else if len(removed) > 0 {
		log.Info(fmt.Sprintf("plugin gc: removed %d leftover marketplace clone(s)", len(removed)))
	}
}

// refreshHubRemoteThreads refreshes the web server's remote-thread cache on a
// ~30s ticker + poke. Capture and publish both go through web's own
// RemoteThreadCache — the walk reads its per-source generations at read time
// and the publish compares them under the publish lock — so the two can never
// disagree about which cache owns a registration.
func refreshHubRemoteThreads(ctx context.Context, poke <-chan struct{}, web *WebServer) {
	if ctx.Err() != nil {
		return
	}
	ticks, stop := hubTicker(30 * time.Second)
	defer stop()
	refresh := func() {
		refreshCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		// The walk captures each source's identity generation immediately
		// before it reads the source (refreshRemoteThreadSnapshot) and hands
		// the read-time capture to the publish, which compares it under the
		// publish lock: a source removed after its read — or removed and
		// re-added, which registers a strictly newer generation (the round-10
		// finding: the first registration's rows must not publish under the
		// re-added identity) — mismatches, while a host added mid-walk is
		// captured under the registration that owns its rows and still
		// appears on this tick.
		snapshot := web.refreshRemoteThreadSnapshot(refreshCtx)
		web.cfg.RemoteThreadCache.StoreWalkSnapshot(hubcore.RemoteThreadSnapshot{
			Threads:  snapshot.threads,
			Complete: snapshot.complete,
			Sources:  snapshot.sources,
		}, snapshot.sourceGenerations)
	}
	refresh()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			refresh()
		case <-poke:
			refresh()
		}
	}
}
