package agent

import (
	"fmt"
	"regexp"
	"sort"
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
	WatchEventKindNames[3]: events.EventSubagentEnd, // job lifecycle; repointed to the job event in Phase 6
	WatchEventKindNames[2]: events.EventCommunicate,
}

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

func (jm *jobManager) configureWatch(a watchArgs) (watchResult, error) {
	if a.Target == "" {
		return watchResult{}, fmt.Errorf("invalid_request: target is required")
	}
	if !a.Clear && !watchArgsHasCondition(a) {
		return watchResult{}, fmt.Errorf("invalid_request: nothing to watch")
	}
	if err := jm.validateWatchTarget(a.Target); err != nil {
		return watchResult{}, err
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
		return jm.clearWatch(key), nil
	}

	cfg, err := newWatchConfig(a)
	if err != nil {
		return watchResult{}, err
	}

	jm.mu.Lock()
	existing := jm.watches[key]
	if existing != nil {
		if watchConfigsEqual(existing, cfg) {
			result := watchResultFromConfig(existing, false)
			jm.mu.Unlock()
			return result, nil
		}
		closeWatchConfig(existing)
		stop := cfg.initProgressStop()
		jm.watches[key] = cfg
		result := watchResultFromConfig(cfg, true)
		jm.mu.Unlock()
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
	jm.mu.Unlock()
	if run != nil {
		return nil
	}

	recs, err := jm.store.Load()
	if err != nil {
		return err
	}
	if recs[target] == nil {
		return fmt.Errorf("target_not_found: job %q not found", target)
	}

	// TODO(spec §5.9): enforce cross-session watch authorization when Phase 5
	// extends nested-job visibility beyond root-caller-visible targets.
	return nil
}

func isWatchSessionTarget(target string) bool {
	switch target {
	case "caller", "main", "watched", "*":
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

func (jm *jobManager) clearWatch(key watchKey) watchResult {
	jm.mu.Lock()
	if key.SendTo != "" {
		closeWatchConfig(jm.watches[key])
		delete(jm.watches, key)
	} else {
		for existingKey, cfg := range jm.watches {
			if existingKey.VisibleSessionID == key.VisibleSessionID && existingKey.Target == key.Target {
				closeWatchConfig(cfg)
				delete(jm.watches, existingKey)
			}
		}
	}
	jm.mu.Unlock()

	return watchResult{
		Target:   key.Target,
		Watching: false,
	}
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
	_ = data
	var notifications []jobNotification

	jm.mu.Lock()
	for _, cfg := range jm.watches {
		if !isWatchSessionTarget(cfg.target) {
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
		if cfg.send == nil {
			notifications = append(notifications, jobNotification{})
		}
	}
	jm.mu.Unlock()

	// Called from Session.emit; only enqueue here so watch delivery does not
	// re-enter session event emission.
	jm.enqueueWatchNotifications(notifications)
}

func (jm *jobManager) feedJobOutput(jobID string, chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	var notifications []jobNotification

	jm.mu.Lock()
	for _, cfg := range jm.watches {
		if cfg.target != jobID || cfg.outputMatcher == nil {
			continue
		}
		matches := cfg.outputMatcher.Feed(chunk)
		if cfg.send == nil {
			for range matches {
				notifications = append(notifications, jobNotification{})
			}
		}
	}
	jm.mu.Unlock()

	jm.enqueueWatchNotifications(notifications)
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
	if cfg.send == nil {
		notifications = append(notifications, jobNotification{})
	}
	jm.mu.Unlock()

	jm.enqueueWatchNotifications(notifications)
	return true
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
