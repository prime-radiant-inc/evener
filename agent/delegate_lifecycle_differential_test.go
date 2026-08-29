package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// errDelegateLifecycleCanceled is the harness's stand-in for context.Canceled in the
// model's run-end taxonomy (the model file avoids importing context twice).
var errDelegateLifecycleCanceled = context.Canceled

// This file is the deterministic differential runner (spec: Project 1). It
// translates a byte sequence into an operation sequence, applies each
// operation to a real delegateTreeController, and asserts cross-layer
// agreement with the independent model (delegate_lifecycle_model_test.go)
// plus invariants I1–I5 after every operation. Determinism: no wall-clock,
// no sleeps, no network; the controller harness uses a fixed clock; the
// runtime layer uses a scripted fake adapter and a FakeClock on the root.
//
// Operations are numbered per the spec's vocabulary. Op 13 (append failure)
// and op 14 (crash) are Task 2 and intentionally absent here.
//
// Single-delegate scope (spec: model state): the runner drives exactly one
// delegate's generation lifecycle, including steering and settlement-claim
// interactions, but not nested-delegate races.

// lifecycleOp identifies one decoded operation.
type lifecycleOp struct {
	code int // 1-12 per the spec vocabulary
	arg  int // sub-selection decoded from the same byte
}

// decodeDelegateLifecycleProgram maps fuzz bytes onto the operation sequence. Each
// byte selects one operation (code 1-12, byte%12+1) and carries a sub-arg
// (byte/12) used by parameterized ops (run-end error class, finish flavor).
func decodeDelegateLifecycleProgram(program []byte) []lifecycleOp {
	ops := make([]lifecycleOp, 0, len(program))
	for _, b := range program {
		ops = append(ops, lifecycleOp{code: int(b%12) + 1, arg: int(b / 12)})
	}
	return ops
}

// delegateLifecycleRunEndErrorClass is op 8's selectable terminal error.
type delegateLifecycleRunEndErrorClass int

const (
	delegateLifecycleErrNil delegateLifecycleRunEndErrorClass = iota
	delegateLifecycleErrGeneric
	delegateLifecycleErrBudget
	delegateLifecycleErrCanceled
	delegateLifecycleErrJoinedBudgetCanceled
)

// lifecycleRunEndError returns the injected error for a class.
func delegateLifecycleRunEndError(class delegateLifecycleRunEndErrorClass) error {
	switch class {
	case delegateLifecycleErrGeneric:
		return errors.New("lifecycle: generic terminal failure")
	case delegateLifecycleErrBudget:
		return &budgetExhaustionError{Budget: exhaustedBudgetTurns, Limit: 23, Resumable: false}
	case delegateLifecycleErrCanceled:
		return context.Canceled
	case delegateLifecycleErrJoinedBudgetCanceled:
		return errors.Join(&budgetExhaustionError{Budget: exhaustedBudgetToolRounds, Limit: 17, Resumable: true}, context.Canceled)
	default:
		return nil
	}
}

// delegateLifecycleHarness is one differential run: a real controller, the model, and
// the observation plumbing. The controller harness matches
// FuzzDelegateControllerTransitions's shape: a real openDelegateTreeController
// over a delegatestore journal with a fixed clock and no goroutines.
type delegateLifecycleHarness struct {
	t          *testing.T
	c          *delegateTreeController
	model      delegateLifecycleModel
	delegateID string
	// runtime is the bound runtime session for steering paths.
	runtime *Session
	// stopDone is the current stop's done channel once a stop is requested.
	stopDone <-chan struct{}
	// lastFinishedDeliveryID tracks I4's delivery uniqueness window.
	lastFinishedDeliveryID string
}

// newDelegateLifecycleHarness seeds one delegate with the given binding kind and
// returns the harness with the model initialized from the same seed.
//
// Binding kinds (spec seed dimension):
//   - production start: evidence attached via the attention-evidence
//     CommitStart path (startDelegateAttentionEvidenceGeneration precedent),
//     requirement attention-only.
//   - legacy/manual binding: nil evidence (seedDelegateControllerRunning
//     precedent — the tolerance path in escalateCompletionRequirementLocked).
func newDelegateLifecycleHarness(t *testing.T, legacyBinding bool) *delegateLifecycleHarness {
	t.Helper()
	c, _ := newDelegateControllerTestHarness(t, 3, 2)
	h := &delegateLifecycleHarness{t: t, c: c, delegateID: "dlg_target"}
	// The root transcript is the durable receiver identity for top-level
	// deliveries (deliveryTranscriptIdentity uses stateDir/rootSessionID);
	// an empty one is enough for the delivery receiver to arm.
	writeEmptyAttentionTranscript(t, transcriptPath(c.stateDir, "root-session"), "root-session")
	if legacyBinding {
		seedDelegateControllerRunning(t, c, h.delegateID, "")
		attachDelegateSteerRuntime(t, c, h.delegateID, afero.NewMemMapFs())
		h.runtime = c.live[h.delegateID].runtime
		h.model = newDelegateLifecycleModel(false, false)
		// A legacy nil-evidence binding is running with no completion
		// requirement tracked (escalation is a no-op there).
		return h
	}
	seedDelegateControllerIdle(t, c, h.delegateID, "")
	lease := startDelegateAttentionEvidenceGeneration(t, c, h.delegateID)
	attachDelegateSteerRuntime(t, c, h.delegateID, afero.NewMemMapFs())
	h.runtime = c.live[h.delegateID].runtime
	h.model = newDelegateLifecycleModel(true, false)
	_ = lease
	return h
}

