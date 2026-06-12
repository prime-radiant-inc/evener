package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/jobstore"
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
)

type watchKey struct {
	VisibleSessionID string
	Target           string
	SendTo           string
}

type watchConfig struct {
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
}

type watchArgs struct {
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
	Target             string
	Watching           bool
	OutputMatch        string
	Events             []string
	ProgressIntervalMS int
	Send               *watchSendArgs
	ReplacedExisting   bool
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
}

type restoredWatchConfigKey struct {
	visibleSessionID string
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
		JobID:  state.Key.ResolvedWatchedIdentity,
		Status: jobNotificationEventWatch,
		Reason: state.TriggerReason,
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
// (isCurrentPendingWatchSend) and for any error-notification behavior.
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
	return nil
}

func (jm *jobManager) configureWatch(a watchArgs) (watchResult, error) {
	if a.Target == "" {
		return watchResult{}, errors.New("invalid_request: target is required")
	}
	if a.ProgressIntervalMS < 0 {
		return watchResult{}, errors.New("invalid_request: progress_interval_ms must be non-negative")
	}
	if a.ProgressIntervalMS > 0 && a.ProgressIntervalMS < minWatchProgressIntervalMS {
		a.ProgressIntervalMS = minWatchProgressIntervalMS
	}
	if a.ProgressIntervalMS > maxWatchProgressIntervalMS {
		a.ProgressIntervalMS = maxWatchProgressIntervalMS
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
		return watchResult{}, err
	}
	if err := validateWatchEventArgs(a); err != nil {
		return watchResult{}, err
	}
	if !a.Clear && !watchArgsHasCondition(a) {
		return watchResult{}, errors.New("invalid_request: nothing to watch")
	}
	if !a.Clear && a.Send != nil {
		if err := jm.validateWatchSendTarget(a.Send.To, a); err != nil {
			return watchResult{}, err
		}
	}
	if !a.Clear && a.OutputMatch != "" && isWatchSessionTarget(a.Target) {
		return watchResult{}, errors.New("invalid_request: output_match requires a concrete job target")
	}

	if a.Clear {
		return jm.clearWatch(key)
	}

	cfg, err := newWatchConfig(a)
	if err != nil {
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
		equal := watchConfigsEqual(existing, cfg)
		if equal && len(detachedCfgs) == 0 {
			result := watchResultFromConfig(existing, false)
			jm.mu.Unlock()
			return result, nil
		}
		var targets []watchConfigTerminalSnapshot
		if !equal {
			targets = append(targets, watchConfigTerminalSnapshot{
				key:      key,
				cfg:      existing,
				terminal: watchSendTerminalSnapshotsLocked(existing, jobstore.EventWatchSendDropped, "watch replaced", jm.now()),
			})
		}
		markWatchConfigSnapshotsRejectingLocked(targets)
		markWatchConfigsRejectingLocked(detachedCfgs)
		dropped := append(terminalSnapshots(targets), detached...)
		jm.mu.Unlock()
		applied, err := jm.appendWatchSendTerminalSnapshots(dropped)
		if err != nil {
			jm.removeWatchSendTerminalSnapshots(applied)
			jm.rollbackWatchConfigSnapshotsRejecting(targets)
			jm.rollbackWatchConfigsRejecting(detachedCfgs)
			return watchResult{}, err
		}

		jm.mu.Lock()
		if jm.watches[key] != existing {
			current := jm.watches[key]
			jm.mu.Unlock()
			jm.removeWatchSendTerminalSnapshots(applied)
			if current == nil {
				return watchResult{Target: key.Target, Watching: false}, nil
			}
			return watchResultFromConfig(current, false), nil
		}
		if equal {
			result := watchResultFromConfig(existing, false)
			jm.mu.Unlock()
			jm.removeWatchSendTerminalSnapshots(applied)
			jm.forgetDetachedWatchSendConfigsIfEmpty(detachedCfgs)
			return result, nil
		}
		closeWatchConfig(existing)
		stop := cfg.initProgressStop()
		jm.watches[key] = cfg
		result := watchResultFromConfig(cfg, true)
		jm.mu.Unlock()
		jm.removeWatchSendTerminalSnapshots(applied)
		jm.forgetDetachedWatchSendConfigsIfEmpty(detachedCfgs)
		jm.startProgressTimer(key, cfg, stop)
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
	stop := cfg.initProgressStop()
	jm.watches[key] = cfg
	result := watchResultFromConfig(cfg, false)
	jm.mu.Unlock()
	jm.startProgressTimer(key, cfg, stop)
	return result, nil
}

func watchArgsHasCondition(a watchArgs) bool {
	return a.OutputMatch != "" || a.ProgressIntervalMS > 0 || len(a.Events) > 0
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

func (jm *jobManager) validateWatchSendTarget(target string, a watchArgs) error {
	if target == "" {
		return errors.New("invalid_request: send.to is required")
	}
	switch target {
	case runtimeMessageAliasCaller:
		return nil
	case runtimeMessageAliasWatched:
		return jm.validateWatchedSendTarget(a)
	case "main", "*":
		return watchTargetNotFoundError(target)
	}

	recs, err := jm.listWithError(listFilter{IncludeNested: true})
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if rec.JobID != target {
			continue
		}
		if rec.Type != jobstore.JobDelegate {
			return fmt.Errorf("target_not_messageable: job %q has type %q", target, rec.Type)
		}
		return nil
	}
	return watchTargetNotFoundError(target)
}

func (jm *jobManager) validateWatchedSendTarget(a watchArgs) error {
	watchTarget := a.Target
	if watchTarget == "" {
		return watchTargetNotFoundError(runtimeMessageAliasWatched)
	}
	if isWatchSessionTarget(watchTarget) {
		if watchCanResolveConcreteWatchedTarget(a) {
			return nil
		}
		return watchTargetNotFoundError(runtimeMessageAliasWatched)
	}
	recs, err := jm.listWithError(listFilter{IncludeNested: true})
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if rec.JobID != watchTarget {
			continue
		}
		if rec.Type != jobstore.JobDelegate {
			return fmt.Errorf("target_not_messageable: job %q has type %q", watchTarget, rec.Type)
		}
		return nil
	}
	return watchTargetNotFoundError(watchTarget)
}

