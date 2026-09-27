package appwire

// Protocol-shape tests for the deploy pipeline's operation wire (08b §10, §11):
// the deploy/restart catalog entries with their request/response shapes, the
// operation record and its closed state set, and the §11 discriminator/code
// pairs this slice's paths emit.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// catalogEntryFor returns the catalog entry for a method, failing the test when
// it is not catalogued.
func catalogEntryFor(t *testing.T, method string) MethodSpec {
	t.Helper()
	for _, m := range Methods {
		if m.Name == method {
			return m
		}
	}
	t.Fatalf("the catalog carries no entry for %s", method)
	return MethodSpec{}
}

// TestHostDeployAndRestartCatalogShapes pins the §10 catalog half: both methods
// are hub-scoped with their own params and single-struct results, and neither
// is registered as a union.
func TestHostDeployAndRestartCatalogShapes(t *testing.T) {
	deploy := catalogEntryFor(t, MethodEvenerHostDeploy)
	if reflect.TypeOf(deploy.Params) != reflect.TypeFor[HostDeployParams]() {
		t.Fatalf("deploy params = %T, want HostDeployParams", deploy.Params)
	}
	if reflect.TypeOf(deploy.Result) != reflect.TypeFor[HostDeployResponse]() {
		t.Fatalf("deploy result = %T, want HostDeployResponse", deploy.Result)
	}
	if deploy.Scope != ScopeHub {
		t.Fatalf("deploy scope = %q, want %q", deploy.Scope, ScopeHub)
	}
	restart := catalogEntryFor(t, MethodEvenerHostRestart)
	if reflect.TypeOf(restart.Params) != reflect.TypeFor[HostRestartParams]() {
		t.Fatalf("restart params = %T, want HostRestartParams", restart.Params)
	}
	if reflect.TypeOf(restart.Result) != reflect.TypeFor[HostRestartResponse]() {
		t.Fatalf("restart result = %T, want HostRestartResponse", restart.Result)
	}
	if restart.Scope != ScopeHub {
		t.Fatalf("restart scope = %q, want %q", restart.Scope, ScopeHub)
	}
	for _, method := range []string{MethodEvenerHostDeploy, MethodEvenerHostRestart} {
		if _, union := MethodResultArms[method]; union {
			t.Fatalf("%s is registered as a union result, want a single struct", method)
		}
	}
}

// TestHostDeployAndRestartRequestShapes pins §10's request and response fields
// field-for-field.
func TestHostDeployAndRestartRequestShapes(t *testing.T) {
	assertJSONKeys(t, HostDeployParams{Name: "m4", Token: "t", OperationID: "op-1"}, "name", "token", "operationId")
	assertJSONKeys(t, HostDeployResponse{ID: "1", ClientOperationID: "op-1", State: OperationStatePending},
		"id", "clientOperationId", "state")
	assertJSONKeys(t, HostRestartParams{Name: "m4", OperationID: "op-1", Generation: 2, IncarnationID: "inc-2"},
		"name", "operationId", "generation", "incarnationId")
	assertJSONKeys(t, HostRestartResponse{ID: "1", ClientOperationID: "op-1", State: OperationStatePending},
		"id", "clientOperationId", "state")
}

// TestOperationRecordShape pins the record's wire fields and its closed state
// set, including `result` present exactly on terminal records by construction
// of the marshaller.
func TestOperationRecordShape(t *testing.T) {
	record := OperationRecord{
		ID:                "00000000000000000001",
		ClientOperationID: "op-1",
		Host:              "m4",
		Generation:        2,
		IncarnationID:     "inc-2",
		Kind:              "deploy",
		State:             OperationStateComplete,
		Progress:          []OperationProgressEntry{{TS: "2026-09-27T12:00:00Z", Message: "pushed"}},
		Result:            &OperationResult{OK: true, Message: "done"},
		CreatedAt:         "2026-09-27T11:59:00Z",
		UpdatedAt:         "2026-09-27T12:00:00Z",
	}
	assertJSONKeys(t, record, "id", "clientOperationId", "host", "generation", "incarnationId",
		"kind", "state", "progress", "result", "createdAt", "updatedAt", "hostRemoved")

	states := []OperationState{
		OperationStatePending, OperationStateRunning, OperationStateComplete,
		OperationStateFailed, OperationStateInterrupted, OperationStateOrphanUnverified,
	}
	want := []string{"pending", "running", "complete", "failed", "interrupted", "orphan-unverified"}
	for i, state := range states {
		if string(state) != want[i] {
			t.Fatalf("state %d = %q, want %q", i, state, want[i])
		}
	}
}