// observe reads the real controller state into a delegateLifecycleObservation.
func (h *delegateLifecycleHarness) observe() delegateLifecycleObservation {
	h.t.Helper()
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	obs := delegateLifecycleObservation{}
	aggregate := h.c.durable[h.delegateID]
	if aggregate != nil {
		obs.phase = aggregate.Phase
		obs.runOpen = aggregate.CurrentRunOpen
		obs.stopPending = aggregate.PendingStopSeq != 0
		obs.generation = aggregate.Generation
		if aggregate.LatestOutcome != nil {
			obs.lastOutcome = aggregate.LatestOutcome.Status
		}
		obs.exhausted = aggregate.LatestOutcome != nil && aggregate.LatestOutcome.Status == delegatestore.OutcomeExhausted
	}
	if live := h.c.live[h.delegateID]; live != nil && live.binding != nil {
		obs.evidencePresent = live.binding.evidence != nil
		if live.binding.evidence != nil {
			obs.reportRequired = live.binding.evidence.requirement == delegateCompletionReportRequired
			obs.terminalSeen = live.binding.evidence.terminalSeen
			obs.attentionNoAction = live.binding.evidence.outcome == delegateCompletionOutcomeAttentionNoAction
		}
	}
	// I4: count acknowledged deliveries for the current generation.
	events, err := h.c.store.Load()
	if err != nil {
		h.t.Fatalf("lifecycle observe: load journal: %v", err)
	}
	acknowledged := map[string]bool{}
	finishedDeliveries := map[string]bool{}
	for _, event := range events {
		if event.DelegateID != h.delegateID {
			continue
		}
		if event.DeliveryAcknowledged != nil {
			acknowledged[event.DeliveryAcknowledged.DeliveryID] = true
		}
		if event.RunFinished != nil && event.RunFinished.DeliveryID != "" {
			finishedDeliveries[event.RunFinished.DeliveryID] = true
		}
	}
	delivered := 0
	for id := range finishedDeliveries {
		if acknowledged[id] {
			delivered++
		}
	}
	obs.deliveries = delivered
	obs.lastDisposition = delegateLifecycleLastDisposition(events, h.delegateID)
	return obs
}

// delegateLifecycleDispositionsAt returns the disposition for the most
// recent run-finished event of the delegate (observed directly from the
// journal), so the model's disposition agreement is read from the durable
// record rather than derived.
func delegateLifecycleLastDisposition(events []delegatestore.Event, delegateID string) delegatestore.RunDisposition {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.DelegateID == delegateID && event.RunFinished != nil {
			return event.RunFinished.Disposition
		}
	}
	return ""
}

// check runs the per-op agreement check and invariants I1–I5.
func (h *delegateLifecycleHarness) check(opName string) {
	h.t.Helper()
	obs := h.observe()
	if err := h.model.agreeWithModel(opName, obs); err != nil {
		h.t.Fatal(err)
	}
	h.assertInvariants(obs)
}

