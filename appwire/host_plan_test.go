package appwire

// Protocol-shape tests for the deploy pipeline's plan wire (08b §10, §11): the
// union's arms marshal as exactly the arm they carry — `plan` and `token`
// absent, never null, on the no-token arm — the union decodes back to one arm,
// the catalog pins the method's params/result and its union registration, and
// §11's stale-entry discriminator pairs with the conflict code.

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// hostPlanCatalogEntry returns the catalog entry for evener/host/plan, failing
// the test when the method is not catalogued.
func hostPlanCatalogEntry(t *testing.T) MethodSpec {
	t.Helper()
	for _, m := range Methods {
		if m.Name == MethodEvenerHostPlan {
			return m
		}
	}
	t.Fatalf("the catalog carries no entry for %s", MethodEvenerHostPlan)
	return MethodSpec{}
}

// TestHostPlanCatalogPinsTheUnionShape pins the catalog half of the plan: the
// method exists with its own params and the union result, and its union
// registration names exactly the two arms, each a named struct of its own.
func TestHostPlanCatalogPinsTheUnionShape(t *testing.T) {
	entry := hostPlanCatalogEntry(t)
	if reflect.TypeOf(entry.Params) != reflect.TypeFor[HostPlanParams]() {
		t.Fatalf("params = %T, want HostPlanParams", entry.Params)
	}
	if reflect.TypeOf(entry.Result) != reflect.TypeFor[HostPlanResult]() {
		t.Fatalf("result = %T, want HostPlanResult", entry.Result)
	}
	if entry.Scope != ScopeHub {
		t.Fatalf("scope = %q, want %q", entry.Scope, ScopeHub)
	}
	arms, ok := MethodResultArms[MethodEvenerHostPlan]
	if !ok {
		t.Fatalf("the union registration carries no arms for %s", MethodEvenerHostPlan)
	}
	names := make([]string, 0, len(arms))
	for _, arm := range arms {
		typ := reflect.TypeOf(arm)
		if typ == nil || typ.Kind() != reflect.Struct {
			t.Fatalf("arm %v is not a named struct", arm)
		}
		names = append(names, typ.Name())
	}
	want := []string{"HostPlanPlanned", "HostPlanNoToken"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("arms = %v, want %v", names, want)
	}
	// Every registered method name is one the catalog defines: a typo in the
	// registration would otherwise silently drop the union rendering.
	for name, registered := range MethodResultArms {
		if len(registered) == 0 {
			t.Fatalf("method %q is registered as a union with no arms", name)
		}
		found := false
		for _, m := range Methods {
			if m.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("the union registration names %q, which the catalog does not define", name)
		}
	}
}