// TestDeployPipelineRefusalsPairDiscriminatorWithCode is §11's pinned pair for
// every discriminator this slice's paths emit: the exact numeric code per
// discriminator, with the data field the client branches on.
func TestDeployPipelineRefusalsPairDiscriminatorWithCode(t *testing.T) {
	cases := []struct {
		name string
		err  WireError
		want ErrorInfo
		code int
		keys []string
	}{
		{"token-missing", TokenMissing("no row"), ErrorTokenMissing, CodeConflict, []string{"evenerErrorInfo"}},
		{"token-mismatched", TokenMismatched("wrong shape"), ErrorTokenMismatched, CodeConflict, []string{"evenerErrorInfo"}},
		{"token-superseded", TokenSuperseded("newer mint"), ErrorTokenSuperseded, CodeConflict, []string{"evenerErrorInfo"}},
		{"token-expired", TokenExpired("deadline passed"), ErrorTokenExpired, CodeConflict, []string{"evenerErrorInfo"}},
		{"conflicting-operation-id", ConflictingOperationID("used up"), ErrorConflictingOperationID, CodeConflict, []string{"evenerErrorInfo"}},
		{"host-detached", HostDetached("no channel"), ErrorHostDetached, CodeUnavailable, []string{"evenerErrorInfo"}},
		{"probe-failed", ProbeFailed("m4", ProbeFailureTimedOut, "timed out"), ErrorProbeFailed, CodeUnavailable,
			[]string{"evenerErrorInfo", "host", "failure"}},
		{"remnant-open", RemnantOpen("remnant-7", "fenced"), ErrorRemnantOpen, CodeConflict, []string{"evenerErrorInfo", "remnantId"}},
		{"host-busy-operation", HostBusyOperation("00000000000000000042", "busy"), ErrorHostBusyOperation, CodeConflict,
			[]string{"evenerErrorInfo", "operationId"}},
		{"host-busy-transient", HostBusyTransient("busy"), ErrorHostBusyTransient, CodeConflict, []string{"evenerErrorInfo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err.Code != tc.code {
				t.Fatalf("code = %d, want %d", tc.err.Code, tc.code)
			}
			raw, err := json.Marshal(tc.err)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !strings.Contains(string(raw), `"evenerErrorInfo":"`+string(tc.want)+`"`) {
				t.Fatalf("refusal bytes %s carry no %q", raw, tc.want)
			}
			var data map[string]json.RawMessage
			if err := json.Unmarshal(raw, &data); err != nil {
				t.Fatalf("unmarshal refusal %s: %v", raw, err)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(data["data"], &payload); err != nil {
				t.Fatalf("refusal %s carries no data object: %v", raw, err)
			}
			if len(payload) != len(tc.keys) {
				t.Fatalf("data keys of %s = %v, want exactly %v", raw, payload, tc.keys)
			}
			for _, key := range tc.keys {
				if _, ok := payload[key]; !ok {
					t.Fatalf("data of %s carries no %q", raw, key)
				}
			}
		})
	}
}

// TestRemnantOpenCarriesTheRemnantID is §11's data rule: the refusal names the
// blocking remnant, mirroring the plan no-token arm's remnantId field-for-field.
func TestRemnantOpenCarriesTheRemnantID(t *testing.T) {
	raw, err := json.Marshal(RemnantOpen("remnant-9", "fenced"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"remnantId":"remnant-9"`) {
		t.Fatalf("refusal bytes %s do not name the remnant", raw)
	}
}
