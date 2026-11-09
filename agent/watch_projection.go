package agent

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

func watchResultFromConfig(cfg *watchConfig, replacedExisting bool) watchResult {
	var send *watchSendArgs
	if !cfg.stableReceiver {
		send = cloneWatchSendArgs(cfg.send)
	}
	progressIntervalMS := cfg.progressIntervalMS
	if cfg.timer {
		progressIntervalMS = 0
	}
	return watchResult{
		WatchID:            cfg.watchID,
		Source:             cfg.sourcePublic,
		Target:             cfg.target,
		Watching:           true,
		OutputMatch:        cfg.outputMatch,
		Events:             append([]string(nil), cfg.events...),
		EventFilter:        cloneWatchEventFilter(cfg.eventFilter),
		ProgressIntervalMS: progressIntervalMS,
		TimerSeconds:       cfg.timerSeconds,
		OneShot:            cfg.oneShot,
		Note:               cfg.note,
		Send:               send,
		ReplacedExisting:   replacedExisting,
	}
}

func watchSendTo(cfg *watchConfig) string {
	if cfg == nil || cfg.send == nil {
		return ""
	}
	return cfg.send.To
}

// recentWatchSummaries projects the watch history ring for job_list, latest
// first. The ring walk runs under jm.mu; the formatting it feeds runs outside
// it, so job_list's read does not hold the lock the delivery path needs for
// work that reads no shared state.
func (jm *jobManager) recentWatchSummaries() []recentWatchEntry {
	return formatRecentWatchSummaries(jm.visibleWatchHistorySnapshots(jm.sessionID))
}

// visibleWatchHistorySnapshots is the history ring's locked collector for the
// session-facing projection: it gathers the entries visible to sessionID, latest
// first, and returns with jm.mu released.
func (jm *jobManager) visibleWatchHistorySnapshots(sessionID string) []watchHistoryEntry {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return watchHistorySnapshotsLocked(jm.watchHistory, func(h watchHistoryEntry) bool {
		return watchHistoryVisibleToSession(h, sessionID)
	})
}

// watchHistorySnapshotsLocked copies the ring entries satisfying visibleHistory,
// latest first. The caller holds jm.mu. A watchHistoryEntry holds only value
// fields (strings, ints, and a time.Time), so the value copy is already private
// and needs no ring copy of its own.
func watchHistorySnapshotsLocked(history []watchHistoryEntry, visibleHistory func(watchHistoryEntry) bool) []watchHistoryEntry {
	out := make([]watchHistoryEntry, 0, len(history))
	for i := range slices.Backward(history) {
		h := history[i]
		if !visibleHistory(h) {
			continue
		}
		out = append(out, h)
	}
	return out
}

// formatRecentWatchSummaries projects the supplied history entries into
// job_list's recent-watch rows, in the order they were collected (latest
// first). It reads only entry fields, so it takes no lock.
func formatRecentWatchSummaries(history []watchHistoryEntry) []recentWatchEntry {
	out := make([]recentWatchEntry, 0, len(history))
	for _, h := range history {
		out = append(out, recentWatchEntry{
			ID:         h.id,
			Source:     watchPublicSource(h.source, h.target),
			Condition:  h.condition,
			Deliveries: h.deliveries,
			EndReason:  h.endReason,
			EndedAt:    h.endedAt.Format(time.RFC3339Nano),
		})
	}
	return out
}

// pendingInspectWatch is one config snapshotted for the watch-list projection,
// plus whether it came from the detached (terminalFlush) set, so the formatter
// can pick the live or detached inspect row after the lock is released.
type pendingInspectWatch struct {
	cfg      watchConfig
	detached bool
}

