package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provenance"
)

// WatchEventKindNames is the canonical, stable list of model-facing event-kind
// names job_watch accepts. DefJobWatch enumerates them in its description; the
// JobManager gates on them via modelEventKinds. Exported so the provider-side
// capabilityJobControl block (which cannot import agent/events) passes the same
// literal into DefJobWatch (Task 8).
var WatchEventKindNames = []string{"assistant.tool", "communicate", "job.notification"}

func availableEventKindNames() []string { return append([]string(nil), WatchEventKindNames...) }

// modelEventKinds maps the model-facing event-kind names that job_watch accepts
// (and DefJobWatch enumerates) to the internal events.EventKind taxonomy. This is
// the discoverable vocabulary of spec §5.9; it is intentionally a small, stable
// subset of the full event stream, not every internal kind.
var modelEventKinds = map[string]events.EventKind{
	"assistant.tool":   events.EventToolCallEnd,
	"communicate":      events.EventCommunicate,
	"job.notification": events.EventJobFinished,
}

const (
	minWatchProgressIntervalMS = 1000
	maxWatchProgressIntervalMS = 3600000
	watchFrameMaxChars         = 4096
	watchMessageMaxChars       = 2048
	watchTriggerMaxChars       = 1024
	watchExcerptTailBytes      = 4096
	watchExcerptMaxChars       = 4096
	watchReadErrorMaxChars     = 256
	watchTruncatedIndicator    = "\n[truncated]"
	defaultWatchSendPendingCap = 32
	// maxWatchEvents bounds the events list ONE watch may carry, next to
	// maxLiveWatches' bound on how many watches a manager holds. The list is
	// copied into every projection of the watch (the diagnostics facet, the page
	// rows, the hub's navigation summary, the SPA payload) and each projection
	// clones it, so an unbounded list here is an unbounded payload everywhere.
	// The watchable kinds are a fixed handful, so this is generous.
	maxWatchEvents = 64
	// maxWatchConditionChars bounds the condition strings a watch may carry at
	// registration: output_match, event_filter.tool_name, and the filter as the
	// browser renders it (the name with its label and status beside it). It is the
	// hub's navigation label bound and the wire codec's own bound for these
	// fields: a longer value would be truncated on its way to the browser and then
	// shown as the condition the watch matches, which is a claim this daemon must
	// not make. Refused here, so the hub's truncation only ever sees a row from
	// some other producer. Counted in runes, like every other "...Chars" bound here.
	maxWatchConditionChars = 512
	// watchDeliveryBudget caps the condition fires a watch config may deliver
	// before the circuit breaker auto-clears it (spec §4 F1). A periodic progress
	// tick counts a delivery but is a clock rather than a condition, so it never
	// counts against this. Hard-coded, no config knob.
	watchDeliveryBudget = 50
	// watchDeliveryTimeCap bounds the per-watch ring of recent delivery instants
	// that feeds the activity panel's delivery timeline. The ring is a display
	// aid, not an audit log: progress ticks make a watch's delivery count
	// unbounded, so the cap trades history older than the most recent deliveries
	// for a small fixed per-watch footprint (32 × 24-byte time.Time) that stays
	// bounded across an unbounded number of live watches. 32 is more instants
	// than a readable timeline plots.
	watchDeliveryTimeCap = 32
	// maxLiveTimers caps timers per job manager; with the 60-second floor it
	// bounds a session to eight timer wakes a minute.
	maxLiveTimers = 8
	// maxLiveWatches caps the live watches one job manager holds, the way
	// maxLiveTimers bounds timers: it bounds what that manager stores, projects
	// and ships for its own session, and it is what keeps a single session's
	// registration from arming watches without end.
	//
	// It is deliberately not a bound on a session's projected row. That row is a
	// rollup -- the session's own manager's rows plus the receiver watches it owns
	// on descendants, each of those managers capped here -- so it grows with the
	// number of source sessions holding watches for the receiver. That is the
	// design, not the gap: a receiver watch belongs on its receiver's row (a
	// subtree rollup counts each watch once, for its receiver), and the hub caps
	// the rows it displays and counts what it drops.
	//
	// Registration refuses a key that would grow the set rather than trimming it,
	// so no live watch goes missing without the agent hearing about it.
	maxLiveWatches = 32
	// runawaySelfInfluenceDepth caps how many delivered self-influenced priors a
	// watch send may descend from before the breaker drops it as a runaway. The
	// existing watchDeliveryBudget is the coarser whole-watch volume floor.
	runawaySelfInfluenceDepth = 8
	stableWatchReceiverTarget = "stable_watch_receiver"
)

// delegateQuietWindow is how long a running delegate may emit no
// parent-observable activity before the quiet-job watchdog fires an owner
// notification, and the cadence on which that notification repeats while the
// delegate stays silent-and-running (one wake per further window, never a
// burst). delegateQuietCheckInterval is how often the watchdog goroutine
// re-evaluates quiet duration. Both are package vars ONLY so tests can scale
// watchdog timing down; they are not config knobs. The production window is the
// 10 minutes the model-facing message names.
var (
	delegateQuietWindow        = 10 * time.Minute
	delegateQuietCheckInterval = 30 * time.Second
)

// quietWatchdogMessage is the owner-notification reason a quiet running delegate
// triggers. It names the actual window so the text stays truthful when tests
// scale the window down (the production window renders as "10m").
func quietWatchdogMessage(window time.Duration, lastActivity time.Time) string {
	return fmt.Sprintf("quiet for %s; last activity: %s", formatQuietWindow(window), lastActivity.Format(time.RFC3339Nano))
}

// formatQuietWindow renders a quiet window for the notification text. A
// whole-minute window renders as "Nm" (so the production 10-minute window reads
// "10m", not "10m0s"); any other window uses the default duration string so a
// test-scaled sub-second window stays truthful (e.g. "50ms").
func formatQuietWindow(window time.Duration) string {
	if window >= time.Minute && window%time.Minute == 0 {
		return fmt.Sprintf("%dm", window/time.Minute)
	}
	return window.String()
}

// watchEndedUnfiredMessage is the single end-notice text a watch delivers on its
// own channel when its target went terminal without the watch ever firing. It
// names the target's terminal outcome because that outcome is the whole reason
// the condition can never match now.
func watchEndedUnfiredMessage(target string, status jobstore.Status, reason string, outputBytes int64) string {
	return fmt.Sprintf(
		"watch ended: %s is terminal (status=%s reason=%s output_bytes=%d); condition never matched",
		target, status, reason, outputBytes,
	)
}

// watchLostAtRestartMessage is the end-notice text for a watch that died with
// its runtime while its target kept running — the shape reconcileLostJobs
// leaves alone, a job another session owns and recovers on its own restore.
// It is a DIFFERENT claim from watchEndedUnfiredMessage's, not a variant of it:
// "condition never matched" says something about the target, and this target may
// match the condition minutes from now with nothing left watching for it. All
// this notice can honestly report is that the watch is gone, which only the
// watcher can decide what to do about.
func watchLostAtRestartMessage(target string, status jobstore.Status) string {
	return fmt.Sprintf(
		"watch ended: this session restarted and the watch did not survive it; %s is still %s, so its condition may still occur with nothing watching — re-arm the watch if you still care",
		target, status,
	)
}

var callbackWatchesCancelledAtRestartMessage = systemNotification("All your callback watches were cancelled because the agent restarted. No further deliveries will occur. If you still want a callback, re-register it with the job_watch tool.")

// watchLostAtRestartSessionMessage is the restart end-notice text for a
// session-target watch (source "self" or "parent"): its target is this
// session's own live event stream, not a job with a terminal outcome to
// report. The session itself keeps running past restart — only the watch's
// condition-tracking runtime is gone — so this is watchLostAtRestartMessage's
// claim (the target may still occur; nothing is watching for it), never
// watchEndedUnfiredMessage's (there is no terminal outcome to name).
func watchLostAtRestartSessionMessage() string {
	return "watch ended: this session restarted and the watch did not survive it; the observed session is still running, so its condition may still occur with nothing watching — re-arm the watch if you still care"
}

func watchLostAtRestartStableDelegateMessage(source string) string {
	return fmt.Sprintf("watch ended: this session restarted and the watch did not survive it; stable delegate %s may still emit matching events — re-arm the watch if you still care", source)
}

// watchBudgetClearedMessage is the single final notification text emitted when a
// watch trips the delivery budget on condition fires (spec §4 F1). The count is
// the budget itself, so the notice names condition matches rather than
// deliveries, and offers only the levers that narrow a condition: a longer
// progress_interval_ms cannot help, because periodic ticks do not count here.
func watchBudgetClearedMessage(target string) string {
	return fmt.Sprintf(
		"watch cleared: %s matched %d times; re-arm with a tighter condition (higher every or narrower output_match)",
		target, watchDeliveryBudget,
	)
}

type watchKey struct {
	VisibleSessionID   string
	Target             string
	SendTo             string
	ReceiverSessionID  string
	ReceiverDelegateID string
	// Slot is the watch id for a timer and empty for every other watch, so
	// each timer create is its own key and never replaces or no-ops against
	// another watch on the same target. It is compared exactly everywhere.
	Slot string
}

type watchConfig struct {
	// id is a stable per-session handle for this watch, assigned at install and
	// preserved across idempotent re-configure; a replacement gets a fresh id.
	id                 string
	watchID            string
	configHash         string
	sourcePublic       string
	receiverSessionID  string
	receiverDelegateID string
	receiverNotify     func(jobNotification)
	receiverHoldWake   func() func()
	sourceDelegateID   string
	sourceGeneration   uint64
	stableReceiver     bool
	target             string
	outputMatch        string
	outputMatcher      *jobstore.OutputMatcher
	progressIntervalMS int
	// slot mirrors watchKey.Slot so config-side key predicates compare it.
	slot string
	// timer marks a watch created with after_seconds or repeat_seconds; its
	// progressIntervalMS is timerSeconds*1000. oneShot ends the watch after
	// its first fire. note rides every fire's block.
	timer   bool
	oneShot bool
	// firedPendingEnd marks a one-shot that has already delivered its single
	// fire but whose durable teardown did not persist. The ticker stays armed
	// so the next tick retries the end instead of leaving a registered watch
	// with a dead timer; the retry never fires again.
	firedPendingEnd bool
	timerSeconds    int
	note            string
	events          []string
	eventKinds      map[events.EventKind]bool
	wildcardEvents  bool
	eventFilter     *watchEventFilter
	// every-Nth throttle: set from `every` + the single events[0] when every > 0.
	triggerKind       events.EventKind
	triggerEvery      int
	eventCount        int
	send              *watchSendArgs
	generation        string
	pending           map[jobstore.WatchSendKey]*jobstore.WatchSendState
	pendingOrder      []jobstore.WatchSendKey
	settledUpdateSeq  map[jobstore.WatchSendKey]uint64
	settledOrder      []jobstore.WatchSendKey
	rejectingDelivery bool
	nextUpdateSeq     uint64
	progressStop      chan struct{}
	// deliveries counts model-facing deliveries for this watch config (rendered
	// caller frames + delivered sidecar sends + no-send watch notifications +
	// periodic progress ticks). It is the volume job_list reports, not the
	// breaker's trigger. Counted jm-side under jm.mu; survives the
	// observation/drain split with the cfg pointer; a replacement cfg from
	// newWatchConfig starts fresh at 0.
	deliveries int
	// conditionFires counts actual condition matches, distinct from deliveries:
	// progress ticks and teardown notices are deliveries but do not satisfy the
	// condition the model asked this watch to observe. The circuit breaker
	// latches here — the watch auto-clears on its watchDeliveryBudget-th
	// condition fire (spec §4 F1) — so a watch that only ever ticks survives.
	conditionFires int
	// budgetTripped latches the breaker to one teardown per config, so a send
	// watch settling several frames at or past the budget reports it just once.
	budgetTripped bool
	// createdAt is the install time of this live watch config, stamped from
	// jm.now() in newWatchConfig and surfaced by job_list (spec §4 F2). A
	// replacement config is a new watch, so it gets a fresh timestamp. Configs
	// reconstructed into terminalFlush on restore are never built via
	// newWatchConfig and so are intentionally left zero (they are not live).
	createdAt time.Time
	// deliveryTimes is a bounded ring of the most recent delivery instants for
	// this config, oldest first and capped at watchDeliveryTimeCap. It is
	// stamped from jm.now() in the same locked helper that increments
	// deliveries, so the count and the ring can never diverge. It feeds the
	// activity panel's timeline only; nothing schedules from it.
	deliveryTimes []time.Time
	// lastClockFire is the most recent instant a clock tick actually fired for
	// this config - a timer's fire or a progress-interval tick - stamped from
	// jm.now() under jm.mu where that tick fires. The delivery ring mixes every
	// delivery kind (output matches, event fires, attach scans), so the derived
	// next fire reads this instead of the ring: a watch that legally combines
	// output_match with progress_interval_ms would otherwise date its progress
	// cadence from an unrelated output match. Zero means the clock has not
	// fired yet. Restored and detached configs never schedule, so they leave it
	// zero exactly as they leave createdAt zero.
	lastClockFire time.Time
}

type watchArgs struct {
	Operation          string
	WatchID            string
	Source             string
	Target             string
	ReceiverSessionID  string
	ReceiverDelegateID string
	ReceiverNotify     func(jobNotification)
	ReceiverHoldWake   func() func()
	OutputMatch        string
	ProgressIntervalMS int
	Events             []string
	Every              int
	EventFilter        *watchEventFilter
	// AfterSeconds and RepeatSeconds are the timer triggers (self only);
	// Note rides every fire of any watch. All three are create-only.
	AfterSeconds         int
	RepeatSeconds        int
	Note                 string
	Send                 *watchSendArgs
	ReceiverSendInternal bool
	SourceDelegateID     string
	SourceGeneration     uint64
	StableReceiver       bool
	Clear                bool
	// ResultTool is the watcher session's result tool name, which validation
	// errors teach; empty means the default.
	ResultTool string
}

type watchSourceKind int

const (
	watchSourceConcreteJob watchSourceKind = iota
	watchSourceSelfSession
	watchSourceParentSession
	watchSourceStableDelegate
)

type watchSource struct {
	Kind     watchSourceKind
	Public   string
	Internal string
}

func normalizeWatchSource(source string) (watchSource, error) {
	source = strings.TrimSpace(source)
	switch source {
	case "":
		return watchSource{}, errors.New("invalid_request: source is required")
	case "self":
		return watchSource{Kind: watchSourceSelfSession, Public: "self", Internal: runtimeMessageAliasCaller}, nil
	case "parent":
		return watchSource{Kind: watchSourceParentSession, Public: "parent", Internal: runtimeMessageAliasCaller}, nil
	default:
		// Accept an optional "job:" prefix so both "job:job_123" and "job_123"
		// resolve to the same concrete job. The transcript_ref convention uses
		// the "job:" prefix; job_watch source should tolerate it for ergonomics.
		source = strings.TrimPrefix(source, "job:")
		if strings.HasPrefix(source, "job_") {
			return watchSource{Kind: watchSourceConcreteJob, Public: source, Internal: source}, nil
		}
		if strings.HasPrefix(source, "dlg_") {
			return watchSource{Kind: watchSourceStableDelegate, Public: source, Internal: runtimeMessageAliasCaller}, nil
		}
		return watchSource{}, fmt.Errorf("source_not_watchable: %q is not self, parent, a concrete job_id, or a stable delegate_id", source)
	}
}

func watchPublicSource(source, target string) string {
	source = strings.TrimSpace(source)
	if source != "" {
		return source
	}
	if target == runtimeMessageAliasCaller {
		return "self"
	}
	return target
}

type watchEventFilter struct {
	ToolName string `json:"tool_name,omitempty"`
	Status   string `json:"status,omitempty"`
}

type watchSendArgs struct {
	To             string
	Message        string
	IncludeExcerpt bool
}

type watchResult struct {
	WatchID            string
	Source             string
	Target             string
	Watching           bool
	OutputMatch        string
	Events             []string
	EventFilter        *watchEventFilter
	ProgressIntervalMS int
	// TimerSeconds and OneShot describe a timer watch; a timer reports its
	// interval in seconds and leaves ProgressIntervalMS zero so the result
	// speaks in the units the model asked in. Note labels any watch.
	TimerSeconds     int
	OneShot          bool
	Note             string
	Send             *watchSendArgs
	ReplacedExisting bool
	// Fired reports that an output_match attach scan found a level match in the
	// running target's already-retained output and fired once at attach (spec
	// §7.1). Only a fresh concrete-running output_match install can set this;
	// idempotent/replace-no-op/session-target installs leave it false. A terminal
	// catch-up that matched also sets it.
	Fired bool
	// TerminalCatchup reports that the request was an output_match-only watch on an
	// already-terminal job, served as a one-shot catch-up scan of retained output
	// rather than a live watch (spec §7.1 "Terminal target"). Watching is always
	// false when this is set; Fired distinguishes a matched catch-up from an
	// unmatched one. Status carries the target's terminal status.
	TerminalCatchup bool
	// Status carries the watched job's terminal status for a terminal catch-up
	// (spec §7.1). Empty for live installs. Reason carries the producer's
	// terminal reason beside it, so display surfaces can join the card's
	// command-outcome words for legacy pre-split records.
	Reason string
	Status string
}

