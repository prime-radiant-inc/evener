package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"syscall"
	"testing"

	"primeradiant.com/evener/appwire"
)

// validPersistRecord builds a journal record that passes validateClientMutationSnapshot,
// parameterized so individual fields can be mutated to trigger each rejection branch.
func validPersistRecord(id string) clientMutationRecord {
	payload := json.RawMessage(`{"ok":true}`)
	sum := sha256.Sum256(payload)
	return clientMutationRecord{
		ClientMutationID:  id,
		Method:            "turn/start",
		Payload:           payload,
		PayloadHash:       hex.EncodeToString(sum[:]),
		OperationState:    clientMutationOperationInFlight,
		ExecutionState:    "pending",
		ProjectionState:   appwire.MutationProjectionPending,
		AttemptGeneration: 1,
	}
}

func validPersistSnapshot(sessionID string) clientMutationSnapshot {
	return clientMutationSnapshot{
		Version:            clientMutationSnapshotVersion,
		SessionID:          sessionID,
		Journal:            map[string]clientMutationRecord{"mutation-1": validPersistRecord("mutation-1")},
		BudgetReservations: make(map[string]clientMutationBudgetReservation),
		PendingExecutions:  make(clientMutationPendingExecutions),
	}
}

// TestValidateClientMutationSnapshot_VersionMismatch covers the unsupported-version branch.
func TestValidateClientMutationSnapshot_VersionMismatch(t *testing.T) {
	s := validPersistSnapshot("session-1")
	s.Version = 999
	if err := validateClientMutationSnapshot(s, "session-1"); err == nil ||
		!contains(err.Error(), "unsupported version") {
		t.Fatalf("expected unsupported-version error, got %v", err)
	}
}

// TestValidateClientMutationSnapshot_SessionMismatch covers the session-id mismatch branch.
func TestValidateClientMutationSnapshot_SessionMismatch(t *testing.T) {
	s := validPersistSnapshot("session-1")
	if err := validateClientMutationSnapshot(s, "session-other"); err == nil ||
		!contains(err.Error(), "does not match") {
		t.Fatalf("expected session mismatch error, got %v", err)
	}
}

// TestValidateClientMutationSnapshot_MissingMaps covers the nil-map branches.
func TestValidateClientMutationSnapshot_MissingMaps(t *testing.T) {
	t.Run("nil journal", func(t *testing.T) {
		s := validPersistSnapshot("session-1")
		s.Journal = nil
		if err := validateClientMutationSnapshot(s, "session-1"); err == nil ||
			!contains(err.Error(), "journal is missing") {
			t.Fatalf("expected journal-missing error, got %v", err)
		}
	})
	t.Run("nil budget reservations", func(t *testing.T) {
		s := validPersistSnapshot("session-1")
		s.BudgetReservations = nil
		if err := validateClientMutationSnapshot(s, "session-1"); err == nil ||
			!contains(err.Error(), "budget reservations are missing") {
			t.Fatalf("expected budget-missing error, got %v", err)
		}
	})
	t.Run("nil pending executions", func(t *testing.T) {
		s := validPersistSnapshot("session-1")
		s.PendingExecutions = nil
		if err := validateClientMutationSnapshot(s, "session-1"); err == nil ||
			!contains(err.Error(), "pending executions are missing") {
			t.Fatalf("expected pending-missing error, got %v", err)
		}
	})
}

// TestValidateClientMutationSnapshot_JournalKeyMismatch covers the
// journal-key/record-id mismatch branch (line 298-300).
func TestValidateClientMutationSnapshot_JournalKeyMismatch(t *testing.T) {
	s := validPersistSnapshot("session-1")
	rec := validPersistRecord("mutation-1")
	rec.ClientMutationID = "different-id"
	s.Journal["mutation-1"] = rec
	if err := validateClientMutationSnapshot(s, "session-1"); err == nil ||
		!contains(err.Error(), "does not match record ID") {
		t.Fatalf("expected key mismatch error, got %v", err)
	}
}