// watchInspectSnapshots gathers, under jm.mu, value copies of every config the
// visibility predicate accepts -- the live jm.watches entries, then the
// terminalFlush (detached) entries whose id an active watch does not already
// carry -- plus the visible history-ring entries, latest first. Both the map and
// ring walks and their predicates run inside the lock; the formatting and
// ordering that consume the copies run outside it. The walk order is preserved,
// so the list projections append the same rows in the same order as before.
func (jm *jobManager) watchInspectSnapshots(visible func(*watchConfig) bool, visibleHistory func(watchHistoryEntry) bool) ([]pendingInspectWatch, []watchHistoryEntry) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	pending := make([]pendingInspectWatch, 0, len(jm.watches))
	activeWatchIDs := make(map[string]bool, len(jm.watches))
	for _, cfg := range jm.watches {
		if !visible(cfg) {
			continue
		}
		pending = append(pending, pendingInspectWatch{cfg: snapshotWatchConfigForProjection(cfg)})
		if cfg.watchID != "" {
			activeWatchIDs[cfg.watchID] = true
		}
	}
	for cfg := range jm.terminalFlush {
		if cfg == nil || cfg.watchID == "" || activeWatchIDs[cfg.watchID] {
			continue
		}
		if !visible(cfg) {
			continue
		}
		pending = append(pending, pendingInspectWatch{cfg: snapshotWatchConfigForProjection(cfg), detached: true})
	}
	history := watchHistorySnapshotsLocked(jm.watchHistory, visibleHistory)
	return pending, history
}

// formatWatchListInspectResult projects the snapshotted configs and history
// entries into job_list's inspect rows: the watch set ordered by (source, id),
// then the recent ring in the collected (latest-first) order. It reads only the
// copies the collector handed it, so it takes no lock.
func formatWatchListInspectResult(pending []pendingInspectWatch, history []watchHistoryEntry) jobWatchListToolResult {
	watches := make([]jobWatchInspectToolResult, 0, len(pending))
	for i := range pending {
		if pending[i].detached {
			watches = append(watches, inspectResultFromDetachedWatchConfig(&pending[i].cfg))
			continue
		}
		watches = append(watches, inspectResultFromWatchConfig(&pending[i].cfg))
	}
	sort.SliceStable(watches, func(i, j int) bool {
		if watches[i].Source != watches[j].Source {
			return watches[i].Source < watches[j].Source
		}
		return watches[i].WatchID < watches[j].WatchID
	})
	recent := make([]jobWatchInspectToolResult, 0, len(history))
	for _, h := range history {
		recent = append(recent, inspectResultFromWatchHistory(h))
	}
	return jobWatchListToolResult{Watches: watches, RecentWatches: recent, Count: len(watches)}
}

func (jm *jobManager) watchListToolResult() jobWatchListToolResult {
	pending, history := jm.watchInspectSnapshots(
		func(cfg *watchConfig) bool { return watchConfigVisibleToSession(cfg, jm.sessionID) },
		func(h watchHistoryEntry) bool { return watchHistoryVisibleToSession(h, jm.sessionID) },
	)
	return formatWatchListInspectResult(pending, history)
}

// watchListToolResultForReceiverShapes returns the receiver-keyed list rows for
// both receiver-key shapes: the delegate-keyed
// (receiverSessionID, receiverDelegateID) pair, and — when a delegate id is
// present — the session-keyed (receiverSessionID, "") pair that
// configureDescendantReceiverWatch installs. A config's receiverDelegateID is
// either empty or the delegate, never both, so the two row sets are disjoint.
// It matches both shapes in ONE snapshot pass: the history ring is walked
// latest-first, and formatting each shape separately before concatenating would
// interleave an older entry ahead of a newer one.
func (jm *jobManager) watchListToolResultForReceiverShapes(receiverSessionID, receiverDelegateID string) jobWatchListToolResult {
	receiverSessionID = strings.TrimSpace(receiverSessionID)
	receiverDelegateID = strings.TrimSpace(receiverDelegateID)
	if receiverSessionID == "" {
		return jobWatchListToolResult{}
	}
	matches := func(sessionID, delegateID string) bool {
		if sessionID != receiverSessionID {
			return false
		}
		return delegateID == receiverDelegateID || (receiverDelegateID != "" && delegateID == "")
	}
	pending, history := jm.watchInspectSnapshots(
		func(cfg *watchConfig) bool {
			return cfg != nil && matches(cfg.receiverSessionID, cfg.receiverDelegateID)
		},
		func(h watchHistoryEntry) bool {
			return matches(h.receiverSessionID, h.receiverDelegateID)
		},
	)
	return formatWatchListInspectResult(pending, history)
}

