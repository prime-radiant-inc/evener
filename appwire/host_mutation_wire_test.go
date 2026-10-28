package appwire

// Protocol-shape tests for the registry mutations' guarded wire (registry spec
// 08 §4, §11, §12): the mutation params carry the idempotency key and the
// (generation, incarnation id) guard pair in the wire spelling a dialog uses,
// HostRow carries the current pair a caller echoes back, the add key stays
// optional, and §12's conflicting-mutation-id rides the conflict-class envelope
// with the discriminator only.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestHostMutationParamsCarryTheGuardFields pins §11's shapes: update/remove
// carry mutationId, expectedGeneration and expectedIncarnationId; add's
// mutationId is optional, so a keyless add marshals no mutationId key at all.
func TestHostMutationParamsCarryTheGuardFields(t *testing.T) {
	entry := HostEntry{Address: "m4.example"}
	assertJSONKeys(t, HostAddParams{Entry: entry}, "entry")
	assertJSONKeys(t, HostAddParams{Entry: entry, MutationID: "mut-1"}, "entry", "mutationId")

	update := HostUpdateParams{
		Name:                  "m4",
		Entry:                 entry,
		MutationID:            "mut-2",
		ExpectedGeneration:    7,
		ExpectedIncarnationID: "inc-7",
	}
	assertJSONKeys(t, update, "name", "entry", "mutationId", "expectedGeneration", "expectedIncarnationId")
	raw, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("marshal update params: %v", err)
	}
	for _, want := range []string{`"mutationId":"mut-2"`, `"expectedGeneration":7`, `"expectedIncarnationId":"inc-7"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("update params %s carry no %s", raw, want)
		}
	}

	remove := HostRemoveParams{
		Name:                  "m4",
		MutationID:            "mut-3",
		ExpectedGeneration:    7,
		ExpectedIncarnationID: "inc-7",
	}
	assertJSONKeys(t, remove, "name", "mutationId", "expectedGeneration", "expectedIncarnationId")

	// The guard field set is exactly the three fields §11 names on both
	// requests: a renamed, dropped, or added wire key fails here rather than at
	// a client.
	for _, typ := range []reflect.Type{reflect.TypeFor[HostUpdateParams](), reflect.TypeFor[HostRemoveParams]()} {
		fields := map[string]bool{}
		for field := range typ.Fields() {
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			fields[name] = true
		}
		for _, name := range []string{"mutationId", "expectedGeneration", "expectedIncarnationId"} {
			if !fields[name] {
				t.Fatalf("%s carries no %q field", typ, name)
			}
		}
	}
}

// TestHostRowCarriesTheGuardedPair pins §11's row half: the live entry's current
// (generation, incarnation id) pair is required on every row, because the UI
// echoes both values back as the guarded-mutation expectations.
func TestHostRowCarriesTheGuardedPair(t *testing.T) {
	row := HostRow{Name: "m4", Origin: "hub.toml", Generation: 7, IncarnationID: "inc-7"}
	assertJSONKeys(t, row, "name", "origin", "attached", "midAttach", "removed", "generation", "incarnationId")
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}
	for _, want := range []string{`"generation":7`, `"incarnationId":"inc-7"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("row %s carries no %s", raw, want)
		}
	}
}

