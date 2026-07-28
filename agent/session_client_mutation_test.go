package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

type clientMutationStartLifecycle interface {
	AcceptClientMutationStart(appwire.TurnStartParams) (appwire.TurnStartResponse, error)
	ClaimClientMutationStart() (queuedInput, bool, error)
	SetClientMutationStartWakeFunc(func())
}

type clientMutationInterruptLifecycle interface {
	InterruptClientMutation(
		context.Context,
		appwire.TurnInterruptParams,
		func(),
	) (appwire.TurnInterruptResponse, error)
}

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

func TestClientMutationStore_RejectionIsIsolatedAcrossReplayAndRestart(t *testing.T) {
	fs := afero.NewMemMapFs()
	store, err := newClientMutationStoreFS(fs, "/state", "session-1", clientMutationFaults{})
	if err != nil {
		t.Fatalf("new mutation store: %v", err)
	}
	request := testClientMutationRequest(t, "turn/steer", "mutation-1", appwire.TurnSteerParams{
		ClientMutationID: "mutation-1",
		ExpectedTurnID:   "turn-1",
		Input:            []appwire.InputItem{{Type: "text", Text: "too late"}},
	})
	reserved, err := store.reserve(request)
	if err != nil {
		t.Fatalf("reserve mutation: %v", err)
	}
	originalRejection := &clientMutationRejection{
		Code:    appwire.CodeConflict,
		Message: "expected turn no longer active",
		Data: appwire.ErrorData{
			SerfErrorInfo:    appwire.ErrorConflict,
			ClientMutationID: "mutation-1",
			MutationOutcome:  appwire.MutationOutcomeNotAccepted,
			RetryDisposition: appwire.RetryDispositionNone,
			Cause:            "expected turn no longer active",
		},
	}
	callbackInput := []clientMutationQueueEntry{{
		ID:               "queue-1",
		ClientMutationID: "mutation-1",
		Input: []appwire.InputItem{{
			Type:     "image",
			Data:     []byte{1, 2, 3},
			Metadata: map[string]string{"source": "original"},
		}},
	}}
	if err := store.update(reserved.Lease, func(snapshot *clientMutationSnapshot, record *clientMutationRecord) error {
		snapshot.InputQueue = callbackInput
		record.OperationState = clientMutationOperationRejected
		record.Rejection = originalRejection
		return nil
	}); err != nil {
		t.Fatalf("commit rejection: %v", err)
	}

	originalRejection.Data.Cause = "mutated by callback owner"
	callbackInput[0].Input[0].Data[0] = 9
	callbackInput[0].Input[0].Metadata["source"] = "mutated"
	storedInput := store.snapshot().InputQueue[0].Input[0]
	if !reflect.DeepEqual(storedInput.Data, []byte{1, 2, 3}) || storedInput.Metadata["source"] != "original" {
		t.Fatalf("stored callback input = %#v, want isolated original input", storedInput)
	}
	firstReplay, err := store.reserve(request)
	if err != nil {
		t.Fatalf("first rejection replay: %v", err)
	}
	if got := firstReplay.Record.Rejection.Data.Cause; got != "expected turn no longer active" {
		t.Fatalf("first replay cause = %v, want original cause", got)
	}

	firstReplay.Record.Rejection.Data.Cause = "mutated by replay caller"
	secondReplay, err := store.reserve(request)
	if err != nil {
		t.Fatalf("second rejection replay: %v", err)
	}
	if got := secondReplay.Record.Rejection.Data.Cause; got != "expected turn no longer active" {
		t.Fatalf("second replay cause = %v, want original cause", got)
	}

	restarted, err := newClientMutationStoreFS(fs, "/state", "session-1", clientMutationFaults{})
	if err != nil {
		t.Fatalf("restart mutation store: %v", err)
	}
	restartReplay, err := restarted.reserve(request)
	if err != nil {
		t.Fatalf("restart rejection replay: %v", err)
	}
	wantRejection := &clientMutationRejection{
		Code:    appwire.CodeConflict,
		Message: "expected turn no longer active",
		Data: appwire.ErrorData{
			SerfErrorInfo:    appwire.ErrorConflict,
			ClientMutationID: "mutation-1",
			MutationOutcome:  appwire.MutationOutcomeNotAccepted,
			RetryDisposition: appwire.RetryDispositionNone,
			Cause:            "expected turn no longer active",
		},
	}
	if !reflect.DeepEqual(restartReplay.Record.Rejection, wantRejection) {
		t.Fatalf("restart replay rejection = %#v, want %#v", restartReplay.Record.Rejection, wantRejection)
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

func TestClientMutation_StartAcceptedRecordRestoresAsRunnableStart(t *testing.T) {
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	params := appwire.TurnStartParams{
		ClientMutationID: "start-accepted-recovery",
		Input: []appwire.InputItem{
			{Type: "text", Text: "resume this exact start"},
			{Type: "image", MediaType: "image/png", Data: []byte("start-image"), Name: "start.png"},
		},
	}
	request := testClientMutationRequest(t, "turn/start", params.ClientMutationID, params)
	if err := sess.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.NextTurnSequence = 1
		snapshot.Journal[params.ClientMutationID] = clientMutationRecord{
			ClientMutationID:  params.ClientMutationID,
			Method:            "turn/start",
			Payload:           append(json.RawMessage(nil), request.Payload...),
			StableTurnID:      "turn_1",
			PayloadHash:       request.PayloadHash,
			OperationState:    clientMutationOperationApplied,
			ExecutionState:    "accepted",
			ProjectionState:   appwire.MutationProjectionReflected,
			AttemptGeneration: 1,
		}
		snapshot.BudgetReservations[params.ClientMutationID] = clientMutationBudgetReservation{
			TurnID: "turn_1",
			Slots:  1,
		}
		snapshot.PendingExecutions[params.ClientMutationID] = appwire.PendingMutation{
			ClientMutationID: params.ClientMutationID,
			Method:           "turn/start",
			Input:            cloneClientMutationInput(params.Input),
			ExecutionState:   "accepted",
			TurnID:           "turn_1",
			ProjectionState:  appwire.MutationProjectionReflected,
		}
		return nil
	}); err != nil {
		t.Fatalf("seed accepted start: %v", err)
	}
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()

	lifecycle, ok := any(restored).(clientMutationStartLifecycle)
	if !ok {
		t.Fatal("session has no durable start lifecycle")
	}
	wakes := 0
	lifecycle.SetClientMutationStartWakeFunc(func() { wakes++ })
	if wakes != 1 {
		t.Fatalf("restored accepted start wake count = %d, want 1", wakes)
	}
	if got := restored.WireState(); got != string(SessionProcessing) {
		t.Fatalf("restored accepted start wire state = %q, want runnable %q", got, SessionProcessing)
	}
	if got := restored.SteeringQueueSnapshot(); len(got) != 0 {
		t.Fatalf("restored accepted start was projected as steering: %#v", got)
	}
	pending := restored.clientMutations.snapshot().PendingExecutions[params.ClientMutationID]
	if pending.ExecutionState != "accepted" ||
		pending.TurnID != "turn_1" ||
		!reflect.DeepEqual(pending.Input, params.Input) {
		t.Fatalf("restored accepted start = %#v, want complete runnable input with stable identity", pending)
	}
	claimed, ok, err := lifecycle.ClaimClientMutationStart()
	if err != nil {
		t.Fatalf("ClaimClientMutationStart after restore: %v", err)
	}
	if !ok ||
		claimed.ClientMutationID != params.ClientMutationID ||
		claimed.StableTurnID != "turn_1" ||
		claimed.Text != "resume this exact start" ||
		len(claimed.Images) != 1 {
		t.Fatalf("restored claimed start = %#v, ok=%v", claimed, ok)
	}
}

