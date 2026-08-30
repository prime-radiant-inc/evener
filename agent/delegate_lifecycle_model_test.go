package agent

import (
	"errors"
	"fmt"
	"strconv"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// This file is the differential harness's independent model of a single
// delegate's generation lifecycle. It is intentionally a third, tiny, auditable
// copy of the decision logic — that is what differential testing is (spec:
// docs/superpowers/specs/2026-08-29-delegate-lifecycle-harness-reducer-design.md,
// "Model state"). It never constructs packets: phases, outcomes,
// exhaustion-payload source, and evidence presence only.
//
// The transition rules mirror the production pins the harness exists to guard:
//
//   - classifyRunEnd (agent/subagents.go): settlement mode is terminal when
//     cancelRequested regardless of err; SubagentCancelled additionally
//     requires errors.Is(err, context.Canceled); the exhaustion payload is
//     non-nil exactly when status is Exhausted, and a joined
//     exhaustion+Canceled under cancel publishes Cancelled with the joined
//     error kept verbatim (no exhaustion payload).
//   - escalateCompletionRequirementLocked (delegate_tree_controller.go):
//     nil-evidence legacy bindings make escalation a no-op — with nil evidence,
//     admitted steering does NOT make the run report-required.
//   - The completion decision / no-action eligibility chain
//     (completionDecision, noActionEvidenceEligible): no-action requires
//     attention-only requirement, the recorded attention-no-action outcome,
//     and no terminal seen; with evidence present, report-requiring work
//     escalates the requirement and blocks no-action.
//   - delegatestore's fold (internal/delegatestore/fold.go): the legal phase
//     sequence and stop/finish effects on the durable aggregate.

// delegateLifecycleExhaustionSource is the value-level dimension of the
// exhaustion-overwrite bug: where a terminal outcome's exhaustion payload
// came from.
type delegateLifecycleExhaustionSource uint8

const (
	// delegateLifecycleExhaustionNone: the terminal outcome is not exhausted.
	delegateLifecycleExhaustionNone delegateLifecycleExhaustionSource = iota
	// delegateLifecycleExhaustionFromRunError: the run error itself carried the budget
	// exhaustion (the payload exists and the run error is replaced by it).
	delegateLifecycleExhaustionFromRunError
	// delegateLifecycleExhaustionFromPreparedTerminal: the outcome was normalized from
	// a previously prepared terminal packet (settlement path), not from this
	// run error.
	delegateLifecycleExhaustionFromPreparedTerminal
)

// delegateLifecycleModel is the model state machine for one delegate's current
// generation. All fields are the spec's model state; the model never holds
// pointers into controller state.
type delegateLifecycleModel struct {
	// phase is the durable phase sequence position.
	phase delegatestore.Phase
	// runOpen mirrors Aggregate.CurrentRunOpen.
	runOpen bool
	// stopPending mirrors Aggregate.PendingStopSeq != 0.
	stopPending bool
	// reportRequired is the model of the live binding evidence requirement:
	// true when report-requiring work was admitted (evidence path) or the
	// generation started report-required (owner-input trigger).
	reportRequired bool
	// terminalSeen mirrors evidence.terminalSeen.
	terminalSeen bool
	// attentionNoAction mirrors evidence.outcome ==
	// delegateCompletionOutcomeAttentionNoAction.
	attentionNoAction bool
	// delivered counts parent deliveries executed+acknowledged for the
	// current generation (I4).
	delivered int
	// evidencePresent is the binding-kind seed: production start attaches
	// evidence, legacy/manual bindings do not.
	evidencePresent bool
	// generation is the current generation number.
	generation uint64
	// exhaustionSource records where a terminal exhaustion payload came from.
	exhaustionSource delegateLifecycleExhaustionSource
	// exhausted is true when the terminal outcome status is exhausted.
	exhausted bool
	// lastOutcome is the terminal outcome status of the most recent finished
	// generation (empty until one finishes).
	lastOutcome delegatestore.OutcomeStatus
	// lastDisposition is the terminal disposition of the most recent finished
	// generation.
	lastDisposition delegatestore.RunDisposition
	// stopCoveredFinish records whether the most recent RunFinished for the
	// delegate was journaled after a covering stop request (I3).
	stopCoveredFinish bool
	// finishAfterStopSeq is the pending-stop sequence in effect when the most
	// recent RunFinished was journaled (0 = no covering stop).
	finishAfterStopSeq uint64
}

// newDelegateLifecycleModel returns the model state after the binding-kind seed: a
// generation is open with the given evidence presence. Production starts
// (CommitStart) attach evidence with a requirement derived from the trigger;
// legacy/manual bindings have nil evidence.
func newDelegateLifecycleModel(evidencePresent bool, reportRequired bool) delegateLifecycleModel {
	return delegateLifecycleModel{
		phase:           delegatestore.PhaseRunning,
		runOpen:         true,
		reportRequired:  reportRequired,
		evidencePresent: evidencePresent,
		generation:      1,
	}
}

// admitReportRequiringWork models op 2 (user input, follow-up, goal
// continuation, steering bind, hook output). On the evidence path this
// escalates the requirement (escalateCompletionRequirementLocked); with nil
// evidence the escalation is a no-op and the legacy outcome rules apply.
func (m *delegateLifecycleModel) admitReportRequiringWork() {
	if m.evidencePresent {
		m.reportRequired = true
	}
}

// recordAttentionNoAction models op 4 (bare attention response): it records
// the attention no-action outcome only when the requirement is still
// attention-only and no terminal was seen (recordAttentionNoAction).
func (m *delegateLifecycleModel) recordAttentionNoAction() {
	if m.reportRequired || m.terminalSeen {
		return
	}
	m.attentionNoAction = true
}

// recordTerminalSeen models op 5 (terminal communicate): terminalSeen
// latches and clears the no-action outcome eligibility.
func (m *delegateLifecycleModel) recordTerminalSeen() {
	m.terminalSeen = true
}

// completionDecisionPrediction is the model's copy of completionDecision's
// branch order (delegate_tree_steer.go): terminal-seen wins (use the
// existing terminal); then attention-only with the recorded no-action
// outcome selects the packetless no-action finish; otherwise the run needs
// a nudge (a report).
func (m *delegateLifecycleModel) completionDecisionPrediction() delegateCompletionDecision {
	if m.terminalSeen {
		return delegateCompletionUseExistingTerminal
	}
	if !m.reportRequired && m.attentionNoAction {
		return delegateCompletionFinishNoAction
	}
	return delegateCompletionNeedsNudge
}

// classifyRunEnd mirrors the production classifier pins for op 8's outcome
// rules. It is a copy of the classifyRunEnd contract restricted to the
// harness's error vocabulary (nil / generic / budget / canceled /
// joined budget+canceled) so the harness can catch an exhaustion-overwrite
// divergence between the model and the real run path.
func (m *delegateLifecycleModel) classifyRunEnd(err error, cancelRequested bool) (status delegatestore.OutcomeStatus, exhaustionFromRunError bool) {
	budgetExhausted := isDelegateLifecycleBudgetExhaustion(err)
	canceled := errors.Is(err, errDelegateLifecycleCanceled)
	switch {
	case cancelRequested && canceled:
		// Cancelled keeps the joined error verbatim — never an exhaustion
		// payload, even when the error also carries a budget component.
		return delegatestore.OutcomeCancelled, false
	case budgetExhausted:
		return delegatestore.OutcomeExhausted, true
	case err != nil:
		return delegatestore.OutcomeFailed, false
	default:
		return delegatestore.OutcomeCompleted, false
	}
}

// isDelegateLifecycleBudgetExhaustion reports whether err carries a budget
// exhaustion component (errors.As over *budgetExhaustionError).
func isDelegateLifecycleBudgetExhaustion(err error) bool {
	var exhausted *budgetExhaustionError
	return errors.As(err, &exhausted)
}

// finish models the terminal effects of a RunFinished event on the durable
// aggregate (fold semantics): the run closes, the phase returns to idle
// (resumable delegates), and the outcome is recorded. stopPendingAtFinish
// is the pending-stop sequence in effect at journal time (I3's event order).
func (m *delegateLifecycleModel) finish(outcome delegatestore.OutcomeStatus, disposition delegatestore.RunDisposition, stopSeqAtFinish uint64) {
	m.runOpen = false
	m.phase = delegatestore.PhaseIdle
	m.lastOutcome = outcome
	m.lastDisposition = disposition
	m.finishAfterStopSeq = stopSeqAtFinish
	m.stopCoveredFinish = stopSeqAtFinish != 0
	m.exhausted = outcome == delegatestore.OutcomeExhausted
	if m.exhausted {
		if m.exhaustionSource == delegateLifecycleExhaustionNone {
			m.exhaustionSource = delegateLifecycleExhaustionFromPreparedTerminal
		}
	} else {
		m.exhaustionSource = delegateLifecycleExhaustionNone
	}
}

// setExhaustionFromRunError records that the terminal exhaustion payload
// came from the run error (the exhaustion-overwrite decision in
// (*subagent).run's finalization: a.err = runEnd.exhaustion).
func (m *delegateLifecycleModel) setExhaustionFromRunError() {
	m.exhaustionSource = delegateLifecycleExhaustionFromRunError
}

// finishOutcomeFromRun is the model's independent copy of the finish-path
// outcome selection (stableDelegateFinishFromRun's branch order): a nil run
// error with a communicated report completes; otherwise a budget-exhaustion
// component wins (BEFORE the canceled test, so a joined budget+Canceled error
// finishes exhausted); otherwise context.Canceled cancels; otherwise the run
// failed. This is the value-level copy the exhaustion-overwrite bug lived in:
// the branch order here and the payload-presence decision in classifyRunEnd
// must not split under a joined error.
func (m *delegateLifecycleModel) finishOutcomeFromRun(err error, communicated bool) delegatestore.OutcomeStatus {
	if err == nil && communicated {
		return delegatestore.OutcomeCompleted
	}
	if isDelegateLifecycleBudgetExhaustion(err) {
		return delegatestore.OutcomeExhausted
	}
	if errors.Is(err, errDelegateLifecycleCanceled) {
		return delegatestore.OutcomeCancelled
	}
	return delegatestore.OutcomeFailed
}

// settlementModeForRun is the model's independent copy of
// delegateSettlementModeForRun (classifyRunEnd's mode projection): terminal
// iff cancelRequested, budget-exhausted, or any error outside the ordinary
// set; budget exhaustion is tested before the ordinary sentinels.
func (m *delegateLifecycleModel) settlementModeForRun(err error, cancelRequested bool) delegateSettlementMode {
	if !cancelRequested {
		ordinary := !isDelegateLifecycleBudgetExhaustion(err) && (err == nil ||
			errors.Is(err, errBareTextWithoutResultTool) || errors.Is(err, errEmptyResponseExhausted))
		if ordinary {
			return delegateSettlementOrdinary
		}
	}
	return delegateSettlementTerminal
}

// requestStop models op 9 (stop request before finalization): the pending
// stop latches and the phase moves to stopping for an open run.
func (m *delegateLifecycleModel) requestStop() {
	if !m.runOpen {
		return
	}
	m.stopPending = true
	if m.phase != delegatestore.PhaseClosed {
		m.phase = delegatestore.PhaseStopping
	}
}

// completeStop models the stop completion (SubtreeStopCompleted): pending
// stop clears and the phase returns to idle for resumable delegates.
func (m *delegateLifecycleModel) completeStop() {
	m.stopPending = false
	if m.phase == delegatestore.PhaseStopping {
		m.phase = delegatestore.PhaseIdle
	}
}

// noActionEligible mirrors the full production eligibility chain in
// prepareNoAction: the packetless no-action finish is reachable only with
// the aggregate still in the running phase (a pending stop request moves the
// aggregate to stopping, which refuses the finish), evidence present,
// attention-only requirement, the recorded no-action outcome, no terminal
// seen, and a retained fallback.
func (m *delegateLifecycleModel) noActionEligible(fallbackRetained bool) bool {
	return m.phase == delegatestore.PhaseRunning && m.evidencePresent && !m.reportRequired &&
		m.attentionNoAction && !m.terminalSeen && fallbackRetained
}

// legalPhaseTransition reports whether from → to is a path in the model's
// transition table (I2). The table mirrors delegatestore's fold: idle →
// running (run started), running → settling (terminal prepared), running |
// settling | stopping → idle (run finished, resumable), any open phase →
// stopping (stop request), stopping → idle (stop completed), idle → closed
// (resumability closed), and closed is terminal.
func legalDelegateLifecyclePhaseTransition(from, to delegatestore.Phase) bool {
	switch from {
	case delegatestore.PhaseIdle:
		// A stop request sets Stopping for every phase except Closed
		// (applySubtreeStopRequested), so idle → stopping is legal too.
		return to == delegatestore.PhaseRunning || to == delegatestore.PhaseClosed || to == delegatestore.PhaseStopping
	case delegatestore.PhaseRunning:
		return to == delegatestore.PhaseSettling || to == delegatestore.PhaseStopping || to == delegatestore.PhaseIdle
	case delegatestore.PhaseSettling:
		return to == delegatestore.PhaseIdle || to == delegatestore.PhaseStopping
	case delegatestore.PhaseStopping:
		return to == delegatestore.PhaseIdle || to == delegatestore.PhaseClosed
	case delegatestore.PhaseClosed:
		return false
	default:
		return false
	}
}

// delegateLifecycleObservation is one observation of the real controller/runtime
// state, taken by the runner after each operation. The agreement checker
// compares it against the model.
type delegateLifecycleObservation struct {
	phase           delegatestore.Phase
	runOpen         bool
	stopPending     bool
	generation      uint64
	lastOutcome     delegatestore.OutcomeStatus
	lastDisposition delegatestore.RunDisposition
	// reportRequired and terminalSeen are read from the live binding evidence
	// when present; with nil evidence they are meaningless (legacy path).
	evidencePresent   bool
	reportRequired    bool
	terminalSeen      bool
	attentionNoAction bool
	// exhaustionBudgetPresent is true when the terminal outcome carries
	// exhaustion metadata (status exhausted implies payload present).
	exhausted bool
	// deliveries is the count of executed+acknowledged parent deliveries for
	// the current generation (I4).
	deliveries int
}

// agreeWithModel is the per-op agreement checker: the observation of the real
// controller must match the model's state. opName names the operation for
// failure messages. Legacy (nil-evidence) observations skip the
// evidence-dependent fields because production does not track them there.
func (m *delegateLifecycleModel) agreeWithModel(opName string, obs delegateLifecycleObservation) error {
	if obs.phase != m.phase {
		return &delegateLifecycleAgreementError{
			op:     opName,
			field:  "phase",
			model:  string(m.phase),
			actual: string(obs.phase),
		}
	}
	if obs.runOpen != m.runOpen {
		return &delegateLifecycleAgreementError{
			op:     opName,
			field:  "runOpen",
			model:  m.runOpen,
			actual: obs.runOpen,
		}
	}
	if obs.stopPending != m.stopPending {
		return &delegateLifecycleAgreementError{
			op:     opName,
			field:  "stopPending",
			model:  m.stopPending,
			actual: obs.stopPending,
		}
	}
	if obs.generation != m.generation {
		return &delegateLifecycleAgreementError{
			op:     opName,
			field:  "generation",
			model:  m.generation,
			actual: obs.generation,
		}
	}
	if m.evidencePresent && obs.evidencePresent {
		if obs.reportRequired != m.reportRequired {
			return &delegateLifecycleAgreementError{
				op:     opName,
				field:  "reportRequired",
				model:  m.reportRequired,
				actual: obs.reportRequired,
			}
		}
		if obs.terminalSeen != m.terminalSeen {
			return &delegateLifecycleAgreementError{
				op:     opName,
				field:  "terminalSeen",
				model:  m.terminalSeen,
				actual: obs.terminalSeen,
			}
		}
		if obs.attentionNoAction != m.attentionNoAction {
			return &delegateLifecycleAgreementError{
				op:     opName,
				field:  "attentionNoAction",
				model:  m.attentionNoAction,
				actual: obs.attentionNoAction,
			}
		}
	}
	if !m.runOpen {
		if obs.lastOutcome != m.lastOutcome {
			return &delegateLifecycleAgreementError{
				op:     opName,
				field:  "lastOutcome",
				model:  string(m.lastOutcome),
				actual: string(obs.lastOutcome),
			}
		}
		if obs.lastDisposition != m.lastDisposition {
			return &delegateLifecycleAgreementError{
				op:     opName,
				field:  "lastDisposition",
				model:  string(m.lastDisposition),
				actual: string(obs.lastDisposition),
			}
		}
		if obs.exhausted != m.exhausted {
			return &delegateLifecycleAgreementError{
				op:     opName,
				field:  "exhausted",
				model:  m.exhausted,
				actual: obs.exhausted,
			}
		}
		// A-M1: whenever the terminal outcome is exhausted, the payload
		// source must be asserted — run-error when the run error carried the
		// budget (the a.err overwrite), prepared-terminal when the outcome
		// was normalized from the prepared terminal packet. A tracked-but-
		// unasserted source is exactly the exhaustion-overwrite blind spot.
		if m.exhausted {
			if m.exhaustionSource != delegateLifecycleExhaustionFromRunError && m.exhaustionSource != delegateLifecycleExhaustionFromPreparedTerminal {
				return &delegateLifecycleAgreementError{
					op:     opName,
					field:  "exhaustionSource",
					model:  fmt.Sprintf("exhausted with source %v", m.exhaustionSource),
					actual: "none",
				}
			}
		} else if m.exhaustionSource != delegateLifecycleExhaustionNone {
			return &delegateLifecycleAgreementError{
				op:     opName,
				field:  "exhaustionSource",
				model:  fmt.Sprintf("not exhausted but source %v", m.exhaustionSource),
				actual: "none",
			}
		}
	}
	if obs.deliveries != m.delivered {
		return &delegateLifecycleAgreementError{
			op:     opName,
			field:  "delivered",
			model:  m.delivered,
			actual: obs.deliveries,
		}
	}
	return nil
}

// delegateLifecycleAgreementError is a differential failure: the model and the real
// controller disagree after an operation.
type delegateLifecycleAgreementError struct {
	op     string
	field  string
	model  any
	actual any
}

func (e *delegateLifecycleAgreementError) Error() string {
	return "lifecycle differential: " + e.op + ": " + e.field + ": model=" + formatDelegateLifecycleValue(e.model) + " actual=" + formatDelegateLifecycleValue(e.actual)
}

func formatDelegateLifecycleValue(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case bool:
		if value {
			return "true"
		}
		return "false"
	case uint64:
		return strconv.FormatUint(value, 10)
	case int:
		return strconv.Itoa(value)
	default:
		return "?"
	}
}