// assertInvariants checks I1–I5 over the observation.
func (h *delegateLifecycleHarness) assertInvariants(obs delegateLifecycleObservation) {
	h.t.Helper()
	h.c.mu.Lock()
	defer h.c.mu.Unlock()

	// I1 — single authority: at most one open generation; CurrentRunOpen and
	// live-binding presence agree (outside the crash window, which Task 2
	// introduces — there is no crash here, so the agreement must always hold).
	aggregate := h.c.durable[h.delegateID]
	if aggregate == nil {
		h.t.Fatal("lifecycle I1: delegate aggregate disappeared")
	}
	live := h.c.live[h.delegateID]
	hasBinding := live != nil && live.binding != nil
	if aggregate.CurrentRunOpen && aggregate.Phase != delegatestore.PhaseRunning && aggregate.Phase != delegatestore.PhaseSettling && aggregate.Phase != delegatestore.PhaseStopping {
		h.t.Fatalf("lifecycle I1: run open in phase %s", aggregate.Phase)
	}
	if aggregate.CurrentRunOpen != hasBinding {
		h.t.Fatalf("lifecycle I1: CurrentRunOpen=%t but binding present=%t", aggregate.CurrentRunOpen, hasBinding)
	}
	if hasBinding && live.binding.lease.generation != aggregate.Generation {
		h.t.Fatalf("lifecycle I1: binding generation %d != aggregate generation %d", live.binding.lease.generation, aggregate.Generation)
	}

	// I2 — legal transitions: the durable phase sequence is a path in the
	// model's table. Replay the journal's phase sequence.
	events, err := h.c.store.Load()
	if err != nil {
		h.t.Fatalf("lifecycle I2: load journal: %v", err)
	}
	previous := delegatestore.Phase("")
	seen := false
	for _, event := range events {
		if event.DelegateID != h.delegateID {
			continue
		}
		next, ok := delegateLifecyclePhaseAfterEvent(event, previous)
		if !ok {
			continue // event does not move this delegate's phase
		}
		if !seen {
			// The first phase-moving event creates the delegate; there is no
			// prior phase to transition from.
			previous = next
			seen = true
			continue
		}
		if !legalDelegateLifecyclePhaseTransition(previous, next) {
			h.t.Fatalf("lifecycle I2: illegal phase transition %s -> %s (event %s)", previous, next, event.Kind)
		}
		previous = next
	}

	// I3 — stop precedence (event-scoped): a RunFinished journaled after a
	// covering stop request has outcome neither completed nor
	// completed_no_action. (A finish before the stop keeps its outcome.)
	var stopSeq uint64
	for _, event := range events {
		if event.DelegateID != h.delegateID {
			continue
		}
		if event.SubtreeStopRequested != nil {
			stopSeq = event.Seq
			continue
		}
		if event.SubtreeStopCompleted != nil {
			stopSeq = 0
			continue
		}
		if event.RunFinished != nil && stopSeq != 0 {
			status := event.RunFinished.Outcome.Status
			disposition := event.RunFinished.Disposition
			if status == delegatestore.OutcomeCompleted || disposition == delegatestore.DispositionCompletedNoAction {
				h.t.Fatalf("lifecycle I3: run finished after covering stop %d with outcome %s / disposition %s", stopSeq, status, disposition)
			}
		}
	}

	// I4 — delivery uniqueness: at most one parent delivery executed and
	// acknowledged per generation, and none for completed_no_action.
	acknowledged := map[string]bool{}
	for _, event := range events {
		if event.DelegateID == h.delegateID && event.DeliveryAcknowledged != nil {
			acknowledged[event.DeliveryAcknowledged.DeliveryID] = true
		}
	}
	perGeneration := map[uint64]int{}
	for _, event := range events {
		if event.DelegateID != h.delegateID || event.RunFinished == nil {
			continue
		}
		finished := event.RunFinished
		if finished.Disposition == delegatestore.DispositionCompletedNoAction {
			if finished.DeliveryID != "" {
				h.t.Fatal("lifecycle I4: completed_no_action finish carries a delivery id")
			}
			if acknowledged[finished.DeliveryID] {
				h.t.Fatal("lifecycle I4: completed_no_action finish acknowledged a delivery")
			}
			continue
		}
		if finished.DeliveryID == "" {
			continue
		}
		if !acknowledged[finished.DeliveryID] {
			continue // pending, not acknowledged
		}
		perGeneration[finished.Generation]++
		if perGeneration[finished.Generation] > 1 {
			h.t.Fatalf("lifecycle I4: generation %d acknowledged %d deliveries", finished.Generation, perGeneration[finished.Generation])
		}
	}

	// I5 — report requirement: if any report-requiring work was admitted
	// with evidence present, the final outcome is not completed_no_action.
	// With nil evidence, escalation is a no-op and legacy rules apply.
	if h.model.evidencePresent && h.model.reportRequired {
		for _, event := range events {
			if event.DelegateID != h.delegateID || event.RunFinished == nil {
				continue
			}
			if event.RunFinished.Disposition == delegatestore.DispositionCompletedNoAction {
				h.t.Fatal("lifecycle I5: report-requiring generation with evidence finished completed_no_action")
			}
		}
	}
}

// delegateLifecyclePhaseAfterEvent returns the delegate's phase after one journal
// event, mirroring the fold's per-event phase effects.
func delegateLifecyclePhaseAfterEvent(event delegatestore.Event, previous delegatestore.Phase) (delegatestore.Phase, bool) {
	switch event.Kind {
	case delegatestore.EventDelegateCreated:
		if event.Created != nil && !event.Created.Descriptor.Resumable {
			return delegatestore.PhaseClosed, true
		}
		return delegatestore.PhaseIdle, true
	case delegatestore.EventDelegateRunStarted:
		return delegatestore.PhaseRunning, true
	case delegatestore.EventDelegateTerminalPrepared:
		return delegatestore.PhaseSettling, true
	case delegatestore.EventDelegateRunFinished:
		// The fold keeps the aggregate's resumability; the harness's delegate
		// is always resumable, so finishing returns to idle.
		return delegatestore.PhaseIdle, true
	case delegatestore.EventDelegateResumabilityClosed:
		if previous == delegatestore.PhaseIdle {
			return delegatestore.PhaseClosed, true
		}
		return previous, true
	case delegatestore.EventDelegateSubtreeStopRequested:
		if previous == delegatestore.PhaseClosed {
			return previous, true
		}
		return delegatestore.PhaseStopping, true
	case delegatestore.EventDelegateSubtreeStopCompleted:
		return delegatestore.PhaseIdle, true
	default:
		return previous, false
	}
}

// applyLifecycleOp applies one operation to the real controller and mirrors
// it in the model. Operations that are not applicable in the current state are
// no-ops in both layers (the controller rejects them; the model ignores them),
// which keeps a single byte program covering the whole state space.
func (h *delegateLifecycleHarness) applyLifecycleOp(op lifecycleOp) string {
	h.t.Helper()
	switch op.code {
	case 1: // start a generation (trigger: user work / shell attention)
		return h.opStartGeneration(op)
	case 2: // admit report-requiring work
		return h.opAdmitReportWork(op)
	case 3: // admit system-only work (attention, notification)
		return "admit-system-only"
	case 4: // bare attention response
		return h.opBareAttention()
	case 5: // terminal communicate
		return h.opTerminalCommunicate()
	case 6: // supervision-boundary decision
		return h.opSupervisionBoundary(op)
	case 7: // bounded nudge continuation
		return h.opNudgeContinuation(op)
	case 8: // run-end error injection
		return h.opRunEndError(op)
	case 9: // stop request
		return h.opStopRequest()
	case 10: // ordinary finish
		return h.opOrdinaryFinish(op)
	case 11: // no-action finish (claim path)
		return h.opNoActionFinish()
	case 12: // delivery execution + acknowledgment
		return h.opDelivery()
	default:
		return "unknown"
	}
}