// watchInspectMatch is one inspected watch, copied under jm.mu, plus which of
// the three sources it came from so the caller can format it after the lock is
// released.
type watchInspectMatch struct {
	cfg         watchConfig
	history     watchHistoryEntry
	detached    bool
	fromHistory bool
}

// result formats the matched watch into its inspect row. It reads only the copy
// the finder handed it, so it takes no lock.
func (m watchInspectMatch) result() jobWatchInspectToolResult {
	switch {
	case m.fromHistory:
		return inspectResultFromWatchHistory(m.history)
	case m.detached:
		return inspectResultFromDetachedWatchConfig(&m.cfg)
	default:
		return inspectResultFromWatchConfig(&m.cfg)
	}
}

// findWatchInspectSnapshot locates the first watch matching watchID and the
// supplied predicates, copies it under jm.mu, and reports which kind it was so
// the caller can format it after the lock is released. Precedence is the inspect
// path's long-standing order -- live watches, then the detached terminalFlush
// set, then the history ring (latest first).
func (jm *jobManager) findWatchInspectSnapshot(watchID string, visible func(*watchConfig) bool, visibleHistory func(watchHistoryEntry) bool) (watchInspectMatch, bool) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for _, cfg := range jm.watches {
		if cfg != nil && cfg.watchID == watchID && visible(cfg) {
			return watchInspectMatch{cfg: snapshotWatchConfigForProjection(cfg)}, true
		}
	}
	for cfg := range jm.terminalFlush {
		if cfg != nil && cfg.watchID == watchID && visible(cfg) {
			return watchInspectMatch{cfg: snapshotWatchConfigForProjection(cfg), detached: true}, true
		}
	}
	for i := range slices.Backward(jm.watchHistory) {
		if jm.watchHistory[i].id == watchID && visibleHistory(jm.watchHistory[i]) {
			return watchInspectMatch{history: jm.watchHistory[i], fromHistory: true}, true
		}
	}
	return watchInspectMatch{}, false
}

func (jm *jobManager) inspectWatchByID(watchID string) jobWatchInspectToolResult {
	if match, ok := jm.findWatchInspectSnapshot(watchID,
		func(cfg *watchConfig) bool { return watchConfigVisibleToSession(cfg, jm.sessionID) },
		func(h watchHistoryEntry) bool { return watchHistoryVisibleToSession(h, jm.sessionID) },
	); ok {
		return match.result()
	}
	return jobWatchInspectToolResult{WatchID: watchID, Watching: false}
}

func (jm *jobManager) inspectReceiverWatchByID(watchID, receiverSessionID, receiverDelegateID string) (jobWatchInspectToolResult, bool) {
	receiverSessionID = strings.TrimSpace(receiverSessionID)
	receiverDelegateID = strings.TrimSpace(receiverDelegateID)
	if receiverSessionID == "" {
		return jobWatchInspectToolResult{}, false
	}
	if match, ok := jm.findWatchInspectSnapshot(watchID,
		func(cfg *watchConfig) bool {
			return watchConfigMatchesReceiver(cfg, receiverSessionID, receiverDelegateID)
		},
		func(h watchHistoryEntry) bool {
			return watchHistoryMatchesReceiver(h, receiverSessionID, receiverDelegateID)
		},
	); ok {
		return match.result(), true
	}
	return jobWatchInspectToolResult{}, false
}

// inspectReceiverWatchByIDShapes is inspectReceiverWatchByID over both
// receiver-key shapes, for the same reason
// watchListToolResultForReceiverShapes queries both.
func (jm *jobManager) inspectReceiverWatchByIDShapes(watchID, receiverSessionID, receiverDelegateID string) (jobWatchInspectToolResult, bool) {
	if inspect, ok := jm.inspectReceiverWatchByID(watchID, receiverSessionID, receiverDelegateID); ok {
		return inspect, true
	}
	if strings.TrimSpace(receiverDelegateID) == "" {
		return jobWatchInspectToolResult{}, false
	}
	return jm.inspectReceiverWatchByID(watchID, receiverSessionID, "")
}

