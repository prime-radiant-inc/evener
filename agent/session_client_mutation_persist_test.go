package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

func TestClientMutationPersist_FullSnapshotRoundTrip(t *testing.T) {
	fs := afero.NewMemMapFs()
	store, err := newClientMutationStoreFS(fs, "/state", "session-1", clientMutationFaults{})
	if err != nil {
		t.Fatalf("new mutation store: %v", err)
	}
	request := testClientMutationRequest(t, "turn/queue", "mutation-1", appwire.TurnQueueParams{
		Ref:              "thread-ref",
		ClientMutationID: "mutation-1",
		ExpectedTurnID:   "turn-2",
		Input: []appwire.InputItem{
			{Type: "text", Text: "inspect this"},
			{
				Type:      "image",
				Name:      "evidence.png",
				MediaType: "image/png",
				Data:      []byte{0, 1, 2, 127, 128, 255},
				Metadata:  map[string]string{"origin": "clipboard"},
			},
		},
	})
	request.Preconditions = clientMutationPreconditions{
		ExpectedTurnID:        "turn-2",
		ExpectedQueueRevision: uint64Pointer(8),
	}
	request.StableQueueEntryIDs = []string{"queue-11", "queue-12"}

	reserved, err := store.reserve(request)
	if err != nil {
		t.Fatalf("reserve mutation: %v", err)
	}
	if err := store.update(reserved.Lease, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		snapshot.InputQueue = []clientMutationQueueEntry{{
			ID:               "queue-11",
			ClientMutationID: "mutation-1",
			Input: []appwire.InputItem{{
				Type:      "image",
				Name:      "evidence.png",
				MediaType: "image/png",
				Data:      []byte{0, 1, 2, 127, 128, 255},
				Metadata:  map[string]string{"origin": "clipboard"},
			}},
		}}
		snapshot.QueueRevision = 9
		snapshot.NextTurnSequence = 7
		snapshot.NextQueueEntrySequence = 13
		snapshot.BudgetReservations["mutation-1"] = clientMutationBudgetReservation{
			TurnID: "turn-7",
			Slots:  1,
		}
		snapshot.InterruptFence = &clientMutationInterruptFence{
			ClientMutationID: "interrupt-1",
			ExpectedTurnID:   "turn-2",
		}
		snapshot.PendingExecutions["mutation-1"] = appwire.PendingMutation{
			ClientMutationID: "mutation-1",
			Method:           "turn/queue",
			Input:            requestInput(t, record.Payload),
			ExecutionState:   "queued",
			QueueEntryIDs:    []string{"queue-11", "queue-12"},
			ProjectionState:  appwire.MutationProjectionPending,
		}
		record.ExecutionState = "queued"
		record.ProjectionState = appwire.MutationProjectionPending
		return nil
	}); err != nil {
		t.Fatalf("persist full effect snapshot: %v", err)
	}

	reloaded, err := newClientMutationStoreFS(fs, "/state", "session-1", clientMutationFaults{})
	if err != nil {
		t.Fatalf("reload mutation store: %v", err)
	}
	got := reloaded.snapshot()
	want := store.snapshot()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-tripped snapshot mismatch\n got: %#v\nwant: %#v", got, want)
	}

	var payload appwire.TurnQueueParams
	if err := json.Unmarshal(got.Journal["mutation-1"].Payload, &payload); err != nil {
		t.Fatalf("decode canonical payload: %v", err)
	}
	if !reflect.DeepEqual(payload.Input, requestInput(t, got.Journal["mutation-1"].Payload)) {
		t.Fatalf("full payload input did not round trip: %#v", payload.Input)
	}
	if !reflect.DeepEqual(payload.Input[1].Data, []byte{0, 1, 2, 127, 128, 255}) {
		t.Fatalf("image bytes = %v, want exact original bytes", payload.Input[1].Data)
	}
}

