package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// residencyWakeRig is one session under a retirement controller, with both
// input wakes recorded. The wakes fire synchronously inside the controller
// call that reopens admission, so a test reads them with no wait at all:
// their presence or absence is the assertion, and a missing wake fails on the
// first read rather than after a timeout.
type residencyWakeRig struct {
	root *Session
	c    *RetirementController
	woke chan struct{}
}

func newResidencyWakeRig(t *testing.T, adapter *fakeAdapter) *residencyWakeRig {
	t.Helper()
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(adapter)
	// The session namer runs on its own goroutine and, with no cheap model
	// configured, names with the session model -- which is the adapter this
	// test scripts. It would take the one scripted turn and leave the real
	// turn answering with bare text, intermittently and only under load. The
	// namer gets its own provider so the script is the turn's alone.
	root, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir, testOnly: sharingHostProbe(testConfig{})})
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
	settleRetirement(c)
	rig := &residencyWakeRig{root: root, c: c, woke: make(chan struct{}, 8)}
	root.SetPendingUserInputWakeFunc(rig.record)
	root.SetClientMutationStartWakeFunc(rig.record)
	return rig
}

func (r *residencyWakeRig) record() {
	select {
	case r.woke <- struct{}{}:
	default:
	}
}

// drain forgets the wakes acceptance already sent, so the next assertion sees
// only what the transition under test owes.
func (r *residencyWakeRig) drain() {
	for {
		select {
		case <-r.woke:
		default:
			return
		}
	}
}

// refuseClaim drives the refusal a non-resident phase produces over this
// session's own admitted work, and returns the snapshot it presented.
func (r *residencyWakeRig) refuseClaim(t *testing.T) RetirementSnapshot {
	t.Helper()
	claim, snap, err := r.c.TryManualClaim(context.Background())
	if claim != nil || err != nil {
		t.Fatalf("TryManualClaim = claim %v, err %v; want the refusal this work owes", claim, err)
	}
	if snap.Phase != "resident" {
		t.Fatalf("phase after the refusal = %q, want resident", snap.Phase)
	}
	return snap
}

// TestResidencyReturnWakesRunnableQueuedInput pins the queued-input half.
//
// beginRetirementMutation refuses every lease while the controller's phase is
// not resident, so the serve loop's queued-input probe can find runnable
// durable input and still have its claim refused. The refusal returns the
// controller to resident, and that return is what makes the input claimable
// again. Nothing observed it on the input path: Changed notifies only Run, and
// the client already holds its Applied receipt, so no later accept arrives to
// send a wake.
//
// The test supplies no wake of its own. It asserts the session's own wake
// fired for the transition, then relays that wake, which is the serve loop's
// response.
func TestResidencyReturnWakesRunnableQueuedInput(t *testing.T) {
	t.Parallel()
	rig := newResidencyWakeRig(t, &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.CommunicateResponse(true, "the queued message ran") },
	}})
	if _, err := rig.root.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "residency-return-queue",
		Input:            clientMutationInput("run me when residency returns", nil, nil),
	}); err != nil {
		t.Fatalf("AcceptClientMutationQueue: %v", err)
	}
	if !rig.root.hasPendingUserInputToRun() {
		t.Fatal("the probe reports nothing runnable, so the refusal below would prove nothing")
	}
	rig.drain()

	rig.refuseClaim(t)

	if !rig.root.hasPendingUserInputToRun() {
		t.Fatal("the refused claim consumed the work; the wake below would prove nothing")
	}
	select {
	case <-rig.woke:
	default:
		t.Fatal("no wake followed the return to resident: the queued input is durably accepted, runnable, and nothing will look again")
	}

	// The wake is what the serve loop answers.
	if _, ran, err := rig.root.ProcessPendingUserInput(context.Background(), nil); err != nil || !ran {
		t.Fatalf("ProcessPendingUserInput after the wake = ran %v, err %v; want the queued message to run", ran, err)
	}
}

// TestResidencyReturnWakesRunnableClientMutationStart pins the start half.
// A pending start takes the same admission lease (ProcessClientMutationStart)
// and is the same retirement evidence (PendingExecutions), so the same refusal
// strands it -- and hasPendingUserInputToRun does not report a start, so the
// queued-input wake alone would leave it durably accepted and unrun.
func TestResidencyReturnWakesRunnableClientMutationStart(t *testing.T) {
	t.Parallel()
	rig := newResidencyWakeRig(t, &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.CommunicateResponse(true, "the accepted start ran") },
	}})
	response, err := rig.root.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "residency-return-start",
		Input:            clientMutationInput("run me when residency returns", nil, nil),
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	if _, runnable := rig.root.runnableClientMutationStartTurnID(); !runnable {
		t.Fatal("the start is not runnable, so the refusal below would prove nothing")
	}
	rig.drain()

	rig.refuseClaim(t)

	if _, runnable := rig.root.runnableClientMutationStartTurnID(); !runnable {
		t.Fatal("the refused claim consumed the start; the wake below would prove nothing")
	}
	select {
	case <-rig.woke:
	default:
		t.Fatal("no wake followed the return to resident: the start is durably accepted, runnable, and nothing will look again")
	}

	// The wake is what the serve loop answers.
	claimed := ""
	_, ran, err := rig.root.ProcessClientMutationStart(context.Background(), func(turnID string, phase ClientMutationStartPhase) {
		if phase == ClientMutationStartClaimed {
			claimed = turnID
		}
	})
	if err != nil || !ran || claimed != response.Turn.ID {
		t.Fatalf("ProcessClientMutationStart after the wake = claimed %q ran %v err %v; want %q to run", claimed, ran, err, response.Turn.ID)
	}
}

// TestResidencyReturnOnAnIdleSessionWakesNothing pins the gate through Abort,
// the other return to resident: a retire with no accepted work must not kick
// the input paths, or every idle interval would send a wake that finds
// nothing.
func TestResidencyReturnOnAnIdleSessionWakesNothing(t *testing.T) {
	t.Parallel()
	rig := newResidencyWakeRig(t, &fakeAdapter{name: "openai"})
	if rig.root.hasPendingUserInputToRun() {
		t.Fatal("the idle session reports runnable work")
	}
	if _, runnable := rig.root.runnableClientMutationStartTurnID(); runnable {
		t.Fatal("the idle session has a runnable start")
	}
	rig.drain()

	// A settled session is claimable outright -- there is no work for the
	// evidence to refuse over -- so the return under test is the Abort that
	// abandons it.
	claim, snap, err := rig.c.TryManualClaim(context.Background())
	if claim == nil || err != nil {
		t.Fatalf("TryManualClaim on a settled session = claim %v, err %v; want the claim", claim, err)
	}
	if snap.Phase != "preparing" {
		t.Fatalf("phase while claimed = %q, want preparing", snap.Phase)
	}
	if err := rig.c.Abort(claim, "prepare_failed"); err != nil {
		t.Fatalf("Abort: %v", err)
	}

	select {
	case <-rig.woke:
		t.Fatal("an idle session's return to resident sent a wake, so the gate does not hold")
	default:
	}
}
