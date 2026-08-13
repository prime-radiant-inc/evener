package agent

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

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

func delegateTranscriptPathFromRef(stateDir, ref string) (string, string, error) {
	projectID, sessionID, err := decodeRef(ref)
	if err != nil {
		return "", "", err
	}
	if projectID != "" {
		return "", "", fmt.Errorf("delegate transcript ref %q leaves controller state directory", ref)
	}
	return transcriptPath(stateDir, sessionID), sessionID, nil
}

func readPendingDelegateAttention(path, expectedSessionID string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open delegate attention transcript: %w", err)
	}
	defer func() { _ = f.Close() }()
	reader := bufio.NewReaderSize(f, 64*1024)
	headerRead := false
	entries := make([]transcript.Entry, 0)
	for {
		line, complete, _, readErr := transcript.ReadLine(reader, transcript.DefaultMaxLineBytes)
		if readErr != nil {
			return nil, readErr
		}
		if !complete {
			break
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !headerRead {
			header, err := transcript.DecodeHeader(line)
			if err != nil {
				return nil, err
			}
			if header.SessionID != expectedSessionID {
				return nil, fmt.Errorf("delegate attention transcript session %q, want %q", header.SessionID, expectedSessionID)
			}
			headerRead = true
			continue
		}
		entry, err := transcript.DecodeEntry(line)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if !headerRead {
		return nil, errors.New("delegate attention transcript has no header")
	}
	fold, err := foldDelegateAttention(entries)
	if err != nil {
		return nil, err
	}
	return fold.pendingIDs(), nil
}

type delegateAttentionFold struct {
	order       []string
	content     map[string]llm.Message
	resolutions map[string]delegateAttentionResolution
}

func foldDelegateAttention(entries []transcript.Entry) (delegateAttentionFold, error) {
	fold := delegateAttentionFold{
		content:     make(map[string]llm.Message),
		resolutions: make(map[string]delegateAttentionResolution),
	}
	for _, entry := range entries {
		turn := entry.Turn
		if turn.AttentionID != "" {
			if turn.Kind != schema.TurnSteering || turn.AttentionResolution != nil {
				return delegateAttentionFold{}, fmt.Errorf("attention %q is not a steering turn", turn.AttentionID)
			}
			if previous, exists := fold.content[turn.AttentionID]; exists {
				if !reflect.DeepEqual(previous, turn.Message) {
					return delegateAttentionFold{}, fmt.Errorf("attention %q has conflicting content", turn.AttentionID)
				}
			} else {
				fold.content[turn.AttentionID] = turn.Message
				fold.order = append(fold.order, turn.AttentionID)
			}
		}
		resolution := turn.AttentionResolution
		if resolution == nil {
			if turn.Kind == schema.TurnAttentionResolution {
				return delegateAttentionFold{}, errors.New("attention resolution turn has no resolution")
			}
			continue
		}
		if turn.Kind != schema.TurnAttentionResolution || turn.AttentionID != "" || resolution.AttentionID == "" {
			return delegateAttentionFold{}, errors.New("invalid attention resolution turn")
		}
		disposition := delegateAttentionResolution(resolution.Disposition)
		if disposition != delegateAttentionConsumed && disposition != delegateAttentionDiscarded {
			return delegateAttentionFold{}, fmt.Errorf("attention %q has invalid resolution %q", resolution.AttentionID, resolution.Disposition)
		}
		if _, exists := fold.content[resolution.AttentionID]; !exists {
			return delegateAttentionFold{}, fmt.Errorf("attention %q resolved before it was appended", resolution.AttentionID)
		}
		if previous, exists := fold.resolutions[resolution.AttentionID]; exists && previous != disposition {
			return delegateAttentionFold{}, fmt.Errorf("attention %q has conflicting resolutions", resolution.AttentionID)
		}
		fold.resolutions[resolution.AttentionID] = disposition
	}
	return fold, nil
}

func (f delegateAttentionFold) pendingIDs() []string {
	pending := make([]string, 0, len(f.order))
	for _, attentionID := range f.order {
		if _, resolved := f.resolutions[attentionID]; !resolved {
			pending = append(pending, attentionID)
		}
	}
	return pending
}

func appendColdAttentionResolution(path, expectedSessionID string, ids []string, disposition delegateAttentionResolution) (err error) {
	if disposition != delegateAttentionConsumed && disposition != delegateAttentionDiscarded {
		return fmt.Errorf("invalid attention resolution %q", disposition)
	}
	writer, entries, err := transcript.OpenWriterForSession(path, expectedSessionID)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, writer.Close()) }()
	fold, err := foldDelegateAttention(entries)
	if err != nil {
		return err
	}
	for _, attentionID := range ids {
		if attentionID == "" {
			return errors.New("attention resolution ID is empty")
		}
		if previous, resolved := fold.resolutions[attentionID]; resolved {
			if previous != disposition {
				return fmt.Errorf("attention %q has conflicting resolution %q", attentionID, previous)
			}
			continue
		}
		if _, pending := fold.content[attentionID]; !pending {
			return fmt.Errorf("attention %q is not pending", attentionID)
		}
		turn := schema.NewTurn(schema.TurnAttentionResolution, llm.System("Attention resolved."))
		turn.AttentionResolution = &schema.AttentionResolutionInfo{
			AttentionID: attentionID,
			Disposition: string(disposition),
		}
		if err := writer.AppendDurable(turn); err != nil {
			return err
		}
		fold.resolutions[attentionID] = disposition
	}
	return nil
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
