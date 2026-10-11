package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// TestRetirementParkedRootAttentionDoesNotBlock pins that root delegate
// attention parked behind a Stop does not hold the daemon resident. Nothing in
// the process can deliver it: the rail waits for the user's re-engagement, and
// both the attention (transcript fold) and the park (the durable queue hold)
// survive retirement. So the daemon retires, the restored session comes back
// parked with the same attention pending and no wake, and the user's next
// message unparks it and arms the delivery.
func TestRetirementParkedRootAttentionDoesNotBlock(t *testing.T) {
	root, _, notifies, _ := newAttentionLivelockSession(t, nil)
	// An accepted Stop of a running turn writes the durable queue hold and
	// parks the rail; with no queued message left behind, this is the whole
	// state it leaves.
	if err := root.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.QueueHeld = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	root.parkRootDelegateAttention()
	armOneRootAttention(t, root, "dlg_parked", "delegate:dlg_parked/delivery/1")
	if wake, _, pending := attentionRailState(root); wake || pending != 1 || !root.rootAttentionRailParked() {
		t.Fatalf("fixture: wake=%t pending=%d parked=%t, want parked attention (false, 1, true)", wake, pending, root.rootAttentionRailParked())
	}
	baseline := notifies.Load()

	c := retirementEvidenceController(t, root)
	claim, state, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("parked attention held the daemon resident: %+v %v", state, err)
	}
	prepared, err := c.Prepare(context.Background(), claim)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := c.Commit(claim); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := c.DrainReaders(context.Background()); err != nil {
		t.Fatalf("DrainReaders: %v", err)
	}
	if err := root.ReleaseForRetirement(context.Background(), prepared); err != nil {
		t.Fatalf("ReleaseForRetirement: %v", err)
	}
	if got := notifies.Load(); got != baseline {
		t.Fatalf("retirement woke the stopped session %d time(s)", got-baseline)
	}

	meta, err := schema.LoadSessionMeta(root.stateDir, root.id)
	if err != nil {
		t.Fatalf("load meta: %v", err)
	}
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	restored, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{StateDir: root.stateDir})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	t.Cleanup(func() { restored.Close() })
	if wake, _, pending := attentionRailState(restored); wake || pending != 1 || !restored.rootAttentionRailParked() {
		t.Fatalf("restored: wake=%t pending=%d parked=%t, want the attention still parked (false, 1, true)", wake, pending, restored.rootAttentionRailParked())
	}

	// The user's next message is the re-engagement that releases the park,
	// and the deferred attention arms its delivery.
	if _, err := restored.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-after-retirement",
		Input:            []appwire.InputItem{{Type: "text", Text: "the user comes back"}},
	}); err != nil {
		t.Fatalf("turn/start after retirement: %v", err)
	}
	if wake, _, pending := attentionRailState(restored); !wake || pending != 1 {
		t.Fatalf("after re-engagement: wake=%t pending=%d, want the parked attention armed (true, 1)", wake, pending)
	}
}