func TestClientMutation_StartAcceptanceOwnsRunnableInputBeforeWake(t *testing.T) {
	sess := newTestSession(t)
	lifecycle, ok := any(sess).(clientMutationStartLifecycle)
	if !ok {
		t.Fatal("session has no durable start lifecycle")
	}
	params := appwire.TurnStartParams{
		ClientMutationID: "start-before-wake",
		Input: []appwire.InputItem{
			{Type: "text", Text: "run after durable acceptance"},
			{Type: "image", MediaType: "image/png", Data: []byte("start-image"), Name: "start.png"},
		},
	}
	var atWake clientMutationSnapshot
	wakes := 0
	lifecycle.SetClientMutationStartWakeFunc(func() {
		wakes++
		atWake = sess.clientMutations.snapshot()
	})

	response, err := lifecycle.AcceptClientMutationStart(params)
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	if wakes != 1 {
		t.Fatalf("wake count = %d, want 1", wakes)
	}
	record := atWake.Journal[params.ClientMutationID]
	pending := atWake.PendingExecutions[params.ClientMutationID]
	if record.OperationState != clientMutationOperationApplied ||
		record.ExecutionState != "accepted" ||
		record.StableTurnID == "" ||
		pending.ExecutionState != "accepted" ||
		pending.TurnID != record.StableTurnID ||
		!reflect.DeepEqual(pending.Input, params.Input) {
		t.Fatalf("state visible to wake = record %#v pending %#v", record, pending)
	}
	reservation, reserved := atWake.BudgetReservations[params.ClientMutationID]
	if !reserved || reservation.TurnID != record.StableTurnID || reservation.Slots != 1 {
		t.Fatalf("budget reservation visible to wake = %#v, present=%v", reservation, reserved)
	}
	if atWake.ActiveTurnID != record.StableTurnID ||
		response.Turn.ID != record.StableTurnID ||
		response.Receipt.TurnID != record.StableTurnID {
		t.Fatalf("stable turn identities: active=%q turn=%q receipt=%q record=%q",
			atWake.ActiveTurnID, response.Turn.ID, response.Receipt.TurnID, record.StableTurnID)
	}

	replayed, err := lifecycle.AcceptClientMutationStart(params)
	if err != nil {
		t.Fatalf("replay accepted start: %v", err)
	}
	if replayed.Receipt.Disposition != appwire.MutationDispositionReplayed ||
		replayed.Turn.ID != response.Turn.ID {
		t.Fatalf("replayed start = %#v, want same turn with replayed receipt", replayed)
	}
	if wakes != 2 {
		t.Fatalf("wake count after replayed still-runnable start = %d, want 2", wakes)
	}

	claimed, ok, err := lifecycle.ClaimClientMutationStart()
	if err != nil {
		t.Fatalf("ClaimClientMutationStart: %v", err)
	}
	if !ok ||
		claimed.ClientMutationID != params.ClientMutationID ||
		claimed.StableTurnID != record.StableTurnID ||
		claimed.Text != "run after durable acceptance" ||
		len(claimed.Images) != 1 {
		t.Fatalf("claimed start = %#v, ok=%v", claimed, ok)
	}
	claimedSnapshot := sess.clientMutations.snapshot()
	if got := claimedSnapshot.PendingExecutions[params.ClientMutationID].ExecutionState; got != "claimed" {
		t.Fatalf("claimed execution state = %q, want claimed", got)
	}
	if _, reserved := claimedSnapshot.BudgetReservations[params.ClientMutationID]; reserved {
		t.Fatal("claimed start retained its budget reservation")
	}
	if claimedSnapshot.AcceptedTurns != 1 {
		t.Fatalf("accepted turns after claim = %d, want 1", claimedSnapshot.AcceptedTurns)
	}
}

func TestClientMutation_ProcessStartUsesDurablePayloadAndIdentity(t *testing.T) {
	sess := newSession(t,
		withSteps(func(llm.Request) llm.Response { return finalResponse("done") }),
		withConfig(SessionConfig{
			MaxSubagentDepth: 1,
			NoProjectPrompts: true,
			testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		}),
	)
	params := appwire.TurnStartParams{
		ClientMutationID: "process-durable-start",
		Input: []appwire.InputItem{
			{Type: "text", Text: "process this durable input"},
			{Type: "image", MediaType: "image/png", Data: []byte("image-bytes"), Name: "input.png"},
		},
	}
	accepted, err := sess.AcceptClientMutationStart(params)
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}

	result, claimed, err := sess.ProcessClientMutationStart(context.Background(), nil)
	if err != nil {
		t.Fatalf("ProcessClientMutationStart: %v", err)
	}
	if !claimed {
		t.Fatal("ProcessClientMutationStart reported no durable start")
	}
	if result != "done" {
		t.Fatalf("ProcessClientMutationStart result = %q, want done", result)
	}

	sess.mu.Lock()
	var userTurn *schema.Turn
	for i := range sess.history {
		if sess.history[i].ClientMutationID == params.ClientMutationID &&
			sess.history[i].Kind == schema.TurnUserInput {
			turn := sess.history[i]
			userTurn = &turn
			break
		}
	}
	sess.mu.Unlock()
	if userTurn == nil {
		t.Fatal("durable start produced no identity-bearing user turn")
	}
	if userTurn.StableTurnID != accepted.Turn.ID {
		t.Fatalf("user turn stable ID = %q, want %q", userTurn.StableTurnID, accepted.Turn.ID)
	}
	if userTurn.Message.Role != llm.RoleUser {
		t.Fatalf("user turn role = %q, want %q", userTurn.Message.Role, llm.RoleUser)
	}
	if len(userTurn.Message.Content) != 2 {
		t.Fatalf("user turn content = %#v, want text and image parts", userTurn.Message.Content)
	}
	textPart := userTurn.Message.Content[0]
	if textPart.Kind != llm.ContentText || textPart.Text != "process this durable input" {
		t.Fatalf("user turn text part = %#v, want durable text", textPart)
	}
	imagePart := userTurn.Message.Content[1]
	if imagePart.Kind != llm.ContentImage || imagePart.Image == nil {
		t.Fatalf("user turn image part = %#v, want durable image", imagePart)
	}
	if imagePart.Image.MediaType != "image/png" || string(imagePart.Image.Data) != "image-bytes" {
		t.Fatalf("user turn image = %#v, want durable PNG bytes", imagePart.Image)
	}
}

