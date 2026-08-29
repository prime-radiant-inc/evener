package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestClassifyRunEnd pins the shared run-end taxonomy table: for every input
// class in the matrix (nil, canceled, wrapped canceled, bare-text,
// empty-response, each budget type, generic error, joined combos) and both
// cancelRequested values, the settlement-mode, fatality, status, and
// exhaustion-payload projections must match the pre-refactor per-site logic:
//
//   - delegateSettlementModeForRun: terminal iff cancelRequested,
//     budget-exhausted, or any error outside {nil, bare-text,
//     empty-response}. Budget exhaustion is tested BEFORE the
//     bare-text/empty-response sentinels, so Join(bareText, exhaustion)
//     settles terminally.
//   - stableDelegateFatalRun: fatal iff err non-nil AND NOT (canceled,
//     bare-text, empty-response, budget-exhausted). cancelRequested is not an
//     input — a cancellation without ctx.Canceled is NOT fatal.
//   - (*subagent).run final-status switch: Cancelled iff cancelRequested AND
//     errors.Is(err, context.Canceled); else Exhausted iff budget-exhausted
//     (with the exhaustion payload exposed for the a.err overwrite); else
//     Failed iff err non-nil; else Completed.
func TestClassifyRunEnd(t *testing.T) {
	turnsExhausted := &budgetExhaustionError{Budget: exhaustedBudgetTurns, Limit: 23, Resumable: false}
	toolRoundsExhausted := &budgetExhaustionError{Budget: exhaustedBudgetToolRounds, Limit: 17, Resumable: true}
	wrappedCanceled := fmt.Errorf("provider stream: %w", context.Canceled)
	wrappedBareText := fmt.Errorf("round ended: %w", errBareTextWithoutResultTool)
	joinedExhaustionAndCanceled := errors.Join(toolRoundsExhausted, context.Canceled)
	joinedGenericAndCanceled := errors.Join(errors.New("boom"), context.Canceled)
	joinedGenericAndBareText := errors.Join(errors.New("boom"), errBareTextWithoutResultTool)
	joinedBareTextAndExhaustion := errors.Join(errBareTextWithoutResultTool, turnsExhausted)

	cases := []struct {
		name       string
		err        error
		cancel     bool
		mode       delegateSettlementMode
		fatal      bool
		status     SubagentStatus
		exhaustion *budgetExhaustionError
	}{
		// --- cancelRequested = false ---
		{name: "nil", err: nil, cancel: false, mode: delegateSettlementOrdinary, fatal: false, status: SubagentCompleted},
		{name: "canceled", err: context.Canceled, cancel: false, mode: delegateSettlementTerminal, fatal: false, status: SubagentFailed},
		{name: "wrapped canceled", err: wrappedCanceled, cancel: false, mode: delegateSettlementTerminal, fatal: false, status: SubagentFailed},
		{name: "bare text", err: errBareTextWithoutResultTool, cancel: false, mode: delegateSettlementOrdinary, fatal: false, status: SubagentFailed},
		{name: "wrapped bare text", err: wrappedBareText, cancel: false, mode: delegateSettlementOrdinary, fatal: false, status: SubagentFailed},
		{name: "empty response", err: errEmptyResponseExhausted, cancel: false, mode: delegateSettlementOrdinary, fatal: false, status: SubagentFailed},
		{name: "turns exhaustion", err: turnsExhausted, cancel: false, mode: delegateSettlementTerminal, fatal: false, status: SubagentExhausted, exhaustion: turnsExhausted},
		{name: "tool-rounds exhaustion", err: toolRoundsExhausted, cancel: false, mode: delegateSettlementTerminal, fatal: false, status: SubagentExhausted, exhaustion: toolRoundsExhausted},
		{name: "generic error", err: errors.New("boom"), cancel: false, mode: delegateSettlementTerminal, fatal: true, status: SubagentFailed},
		{name: "joined exhaustion+canceled", err: joinedExhaustionAndCanceled, cancel: false, mode: delegateSettlementTerminal, fatal: false, status: SubagentExhausted, exhaustion: toolRoundsExhausted},
		{name: "joined generic+canceled", err: joinedGenericAndCanceled, cancel: false, mode: delegateSettlementTerminal, fatal: false, status: SubagentFailed},
		{name: "joined generic+bare-text", err: joinedGenericAndBareText, cancel: false, mode: delegateSettlementOrdinary, fatal: false, status: SubagentFailed},
		// Exhaustion precedence over the bare-text sentinel in settlement mode.
		{name: "joined bare-text+exhaustion", err: joinedBareTextAndExhaustion, cancel: false, mode: delegateSettlementTerminal, fatal: false, status: SubagentExhausted, exhaustion: turnsExhausted},

		// --- cancelRequested = true ---
		// Mode is terminal regardless of err; status Cancelled additionally
		// requires errors.Is(err, context.Canceled). Fatality never depends
		// on cancelRequested (a cancel without ctx.Canceled is not fatal).
		{name: "cancel with nil err", err: nil, cancel: true, mode: delegateSettlementTerminal, fatal: false, status: SubagentCompleted},
		{name: "cancel with canceled err", err: context.Canceled, cancel: true, mode: delegateSettlementTerminal, fatal: false, status: SubagentCancelled},
		{name: "cancel with wrapped canceled err", err: wrappedCanceled, cancel: true, mode: delegateSettlementTerminal, fatal: false, status: SubagentCancelled},
		{name: "cancel with bare text", err: errBareTextWithoutResultTool, cancel: true, mode: delegateSettlementTerminal, fatal: false, status: SubagentFailed},
		{name: "cancel with empty response", err: errEmptyResponseExhausted, cancel: true, mode: delegateSettlementTerminal, fatal: false, status: SubagentFailed},
		// Budget exhaustion wins the status projection over the cancel
		// branch ONLY when the error is not ctx.Canceled (the original
		// switch ordered Cancelled first). A cancelled run carries NO
		// exhaustion payload — the caller's a.err overwrite keys on payload
		// presence, so the joined error must be kept verbatim.
		{name: "cancel with turns exhaustion", err: turnsExhausted, cancel: true, mode: delegateSettlementTerminal, fatal: false, status: SubagentExhausted, exhaustion: turnsExhausted},
		{name: "cancel with tool-rounds exhaustion", err: toolRoundsExhausted, cancel: true, mode: delegateSettlementTerminal, fatal: false, status: SubagentExhausted, exhaustion: toolRoundsExhausted},
		{name: "cancel with generic err", err: errors.New("boom"), cancel: true, mode: delegateSettlementTerminal, fatal: true, status: SubagentFailed},
		{name: "cancel with joined exhaustion+canceled", err: joinedExhaustionAndCanceled, cancel: true, mode: delegateSettlementTerminal, fatal: false, status: SubagentCancelled},
		{name: "cancel with joined generic+canceled", err: joinedGenericAndCanceled, cancel: true, mode: delegateSettlementTerminal, fatal: false, status: SubagentCancelled},
		{name: "cancel with joined generic+bare-text", err: joinedGenericAndBareText, cancel: true, mode: delegateSettlementTerminal, fatal: false, status: SubagentFailed},
		{name: "cancel with joined bare-text+exhaustion", err: joinedBareTextAndExhaustion, cancel: true, mode: delegateSettlementTerminal, fatal: false, status: SubagentExhausted, exhaustion: turnsExhausted},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cls := classifyRunEnd(tc.err, tc.cancel)
			if cls.mode != tc.mode {
				t.Errorf("mode = %v, want %v", cls.mode, tc.mode)
			}
			if cls.fatal != tc.fatal {
				t.Errorf("fatal = %v, want %v", cls.fatal, tc.fatal)
			}
			if cls.status != tc.status {
				t.Errorf("status = %q, want %q", cls.status, tc.status)
			}
			if cls.exhaustion != tc.exhaustion {
				t.Errorf("exhaustion = %v, want %v", cls.exhaustion, tc.exhaustion)
			}
		})
	}
}