func inspectResultFromWatchConfig(cfg *watchConfig) jobWatchInspectToolResult {
	if cfg == nil {
		return jobWatchInspectToolResult{Watching: false}
	}
	return jobWatchInspectToolResult{
		WatchID:    cfg.watchID,
		Source:     watchPublicSource(cfg.sourcePublic, cfg.target),
		Watching:   true,
		Condition:  watchConditionSummary(cfg),
		Note:       cfg.note,
		Deliveries: cfg.deliveries,
		CreatedAt:  cfg.createdAt.Format(time.RFC3339Nano),
	}
}

func inspectResultFromDetachedWatchConfig(cfg *watchConfig) jobWatchInspectToolResult {
	result := inspectResultFromWatchConfig(cfg)
	result.Watching = false
	return result
}

func inspectResultFromWatchHistory(h watchHistoryEntry) jobWatchInspectToolResult {
	return jobWatchInspectToolResult{
		WatchID:    h.id,
		Source:     watchPublicSource(h.source, h.target),
		Watching:   false,
		Condition:  h.condition,
		Note:       h.note,
		Deliveries: h.deliveries,
		EndReason:  h.endReason,
		EndedAt:    h.endedAt.Format(time.RFC3339Nano),
	}
}

func (jm *jobManager) liveWatchSummaries() []watchListEntry {
	return formatWatchSummaries(jm.visibleWatchConfigSnapshots(jm.sessionID))
}

// formatWatchSummaries projects the supplied configs into job_list's
// model-facing rows and orders them by (source, id). It reads only config
// fields, so it takes no lock; its callers pass configs snapshotted under jm.mu.
func formatWatchSummaries(cfgs []*watchConfig) []watchListEntry {
	entries := make([]watchListEntry, 0, len(cfgs))
	for _, cfg := range cfgs {
		entries = append(entries, watchListEntry{
			ID:         cfg.watchID,
			Source:     watchPublicSource(cfg.sourcePublic, cfg.target),
			Condition:  watchConditionSummary(cfg),
			Deliveries: cfg.deliveries,
			CreatedAt:  cfg.createdAt.Format(time.RFC3339Nano),
		})
	}
	sort.SliceStable(entries, watchListEntryLess(entries))
	return entries
}

// liveWatchStatuses snapshots the session's visible live watches as structured
// status rows. It walks the same jm.watches map under the same lock and reuses
// the same visibility predicate as liveWatchSummaries, so the two projections
// always agree on which watches are visible; only liveWatchSummaries feeds
// job_list's model-facing output, and this one leaves it untouched.
func (jm *jobManager) liveWatchStatuses() []WatchStatusInfo {
	return jm.liveWatchStatusesForSession(jm.sessionID)
}

// liveWatchStatusesForSession is liveWatchStatuses' session-parameterized body:
// it snapshots the configs THIS manager holds that are visible to sessionID. A
// config with no receiver key belongs to the manager that owns it, so it is
// visible only when that manager is sessionID's own; otherwise a descendant's
// keyless watch would leak onto an ancestor's projection.
//
// The map walk and its visibility predicates run under jm.mu; the formatting and
// ordering they feed run outside it, so the hub's status-polling read no longer
// holds the lock the delivery path needs for work that reads no shared state.
func (jm *jobManager) liveWatchStatusesForSession(sessionID string) []WatchStatusInfo {
	return formatWatchStatuses(jm.visibleWatchConfigSnapshots(sessionID))
}

// formatWatchStatuses projects the supplied configs into structured status rows,
// ordered by (source, id).
//
// The rows are ordered before returning. The walk above reads a map, so without
// this the order varies between two snapshots of unchanged state -- unstable
// wire output, and row churn for a consumer that rebuilds its rows from it. The
// order lives here, next to the projection, rather than at each call site: four
// callers sorted the result afterward, and the fifth published an unordered
// projection that no test could distinguish from a changed one.
//
// It reads only config fields, so it takes no lock: the per-instant RFC3339Nano
// formatting and the sort run entirely outside the walk's lock.
func formatWatchStatuses(cfgs []*watchConfig) []WatchStatusInfo {
	statuses := make([]WatchStatusInfo, 0, len(cfgs))
	for _, cfg := range cfgs {
		statuses = append(statuses, watchStatusInfoFromConfig(cfg))
	}
	sortWatchStatuses(statuses)
	return statuses
}