type watchSendDelivery struct {
	cfg                      *watchConfig
	key                      watchKey
	generation               string
	updateSeq                uint64
	allowAfterTerminalExpiry bool
	send                     *watchSendArgs
	deliveryID               string
	message                  string
	frame                    string
	visibleSessionID         string
	watchTarget              string
	watchedIdentity          string
	trigger                  string
	triggerProvenance        *provenance.Causal
	// provenance is the delivery's causal chain: the triggering event's
	// provenance extended with this watch's own (watch_id, generation) key and a
	// chain entry naming this delivery. Persisted onto the pending WatchSendState
	// so a downstream event this send causes is recognized as the watch's echo.
	provenance *provenance.Causal
	eventKind  events.EventKind
	eventData  events.EventData
	// endNotice marks the teardown frame a watch sends when it ends without ever
	// having fired, so the durable state says what the trigger prose only implies.
	endNotice bool
	// self-influence classification, stamped at the observation site; fuseDepth
	// drives the runaway fuse, the rest the gradient inform.
	selfInfluence bool
	gradientDepth int
	fuseDepth     int
	truncated     bool
}

// withSelfInfluence stamps the breaker's classification onto a built delivery.
func (d watchSendDelivery) withSelfInfluence(c selfInfluence) watchSendDelivery {
	d.selfInfluence, d.gradientDepth, d.fuseDepth, d.truncated = c.self, c.gradientDepth, c.fuseDepth, c.truncated
	return d
}

type restoredWatchConfigKey struct {
	visibleSessionID   string
	watchID            string
	target             string
	sendTo             string
	receiverSessionID  string
	receiverDelegateID string
	generation         string
	sourceDelegateID   string
	sourceGeneration   uint64
	stableReceiver     bool
}

// watchSendToken identifies a pending caller-targeted watch send. Tokens are
// at-least-once and harmless when stale: render-by-key skips any token whose
// pending state was replaced, cleared, dropped, or already settled.
type watchSendToken struct {
	ChildSessionID string // "" = the session's own jobManager
	Key            jobstore.WatchSendKey
	UpdateSeq      uint64
	DeliveryID     string
}

func watchSendTokenNotification(childSessionID string, state jobstore.WatchSendState) jobNotification {
	return jobNotification{
		Kind:       jobNotificationKindWatch,
		JobID:      state.Key.ResolvedWatchedIdentity,
		Status:     jobNotificationEventWatch,
		Reason:     state.TriggerReason,
		Provenance: provenance.Clone(state.Provenance),
		WatchSend: &watchSendToken{
			ChildSessionID: childSessionID,
			Key:            state.Key,
			UpdateSeq:      state.UpdateSeq,
			DeliveryID:     state.DeliveryID,
		},
	}
}

// jobManagerForToken resolves which jobManager owns a token's pending state.
func (s *Session) jobManagerForToken(tok *watchSendToken) *jobManager {
	if tok == nil {
		return nil
	}
	if tok.ChildSessionID == "" {
		return s.jobManager
	}
	if s.subagents == nil {
		return nil
	}
	if sub := s.subagents.get(tok.ChildSessionID); sub != nil && sub.sess != nil {
		return sub.sess.jobManager
	}
	return nil
}

// resolveWatchSendToken returns the CURRENT frame for a token, or ok=false if
// the token is stale (latest-frame-wins; also covers delivery-after-drop).
func (s *Session) resolveWatchSendToken(tok *watchSendToken) (jm *jobManager, cfg *watchConfig, state jobstore.WatchSendState, ok bool) {
	jm = s.jobManagerForToken(tok)
	if jm == nil {
		return nil, nil, jobstore.WatchSendState{}, false
	}
	jm.mu.Lock()
	defer jm.mu.Unlock()
	cfg = jm.watchConfigForKeyLocked(tok.Key)
	if cfg == nil {
		return nil, nil, jobstore.WatchSendState{}, false
	}
	cur := cfg.pending[tok.Key]
	if cur == nil || cur.UpdateSeq != tok.UpdateSeq {
		return nil, nil, jobstore.WatchSendState{}, false
	}
	return jm, cfg, *cur, true
}

// watchConfigForKeyLocked returns the live or terminal-flush watch config that
// currently holds a pending entry for key, or nil. Caller holds jm.mu.
func (jm *jobManager) watchConfigForKeyLocked(key jobstore.WatchSendKey) *watchConfig {
	for _, cfg := range jm.watches {
		if cfg.pending[key] != nil {
			return cfg
		}
	}
	for cfg := range jm.terminalFlush {
		if cfg.pending[key] != nil {
			return cfg
		}
	}
	return nil
}

// settleWatchSendDelivered durably records delivery for a watch-send state and
// removes its exact pending entry when it is still the current cursor. A newer
// coalesced cursor is preserved while this delivered source is acknowledged.
// The caller is responsible for any error-notification behavior. This is the
// model-facing completion for both watch-send rails (delegate sidecar sends and
// caller frames), so it is where the send half of the delivery budget is counted;
// observation/coalescing does not count (spec §4 F1). It is not, however, the
// only place the condition-fire breaker can trip — the match site latches it
// too, so frames that never settle stay bounded — and the latch keeps the two
// to one teardown.
func (jm *jobManager) settleWatchSendDelivered(cfg *watchConfig, state jobstore.WatchSendState) error {
	defer jm.publishWatchActivity()
	delivered := state
	if err := jm.appendWatchSendEvents([]jobstore.Event{{
		Kind:      jobstore.EventWatchSendDelivered,
		TS:        jm.now(),
		WatchSend: &delivered,
	}}); err != nil {
		return err
	}
	jm.removePendingWatchSend(cfg, delivered.Key, delivered.UpdateSeq)
	jm.releaseStableWatchReceipt(delivered.DeliveryID)
	jm.mu.Lock()
	if delivered.DeliveryID != "" {
		jm.deliveredWatchSendIDs[delivered.DeliveryID] = struct{}{}
	}
	crossedBudget := jm.recordWatchDeliveryLocked(cfg)
	jm.mu.Unlock()
	if crossedBudget {
		jm.autoClearWatchOverBudget(cfg)
	}
	return nil
}

// watchSendDeliveredLocked reports whether a watch-send with deliveryID has
// settled delivered. Caller holds jm.mu.
func (jm *jobManager) watchSendDeliveredLocked(deliveryID string) bool {
	_, ok := jm.deliveredWatchSendIDs[deliveryID]
	return ok
}

// watchSendDelivered reports whether a watch-send with deliveryID has settled
// delivered, taking jm.mu for callers outside a locked section.
func (jm *jobManager) watchSendDelivered(deliveryID string) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.watchSendDeliveredLocked(deliveryID)
}

// claimWatchSendDelivery takes process-local ownership of one watch-send
// delivery id. It returns false when another executor already owns it, so the
// second caller stands down instead of racing the same store write. Mirrors
// claimStableWatchSettlementRetries. Guarded by jm.mu.
func (jm *jobManager) claimWatchSendDelivery(deliveryID string) bool {
	if deliveryID == "" {
		return true
	}
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if _, held := jm.deliveringWatchSends[deliveryID]; held {
		return false
	}
	if jm.deliveringWatchSends == nil {
		jm.deliveringWatchSends = make(map[string]struct{})
	}
	jm.deliveringWatchSends[deliveryID] = struct{}{}
	return true
}

// releaseWatchSendDeliveryClaim releases a claim taken by
// claimWatchSendDelivery. It is safe to call on a delivery id that was never
// claimed.
func (jm *jobManager) releaseWatchSendDeliveryClaim(deliveryID string) {
	if deliveryID == "" {
		return
	}
	jm.mu.Lock()
	delete(jm.deliveringWatchSends, deliveryID)
	_, superseded := jm.supersededWatchSends[deliveryID]
	delete(jm.supersededWatchSends, deliveryID)
	handOff := superseded && jm.retryUnsettledWatchSendLocked(deliveryID)
	_, retrying := jm.stableWatchSettlementRetries[deliveryID]
	jm.mu.Unlock()
	if handOff {
		jm.kick()
	}
	if superseded && !retrying {
		jm.releaseStableWatchReceipt(deliveryID)
	}
}

// retryUnsettledWatchSendLocked hands a replaced frame the receiver took but
// never acknowledged to the settlement retry, which keeps its receipt until it
// acknowledges. It reports false when the frame has no failed acknowledgement
// noted. Caller holds jm.mu.
func (jm *jobManager) retryUnsettledWatchSendLocked(deliveryID string) bool {
	unsettled, accepted := jm.unsettledWatchSends[deliveryID]
	if !accepted {
		return false
	}
	delete(jm.unsettledWatchSends, deliveryID)
	jm.rememberStableWatchSettlementRetryLocked(unsettled)
	return true
}

// watchSendSuperseded reports whether a coalesced frame replaced the claimed
// delivery deliveryID while it was being delivered.
func (jm *jobManager) watchSendSuperseded(deliveryID string) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	_, superseded := jm.supersededWatchSends[deliveryID]
	return superseded
}

