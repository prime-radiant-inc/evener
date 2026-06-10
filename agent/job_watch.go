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
	triggerEvent       string
	triggerKind        events.EventKind
	triggerEvery       int
	eventCount         int
	send               *watchSendArgs
	generation         string
	pending            map[jobstore.WatchSendKey]*jobstore.WatchSendState
	pendingOrder       []jobstore.WatchSendKey
	nextUpdateSeq      uint64
	progressStop       chan struct{}
}

type watchArgs struct {
	Target             string
	OutputMatch        string
	ProgressIntervalMS int
	Events             []string
	TriggerEvent       string
	TriggerEvery       int
	Send               *watchSendArgs
	Clear              bool
}

type watchSendArgs struct {
	To             string
	Message        string
	IncludeFrame   bool
	IncludeExcerpt bool
}

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
	cfg              *watchConfig
	key              watchKey
	generation       string
	send             *watchSendArgs
	visibleSessionID string
	watchTarget      string
	watchedIdentity  string
	trigger          string
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
	if err := jm.validateWatchTarget(a.Target); err != nil {
		return watchResult{}, err
	}
	if !a.Clear && !watchArgsHasCondition(a) {
		return watchResult{}, errors.New("invalid_request: nothing to watch")
	}
	if !a.Clear && a.Send != nil {
		a.Send.To = strings.TrimSpace(a.Send.To)
		if err := jm.validateWatchSendTarget(a.Send.To, a); err != nil {
			return watchResult{}, err
		}
	}
	if !a.Clear && a.OutputMatch != "" && isWatchSessionTarget(a.Target) {
		return watchResult{}, errors.New("invalid_request: output_match requires a concrete job target")
	}

	sendTo := ""
	if a.Send != nil {
		sendTo = a.Send.To
	}
	key := watchKey{
		VisibleSessionID: jm.sessionID,
		Target:           a.Target,
		SendTo:           sendTo,
	}

	if a.Clear {
		return jm.clearWatch(key)
	}

	cfg, err := newWatchConfig(a)
	if err != nil {
		return watchResult{}, err
	}

	jm.mu.Lock()
	if run := jm.running[key.Target]; !isWatchSessionTarget(key.Target) && run != nil && !isWatchableConcreteJobLocked(run) {
		jm.mu.Unlock()
		return watchResult{}, watchTargetNotFoundError(key.Target)
	}
	existing := jm.watches[key]
	if existing != nil {
		if watchConfigsEqual(existing, cfg) {
			result := watchResultFromConfig(existing, false)
			jm.mu.Unlock()
			return result, nil
		}
		dropped := watchSendTerminalEventsLocked(existing, jobstore.EventWatchSendDropped, "watch replaced", jm.now())
		closeWatchConfig(existing)
		stop := cfg.initProgressStop()
		jm.watches[key] = cfg
		result := watchResultFromConfig(cfg, true)
		jm.mu.Unlock()
		if err := jm.appendWatchSendEvents(dropped); err != nil {
			return watchResult{}, err
		}
		jm.startProgressTimer(key, cfg, stop)
		return result, nil
	}

	stop := cfg.initProgressStop()
	jm.watches[key] = cfg
	result := watchResultFromConfig(cfg, false)
	jm.mu.Unlock()
	jm.startProgressTimer(key, cfg, stop)
	return result, nil
}