// watchConfigSnapshotsWhere gathers value copies of this manager's live configs
// that satisfy keep. Both the map walk and the predicate run inside jm.mu;
// everything a projection derives from the copies runs outside it. The walk
// order is preserved, so a projection that sorts stably sees the same input.
func (jm *jobManager) watchConfigSnapshotsWhere(keep func(*watchConfig) bool) []*watchConfig {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	cfgs := make([]*watchConfig, 0, len(jm.watches))
	for _, cfg := range jm.watches {
		if !keep(cfg) {
			continue
		}
		snapshot := snapshotWatchConfigForProjection(cfg)
		cfgs = append(cfgs, &snapshot)
	}
	return cfgs
}

// visibleWatchConfigSnapshots gathers value copies of this manager's live
// configs that are visible to sessionID. A config with no receiver key belongs
// to the manager that owns it, so it is visible only when that manager is
// sessionID's own; otherwise a descendant's keyless watch would leak onto an
// ancestor's projection. liveWatchSummaries always passes the manager's own
// session, for which the keyless predicate is a no-op, so one collector keeps
// the two projections' visibility sets identical.
func (jm *jobManager) visibleWatchConfigSnapshots(sessionID string) []*watchConfig {
	return jm.watchConfigSnapshotsWhere(func(cfg *watchConfig) bool {
		if !watchConfigVisibleToSession(cfg, sessionID) {
			return false
		}
		return cfg.receiverSessionID != "" || jm.sessionID == sessionID
	})
}

// snapshotWatchConfigForProjection copies a live config so a projection can
// format it after jm.mu is released. The delivery path mutates deliveries and
// the deliveryTimes ring in place under jm.mu (countWatchDeliveryLocked), so a
// projection that formatted the live pointer after unlocking would race the
// delivery path. The ring is copied because an at-cap delivery rewrites its
// backing array in place; every other field a projection reads is set once at
// install and never mutated.
func snapshotWatchConfigForProjection(cfg *watchConfig) watchConfig {
	snapshot := *cfg
	snapshot.deliveryTimes = append([]time.Time(nil), cfg.deliveryTimes...)
	return snapshot
}

func sortWatchStatuses(statuses []WatchStatusInfo) {
	sort.SliceStable(statuses, func(i, j int) bool {
		if statuses[i].Source != statuses[j].Source {
			return statuses[i].Source < statuses[j].Source
		}
		return statuses[i].ID < statuses[j].ID
	})
}

// LiveWatchRowsForSessions resolves the supplied thread IDs to the live watch
// rows that belong on each of their rows, in one pass over this session's live
// tree.
//
// It is the batch form of LiveWatchesForSession, and the thread LIST path is why
// it exists: that path knows every row ID before it samples any of them, and
// resolving them one at a time searched the descendant tree once per row and
// asked every manager for its rows once per row -- a lock and a fresh slice per
// manager per row -- so a page of N sessions against N managers paid up to N x N
// of both. A page now takes one lock per manager: each manager appends its
// watches once, keyed by the row each one belongs on, and every requested ID
// reads its rows back out of that.
//
// An entry exists only for an ID this session can answer for: the root, or a
// live descendant. A present entry with an empty non-nil slice is a real answer
// -- that row has no watches now -- exactly as the single-ID resolvers' non-nil
// empty is; an absent ID is their nil answer (an empty or unknown ID, and the
// root asked through LiveWatchesForDescendant). No IDs means no answers: nil.
func (s *Session) LiveWatchRowsForSessions(sessionIDs []string) map[string][]WatchStatusInfo {
	if s == nil || len(sessionIDs) == 0 {
		return nil
	}
	answerable := make(map[string]*Session, len(sessionIDs))
	answerable[s.ID()] = s
	for _, descendant := range s.liveDescendantSessions() {
		if descendant == nil {
			continue
		}
		answerable[descendant.ID()] = descendant
	}
	// The managers a row's watches can live in: every answerable session's own
	// manager, plus the sessions a delegate controller lets hold a receiver watch
	// for one of them -- a delegate session is not a subagent descendant, so its
	// manager would otherwise be missed. One controller serves its whole tree, so
	// its source list is asked for once, not once per row.
	rows := make(map[string][]WatchStatusInfo, len(answerable))
	seenManagers := make(map[*jobManager]struct{}, len(answerable))
	seenControllers := make(map[*delegateTreeController]struct{}, 1)
	appendManager := func(session *Session) {
		if session == nil || session.jobManager == nil {
			return
		}
		if _, ok := seenManagers[session.jobManager]; ok {
			return
		}
		seenManagers[session.jobManager] = struct{}{}
		session.jobManager.appendWatchStatusesByRow(rows)
	}
	for _, session := range answerable {
		appendManager(session)
		controller := session.delegateController
		if controller == nil {
			continue
		}
		if _, ok := seenControllers[controller]; ok {
			continue
		}
		seenControllers[controller] = struct{}{}
		for _, source := range session.stableWatchSourceSessions() {
			appendManager(source)
		}
	}
	out := make(map[string][]WatchStatusInfo, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		if _, ok := answerable[sessionID]; !ok {
			continue
		}
		sessionRows := rows[sessionID]
		if sessionRows == nil {
			sessionRows = []WatchStatusInfo{}
		}
		sortWatchStatuses(sessionRows)
		out[sessionID] = sessionRows
	}
	return out
}

