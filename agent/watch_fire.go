package agent

import (
	"errors"
	"fmt"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
)

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