// validateWatchConfig runs the target-independent validation a watch install must
// pass — condition presence, send target, and config build — and returns the built
// config. configureWatch routes install validation through it.
// Event-arg shape (validateWatchEventArgs) and session-target shape are validated by
// the caller.
func (jm *jobManager) validateWatchConfig(a watchArgs, slot string) (*watchConfig, error) {
	if !watchArgsHasCondition(a) {
		return nil, errors.New("invalid_request: nothing to watch")
	}
	if a.Send != nil && !a.ReceiverSendInternal {
		if err := jm.validateWatchSendTarget(a.Send.To, a); err != nil {
			return nil, err
		}
	}
	cfg, err := newWatchConfig(a, jm.now(), slot)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// normalizeWatchArgs validates and normalizes the input-shape fields a watch install
// depends on, before any other validation: progress_interval_ms (reject negative,
// clamp to bounds) and every (1 reads as the unset default).
func normalizeWatchArgs(a *watchArgs) error {
	if a.ProgressIntervalMS < 0 {
		return errors.New("invalid_request: progress_interval_ms must be non-negative")
	}
	if a.ProgressIntervalMS > 0 && a.ProgressIntervalMS < minWatchProgressIntervalMS {
		a.ProgressIntervalMS = minWatchProgressIntervalMS
	}
	if a.ProgressIntervalMS > maxWatchProgressIntervalMS {
		a.ProgressIntervalMS = maxWatchProgressIntervalMS
	}
	if a.AfterSeconds != 0 && (a.AfterSeconds < 60 || a.AfterSeconds > 86400) {
		return errors.New("invalid_request: after_seconds must be between 60 and 86400")
	}
	if a.RepeatSeconds != 0 && (a.RepeatSeconds < 60 || a.RepeatSeconds > 3600) {
		return errors.New("invalid_request: repeat_seconds must be between 60 and 3600")
	}
	// A negative every is not "no throttle": it asks for a throttle the daemon
	// cannot honour, and installing an unthrottled watch in its place would
	// deliver every matching event to a caller who asked for one in N. Refuse it
	// here, before the every>0 branches below treat it as absent everywhere.
	if a.Every < 0 {
		return fmt.Errorf("invalid_request: every must be a positive count (it fires on each Nth matching event), got %d", a.Every)
	}
	a.Note = limitWatchText(a.Note, watchMessageMaxChars)
	// The payload bounds: a watch whose trigger arguments are unbounded makes
	// every projection of it unbounded too, so they are refused here rather than
	// truncated (a cut regex or a cut tool name would silently change what the
	// watch matches).
	if len(a.Events) > maxWatchEvents {
		return fmt.Errorf("invalid_request: events names at most %d kinds, got %d", maxWatchEvents, len(a.Events))
	}
	if runes := len([]rune(a.OutputMatch)); runes > maxWatchConditionChars {
		return fmt.Errorf("invalid_request: output_match must be at most %d characters, got %d", maxWatchConditionChars, runes)
	}
	// every:1 is the semantic default (fire on each occurrence), so it reads as
	// unset everywhere downstream; the single-concrete-kind requirement applies
	// only to every>1, which actually throttles.
	if a.Every == 1 {
		a.Every = 0
	}
	if a.EventFilter != nil {
		a.EventFilter.ToolName = strings.TrimSpace(a.EventFilter.ToolName)
		a.EventFilter.Status = strings.ToLower(strings.TrimSpace(a.EventFilter.Status))
		if runes := len([]rune(a.EventFilter.ToolName)); runes > maxWatchConditionChars {
			return fmt.Errorf("invalid_request: event_filter.tool_name must be at most %d characters, got %d", maxWatchConditionChars, runes)
		}
		// The hub renders the filter as ONE label ("tool_name=…, status=…") and
		// bounds that label, so the field's own bound is not enough: a name at the
		// bound plus the decoration around it was cut on its way to the browser,
		// which then showed a shortened tool name as the one being matched. Bound
		// the rendered label with the very function that renders it, so the two
		// cannot drift apart.
		if summary := watchEventFilterSummary(a.EventFilter); len([]rune(summary)) > maxWatchConditionChars {
			return fmt.Errorf("invalid_request: event_filter must render to at most %d characters, got %d (tool_name shares the bound with its label and status)", maxWatchConditionChars, len([]rune(summary)))
		}
		if a.EventFilter.ToolName == "" && a.EventFilter.Status == "" {
			a.EventFilter = nil
		}
	}
	return nil
}

type watchConfigureHooks struct {
	beforeTargetRevalidate func(string)
	afterDetachedTeardown  func(watchKey)
}

func (jm *jobManager) configureWatch(a watchArgs) (watchResult, error) {
	return jm.configureWatchWithHooks(a, watchConfigureHooks{})
}

func (jm *jobManager) configureWatchWithHooks(a watchArgs, hooks watchConfigureHooks) (watchResult, error) {
	defer jm.publishWatchActivity()
	release, err := jm.beginRetirementMutation("watch")
	if err != nil {
		return watchResult{}, err
	}
	defer release()
	if a.Target == "" {
		return watchResult{}, errors.New("invalid_request: target is required")
	}
	if err := normalizeWatchArgs(&a); err != nil {
		return watchResult{}, err
	}
	applyStableReceiverWatchSend(&a)
	sendTo := ""
	if a.Send != nil && !a.ReceiverSendInternal {
		a.Send.To = strings.TrimSpace(a.Send.To)
		sendTo = a.Send.To
	}
	if a.Send != nil && a.ReceiverSendInternal {
		sendTo = strings.TrimSpace(a.Send.To)
	}
	// The Slot is filled in below, once the request has earned an id. Every use
	// of the key above that point is a clear or a terminal catch-up, and neither
	// carries a slot: a fresh id would build a key that matches nothing.
	key := watchKey{
		VisibleSessionID:   jm.sessionID,
		Target:             a.Target,
		SendTo:             sendTo,
		ReceiverSessionID:  strings.TrimSpace(a.ReceiverSessionID),
		ReceiverDelegateID: strings.TrimSpace(a.ReceiverDelegateID),
	}
	if a.Clear && jm.hasWatchClearState(key) {
		return jm.clearWatch(key)
	}
	if err := jm.validateWatchTarget(a.Target); err != nil {
		// Clearing is idempotent regardless of target state: once a job goes
		// terminal its concrete watch auto-removes, so a clear on that target must
		// be a no-op success rather than target_terminal. A genuinely-missing
		// target still returns its original target_not_found.
		if a.Clear {
			if _, _, terminal, statusErr := jm.terminalWatchTargetStatus(a.Target); statusErr == nil && terminal {
				return jm.clearWatch(key)
			}
			return watchResult{}, err
		}
		// An output_match-only watch on an already-terminal job is served as a
		// one-shot catch-up scan of retained output rather than rejected (spec
		// §7.1 "Terminal target"). Any other terminal request, and target_not_found,
		// keep their original error. terminalWatchTargetStatus resolves the terminal
		// status directly rather than parsing the error string.
		if watchArgsIsOutputMatchOnly(a) {
			status, reason, terminal, statusErr := jm.terminalWatchTargetStatus(a.Target)
			if statusErr != nil {
				return watchResult{}, statusErr
			}
			if terminal {
				if a.Send != nil && !a.ReceiverSendInternal {
					if sendErr := jm.validateWatchSendTarget(a.Send.To, a); sendErr != nil {
						return watchResult{}, sendErr
					}
				}
				return jm.runTerminalCatchup(a, key, status, reason)
			}
		}
		return watchResult{}, err
	}
	if err := validateWatchEventArgs(a); err != nil {
		return watchResult{}, err
	}
	if err := validateWatchTriggerShape(a); err != nil {
		return watchResult{}, err
	}
	if a.Clear {
		return jm.clearWatch(key)
	}
	// Session-target shape checks are specific to configureWatch's session targets
	// (caller, *); a concrete launch target never reaches them, so they stay here
	// rather than in the shared install validator.
	if a.OutputMatch != "" && isWatchSessionTarget(a.Target) {
		if len(a.Events) == 1 && a.Events[0] == "communicate" {
			return watchResult{}, errors.New("invalid_request: output_match watches concrete job output" + parentObserverRepair(a.ResultTool))
		}
		return watchResult{}, errors.New(`invalid_request: output_match watches concrete job output; session-source watches use events and, for assistant.tool, event_filter`)
	}
	if a.Send != nil && a.Send.IncludeExcerpt && isWatchSessionTarget(a.Target) {
		return watchResult{}, errors.New("invalid_request: include_excerpt requires a concrete job target; session-target frames carry bounded event payloads, not output excerpts")
	}
	// A timer's id is minted here, after the target, event, and shape checks
	// that reject most creates. The live-timer cap check below can still
	// reject after the mint; ids are stateless, so nothing is burned. The id
	// goes into the key's Slot and through validateWatchConfig into the
	// config, so the key's Slot, the config's slot, and the config's watchID
	// are all the same id.
	if !a.Clear && watchArgsIsTimer(a) {
		key.Slot = jobstore.NewWatchID()
	}
	cfg, err := jm.validateWatchConfig(a, key.Slot)
	if err != nil {
		return watchResult{}, err
	}
	jm.mu.Lock()
	if !isWatchSessionTarget(key.Target) && !isWatchableConcreteJobLocked(jm.running[key.Target]) {
		jm.mu.Unlock()
		if hooks.beforeTargetRevalidate != nil {
			hooks.beforeTargetRevalidate(key.Target)
		}
		if err := jm.validateWatchTarget(key.Target); err != nil {
			return watchResult{}, err
		}
		jm.mu.Lock()
		if !isWatchableConcreteJobLocked(jm.running[key.Target]) {
			jm.mu.Unlock()
			return watchResult{}, watchTargetNotFoundError(key.Target)
		}
	}
	if cfg.timer && jm.liveTimerCountLocked() >= maxLiveTimers {
		jm.mu.Unlock()
		return watchResult{}, fmt.Errorf("invalid_request: too many timers (%d live); clear one first", maxLiveTimers)
	}
	existing := jm.watches[key]
	// Only a fresh key grows the set: re-registering or replacing one the
	// manager already holds is refused by neither this guard nor the cap's
	// intent. See maxLiveWatches.
	if existing == nil && len(jm.watches) >= maxLiveWatches {
		jm.mu.Unlock()
		return watchResult{}, fmt.Errorf("invalid_request: too many watches (%d live); clear one first", maxLiveWatches)
	}
	detachedCfgs, detached := jm.detachedWatchSendTerminalSnapshotsLocked(key, jobstore.EventWatchSendDropped, "watch replaced", jm.now())
	if existing != nil {
		equal := existing.configHash == cfg.configHash
		if equal && len(detachedCfgs) == 0 {
			result := watchResultFromConfig(existing, false)
			jm.mu.Unlock()
			return result, nil
		}
		var targets []watchConfigTerminalSnapshot
		if !equal {
			targets = append(targets, watchConfigTerminalSnapshot{
				key:       key,
				cfg:       existing,
				terminal:  watchSendTerminalSnapshotsLocked(existing, jobstore.EventWatchSendDropped, "watch replaced", jm.now()),
				endReason: "replaced",
			})
		}
		markWatchConfigSnapshotsRejectingLocked(targets)
		markWatchConfigsRejectingLocked(detachedCfgs)
		dropped := append(terminalSnapshots(targets), detached...)
		if equal {
			if err := jm.appendWatchTeardownBatch(detached, nil); err != nil {
				rollbackWatchConfigsRejectingLocked(jm, detachedCfgs)
				jm.mu.Unlock()
				return watchResult{}, err
			}
			result := watchResultFromConfig(existing, false)
			jm.mu.Unlock()
			jm.removeWatchSendTerminalSnapshots(dropped)
			jm.forgetDetachedWatchSendConfigsIfEmpty(detachedCfgs)
			return result, nil
		}
		if err := jm.appendWatchReplacementBatch(cfg, targets, dropped); err != nil {
			rollbackWatchConfigSnapshotsRejectingLocked(jm, targets)
			rollbackWatchConfigsRejectingLocked(jm, detachedCfgs)
			jm.mu.Unlock()
			return watchResult{}, err
		}
		jm.recordWatchEndedLocked(key, existing, "replaced")
		closeWatchConfig(existing)
		stop := cfg.initProgressStop()
		jm.watches[key] = cfg
		result := watchResultFromConfig(cfg, true)
		scanData, scan, prepErr := jm.prepareAttachScanLocked(cfg, jm.running[key.Target])
		jm.mu.Unlock()
		jm.removeWatchSendTerminalSnapshots(dropped)
		jm.forgetDetachedWatchSendConfigsIfEmpty(detachedCfgs)
		jm.startProgressTimer(key, cfg, stop)
		result.Fired = jm.completeAttachScan(cfg, key.Target, scanData, scan, prepErr)
		return result, nil
	}

	if len(detachedCfgs) != 0 {
		markWatchConfigsRejectingLocked(detachedCfgs)
		jm.mu.Unlock()
		applied, err := jm.appendWatchSendTerminalSnapshots(detached)
		if err != nil {
			jm.removeWatchSendTerminalSnapshots(applied)
			jm.rollbackWatchConfigsRejecting(detachedCfgs)
			return watchResult{}, err
		}
		if hooks.afterDetachedTeardown != nil {
			hooks.afterDetachedTeardown(key)
		}
		jm.mu.Lock()
		if current := jm.watches[key]; current != nil {
			jm.mu.Unlock()
			jm.removeWatchSendTerminalSnapshots(applied)
			jm.forgetDetachedWatchSendConfigsIfEmpty(detachedCfgs)
			return watchResultFromConfig(current, false), nil
		}
		jm.mu.Unlock()
		jm.removeWatchSendTerminalSnapshots(applied)
		jm.forgetDetachedWatchSendConfigsIfEmpty(detachedCfgs)
		jm.mu.Lock()
	}
	if err := jm.appendWatchRegisteredEvent(cfg); err != nil {
		jm.mu.Unlock()
		return watchResult{}, err
	}
	stop := cfg.initProgressStop()
	jm.watches[key] = cfg
	result := watchResultFromConfig(cfg, false)
	scanData, scan, prepErr := jm.prepareAttachScanLocked(cfg, jm.running[key.Target])
	jm.mu.Unlock()
	jm.startProgressTimer(key, cfg, stop)
	result.Fired = jm.completeAttachScan(cfg, key.Target, scanData, scan, prepErr)
	return result, nil
}

func watchArgsHasCondition(a watchArgs) bool {
	if strings.TrimSpace(a.ReceiverSessionID) != "" && a.ReceiverSessionID != a.Target && isWatchSessionTarget(a.Target) {
		return true
	}
	return a.OutputMatch != "" || a.ProgressIntervalMS > 0 || len(a.Events) > 0 || watchArgsIsTimer(a)
}

// watchArgsIsTimer reports whether the request is a timer create: either time
// field present and nonzero. Timers are ordinary self watches whose progress
// interval is set from seconds and which carry a note. A negative value is a
// timer too, so the source still defaults to self and the request reaches
// normalizeWatchArgs' bounds check, which names the field the caller got wrong
// instead of reporting a missing source. A present zero reads as absent:
// providers materialize every optional property, and a zero arms nothing.
func watchArgsIsTimer(a watchArgs) bool {
	return a.AfterSeconds != 0 || a.RepeatSeconds != 0
}

// watchArgsIsOutputMatchOnly reports whether a watch request carries an
// output_match condition and NO other trigger source — the only shape eligible
// for terminal catch-up (spec §7.1 "Terminal target"). events/progress/every on a
// terminal target can never fire, so they still fail target_terminal. Clear
// requests are never catch-up. A time field is excluded too: catch-up runs
// before validateWatchTriggerShape, so admitting that shape would serve a scan
// instead of the timer rules' correction. A note is not a trigger — it is the
// watch's prose payload — so it rides the scan's notification like any other.
func watchArgsIsOutputMatchOnly(a watchArgs) bool {
	return !a.Clear &&
		a.OutputMatch != "" &&
		len(a.Events) == 0 &&
		a.Every == 0 &&
		a.ProgressIntervalMS == 0 &&
		!watchArgsIsTimer(a)
}

// parentObserverRepair is the repair shape appended to a validation error when
// a watch asks for the parent's communicate events the wrong way. The events
// token "communicate" is the event kind; resultTool is the tool the watcher
// calls, and empty means the default.
func parentObserverRepair(resultTool string) string {
	if resultTool == "" {
		resultTool = tool.DefaultResultToolName
	}
	return `; parent observers that need communicate messages use source="parent" with events ["communicate"], read delivered event.message in the frame, send updates with ` + resultTool + `(end_turn=false), and report findings with ` + resultTool + `(end_turn=true)`
}

func validateWatchEventArgs(a watchArgs) error {
	for _, name := range a.Events {
		if name == "*" {
			continue
		}
		if name == "assistant.message" {
			return errors.New("invalid_request: assistant.message is not watchable; use communicate for result messages, assistant.tool for tool calls, or job.notification for job lifecycle events")
		}
		if _, ok := modelEventKinds[name]; !ok {
			return fmt.Errorf("invalid_request: unknown event kind %q", name)
		}
	}
	if a.Every > 0 {
		if len(a.Events) != 1 {
			if len(a.Events) == 0 {
				return errors.New(`invalid_request: every requires events naming exactly one kind (e.g. events ["communicate"], every 3); every with no events has nothing to fire on`)
			}
			return errors.New(`invalid_request: every requires events naming exactly one kind, not several (e.g. events ["communicate"], every 3)`)
		}
		if a.Events[0] == "*" {
			return errors.New(`invalid_request: every requires a single concrete event kind, not "*"`)
		}
	}
	if a.EventFilter != nil {
		if len(a.Events) == 0 {
			return errors.New(`invalid_request: event_filter requires events naming assistant.tool (e.g. events ["assistant.tool"], event_filter {"tool_name":"read_file","status":"ok"}); event_filter with no events has nothing to filter`)
		}
		if len(a.Events) != 1 || a.Events[0] != "assistant.tool" {
			if len(a.Events) == 1 && a.Events[0] == "communicate" {
				return errors.New("invalid_request: event_filter matches assistant.tool events" + parentObserverRepair(a.ResultTool))
			}
			return errors.New(`invalid_request: event_filter matches assistant.tool events; use events ["assistant.tool"] with event_filter {"tool_name":"read_file","status":"ok"}`)
		}
		switch a.EventFilter.Status {
		case "", "ok", "error":
		default:
			return errors.New(`invalid_request: event_filter.status must be "ok" or "error"`)
		}
	}
	return nil
}

func validateWatchTriggerShape(a watchArgs) error {
	// The timer rules run first. A request that named a timer field is answered
	// in its own terms: told which OTHER field it conflicts with, rather than
	// pointed at the timer field it already used by the progress-trigger hint
	// below, which exists for requests that named no timer field at all.
	if watchArgsIsTimer(a) {
		if a.Source != "self" {
			return errors.New("invalid_request: timers apply to source self; delegates and jobs wake you when they finish")
		}
		if a.AfterSeconds > 0 && a.RepeatSeconds > 0 {
			return errors.New("invalid_request: after_seconds and repeat_seconds are mutually exclusive")
		}
		name := "after_seconds"
		if a.RepeatSeconds > 0 {
			name = "repeat_seconds"
		}
		for _, other := range []struct {
			set  bool
			name string
		}{
			{a.ProgressIntervalMS > 0, "progress_interval_ms"},
			{a.OutputMatch != "", "output_match"},
			{len(a.Events) > 0, "events"},
			{a.Every > 0, "every"},
			{a.EventFilter != nil, "event_filter"},
			// A timer has no send rail: watchSendSnapshot and
			// isCurrentPendingWatchSendLocked rebuild the watch key without the
			// slot, so a timer's sends would be silently dropped as stale.
			{a.Send != nil, "send"},
		} {
			if other.set {
				return fmt.Errorf("invalid_request: %s and %s are mutually exclusive", name, other.name)
			}
		}
	}
	if a.Operation == "create" && a.ProgressIntervalMS > 0 && a.Source != "" && a.Source != a.Target && isWatchSessionTarget(a.Target) {
		return errors.New("invalid_request: progress_interval_ms is a job progress trigger; for a timer use repeat_seconds")
	}
	if a.ProgressIntervalMS > 0 && len(a.Events) > 0 && isWatchSessionTarget(a.Target) {
		return errors.New("invalid_request: session event watches use events/event_filter/every; progress_interval_ms is for periodic progress watches")
	}
	// Internal watch machinery retains concrete-job event predicates for
	// coalescing and delivery bookkeeping. The public job_watch create surface is
	// narrower: a shell process cannot originate assistant/communicate events, and
	// its terminal notification is delivered automatically.
	if a.Operation == "create" && strings.HasPrefix(a.Target, "job_") {
		if len(a.Events) > 0 {
			return fmt.Errorf("invalid_request: concrete shell job %q cannot use events %s; its terminal notification is automatic, so end its turn and await completion, or use output_match for its output or progress_interval_ms for periodic progress", a.Target, strings.Join(a.Events, ", "))
		}
	}
	return nil
}

func isSupportedWatchEventKind(kind events.EventKind) bool {
	for _, supported := range modelEventKinds {
		if supported == kind {
			return true
		}
	}
	return false
}

func (jm *jobManager) validateWatchTarget(target string) error {
	if isWatchSessionTarget(target) {
		return nil
	}

	jm.mu.Lock()
	run := jm.running[target]
	if isWatchableConcreteJobLocked(run) {
		jm.mu.Unlock()
		return nil
	}
	if run != nil && run.terminal != nil {
		jm.mu.Unlock()
		return watchTargetTerminalError(target, string(run.terminal.status))
	}
	if run != nil && run.finalize != nil {
		jm.mu.Unlock()
		return watchTargetTerminalError(target, "finalizing")
	}
	jm.mu.Unlock()

	recs, err := jm.store.Load()
	if err != nil {
		return err
	}
	rec := recs[target]
	if rec == nil {
		return watchTargetNotFoundError(target)
	}
	if rec.Status.IsTerminal() {
		return watchTargetTerminalError(target, string(rec.Status))
	}
	if rec.OwnerSessionID != "" && rec.OwnerSessionID != jm.sessionID {
		return fmt.Errorf("target_not_watchable: job %q is owned by nested session %q; watches must be attached from the owning session", target, rec.OwnerSessionID)
	}

	// Cross-session watch authorization (spec §5.9 target_not_watchable) is
	// settled, in two places, and neither of them is here. Nested-job
	// visibility landed: a forwarded record for a job a descendant owns is in
	// this store, so it reaches the ownership rejection above and is refused.
	// A caller entitled to that job never arrives here at all — the tool routes
	// it first, installing the watch on the OWNER's manager with this session
	// as receiver (session_tools_jobs.go's configureDescendantReceiverWatch),
	// and the authorization is the reachability walk it depends on:
	// resolveDescendantJobOwner (jobs_nested.go) descends only this session's
	// OWN live subtree, so a job owned outside it is never resolved and falls
	// through to the rejection above.
	//
	// So a target that reaches this line is either this manager's own or one
	// already authorized upstream.
	//
	// The guard on the rejection above is TestConfigureWatchRejectsForwardedNestedTarget,
	// and it is the ONLY one: terminalWatchTargetStatus carries its own
	// independent copy of the same ownership check, so
	// TestTerminalCatchupRejectsForwardedNestedTarget stays green if this one is
	// deleted. Two checks, two tests, one each -- do not read either test as
	// cover for the other rule.
	return nil
}

// terminalWatchTargetStatus reports the terminal status of a concrete job
// target, or terminal=false if the target is running, not found, or not
// readable. It mirrors validateWatchTarget's terminal detection but returns the
// raw status for catch-up rather than a formatted error.
//
// A job that is mid-finalize (run.finalize != nil, run.terminal still nil) is
// deliberately NOT catch-up-eligible: its output is still settling and its store
// status is still running, so it falls through to terminal=false and the caller
// returns the transient target_terminal "finalizing" error — the model can retry
// once finalize completes. Only a job whose runtime terminal frame is set
// (run.terminal != nil) or whose store record is terminal (rec.Status.IsTerminal,
// the common post-finalize case after the job is removed from jm.running) is
// catch-up-eligible, because only then is the retained output final.
//
// A nested-session-owned target is NOT catch-up-eligible regardless of its
// terminal state: validateWatchTarget rejects it with target_not_watchable
// ("watches must be attached from the owning session"), but checks terminality
// before ownership, so a terminal nested-owned job surfaces as target_terminal
// here. Mirroring that ownership rejection keeps catch-up from scanning or firing
// on a job the caller is forbidden to watch.
func (jm *jobManager) terminalWatchTargetStatus(target string) (status jobstore.Status, reason string, terminal bool, err error) {
	if isWatchSessionTarget(target) {
		return "", "", false, nil
	}
	jm.mu.Lock()
	run := jm.running[target]
	if run != nil && run.terminal != nil {
		s := run.terminal.status
		jm.mu.Unlock()
		return s, run.terminal.reason, true, nil
	}
	if run != nil {
		// Running or finalizing: not catch-up-eligible (see doc comment).
		jm.mu.Unlock()
		return "", "", false, nil
	}
	jm.mu.Unlock()

	recs, err := jm.store.Load()
	if err != nil {
		return "", "", false, err
	}
	rec := recs[target]
	if rec == nil {
		return "", "", false, nil
	}
	if rec.OwnerSessionID != "" && rec.OwnerSessionID != jm.sessionID {
		// Owned by a nested session: not watchable from here, so not catch-up-
		// eligible. Fall through to the original validateWatchTarget error.
		return "", "", false, nil
	}
	if rec.Status.IsTerminal() {
		return rec.Status, rec.Reason, true, nil
	}
	return "", "", false, nil
}

func (jm *jobManager) validateWatchSendTarget(target string, a watchArgs) error {
	return validateWatchSendDeliveryTarget(target, a)
}

func validateWatchSendDeliveryTarget(target string, _ watchArgs) error {
	if target == "" {
		return errors.New("invalid_request: internal watch delivery target is required")
	}
	switch target {
	case runtimeMessageAliasCaller:
		return nil
	case runtimeMessageAliasWatched:
		return errors.New("invalid_request: watched is not a v1 delivery target")
	case "main", "*":
		return watchTargetNotFoundError(target)
	}
	if strings.HasPrefix(target, "job_") {
		return errors.New("invalid_request: job_id is a job/turn handle; internal watch delivery targets delegate_id")
	}
	return watchTargetNotFoundError(target)
}

func isWatchableConcreteJobLocked(run *runningJob) bool {
	return run != nil && run.terminal == nil && run.finalize == nil
}

// errWatchTargetNotFound is the sentinel underlying a watch target_not_found. The
// watch tool checks for it (errors.Is) so it can enrich a miss with the
// delegate-the-watching guidance when the missed target is actually a known
// descendant the caller cannot directly watch (spec §3/§8).
var errWatchTargetNotFound = errors.New("target_not_found")

func watchTargetNotFoundError(target string) error {
	return fmt.Errorf("%w: job %q not found", errWatchTargetNotFound, target)
}

func watchTargetTerminalError(target, status string) error {
	return fmt.Errorf("target_terminal: job %q is %s; watches can only attach to running jobs", target, status)
}

func isWatchSessionTarget(target string) bool {
	switch target {
	case runtimeMessageAliasCaller, "*":
		return true
	default:
		return false
	}
}

func newWatchConfig(a watchArgs, createdAt time.Time, slot string) (*watchConfig, error) {
	applyStableReceiverWatchSend(&a)
	sourceDelegateID := stableWatchSourceID(a)
	eventKinds, wildcardEvents := resolveEventKinds(a.Events)
	watchID := slot
	if watchID == "" {
		watchID = jobstore.NewWatchID()
	}
	cfg := &watchConfig{
		id:                 watchID,
		watchID:            watchID,
		configHash:         normalizedWatchConfigHash(a),
		sourcePublic:       watchPublicSource(a.Source, a.Target),
		receiverSessionID:  strings.TrimSpace(a.ReceiverSessionID),
		receiverDelegateID: strings.TrimSpace(a.ReceiverDelegateID),
		receiverNotify:     a.ReceiverNotify,
		receiverHoldWake:   a.ReceiverHoldWake,
		target:             a.Target,
		outputMatch:        a.OutputMatch,
		progressIntervalMS: a.ProgressIntervalMS,
		slot:               slot,
		timer:              watchArgsIsTimer(a),
		oneShot:            a.AfterSeconds > 0,
		timerSeconds:       max(a.AfterSeconds, a.RepeatSeconds),
		note:               a.Note,
		events:             canonicalWatchEvents(a.Events),
		eventKinds:         eventKinds,
		wildcardEvents:     wildcardEvents,
		eventFilter:        cloneWatchEventFilter(a.EventFilter),
		send:               cloneWatchSendArgs(a.Send),
		generation:         jobstore.NewWatchGeneration(),
		createdAt:          createdAt,
		sourceDelegateID:   sourceDelegateID,
		sourceGeneration:   a.SourceGeneration,
		stableReceiver:     a.StableReceiver,
	}
	if cfg.timer {
		cfg.progressIntervalMS = cfg.timerSeconds * 1000
	}
	// every is valid only with exactly one event kind (enforced by validation).
	if a.Every > 0 && len(a.Events) == 1 {
		cfg.triggerKind = modelEventKinds[a.Events[0]]
		cfg.triggerEvery = a.Every
	}
	if a.OutputMatch != "" {
		re, err := jobstore.CompileOutputMatch(a.OutputMatch)
		if err != nil {
			return nil, fmt.Errorf("invalid_request: output_match: %w", err)
		}
		cfg.outputMatcher = jobstore.NewOutputMatcher(re)
	}
	return cfg, nil
}

func applyStableReceiverWatchSend(a *watchArgs) {
	if a == nil || a.Send != nil {
		return
	}
	if a.StableReceiver {
		a.Send = &watchSendArgs{To: stableWatchReceiverTarget}
		a.ReceiverSendInternal = true
	}
}

func normalizedWatchConfigHash(a watchArgs) string {
	applyStableReceiverWatchSend(&a)
	snapshot := jobstore.WatchConfigSnapshot{
		Target:                   a.Target,
		OutputMatch:              a.OutputMatch,
		ProgressIntervalMS:       a.ProgressIntervalMS,
		Events:                   canonicalWatchEvents(a.Events),
		Every:                    a.Every,
		EventFilter:              watchEventFilterSnapshot(a.EventFilter),
		ReceiverSessionID:        strings.TrimSpace(a.ReceiverSessionID),
		ReceiverDelegateID:       strings.TrimSpace(a.ReceiverDelegateID),
		Source:                   stableWatchSourceSnapshot(a),
		SourceDelegateID:         stableWatchSourceID(a),
		SourceDelegateGeneration: a.SourceGeneration,
		StableReceiver:           a.StableReceiver,
		// The timer's own fields, not the interval they derive: a one-shot and a
		// repeating timer that fire the same number of seconds apart are still
		// two different watches, and so are two repeats with different notes.
		AfterSeconds:  a.AfterSeconds,
		RepeatSeconds: a.RepeatSeconds,
		Note:          a.Note,
	}
	if a.Send != nil {
		snapshot.SendTo = a.Send.To
		snapshot.SendMessage = a.Send.Message
		snapshot.IncludeExcerpt = a.Send.IncludeExcerpt
	}
	// snapshot contains only strings, ints, bools, and slices of strings, so the
	// standard encoder cannot fail. The receiver fields are hashed in place rather
	// than beside the snapshot: they are part of the same configured identity, and
	// because they sit last in the struct the encoding is byte-identical to the
	// wrapper form, so hashes already written to jobs.jsonl still match.
	b, _ := json.Marshal(snapshot)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func stableWatchSourceSnapshot(a watchArgs) string {
	if stableWatchSourceID(a) == "" && !a.StableReceiver {
		return ""
	}
	return strings.TrimSpace(a.Source)
}

func stableWatchSourceID(a watchArgs) string {
	if id := strings.TrimSpace(a.SourceDelegateID); id != "" {
		return id
	}
	source := strings.TrimSpace(a.Source)
	if strings.HasPrefix(source, "dlg_") {
		return source
	}
	return ""
}

func resolveEventKinds(names []string) (map[events.EventKind]bool, bool) {
	resolved := make(map[events.EventKind]bool)
	wildcard := false
	for _, name := range names {
		if name == "*" {
			wildcard = true
			continue
		}
		kind, ok := modelEventKinds[name]
		if !ok {
			continue
		}
		resolved[kind] = true
	}
	return resolved, wildcard
}

func canonicalWatchEvents(events []string) []string {
	if len(events) == 0 {
		return nil
	}
	out := append([]string(nil), events...)
	sort.Strings(out)
	// Duplicates collapse: matching is by kind, so a repeated name would add
	// nothing but payload to every projection of the watch. This runs after
	// validation, so the single-concrete-kind rules still read the caller's own
	// list.
	return slices.Compact(out)
}

func cloneWatchEventFilter(filter *watchEventFilter) *watchEventFilter {
	if filter == nil {
		return nil
	}
	clone := *filter
	return &clone
}

func watchEventFilterSnapshot(filter *watchEventFilter) *jobstore.WatchEventFilterSnapshot {
	if filter == nil {
		return nil
	}
	return &jobstore.WatchEventFilterSnapshot{
		ToolName: filter.ToolName,
		Status:   filter.Status,
	}
}

func cloneWatchSendArgs(send *watchSendArgs) *watchSendArgs {
	if send == nil {
		return nil
	}
	clone := *send
	return &clone
}

func (jm *jobManager) watchSendSnapshot(cfg *watchConfig, jobID, trigger string, root events.SessionEvent) watchSendDelivery {
	sendTo := ""
	if cfg.send != nil {
		sendTo = cfg.send.To
	}
	cfg.nextUpdateSeq++
	deliveryID := jobstore.NewWatchSendDeliveryID()
	return watchSendDelivery{
		cfg:               cfg,
		key:               watchKey{VisibleSessionID: jm.sessionID, Target: cfg.target, SendTo: sendTo, ReceiverSessionID: cfg.receiverSessionID, ReceiverDelegateID: cfg.receiverDelegateID},
		generation:        cfg.generation,
		updateSeq:         cfg.nextUpdateSeq,
		send:              cloneWatchSendArgs(cfg.send),
		deliveryID:        deliveryID,
		visibleSessionID:  jm.sessionID,
		watchTarget:       cfg.target,
		watchedIdentity:   jobID,
		trigger:           trigger,
		triggerProvenance: provenance.Clone(root.Provenance),
		provenance:        provenance.WithWatch(root.Provenance, cfg.watchID, cfg.generation, deliveryID, root.SessionID, jobID),
		eventKind:         root.Kind,
		eventData:         root.Data,
	}
}

// jobProvenanceForWatch returns a clone of the watched job's recorded causal
// provenance, the base for a synthetic delivery root on the non-session-event
// fire paths (output_match, attach scan, terminal catch-up, terminal flush,
// progress tick) where there is no triggering SessionEvent to carry it. It is
// self-locking and reads the store on a fallback, so callers MUST NOT hold
// jm.mu. A session-target jobID (or an unknown job) has no job provenance and
// returns nil.
func jobProvenanceForWatch(jm *jobManager, jobID string) *provenance.Causal {
	if jm == nil || jobID == "" || isWatchSessionTarget(jobID) {
		return nil
	}
	jm.mu.Lock()
	if run := jm.running[jobID]; run != nil && run.rec != nil {
		p := provenance.Clone(run.rec.Provenance)
		jm.mu.Unlock()
		return p
	}
	jm.mu.Unlock()
	recs, err := jm.store.Load()
	if err != nil {
		return nil
	}
	if rec := recs[jobID]; rec != nil {
		return provenance.Clone(rec.Provenance)
	}
	return nil
}

type watchSendLoader func() (jobstore.WatchSendRecord, error)

func (jm *jobManager) restoreWatchSendPending() error {
	return jm.restoreWatchSendPendingFrom(jm.store.LoadWatchSends)
}

func (jm *jobManager) restoreWatchSendPendingFrom(load watchSendLoader) error {
	rec, err := load()
	if err != nil {
		return err
	}
	if len(rec.Pending) == 0 {
		return nil
	}
	retired, err := jm.retiredDelegateJobIDs()
	if err != nil {
		return err
	}
	states := make([]*jobstore.WatchSendState, 0, len(rec.Pending))
	for _, state := range rec.Pending {
		if state == nil || addressedThroughRetiredDelegate(state.Key, retired) {
			continue
		}
		copied := *state
		states = append(states, &copied)
	}
	sort.SliceStable(states, func(i, j int) bool {
		return watchSendStateLess(states[i], states[j])
	})

	cfgs := make(map[restoredWatchConfigKey]*watchConfig)
	for _, state := range states {
		key := state.Key
		cfgKey := restoredWatchConfigKey{
			visibleSessionID:   key.VisibleSessionID,
			watchID:            key.WatchID,
			target:             key.WatchTarget,
			sendTo:             key.ResolvedSendTo,
			receiverSessionID:  strings.TrimSpace(state.ReceiverSessionID),
			receiverDelegateID: strings.TrimSpace(state.ReceiverDelegateID),
			generation:         key.WatchGeneration,
			sourceDelegateID:   strings.TrimSpace(state.SourceDelegateID),
			sourceGeneration:   state.SourceDelegateGeneration,
			stableReceiver:     state.StableReceiver,
		}
		cfg := cfgs[cfgKey]
		if cfg == nil {
			sourcePublic := watchPublicSource(state.SourceDelegateID, key.WatchTarget)
			if cfgKey.sourceDelegateID == "" && cfgKey.receiverSessionID != "" && key.WatchTarget == runtimeMessageAliasCaller {
				sourcePublic = "parent"
			}
			cfg = &watchConfig{
				id:                 key.WatchID,
				watchID:            key.WatchID,
				sourcePublic:       sourcePublic,
				receiverSessionID:  cfgKey.receiverSessionID,
				receiverDelegateID: cfgKey.receiverDelegateID,
				target:             key.WatchTarget,
				send:               &watchSendArgs{To: key.ResolvedSendTo},
				generation:         key.WatchGeneration,
				pending:            make(map[jobstore.WatchSendKey]*jobstore.WatchSendState),
				sourceDelegateID:   cfgKey.sourceDelegateID,
				sourceGeneration:   cfgKey.sourceGeneration,
				stableReceiver:     cfgKey.stableReceiver,
			}
			cfgs[cfgKey] = cfg
		}
		pending := *state
		cfg.pending[key] = &pending
		cfg.pendingOrder = append(cfg.pendingOrder, key)
		if pending.UpdateSeq > cfg.nextUpdateSeq {
			cfg.nextUpdateSeq = pending.UpdateSeq
		}
	}
	if jm.terminalFlush == nil {
		jm.terminalFlush = make(map[*watchConfig]bool)
	}
	for _, cfg := range cfgs {
		if len(cfg.pending) != 0 {
			jm.terminalFlush[cfg] = true
		}
	}
	return nil
}

func watchSendStateLess(a, b *jobstore.WatchSendState) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	if !a.UpdatedAt.Equal(b.UpdatedAt) {
		return a.UpdatedAt.Before(b.UpdatedAt)
	}
	if a.UpdateSeq != b.UpdateSeq {
		return a.UpdateSeq < b.UpdateSeq
	}
	return watchSendKeyLess(a.Key, b.Key)
}

func watchSendKeyLess(a, b jobstore.WatchSendKey) bool {
	if a.VisibleSessionID != b.VisibleSessionID {
		return a.VisibleSessionID < b.VisibleSessionID
	}
	if a.WatchTarget != b.WatchTarget {
		return a.WatchTarget < b.WatchTarget
	}
	if a.ResolvedWatchedIdentity != b.ResolvedWatchedIdentity {
		return a.ResolvedWatchedIdentity < b.ResolvedWatchedIdentity
	}
	if a.ResolvedSendTo != b.ResolvedSendTo {
		return a.ResolvedSendTo < b.ResolvedSendTo
	}
	if a.WatchID != b.WatchID {
		return a.WatchID < b.WatchID
	}
	return a.WatchGeneration < b.WatchGeneration
}

func (jm *jobManager) clearWatch(key watchKey) (watchResult, error) {
	defer jm.publishWatchActivity()
	var targets []watchConfigTerminalSnapshot
	jm.mu.Lock()
	detachedCfgs, detached := jm.detachedWatchSendTerminalSnapshotsLocked(key, jobstore.EventWatchSendDropped, "watch cleared", jm.now())
	if key.SendTo != "" {
		cfg := jm.watches[key]
		targets = append(targets, watchConfigTerminalSnapshot{
			key:       key,
			cfg:       cfg,
			terminal:  watchSendTerminalSnapshotsLocked(cfg, jobstore.EventWatchSendDropped, "watch cleared", jm.now()),
			endReason: "cleared",
		})
	} else {
		for existingKey, cfg := range jm.watches {
			if watchKeyMatchesClearRequest(existingKey, key) {
				targets = append(targets, watchConfigTerminalSnapshot{
					key:       existingKey,
					cfg:       cfg,
					terminal:  watchSendTerminalSnapshotsLocked(cfg, jobstore.EventWatchSendDropped, "watch cleared", jm.now()),
					endReason: "cleared",
				})
			}
		}
	}
	markWatchConfigSnapshotsRejectingLocked(targets)
	markWatchConfigsRejectingLocked(detachedCfgs)
	jm.mu.Unlock()
	dropped := append(terminalSnapshots(targets), detached...)
	if err := jm.appendWatchTeardownBatch(dropped, targets); err != nil {
		jm.rollbackWatchConfigSnapshotsRejecting(targets)
		jm.rollbackWatchConfigsRejecting(detachedCfgs)
		return watchResult{}, err
	}
	jm.detachWatchConfigSnapshots(targets)
	jm.removeWatchSendTerminalSnapshots(dropped)
	jm.forgetDetachedWatchSendConfigsIfEmpty(detachedCfgs)

	return watchResult{
		Source:   sourcePublicForClearedWatch(key, targets),
		Target:   key.Target,
		Watching: false,
	}, nil
}

func (jm *jobManager) clearWatchByID(watchID string) (watchResult, error) {
	return jm.clearWatchByIDMatching(watchID, func(cfg *watchConfig) bool {
		return watchConfigVisibleToSession(cfg, jm.sessionID)
	}, true)
}

func (jm *jobManager) clearWatchByIDMatching(watchID string, allow func(*watchConfig) bool, allowDurable bool) (watchResult, error) {
	return jm.clearWatchByIDMatchingWithReason(watchID, allow, allowDurable, "cleared")
}

// clearWatchByIDMatchingWithReason is the teardown clearWatchByIDMatching runs,
// with the end reason recorded in the durable clear event and the history ring
// as a parameter: a one-shot timer retires through the same sequence but is
// recorded as "fired" rather than "cleared".
func (jm *jobManager) clearWatchByIDMatchingWithReason(watchID string, allow func(*watchConfig) bool, allowDurable bool, endReason string) (watchResult, error) {
	defer jm.publishWatchActivity()
	jm.mu.Lock()
	key, cfg, ok := jm.watchConfigByIDLocked(watchID)
	if ok && !allow(cfg) {
		jm.mu.Unlock()
		return watchResult{WatchID: watchID, Watching: false}, nil
	}
	detachedCfgs, detached, hiddenDetached := jm.detachedWatchSendTerminalSnapshotsByWatchIDLocked(watchID, jobstore.EventWatchSendDropped, "watch cleared", jm.now(), allow)
	markWatchConfigsRejectingLocked(detachedCfgs)
	if !ok {
		jm.mu.Unlock()
		if hiddenDetached || !allowDurable {
			if len(detachedCfgs) == 0 {
				return watchResult{WatchID: watchID, Watching: false}, nil
			}
		}
		var clearEvent *jobstore.Event
		var clearReceiver string
		if allowDurable {
			var err error
			clearEvent, clearReceiver, err = jm.durableWatchClearEvent(watchID, endReason)
			if err != nil {
				jm.rollbackWatchConfigsRejecting(detachedCfgs)
				return watchResult{}, err
			}
		}
		if len(detachedCfgs) == 0 && clearEvent == nil {
			return watchResult{WatchID: watchID, Watching: false}, nil
		}
		dropped := detached
		events := watchSendTerminalEvents(dropped)
		if clearEvent != nil {
			events = append(events, *clearEvent)
		}
		if err := jm.appendWatchRegistryEvents(events); err != nil {
			jm.rollbackWatchConfigsRejecting(detachedCfgs)
			return watchResult{}, err
		}
		if clearEvent != nil {
			jm.noteWatchActivity(clearReceiver)
		}
		jm.removeWatchSendTerminalSnapshots(dropped)
		jm.forgetDetachedWatchSendConfigsIfEmpty(detachedCfgs)
		return watchResult{WatchID: watchID, Watching: false}, nil
	}
	targets := []watchConfigTerminalSnapshot{{
		key:       key,
		cfg:       cfg,
		terminal:  watchSendTerminalSnapshotsLocked(cfg, jobstore.EventWatchSendDropped, "watch cleared", jm.now()),
		endReason: endReason,
	}}
	markWatchConfigSnapshotsRejectingLocked(targets)
	jm.mu.Unlock()

	dropped := append(terminalSnapshots(targets), detached...)
	if err := jm.appendWatchTeardownBatch(dropped, targets); err != nil {
		jm.rollbackWatchConfigSnapshotsRejecting(targets)
		jm.rollbackWatchConfigsRejecting(detachedCfgs)
		return watchResult{}, err
	}
	jm.detachWatchConfigSnapshots(targets)
	jm.removeWatchSendTerminalSnapshots(dropped)
	jm.forgetDetachedWatchSendConfigsIfEmpty(detachedCfgs)

	return watchResult{
		WatchID:  watchID,
		Source:   cfg.sourcePublic,
		Target:   key.Target,
		Watching: false,
	}, nil
}

// watchConfigByIDWithFlushLocked resolves a watch config by ID, including
// detached terminal-flush configs. Callers must hold jm.mu.
func (jm *jobManager) watchConfigByIDWithFlushLocked(watchID string) (*watchConfig, bool) {
	if _, cfg, ok := jm.watchConfigByIDLocked(watchID); ok {
		return cfg, true
	}
	for cfg := range jm.terminalFlush {
		if cfg != nil && cfg.watchID == watchID {
			return cfg, true
		}
	}
	return nil, false
}

// watchReceiverIdentity looks a watch up by ID ignoring session visibility —
// unlike hasWatchID, which returns the visibility verdict. Returns the watch's
// receiver identity so a session holding a receiver-keyed watch it cannot see
// can decide, from tree topology, whether it may route that watch's clear to
// the receiver.
func (jm *jobManager) watchReceiverIdentity(watchID string) (receiverSessionID, receiverDelegateID string, found bool) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	cfg, found := jm.watchConfigByIDWithFlushLocked(watchID)
	if !found || cfg == nil {
		return "", "", false
	}
	return cfg.receiverSessionID, cfg.receiverDelegateID, true
}

