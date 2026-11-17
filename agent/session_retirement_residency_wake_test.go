package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// TestResidencyReturnWakesRunnableQueuedInput pins the gate that can strand
// accepted work: admittance.
//
// beginRetirementMutation refuses every lease while the controller's phase is
// not resident (RetirementController.beginMutation), so the serve loop's
// queued-input probe can find runnable durable input and still have its claim
// refused -- and it is refused for the one window a claim spends reading
// evidence, which is exactly when that input is the evidence. The claim then
// returns the controller to resident, and that return is the transition that
// makes the input claimable again. Nothing observes it on the input path:
// Changed notifies only Run, and the client already holds its Applied receipt,
// so no later accept arrives to send a wake.
//
// The test drives the real refusal and then relays only the wake the session
// itself asks for. It supplies none of its own: with the wake missing, nothing
// arrives and the message stays durably accepted and unrun.
func TestResidencyReturnWakesRunnableQueuedInput(t *testing.T) {
	t.Parallel()
	// The run the wake is supposed to cause answers the way a real turn ends.
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			args, err := json.Marshal(map[string]any{
				"end_turn": true,
				"message":  "the queued message ran",
				"output":   map[string]any{"message": "", "data": map[string]any{}, "artifacts": []string{}},
			})
			if err != nil {
				t.Fatalf("marshal communicate arguments: %v", err)
			}
			return agenttest.ToolCallResponse(llm.ToolCallData{ID: "communicate-residency", Name: "communicate", Arguments: args, Type: "function"})
		},
	}}
	root, c, _ := newSettledRetirementRootWithAdapter(t, adapter)
	woke := make(chan struct{}, 1)
	root.SetPendingUserInputWakeFunc(func() {
		select {
		case woke <- struct{}{}:
		default:
		}
	})
	if _, err := root.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "residency-return-queue",
		Input:            clientMutationInput("run me when residency returns", nil, nil),
	}); err != nil {
		t.Fatalf("AcceptClientMutationQueue: %v", err)
	}
	if !root.hasPendingUserInputToRun() {
		t.Fatal("the probe reports nothing runnable, so the refusal below would prove nothing")
	}
	drainResidencyWakes(woke) // the acceptance wake; the transition under test owes its own

	// The retire refuses over this input's own evidence and returns the
	// controller to resident.
	claim, snap, err := c.TryManualClaim(context.Background())
	if claim != nil || err != nil {
		t.Fatalf("TryManualClaim = claim %v, err %v; want the refusal this input owes", claim, err)
	}
	if snap.Phase != "resident" {
		t.Fatalf("phase after the refusal = %q, want resident", snap.Phase)
	}
	if !root.hasPendingUserInputToRun() {
		t.Fatal("the refused claim consumed the work; the wake below would prove nothing")
	}

	select {
	case <-woke:
	case <-time.After(5 * time.Second):
		t.Fatal("no wake followed the return to resident: the queued input is durably accepted, runnable, and nothing will look again")
	}

	// The wake is what the serve loop answers. Relaying it here is the loop's
	// own response, not a wake the test invented.
	if _, ran, err := root.ProcessPendingUserInput(context.Background(), nil); err != nil || !ran {
		t.Fatalf("ProcessPendingUserInput after the wake = ran %v, err %v; want the queued message to run", ran, err)
	}
}

// drainResidencyWakes empties the relay so the assertion above observes only
// the wake the transition under test owes.
func drainResidencyWakes(woke chan struct{}) {
	for {
		select {
		case <-woke:
		default:
			return
		}
	}
}

// newSettledRetirementRootWithAdapter is newSettledRetirementRoot with the
// model scripted, for a test whose wake must end in a real turn.
func newSettledRetirementRootWithAdapter(t *testing.T, adapter *fakeAdapter) (*Session, *RetirementController, time.Time) {
	t.Helper()
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(adapter)
	root, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir, testOnly: sharingHostProbe(testConfig{})})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(root.Close)
	if err := root.ensureClientMutationStore(); err != nil {
		t.Fatal(err)
	}
	c, err := NewRetirementController(time.Hour, agenttest.NewFakeClock())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	return root, c, settleRetirement(c)
}
