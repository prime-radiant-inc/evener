package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
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
	// watchDeliveryBudget caps the condition fires a watch config may deliver
	// before the circuit breaker auto-clears it (spec §4 F1). A periodic progress
	// tick counts a delivery but is a clock rather than a condition, so it never
	// counts against this. Hard-coded, no config knob.
	watchDeliveryBudget = 50
	// maxLiveTimers caps timers per job manager; with the 60-second floor it
	// bounds a session to eight timer wakes a minute.
	maxLiveTimers = 8
	// runawaySelfInfluenceDepth caps how many delivered self-influenced priors a
	// watch send may descend from before the breaker drops it as a runaway. The
	// existing watchDeliveryBudget is the coarser whole-watch volume floor.
	runawaySelfInfluenceDepth = 8
	stableWatchReceiverTarget = "stable_watch_receiver"
)

// delegateQuietWindow is how long a running delegate may emit no
// parent-observable activity before the quiet-job watchdog fires one owner
// notification. delegateQuietCheckInterval is how often the watchdog goroutine
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
	id         string
	watchID    string
	configHash string
	// lineageWatchIDs carries the watchIDs of PREDECESSOR configs replaced in
	// this slot (same watch key). The runaway fuse counts delivered priors
	// across the whole lineage so a self-reconfiguring loop cannot reset the
	// fuse by replacing itself; capped, most-recent kept. In-memory only,
	// like the volume-budget deliveries counter.
	lineageWatchIDs    []string
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
	// Note rides every timer fire. All three are create-only.
	AfterSeconds         int
	RepeatSeconds        int
	Note                 string
	Send                 *watchSendArgs
	ReceiverSendInternal bool
	SourceDelegateID     string
	SourceGeneration     uint64
	StableReceiver       bool
	Clear                bool
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
	// TimerSeconds, OneShot, and Note describe a timer watch; a timer reports
	// its interval in seconds and leaves ProgressIntervalMS zero so the result
	// speaks in the units the model asked in.
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
	// (spec §7.1). Empty for live installs.
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
	a.Note = limitWatchText(a.Note, watchMessageMaxChars)
	// every:1 is the semantic default (fire on each occurrence), so it reads as
	// unset everywhere downstream; the single-concrete-kind requirement applies
	// only to every>1, which actually throttles.
	if a.Every == 1 {
		a.Every = 0
	}
	if a.EventFilter != nil {
		a.EventFilter.ToolName = strings.TrimSpace(a.EventFilter.ToolName)
		a.EventFilter.Status = strings.ToLower(strings.TrimSpace(a.EventFilter.Status))
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
			if _, terminal, statusErr := jm.terminalWatchTargetStatus(a.Target); statusErr == nil && terminal {
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
			status, terminal, statusErr := jm.terminalWatchTargetStatus(a.Target)
			if statusErr != nil {
				return watchResult{}, statusErr
			}
			if terminal {
				if a.Send != nil && !a.ReceiverSendInternal {
					if sendErr := jm.validateWatchSendTarget(a.Send.To, a); sendErr != nil {
						return watchResult{}, sendErr
					}
				}
				return jm.runTerminalCatchup(a, key, status)
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
			return watchResult{}, errors.New(`invalid_request: output_match watches concrete job output; parent observers that need communicate messages use source="parent" with events ["communicate"], read delivered event.message in the frame, and report findings with communicate(end_turn=true)`)
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
		jm.adoptWatchLineageLocked(key, cfg)
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
	jm.adoptWatchLineageLocked(key, cfg)
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
// requests are never catch-up. A time field or a note is excluded too: catch-up
// runs before validateWatchTriggerShape, so admitting those shapes would serve a
// scan instead of the timer rules' correction.
func watchArgsIsOutputMatchOnly(a watchArgs) bool {
	return !a.Clear &&
		a.OutputMatch != "" &&
		len(a.Events) == 0 &&
		a.Every == 0 &&
		a.ProgressIntervalMS == 0 &&
		!watchArgsIsTimer(a) &&
		a.Note == ""
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
				return errors.New(`invalid_request: event_filter matches assistant.tool events; parent observers that need communicate messages use source="parent" with events ["communicate"], read delivered event.message in the frame, and report findings with communicate(end_turn=true)`)
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
	if a.Operation == "create" && a.ProgressIntervalMS > 0 && a.Source != "" && a.Source != a.Target && isWatchSessionTarget(a.Target) {
		return errors.New("invalid_request: progress_interval_ms is a job progress trigger; for a timer use repeat_seconds")
	}
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
	} else if a.Note != "" {
		return errors.New("invalid_request: note applies to timers")
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
func (jm *jobManager) terminalWatchTargetStatus(target string) (status jobstore.Status, terminal bool, err error) {
	if isWatchSessionTarget(target) {
		return "", false, nil
	}
	jm.mu.Lock()
	run := jm.running[target]
	if run != nil && run.terminal != nil {
		s := run.terminal.status
		jm.mu.Unlock()
		return s, true, nil
	}
	if run != nil {
		// Running or finalizing: not catch-up-eligible (see doc comment).
		jm.mu.Unlock()
		return "", false, nil
	}
	jm.mu.Unlock()

	recs, err := jm.store.Load()
	if err != nil {
		return "", false, err
	}
	rec := recs[target]
	if rec == nil {
		return "", false, nil
	}
	if rec.OwnerSessionID != "" && rec.OwnerSessionID != jm.sessionID {
		// Owned by a nested session: not watchable from here, so not catch-up-
		// eligible. Fall through to the original validateWatchTarget error.
		return "", false, nil
	}
	if rec.Status.IsTerminal() {
		return rec.Status, true, nil
	}
	return "", false, nil
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
	return out
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
	states := make([]*jobstore.WatchSendState, 0, len(rec.Pending))
	for _, state := range rec.Pending {
		if state == nil {
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
		if allowDurable {
			var err error
			clearEvent, err = jm.durableWatchClearEvent(watchID, endReason)
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

func (jm *jobManager) durableWatchClearEvent(watchID, endReason string) (*jobstore.Event, error) {
	watches, err := jm.store.LoadWatches()
	if err != nil {
		return nil, err
	}
	w := watches[watchID]
	if w == nil || !w.Active || w.Generation == "" {
		return nil, nil
	}
	return &jobstore.Event{
		Kind:    jobstore.EventWatchCleared,
		TS:      jm.now(),
		WatchID: watchID,
		Watch: &jobstore.WatchEvent{
			Generation: w.Generation,
			EndReason:  endReason,
		},
	}, nil
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

// noteConditionFireLocked counts one condition match for cfg and reports whether
// it crossed the condition-fire budget. It is the ONLY place conditionFires
// moves: every match site — the event rail, the live output rail, and both
// attach-scan rails — goes through it, so no path can count a fire without
// consulting the breaker. A true result means the caller must schedule
// autoClearWatchOverBudget(cfg) after releasing jm.mu, on
// tripConditionFireBudgetLocked's terms. The caller must hold jm.mu.
func noteConditionFireLocked(cfg *watchConfig) (crossedBudget bool) {
	if cfg == nil {
		return false
	}
	cfg.conditionFires++
	return tripConditionFireBudgetLocked(cfg)
}

// countWatchDeliveryLocked increments the model-facing delivery count for cfg.
// It says nothing about the breaker: the budget bounds CONDITION fires, so a
// periodic progress tick counts a delivery here and never trips anything, and a
// condition fire's crossing is reported by noteConditionFireLocked at the match.
// The caller must hold jm.mu.
func countWatchDeliveryLocked(cfg *watchConfig) {
	if cfg == nil {
		return
	}
	cfg.deliveries++
}

// recordWatchDeliveryLocked counts a delivery at the send rail's settle end and
// reports whether it crossed the condition-fire budget. The send rail counts its
// fire when it snapshots a frame and its delivery when that frame settles, so
// this is the second of the two ends that can report the crossing; the latch
// keeps the pair to one teardown. The caller must hold jm.mu.
func (jm *jobManager) recordWatchDeliveryLocked(cfg *watchConfig) (crossedBudget bool) {
	countWatchDeliveryLocked(cfg)
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
// mutation plus durable drop of pending sends — NO delivery from observation
// (spec §3). It mirrors clearWatch's terminal-snapshot machinery but operates on
// a single (key, cfg) pair, so a no-send watch sharing a target with other watches
// does not over-clear its neighbors.
//
// The reverse lookup under jm.mu doubles as the no-double-fire latch: once the
// cfg is detached, a later in-flight settle that increments past the budget
// finds no live key and returns without re-notifying.
func (jm *jobManager) autoClearWatchOverBudgetNotification(cfg *watchConfig) (jobNotification, bool) {
	jm.mu.Lock()
	key, ok := watchKeyForConfigLocked(jm, cfg)
	if !ok {
		jm.mu.Unlock()
		return jobNotification{}, false
	}
	targets := []watchConfigTerminalSnapshot{{
		key:       key,
		cfg:       cfg,
		terminal:  watchSendTerminalSnapshotsLocked(cfg, jobstore.EventWatchSendDropped, "watch cleared", jm.now()),
		endReason: "budget_exhausted",
	}}
	markWatchConfigSnapshotsRejectingLocked(targets)
	jm.mu.Unlock()

	dropped := terminalSnapshots(targets)
	if err := jm.appendWatchTeardownBatch(dropped, targets); err != nil {
		jm.rollbackWatchBudgetTeardown(targets, cfg)
		return jobNotification{}, false
	}
	jm.detachWatchConfigSnapshots(targets)
	jm.removeWatchSendTerminalSnapshots(dropped)

	return jm.watchNotificationFromWatch(cfg, "", watchBudgetClearedMessage(cfg.target), nil), true
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
		return jm.appendEvents(events)
	}
	for _, event := range events {
		if err := jm.appendEvent(event); err != nil {
			return err
		}
	}
	return nil
}

// liveWatchSummaries snapshots the session's active watch configs (jm.watches
// only; terminalFlush is drain-only residue and excluded) into model-facing
// rows for job_list. One row per live config, ordered by source for stable
// output.
// watchHistoryCap bounds the recent-watch ring surfaced by job_list. Old entries
// are trimmed; the ring is a debugging aid, not a durable audit log.
const watchHistoryCap = 16

// watchHistoryEntry is the final state of a watch that has left the active set.
type watchHistoryEntry struct {
	id                 string
	source             string
	target             string
	condition          string
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
	sendTo := key.SendTo
	if cfg.receiverDelegateID != "" {
		sendTo = ""
	}
	jm.rememberWatchLineageLocked(key, cfg)
	jm.watchHistory = append(jm.watchHistory, watchHistoryEntry{
		id:                 cfg.id,
		source:             cfg.sourcePublic,
		target:             cfg.target,
		condition:          watchConditionSummary(cfg),
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

// recentWatchSummaries projects the watch history ring for job_list, latest first.
func (jm *jobManager) recentWatchSummaries() []recentWatchEntry {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	out := make([]recentWatchEntry, 0, len(jm.watchHistory))
	for i := range slices.Backward(jm.watchHistory) {
		h := jm.watchHistory[i]
		if !watchHistoryVisibleToSession(h, jm.sessionID) {
			continue
		}
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

func (jm *jobManager) watchListToolResult() jobWatchListToolResult {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	watches := make([]jobWatchInspectToolResult, 0, len(jm.watches))
	activeWatchIDs := make(map[string]bool, len(jm.watches))
	for key, cfg := range jm.watches {
		if !watchConfigVisibleToSession(cfg, jm.sessionID) {
			continue
		}
		watches = append(watches, inspectResultFromWatchConfig(key, cfg))
		if cfg != nil && cfg.watchID != "" {
			activeWatchIDs[cfg.watchID] = true
		}
	}
	for cfg := range jm.terminalFlush {
		if cfg == nil || cfg.watchID == "" || activeWatchIDs[cfg.watchID] {
			continue
		}
		if !watchConfigVisibleToSession(cfg, jm.sessionID) {
			continue
		}
		watches = append(watches, inspectResultFromDetachedWatchConfig(cfg))
	}
	sort.SliceStable(watches, func(i, j int) bool {
		if watches[i].Source != watches[j].Source {
			return watches[i].Source < watches[j].Source
		}
		return watches[i].WatchID < watches[j].WatchID
	})
	recent := make([]jobWatchInspectToolResult, 0, len(jm.watchHistory))
	for i := range slices.Backward(jm.watchHistory) {
		if !watchHistoryVisibleToSession(jm.watchHistory[i], jm.sessionID) {
			continue
		}
		recent = append(recent, inspectResultFromWatchHistory(jm.watchHistory[i]))
	}
	return jobWatchListToolResult{Watches: watches, RecentWatches: recent, Count: len(watches)}
}

func (jm *jobManager) watchListToolResultForReceiver(receiverSessionID, receiverDelegateID string) jobWatchListToolResult {
	receiverSessionID = strings.TrimSpace(receiverSessionID)
	receiverDelegateID = strings.TrimSpace(receiverDelegateID)
	if receiverSessionID == "" {
		return jobWatchListToolResult{}
	}
	jm.mu.Lock()
	defer jm.mu.Unlock()
	watches := make([]jobWatchInspectToolResult, 0, len(jm.watches))
	activeWatchIDs := make(map[string]bool, len(jm.watches))
	for key, cfg := range jm.watches {
		if !watchConfigMatchesReceiver(cfg, receiverSessionID, receiverDelegateID) {
			continue
		}
		watches = append(watches, inspectResultFromWatchConfig(key, cfg))
		if cfg != nil && cfg.watchID != "" {
			activeWatchIDs[cfg.watchID] = true
		}
	}
	for cfg := range jm.terminalFlush {
		if cfg == nil || cfg.watchID == "" || activeWatchIDs[cfg.watchID] {
			continue
		}
		if !watchConfigMatchesReceiver(cfg, receiverSessionID, receiverDelegateID) {
			continue
		}
		watches = append(watches, inspectResultFromDetachedWatchConfig(cfg))
	}
	sort.SliceStable(watches, func(i, j int) bool {
		if watches[i].Source != watches[j].Source {
			return watches[i].Source < watches[j].Source
		}
		return watches[i].WatchID < watches[j].WatchID
	})
	recent := make([]jobWatchInspectToolResult, 0, len(jm.watchHistory))
	for i := range slices.Backward(jm.watchHistory) {
		if !watchHistoryMatchesReceiver(jm.watchHistory[i], receiverSessionID, receiverDelegateID) {
			continue
		}
		recent = append(recent, inspectResultFromWatchHistory(jm.watchHistory[i]))
	}
	return jobWatchListToolResult{Watches: watches, RecentWatches: recent, Count: len(watches)}
}

func (jm *jobManager) inspectWatchByID(watchID string) jobWatchInspectToolResult {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for key, cfg := range jm.watches {
		if cfg != nil && cfg.watchID == watchID && watchConfigVisibleToSession(cfg, jm.sessionID) {
			return inspectResultFromWatchConfig(key, cfg)
		}
	}
	for cfg := range jm.terminalFlush {
		if cfg != nil && cfg.watchID == watchID && watchConfigVisibleToSession(cfg, jm.sessionID) {
			return inspectResultFromDetachedWatchConfig(cfg)
		}
	}
	for i := range slices.Backward(jm.watchHistory) {
		if jm.watchHistory[i].id == watchID && watchHistoryVisibleToSession(jm.watchHistory[i], jm.sessionID) {
			return inspectResultFromWatchHistory(jm.watchHistory[i])
		}
	}
	return jobWatchInspectToolResult{WatchID: watchID, Watching: false}
}

func (jm *jobManager) inspectReceiverWatchByID(watchID, receiverSessionID, receiverDelegateID string) (jobWatchInspectToolResult, bool) {
	receiverSessionID = strings.TrimSpace(receiverSessionID)
	receiverDelegateID = strings.TrimSpace(receiverDelegateID)
	if receiverSessionID == "" {
		return jobWatchInspectToolResult{}, false
	}
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for key, cfg := range jm.watches {
		if cfg != nil && cfg.watchID == watchID && watchConfigMatchesReceiver(cfg, receiverSessionID, receiverDelegateID) {
			return inspectResultFromWatchConfig(key, cfg), true
		}
	}
	for cfg := range jm.terminalFlush {
		if cfg != nil && cfg.watchID == watchID && watchConfigMatchesReceiver(cfg, receiverSessionID, receiverDelegateID) {
			return inspectResultFromDetachedWatchConfig(cfg), true
		}
	}
	for i := range slices.Backward(jm.watchHistory) {
		if jm.watchHistory[i].id == watchID && watchHistoryMatchesReceiver(jm.watchHistory[i], receiverSessionID, receiverDelegateID) {
			return inspectResultFromWatchHistory(jm.watchHistory[i]), true
		}
	}
	return jobWatchInspectToolResult{}, false
}

func inspectResultFromWatchConfig(key watchKey, cfg *watchConfig) jobWatchInspectToolResult {
	if cfg == nil {
		return jobWatchInspectToolResult{Watching: false}
	}
	_ = key
	return jobWatchInspectToolResult{
		WatchID:    cfg.watchID,
		Source:     watchPublicSource(cfg.sourcePublic, cfg.target),
		Watching:   true,
		Condition:  watchConditionSummary(cfg),
		Deliveries: cfg.deliveries,
		CreatedAt:  cfg.createdAt.Format(time.RFC3339Nano),
	}
}

func inspectResultFromDetachedWatchConfig(cfg *watchConfig) jobWatchInspectToolResult {
	result := inspectResultFromWatchConfig(watchKey{SendTo: watchSendTo(cfg)}, cfg)
	result.Watching = false
	return result
}

func inspectResultFromWatchHistory(h watchHistoryEntry) jobWatchInspectToolResult {
	return jobWatchInspectToolResult{
		WatchID:    h.id,
		Source:     watchPublicSource(h.source, h.target),
		Watching:   false,
		Condition:  h.condition,
		Deliveries: h.deliveries,
		EndReason:  h.endReason,
		EndedAt:    h.endedAt.Format(time.RFC3339Nano),
	}
}

func (jm *jobManager) liveWatchSummaries() []watchListEntry {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	entries := make([]watchListEntry, 0, len(jm.watches))
	for key, cfg := range jm.watches {
		if !watchConfigVisibleToSession(cfg, jm.sessionID) {
			continue
		}
		_ = key
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
	if receiverSessionID == "" || receiverDelegateID == "" {
		return nil
	}
	jm.mu.Lock()
	defer jm.mu.Unlock()
	entries := make([]watchListEntry, 0, len(jm.watches))
	for key, cfg := range jm.watches {
		if !watchConfigMatchesReceiver(cfg, receiverSessionID, receiverDelegateID) {
			continue
		}
		_ = key
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
	if cfg.timer && cfg.note != "" {
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
		// The match is counted and the breaker consulted before the rails
		// split: the send rail counts its delivery only when the frame settles,
		// which a frame the receiver never takes never reaches, so latching at
		// the match is what bounds an unsettled watch.
		crossedBudget := noteConditionFireLocked(cfg)
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
			countWatchDeliveryLocked(cfg)
		}
		if crossedBudget {
			overBudget = append(overBudget, cfg)
		}
	}
	jm.mu.Unlock()

	// Called from Session.emit; only persist + wake here so watch delivery does
	// not re-enter session event emission (spec §3).
	for _, cfg := range overBudget {
		if notification, ok := jm.autoClearWatchOverBudgetNotification(cfg); ok {
			notifications = append(notifications, notification)
		}
	}
	jm.enqueueWatchNotifications(notifications)
	jm.recordWatchSendsAndKick(deliveries)
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

// watchLineageCap bounds how many predecessor watchIDs a config retains: a
// loop replacing itself thousands of times must not grow the slice or the
// classify cost unboundedly. Most-recent predecessors are kept — stale ids stop
// appearing in fresh chains anyway. watchLineageKeyCap bounds how many watch
// KEYS keep a lineage tombstone after their watch ended.
const (
	watchLineageCap    = 15
	watchLineageKeyCap = 64
)

// inheritWatchLineage returns the lineage a successor config inherits from the
// config that ended: the predecessor's lineage plus the predecessor's own
// watchID, capped to the most recent watchLineageCap entries.
func inheritWatchLineage(existing *watchConfig) []string {
	lineage := append(append([]string{}, existing.lineageWatchIDs...), existing.watchID)
	if len(lineage) > watchLineageCap {
		lineage = lineage[len(lineage)-watchLineageCap:]
	}
	return lineage
}

// rememberWatchLineageLocked stashes the ended config's lineage under its key
// so the NEXT install for the same key (recreate after clear/expiry, or the
// replacement install) adopts it — a self-influenced loop cannot reset the
// runaway fuse by tearing its watch down and recreating it. Caller holds jm.mu.
func (jm *jobManager) rememberWatchLineageLocked(key watchKey, cfg *watchConfig) {
	if jm.watchLineage == nil {
		jm.watchLineage = make(map[watchKey][]string)
	}
	if _, exists := jm.watchLineage[key]; !exists {
		jm.watchLineageOrder = append(jm.watchLineageOrder, key)
		if len(jm.watchLineageOrder) > watchLineageKeyCap {
			evict := jm.watchLineageOrder[0]
			jm.watchLineageOrder = jm.watchLineageOrder[1:]
			delete(jm.watchLineage, evict)
		}
	}
	jm.watchLineage[key] = inheritWatchLineage(cfg)
}

// adoptWatchLineageLocked moves any remembered lineage for key onto a config
// being installed in that slot. Caller holds jm.mu.
func (jm *jobManager) adoptWatchLineageLocked(key watchKey, cfg *watchConfig) {
	if lineage := jm.watchLineage[key]; len(lineage) > 0 {
		cfg.lineageWatchIDs = lineage
	}
}

// classifySelfInfluenceLocked classifies triggering provenance p for watch cfg.
// gradientDepth is scoped to the CURRENT identity and generation (a genuine
// re-arm or reconfiguration reads as fresh to the sidecar); fuseDepth is
// generation-agnostic AND lineage-wide (neither a re-arm-every-delivery watch
// nor a replace-itself loop can reset the fuse). truncated is the runaway
// backstop: WatchKeys never truncate so `self` stays reliable, but a truncated
// Chain can undercount the depths. Caller holds jm.mu (reads the delivered set
// via watchSendDeliveredLocked).
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
// priors of the current identity plus every lineage predecessor (distinct
// watchIDs mark disjoint chain hops, so per-id depths sum). Caller holds jm.mu.
func (jm *jobManager) fuseDepthLocked(cfg *watchConfig, p *provenance.Causal) int {
	depth := provenance.SelfInfluenceDepth(p, cfg.watchID, "", jm.watchSendDeliveredLocked)
	for _, id := range cfg.lineageWatchIDs {
		depth += provenance.SelfInfluenceDepth(p, id, "", jm.watchSendDeliveredLocked)
	}
	return depth
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
	case *events.ToolCallEndData:
		if d == nil {
			return false
		}
		data = *d
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
			crossedBudget := noteConditionFireLocked(cfg)
			matchRoot := root
			matchRoot.Provenance = provenance.Clone(match.Provenance)
			if cfg.send != nil {
				deliveries = append(deliveries, jm.watchSendSnapshot(cfg, jobID, "output_match: "+match.Text, matchRoot).withSelfInfluence(jm.classifySelfInfluenceLocked(cfg, match.Provenance)))
			} else {
				notifications = append(notifications, jm.watchNotificationFromWatch(cfg, jobID, "output_match: "+match.Text, match.Provenance))
				countWatchDeliveryLocked(cfg)
			}
			if crossedBudget {
				overBudget = append(overBudget, cfg)
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
	last, matched := cfg.outputMatcher.ScanRetained(data)
	if !matched {
		return false
	}
	reason := "output_match: " + last
	if cfg.send != nil {
		root := events.SessionEvent{SessionID: jm.sessionID, Provenance: jobProvenanceForWatch(jm, jobID)}
		jm.mu.Lock()
		crossedBudget := noteConditionFireLocked(cfg)
		delivery := jm.watchSendSnapshot(cfg, jobID, reason, root)
		jm.mu.Unlock()
		jm.recordWatchSendsAndKick([]watchSendDelivery{delivery})
		if crossedBudget {
			jm.autoClearWatchOverBudget(cfg)
		}
		return true
	}
	jm.mu.Lock()
	crossedBudget := noteConditionFireLocked(cfg)
	countWatchDeliveryLocked(cfg)
	jm.mu.Unlock()
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
func (jm *jobManager) runTerminalCatchup(a watchArgs, key watchKey, status jobstore.Status) (watchResult, error) {
	// Build the one-shot detached config up front: it validates and compiles
	// output_match into its matcher (wrapping a bad pattern as
	// "invalid_request: output_match:"), and the send branch reuses it to carry
	// the send through the durable rail with a fresh generation and cloned send.
	// The scan reuses the config's own matcher so output_match compiles once.
	cfg, err := newWatchConfig(a, jm.now(), key.Slot)
	if err != nil {
		return watchResult{}, err
	}

	result := watchResult{Source: cfg.sourcePublic, Target: key.Target, Watching: false, TerminalCatchup: true, Status: string(status)}

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
	reason := "output_match: " + last

	if a.Send == nil {
		jm.enqueueWatchNotifications([]jobNotification{jm.watchNotificationFromWatch(cfg, key.Target, reason, jobProvenanceForWatch(jm, key.Target))})
		return result, nil
	}
	result = watchResultFromConfig(cfg, false)
	result.Watching = false
	result.Fired = true
	result.TerminalCatchup = true
	result.Status = string(status)

	root := events.SessionEvent{SessionID: jm.sessionID, Provenance: jobProvenanceForWatch(jm, key.Target)}
	jm.mu.Lock()
	delivery := jm.watchSendSnapshot(cfg, key.Target, reason, root)
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
	recs, err := jm.store.Load()
	if err != nil {
		return err
	}
	stored, err := jm.store.LoadEvents()
	if err != nil {
		return err
	}
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
			n.WatchID, n.Fires, n.Note, n.IntervalSeconds, n.Terminal = cfg.watchID, 1, cfg.note, cfg.timerSeconds, cfg.oneShot
		}
		notifications = append(notifications, n)
		cfg.deliveries++ // periodic ticks never trip the condition-fire budget
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

func watchNotification(jobID, reason string) jobNotification {
	return jobNotification{
		Kind:    jobNotificationKindWatch,
		JobID:   jobID,
		JobType: jobNotificationEventWatch,
		Status:  jobNotificationEventWatch,
		Reason:  reason,
	}
}

func (jm *jobManager) watchNotificationFromWatch(cfg *watchConfig, jobID, reason string, root *provenance.Causal) jobNotification {
	n := watchNotification(jobID, reason)
	if cfg == nil {
		return n
	}
	visibleSessionID := cfg.receiverSessionID
	if visibleSessionID == "" {
		visibleSessionID = jm.sessionID
	}
	n.Provenance = provenance.WithWatch(root, cfg.watchID, cfg.generation, "", visibleSessionID, jobID)
	if cfg.receiverSessionID != "" {
		n.receiverSessionID = cfg.receiverSessionID
		n.receiverNotify = cfg.receiverNotify
		n.receiverHoldWake = cfg.receiverHoldWake
	}
	return n
}

func (jm *jobManager) snapshotWatchSendFrames(deliveries []watchSendDelivery) []watchSendDelivery {
	for i := range deliveries {
		deliveries[i] = jm.snapshotWatchSendFrame(deliveries[i])
	}
	return deliveries
}

func (jm *jobManager) snapshotWatchSendFrame(d watchSendDelivery) watchSendDelivery {
	if d.send == nil {
		return d
	}
	d.message = limitWatchText(strings.TrimSpace(d.send.Message), watchMessageMaxChars)
	watchID := ""
	generation := ""
	if d.cfg != nil {
		watchID = d.cfg.watchID
		generation = d.cfg.generation
	}
	d.frame = jm.buildWatchFrame(&watchConfig{
		watchID:    watchID,
		generation: generation,
		send:       d.send,
	}, d.watchedIdentity, d.trigger, d.deliveryID, events.SessionEvent{
		Kind:       d.eventKind,
		SessionID:  jm.sessionID,
		Data:       d.eventData,
		Provenance: provenance.Clone(d.triggerProvenance),
	}, d.triggerProvenance)
	if notice := selfInfluenceNotice(d.selfInfluence, d.gradientDepth, d.truncated); notice != "" {
		d.frame = notice + "\n" + d.frame
	}
	return d
}

// recordWatchSend persists a fired send as pending and returns its state.
// ok=false means the send was superseded or unresolvable (already handled).
// Pure observation: no delivery, no Session calls (spec §3).
func (jm *jobManager) recordWatchSend(d watchSendDelivery) (state jobstore.WatchSendState, cfg *watchConfig, ok bool, err error) {
	if d.cfg == nil || d.send == nil || !jm.isCurrentWatchSendDelivery(d) {
		return jobstore.WatchSendState{}, nil, false, nil
	}
	target, terr := resolveWatchSendTarget(d.send.To, d.watchedIdentity)
	notificationJobID := ""
	if jobID, delegateID, ok := watchFrameJob(d.eventData); ok {
		d.frame = appendWatchFrameJobRead(d.frame, jobID)
		notificationJobID = jobID
		_ = delegateID
	}
	state = jm.watchSendState(d, target)
	state.NotificationJobID = notificationJobID
	// Coalescing (recordWatchSendPending) unions the superseded pending's
	// provenance into the survivor and recomputes the self-influence depth on
	// the union — two below-threshold branches with disjoint delivered priors
	// can cross the runaway threshold only together. Everything downstream
	// (the fuse decision and any drop event) uses the PERSISTED coalesced
	// state so the recorded evidence carries the ancestry that tripped it.
	persistedState, persisted, perr := jm.persistPendingWatchSend(state, d)
	if perr != nil {
		if d.allowAfterTerminalExpiry && !persisted {
			jm.rememberUnpersistedTerminalPendingWatchSend(d.cfg, state)
		}
		return jobstore.WatchSendState{}, nil, false, perr
	}
	if !persisted {
		return jobstore.WatchSendState{}, nil, false, nil
	}
	state = persistedState
	if terr != nil {
		return jobstore.WatchSendState{}, nil, false, jm.dropWatchSend(state, d.cfg, terr.Error())
	}
	if state.SelfInfluenceDepth >= runawaySelfInfluenceDepth {
		return jobstore.WatchSendState{}, nil, false, jm.dropWatchSend(state, d.cfg, "runaway")
	}
	return state, d.cfg, true, nil
}

// recordWatchSendsAndKick is the observation-side half of watch delivery:
// persist every fired send, enqueue caller wake tokens, kick the owner. It
// never delivers (spec §3); the owner's loop drains and delivers on wake. With
// nothing recorded there is nothing to drain, so the owner is left undisturbed.
func (jm *jobManager) recordWatchSendsAndKick(deliveries []watchSendDelivery) {
	tokens, recorded := jm.recordWatchSends(deliveries)
	// A queued token wakes the owner by itself; a kick is what covers a
	// recorded send that queued nothing.
	if queued := jm.enqueueNotifications(tokens); recorded && !queued {
		jm.kick()
	}
}

// recordWatchSends is the durable half of recordWatchSendsAndKick: it persists
// every fired send and RETURNS the caller wake tokens rather than queueing
// them, so a caller with more to say about the same event can wake the owner
// once for all of it. recorded reports whether anything was persisted at all —
// with tokens empty, that is what still owes the owner a kick.
func (jm *jobManager) recordWatchSends(deliveries []watchSendDelivery) (tokens []jobNotification, recorded bool) {
	if len(deliveries) == 0 {
		return nil, false
	}
	deliveries = jm.snapshotWatchSendFrames(deliveries)
	for _, d := range deliveries {
		state, _, ok, err := jm.recordWatchSend(d)
		if err != nil || !ok {
			continue // recordWatchSend already produced diagnostics/drops
		}
		recorded = true
		if state.Key.ResolvedSendTo == runtimeMessageAliasCaller {
			tokens = append(tokens, watchSendTokenNotification("", state))
		}
	}
	return tokens, recorded
}

func (jm *jobManager) watchSendState(d watchSendDelivery, resolvedSendTo string) jobstore.WatchSendState {
	deliveryID := d.deliveryID
	if deliveryID == "" {
		deliveryID = jobstore.NewWatchSendDeliveryID()
	}
	return jobstore.WatchSendState{
		Key: jobstore.WatchSendKey{
			VisibleSessionID:        d.visibleSessionID,
			WatchID:                 d.cfg.watchID,
			WatchTarget:             d.watchTarget,
			ResolvedWatchedIdentity: d.watchedIdentity,
			ResolvedSendTo:          resolvedSendTo,
			WatchGeneration:         d.generation,
		},
		DeliveryID:               deliveryID,
		UpdateSeq:                d.updateSeq,
		Message:                  d.message,
		Frame:                    d.frame,
		TriggerIdentity:          d.watchedIdentity,
		TriggerReason:            d.trigger,
		Provenance:               provenance.Clone(d.provenance),
		ReceiverSessionID:        d.cfg.receiverSessionID,
		ReceiverDelegateID:       d.cfg.receiverDelegateID,
		SourceDelegateID:         d.cfg.sourceDelegateID,
		SourceDelegateGeneration: d.cfg.sourceGeneration,
		StableReceiver:           d.cfg.stableReceiver,
		SelfInfluenceDepth:       d.fuseDepth,
		EndNotice:                d.endNotice,
	}
}

// watchTargetSessionID resolves a delegate target to its child session.
func watchFrameJob(data events.EventData) (jobID, delegateID string, ok bool) {
	switch d := data.(type) {
	case events.JobFinishedData:
		return d.JobID, d.DelegateID, d.JobID != ""
	case *events.JobFinishedData:
		if d == nil {
			return "", "", false
		}
		return d.JobID, d.DelegateID, d.JobID != ""
	default:
		return "", "", false
	}
}

// jobFinishedEventData extracts the EventJobFinished payload off a session
// event's data, or ok=false for any other event kind.
func jobFinishedEventData(data events.EventData) (events.JobFinishedData, bool) {
	switch d := data.(type) {
	case events.JobFinishedData:
		return d, true
	case *events.JobFinishedData:
		if d != nil {
			return *d, true
		}
	}
	return events.JobFinishedData{}, false
}

func (jm *jobManager) deliverPendingWatchSend(cfg *watchConfig, state jobstore.WatchSendState, ensurePending bool) (bool, error) {
	if !jm.isCurrentPendingWatchSend(cfg, state) {
		return false, nil
	}
	if ensurePending {
		if err := jm.appendWatchSendPendingState(cfg, state); err != nil {
			jm.enqueueWatchNotifications([]jobNotification{
				watchNotification(state.Key.ResolvedWatchedIdentity, "watch send pending state failed: "+limitWatchText(err.Error(), watchReadErrorMaxChars)),
			})
			return false, err
		}
	}
	if state.StableReceiver {
		return jm.deliverStableWatchSend(cfg, state)
	}
	return false, jm.dropWatchSend(state, cfg, "retired loose watch receiver")
}

func (jm *jobManager) dropWatchSend(state jobstore.WatchSendState, cfg *watchConfig, reason string) error {
	if !jm.isCurrentPendingWatchSend(cfg, state) {
		return nil
	}
	dropped := state
	dropped.DiagnosticReason = limitWatchText(reason, watchReadErrorMaxChars)
	if err := jm.appendWatchSendEvents([]jobstore.Event{{
		Kind:      jobstore.EventWatchSendDropped,
		TS:        jm.now(),
		WatchSend: &dropped,
	}}); err != nil {
		jm.enqueueWatchNotifications([]jobNotification{
			watchNotification(state.Key.ResolvedWatchedIdentity, "watch send dropped state failed: "+limitWatchText(err.Error(), watchReadErrorMaxChars)),
		})
		return err
	}
	jm.removePendingWatchSend(cfg, dropped.Key, dropped.UpdateSeq)
	jm.releaseStableWatchReceipt(dropped.DeliveryID)
	jm.enqueueWatchNotifications([]jobNotification{
		watchNotification(state.Key.ResolvedWatchedIdentity, "watch send failed: delivery_id="+state.DeliveryID+": "+dropped.DiagnosticReason),
	})
	return nil
}

func (jm *jobManager) deliverStableWatchSend(cfg *watchConfig, state jobstore.WatchSendState) (bool, error) {
	controller := jm.delegateController
	if controller == nil {
		return false, errors.New("stable watch controller is unavailable")
	}
	receipt := jm.stableWatchReceipt(state.DeliveryID)
	if receipt == nil {
		var err error
		receipt, err = controller.AcquireWatchDelivery(
			state.SourceDelegateID,
			state.SourceDelegateGeneration,
			state.ReceiverDelegateID,
			state.DeliveryID,
			state.UpdateSeq,
			state.EndNotice,
		)
		if err != nil {
			return false, err
		}
		jm.rememberStableWatchReceipt(receipt)
	}
	folded, err := jm.store.LoadWatchSends()
	if err != nil {
		return false, err
	}
	pending := folded.Pending[state.Key]
	if pending == nil || pending.DeliveryID != state.DeliveryID || pending.UpdateSeq != state.UpdateSeq {
		return false, errors.New("stable watch delivery is not the durable pending head")
	}
	receiver, err := controller.stableWatchReceiver(state.ReceiverSessionID, state.ReceiverDelegateID)
	if err != nil {
		return false, err
	}
	attentionID := stableWatchAttentionID(state)
	_, appendErr := receiver.appendDelegateNotificationDurably(attentionID, stableWatchNotificationContent(state))
	if !jm.isCurrentPendingWatchSend(cfg, state) {
		if appendErr != nil {
			jm.releaseStableWatchReceipt(state.DeliveryID)
			return false, appendErr
		}
		if err := armStableWatchAttention(controller, receiver, state.ReceiverDelegateID, attentionID); err != nil {
			return false, err
		}
		if err := jm.settleWatchSendDelivered(cfg, state); err != nil {
			jm.rememberStableWatchSettlementRetry(cfg, state)
			return false, err
		}
		return false, nil
	}
	if appendErr != nil {
		return false, appendErr
	}
	if err := armStableWatchAttention(controller, receiver, state.ReceiverDelegateID, attentionID); err != nil {
		return false, err
	}
	if err := jm.settleWatchSendDelivered(cfg, state); err != nil {
		return false, err
	}
	return true, nil
}

func armStableWatchAttention(controller *delegateTreeController, receiver delegateDeliveryReceiver, receiverDelegateID, attentionID string) error {
	switch receiver := receiver.(type) {
	case *Session:
		return receiver.armDelegateAttention(attentionID)
	case coldDelegateDeliveryReceiver:
		return controller.armColdDelegateAttention(receiverDelegateID, attentionID)
	}
	return nil
}

func stableWatchAttentionID(state jobstore.WatchSendState) string {
	return "watch:" + state.DeliveryID + ":" + strconv.FormatUint(state.UpdateSeq, 10)
}

func stableWatchNotificationContent(state jobstore.WatchSendState) string {
	lines := strings.Split(state.Frame, "\n")
	out := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "delivery_id:") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func (jm *jobManager) isCurrentPendingWatchSend(cfg *watchConfig, state jobstore.WatchSendState) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.isCurrentPendingWatchSendLocked(cfg, state)
}

func (jm *jobManager) isCurrentPendingWatchSendLocked(cfg *watchConfig, state jobstore.WatchSendState) bool {
	if cfg == nil || cfg.rejectingDelivery || jm.closing {
		return false
	}
	pending := cfg.pending[state.Key]
	if pending == nil || pending.UpdateSeq != state.UpdateSeq || pending.DeliveryID != state.DeliveryID {
		return false
	}
	if jm.terminalFlush != nil && jm.terminalFlush[cfg] {
		return true
	}
	key := watchKey{VisibleSessionID: state.Key.VisibleSessionID, Target: state.Key.WatchTarget, SendTo: cfg.sendTo(), ReceiverSessionID: cfg.receiverSessionID, ReceiverDelegateID: cfg.receiverDelegateID}
	return jm.watches[key] == cfg
}

func (cfg *watchConfig) sendTo() string {
	if cfg == nil || cfg.send == nil {
		return ""
	}
	return cfg.send.To
}

func (jm *jobManager) rememberUnpersistedTerminalPendingWatchSend(cfg *watchConfig, state jobstore.WatchSendState) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if cfg == nil || cfg.rejectingDelivery || jm.closing {
		return
	}
	if cfg.pending == nil {
		cfg.pending = make(map[jobstore.WatchSendKey]*jobstore.WatchSendState)
	}
	if cfg.pending[state.Key] == nil {
		cfg.pendingOrder = append(cfg.pendingOrder, state.Key)
	}
	now := jm.now()
	if state.CreatedAt.IsZero() {
		state.CreatedAt = now
	}
	state.UpdatedAt = now
	pending := state
	cfg.pending[state.Key] = &pending
	jm.rememberDetachedPendingLocked(cfg)
}

func (jm *jobManager) isCurrentWatchSendDelivery(d watchSendDelivery) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.isCurrentWatchSendDeliveryLocked(d)
}

func (jm *jobManager) isCurrentWatchSendDeliveryLocked(d watchSendDelivery) bool {
	if d.cfg == nil || d.cfg.generation != d.generation || d.cfg.rejectingDelivery || jm.closing {
		return false
	}
	if d.allowAfterTerminalExpiry {
		return jm.terminalFlush != nil && jm.terminalFlush[d.cfg]
	}
	return jm.watches[d.key] == d.cfg
}

func (jm *jobManager) persistPendingWatchSend(state jobstore.WatchSendState, d watchSendDelivery) (jobstore.WatchSendState, bool, error) {
	releasePersistence := jm.beginWatchPersistence()
	defer releasePersistence()

	record := jm.planWatchSendPending(state, d)
	if len(record.pendingEvents) == 0 {
		return state, false, nil
	}
	enqueueReceipt, err := jm.beginStableWatchEnqueue(record.persisted)
	if err != nil {
		return state, false, err
	}
	enqueueCompleted := false
	defer func() {
		if enqueueReceipt != nil && !enqueueCompleted {
			jm.observeWatchReceiptBoundary()
			enqueueReceipt.controller.AbortWatchEnqueue(enqueueReceipt)
		}
	}()
	if err := jm.appendWatchSendEvents(record.pendingEvents); err != nil {
		jm.enqueueWatchNotifications([]jobNotification{
			watchNotification(state.Key.ResolvedWatchedIdentity, "watch send pending state failed: "+limitWatchText(err.Error(), watchReadErrorMaxChars)),
		})
		return state, false, err
	}
	jm.commitWatchSendPendingRecord(record, d.allowAfterTerminalExpiry)
	if enqueueReceipt != nil {
		jm.observeWatchReceiptBoundary()
		deliveryReceipt, err := enqueueReceipt.controller.CompleteWatchEnqueue(enqueueReceipt)
		if err != nil {
			return record.persisted, true, err
		}
		enqueueCompleted = true
		jm.rememberStableWatchReceipt(deliveryReceipt)
		folded, err := jm.store.LoadWatchSends()
		if err != nil {
			return record.persisted, true, err
		}
		pending := folded.Pending[record.persisted.Key]
		if pending == nil || pending.DeliveryID != record.persisted.DeliveryID || pending.UpdateSeq != record.persisted.UpdateSeq {
			return record.persisted, true, errors.New("stable watch pending frame did not survive durable refold")
		}
	}
	var evictionDiagnostics []jobNotification
	for _, eviction := range record.evictions {
		applied, err := jm.appendWatchSendTerminalSnapshots([]watchSendTerminalSnapshot{eviction.terminal})
		if err != nil {
			jm.removeWatchSendTerminalSnapshots(applied)
			jm.enqueueWatchNotifications([]jobNotification{
				watchNotification(state.Key.ResolvedWatchedIdentity, "watch send pending state failed: "+limitWatchText(err.Error(), watchReadErrorMaxChars)),
			})
			return record.persisted, true, err
		}
		jm.removeWatchSendTerminalSnapshots(applied)
		evictionDiagnostics = append(evictionDiagnostics, eviction.diagnostic)
	}
	for _, diagnostic := range evictionDiagnostics {
		jm.enqueueWatchNotifications([]jobNotification{diagnostic})
	}
	return record.persisted, true, nil
}

func (jm *jobManager) beginWatchPersistence() func() {
	for {
		jm.watchPersistMu.Lock()
		if jm.watchPersistDone == nil {
			done := make(chan struct{})
			jm.watchPersistDone = done
			jm.watchPersistMu.Unlock()
			return func() {
				jm.watchPersistMu.Lock()
				jm.watchPersistDone = nil
				close(done)
				jm.watchPersistMu.Unlock()
			}
		}
		done := jm.watchPersistDone
		jm.watchPersistMu.Unlock()
		<-done
	}
}

func (jm *jobManager) beginStableWatchEnqueue(state jobstore.WatchSendState) (*delegateWatchReceipt, error) {
	if !state.StableReceiver {
		return nil, nil
	}
	if jm.delegateController == nil {
		return nil, errors.New("stable watch controller is unavailable")
	}
	jm.observeWatchReceiptBoundary()
	return jm.delegateController.BeginWatchEnqueue(
		state.SourceDelegateID,
		state.SourceDelegateGeneration,
		state.ReceiverDelegateID,
		state.DeliveryID,
		state.UpdateSeq,
		state.EndNotice,
	)
}

func (jm *jobManager) observeWatchReceiptBoundary() {
	if jm.watchReceiptBoundary != nil {
		jm.watchReceiptBoundary()
	}
}

func (jm *jobManager) rememberStableWatchReceipt(receipt *delegateWatchReceipt) {
	if receipt == nil {
		return
	}
	jm.mu.Lock()
	jm.stableWatchReceipts[receipt.deliveryID] = receipt
	jm.mu.Unlock()
}

func (jm *jobManager) stableWatchReceipt(deliveryID string) *delegateWatchReceipt {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.stableWatchReceipts[deliveryID]
}

func (jm *jobManager) releaseStableWatchReceipt(deliveryID string) {
	jm.mu.Lock()
	receipt := jm.stableWatchReceipts[deliveryID]
	delete(jm.stableWatchReceipts, deliveryID)
	jm.mu.Unlock()
	if receipt != nil {
		jm.observeWatchReceiptBoundary()
		_ = receipt.controller.CompleteWatchDelivery(receipt)
	}
}

func (jm *jobManager) rememberStableWatchSettlementRetry(cfg *watchConfig, state jobstore.WatchSendState) {
	if state.DeliveryID == "" {
		return
	}
	jm.mu.Lock()
	if jm.stableWatchSettlementRetries == nil {
		jm.stableWatchSettlementRetries = make(map[string]pendingWatchSendDelivery)
	}
	jm.stableWatchSettlementRetries[state.DeliveryID] = pendingWatchSendDelivery{cfg: cfg, state: state}
	jm.mu.Unlock()
	jm.kick()
}

func (jm *jobManager) forgetStableWatchSettlementRetry(state jobstore.WatchSendState) {
	jm.mu.Lock()
	retry, ok := jm.stableWatchSettlementRetries[state.DeliveryID]
	if ok && retry.state.UpdateSeq == state.UpdateSeq {
		delete(jm.stableWatchSettlementRetries, state.DeliveryID)
	}
	jm.mu.Unlock()
}

func (jm *jobManager) claimStableWatchSettlementRetries() ([]pendingWatchSendDelivery, bool) {
	jm.mu.Lock()
	if jm.stableWatchSettlementRetrying {
		jm.mu.Unlock()
		return nil, false
	}
	retries := jm.stableWatchSettlementRetryBatchLocked()
	if len(retries) != 0 {
		jm.stableWatchSettlementRetrying = true
	}
	jm.mu.Unlock()
	return retries, true
}

func (jm *jobManager) stableWatchSettlementRetryBatchLocked() []pendingWatchSendDelivery {
	retries := make([]pendingWatchSendDelivery, 0, len(jm.stableWatchSettlementRetries))
	for _, retry := range jm.stableWatchSettlementRetries {
		retries = append(retries, retry)
	}
	sort.Slice(retries, func(i, j int) bool {
		return watchSendStateLess(&retries[i].state, &retries[j].state)
	})
	return retries
}

func (jm *jobManager) finishStableWatchSettlementRetry() {
	jm.mu.Lock()
	jm.stableWatchSettlementRetrying = false
	jm.mu.Unlock()
}

func (jm *jobManager) nextStableWatchSettlementRetryBatch() ([]pendingWatchSendDelivery, bool) {
	jm.mu.Lock()
	if len(jm.stableWatchSettlementRetries) == 0 {
		jm.stableWatchSettlementRetrying = false
		jm.mu.Unlock()
		return nil, false
	}
	retries := jm.stableWatchSettlementRetryBatchLocked()
	jm.mu.Unlock()
	return retries, true
}

func (jm *jobManager) hasPendingStableWatchSettlementRetry() bool {
	if jm == nil {
		return false
	}
	jm.mu.Lock()
	pending := len(jm.stableWatchSettlementRetries) != 0 || jm.stableWatchSettlementRetrying
	jm.mu.Unlock()
	return pending
}

func (jm *jobManager) retryStableWatchSettlements() (bool, error) {
	retries, claimed := jm.claimStableWatchSettlementRetries()
	if !claimed {
		return false, nil
	}
	if len(retries) == 0 {
		return true, nil
	}
	releaseClaim := true
	defer func() {
		if releaseClaim {
			jm.finishStableWatchSettlementRetry()
		}
	}()
	for {
		for _, retry := range retries {
			controller := jm.delegateController
			if controller == nil {
				return true, errors.New("stable watch controller is unavailable")
			}
			receiver, err := controller.stableWatchReceiver(retry.state.ReceiverSessionID, retry.state.ReceiverDelegateID)
			if err != nil {
				return true, err
			}
			if err := jm.settleWatchSendDelivered(retry.cfg, retry.state); err != nil {
				return true, err
			}
			jm.forgetStableWatchSettlementRetry(retry.state)
			if err := armStableWatchAttention(controller, receiver, retry.state.ReceiverDelegateID, stableWatchAttentionID(retry.state)); err != nil {
				return true, err
			}
		}
		var more bool
		retries, more = jm.nextStableWatchSettlementRetryBatch()
		if !more {
			releaseClaim = false
			return true, nil
		}
	}
}

type watchSendPendingRecord struct {
	pendingEvents []jobstore.Event
	evictions     []watchSendEviction
	cfg           *watchConfig
	key           jobstore.WatchSendKey
	previous      *jobstore.WatchSendState
	// persisted is the exact state written to the pending map and event log
	// (post-coalescing: unioned provenance, recomputed self-influence depth).
	persisted jobstore.WatchSendState
}

type watchSendEviction struct {
	terminal   watchSendTerminalSnapshot
	diagnostic jobNotification
}

func (jm *jobManager) planWatchSendPending(state jobstore.WatchSendState, d watchSendDelivery) watchSendPendingRecord {
	now := jm.now()
	jm.mu.Lock()
	defer jm.mu.Unlock()

	if !jm.isCurrentWatchSendDeliveryLocked(d) {
		return watchSendPendingRecord{}
	}
	cfg := d.cfg
	if settled, ok := cfg.settledUpdateSeq[state.Key]; ok && state.UpdateSeq <= settled {
		return watchSendPendingRecord{}
	}
	// The pending map is created only after the pending event is durable.
	record := watchSendPendingRecord{
		cfg: cfg,
		key: state.Key,
	}
	if existing := cfg.pending[state.Key]; existing != nil {
		if state.UpdateSeq < existing.UpdateSeq {
			return watchSendPendingRecord{}
		}
		previous := *existing
		record.previous = &previous
		state.CoalescedCount = existing.CoalescedCount + 1
		state.CreatedAt = existing.CreatedAt
		// The coalesced frame stands in for both the superseded delivery and this
		// one, so it must carry both causes so either's downstream echo is
		// recognized — and the breaker must judge the UNION: two below-threshold
		// branches with disjoint delivered priors can cross the runaway
		// threshold only together.
		state.Provenance = provenance.Union(existing.Provenance, state.Provenance)
		if ud := jm.fuseDepthLocked(cfg, state.Provenance); ud > state.SelfInfluenceDepth {
			state.SelfInfluenceDepth = ud
		}
	}
	if state.CreatedAt.IsZero() {
		state.CreatedAt = now
	}
	state.UpdatedAt = now
	pendingState := state
	record.persisted = pendingState

	record.pendingEvents = []jobstore.Event{{
		Kind:      jobstore.EventWatchSendPending,
		TS:        now,
		WatchSend: &pendingState,
	}}
	projectedPending := len(cfg.pending)
	if cfg.pending[state.Key] == nil {
		projectedPending++
	}
	overflow := projectedPending - defaultWatchSendPendingCap
	for _, evictedKey := range cfg.pendingOrder {
		if overflow <= 0 {
			break
		}
		evicted := cfg.pending[evictedKey]
		if evicted == nil {
			continue
		}
		evictedState := *evicted
		evictedState.DiagnosticReason = "pending cap exceeded"
		record.evictions = append(record.evictions, watchSendEviction{
			terminal: watchSendTerminalSnapshot{cfg: cfg, events: []jobstore.Event{{
				Kind:      jobstore.EventWatchSendEvicted,
				TS:        now,
				WatchSend: &evictedState,
			}}},
			diagnostic: watchNotification(evictedState.Key.ResolvedWatchedIdentity, "watch send evicted: "+evictedState.TriggerIdentity),
		})
		overflow--
	}
	return record
}

// recordWatchSendPending retains the pure runtime-state transition used by the
// state-machine harnesses. Production persistence uses planWatchSendPending,
// fsyncs its event, and calls commitWatchSendPendingRecord only afterward.
//
//nolint:unused // retained for the evenerfuzz watch/delegate state-machine owner.
func (jm *jobManager) recordWatchSendPending(state jobstore.WatchSendState, d watchSendDelivery) watchSendPendingRecord {
	record := jm.planWatchSendPending(state, d)
	jm.commitWatchSendPendingRecord(record, d.allowAfterTerminalExpiry)
	return record
}

func (jm *jobManager) commitWatchSendPendingRecord(record watchSendPendingRecord, detached bool) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if record.cfg == nil || len(record.pendingEvents) == 0 {
		return
	}
	cfg := record.cfg
	if cfg.pending == nil {
		cfg.pending = make(map[jobstore.WatchSendKey]*jobstore.WatchSendState)
	}
	current := cfg.pending[record.key]
	if current != nil && current.UpdateSeq > record.persisted.UpdateSeq {
		return
	}
	if current == nil {
		cfg.pendingOrder = append(cfg.pendingOrder, record.key)
	}
	persisted := record.persisted
	cfg.pending[record.key] = &persisted
	if detached || cfg.rejectingDelivery || jm.closing {
		jm.rememberDetachedPendingLocked(cfg)
	}
}

func (jm *jobManager) removePendingWatchSend(cfg *watchConfig, key jobstore.WatchSendKey, updateSeq uint64) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	removePendingWatchSendLocked(cfg, key, updateSeq)
	jm.forgetTerminalFlushIfEmptyLocked(cfg)
}

func (jm *jobManager) removeRuntimePendingWatchSend(state jobstore.WatchSendState) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	cfg := jm.watchConfigForKeyLocked(state.Key)
	if cfg == nil {
		return
	}
	removePendingWatchSendLocked(cfg, state.Key, state.UpdateSeq)
	jm.forgetTerminalFlushIfEmptyLocked(cfg)
}

func removePendingWatchSendLocked(cfg *watchConfig, key jobstore.WatchSendKey, updateSeq uint64) {
	if cfg == nil {
		return
	}
	settleWatchSendLocked(cfg, key, updateSeq)
	if cfg.pending == nil {
		return
	}
	pending, ok := cfg.pending[key]
	if !ok || updateSeq < pending.UpdateSeq {
		return
	}
	deletePendingWatchSendKeyLocked(cfg, key)
}

func deletePendingWatchSendKeyLocked(cfg *watchConfig, key jobstore.WatchSendKey) {
	if cfg == nil || cfg.pending == nil {
		return
	}
	delete(cfg.pending, key)
	for i, orderedKey := range cfg.pendingOrder {
		if orderedKey != key {
			continue
		}
		copy(cfg.pendingOrder[i:], cfg.pendingOrder[i+1:])
		cfg.pendingOrder = cfg.pendingOrder[:len(cfg.pendingOrder)-1]
		return
	}
}

func settleWatchSendLocked(cfg *watchConfig, key jobstore.WatchSendKey, updateSeq uint64) {
	if cfg.settledUpdateSeq == nil {
		cfg.settledUpdateSeq = make(map[jobstore.WatchSendKey]uint64)
	}
	if _, ok := cfg.settledUpdateSeq[key]; !ok {
		cfg.settledOrder = append(cfg.settledOrder, key)
	}
	if updateSeq > cfg.settledUpdateSeq[key] {
		cfg.settledUpdateSeq[key] = updateSeq
	}
	for len(cfg.settledOrder) > defaultWatchSendPendingCap {
		oldest := cfg.settledOrder[0]
		copy(cfg.settledOrder, cfg.settledOrder[1:])
		cfg.settledOrder = cfg.settledOrder[:len(cfg.settledOrder)-1]
		delete(cfg.settledUpdateSeq, oldest)
	}
}

func (jm *jobManager) forgetTerminalFlushIfEmptyLocked(cfg *watchConfig) {
	if cfg == nil || len(cfg.pending) != 0 || jm.terminalFlush == nil {
		return
	}
	delete(jm.terminalFlush, cfg)
}

func (jm *jobManager) rememberDetachedPendingLocked(cfg *watchConfig) {
	if cfg == nil {
		return
	}
	if jm.terminalFlush == nil {
		jm.terminalFlush = make(map[*watchConfig]bool)
	}
	jm.terminalFlush[cfg] = true
}

type watchSendTerminalSnapshot struct {
	cfg    *watchConfig
	events []jobstore.Event
}

type watchConfigTerminalSnapshot struct {
	key      watchKey
	cfg      *watchConfig
	terminal watchSendTerminalSnapshot
	// endReason, when set, is recorded to the watch history ring as the config is
	// detached from the active set (cleared / budget_exhausted). Left empty by
	// snapshot callers that are not removing an active watch.
	endReason string
}

func terminalSnapshots(targets []watchConfigTerminalSnapshot) []watchSendTerminalSnapshot {
	out := make([]watchSendTerminalSnapshot, 0, len(targets))
	for _, target := range targets {
		out = append(out, target.terminal)
	}
	return out
}

func markWatchConfigSnapshotsRejectingLocked(targets []watchConfigTerminalSnapshot) {
	for _, target := range targets {
		if target.cfg != nil {
			target.cfg.rejectingDelivery = true
		}
	}
}

func (jm *jobManager) rollbackWatchConfigSnapshotsRejecting(targets []watchConfigTerminalSnapshot) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	rollbackWatchConfigSnapshotsRejectingLocked(jm, targets)
}

func rollbackWatchConfigSnapshotsRejectingLocked(jm *jobManager, targets []watchConfigTerminalSnapshot) {
	for _, target := range targets {
		if target.cfg != nil && jm.watches[target.key] == target.cfg {
			target.cfg.rejectingDelivery = false
		}
	}
}

func (jm *jobManager) closeWatchConfigSnapshots(targets []watchConfigTerminalSnapshot) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for _, target := range targets {
		closeWatchConfig(target.cfg)
	}
}

func (jm *jobManager) detachWatchConfigSnapshots(targets []watchConfigTerminalSnapshot) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for _, target := range targets {
		if target.cfg != nil && jm.watches[target.key] == target.cfg {
			if target.endReason != "" {
				jm.recordWatchEndedLocked(target.key, target.cfg, target.endReason)
			}
			closeWatchConfig(target.cfg)
			delete(jm.watches, target.key)
		}
	}
}

func watchSendTerminalSnapshotsLocked(cfg *watchConfig, kind jobstore.EventKind, reason string, now time.Time) watchSendTerminalSnapshot {
	snapshot := watchSendTerminalSnapshot{cfg: cfg}
	if cfg == nil || len(cfg.pending) == 0 {
		return snapshot
	}
	snapshot.events = make([]jobstore.Event, 0, len(cfg.pending))
	for _, key := range cfg.pendingOrder {
		state := cfg.pending[key]
		if state == nil {
			continue
		}
		terminal := *state
		terminal.DiagnosticReason = reason
		snapshot.events = append(snapshot.events, jobstore.Event{
			Kind:      kind,
			TS:        now,
			WatchSend: &terminal,
		})
	}
	return snapshot
}

func (jm *jobManager) detachedWatchSendTerminalSnapshotsLocked(key watchKey, kind jobstore.EventKind, reason string, now time.Time) ([]*watchConfig, []watchSendTerminalSnapshot) {
	var cfgs []*watchConfig
	var snapshots []watchSendTerminalSnapshot
	for cfg := range jm.terminalFlush {
		if !watchConfigMatchesWatchKey(cfg, key) {
			continue
		}
		snapshot := watchSendTerminalSnapshotMatchingKeyLocked(cfg, key, kind, reason, now)
		if len(snapshot.events) != 0 {
			cfgs = append(cfgs, cfg)
			snapshots = append(snapshots, snapshot)
			continue
		}
		if watchConfigSendToMatchesWatchKey(cfg, key) {
			cfgs = append(cfgs, cfg)
		}
	}
	return cfgs, snapshots
}

func (jm *jobManager) detachedWatchSendTerminalSnapshotsByWatchIDLocked(watchID string, kind jobstore.EventKind, reason string, now time.Time, allow func(*watchConfig) bool) ([]*watchConfig, []watchSendTerminalSnapshot, bool) {
	if watchID == "" {
		return nil, nil, false
	}
	var cfgs []*watchConfig
	var snapshots []watchSendTerminalSnapshot
	hidden := false
	for cfg := range jm.terminalFlush {
		if cfg == nil || cfg.watchID != watchID {
			continue
		}
		if allow != nil && !allow(cfg) {
			hidden = true
			continue
		}
		snapshot := watchSendTerminalSnapshotsLocked(cfg, kind, reason, now)
		cfgs = append(cfgs, cfg)
		if len(snapshot.events) != 0 {
			snapshots = append(snapshots, snapshot)
		}
	}
	return cfgs, snapshots, hidden
}

func watchSendTerminalSnapshotMatchingKeyLocked(cfg *watchConfig, key watchKey, kind jobstore.EventKind, reason string, now time.Time) watchSendTerminalSnapshot {
	snapshot := watchSendTerminalSnapshot{cfg: cfg}
	if cfg == nil || len(cfg.pending) == 0 {
		return snapshot
	}
	for _, pendingKey := range cfg.pendingOrder {
		if !watchSendKeyMatchesWatchKey(pendingKey, key) {
			continue
		}
		state := cfg.pending[pendingKey]
		if state == nil {
			continue
		}
		terminal := *state
		terminal.DiagnosticReason = reason
		snapshot.events = append(snapshot.events, jobstore.Event{
			Kind:      kind,
			TS:        now,
			WatchSend: &terminal,
		})
	}
	return snapshot
}

func watchSendKeyMatchesWatchKey(pending jobstore.WatchSendKey, key watchKey) bool {
	// A durable send key carries no slot, and only send-rail configs reach
	// here, so a timer key never matches a pending send.
	if key.Slot != "" {
		return false
	}
	if pending.VisibleSessionID != key.VisibleSessionID || pending.WatchTarget != key.Target {
		return false
	}
	if key.SendTo == "" || pending.ResolvedSendTo == key.SendTo {
		return true
	}
	return key.SendTo == runtimeMessageAliasWatched &&
		pending.ResolvedWatchedIdentity != "" &&
		pending.ResolvedSendTo == pending.ResolvedWatchedIdentity
}

func watchConfigMatchesWatchKey(cfg *watchConfig, key watchKey) bool {
	if cfg == nil || cfg.target != key.Target {
		return false
	}
	if cfg.slot != key.Slot {
		return false
	}
	if !watchConfigReceiverMatchesWatchKey(cfg, key) {
		return false
	}
	if key.SendTo == "" {
		return true
	}
	if key.SendTo == runtimeMessageAliasWatched {
		return true
	}
	return watchConfigSendToMatchesWatchKey(cfg, key)
}

func watchConfigSendToMatchesWatchKey(cfg *watchConfig, key watchKey) bool {
	if !watchConfigReceiverMatchesWatchKey(cfg, key) {
		return false
	}
	if key.SendTo == "" {
		return true
	}
	return cfg.send != nil && cfg.send.To == key.SendTo
}

func watchConfigReceiverMatchesWatchKey(cfg *watchConfig, key watchKey) bool {
	if cfg == nil {
		return false
	}
	if key.ReceiverSessionID == "" && key.ReceiverDelegateID == "" {
		return true
	}
	return cfg.receiverSessionID == key.ReceiverSessionID &&
		cfg.receiverDelegateID == key.ReceiverDelegateID
}

func markWatchConfigsRejectingLocked(cfgs []*watchConfig) {
	for _, cfg := range cfgs {
		if cfg != nil {
			cfg.rejectingDelivery = true
		}
	}
}

func (jm *jobManager) rollbackWatchConfigsRejecting(cfgs []*watchConfig) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	rollbackWatchConfigsRejectingLocked(jm, cfgs)
}

func rollbackWatchConfigsRejectingLocked(jm *jobManager, cfgs []*watchConfig) {
	for _, cfg := range cfgs {
		if cfg != nil && jm.terminalFlush != nil && jm.terminalFlush[cfg] {
			cfg.rejectingDelivery = false
		}
	}
}

func (jm *jobManager) forgetDetachedWatchSendConfigsIfEmpty(cfgs []*watchConfig) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for _, cfg := range cfgs {
		jm.forgetTerminalFlushIfEmptyLocked(cfg)
	}
}

func (jm *jobManager) appendWatchSendTerminalSnapshots(snapshots []watchSendTerminalSnapshot) ([]watchSendTerminalSnapshot, error) {
	applied := make([]watchSendTerminalSnapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		appliedSnapshot := watchSendTerminalSnapshot{cfg: snapshot.cfg}
		for _, event := range snapshot.events {
			if err := jm.appendEvent(event); err != nil {
				if len(appliedSnapshot.events) != 0 {
					applied = append(applied, appliedSnapshot)
				}
				return applied, err
			}
			appliedSnapshot.events = append(appliedSnapshot.events, event)
		}
		if len(appliedSnapshot.events) != 0 {
			applied = append(applied, appliedSnapshot)
		}
	}
	return applied, nil
}

func (jm *jobManager) removeWatchSendTerminalSnapshots(snapshots []watchSendTerminalSnapshot) {
	jm.mu.Lock()
	var receiptIDs []string
	for _, snapshot := range snapshots {
		for _, event := range snapshot.events {
			if event.WatchSend == nil {
				continue
			}
			receiptIDs = append(receiptIDs, event.WatchSend.DeliveryID)
			removePendingWatchSendLocked(snapshot.cfg, event.WatchSend.Key, event.WatchSend.UpdateSeq)
		}
		jm.forgetTerminalFlushIfEmptyLocked(snapshot.cfg)
	}
	jm.mu.Unlock()
	for _, deliveryID := range receiptIDs {
		jm.releaseStableWatchReceipt(deliveryID)
	}
}

func (jm *jobManager) appendWatchSendEvents(events []jobstore.Event) error {
	for _, e := range events {
		if err := jm.appendEvent(e); err != nil {
			return err
		}
	}
	return nil
}

func (jm *jobManager) appendWatchSendPendingState(cfg *watchConfig, state jobstore.WatchSendState) error {
	if !jm.isCurrentPendingWatchSend(cfg, state) {
		return nil
	}
	pending := state
	return jm.appendWatchSendEvents([]jobstore.Event{{
		Kind:      jobstore.EventWatchSendPending,
		TS:        jm.now(),
		WatchSend: &pending,
	}})
}

type pendingWatchSendDelivery struct {
	cfg   *watchConfig
	state jobstore.WatchSendState
}

// drainPendingWatchSends is the ONLY executor of watch-send delivery. Call it
// solely from loop-owned code: never from event observation, never under
// responseSideEffectsMu (spec §3/§4.2). A wake submits an EntryNotification; an
// all-token batch renders and settles in the notification turn; an EMPTY accept
// hits finishNotificationNoop → finishProcessingAtBoundary → here, so
// delegate-only wakes deliver with zero model turns.
type watchSendDrainResult struct {
	observerHandoff bool
}

func (s *Session) drainPendingWatchSends(ctx context.Context) error {
	_, err := s.drainPendingWatchSendsReport(ctx)
	return err
}

func (s *Session) drainPendingWatchSendsReport(ctx context.Context) (watchSendDrainResult, error) {
	var result watchSendDrainResult
	var errs []error
	if s.jobManager != nil {
		drained, err := s.drainJobManagerWatchSends(ctx, s.jobManager, "")
		result.observerHandoff = result.observerHandoff || drained.observerHandoff
		errs = append(errs, err)
	}
	if s.subagents != nil {
		for _, child := range s.subagents.sessions() {
			// sessions filters nil subagents and sessions before returning.
			if child.jobManager == nil {
				continue
			}
			drained, err := child.drainJobManagerWatchSends(ctx, child.jobManager, child.id)
			result.observerHandoff = result.observerHandoff || drained.observerHandoff
			errs = append(errs, err)
		}
	}
	s.driveChildrenWithUndeliveredAttention()
	return result, errors.Join(errs...)
}

// driveChildrenWithUndeliveredAttention re-purposes the parent's loop-boundary
// child traversal as a DRIVE-SIGNAL reader (spec §3): for each LIVE, IDLE direct
// child with undelivered attention, the parent launches ONE EntryNotification
// turn on the child's own drain loop. This is signal-reading only: it renders no
// notification on the parent's rail (worker terminals reach the child's own
// model, never the parent's — spec §3). The drive turn is async
// (driveSubagentNotificationTurn fires a goroutine), so the parent's boundary
// never blocks on a child's loop.
//
// Drive signals (spec §3): (a) queued job notifications (peekNotifications) and
// (b) pending caller-targeted watch sends (hasPendingWatchSends). Signal (b) is
// read here now that the v2 child-iteration re-route is deleted — a mid-owner
// caller frame renders in the mid's own drive turn instead of being re-tokened
// onto the parent's rail. A deliberately stopped child is stop-gated (no
// resurrection); a successful handoff settles the parent's forwarded drive
// signal; a child that cannot be driven escalates as "child unreachable:".
func (s *Session) driveChildrenWithUndeliveredAttention() {
	live := make(map[string]bool)
	for _, sub := range s.liveDirectSubagents() {
		child := sub.sess
		// liveDirectSubagents filters nil subagents and sessions.
		live[child.id] = true
		// Stop-gating (spec §3): a deliberately stopped child is never resurrected
		// by a drive for attention that predates the stop. New work clears the gate.
		// A child the one-shot drain has given up on is skipped for the same
		// reason and, additionally, for consistency: the drain declares it not
		// live work, so driving it here would have the drain kicking a child it
		// has already told the operator it abandoned — and abandoning a queued
		// notification the drive loop was mid-way through delivering.
		if s.childStopGated(child.id) || s.childFatalRunGated(child.id) || s.childDrainAbandoned(child.id) || s.childDrainGracePending(child.id) {
			continue
		}
		if child.peekNotifications() > 0 || child.jobManager.hasPendingWatchSends() {
			if s.driveSubagentNotificationTurn(sub) {
				// Settle the parent's forwarded drive signal on a successful
				// handoff so the same stale pending does not re-drive forever; the
				// child's own ledger stays the source of truth.
				s.settleDrivenChildForwardedPendings(child.id)
			}
		}
	}
	s.renderUnreachableChildPendings(live)
}

// driveChildIfNotStopGated is the wake-edge drive: it skips a stop-gated child so
// a deliberately stopped child is not resurrected by its own pre-stop notify
// (spec §3 stop-gating), and a child the one-shot drain has abandoned for the
// same reason driveChildrenWithUndeliveredAttention does. On a successful
// handoff it settles the parent's forwarded drive signal for that child.
func (s *Session) driveChildIfNotStopGated(sub *subagent) {
	if sub == nil || sub.sess == nil {
		return
	}
	if s.childStopGated(sub.sess.id) || s.childFatalRunGated(sub.sess.id) || s.childDrainAbandoned(sub.sess.id) || s.childDrainGracePending(sub.sess.id) {
		return
	}
	if s.driveStableDelegateAttention(sub) {
		return
	}
	if s.driveSubagentNotificationTurn(sub) {
		s.settleDrivenChildForwardedPendings(sub.sess.id)
	}
}

// renderUnreachableChildPendings is the failure fallback (spec §3): a forwarded
// pending in the parent's store whose owner child cannot be driven — the child is
// not a live direct subagent AND is non-resumable (closed, descriptor-less,
// validation failure) — is rendered by the parent itself, prefixed "child
// unreachable:". Attention escalates one honest level instead of vanishing. live
// is the set of session ids that ARE live direct subagents this pass (those are
// driven, not rendered here). A child that is terminal-but-resumable is left
// pending for its own future drive/resume turn — not falsely escalated.
func (s *Session) renderUnreachableChildPendings(live map[string]bool) {
	if s == nil || s.jobManager == nil {
		return
	}
	jm := s.jobManager
	s.renderUnreachableChildPendingsWithLoaders(live, jm.store.Load, jm.store.LoadWatchSends)
}

func (s *Session) renderUnreachableChildPendingsWithLoaders(
	live map[string]bool,
	loadJobs func() (map[string]*jobstore.JobRecord, error),
	loadWatchSends watchSendLoader,
) {
	jm := s.jobManager
	recs, err := loadJobs()
	if err != nil {
		return
	}
	for _, rec := range recs {
		if rec == nil ||
			rec.OwnerSessionID == "" ||
			rec.OwnerSessionID == jm.sessionID ||
			rec.VisibleToSession != jm.sessionID ||
			rec.TerminalGen == "" ||
			rec.NotifyState != jobstore.NotifyPending {
			continue
		}
		if live[rec.OwnerSessionID] {
			continue // driven, not rendered
		}
		if s.childResumable(rec.OwnerSessionID) {
			continue // resumable but idle: left for its own future drive/resume turn
		}
		n := jobNotificationFromRecord(rec)
		n.Reason = strings.TrimSpace("child unreachable: " + rec.Reason)
		s.enqueueJobNotification(n)
	}
	watchSends, err := loadWatchSends()
	if err != nil {
		return
	}
	for _, state := range watchSends.Pending {
		if state == nil ||
			state.Key.ResolvedSendTo != runtimeMessageAliasCaller ||
			state.Key.VisibleSessionID == "" ||
			state.Key.VisibleSessionID == jm.sessionID {
			continue
		}
		childSessionID := state.Key.VisibleSessionID
		if live[childSessionID] {
			continue // driven, not rendered
		}
		if s.childResumable(childSessionID) {
			continue // resumable but idle: left for its own future drive/resume turn
		}
		dropped := *state
		dropped.DiagnosticReason = limitWatchText("child unreachable: "+state.TriggerReason, watchReadErrorMaxChars)
		if err := jm.appendWatchSendEvents([]jobstore.Event{{
			Kind:      jobstore.EventWatchSendDropped,
			TS:        jm.now(),
			WatchSend: &dropped,
		}}); err != nil {
			s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("watch-send fallback drop failed: %v", err)})
			continue
		}
		jm.removeRuntimePendingWatchSend(dropped)
		n := watchNotification(state.Key.ResolvedWatchedIdentity, dropped.DiagnosticReason)
		n.Provenance = provenance.Clone(state.Provenance)
		s.enqueueJobNotification(n)
	}
}

// childResumable reports whether the stable controller holds a direct child
// descriptor that is currently resumable.
func (s *Session) childResumable(childSessionID string) bool {
	row, ok := s.directStableDelegateForChildSession(childSessionID)
	return ok && row.resumable
}

func (s *Session) directStableDelegateForChildSession(childSessionID string) (delegateSnapshot, bool) {
	if s == nil || s.delegateController == nil || childSessionID == "" {
		return delegateSnapshot{}, false
	}
	for _, visible := range stableDelegateRowsForSession(s, false) {
		if visible.snapshot.descriptor.ChildSessionID == childSessionID {
			return visible.snapshot, true
		}
	}
	return delegateSnapshot{}, false
}

// childStopGated reports whether the direct stable delegate's latest settled
// generation was deliberately stopped by its parent. A newer running
// generation clears the gate.
func (s *Session) childStopGated(childSessionID string) bool {
	row, ok := s.directStableDelegateForChildSession(childSessionID)
	return ok && delegateRowStopGated(row)
}

// delegateRowStopGated is childStopGated's verdict on a row the caller already
// holds. The drain's abandonment pass takes one snapshot per session per pass
// and asks several questions of each row, so it must not re-snapshot the whole
// controller to ask this one.
func delegateRowStopGated(row delegateSnapshot) bool {
	if row.currentRunOpen || row.lastOutcome == nil {
		return false
	}
	return row.lastOutcome.Status == delegatestore.OutcomeStopped && row.lastOutcome.Reason == "stopped_by_parent"
}

// settleDrivenChildForwardedPendings marks the parent's forwarded pending COPIES
// for childSessionID delivered at a successful drive handoff (spec §3 settle).
// This is safe because the parent's forwarded copy is only a DRIVE SIGNAL, not
// the delivery ledger: the child's own durable queue (armed at the original
// enqueue, re-armed at the child's restore) is the ledger, and the no-loss clause
// keeps its meaning on the owner's copy. Settling the signal stops the same stale
// pending from re-driving forever; it never touches the child's own store.
func (s *Session) settleDrivenChildForwardedPendings(childSessionID string) {
	if s == nil || s.jobManager == nil || childSessionID == "" {
		return
	}
	jm := s.jobManager
	recs, err := jm.store.Load()
	if err != nil {
		return
	}
	for _, rec := range recs {
		if rec == nil ||
			rec.OwnerSessionID != childSessionID ||
			rec.VisibleToSession != jm.sessionID ||
			rec.TerminalGen == "" ||
			rec.NotifyState != jobstore.NotifyPending {
			continue
		}
		if err := jm.appendEvent(jobstore.Event{
			Kind:        jobstore.EventJobNotificationDelivered,
			TS:          jm.now(),
			JobID:       rec.JobID,
			TerminalGen: rec.TerminalGen,
		}); err != nil {
			s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("drive settle mark failed: %v", err)})
		}
	}
}

// enqueueOwnCallerWatchSendTokens enqueues this session's pending caller-targeted
// watch-send tokens onto its OWN rail. A drive turn launched on the
// hasPendingWatchSends signal (drive signal b) may have no token queued yet; this
// puts the caller frames on the rail so they render in the same drive turn. It is
// caller-only — it does not deliver delegate-targeted observer frames, which the loop
// boundary handles — so it never duplicates a sidecar delivery.
func (s *Session) enqueueOwnCallerWatchSendTokens() {
	if s == nil || s.jobManager == nil || s.jobManager.enqueue == nil {
		return
	}
	for _, delivery := range s.jobManager.pendingWatchSendDeliveries(nil) {
		if delivery.state.Key.ResolvedSendTo != runtimeMessageAliasCaller {
			continue
		}
		s.jobManager.enqueue(watchSendTokenNotification("", delivery.state))
	}
}

// drainJobManagerWatchSends drains jm through the session that owns jm. The
// receiver is the control surface for delegate-targeted sends; childSessionID is
// only the caller-token routing marker used when a parent scans a child store.
func (s *Session) drainJobManagerWatchSends(ctx context.Context, jm *jobManager, childSessionID string) (watchSendDrainResult, error) {
	var result watchSendDrainResult
	var errs []error
	retrySettled, err := jm.retryStableWatchSettlements()
	if err != nil {
		return result, err
	}
	if !retrySettled {
		return result, nil
	}
	for _, delivery := range jm.pendingWatchSendDeliveries(nil) {
		target := delivery.state.Key.ResolvedSendTo
		if target == runtimeMessageAliasCaller {
			// Caller sends render on the OWNING session's own rail. Tokens are
			// enqueued at observation time; this re-token covers restored /
			// crash-recovered pendings. Duplicates are harmless (render-by-key +
			// batch dedupe). A mid-level owner's caller frame renders in the mid's
			// OWN drive turn (spec §3 mid-owner caller sends): the parent drives
			// the mid on the pending-watch-send signal, and the mid renders here
			// against its own rail (childSessionID == "" from the mid's view). The
			// v2 child-iteration re-route — re-tokening a child's caller pending
			// onto the PARENT's rail with ChildSessionID set — is deleted; the
			// drive signal replaces it, so only the owning session enqueues its
			// own caller token.
			if jm.enqueue != nil && childSessionID == "" {
				jm.enqueue(watchSendTokenNotification("", delivery.state))
			}
			continue
		}
		delivered, err := jm.deliverPendingWatchSend(delivery.cfg, delivery.state, true)
		result.observerHandoff = result.observerHandoff || delivered
		if err != nil {
			errs = append(errs, err)
		}
	}
	return result, errors.Join(errs...)
}

func (s *Session) retryRestoredPendingWatchSends(_ context.Context) error {
	if s == nil || s.jobManager == nil {
		return nil
	}
	deliveries := s.jobManager.pendingWatchSendDeliveries(nil)
	for _, delivery := range deliveries {
		if delivery.state.Key.ResolvedSendTo == runtimeMessageAliasCaller {
			if s.jobManager != nil && s.jobManager.enqueue != nil {
				s.jobManager.enqueue(watchSendTokenNotification("", delivery.state))
			}
			continue
		}
		if delivery.state.StableReceiver {
			s.jobManager.kick()
			continue
		}
		if err := s.jobManager.dropWatchSend(delivery.state, delivery.cfg, "retired loose watch receiver"); err != nil {
			return err
		}
	}
	return nil
}

func (jm *jobManager) pendingWatchSendDeliveries(include func(*jobstore.WatchSendState) bool) []pendingWatchSendDelivery {
	jm.mu.Lock()

	var all []pendingWatchSendDelivery
	seen := make(map[*watchConfig]bool)
	collect := func(cfg *watchConfig) {
		if cfg == nil || seen[cfg] {
			return
		}
		seen[cfg] = true
		for _, key := range cfg.pendingOrder {
			state := cfg.pending[key]
			if state == nil {
				continue
			}
			copied := *state
			all = append(all, pendingWatchSendDelivery{cfg: cfg, state: copied})
		}
	}
	for _, cfg := range jm.watches {
		collect(cfg)
	}
	for cfg := range jm.terminalFlush {
		collect(cfg)
	}
	jm.mu.Unlock()

	deliveries := all[:0]
	for _, delivery := range all {
		if include == nil || include(&delivery.state) {
			deliveries = append(deliveries, delivery)
		}
	}
	return deliveries
}

func (jm *jobManager) kick() {
	if jm.wake != nil {
		jm.wake()
	}
}

// hasLiveUnfiredWatchOnTarget reports whether any ARMED watch that has not yet
// matched its condition targets jobID. Progress ticks and teardown notices are
// deliberately not condition fires: they say the watcher is alive, not that
// the model's requested condition occurred. The undisposed-background-job
// announcement reads this directly rather than inferring "watched" from watch
// frames in the notification queue: on a long progress interval the queue is
// empty between frames, and inferring from it would re-announce — and eventually
// kill — a job the model explicitly said it was waiting on. A cleared watch
// (model-cleared, or the delivery-budget auto-clear) leaves this map, so the job
// counts as undisposed again. A watch that has just tripped the budget is
// retained as a temporary excuse while its asynchronous teardown persists the
// clear and queues the final notification; otherwise the drain can announce in
// that handoff window before it receives the notification that explains the
// clear. That window is keyed on conditionFires, the counter the breaker
// latches on, so it closes when the teardown removes the config.
func (jm *jobManager) hasLiveUnfiredWatchOnTarget(jobID string) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for key, cfg := range jm.watches {
		if key.Target == jobID && (cfg.conditionFires == 0 || cfg.conditionFires >= watchDeliveryBudget) {
			return true
		}
	}
	return false
}

// hasPendingWatchSends reports whether any live or terminal-flush watch config
// holds undelivered pending sends. Drain-loop tails use it to decide whether a
// wake needs a drain pass.
func (jm *jobManager) hasPendingWatchSends() bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if len(jm.stableWatchSettlementRetries) != 0 || jm.stableWatchSettlementRetrying {
		return true
	}
	for _, cfg := range jm.watches {
		if len(cfg.pendingOrder) > 0 {
			return true
		}
	}
	for cfg := range jm.terminalFlush {
		if len(cfg.pendingOrder) > 0 {
			return true
		}
	}
	return false
}

func resolveWatchSendTarget(target, watchedJobID string) (string, error) {
	if target != runtimeMessageAliasWatched {
		return target, nil
	}
	// New watch configurations reject the watched alias. Keep resolution here
	// so historical pending sends can still be folded, drained, or cleared.
	if watchedJobID == "" || isWatchSessionTarget(watchedJobID) {
		return "", errors.New("watched_unresolved")
	}
	return watchedJobID, nil
}

func (jm *jobManager) buildWatchFrame(cfg *watchConfig, jobID string, trigger string, deliveryID string, ev events.SessionEvent, p *provenance.Causal) string {
	if cfg == nil || cfg.send == nil {
		return ""
	}

	var b strings.Builder
	message := limitWatchText(strings.TrimSpace(cfg.send.Message), watchMessageMaxChars)
	if message != "" {
		b.WriteString(message)
		b.WriteString("\n\n")
	}
	b.WriteString("Watch frame\n")
	if cfg.watchID != "" {
		b.WriteString("watch_id: ")
		b.WriteString(limitWatchText(cfg.watchID, watchTriggerMaxChars))
		b.WriteString("\n")
	}
	if deliveryID != "" {
		b.WriteString("delivery_id: ")
		b.WriteString(limitWatchText(deliveryID, watchTriggerMaxChars))
		b.WriteString("\n")
	}
	b.WriteString("job_id: ")
	b.WriteString(limitWatchText(jobID, watchTriggerMaxChars))
	b.WriteString("\n")
	writeWatchFrameTopField(&b, "trigger", limitWatchText(trigger, watchTriggerMaxChars))
	writeWatchFrameProvenance(&b, p)
	writeWatchFrameEvent(&b, ev)

	if !isWatchSessionTarget(jobID) && cfg.send.IncludeExcerpt {
		excerpt, _, truncated, err := jm.readOutput(jobID, watchExcerptTailBytes)
		b.WriteString("excerpt:\n")
		if err != nil {
			writeWatchFrameIndentedBlock(&b, "output_read_error: "+limitWatchText(err.Error(), watchReadErrorMaxChars))
		} else {
			writeWatchFrameIndentedBlock(&b, limitWatchText(excerpt, watchExcerptMaxChars))
			if truncated {
				writeWatchFrameIndentedBlock(&b, "[excerpt truncated]")
			}
		}
	}

	return limitWatchText(b.String(), watchFrameMaxChars)
}

// appendWatchFrameJobRead teaches a recipient how to read the concrete job
// named by a finished-job notification. It is appended after the frame cap so
// the annotation is never truncated away.
func appendWatchFrameJobRead(frame, jobID string) string {
	if frame == "" || jobID == "" {
		return frame
	}
	if !strings.HasSuffix(frame, "\n") {
		frame += "\n"
	}
	return frame + `read with: read_transcript(transcript_ref="job:` + jobID + `")` + "\n"
}

func writeWatchFrameIndentedBlock(b *strings.Builder, text string) {
	text = normalizeWatchFrameLineEndings(text)
	for line := range strings.SplitSeq(text, "\n") {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteString("\n")
	}
}

func normalizeWatchFrameLineEndings(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return text
}

func writeWatchFrameTopField(b *strings.Builder, name, value string) {
	value = normalizeWatchFrameLineEndings(value)
	b.WriteString(name)
	b.WriteString(": ")
	b.WriteString(strings.ReplaceAll(value, "\n", "\n  "))
	b.WriteString("\n")
}

func writeWatchFrameProvenance(b *strings.Builder, p *provenance.Causal) {
	if p == nil || len(p.WatchKeys) == 0 {
		b.WriteString("provenance: external\n")
		return
	}
	b.WriteString("provenance:\n")
	b.WriteString("  watch_keys:\n")
	for _, key := range p.WatchKeys {
		b.WriteString("    - watch_id: ")
		b.WriteString(limitWatchText(key.WatchID, watchTriggerMaxChars))
		b.WriteString("\n      watch_generation: ")
		b.WriteString(limitWatchText(key.WatchGeneration, watchTriggerMaxChars))
		b.WriteString("\n")
	}
	if latest := provenance.LatestDeliveryID(p); latest != "" {
		b.WriteString("  latest_delivery_id: ")
		b.WriteString(limitWatchText(latest, watchTriggerMaxChars))
		b.WriteString("\n")
	}
}

func writeWatchFrameEvent(b *strings.Builder, ev events.SessionEvent) {
	switch data := ev.Data.(type) {
	case events.CommunicateData:
		writeCommunicateWatchEvent(b, data)
	case *events.CommunicateData:
		if data != nil {
			writeCommunicateWatchEvent(b, *data)
		}
	case events.AssistantTextEndData:
		writeAssistantMessageWatchEvent(b, data)
	case *events.AssistantTextEndData:
		if data != nil {
			writeAssistantMessageWatchEvent(b, *data)
		}
	case events.ToolCallEndData:
		writeAssistantToolWatchEvent(b, data)
	case *events.ToolCallEndData:
		if data != nil {
			writeAssistantToolWatchEvent(b, *data)
		}
	case events.JobFinishedData:
		writeJobNotificationWatchEvent(b, data)
	case *events.JobFinishedData:
		if data != nil {
			writeJobNotificationWatchEvent(b, *data)
		}
	}
}

func writeCommunicateWatchEvent(b *strings.Builder, data events.CommunicateData) {
	// maxMessageChars caps the event-payload excerpt; tighter than the frame's own
	// message cap because this is context, not the primary message.
	const maxMessageChars = 1000
	message := limitWatchText(data.Message, maxMessageChars)
	truncated := message != data.Message
	b.WriteString("event:\n")
	b.WriteString("  kind: communicate\n")
	writeWatchFrameTextField(b, "message", message)
	b.WriteString("  end_turn: ")
	if data.EndTurn {
		b.WriteString("true\n")
	} else {
		b.WriteString("false\n")
	}
	b.WriteString("  truncated: ")
	if truncated {
		b.WriteString("true\n")
	} else {
		b.WriteString("false\n")
	}
}

func writeAssistantMessageWatchEvent(b *strings.Builder, data events.AssistantTextEndData) {
	text, truncated := limitedWatchEventText(data.Text)
	b.WriteString("event:\n")
	b.WriteString("  kind: assistant.message\n")
	writeWatchFrameOptionalField(b, "model", data.Model)
	writeWatchFrameOptionalField(b, "finish_reason", data.FinishReason)
	writeWatchFrameTextField(b, "text", text)
	writeWatchFrameBoolField(b, "truncated", truncated)
}

func writeAssistantToolWatchEvent(b *strings.Builder, data events.ToolCallEndData) {
	b.WriteString("event:\n")
	b.WriteString("  kind: assistant.tool\n")
	writeWatchFrameOptionalField(b, "tool_name", data.ToolName)
	writeWatchFrameOptionalField(b, "call_id", data.CallID)
	if data.Error != "" {
		writeWatchFrameOptionalField(b, "status", "error")
	} else {
		writeWatchFrameOptionalField(b, "status", "ok")
	}
	writeWatchFrameOptionalField(b, "arguments_json", data.ArgumentsJSON)
	if data.Output != "" {
		output, truncated := limitedWatchEventText(data.Output)
		writeWatchFrameTextField(b, "output", output)
		writeWatchFrameBoolField(b, "output_truncated", truncated)
	}
	if data.Error != "" {
		errText, truncated := limitedWatchEventText(data.Error)
		writeWatchFrameTextField(b, "error", errText)
		writeWatchFrameBoolField(b, "error_truncated", truncated)
	}
}

func writeJobNotificationWatchEvent(b *strings.Builder, data events.JobFinishedData) {
	b.WriteString("event:\n")
	b.WriteString("  kind: job.notification\n")
	writeWatchFrameOptionalField(b, "job_id", data.JobID)
	writeWatchFrameOptionalField(b, "job_type", data.JobType)
	writeWatchFrameOptionalField(b, "status", data.Status)
	writeWatchFrameOptionalField(b, "reason", data.Reason)
	if data.ExitCode != nil {
		writeWatchFrameOptionalField(b, "exit_code", strconv.Itoa(*data.ExitCode))
	}
	b.WriteString("  output_bytes: ")
	b.WriteString(strconv.FormatInt(data.OutputBytes, 10))
	b.WriteString("\n")
}

func limitedWatchEventText(s string) (string, bool) {
	const maxEventTextChars = 1000
	limited := limitWatchText(s, maxEventTextChars)
	return limited, limited != s
}

func writeWatchFrameOptionalField(b *strings.Builder, name, value string) {
	value = limitWatchText(value, watchTriggerMaxChars)
	if value == "" {
		return
	}
	writeWatchFrameTextField(b, name, value)
}

func writeWatchFrameTextField(b *strings.Builder, name, value string) {
	value = normalizeWatchFrameLineEndings(value)
	b.WriteString("  ")
	b.WriteString(name)
	b.WriteString(": ")
	b.WriteString(strings.ReplaceAll(value, "\n", "\n    "))
	b.WriteString("\n")
}

func writeWatchFrameBoolField(b *strings.Builder, name string, value bool) {
	b.WriteString("  ")
	b.WriteString(name)
	b.WriteString(": ")
	if value {
		b.WriteString("true\n")
	} else {
		b.WriteString("false\n")
	}
}

func limitWatchText(s string, maxChars int) string {
	if maxChars <= 0 || len([]rune(s)) <= maxChars {
		return s
	}
	indicator := watchTruncatedIndicator
	keep := maxChars - len([]rune(indicator))
	if keep <= 0 {
		return string([]rune(s)[:maxChars])
	}
	return string([]rune(s)[:keep]) + indicator
}

func (jm *jobManager) enqueueWatchNotifications(notifications []jobNotification) {
	_ = jm.enqueueNotifications(jm.routeWatchNotifications(notifications))
}

// routeWatchNotifications delivers every watch notification addressed to
// another session and RETURNS the ones bound for this session's own rail,
// rather than queueing them. A caller that has more to say about the same
// event (armFinalizedJob, which also has the job's terminal status) can then
// queue the whole group with one wake; enqueueWatchNotifications is the
// nothing-else-to-add case.
func (jm *jobManager) routeWatchNotifications(notifications []jobNotification) []jobNotification {
	if len(notifications) == 0 {
		return nil
	}
	jm.watchNotifyMu.Lock()
	defer jm.watchNotifyMu.Unlock()
	jm.mu.Lock()
	closing := jm.closing
	jm.mu.Unlock()
	if closing {
		return nil
	}
	var own []jobNotification
	type receiverGroup struct {
		sessionID string
		notify    func(jobNotification)
		hold      func() func()
		notices   []jobNotification
	}
	var receiverGroups []receiverGroup
	groupBySession := make(map[string]int)
	for _, n := range notifications {
		if n.receiverNotify != nil {
			if n.receiverHoldWake == nil {
				enqueue := n.receiverNotify
				n.receiverNotify = nil
				n.receiverHoldWake = nil
				enqueue(n)
				continue
			}
			group, ok := groupBySession[n.receiverSessionID]
			if !ok {
				group = len(receiverGroups)
				groupBySession[n.receiverSessionID] = group
				receiverGroups = append(receiverGroups, receiverGroup{
					sessionID: n.receiverSessionID,
					notify:    n.receiverNotify,
					hold:      n.receiverHoldWake,
				})
			}
			n.receiverNotify = nil
			n.receiverHoldWake = nil
			receiverGroups[group].notices = append(receiverGroups[group].notices, n)
			continue
		}
		if n.receiverSessionID != "" && n.receiverSessionID != jm.sessionID {
			continue
		}
		own = append(own, n)
	}
	for _, group := range receiverGroups {
		release := group.hold()
		func() {
			defer release()
			for _, n := range group.notices {
				group.notify(n)
			}
		}()
	}
	return own
}

func (jm *jobManager) watchCount() int {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return len(jm.watches)
}