// opStartGeneration starts a new generation when the delegate is idle
// (trigger attention keeps the evidence path reachable on restarts).
func (h *delegateLifecycleHarness) opStartGeneration(_ lifecycleOp) string {
	h.c.mu.Lock()
	aggregate := h.c.durable[h.delegateID]
	phase := delegatestore.PhaseIdle
	if aggregate != nil {
		phase = aggregate.Phase
	}
	h.c.mu.Unlock()
	if phase != delegatestore.PhaseIdle {
		return "start-suppressed"
	}
	lease := startDelegateAttentionEvidenceGeneration(h.t, h.c, h.delegateID)
	attachDelegateSteerRuntime(h.t, h.c, h.delegateID, afero.NewMemMapFs())
	h.runtime = h.c.live[h.delegateID].runtime
	// A fresh generation resets the model (the previous run finished back to
	// idle before this start).
	h.model = newDelegateLifecycleModel(true, false)
	h.model.generation = lease.generation
	return "start-generation"
}

// opAdmitReportWork admits report-requiring work via a steering bind (the
// evidence-path escalation trigger). On the legacy nil-evidence binding the
// production escalation is a no-op (the tolerance path).
func (h *delegateLifecycleHarness) opAdmitReportWork(_ lifecycleOp) string {
	if !h.currentRunOpen() {
		return "report-work-suppressed"
	}
	if _, err := h.c.Steer(h.t.Context(), rootDelegateActor("root-session"), h.delegateID, "lifecycle report work"); err != nil {
		return "report-work-rejected"
	}
	// Bind the steer through a model request so the escalation path runs
	// (CompleteModelRequest consumes admitted steering and escalates).
	if _, err := completeDelegateModelRequest(h.c, h.currentLease()); err != nil {
		return "report-work-unbound"
	}
	h.model.admitReportRequiringWork()
	return "admit-report-work"
}

// opBareAttention records the attention no-action outcome when eligible
// (op 4).
func (h *delegateLifecycleHarness) opBareAttention() string {
	if !h.currentRunOpen() {
		return "bare-attention-suppressed"
	}
	lease := h.currentLease()
	recorded, err := h.c.recordAttentionNoAction(lease)
	if err != nil {
		return "bare-attention-rejected"
	}
	if !recorded {
		return "bare-attention-ineligible"
	}
	h.model.recordAttentionNoAction()
	return "bare-attention-no-action"
}

// opTerminalCommunicate latches terminalSeen (op 5).
func (h *delegateLifecycleHarness) opTerminalCommunicate() string {
	if !h.currentRunOpen() {
		return "terminal-communicate-suppressed"
	}
	if err := h.c.recordTerminalSeen(h.currentLease()); err != nil {
		return "terminal-communicate-rejected"
	}
	h.model.recordTerminalSeen()
	return "terminal-communicate"
}

// opSupervisionBoundary drives the supervision-boundary decision (op 6):
// pending steers → continue; suppression arbitration.
func (h *delegateLifecycleHarness) opSupervisionBoundary(_ lifecycleOp) string {
	if !h.currentRunOpen() {
		return "supervision-suppressed"
	}
	boundary, err := h.c.SupervisionBoundary(h.currentLease(), delegateSettlementOrdinary)
	if err != nil {
		return "supervision-busy"
	}
	switch boundary {
	case delegateSupervisionContinue:
		return "supervision-continue"
	case delegateSupervisionSuppress:
		return "supervision-suppress"
	default:
		return "supervision-proceed"
	}
}

// opNudgeContinuation drives the needs-nudge decision → nudge → outcome
// chain (op 7) at the controller level: the completion decision selects
// nudge, and a subsequent report work admission resolves it.
func (h *delegateLifecycleHarness) opNudgeContinuation(_ lifecycleOp) string {
	if !h.currentRunOpen() {
		return "nudge-suppressed"
	}
	decision, err := h.c.completionDecision(h.currentLease())
	if err != nil {
		return "nudge-decision-error"
	}
	switch decision {
	case delegateCompletionNeedsNudge:
		// The bounded nudge fires when no report has landed yet. The model
		// records the requirement only when the run's entry kind actually
		// required a report; the attention trigger starts attention-only, so
		// a needs-nudge decision alone does not escalate the requirement —
		// the nudge's outcome (a report or bare response) decides.
		return "nudge-needs-report"
	case delegateCompletionFinishNoAction:
		return "nudge-no-action"
	default:
		return "nudge-existing-terminal"
	}
}

