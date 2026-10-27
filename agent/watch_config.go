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
)

// validateWatchConfig runs the target-independent validation a watch install must
// pass — condition presence, send target, and config build — and returns the built
// config. configureWatch routes install validation through it.
// Event-arg shape (validateWatchEventArgs) and session-target shape are validated by
// the caller.
func (jm *jobManager) validateWatchConfig(a watchArgs, slot string) (*watchConfig, error) {
	if !watchArgsHasCondition(a) {
		return nil, errors.New("invalid_request: nothing to watch")
	}
	if err := validateWatchSendArgs(a); err != nil {
		return nil, err
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
	if result, done, err := jm.validateWatchRequest(a, key); done {
		return result, err
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
	return jm.installWatch(key, cfg, hooks)
}

// validateWatchRequest runs the checks a request must pass before it is
// entitled to an id. done reports that the request was answered here, as a
// clear, a terminal catch-up or a rejection, and result and err are its
// answer; otherwise the request is a valid install.
func (jm *jobManager) validateWatchRequest(a watchArgs, key watchKey) (result watchResult, done bool, err error) {
	if a.Clear && jm.hasWatchClearState(key) {
		result, err := jm.clearWatch(key)
		return result, true, err
	}
	if err := jm.validateWatchTarget(a.Target); err != nil {
		// Clearing is idempotent regardless of target state: once a job goes
		// terminal its concrete watch auto-removes, so a clear on that target must
		// be a no-op success rather than target_terminal. A genuinely-missing
		// target still returns its original target_not_found.
		if a.Clear {
			if _, _, terminal, statusErr := jm.terminalWatchTargetStatus(a.Target); statusErr == nil && terminal {
				result, err := jm.clearWatch(key)
				return result, true, err
			}
			return watchResult{}, true, err
		}
		// An output_match-only watch on an already-terminal job is served as a
		// one-shot catch-up scan of retained output rather than rejected (spec
		// §7.1 "Terminal target"). Any other terminal request, and target_not_found,
		// keep their original error. terminalWatchTargetStatus resolves the terminal
		// status directly rather than parsing the error string.
		if watchArgsIsOutputMatchOnly(a) {
			status, reason, terminal, statusErr := jm.terminalWatchTargetStatus(a.Target)
			if statusErr != nil {
				return watchResult{}, true, statusErr
			}
			if terminal {
				if sendErr := validateWatchSendArgs(a); sendErr != nil {
					return watchResult{}, true, sendErr
				}
				result, err := jm.runTerminalCatchup(a, key, status, reason)
				return result, true, err
			}
		}
		return watchResult{}, true, err
	}
	if err := validateWatchEventArgs(a); err != nil {
		return watchResult{}, true, err
	}
	if err := validateWatchTriggerShape(a); err != nil {
		return watchResult{}, true, err
	}
	if a.Clear {
		result, err := jm.clearWatch(key)
		return result, true, err
	}
	// Session-target shape checks are specific to configureWatch's session targets
	// (caller, *); a concrete launch target never reaches them, so they stay here
	// rather than in the shared install validator.
	if a.OutputMatch != "" && isWatchSessionTarget(a.Target) {
		if len(a.Events) == 1 && a.Events[0] == "communicate" {
			return watchResult{}, true, errors.New("invalid_request: output_match watches concrete job output" + parentObserverRepair(a.ResultTool))
		}
		return watchResult{}, true, errors.New(`invalid_request: output_match watches concrete job output; session-source watches use events and, for assistant.tool, event_filter`)
	}
	if a.Send != nil && a.Send.IncludeExcerpt && isWatchSessionTarget(a.Target) {
		return watchResult{}, true, errors.New("invalid_request: include_excerpt requires a concrete job target; session-target frames carry bounded event payloads, not output excerpts")
	}
	return watchResult{}, false, nil
}

// installWatch registers cfg under key, replacing an existing watch or reviving
// a detached one, and runs the level-trigger attach scan. It takes jm.mu itself.
func (jm *jobManager) installWatch(key watchKey, cfg *watchConfig, hooks watchConfigureHooks) (watchResult, error) {
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
		return jm.replaceWatch(key, existing, cfg, detachedCfgs, detached)
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

// replaceWatch handles a key that already holds a watch: an identical
// config is a no-op that only settles detached frames, a different one replaces
// it. The caller holds jm.mu and it is released before this returns.
func (jm *jobManager) replaceWatch(key watchKey, existing, cfg *watchConfig, detachedCfgs []*watchConfig, detached []watchSendTerminalSnapshot) (watchResult, error) {
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
	teardown := watchTeardown{targets: targets, detachedCfgs: detachedCfgs}
	teardown.markRejectingLocked()
	dropped := append(terminalSnapshots(targets), detached...)
	if equal {
		if err := jm.appendWatchTeardownBatch(detached, nil); err != nil {
			teardown.rollbackLocked(jm)
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
		teardown.rollbackLocked(jm)
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

// validateWatchSendArgs checks a watch's delivery target. A watch with no send,
// or one whose send is the receiver's own internal rail, has no target to check.
func validateWatchSendArgs(a watchArgs) error {
	if a.Send == nil || a.ReceiverSendInternal {
		return nil
	}
	return validateWatchSendDeliveryTarget(a.Send.To)
}

func validateWatchSendDeliveryTarget(target string) error {
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