// appendWatchStatusesByRow appends this manager's live watches to rows, keyed by
// the session whose row each one belongs on: its receiver's row when it has one,
// and this manager's own session's row when it does not. A keyless watch belongs
// to the session that owns the manager, which is what keeps a child's watch off
// its ancestors' rows.
//
// The map walk and its row keying run under jm.mu; the projection that formats
// each row runs outside it, so the hub's thread-list read does not hold the lock
// the delivery path needs for work that reads no shared state. The walk order is
// preserved, so each row is appended in the same order as before.
func (jm *jobManager) appendWatchStatusesByRow(rows map[string][]WatchStatusInfo) {
	type watchRow struct {
		row string
		cfg watchConfig
	}
	jm.mu.Lock()
	pending := make([]watchRow, 0, len(jm.watches))
	for _, cfg := range jm.watches {
		if cfg == nil {
			continue
		}
		row := cfg.receiverSessionID
		if row == "" {
			row = jm.sessionID
		}
		pending = append(pending, watchRow{row: row, cfg: snapshotWatchConfigForProjection(cfg)})
	}
	jm.mu.Unlock()
	for i := range pending {
		rows[pending[i].row] = append(rows[pending[i].row], watchStatusInfoFromConfig(&pending[i].cfg))
	}
}

// watchStatusInfoFromConfig projects one live config into its structured
// status row. It reads only config fields and never mutates them.
func watchStatusInfoFromConfig(cfg *watchConfig) WatchStatusInfo {
	if cfg == nil {
		return WatchStatusInfo{}
	}
	return WatchStatusInfo{
		ID:             cfg.watchID,
		Source:         watchPublicSource(cfg.sourcePublic, cfg.target),
		Target:         cfg.target,
		SendTo:         watchSendTo(cfg),
		Note:           cfg.note,
		Cadence:        watchCadencesOf(cfg),
		OutputMatch:    cfg.outputMatch,
		Events:         append([]string(nil), cfg.events...),
		WildcardEvents: cfg.wildcardEvents,
		Deliveries:     cfg.deliveries,
		DeliveryTimes:  watchDeliveryTimesOf(cfg),
		CreatedAt:      cfg.createdAt.Format(time.RFC3339Nano),
		// A one-shot that has already fired is no longer armed, even while its
		// durable teardown is still pending: the armed state and the derived next
		// fire must answer "is it still waiting" the same way.
		Active: !watchOneShotHasFired(cfg),
	}
}

// watchOneShotHasFired reports whether a one-shot watch has already fired. A
// one-shot fires once, whatever fires it, so its first delivery of ANY kind ends
// it -- and no single field marks that moment. firedPendingEnd is written only
// once the durable teardown starts, a send-routed fire stamps lastClockFire
// before it counts a delivery, and a condition-fired one-shot has a delivery
// before either. All three signals therefore read as spent, and both the armed
// state and the derived next fire take their answer from here so the two can
// never disagree. A repeating watch never reports spent this way.
func watchOneShotHasFired(cfg *watchConfig) bool {
	if cfg == nil || !cfg.oneShot {
		return false
	}
	return cfg.firedPendingEnd || !cfg.lastClockFire.IsZero() || len(cfg.deliveryTimes) > 0
}