// opRunEndError injects a selected terminal run error through the real
// subagent run path (op 8): BeginRunFinalization with the error bound, then
// the finish. This is what makes the exhaustion-overwrite class reachable.
func (h *delegateLifecycleHarness) opRunEndError(op lifecycleOp) string {
	if !h.currentRunOpen() {
		return "run-end-suppressed"
	}
	class := delegateLifecycleRunEndErrorClass(op.arg % 5)
	cancelRequested := (op.arg/5)%2 == 1
	err := delegateLifecycleRunEndError(class)
	lease := h.currentLease()

	// Mirror (*subagent).run's finalization. The settlement mode is predicted
	// by the model's own copy (settlementModeForRun) — production must agree
	// with it, which is itself one of the differential checks.
	mode := h.model.settlementModeForRun(err, cancelRequested)
	claim, continued, beginErr := h.c.BeginRunFinalization(lease, mode, err)
	if beginErr != nil {
		return "run-end-begin-rejected"
	}
	if continued {
		return "run-end-continued"
	}
	<-claim.ready
	if _, err := h.c.AttentionResolutionsForFinalization(claim); err != nil {
		return "run-end-attention-error"
	}
	// The model predicts the finish outcome with its own independent copy of
	// the finish-path outcome selection (finishOutcomeFromRun) and the
	// run-end classification (classifyRunEnd); the finish handed to
	// production is built by the production finish-from-run builder from the
	// sampled error. The two copies must agree — including under the joined
	// budget+Canceled error, where the exhaustion-overwrite bug split them.
	modelStatus, exhaustionFromRunError := h.model.classifyRunEnd(err, cancelRequested)
	modelFinishStatus := h.model.finishOutcomeFromRun(err, false)
	// The two model copies intentionally differ on one input: the joined
	// budget+Canceled error under cancelRequested. classifyRunEnd (the run
	// loop's a.err/status projection) pins Cancelled there — the joined error
	// is kept verbatim and no exhaustion payload exists — while
	// finishOutcomeFromRun (the packet/outcome projection) still selects
	// exhausted because stableDelegateFinishFromRun tests the budget
	// component before the canceled test. That split is the exhaustion-
	// overwrite boundary, so the harness pins each copy against its own
	// production counterpart below rather than against each other.
	finish := stableDelegateFinishFromRun(delegateTerminalRunInputs{
		runErr:  err,
		endedAt: h.c.now(),
	})
	productionClass := classifyRunEnd(err, cancelRequested)
	if finish.outcome != modelFinishStatus {
		h.t.Fatalf("lifecycle differential: run end: production finish outcome=%s, model finish=%s (exhaustion-overwrite divergence)", finish.outcome, modelFinishStatus)
	}
	if delegatestore.OutcomeStatus(productionClass.status) != modelStatus {
		h.t.Fatalf("lifecycle differential: run end: production classifyRunEnd status=%s, model=%s (run-end classification divergence)", productionClass.status, modelStatus)
	}
	if (productionClass.exhaustion != nil) != exhaustionFromRunError {
		h.t.Fatalf("lifecycle differential: run end: production exhaustion payload=%t, model=%t (exhaustion-overwrite payload divergence)", productionClass.exhaustion != nil, exhaustionFromRunError)
	}
	if claim.mode == delegateSettlementTerminal {
		if _, finishErr := h.c.FinishGeneration(lease, finish); finishErr != nil {
			return "run-end-finish-error"
		}
		h.applyRunEndModelFinish(finish.outcome, finish.disposition, exhaustionFromRunError, nonResumableExhaustion(finish))
		h.reconcileModelStopAfterFinish()
		return "run-end-terminal"
	}
	// Ordinary settlement: prepare the packet through the claim, then finish.
	if _, settleErr := h.c.CompleteSettlement(claim, finish.packet); settleErr != nil {
		return "run-end-settle-error"
	}
	if _, finishErr := h.c.FinishGeneration(lease, finish); finishErr != nil {
		return "run-end-finish-error"
	}
	h.applyRunEndModelFinish(finish.outcome, finish.disposition, exhaustionFromRunError, nonResumableExhaustion(finish))
	h.reconcileModelStopAfterFinish()
	return "run-end-ordinary"
}

// nonResumableExhaustion reports whether the finish is a non-resumable
// exhaustion (which appends a resumability closure and ends in PhaseClosed).
func nonResumableExhaustion(finish delegateFinish) bool {
	return finish.outcome == delegatestore.OutcomeExhausted && finish.exhaustionResumable != nil && !*finish.exhaustionResumable
}

// applyRunEndModelFinish records a run-end finish in the model, including the
// exhaustion-payload source decision (the exhaustion-overwrite guard).
func (h *delegateLifecycleHarness) applyRunEndModelFinish(status delegatestore.OutcomeStatus, disposition delegatestore.RunDisposition, exhaustionFromRunError bool, nonResumableExhaustion bool) {
	if exhaustionFromRunError && status == delegatestore.OutcomeExhausted {
		h.model.setExhaustionFromRunError()
	}
	h.c.mu.Lock()
	stopSeq := uint64(0)
	if aggregate := h.c.durable[h.delegateID]; aggregate != nil {
		stopSeq = aggregate.PendingStopSeq
	}
	h.c.mu.Unlock()
	h.model.finish(status, disposition, stopSeq)
	if nonResumableExhaustion {
		// Non-resumable exhaustion closes the delegate (the finish appends a
		// resumability closure alongside the run-finished event).
		h.model.phase = delegatestore.PhaseClosed
	}
}

