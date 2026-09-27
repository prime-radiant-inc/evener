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
