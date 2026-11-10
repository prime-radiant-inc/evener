package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	agentevent "primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/transcript"
)

type stableShellAttentionBootstrapTarget struct {
	delegateID string
	descriptor delegatestore.Descriptor
	storePath  string
}

// repairStableShellAttentionForBootstrap completes terminal shell handoffs
// without constructing the owning delegate Session. Receiver transcript fsync
// precedes the exact source acknowledgement, so a crash can only replay the
// same attention ID.
func repairStableShellAttentionForBootstrap(c *delegateTreeController) error {
	if c == nil {
		return errors.New("stable shell bootstrap controller is nil")
	}
	c.mu.Lock()
	stateDir := c.stateDir
	now := c.now
	open := c.attentionOpen
	targets := make([]stableShellAttentionBootstrapTarget, 0, len(c.durable))
	for delegateID, aggregate := range c.durable {
		if aggregate == nil || !aggregate.Resumable || aggregate.PendingStopSeq != 0 || aggregate.Phase == delegatestore.PhaseClosed {
			continue
		}
		descriptor := cloneDelegateStartDescriptor(aggregate.Descriptor)
		targets = append(targets, stableShellAttentionBootstrapTarget{
			delegateID: delegateID,
			descriptor: descriptor,
			storePath:  filepath.Join(jobsDir(stateDir, descriptor.ChildSessionID), "jobs.jsonl"),
		})
	}
	c.mu.Unlock()
	sort.Slice(targets, func(i, j int) bool { return targets[i].delegateID < targets[j].delegateID })
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if open == nil {
		open = transcript.OpenWriterForSession
	}
	for _, target := range targets {
		info, err := os.Stat(target.storePath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("stat stable shell source journal: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("stable shell source journal %s is not a regular file", target.storePath)
		}
		if err := repairStableShellAttentionTarget(target, stateDir, now, open); err != nil {
			return err
		}
	}
	return nil
}

func repairStableShellAttentionTarget(target stableShellAttentionBootstrapTarget, stateDir string, now func() time.Time, open delegateAttentionWriterOpener) (err error) {
	store, err := jobstore.Open(target.storePath)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	records, err := store.Load()
	if err != nil {
		return err
	}
	jobIDs := make([]string, 0, len(records))
	for jobID, record := range records {
		if record != nil && record.Type == jobstore.JobShell && record.ParentDelegateID == target.delegateID && record.Status.IsTerminal() && record.TerminalGen != "" && record.NotifyState == jobstore.NotifyPending {
			jobIDs = append(jobIDs, jobID)
		}
	}
	sort.Strings(jobIDs)
	path, sessionID, err := delegateTranscriptPathFromRef(stateDir, target.descriptor.TranscriptRef)
	if err != nil {
		return err
	}
	if sessionID != target.descriptor.ChildSessionID {
		return fmt.Errorf("stable shell receiver transcript session %q does not match %q", sessionID, target.descriptor.ChildSessionID)
	}
	for _, jobID := range jobIDs {
		records, err = store.Load()
		if err != nil {
			return err
		}
		record := records[jobID]
		if record == nil || record.Type != jobstore.JobShell || record.ParentDelegateID != target.delegateID || !record.Status.IsTerminal() || record.TerminalGen == "" || record.NotifyState != jobstore.NotifyPending {
			continue
		}
		attentionID := stableShellAttentionID(record.JobID, record.TerminalGen)
		content := stableShellAttentionContent(target.storePath, target.descriptor, record)
		if _, err := appendColdDelegateNotificationDurablyWithOpen(path, sessionID, attentionID, content, now(), open); err != nil {
			return err
		}
		if err := store.Append(jobstore.Event{
			Kind:        jobstore.EventJobNotificationDelivered,
			TS:          now(),
			JobID:       record.JobID,
			TerminalGen: record.TerminalGen,
		}); err != nil {
			return err
		}
	}
	return nil
}

func stableShellAttentionContent(storePath string, descriptor delegatestore.Descriptor, record *jobstore.JobRecord) string {
	notification := jobNotificationFromRecord(record)
	excerpt := stableShellNotificationExcerpt(storePath, record)
	return formatJobNotificationBlock(notification, excerpt, hasString(descriptor.ToolNameCeiling, "read_transcript"))
}

func stableShellNotificationExcerpt(storePath string, record *jobstore.JobRecord) notificationExcerpt {
	if record == nil || !record.Status.IsTerminal() {
		return notificationExcerpt{}
	}
	path := record.OutputPath
	if path == "" {
		path = filepath.Join(filepath.Dir(storePath), "jobs", record.JobID+".log")
	}
	excerpt, _, truncated, err := readClosedJobOutput(path, record, terminalExcerptBytes, false)
	if err != nil || excerpt == "" {
		return notificationExcerpt{}
	}
	rendered := limitWatchText(strings.ToValidUTF8(excerpt, "\uFFFD"), terminalExcerptMaxChars)
	if truncated {
		rendered += "\n[excerpt truncated]"
	}
	return notificationExcerpt{text: rendered, complete: !truncated}
}

func collectDelegateReconcileEvidence(stateDir string, requirements delegateReconcileRequirements) (delegateReconcileEvidence, error) {
	evidence := delegateReconcileEvidence{
		evidenceVersion: requirements.evidenceVersion,
		stopRequestSeq:  requirements.stopRequestSeq,
		shells:          make(map[string]shellRuntimeLossEvidence),
		attention:       make(map[string][]string),
	}
	ids := make(map[string]struct{}, len(requirements.shellStores)+len(requirements.attentionTranscripts))
	for id := range requirements.shellStores {
		ids[id] = struct{}{}
	}
	for id := range requirements.attentionTranscripts {
		ids[id] = struct{}{}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, delegateID := range ordered {
		// The source shell ledger is always read before its receiver transcript.
		// A durable source acknowledgement follows receiver fsync, so this order
		// cannot observe a handoff as absent from both ledgers.
		if storePath := requirements.shellStores[delegateID]; storePath != "" {
			shells, err := collectShellRuntimeLossEvidence(storePath)
			if err != nil {
				return delegateReconcileEvidence{}, err
			}
			evidence.shells[delegateID] = shells
		}
		if ref := requirements.attentionTranscripts[delegateID]; ref != "" {
			path, expectedSessionID, err := delegateTranscriptPathFromRef(stateDir, ref)
			if err != nil {
				return delegateReconcileEvidence{}, err
			}
			pending, err := readPendingDelegateAttention(path, expectedSessionID)
			if err != nil {
				return delegateReconcileEvidence{}, err
			}
			evidence.attention[delegateID] = pending
		}
	}
	return evidence, nil
}

func collectShellRuntimeLossEvidence(path string) (shellRuntimeLossEvidence, error) {
	events, err := jobstore.ReadEvents(path)
	if err != nil {
		return shellRuntimeLossEvidence{}, err
	}
	records := jobstore.Fold(events)
	watches := jobstore.FoldWatches(events)
	ids := make([]string, 0, len(records))
	for jobID := range records {
		ids = append(ids, jobID)
	}
	sort.Strings(ids)
	var evidence shellRuntimeLossEvidence
	for _, jobID := range ids {
		record := records[jobID]
		if record == nil || record.Type != jobstore.JobShell {
			continue
		}
		if record.Status == jobstore.StatusRunning {
			evidence.runningJobIDs = append(evidence.runningJobIDs, jobID)
		}
		if record.Status.IsTerminal() && record.NotifyState == jobstore.NotifyPending && record.TerminalGen != "" {
			evidence.pendingNotification = append(evidence.pendingNotification, shellNotificationIdentity{
				jobID:              jobID,
				terminalGeneration: record.TerminalGen,
			})
		}
		if record.Status.IsTerminal() && record.TerminalGen != "" && hasActiveSendWatchFor(watches, jobID) {
			evidence.terminalWatchJobIDs = append(evidence.terminalWatchJobIDs, jobID)
		}
		// A terminal job whose live finish path armed a completion notification
		// but still reads not_armed is the finish-only prefix: the notification
		// record was lost with the torn batch.
		if shellTerminalNeedsNotification(record) && record.NotifyState == jobstore.NotifyNotArmed {
			evidence.unnotifiedTerminal = append(evidence.unnotifiedTerminal, shellNotificationIdentity{
				jobID:              jobID,
				terminalGeneration: record.TerminalGen,
			})
		}
	}
	return evidence, nil
}

// shellTerminalNeedsNotification reports whether the live finish path would
// have armed an owner completion notification for this terminal record. A
// background shell always does. A foreground shell does only when its process
// hit max_runtime_ms: finishForegroundRuntimeTimeout finalizes through the
// notification-arming path while Background stays false, whereas a kept-sync
// completion uses finalizeKeptSync and never arms one. Reason is the durable
// evidence of that path, and Background survives from the start event.
func shellTerminalNeedsNotification(record *jobstore.JobRecord) bool {
	if record == nil || !record.Status.IsTerminal() || record.TerminalGen == "" {
		return false
	}
	return record.Background || record.Reason == "run_timeout"
}

// hasActiveSendWatchFor reports whether a send watch that can still be cleared
// targets jobID. A no-send callback watch is left for the child's restore,
// which is the only path that can cancel it and tell its receiver; clearing it
// here would drop that notice. A watch with no generation cannot produce a
// clear event, so counting it would create a repair plan with nothing to write.
func hasActiveSendWatchFor(watches map[string]*jobstore.WatchRecord, jobID string) bool {
	for _, watch := range watches {
		if watch != nil && watch.Active && watch.SendTo != "" && watch.Generation != "" &&
			watch.VisibleSessionID != "" && watch.Target == jobID {
			return true
		}
	}
	return false
}

func executeDelegateShellRepair(plan delegateShellRepairPlan, now time.Time) (err error) {
	if plan.storePath == "" {
		return errors.New("delegate shell repair store path is empty")
	}
	info, err := os.Stat(plan.storePath)
	if err != nil {
		return fmt.Errorf("stat delegate shell repair store: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("delegate shell repair store %s is not a regular file", plan.storePath)
	}
	store, err := jobstore.Open(plan.storePath)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	records, err := store.Load()
	if err != nil {
		return err
	}
	watches, err := store.LoadWatches()
	if err != nil {
		return err
	}
	stored, err := store.LoadEvents()
	if err != nil {
		return err
	}
	// Any frame this generation ever persisted means the watch already spoke,
	// including a first output-match frame still pending after the crash. The
	// terminal loop below must not synthesize an end notice for such a
	// generation: the notice shares the frame's key and first UpdateSeq, so
	// FoldWatchSends would replace the matched output with a claim that the
	// condition never matched.
	spoke := watchGenerationsThatSpoke(stored)

	runningIDs := append([]string(nil), plan.runningJobIDs...)
	sort.Strings(runningIDs)
	events := make([]jobstore.Event, 0, len(runningIDs)*3+len(plan.pendingNotification))
	clearedJobs := make(map[string]struct{})
	seenRunning := make(map[string]struct{})
	for _, jobID := range runningIDs {
		if _, seen := seenRunning[jobID]; seen {
			continue
		}
		seenRunning[jobID] = struct{}{}
		record := records[jobID]
		if record == nil || record.Type != jobstore.JobShell || record.Status != jobstore.StatusRunning {
			continue
		}
		finished := jobstore.Reconcile(map[string]*jobstore.JobRecord{jobID: record}, nil, now)
		if len(finished) != 1 {
			continue
		}
		finish := finished[0]
		finish.Provenance = provenance.Clone(record.Provenance)
		outputPath := record.OutputPath
		if outputPath == "" {
			outputPath = filepath.Join(filepath.Dir(plan.storePath), "jobs", jobID+".log")
		}
		if total, _, statErr := jobstore.OutputFileStats(outputPath); statErr == nil {
			finish.OutputBytes = total
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		events = append(events, finish)
		notificationKind := jobstore.EventJobNotificationPending
		if plan.suppressOwnerNotify {
			notificationKind = jobstore.EventJobNotificationConsumed
		}
		events = append(events, jobstore.Event{
			Kind:        notificationKind,
			TS:          now,
			JobID:       jobID,
			TerminalGen: finish.TerminalGen,
			Provenance:  provenance.Clone(record.Provenance),
		})
		clearedJobs[jobID] = struct{}{}
	}

	pending := append([]shellNotificationIdentity(nil), plan.pendingNotification...)
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].jobID != pending[j].jobID {
			return pending[i].jobID < pending[j].jobID
		}
		return pending[i].terminalGeneration < pending[j].terminalGeneration
	})
	seenPending := make(map[shellNotificationIdentity]struct{})
	for _, identity := range pending {
		if _, seen := seenPending[identity]; seen {
			continue
		}
		seenPending[identity] = struct{}{}
		record := records[identity.jobID]
		if record == nil || record.Type != jobstore.JobShell || record.TerminalGen != identity.terminalGeneration || record.NotifyState != jobstore.NotifyPending {
			continue
		}
		if plan.suppressOwnerNotify {
			events = append(events, jobstore.Event{
				Kind:        jobstore.EventJobNotificationConsumed,
				TS:          now,
				JobID:       identity.jobID,
				TerminalGen: identity.terminalGeneration,
			})
			// Do not clear this job's watches here. Send watches were cleared
			// with their durable end notice by the terminalWatchJobIDs loop
			// above. A no-send callback watch must stay active: the child's
			// restore is the only path that can cancel it and route its
			// receiver's cancellation notice.
		}
	}
	for _, jobID := range plan.terminalWatchJobIDs {
		record := records[jobID]
		if record == nil || record.Type != jobstore.JobShell || !record.Status.IsTerminal() || record.TerminalGen == "" {
			continue
		}
		// The terminal event is already durable, so its send watches must be
		// cleared. A generation that never emitted a frame also gets the end
		// notice the child's restore would have sent.
		events = append(events, watchEndNoticeEvents(watches, record, spoke, now)...)
	}
	for _, identity := range plan.unnotifiedTerminal {
		record := records[identity.jobID]
		if record == nil || record.Type != jobstore.JobShell || !record.Status.IsTerminal() || record.TerminalGen != identity.terminalGeneration || record.NotifyState != jobstore.NotifyNotArmed {
			continue
		}
		kind := jobstore.EventJobNotificationPending
		if plan.suppressOwnerNotify {
			kind = jobstore.EventJobNotificationConsumed
		}
		events = append(events, jobstore.Event{
			Kind:        kind,
			TS:          now,
			JobID:       identity.jobID,
			TerminalGen: identity.terminalGeneration,
		})
	}
	clearedIDs := make([]string, 0, len(clearedJobs))
	for jobID := range clearedJobs {
		clearedIDs = append(clearedIDs, jobID)
	}
	sort.Strings(clearedIDs)
	for _, jobID := range clearedIDs {
		events = append(events, recoveredTerminalSendWatchClearEvents(watches, jobID, now)...)
	}
	return store.AppendBatch(events)
}

// recoveredTerminalSendWatchClearEvents clears the send watches for a job the
// cold repair just made terminal. A no-send callback watch is left active for
// the child's restore, which is the only path that can cancel it and route its
// receiver's cancellation notice; clearing it here would drop that notice
// permanently (#5834 review).
func recoveredTerminalSendWatchClearEvents(watches map[string]*jobstore.WatchRecord, jobID string, ts time.Time) []jobstore.Event {
	return watchClearEventsForRecords(watches, ts, "auto_removed_terminal", func(watch *jobstore.WatchRecord) bool {
		return watch.Target == jobID && watch.SendTo != ""
	})
}

// watchEndNoticeEvents builds the clear for every active send watch targeting a
// terminal job and, for a generation that never emitted a frame, the durable
// end-notice frame. It mirrors the live terminal teardown and the restart-side
// noticeUnrestoredWatchEnds: a generation that already spoke keeps its pending
// frame, and a generation that never did gets one frame saying the condition
// can no longer match. Every routable watch is cleared either way. A watch that
// cannot be routed is left active for the child's restore path.
func watchEndNoticeEvents(watches map[string]*jobstore.WatchRecord, record *jobstore.JobRecord, spoke map[watchFrameOrigin]bool, now time.Time) []jobstore.Event {
	var watchIDs []string
	for watchID, watch := range watches {
		if watch == nil || !watch.Active || watch.SendTo == "" || watch.Generation == "" ||
			watch.VisibleSessionID == "" || watch.Target != record.JobID {
			continue
		}
		watchIDs = append(watchIDs, watchID)
	}
	sort.Strings(watchIDs)
	var events []jobstore.Event
	for _, watchID := range watchIDs {
		watch := watches[watchID]
		resolved, err := resolveWatchSendTarget(watch.SendTo, record.JobID)
		if err != nil {
			continue
		}
		if !spoke[watchFrameOrigin{watchID: watch.WatchID, generation: watch.Generation}] {
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
			manager := &jobManager{sessionID: watch.VisibleSessionID}
			root := agentevent.SessionEvent{SessionID: watch.VisibleSessionID, Provenance: provenance.Clone(record.Provenance)}
			trigger := watchEndedUnfiredMessage(watch.Target, record.Status, record.Reason, record.OutputBytes)
			delivery := manager.watchSendSnapshot(cfg, watch.Target, trigger, root)
			delivery.allowAfterTerminalExpiry = true
			delivery.endNotice = true
			delivery = manager.snapshotWatchSendFrame(delivery)
			state := manager.watchSendState(delivery, resolved)
			events = append(events, jobstore.Event{Kind: jobstore.EventWatchSendPending, TS: now, WatchSend: &state})
		}
		events = append(events, watchClearEventsForRecords(watches, now, "auto_removed_terminal", func(candidate *jobstore.WatchRecord) bool {
			return candidate == watch
		})...)
	}
	return events
}