func TestClientMutation_StartCrashAfterReservationRecoversCompleteIntent(t *testing.T) {
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	params := appwire.TurnStartParams{
		ClientMutationID: "start-reservation-crash",
		Input: []appwire.InputItem{
			{Type: "text", Text: "recover complete reservation"},
			{Type: "image", MediaType: "image/png", Data: []byte("reserved-image"), Name: "reserved.png"},
		},
	}
	injected := errors.New("crash after start reservation")
	sess.clientMutations.faults.AfterReservation = func() error { return injected }
	if _, err := sess.AcceptClientMutationStart(params); !errors.Is(err, injected) {
		t.Fatalf("AcceptClientMutationStart error = %v, want %v", err, injected)
	}
	reserved := sess.clientMutations.snapshot()
	record := reserved.Journal[params.ClientMutationID]
	if record.OperationState != clientMutationOperationInFlight ||
		record.StableTurnID == "" ||
		record.AttemptGeneration != 1 {
		t.Fatalf("reserved start record = %#v", record)
	}
	if reservation, ok := reserved.BudgetReservations[params.ClientMutationID]; !ok ||
		reservation.TurnID != record.StableTurnID ||
		reservation.Slots != 1 {
		t.Fatalf("reserved start budget = %#v, ok=%v", reservation, ok)
	}
	var payload appwire.TurnStartParams
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		t.Fatalf("decode reserved start payload: %v", err)
	}
	if !reflect.DeepEqual(payload.Input, params.Input) {
		t.Fatalf("reserved start input = %#v, want %#v", payload.Input, params.Input)
	}
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	response, err := restored.AcceptClientMutationStart(params)
	if err != nil {
		t.Fatalf("recover reserved start: %v", err)
	}
	if response.Turn.ID != record.StableTurnID {
		t.Fatalf("recovered turn ID = %q, want %q", response.Turn.ID, record.StableTurnID)
	}
	recovered := restored.clientMutations.snapshot()
	if recovered.Journal[params.ClientMutationID].AttemptGeneration != 2 ||
		!reflect.DeepEqual(recovered.PendingExecutions[params.ClientMutationID].Input, params.Input) {
		t.Fatalf("recovered start snapshot = %#v", recovered)
	}
}

func TestClientMutation_StartClaimedWithoutTranscriptRestoresRunnableSameTurn(t *testing.T) {
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	params := appwire.TurnStartParams{
		ClientMutationID: "start-claimed-recovery",
		Input:            []appwire.InputItem{{Type: "text", Text: "resume claimed start"}},
	}
	started, err := sess.AcceptClientMutationStart(params)
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	claimed, ok, err := sess.ClaimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("ClaimClientMutationStart: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	snapshot := restored.clientMutations.snapshot()
	pending := snapshot.PendingExecutions[params.ClientMutationID]
	if pending.Method != clientMutationMethodStart ||
		pending.ExecutionState != "accepted" ||
		pending.TurnID != started.Turn.ID ||
		!reflect.DeepEqual(pending.Input, params.Input) {
		t.Fatalf("restored claimed start = %#v", pending)
	}
	if _, reserved := snapshot.BudgetReservations[params.ClientMutationID]; !reserved {
		t.Fatal("restored claimed start has no turn-budget reservation")
	}
	if snapshot.AcceptedTurns != 0 {
		t.Fatalf("accepted turns after returning claimed start = %d, want 0", snapshot.AcceptedTurns)
	}
	reclaimed, ok, err := restored.ClaimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("reclaim restored start: claimed=%#v ok=%v err=%v", reclaimed, ok, err)
	}
	if reclaimed.ClientMutationID != params.ClientMutationID ||
		reclaimed.StableTurnID != started.Turn.ID ||
		reclaimed.Text != "resume claimed start" {
		t.Fatalf("reclaimed start = %#v, want original logical turn", reclaimed)
	}
}

func TestClientMutation_StartClaimedWithTranscriptRestoresRunnableWithoutDuplicateAppend(t *testing.T) {
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	params := appwire.TurnStartParams{
		ClientMutationID: "start-incorporated-recovery",
		Input:            []appwire.InputItem{{Type: "text", Text: "resume after append"}},
	}
	started, err := sess.AcceptClientMutationStart(params)
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	claimed, ok, err := sess.ClaimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("ClaimClientMutationStart: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	turn := schema.NewTurn(schema.TurnUserInput, buildUserInputMessage(claimed.Text, claimed.Images))
	turn.ClientMutationID = claimed.ClientMutationID
	turn.StableTurnID = claimed.StableTurnID
	if err := sess.appendClientMutationTranscript(turn); err != nil {
		t.Fatalf("append claimed start: %v", err)
	}
	sess.history = append(sess.history, turn)
	if err := sess.markClaimedUserTranscriptIncorporated(claimed.ClientMutationID); err != nil {
		t.Fatalf("mark claimed start incorporated: %v", err)
	}
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	snapshot := restored.clientMutations.snapshot()
	pending, ok := snapshot.PendingExecutions[params.ClientMutationID]
	if !ok ||
		pending.Method != clientMutationMethodStart ||
		pending.ExecutionState != "incorporated" ||
		pending.TurnID != started.Turn.ID ||
		!reflect.DeepEqual(pending.Input, params.Input) {
		t.Fatalf("restored incorporated start = %#v, ok=%v", pending, ok)
	}
	if snapshot.Journal[params.ClientMutationID].OperationState == clientMutationOperationTerminal {
		t.Fatal("restored incorporated start was terminalized as steering")
	}
	if len(snapshot.SteeringOrder) != 0 || len(restored.SteeringQueueSnapshot()) != 0 {
		t.Fatalf("restored start entered steering projection: order=%v queue=%v",
			snapshot.SteeringOrder, restored.SteeringQueueSnapshot())
	}
	reclaimed, ok, err := restored.ClaimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claim incorporated start: claimed=%#v ok=%v err=%v", reclaimed, ok, err)
	}
	if reclaimed.ClientMutationID != params.ClientMutationID ||
		reclaimed.StableTurnID != started.Turn.ID {
		t.Fatalf("incorporated recovery claim = %#v", reclaimed)
	}
	before := countClientMutationTranscriptTurns(t, restored, params.ClientMutationID)
	if err := restored.acceptUserInput(
		withQueuedClientMutation(context.Background(), reclaimed),
		reclaimed.Text,
		reclaimed.Images,
		nil,
		false,
	); err != nil {
		t.Fatalf("resume incorporated start: %v", err)
	}
	after := countClientMutationTranscriptTurns(t, restored, params.ClientMutationID)
	if after != before {
		t.Fatalf("incorporated recovery appended duplicate transcript item: before=%d after=%d", before, after)
	}
}

