package agent

import (
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
)

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
		id:                 cfg.watchID,
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