func (jm *jobManager) hasWatchID(watchID string) (bool, error) {
	jm.mu.Lock()
	if _, cfg, ok := jm.watchConfigByIDLocked(watchID); ok {
		jm.mu.Unlock()
		return watchConfigVisibleToSession(cfg, jm.sessionID), nil
	}
	for cfg := range jm.terminalFlush {
		if cfg != nil && cfg.watchID == watchID {
			jm.mu.Unlock()
			return watchConfigVisibleToSession(cfg, jm.sessionID), nil
		}
	}
	jm.mu.Unlock()

	watches, err := jm.store.LoadWatches()
	if err != nil {
		return false, err
	}
	w := watches[watchID]
	return w != nil && w.Active, nil
}

func (jm *jobManager) clearReceiverWatchByID(watchID, receiverSessionID, receiverDelegateID string) (watchResult, error) {
	receiverSessionID = strings.TrimSpace(receiverSessionID)
	receiverDelegateID = strings.TrimSpace(receiverDelegateID)
	if receiverSessionID == "" {
		return watchResult{}, errors.New("source_not_watchable: parent watch observer session is unknown")
	}

	jm.mu.Lock()
	_, cfg, ok := jm.watchConfigByIDLocked(watchID)
	if !ok {
		for terminalCfg := range jm.terminalFlush {
			if terminalCfg != nil && terminalCfg.watchID == watchID {
				cfg = terminalCfg
				ok = true
				break
			}
		}
	}
	if !ok {
		jm.mu.Unlock()
		return watchResult{WatchID: watchID, Watching: false}, nil
	}
	if cfg.receiverSessionID != receiverSessionID || cfg.receiverDelegateID != receiverDelegateID {
		jm.mu.Unlock()
		return watchResult{WatchID: watchID, Watching: false}, nil
	}
	jm.mu.Unlock()
	return jm.clearWatchByIDMatching(watchID, func(cfg *watchConfig) bool {
		return watchConfigMatchesReceiver(cfg, receiverSessionID, receiverDelegateID)
	}, false)
}