// reconcileModelStopAfterFinish aligns the model with the durable state
// after a run-end finish that landed under a pending stop: the fold records
// the finish and the stop keeps the phase in stopping with outcome stopped
// until SubtreeStopCompleted.
func (h *delegateLifecycleHarness) reconcileModelStopAfterFinish() {
	h.c.mu.Lock()
	aggregate := h.c.durable[h.delegateID]
	h.c.mu.Unlock()
	if aggregate == nil || aggregate.PendingStopSeq == 0 {
		return
	}
	h.model.stopPending = true
	h.model.phase = delegatestore.PhaseStopping
	h.model.lastOutcome = delegatestore.OutcomeStopped
	h.model.lastDisposition = delegatestore.DispositionTerminalError
}

// opStopRequest requests a subtree stop before finalization (op 9). The stop
// is drained synchronously through Reconcile — never StopSubtreeAndDrive.
func (h *delegateLifecycleHarness) opStopRequest() string {
	h.c.mu.Lock()
	aggregate := h.c.durable[h.delegateID]
	h.c.mu.Unlock()
	if aggregate == nil {
		return "stop-suppressed"
	}
	result, cancelPlan, plans, err := h.c.StopSubtree(rootDelegateActor("root-session"), h.delegateID)
	if err != nil {
		return "stop-rejected"
	}
	executeDelegateCancelPlan(cancelPlan)
	if err := h.executeLifecyclePlans(plans); err != nil {
		h.t.Fatalf("lifecycle stop: execute plans: %v", err)
	}
	h.model.requestStop()
	h.stopDone = result.done
	// Drain the stop synchronously via the real reconcile path.
	for {
		h.c.mu.Lock()
		pending := h.c.stop
		h.c.mu.Unlock()
		if pending == nil {
			break
		}
		evidence, err := collectDelegateReconcileEvidence(h.c.stateDir, h.c.ReconcileRequirements())
		if err != nil {
			h.t.Fatalf("lifecycle stop: collect evidence: %v", err)
		}
		reconcilePlans, err := h.c.Reconcile(evidence)
		if err != nil {
			h.t.Fatalf("lifecycle stop: reconcile: %v", err)
		}
		if err := h.executeLifecyclePlans(reconcilePlans); err != nil {
			h.t.Fatalf("lifecycle stop: execute reconcile plans: %v", err)
		}
		h.c.mu.Lock()
		stillPending := h.c.stop
		h.c.mu.Unlock()
		if stillPending == pending {
			// The stop is waiting on the open generation's finish (or an
			// in-flight runner this harness never launches). The pending stop
			// legitimately persists; the model keeps stopPending until the
			// stop completes.
			h.model.requestStop()
			return "stop-requested-pending"
		}
	}
	h.model.completeStop()
	return "stop-requested"
}

// opOrdinaryFinish performs an ordinary FinishGeneration (op 10).
func (h *delegateLifecycleHarness) opOrdinaryFinish(op lifecycleOp) string {
	if !h.currentRunOpen() {
		return "finish-suppressed"
	}
	lease := h.currentLease()
	finish := delegateFinish{outcome: delegatestore.OutcomeCompleted, reason: "lifecycle ordinary finish", endedAt: h.c.now()}
	switch op.arg % 3 {
	case 0:
		packet := delegateControllerReportedPacket("lifecycle report")
		finish.packet = &packet
		finish.disposition = delegatestore.DispositionReported
	case 1:
		packet := delegateTerminalErrorPacket("lifecycle terminal error")
		finish.packet = &packet
		finish.outcome = delegatestore.OutcomeFailed
		finish.disposition = delegatestore.DispositionTerminalError
	default:
		packet := delegateTerminalErrorPacket("lifecycle missing terminal")
		finish.packet = &packet
		finish.outcome = delegatestore.OutcomeFailed
		finish.disposition = delegatestore.DispositionTerminalError
	}
	if _, err := h.c.FinishGeneration(lease, finish); err != nil {
		return "finish-rejected"
	}
	h.c.mu.Lock()
	stopSeq := uint64(0)
	phase := delegatestore.PhaseIdle
	if aggregate := h.c.durable[h.delegateID]; aggregate != nil {
		stopSeq = aggregate.PendingStopSeq
		phase = aggregate.Phase
	}
	h.c.mu.Unlock()
	h.model.finish(finish.outcome, finish.disposition, stopSeq)
	if phase == delegatestore.PhaseStopping {
		// The finish landed under a covering stop that has not completed yet:
		// the fold forces the outcome to stopped (outcome_stopped /
		// stopped_by_parent) and keeps the phase in stopping until
		// SubtreeStopCompleted.
		h.model.stopPending = true
		h.model.phase = delegatestore.PhaseStopping
		h.model.lastOutcome = delegatestore.OutcomeStopped
		h.model.lastDisposition = delegatestore.DispositionTerminalError
	}
	return "ordinary-finish"
}

