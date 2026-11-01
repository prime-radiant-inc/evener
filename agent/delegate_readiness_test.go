package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
)

type readinessDeliveryReceiver struct {
	appended []string
}

func (r *readinessDeliveryReceiver) appendDelegateNotificationDurably(attentionID, content string) (bool, error) {
	r.appended = append(r.appended, content)
	return true, nil
}

func TestDelegateReadinessUpdateReachesParent(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	actor := delegateActor{lease: &delegateLease{delegateID: "dlg_target", generation: 1}}

	_, plans, err := c.PostUpdate(actor, schema.CommunicateInfo{Message: "ready for review"}, publicationPacket("ready for review"), tool.DelegateReadinessReady)
	if err != nil {
		t.Fatalf("PostUpdate: %v", err)
	}
	if len(plans.deliveries) != 1 {
		t.Fatalf("delivery plans = %#v, want one head plan", plans.deliveries)
	}
	plan := plans.deliveries[0]
	if plan.readiness != tool.DelegateReadinessReady {
		t.Fatalf("plan readiness = %q, want %q", plan.readiness, tool.DelegateReadinessReady)
	}
	if !strings.Contains(string(plan.packet.Metadata), `"readiness":"ready"`) {
		t.Fatalf("update packet metadata = %s, want readiness ready", plan.packet.Metadata)
	}
	receiver := &readinessDeliveryReceiver{}
	if _, err := deliverDelegatePacket(plan, receiver); err != nil {
		t.Fatalf("deliverDelegatePacket: %v", err)
	}
	if len(receiver.appended) != 1 || !strings.Contains(receiver.appended[0], `readiness="ready"`) {
		t.Fatalf("delivered update = %#v, want one readiness frame", receiver.appended)
	}
}

func TestDelegateReadinessResolvesInlineWaiter(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	if err := attachRuntimeWithHandoffForTest(c, lease, &Session{}); err != nil {
		t.Fatalf("AttachRuntime: %v", err)
	}
	if _, err := c.AdmitStartInput(lease, func() error { return nil }); err != nil {
		t.Fatalf("AdmitStartInput: %v", err)
	}

	_, plans, err := c.PostUpdate(delegateActor{lease: &lease}, schema.CommunicateInfo{Message: "ready"}, publicationPacket("ready"), tool.DelegateReadinessReady)
	if err != nil {
		t.Fatalf("PostUpdate: %v", err)
	}
	if len(plans.deliveries) != 1 {
		t.Fatalf("delivery plans = %#v, want one head plan", plans.deliveries)
	}
	if _, err := deliverDelegatePacket(plans.deliveries[0], &readinessDeliveryReceiver{}); err != nil {
		t.Fatalf("deliverDelegatePacket: %v", err)
	}
	resolution := c.waitForDelegateInline(context.Background(), waiter)
	if resolution.readiness != tool.DelegateReadinessReady {
		t.Fatalf("resolution readiness = %q, want %q", resolution.readiness, tool.DelegateReadinessReady)
	}
	if resolution.packet != nil || resolution.commit != nil {
		t.Fatalf("resolution = %#v, want no terminal packet or commit", resolution)
	}
}

func TestDelegateReadinessResolvesBehindAHeldUpdate(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	if err := attachRuntimeWithHandoffForTest(c, lease, &Session{}); err != nil {
		t.Fatalf("AttachRuntime: %v", err)
	}
	if _, err := c.AdmitStartInput(lease, func() error { return nil }); err != nil {
		t.Fatalf("AdmitStartInput: %v", err)
	}
	actor := delegateActor{lease: &lease}
	if _, _, err := c.PostUpdate(actor, schema.CommunicateInfo{Message: "first update"}, publicationPacket("first update"), ""); err != nil {
		t.Fatalf("PostUpdate first: %v", err)
	}
	_, plans, err := c.PostUpdate(actor, schema.CommunicateInfo{Message: "ready"}, publicationPacket("ready"), tool.DelegateReadinessReady)
	if err != nil {
		t.Fatalf("PostUpdate ready: %v", err)
	}
	if len(plans.deliveries) != 0 {
		t.Fatalf("ready update planned while an earlier update was held: %#v", plans.deliveries)
	}
	resolution := c.waitForDelegateInline(context.Background(), waiter)
	if resolution.readiness != tool.DelegateReadinessReady {
		t.Fatalf("resolution readiness = %q, want %q", resolution.readiness, tool.DelegateReadinessReady)
	}
}

func TestDelegateReadinessDoesNotSupersedeTerminalDelivery(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	actor := delegateActor{lease: &delegateLease{delegateID: "dlg_target", generation: 1}}

	_, updates, err := c.PostUpdate(actor, schema.CommunicateInfo{Message: "ready"}, publicationPacket("ready"), tool.DelegateReadinessReady)
	if err != nil {
		t.Fatalf("PostUpdate: %v", err)
	}
	if _, err := deliverDelegatePacket(updates.deliveries[0], &readinessDeliveryReceiver{}); err != nil {
		t.Fatalf("deliver update: %v", err)
	}

	packet := delegatestore.TerminalPacket{Kind: delegatestore.PacketReported, Message: []byte(`"finished"`)}
	claim, _, err := c.BeginSettlement(*actor.lease)
	if err != nil {
		t.Fatalf("BeginSettlement: %v", err)
	}
	plans, err := c.CompleteSettlement(claim, &packet)
	if err != nil {
		t.Fatalf("CompleteSettlement: %v", err)
	}
	if len(plans.updates) == 0 {
		t.Fatalf("CompleteSettlement returned no preparation plans: %#v", plans)
	}
	plans, err = c.FinishGeneration(*actor.lease, delegateFinish{
		outcome:     delegatestore.OutcomeCompleted,
		disposition: delegatestore.DispositionReported,
		packet:      &packet,
	})
	if err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	if len(plans.deliveries) != 1 {
		t.Fatalf("terminal delivery plans = %#v, want one", plans.deliveries)
	}
	if plans.deliveries[0].waiter != nil {
		t.Fatal("terminal delivery claims the waiter the readiness update already resolved")
	}
	if plans.deliveries[0].packet.Kind != delegatestore.PacketReported {
		t.Fatalf("terminal packet kind = %s, want reported", plans.deliveries[0].packet.Kind)
	}
}