func TestClientMutation_IncorporationFailureWritesOneFailedIdentityBearingUserItem(t *testing.T) {
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	params := appwire.TurnStartParams{
		ClientMutationID: "start-incorporation-failure",
		Input:            []appwire.InputItem{{Type: "text", Text: "preserve failed start"}},
	}
	started, err := sess.AcceptClientMutationStart(params)
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	claimed, ok, err := sess.ClaimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("ClaimClientMutationStart: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	injected := errors.New("deterministic pre-append failure")
	sess.clientMutationPreAppendFailure = func(schema.Turn) error { return injected }

	err = sess.acceptUserInput(
		withQueuedClientMutation(context.Background(), claimed),
		claimed.Text,
		claimed.Images,
		nil,
		false,
	)
	if !errors.Is(err, injected) {
		t.Fatalf("acceptUserInput error = %v, want %v", err, injected)
	}
	snapshot := sess.clientMutations.snapshot()
	record := snapshot.Journal[params.ClientMutationID]
	if record.OperationState != clientMutationOperationTerminal ||
		record.ExecutionState != "failed" ||
		record.ProjectionState != appwire.MutationProjectionReflected ||
		len(record.Payload) != 0 {
		t.Fatalf("failed mutation record = %#v", record)
	}
	if _, ok := snapshot.PendingExecutions[params.ClientMutationID]; ok {
		t.Fatal("failed mutation remained runnable")
	}
	if snapshot.ActiveTurnID != "" {
		t.Fatalf("active turn after deterministic failure = %q", snapshot.ActiveTurnID)
	}
	var liveUser, liveFailure int
	for {
		select {
		case event := <-sess.Events():
			switch event.Kind {
			case events.EventUserInput:
				data, _ := event.Data.(events.UserInputData)
				if data.ClientMutationID == params.ClientMutationID && data.StableTurnID == started.Turn.ID {
					liveUser++
				}
			case events.EventError:
				liveFailure++
			}
		default:
			if liveUser != 1 || liveFailure != 1 {
				t.Fatalf("live failed projection: user=%d error=%d, want 1 each", liveUser, liveFailure)
			}
			goto eventsDrained
		}
	}
eventsDrained:
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	var userTurns, failureTurns int
	for _, turn := range restored.history {
		if turn.ClientMutationID != params.ClientMutationID {
			continue
		}
		if turn.StableTurnID != started.Turn.ID {
			t.Fatalf("failed transcript stable turn = %q, want %q", turn.StableTurnID, started.Turn.ID)
		}
		switch turn.Kind {
		case schema.TurnUserInput:
			userTurns++
		case schema.TurnFailure:
			failureTurns++
			if turn.Error == nil || turn.Error.Message == "" {
				t.Fatalf("failure turn has no diagnostic: %#v", turn)
			}
		}
	}
	if userTurns != 1 || failureTurns != 1 {
		t.Fatalf("failed transcript items: user=%d failure=%d, want 1 each", userTurns, failureTurns)
	}
	replayed, err := restored.AcceptClientMutationStart(params)
	if err != nil {
		t.Fatalf("replay failed start receipt: %v", err)
	}
	if replayed.Receipt.Disposition != appwire.MutationDispositionReplayed ||
		replayed.Turn.ID != started.Turn.ID {
		t.Fatalf("failed start replay = %#v", replayed)
	}
}

func TestClientMutation_StartTranscriptIOFailureRemainsRunnable(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	params := appwire.TurnStartParams{
		ClientMutationID: "start-transcript-io-failure",
		Input:            []appwire.InputItem{{Type: "text", Text: "retry durable append"}},
	}
	started, err := sess.AcceptClientMutationStart(params)
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	claimed, ok, err := sess.ClaimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("ClaimClientMutationStart: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	injected := errors.New("transcript storage unavailable")
	sess.clientMutationTranscriptAppend = func(schema.Turn) error { return injected }
	err = sess.acceptUserInput(
		withQueuedClientMutation(context.Background(), claimed),
		claimed.Text,
		claimed.Images,
		nil,
		false,
	)
	if !errors.Is(err, injected) {
		t.Fatalf("acceptUserInput error = %v, want %v", err, injected)
	}
	snapshot := sess.clientMutations.snapshot()
	pending := snapshot.PendingExecutions[params.ClientMutationID]
	if pending.ExecutionState != "accepted" || pending.TurnID != started.Turn.ID {
		t.Fatalf("start after transcript I/O failure = %#v", pending)
	}
	if _, ok := snapshot.BudgetReservations[params.ClientMutationID]; !ok {
		t.Fatal("transcript I/O failure lost start budget reservation")
	}
	if snapshot.Journal[params.ClientMutationID].OperationState != clientMutationOperationApplied {
		t.Fatalf("transcript I/O failure terminalized start: %#v", snapshot.Journal[params.ClientMutationID])
	}
}

func TestClientMutation_StartTranscriptIOFailureProcessPathRemainsRunnable(t *testing.T) {
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(&fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return finalResponse("must not run") },
		},
	})
	sess, err := NewSession(
		client,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(dir),
		SessionConfig{StateDir: dir},
	)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	params := appwire.TurnStartParams{
		ClientMutationID: "start-process-transcript-io-failure",
		Input:            []appwire.InputItem{{Type: "text", Text: "retry whole process path"}},
	}
	started, err := sess.AcceptClientMutationStart(params)
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	claimed, ok, err := sess.ClaimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("ClaimClientMutationStart: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	injected := errors.New("transcript storage unavailable")
	sess.clientMutationTranscriptAppend = func(schema.Turn) error { return injected }
	_, err = sess.processInputKindWithProvenance(
		withQueuedClientMutation(context.Background(), claimed),
		claimed.Text,
		claimed.Images,
		EntryUserInput,
		nil,
	)
	if !errors.Is(err, injected) {
		t.Fatalf("process claimed start error = %v, want %v", err, injected)
	}
	snapshot := sess.clientMutations.snapshot()
	pending := snapshot.PendingExecutions[params.ClientMutationID]
	if pending.ExecutionState != "accepted" || pending.TurnID != started.Turn.ID {
		t.Fatalf("start after process-level transcript I/O failure = %#v", pending)
	}
	if snapshot.Journal[params.ClientMutationID].OperationState != clientMutationOperationApplied {
		t.Fatalf("process-level transcript I/O failure terminalized start: %#v", snapshot.Journal[params.ClientMutationID])
	}
}