func TestValidateClientMutationSnapshot_EmptyJournalKey(t *testing.T) {
	s := validPersistSnapshot("session-1")
	rec := validPersistRecord("")
	s.Journal[""] = rec
	delete(s.Journal, "mutation-1")
	if err := validateClientMutationSnapshot(s, "session-1"); err == nil ||
		!contains(err.Error(), "does not match record ID") {
		t.Fatalf("expected empty-key error, got %v", err)
	}
}

// TestValidateClientMutationSnapshot_NoMethod covers the empty-method branch (line 301-303).
func TestValidateClientMutationSnapshot_NoMethod(t *testing.T) {
	s := validPersistSnapshot("session-1")
	rec := validPersistRecord("mutation-1")
	rec.Method = ""
	s.Journal["mutation-1"] = rec
	if err := validateClientMutationSnapshot(s, "session-1"); err == nil ||
		!contains(err.Error(), "has no method") {
		t.Fatalf("expected no-method error, got %v", err)
	}
}

// TestValidateClientMutationSnapshot_EmptyPayloadNonTerminal covers the
// no-payload-outside-terminal/rejected branch (line 304-308).
func TestValidateClientMutationSnapshot_EmptyPayloadNonTerminal(t *testing.T) {
	s := validPersistSnapshot("session-1")
	rec := validPersistRecord("mutation-1")
	rec.Payload = nil
	rec.PayloadHash = ""
	rec.OperationState = clientMutationOperationInFlight
	s.Journal["mutation-1"] = rec
	if err := validateClientMutationSnapshot(s, "session-1"); err == nil ||
		!contains(err.Error(), "no payload outside terminal or rejected state") {
		t.Fatalf("expected no-payload error, got %v", err)
	}
}

// TestValidateClientMutationSnapshot_EmptyPayloadTerminalNoHash covers the
// terminal-no-payload-hash branch (line 309-311).
func TestValidateClientMutationSnapshot_EmptyPayloadTerminalNoHash(t *testing.T) {
	s := validPersistSnapshot("session-1")
	rec := validPersistRecord("mutation-1")
	rec.Payload = nil
	rec.PayloadHash = ""
	rec.OperationState = clientMutationOperationTerminal
	s.Journal["mutation-1"] = rec
	if err := validateClientMutationSnapshot(s, "session-1"); err == nil ||
		!contains(err.Error(), "no payload hash") {
		t.Fatalf("expected no-payload-hash error, got %v", err)
	}
}

// TestValidateClientMutationSnapshot_EmptyPayloadRejectedNoHash covers the
// rejected-no-payload-hash branch.
func TestValidateClientMutationSnapshot_EmptyPayloadRejectedNoHash(t *testing.T) {
	s := validPersistSnapshot("session-1")
	rec := validPersistRecord("mutation-1")
	rec.Payload = nil
	rec.PayloadHash = ""
	rec.OperationState = clientMutationOperationRejected
	rec.Rejection = &clientMutationRejection{Code: 1, Message: "no"}
	s.Journal["mutation-1"] = rec
	if err := validateClientMutationSnapshot(s, "session-1"); err == nil ||
		!contains(err.Error(), "no payload hash") {
		t.Fatalf("expected no-payload-hash error, got %v", err)
	}
}

// TestValidateClientMutationSnapshot_InvalidPayload covers the invalid-JSON payload branch (line 313-315).
func TestValidateClientMutationSnapshot_InvalidPayload(t *testing.T) {
	s := validPersistSnapshot("session-1")
	rec := validPersistRecord("mutation-1")
	rec.Payload = json.RawMessage(`{not json`)
	rec.PayloadHash = ""
	s.Journal["mutation-1"] = rec
	if err := validateClientMutationSnapshot(s, "session-1"); err == nil ||
		!contains(err.Error(), "invalid payload") {
		t.Fatalf("expected invalid-payload error, got %v", err)
	}
}