// TestConflictingMutationIDCarriesTheDiscriminatorOnly pins §12's classification
// pair: conflict class, the conflicting-mutation-id discriminator, no data
// beyond it — the same envelope its operation-id sibling rides.
func TestConflictingMutationIDCarriesTheDiscriminatorOnly(t *testing.T) {
	refusal := ConflictingMutationID(`host "m4": mutation id "mut-1" is already used by another host`)
	if refusal.Code != CodeConflict {
		t.Fatalf("code = %d, want %d", refusal.Code, CodeConflict)
	}
	data, ok := refusal.Data.(ErrorData)
	if !ok {
		t.Fatalf("data = %T, want ErrorData", refusal.Data)
	}
	if data.EvenerErrorInfo != ErrorConflictingMutationID {
		t.Fatalf("evenerErrorInfo = %q, want %q", data.EvenerErrorInfo, ErrorConflictingMutationID)
	}
	raw, err := json.Marshal(refusal)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"evenerErrorInfo":"conflicting-mutation-id"`) {
		t.Fatalf("refusal bytes %s carry no discriminator", raw)
	}
}

// TestHostRowTombstoneFields pins §11's S11 row fields: `retainedRows` is
// present on tombstone rows only (a live row carries no key at all; a
// tombstone with a zero-row projection still renders `retainedRows: 0`), and
// `rowsTruncated` is present as true exactly on a truncated projection.
func TestHostRowTombstoneFields(t *testing.T) {
	live := HostRow{Name: "m4", Origin: "hub.toml", Generation: 7, IncarnationID: "inc-7"}
	assertJSONKeys(t, live, "name", "origin", "attached", "midAttach", "removed", "generation", "incarnationId")

	retained := 0
	tombstone := HostRow{
		Name: "gone", Origin: "hub.toml", Removed: true,
		Generation: 3, IncarnationID: "inc-3",
		RetainedRows: &retained,
	}
	assertJSONKeys(t, tombstone, "name", "origin", "attached", "midAttach", "removed", "generation", "incarnationId", "retainedRows")
	raw, err := json.Marshal(tombstone)
	if err != nil {
		t.Fatalf("marshal tombstone row: %v", err)
	}
	if !strings.Contains(string(raw), `"retainedRows":0`) {
		t.Fatalf("tombstone row %s carries no retainedRows:0", raw)
	}
	if strings.Contains(string(raw), "rowsTruncated") {
		t.Fatalf("untruncated tombstone row %s carries rowsTruncated", raw)
	}
	truncated := tombstone
	truncated.RowsTruncated = true
	raw, err = json.Marshal(truncated)
	if err != nil {
		t.Fatalf("marshal truncated row: %v", err)
	}
	if !strings.Contains(string(raw), `"rowsTruncated":true`) {
		t.Fatalf("truncated tombstone row %s carries no rowsTruncated:true", raw)
	}
}

// TestTombstoneCapacityCarriesBoundAndBlockingNames pins §11/§12's
// classification pair: conflict class, the tombstone-capacity discriminator,
// and data `{bound, blockingNames}`.
func TestTombstoneCapacityCarriesBoundAndBlockingNames(t *testing.T) {
	refusal := TombstoneCapacity(TombstoneCapacityBoundCount, []string{"m4", "m5"},
		`the tombstones in hub.toml would exceed the global count bound`)
	if refusal.Code != CodeConflict {
		t.Fatalf("code = %d, want %d", refusal.Code, CodeConflict)
	}
	data, ok := refusal.Data.(TombstoneCapacityErrorData)
	if !ok {
		t.Fatalf("data = %T, want TombstoneCapacityErrorData", refusal.Data)
	}
	if data.EvenerErrorInfo != ErrorTombstoneCapacity {
		t.Fatalf("evenerErrorInfo = %q, want %q", data.EvenerErrorInfo, ErrorTombstoneCapacity)
	}
	if data.Bound != TombstoneCapacityBoundCount {
		t.Fatalf("bound = %q, want %q", data.Bound, TombstoneCapacityBoundCount)
	}
	if len(data.BlockingNames) != 2 || data.BlockingNames[0] != "m4" || data.BlockingNames[1] != "m5" {
		t.Fatalf("blockingNames = %v, want the gated names", data.BlockingNames)
	}
	raw, err := json.Marshal(refusal)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"evenerErrorInfo":"tombstone-capacity"`, `"bound":"count"`, `"blockingNames":["m4","m5"]`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("refusal bytes %s carry no %s", raw, want)
		}
	}
	// The copy the refusal carries is defensive: mutating the caller's slice
	// must not reach the envelope.
	names := []string{"m4"}
	copied := TombstoneCapacity(TombstoneCapacityBoundBytes, names, "over")
	names[0] = "mutated"
	copiedData := copied.Data.(TombstoneCapacityErrorData)
	if copiedData.BlockingNames[0] != "m4" {
		t.Fatal("the envelope aliases the caller's blockingNames slice")
	}
}

