package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/agent/schema"
)

// WatchEventKindNames is the canonical, stable list of model-facing event-kind
// names job_watch accepts. DefJobWatch enumerates them in its description; the
// JobManager gates on them via modelEventKinds. Exported so the provider-side
// capabilityJobControl block (which cannot import agent/events) passes the same
// literal into DefJobWatch (Task 8).
var WatchEventKindNames = []string{"assistant.message", "assistant.tool", "communicate", "job.notification"}

func availableEventKindNames() []string { return append([]string(nil), WatchEventKindNames...) }

// modelEventKinds maps the model-facing event-kind names that job_watch accepts
// (and DefJobWatch enumerates) to the internal events.EventKind taxonomy. This is
// the discoverable vocabulary of spec §5.9; it is intentionally a small, stable
// subset of the full event stream, not every internal kind.
var modelEventKinds = map[string]events.EventKind{
	WatchEventKindNames[0]: events.EventAssistantTextEnd,
	WatchEventKindNames[1]: events.EventToolCallEnd,
	WatchEventKindNames[3]: events.EventJobFinished,
	WatchEventKindNames[2]: events.EventCommunicate,
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
	// watchDeliveryBudget caps model-facing deliveries per watch config before the
	// circuit breaker auto-clears it (spec §4 F1). Hard-coded, no config knob.
	watchDeliveryBudget = 50
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

// watchBudgetClearedMessage is the single final notification text emitted when a
// watch trips the delivery budget (spec §4 F1). The count is the budget itself.
func watchBudgetClearedMessage(target string) string {
	return fmt.Sprintf(
		"watch cleared: %s delivered %d times; re-arm with a tighter condition (higher every, narrower output_match, or longer progress_interval_ms)",
		target, watchDeliveryBudget,
	)
}

type watchKey struct {
	VisibleSessionID string
	Target           string
	SendTo           string
}

type watchConfig struct {
	// id is a stable per-session handle for this watch, assigned at install and
	// preserved across idempotent re-configure; a replacement gets a fresh id.
	id                 string
	watchID            string
	configHash         string
	target             string
	outputMatch        string
	outputMatcher      *jobstore.OutputMatcher
	progressIntervalMS int
	events             []string
	eventKinds         map[events.EventKind]bool
	wildcardEvents     bool
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
	// caller frames + delivered sidecar sends + no-send watch notifications). At
	// watchDeliveryBudget the watch auto-clears (circuit breaker, spec §4 F1).
	// Counted jm-side under jm.mu; survives the observation/drain split with the
	// cfg pointer; a replacement cfg from newWatchConfig starts fresh at 0.
	deliveries int
	// createdAt is the install time of this live watch config, stamped from
	// jm.now() in newWatchConfig and surfaced by job_list (spec §4 F2). A
	// replacement config is a new watch, so it gets a fresh timestamp. Configs
	// reconstructed into terminalFlush on restore are never built via
	// newWatchConfig and so are intentionally left zero (they are not live).
	createdAt time.Time
	// grantsMinted records (send target, watched job) pairs whose observer
	// read grant was appended during this config's lifetime (spec §5.1);
	// per-fire minting skips them to control append noise (FoldGrants is
	// idempotent, so a duplicate would be harmless). Guarded by jm.mu after
	// install; marked only on successful append so a failed mint retries on
	// the next fire. A replacement config starts fresh and simply re-mints.
	grantsMinted map[watchGrantKey]bool
}

// watchGrantKey identifies a minted (observer send target, watched job) read-
// grant pair within one watch config's lifetime. It keys on job ids rather
// than the observer's session id so the per-fire dedup check needs no store
// read: within a config's lifetime a send-target job id never remaps to a
// different child session (resuming an idle observer mints a NEW job id for
// the same session).
type watchGrantKey struct {
	sendTo       string
	watchedJobID string
}

type watchArgs struct {
	Operation          string
	WatchID            string
	Target             string
	OutputMatch        string
	ProgressIntervalMS int
	Events             []string
	Every              int
	Send               *watchSendArgs
	Clear              bool
}

type watchSendArgs struct {
	To             string
	Message        string
	IncludeExcerpt bool
}

type watchSendDeliveryClass int

const (
	watchSendDelivered watchSendDeliveryClass = iota
	watchSendBusy
	watchSendHardFailure
)

type watchResult struct {
	WatchID            string
	Target             string
	Watching           bool
	OutputMatch        string
	Events             []string
	ProgressIntervalMS int
	Send               *watchSendArgs
	ReplacedExisting   bool
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
	delegateGeneration       string
	// provenance is the delivery's causal chain: the triggering event's
	// provenance extended with this watch's own (watch_id, generation) key and a
	// chain entry naming this delivery. Persisted onto the pending WatchSendState
	// so a downstream event this send causes is recognized as the watch's echo.
	provenance *provenance.Causal
	eventKind  events.EventKind
	eventData  events.EventData
}

type restoredWatchConfigKey struct {
	visibleSessionID string
	watchID          string
	target           string
	sendTo           string
	generation       string
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
// removes its pending entry. The caller is responsible for the currency guard
// (isCurrentPendingWatchSend) and for any error-notification behavior. This is
// the model-facing completion for both watch-send rails (delegate sidecar sends
// and caller frames), so it is where the send half of the delivery budget is
// counted; observation/coalescing does not count (spec §4 F1).
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
	jm.mu.Lock()
	crossedBudget := jm.recordWatchDeliveryLocked(cfg)
	jm.mu.Unlock()
	if crossedBudget {
		jm.autoClearWatchOverBudget(cfg)
	}
	return nil
}

// validateWatchConfig runs the target-independent validation a watch install must
// pass — condition presence, send target, config build, and delivery-loop safety —
// and returns the built config. configureWatch routes install validation through it.
// Event-arg shape (validateWatchEventArgs) and session-target shape are validated by
// the caller.
func (jm *jobManager) validateWatchConfig(a watchArgs) (*watchConfig, error) {
	if !watchArgsHasCondition(a) {
		return nil, errors.New("invalid_request: nothing to watch")
	}
	if a.Send != nil {
		if err := jm.validateWatchSendTarget(a.Send.To, a); err != nil {
			return nil, err
		}
	}
	cfg, err := newWatchConfig(a, jm.now())
	if err != nil {
		return nil, err
	}
	if err := validateWatchDeliveryLoop(cfg); err != nil {
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
	// every:1 is the semantic default (fire on each occurrence), so it reads as
	// unset everywhere downstream; the single-concrete-kind requirement applies
	// only to every>1, which actually throttles.
	if a.Every == 1 {
		a.Every = 0
	}
	return nil
}

func (jm *jobManager) configureWatch(a watchArgs) (watchResult, error) {
	if a.Target == "" {
		return watchResult{}, errors.New("invalid_request: target is required")
	}
	if err := normalizeWatchArgs(&a); err != nil {
		return watchResult{}, err
	}
	sendTo := ""
	if a.Send != nil {
		a.Send.To = strings.TrimSpace(a.Send.To)
		sendTo = a.Send.To
	}
	key := watchKey{
		VisibleSessionID: jm.sessionID,
		Target:           a.Target,
		SendTo:           sendTo,
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
				if a.Send != nil {
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
	if a.Clear {
		return jm.clearWatch(key)
	}
	// Session-target shape checks are specific to configureWatch's session targets
	// (caller, *); a concrete launch target never reaches them, so they stay here
	// rather than in the shared install validator.
	if a.OutputMatch != "" && isWatchSessionTarget(a.Target) {
		return watchResult{}, errors.New("invalid_request: output_match requires a concrete job target")
	}
	if a.Send != nil && a.Send.IncludeExcerpt && isWatchSessionTarget(a.Target) {
		return watchResult{}, errors.New("invalid_request: include_excerpt requires a concrete job target; session-target frames carry bounded event payloads, not output excerpts")
	}
	cfg, err := jm.validateWatchConfig(a)
	if err != nil {
		return watchResult{}, err
	}
	// A sidecar watch (concrete job target, send.to = a concrete delegate job)
	// mints its observer read grant BEFORE install so a grant failure fails the
	// creation and never installs the watch (spec §5.1). The replace and
	// idempotent re-configure paths below re-append the same grant; FoldGrants
	// collapses duplicates. The converse residue — an abort below (target
	// vanished, snapshot-append failure) orphaning a just-minted grant — is
	// deliberate: grants are append-only read capabilities, never revoked
	// (spec §5.1), so an orphan grant is harmless.
	if err := jm.mintWatchCreateReadGrant(cfg); err != nil {
		return watchResult{}, err
	}

	jm.mu.Lock()
	if !isWatchSessionTarget(key.Target) && !isWatchableConcreteJobLocked(jm.running[key.Target]) {
		jm.mu.Unlock()
		if err := jm.validateWatchTarget(key.Target); err != nil {
			return watchResult{}, err
		}
		jm.mu.Lock()
		if !isWatchableConcreteJobLocked(jm.running[key.Target]) {
			jm.mu.Unlock()
			return watchResult{}, watchTargetNotFoundError(key.Target)
		}
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
	return a.OutputMatch != "" || a.ProgressIntervalMS > 0 || len(a.Events) > 0
}

// watchArgsIsOutputMatchOnly reports whether a watch request carries an
// output_match condition and NO other trigger source — the only shape eligible
// for terminal catch-up (spec §7.1 "Terminal target"). events/progress/every on a
// terminal target can never fire, so they still fail target_terminal. Clear
// requests are never catch-up.
func watchArgsIsOutputMatchOnly(a watchArgs) bool {
	return !a.Clear &&
		a.OutputMatch != "" &&
		len(a.Events) == 0 &&
		a.Every == 0 &&
		a.ProgressIntervalMS == 0
}

func validateWatchEventArgs(a watchArgs) error {
	for _, name := range a.Events {
		if name == "*" {
			continue
		}
		if _, ok := modelEventKinds[name]; !ok {
			return fmt.Errorf("invalid_request: unknown event kind %q", name)
		}
	}
	if a.Every > 0 {
		if len(a.Events) != 1 {
			return errors.New("invalid_request: every requires exactly one watched event kind")
		}
		if a.Events[0] == "*" {
			return errors.New(`invalid_request: every requires a single concrete event kind, not "*"`)
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
	if run != nil {
		return watchTargetNotFoundError(target)
	}

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

	// TODO(spec §5.9): enforce cross-session watch authorization when Phase 5
	// extends nested-job visibility beyond root-caller-visible targets.
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
	if target == "" {
		return errors.New("invalid_request: send.to is required")
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
		return errors.New("invalid_request: job_id is a job/turn handle; watch sends target delegate_id")
	}
	if !strings.HasPrefix(target, "dlg_") {
		return watchTargetNotFoundError(target)
	}
	delegates, err := jm.store.LoadDelegates()
	if err != nil {
		return err
	}
	d := delegates[target]
	if d == nil {
		return fmt.Errorf("target_not_found: delegate %q not found", target)
	}
	if d.OwnerSessionID != "" && d.OwnerSessionID != jm.sessionID {
		return fmt.Errorf("not_controllable: delegate %q is not owned by this session", target)
	}
	jobID := d.CurrentJobID
	if jobID == "" {
		jobID = d.LatestJobID
	}
	if jobID == "" {
		return fmt.Errorf("target_not_resumable: delegate %q has no job history", target)
	}
	rec, err := findJobRecord(jm, jobID)
	if err != nil {
		return fmt.Errorf("target_not_found: %w", err)
	}
	if rec.OwnerSessionID != "" && rec.OwnerSessionID != jm.sessionID {
		return fmt.Errorf("not_controllable: delegate job %q is owned by descendant session %q; you may only message your own direct delegates", target, rec.OwnerSessionID)
	}
	if rec.Type != jobstore.JobDelegate {
		return fmt.Errorf("target_not_messageable: job %q has type %q", target, rec.Type)
	}
	if d.Status == jobstore.DelegateNotResumable || !d.Resumable {
		return fmt.Errorf("target_not_resumable: delegate %q is %s", target, d.Status)
	}
	if rec.Status.IsTerminal() && (rec.Resumable == nil || !*rec.Resumable) {
		return fmt.Errorf("target_not_resumable: delegate job %q is %s", jobID, rec.Status)
	}
	return nil
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

func newWatchConfig(a watchArgs, createdAt time.Time) (*watchConfig, error) {
	eventKinds, wildcardEvents := resolveEventKinds(a.Events)
	watchID := jobstore.NewWatchID()
	cfg := &watchConfig{
		id:                 watchID,
		watchID:            watchID,
		configHash:         normalizedWatchConfigHash(a),
		target:             a.Target,
		outputMatch:        a.OutputMatch,
		progressIntervalMS: a.ProgressIntervalMS,
		events:             canonicalWatchEvents(a.Events),
		eventKinds:         eventKinds,
		wildcardEvents:     wildcardEvents,
		send:               cloneWatchSendArgs(a.Send),
		generation:         jobstore.NewWatchGeneration(),
		createdAt:          createdAt,
	}
	// every is valid only with exactly one event kind (enforced by validation).
	if a.Every > 0 && len(a.Events) == 1 {
		cfg.triggerKind = modelEventKinds[a.Events[0]]
		cfg.triggerEvery = a.Every
	}
	if a.OutputMatch != "" {
		re, err := regexp.Compile(a.OutputMatch)
		if err != nil {
			return nil, fmt.Errorf("invalid_request: output_match: %w", err)
		}
		cfg.outputMatcher = jobstore.NewOutputMatcher(re)
	}
	return cfg, nil
}

func normalizedWatchConfigHash(a watchArgs) string {
	snapshot := jobstore.WatchConfigSnapshot{
		Target:             a.Target,
		OutputMatch:        a.OutputMatch,
		ProgressIntervalMS: a.ProgressIntervalMS,
		Events:             canonicalWatchEvents(a.Events),
		Every:              a.Every,
	}
	if a.Send != nil {
		snapshot.SendTo = a.Send.To
		snapshot.SendMessage = a.Send.Message
		snapshot.IncludeExcerpt = a.Send.IncludeExcerpt
	}
	b, err := json.Marshal(snapshot)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
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

// validateWatchDeliveryLoop rejects configs that deliver self-generated event
// kinds back into the session that generates them — a structural feedback loop
// regardless of watch target (spec §6.1). assistant.message/assistant.tool/
// communicate (including via the "*" wildcard) are produced by the owning
// session's own turn, and onSessionEvent matches event kinds across every watch
// independent of cfg.target, so delivering such a kind back to the caller (send
// omitted, or send.to=caller) makes each delivery cause the next event.
func validateWatchDeliveryLoop(cfg *watchConfig) error {
	selfDelivery := cfg.send == nil || cfg.send.To == runtimeMessageAliasCaller
	if !selfDelivery {
		return nil
	}
	selfGenerated := cfg.wildcardEvents ||
		cfg.eventKinds[events.EventAssistantTextEnd] ||
		cfg.eventKinds[events.EventToolCallEnd] ||
		cfg.eventKinds[events.EventCommunicate]
	if !selfGenerated {
		return nil
	}
	return errors.New("invalid_request: watching assistant.message/assistant.tool/communicate with delivery back to the caller is a feedback loop (each delivery causes the next event); watch these kinds only with send.to set to an observer delegate")
}

func canonicalWatchEvents(events []string) []string {
	if len(events) == 0 {
		return nil
	}
	out := append([]string(nil), events...)
	sort.Strings(out)
	return out
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
		cfg:                cfg,
		key:                watchKey{VisibleSessionID: jm.sessionID, Target: cfg.target, SendTo: sendTo},
		generation:         cfg.generation,
		updateSeq:          cfg.nextUpdateSeq,
		send:               cloneWatchSendArgs(cfg.send),
		deliveryID:         deliveryID,
		visibleSessionID:   jm.sessionID,
		watchTarget:        cfg.target,
		watchedIdentity:    jobID,
		trigger:            trigger,
		triggerProvenance:  provenance.Clone(root.Provenance),
		delegateGeneration: jm.watchSendDelegateGeneration(sendTo, jobID),
		provenance:         provenance.WithWatch(root.Provenance, cfg.watchID, cfg.generation, deliveryID, root.SessionID, jobID),
		eventKind:          root.Kind,
		eventData:          root.Data,
	}
}

func (jm *jobManager) watchSendDelegateGeneration(sendTo, watchedIdentity string) string {
	target, err := resolveWatchSendTarget(sendTo, watchedIdentity)
	if err != nil || !strings.HasPrefix(target, "dlg_") {
		return ""
	}
	delegates, err := jm.store.LoadDelegates()
	if err != nil {
		return ""
	}
	if delegate := delegates[target]; delegate != nil {
		return delegate.Generation
	}
	return ""
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

func (jm *jobManager) restoreWatchSendPending() error {
	rec, err := jm.store.LoadWatchSends()
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
			visibleSessionID: key.VisibleSessionID,
			watchID:          key.WatchID,
			target:           key.WatchTarget,
			sendTo:           key.ResolvedSendTo,
			generation:       key.WatchGeneration,
		}
		cfg := cfgs[cfgKey]
		if cfg == nil {
			cfg = &watchConfig{
				id:         key.WatchID,
				watchID:    key.WatchID,
				target:     key.WatchTarget,
				send:       &watchSendArgs{To: key.ResolvedSendTo},
				generation: key.WatchGeneration,
				pending:    make(map[jobstore.WatchSendKey]*jobstore.WatchSendState),
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
			if existingKey.VisibleSessionID == key.VisibleSessionID && existingKey.Target == key.Target {
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
		Target:   key.Target,
		Watching: false,
	}, nil
}

func (jm *jobManager) clearWatchByID(watchID string) (watchResult, error) {
	jm.mu.Lock()
	key, cfg, ok := jm.watchConfigByIDLocked(watchID)
	detachedCfgs, detached := jm.detachedWatchSendTerminalSnapshotsByWatchIDLocked(watchID, jobstore.EventWatchSendDropped, "watch cleared", jm.now())
	markWatchConfigsRejectingLocked(detachedCfgs)
	if !ok {
		jm.mu.Unlock()
		clearEvent, err := jm.durableWatchClearEvent(watchID, "cleared")
		if err != nil {
			jm.rollbackWatchConfigsRejecting(detachedCfgs)
			return watchResult{}, err
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
		endReason: "cleared",
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
		Target:   key.Target,
		Watching: false,
	}, nil
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

// recordWatchDeliveryLocked increments the model-facing delivery count for cfg
// and reports whether this increment is the one that crossed the delivery
// budget. The crossing is latched to exactly one true per cfg lifetime: the
// counter only ever rises, so it equals watchDeliveryBudget on a single
// increment. The caller must hold jm.mu. A true result means the caller should
// schedule autoClearWatchOverBudget(cfg) AFTER releasing jm.mu (the auto-clear
// does durable I/O and re-takes jm.mu; it must never run from inside an
// observation's critical section, spec §3).
func (jm *jobManager) recordWatchDeliveryLocked(cfg *watchConfig) (crossedBudget bool) {
	if cfg == nil {
		return false
	}
	cfg.deliveries++
	return cfg.deliveries == watchDeliveryBudget
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

// autoClearWatchOverBudget tears down exactly the one watch config that tripped
// the delivery budget and emits ONE final cleared notification (spec §4 F1). It
// is the circuit breaker's teardown: jm-state mutation + durable drop of pending
// sends + one enqueue + one kick — NO delivery from observation (spec §3). It
// mirrors clearWatch's terminal-snapshot machinery but operates on a single
// (key, cfg) pair, so a no-send watch sharing a target with other watches does
// not over-clear its neighbors.
//
// The reverse lookup under jm.mu doubles as the no-double-fire latch: once the
// cfg is detached, a later in-flight settle that increments past the budget
// finds no live key and returns without re-notifying.
func (jm *jobManager) autoClearWatchOverBudget(cfg *watchConfig) {
	jm.mu.Lock()
	key, ok := watchKeyForConfigLocked(jm, cfg)
	if !ok {
		jm.mu.Unlock()
		return
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
		jm.rollbackWatchConfigSnapshotsRejecting(targets)
		return
	}
	jm.detachWatchConfigSnapshots(targets)
	jm.removeWatchSendTerminalSnapshots(dropped)

	jm.enqueueWatchNotifications([]jobNotification{
		watchNotification("", watchBudgetClearedMessage(cfg.target)),
	})
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
			if existingKey.VisibleSessionID == key.VisibleSessionID && existingKey.Target == key.Target {
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
	for pendingKey := range cfg.pending {
		if watchSendKeyMatchesWatchKey(pendingKey, key) {
			return true
		}
	}
	return false
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
	return watchResult{
		WatchID:            cfg.watchID,
		Target:             cfg.target,
		Watching:           true,
		OutputMatch:        cfg.outputMatch,
		Events:             append([]string(nil), cfg.events...),
		ProgressIntervalMS: cfg.progressIntervalMS,
		Send:               cloneWatchSendArgs(cfg.send),
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
		Target:             cfg.target,
		OutputMatch:        cfg.outputMatch,
		ProgressIntervalMS: cfg.progressIntervalMS,
		Events:             append([]string(nil), cfg.events...),
		Every:              cfg.triggerEvery,
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
// rows for job_list (spec §4 F2). One row per live config, ordered by target
// then send.to for stable output.
// watchHistoryCap bounds the recent-watch ring surfaced by job_list. Old entries
// are trimmed; the ring is a debugging aid, not a durable audit log.
const watchHistoryCap = 16

// watchHistoryEntry is the final state of a watch that has left the active set.
type watchHistoryEntry struct {
	id         string
	target     string
	condition  string
	sendTo     string
	deliveries int
	endReason  string
	endedAt    time.Time
}

// recordWatchEndedLocked appends a watch's final state to the bounded history ring
// so a watch that fired and then left the active set stays legible in job_list. It
// is pure in-memory bookkeeping; the caller holds jm.mu.
func (jm *jobManager) recordWatchEndedLocked(key watchKey, cfg *watchConfig, reason string) {
	if cfg == nil {
		return
	}
	jm.watchHistory = append(jm.watchHistory, watchHistoryEntry{
		id:         cfg.id,
		target:     cfg.target,
		condition:  watchConditionSummary(cfg),
		sendTo:     key.SendTo,
		deliveries: cfg.deliveries,
		endReason:  reason,
		endedAt:    jm.now(),
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
	for i := len(jm.watchHistory) - 1; i >= 0; i-- {
		h := jm.watchHistory[i]
		out = append(out, recentWatchEntry{
			ID:         h.id,
			Target:     h.target,
			Condition:  h.condition,
			SendTo:     h.sendTo,
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
		watches = append(watches, inspectResultFromWatchConfig(key, cfg))
		if cfg != nil && cfg.watchID != "" {
			activeWatchIDs[cfg.watchID] = true
		}
	}
	for cfg := range jm.terminalFlush {
		if cfg == nil || cfg.watchID == "" || activeWatchIDs[cfg.watchID] {
			continue
		}
		watches = append(watches, inspectResultFromDetachedWatchConfig(cfg))
	}
	sort.SliceStable(watches, func(i, j int) bool {
		if watches[i].Target != watches[j].Target {
			return watches[i].Target < watches[j].Target
		}
		if watches[i].SendTo != watches[j].SendTo {
			return watches[i].SendTo < watches[j].SendTo
		}
		return watches[i].WatchID < watches[j].WatchID
	})
	recent := make([]jobWatchInspectToolResult, 0, len(jm.watchHistory))
	for i := len(jm.watchHistory) - 1; i >= 0; i-- {
		recent = append(recent, inspectResultFromWatchHistory(jm.watchHistory[i]))
	}
	return jobWatchListToolResult{Watches: watches, RecentWatches: recent, Count: len(watches)}
}

func (jm *jobManager) inspectWatchByID(watchID string) jobWatchInspectToolResult {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for key, cfg := range jm.watches {
		if cfg != nil && cfg.watchID == watchID {
			return inspectResultFromWatchConfig(key, cfg)
		}
	}
	for cfg := range jm.terminalFlush {
		if cfg != nil && cfg.watchID == watchID {
			return inspectResultFromDetachedWatchConfig(cfg)
		}
	}
	for i := len(jm.watchHistory) - 1; i >= 0; i-- {
		if jm.watchHistory[i].id == watchID {
			return inspectResultFromWatchHistory(jm.watchHistory[i])
		}
	}
	return jobWatchInspectToolResult{WatchID: watchID, Watching: false}
}

func inspectResultFromWatchConfig(key watchKey, cfg *watchConfig) jobWatchInspectToolResult {
	if cfg == nil {
		return jobWatchInspectToolResult{Watching: false}
	}
	return jobWatchInspectToolResult{
		WatchID:    cfg.watchID,
		Target:     cfg.target,
		Watching:   true,
		Condition:  watchConditionSummary(cfg),
		SendTo:     key.SendTo,
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
		Target:     h.target,
		Watching:   false,
		Condition:  h.condition,
		SendTo:     h.sendTo,
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
		entries = append(entries, watchListEntry{
			ID:         cfg.id,
			Target:     cfg.target,
			Condition:  watchConditionSummary(cfg),
			SendTo:     key.SendTo,
			Deliveries: cfg.deliveries,
			CreatedAt:  cfg.createdAt.Format(time.RFC3339Nano),
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Target != entries[j].Target {
			return entries[i].Target < entries[j].Target
		}
		return entries[i].SendTo < entries[j].SendTo
	})
	return entries
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
	if cfg.progressIntervalMS > 0 {
		parts = append(parts, fmt.Sprintf("progress_interval_ms: %d", cfg.progressIntervalMS))
	}
	if cfg.wildcardEvents {
		parts = append(parts, "events: [*]")
	} else if len(cfg.events) > 0 {
		summary := "events: [" + strings.Join(cfg.events, ", ") + "]"
		if cfg.triggerEvery > 0 {
			summary += fmt.Sprintf(" every %d", cfg.triggerEvery)
		}
		parts = append(parts, summary)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ")
}

func (jm *jobManager) onSessionEvent(ev events.SessionEvent) {
	kind := ev.Kind
	data := ev.Data
	var notifications []jobNotification
	var deliveries []watchSendDelivery
	var overBudget []*watchConfig

	jm.mu.Lock()
	for _, cfg := range jm.watches {
		if !isActiveWatchTargetLocked(jm, cfg.target) {
			continue
		}
		if cfg.wildcardEvents {
			if !isSupportedWatchEventKind(kind) {
				continue
			}
		} else if !cfg.eventKinds[kind] {
			continue
		}
		if !watchEventMatchesTarget(cfg.target, data) {
			continue
		}
		if shouldSuppressWatch(cfg, ev.Provenance) {
			continue
		}
		if cfg.triggerEvery > 0 && cfg.triggerKind == kind {
			cfg.eventCount++
			if cfg.eventCount%cfg.triggerEvery != 0 {
				continue
			}
		}
		watchedIdentity := watchEventWatchedIdentity(cfg.target, data)
		if cfg.send != nil {
			if cfg.send.To == runtimeMessageAliasWatched && isWatchSessionTarget(watchedIdentity) {
				continue
			}
			deliveries = append(deliveries, jm.watchSendSnapshot(cfg, watchedIdentity, fmt.Sprintf("event: %s", kind), ev))
		} else {
			notifyJobID := watchedIdentity
			if isWatchSessionTarget(notifyJobID) {
				notifyJobID = ""
			}
			notifications = append(notifications, jm.watchNotificationFromWatch(cfg, notifyJobID, fmt.Sprintf("event: %s", kind), ev.Provenance))
			if jm.recordWatchDeliveryLocked(cfg) {
				overBudget = append(overBudget, cfg)
			}
		}
	}
	jm.mu.Unlock()

	// Called from Session.emit; only persist + wake here so watch delivery does
	// not re-enter session event emission (spec §3).
	jm.enqueueWatchNotifications(notifications)
	jm.recordWatchSendsAndKick(deliveries)
	jm.autoClearOverBudgetWatches(overBudget)
}

// shouldSuppressWatch drops an event whose causal provenance already carries this
// watch's own (watch_id, generation) key: the event is the watch's own downstream
// echo, so re-delivering it would loop. Membership is per-watch and generation-
// scoped, so a recreated watch (new generation) is never silenced by a prior
// generation's deliveries.
func shouldSuppressWatch(cfg *watchConfig, p *provenance.Causal) bool {
	if cfg == nil {
		return false
	}
	return provenance.ContainsWatch(p, cfg.watchID, cfg.generation)
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
			if shouldSuppressWatch(cfg, match.Provenance) {
				continue
			}
			matchRoot := root
			matchRoot.Provenance = provenance.Clone(match.Provenance)
			if cfg.send != nil {
				deliveries = append(deliveries, jm.watchSendSnapshot(cfg, jobID, "output_match: "+match.Line, matchRoot))
			} else {
				notifications = append(notifications, jm.watchNotificationFromWatch(cfg, jobID, "output_match: "+match.Line, match.Provenance))
				if jm.recordWatchDeliveryLocked(cfg) {
					overBudget = append(overBudget, cfg)
				}
			}
		}
	}
	jm.mu.Unlock()

	jm.enqueueWatchNotifications(notifications)
	jm.recordWatchSendsAndKick(deliveries)
	jm.autoClearOverBudgetWatches(overBudget)
}

// tailAfterLastNewline returns the slice of data after its last '\n', or all of
// data if it contains no newline. This is the unterminated final-line tail that
// seeds an attach-scan matcher's carry so a token half-written at attach
// completes through the live FeedAt path (spec §7.1 step 1).
func tailAfterLastNewline(data []byte) []byte {
	if idx := bytes.LastIndexByte(data, '\n'); idx >= 0 {
		return data[idx+1:]
	}
	return data
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
// scan covers), and seeds the carry with the tail after the last newline (so a
// token straddling the attach boundary completes through FeedAt). The actual scan
// runs after the lock is released, in fireAttachScan.
func (jm *jobManager) prepareAttachScanLocked(cfg *watchConfig, run *runningJob) (data []byte, scan bool, err error) {
	if cfg == nil || cfg.outputMatcher == nil {
		return nil, false, nil
	}
	if !isWatchableConcreteJobLocked(run) || run.output == nil {
		return nil, false, nil
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
	cfg.outputMatcher.SeedCarry(tailAfterLastNewline(buf))
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
			watchNotification(jobID, "output_match attach scan skipped: "+limitWatchText(prepErr.Error(), watchReadErrorMaxChars)),
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
// match: recordWatchSendsAndKick for a send watch, or enqueueWatchNotifications +
// over-budget handling for a no-send watch. It runs after jm.mu is released.
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
		delivery := jm.watchSendSnapshot(cfg, jobID, reason, root)
		jm.mu.Unlock()
		jm.recordWatchSendsAndKick([]watchSendDelivery{delivery})
		return true
	}
	var overBudget []*watchConfig
	jm.mu.Lock()
	if jm.recordWatchDeliveryLocked(cfg) {
		overBudget = append(overBudget, cfg)
	}
	jm.mu.Unlock()
	jm.enqueueWatchNotifications([]jobNotification{jm.watchNotificationFromWatch(cfg, jobID, reason, jobProvenanceForWatch(jm, jobID))})
	jm.autoClearOverBudgetWatches(overBudget)
	return true
}

// runTerminalCatchup serves an output_match-only watch on an already-terminal job
// as a one-shot catch-up: it scans the terminal job's retained output and, if a
// line matches, fires once (spec §7.1 "Terminal target"). No live watch is
// installed either way; the result reports terminal_catchup with the terminal
// status, and Fired distinguishes a matched scan from an unmatched one.
//
// The scan uses jm.grepOutput, which reads retained output for both running-but-
// terminal and store-only jobs and — unlike T2's attach scan (ScanRetained) —
// matches the final UNTERMINATED line at EOF. That divergence is intentional: the
// job is dead, so nothing will ever complete the tail; a match on the last line
// of a job whose output has no trailing newline must still count. The frame
// carries the LAST matching line.
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
	// The scan reuses the same compiled regexp so output_match compiles once.
	cfg, err := newWatchConfig(a, jm.now())
	if err != nil {
		return watchResult{}, err
	}
	re := cfg.outputMatcher.Regexp()

	result := watchResult{Target: key.Target, Watching: false, TerminalCatchup: true, Status: string(status)}

	matches, err := jm.grepOutput(key.Target, re)
	if err != nil {
		return watchResult{}, err
	}
	if len(matches) == 0 {
		return result, nil
	}
	result.Fired = true
	reason := "output_match: " + matches[len(matches)-1].Line

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
	for _, e := range expired {
		if jm.watches[e.key] != e.cfg {
			continue
		}
		cfg := e.cfg
		if cfg.outputMatcher != nil {
			trackTerminalFlush := len(cfg.pending) != 0
			for _, match := range cfg.outputMatcher.FlushWithProvenance(root.Provenance) {
				if shouldSuppressWatch(cfg, match.Provenance) {
					continue
				}
				matchRoot := root
				matchRoot.Provenance = provenance.Clone(match.Provenance)
				if cfg.send != nil {
					delivery := jm.watchSendSnapshot(cfg, jobID, "output_match: "+match.Line, matchRoot)
					delivery.allowAfterTerminalExpiry = true
					deliveries = append(deliveries, delivery)
					trackTerminalFlush = true
				} else {
					notifications = append(notifications, jm.watchNotificationFromWatch(cfg, jobID, "output_match: "+match.Line, match.Provenance))
					// A no-send caller notification is delivered for sure, but only
					// after the cfg is removed below, so the drain's later increment
					// would never reach recent_watches. Count it now. Send deliveries
					// are NOT pre-counted: they may still be dropped, and they settle
					// (and count) on their own path.
					cfg.deliveries++
				}
			}
			if trackTerminalFlush {
				jm.rememberDetachedPendingLocked(cfg)
			}
		} else if len(cfg.pending) != 0 {
			jm.rememberDetachedPendingLocked(cfg)
		}
		jm.recordWatchEndedLocked(e.key, e.cfg, e.endReason)
		closeWatchConfig(e.cfg)
		delete(jm.watches, e.key)
	}
	return notifications, deliveries
}

func (jm *jobManager) startProgressTimer(key watchKey, cfg *watchConfig, stop <-chan struct{}) {
	if cfg == nil || cfg.progressIntervalMS <= 0 || stop == nil {
		return
	}
	interval := time.Duration(cfg.progressIntervalMS) * time.Millisecond

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if !jm.fireProgressTick(key, cfg) {
					return
				}
			case <-stop:
				return
			}
		}
	}()
}

func (jm *jobManager) fireProgressTick(key watchKey, cfg *watchConfig) bool {
	var notifications []jobNotification
	var deliveries []watchSendDelivery
	var overBudget bool

	root := events.SessionEvent{SessionID: jm.sessionID, Provenance: jobProvenanceForWatch(jm, cfg.target)}
	jm.mu.Lock()
	if jm.closing {
		jm.mu.Unlock()
		return false
	}
	if jm.watches[key] != cfg {
		jm.mu.Unlock()
		return false
	}
	if !isWatchSessionTarget(cfg.target) && jm.running[cfg.target] == nil {
		jm.mu.Unlock()
		return false
	}
	if shouldSuppressWatch(cfg, root.Provenance) {
		jm.mu.Unlock()
		return true
	}
	if cfg.send != nil {
		deliveries = append(deliveries, jm.watchSendSnapshot(cfg, cfg.target, "progress_tick", root))
	} else {
		notifyJobID := cfg.target
		if isWatchSessionTarget(notifyJobID) {
			notifyJobID = ""
		}
		notifications = append(notifications, jm.watchNotificationFromWatch(cfg, notifyJobID, "progress_tick", root.Provenance))
		overBudget = jm.recordWatchDeliveryLocked(cfg)
	}
	jm.mu.Unlock()

	jm.enqueueWatchNotifications(notifications)
	jm.recordWatchSendsAndKick(deliveries)
	if overBudget {
		jm.autoClearWatchOverBudget(cfg)
	}
	return true
}

func watchNotification(jobID, reason string) jobNotification {
	return jobNotification{
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
	n.Provenance = provenance.WithWatch(root, cfg.watchID, cfg.generation, "", jm.sessionID, jobID)
	return n
}

// startQuietWatchdog arms the quiet-job watchdog for a running delegate. The
// goroutine ticks at delegateQuietCheckInterval and fires one owner
// notification (enqueue + kick only, spec §3) when the delegate has emitted no
// parent-observable activity for delegateQuietWindow, once per quiet stretch.
// stop is closed at finalize. Modeled on startProgressTimer.
func (jm *jobManager) startQuietWatchdog(jobID string, stop <-chan struct{}) {
	if stop == nil {
		return
	}
	// Snapshot the watchdog timing synchronously in the caller's goroutine so the
	// spawned goroutine reads no mutable package global. Tests scale these vars
	// from the test goroutine before the watchdog starts; capturing here gives a
	// happens-before edge. In production the values are effectively constants.
	window := delegateQuietWindow
	checkInterval := delegateQuietCheckInterval
	go func() {
		ticker := time.NewTicker(checkInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if !jm.fireQuietWatchdogTick(jobID, window) {
					return
				}
			case <-stop:
				return
			}
		}
	}()
}

// fireQuietWatchdogTick evaluates one watchdog tick. It returns false (ending
// the goroutine) when the manager is closing or the delegate is no longer a
// running job. Otherwise it enqueues at most one quiet notification per quiet
// stretch and kicks the drain loop. It performs no delivery and never touches
// Session.emit or responseSideEffectsMu (spec §3), mirroring fireProgressTick.
func (jm *jobManager) fireQuietWatchdogTick(jobID string, window time.Duration) bool {
	var notifications []jobNotification

	jm.mu.Lock()
	if jm.closing {
		jm.mu.Unlock()
		return false
	}
	run := jm.running[jobID]
	if run == nil || run.rec == nil || run.rec.Type != jobstore.JobDelegate {
		jm.mu.Unlock()
		return false
	}
	last := run.rec.StartedAt
	if run.rec.LastActivity != nil {
		last = *run.rec.LastActivity
	}
	quiet := jm.now().Sub(last)
	if quiet >= window {
		if !run.quietNotified {
			run.quietNotified = true
			notifications = append(notifications, watchNotification(jobID, quietWatchdogMessage(window, last)))
		}
	} else {
		// Activity resumed within the window: clear the latch so the next quiet
		// stretch fires again.
		run.quietNotified = false
	}
	jm.mu.Unlock()

	if len(notifications) > 0 {
		jm.enqueueWatchNotifications(notifications)
		jm.kick()
	}
	return true
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
	if terr == nil {
		// Mint the observer read grant BEFORE the pending persist so a durable
		// pending send always implies its grant was at least attempted
		// (restore re-delivers pendings without re-running this path).
		jm.mintWatchSendReadGrant(d.cfg, target, d.watchedIdentity)
	}
	state = jm.watchSendState(d, target)
	persisted, perr := jm.persistPendingWatchSend(state, d)
	if perr != nil {
		if d.allowAfterTerminalExpiry && !persisted {
			jm.rememberUnpersistedTerminalPendingWatchSend(d.cfg, state)
		}
		return jobstore.WatchSendState{}, nil, false, perr
	}
	if !persisted {
		return jobstore.WatchSendState{}, nil, false, nil
	}
	if terr != nil {
		return jobstore.WatchSendState{}, nil, false, jm.dropWatchSend(state, d.cfg, terr.Error())
	}
	return state, d.cfg, true, nil
}

// recordWatchSendsAndKick is the observation-side half of watch delivery:
// persist every fired send, enqueue caller wake tokens, kick the owner. It
// never delivers (spec §3); the owner's loop drains and delivers on wake. With
// nothing recorded there is nothing to drain, so the owner is left undisturbed.
func (jm *jobManager) recordWatchSendsAndKick(deliveries []watchSendDelivery) {
	if len(deliveries) == 0 {
		return
	}
	deliveries = jm.snapshotWatchSendFrames(deliveries)
	recorded := false
	kicked := false
	for _, d := range deliveries {
		state, _, ok, err := jm.recordWatchSend(d)
		if err != nil || !ok {
			continue // recordWatchSend already produced diagnostics/drops
		}
		recorded = true
		if state.Key.ResolvedSendTo == runtimeMessageAliasCaller && jm.enqueue != nil {
			jm.enqueue(watchSendTokenNotification("", state))
			kicked = true // enqueueJobNotificationAndNotify wakes internally
		}
	}
	if recorded && !kicked {
		jm.kick()
	}
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
		DeliveryID:         deliveryID,
		UpdateSeq:          d.updateSeq,
		Message:            d.message,
		Frame:              d.frame,
		TriggerIdentity:    d.watchedIdentity,
		TriggerReason:      d.trigger,
		Provenance:         provenance.Clone(d.provenance),
		DelegateGeneration: d.delegateGeneration,
	}
}

// appendWatchReadGrant durably records that observerSessionID may
// job_read_output watchedJobID. Grants are append-only capabilities: never
// revoked on watch clear or expiry, because the observer's main read happens
// after the watched job finishes; output lifetime is bounded by retention
// (spec §5.1).
func (jm *jobManager) appendWatchReadGrant(observerSessionID, watchedJobID string) error {
	return jm.appendEvent(jobstore.Event{
		Kind:              jobstore.EventWatchReadGrant,
		TS:                jm.now(),
		JobID:             watchedJobID,
		ObserverSessionID: observerSessionID,
	})
}

// watchReadGrantObserver resolves a delegate watch-send target to the child
// session id its read grant keys on. ok=false with nil err means the target is
// not a grantable observer; delivery itself reports that route failure.
func (jm *jobManager) watchReadGrantObserver(sendTo string) (childSessionID string, ok bool, err error) {
	if !strings.HasPrefix(sendTo, "dlg_") {
		return "", false, nil
	}
	delegates, err := jm.store.LoadDelegates()
	if err != nil {
		return "", false, err
	}
	d := delegates[sendTo]
	if d == nil || d.ChildSessionID == "" {
		return "", false, nil
	}
	return d.ChildSessionID, true, nil
}

// watchedWorkerSessionID resolves a concrete watched job to the worker child
// session id its transcript ref names, mirroring watchReadGrantObserver. ok is
// false when the job has no local worker session to stamp: not found, not a
// delegate, a cross-project (proj:) transcript ref the hub cannot read meta for,
// or an undecodable ref. There is no error return: this feeds a best-effort
// stamp (the read grant is the load-bearing capability; the observer-link stamp
// is a hub convenience), so every "cannot resolve" reason collapses to ok=false.
func (jm *jobManager) watchedWorkerSessionID(watchedJobID string) (workerSessionID string, ok bool) {
	rec, err := findJobRecord(jm, watchedJobID)
	if err != nil || rec.Type != jobstore.JobDelegate {
		return "", false
	}
	bucketHash, childID, err := decodeRef(rec.TranscriptRef)
	if err != nil || bucketHash != "" {
		// Undecodable, or a cross-project worker whose meta the hub cannot read.
		return "", false
	}
	return childID, true
}

// stampObservedBy records that observerSessionID observes the worker session, on
// the worker's SessionMeta. It is append-only and deduped, mirroring the durable
// read grant it accompanies. Best-effort: the watch's load-bearing artifact is
// the read grant; an unwritable or absent worker meta only costs the hub its
// auto-open convenience, so any error is returned for the caller to swallow
// rather than failing watch installation. Skipped when no project state dir is
// configured (temp/test fallback) or the worker meta cannot be loaded (e.g. the
// worker session has not persisted one yet; inventing one here would race the
// worker's own writes).
func (jm *jobManager) stampObservedBy(workerSessionID, observerSessionID string) error {
	if jm.stateDir == "" || workerSessionID == "" || observerSessionID == "" {
		return nil
	}
	meta, err := schema.LoadSessionMeta(jm.stateDir, workerSessionID)
	if err != nil {
		return err
	}
	for _, id := range meta.ObservedBy {
		if id == observerSessionID {
			return nil
		}
	}
	meta.ObservedBy = append(meta.ObservedBy, observerSessionID)
	return schema.SaveSessionMeta(jm.stateDir, meta)
}

// recordObserverLink resolves the watched job to its worker session and stamps
// the observer link on the worker's meta. Best-effort and never fails the watch:
// an unresolvable worker or a write error only deprives the hub of auto-open.
func (jm *jobManager) recordObserverLink(watchedJobID, observerSessionID string) {
	workerSessionID, ok := jm.watchedWorkerSessionID(watchedJobID)
	if !ok {
		return
	}
	_ = jm.stampObservedBy(workerSessionID, observerSessionID)
}

// grantedJobRead is the read-only view the parent returns for a watch-granted
// cross-session read (spec §5.1): a snapshot of the watched job's record
// (clone, never a live pointer) plus closures over the parent jobManager's
// retained-output readers. Everything reachable through it is jobstore-level
// access guarded by the parent jobManager's own locking — the observer child
// never touches parent Session state.
type grantedJobRead struct {
	record     *jobstore.JobRecord
	readWindow func(bytes int, fromHead bool) (content string, total int64, dropped int64, truncated bool, err error)
	grepOutput func(re *regexp.Regexp) ([]jobstore.Match, error)
}

// lookupGrantedJobRead consults the durable watch read-grant table for
// (observerSessionID, jobID) and, on a hit, returns the read-only view of the
// granted job. A miss, an unreadable or closed store (parent session gone),
// and a granted job whose record is no longer retained all return ok=false:
// the caller preserves its original target_not_found instead of inventing a
// new failure mode for a read the observer was never promised. The method is
// injected into observer children as cfg.spawn.parentGrantedJobRead and is
// called from child goroutines, so it must stay jobstore-level (jobManager +
// store locking only, no Session state).
func (s *Session) lookupGrantedJobRead(observerSessionID, jobID string) (*grantedJobRead, bool) {
	if s == nil || s.jobManager == nil || s.jobManager.store == nil {
		return nil, false
	}
	jm := s.jobManager
	grants, err := jm.store.LoadGrants()
	if err != nil || !grants[observerSessionID][jobID] {
		return nil, false
	}
	rec, err := findJobRecord(jm, jobID)
	if err != nil {
		return nil, false
	}
	return &grantedJobRead{
		record: cloneJobRecord(rec),
		readWindow: func(bytes int, fromHead bool) (string, int64, int64, bool, error) {
			return jm.readJobWindow(jobID, bytes, fromHead)
		},
		grepOutput: func(re *regexp.Regexp) ([]jobstore.Match, error) {
			return jm.grepOutput(jobID, re)
		},
	}, true
}

// mintWatchCreateReadGrant durably grants the observer delegate's child
// session job_read_output on the watched job for a sidecar watch — a concrete
// job target delivered to a concrete delegate job (spec §5.1). The grant keys
// on the observer's child SESSION id, not its job id: frame delivery to an
// idle observer resumes it under a NEW job id, so a job-keyed grant would deny
// the canonical fire → resume → read flow; the session id is stable across
// resumes. Any failure fails the watch creation loudly — installing the
// sidecar watch without its grant would hand the observer frames about a job
// it cannot read, the keyhole this grant exists to open. The caller and
// watched aliases mint nothing here: caller delivery grants nothing, and
// watched is rejected during watch configuration.
func (jm *jobManager) mintWatchCreateReadGrant(cfg *watchConfig) error {
	if cfg.send == nil || isWatchSessionTarget(cfg.target) {
		return nil
	}
	switch cfg.send.To {
	case runtimeMessageAliasCaller, runtimeMessageAliasWatched:
		return nil
	}
	observerSessionID, ok, err := jm.watchReadGrantObserver(cfg.send.To)
	if err == nil && !ok {
		// validateWatchSendTarget proved send.to is a delegate job and the
		// store is append-only, so an unresolvable observer here is a fault.
		err = fmt.Errorf("job %q is not a grantable delegate", cfg.send.To)
	}
	if err == nil {
		err = jm.appendWatchReadGrant(observerSessionID, cfg.target)
	}
	if err != nil {
		return fmt.Errorf("watch read grant for send.to %q: %w", cfg.send.To, err)
	}
	// Surface the observer link to the hub by stamping the watched worker's
	// meta. Best-effort: the read grant above is the load-bearing capability.
	jm.recordObserverLink(cfg.target, observerSessionID)
	// Seed the per-fire dedup: cfg is not yet installed, so no lock is needed.
	cfg.grantsMinted = map[watchGrantKey]bool{
		{sendTo: cfg.send.To, watchedJobID: cfg.target}: true,
	}
	return nil
}

// mintWatchSendReadGrant appends the observer read grant for a fired
// delegate-targeted send whose watched identity resolved to a concrete job
// (spec §5.1). This is the per-fire half of grant minting: wildcard-target
// watches learn the concrete watched job at fire time, and a terminal catch-up
// send never had a create mint.
// Pairs already minted in this config's lifetime are skipped (append-noise
// control; duplicates fold harmlessly), and a failed mint is NOT remembered,
// so the next fire retries. Failure policy: enqueue one diagnostic
// notification and let the send proceed — delivery outranks the grant (the
// observer still learns about the job; at worst it cannot read output until a
// later fire re-mints).
func (jm *jobManager) mintWatchSendReadGrant(cfg *watchConfig, resolvedSendTo, watchedIdentity string) {
	if cfg == nil || resolvedSendTo == runtimeMessageAliasCaller || isWatchSessionTarget(watchedIdentity) {
		return
	}
	key := watchGrantKey{sendTo: resolvedSendTo, watchedJobID: watchedIdentity}
	jm.mu.Lock()
	minted := cfg.grantsMinted[key]
	jm.mu.Unlock()
	if minted {
		return
	}
	observerSessionID, ok, err := jm.watchReadGrantObserver(resolvedSendTo)
	if err == nil && !ok {
		return
	}
	if err == nil {
		err = jm.appendWatchReadGrant(observerSessionID, watchedIdentity)
	}
	if err != nil {
		jm.enqueueWatchNotifications([]jobNotification{
			watchNotification(watchedIdentity, "watch read grant failed: "+limitWatchText(err.Error(), watchReadErrorMaxChars)),
		})
		return
	}
	// Surface the observer link to the hub (best-effort; see mintWatchCreateReadGrant).
	jm.recordObserverLink(watchedIdentity, observerSessionID)
	jm.mu.Lock()
	if cfg.grantsMinted == nil {
		cfg.grantsMinted = make(map[watchGrantKey]bool)
	}
	cfg.grantsMinted[key] = true
	jm.mu.Unlock()
}

// sendMessageFunc delivers a watch-send frame to its resolved target. The
// loop-owned drain passes s.sendDelegateMessage; this is the only delivery path
// for watch sends (observation never delivers, spec §3). A nil sender drops the
// pending send as undeliverable.
type sendMessageFunc func(context.Context, sendMessageArgs) sendMessageResult

func (jm *jobManager) deliverPendingWatchSend(ctx context.Context, cfg *watchConfig, state jobstore.WatchSendState, ensurePending bool, send sendMessageFunc) error {
	if !jm.isCurrentPendingWatchSend(cfg, state) {
		return nil
	}
	if ensurePending {
		if err := jm.appendWatchSendPendingState(cfg, state); err != nil {
			jm.enqueueWatchNotifications([]jobNotification{
				watchNotification(state.Key.ResolvedWatchedIdentity, "watch send pending state failed: "+limitWatchText(err.Error(), watchReadErrorMaxChars)),
			})
			return err
		}
	}
	if send == nil {
		return jm.dropWatchSend(state, cfg, "delivery unavailable")
	}
	if stale, reason, err := jm.staleDelegateWatchSend(state); err != nil {
		jm.enqueueWatchNotifications([]jobNotification{
			watchNotification(state.Key.ResolvedWatchedIdentity, "watch send gate check failed: "+limitWatchText(err.Error(), watchReadErrorMaxChars)),
		})
		return err
	} else if stale {
		return jm.dropWatchSend(state, cfg, reason)
	}
	res := send(ctx, sendMessageArgs{
		Target:        state.Key.ResolvedSendTo,
		Message:       state.Frame,
		OnIdle:        "start",
		Background:    true,
		BackgroundSet: true,
		FromWatch:     true,
		Provenance:    provenance.Clone(state.Provenance),
	})
	switch classifyWatchSendDelivery(res) {
	case watchSendDelivered:
		if !jm.isCurrentPendingWatchSend(cfg, state) {
			return nil
		}
		if err := jm.settleWatchSendDelivered(cfg, state); err != nil {
			jm.enqueueWatchNotifications([]jobNotification{
				watchNotification(state.Key.ResolvedWatchedIdentity, "watch send delivered state failed: "+limitWatchText(err.Error(), watchReadErrorMaxChars)),
			})
			return err
		}
		return nil
	case watchSendBusy:
		return nil
	case watchSendHardFailure:
		reason := "delivery failed"
		if res.Err != nil {
			reason = res.Err.Error()
		}
		return jm.dropWatchSend(state, cfg, reason)
	default:
		return nil
	}
}

func (jm *jobManager) staleDelegateWatchSend(state jobstore.WatchSendState) (bool, string, error) {
	target := state.Key.ResolvedSendTo
	if !strings.HasPrefix(target, "dlg_") {
		return false, "", nil
	}
	delegates, err := jm.store.LoadDelegates()
	if err != nil {
		return false, "", err
	}
	delegate := delegates[target]
	if delegate == nil {
		return false, "", nil
	}
	if state.DelegateGeneration == "" {
		if delegate.StopGateClosed {
			return true, "delegate stopped before delivery", nil
		}
		stopped, err := jm.delegateStoppedAfterWatchSendPending(target, state)
		if err != nil {
			return false, "", err
		}
		if stopped {
			return true, "delegate stopped before delivery", nil
		}
		return false, "", nil
	}
	if delegate.StopGateClosed || delegate.Generation != state.DelegateGeneration {
		return true, "delegate stopped before delivery", nil
	}
	return false, "", nil
}

func (jm *jobManager) delegateStoppedAfterWatchSendPending(delegateID string, state jobstore.WatchSendState) (bool, error) {
	events, err := jm.store.LoadEvents()
	if err != nil {
		return false, err
	}
	pendingSeq := watchSendPendingCreationSeq(events, state)
	if pendingSeq == 0 {
		return false, nil
	}
	for _, event := range events {
		if event.Kind != jobstore.EventDelegateStopGateClosed || event.DelegateID != delegateID {
			continue
		}
		if event.Seq > pendingSeq {
			return true, nil
		}
	}
	return false, nil
}

func watchSendPendingCreationSeq(events []jobstore.Event, state jobstore.WatchSendState) int64 {
	currentSeq := watchSendCurrentPendingSeq(events, state)
	if currentSeq == 0 {
		return 0
	}
	var settledSeq int64
	for _, event := range events {
		if event.Seq >= currentSeq {
			break
		}
		if isWatchSendTerminalEvent(event.Kind) && watchSendEventMatchesKey(event.WatchSend, state.Key) {
			settledSeq = event.Seq
		}
	}
	for _, event := range events {
		if event.Seq <= settledSeq {
			continue
		}
		if event.Seq > currentSeq {
			break
		}
		if event.Kind == jobstore.EventWatchSendPending && watchSendEventMatchesKey(event.WatchSend, state.Key) {
			return event.Seq
		}
	}
	return 0
}

func watchSendCurrentPendingSeq(events []jobstore.Event, state jobstore.WatchSendState) int64 {
	var seq int64
	for _, event := range events {
		if event.Kind == jobstore.EventWatchSendPending && watchSendEventMatchesState(event.WatchSend, state) {
			seq = event.Seq
		}
	}
	return seq
}

func watchSendEventMatchesKey(eventState *jobstore.WatchSendState, key jobstore.WatchSendKey) bool {
	return eventState != nil && eventState.Key == key
}

func isWatchSendTerminalEvent(kind jobstore.EventKind) bool {
	return kind == jobstore.EventWatchSendDelivered ||
		kind == jobstore.EventWatchSendDropped ||
		kind == jobstore.EventWatchSendEvicted
}

func watchSendEventMatchesState(eventState *jobstore.WatchSendState, state jobstore.WatchSendState) bool {
	if eventState == nil || eventState.Key != state.Key {
		return false
	}
	if state.DeliveryID != "" && eventState.DeliveryID != state.DeliveryID {
		return false
	}
	if state.UpdateSeq != 0 && eventState.UpdateSeq != state.UpdateSeq {
		return false
	}
	return true
}

func classifyWatchSendDelivery(res sendMessageResult) watchSendDeliveryClass {
	if res.WatchSendDeliveryClassSet {
		return res.WatchSendDeliveryClass
	}
	if res.Err == nil {
		return watchSendDelivered
	}
	return watchSendBusy
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
	jm.enqueueWatchNotifications([]jobNotification{
		watchNotification(state.Key.ResolvedWatchedIdentity, "watch send failed: delivery_id="+state.DeliveryID+": "+dropped.DiagnosticReason),
	})
	return nil
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
	key := watchKey{VisibleSessionID: state.Key.VisibleSessionID, Target: state.Key.WatchTarget, SendTo: cfg.sendTo()}
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

func (jm *jobManager) persistPendingWatchSend(state jobstore.WatchSendState, d watchSendDelivery) (bool, error) {
	record := jm.recordWatchSendPending(state, d)
	if len(record.pendingEvents) == 0 {
		return false, nil
	}
	if err := jm.appendWatchSendEvents(record.pendingEvents); err != nil {
		jm.rollbackWatchSendPendingRecord(record)
		jm.enqueueWatchNotifications([]jobNotification{
			watchNotification(state.Key.ResolvedWatchedIdentity, "watch send pending state failed: "+limitWatchText(err.Error(), watchReadErrorMaxChars)),
		})
		return false, err
	}
	var evictionDiagnostics []jobNotification
	for _, eviction := range record.evictions {
		applied, err := jm.appendWatchSendTerminalSnapshots([]watchSendTerminalSnapshot{eviction.terminal})
		if err != nil {
			jm.removeWatchSendTerminalSnapshots(applied)
			jm.enqueueWatchNotifications([]jobNotification{
				watchNotification(state.Key.ResolvedWatchedIdentity, "watch send pending state failed: "+limitWatchText(err.Error(), watchReadErrorMaxChars)),
			})
			return true, err
		}
		jm.removeWatchSendTerminalSnapshots(applied)
		evictionDiagnostics = append(evictionDiagnostics, eviction.diagnostic)
	}
	for _, diagnostic := range evictionDiagnostics {
		jm.enqueueWatchNotifications([]jobNotification{diagnostic})
	}
	return true, nil
}

type watchSendPendingRecord struct {
	pendingEvents []jobstore.Event
	evictions     []watchSendEviction
	cfg           *watchConfig
	key           jobstore.WatchSendKey
	previous      *jobstore.WatchSendState
}

type watchSendEviction struct {
	terminal   watchSendTerminalSnapshot
	diagnostic jobNotification
}

func (jm *jobManager) recordWatchSendPending(state jobstore.WatchSendState, d watchSendDelivery) watchSendPendingRecord {
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
	if cfg.pending == nil {
		cfg.pending = make(map[jobstore.WatchSendKey]*jobstore.WatchSendState)
	}
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
		// one, so it must carry both causes to suppress either's downstream echo.
		state.Provenance = provenance.Union(existing.Provenance, state.Provenance)
	} else {
		cfg.pendingOrder = append(cfg.pendingOrder, state.Key)
	}
	if state.CreatedAt.IsZero() {
		state.CreatedAt = now
	}
	state.UpdatedAt = now
	pendingState := state
	cfg.pending[state.Key] = &pendingState
	if d.allowAfterTerminalExpiry {
		jm.rememberDetachedPendingLocked(cfg)
	}

	record.pendingEvents = []jobstore.Event{{
		Kind:      jobstore.EventWatchSendPending,
		TS:        now,
		WatchSend: &pendingState,
	}}
	overflow := len(cfg.pending) - defaultWatchSendPendingCap
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
	jm.forgetTerminalFlushIfEmptyLocked(cfg)
	return record
}

func (jm *jobManager) rollbackWatchSendPendingRecord(record watchSendPendingRecord) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	cfg := record.cfg
	if cfg == nil || cfg.pending == nil || len(record.pendingEvents) == 0 || record.pendingEvents[0].WatchSend == nil {
		return
	}
	persisted := record.pendingEvents[0].WatchSend
	current := cfg.pending[record.key]
	if current == nil || current.UpdateSeq != persisted.UpdateSeq {
		return
	}
	if record.previous != nil {
		previous := *record.previous
		cfg.pending[record.key] = &previous
	} else {
		deletePendingWatchSendKeyLocked(cfg, record.key)
	}
	jm.forgetTerminalFlushIfEmptyLocked(cfg)
}

func (jm *jobManager) removePendingWatchSend(cfg *watchConfig, key jobstore.WatchSendKey, updateSeq uint64) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	removePendingWatchSendLocked(cfg, key, updateSeq)
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

func (jm *jobManager) detachedWatchSendTerminalSnapshotsByWatchIDLocked(watchID string, kind jobstore.EventKind, reason string, now time.Time) ([]*watchConfig, []watchSendTerminalSnapshot) {
	if watchID == "" {
		return nil, nil
	}
	var cfgs []*watchConfig
	var snapshots []watchSendTerminalSnapshot
	for cfg := range jm.terminalFlush {
		if cfg == nil || cfg.watchID != watchID {
			continue
		}
		snapshot := watchSendTerminalSnapshotsLocked(cfg, kind, reason, now)
		cfgs = append(cfgs, cfg)
		if len(snapshot.events) != 0 {
			snapshots = append(snapshots, snapshot)
		}
	}
	return cfgs, snapshots
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
	if key.SendTo == "" {
		return true
	}
	if key.SendTo == runtimeMessageAliasWatched {
		return true
	}
	return watchConfigSendToMatchesWatchKey(cfg, key)
}

func watchConfigSendToMatchesWatchKey(cfg *watchConfig, key watchKey) bool {
	if key.SendTo == "" {
		return true
	}
	return cfg.send != nil && cfg.send.To == key.SendTo
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
	defer jm.mu.Unlock()
	for _, snapshot := range snapshots {
		for _, event := range snapshot.events {
			if event.WatchSend == nil {
				continue
			}
			removePendingWatchSendLocked(snapshot.cfg, event.WatchSend.Key, event.WatchSend.UpdateSeq)
		}
		jm.forgetTerminalFlushIfEmptyLocked(snapshot.cfg)
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
func (s *Session) drainPendingWatchSends(ctx context.Context) error {
	var errs []error
	if s.jobManager != nil {
		errs = append(errs, s.drainJobManagerWatchSends(ctx, s.jobManager, ""))
	}
	if s.subagents != nil {
		for _, child := range s.subagents.sessions() {
			if child == nil || child.jobManager == nil {
				continue
			}
			errs = append(errs, child.drainJobManagerWatchSends(ctx, child.jobManager, child.id))
		}
	}
	s.driveChildrenWithUndeliveredAttention()
	return errors.Join(errs...)
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
		if child == nil {
			continue
		}
		live[child.id] = true
		// Stop-gating (spec §3): a deliberately stopped child is never resurrected
		// by a drive for attention that predates the stop. New work clears the gate.
		if s.childStopGated(child.id) {
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
// (spec §3 stop-gating). On a successful handoff it settles the parent's
// forwarded drive signal for that child.
func (s *Session) driveChildIfNotStopGated(sub *subagent) {
	if sub == nil || sub.sess == nil {
		return
	}
	if s.childStopGated(sub.sess.id) {
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
	recs, err := jm.store.Load()
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
}

// childResumable reports whether the parent holds an OWN delegate record for
// childSessionID that is currently resumable. A child with no parent delegate
// record (descriptor-less) or whose record projects non-resumable is NOT
// resumable — the fallback renders its pendings as "child unreachable:".
func (s *Session) childResumable(childSessionID string) bool {
	if s == nil || s.jobManager == nil || childSessionID == "" {
		return false
	}
	ordered, err := s.jobManager.store.LoadOrdered()
	if err != nil {
		return false
	}
	var latest *jobstore.JobRecord
	for _, rec := range ordered {
		if rec == nil || rec.Type != jobstore.JobDelegate {
			continue
		}
		if _, child, err := decodeRef(rec.TranscriptRef); err != nil || child != childSessionID {
			continue
		}
		latest = rec
	}
	if latest == nil {
		return false
	}
	return s.assessDelegateResumability(latest, delegateResumabilityProjection).Resumable
}

// childStopGated reports whether the LATEST delegate record (durable APPEND
// ORDER, not wall-clock — resolved decision #3) the parent holds for
// childSessionID terminated by deliberate stop (Cancelled/stopped_by_parent).
// A stop-gated child is not driven for attention that predates the stop; new
// work appends a newer record for the same child session and clears the gate
// (spec §3 stop-gating, no resurrection).
func (s *Session) childStopGated(childSessionID string) bool {
	if s == nil || s.jobManager == nil || childSessionID == "" {
		return false
	}
	ordered, err := s.jobManager.store.LoadOrdered()
	if err != nil {
		return false
	}
	var latest *jobstore.JobRecord
	for _, rec := range ordered {
		if rec == nil || rec.Type != jobstore.JobDelegate {
			continue
		}
		if _, child, err := decodeRef(rec.TranscriptRef); err != nil || child != childSessionID {
			continue
		}
		latest = rec
	}
	if latest == nil {
		return false
	}
	return latest.Status == jobstore.StatusCancelled && latest.Reason == "stopped_by_parent"
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
// caller-only — it does not deliver sidecar (send.to=job) frames, which the loop
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
func (s *Session) drainJobManagerWatchSends(ctx context.Context, jm *jobManager, childSessionID string) error {
	var errs []error
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
		if err := jm.deliverPendingWatchSend(ctx, delivery.cfg, delivery.state, true, s.sendDelegateMessage); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Session) retryRestoredPendingWatchSends(ctx context.Context) error {
	if s == nil || s.jobManager == nil {
		return nil
	}
	deliveries := s.jobManager.pendingWatchSendDeliveries(nil)
	for _, delivery := range deliveries {
		class, reason := s.classifyRestoredWatchSendTarget(delivery.state.Key.ResolvedSendTo)
		switch class {
		case watchSendDelivered: // runtime alias (caller): token + kick
			if s.jobManager != nil && s.jobManager.enqueue != nil {
				s.jobManager.enqueue(watchSendTokenNotification("", delivery.state))
			}
		case watchSendBusy:
			continue
		case watchSendHardFailure:
			if err := s.jobManager.dropWatchSend(delivery.state, delivery.cfg, reason); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Session) classifyRestoredWatchSendTarget(target string) (watchSendDeliveryClass, string) {
	target = strings.TrimSpace(target)
	if isRuntimeMessageAlias(target) {
		return watchSendDelivered, ""
	}
	if target == "" {
		return watchSendHardFailure, "target is required"
	}
	if isUnsupportedRuntimeMessageAlias(target) {
		return watchSendHardFailure, fmt.Sprintf("target_not_found: job %q not found", target)
	}
	if s == nil || s.jobManager == nil {
		return watchSendBusy, ""
	}
	if strings.HasPrefix(target, "job_") {
		return watchSendHardFailure, "invalid_request: job_id is a job/turn handle; watch sends target delegate_id"
	}
	if strings.HasPrefix(target, "dlg_") {
		delegates, err := s.jobManager.store.LoadDelegates()
		if err != nil {
			return watchSendBusy, ""
		}
		d := delegates[target]
		if d == nil {
			return watchSendHardFailure, fmt.Sprintf("target_not_found: delegate %q not found", target)
		}
		if d.OwnerSessionID != "" && d.OwnerSessionID != s.id {
			return watchSendHardFailure, fmt.Sprintf("not_controllable: delegate %q is not owned by this session", target)
		}
		jobID := d.CurrentJobID
		if jobID == "" {
			jobID = d.LatestJobID
		}
		if jobID == "" {
			return watchSendHardFailure, fmt.Sprintf("target_not_resumable: delegate %q has no job history", target)
		}
		rec, err := findJobRecord(s.jobManager, jobID)
		if err != nil {
			return watchSendHardFailure, "target_not_found: " + err.Error()
		}
		if rec.OwnerSessionID != "" && rec.OwnerSessionID != s.id {
			return watchSendHardFailure, fmt.Sprintf("not_controllable: delegate job %q is owned by descendant session %q; you may only message your own direct delegates", target, rec.OwnerSessionID)
		}
		if rec.Type != jobstore.JobDelegate {
			return watchSendHardFailure, fmt.Sprintf("target_not_messageable: job %q has type %q", target, rec.Type)
		}
		if d.Status == jobstore.DelegateNotResumable || !d.Resumable {
			return watchSendHardFailure, fmt.Sprintf("target_not_resumable: delegate %q is %s", target, d.Status)
		}
		if rec.Status == jobstore.StatusRunning {
			return watchSendBusy, ""
		}
		if !rec.Status.IsTerminal() {
			return watchSendHardFailure, fmt.Sprintf("target_not_resumable: delegate job %q is %s", jobID, rec.Status)
		}
		if rec.Resumable == nil || !*rec.Resumable {
			return watchSendHardFailure, fmt.Sprintf("target_not_resumable: delegate job %q is %s", jobID, rec.Status)
		}
		assessment := s.assessDelegateResumability(rec, delegateResumabilityProjection)
		if !assessment.Resumable {
			return watchSendHardFailure, "target_not_resumable:" + assessment.Reason
		}
		return watchSendBusy, ""
	}
	rec, err := findJobRecord(s.jobManager, target)
	if err != nil {
		return watchSendHardFailure, "target_not_found: " + err.Error()
	}
	if rec.Type != jobstore.JobDelegate {
		return watchSendHardFailure, fmt.Sprintf("target_not_messageable: job %q has type %q", target, rec.Type)
	}
	if rec.Status == jobstore.StatusRunning {
		return watchSendBusy, ""
	}
	if isRuntimeLostDelegate(rec) {
		assessment := s.assessDelegateResumability(rec, delegateResumabilityProjection)
		if !assessment.Resumable {
			return watchSendHardFailure, "target_not_resumable:" + assessment.Reason
		}
		// Delivering to a terminal delegate resumes it; restore may only project
		// resumability, so keep the frame pending for an explicit later send/retry.
		return watchSendBusy, ""
	}
	if !rec.Status.IsTerminal() || rec.Resumable == nil || !*rec.Resumable {
		return watchSendHardFailure, fmt.Sprintf("target_not_resumable: delegate job %q is %s", target, rec.Status)
	}
	assessment := s.assessDelegateResumability(rec, delegateResumabilityProjection)
	if !assessment.Resumable {
		return watchSendHardFailure, "target_not_resumable:" + assessment.Reason
	}
	// Delivering to a terminal delegate resumes it; restore may only project
	// resumability, so keep the frame pending for an explicit later send/retry.
	return watchSendBusy, ""
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

// hasPendingWatchSends reports whether any live or terminal-flush watch config
// holds undelivered pending sends. Drain-loop tails use it to decide whether a
// wake needs a drain pass.
func (jm *jobManager) hasPendingWatchSends() bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
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

func writeWatchFrameIndentedBlock(b *strings.Builder, text string) {
	text = normalizeWatchFrameLineEndings(text)
	for _, line := range strings.Split(text, "\n") {
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
	b.WriteString("  await_reply: ")
	if data.AwaitReply {
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
		writeWatchFrameOptionalField(b, "exit_code", fmt.Sprint(*data.ExitCode))
	}
	b.WriteString("  output_bytes: ")
	b.WriteString(fmt.Sprint(data.OutputBytes))
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
	if len(notifications) == 0 {
		return
	}
	jm.watchNotifyMu.Lock()
	defer jm.watchNotifyMu.Unlock()
	jm.mu.Lock()
	closing := jm.closing
	jm.mu.Unlock()
	if closing {
		return
	}
	for _, n := range notifications {
		if jm.enqueue != nil {
			jm.enqueue(n)
		}
	}
}

func (jm *jobManager) watchCount() int {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return len(jm.watches)
}
