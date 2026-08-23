package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

func TestClientMutationQueueConstants(t *testing.T) {
	if clientMutationMethodQueue != "turn/queue" {
		t.Fatalf("clientMutationMethodQueue = %q", clientMutationMethodQueue)
	}
	if clientMutationMethodSteer != "turn/steer" {
		t.Fatalf("clientMutationMethodSteer = %q", clientMutationMethodSteer)
	}
	if clientMutationMethodDrain != "turn/drainAsSteer" {
		t.Fatalf("clientMutationMethodDrain = %q", clientMutationMethodDrain)
	}
	if clientMutationMethodPromote != "turn/promoteQueuedAsSteer" {
		t.Fatalf("clientMutationMethodPromote = %q", clientMutationMethodPromote)
	}
	if clientMutationMethodCancel != "turn/cancelQueued" {
		t.Fatalf("clientMutationMethodCancel = %q", clientMutationMethodCancel)
	}
}

// ---------------------------------------------------------------------------
// acceptedClientMutationProjection
// ---------------------------------------------------------------------------

func TestAcceptedClientMutationProjection(t *testing.T) {
	tests := []struct {
		method string
		want   appwire.MutationProjectionState
	}{
		{clientMutationMethodStart, appwire.MutationProjectionPending},
		{clientMutationMethodSteer, appwire.MutationProjectionPending},
		{clientMutationMethodQueue, appwire.MutationProjectionPending},
		{clientMutationMethodDrain, appwire.MutationProjectionPending},
		{clientMutationMethodPromote, appwire.MutationProjectionPending},
		{clientMutationMethodInterrupt, appwire.MutationProjectionReflected},
		{clientMutationMethodCancel, appwire.MutationProjectionRemoved},
		{"unknown_method", appwire.MutationProjectionPending},
		{"", appwire.MutationProjectionPending},
	}
	for _, tc := range tests {
		got := acceptedClientMutationProjection(tc.method)
		if got != tc.want {
			t.Errorf("acceptedClientMutationProjection(%q) = %v, want %v", tc.method, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// mutationReceipt
// ---------------------------------------------------------------------------

func TestMutationReceipt(t *testing.T) {
	record := clientMutationRecord{
		ClientMutationID:    "cm_123",
		StableTurnID:        "turn_456",
		StableQueueEntryIDs: []string{"queue_1", "queue_2"},
	}
	receipt := mutationReceipt("thread_abc", record, appwire.MutationDispositionApplied, appwire.MutationProjectionPending)
	if receipt.ClientMutationID != "cm_123" {
		t.Fatalf("cmID = %q", receipt.ClientMutationID)
	}
	if receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("disposition = %v", receipt.Disposition)
	}
	if receipt.ThreadID != "thread_abc" {
		t.Fatalf("threadID = %q", receipt.ThreadID)
	}
	if receipt.TurnID != "turn_456" {
		t.Fatalf("turnID = %q", receipt.TurnID)
	}
	if len(receipt.QueueEntryIDs) != 2 || receipt.QueueEntryIDs[0] != "queue_1" {
		t.Fatalf("queueEntryIDs = %v", receipt.QueueEntryIDs)
	}
	if receipt.ProjectionState != appwire.MutationProjectionPending {
		t.Fatalf("projectionState = %v", receipt.ProjectionState)
	}
}

func TestMutationReceiptEmpty(t *testing.T) {
	receipt := mutationReceipt("t", clientMutationRecord{}, appwire.MutationDispositionReplayed, appwire.MutationProjectionReflected)
	if receipt.ClientMutationID != "" || receipt.TurnID != "" {
		t.Fatalf("expected empty fields")
	}
	if len(receipt.QueueEntryIDs) != 0 {
		t.Fatalf("expected empty queue entry IDs")
	}
}

// ---------------------------------------------------------------------------
// applyClientMutationRecord
// ---------------------------------------------------------------------------

func TestApplyClientMutationRecord(t *testing.T) {
	record := &clientMutationRecord{ClientMutationID: "cm_1"}
	result := json.RawMessage(`{"key":"val"}`)
	applyClientMutationRecord(record, result, appwire.MutationProjectionPending)
	if record.OperationState != clientMutationOperationApplied {
		t.Fatalf("operationState = %v", record.OperationState)
	}
	if record.ExecutionState != "accepted" {
		t.Fatalf("executionState = %q", record.ExecutionState)
	}
	if record.ProjectionState != appwire.MutationProjectionPending {
		t.Fatalf("projectionState = %v", record.ProjectionState)
	}
	if string(record.Result) != `{"key":"val"}` {
		t.Fatalf("result = %q", string(record.Result))
	}
}

// ---------------------------------------------------------------------------
// rejectClientMutation
// ---------------------------------------------------------------------------

func TestRejectClientMutationWithWireError(t *testing.T) {
	record := &clientMutationRecord{ClientMutationID: "cm_1"}
	wireErr := appwire.Conflict("some conflict")
	rejectClientMutation(record, wireErr)
	if record.OperationState != clientMutationOperationRejected {
		t.Fatalf("operationState = %v", record.OperationState)
	}
	if record.ExecutionState != "rejected" {
		t.Fatalf("executionState = %q", record.ExecutionState)
	}
	if record.ProjectionState != appwire.MutationProjectionRemoved {
		t.Fatalf("projectionState = %v", record.ProjectionState)
	}
	if record.Rejection == nil {
		t.Fatalf("expected rejection to be set")
	}
	if record.Rejection.Message != "some conflict" {
		t.Fatalf("rejection message = %q", record.Rejection.Message)
	}
	if record.Payload != nil {
		t.Fatalf("expected payload to be nil")
	}
}

func TestRejectClientMutationWithNonWireError(t *testing.T) {
	record := &clientMutationRecord{ClientMutationID: "cm_2"}
	// A plain error (not a WireError) should be wrapped as Conflict
	rejectClientMutation(record, errors.New("plain error"))
	if record.Rejection == nil {
		t.Fatalf("expected rejection")
	}
	if record.Rejection.Message != "plain error" {
		t.Fatalf("rejection message = %q", record.Rejection.Message)
	}
}

func TestRejectClientMutationDataHasClientMutationID(t *testing.T) {
	record := &clientMutationRecord{ClientMutationID: "cm_3"}
	rejectClientMutation(record, appwire.InvalidParams("bad params"))
	if record.Rejection.Data.ClientMutationID != "cm_3" {
		t.Fatalf("expected ClientMutationID in rejection data, got %q", record.Rejection.Data.ClientMutationID)
	}
	if record.Rejection.Data.MutationOutcome != appwire.MutationOutcomeNotAccepted {
		t.Fatalf("expected MutationOutcomeNotAccepted, got %v", record.Rejection.Data.MutationOutcome)
	}
	if record.Rejection.Data.RetryDisposition != appwire.RetryDispositionNone {
		t.Fatalf("expected RetryDispositionNone, got %v", record.Rejection.Data.RetryDisposition)
	}
}

// ---------------------------------------------------------------------------
// clientMutationRejectionError
// ---------------------------------------------------------------------------

func TestClientMutationRejectionError(t *testing.T) {
	t.Run("with rejection", func(t *testing.T) {
		record := clientMutationRecord{
			Rejection: &clientMutationRejection{
				Code:    409,
				Message: "conflict occurred",
				Data:    appwire.ErrorData{ClientMutationID: "cm_1"},
			},
		}
		err := clientMutationRejectionError(record)
		if err == nil {
			t.Fatalf("expected error")
		}
		var wireErr appwire.WireError
		if !errors.As(err, &wireErr) {
			t.Fatalf("expected WireError, got %T", err)
		}
		if wireErr.Message != "conflict occurred" {
			t.Fatalf("message = %q", wireErr.Message)
		}
	})
	t.Run("nil rejection", func(t *testing.T) {
		record := clientMutationRecord{}
		err := clientMutationRejectionError(record)
		if err == nil {
			t.Fatalf("expected error for nil rejection")
		}
		if !strings.Contains(err.Error(), "rejection is missing") {
			t.Fatalf("expected missing rejection message, got %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// reservedClientMutationTurns
// ---------------------------------------------------------------------------

func TestReservedClientMutationTurns(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		snap := &clientMutationSnapshot{}
		if n := reservedClientMutationTurns(snap); n != 0 {
			t.Fatalf("expected 0, got %d", n)
		}
	})
	t.Run("with reservations", func(t *testing.T) {
		snap := &clientMutationSnapshot{
			BudgetReservations: map[string]clientMutationBudgetReservation{
				"cm_1": {Slots: 2},
				"cm_2": {Slots: 3},
			},
		}
		if n := reservedClientMutationTurns(snap); n != 5 {
			t.Fatalf("expected 5, got %d", n)
		}
	})
}

// ---------------------------------------------------------------------------
// queueResponseFromRecord
// ---------------------------------------------------------------------------

func TestQueueResponseFromRecord(t *testing.T) {
	t.Run("with result", func(t *testing.T) {
		resultJSON := `{"receipt":{"client_mutation_id":"cm_1"}}`
		record := clientMutationRecord{
			ClientMutationID:    "cm_1",
			StableQueueEntryIDs: []string{"queue_1"},
			Result:              json.RawMessage(resultJSON),
			ProjectionState:     appwire.MutationProjectionPending,
		}
		resp, err := queueResponseFromRecord("thread_1", record, appwire.MutationDispositionReplayed)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Receipt.ClientMutationID != "cm_1" {
			t.Fatalf("cmID = %q", resp.Receipt.ClientMutationID)
		}
		if resp.Receipt.Disposition != appwire.MutationDispositionReplayed {
			t.Fatalf("disposition = %v", resp.Receipt.Disposition)
		}
		if resp.Receipt.ThreadID != "thread_1" {
			t.Fatalf("threadID = %q", resp.Receipt.ThreadID)
		}
		if len(resp.Receipt.QueueEntryIDs) != 1 || resp.Receipt.QueueEntryIDs[0] != "queue_1" {
			t.Fatalf("queueEntryIDs = %v", resp.Receipt.QueueEntryIDs)
		}
		if resp.Receipt.ProjectionState != appwire.MutationProjectionPending {
			t.Fatalf("projectionState = %v", resp.Receipt.ProjectionState)
		}
	})
	t.Run("empty result", func(t *testing.T) {
		record := clientMutationRecord{ClientMutationID: "cm_2"}
		resp, err := queueResponseFromRecord("t", record, appwire.MutationDispositionApplied)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Receipt.ClientMutationID != "cm_2" {
			t.Fatalf("cmID = %q", resp.Receipt.ClientMutationID)
		}
	})
	t.Run("invalid result json", func(t *testing.T) {
		record := clientMutationRecord{Result: json.RawMessage(`invalid`)}
		_, err := queueResponseFromRecord("t", record, appwire.MutationDispositionApplied)
		if err == nil || !strings.Contains(err.Error(), "decode queue mutation result") {
			t.Fatalf("expected decode error, got %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// queuedInputFromClientMutation
// ---------------------------------------------------------------------------

func TestQueuedInputFromClientMutation(t *testing.T) {
	t.Run("text only", func(t *testing.T) {
		entry := clientMutationQueueEntry{
			ID:    "q_1",
			Input: []appwire.InputItem{{Type: "text", Text: "hello"}},
		}
		queued := queuedInputFromClientMutation(entry)
		if queued.ID != "q_1" {
			t.Fatalf("id = %q", queued.ID)
		}
		if queued.Text != "hello" {
			t.Fatalf("text = %q", queued.Text)
		}
		if len(queued.Images) != 0 {
			t.Fatalf("expected no images")
		}
	})
	t.Run("image only", func(t *testing.T) {
		entry := clientMutationQueueEntry{
			Input: []appwire.InputItem{
				{Type: "image", MediaType: "image/png", Data: []byte("imgdata"), Name: "test.png"},
			},
		}
		queued := queuedInputFromClientMutation(entry)
		if len(queued.Images) != 1 {
			t.Fatalf("expected 1 image, got %d", len(queued.Images))
		}
		if queued.Images[0].MediaType != "image/png" {
			t.Fatalf("mediaType = %q", queued.Images[0].MediaType)
		}
		if string(queued.Images[0].Data) != "imgdata" {
			t.Fatalf("data = %q", string(queued.Images[0].Data))
		}
		if queued.Images[0].Name != "test.png" {
			t.Fatalf("name = %q", queued.Images[0].Name)
		}
	})
	t.Run("text and image", func(t *testing.T) {
		entry := clientMutationQueueEntry{
			Input: []appwire.InputItem{
				{Type: "text", Text: "desc"},
				{Type: "image", MediaType: "image/jpeg", Data: []byte("jpg"), Name: "photo.jpg"},
			},
		}
		queued := queuedInputFromClientMutation(entry)
		if queued.Text != "desc" {
			t.Fatalf("text = %q", queued.Text)
		}
		if len(queued.Images) != 1 {
			t.Fatalf("expected 1 image")
		}
	})
	t.Run("empty input", func(t *testing.T) {
		queued := queuedInputFromClientMutation(clientMutationQueueEntry{})
		if queued.Text != "" || len(queued.Images) != 0 {
			t.Fatalf("expected empty queued input")
		}
	})
	t.Run("unknown type ignored", func(t *testing.T) {
		entry := clientMutationQueueEntry{
			Input: []appwire.InputItem{{Type: "unknown", Text: "ignored"}},
		}
		queued := queuedInputFromClientMutation(entry)
		if queued.Text != "" {
			t.Fatalf("expected empty text for unknown type")
		}
	})
	t.Run("multiple text items concatenated", func(t *testing.T) {
		entry := clientMutationQueueEntry{
			Input: []appwire.InputItem{
				{Type: "text", Text: "hello "},
				{Type: "text", Text: "world"},
			},
		}
		queued := queuedInputFromClientMutation(entry)
		if queued.Text != "hello world" {
			t.Fatalf("text = %q, want 'hello world'", queued.Text)
		}
	})
}

// ---------------------------------------------------------------------------
// clientMutationInput
// ---------------------------------------------------------------------------

func TestClientMutationInput(t *testing.T) {
	t.Run("text only", func(t *testing.T) {
		input := clientMutationInput("hello", nil)
		if len(input) != 1 {
			t.Fatalf("expected 1 item, got %d", len(input))
		}
		if input[0].Type != "text" || input[0].Text != "hello" {
			t.Fatalf("input[0] = %+v", input[0])
		}
	})
	t.Run("images only", func(t *testing.T) {
		images := []ImageAttachment{
			{MediaType: "image/png", Data: []byte("data1"), Name: "a.png"},
			{MediaType: "image/jpeg", Data: []byte("data2"), Name: "b.jpg"},
		}
		input := clientMutationInput("", images)
		if len(input) != 2 {
			t.Fatalf("expected 2 items, got %d", len(input))
		}
		if input[0].Type != "image" || input[0].MediaType != "image/png" {
			t.Fatalf("input[0] = %+v", input[0])
		}
		// Verify data is copied (not shared)
		input[0].Data[0] = 'X'
		if images[0].Data[0] == 'X' {
			t.Fatalf("expected data to be copied")
		}
	})
	t.Run("empty", func(t *testing.T) {
		input := clientMutationInput("", nil)
		if len(input) != 0 {
			t.Fatalf("expected 0 items, got %d", len(input))
		}
	})
	t.Run("text and images", func(t *testing.T) {
		images := []ImageAttachment{{MediaType: "image/png", Data: []byte("img"), Name: "x.png"}}
		input := clientMutationInput("hello", images)
		if len(input) != 2 {
			t.Fatalf("expected 2 items, got %d", len(input))
		}
		if input[0].Type != "text" || input[1].Type != "image" {
			t.Fatalf("expected text then image")
		}
	})
}

// ---------------------------------------------------------------------------
// cloneClientMutationInput
// ---------------------------------------------------------------------------

func TestCloneClientMutationInput(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		dst := cloneClientMutationInput(nil)
		if len(dst) != 0 {
			t.Fatalf("expected 0 items")
		}
	})
	t.Run("deep copy", func(t *testing.T) {
		src := []appwire.InputItem{
			{Type: "image", Data: []byte("original"), Metadata: map[string]string{"k": "v"}},
		}
		dst := cloneClientMutationInput(src)
		if len(dst) != 1 {
			t.Fatalf("expected 1 item")
		}
		// Verify data is copied
		dst[0].Data[0] = 'X'
		if src[0].Data[0] == 'X' {
			t.Fatalf("expected data to be deep-copied")
		}
		// Verify metadata is copied
		dst[0].Metadata["k"] = "modified"
		if src[0].Metadata["k"] == "modified" {
			t.Fatalf("expected metadata to be deep-copied")
		}
	})
	t.Run("nil metadata", func(t *testing.T) {
		src := []appwire.InputItem{{Type: "text", Text: "hello"}}
		dst := cloneClientMutationInput(src)
		if dst[0].Text != "hello" {
			t.Fatalf("text = %q", dst[0].Text)
		}
		if dst[0].Metadata != nil {
			t.Fatalf("expected nil metadata preserved")
		}
	})
}

// ---------------------------------------------------------------------------
// clientMutationQueueIDs
// ---------------------------------------------------------------------------

func TestClientMutationQueueIDs(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		ids := clientMutationQueueIDs(nil)
		if len(ids) != 0 {
			t.Fatalf("expected 0 ids")
		}
	})
	t.Run("with entries", func(t *testing.T) {
		entries := []clientMutationQueueEntry{
			{ID: "q_1"},
			{ID: "q_2"},
			{ID: "q_3"},
		}
		ids := clientMutationQueueIDs(entries)
		if len(ids) != 3 {
			t.Fatalf("expected 3 ids, got %d", len(ids))
		}
		if ids[0] != "q_1" || ids[1] != "q_2" || ids[2] != "q_3" {
			t.Fatalf("ids = %v", ids)
		}
	})
}

// ---------------------------------------------------------------------------
// clientMutationQueueEntryIndex
// ---------------------------------------------------------------------------

func TestClientMutationQueueEntryIndex(t *testing.T) {
	entries := []clientMutationQueueEntry{
		{ID: "q_1"},
		{ID: "q_2"},
		{ID: "q_3"},
	}
	t.Run("found", func(t *testing.T) {
		if i := clientMutationQueueEntryIndex(entries, "q_2"); i != 1 {
			t.Fatalf("index = %d, want 1", i)
		}
	})
	t.Run("not found", func(t *testing.T) {
		if i := clientMutationQueueEntryIndex(entries, "q_999"); i != -1 {
			t.Fatalf("index = %d, want -1", i)
		}
	})
	t.Run("empty queue", func(t *testing.T) {
		if i := clientMutationQueueEntryIndex(nil, "q_1"); i != -1 {
			t.Fatalf("index = %d, want -1", i)
		}
	})
}

// ---------------------------------------------------------------------------
// clientMutationQueueEntryReserved
// ---------------------------------------------------------------------------

func TestClientMutationQueueEntryReserved(t *testing.T) {
	t.Run("not reserved", func(t *testing.T) {
		snap := &clientMutationSnapshot{
			Journal: map[string]clientMutationRecord{},
		}
		if clientMutationQueueEntryReserved(snap, "q_1") {
			t.Fatalf("expected not reserved")
		}
	})
	t.Run("reserved by inFlight record", func(t *testing.T) {
		snap := &clientMutationSnapshot{
			Journal: map[string]clientMutationRecord{
				"cm_1": {
					OperationState:      clientMutationOperationInFlight,
					StableQueueEntryIDs: []string{"q_1"},
				},
			},
		}
		if !clientMutationQueueEntryReserved(snap, "q_1") {
			t.Fatalf("expected reserved")
		}
	})
	t.Run("not reserved by applied record", func(t *testing.T) {
		snap := &clientMutationSnapshot{
			Journal: map[string]clientMutationRecord{
				"cm_1": {
					OperationState:      clientMutationOperationApplied,
					StableQueueEntryIDs: []string{"q_1"},
				},
			},
		}
		if clientMutationQueueEntryReserved(snap, "q_1") {
			t.Fatalf("expected not reserved (applied, not inFlight)")
		}
	})
	t.Run("not reserved by terminal record", func(t *testing.T) {
		snap := &clientMutationSnapshot{
			Journal: map[string]clientMutationRecord{
				"cm_1": {
					OperationState:      clientMutationOperationTerminal,
					StableQueueEntryIDs: []string{"q_1"},
				},
			},
		}
		if clientMutationQueueEntryReserved(snap, "q_1") {
			t.Fatalf("expected not reserved (terminal)")
		}
	})
}

// ---------------------------------------------------------------------------
// takeClientMutationQueueEntries
// ---------------------------------------------------------------------------

func TestTakeClientMutationQueueEntries(t *testing.T) {
	queue := []clientMutationQueueEntry{
		{ID: "q_1"},
		{ID: "q_2"},
		{ID: "q_3"},
	}
	t.Run("take all", func(t *testing.T) {
		selected, remaining, ok := takeClientMutationQueueEntries(queue, []string{"q_1", "q_2", "q_3"})
		if !ok {
			t.Fatalf("expected foundAll=true")
		}
		if len(selected) != 3 {
			t.Fatalf("expected 3 selected, got %d", len(selected))
		}
		if len(remaining) != 0 {
			t.Fatalf("expected 0 remaining, got %d", len(remaining))
		}
	})
	t.Run("take some", func(t *testing.T) {
		selected, remaining, ok := takeClientMutationQueueEntries(queue, []string{"q_1", "q_3"})
		if !ok {
			t.Fatalf("expected foundAll=true")
		}
		if len(selected) != 2 {
			t.Fatalf("expected 2 selected, got %d", len(selected))
		}
		if len(remaining) != 1 || remaining[0].ID != "q_2" {
			t.Fatalf("remaining = %v", remaining)
		}
	})
	t.Run("not found", func(t *testing.T) {
		_, remaining, ok := takeClientMutationQueueEntries(queue, []string{"q_1", "q_999"})
		if ok {
			t.Fatalf("expected foundAll=false")
		}
		if len(remaining) != 3 {
			t.Fatalf("expected remaining to be full queue, got %d", len(remaining))
		}
	})
	t.Run("empty ids", func(t *testing.T) {
		selected, remaining, ok := takeClientMutationQueueEntries(queue, nil)
		if !ok {
			t.Fatalf("expected foundAll=true")
		}
		if len(selected) != 0 {
			t.Fatalf("expected 0 selected")
		}
		if len(remaining) != 3 {
			t.Fatalf("expected 3 remaining")
		}
	})
	t.Run("empty queue", func(t *testing.T) {
		_, _, ok := takeClientMutationQueueEntries(nil, []string{"q_1"})
		if ok {
			t.Fatalf("expected foundAll=false")
		}
	})
}

// ---------------------------------------------------------------------------
// removeQueuedMutationSource
// ---------------------------------------------------------------------------

func TestRemoveQueuedMutationSource(t *testing.T) {
	t.Run("existing record", func(t *testing.T) {
		snap := &clientMutationSnapshot{
			BudgetReservations: map[string]clientMutationBudgetReservation{
				"cm_1": {Slots: 1},
			},
			Journal: map[string]clientMutationRecord{
				"cm_1": {OperationState: clientMutationOperationApplied},
			},
		}
		entry := clientMutationQueueEntry{ClientMutationID: "cm_1"}
		removeQueuedMutationSource(snap, entry, "transformed")
		if _, exists := snap.BudgetReservations["cm_1"]; exists {
			t.Fatalf("expected reservation to be deleted")
		}
		record, ok := snap.Journal["cm_1"]
		if !ok {
			t.Fatalf("expected record to still exist")
		}
		if record.OperationState != clientMutationOperationTerminal {
			t.Fatalf("expected terminal, got %v", record.OperationState)
		}
		if record.ExecutionState != "transformed" {
			t.Fatalf("executionState = %q", record.ExecutionState)
		}
		if record.ProjectionState != appwire.MutationProjectionRemoved {
			t.Fatalf("projectionState = %v", record.ProjectionState)
		}
		if record.Payload != nil {
			t.Fatalf("expected payload to be nil")
		}
	})
	t.Run("non-existing record", func(t *testing.T) {
		snap := &clientMutationSnapshot{
			BudgetReservations: map[string]clientMutationBudgetReservation{},
			Journal:            map[string]clientMutationRecord{},
		}
		entry := clientMutationQueueEntry{ClientMutationID: "cm_nonexistent"}
		removeQueuedMutationSource(snap, entry, "canceled")
		// Should just delete the (non-existent) reservation and return
		if len(snap.BudgetReservations) != 0 {
			t.Fatalf("expected empty reservations")
		}
		if len(snap.Journal) != 0 {
			t.Fatalf("expected empty journal")
		}
	})
}

// ---------------------------------------------------------------------------
// removeClientMutationSteeringOrder
// ---------------------------------------------------------------------------

func TestRemoveClientMutationSteeringOrder(t *testing.T) {
	t.Run("found and removed", func(t *testing.T) {
		snap := &clientMutationSnapshot{
			SteeringOrder: []string{"cm_1", "cm_2", "cm_3"},
		}
		removeClientMutationSteeringOrder(snap, "cm_2")
		if len(snap.SteeringOrder) != 2 {
			t.Fatalf("expected 2 remaining, got %d", len(snap.SteeringOrder))
		}
		if snap.SteeringOrder[0] != "cm_1" || snap.SteeringOrder[1] != "cm_3" {
			t.Fatalf("steeringOrder = %v", snap.SteeringOrder)
		}
	})
	t.Run("not found", func(t *testing.T) {
		snap := &clientMutationSnapshot{
			SteeringOrder: []string{"cm_1", "cm_2"},
		}
		removeClientMutationSteeringOrder(snap, "cm_999")
		if len(snap.SteeringOrder) != 2 {
			t.Fatalf("expected 2 remaining (nothing removed)")
		}
	})
	t.Run("empty order", func(t *testing.T) {
		snap := &clientMutationSnapshot{}
		removeClientMutationSteeringOrder(snap, "cm_1")
		if len(snap.SteeringOrder) != 0 {
			t.Fatalf("expected 0 remaining")
		}
	})
	t.Run("first element", func(t *testing.T) {
		snap := &clientMutationSnapshot{
			SteeringOrder: []string{"cm_1", "cm_2"},
		}
		removeClientMutationSteeringOrder(snap, "cm_1")
		if len(snap.SteeringOrder) != 1 || snap.SteeringOrder[0] != "cm_2" {
			t.Fatalf("steeringOrder = %v", snap.SteeringOrder)
		}
	})
	t.Run("last element", func(t *testing.T) {
		snap := &clientMutationSnapshot{
			SteeringOrder: []string{"cm_1", "cm_2"},
		}
		removeClientMutationSteeringOrder(snap, "cm_2")
		if len(snap.SteeringOrder) != 1 || snap.SteeringOrder[0] != "cm_1" {
			t.Fatalf("steeringOrder = %v", snap.SteeringOrder)
		}
	})
}

// ---------------------------------------------------------------------------
// reserveClientMutationTurnID
// ---------------------------------------------------------------------------

func TestReserveClientMutationTurnID(t *testing.T) {
	snap := &clientMutationSnapshot{NextTurnSequence: 5}
	record := &clientMutationRecord{ClientMutationID: "cm_1"}
	reserveClientMutationTurnID(snap, record)
	if snap.NextTurnSequence != 6 {
		t.Fatalf("NextTurnSequence = %d, want 6", snap.NextTurnSequence)
	}
	if record.StableTurnID == "" {
		t.Fatalf("expected StableTurnID to be set")
	}
}

// ---------------------------------------------------------------------------
// replayClientMutationResult
// ---------------------------------------------------------------------------

func TestReplayClientMutationResult(t *testing.T) {
	t.Run("valid result", func(t *testing.T) {
		record := clientMutationRecord{Result: json.RawMessage(`{"key":"val"}`)}
		var dest map[string]any
		err := replayClientMutationResult(record, &dest)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dest["key"] != "val" {
			t.Fatalf("dest = %v", dest)
		}
	})
	t.Run("empty result", func(t *testing.T) {
		record := clientMutationRecord{}
		var dest map[string]any
		err := replayClientMutationResult(record, &dest)
		if err == nil || !strings.Contains(err.Error(), "result is missing") {
			t.Fatalf("expected missing error, got %v", err)
		}
	})
	t.Run("invalid json", func(t *testing.T) {
		record := clientMutationRecord{Result: json.RawMessage(`invalid`)}
		var dest map[string]any
		err := replayClientMutationResult(record, &dest)
		if err == nil || !strings.Contains(err.Error(), "decode client mutation result") {
			t.Fatalf("expected decode error, got %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// addPendingSteering
// ---------------------------------------------------------------------------

func TestAddPendingSteering(t *testing.T) {
	snap := &clientMutationSnapshot{
		PendingExecutions: map[string]appwire.PendingMutation{},
	}
	record := &clientMutationRecord{
		ClientMutationID: "cm_1",
		Method:           clientMutationMethodSteer,
		StableTurnID:     "turn_1",
	}
	input := []appwire.InputItem{{Type: "text", Text: "steer msg"}}
	addPendingSteering(snap, record, input)
	pending, ok := snap.PendingExecutions["cm_1"]
	if !ok {
		t.Fatalf("expected pending execution to be added")
	}
	if pending.Method != clientMutationMethodSteer {
		t.Fatalf("method = %q", pending.Method)
	}
	if pending.ExecutionState != "accepted" {
		t.Fatalf("executionState = %q", pending.ExecutionState)
	}
	if pending.TurnID != "turn_1" {
		t.Fatalf("turnID = %q", pending.TurnID)
	}
	if len(pending.Input) != 1 || pending.Input[0].Text != "steer msg" {
		t.Fatalf("input = %v", pending.Input)
	}
	if len(snap.SteeringOrder) != 1 || snap.SteeringOrder[0] != "cm_1" {
		t.Fatalf("steeringOrder = %v", snap.SteeringOrder)
	}
}

// ---------------------------------------------------------------------------
// clientSteeringFromSnapshot
// ---------------------------------------------------------------------------

func TestClientSteeringFromSnapshot(t *testing.T) {
	t.Run("with accepted steering", func(t *testing.T) {
		snap := &clientMutationSnapshot{
			SteeringOrder: []string{"cm_1", "cm_2"},
			PendingExecutions: map[string]appwire.PendingMutation{
				"cm_1": {
					Method:         clientMutationMethodSteer,
					ExecutionState: "accepted",
					TurnID:         "turn_1",
					Input:          []appwire.InputItem{{Type: "text", Text: "steer1"}},
				},
				"cm_2": {
					Method:         clientMutationMethodSteer,
					ExecutionState: "incorporated",
					TurnID:         "turn_2",
					Input:          []appwire.InputItem{{Type: "text", Text: "steer2"}},
				},
			},
		}
		steering := clientSteeringFromSnapshot(*snap)
		if len(steering) != 1 {
			t.Fatalf("expected 1 steering message, got %d", len(steering))
		}
		if steering[0].Text != "steer1" {
			t.Fatalf("text = %q", steering[0].Text)
		}
		if steering[0].ClientMutationID != "cm_1" {
			t.Fatalf("cmID = %q", steering[0].ClientMutationID)
		}
		if steering[0].StableTurnID != "turn_1" {
			t.Fatalf("turnID = %q", steering[0].StableTurnID)
		}
		if steering[0].Source != events.SteeringSourceUser {
			t.Fatalf("source = %q", steering[0].Source)
		}
	})
	t.Run("empty", func(t *testing.T) {
		snap := &clientMutationSnapshot{
			SteeringOrder:     []string{},
			PendingExecutions: map[string]appwire.PendingMutation{},
		}
		steering := clientSteeringFromSnapshot(*snap)
		if len(steering) != 0 {
			t.Fatalf("expected 0 steering messages")
		}
	})
	t.Run("non-existent pending", func(t *testing.T) {
		snap := &clientMutationSnapshot{
			SteeringOrder:     []string{"cm_1"},
			PendingExecutions: map[string]appwire.PendingMutation{},
		}
		steering := clientSteeringFromSnapshot(*snap)
		if len(steering) != 0 {
			t.Fatalf("expected 0 steering messages for non-existent pending")
		}
	})
}

// ---------------------------------------------------------------------------
// combineClientMutationInputs
// ---------------------------------------------------------------------------

func TestCombineClientMutationInputs(t *testing.T) {
	t.Run("entries only", func(t *testing.T) {
		entries := []clientMutationQueueEntry{
			{Input: []appwire.InputItem{{Type: "text", Text: "msg1"}}},
			{Input: []appwire.InputItem{{Type: "text", Text: "msg2"}}},
		}
		result := combineClientMutationInputs(entries, nil)
		if len(result) != 1 {
			t.Fatalf("expected 1 combined text item, got %d items", len(result))
		}
		if result[0].Type != "text" {
			t.Fatalf("type = %q", result[0].Type)
		}
		if result[0].Text != "msg1\n\nmsg2" {
			t.Fatalf("text = %q, want 'msg1\\n\\nmsg2'", result[0].Text)
		}
	})
	t.Run("extra only", func(t *testing.T) {
		extra := []appwire.InputItem{{Type: "text", Text: "extra msg"}}
		result := combineClientMutationInputs(nil, extra)
		if len(result) != 1 {
			t.Fatalf("expected 1 item, got %d", len(result))
		}
		if result[0].Text != "extra msg" {
			t.Fatalf("text = %q", result[0].Text)
		}
	})
	t.Run("entries and extra", func(t *testing.T) {
		entries := []clientMutationQueueEntry{
			{Input: []appwire.InputItem{{Type: "text", Text: "queued"}}},
		}
		extra := []appwire.InputItem{{Type: "text", Text: "steered"}}
		result := combineClientMutationInputs(entries, extra)
		if len(result) != 1 {
			t.Fatalf("expected 1 combined item, got %d", len(result))
		}
		if result[0].Text != "queued\n\nsteered" {
			t.Fatalf("text = %q, want 'queued\\n\\nsteered'", result[0].Text)
		}
	})
	t.Run("with images", func(t *testing.T) {
		entries := []clientMutationQueueEntry{
			{Input: []appwire.InputItem{
				{Type: "text", Text: "msg"},
				{Type: "image", MediaType: "image/png", Data: []byte("img1"), Name: "a.png"},
			}},
		}
		extra := []appwire.InputItem{
			{Type: "image", MediaType: "image/jpeg", Data: []byte("img2"), Name: "b.jpg"},
		}
		result := combineClientMutationInputs(entries, extra)
		// Should have 1 text + 2 images
		if len(result) != 3 {
			t.Fatalf("expected 3 items (1 text + 2 images), got %d", len(result))
		}
		if result[0].Type != "text" {
			t.Fatalf("first item should be text")
		}
		if result[1].Type != "image" || result[2].Type != "image" {
			t.Fatalf("expected images at positions 1 and 2")
		}
	})
	t.Run("whitespace-only text skipped", func(t *testing.T) {
		entries := []clientMutationQueueEntry{
			{Input: []appwire.InputItem{{Type: "text", Text: "   "}}},
			{Input: []appwire.InputItem{{Type: "text", Text: "real msg"}}},
		}
		result := combineClientMutationInputs(entries, nil)
		if len(result) != 1 {
			t.Fatalf("expected 1 item, got %d", len(result))
		}
		if result[0].Text != "real msg" {
			t.Fatalf("text = %q, want 'real msg'", result[0].Text)
		}
	})
	t.Run("empty all", func(t *testing.T) {
		result := combineClientMutationInputs(nil, nil)
		if len(result) != 0 {
			t.Fatalf("expected 0 items, got %d", len(result))
		}
	})
}

// ---------------------------------------------------------------------------
// returnClaimedQueuedMutation
// ---------------------------------------------------------------------------

func TestReturnClaimedQueuedMutation(t *testing.T) {
	t.Run("valid return", func(t *testing.T) {
		snap := &clientMutationSnapshot{
			InputQueue: []clientMutationQueueEntry{
				{ID: "q_other", ClientMutationID: "cm_other"},
			},
			AcceptedTurns:      5,
			BudgetReservations: map[string]clientMutationBudgetReservation{},
			PendingExecutions: map[string]appwire.PendingMutation{
				"cm_1": {
					QueueEntryIDs: []string{"q_1"},
					TurnID:        "turn_1",
					Input:         []appwire.InputItem{{Type: "text", Text: "msg"}},
					Method:        clientMutationMethodQueue,
				},
			},
			ActiveTurnID: "turn_1",
		}
		record := &clientMutationRecord{Method: clientMutationMethodQueue}
		err := returnClaimedQueuedMutation(snap, "cm_1", snap.PendingExecutions["cm_1"], record)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Entry should be prepended to queue
		if len(snap.InputQueue) != 2 {
			t.Fatalf("expected 2 queue entries, got %d", len(snap.InputQueue))
		}
		if snap.InputQueue[0].ID != "q_1" {
			t.Fatalf("first entry should be q_1, got %q", snap.InputQueue[0].ID)
		}
		// AcceptedTurns should be decremented
		if snap.AcceptedTurns != 4 {
			t.Fatalf("AcceptedTurns = %d, want 4", snap.AcceptedTurns)
		}
		// Budget reservation should be set
		if _, ok := snap.BudgetReservations["cm_1"]; !ok {
			t.Fatalf("expected budget reservation for cm_1")
		}
		// PendingExecutions should be deleted
		if _, ok := snap.PendingExecutions["cm_1"]; ok {
			t.Fatalf("expected cm_1 to be deleted from PendingExecutions")
		}
		// ActiveTurnID should be cleared
		if snap.ActiveTurnID != "" {
			t.Fatalf("expected ActiveTurnID to be cleared")
		}
		// Record projection state should be set
		if record.ProjectionState != appwire.MutationProjectionPending {
			t.Fatalf("projectionState = %v", record.ProjectionState)
		}
	})
	t.Run("wrong number of queue entry IDs", func(t *testing.T) {
		snap := &clientMutationSnapshot{
			PendingExecutions: map[string]appwire.PendingMutation{
				"cm_1": {
					QueueEntryIDs: []string{"q_1", "q_2"}, // 2 IDs, should be 1
				},
			},
		}
		record := &clientMutationRecord{}
		err := returnClaimedQueuedMutation(snap, "cm_1", snap.PendingExecutions["cm_1"], record)
		if err == nil {
			t.Fatalf("expected error")
		}
		if !strings.Contains(err.Error(), "has 2 queue entry IDs") {
			t.Fatalf("expected error about 2 queue entry IDs, got %v", err)
		}
	})
	t.Run("zero accepted turns stays zero", func(t *testing.T) {
		snap := &clientMutationSnapshot{
			AcceptedTurns:      0,
			BudgetReservations: map[string]clientMutationBudgetReservation{},
			PendingExecutions: map[string]appwire.PendingMutation{
				"cm_1": {
					QueueEntryIDs: []string{"q_1"},
					Input:         []appwire.InputItem{{Type: "text", Text: "msg"}},
					Method:        clientMutationMethodQueue,
				},
			},
		}
		record := &clientMutationRecord{Method: clientMutationMethodQueue}
		err := returnClaimedQueuedMutation(snap, "cm_1", snap.PendingExecutions["cm_1"], record)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if snap.AcceptedTurns != 0 {
			t.Fatalf("expected 0 accepted turns, got %d", snap.AcceptedTurns)
		}
	})
}

// ---------------------------------------------------------------------------
// clientMutationTranscriptItems struct
// ---------------------------------------------------------------------------

func TestClientMutationTranscriptItemsStruct(t *testing.T) {
	items := clientMutationTranscriptItems{
		StableTurnID: "turn_1",
		User:         true,
		Failure:      false,
	}
	if items.StableTurnID != "turn_1" || !items.User || items.Failure {
		t.Fatalf("items = %+v", items)
	}
}

// ---------------------------------------------------------------------------
// withSteeringCarrierTurn / steeringCarrierTurnIDFromContext
// ---------------------------------------------------------------------------

func TestSteeringCarrierTurnContext(t *testing.T) {
	ctx := withSteeringCarrierTurn(context.Background(), "turn_ctx_1")
	if id := steeringCarrierTurnIDFromContext(ctx); id != "turn_ctx_1" {
		t.Fatalf("turnID = %q, want 'turn_ctx_1'", id)
	}
}

func TestSteeringCarrierTurnIDFromContextEmpty(t *testing.T) {
	if id := steeringCarrierTurnIDFromContext(context.Background()); id != "" {
		t.Fatalf("expected empty turnID, got %q", id)
	}
}

// ---------------------------------------------------------------------------
// ClientMutationProjection nil session
// ---------------------------------------------------------------------------

func TestClientMutationProjectionNilSession(t *testing.T) {
	var s *Session
	queue, pending := s.ClientMutationProjection()
	if queue.Depth != 0 {
		t.Fatalf("expected depth 0, got %d", queue.Depth)
	}
	if pending != nil {
		t.Fatalf("expected nil pending")
	}
}

// ---------------------------------------------------------------------------
// ClientMutationProjection with nil clientMutations
// ---------------------------------------------------------------------------

func TestClientMutationProjectionNilStore(t *testing.T) {
	s := &Session{}
	queue, pending := s.ClientMutationProjection()
	if queue.Depth != 0 {
		t.Fatalf("expected depth 0, got %d", queue.Depth)
	}
	if pending != nil {
		t.Fatalf("expected nil pending")
	}
}

// ---------------------------------------------------------------------------
// recoverClientMutationFailures nil session
// ---------------------------------------------------------------------------

func TestRecoverClientMutationFailuresNil(t *testing.T) {
	var s *Session
	if err := s.recoverClientMutationFailures(); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestRecoverClientMutationFailuresNilStore(t *testing.T) {
	s := &Session{}
	if err := s.recoverClientMutationFailures(); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// completeClientMutationTurn / completeClientMutationInterruptedTurn
// (nil store path)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// fmt error message verification
// ---------------------------------------------------------------------------

func TestRejectClientMutationPreservesCode(t *testing.T) {
	record := &clientMutationRecord{ClientMutationID: "cm_1"}
	wireErr := appwire.Conflict("test")
	rejectClientMutation(record, wireErr)
	if record.Rejection.Code != wireErr.Code {
		t.Fatalf("code = %d, want %d", record.Rejection.Code, wireErr.Code)
	}
}

// ---------------------------------------------------------------------------
// ClientMutationProjection with populated snapshot
// ---------------------------------------------------------------------------

func TestClientMutationProjectionPopulated(t *testing.T) {
	// We can't easily create a Session with a real clientMutations store
	// without state dir, so test the projection indirectly through
	// the snapshot structure.
	snap := clientMutationSnapshot{
		InputQueue: []clientMutationQueueEntry{
			{ID: "q_1", ClientMutationID: "cm_1", Input: []appwire.InputItem{{Type: "text", Text: "msg1"}}},
			{ID: "q_2", ClientMutationID: "cm_2", Input: []appwire.InputItem{{Type: "text", Text: "msg2"}}},
		},
		QueueRevision: 5,
		Journal: map[string]clientMutationRecord{
			"cm_1": {Method: clientMutationMethodQueue, ExecutionState: "accepted"},
			"cm_2": {Method: clientMutationMethodQueue, ExecutionState: "accepted"},
		},
		PendingExecutions: map[string]appwire.PendingMutation{},
	}

	// Verify queue structure
	if len(snap.InputQueue) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(snap.InputQueue))
	}

	// Verify preview lines
	for i, entry := range snap.InputQueue {
		queued := queuedInputFromClientMutation(entry)
		preview := queuedEntryPreviewLine(queued)
		if preview == "" {
			t.Fatalf("expected non-empty preview for entry %d", i)
		}
	}
}

// ---------------------------------------------------------------------------
// queuedEntryPreviewLine integration
// ---------------------------------------------------------------------------

func TestQueuedEntryPreviewLineIntegration(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		preview := queuedEntryPreviewLine(queuedInput{Text: "first line\nsecond line"})
		if preview != "first line" {
			t.Fatalf("preview = %q", preview)
		}
	})
	t.Run("single image", func(t *testing.T) {
		preview := queuedEntryPreviewLine(queuedInput{Images: []ImageAttachment{{}}})
		if preview != "[image]" {
			t.Fatalf("preview = %q", preview)
		}
	})
	t.Run("multiple images", func(t *testing.T) {
		preview := queuedEntryPreviewLine(queuedInput{Images: []ImageAttachment{{}, {}}})
		if preview != "[2 images]" {
			t.Fatalf("preview = %q", preview)
		}
	})
	t.Run("empty", func(t *testing.T) {
		preview := queuedEntryPreviewLine(queuedInput{})
		if preview != "" {
			t.Fatalf("preview = %q", preview)
		}
	})
}

// ---------------------------------------------------------------------------
// fmt.Sprintf usage in rejectClientMutation
// ---------------------------------------------------------------------------

func TestRejectClientMutationWithInvalidParams(t *testing.T) {
	record := &clientMutationRecord{ClientMutationID: "cm_invalid"}
	rejectClientMutation(record, appwire.InvalidParams("bad input"))
	if record.Rejection == nil {
		t.Fatalf("expected rejection")
	}
	// InvalidParams has a specific code
	if record.Rejection.Code != appwire.CodeInvalidParams {
		t.Fatalf("code = %d, want %d", record.Rejection.Code, appwire.CodeInvalidParams)
	}
}

// ---------------------------------------------------------------------------
// Ensure no unused imports
// ---------------------------------------------------------------------------

var _ = fmt.Sprintf
var _ = errors.New
