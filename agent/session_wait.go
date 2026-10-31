package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

type activationDisposition uint8

const (
	activationSettled activationDisposition = iota
	activationYielded
)

type activationFinalization struct {
	Text        string
	Disposition activationDisposition
	Checkpoint  *schema.WaitCheckpointInfo
}

// persistWaitCheckpoint records only transcript evidence, never model history.
// Callers own activation/communicate admission and delegate journal validation.
// A retained-unsynced error must not be treated as a safe yield or re-appended:
// the existing writer's EstablishDurability barrier settles that record.
func (s *Session) persistWaitCheckpoint(checkpoint schema.WaitCheckpointInfo) error {
	return s.persistWaitCheckpointThrough(checkpoint, false)
}

func (s *Session) persistWaitCheckpointThrough(checkpoint schema.WaitCheckpointInfo, establish bool) error {
	if err := validateWaitCheckpoint(checkpoint, s.id); err != nil {
		return err
	}
	writer := s.attachedTranscript()
	if writer == nil {
		return errors.New("wait checkpoint requires a transcript writer")
	}
	if header := writer.Header(); header.SessionID != s.id {
		return &transcript.SessionIDMismatchError{HeaderSessionID: header.SessionID, RequestedSessionID: s.id}
	}
	appended := false
	rec, err := func() (transcript.Record, error) {
		s.attentionMu.Lock()
		defer s.attentionMu.Unlock()
		if establish && checkpoint.ResumedByTurnID != "" && checkpoint.DelegateID != "" {
			c := s.delegateController
			if c == nil {
				return transcript.Record{}, errDelegateStaleLease
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			lease := delegateLease{delegateID: checkpoint.DelegateID, generation: checkpoint.Generation}
			_, live, err := c.admitLeaseLocked(lease, delegatestore.PhaseRunning)
			if err != nil {
				return transcript.Record{}, err
			}
			if live.binding.runtime != s || live.binding.yielded || c.hasSettlementClaimLocked(lease) {
				return transcript.Record{}, errDelegateTargetBusy
			}
		}
		// The lock order is attentionMu, controller lease, Session.mu, then
		// the writer's leaf locks. No event/retirement callback runs here.
		appended = true
		rec, err := s.recordTranscriptLocked(schema.Turn{
			Kind: schema.TurnWaitCheckpoint, WaitCheckpoint: &checkpoint,
		}, transcript.DoorSynced, transcript.PlaceInTurn(waitCheckpointRecordTurn(checkpoint)))
		if establish && retainedUnsyncedError(err) != nil {
			err = writer.EstablishDurability() // Repair the retained record, never append twice.
		}
		return rec, err
	}()
	if appended {
		if err != nil {
			s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("transcript write failed: %v", err)})
		}
		s.surfaceTranscriptWarnings()
	}
	if err != nil {
		return err
	}
	if !rec.Recorded {
		return errors.New("wait checkpoint was not recorded")
	}
	return nil
}