func TestClientMutation_StartLifecycleTerminalizesAfterRunnerCompletion(t *testing.T) {
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(&fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return finalResponse("done") },
		},
	})
	sess, err := NewSession(
		client,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(dir),
		SessionConfig{StateDir: dir},
	)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	params := appwire.TurnStartParams{
		ClientMutationID: "start-runner-terminal",
		Input:            []appwire.InputItem{{Type: "text", Text: "finish this start"}},
	}
	started, err := sess.AcceptClientMutationStart(params)
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	claimed, ok, err := sess.ClaimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("ClaimClientMutationStart: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	ctx := withQueuedClientMutation(context.Background(), claimed)
	if _, err := sess.processInputKindWithProvenance(
		ctx,
		claimed.Text,
		claimed.Images,
		EntryUserInput,
		nil,
	); err != nil {
		t.Fatalf("process claimed start: %v", err)
	}
	snapshot := sess.clientMutations.snapshot()
	record := snapshot.Journal[params.ClientMutationID]
	if record.OperationState != clientMutationOperationTerminal ||
		record.ExecutionState != "terminal" ||
		len(record.Payload) != 0 {
		t.Fatalf("completed start record = %#v", record)
	}
	if snapshot.ActiveTurnID != "" {
		t.Fatalf("active turn after completion = %q", snapshot.ActiveTurnID)
	}
	if _, pending := snapshot.PendingExecutions[params.ClientMutationID]; pending {
		t.Fatal("completed start remained pending")
	}
	replayed, err := sess.AcceptClientMutationStart(params)
	if err != nil {
		t.Fatalf("replay completed start: %v", err)
	}
	if replayed.Receipt.Disposition != appwire.MutationDispositionReplayed ||
		replayed.Turn.ID != started.Turn.ID {
		t.Fatalf("completed start replay = %#v", replayed)
	}
}

func TestClientMutation_IncorporationFailureCrashBoundariesRecoverWithoutDuplicates(t *testing.T) {
	for _, boundary := range []string{"after_user", "after_failure"} {
		t.Run(boundary, func(t *testing.T) {
			dir := t.TempDir()
			sess := newQueuePersistTestSession(t, dir)
			id := sess.ID()
			params := appwire.TurnStartParams{
				ClientMutationID: "start-failure-crash-" + boundary,
				Input:            []appwire.InputItem{{Type: "text", Text: "recover failed start"}},
			}
			started, err := sess.AcceptClientMutationStart(params)
			if err != nil {
				t.Fatalf("AcceptClientMutationStart: %v", err)
			}
			claimed, ok, err := sess.ClaimClientMutationStart()
			if err != nil || !ok {
				t.Fatalf("ClaimClientMutationStart: claimed=%#v ok=%v err=%v", claimed, ok, err)
			}
			failure := errors.New("deterministic pre-append failure")
			crash := errors.New("simulated crash at " + boundary)
			sess.clientMutationPreAppendFailure = func(schema.Turn) error { return failure }
			sess.clientMutationFailureRecoveryFault = func(got string) error {
				if got == boundary {
					return crash
				}
				return nil
			}
			err = sess.acceptUserInput(
				withQueuedClientMutation(context.Background(), claimed),
				claimed.Text,
				claimed.Images,
				nil,
				false,
			)
			if !errors.Is(err, failure) || !errors.Is(err, crash) {
				t.Fatalf("acceptUserInput error = %v, want failure and crash", err)
			}
			record := sess.clientMutations.snapshot().Journal[params.ClientMutationID]
			if record.ExecutionState != "failureRecording" || record.Failure == nil {
				t.Fatalf("crash did not retain failure intent: %#v", record)
			}
			sess.Close()

			restored := restoreQueuePersistTestSession(t, dir, id)
			defer restored.Close()
			var userTurns, failureTurns int
			for _, turn := range restored.history {
				if turn.ClientMutationID != params.ClientMutationID {
					continue
				}
				if turn.StableTurnID != started.Turn.ID {
					t.Fatalf("recovered stable turn = %q, want %q", turn.StableTurnID, started.Turn.ID)
				}
				switch turn.Kind {
				case schema.TurnUserInput:
					userTurns++
				case schema.TurnFailure:
					failureTurns++
				}
			}
			if userTurns != 1 || failureTurns != 1 {
				t.Fatalf("recovered failed transcript items: user=%d failure=%d", userTurns, failureTurns)
			}
			recovered := restored.clientMutations.snapshot()
			if recovered.Journal[params.ClientMutationID].ExecutionState != "failed" {
				t.Fatalf("recovered failure record = %#v", recovered.Journal[params.ClientMutationID])
			}
			if _, pending := recovered.PendingExecutions[params.ClientMutationID]; pending {
				t.Fatal("recovered failure remained pending")
			}
		})
	}
}

