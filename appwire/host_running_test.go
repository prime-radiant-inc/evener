package appwire

// evener/host/running's wire shapes (deploy pipeline 08b §10): the required
// fencing epoch on the params, the local revision/health/process-start-time
// response with the absent-when-unknown rule for the start time, and the two
// busy-class errors §5 renders for a held per-host gate. The catalog entry is
// pinned here too, so a handler or client cannot drift from the shapes.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// runningCatalogEntry returns the catalog entry for evener/host/running,
// failing the test when the method is not catalogued.
func runningCatalogEntry(t *testing.T) MethodSpec {
	t.Helper()
	for _, m := range Methods {
		if m.Name == MethodEvenerHostRunning {
			return m
		}
	}
	t.Fatalf("the catalog carries no entry for %s", MethodEvenerHostRunning)
	return MethodSpec{}
}

// TestHostRunningCatalogPinsTheShapes pins the catalog half: the method exists
// with its own params/result types on the hub scope, and it is not a union.
func TestHostRunningCatalogPinsTheShapes(t *testing.T) {
	entry := runningCatalogEntry(t)
	if reflect.TypeOf(entry.Params) != reflect.TypeFor[HostRunningParams]() {
		t.Fatalf("params = %T, want HostRunningParams", entry.Params)
	}
	if reflect.TypeOf(entry.Result) != reflect.TypeFor[HostRunningResponse]() {
		t.Fatalf("result = %T, want HostRunningResponse", entry.Result)
	}
	if entry.Scope != ScopeHub {
		t.Fatalf("scope = %q, want %q", entry.Scope, ScopeHub)
	}
	if _, union := MethodResultArms[MethodEvenerHostRunning]; union {
		t.Fatal("evener/host/running registered as a result union")
	}
}

// TestHostRunningParamsCarryTheEpochFieldForField pins §10's params: exactly
// `fencingEpoch: {bootId, opSeq}` — the generated client carries the field, so
// a well-formed call never presents a defaulted epoch.
func TestHostRunningParamsCarryTheEpochFieldForField(t *testing.T) {
	params := HostRunningParams{FencingEpoch: FencingEpoch{BootID: "boot-1", OpSeq: 7}}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"fencingEpoch":{"bootId":"boot-1","opSeq":7}}`
	if string(raw) != want {
		t.Fatalf("params = %s, want %s", raw, want)
	}
	var decoded HostRunningParams
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.FencingEpoch != params.FencingEpoch {
		t.Fatalf("decoded epoch = %+v, want %+v", decoded.FencingEpoch, params.FencingEpoch)
	}
}

// TestHostRunningResponseAbsentWhenUnknown pins the response's presence rule:
// processStartTime is present exactly when the serving hub knows it, and absent
// — never null — otherwise.
func TestHostRunningResponseAbsentWhenUnknown(t *testing.T) {
	without, err := json.Marshal(HostRunningResponse{BuildRevision: "v1.2.3", Healthy: true})
	if err != nil {
		t.Fatalf("marshal without start time: %v", err)
	}
	if strings.Contains(string(without), "processStartTime") {
		t.Fatalf("response %s carries a processStartTime the hub does not know", without)
	}
	if string(without) != `{"buildRevision":"v1.2.3","healthy":true}` {
		t.Fatalf("response = %s, want buildRevision/healthy only", without)
	}
	with, err := json.Marshal(HostRunningResponse{BuildRevision: "dev", Healthy: false, ProcessStartTime: "2026-09-27T12:00:00Z"})
	if err != nil {
		t.Fatalf("marshal with start time: %v", err)
	}
	if !strings.Contains(string(with), `"processStartTime":"2026-09-27T12:00:00Z"`) {
		t.Fatalf("response %s lost the known process start time", with)
	}
}

// TestHostBusyErrorsPairDiscriminatorsWithTheConflictCode pins §11's busy
// class: both refusals are conflict-class errors carrying their discriminators,
// and the operation form names the record id while the transient form carries
// no operation reference.
func TestHostBusyErrorsPairDiscriminatorsWithTheConflictCode(t *testing.T) {
	operation := HostBusyOperation("00000000000000000042", "host busy: an operation is in progress")
	if operation.Code != CodeConflict {
		t.Fatalf("operation busy code = %d, want %d", operation.Code, CodeConflict)
	}
	data, ok := operation.Data.(HostBusyOperationErrorData)
	if !ok {
		t.Fatalf("operation busy data = %T, want HostBusyOperationErrorData", operation.Data)
	}
	if data.EvenerErrorInfo != ErrorHostBusyOperation || data.OperationID != "00000000000000000042" {
		t.Fatalf("operation busy data = %+v, want the discriminator and the operation id", data)
	}
	raw, err := json.Marshal(operation)
	if err != nil {
		t.Fatalf("marshal operation busy: %v", err)
	}
	for _, want := range []string{`"evenerErrorInfo":"host-busy-operation"`, `"operationId":"00000000000000000042"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("operation busy bytes %s carry no %s", raw, want)
		}
	}

	transient := HostBusyTransient(`host "m4" busy (plan in progress)`)
	if transient.Code != CodeConflict {
		t.Fatalf("transient busy code = %d, want %d", transient.Code, CodeConflict)
	}
	transientData, ok := transient.Data.(ErrorData)
	if !ok {
		t.Fatalf("transient busy data = %T, want ErrorData", transient.Data)
	}
	if transientData.EvenerErrorInfo != ErrorHostBusyTransient {
		t.Fatalf("transient busy discriminator = %q, want %q", transientData.EvenerErrorInfo, ErrorHostBusyTransient)
	}
	raw, err = json.Marshal(transient)
	if err != nil {
		t.Fatalf("marshal transient busy: %v", err)
	}
	if !strings.Contains(string(raw), `"evenerErrorInfo":"host-busy-transient"`) {
		t.Fatalf("transient busy bytes %s carry no discriminator", raw)
	}
	if strings.Contains(string(raw), "operationId") {
		t.Fatalf("transient busy bytes %s carry an operation reference", raw)
	}
}
