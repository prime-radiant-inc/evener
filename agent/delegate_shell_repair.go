package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
	validatedTotal, _, err := validatedOutputStatsForRecord(path, record)
	if err != nil {
		return notificationExcerpt{}
	}
	excerpt, _, truncated, err := tailOutputFile(path, terminalExcerptBytes, validatedTotal)
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
	}
	return evidence, nil
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
			clearedJobs[identity.jobID] = struct{}{}
		}
	}
	clearedIDs := make([]string, 0, len(clearedJobs))
	for jobID := range clearedJobs {
		clearedIDs = append(clearedIDs, jobID)
	}
	sort.Strings(clearedIDs)
	for _, jobID := range clearedIDs {
		events = append(events, recoveredTerminalWatchClearEvents(watches, jobID, now)...)
	}
	return store.AppendBatch(events)
}