// TestValidateClientMutationSnapshot_PayloadHashMismatch covers the
// payload-hash-mismatch branch (line 316-319).
func TestValidateClientMutationSnapshot_PayloadHashMismatch(t *testing.T) {
	s := validPersistSnapshot("session-1")
	rec := validPersistRecord("mutation-1")
	rec.PayloadHash = "deadbeef"
	s.Journal["mutation-1"] = rec
	if err := validateClientMutationSnapshot(s, "session-1"); err == nil ||
		!contains(err.Error(), "payload hash does not match") {
		t.Fatalf("expected hash-mismatch error, got %v", err)
	}
}

// TestValidateClientMutationSnapshot_NoAttemptGeneration covers the
// zero-attempt-generation branch (line 321-323).
func TestValidateClientMutationSnapshot_NoAttemptGeneration(t *testing.T) {
	s := validPersistSnapshot("session-1")
	rec := validPersistRecord("mutation-1")
	rec.AttemptGeneration = 0
	s.Journal["mutation-1"] = rec
	if err := validateClientMutationSnapshot(s, "session-1"); err == nil ||
		!contains(err.Error(), "no attempt generation") {
		t.Fatalf("expected no-attempt-generation error, got %v", err)
	}
}

// TestValidateClientMutationSnapshot_NoExecutionState covers the
// empty-execution-state branch (line 324-326).
func TestValidateClientMutationSnapshot_NoExecutionState(t *testing.T) {
	s := validPersistSnapshot("session-1")
	rec := validPersistRecord("mutation-1")
	rec.ExecutionState = ""
	s.Journal["mutation-1"] = rec
	if err := validateClientMutationSnapshot(s, "session-1"); err == nil ||
		!contains(err.Error(), "no execution state") {
		t.Fatalf("expected no-execution-state error, got %v", err)
	}
}

// TestValidateClientMutationSnapshot_RejectionOutsideRejected covers the
// has-rejection-outside-rejected-state branch (line 329-331).
func TestValidateClientMutationSnapshot_RejectionOutsideRejected(t *testing.T) {
	s := validPersistSnapshot("session-1")
	rec := validPersistRecord("mutation-1")
	rec.Rejection = &clientMutationRejection{Code: 1, Message: "no"}
	rec.OperationState = clientMutationOperationInFlight
	s.Journal["mutation-1"] = rec
	if err := validateClientMutationSnapshot(s, "session-1"); err == nil ||
		!contains(err.Error(), "rejection outside rejected state") {
		t.Fatalf("expected rejection-outside-rejected error, got %v", err)
	}
}

// TestValidateClientMutationSnapshot_RejectedNoRejection covers the
// rejected-but-no-rejection branch (line 332-335).
func TestValidateClientMutationSnapshot_RejectedNoRejection(t *testing.T) {
	s := validPersistSnapshot("session-1")
	rec := validPersistRecord("mutation-1")
	rec.OperationState = clientMutationOperationRejected
	rec.Rejection = nil
	s.Journal["mutation-1"] = rec
	if err := validateClientMutationSnapshot(s, "session-1"); err == nil ||
		!contains(err.Error(), "no rejection") {
		t.Fatalf("expected no-rejection error, got %v", err)
	}
}

// TestValidateClientMutationSnapshot_InvalidOperationState covers the
// default-operation-state branch (line 336-337).
func TestValidateClientMutationSnapshot_InvalidOperationState(t *testing.T) {
	s := validPersistSnapshot("session-1")
	rec := validPersistRecord("mutation-1")
	rec.OperationState = clientMutationOperationState("bogus")
	s.Journal["mutation-1"] = rec
	if err := validateClientMutationSnapshot(s, "session-1"); err == nil ||
		!contains(err.Error(), "invalid operation state") {
		t.Fatalf("expected invalid-operation-state error, got %v", err)
	}
}