func sourcePublicForClearedWatch(key watchKey, targets []watchConfigTerminalSnapshot) string {
	for _, target := range targets {
		if watchKeyMatchesClearRequest(target.key, key) && target.cfg != nil && target.cfg.sourcePublic != "" {
			return target.cfg.sourcePublic
		}
	}
	return ""
}

func watchKeyMatchesClearRequest(candidate, request watchKey) bool {
	if candidate.Slot != request.Slot {
		return false
	}
	if candidate.VisibleSessionID != request.VisibleSessionID || candidate.Target != request.Target {
		return false
	}
	if request.SendTo != "" && candidate.SendTo != request.SendTo {
		return false
	}
	if request.ReceiverSessionID != "" && candidate.ReceiverSessionID != request.ReceiverSessionID {
		return false
	}
	if request.ReceiverDelegateID != "" && candidate.ReceiverDelegateID != request.ReceiverDelegateID {
		return false
	}
	return true
}

func (jm *jobManager) durableWatchClearEvent(watchID, endReason string) (*jobstore.Event, string, error) {
	watches, err := jm.store.LoadWatches()
	if err != nil {
		return nil, "", err
	}
	w := watches[watchID]
	if w == nil || !w.Active || w.Generation == "" {
		return nil, "", nil
	}
	return &jobstore.Event{
		Kind:    jobstore.EventWatchCleared,
		TS:      jm.now(),
		WatchID: watchID,
		Watch: &jobstore.WatchEvent{
			Generation: w.Generation,
			EndReason:  endReason,
		},
	}, w.ReceiverSessionID, nil
}

// tripConditionFireBudgetLocked latches the condition-fire circuit breaker for
// cfg and reports whether THIS call tripped it. The test is "at or past the
// budget", latched by budgetTripped to exactly one true per cfg lifetime.
// Equality would be wrong for the send rail: a send watch counts its fire when
// it SNAPSHOTS a frame and counts its delivery when that frame SETTLES, so two
// fires snapshotted before either settles walk conditionFires from 49 to 51 and
// every later call would compare a counter that already stepped over the
// budget.
//
// Both ends of the send rail consult it: the observation side, where the match
// is counted, so a watch whose frames never settle still trips; and the settle
// side, which keeps its delivery accounting. The latch is what makes the second
// of those a no-op rather than a second teardown.
//
// The caller must hold jm.mu. A true result means the caller should schedule
// autoClearWatchOverBudget(cfg) AFTER releasing jm.mu (the auto-clear does
// durable I/O and re-takes jm.mu; it must never run from inside an
// observation's critical section, spec §3).
func tripConditionFireBudgetLocked(cfg *watchConfig) (crossedBudget bool) {
	if cfg == nil || cfg.conditionFires < watchDeliveryBudget || cfg.budgetTripped {
		return false
	}
	cfg.budgetTripped = true
	return true
}

// noteConditionFireLocked offers one condition match to cfg's breaker. It is the
// ONLY place conditionFires moves: every match site — the event rail, the live
// output rail, and both attach-scan rails — goes through it, so no path can
// count a fire without consulting the breaker.
//
// accepted=false means the breaker has already latched and the caller must build
// NOTHING from this match. The window it closes is the teardown's own durable
// append: the config is not detached until that append returns, so it is still
// in jm.watches and still matching. rejectingDelivery stops the send rail at the
// delivery gate, but a no-send watch's notification has no such gate, so the
// refusal has to happen where the match is counted. The match that crosses the
// budget is itself accepted — the budget is a count of allowed fires, and that
// one is the last of them.
//
// crossedBudget=true means the caller must schedule autoClearWatchOverBudget(cfg)
// after releasing jm.mu, on tripConditionFireBudgetLocked's terms. The caller
// must hold jm.mu.
func noteConditionFireLocked(cfg *watchConfig) (accepted, crossedBudget bool) {
	if cfg == nil || cfg.budgetTripped {
		return false, false
	}
	cfg.conditionFires++
	return true, tripConditionFireBudgetLocked(cfg)
}

// countWatchDeliveryLocked increments the model-facing delivery count for cfg
// and appends this delivery's instant to its bounded timeline ring, stamped
// from the job manager's clock. Counting and stamping happen together here so
// the count and the ring can never diverge. It says nothing about the breaker:
// the budget bounds CONDITION fires, so a periodic progress tick counts a
// delivery here and never trips anything, and a condition fire's crossing is
// reported by noteConditionFireLocked at the match. The caller must hold jm.mu.
func (jm *jobManager) countWatchDeliveryLocked(cfg *watchConfig) {
	if cfg == nil {
		return
	}
	cfg.deliveries++
	jm.noteWatchActivity(cfg.receiverSessionID)
	now := jm.now()
	if len(cfg.deliveryTimes) == watchDeliveryTimeCap {
		copy(cfg.deliveryTimes, cfg.deliveryTimes[1:])
		cfg.deliveryTimes[watchDeliveryTimeCap-1] = now
		return
	}
	cfg.deliveryTimes = append(cfg.deliveryTimes, now)
}

// recordWatchDeliveryLocked counts a delivery at the send rail's settle end and
// reports whether it crossed the condition-fire budget. The send rail counts its
// fire when it snapshots a frame and its delivery when that frame settles, so
// this is the second of the two ends that can report the crossing; the latch
// keeps the pair to one teardown. The caller must hold jm.mu.
func (jm *jobManager) recordWatchDeliveryLocked(cfg *watchConfig) (crossedBudget bool) {
	jm.countWatchDeliveryLocked(cfg)
	return tripConditionFireBudgetLocked(cfg)
}

// watchKeyForConfigLocked finds the live map key holding cfg. ok=false means cfg
// is no longer in jm.watches (already cleared, replaced, or detached). The caller
// must hold jm.mu.
func watchKeyForConfigLocked(jm *jobManager, cfg *watchConfig) (watchKey, bool) {
	for key, c := range jm.watches {
		if c == cfg {
			return key, true
		}
	}
	return watchKey{}, false
}

// autoClearWatchOverBudgetNotification tears down exactly the one watch config
// that tripped the delivery budget and returns its ONE final cleared notification
// without enqueuing or waking. It is the circuit breaker's teardown: jm-state
// mutation only — NO delivery from observation (spec §3). It works on the single
// (key, cfg) pair the breaker latched, so a watch sharing a target with others
// does not over-clear its neighbors.
//
// The sequence is mark, persist, detach. It takes no terminal snapshot and drops
// nothing, which is what separates it from a model-requested clear: the budget
// bounds how many times a watch may FIRE, and a frame it already produced —
// including the one whose match crossed the budget, which every rail records
// before calling this — is persisted as pending and owed to its receiver. All
// that is persisted here is the watch's own cleared event; the detached config
// keeps its frames and goes onto the terminal-flush rail, exactly where a watch
// auto-removed by its target's exit parks them. rejectingDelivery is the freeze
// that holds across the durable append, so no fire past the crossing can add a
// frame between the mark and the detach; detachOverBudgetWatch lifts it.
//
// The reverse lookup under jm.mu doubles as the no-double-fire latch: once the
// cfg is detached, a later in-flight settle that increments past the budget
// finds no live key and returns without re-notifying.
func (jm *jobManager) autoClearWatchOverBudgetNotification(cfg *watchConfig) (jobNotification, bool) {
	defer jm.publishWatchActivity()
	jm.mu.Lock()
	key, ok := watchKeyForConfigLocked(jm, cfg)
	if !ok {
		jm.mu.Unlock()
		return jobNotification{}, false
	}
	targets := []watchConfigTerminalSnapshot{{key: key, cfg: cfg, endReason: "budget_exhausted"}}
	markWatchConfigSnapshotsRejectingLocked(targets)
	jm.mu.Unlock()

	if err := jm.appendWatchTeardownBatch(nil, targets); err != nil {
		jm.rollbackWatchBudgetTeardown(targets, cfg)
		return jobNotification{}, false
	}
	jm.detachOverBudgetWatch(targets[0])

	return jm.watchNotificationFromWatch(cfg, "", watchBudgetClearedMessage(cfg.target), nil), true
}

// detachOverBudgetWatch removes the over-budget config from the live set and, in
// the SAME critical section, releases it onto the terminal-flush rail, where
// drains, restore, and clear-by-id can still see and settle the frames it
// already produced. The rejecting mark comes off HERE and not earlier: it is
// what froze the config while the teardown persisted, so no fire past the
// crossing could slip a frame in, and a config still marked rejecting delivers
// nothing. Detached, it can gain no new frames — a delivery snapshotted before
// the teardown no longer resolves to a live watch. A config with nothing pending
// is left off the rail: terminalFlush holds flushing configs, not ended ones.
//
// The release is gated on this call being the one that detached the config, and
// shares its critical section for the same reason. A model-requested clear that
// won the key owns that config: it has already dropped those frames under its
// own rejecting mark, and unmarking it here would hand a config that is in
// neither jm.watches nor terminalFlush back to a fire still in flight.
func (jm *jobManager) detachOverBudgetWatch(target watchConfigTerminalSnapshot) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if !jm.detachWatchConfigSnapshotLocked(target) {
		return
	}
	cfg := target.cfg
	cfg.rejectingDelivery = false
	if len(cfg.pending) == 0 {
		return
	}
	jm.rememberDetachedPendingLocked(cfg)
}