// watchDeliveryTimesOf renders cfg's bounded delivery-instant ring oldest
// first, formatting each instant exactly the way watchStatusInfoFromConfig
// formats CreatedAt (RFC3339Nano). An empty ring yields nil, which the
// omitempty wire tag renders as an absent field.
func watchDeliveryTimesOf(cfg *watchConfig) []string {
	if cfg == nil || len(cfg.deliveryTimes) == 0 {
		return nil
	}
	out := make([]string, 0, len(cfg.deliveryTimes))
	for _, at := range cfg.deliveryTimes {
		out = append(out, at.Format(time.RFC3339Nano))
	}
	return out
}

// watchCadencesOf projects a config's orthogonal trigger sources into cadence
// rows in the same order watchConditionSummary renders them. A timer's
// progressIntervalMS is timerSeconds*1000, so the timer branch comes first and
// prevents the timer from also reading as a progress cadence.
func watchCadencesOf(cfg *watchConfig) []WatchCadenceInfo {
	var cadences []WatchCadenceInfo
	if cfg.outputMatch != "" {
		cadences = append(cadences, WatchCadenceInfo{Kind: "output"})
	}
	switch {
	case cfg.timer && cfg.oneShot:
		cadences = append(cadences, WatchCadenceInfo{Kind: "after", Seconds: float64(cfg.timerSeconds), DerivedNextFireAt: watchDerivedNextFireAt(cfg, time.Duration(cfg.timerSeconds)*time.Second)})
	case cfg.timer:
		cadences = append(cadences, WatchCadenceInfo{Kind: "every", Seconds: float64(cfg.timerSeconds), DerivedNextFireAt: watchDerivedNextFireAt(cfg, time.Duration(cfg.timerSeconds)*time.Second)})
	case cfg.progressIntervalMS > 0:
		cadences = append(cadences, WatchCadenceInfo{Kind: "progress", Seconds: float64(cfg.progressIntervalMS) / 1000, DerivedNextFireAt: watchDerivedNextFireAt(cfg, time.Duration(cfg.progressIntervalMS)*time.Millisecond)})
	}
	if cfg.wildcardEvents || len(cfg.events) > 0 {
		// Mirror watchConditionSummary exactly: only a concrete (non-wildcard)
		// event watch renders its every-Nth throttle and its filter, so only it
		// carries them on the wire. A wildcard watch has neither.
		events := WatchCadenceInfo{Kind: "events"}
		if !cfg.wildcardEvents {
			events.Every = cfg.triggerEvery
			events.Filter = watchEventFilterSummary(cfg.eventFilter)
		}
		cadences = append(cadences, events)
	}
	return cadences
}

// watchDerivedNextFireAt is the best honest next-fire instant for one
// clock-driven watch, from data the config already holds. A repeating ticker
// (every/progress) advances from its newest CLOCK fire, or from the install
// instant when it has not fired yet; a one-shot advances from the install
// instant. A one-shot that already fired has no next fire. The delivery ring is
// deliberately not consulted: it holds every delivery kind, so an output match
// on a watch that also ticks would move the progress cadence's date. The value
// is approximate - the runtime keeps a Go ticker, whose callback the scheduler
// can delay and whose missed ticks coalesce - which is why every surface that
// shows it words it with a "~". Returns "" when no instant can be derived.
func watchDerivedNextFireAt(cfg *watchConfig, interval time.Duration) string {
	if cfg == nil || interval <= 0 {
		return ""
	}
	// A one-shot advances from the install instant and fires exactly once. Once
	// it has fired it has no next fire; its durable teardown is the only thing
	// still pending, and that is not a fire. watchOneShotHasFired owns that
	// question, so the countdown cannot outlive the armed state.
	if watchOneShotHasFired(cfg) {
		return ""
	}
	// A repeating cadence advances from whichever of its install instant and its
	// newest CLOCK fire is later: a clock that steps backwards (or a fire stamped
	// before the install instant during a skew) must not move the next fire
	// earlier, where the panel would hide it as already past.
	base := cfg.createdAt
	if !cfg.lastClockFire.IsZero() && cfg.lastClockFire.After(cfg.createdAt) {
		base = cfg.lastClockFire
	}
	if base.IsZero() {
		return ""
	}
	return base.Add(interval).Format(time.RFC3339Nano)
}

