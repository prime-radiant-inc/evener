package server

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
)

// daemonRetireHold captures the claim a controller-backed retire hook takes so
// the test can abort it and leave the controller resident.
type daemonRetireHold struct {
	claim *agent.RetirementClaim
}

// installDaemonLifecycle wires the typed hooks the way the serve exit-owner
// will: status is a detached controller snapshot, retire goes through the
// controller claim and reports blockers when the process is not eligible.
func installDaemonLifecycle(s *Server, c *agent.RetirementController, hold *daemonRetireHold) {
	s.SetDaemonLifecycle(
		func() appwire.DaemonLifecycle { return DaemonLifecycleFromSnapshot(c.Snapshot()) },
		func(_ context.Context, _ appwire.DaemonRetireParams) (appwire.DaemonRetireResponse, error) {
			claim, snap, err := c.TryClaim(true)
			if err != nil {
				return appwire.DaemonRetireResponse{}, err
			}
			if claim == nil {
				return appwire.DaemonRetireResponse{Accepted: false, Lifecycle: DaemonLifecycleFromSnapshot(snap)}, nil
			}
			hold.claim = claim
			return appwire.DaemonRetireResponse{Accepted: true, Lifecycle: DaemonLifecycleFromSnapshot(snap)}, nil
		},
	)
}

func dispatchDaemon(t *testing.T, s *Server, method string, params any) (any, error) {
	t.Helper()
	var raw json.RawMessage
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		raw = encoded
	}
	return s.AppServer().Router().Dispatch(context.Background(), appwire.Request{Method: method, Params: raw})
}

func assertLifecycleUnavailable(t *testing.T, err error, phase string) {
	t.Helper()
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error %v is not a WireError", err)
	}
	if wire.Code != appwire.CodeUnavailable {
		t.Fatalf("code=%d, want CodeUnavailable", wire.Code)
	}
	data, ok := wire.Data.(appwire.LifecycleErrorData)
	if !ok {
		raw, _ := json.Marshal(wire.Data)
		t.Fatalf("error data type %T, want LifecycleErrorData: %s", wire.Data, raw)
	}
	if data.LifecycleReason != phase {
		t.Fatalf("lifecycleReason=%q, want %q", data.LifecycleReason, phase)
	}
	if !data.Retryable {
		t.Fatal("lifecycle race must be retryable")
	}
	// mutationOutcome is pinned to unknown, not notAccepted: the admission
	// fence runs before the handler's mutation replay lookup, so the
	// refusing path cannot know whether a retried ClientMutationID was
	// already durably accepted. Plan line 918 forbids claiming
	// notAccepted for a mutation whose reply may have been lost.
	if data.MutationOutcome != appwire.MutationOutcomeUnknown {
		t.Fatalf("mutationOutcome=%q, want unknown", data.MutationOutcome)
	}
}

func TestDaemonLifecycleFromSnapshotMapping(t *testing.T) {
	eligible := time.Date(2026, 9, 12, 10, 0, 0, 0, time.FixedZone("UTC+2", 2*60*60))
	deadline := eligible.Add(90 * time.Second)
	snap := agent.RetirementSnapshot{
		Phase: "resident", Timeout: 90 * time.Second,
		EligibleSince: eligible, Deadline: deadline,
		Blockers: []agent.RetirementBlocker{{Category: "delegate", SessionID: "sess_1", DelegateID: "dlg_1"}},
		Failure:  "prepare_failed",
	}
	got := DaemonLifecycleFromSnapshot(snap)
	if got.Phase != "resident" || got.TimeoutMillis != 90000 {
		t.Fatalf("lifecycle=%+v", got)
	}
	if got.EligibleSince != eligible.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("eligibleSince=%q, want UTC instant", got.EligibleSince)
	}
	if got.Deadline != deadline.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("deadline=%q, want UTC instant", got.Deadline)
	}
	if len(got.Blockers) != 1 || got.Blockers[0] != (appwire.DaemonBlocker{Category: "delegate", SessionID: "sess_1", DelegateID: "dlg_1"}) {
		t.Fatalf("blockers=%+v", got.Blockers)
	}
	if got.Failure != "prepare_failed" {
		t.Fatalf("failure=%q", got.Failure)
	}

	zero := DaemonLifecycleFromSnapshot(agent.RetirementSnapshot{Phase: "resident"})
	if zero.Blockers == nil || len(zero.Blockers) != 0 {
		t.Fatalf("blockers must be a non-nil empty array: %+v", zero.Blockers)
	}
	if zero.EligibleSince != "" || zero.Deadline != "" {
		t.Fatalf("zero times must stay unknown: %+v", zero)
	}
}

