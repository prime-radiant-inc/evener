package hub

import (
	"cmp"
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// attemptTransition is one stored attempt record with its instants reduced to
// presence, so two runs on different clocks compare for shape.
type attemptTransition struct {
	State         string
	HasStartedAt  bool
	HasTimedOutAt bool
	HasFencedAt   bool
	Boot          string
}

// resolutionOutcome is what a retry or a recover leaves behind once the
// remnant clears, minus the fields that name which of the two ran.
type resolutionOutcome struct {
	attempts         []attemptTransition
	resolvedHostKind string
	resolvedHasKey   bool
	receiptResolved  bool
	remnantOpen      bool
}

func settleRemnantWithPriorTimedOutAttempt(t *testing.T, resolve func(m *hubHostManager, remnantID string)) resolutionOutcome {
	t.Helper()
	m, hosts, _ := newRemnantFixture(t)
	hosts.refuseRemove.Store(true)
	result, err := m.RemoveResult(context.Background(), removeRequest(t, m, "side"))
	if err != nil {
		t.Fatalf("Remove = %v", err)
	}
	arm := result.HostMutationTeardownFailureRemoved
	if arm == nil {
		t.Fatalf("Remove = %+v, want the failure arm", result)
	}
	// A timed-out retry leaves an open attempt the next claim must fence.
	m.cfg.policy.teardownTimeout = time.Nanosecond
	if _, err := m.TeardownRetry(context.Background(), appwire.HostTeardownRetryParams{RemnantID: arm.RemnantID}); err != nil {
		t.Fatalf("timed-out retry = %v", err)
	}
	m.cfg.policy.teardownTimeout = time.Minute
	hosts.refuseRemove.Store(false)

	resolve(m, arm.RemnantID)

	var out resolutionOutcome
	for _, attempt := range m.cfg.store.attemptsSnapshot() {
		out.attempts = append(out.attempts, attemptTransition{
			State:         attempt.State,
			HasStartedAt:  attempt.StartedAt != "",
			HasTimedOutAt: attempt.TimedOutAt != "",
			HasFencedAt:   attempt.FencedAt != "",
			Boot:          attempt.FencingEpochBoot,
		})
	}
	slices.SortFunc(out.attempts, func(a, b attemptTransition) int {
		return cmp.Compare(a.State+a.Boot, b.State+b.Boot)
	})
	remnant, _ := m.cfg.store.remnantByID(arm.RemnantID)
	out.remnantOpen = remnant.open()
	if remnant.Resolved != nil {
		out.resolvedHostKind = remnant.Resolved.HostKind
		out.resolvedHasKey = remnant.Resolved.MutationKey != ""
	}
	for _, receipt := range m.cfg.store.receiptsSnapshot() {
		if receipt.RemnantResolvedAt != "" {
			out.receiptResolved = true
		}
	}
	return out
}

// TestTeardownRetryAndRecoverCloseTheirAttemptsAlike pins that the two ways to
// clear a remnant claim and finalize an attempt identically: the prior
// timed-out attempt is fenced closed, the new attempt ends fenced closed, the
// resolved record and the original receipt are written, and the remnant closes.
func TestTeardownRetryAndRecoverCloseTheirAttemptsAlike(t *testing.T) {
	retried := settleRemnantWithPriorTimedOutAttempt(t, func(m *hubHostManager, remnantID string) {
		if _, err := m.TeardownRetry(context.Background(), appwire.HostTeardownRetryParams{RemnantID: remnantID}); err != nil {
			t.Fatalf("retry = %v", err)
		}
	})
	recovered := settleRemnantWithPriorTimedOutAttempt(t, func(m *hubHostManager, remnantID string) {
		// Recover clears only a name whose pinned handle is gone from the registry.
		if err := m.cfg.hosts.Remove("side"); err != nil {
			t.Fatalf("remove pinned entry: %v", err)
		}
		if _, err := m.TeardownRecover(context.Background(), appwire.HostTeardownRecoverParams{
			RemnantID: remnantID,
			Attestation: appwire.HostTeardownAttestation{
				Operator: "op", Statement: hostRecoveryStatement, ObservedAt: "2026-09-27T12:00:00Z",
			},
		}); err != nil {
			t.Fatalf("recover = %v", err)
		}
	})
	if len(retried.attempts) != 2 {
		t.Fatalf("attempts = %+v, want the fenced prior attempt and the closed new one", retried.attempts)
	}
	if retried.remnantOpen || !retried.receiptResolved || !retried.resolvedHasKey {
		t.Fatalf("retry outcome = %+v, want the remnant closed and the receipt resolved", retried)
	}
	if !reflect.DeepEqual(retried, recovered) {
		t.Fatalf("retry outcome %+v differs from recover outcome %+v", retried, recovered)
	}
}