// TestClassifyRunEndProjectionsMatchLegacySites cross-checks the classifier
// against the original per-site logic over an expanded join matrix, guarding
// the behavior-preserving property directly rather than only the pinned
// table above.
func TestClassifyRunEndProjectionsMatchLegacySites(t *testing.T) {
	baseErrs := []error{
		nil,
		context.Canceled,
		errBareTextWithoutResultTool,
		errEmptyResponseExhausted,
		errors.New("generic"),
		&budgetExhaustionError{Budget: exhaustedBudgetTurns, Limit: 23, Resumable: false},
		&budgetExhaustionError{Budget: exhaustedBudgetToolRounds, Limit: 17, Resumable: true},
	}
	extras := []error{nil, context.Canceled, errBareTextWithoutResultTool, errors.New("other")}
	for _, base := range baseErrs {
		for _, extra := range extras {
			joined := base
			if extra != nil {
				if base == nil {
					joined = extra
				} else {
					joined = errors.Join(base, extra)
				}
			}
			// Loop-invariant precomputations for both cancelRequested values.
			_, exhausted := budgetExhaustionFromError(joined)
			wantFatal := joined != nil &&
				!errors.Is(joined, context.Canceled) &&
				!errors.Is(joined, errBareTextWithoutResultTool) &&
				!errors.Is(joined, errEmptyResponseExhausted) &&
				!exhausted
			exhaustion, budgetExhausted := budgetExhaustionFromError(joined)
			for _, cancelRequested := range []bool{false, true} {
				cls := classifyRunEnd(joined, cancelRequested)

				// Site 1: delegateSettlementModeForRun (subagents.go).
				wantMode := delegateSettlementTerminal
				if !cancelRequested {
					_, exhausted := budgetExhaustionFromError(joined)
					if !exhausted && (joined == nil ||
						errors.Is(joined, errBareTextWithoutResultTool) || errors.Is(joined, errEmptyResponseExhausted)) {
						wantMode = delegateSettlementOrdinary
					}
				}
				if cls.mode != wantMode {
					t.Errorf("err=%v cancel=%t: mode=%v, want %v", joined, cancelRequested, cls.mode, wantMode)
				}

				// Site 2: stableDelegateFatalRun (subagents.go).
				if cls.fatal != wantFatal {
					t.Errorf("err=%v cancel=%t: fatal=%v, want %v", joined, cancelRequested, cls.fatal, wantFatal)
				}

				// Site 3: the final-status switch in (*subagent).run.
				var wantStatus SubagentStatus
				switch {
				case cancelRequested && errors.Is(joined, context.Canceled):
					wantStatus = SubagentCancelled
				case budgetExhausted:
					wantStatus = SubagentExhausted
				case joined != nil:
					wantStatus = SubagentFailed
				default:
					wantStatus = SubagentCompleted
				}
				if cls.status != wantStatus {
					t.Errorf("err=%v cancel=%t: status=%q, want %q", joined, cancelRequested, cls.status, wantStatus)
				}
				wantExhaustion := exhaustion
				if wantStatus != SubagentExhausted {
					wantExhaustion = nil
				}
				if cls.exhaustion != wantExhaustion {
					t.Errorf("err=%v cancel=%t: exhaustion payload = %v, want %v", joined, cancelRequested, cls.exhaustion, wantExhaustion)
				}
			}
		}
	}
}
