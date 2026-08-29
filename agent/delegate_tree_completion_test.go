package agent

import (
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

func TestDelegateGenerationEvidenceInitialRequirement(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_attention", "")
	attentionLease := startDelegateAttentionEvidenceGeneration(t, c, "dlg_attention")
	attention, err := c.completionSnapshot(attentionLease)
	if err != nil {
		t.Fatalf("attention completionSnapshot: %v", err)
	}
	if attention.requirement != delegateCompletionAttentionOnly || attention.outcome != delegateCompletionOutcomeNone || attention.terminalSeen {
		t.Fatalf("attention evidence = %#v, want attention-only, empty, terminal-unseen", attention)
	}

	seedDelegateControllerIdle(t, c, "dlg_owner", "")
	ownerLease, _ := startDelegateDeliveryGeneration(t, c, "dlg_owner", false)
	owner, err := c.completionSnapshot(ownerLease)
	if err != nil {
		t.Fatalf("owner completionSnapshot: %v", err)
	}
	if owner.requirement != delegateCompletionReportRequired || owner.outcome != delegateCompletionOutcomeNone || owner.terminalSeen {
		t.Fatalf("owner evidence = %#v, want report-required, empty, terminal-unseen", owner)
	}
}

func TestDelegateGenerationEvidenceRejectsStaleLease(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	first, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	finishDelegateDeliveryGeneration(t, c, first, "first")
	second, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	before, err := c.completionSnapshot(second)
	if err != nil {
		t.Fatalf("generation 2 completionSnapshot: %v", err)
	}

	if err := c.escalateCompletionRequirement(first); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("stale escalation error = %v, want stale lease", err)
	}
	if _, err := c.recordAttentionNoAction(first); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("stale no-action error = %v, want stale lease", err)
	}
	if err := c.recordTerminalSeen(first); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("stale terminal error = %v, want stale lease", err)
	}
	if _, err := c.completionSnapshot(first); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("stale snapshot error = %v, want stale lease", err)
	}

	after, err := c.completionSnapshot(second)
	if err != nil {
		t.Fatalf("generation 2 completionSnapshot after stale mutations: %v", err)
	}
	if after != before {
		t.Fatalf("generation 2 evidence changed after stale mutations: before=%#v after=%#v", before, after)
	}
}

func TestDelegateGenerationEvidenceEscalationIsMonotonic(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease := startDelegateAttentionEvidenceGeneration(t, c, "dlg_target")

	if err := c.escalateCompletionRequirement(lease); err != nil {
		t.Fatalf("first escalation: %v", err)
	}
	if err := c.escalateCompletionRequirement(lease); err != nil {
		t.Fatalf("second escalation: %v", err)
	}
	if recorded, err := c.recordAttentionNoAction(lease); err != nil || recorded {
		t.Fatalf("no-action after escalation = recorded %t err %v, want refusal", recorded, err)
	}
	snapshot, err := c.completionSnapshot(lease)
	if err != nil {
		t.Fatalf("completionSnapshot: %v", err)
	}
	if snapshot.requirement != delegateCompletionReportRequired || snapshot.outcome != delegateCompletionOutcomeNone || snapshot.terminalSeen {
		t.Fatalf("escalated evidence = %#v, want report-required with no outcome", snapshot)
	}
}

func TestDelegateGenerationEvidenceClearsOnRelease(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	if err := c.recordTerminalSeen(lease); err != nil {
		t.Fatalf("record terminal: %v", err)
	}
	if _, err := c.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeCompleted}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	if _, err := c.completionSnapshot(lease); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("released snapshot error = %v, want stale lease", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if live := c.live[lease.delegateID]; live != nil && live.binding != nil {
		t.Fatalf("released generation retained binding: %#v", live.binding)
	}
}

func TestDelegateGenerationEvidenceSnapshotDeepClonesFallback(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	resumable := true
	valid := true
	c.mu.Lock()
	c.live[lease.delegateID].binding.evidence.fallback = &delegateFinish{
		outcome:             delegatestore.OutcomeExhausted,
		disposition:         delegatestore.DispositionReported,
		reason:              "original reason",
		packet:              &delegatestore.TerminalPacket{Kind: delegatestore.PacketReported, Message: json.RawMessage(`"original message"`), StructuredResult: json.RawMessage(`{"original":true}`), StructuredResultValid: &valid, Warnings: []string{"original warning"}, Metadata: json.RawMessage(`{"original":true}`)},
		exhaustionBudget:    delegatestore.ExhaustionBudgetTurns,
		exhaustionLimit:     3,
		exhaustionResumable: &resumable,
	}
	c.mu.Unlock()

	snapshot, err := c.completionSnapshot(lease)
	if err != nil {
		t.Fatalf("first completionSnapshot: %v", err)
	}
	snapshot.fallback.reason = "mutated reason"
	*snapshot.fallback.exhaustionResumable = false
	*snapshot.fallback.packet.StructuredResultValid = false
	snapshot.fallback.packet.Message[0] = 'X'
	snapshot.fallback.packet.StructuredResult[0] = 'X'
	snapshot.fallback.packet.Warnings[0] = "mutated warning"
	snapshot.fallback.packet.Metadata[0] = 'X'

	second, err := c.completionSnapshot(lease)
	if err != nil {
		t.Fatalf("second completionSnapshot: %v", err)
	}
	if second.fallback == nil || second.fallback.reason != "original reason" || second.fallback.exhaustionResumable == nil || !*second.fallback.exhaustionResumable {
		t.Fatalf("controller fallback changed through snapshot: %#v", second.fallback)
	}
	if second.fallback.packet == nil || string(second.fallback.packet.Message) != `"original message"` || string(second.fallback.packet.StructuredResult) != `{"original":true}` || second.fallback.packet.StructuredResultValid == nil || !*second.fallback.packet.StructuredResultValid || second.fallback.packet.Warnings[0] != "original warning" || string(second.fallback.packet.Metadata) != `{"original":true}` {
		t.Fatalf("controller fallback packet changed through snapshot: %#v", second.fallback.packet)
	}
}

func startDelegateAttentionEvidenceGeneration(t *testing.T, c *delegateTreeController, delegateID string) delegateLease {
	t.Helper()
	runtime := &Session{id: "child-" + delegateID, stateDir: c.stateDir, delegateController: c}
	c.mu.Lock()
	if _, err := c.appendLocked(delegatestore.Event{
		Kind:       delegatestore.EventDelegateAttentionChanged,
		DelegateID: delegateID,
		AttentionChanged: &delegatestore.DelegateAttentionChanged{
			NeedsAttention: true,
		},
	}); err != nil {
		c.mu.Unlock()
		t.Fatalf("append attention projection: %v", err)
	}
	c.live[delegateID] = &delegateLiveState{runtime: runtime}
	c.noteDelegateAttentionLocked(delegateID, "attention-"+delegateID)
	c.mu.Unlock()

	reservation, err := c.ReserveAttention(runtime, "attention-"+delegateID)
	if err != nil {
		t.Fatalf("ReserveAttention: %v", err)
	}
	if err := c.prepareAttentionStart(reservation, runtime, nil); err != nil {
		t.Fatalf("prepareAttentionStart: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart attention: %v", err)
	}
	return started.lease
}
