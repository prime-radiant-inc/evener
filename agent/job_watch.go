package agent

import (
	"fmt"
	"regexp"
	"sort"

	"primeradiant.com/serf/agent/internal/jobstore"
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
	triggerEvent       string
	triggerEvery       int
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
		cfg.initProgressStop()
		jm.watches[key] = cfg
		result := watchResultFromConfig(cfg, true)
		jm.mu.Unlock()
		return result, nil
	}

	cfg.initProgressStop()
	jm.watches[key] = cfg
	result := watchResultFromConfig(cfg, false)
	jm.mu.Unlock()
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
	cfg := &watchConfig{
		target:             a.Target,
		outputMatch:        a.OutputMatch,
		progressIntervalMS: a.ProgressIntervalMS,
		events:             canonicalWatchEvents(a.Events),
		triggerEvent:       a.TriggerEvent,
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

func (cfg *watchConfig) initProgressStop() {
	if cfg.progressIntervalMS > 0 {
		cfg.progressStop = make(chan struct{})
	}
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

func (jm *jobManager) watchCount() int {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return len(jm.watches)
}