// rollbackWatchBudgetTeardown undoes a budget teardown that did not persist:
// the rejecting marks come off and the once-only budget latch is re-armed. The
// rollback leaves the watch live and still over budget, so the latch has to come
// off with the rejecting marks — held set, no later condition fire would report
// a crossing and nothing would ever retry the auto-clear. Both flip in ONE jm.mu
// critical section: a condition fire that landed between two acquisitions would
// find delivery re-enabled with the latch still set and skip its own teardown.
// Only the failed-teardown path calls this; a teardown that lands detaches the
// config and the latch goes with it.
func (jm *jobManager) rollbackWatchBudgetTeardown(targets []watchConfigTerminalSnapshot, cfg *watchConfig) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	rollbackWatchBudgetTeardownLocked(jm, targets, cfg)
}

// rollbackWatchBudgetTeardownLocked is rollbackWatchBudgetTeardown's body. The
// caller must hold jm.mu.
func rollbackWatchBudgetTeardownLocked(jm *jobManager, targets []watchConfigTerminalSnapshot, cfg *watchConfig) {
	rollbackWatchConfigSnapshotsRejectingLocked(jm, targets)
	if cfg != nil {
		cfg.budgetTripped = false
	}
}

// autoClearWatchOverBudget is the standalone wrapper for attach scans and watch
// sends, where there is no same-event notification batch to extend.
func (jm *jobManager) autoClearWatchOverBudget(cfg *watchConfig) {
	notification, ok := jm.autoClearWatchOverBudgetNotification(cfg)
	if !ok {
		return
	}
	jm.enqueueWatchNotifications([]jobNotification{notification})
	jm.kick()
}

// autoClearOverBudgetWatches runs autoClearWatchOverBudget for each cfg that
// crossed the budget during a single observation pass. Call it AFTER releasing
// the observation's jm.mu critical section, alongside enqueueWatchNotifications.
func (jm *jobManager) autoClearOverBudgetWatches(cfgs []*watchConfig) {
	for _, cfg := range cfgs {
		jm.autoClearWatchOverBudget(cfg)
	}
}

func (jm *jobManager) hasWatchClearState(key watchKey) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if key.SendTo != "" {
		if jm.watches[key] != nil {
			return true
		}
	} else {
		for existingKey := range jm.watches {
			if watchKeyMatchesClearRequest(existingKey, key) {
				return true
			}
		}
	}
	for cfg := range jm.terminalFlush {
		if watchConfigHasPendingMatchingKey(cfg, key) {
			return true
		}
	}
	return false
}

func watchConfigHasPendingMatchingKey(cfg *watchConfig, key watchKey) bool {
	if cfg == nil || len(cfg.pending) == 0 {
		return false
	}
	if !watchConfigReceiverMatchesWatchKey(cfg, key) {
		return false
	}
	for pendingKey := range cfg.pending {
		if watchSendKeyMatchesWatchKey(pendingKey, key) {
			return true
		}
	}
	return false
}

// liveTimerCountLocked counts installed timer watches. Caller holds jm.mu.
func (jm *jobManager) liveTimerCountLocked() int {
	n := 0
	for _, cfg := range jm.watches {
		if cfg.timer {
			n++
		}
	}
	return n
}

func (jm *jobManager) watchConfigByIDLocked(watchID string) (watchKey, *watchConfig, bool) {
	for key, cfg := range jm.watches {
		if cfg != nil && cfg.watchID == watchID {
			return key, cfg, true
		}
	}
	return watchKey{}, nil, false
}

func (jm *jobManager) pruneWatchedTargetWatchesLocked(jobID, reason string, now time.Time) []watchConfigTerminalSnapshot {
	var targets []watchConfigTerminalSnapshot
	for key, cfg := range jm.watches {
		if key.Target != jobID {
			continue
		}
		targets = append(targets, watchConfigTerminalSnapshot{
			key:      key,
			cfg:      cfg,
			terminal: watchSendTerminalSnapshotsLocked(cfg, jobstore.EventWatchSendDropped, reason, now),
		})
	}
	markWatchConfigSnapshotsRejectingLocked(targets)
	return targets
}

func (cfg *watchConfig) initProgressStop() chan struct{} {
	if cfg.progressIntervalMS > 0 {
		cfg.progressStop = make(chan struct{})
	}
	return cfg.progressStop
}

func closeWatchConfig(cfg *watchConfig) {
	if cfg == nil || cfg.progressStop == nil {
		return
	}
	close(cfg.progressStop)
	cfg.progressStop = nil
}

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

func watchConfigSnapshot(cfg *watchConfig) *jobstore.WatchConfigSnapshot {
	if cfg == nil {
		return nil
	}
	snapshot := &jobstore.WatchConfigSnapshot{
		Target:                   cfg.target,
		OutputMatch:              cfg.outputMatch,
		ProgressIntervalMS:       cfg.progressIntervalMS,
		Events:                   append([]string(nil), cfg.events...),
		Every:                    cfg.triggerEvery,
		EventFilter:              watchEventFilterSnapshot(cfg.eventFilter),
		ReceiverSessionID:        cfg.receiverSessionID,
		ReceiverDelegateID:       cfg.receiverDelegateID,
		Source:                   cfg.sourcePublic,
		SourceDelegateID:         cfg.sourceDelegateID,
		SourceDelegateGeneration: cfg.sourceGeneration,
		StableReceiver:           cfg.stableReceiver,
		Note:                     cfg.note,
	}
	// A timer keeps only the seconds it derived and which mode derived them, so
	// the snapshot rebuilds the field the model actually named: inspect and
	// history have to be able to say "fires once in 10 minutes" apart from
	// "fires every 10 minutes".
	if cfg.timer {
		if cfg.oneShot {
			snapshot.AfterSeconds = cfg.timerSeconds
		} else {
			snapshot.RepeatSeconds = cfg.timerSeconds
		}
	}
	if cfg.send != nil {
		snapshot.SendTo = cfg.send.To
		snapshot.SendMessage = cfg.send.Message
		snapshot.IncludeExcerpt = cfg.send.IncludeExcerpt
	}
	return snapshot
}

func (jm *jobManager) appendWatchRegisteredEvent(cfg *watchConfig) error {
	return jm.appendWatchRegistryEvents([]jobstore.Event{watchRegisteredEvent(jm, cfg)})
}

func watchRegisteredEvent(jm *jobManager, cfg *watchConfig) jobstore.Event {
	return jobstore.Event{
		Kind:    jobstore.EventWatchRegistered,
		TS:      jm.now(),
		WatchID: cfg.watchID,
		Watch: &jobstore.WatchEvent{
			Generation:       cfg.generation,
			OwnerSessionID:   jm.sessionID,
			VisibleSessionID: jm.sessionID,
			Target:           cfg.target,
			SendTo:           watchSendTo(cfg),
			ConfigHash:       cfg.configHash,
			Condition:        watchConditionSummary(cfg),
			Config:           watchConfigSnapshot(cfg),
		},
	}
}

func watchClearedEvent(jm *jobManager, cfg *watchConfig, endReason string) jobstore.Event {
	return jobstore.Event{
		Kind:    jobstore.EventWatchCleared,
		TS:      jm.now(),
		WatchID: cfg.watchID,
		Watch: &jobstore.WatchEvent{
			Generation: cfg.generation,
			EndReason:  endReason,
		},
	}
}

func (jm *jobManager) appendWatchReplacementBatch(cfg *watchConfig, targets []watchConfigTerminalSnapshot, snapshots []watchSendTerminalSnapshot) error {
	events := watchSendTerminalEvents(snapshots)
	events = append(events, watchRegisteredEvent(jm, cfg))
	for _, target := range targets {
		if target.cfg == nil || target.endReason == "" {
			continue
		}
		events = append(events, watchClearedEvent(jm, target.cfg, target.endReason))
	}
	return jm.appendWatchRegistryEvents(events)
}

func (jm *jobManager) appendWatchTeardownBatch(snapshots []watchSendTerminalSnapshot, targets []watchConfigTerminalSnapshot) error {
	events := watchSendTerminalEvents(snapshots)
	for _, target := range targets {
		if target.cfg == nil || target.endReason == "" {
			continue
		}
		events = append(events, watchClearedEvent(jm, target.cfg, target.endReason))
	}
	return jm.appendWatchRegistryEvents(events)
}

func watchSendTerminalEvents(snapshots []watchSendTerminalSnapshot) []jobstore.Event {
	var events []jobstore.Event
	for _, snapshot := range snapshots {
		events = append(events, snapshot.events...)
	}
	return events
}

func (jm *jobManager) appendWatchRegistryEvents(events []jobstore.Event) error {
	if len(events) == 0 {
		return nil
	}
	if jm.appendEvents != nil {
		if err := jm.appendEvents(events); err != nil {
			return err
		}
		jm.noteAcceptedWatchEvents(events)
		return nil
	}
	for _, event := range events {
		if err := jm.appendEvent(event); err != nil {
			return err
		}
		jm.noteAcceptedWatchEvents([]jobstore.Event{event})
	}
	return nil
}

// liveWatchSummaries snapshots the session's active watch configs (jm.watches
// only; terminalFlush is drain-only residue and excluded) into model-facing
// rows for job_list. One row per live config, ordered by source for stable
// output. The visibility walk runs under jm.mu; the formatting and ordering it
// feeds run outside it, so job_list's read no longer holds the lock the delivery
// path needs for work that reads no shared state.
// watchHistoryCap bounds the recent-watch ring surfaced by job_list. Old entries
// are trimmed; the ring is a debugging aid, not a durable audit log.
const watchHistoryCap = 16

// watchHistoryEntry is the final state of a watch that has left the active set.
type watchHistoryEntry struct {
	id                 string
	source             string
	target             string
	condition          string
	note               string
	sendTo             string
	receiverSessionID  string
	receiverDelegateID string
	deliveries         int
	endReason          string
	endedAt            time.Time
}