func TestClientMutationPersist_FailedEffectWriteRetainsReservation(t *testing.T) {
	fs := afero.NewMemMapFs()
	injected := errors.New("before effect rename")
	store, err := newClientMutationStoreFS(fs, "/state", "session-1", clientMutationFaults{
		BeforeEffectSnapshotRename: func() error { return injected },
	})
	if err != nil {
		t.Fatalf("new mutation store: %v", err)
	}
	request := testClientMutationRequest(t, "turn/start", "mutation-1", appwire.TurnStartParams{
		ClientMutationID: "mutation-1",
		Input: []appwire.InputItem{{
			Type:      "image",
			MediaType: "image/png",
			Data:      []byte{9, 8, 7, 6},
		}},
	})

	reserved, err := store.reserve(request)
	if err != nil {
		t.Fatalf("reserve mutation: %v", err)
	}
	err = store.update(reserved.Lease, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		record.OperationState = clientMutationOperationApplied
		record.Result = json.RawMessage(`{"turnId":"turn-1"}`)
		snapshot.QueueRevision = 99
		return nil
	})
	if !errors.Is(err, injected) {
		t.Fatalf("effect update error = %v, want %v", err, injected)
	}

	reloaded, err := newClientMutationStoreFS(fs, "/state", "session-1", clientMutationFaults{})
	if err != nil {
		t.Fatalf("reload after failed effect: %v", err)
	}
	durable := reloaded.snapshot()
	record := durable.Journal["mutation-1"]
	if record.OperationState != clientMutationOperationInFlight {
		t.Fatalf("durable state = %q, want inFlight reservation", record.OperationState)
	}
	if durable.QueueRevision != 0 {
		t.Fatalf("durable queue revision = %d, want pre-effect 0", durable.QueueRevision)
	}
	var payload appwire.TurnStartParams
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		t.Fatalf("decode retained payload: %v", err)
	}
	if !reflect.DeepEqual(payload.Input[0].Data, []byte{9, 8, 7, 6}) {
		t.Fatalf("retained payload bytes = %v, want complete input", payload.Input[0].Data)
	}

	takeover, err := store.reserve(request)
	if err != nil {
		t.Fatalf("same-process takeover after failed effect: %v", err)
	}
	if takeover.Disposition != clientMutationDispositionReserved {
		t.Fatalf("post-failure disposition = %q, want reserved takeover", takeover.Disposition)
	}
	if takeover.Record.AttemptGeneration != 2 {
		t.Fatalf("post-failure generation = %d, want 2", takeover.Record.AttemptGeneration)
	}
	takeover.Lease.Release()
}

func TestClientMutationPersist_RestartRecoversUnownedReservation(t *testing.T) {
	fs := afero.NewMemMapFs()
	first, err := newClientMutationStoreFS(fs, "/state", "session-1", clientMutationFaults{})
	if err != nil {
		t.Fatalf("new first store: %v", err)
	}
	request := testClientMutationRequest(t, "turn/start", "mutation-1", appwire.TurnStartParams{
		ClientMutationID: "mutation-1",
		Input:            []appwire.InputItem{{Type: "text", Text: "resume me"}},
	})
	reserved, err := first.reserve(request)
	if err != nil {
		t.Fatalf("reserve before restart: %v", err)
	}

	restarted, err := newClientMutationStoreFS(fs, "/state", "session-1", clientMutationFaults{})
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}
	takeover, err := restarted.reserve(request)
	if err != nil {
		t.Fatalf("recover reservation: %v", err)
	}
	if takeover.Disposition != clientMutationDispositionReserved {
		t.Fatalf("restart disposition = %q, want reserved takeover", takeover.Disposition)
	}
	if takeover.Record.AttemptGeneration != reserved.Record.AttemptGeneration+1 {
		t.Fatalf("restart generation = %d, want %d", takeover.Record.AttemptGeneration, reserved.Record.AttemptGeneration+1)
	}
	reserved.Lease.Release()
	takeover.Lease.Release()
}

func TestClientMutationPersist_AfterEffectRenamePublishesDurableClone(t *testing.T) {
	fs := afero.NewMemMapFs()
	injected := errors.New("after effect rename")
	store, err := newClientMutationStoreFS(fs, "/state", "session-1", clientMutationFaults{
		AfterEffectSnapshotRename: func() error { return injected },
	})
	if err != nil {
		t.Fatalf("new mutation store: %v", err)
	}
	request := testClientMutationRequest(t, "turn/start", "mutation-1", appwire.TurnStartParams{ClientMutationID: "mutation-1"})
	reserved, err := store.reserve(request)
	if err != nil {
		t.Fatalf("reserve mutation: %v", err)
	}
	err = store.update(reserved.Lease, func(_ *clientMutationSnapshot, record *clientMutationRecord) error {
		record.OperationState = clientMutationOperationApplied
		return nil
	})
	if !errors.Is(err, injected) {
		t.Fatalf("effect update error = %v, want %v", err, injected)
	}
	if got := store.snapshot().Journal["mutation-1"].OperationState; got != clientMutationOperationApplied {
		t.Fatalf("in-memory state after successful rename = %q, want applied", got)
	}
	reloaded, err := newClientMutationStoreFS(fs, "/state", "session-1", clientMutationFaults{})
	if err != nil {
		t.Fatalf("reload mutation store: %v", err)
	}
	if got := reloaded.snapshot().Journal["mutation-1"].OperationState; got != clientMutationOperationApplied {
		t.Fatalf("durable state after successful rename = %q, want applied", got)
	}
}