// TestDaemonStatusDoesNotResetRetirementInterval proves a lifecycle status
// request is a detached read: it must not mark activity, clear eligibility, or
// otherwise mutate controller state, even while a timeout is configured.
func TestDaemonStatusDoesNotResetRetirementInterval(t *testing.T) {
	s, _, c := retirementEngineServer(t)
	installDaemonLifecycle(s, c, &daemonRetireHold{})
	before := c.Snapshot()
	out, err := dispatchDaemon(t, s, appwire.MethodEvenerDaemonStatus, appwire.DaemonStatusParams{})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	resp, ok := out.(appwire.DaemonStatusResponse)
	if !ok {
		t.Fatalf("status response type %T", out)
	}
	if want := DaemonLifecycleFromSnapshot(before); !reflect.DeepEqual(resp.Lifecycle, want) {
		t.Fatalf("status lifecycle=%+v, want %+v", resp.Lifecycle, want)
	}
	if after := c.Snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatalf("status mutated retirement state: before %+v after %+v", before, after)
	}
}

// TestDaemonManualRetireWithZeroTimeoutClaimsOnlyManually pins that a disabled
// (zero) retirement timeout never claims through the automatic path, while the
// manual wire request claims immediately — and a raced second request gets the
// typed lifecycle error instead of a second claim.
func TestDaemonManualRetireWithZeroTimeoutClaimsOnlyManually(t *testing.T) {
	s, _, c := retirementEngineServer(t) // zero timeout: automatic retirement disabled
	hold := &daemonRetireHold{}
	installDaemonLifecycle(s, c, hold)
	if claim, _, err := c.TryClaim(false); err != nil || claim != nil {
		t.Fatalf("automatic retire with disabled timeout: claim=%v err=%v", claim, err)
	}
	out, err := dispatchDaemon(t, s, appwire.MethodEvenerDaemonRetire, appwire.DaemonRetireParams{})
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	resp, ok := out.(appwire.DaemonRetireResponse)
	if !ok {
		t.Fatalf("retire response type %T", out)
	}
	if !resp.Accepted || resp.Lifecycle.Phase != "preparing" {
		t.Fatalf("manual retire not accepted: %+v", resp)
	}
	if hold.claim == nil {
		t.Fatal("accepted retire did not take the claim")
	}
	_, err = dispatchDaemon(t, s, appwire.MethodEvenerDaemonRetire, appwire.DaemonRetireParams{})
	assertLifecycleUnavailable(t, err, "preparing")
	if err := c.Abort(hold.claim, ""); err != nil {
		t.Fatal(err)
	}
}

// TestDaemonRetirePendingQuestionReturnsFreshBlockers proves a blocked retire
// is refused with Accepted:false and the current blocker set — and that the
// blockers are fresh (they clear once the question settles), not a stale copy.
func TestDaemonRetirePendingQuestionReturnsFreshBlockers(t *testing.T) {
	s, root, c := retirementEngineServer(t)
	hold := &daemonRetireHold{}
	installDaemonLifecycle(s, c, hold)
	release, err := c.BeginMutation(root.ID(), "question")
	if err != nil {
		t.Fatal(err)
	}
	out, err := dispatchDaemon(t, s, appwire.MethodEvenerDaemonRetire, appwire.DaemonRetireParams{})
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	resp, ok := out.(appwire.DaemonRetireResponse)
	if !ok {
		t.Fatalf("retire response type %T", out)
	}
	if resp.Accepted {
		t.Fatalf("retire accepted over a pending question: %+v", resp)
	}
	found := false
	for _, blocker := range resp.Lifecycle.Blockers {
		if blocker.Category == "question" && blocker.SessionID == root.ID() {
			found = true
		}
	}
	if !found {
		t.Fatalf("pending question missing from blockers: %+v", resp.Lifecycle.Blockers)
	}
	release()
	out, err = dispatchDaemon(t, s, appwire.MethodEvenerDaemonRetire, appwire.DaemonRetireParams{})
	if err != nil {
		t.Fatalf("retire after question settled: %v", err)
	}
	resp, ok = out.(appwire.DaemonRetireResponse)
	if !ok {
		t.Fatalf("retire response type %T", out)
	}
	if !resp.Accepted {
		t.Fatalf("stale blockers refused a clean retire: %+v", resp)
	}
	if err := c.Abort(hold.claim, ""); err != nil {
		t.Fatal(err)
	}
}