// opNoActionFinish performs the no-action finish through the claim path
// (op 11): BeginRunFinalization with a nil run error, prepareNoAction, then
// FinishNoAction. This is the #580 no-action selection path.
func (h *delegateLifecycleHarness) opNoActionFinish() string {
	if !h.currentRunOpen() {
		return "no-action-suppressed"
	}
	lease := h.currentLease()
	claim, continued, err := h.c.BeginRunFinalization(lease, delegateSettlementOrdinary, nil)
	if err != nil {
		return "no-action-begin-rejected"
	}
	if continued {
		return "no-action-continued"
	}
	<-claim.ready
	if _, err := h.c.AttentionResolutionsForFinalization(claim); err != nil {
		return "no-action-attention-error"
	}
	fallback := delegateFinish{outcome: delegatestore.OutcomeCompleted, disposition: delegatestore.DispositionReported, reason: "lifecycle fallback", endedAt: h.c.now()}
	prepared, prepareErr := h.c.prepareNoAction(claim, fallback)
	if prepareErr != nil {
		return "no-action-prepare-error"
	}
	if !prepared {
		// Production refused the no-action finish the model says is
		// eligible: that refusal is the #580 no-action-selection defect
		// class (the durable store knew completed_no_action; the runtime
		// path could not select it).
		if h.model.noActionEligible(true) {
			h.t.Fatal("lifecycle differential: no-action: production refused an eligible no-action finish (#580 selection class)")
		}
		return "no-action-ineligible"
	}
	if !h.model.noActionEligible(true) {
		h.t.Fatal("lifecycle differential: no-action: production prepared but model ineligible")
	}
	if _, finishErr := h.c.FinishNoAction(claim); finishErr != nil {
		// Production prepared the eligible no-action finish but could not
		// execute it: the durable store selected completed_no_action while
		// the finish path refused — the #580 no-action-selection defect class.
		h.t.Fatalf("lifecycle differential: no-action: FinishNoAction failed on an eligible prepared claim (#580 selection class): %v", finishErr)
	}
	h.c.mu.Lock()
	stopSeq := uint64(0)
	if aggregate := h.c.durable[h.delegateID]; aggregate != nil {
		stopSeq = aggregate.PendingStopSeq
	}
	h.c.mu.Unlock()
	h.model.finish(delegatestore.OutcomeCompleted, delegatestore.DispositionCompletedNoAction, stopSeq)
	return "no-action-finish"
}

// opDelivery executes and acknowledges a pending parent delivery (op 12):
// BeginDelivery then CompleteDelivery, both synchronous. This is I4's
// parent-visible half.
func (h *delegateLifecycleHarness) opDelivery() string {
	h.c.mu.Lock()
	deliveries := 0
	if aggregate := h.c.durable[h.delegateID]; aggregate != nil {
		deliveries = len(aggregate.PendingDeliveries)
	}
	h.c.mu.Unlock()
	if deliveries == 0 {
		return "delivery-none-pending"
	}
	plans := h.c.ReplayDeliveries()
	if len(plans) == 0 {
		return "delivery-none-replayable"
	}
	plan := plans[0]
	receiver := newFakeDelegateDeliveryReceiver()
	next, err := deliverDelegatePacket(plan, receiver)
	if err != nil {
		return "delivery-rejected"
	}
	_ = next
	h.model.delivered++
	return "delivery-acknowledged"
}

// currentRunOpen reports whether the delegate's current run is open.
func (h *delegateLifecycleHarness) currentRunOpen() bool {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	aggregate := h.c.durable[h.delegateID]
	return aggregate != nil && aggregate.CurrentRunOpen
}

// currentLease returns the current generation's lease from the live binding.
func (h *delegateLifecycleHarness) currentLease() delegateLease {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	live := h.c.live[h.delegateID]
	if live == nil || live.binding == nil {
		h.t.Fatal("lifecycle: no live binding for current lease")
	}
	return live.binding.lease
}

// executeLifecyclePlans executes mutation plans on a bound root-free session.
func (h *delegateLifecycleHarness) executeLifecyclePlans(plans delegateMutationPlans) error {
	for _, update := range plans.updates {
		_ = update
	}
	for _, attention := range plans.attention {
		if err := h.c.executeDelegateAttentionCleanup(attention); err != nil {
			return err
		}
	}
	for _, plan := range plans.deliveries {
		if _, _, err := h.c.BeginDelivery(plan); err != nil {
			continue // not currently deliverable; leave pending
		}
	}
	return nil
}

// runLifecycleProgram applies the whole decoded program, checking agreement
// and invariants after every operation. It returns the operation trace for
// determinism comparison.
func runDelegateLifecycleProgram(t *testing.T, program []byte, legacyBinding bool) []string {
	t.Helper()
	h := newDelegateLifecycleHarness(t, legacyBinding)
	trace := []string{}
	for _, op := range decodeDelegateLifecycleProgram(program) {
		name := h.applyLifecycleOp(op)
		trace = append(trace, name)
		h.check(name)
	}
	return trace
}

// runLifecycleProgramChecked runs with per-op panic recovery so a mid-program
// failure still reports the trace up to the failing operation.
func runDelegateLifecycleProgramChecked(t *testing.T, program []byte, legacyBinding bool) (trace []string) {
	h := newDelegateLifecycleHarness(t, legacyBinding)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("lifecycle panic after ops %v: %v", trace, r)
		}
	}()
	for _, op := range decodeDelegateLifecycleProgram(program) {
		name := h.applyLifecycleOp(op)
		trace = append(trace, name)
		h.check(name)
	}
	return trace
}

// lifecycleSeed is one checked-in corpus seed: a byte program, a binding
// kind, and the defect class it targets.
type lifecycleSeed struct {
	name          string
	program       []byte
	legacyBinding bool
	targets       string
}