func TestClientMutationPersist_RejectsIncompleteSnapshot(t *testing.T) {
	request := testClientMutationRequest(t, "turn/start", "mutation-1", appwire.TurnStartParams{
		ClientMutationID: "mutation-1",
	})
	valid := newEmptyClientMutationSnapshot("session-1")
	valid.Journal["mutation-1"] = clientMutationRecord{
		ClientMutationID:  request.ClientMutationID,
		Method:            request.Method,
		Payload:           request.Payload,
		PayloadHash:       request.PayloadHash,
		OperationState:    clientMutationOperationInFlight,
		ExecutionState:    "pending",
		ProjectionState:   appwire.MutationProjectionPending,
		AttemptGeneration: 1,
	}
	validJSON, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("marshal valid fixture: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name:   "missing journal",
			mutate: func(snapshot map[string]any) { delete(snapshot, "journal") },
		},
		{
			name:   "null journal",
			mutate: func(snapshot map[string]any) { snapshot["journal"] = nil },
		},
		{
			name:   "missing budget reservations",
			mutate: func(snapshot map[string]any) { delete(snapshot, "budgetReservations") },
		},
		{
			name:   "null budget reservations",
			mutate: func(snapshot map[string]any) { snapshot["budgetReservations"] = nil },
		},
		{
			name:   "missing pending executions",
			mutate: func(snapshot map[string]any) { delete(snapshot, "pendingExecutions") },
		},
		{
			name:   "null pending executions",
			mutate: func(snapshot map[string]any) { snapshot["pendingExecutions"] = nil },
		},
		{
			name: "missing execution state",
			mutate: func(snapshot map[string]any) {
				record := snapshot["journal"].(map[string]any)["mutation-1"].(map[string]any)
				delete(record, "executionState")
			},
		},
		{
			name: "missing projection state",
			mutate: func(snapshot map[string]any) {
				record := snapshot["journal"].(map[string]any)["mutation-1"].(map[string]any)
				delete(record, "projectionState")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fixture map[string]any
			if err := json.Unmarshal(validJSON, &fixture); err != nil {
				t.Fatalf("decode valid fixture: %v", err)
			}
			tt.mutate(fixture)
			data, err := json.Marshal(fixture)
			if err != nil {
				t.Fatalf("marshal invalid fixture: %v", err)
			}

			fs := afero.NewMemMapFs()
			path := clientMutationFilePath("/state", "session-1")
			if err := fs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("create fixture dir: %v", err)
			}
			if err := afero.WriteFile(fs, path, data, 0o644); err != nil {
				t.Fatalf("write invalid fixture: %v", err)
			}
			if _, err := newClientMutationStoreFS(fs, "/state", "session-1", clientMutationFaults{}); err == nil {
				t.Fatal("loaded an incomplete client mutation snapshot")
			}
		})
	}
}

func TestClientMutationPersist_RestoreRejectsMalformedSnapshot(t *testing.T) {
	stateDir := t.TempDir()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(
		client,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(stateDir),
		SessionConfig{StateDir: stateDir},
	)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sessionID := sess.ID()
	sess.Close()

	path := filepath.Join(stateDir, "mutations", sessionID+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"journal":`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	meta, err := schema.LoadSessionMeta(stateDir, sessionID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	restored, err := RestoreSessionFromMetaWithConfig(
		client,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(stateDir),
		meta,
		RestoreSessionConfig{StateDir: stateDir},
	)
	if restored != nil {
		restored.Close()
		t.Fatal("RestoreSessionFromMetaWithConfig returned a session for a malformed mutation snapshot")
	}
	if err == nil {
		t.Fatal("RestoreSessionFromMetaWithConfig accepted a malformed mutation snapshot")
	}
}

func uint64Pointer(value uint64) *uint64 {
	return &value
}

func requestInput(t *testing.T, raw json.RawMessage) []appwire.InputItem {
	t.Helper()
	var params appwire.TurnQueueParams
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("decode request input: %v", err)
	}
	return params.Input
}