func TestClientMutation_InterruptWaitReleasesSerializerAndRunnerTerminalizesFence(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	params := appwire.TurnStartParams{
		ClientMutationID: "start-for-interrupt",
		Input:            []appwire.InputItem{{Type: "text", Text: "interrupt this"}},
	}
	started, err := sess.AcceptClientMutationStart(params)
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	claimed, ok, err := sess.ClaimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("ClaimClientMutationStart: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	if err := sess.acceptUserInput(
		withQueuedClientMutation(context.Background(), claimed),
		claimed.Text,
		claimed.Images,
		nil,
		false,
	); err != nil {
		t.Fatalf("incorporate start: %v", err)
	}
	lifecycle, ok := any(sess).(clientMutationInterruptLifecycle)
	if !ok {
		t.Fatal("session has no durable interrupt lifecycle")
	}
	interrupt := appwire.TurnInterruptParams{
		ClientMutationID: "interrupt-barrier",
		ExpectedTurnID:   started.Turn.ID,
	}
	cancelSignaled := make(chan struct{})
	serializerReleased := make(chan bool, 1)
	releaseWait := make(chan struct{})
	responseCh := make(chan appwire.TurnInterruptResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		response, err := lifecycle.InterruptClientMutation(context.Background(), interrupt, func() {
			if sess.clientMutations.mu.TryLock() {
				sess.clientMutations.mu.Unlock()
				serializerReleased <- true
			} else {
				serializerReleased <- false
			}
			close(cancelSignaled)
			<-releaseWait
		})
		responseCh <- response
		errCh <- err
	}()
	<-cancelSignaled

	if !<-serializerReleased {
		t.Fatal("interrupt cancellation/wait callback ran while serializer was held")
	}
	retryResponseCh := make(chan appwire.TurnInterruptResponse, 1)
	retryErrCh := make(chan error, 1)
	unexpectedRetryCancel := make(chan struct{}, 1)
	retryJoined := make(chan struct{})
	sess.clientMutationInterruptJoined = func() { close(retryJoined) }
	go func() {
		response, err := lifecycle.InterruptClientMutation(context.Background(), interrupt, func() {
			unexpectedRetryCancel <- struct{}{}
		})
		retryResponseCh <- response
		retryErrCh <- err
	}()
	<-retryJoined
	queuedDuringFence := appwire.TurnQueueParams{
		ClientMutationID: "queue-during-interrupt-fence",
		ExpectedTurnID:   started.Turn.ID,
		Input:            []appwire.InputItem{{Type: "text", Text: "must not pass fence"}},
	}
	if _, err := sess.clientMutationQueue(queuedDuringFence); err == nil {
		t.Fatal("queue mutation passed an active interrupt fence")
	}
	if err := sess.completeClientMutationInterruptedTurn(params.ClientMutationID); err != nil {
		t.Fatalf("runner terminal transition: %v", err)
	}
	terminalWhileWaiting := sess.clientMutations.snapshot()
	if terminalWhileWaiting.InterruptFence != nil ||
		terminalWhileWaiting.ActiveTurnID != "" ||
		terminalWhileWaiting.Journal[params.ClientMutationID].ExecutionState != "interrupted" ||
		terminalWhileWaiting.Journal[interrupt.ClientMutationID].OperationState != clientMutationOperationTerminal {
		t.Fatalf("runner did not atomically terminalize interrupt while RPC waited: %#v", terminalWhileWaiting)
	}
	close(releaseWait)
	if err := <-errCh; err != nil {
		t.Fatalf("InterruptClientMutation: %v", err)
	}
	response := <-responseCh
	if response.Receipt.Disposition != appwire.MutationDispositionApplied ||
		response.Receipt.TurnID != started.Turn.ID {
		t.Fatalf("interrupt response = %#v", response)
	}
	if err := <-retryErrCh; err != nil {
		t.Fatalf("same-ID joined retry: %v", err)
	}
	retryResponse := <-retryResponseCh
	if retryResponse.Receipt.Disposition != appwire.MutationDispositionReplayed ||
		retryResponse.Receipt.TurnID != started.Turn.ID {
		t.Fatalf("same-ID joined retry = %#v", retryResponse)
	}
	select {
	case <-unexpectedRetryCancel:
		t.Fatal("same-ID joined retry signaled cancellation again")
	default:
	}
	snapshot := sess.clientMutations.snapshot()
	if snapshot.InterruptFence != nil || snapshot.ActiveTurnID != "" {
		t.Fatalf("terminal interrupt state: fence=%#v active=%q", snapshot.InterruptFence, snapshot.ActiveTurnID)
	}
	if snapshot.Journal[params.ClientMutationID].ExecutionState != "interrupted" {
		t.Fatalf("target record = %#v", snapshot.Journal[params.ClientMutationID])
	}
	interruptRecord := snapshot.Journal[interrupt.ClientMutationID]
	if interruptRecord.OperationState != clientMutationOperationTerminal ||
		interruptRecord.ExecutionState != "interrupted" {
		t.Fatalf("interrupt record = %#v", interruptRecord)
	}
	if len(snapshot.InputQueue) != 0 {
		t.Fatalf("incompatible queue mutation crossed interrupt fence: %#v", snapshot.InputQueue)
	}
	if sess.clientMutations.interruptCallbackCompleted(interrupt.ClientMutationID) {
		t.Fatal("runner-terminalized interrupt retained its callback completion marker")
	}
	replayed, err := lifecycle.InterruptClientMutation(context.Background(), interrupt, func() {
		t.Fatal("replayed interrupt signaled cancellation again")
	})
	if err != nil {
		t.Fatalf("replay interrupt: %v", err)
	}
	if replayed.Receipt.Disposition != appwire.MutationDispositionReplayed ||
		replayed.Receipt.TurnID != started.Turn.ID {
		t.Fatalf("replayed interrupt = %#v", replayed)
	}
}

