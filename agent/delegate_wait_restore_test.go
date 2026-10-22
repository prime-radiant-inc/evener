package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestOpenWaitTargetSurvivesRestoreWhenChildIsLive(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	writer, err := transcript.NewWriter(transcriptPath(c.stateDir, "child-session"), transcript.Header{SessionID: "child-session"})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	turn := schema.NewTurn(schema.TurnCommunicate, llm.Assistant("waiting"))
	turn.Communicate = &schema.CommunicateInfo{EndTurn: true, EndReason: "waiting_on_work", WaitingOn: "dlg_child"}
	if err := writer.AppendDurable(turn); err != nil {
		_ = writer.Close()
		t.Fatalf("AppendDurable: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	c.durable["dlg_child"] = &delegatestore.Aggregate{
		DelegateID:     "dlg_child",
		Phase:          delegatestore.PhaseRunning,
		CurrentRunOpen: true,
	}
	aggregate := &delegatestore.Aggregate{
		DelegateID: "dlg_waiter",
		Descriptor: delegatestore.Descriptor{
			ChildSessionID: "child-session",
			TranscriptRef:  "local:child-session",
		},
	}
	target, live := c.openWaitTargetLocked(aggregate)
	if !live || target != "dlg_child" {
		t.Fatalf("openWaitTargetLocked = %q, %v; want dlg_child, true", target, live)
	}
}