// readWaitCheckpoint returns the latest yield or resumption in a validated
// transcript. Identical retries are idempotent, including an old yield repeated
// after its resumption. Contradictions and incomplete tails return no evidence.
// Only a successful complete read with no checkpoints returns (nil, nil).
//
// This read is read-only: readable bytes do not prove durability. Before using
// the result as safe waiting, the caller must establish durability through the
// existing transcript writer's EstablishDurability and validate the activation,
// communicate outcome and delegate journal generation. This helper is not a
// restore classifier and does not establish that the latest execution yielded.
func readWaitCheckpoint(path, sessionID string) (*schema.WaitCheckpointInfo, error) {
	if sessionID == "" {
		return nil, errors.New("wait checkpoint requires a session ID")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open wait checkpoint transcript: %w", err)
	}
	defer func() { _ = file.Close() }()
	reader := bufio.NewReader(file)
	header, err := transcript.ReadHeader(context.Background(), reader, transcript.DefaultMaxLineBytes)
	if err != nil {
		return nil, err
	}
	if header.SessionID != sessionID {
		return nil, &transcript.SessionIDMismatchError{HeaderSessionID: header.SessionID, RequestedSessionID: sessionID}
	}

	checkpoints := make(map[string]schema.WaitCheckpointInfo)
	var latest *schema.WaitCheckpointInfo
	for {
		line, complete, read, err := transcript.ReadLine(reader, transcript.DefaultMaxLineBytes)
		if err != nil {
			return nil, err
		}
		if !complete {
			if read != 0 {
				return nil, fmt.Errorf("incomplete wait checkpoint transcript: %w", io.ErrUnexpectedEOF)
			}
			return latest, nil
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		entry, err := transcript.DecodeEntry(line)
		if err != nil {
			return nil, err
		}
		turn := entry.Turn
		if turn.Kind != schema.TurnWaitCheckpoint {
			if turn.WaitCheckpoint != nil {
				return nil, errors.New("wait checkpoint payload on another record kind")
			}
			continue
		}
		if turn.WaitCheckpoint == nil {
			return nil, errors.New("wait checkpoint record has no payload")
		}
		checkpoint := *turn.WaitCheckpoint
		if err := validateWaitCheckpoint(checkpoint, sessionID); err != nil {
			return nil, err
		}
		if turn.Format != schema.TurnFormatIdentity || turn.TurnID != waitCheckpointRecordTurn(checkpoint) {
			return nil, errors.New("wait checkpoint record has mismatched turn identity")
		}

		previous, exists := checkpoints[checkpoint.ExecutionTurnID]
		if !exists {
			if checkpoint.ResumedByTurnID != "" {
				return nil, errors.New("wait checkpoint resumption has no earlier yield")
			}
		} else {
			identity := checkpoint
			identity.ResumedByTurnID = previous.ResumedByTurnID
			if identity != previous {
				return nil, errors.New("wait checkpoint identity contradicts earlier yield")
			}
			if checkpoint.ResumedByTurnID == "" || checkpoint.ResumedByTurnID == previous.ResumedByTurnID {
				continue // A duplicate cannot reopen a claimed yield or rewind latest.
			}
			if previous.ResumedByTurnID != "" {
				return nil, errors.New("wait checkpoint has conflicting resumption claims")
			}
		}
		checkpoints[checkpoint.ExecutionTurnID] = checkpoint
		latest = &checkpoint
	}
}

func validateWaitCheckpoint(checkpoint schema.WaitCheckpointInfo, sessionID string) error {
	if checkpoint.SessionID == "" || checkpoint.SessionID != sessionID {
		return errors.New("wait checkpoint session identity does not match")
	}
	if checkpoint.ExecutionTurnID == "" || checkpoint.CommunicateCallID == "" {
		return errors.New("wait checkpoint requires execution and communicate identity")
	}
	if checkpoint.ResumedByTurnID != "" && checkpoint.ResumedByTurnID == checkpoint.ExecutionTurnID {
		return errors.New("wait checkpoint resumption requires a new turn ID")
	}
	return nil
}

func waitCheckpointRecordTurn(checkpoint schema.WaitCheckpointInfo) string {
	if checkpoint.ResumedByTurnID != "" {
		return checkpoint.ResumedByTurnID
	}
	return checkpoint.ExecutionTurnID
}

// safeWaitBoundary checks execution evidence, not just checkpoint shape. The
// caller still owns the durability barrier and the journal generation check.
// A resumption claim never covers a later interrupted activation.
func safeWaitBoundary(checkpoint schema.WaitCheckpointInfo, entries []transcript.Entry, resultToolName string) bool {
	if checkpoint.ResumedByTurnID != "" {
		return false
	}
	if resultToolName == "" {
		resultToolName = "communicate"
	}
	started, completed, communicated, accepted, sealed := false, false, false, false, false
	pending := make(map[string]bool)
	for _, entry := range entries {
		turn := entry.Turn
		if turn.OriginalOrdinal != nil {
			continue // a compaction copy is not a new execution or outcome
		}
		if turn.TurnKind == schema.TurnSpanExecution || turn.Kind == schema.TurnReopen {
			if started || turn.TurnID != checkpoint.ExecutionTurnID {
				// Earlier executions are fine, later ones invalidate this wait.
				if started {
					return false
				}
				continue
			}
			started = true
		}
		if !started {
			continue
		}
		// Questions and approval state can be recorded in delivery/gap spans,
		// not only in the yielding execution. They retain their own hold owner.
		if (completed || sealed) && turn.Notice != nil && turn.Notice.Kind == schema.NoticeApprovalDecision {
			return false
		}
		for _, part := range turn.Message.Content {
			if (completed || sealed) && part.ToolResult != nil && part.ToolResult.Name == askUserToolName {
				return false
			}
		}
		if turn.Kind == schema.TurnFailure || turn.Kind == schema.TurnCompletion && turn.TurnID != checkpoint.ExecutionTurnID {
			return false
		}
		if turn.TurnID != checkpoint.ExecutionTurnID {
			// Delivery turns do not resume an execution. The executor does.
			continue
		}
		switch turn.Kind {
		case schema.TurnFailure, schema.TurnReopen:
			return false
		case schema.TurnAssistant, schema.TurnTool, schema.TurnToolResults:
			if completed || sealed {
				return false
			}
		case schema.TurnCommunicate:
			info := turn.Communicate
			if completed || sealed || info == nil || !pending[info.CallID] {
				return false
			}
			if info.CallID != checkpoint.CommunicateCallID {
				if info.EndTurn || communicated {
					return false
				}
				break // An earlier nonterminal update still needs its result below.
			}
			if !info.EndTurn || info.EndReason != "waiting_on_work" || info.WaitingOn != checkpoint.WaitingOn {
				return false
			}
			communicated = true
		case schema.TurnWaitCheckpoint:
			if turn.WaitCheckpoint == nil || *turn.WaitCheckpoint != checkpoint || len(pending) != 0 || !communicated || !accepted {
				return false
			}
			sealed = true
		case schema.TurnCompletion:
			if turn.Completion == nil || turn.Completion.Status != schema.TurnCompleted || len(pending) != 0 || !communicated || !accepted {
				return false
			}
			completed = true
		}
		for _, part := range turn.Message.Content {
			switch part.Kind {
			case llm.ContentToolCall:
				call := part.ToolCall
				if call == nil || call.ID == "" || pending[call.ID] {
					return false
				}
				pending[call.ID] = true
				if call.ID == checkpoint.CommunicateCallID {
					var args struct {
						EndTurn   bool   `json:"end_turn"`
						EndReason string `json:"end_reason"`
						WaitingOn string `json:"waiting_on"`
					}
					if call.Name != resultToolName || json.Unmarshal(call.Arguments, &args) != nil || !args.EndTurn || args.EndReason != "waiting_on_work" || args.WaitingOn != checkpoint.WaitingOn {
						return false
					}
				}
			case llm.ContentToolResult:
				result := part.ToolResult
				if result == nil || !pending[result.ToolCallID] {
					return false
				}
				delete(pending, result.ToolCallID)
				if result.ToolCallID == checkpoint.CommunicateCallID {
					var state communicateEndState
					if result.Name != resultToolName || result.IsError || json.Unmarshal(result.ToolState, &state) != nil || state.EndReason != "waiting_on_work" {
						return false
					}
					accepted = true
				}
			}
		}
	}
	return started && sealed && communicated && accepted && len(pending) == 0
}

// checkpointCompletedWait runs after processOneInput joined its entire tool
// batch and synced its results, before the completion marker or executor release.
func (s *Session) checkpointCompletedWait(ctx context.Context) error {
	s.mu.Lock()
	report, execution := s.comm, s.execution
	s.mu.Unlock()
	if !report.called || report.endReason != "waiting_on_work" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !execution.running || execution.failed || execution.waitCallID == "" {
		return errors.New("wait has no completed execution boundary")
	}
	checkpoint := schema.WaitCheckpointInfo{SessionID: s.id, ExecutionTurnID: execution.turnID, CommunicateCallID: execution.waitCallID, WaitingOn: report.waitingOn}
	if s.owningDelegateID != "" {
		actor, err := s.delegateActor(ctx)
		if err != nil {
			return err
		}
		if actor.lease == nil {
			return errDelegateStaleLease
		}
		c := s.delegateController
		c.mu.Lock()
		_, live, err := c.admitLeaseLocked(*actor.lease, delegatestore.PhaseRunning)
		if err == nil && (live.binding.evidence.batch != nil || c.hasSettlementClaimLocked(*actor.lease)) {
			err = errDelegateTargetBusy
		}
		c.mu.Unlock()
		if err != nil {
			return err
		}
		checkpoint.DelegateID, checkpoint.Generation = actor.lease.delegateID, actor.lease.generation
	}
	if s.stateDir == "" {
		return nil // Lifetime-local waits have no cold-restart checkpoint promise.
	}
	return s.persistWaitCheckpointDurably(checkpoint)
}

func (s *Session) persistWaitCheckpointDurably(checkpoint schema.WaitCheckpointInfo) error {
	return s.persistWaitCheckpointThrough(checkpoint, true)
}

// settledFailedWaitBoundary proves that a complete wait boundary subsequently
// ended in failure. Failure records after the checkpoint describe that ending,
// not another model/tool boundary. Nothing else may bypass safeWaitBoundary.
// This is supersession evidence, never permission to resume the failed turn.
func settledFailedWaitBoundary(checkpoint schema.WaitCheckpointInfo, entries []transcript.Entry, resultToolName string) bool {
	boundary := make([]transcript.Entry, 0, len(entries))
	sealed := false
	for _, entry := range entries {
		turn := entry.Turn
		if turn.OriginalOrdinal != nil {
			continue
		}
		if turn.TurnID == checkpoint.ExecutionTurnID {
			if turn.Kind == schema.TurnCompletion && turn.Completion != nil && turn.Completion.Status == schema.TurnFailed {
				return safeWaitBoundary(checkpoint, boundary, resultToolName)
			}
			if turn.Kind == schema.TurnFailure && sealed {
				continue
			}
			if turn.Kind == schema.TurnWaitCheckpoint && turn.WaitCheckpoint != nil && *turn.WaitCheckpoint == checkpoint {
				sealed = true
			}
		}
		boundary = append(boundary, entry)
	}
	return false
}

// The caller already owns the single input executor. Claim before recording
// any activity of its new execution, so a crash is ordinary interruption.
func (s *Session) claimWaitResumption(turnID string) error {
	if s.attachedTranscript() == nil || s.stateDir == "" {
		return nil
	}
	checkpoint, err := readWaitCheckpoint(transcriptPath(s.stateDir, s.id), s.id)
	if err != nil {
		return err
	}
	if checkpoint == nil || checkpoint.ResumedByTurnID != "" {
		return nil
	}
	if checkpoint.DelegateID != "" && s.delegateController != nil {
		c := s.delegateController
		c.mu.Lock()
		run := c.durable[checkpoint.DelegateID]
		historical := run != nil && (run.Generation != checkpoint.Generation || !run.CurrentRunOpen)
		c.mu.Unlock()
		if historical {
			return nil // Stop or settlement, not a resumption of this wait.
		}
	}
	data, err := readTranscriptFull(transcriptPath(s.stateDir, s.id), "")
	if err != nil {
		return err
	}
	if !safeWaitBoundary(*checkpoint, data.Entries, s.resultToolName()) {
		if checkpoint.DelegateID == "" && turnID != checkpoint.ExecutionTurnID && settledFailedWaitBoundary(*checkpoint, data.Entries, s.resultToolName()) {
			// A root's settled failure is historical, not a yielded activation. Keep
			// its checkpoint unchanged until the new input is durably admitted by
			// the ordinary execution/input owner. Readable bytes alone are not proof.
			return s.attachedTranscript().EstablishDurability()
		}
		return errors.New("wait resumption lacks a safe boundary")
	}
	checkpoint.ResumedByTurnID = turnID
	return s.persistWaitCheckpointDurably(*checkpoint)
}

func waitCapacityKind(run *delegatestore.Aggregate) delegateCapacityKind {
	if run.Trigger == delegatestore.TriggerAttention {
		return delegateDriveCapacity
	}
	return delegateTurnCapacity
}

// yieldActivation releases only model execution. The binding, lease, child
// callbacks, job manager and waiter channel remain owned by this generation.
func (a *subagent) yieldActivation(ctx context.Context, checkpoint *schema.WaitCheckpointInfo) error {
	c := a.sess.delegateController
	lease, ok := ctx.Value(delegateRunLeaseContextKey{}).(delegateLease)
	if !ok || c == nil || checkpoint == nil {
		return errDelegateStaleLease
	}
	a.mu.Lock()
	c.mu.Lock()
	run, live, err := c.admitLeaseLocked(lease, delegatestore.PhaseRunning)
	if err == nil && (a.cancelRequested || c.hasSettlementClaimLocked(lease) || live.binding.evidence.batch != nil || live.binding.yielded) {
		err = errDelegateTargetBusy
	}
	if err == nil {
		a.bindYieldCancellationLocked(live.binding)
		live.binding.yielded = true
		c.releaseCapacityLocked(waitCapacityKind(run))
		c.evidenceVersion++
		a.running = false
		a.finalizing = false
		a.cancel = nil
	}
	c.mu.Unlock()
	a.mu.Unlock()
	if err == nil {
		c.recheckNotificationReceivers()
		_ = a.sess.releaseNotificationAdmission()
	}
	return err
}

// Called under the subagent and controller locks. Stop already invokes this
// binding's cancellation outside its locks, including when no executor remains.
func (a *subagent) bindYieldCancellationLocked(binding *delegateRuntimeBinding) {
	if binding.yieldCancelBound {
		return
	}
	base, lease := binding.cancel, binding.lease
	binding.cancel = func() {
		if base != nil {
			base()
		}
		a.cancelYieldedActivation(lease)
	}
	binding.yieldCancelBound = true
}

func (a *subagent) cancelYieldedActivation(lease delegateLease) {
	c := a.sess.delegateController
	a.mu.Lock()
	c.mu.Lock()
	live := c.live[lease.delegateID]
	var cancel context.CancelFunc
	finish := false
	if live != nil && live.binding != nil && live.binding.lease == lease {
		cancel = live.binding.executionCancel
		finish = live.binding.yielded && !a.running
		if finish {
			a.running, a.cancelRequested = true, true
		}
	}
	c.mu.Unlock()
	a.mu.Unlock()
	var returned func()
	if finish {
		if factory := a.sess.cfg.testOnly.subagentActivationReturn; factory != nil {
			returned = factory(a, lease)
		}
	}
	if cancel != nil {
		cancel()
	}
	if finish {
		if returned != nil {
			defer returned()
		}
		// Reuse only the existing canceled-run finalization tail. This cannot
		// enter a model or tool call and owns no execution capacity.
		ctx, cancel := context.WithCancel(a.sess.sessionContext())
		cancel()
		ctx = context.WithValue(ctx, delegateRunLeaseContextKey{}, lease)
		a.run(ctx, "", nil)
	}
}

// resumeYieldedDelegate is called with the existing subagent drive guard held.
// It borrows the same capacity counter as the generation's original executor.
func (s *Session) resumeYieldedDelegate(sub *subagent) (bool, bool) {
	c := s.delegateController
	// Join the owner's existing executor lifetime before taking sub.mu or c.mu.
	// Close must not race a WaitGroup Add after its admission fence.
	s.mu.Lock()
	if s.closingOrClosedLocked() {
		s.mu.Unlock()
		return true, false
	}
	s.sendersWG.Add(1)
	s.mu.Unlock()
	launched := false
	defer func() {
		if !launched {
			s.sendersWG.Done()
		}
	}()
	sub.mu.Lock()
	c.mu.Lock()
	run := c.durable[sub.sess.owningDelegateID]
	live := c.live[sub.sess.owningDelegateID]
	if live == nil || live.binding == nil || !live.binding.yielded {
		c.mu.Unlock()
		sub.mu.Unlock()
		return false, false
	}
	lease := live.binding.lease
	_, _, err := c.admitLeaseLocked(lease, delegatestore.PhaseRunning)
	if err == nil && (sub.running || sub.finalizing || sub.closed || sub.cancelRequested || sub.settlementClaimed || !sub.driving || live.runtime != sub.sess || c.hasSettlementClaimLocked(lease) || live.binding.evidence.batch != nil) {
		err = errDelegateTargetBusy
	}
	if err == nil && !c.reserveCapacityLocked(waitCapacityKind(run)) {
		err = errTreeAtCapacity
	}
	if err != nil {
		c.mu.Unlock()
		sub.mu.Unlock()
		sub.sess.deferNotificationAdmission()
		return true, false
	}
	runCtx, runCancel := context.WithTimeout(sub.sess.sessionContext(), driveTurnTimeout)
	runCtx = context.WithValue(runCtx, delegateRunLeaseContextKey{}, lease)
	runCtx = context.WithValue(runCtx, delegateAttentionRunContextKey{}, struct{}{})
	live.binding.executionCancel = runCancel
	live.binding.yielded = false
	// This is another executor of the SAME generation. Install its exact cancel
	// while Stop is excluded, without replacing done or resetting Stop flags.
	sub.running, sub.finalizing = true, false
	sub.status = SubagentRunning
	sub.cancel = runCancel
	sub.driving, sub.attentionDriveRefused = false, false
	sub.endedAt = nil
	c.attentionDriveSeq++
	c.attentionStateLocked(sub.sess.owningDelegateID).driveTurn = c.attentionDriveSeq
	c.evidenceVersion++
	descriptor := cloneDelegateStartDescriptor(run.Descriptor)
	c.mu.Unlock()
	sub.mu.Unlock()
	// No fallible admission remains after the atomic handoff. Direct Stop now
	// either prevented it or cancels this exact executor before it reads input.
	bindStableDelegateActivity(sub.sess, c, lease)
	launched = true
	s.launchSubagentRun(runCtx, sub, runCancel, "", descriptorProvenance(descriptor))
	s.startDelegateQuietWatchdog(runCtx, lease)
	return true, true
}

// beginWaitingRuntimeRestore claims the existing open lease before releasing
// the controller lock. Its binding excludes cold writers and competing restores.
// It reserves no execution capacity and creates no generation event.
func (c *delegateTreeController) beginWaitingRuntimeRestore(id string) (delegateStartCommit, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	run := c.durable[id]
	if c.closing || run == nil || c.reclamationCoversLocked(id) {
		return delegateStartCommit{}, "", errDelegateTargetBusy
	}
	safe, err := c.readSafeDelegateWaitLocked(run)
	if err != nil {
		return delegateStartCommit{}, "", err
	}
	if !safe {
		return delegateStartCommit{}, "", errDelegateTargetBusy
	}
	live := c.live[id]
	if live == nil {
		live = &delegateLiveState{}
		c.live[id] = live
	}
	ctx, cancel := context.WithCancel(c.rootRuntime.sessionContext())
	lease := delegateLease{delegateID: id, generation: run.Generation}
	live.binding = &delegateRuntimeBinding{lease: lease, cancel: cancel, yielded: true, evidence: &delegateGenerationEvidence{requirement: delegateCompletionReportRequired}}
	c.evidenceVersion++
	return delegateStartCommit{lease: lease, ctx: ctx, descriptor: cloneDelegateStartDescriptor(run.Descriptor), startedAt: run.RunStartedAt, transcriptPath: transcriptPath(c.stateDir, run.Descriptor.ChildSessionID)}, run.Descriptor.ParentDelegateID, nil
}

func (c *delegateTreeController) abandonWaitingRuntimeRestore(lease delegateLease) {
	c.mu.Lock()
	defer c.mu.Unlock()
	live := c.live[lease.delegateID]
	if live != nil && live.runtime == nil && live.binding != nil && live.binding.lease == lease && live.binding.yielded {
		if live.binding.cancel != nil {
			live.binding.cancel()
		}
		live.binding = nil
		c.evidenceVersion++
	}
}

func (c *delegateTreeController) attachWaitingRuntime(lease delegateLease, sub *subagent) (bool, error) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	runtime := sub.sess
	run, live, err := c.exactLeaseLocked(lease)
	if err != nil {
		return false, err
	}
	if c.closing || c.reclamationCoversLocked(lease.delegateID) || run.Phase != delegatestore.PhaseRunning || run.PendingStopSeq != 0 || !run.Resumable || live.recoveryRequired {
		return false, errDelegateTargetBusy
	}
	if blocked, _ := c.ancestorFenceLocked(run.Descriptor.ParentDelegateID); blocked {
		return false, errDelegateTargetBusy
	}
	if !live.binding.yielded || live.binding.runtime != nil && live.binding.runtime != runtime || live.runtime != nil && live.runtime != runtime || runtime.delegateController != c || runtime.owningDelegateID != lease.delegateID || runtime.id != run.Descriptor.ChildSessionID || c.hasDeliveryWorkForOwnerLocked(lease.delegateID) {
		return false, errDelegateTargetBusy
	}
	ownerID, owner, err := c.runtimeOwnerLocked(runtime)
	if err != nil || owner != nil && owner != live || ownerID != "" && ownerID != lease.delegateID {
		return false, errDelegateTargetBusy
	}
	sub.bindYieldCancellationLocked(live.binding)
	if sub.done == nil {
		sub.done = make(chan struct{})
	}
	sub.startedAt, sub.endedAt = run.RunStartedAt, nil
	recheck := c.setResidentRuntimeLocked(live, runtime)
	live.binding.runtime = runtime
	live.binding.ready = true
	c.evidenceVersion++
	return recheck, nil
}