// TestHostMutationResultArmsRoundTrip pins the result union's codec (registry
// spec 08 §11): every arm round-trips through the one pointer it sets, the
// removed-row arms are told apart from the live-row arms by the row shape
// itself, and a union with no arm or more than one is a marshaling error.
func TestHostMutationResultArmsRoundTrip(t *testing.T) {
	t.Parallel()
	row := HostRow{Name: "m4.example", Generation: 7}
	removedRow := RemovedRow{Name: "m4.example", Generation: 7}
	cases := []struct {
		name string
		arm  any
		set  func(*HostMutationResult) bool
	}{
		{
			name: "committed live",
			arm:  HostMutationCommitted{Outcome: HostMutationOutcomeCommitted, Host: row},
			set: func(u *HostMutationResult) bool {
				return u.HostMutationCommitted != nil && u.HostMutationCommittedRemoved == nil
			},
		},
		{
			name: "committed removed",
			arm:  HostMutationCommittedRemoved{Outcome: HostMutationOutcomeCommitted, Host: removedRow},
			set: func(u *HostMutationResult) bool {
				return u.HostMutationCommittedRemoved != nil && u.HostMutationCommitted == nil
			},
		},
		{
			name: "teardown failure live",
			arm:  HostMutationTeardownFailure{Outcome: HostMutationOutcomeTeardownFailure, Seam: "detach", RemnantID: "r1", Host: row},
			set: func(u *HostMutationResult) bool {
				return u.HostMutationTeardownFailure != nil && u.HostMutationTeardownFailureRemoved == nil
			},
		},
		{
			name: "teardown failure removed",
			arm:  HostMutationTeardownFailureRemoved{Outcome: HostMutationOutcomeTeardownFailure, Seam: "detach", RemnantID: "r2", Host: removedRow},
			set: func(u *HostMutationResult) bool {
				return u.HostMutationTeardownFailureRemoved != nil && u.HostMutationTeardownFailure == nil
			},
		},
		{
			name: "collision dropped",
			arm:  HostMutationCollisionDropped{Outcome: HostMutationOutcomeCollisionDropped, DroppedEntry: row, WinningFingerprint: "abc"},
			set:  func(u *HostMutationResult) bool { return u.HostMutationCollisionDropped != nil },
		},
		{
			name: "ambiguous",
			arm:  HostMutationAmbiguous{Outcome: HostMutationOutcomeAmbiguous, ObservedRow: row},
			set:  func(u *HostMutationResult) bool { return u.HostMutationAmbiguous != nil },
		},
	}
	for _, tc := range cases {
		raw, err := json.Marshal(tc.arm)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		var decoded HostMutationResult
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("%s: decode %s: %v", tc.name, raw, err)
		}
		if !tc.set(&decoded) {
			t.Fatalf("%s: decoded %s set the wrong arms: %+v", tc.name, raw, decoded)
		}
		remarshaled, err := json.Marshal(decoded)
		if err != nil {
			t.Fatalf("%s: remarshal: %v", tc.name, err)
		}
		var again map[string]json.RawMessage
		if err := json.Unmarshal(remarshaled, &again); err != nil {
		}
	}
}

// TestHostMutationResultRefusesUndeclaredValues pins the union's refusals: no
// arm and more than one arm are marshaling errors, an unknown outcome is a
// decode error that sets no arm, and each declared outcome decodes through its
// own arm type, so a host of the wrong shape for that arm is refused.
func TestHostMutationResultRefusesUndeclaredValues(t *testing.T) {
	t.Parallel()
	if _, err := json.Marshal(HostMutationResult{}); err == nil {
		t.Fatal("marshaling a union carrying no arm succeeded, want an error")
	}
	both := HostMutationResult{
		HostMutationCommitted: &HostMutationCommitted{},
		HostMutationAmbiguous: &HostMutationAmbiguous{},
	}
	if _, err := json.Marshal(both); err == nil {
		t.Fatal("marshaling a union carrying two arms succeeded, want an error")
	}
	var decoded HostMutationResult
	if err := json.Unmarshal([]byte(`{"outcome":"bogus"}`), &decoded); err == nil {
		t.Fatal("unmarshaling an unknown outcome succeeded, want an error")
	}
	if decoded.HostMutationCommitted != nil {
		t.Fatalf("a refused decode set an arm: %+v", decoded)
	}
	malformed := []string{
		`{"outcome":"committed","host":"not-a-row"}`,
		`{"outcome":"committed","host":{"midEnsure":"not-a-bool"}}`,
		`{"outcome":"committed-with-teardown-failure","host":"not-a-row"}`,
		`{"outcome":"committed-with-teardown-failure","host":{"midEnsure":"not-a-bool"}}`,
		`{"outcome":"collision-dropped","droppedEntry":"not-a-row"}`,
		`{"outcome":"ambiguous","observedRow":"not-a-row"}`,
	}
	for _, raw := range malformed {
		var decoded HostMutationResult
		if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
			t.Fatalf("decoding %s succeeded, want the arm's own decode error", raw)
		}
	}
}
