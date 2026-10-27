package agent

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
)

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
	// One journal read for both consumers: LoadEvents returns the raw events
	// and Fold derives the same job records Load would fold from them, so this
	// path does one readAllLocked+fold instead of two. No cache is added — the
	// events are a local of this call only.
	stored, err := jm.store.LoadEvents()
	if err != nil {
		return err
	}
	recs := jobstore.Fold(stored)
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