// Reuse restore installation and its callback gate, but keep every new runtime's
// drive guard until the caller has admitted cancellation or parent input. Finishing is
// mandatory even on failure: deferred source repair and callbacks remain owed.
func (s *Session) restoreWaitingDelegateGuarded(id string) (*subagent, func() error, error) {
	c := s.delegateController
	c.mu.Lock()
	expected := delegateLease{delegateID: id, generation: c.durable[id].Generation}
	c.mu.Unlock()
	gate := &owedBootstrapRestore{held: true, holdDrives: true, pending: make(map[*Session]func()), done: make(map[*subagent]bool)}
	var recheck bool
	finish := func() error {
		defer func() {
			gate.open()
			if recheck {
				c.recheckNotificationReceivers()
				recheck = false
			}
		}()
		var err error
		for _, runtime := range gate.restored {
			err = errors.Join(err, runtime.sub.sess.runDeferredRestoreSideEffects())
		}
		return err
	}
	_, sub, released, err := s.restoreColdDelegateAttentionRuntime(id, gate)
	recheck = released
	if err != nil {
		// A concurrent restore can win installation. Use its tracked object,
		// never a discarded candidate. Otherwise preserve retryable uncertainty.
		childID, _ := c.childSessionIDFor(id)
		if current := s.subagentForChild(childID); current != nil {
			c.mu.Lock()
			live := c.live[id]
			matching := live != nil && live.binding != nil && live.binding.lease == expected && live.binding.ready && live.binding.runtime == current.sess && live.runtime == current.sess
			c.mu.Unlock()
			if matching {
				return current, finish, nil
			}
		}
	}
	return sub, finish, err
}