func TestClientMutation_InterruptCrashAfterFenceRecoversTerminalReceipt(t *testing.T) {
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	start := appwire.TurnStartParams{
		ClientMutationID: "start-before-interrupt-crash",
		Input:            []appwire.InputItem{{Type: "text", Text: "interrupt across restart"}},
	}
	started, err := sess.AcceptClientMutationStart(start)
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	claimed, ok, err := sess.ClaimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("ClaimClientMutationStart: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	if err := sess.acceptUserInput(
		withQueuedClientMutation(context.Background(), claimed),
		claimed.Text,
		claimed.Images,
		nil,
		false,
	); err != nil {
		t.Fatalf("incorporate start: %v", err)
	}
	lifecycle := any(sess).(clientMutationInterruptLifecycle)
	injected := errors.New("crash after interrupt fence")
	sess.clientMutations.faults.AfterReservation = func() error { return injected }
	interrupt := appwire.TurnInterruptParams{
		ClientMutationID: "interrupt-crash-after-fence",
		ExpectedTurnID:   started.Turn.ID,
	}
	canceled := false
	if _, err := lifecycle.InterruptClientMutation(context.Background(), interrupt, func() {
		canceled = true
	}); !errors.Is(err, injected) {
		t.Fatalf("InterruptClientMutation error = %v, want %v", err, injected)
	}
	if canceled {
		t.Fatal("interrupt signaled cancellation after simulated handler crash")
	}
	fenced := sess.clientMutations.snapshot()
	if fenced.InterruptFence == nil ||
		fenced.InterruptFence.ClientMutationID != interrupt.ClientMutationID ||
		fenced.Journal[interrupt.ClientMutationID].OperationState != clientMutationOperationInFlight {
		t.Fatalf("durable crash fence = %#v", fenced)
	}
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	recovered := restored.clientMutations.snapshot()
	if recovered.InterruptFence != nil || recovered.ActiveTurnID != "" {
		t.Fatalf("recovered interrupt fence=%#v active=%q", recovered.InterruptFence, recovered.ActiveTurnID)
	}
	if recovered.Journal[start.ClientMutationID].ExecutionState != "interrupted" {
		t.Fatalf("recovered target = %#v", recovered.Journal[start.ClientMutationID])
	}
	interruptRecord := recovered.Journal[interrupt.ClientMutationID]
	if interruptRecord.OperationState != clientMutationOperationTerminal ||
		interruptRecord.ExecutionState != "interrupted" {
		t.Fatalf("recovered interrupt = %#v", interruptRecord)
	}
	replayed, err := restored.InterruptClientMutation(context.Background(), interrupt, func() {
		t.Fatal("recovered interrupt replay signaled cancellation")
	})
	if err != nil {
		t.Fatalf("replay recovered interrupt: %v", err)
	}
	if replayed.Receipt.Disposition != appwire.MutationDispositionReplayed ||
		replayed.Receipt.TurnID != started.Turn.ID {
		t.Fatalf("recovered interrupt replay = %#v", replayed)
	}
}