// TestValidateClientMutationSnapshot_InvalidProjectionState covers the
// default-projection-state branch (line 341-342).
func TestValidateClientMutationSnapshot_InvalidProjectionState(t *testing.T) {
	s := validPersistSnapshot("session-1")
	rec := validPersistRecord("mutation-1")
	rec.ProjectionState = appwire.MutationProjectionState("bogus")
	s.Journal["mutation-1"] = rec
	if err := validateClientMutationSnapshot(s, "session-1"); err == nil ||
		!contains(err.Error(), "invalid projection state") {
		t.Fatalf("expected invalid-projection-state error, got %v", err)
	}
}

// TestValidateClientMutationSnapshot_PendingExecutionKeyMismatch covers
// the pending-execution-key/mutation-id mismatch branch (line 346-348).
func TestValidateClientMutationSnapshot_PendingExecutionKeyMismatch(t *testing.T) {
	s := validPersistSnapshot("session-1")
	s.PendingExecutions["pending-1"] = appwire.PendingMutation{
		ClientMutationID: "different-id",
		Method:           "turn/start",
		ExecutionState:   "pending",
		ProjectionState:  appwire.MutationProjectionPending,
	}
	if err := validateClientMutationSnapshot(s, "session-1"); err == nil ||
		!contains(err.Error(), "does not match mutation ID") {
		t.Fatalf("expected pending key mismatch error, got %v", err)
	}
}

// TestValidateClientMutationSnapshot_Valid covers the success path.
func TestValidateClientMutationSnapshot_Valid(t *testing.T) {
	s := validPersistSnapshot("session-1")
	// Add a rejected record with rejection to exercise that branch.
	rec := validPersistRecord("mutation-2")
	rec.Payload = nil
	rec.PayloadHash = "hash-of-nothing"
	rec.OperationState = clientMutationOperationRejected
	rec.Rejection = &clientMutationRejection{Code: 1, Message: "rejected"}
	s.Journal["mutation-2"] = rec
	// Add a terminal record.
	termRec := validPersistRecord("mutation-3")
	termRec.Payload = nil
	termRec.PayloadHash = "hash-of-nothing"
	termRec.OperationState = clientMutationOperationTerminal
	s.Journal["mutation-3"] = termRec
	// Add an applied record.
	appliedRec := validPersistRecord("mutation-4")
	appliedRec.OperationState = clientMutationOperationApplied
	s.Journal["mutation-4"] = appliedRec
	if err := validateClientMutationSnapshot(s, "session-1"); err != nil {
		t.Fatalf("expected valid snapshot, got %v", err)
	}
}