// TestDaemonRacedMutationReturnsTypedLifecycleError proves a mutation that
// loses the race with retirement fails before durable acceptance with the
// composed typed error: CodeUnavailable, data.lifecycleReason, retryable.
func TestDaemonRacedMutationReturnsTypedLifecycleError(t *testing.T) {
	s, root, c := retirementEngineServer(t)
	installDaemonLifecycle(s, c, &daemonRetireHold{})
	s.SetRetirementAdmission(func(_ context.Context, kind string) (func(), error) {
		if kind == "read" {
			return c.Borrow()
		}
		return c.BeginMutation(root.ID(), "admission")
	})
	claim, _, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v", err)
	}
	input := []appwire.InputItem{{Type: "text", Text: "raced"}}
	_, err = dispatchDaemon(t, s, appwire.MethodTurnStart, appwire.TurnStartParams{
		ClientMutationID: "raced-start", ExpectedInstanceID: root.ID(), Input: input,
	})
	assertLifecycleUnavailable(t, err, "preparing")
	if root.QueueDepth() != 0 || len(root.SteeringQueueSnapshot()) != 0 {
		t.Fatal("raced mutation was durably accepted")
	}
	if err := c.Abort(claim, ""); err != nil {
		t.Fatal(err)
	}
}

// TestDaemonHandlersWithoutHooksReturnUnavailable pins that the live router
// never answers a lifecycle request with a stub success: without installed
// hooks both methods fail with CodeUnavailable.
func TestDaemonHandlersWithoutHooksReturnUnavailable(t *testing.T) {
	s := NewServer(ServerConfig{})
	for _, method := range []string{appwire.MethodEvenerDaemonStatus, appwire.MethodEvenerDaemonRetire} {
		_, err := dispatchDaemon(t, s, method, nil)
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
			t.Fatalf("%s without hooks = %v, want CodeUnavailable", method, err)
		}
	}
}

// TestDaemonLostRetireResponseIsNotProofOfExit pins the wire semantics a Hub
// must honor: an errored/lost retire response says nothing about process exit.
// The lifecycle status remains the authority and the daemon keeps answering.
func TestDaemonLostRetireResponseIsNotProofOfExit(t *testing.T) {
	s, _, c := retirementEngineServer(t)
	hold := &daemonRetireHold{}
	installDaemonLifecycle(s, c, hold)
	out, err := dispatchDaemon(t, s, appwire.MethodEvenerDaemonRetire, appwire.DaemonRetireParams{})
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if resp, ok := out.(appwire.DaemonRetireResponse); !ok || !resp.Accepted {
		t.Fatalf("retire not accepted: %+v", out)
	}
	// The response to a later retire request is lost in transit (here: the hook
	// errors after the claim is already held). That failure is not evidence the
	// process exited.
	s.SetDaemonLifecycle(
		func() appwire.DaemonLifecycle { return DaemonLifecycleFromSnapshot(c.Snapshot()) },
		func(context.Context, appwire.DaemonRetireParams) (appwire.DaemonRetireResponse, error) {
			return appwire.DaemonRetireResponse{}, errors.New("response lost in transit")
		},
	)
	if _, err := dispatchDaemon(t, s, appwire.MethodEvenerDaemonRetire, appwire.DaemonRetireParams{}); err == nil {
		t.Fatal("lost-response retire must surface its error")
	}
	if got := c.Snapshot().Phase; got != "preparing" {
		t.Fatalf("lost response changed lifecycle phase: %q", got)
	}
	out, err = dispatchDaemon(t, s, appwire.MethodEvenerDaemonStatus, appwire.DaemonStatusParams{})
	if err != nil {
		t.Fatalf("status after lost retire response: %v", err)
	}
	if resp := out.(appwire.DaemonStatusResponse); resp.Lifecycle.Phase != "preparing" {
		t.Fatalf("status after lost response = %+v, want preparing", resp.Lifecycle)
	}
	if err := c.Abort(hold.claim, ""); err != nil {
		t.Fatal(err)
	}
	out, err = dispatchDaemon(t, s, appwire.MethodEvenerDaemonStatus, appwire.DaemonStatusParams{})
	if err != nil {
		t.Fatalf("status after abort: %v", err)
	}
	if resp := out.(appwire.DaemonStatusResponse); resp.Lifecycle.Phase != "resident" {
		t.Fatalf("daemon did not return to resident: %+v", resp.Lifecycle)
	}
}
