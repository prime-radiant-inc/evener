package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// collectBufferedSessionEvents removes every event currently buffered on the
// session.
func collectBufferedSessionEvents(sess *Session) []events.SessionEvent {
	var out []events.SessionEvent
	for {
		select {
		case ev := <-sess.Events():
			out = append(out, ev)
		default:
			return out
		}
	}
}

func userInputEventsFor(evs []events.SessionEvent, clientMutationID string) []events.UserInputData {
	var out []events.UserInputData
	for _, ev := range evs {
		if data, ok := ev.Data.(events.UserInputData); ok && data.ClientMutationID == clientMutationID {
			out = append(out, data)
		}
	}
	return out
}

func newTwoStageMutationSession(t *testing.T) *Session {
	t.Helper()
	sess := newSession(t,
		withStateDir(t.TempDir()),
		withSteps(func(llm.Request) llm.Response { return agenttest.FinalResponse("done") }),
		withConfig(SessionConfig{
			MaxSubagentDepth: 1,
			testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		}),
	)
	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	return sess
}

// TestClientMutationPublishedEffectAcceptsThenApplies covers the two-stage
// acknowledgment for an effect whose directory sync failed after the rename:
// the durable acceptance is returned, the projection/wake runs, and the applied
// USER_INPUT event follows for the accepted turn.
func TestClientMutationPublishedEffectAcceptsThenApplies(t *testing.T) {
	t.Parallel()
	sess := newTwoStageMutationSession(t)
	params := appwire.TurnStartParams{
		ClientMutationID: "published-effect-start",
		Input:            []appwire.InputItem{{Type: "text", Text: "apply me"}},
	}
	var snapshotSyncs atomic.Int32
	sess.clientMutations.faults.SnapshotDirSync = func() error {
		if snapshotSyncs.Add(1) == 2 { // the effect write after the reservation
			return errors.New("injected directory sync failure")
		}
		return nil
	}
	collectBufferedSessionEvents(sess)

	response, err := sess.AcceptClientMutationStart(params)
	if err != nil {
		t.Fatalf("AcceptClientMutationStart = %v, want the durable acceptance", err)
	}
	if response.Turn.ID == "" || response.Receipt.ClientMutationID != params.ClientMutationID {
		t.Fatalf("accepted response = %#v", response)
	}
	if response.Receipt.ProjectionState != appwire.MutationProjectionPending {
		t.Fatalf("accepted projection state = %q, want pending", response.Receipt.ProjectionState)
	}
	snapshot := sess.clientMutations.snapshot()
	pending := snapshot.PendingExecutions[params.ClientMutationID]
	if pending.ExecutionState != "accepted" || pending.TurnID != response.Turn.ID {
		t.Fatalf("published pending execution = %#v", pending)
	}
	if applied := userInputEventsFor(collectBufferedSessionEvents(sess), params.ClientMutationID); len(applied) != 0 {
		t.Fatalf("applied event before projection = %#v", applied)
	}

	result, claimed, err := sess.ProcessClientMutationStart(context.Background(), nil)
	if err != nil || !claimed || result != "done" {
		t.Fatalf("ProcessClientMutationStart = (%q, %v, %v), want the accepted turn applied", result, claimed, err)
	}
	applied := userInputEventsFor(collectBufferedSessionEvents(sess), params.ClientMutationID)
	if len(applied) != 1 {
		t.Fatalf("applied USER_INPUT events = %#v, want one", applied)
	}
	if applied[0].StableTurnID != response.Turn.ID || applied[0].Turn == 0 {
		t.Fatalf("applied USER_INPUT = %#v, want turn %q for %q", applied[0], response.Turn.ID, params.ClientMutationID)
	}
}

// TestClientMutationPublishedStartClaimIsApplied covers a claim write whose
// directory sync failed after the rename. The claim was durably published, so
// the normal apply path must run it and emit the applied USER_INPUT event
// rather than strand the accepted start.
func TestClientMutationPublishedStartClaimIsApplied(t *testing.T) {
	t.Parallel()
	sess := newTwoStageMutationSession(t)
	params := appwire.TurnStartParams{
		ClientMutationID: "published-claim-start",
		Input:            []appwire.InputItem{{Type: "text", Text: "claim and apply"}},
	}
	response, err := sess.AcceptClientMutationStart(params)
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	collectBufferedSessionEvents(sess)

	var claimSyncs atomic.Int32
	sess.clientMutations.faults.SnapshotDirSync = func() error {
		if claimSyncs.Add(1) == 1 { // the claim write
			return errors.New("injected claim directory sync failure")
		}
		return nil
	}

	result, claimed, err := sess.ProcessClientMutationStart(context.Background(), nil)
	if err != nil || !claimed || result != "done" {
		t.Fatalf("ProcessClientMutationStart = (%q, %v, %v), want the published claim applied", result, claimed, err)
	}
	applied := userInputEventsFor(collectBufferedSessionEvents(sess), params.ClientMutationID)
	if len(applied) != 1 {
		t.Fatalf("applied USER_INPUT events = %#v, want one", applied)
	}
	if applied[0].StableTurnID != response.Turn.ID {
		t.Fatalf("applied turn = %q, want %q", applied[0].StableTurnID, response.Turn.ID)
	}
	record := sess.clientMutations.snapshot().Journal[params.ClientMutationID]
	if record.OperationState != clientMutationOperationTerminal || record.ProjectionState != appwire.MutationProjectionReflected {
		t.Fatalf("claimed start after apply = %#v, want terminal/reflected", record)
	}
}