// TestClientMutationSyncUnsupported covers the sync-unsupported classifier.
func TestClientMutationSyncUnsupported(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{syscall.ENOSYS, true},
		{syscall.ENOTSUP, true},
		{syscall.EINVAL, true},
		{errors.New("other"), false},
		{nil, false},
	}
	for _, tc := range tests {
		if got := clientMutationSyncUnsupported(tc.err); got != tc.want {
			t.Errorf("clientMutationSyncUnsupported(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
	// Wrapped errors are also detected.
	if !clientMutationSyncUnsupported(errors.Join(syscall.ENOSYS)) {
		t.Error("expected wrapped ENOSYS to be unsupported")
	}
}

// TestRejectTrailingClientMutationJSON covers the trailing-data and EOF branches.
func TestRejectTrailingClientMutationJSON(t *testing.T) {
	t.Run("eof is ok", func(t *testing.T) {
		decoder := json.NewDecoder(bytes.NewReader([]byte(`{"a":1}`)))
		var v map[string]any
		if err := decoder.Decode(&v); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if err := rejectTrailingClientMutationJSON(decoder); err != nil {
			t.Fatalf("expected nil for eof, got %v", err)
		}
	})
	t.Run("trailing value rejected", func(t *testing.T) {
		decoder := json.NewDecoder(bytes.NewReader([]byte(`{"a":1}{"b":2}`)))
		var v map[string]any
		if err := decoder.Decode(&v); err != nil {
			t.Fatalf("decode: %v", err)
		}
		err := rejectTrailingClientMutationJSON(decoder)
		if err == nil || !contains(err.Error(), "trailing JSON value") {
			t.Fatalf("expected trailing-value error, got %v", err)
		}
	})
	t.Run("trailing invalid data rejected", func(t *testing.T) {
		// After a valid value, invalid trailing JSON yields a non-EOF decode error.
		decoder := json.NewDecoder(bytes.NewReader([]byte(`123 abc`)))
		var v int
		if err := decoder.Decode(&v); err != nil {
			t.Fatalf("decode first: %v", err)
		}
		err := rejectTrailingClientMutationJSON(decoder)
		if err == nil {
			t.Fatal("expected error for trailing invalid data")
		}
		// io.ErrUnexpectedEOF or similar should be wrapped, not nil.
		if errors.Is(err, io.EOF) {
			t.Fatalf("expected non-EOF error, got %v", err)
		}
	})
}

// TestClientMutationRejectionUnmarshalJSON covers the rejection unmarshal paths.
func TestClientMutationRejectionUnmarshalJSON(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		data := `{"code":42,"message":"no","data":{"evener_error_info":"invalidParams","client_mutation_id":"m1","mutation_outcome":"notAccepted","retry_disposition":"automatic","cause":"because"}}`
		var r clientMutationRejection
		if err := json.Unmarshal([]byte(data), &r); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if r.Code != 42 || r.Message != "no" || r.Data.ClientMutationID != "m1" {
			t.Fatalf("unmarshaled rejection = %#v", r)
		}
	})
	t.Run("unknown field rejected", func(t *testing.T) {
		data := `{"code":1,"message":"no","data":{},"unknown":1}`
		var r clientMutationRejection
		if err := json.Unmarshal([]byte(data), &r); err == nil {
			t.Fatal("expected error for unknown field")
		}
	})
	t.Run("trailing data rejected", func(t *testing.T) {
		data := `{"code":1,"message":"no","data":{}}{}`
		var r clientMutationRejection
		if err := json.Unmarshal([]byte(data), &r); err == nil {
			t.Fatal("expected error for trailing data")
		}
	})
	t.Run("invalid json rejected", func(t *testing.T) {
		var r clientMutationRejection
		if err := json.Unmarshal([]byte(`{not json`), &r); err == nil {
			t.Fatal("expected error for invalid json")
		}
	})
}

