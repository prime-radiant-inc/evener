package agent

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
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