// recordWatchEndedLocked appends a watch's final state to the bounded history ring
// so a watch that fired and then left the active set stays legible in job_list. It
// is pure in-memory bookkeeping; the caller holds jm.mu.
func (jm *jobManager) recordWatchEndedLocked(key watchKey, cfg *watchConfig, reason string) {
	if cfg == nil {
		return
	}
	jm.noteWatchActivity(cfg.receiverSessionID)
	sendTo := key.SendTo
	if cfg.receiverDelegateID != "" {
		sendTo = ""
	}
	jm.watchHistory = append(jm.watchHistory, watchHistoryEntry{
		id:                 cfg.id,
		source:             cfg.sourcePublic,
		target:             cfg.target,
		condition:          watchConditionSummary(cfg),
		note:               cfg.note,
		sendTo:             sendTo,
		receiverSessionID:  cfg.receiverSessionID,
		receiverDelegateID: cfg.receiverDelegateID,
		deliveries:         cfg.deliveries,
		endReason:          reason,
		endedAt:            jm.now(),
	})
	if len(jm.watchHistory) > watchHistoryCap {
		jm.watchHistory = jm.watchHistory[len(jm.watchHistory)-watchHistoryCap:]
	}
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
			ID:         cfg.id,
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
		ID:             cfg.id,
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

func (jm *jobManager) onSessionEvent(ev events.SessionEvent) {
	defer jm.publishWatchActivity()
	kind := ev.Kind
	var notifications []jobNotification
	var deliveries []watchSendDelivery
	var overBudget []*watchConfig

	jm.mu.Lock()
	for _, cfg := range jm.watches {
		dec := evaluateWatchEvent(cfg.eventSnapshot(isActiveWatchTargetLocked(jm, cfg.target)), ev)
		cfg.eventCount = dec.eventCount
		if !dec.matched {
			continue
		}
		// The match is offered to the breaker before the rails split: the send
		// rail counts its delivery only when the frame settles, which a frame the
		// receiver never takes never reaches, so latching at the match is what
		// bounds an unsettled watch. A refused match is a match this watch is no
		// longer entitled to, so nothing is built from it.
		accepted, crossedBudget := noteConditionFireLocked(cfg)
		if !accepted {
			continue
		}
		if dec.send {
			deliveries = append(deliveries, jm.watchSendSnapshot(cfg, dec.watchedIdentity, fmt.Sprintf("event: %s", kind), ev).withSelfInfluence(jm.classifySelfInfluenceLocked(cfg, ev.Provenance)))
		} else {
			n := jm.watchNotificationFromWatch(cfg, dec.notifyJobID, fmt.Sprintf("event: %s", kind), ev.Provenance)
			// A self/parent-target job.notification watch must deliver the
			// concrete completed job's own identity (kata 673k), not the thin
			// generic watch shape watchNotificationFromWatch builds by default —
			// even though dec.notifyJobID is "" for a session-target watch (the
			// send-coalescing key deliberately stays target-shaped; only the
			// notify-branch frame identity is fixed here).
			if data, ok := jobFinishedEventData(ev.Data); ok && data.JobID != "" {
				n = jobFinishedEventIdentity(n, data)
			}
			notifications = append(notifications, n)
			jm.countWatchDeliveryLocked(cfg)
		}
		if crossedBudget {
			overBudget = append(overBudget, cfg)
		}
	}
	jm.mu.Unlock()

	// Called from Session.emit; only persist + wake here so watch delivery does
	// not re-enter session event emission (spec §3).
	//
	// The fired sends are recorded BEFORE the over-budget teardown, the way the
	// output rail and the attach scan already order it, so the frame whose match
	// crossed the budget is pending by the time its watch is torn down and
	// leaves on the terminal-flush rail: the budget bounds how many times a
	// watch may fire, not whether a frame it already produced reaches its
	// receiver. The teardown's cleared notification still rides out with this
	// event's matched notifications, so the model is woken once for both.
	jm.recordWatchSendsAndKick(deliveries)
	for _, cfg := range overBudget {
		if notification, ok := jm.autoClearWatchOverBudgetNotification(cfg); ok {
			notifications = append(notifications, notification)
		}
	}
	jm.enqueueWatchNotifications(notifications)
}

// watchEventSnapshot is a read-only copy of the per-watch fields that decide
// whether a session event fires a watch, taken under jm.mu so evaluateWatchEvent
// can run as a pure function of (snapshot, event). eventKinds is aliased, not
// deep-copied — the evaluator only reads it.
type watchEventSnapshot struct {
	target         string
	targetActive   bool
	wildcardEvents bool
	eventKinds     map[events.EventKind]bool
	eventFilter    *watchEventFilter
	watchID        string
	generation     string
	triggerKind    events.EventKind
	triggerEvery   int
	eventCount     int
	hasSend        bool
	sendTo         string
}

func (cfg *watchConfig) eventSnapshot(targetActive bool) watchEventSnapshot {
	snap := watchEventSnapshot{
		target:         cfg.target,
		targetActive:   targetActive,
		wildcardEvents: cfg.wildcardEvents,
		eventKinds:     cfg.eventKinds,
		eventFilter:    cfg.eventFilter,
		watchID:        cfg.watchID,
		generation:     cfg.generation,
		triggerKind:    cfg.triggerKind,
		triggerEvery:   cfg.triggerEvery,
		eventCount:     cfg.eventCount,
	}
	if cfg.send != nil {
		snap.hasSend = true
		snap.sendTo = cfg.send.To
	}
	return snap
}

// watchEventDecision is what evaluateWatchEvent returns for one watch: whether it
// fires, how it routes, and the throttle counter the wrapper must store back.
// eventCount is always the post-evaluation counter (unchanged when this watch has
// no every-Nth throttle for the event kind), so the wrapper can assign it
// unconditionally.
type watchEventDecision struct {
	matched         bool
	send            bool
	watchedIdentity string
	notifyJobID     string
	eventCount      int
}

// evaluateWatchEvent is the pure decision core lifted out of onSessionEvent: it
// applies the target/kind/target-match/filter/throttle gates to a snapshot and
// reports the routing decision plus the advanced throttle counter. Self-influence
// (the event carrying this watch's own provenance key) does NOT gate matching —
// under the inform+breaker policy the echo is delivered and classified at the
// observation site; the runaway fuse in recordWatchSend bounds the loop.
// It locks nothing, mutates nothing, and emits nothing; the caller performs the
// effects (snapshot deliveries, notifications, over-budget clears).
func evaluateWatchEvent(snap watchEventSnapshot, ev events.SessionEvent) watchEventDecision {
	kind := ev.Kind
	data := ev.Data
	miss := watchEventDecision{eventCount: snap.eventCount}
	if !snap.targetActive {
		return miss
	}
	if snap.wildcardEvents {
		if !isSupportedWatchEventKind(kind) {
			return miss
		}
	} else if !snap.eventKinds[kind] {
		return miss
	}
	if !watchEventMatchesTarget(snap.target, data) {
		return miss
	}
	if !watchEventFilterMatches(snap.eventFilter, ev) {
		return miss
	}
	eventCount := snap.eventCount
	if snap.triggerEvery > 0 && snap.triggerKind == kind {
		eventCount++
		if eventCount%snap.triggerEvery != 0 {
			return watchEventDecision{eventCount: eventCount}
		}
	}
	watchedIdentity := watchEventWatchedIdentity(snap.target, data)
	if snap.hasSend {
		if snap.sendTo == runtimeMessageAliasWatched && isWatchSessionTarget(watchedIdentity) {
			return watchEventDecision{eventCount: eventCount}
		}
		return watchEventDecision{matched: true, send: true, watchedIdentity: watchedIdentity, eventCount: eventCount}
	}
	notifyJobID := watchedIdentity
	if isWatchSessionTarget(notifyJobID) {
		notifyJobID = ""
	}
	return watchEventDecision{matched: true, watchedIdentity: watchedIdentity, notifyJobID: notifyJobID, eventCount: eventCount}
}

// selfInfluence is the breaker's read of an incoming triggering provenance for
// one watch.
type selfInfluence struct {
	self          bool // the event carries this watch's (watch_id, generation) key
	gradientDepth int  // distinct delivered priors of this watch, this generation (sidecar-facing)
	fuseDepth     int  // distinct delivered priors of this watch, any generation (runaway fuse)
	truncated     bool // self-influenced AND the diagnostic chain was truncated (depth may undercount)
}

// classifySelfInfluenceLocked classifies triggering provenance p for watch cfg.
// gradientDepth is scoped to the CURRENT identity and generation (a genuine
// re-arm or reconfiguration reads as fresh to the sidecar); fuseDepth is
// generation-agnostic (a re-arm-every-delivery watch cannot reset the fuse)
// but scoped to the current watchID, so a re-created watch starts fresh.
// truncated is the runaway backstop: WatchKeys never truncate so `self` stays
// reliable, but a truncated Chain can undercount the depths. Caller holds
// jm.mu (reads the delivered set via watchSendDeliveredLocked).
func (jm *jobManager) classifySelfInfluenceLocked(cfg *watchConfig, p *provenance.Causal) selfInfluence {
	self := provenance.ContainsWatch(p, cfg.watchID, cfg.generation)
	return selfInfluence{
		self:          self,
		gradientDepth: provenance.SelfInfluenceDepth(p, cfg.watchID, cfg.generation, jm.watchSendDeliveredLocked),
		fuseDepth:     jm.fuseDepthLocked(cfg, p),
		truncated:     self && p != nil && p.ChainTruncated,
	}
}

// fuseDepthLocked is the runaway fuse's read of provenance p for cfg: delivered
// priors of the current identity across every generation. Caller holds jm.mu.
func (jm *jobManager) fuseDepthLocked(cfg *watchConfig, p *provenance.Causal) int {
	return provenance.SelfInfluenceDepth(p, cfg.watchID, "", jm.watchSendDeliveredLocked)
}

// selfInfluenceNotice is the breaker's worker-facing line for a self-influenced
// delivery: terse when shallow, pointed as the loop tightens (or when the chain
// truncated, so the exact depth is unknown). Empty when not self-influenced.
func selfInfluenceNotice(self bool, gradientDepth int, truncated bool) string {
	if !self {
		return ""
	}
	switch {
	case truncated:
		return systemReminder("↳ you're many exchanges deep responding to your own influence — consider disengaging.")
	case gradientDepth >= 2:
		return systemReminderf("↳ you're ~%d exchanges deep responding to your own influence — consider disengaging.", gradientDepth)
	default:
		return systemReminder("↳ this turn responded to your last message.")
	}
}

func watchEventFilterMatches(filter *watchEventFilter, ev events.SessionEvent) bool {
	if filter == nil {
		return true
	}
	if ev.Kind != events.EventToolCallEnd {
		return false
	}
	var data events.ToolCallEndData
	switch d := ev.Data.(type) {
	case events.ToolCallEndData:
		data = d
	default:
		return false
	}
	if filter.ToolName != "" && data.ToolName != filter.ToolName {
		return false
	}
	if filter.Status != "" && toolCallStatus(data) != filter.Status {
		return false
	}
	return true
}

func toolCallStatus(data events.ToolCallEndData) string {
	if data.Error != "" {
		return "error"
	}
	return "ok"
}

func watchEventWatchedIdentity(target string, data events.EventData) string {
	if !isWatchSessionTarget(target) {
		return target
	}
	if target != "*" {
		return target
	}
	switch d := data.(type) {
	case events.JobStartedData:
		return d.JobID
	case events.JobFinishedData:
		return d.JobID
	default:
		return target
	}
}

func watchEventMatchesTarget(target string, data events.EventData) bool {
	if isWatchSessionTarget(target) {
		return true
	}
	switch d := data.(type) {
	case events.JobStartedData:
		return d.JobID == target
	case events.JobFinishedData:
		return d.JobID == target
	default:
		return true
	}
}

func isActiveWatchTargetLocked(jm *jobManager, target string) bool {
	if isWatchSessionTarget(target) {
		return true
	}
	return isWatchableConcreteJobLocked(jm.running[target])
}

// feedJobOutput level-triggers output_match watches on a freshly produced
// chunk whose final byte sits just before stream offset endOffset, the job's
// lifetime output byte count after the append. endOffset is monotone per job
// (the output pump is single-goroutine); a regression is dropped with a single
// warning so a caller bug cannot poison the matcher's scan-offset accounting.
func (jm *jobManager) feedJobOutput(jobID string, chunk []byte, endOffset int64) {
	jm.feedJobOutputWithProvenance(jobID, chunk, endOffset, nil)
}

func (jm *jobManager) feedJobOutputWithProvenance(jobID string, chunk []byte, endOffset int64, p *provenance.Causal) {
	defer jm.publishWatchActivity()
	if len(chunk) == 0 {
		return
	}
	var notifications []jobNotification
	var deliveries []watchSendDelivery
	var overBudget []*watchConfig

	root := events.SessionEvent{SessionID: jm.sessionID, Provenance: provenance.Union(jobProvenanceForWatch(jm, jobID), p)}
	jm.mu.Lock()
	if last, ok := jm.lastFedOffset[jobID]; ok && endOffset < last {
		jm.mu.Unlock()
		jm.enqueueWatchNotifications([]jobNotification{
			watchNotification(jobID, fmt.Sprintf("output dropped: feed offset regressed from %d to %d", last, endOffset)),
		})
		return
	}
	jm.lastFedOffset[jobID] = endOffset
	jm.stampLastActivityLocked(jobID)
	for _, cfg := range jm.watches {
		if cfg.target != jobID || cfg.outputMatcher == nil {
			continue
		}
		matches := cfg.outputMatcher.FeedAtWithProvenance(chunk, endOffset, root.Provenance)
		for _, match := range matches {
			accepted, crossedBudget := noteConditionFireLocked(cfg)
			if !accepted {
				break
			}
			matchRoot := root
			matchRoot.Provenance = provenance.Clone(match.Provenance)
			if cfg.send != nil {
				deliveries = append(deliveries, jm.watchSendSnapshot(cfg, jobID, "output_match: "+match.Text, matchRoot).withSelfInfluence(jm.classifySelfInfluenceLocked(cfg, match.Provenance)))
			} else {
				notifications = append(notifications, jm.watchNotificationFromWatch(cfg, jobID, "output_match: "+match.Text, match.Provenance))
				jm.countWatchDeliveryLocked(cfg)
			}
			if crossedBudget {
				overBudget = append(overBudget, cfg)
				// The matcher hands back every match in the chunk at once, and
				// the teardown this crossing schedules cannot run until jm.mu is
				// released. Without this break the rest of the chunk would fire a
				// watch that is already over budget — one chunk carrying a
				// thousand matches would deliver a thousand frames. The crossing
				// match itself has been recorded above: it is inside the budget,
				// and it is the last one this watch gets.
				break
			}
		}
	}
	jm.mu.Unlock()

	jm.enqueueWatchNotifications(notifications)
	jm.recordWatchSendsAndKick(deliveries)
	jm.autoClearOverBudgetWatches(overBudget)
}

// prepareAttachScanLocked primes a freshly installed output_match matcher for a
// level-trigger attach scan and returns the retained output to scan after the
// lock is released. The caller holds jm.mu and has just installed cfg for a
// concrete running target. It returns scan=false (and no data) when cfg has no
// matcher or the target is no longer a readable running job — the only cases are
// a non-output_match watch or a target that finalized in a lock gap, neither of
// which has retained output to level-check.
//
// Under jm.mu it reads the job's retained output and lifetime length N, records
// scanOffset=N on the matcher (so the live FeedAt path will not re-fire bytes the
// scan covers), and seeds the scan window with that retained output (so a token
// straddling the attach boundary completes through FeedAt without firing twice).
// The actual scan runs after the lock is released, in fireAttachScan.
//
// A watchable job with no output store cannot be level-checked at all. That is
// not a quiet no-fire: it is reported to the caller as an unable-to-scan warning
// by way of the error return, so a watch that can never see its target's
// already-produced output says so instead of looking armed.
func (jm *jobManager) prepareAttachScanLocked(cfg *watchConfig, run *runningJob) (data []byte, scan bool, err error) {
	if cfg == nil || cfg.outputMatcher == nil {
		return nil, false, nil
	}
	if !isWatchableConcreteJobLocked(run) {
		return nil, false, nil
	}
	if run.output == nil {
		return nil, false, errors.New("job has no readable output store")
	}
	// truncated is discarded: a pruned prefix can't be level-checked (its bytes
	// are gone), but SetScanOffset(total) uses the full lifetime count, so the
	// live FeedAt path still treats those already-produced bytes as covered (no
	// double-fire).
	buf, total, _, err := run.output.Tail(maxJobOutputRetentionBytes)
	if err != nil {
		return nil, false, err
	}
	cfg.outputMatcher.SetScanOffset(total)
	cfg.outputMatcher.SeedCarry(buf)
	return buf, true, nil
}

// completeAttachScan finishes an output_match attach scan after configureWatch
// releases jm.mu and reports whether it fired. A retained-read failure from
// prepareAttachScanLocked surfaces as one warning notification (the watch is
// installed and live regardless — only the level-trigger is lost), mirroring how
// the live feedJobOutput path surfaces an output problem rather than failing the
// install. scan=false (non-output_match watch, or a target that finalized in a
// lock gap) is a quiet no-fire.
func (jm *jobManager) completeAttachScan(cfg *watchConfig, jobID string, data []byte, scan bool, prepErr error) bool {
	if prepErr != nil {
		jm.enqueueWatchNotifications([]jobNotification{
			jm.watchNotificationFromWatch(cfg, jobID, "output_match attach scan skipped: "+limitWatchText(prepErr.Error(), watchReadErrorMaxChars), nil),
		})
		return false
	}
	if !scan {
		return false
	}
	return jm.fireAttachScan(cfg, jobID, data)
}

// fireAttachScan applies the watch's regexp to the complete lines in the retained
// output captured at attach and, if any line matches, fires the watch exactly
// once (a level check — "the output already contains the pattern" — not a replay
// of N lines, spec §7.1 "Attach-scan fire cardinality"). The single fire carries
// the LAST matching line and routes through the same rail as a live feedJobOutput
// match: recordWatchSendsAndKick for a send watch, enqueueWatchNotifications for a
// no-send one, and on either rail the same breaker check the live sites make. It
// runs after jm.mu is released.
// Returns whether the scan fired.
func (jm *jobManager) fireAttachScan(cfg *watchConfig, jobID string, data []byte) bool {
	defer jm.publishWatchActivity()
	last, matched := cfg.outputMatcher.ScanRetained(data)
	if !matched {
		return false
	}
	reason := "output_match: " + last
	if cfg.send != nil {
		root := events.SessionEvent{SessionID: jm.sessionID, Provenance: jobProvenanceForWatch(jm, jobID)}
		jm.mu.Lock()
		accepted, crossedBudget := noteConditionFireLocked(cfg)
		if !accepted {
			jm.mu.Unlock()
			return false
		}
		delivery := jm.watchSendSnapshot(cfg, jobID, reason, root)
		jm.mu.Unlock()
		jm.recordWatchSendsAndKick([]watchSendDelivery{delivery})
		if crossedBudget {
			jm.autoClearWatchOverBudget(cfg)
		}
		return true
	}
	jm.mu.Lock()
	accepted, crossedBudget := noteConditionFireLocked(cfg)
	if accepted {
		jm.countWatchDeliveryLocked(cfg)
	}
	jm.mu.Unlock()
	if !accepted {
		return false
	}
	jm.enqueueWatchNotifications([]jobNotification{jm.watchNotificationFromWatch(cfg, jobID, reason, jobProvenanceForWatch(jm, jobID))})
	if crossedBudget {
		jm.autoClearWatchOverBudget(cfg)
	}
	return true
}

// runTerminalCatchup serves an output_match-only watch on an already-terminal job
// as a one-shot catch-up: it scans the terminal job's retained output and, if it
// matches, fires once (spec §7.1 "Terminal target"). No live watch is installed
// either way; the result reports terminal_catchup with the terminal status, and
// Fired distinguishes a matched scan from an unmatched one.
//
// The scan is the SAME windowed level scan the attach path runs (ScanRetained)
// over the same retained bytes jm.readOutput serves for both running-but-terminal
// and store-only jobs. Catch-up and attach therefore agree on exactly what can
// match — including a match buried in a line longer than the scan window, and a
// match in an unterminated tail, neither of which a line-based scan can see. The
// frame carries the LAST match.
//
// A matched catch-up with a send has no home in the live pending machinery (no
// watch is installed), so it mints a one-shot DETACHED config registered in
// terminalFlush via rememberDetachedPendingLocked — exactly how
// expireJobWatchesLocked parks a terminal output_match send — so drains, restore,
// and pendingWatchSendDeliveries can see and settle it.
func (jm *jobManager) runTerminalCatchup(a watchArgs, key watchKey, status jobstore.Status, terminalReason string) (watchResult, error) {
	// Build the one-shot detached config up front: it validates and compiles
	// output_match into its matcher (wrapping a bad pattern as
	// "invalid_request: output_match:"), and the send branch reuses it to carry
	// the send through the durable rail with a fresh generation and cloned send.
	// The scan reuses the config's own matcher so output_match compiles once.
	cfg, err := newWatchConfig(a, jm.now(), key.Slot)
	if err != nil {
		return watchResult{}, err
	}

	result := watchResult{Source: cfg.sourcePublic, Target: key.Target, Watching: false, TerminalCatchup: true, Status: string(status), Reason: terminalReason}

	// maxJobOutputRetentionBytes caps retention, so it doubles as the scan budget.
	data, _, _, err := jm.readOutput(key.Target, maxJobOutputRetentionBytes)
	if err != nil {
		return watchResult{}, err
	}
	last, matched := cfg.outputMatcher.ScanRetained([]byte(data))
	if !matched {
		return result, nil
	}
	result.Fired = true
	matchReason := "output_match: " + last

	if a.Send == nil {
		jm.enqueueWatchNotifications([]jobNotification{jm.watchNotificationFromWatch(cfg, key.Target, matchReason, jobProvenanceForWatch(jm, key.Target))})
		return result, nil
	}
	result = watchResultFromConfig(cfg, false)
	result.Watching = false
	result.Fired = true
	result.TerminalCatchup = true
	result.Status = string(status)
	result.Reason = terminalReason

	root := events.SessionEvent{SessionID: jm.sessionID, Provenance: jobProvenanceForWatch(jm, key.Target)}
	jm.mu.Lock()
	delivery := jm.watchSendSnapshot(cfg, key.Target, matchReason, root)
	delivery.allowAfterTerminalExpiry = true
	jm.rememberDetachedPendingLocked(cfg)
	jm.mu.Unlock()
	jm.recordWatchSendsAndKick([]watchSendDelivery{delivery})
	return result, nil
}

type expiredJobWatch struct {
	key       watchKey
	cfg       *watchConfig
	endReason string
}

func (jm *jobManager) expireJobWatchesLocked(jobID string) ([]jobstore.Event, []expiredJobWatch, *provenance.Causal) {
	var registryEvents []jobstore.Event
	var expired []expiredJobWatch

	// The terminal job's record is still in jm.running here (the caller deletes it
	// after this returns), so its provenance is readable under the held lock
	// without the store fallback jobProvenanceForWatch would otherwise need.
	root := events.SessionEvent{SessionID: jm.sessionID}
	if run := jm.running[jobID]; run != nil && run.rec != nil {
		root.Provenance = provenance.Clone(run.rec.Provenance)
	}

	for key, cfg := range jm.watches {
		if key.Target != jobID {
			continue
		}
		registryEvents = append(registryEvents, watchClearedEvent(jm, cfg, "auto_removed_terminal"))
		expired = append(expired, expiredJobWatch{key: key, cfg: cfg, endReason: "auto_removed_terminal"})
	}

	return registryEvents, expired, root.Provenance
}

func (jm *jobManager) completeExpiredJobWatchesLocked(jobID string, expired []expiredJobWatch, rootProvenance *provenance.Causal) ([]jobNotification, []watchSendDelivery) {
	if len(expired) == 0 {
		return nil, nil
	}
	var notifications []jobNotification
	var deliveries []watchSendDelivery
	root := events.SessionEvent{SessionID: jm.sessionID, Provenance: provenance.Clone(rootProvenance)}
	endStatus, endReason, endOutputBytes := jm.terminalJobFactsLocked(jobID)
	for _, e := range expired {
		if jm.watches[e.key] != e.cfg {
			continue
		}
		cfg := e.cfg
		spoke := watchEverDelivered(cfg)
		// There is no terminal flush for an output_match watch: the byte-window
		// scanner matches every byte as it arrives instead of waiting for a line
		// terminator, so nothing is ever left buffered when the job ends.
		if len(cfg.pending) != 0 {
			jm.rememberDetachedPendingLocked(cfg)
		}
		if !spoke {
			// The watch is ending without ever having put anything on its own
			// channel. Its watcher is waiting on a condition that can no longer
			// match, and for a cross-session send_to there is no owner-side
			// job-stopped notification to fall back on, so this one frame is the
			// only thing that ever tells it to stop waiting.
			//
			// It is teardown, not a condition fire, which settles both accounting
			// questions: it is not counted into cfg.deliveries (that count is what
			// reports "this watch never matched" to job_list), and it carries no
			// self-influence depth (a runaway fuse drop would silently reopen the
			// hole this frame exists to close, and one frame from a config that is
			// deleted on the next line cannot cascade).
			trigger := watchEndedUnfiredMessage(jobID, endStatus, endReason, endOutputBytes)
			if cfg.send != nil {
				delivery := jm.watchSendSnapshot(cfg, jobID, trigger, root)
				delivery.allowAfterTerminalExpiry = true
				delivery.endNotice = true
				deliveries = append(deliveries, delivery)
				jm.rememberDetachedPendingLocked(cfg)
			} else {
				notifications = append(notifications, jm.watchNotificationFromWatch(cfg, jobID, trigger, root.Provenance))
			}
		}
		jm.recordWatchEndedLocked(e.key, e.cfg, e.endReason)
		closeWatchConfig(e.cfg)
		delete(jm.watches, e.key)
	}
	return notifications, deliveries
}

// watchEverDelivered reports whether cfg has ever put a frame on its own
// channel. deliveries counts settled model-facing deliveries (no-send
// notifications immediately, sends when they settle); nextUpdateSeq rises once
// per send frame built, so a send that fired and is still in flight counts as
// spoken even though nothing has settled yet.
func watchEverDelivered(cfg *watchConfig) bool {
	return cfg != nil && (cfg.deliveries > 0 || cfg.nextUpdateSeq > 0)
}

// noticeUnrestoredWatchEnds is the restart-side half of the watch teardown
// contract completeExpiredJobWatchesLocked keeps on the live path. A no-send
// callback watch is process-local, so restore cancels it and tells its receiver
// through the durable steering queue. Send watches retain their separate
// restart end-notice path below.
//
// Callback cancellation is coalesced once per receiver session. A single notice
// is enough to tell an agent that every callback closure from the lost runtime is
// invalid, and avoids one startup reminder per watch.
//
// Send notices still require a recorded target and durable watch-send evidence:
// any watch-send frame ever persisted for that generation, settled or pending,
// means the watch already spoke.
//
// The target's state picks WHICH notice, and the two are different claims. A
// terminal target gets watchEndedUnfiredMessage — the condition can never match
// again, and the terminal outcome is why. A target still running gets
// watchLostAtRestartMessage: reconcileLostJobs leaves foreign-owned records
// alone, so their owner recovers them and the condition may still occur, with
// nothing watching for it. Borrowing the terminal frame there would be a lie
// about a job that is still going.
//
// It must run AFTER reconcileLostJobs so a target lost with the runtime is
// already terminal, and it is idempotent — the notice it appends is itself a
// watch-send frame, so a repeat pass reads that watch as having spoken.
func (jm *jobManager) noticeUnrestoredWatchEnds() error {
	if len(jm.watchesLostAtRestore) == 0 {
		return nil
	}
	// A receiver that can't be routed to — its Session not yet reconstructed,
	// or gone for good — must not hold every OTHER lost watch's end notice
	// hostage. Log the aggregate and keep going; restore itself never fails on
	// a notification-delivery miss.
	if err := jm.notifyRestartCancelledCallbackWatches(); err != nil && jm.emit != nil {
		jm.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("restart callback cancellation: %v", err)}, nil)
	}
	// One journal read for both consumers: LoadEvents returns the raw events
	// and Fold derives the same job records Load would fold from them, so this
	// path does one readAllLocked+fold instead of two. No cache is added — the
	// events are a local of this call only.
	stored, err := jm.store.LoadEvents()
	if err != nil {
		return err
	}
	recs := jobstore.Fold(stored)
	spoke := watchGenerationsThatSpoke(stored)
	for _, watch := range jm.watchesLostAtRestore {
		if watch.SendTo == "" || spoke[watchFrameOrigin{watchID: watch.WatchID, generation: watch.Generation}] {
			continue
		}
		var trigger string
		if watch.SourceDelegateID != "" {
			trigger = watchLostAtRestartStableDelegateMessage(watch.SourceDelegateID)
		} else if isWatchSessionTarget(watch.Target) {
			// A session-target watch (source "self" or "parent") has no job
			// record: its target is this session's own live event stream, not a
			// job with a terminal outcome. recs[watch.Target] would never match
			// (watch.Target is the "caller" alias), silently dropping the notice.
			// The session itself keeps running past restart, so this is
			// watchLostAtRestartMessage's claim, not watchEndedUnfiredMessage's.
			trigger = watchLostAtRestartSessionMessage()
		} else {
			rec := recs[watch.Target]
			if rec == nil {
				continue
			}
			trigger = watchLostAtRestartMessage(watch.Target, rec.Status)
			if rec.Status.IsTerminal() {
				trigger = watchEndedUnfiredMessage(watch.Target, rec.Status, rec.Reason, rec.OutputBytes)
			}
		}
		// A detached config, like the one restoreWatchSendPendingFrom rebuilds for
		// a pending frame: enough identity for the send rail to route and settle
		// this one frame, and never registered in jm.watches.
		cfg := &watchConfig{
			id:                 watch.WatchID,
			watchID:            watch.WatchID,
			sourcePublic:       watchPublicSource(watch.Source, watch.Target),
			receiverSessionID:  watch.ReceiverSessionID,
			receiverDelegateID: watch.ReceiverDelegateID,
			target:             watch.Target,
			send:               &watchSendArgs{To: watch.SendTo},
			generation:         watch.Generation,
			pending:            make(map[jobstore.WatchSendKey]*jobstore.WatchSendState),
			sourceDelegateID:   watch.SourceDelegateID,
			sourceGeneration:   watch.SourceDelegateGeneration,
			stableReceiver:     watch.StableReceiver,
		}
		root := events.SessionEvent{SessionID: jm.sessionID, Provenance: jobProvenanceForWatch(jm, watch.Target)}
		jm.mu.Lock()
		delivery := jm.watchSendSnapshot(cfg, watch.Target, trigger, root)
		delivery.allowAfterTerminalExpiry = true
		delivery.endNotice = true
		jm.rememberDetachedPendingLocked(cfg)
		jm.mu.Unlock()
		// Teardown, not a condition fire: no delivery count and no self-influence
		// depth, for the reasons the live notice records.
		jm.recordWatchSendsAndKick([]watchSendDelivery{delivery})
	}
	return nil
}

