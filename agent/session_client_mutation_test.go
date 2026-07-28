package agent

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/serf/appwire"
)

func TestClientMutationStore_ReserveReplayAndReject(t *testing.T) {
	tests := []struct {
		name      string
		terminal  clientMutationOperationState
		result    json.RawMessage
		rejection *clientMutationRejection
	}{
		{
			name:     "applied mutation replays",
			terminal: clientMutationOperationApplied,
			result:   json.RawMessage(`{"turnId":"turn-7"}`),
		},
		{
			name:     "incorporated terminal mutation replays",
			terminal: clientMutationOperationTerminal,
			result:   json.RawMessage(`{"turnId":"turn-7"}`),
		},
		{
			name:     "terminal rejection replays",
			terminal: clientMutationOperationRejected,
			rejection: &clientMutationRejection{
				Code:    appwire.CodeConflict,
				Message: "expected turn no longer active",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestClientMutationStore(t, clientMutationFaults{})
			request := testClientMutationRequest(t, "turn/start", "mutation-1", appwire.TurnStartParams{
				ClientMutationID: "mutation-1",
				Input:            []appwire.InputItem{{Type: "text", Text: "ship it"}},
			})

			first, err := store.reserve(request)
			if err != nil {
				t.Fatalf("reserve unseen mutation: %v", err)
			}
			if first.Disposition != clientMutationDispositionReserved {
				t.Fatalf("unseen disposition = %q, want %q", first.Disposition, clientMutationDispositionReserved)
			}
			if first.Record.AttemptGeneration != 1 {
				t.Fatalf("unseen attempt generation = %d, want 1", first.Record.AttemptGeneration)
			}
			if len(first.Record.Payload) == 0 {
				t.Fatal("unseen reservation did not retain the canonical payload")
			}

			if err := store.update(first.Lease, func(_ *clientMutationSnapshot, record *clientMutationRecord) error {
				record.OperationState = tt.terminal
				record.Result = tt.result
				record.Rejection = tt.rejection
				return nil
			}); err != nil {
				t.Fatalf("commit terminal mutation: %v", err)
			}

			replay, err := store.reserve(request)
			if err != nil {
				t.Fatalf("reserve completed mutation: %v", err)
			}
			if replay.Disposition != clientMutationDispositionReplayed {
				t.Fatalf("completed disposition = %q, want %q", replay.Disposition, clientMutationDispositionReplayed)
			}
			if !reflect.DeepEqual(replay.Record.Rejection, tt.rejection) {
				t.Fatalf("replayed rejection = %#v, want %#v", replay.Record.Rejection, tt.rejection)
			}
			if string(replay.Record.Result) != string(tt.result) {
				t.Fatalf("replayed result = %s, want %s", replay.Record.Result, tt.result)
			}
		})
	}
}

func TestClientMutationStore_SameIDPayloadMismatch(t *testing.T) {
	store := newTestClientMutationStore(t, clientMutationFaults{})
	original := testClientMutationRequest(t, "turn/queue", "mutation-1", appwire.TurnQueueParams{
		ClientMutationID: "mutation-1",
		ExpectedTurnID:   "turn-1",
		Input:            []appwire.InputItem{{Type: "text", Text: "first"}},
	})
	reserved, err := store.reserve(original)
	if err != nil {
		t.Fatalf("reserve original: %v", err)
	}
	reserved.Lease.Release()

	tests := []struct {
		name    string
		request clientMutationRequest
	}{
		{
			name: "different payload",
			request: testClientMutationRequest(t, "turn/queue", "mutation-1", appwire.TurnQueueParams{
				ClientMutationID: "mutation-1",
				ExpectedTurnID:   "turn-1",
				Input:            []appwire.InputItem{{Type: "text", Text: "different"}},
			}),
		},
		{
			name:    "different method",
			request: testClientMutationRequest(t, "turn/steer", "mutation-1", appwire.TurnQueueParams{ClientMutationID: "mutation-1"}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := store.reserve(tt.request)
			if !errors.Is(err, errClientMutationMismatch) {
				t.Fatalf("reserve mismatch error = %v, want %v", err, errClientMutationMismatch)
			}
		})
	}
}

func TestClientMutationOwnership_JoinReleaseAndTakeover(t *testing.T) {
	store := newTestClientMutationStore(t, clientMutationFaults{})
	request := testClientMutationRequest(t, "turn/start", "mutation-1", appwire.TurnStartParams{
		ClientMutationID: "mutation-1",
		Input:            []appwire.InputItem{{Type: "text", Text: "hello"}},
	})

	first, err := store.reserve(request)
	if err != nil {
		t.Fatalf("reserve first owner: %v", err)
	}
	joined, err := store.reserve(request)
	if err != nil {
		t.Fatalf("join active owner: %v", err)
	}
	if joined.Disposition != clientMutationDispositionJoined {
		t.Fatalf("active-owner disposition = %q, want %q", joined.Disposition, clientMutationDispositionJoined)
	}
	if joined.OwnerAttemptGeneration != first.Record.AttemptGeneration {
		t.Fatalf("joined attempt = %d, want %d", joined.OwnerAttemptGeneration, first.Record.AttemptGeneration)
	}

	first.Lease.Release()
	select {
	case <-joined.OwnerDone:
	default:
		t.Fatal("joined owner was not notified when the nonterminal owner released")
	}

	takeover, err := store.reserve(request)
	if err != nil {
		t.Fatalf("take over unowned mutation: %v", err)
	}
	if takeover.Disposition != clientMutationDispositionReserved {
		t.Fatalf("takeover disposition = %q, want %q", takeover.Disposition, clientMutationDispositionReserved)
	}
	if takeover.Record.AttemptGeneration != first.Record.AttemptGeneration+1 {
		t.Fatalf("takeover generation = %d, want %d", takeover.Record.AttemptGeneration, first.Record.AttemptGeneration+1)
	}
	takeover.Lease.Release()
}

func TestClientMutationOwnership_AfterReservationFaultReleasesOwner(t *testing.T) {
	injected := errors.New("after reservation")
	store := newTestClientMutationStore(t, clientMutationFaults{
		AfterReservation: func() error { return injected },
	})
	request := testClientMutationRequest(t, "turn/start", "mutation-1", appwire.TurnStartParams{
		ClientMutationID: "mutation-1",
		Input:            []appwire.InputItem{{Type: "text", Text: "survives"}},
	})

	if _, err := store.reserve(request); !errors.Is(err, injected) {
		t.Fatalf("reserve error = %v, want %v", err, injected)
	}
	store.faults.AfterReservation = nil

	takeover, err := store.reserve(request)
	if err != nil {
		t.Fatalf("take over after injected exit: %v", err)
	}
	if takeover.Disposition != clientMutationDispositionReserved {
		t.Fatalf("takeover disposition = %q, want reserved", takeover.Disposition)
	}
	if takeover.Record.AttemptGeneration != 2 {
		t.Fatalf("takeover generation = %d, want 2", takeover.Record.AttemptGeneration)
	}
	takeover.Lease.Release()
}

func newTestClientMutationStore(t *testing.T, faults clientMutationFaults) *clientMutationStore {
	t.Helper()
	store, err := newClientMutationStoreFS(afero.NewMemMapFs(), "/state", "session-1", faults)
	if err != nil {
		t.Fatalf("new mutation store: %v", err)
	}
	return store
}

func testClientMutationRequest(t *testing.T, method, id string, payload any) clientMutationRequest {
	t.Helper()
	request, err := newClientMutationRequest(method, id, payload)
	if err != nil {
		t.Fatalf("new mutation request: %v", err)
	}
	return request
}