// watchListEntryLess orders watch rows by (Source, ID) — the shared ordering
// for every receiver-keyed watch projection.
func watchListEntryLess(entries []watchListEntry) func(i, j int) bool {
	return func(i, j int) bool {
		if entries[i].Source != entries[j].Source {
			return entries[i].Source < entries[j].Source
		}
		return entries[i].ID < entries[j].ID
	}
}

func (jm *jobManager) liveWatchSummariesForReceiver(receiverSessionID, receiverDelegateID string) []watchListEntry {
	receiverSessionID = strings.TrimSpace(receiverSessionID)
	receiverDelegateID = strings.TrimSpace(receiverDelegateID)
	if receiverSessionID == "" {
		return nil
	}
	entries := formatWatchSummaries(jm.watchConfigSnapshotsWhere(func(cfg *watchConfig) bool {
		return watchConfigMatchesReceiver(cfg, receiverSessionID, receiverDelegateID)
	}))
	if len(entries) == 0 {
		return nil
	}
	return entries
}

func watchConfigMatchesReceiver(cfg *watchConfig, receiverSessionID, receiverDelegateID string) bool {
	if cfg == nil {
		return false
	}
	return cfg.receiverSessionID == receiverSessionID &&
		cfg.receiverDelegateID == receiverDelegateID
}

func watchConfigVisibleToSession(cfg *watchConfig, sessionID string) bool {
	if cfg == nil {
		return false
	}
	return cfg.receiverSessionID == "" || cfg.receiverSessionID == sessionID
}

func watchHistoryMatchesReceiver(h watchHistoryEntry, receiverSessionID, receiverDelegateID string) bool {
	return h.receiverSessionID == receiverSessionID &&
		h.receiverDelegateID == receiverDelegateID
}

func watchHistoryVisibleToSession(h watchHistoryEntry, sessionID string) bool {
	return h.receiverSessionID == "" || h.receiverSessionID == sessionID
}

// watchConditionSummary renders a watch config's trigger condition as a single
// compact line for job_list (spec §4 F2). Trigger sources are orthogonal, so a
// config with more than one set condition joins them with "; ". The pattern is
// bounded because output_match is caller-supplied.
func watchConditionSummary(cfg *watchConfig) string {
	var parts []string
	if cfg.outputMatch != "" {
		parts = append(parts, "output_match: "+limitWatchText(cfg.outputMatch, watchTriggerMaxChars))
	}
	switch {
	case cfg.timer && cfg.oneShot:
		parts = append(parts, fmt.Sprintf("after_seconds: %d", cfg.timerSeconds))
	case cfg.timer:
		parts = append(parts, fmt.Sprintf("repeat_seconds: %d", cfg.timerSeconds))
	case cfg.progressIntervalMS > 0:
		parts = append(parts, fmt.Sprintf("progress_interval_ms: %d", cfg.progressIntervalMS))
	}
	// The note is bounded where it is stored, not at the tighter output_match
	// bound: job_list, formatJobWatch, and the tool description's verbatim claim
	// must agree on what the model gets back.
	if cfg.note != "" {
		parts = append(parts, "note: "+limitWatchText(cfg.note, watchMessageMaxChars))
	}
	if cfg.wildcardEvents {
		parts = append(parts, "events: [*]")
	} else if len(cfg.events) > 0 {
		summary := "events: [" + strings.Join(cfg.events, ", ") + "]"
		if cfg.triggerEvery > 0 {
			summary += fmt.Sprintf(" every %d", cfg.triggerEvery)
		}
		if filterSummary := watchEventFilterSummary(cfg.eventFilter); filterSummary != "" {
			summary += " where " + filterSummary
		}
		parts = append(parts, summary)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ")
}

func watchEventFilterSummary(filter *watchEventFilter) string {
	if filter == nil {
		return ""
	}
	var parts []string
	if filter.ToolName != "" {
		parts = append(parts, "tool_name="+filter.ToolName)
	}
	if filter.Status != "" {
		parts = append(parts, "status="+filter.Status)
	}
	return strings.Join(parts, ", ")
}