// delegateLifecycleSeedCorpus is the plan's eight seed sequences plus the drivers
var delegateLifecycleSeedCorpus = []lifecycleSeed{
	{
		// bare-attention-then-queued-work: after a bare attention no-action
		// (op 4), report-requiring work (op 2) must escalate the requirement
		// so the generation cannot finish no-action (I5, #580 class).
		name:          "bare-attention-then-queued-work",
		program:       []byte{op1(4), op1(2), op1(11), op1(10)},
		legacyBinding: false,
		targets:       "no-action selection after report work (I5)",
	},
	{
		// stop-before-finish: a stop request (op 9) before the finish means
		// the RunFinished is journaled after the covering stop, so the
		// outcome must be neither completed nor completed_no_action (I3).
		name:          "stop-before-finish",
		program:       []byte{op1(9), op1(10)},
		legacyBinding: false,
		targets:       "stop precedence (I3)",
	},
	{
		// attention-no-action finish: bare attention (op 4) then the claim
		// path no-action finish (op 11) must select completed_no_action with
		// no delivery (I4) — the #580 no-action selection class.
		name:          "attention-no-action-finish",
		program:       []byte{op1(4), op1(11)},
		legacyBinding: false,
		targets:       "no-action selection (#580, I4)",
	},
	{
		// terminal-communicate finish: a terminal communicate (op 5) makes
		// the completion decision use the existing terminal, and the
		// ordinary finish (op 10) settles reported (I2, I4).
		name:          "terminal-communicate-finish",
		program:       []byte{op1(5), op1(6), op1(10)},
		legacyBinding: false,
		targets:       "terminal-seen finish path (I2, I4)",
	},
	{
		// nudge-then-report: the needs-nudge decision (op 7) escalates the
		// requirement, so the subsequent no-action attempt is ineligible and
		// the ordinary finish reports (I5).
		name:          "nudge-then-report",
		program:       []byte{op1(7), op1(11), op1(10)},
		legacyBinding: false,
		targets:       "nudge escalation (I5)",
	},
	{
		// nil-evidence steering: on the legacy nil-evidence binding, steering
		// (op 2) is admitted but escalation is a no-op — the run still
		// finishes by the legacy outcome rules (the nil-evidence tolerance
		// class).
		name:          "nil-evidence-steering",
		program:       []byte{op1(2), op1(4), op1(10)},
		legacyBinding: true,
		targets:       "nil-evidence escalation tolerance",
	},
	{
		// joined cancel+budget exhaustion: op 8 with the joined error under a
		// racing cancel publishes cancelled (never exhausted, never an
		// exhaustion payload) and keeps the joined error verbatim — the
		// exhaustion-overwrite class.
		name: "joined-cancel-budget-exhaustion",
		// arg 9: class 4 (joined budget+canceled) with the cancel bit set.
		program:       []byte{opArg(8, 9)},
		legacyBinding: false,
		targets:       "exhaustion overwrite (joined cancel+budget)",
	},
	{
		// delivery execute+ack: after a reported finish (op 10), the pending
		// delivery is executed and acknowledged exactly once (op 12) (I4).
		name:          "delivery-execute-ack",
		program:       []byte{op1(5), op1(10), op1(12)},
		legacyBinding: false,
		targets:       "delivery uniqueness (I4)",
	},
}

// op1 encodes one byte selecting operation code with arg 0.
func op1(code int) byte {
	return byte(code - 1)
}

// opArg encodes one byte selecting operation code with the given arg.
func opArg(code, arg int) byte {
	return byte((code - 1) + 12*arg)
}

// TestDelegateLifecycleDifferentialSeeds drives every checked-in seed through
// the real controller and asserts model agreement plus I1–I5 after every
// operation.
func TestDelegateLifecycleDifferentialSeeds(t *testing.T) {
	for _, seed := range delegateLifecycleSeedCorpus {
		t.Run(seed.name, func(t *testing.T) {
			runDelegateLifecycleProgramChecked(t, seed.program, seed.legacyBinding)
		})
	}
}

// TestDelegateLifecycleDifferentialDeterminism proves byte-for-byte
// determinism: the same program run twice produces the identical operation
// trace (spec: determinism requirements — a failing seed reproduces from the
// input alone).
func TestDelegateLifecycleDifferentialDeterminism(t *testing.T) {
	for _, seed := range delegateLifecycleSeedCorpus {
		t.Run(seed.name, func(t *testing.T) {
			first := runDelegateLifecycleProgram(t, seed.program, seed.legacyBinding)
			second := runDelegateLifecycleProgram(t, seed.program, seed.legacyBinding)
			if len(first) != len(second) {
				t.Fatalf("trace lengths differ: %d vs %d", len(first), len(second))
			}
			for i := range first {
				if first[i] != second[i] {
					t.Fatalf("trace diverges at op %d: %q vs %q", i, first[i], second[i])
				}
			}
		})
	}
}

// TestDelegateLifecycleDifferentialCrossBinding runs every seed under both
// binding kinds, pinning that the seed dimension reaches both the evidence
// path and the legacy nil-evidence tolerance path.
func TestDelegateLifecycleDifferentialCrossBinding(t *testing.T) {
	for _, seed := range delegateLifecycleSeedCorpus {
		t.Run(seed.name, func(t *testing.T) {
			runDelegateLifecycleProgramChecked(t, seed.program, !seed.legacyBinding)
		})
	}
}