func TestClientMutation_InterruptReservationFaultSameProcessRetryTakesOverAndCancels(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	start := appwire.TurnStartParams{
		ClientMutationID: "start-before-interrupt-takeover",
		Input:            []appwire.InputItem{{Type: "text", Text: "cancel on takeover"}},
	}
	started, err := sess.AcceptClientMutationStart(start)
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	claimed, ok, err := sess.ClaimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("ClaimClientMutationStart: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	if err := sess.acceptUserInput(
		withQueuedClientMutation(context.Background(), claimed),
		claimed.Text,
		claimed.Images,
		nil,
		false,
	); err != nil {
		t.Fatalf("incorporate start: %v", err)
	}
	injected := errors.New("reservation owner lost")
	faulted := false
	sess.clientMutations.faults.AfterReservation = func() error {
		if !faulted {
			faulted = true
			return injected
		}
		return nil
	}
	interrupt := appwire.TurnInterruptParams{
		ClientMutationID: "interrupt-same-process-takeover",
		ExpectedTurnID:   started.Turn.ID,
	}
	cancelCalls := 0
	if _, err := sess.InterruptClientMutation(context.Background(), interrupt, func() {
		cancelCalls++
	}); !errors.Is(err, injected) {
		t.Fatalf("first interrupt error = %v, want %v", err, injected)
	}
	if cancelCalls != 0 {
		t.Fatalf("failed reservation owner canceled %d times, want 0", cancelCalls)
	}

	response, err := sess.InterruptClientMutation(context.Background(), interrupt, func() {
		cancelCalls++
		if err := sess.completeClientMutationInterruptedTurn(start.ClientMutationID); err != nil {
			t.Errorf("runner terminal transition: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("same-process interrupt takeover: %v", err)
	}
	if cancelCalls != 1 {
		t.Fatalf("same-process takeover cancellation calls = %d, want 1", cancelCalls)
	}
	if response.Receipt.Disposition != appwire.MutationDispositionApplied ||
		response.Receipt.TurnID != started.Turn.ID {
		t.Fatalf("same-process takeover response = %#v", response)
	}
	record := sess.clientMutations.snapshot().Journal[interrupt.ClientMutationID]
	if record.AttemptGeneration != 2 ||
		record.OperationState != clientMutationOperationTerminal {
		t.Fatalf("same-process takeover record = %#v", record)
	}
}

func TestClientMutation_InterruptAcceptedStartFallbackTerminalizesTargetAtomically(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	start := appwire.TurnStartParams{
		ClientMutationID: "start-accepted-before-interrupt",
		Input:            []appwire.InputItem{{Type: "text", Text: "never run this accepted start"}},
	}
	started, err := sess.AcceptClientMutationStart(start)
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	interrupt := appwire.TurnInterruptParams{
		ClientMutationID: "interrupt-accepted-start",
		ExpectedTurnID:   started.Turn.ID,
	}
	cancelCalls := 0
	response, err := sess.InterruptClientMutation(context.Background(), interrupt, func() {
		cancelCalls++
	})
	if err != nil {
		t.Fatalf("InterruptClientMutation: %v", err)
	}
	if cancelCalls != 1 {
		t.Fatalf("accepted-start cancellation calls = %d, want 1", cancelCalls)
	}
	if response.Receipt.TurnID != started.Turn.ID {
		t.Fatalf("interrupt receipt turn = %q, want %q", response.Receipt.TurnID, started.Turn.ID)
	}

	snapshot := sess.clientMutations.snapshot()
	target := snapshot.Journal[start.ClientMutationID]
	receipt := snapshot.Journal[interrupt.ClientMutationID]
	if target.OperationState != clientMutationOperationTerminal ||
		target.ExecutionState != "interrupted" ||
		len(target.Payload) != 0 {
		t.Fatalf("accepted interrupt target = %#v, want compact interrupted terminal", target)
	}
	if receipt.OperationState != clientMutationOperationTerminal ||
		receipt.ExecutionState != "interrupted" {
		t.Fatalf("accepted interrupt receipt = %#v", receipt)
	}
	if _, pending := snapshot.PendingExecutions[start.ClientMutationID]; pending {
		t.Fatal("successfully interrupted accepted start remained pending")
	}
	if _, reserved := snapshot.BudgetReservations[start.ClientMutationID]; reserved {
		t.Fatal("successfully interrupted accepted start retained its turn budget")
	}
	if snapshot.ActiveTurnID != "" || snapshot.InterruptFence != nil {
		t.Fatalf("accepted interrupt terminal state: active=%q fence=%#v",
			snapshot.ActiveTurnID, snapshot.InterruptFence)
	}
	if sess.clientMutations.interruptCallbackCompleted(interrupt.ClientMutationID) {
		t.Fatal("accepted interrupt retained its callback completion marker")
	}
	if claimed, ok, err := sess.ClaimClientMutationStart(); err != nil || ok {
		t.Fatalf("interrupted accepted start remained claimable: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
}

func TestClientMutation_InterruptPostSignalEffectFailureDirectRetryDoesNotCancelTwice(t *testing.T) {
	sess, start, started := newIncorporatedInterruptTestStart(t, "direct")
	defer sess.Close()
	interrupt := appwire.TurnInterruptParams{
		ClientMutationID: "interrupt-post-signal-direct",
		ExpectedTurnID:   started.Turn.ID,
	}
	injected := errors.New("interrupt terminal effect write failed")
	faulted := false
	sess.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
		if !faulted {
			faulted = true
			return injected
		}
		return nil
	}
	cancelCalls := 0
	if _, err := sess.InterruptClientMutation(context.Background(), interrupt, func() {
		cancelCalls++
	}); !errors.Is(err, injected) {
		t.Fatalf("first interrupt error = %v, want %v", err, injected)
	}
	if cancelCalls != 1 {
		t.Fatalf("first interrupt cancellation calls = %d, want 1", cancelCalls)
	}
	fenced := sess.clientMutations.snapshot()
	if fenced.InterruptFence == nil ||
		fenced.Journal[interrupt.ClientMutationID].OperationState != clientMutationOperationInFlight ||
		fenced.PendingExecutions[start.ClientMutationID].ExecutionState != "incorporated" {
		t.Fatalf("post-signal failed effect state = %#v", fenced)
	}
	if !sess.clientMutations.interruptCallbackCompleted(interrupt.ClientMutationID) {
		t.Fatal("post-signal failed effect lost its callback completion marker")
	}

	response, err := sess.InterruptClientMutation(context.Background(), interrupt, func() {
		cancelCalls++
	})
	if err != nil {
		t.Fatalf("direct post-signal takeover: %v", err)
	}
	if cancelCalls != 1 {
		t.Fatalf("direct post-signal takeover cancellation calls = %d, want 1", cancelCalls)
	}
	if response.Receipt.TurnID != started.Turn.ID {
		t.Fatalf("direct takeover receipt = %#v", response)
	}
	assertAtomicInterruptedMutation(t, sess, start.ClientMutationID, interrupt.ClientMutationID)
}

func TestClientMutation_InterruptPostSignalEffectFailureJoinedRetryDoesNotCancelTwice(t *testing.T) {
	sess, start, started := newIncorporatedInterruptTestStart(t, "joined")
	defer sess.Close()
	interrupt := appwire.TurnInterruptParams{
		ClientMutationID: "interrupt-post-signal-joined",
		ExpectedTurnID:   started.Turn.ID,
	}
	injected := errors.New("joined interrupt terminal effect write failed")
	faulted := false
	sess.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
		if !faulted {
			faulted = true
			return injected
		}
		return nil
	}
	var cancelCalls atomic.Int32
	cancelReturned := make(chan struct{})
	releaseOwnerWait := make(chan struct{})
	ownerErr := make(chan error, 1)
	go func() {
		_, err := sess.InterruptClientMutation(context.Background(), interrupt, func() {
			cancelCalls.Add(1)
			close(cancelReturned)
			<-releaseOwnerWait
		})
		ownerErr <- err
	}()
	<-cancelReturned

	retryJoined := make(chan struct{})
	sess.clientMutationInterruptJoined = func() { close(retryJoined) }
	retryResponse := make(chan appwire.TurnInterruptResponse, 1)
	retryErr := make(chan error, 1)
	go func() {
		response, err := sess.InterruptClientMutation(context.Background(), interrupt, func() {
			cancelCalls.Add(1)
		})
		retryResponse <- response
		retryErr <- err
	}()
	<-retryJoined
	close(releaseOwnerWait)
	if err := <-ownerErr; !errors.Is(err, injected) {
		t.Fatalf("owner interrupt error = %v, want %v", err, injected)
	}
	if err := <-retryErr; err != nil {
		t.Fatalf("joined post-signal takeover: %v", err)
	}
	response := <-retryResponse
	if response.Receipt.TurnID != started.Turn.ID {
		t.Fatalf("joined takeover receipt = %#v", response)
	}
	if got := cancelCalls.Load(); got != 1 {
		t.Fatalf("joined post-signal takeover cancellation calls = %d, want 1", got)
	}
	assertAtomicInterruptedMutation(t, sess, start.ClientMutationID, interrupt.ClientMutationID)
}

func newIncorporatedInterruptTestStart(
	t *testing.T,
	suffix string,
) (*Session, appwire.TurnStartParams, appwire.TurnStartResponse) {
	t.Helper()
	sess := newQueuePersistTestSession(t, t.TempDir())
	start := appwire.TurnStartParams{
		ClientMutationID: "start-before-post-signal-" + suffix,
		Input:            []appwire.InputItem{{Type: "text", Text: "interrupt after signal " + suffix}},
	}
	started, err := sess.AcceptClientMutationStart(start)
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	claimed, ok, err := sess.ClaimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("ClaimClientMutationStart: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	if err := sess.acceptUserInput(
		withQueuedClientMutation(context.Background(), claimed),
		claimed.Text,
		claimed.Images,
		nil,
		false,
	); err != nil {
		t.Fatalf("incorporate start: %v", err)
	}
	return sess, start, started
}

func assertAtomicInterruptedMutation(t *testing.T, sess *Session, targetID, interruptID string) {
	t.Helper()
	snapshot := sess.clientMutations.snapshot()
	target := snapshot.Journal[targetID]
	receipt := snapshot.Journal[interruptID]
	if target.OperationState != clientMutationOperationTerminal ||
		target.ExecutionState != "interrupted" ||
		receipt.OperationState != clientMutationOperationTerminal ||
		receipt.ExecutionState != "interrupted" ||
		snapshot.InterruptFence != nil ||
		snapshot.ActiveTurnID != "" {
		t.Fatalf("atomic interrupted snapshot: target=%#v receipt=%#v fence=%#v active=%q",
			target, receipt, snapshot.InterruptFence, snapshot.ActiveTurnID)
	}
	if _, pending := snapshot.PendingExecutions[targetID]; pending {
		t.Fatal("atomically interrupted target remained pending")
	}
	if sess.clientMutations.interruptCallbackCompleted(interruptID) {
		t.Fatal("terminal interrupt retained its callback completion marker")
	}
}

func countClientMutationTranscriptTurns(t *testing.T, sess *Session, clientMutationID string) int {
	t.Helper()
	var count int
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for _, turn := range sess.history {
		if turn.ClientMutationID == clientMutationID {
			count++
		}
	}
	return count
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