// watchCanResolveConcreteWatchedTarget reports whether a wildcard-target watch
// with send.to=watched can ever resolve a concrete job. A concrete job identity
// is carried only by job-carrying event kinds: job.notification and * (wildcard).
// Without at least one such kind in events, watched can never resolve.
func watchCanResolveConcreteWatchedTarget(a watchArgs) bool {
	if a.Target != "*" || a.ProgressIntervalMS > 0 || a.OutputMatch != "" {
		return false
	}
	for _, name := range a.Events {
		if name == "*" || name == "job.notification" {
			return true
		}
	}
	return false
}

func isWatchableConcreteJobLocked(run *runningJob) bool {
	return run != nil && run.terminal == nil && run.finalize == nil
}

func watchTargetNotFoundError(target string) error {
	return fmt.Errorf("target_not_found: job %q not found", target)
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

func newWatchConfig(a watchArgs) (*watchConfig, error) {
	eventKinds, wildcardEvents := resolveEventKinds(a.Events)
	cfg := &watchConfig{
		target:             a.Target,
		outputMatch:        a.OutputMatch,
		progressIntervalMS: a.ProgressIntervalMS,
		events:             canonicalWatchEvents(a.Events),
		eventKinds:         eventKinds,
		wildcardEvents:     wildcardEvents,
		send:               cloneWatchSendArgs(a.Send),
		generation:         jobstore.NewWatchGeneration(),
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

func cloneWatchSendArgs(send *watchSendArgs) *watchSendArgs {
	if send == nil {
		return nil
	}
	clone := *send
	return &clone
}

func (jm *jobManager) watchSendSnapshot(cfg *watchConfig, jobID, trigger string) watchSendDelivery {
	sendTo := ""
	if cfg.send != nil {
		sendTo = cfg.send.To
	}
	cfg.nextUpdateSeq++
	return watchSendDelivery{
		cfg:              cfg,
		key:              watchKey{VisibleSessionID: jm.sessionID, Target: cfg.target, SendTo: sendTo},
		generation:       cfg.generation,
		updateSeq:        cfg.nextUpdateSeq,
		send:             cloneWatchSendArgs(cfg.send),
		deliveryID:       jobstore.NewWatchSendDeliveryID(),
		visibleSessionID: jm.sessionID,
		watchTarget:      cfg.target,
		watchedIdentity:  jobID,
		trigger:          trigger,
	}
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
			target:           key.WatchTarget,
			sendTo:           key.ResolvedSendTo,
			generation:       key.WatchGeneration,
		}
		cfg := cfgs[cfgKey]
		if cfg == nil {
			cfg = &watchConfig{
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
	return a.WatchGeneration < b.WatchGeneration
}

func (jm *jobManager) clearWatch(key watchKey) (watchResult, error) {
	var targets []watchConfigTerminalSnapshot
	jm.mu.Lock()
	detachedCfgs, detached := jm.detachedWatchSendTerminalSnapshotsLocked(key, jobstore.EventWatchSendDropped, "watch cleared", jm.now())
	if key.SendTo != "" {
		cfg := jm.watches[key]
		targets = append(targets, watchConfigTerminalSnapshot{
			key:      key,
			cfg:      cfg,
			terminal: watchSendTerminalSnapshotsLocked(cfg, jobstore.EventWatchSendDropped, "watch cleared", jm.now()),
		})
	} else {
		for existingKey, cfg := range jm.watches {
			if existingKey.VisibleSessionID == key.VisibleSessionID && existingKey.Target == key.Target {
				targets = append(targets, watchConfigTerminalSnapshot{
					key:      existingKey,
					cfg:      cfg,
					terminal: watchSendTerminalSnapshotsLocked(cfg, jobstore.EventWatchSendDropped, "watch cleared", jm.now()),
				})
			}
		}
	}
	markWatchConfigSnapshotsRejectingLocked(targets)
	markWatchConfigsRejectingLocked(detachedCfgs)
	jm.mu.Unlock()
	dropped := append(terminalSnapshots(targets), detached...)
	applied, err := jm.appendWatchSendTerminalSnapshots(dropped)
	if err != nil {
		jm.removeWatchSendTerminalSnapshots(applied)
		jm.rollbackWatchConfigSnapshotsRejecting(targets)
		jm.rollbackWatchConfigsRejecting(detachedCfgs)
		return watchResult{}, err
	}
	jm.detachWatchConfigSnapshots(targets)
	jm.removeWatchSendTerminalSnapshots(applied)
	jm.forgetDetachedWatchSendConfigsIfEmpty(detachedCfgs)

	return watchResult{
		Target:   key.Target,
		Watching: false,
	}, nil
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

func watchConfigsEqual(a, b *watchConfig) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.target != b.target ||
		a.outputMatch != b.outputMatch ||
		a.progressIntervalMS != b.progressIntervalMS ||
		a.triggerKind != b.triggerKind ||
		a.triggerEvery != b.triggerEvery {
		return false
	}
	if !watchEventsEqual(a.events, b.events) {
		return false
	}
	return watchSendArgsEqual(a.send, b.send)
}

func watchEventsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func watchSendArgsEqual(a, b *watchSendArgs) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.To == b.To &&
		a.Message == b.Message &&
		a.IncludeExcerpt == b.IncludeExcerpt
}

func watchResultFromConfig(cfg *watchConfig, replacedExisting bool) watchResult {
	return watchResult{
		Target:             cfg.target,
		Watching:           true,
		OutputMatch:        cfg.outputMatch,
		Events:             append([]string(nil), cfg.events...),
		ProgressIntervalMS: cfg.progressIntervalMS,
		Send:               cloneWatchSendArgs(cfg.send),
		ReplacedExisting:   replacedExisting,
	}
}

func (jm *jobManager) onSessionEvent(kind events.EventKind, data events.EventData) {
	if isWatchOriginEventData(data) {
		return
	}
	var notifications []jobNotification
	var deliveries []watchSendDelivery

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
			deliveries = append(deliveries, jm.watchSendSnapshot(cfg, watchedIdentity, fmt.Sprintf("event: %s", kind)))
		} else {
			notifyJobID := watchedIdentity
			if isWatchSessionTarget(notifyJobID) {
				notifyJobID = ""
			}
			notifications = append(notifications, watchNotification(notifyJobID, fmt.Sprintf("event: %s", kind)))
		}
	}
	jm.mu.Unlock()

	// Called from Session.emit; only persist + wake here so watch delivery does
	// not re-enter session event emission (spec §3).
	jm.enqueueWatchNotifications(notifications)
	jm.recordWatchSendsAndKick(deliveries)
}

func isWatchOriginEventData(data events.EventData) bool {
	switch d := data.(type) {
	case events.JobStartedData:
		return d.FromWatch
	case events.JobFinishedData:
		return d.FromWatch
	default:
		return false
	}
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

func isActiveWatchTargetLocked(jm *jobManager, target string) bool {
	if isWatchSessionTarget(target) {
		return true
	}
	return isWatchableConcreteJobLocked(jm.running[target])
}

func (jm *jobManager) feedJobOutput(jobID string, chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	var notifications []jobNotification
	var deliveries []watchSendDelivery

	jm.mu.Lock()
	for _, cfg := range jm.watches {
		if cfg.target != jobID || cfg.outputMatcher == nil {
			continue
		}
		matches := cfg.outputMatcher.Feed(chunk)
		for _, match := range matches {
			if cfg.send != nil {
				deliveries = append(deliveries, jm.watchSendSnapshot(cfg, jobID, "output_match: "+match))
			} else {
				notifications = append(notifications, watchNotification(jobID, "output_match: "+match))
			}
		}
	}
	jm.mu.Unlock()

	jm.enqueueWatchNotifications(notifications)
	jm.recordWatchSendsAndKick(deliveries)
}

func (jm *jobManager) expireJobWatchesLocked(jobID string) ([]jobNotification, []watchSendDelivery) {
	var notifications []jobNotification
	var deliveries []watchSendDelivery

	for key, cfg := range jm.watches {
		if key.Target != jobID {
			continue
		}
		if cfg.outputMatcher != nil {
			trackTerminalFlush := len(cfg.pending) != 0
			for _, match := range cfg.outputMatcher.Flush() {
				if cfg.send != nil {
					delivery := jm.watchSendSnapshot(cfg, jobID, "output_match: "+match)
					delivery.allowAfterTerminalExpiry = true
					deliveries = append(deliveries, delivery)
					trackTerminalFlush = true
				} else {
					notifications = append(notifications, watchNotification(jobID, "output_match: "+match))
				}
			}
			if trackTerminalFlush {
				jm.rememberDetachedPendingLocked(cfg)
			}
		} else if len(cfg.pending) != 0 {
			jm.rememberDetachedPendingLocked(cfg)
		}
		closeWatchConfig(cfg)
		delete(jm.watches, key)
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
	if cfg.send != nil {
		deliveries = append(deliveries, jm.watchSendSnapshot(cfg, cfg.target, "progress_tick"))
	} else {
		notifyJobID := cfg.target
		if isWatchSessionTarget(notifyJobID) {
			notifyJobID = ""
		}
		notifications = append(notifications, watchNotification(notifyJobID, "progress_tick"))
	}
	jm.mu.Unlock()

	jm.enqueueWatchNotifications(notifications)
	jm.recordWatchSendsAndKick(deliveries)
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
	d.frame = jm.buildWatchFrame(&watchConfig{send: d.send}, d.watchedIdentity, d.trigger, d.deliveryID)
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
			WatchTarget:             d.watchTarget,
			ResolvedWatchedIdentity: d.watchedIdentity,
			ResolvedSendTo:          resolvedSendTo,
			WatchGeneration:         d.generation,
		},
		DeliveryID:      deliveryID,
		UpdateSeq:       d.updateSeq,
		Message:         d.message,
		Frame:           d.frame,
		TriggerIdentity: d.watchedIdentity,
		TriggerReason:   d.trigger,
	}
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
	res := send(ctx, sendMessageArgs{
		Target:        state.Key.ResolvedSendTo,
		Message:       state.Frame,
		Background:    true,
		BackgroundSet: true,
		FromWatch:     true,
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
			errs = append(errs, s.drainJobManagerWatchSends(ctx, child.jobManager, child.id))
		}
	}
	return errors.Join(errs...)
}

func (s *Session) drainJobManagerWatchSends(ctx context.Context, jm *jobManager, childSessionID string) error {
	var errs []error
	for _, delivery := range jm.pendingWatchSendDeliveries(nil) {
		target := delivery.state.Key.ResolvedSendTo
		if target == runtimeMessageAliasCaller {
			// Caller sends deliver via the notification rail. Tokens are enqueued
			// at observation time; this re-token covers restored / crash-recovered
			// pendings. Duplicates are harmless (render-by-key + batch dedupe).
			// A child's caller is the parent: the token rides the PARENT's rail
			// (it owns the accept loop), and ChildSessionID routes render-by-key
			// back to the child's jobManager at accept time.
			if jm.enqueue != nil && childSessionID == "" {
				jm.enqueue(watchSendTokenNotification("", delivery.state))
			} else if s.jobManager != nil && s.jobManager.enqueue != nil {
				s.jobManager.enqueue(watchSendTokenNotification(childSessionID, delivery.state))
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
	if watchedJobID == "" || isWatchSessionTarget(watchedJobID) {
		return "", errors.New("watched_unresolved")
	}
	return watchedJobID, nil
}

func (jm *jobManager) buildWatchFrame(cfg *watchConfig, jobID string, trigger string, deliveryID string) string {
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
	b.WriteString("job_id: ")
	b.WriteString(limitWatchText(jobID, watchTriggerMaxChars))
	if deliveryID != "" {
		b.WriteString("\ndelivery_id: ")
		b.WriteString(limitWatchText(deliveryID, watchTriggerMaxChars))
	}
	b.WriteString("\ntrigger: ")
	b.WriteString(limitWatchText(trigger, watchTriggerMaxChars))

	if cfg.send.IncludeExcerpt {
		b.WriteString("\n")
		excerpt, _, truncated, err := jm.readOutput(jobID, watchExcerptTailBytes)
		b.WriteString("excerpt:\n")
		if err != nil {
			b.WriteString("output_read_error: ")
			b.WriteString(limitWatchText(err.Error(), watchReadErrorMaxChars))
		} else {
			b.WriteString(limitWatchText(excerpt, watchExcerptMaxChars))
			if truncated {
				b.WriteString("\n[excerpt truncated]")
			}
		}
	}

	return limitWatchText(b.String(), watchFrameMaxChars)
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
