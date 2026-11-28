package appwire

// Protocol-shape tests for evener/host/operations (deploy pipeline 08b §8,
// §10): the catalog pins the method's params and response types, the params and
// response marshal field-for-field with the §10 presence rule —
// generation/incarnationId exactly on host-pinned reads — and OperationRecord
// carries incarnationId.

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
	}, "name", "operationId", "state", "generation", "incarnationId", "id", "limit")
}

// TestHostOperationsResponsePresenceRules pins §10's response shape: the
// operations list always present, and generation/incarnationId present exactly
// on host-pinned reads.
func TestHostOperationsResponsePresenceRules(t *testing.T) {
	pinned := HostOperationsResponse{
		Operations:    []OperationRecord{},
		Generation:    2,
		IncarnationID: "inc-m4",
	}
	assertJSONKeys(t, pinned, "operations", "generation", "incarnationId")

	unfiltered := HostOperationsResponse{Operations: []OperationRecord{}}
	assertJSONKeys(t, unfiltered, "operations")

	empty := HostOperationsResponse{Operations: []OperationRecord{}}
	assertJSONKeys(t, empty, "operations")
}

// TestOperationRecordCarriesTheOperationsFields pins §10's OperationRecord
// field-for-field: every servable field is present, and S6's `compacted`
// marker appears exactly on a tombstone replay (fencing-owned
// orphanBoundary/orphanResolved/attestation stay off it until their slices,
// per the type's own comment).
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
	record.Compacted = true
	raw, err = json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal(compacted): %v", err)
	}
	if !strings.Contains(string(raw), `"compacted":true`) {
		t.Fatalf("tombstone-replay record bytes %s carry no compacted marker", raw)
	}
	record.Compacted = false
	raw, err = json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal(retained): %v", err)
	}
	if strings.Contains(string(raw), `"compacted"`) {
		t.Fatalf("retained record bytes %s carry a compacted marker", raw)
	}
}
