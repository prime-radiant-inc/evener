package appwire

// Protocol-shape tests for evener/host/operations (deploy pipeline 08b §8, §10,
// §11): the catalog pins the method's params and response types, the params and
// response marshal field-for-field with the §10 presence rules —
// generation/incarnationId exactly on host-pinned pages, hostBoundaries
// exactly on unfiltered pages, its value union the boundary object or the
// literal "absent" — OperationRecord carries incarnationId, and the
// cursor-too-large discriminator pairs with the conflict code and its
// {capBytes: 8192} data.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// hostOperationsCatalogEntry returns the catalog entry for
// evener/host/operations, failing the test when the method is not catalogued.
func hostOperationsCatalogEntry(t *testing.T) MethodSpec {
	t.Helper()
	for _, m := range Methods {
		if m.Name == MethodEvenerHostOperations {
			return m
		}
	}
	t.Fatalf("the catalog carries no entry for %s", MethodEvenerHostOperations)
	return MethodSpec{}
}

// TestHostOperationsCatalogPinsTheWire pins the catalog half: the method exists,
// hub-scoped, with its own params and response types and no union result.
func TestHostOperationsCatalogPinsTheWire(t *testing.T) {
	entry := hostOperationsCatalogEntry(t)
	if reflect.TypeOf(entry.Params) != reflect.TypeFor[HostOperationsParams]() {
		t.Fatalf("params = %T, want HostOperationsParams", entry.Params)
	}
	if reflect.TypeOf(entry.Result) != reflect.TypeFor[HostOperationsResponse]() {
		t.Fatalf("result = %T, want HostOperationsResponse", entry.Result)
	}
	if entry.Scope != ScopeHub {
		t.Fatalf("scope = %q, want %q", entry.Scope, ScopeHub)
	}
	if _, union := MethodResultArms[MethodEvenerHostOperations]; union {
		t.Fatalf("%s is registered as a union; its response is a single struct", MethodEvenerHostOperations)
	}
}

// TestHostOperationsParamsMarshalFieldForField pins §10's params: every field
// optional (empty params list the first unfiltered page) and lowerCamel on the
// wire.
func TestHostOperationsParamsMarshalFieldForField(t *testing.T) {
	assertJSONKeys(t, HostOperationsParams{})

	assertJSONKeys(t, HostOperationsParams{
		Name:          "m4",
		OperationID:   "op-1",
		State:         OperationStateRunning,
		Generation:    2,
		IncarnationID: "inc-m4",
		ID:            "00000000000000000007",
		Limit:         50,
		Cursor:        "eyJ2IjoyfQ",
	}, "name", "operationId", "state", "generation", "incarnationId", "id", "limit", "cursor")
}

// TestHostOperationsResponsePresenceRules pins §10's response shape: the
// operations list always present; generation/incarnationId present exactly on
// host-pinned pages; hostBoundaries present exactly on unfiltered pages, with
// its value union marshaling as the {generation, incarnationId, presenceEpoch}
// object or the literal "absent"; nextCursor present exactly when set.
func TestHostOperationsResponsePresenceRules(t *testing.T) {
	pinned := HostOperationsResponse{
		Operations:    []OperationRecord{},
		Generation:    2,
		IncarnationID: "inc-m4",
		NextCursor:    "eyJ2IjoyfQ",
	}
	assertJSONKeys(t, pinned, "operations", "generation", "incarnationId", "nextCursor")

	unfiltered := HostOperationsResponse{
		Operations: []OperationRecord{},
		HostBoundaries: map[string]any{
			"m4": HostBoundary{Generation: 2, IncarnationID: "inc-m4", PresenceEpoch: 3},
			"m5": HostBoundaryAbsent,
		},
	}
	assertJSONKeys(t, unfiltered, "operations", "hostBoundaries")
	raw, err := json.Marshal(unfiltered)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `"hostBoundaries":{"m4":{"generation":2,"incarnationId":"inc-m4","presenceEpoch":3},"m5":"absent"}`
	if !strings.Contains(string(raw), want) {
		t.Fatalf("hostBoundaries bytes =\n%s\nwant to contain\n%s", raw, want)
	}

	empty := HostOperationsResponse{Operations: []OperationRecord{}}
	assertJSONKeys(t, empty, "operations")
}

// TestOperationRecordCarriesTheOperationsFields pins §10's OperationRecord
// field-for-field on this slice's wire: the S5-servable fields are all present
// (fencing-owned orphanBoundary/orphanResolved/attestation and S6's compacted
// stay off it until their slices, per the type's own comment).
func TestOperationRecordCarriesTheOperationsFields(t *testing.T) {
	record := OperationRecord{
		ID:                "00000000000000000007",
		ClientOperationID: "op-1",
		Host:              "m4",
		Generation:        2,
		IncarnationID:     "inc-m4",
		Kind:              "deploy",
		State:             OperationStateComplete,
		Progress:          []OperationProgressEntry{{TS: "2026-09-28T00:00:00Z", Message: "done"}},
		Result:            &OperationResult{OK: true, Message: "deployed"},
		CreatedAt:         "2026-09-28T00:00:00Z",
		UpdatedAt:         "2026-09-28T00:01:00Z",
		HostRemoved:       false,
	}
	assertJSONKeys(t, record, "id", "clientOperationId", "host", "generation", "incarnationId",
		"kind", "state", "progress", "result", "createdAt", "updatedAt", "hostRemoved")
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"incarnationId":"inc-m4"`) {
		t.Fatalf("record bytes %s carry no incarnationId", raw)
	}
}

// TestCursorTooLargeRefusalPairsTheDiscriminator pins §11's pair: the
// cursor-too-large refusal is a conflict-class error carrying the discriminator
// and {capBytes: 8192}, never a compacting compactSeq.
func TestCursorTooLargeRefusalPairsTheDiscriminator(t *testing.T) {
	refusal := CursorTooLarge(8192, "the boundary map would exceed the encoded cursor cap")
	if refusal.Code != CodeConflict {
		t.Fatalf("code = %d, want %d", refusal.Code, CodeConflict)
	}
	data, ok := refusal.Data.(CursorTooLargeErrorData)
	if !ok {
		t.Fatalf("data = %T, want CursorTooLargeErrorData", refusal.Data)
	}
	if data.EvenerErrorInfo != ErrorCursorTooLarge {
		t.Fatalf("evenerErrorInfo = %q, want %q", data.EvenerErrorInfo, ErrorCursorTooLarge)
	}
	if data.CapBytes != 8192 {
		t.Fatalf("capBytes = %d, want 8192", data.CapBytes)
	}
	raw, err := json.Marshal(refusal)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"evenerErrorInfo":"cursor-too-large"`, `"capBytes":8192`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("refusal bytes %s carry no %s", raw, want)
		}
	}
}