func (jm *jobManager) notifyRestartCancelledCallbackWatches() error {
	receivers := make(map[string]struct{})
	for _, watch := range jm.watchesLostAtRestore {
		if watch == nil || watch.SendTo != "" {
			continue
		}
		receiver := strings.TrimSpace(watch.ReceiverSessionID)
		if receiver == "" {
			receiver = jm.sessionID
		}
		receivers[receiver] = struct{}{}
	}
	if len(receivers) == 0 {
		return nil
	}

	ids := make([]string, 0, len(receivers))
	for receiver := range receivers {
		ids = append(ids, receiver)
	}
	sort.Strings(ids)
	// One receiver's route being unavailable must not stop the rest from being
	// told: collect every failure and keep notifying the remaining receivers,
	// so a single unroutable session can never suppress a legitimate notice to
	// another.
	var errs []error
	for _, receiver := range ids {
		if jm.notifySystem != nil {
			if !jm.notifySystem(receiver, callbackWatchesCancelledAtRestartMessage) {
				errs = append(errs, fmt.Errorf("route callback cancellation notification to session %q: session unavailable", receiver))
			}
			continue
		}
		// Job-manager-only tests and legacy restore fixtures have no Session
		// tree to route through. Keep their local notification behavior on the
		// standard queue; production managers always install notifySystem.
		if receiver != jm.sessionID || jm.enqueue == nil {
			errs = append(errs, fmt.Errorf("route callback cancellation notification to session %q: no system-notification route", receiver))
			continue
		}
		jm.enqueue(jobNotification{Kind: jobNotificationKindWatch, Status: jobNotificationEventWatch, Reason: callbackWatchesCancelledAtRestartMessage})
	}
	return errors.Join(errs...)
}

// watchFrameOrigin identifies the watch config a durable watch-send frame came
// from. The generation is part of it because a replaced watch reuses neither id
// nor generation, and a frame from a predecessor says nothing about its successor.
type watchFrameOrigin struct {
	watchID    string
	generation string
}

// watchGenerationsThatSpoke folds the durable evidence that a watch put a frame
// on its own channel: every persisted watch-send frame, pending or settled. It
// is the restore-side reading of watchEverDelivered, which asks the same
// question of a live config's counters.
func watchGenerationsThatSpoke(stored []jobstore.Event) map[watchFrameOrigin]bool {
	spoke := make(map[watchFrameOrigin]bool)
	for _, event := range stored {
		if event.WatchSend == nil {
			continue
		}
		spoke[watchFrameOrigin{watchID: event.WatchSend.Key.WatchID, generation: event.WatchSend.Key.WatchGeneration}] = true
	}
	return spoke
}

// terminalJobFactsLocked reads the watched job's terminal outcome for the
// end-notice text. The caller holds jm.mu and runs inside the finalize path,
// before the run record is deleted, so the terminal is still reachable.
func (jm *jobManager) terminalJobFactsLocked(jobID string) (status jobstore.Status, reason string, outputBytes int64) {
	run := jm.running[jobID]
	if run == nil || run.terminal == nil {
		return "", "", 0
	}
	return run.terminal.status, run.terminal.reason, run.terminal.outputBytes
}

func (jm *jobManager) startProgressTimer(key watchKey, cfg *watchConfig, stop <-chan struct{}) {
	if cfg == nil || cfg.progressIntervalMS <= 0 || stop == nil {
		return
	}
	interval := time.Duration(cfg.progressIntervalMS) * time.Millisecond

	go func() {
		ticker := jm.clock.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C():
				if !jm.fireProgressTick(key, cfg) {
					return
				}
			case <-stop:
				return
			}
		}
	}()
}

// progressTickSnapshot is the read-only view fireProgressTick gathers under the
// lock before consulting decideProgressTick. target is the raw watch target,
// echoed into the notification job id for non-session targets. oneShot marks a
// one-shot timer, whose single fire is also its last.
type progressTickSnapshot struct {
	closing         bool
	stillRegistered bool
	sessionTarget   bool
	targetRunning   bool
	hasSend         bool
	oneShot         bool
	firedPendingEnd bool
	target          string
}

// progressTickDecision is what decideProgressTick returns for one tick: whether
// the timer goroutine keeps running (keepAlive), whether this tick delivers
// (fire) and how it routes — a watch send (sendDelivery) or a notification that
// counts a delivery (recordBudget), plus the notification job id. A periodic
// tick is not a condition fire, so its delivery never counts against the budget.
// endOneShot asks the caller to end a fired one-shot: set together with fire on
// its first delivery, and alone when a previous end failed to persist and this
// tick only retries the teardown.
type progressTickDecision struct {
	keepAlive    bool
	fire         bool
	sendDelivery bool
	recordBudget bool
	endOneShot   bool
	notifyJobID  string
}

// decideProgressTick is the pure decision core lifted out of fireProgressTick. It
// gates a progress tick on manager/registration/liveness and reports
// how the surviving tick routes. It locks nothing and mutates nothing; the caller
// performs the effects. A send tick and a budget-counted notification are mutually
// exclusive. A gated-out tick neither fires nor keeps the goroutine alive; a
// one-shot timer's tick fires and then ends the goroutine, and a one-shot whose
// end could not persist keeps the goroutine alive without firing until the
// retry lands, so fire alone — not keepAlive — decides whether this tick
// delivers.
func decideProgressTick(snap progressTickSnapshot) progressTickDecision {
	if snap.closing || !snap.stillRegistered {
		return progressTickDecision{}
	}
	// A one-shot that already fired and could not persist its end retries the
	// teardown on this tick and delivers nothing. The retry outranks the
	// liveness gate below: the watch is registered and owes an end, whatever
	// its target is now doing.
	if snap.firedPendingEnd {
		return progressTickDecision{keepAlive: true, endOneShot: true}
	}
	if !snap.sessionTarget && !snap.targetRunning {
		return progressTickDecision{}
	}
	dec := progressTickDecision{keepAlive: !snap.oneShot, fire: true, endOneShot: snap.oneShot}
	if snap.hasSend {
		dec.sendDelivery = true
		return dec
	}
	dec.recordBudget = true
	if !snap.sessionTarget {
		dec.notifyJobID = snap.target
	}
	return dec
}

func (jm *jobManager) fireProgressTick(key watchKey, cfg *watchConfig) bool {
	defer jm.publishWatchActivity()
	release, err := jm.beginRetirementMutation("watch")
	if err != nil {
		// Refusal must not consume the firing or end its registration. If a
		// tentative claim aborts, the existing ticker retries normally.
		return true
	}
	defer release()
	var notifications []jobNotification
	var deliveries []watchSendDelivery

	root := events.SessionEvent{SessionID: jm.sessionID, Provenance: jobProvenanceForWatch(jm, cfg.target)}
	jm.mu.Lock()
	snap := progressTickSnapshot{
		closing:         jm.closing,
		stillRegistered: jm.watches[key] == cfg,
		sessionTarget:   isWatchSessionTarget(cfg.target),
		targetRunning:   jm.running[cfg.target] != nil,
		hasSend:         cfg.send != nil,
		oneShot:         cfg.oneShot,
		firedPendingEnd: cfg.firedPendingEnd,
		target:          cfg.target,
	}
	dec := decideProgressTick(snap)
	if !dec.fire && !dec.endOneShot {
		jm.mu.Unlock()
		return false
	}
	if dec.fire {
		// This tick is the cadence's own fire instant, whether it routes to a
		// send rail (whose delivery is counted only when the frame settles) or
		// to the notification counted below. The delivery ring cannot serve as
		// the clock's history; see lastClockFire.
		cfg.lastClockFire = jm.now()
	}
	if dec.sendDelivery {
		deliveries = append(deliveries, jm.watchSendSnapshot(cfg, cfg.target, "progress_tick", root).withSelfInfluence(jm.classifySelfInfluenceLocked(cfg, root.Provenance)))
	}
	if dec.recordBudget {
		reason := "progress_tick"
		if cfg.timer {
			reason = "repeat"
			if cfg.oneShot {
				reason = "after"
			}
		}
		n := jm.watchNotificationFromWatch(cfg, dec.notifyJobID, reason, root.Provenance)
		if cfg.timer {
			n.WatchID, n.Fires, n.IntervalSeconds, n.Terminal = cfg.watchID, 1, cfg.timerSeconds, cfg.oneShot
		}
		notifications = append(notifications, n)
		// Periodic ticks never trip the condition-fire budget.
		jm.countWatchDeliveryLocked(cfg)
	}
	jm.mu.Unlock()

	// A one-shot ends on the tick that fires it, or on a later tick retrying an
	// end that did not persist. An end that lands stops the ticker with the
	// watch; one that fails keeps it armed for the next retry.
	if dec.endOneShot {
		dec.keepAlive = !jm.endFiredOneShot(cfg)
		if dec.keepAlive {
			jm.markOneShotFiredPendingEnd(cfg)
		}
	}
	jm.enqueueWatchNotifications(notifications)
	jm.recordWatchSendsAndKick(deliveries)
	return dec.keepAlive
}

// endFiredOneShot retires a one-shot timer after its only fire through the
// same snapshot, persist, detach sequence clearWatch uses, recorded with end
// reason "fired" so history distinguishes it from a clear. Called with jm.mu
// released; the fire's notification is enqueued by the caller afterwards. It
// reports whether the end persisted; a failure is warned about and left for the
// caller to retry.
func (jm *jobManager) endFiredOneShot(cfg *watchConfig) bool {
	if _, err := jm.clearWatchByIDMatchingWithReason(cfg.watchID, func(c *watchConfig) bool { return c == cfg }, true, "fired"); err != nil {
		if jm.emit != nil {
			jm.emit(events.EventWarning, events.WarningData{
				Message: fmt.Sprintf("job_watch: one-shot %s fired but its teardown did not persist: %v", cfg.watchID, err),
			}, nil)
		}
		return false
	}
	return true
}

// markOneShotFiredPendingEnd records that a one-shot has spent its single fire
// and still owes a durable end, so the next tick retries the teardown without
// delivering again.
func (jm *jobManager) markOneShotFiredPendingEnd(cfg *watchConfig) {
	if cfg == nil {
		return
	}
	jm.mu.Lock()
	defer jm.mu.Unlock()
	cfg.firedPendingEnd = true
}