// TestClientMutationRejectionMarshalJSON covers the rejection marshal path.
func TestClientMutationRejectionMarshalJSON(t *testing.T) {
	r := clientMutationRejection{
		Code:    7,
		Message: "denied",
		Data: appwire.ErrorData{
			ClientMutationID: "m1",
			Cause:            "test",
		},
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !contains(string(data), `"code":7`) || !contains(string(data), `"message":"denied"`) {
		t.Fatalf("unexpected json: %s", data)
	}
	// Round-trip.
	var r2 clientMutationRejection
	if err := json.Unmarshal(data, &r2); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	if r2.Code != r.Code || r2.Message != r.Message || r2.Data.ClientMutationID != r.Data.ClientMutationID {
		t.Fatalf("round-trip mismatch: %#v vs %#v", r2, r)
	}
}

// TestClientMutationPendingExecutionsMarshalJSON covers the nil and non-nil marshal paths.
func TestClientMutationPendingExecutionsMarshalJSON(t *testing.T) {
	t.Run("nil marshals to null", func(t *testing.T) {
		var pending clientMutationPendingExecutions
		data, err := json.Marshal(pending)
		if err != nil {
			t.Fatalf("marshal nil: %v", err)
		}
		if string(data) != "null" {
			t.Fatalf("expected null, got %s", data)
		}
	})
	t.Run("non-nil marshals map", func(t *testing.T) {
		pending := clientMutationPendingExecutions{
			"m1": appwire.PendingMutation{
				ClientMutationID: "m1",
				Method:           "turn/start",
				ExecutionState:   "pending",
				ProjectionState:  appwire.MutationProjectionPending,
			},
		}
		data, err := json.Marshal(pending)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !contains(string(data), `"client_mutation_id":"m1"`) {
			t.Fatalf("expected m1 in json: %s", data)
		}
	})
}

// TestClientMutationPendingExecutionsUnmarshalJSON covers the unmarshal paths.
func TestClientMutationPendingExecutionsUnmarshalJSON(t *testing.T) {
	t.Run("null yields nil", func(t *testing.T) {
		var pending clientMutationPendingExecutions
		if err := json.Unmarshal([]byte(`null`), &pending); err != nil {
			t.Fatalf("unmarshal null: %v", err)
		}
		if pending != nil {
			t.Fatalf("expected nil, got %#v", pending)
		}
	})
	t.Run("valid map", func(t *testing.T) {
		data := `{"m1":{"client_mutation_id":"m1","method":"turn/start","execution_state":"pending","projection_state":"pending"}}`
		var pending clientMutationPendingExecutions
		if err := json.Unmarshal([]byte(data), &pending); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(pending) != 1 || pending["m1"].Method != "turn/start" {
			t.Fatalf("unexpected pending: %#v", pending)
		}
	})
	t.Run("invalid json rejected", func(t *testing.T) {
		var pending clientMutationPendingExecutions
		if err := json.Unmarshal([]byte(`{not json`), &pending); err == nil {
			t.Fatal("expected error for invalid json")
		}
	})
	t.Run("entry unknown field rejected", func(t *testing.T) {
		data := `{"m1":{"client_mutation_id":"m1","method":"turn/start","execution_state":"pending","projection_state":"pending","unknown":1}}`
		var pending clientMutationPendingExecutions
		if err := json.Unmarshal([]byte(data), &pending); err == nil {
			t.Fatal("expected error for unknown field in entry")
		}
	})
	t.Run("entry invalid json rejected", func(t *testing.T) {
		data := `{"m1":{not json}`
		var pending clientMutationPendingExecutions
		if err := json.Unmarshal([]byte(data), &pending); err == nil {
			t.Fatal("expected error for invalid entry json")
		}
	})
}

// TestForgetRunningTurnNoOneOwns covers the forgetRunningTurnNoOneOwns helper.
func TestForgetRunningTurnNoOneOwns(t *testing.T) {
	t.Run("empty active turn is noop", func(t *testing.T) {
		s := newEmptyClientMutationSnapshot("s1")
		forgetRunningTurnNoOneOwns(&s)
		if s.ActiveTurnID != "" {
			t.Fatalf("expected empty active turn")
		}
	})
	t.Run("owned turn is kept", func(t *testing.T) {
		s := newEmptyClientMutationSnapshot("s1")
		s.ActiveTurnID = "turn-1"
		s.PendingExecutions["m1"] = appwire.PendingMutation{
			ClientMutationID: "m1",
			TurnID:           "turn-1",
			Method:           "turn/start",
			ExecutionState:   "pending",
			ProjectionState:  appwire.MutationProjectionPending,
		}
		forgetRunningTurnNoOneOwns(&s)
		if s.ActiveTurnID != "turn-1" {
			t.Fatalf("expected owned turn kept, got %q", s.ActiveTurnID)
		}
	})
	t.Run("unowned turn is dropped", func(t *testing.T) {
		s := newEmptyClientMutationSnapshot("s1")
		s.ActiveTurnID = "turn-1"
		s.PendingExecutions["m1"] = appwire.PendingMutation{
			ClientMutationID: "m1",
			TurnID:           "turn-other",
			Method:           "turn/start",
			ExecutionState:   "pending",
			ProjectionState:  appwire.MutationProjectionPending,
		}
		forgetRunningTurnNoOneOwns(&s)
		if s.ActiveTurnID != "" {
			t.Fatalf("expected unowned turn dropped, got %q", s.ActiveTurnID)
		}
	})
}

// TestClientMutationFilePath covers the path constructor.
func TestClientMutationFilePath(t *testing.T) {
	got := clientMutationFilePath("/state", "sess-1")
	want := "/state/mutations/sess-1.json"
	if got != want {
		t.Fatalf("clientMutationFilePath = %q, want %q", got, want)
	}
}