func watchArgsHasCondition(a watchArgs) bool {
	return a.OutputMatch != "" || a.ProgressIntervalMS > 0 || len(a.Events) > 0 || a.TriggerEvent != ""
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
	jm.mu.Unlock()
	if run != nil {
		return watchTargetNotFoundError(target)
	}

	recs, err := jm.store.Load()
	if err != nil {
		return err
	}
	rec := recs[target]
	if rec == nil || rec.Status.IsTerminal() {
		return watchTargetNotFoundError(target)
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
	return nil
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

func watchCanResolveConcreteWatchedTarget(a watchArgs) bool {
	if a.Target != "*" || a.ProgressIntervalMS > 0 || a.OutputMatch != "" {
		return false
	}
	if a.TriggerEvent != "" && a.TriggerEvent != "job.notification" {
		return false
	}
	if len(a.Events) == 0 {
		return a.TriggerEvent == "job.notification"
	}
	for _, eventName := range a.Events {
		if eventName != "job.notification" {
			return false
		}
	}
	return true
}

func isWatchableConcreteJobLocked(run *runningJob) bool {
	return run != nil && run.terminal == nil && run.finalize == nil
}

func watchTargetNotFoundError(target string) error {
	return fmt.Errorf("target_not_found: job %q not found", target)
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
	triggerKind := modelEventKinds[a.TriggerEvent]
	if len(a.Events) == 0 && a.TriggerEvent != "" && triggerKind != "" {
		eventKinds[triggerKind] = true
	}
	cfg := &watchConfig{
		target:             a.Target,
		outputMatch:        a.OutputMatch,
		progressIntervalMS: a.ProgressIntervalMS,
		events:             canonicalWatchEvents(a.Events),
		eventKinds:         eventKinds,
		wildcardEvents:     wildcardEvents,
		triggerEvent:       a.TriggerEvent,
		triggerKind:        triggerKind,
		triggerEvery:       a.TriggerEvery,
		send:               cloneWatchSendArgs(a.Send),
		generation:         jobstore.NewWatchGeneration(),
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
	return watchSendDelivery{
		cfg:              cfg,
		key:              watchKey{VisibleSessionID: jm.sessionID, Target: cfg.target, SendTo: sendTo},
		generation:       cfg.generation,
		send:             cloneWatchSendArgs(cfg.send),
		visibleSessionID: jm.sessionID,
		watchTarget:      cfg.target,
		watchedIdentity:  jobID,
		trigger:          trigger,
	}
}

func (jm *jobManager) clearWatch(key watchKey) (watchResult, error) {
	var dropped []jobstore.Event
	jm.mu.Lock()
	if key.SendTo != "" {
		dropped = append(dropped, watchSendTerminalEventsLocked(jm.watches[key], jobstore.EventWatchSendDropped, "watch cleared", jm.now())...)
		closeWatchConfig(jm.watches[key])
		delete(jm.watches, key)
	} else {
		for existingKey, cfg := range jm.watches {
			if existingKey.VisibleSessionID == key.VisibleSessionID && existingKey.Target == key.Target {
				dropped = append(dropped, watchSendTerminalEventsLocked(cfg, jobstore.EventWatchSendDropped, "watch cleared", jm.now())...)
				closeWatchConfig(cfg)
				delete(jm.watches, existingKey)
			}
		}
	}
	jm.mu.Unlock()
	if err := jm.appendWatchSendEvents(dropped); err != nil {
		return watchResult{}, err
	}

	return watchResult{
		Target:   key.Target,
		Watching: false,
	}, nil
}

func (jm *jobManager) pruneWatchedTargetWatchesLocked(jobID, reason string, now time.Time) []jobstore.Event {
	var dropped []jobstore.Event
	for key, cfg := range jm.watches {
		if key.Target != jobID {
			continue
		}
		dropped = append(dropped, watchSendTerminalEventsLocked(cfg, jobstore.EventWatchSendDropped, reason, now)...)
		closeWatchConfig(cfg)
		delete(jm.watches, key)
	}
	return dropped
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
		a.triggerEvent != b.triggerEvent ||
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
		a.IncludeFrame == b.IncludeFrame &&
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
		if !cfg.wildcardEvents && !cfg.eventKinds[kind] {
			continue
		}
		if cfg.triggerEvent != "" {
			if cfg.triggerKind != kind {
				continue
			}
			cfg.eventCount++
			triggerEvery := cfg.triggerEvery
			if triggerEvery <= 0 {
				triggerEvery = 1
			}
			if cfg.eventCount%triggerEvery != 0 {
				continue
			}
		}
		if cfg.send != nil {
			deliveries = append(deliveries, jm.watchSendSnapshot(cfg, watchEventWatchedIdentity(cfg.target, data), fmt.Sprintf("event: %s", kind)))
		} else {
			notifications = append(notifications, watchNotification(cfg.target, fmt.Sprintf("event: %s", kind)))
		}
	}
	jm.mu.Unlock()

	// Called from Session.emit; only enqueue here so watch delivery does not
	// re-enter session event emission.
	jm.enqueueWatchNotifications(notifications)
	jm.deliverWatchSends(context.Background(), deliveries)
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
	jm.deliverWatchSends(context.Background(), deliveries)
}

func (jm *jobManager) expireJobWatchesLocked(jobID string) ([]jobNotification, []watchSendDelivery) {
	var notifications []jobNotification
	var deliveries []watchSendDelivery

	for key, cfg := range jm.watches {
		if key.Target != jobID {
			continue
		}
		if cfg.outputMatcher != nil {
			for _, match := range cfg.outputMatcher.Flush() {
				if cfg.send != nil {
					deliveries = append(deliveries, jm.watchSendSnapshot(cfg, jobID, "output_match: "+match))
				} else {
					notifications = append(notifications, watchNotification(jobID, "output_match: "+match))
				}
			}
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
		notifications = append(notifications, watchNotification(cfg.target, "progress_tick"))
	}
	jm.mu.Unlock()

	jm.enqueueWatchNotifications(notifications)
	jm.deliverWatchSends(context.Background(), deliveries)
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

func (jm *jobManager) deliverWatchSends(ctx context.Context, deliveries []watchSendDelivery) {
	for _, d := range deliveries {
		jm.deliverWatchSend(ctx, d)
	}
}

func (jm *jobManager) deliverWatchSend(ctx context.Context, d watchSendDelivery) {
	if d.cfg == nil || d.send == nil {
		return
	}
	if !jm.isCurrentWatchSendDelivery(d) {
		return
	}
	target, err := resolveWatchSendTarget(d.send.To, d.watchedIdentity)
	if err != nil {
		jm.enqueueWatchNotifications([]jobNotification{
			watchNotification(d.watchedIdentity, "watch send failed: "+err.Error()),
		})
		return
	}
	state := jm.watchSendState(d, target)
	if jm.send == nil {
		if !jm.isCurrentWatchSendDelivery(d) {
			return
		}
		jm.persistPendingWatchSend(state, d)
		jm.enqueueWatchNotifications([]jobNotification{
			watchNotification(d.watchedIdentity, "watch send failed: delivery unavailable"),
		})
		return
	}
	res := jm.send(ctx, sendMessageArgs{
		Target:        target,
		Message:       state.Frame,
		Background:    true,
		BackgroundSet: true,
		FromWatch:     true,
	})
	if res.Err != nil {
		if !jm.isCurrentWatchSendDelivery(d) {
			return
		}
		jm.persistPendingWatchSend(state, d)
		jm.enqueueWatchNotifications([]jobNotification{
			watchNotification(d.watchedIdentity, "watch send failed: "+limitWatchText(res.Err.Error(), watchReadErrorMaxChars)),
		})
		return
	}
	if !jm.isCurrentWatchSendDelivery(d) {
		return
	}
	delivered := state
	jm.removePendingWatchSend(d.cfg, delivered.Key)
	if err := jm.appendWatchSendEvents([]jobstore.Event{{
		Kind:      jobstore.EventWatchSendDelivered,
		TS:        jm.now(),
		WatchSend: &delivered,
	}}); err != nil {
		jm.enqueueWatchNotifications([]jobNotification{
			watchNotification(d.watchedIdentity, "watch send delivered state failed: "+limitWatchText(err.Error(), watchReadErrorMaxChars)),
		})
	}
}

func (jm *jobManager) watchSendState(d watchSendDelivery, resolvedSendTo string) jobstore.WatchSendState {
	message := limitWatchText(strings.TrimSpace(d.send.Message), watchMessageMaxChars)
	return jobstore.WatchSendState{
		Key: jobstore.WatchSendKey{
			VisibleSessionID:        d.visibleSessionID,
			WatchTarget:             d.watchTarget,
			ResolvedWatchedIdentity: d.watchedIdentity,
			ResolvedSendTo:          resolvedSendTo,
			WatchGeneration:         d.generation,
		},
		DeliveryID:      jobstore.NewWatchSendDeliveryID(),
		Message:         message,
		Frame:           jm.buildWatchFrame(&watchConfig{send: d.send}, d.watchedIdentity, d.trigger),
		TriggerIdentity: d.watchedIdentity,
		TriggerReason:   d.trigger,
	}
}

func (jm *jobManager) isCurrentWatchSendDelivery(d watchSendDelivery) bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.isCurrentWatchSendDeliveryLocked(d)
}

func (jm *jobManager) isCurrentWatchSendDeliveryLocked(d watchSendDelivery) bool {
	return d.cfg != nil && jm.watches[d.key] == d.cfg && d.cfg.generation == d.generation
}

func (jm *jobManager) persistPendingWatchSend(state jobstore.WatchSendState, d watchSendDelivery) {
	events, diagnostics := jm.recordWatchSendPending(state, d)
	if err := jm.appendWatchSendEvents(events); err != nil {
		diagnostics = append(diagnostics, watchNotification(state.Key.ResolvedWatchedIdentity, "watch send pending state failed: "+limitWatchText(err.Error(), watchReadErrorMaxChars)))
	}
	jm.enqueueWatchNotifications(diagnostics)
}

func (jm *jobManager) recordWatchSendPending(state jobstore.WatchSendState, d watchSendDelivery) ([]jobstore.Event, []jobNotification) {
	now := jm.now()
	jm.mu.Lock()
	defer jm.mu.Unlock()

	if !jm.isCurrentWatchSendDeliveryLocked(d) {
		return nil, nil
	}
	cfg := d.cfg
	if cfg.pending == nil {
		cfg.pending = make(map[jobstore.WatchSendKey]*jobstore.WatchSendState)
	}
	if existing := cfg.pending[state.Key]; existing != nil {
		state.CoalescedCount = existing.CoalescedCount + 1
		state.CreatedAt = existing.CreatedAt
	} else {
		cfg.pendingOrder = append(cfg.pendingOrder, state.Key)
	}
	if state.CreatedAt.IsZero() {
		state.CreatedAt = now
	}
	state.UpdatedAt = now
	cfg.nextUpdateSeq++
	state.UpdateSeq = cfg.nextUpdateSeq
	pendingState := state
	cfg.pending[state.Key] = &pendingState

	events := []jobstore.Event{{
		Kind:      jobstore.EventWatchSendPending,
		TS:        now,
		WatchSend: &pendingState,
	}}
	var diagnostics []jobNotification
	for len(cfg.pending) > defaultWatchSendPendingCap {
		evictedKey := cfg.pendingOrder[0]
		cfg.pendingOrder = cfg.pendingOrder[1:]
		evicted := cfg.pending[evictedKey]
		if evicted == nil {
			continue
		}
		delete(cfg.pending, evictedKey)
		evictedState := *evicted
		evictedState.DiagnosticReason = "pending cap exceeded"
		events = append(events, jobstore.Event{
			Kind:      jobstore.EventWatchSendEvicted,
			TS:        now,
			WatchSend: &evictedState,
		})
		diagnostics = append(diagnostics, watchNotification(evictedState.Key.ResolvedWatchedIdentity, "watch send evicted: "+evictedState.TriggerIdentity))
	}
	return events, diagnostics
}

func (jm *jobManager) removePendingWatchSend(cfg *watchConfig, key jobstore.WatchSendKey) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	removePendingWatchSendLocked(cfg, key)
}

func removePendingWatchSendLocked(cfg *watchConfig, key jobstore.WatchSendKey) {
	if cfg == nil || cfg.pending == nil {
		return
	}
	if _, ok := cfg.pending[key]; !ok {
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

func watchSendTerminalEventsLocked(cfg *watchConfig, kind jobstore.EventKind, reason string, now time.Time) []jobstore.Event {
	if cfg == nil || len(cfg.pending) == 0 {
		return nil
	}
	events := make([]jobstore.Event, 0, len(cfg.pending))
	for _, key := range cfg.pendingOrder {
		state := cfg.pending[key]
		if state == nil {
			continue
		}
		terminal := *state
		terminal.DiagnosticReason = reason
		events = append(events, jobstore.Event{
			Kind:      kind,
			TS:        now,
			WatchSend: &terminal,
		})
		delete(cfg.pending, key)
	}
	cfg.pendingOrder = nil
	return events
}

func (jm *jobManager) appendWatchSendEvents(events []jobstore.Event) error {
	for _, e := range events {
		if err := jm.appendEvent(e); err != nil {
			return err
		}
	}
	return nil
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

func (jm *jobManager) buildWatchFrame(cfg *watchConfig, jobID string, trigger string) string {
	if cfg == nil || cfg.send == nil {
		return ""
	}

	message := limitWatchText(strings.TrimSpace(cfg.send.Message), watchMessageMaxChars)
	if !cfg.send.IncludeFrame && !cfg.send.IncludeExcerpt {
		return limitWatchText(message, watchFrameMaxChars)
	}

	var b strings.Builder
	if message != "" {
		b.WriteString(message)
		b.WriteString("\n\n")
	}
	if cfg.send.IncludeFrame {
		b.WriteString("Watch frame\n")
		b.WriteString("job_id: ")
		b.WriteString(limitWatchText(jobID, watchTriggerMaxChars))
		b.WriteString("\ntrigger: ")
		b.WriteString(limitWatchText(trigger, watchTriggerMaxChars))
	}

	if cfg.send.IncludeExcerpt {
		if cfg.send.IncludeFrame {
			b.WriteString("\n")
		}
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
