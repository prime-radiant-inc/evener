package agent

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/tool"
)

func rootDispositionRegistry(t *testing.T, validate func(context.Context, string) error) (*tool.Registry, *[]events.CommunicateData) {
	t.Helper()
	delivered := []events.CommunicateData{}
	deps := &toolDeps{
		emit: func(events.EventKind, events.EventData) {},
		deliverCommunicate: func(data events.CommunicateData) error {
			delivered = append(delivered, data)
			return nil
		},
		abort:                  func(context.Context) error { return nil },
		drainSteering:          func() []steeringMessage { return nil },
		prependSteering:        func([]steeringMessage) {},
		resultToolName:         func() string { return "communicate" },
		setCommunicateTerminal: func(context.Context, string, string, string, any, string) bool { return true },
		offersEndReason:        true,
		validateWaitTarget:     validate,
	}
	reg := tool.NewRegistry()
	if err := registerCommunicateTool(reg, deps); err != nil {
		t.Fatalf("register communicate: %v", err)
	}
	return reg, &delivered
}

func TestDelegateReportBareTextGetsOneSteeringRound(t *testing.T) {
	if got := routeNoToolCalls(EntryNotification, false, false, true); got != runNoToolCalls {
		t.Fatalf("delegate report route = %v, want runNoToolCalls for one steering round", got)
	}
	if got := routeNoToolCalls(EntryNotification, false, false, false); got != finishIdle {
		t.Fatalf("ordinary notification route = %v, want finishIdle", got)
	}
}

func TestDelegateReportAcknowledgementSpendsOneRound(t *testing.T) {
	if delegateReportAcknowledgementExhausted(EntryNotification, true, retryTracker{}) {
		t.Fatal("the first bare-text acknowledgement was treated as spent")
	}
	if !delegateReportAcknowledgementExhausted(EntryNotification, true, retryTracker{consecutiveBareText: 1}) {
		t.Fatal("the second bare-text acknowledgement was not treated as spent")
	}
	if delegateReportAcknowledgementExhausted(EntryUserInput, true, retryTracker{consecutiveBareText: 1}) {
		t.Fatal("a user-input turn was treated as a report acknowledgement")
	}
}

func TestDelegateReportExplicitDoneSettles(t *testing.T) {
	reg, delivered := rootDispositionRegistry(t, nil)
	res := reg.ExecuteCall(context.Background(), nil, communicateCallArgs("comm-1", map[string]any{
		"message":    "acknowledged",
		"end_turn":   true,
		"end_reason": tool.CommunicateEndReasonDone,
	}))
	if res.IsError {
		t.Fatalf("done report: %s", res.Output)
	}
	if len(*delivered) != 1 || (*delivered)[0].EndReason != tool.CommunicateEndReasonDone {
		t.Fatalf("delivered = %+v, want one done settlement", *delivered)
	}
}

func TestDelegateReportWaitingWithoutWakerIsRefused(t *testing.T) {
	reg, delivered := rootDispositionRegistry(t, func(context.Context, string) error {
		return errors.New("no live waker")
	})
	res := reg.ExecuteCall(context.Background(), nil, communicateCallArgs("comm-1", map[string]any{
		"message":    "waiting",
		"end_turn":   true,
		"end_reason": tool.CommunicateEndReasonWaitingOnWork,
		"waiting_on": "job-1",
	}))
	if !res.IsError {
		t.Fatalf("result = %+v, want the absent waker refused", res)
	}
	if len(*delivered) != 0 {
		t.Fatalf("delivered = %+v, want nothing delivered on a refusal", *delivered)
	}
}

func TestDelegateReportNeedsResponseKeepsExistingQuestion(t *testing.T) {
	reg, delivered := rootDispositionRegistry(t, nil)
	res := reg.ExecuteCall(context.Background(), nil, communicateCallArgs("comm-1", map[string]any{
		"message":    "which one?",
		"end_turn":   true,
		"end_reason": tool.CommunicateEndReasonNeedsResponse,
	}))
	if res.IsError {
		t.Fatalf("needs_response: %s", res.Output)
	}
	if len(*delivered) != 1 || (*delivered)[0].EndReason != tool.CommunicateEndReasonNeedsResponse {
		t.Fatalf("delivered = %+v, want one needs_response question", *delivered)
	}
}

func TestDelegateReportCreatesNoAttentionEntry(t *testing.T) {
	s := &Session{}
	s.rootAttentionWakeIDs = map[string]struct{}{"delegate:dlg_target/delivery/1": {}}
	if !s.pendingRootReportAttention() {
		t.Fatal("a report delivery was not recognized as report attention")
	}
	s.rootAttentionWakeIDs = map[string]struct{}{"delegate:dlg_target/update/1": {}}
	if s.pendingRootReportAttention() {
		t.Fatal("an update delivery was mistaken for report attention")
	}
}