// TestHostPlanArmsMarshalExactly is the §10 shape rule: each arm carries its own
// keys and nothing else — in particular `plan` and `token` are absent, never
// null, on the no-token arm — and the union decodes back to exactly one arm.
func TestHostPlanArmsMarshalExactly(t *testing.T) {
	planned := HostPlanResult{HostPlanPlanned: &HostPlanPlanned{
		Outcome: HostPlanOutcomePlanned,
		Plan: HostPlan{
			Host:               "m4",
			Generation:         7,
			TargetPath:         "/opt/evener/bin/evener",
			ControllerRevision: "v1.2.3",
			FactsRevision:      "digest",
			HubTOMLFingerprint: "fingerprint",
			FactsCapturedAt:    "2026-09-27T12:00:00Z",
			FactsAgeSec:        3,
			RunningVersion:     "v1.1.0",
			RunningHealthy:     true,
		},
		Token: strings.Repeat("A", 32),
	}}
	assertJSONKeys(t, planned, "outcome", "plan", "token")

	noToken := HostPlanResult{HostPlanNoToken: &HostPlanNoToken{
		Outcome: HostPlanOutcomeNoToken,
		StaleFacts: HostPlanStaleFacts{
			Message:  "host is not attached",
			Attached: false,
			Reason:   HostPlanReasonUnattached,
		},
		Terminal: false,
	}}
	assertJSONKeys(t, noToken, "outcome", "staleFacts", "terminal")

	// remnantId is present exactly on the remnant-open arm.
	withRemnant := HostPlanResult{HostPlanNoToken: &HostPlanNoToken{
		Outcome: HostPlanOutcomeNoToken,
		StaleFacts: HostPlanStaleFacts{
			Message:  "an open teardown remnant fences this name",
			Attached: true,
			Reason:   HostPlanReasonRemnantOpen,
		},
		Terminal:  false,
		RemnantID: "remnant-1",
	}}
	assertJSONKeys(t, withRemnant, "outcome", "staleFacts", "terminal", "remnantId")

	for name, union := range map[string]HostPlanResult{"planned": planned, "no-token": noToken, "remnant": withRemnant} {
		raw, err := json.Marshal(union)
		if err != nil {
			t.Fatalf("%s arm: marshal: %v", name, err)
		}
		var decoded HostPlanResult
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("%s arm: unmarshal %s: %v", name, raw, err)
		}
		wantPlanned := union.HostPlanPlanned != nil
		if (decoded.HostPlanPlanned != nil) != wantPlanned {
			t.Fatalf("%s arm decoded to the wrong arm: %s", name, raw)
		}
		if (decoded.HostPlanNoToken != nil) != (union.HostPlanNoToken != nil) {
			t.Fatalf("%s arm decoded to the wrong arm: %s", name, raw)
		}
		again, err := json.Marshal(decoded)
		if err != nil {
			t.Fatalf("%s arm: re-marshal: %v", name, err)
		}
		if !bytes.Equal(again, raw) {
			t.Fatalf("%s arm round trip changed the wire bytes:\n%s\n%s", name, raw, again)
		}
	}
}

// assertJSONKeys marshals v and fails unless the JSON object carries exactly the
// given keys, in sorted order.
func assertJSONKeys(t *testing.T, v any, keys ...string) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("marshal %T produced non-object %s: %v", v, raw, err)
	}
	if len(object) != len(keys) {
		t.Fatalf("keys of %s = %v, want exactly %v", raw, objectKeys(object), keys)
	}
	for _, key := range keys {
		if value, ok := object[key]; !ok {
			t.Fatalf("keys of %s = %v, want %v", raw, objectKeys(object), keys)
		} else if string(value) == "null" {
			t.Fatalf("key %q of %s is null, want absent or a value", key, raw)
		}
	}
}

// objectKeys names a decoded object's keys, for failure messages.
func objectKeys(object map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	return keys
}

// TestStaleEntryRefusalPairsTheDiscriminatorWithTheConflictCode is §11's pinned
// pair: the stale-entry refusal is a conflict-class error carrying the
// discriminator and the binding that moved.
func TestStaleEntryRefusalPairsTheDiscriminatorWithTheConflictCode(t *testing.T) {
	refusal := StaleEntry(StaleEntryBindingGeneration, "the host's registration moved")
	if refusal.Code != CodeConflict {
		t.Fatalf("code = %d, want %d", refusal.Code, CodeConflict)
	}
	data, ok := refusal.Data.(StaleEntryErrorData)
	if !ok {
		t.Fatalf("data = %T, want StaleEntryErrorData", refusal.Data)
	}
	if data.EvenerErrorInfo != ErrorStaleEntry {
		t.Fatalf("evenerErrorInfo = %q, want %q", data.EvenerErrorInfo, ErrorStaleEntry)
	}
	if data.Binding != StaleEntryBindingGeneration {
		t.Fatalf("binding = %q, want %q", data.Binding, StaleEntryBindingGeneration)
	}
	raw, err := json.Marshal(refusal)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"evenerErrorInfo":"stale-entry"`, `"binding":"generation"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("refusal bytes %s carry no %s", raw, want)
		}
	}
}
