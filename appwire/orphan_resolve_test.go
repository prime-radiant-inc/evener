package appwire

// Tests for crash-fencing spec 08c §5/§8/§9's wire shapes: the
// `evener/host/orphan-resolve` method and its attestation params, the
// `orphanBoundary` / `BoundaryEntry[]` five-variant union, the OperationRecord
// orphan fields' presence rules, and the fence discriminators with their data
// shapes. The store and handler halves live in their own packages.

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestOrphanResolveCatalogPinsTheShape pins the method's catalog entry: its own
// params and result types on the hub scope, with no union-result registration.
func TestOrphanResolveCatalogPinsTheShape(t *testing.T) {
	if _, union := MethodResultArms[MethodEvenerHostOrphanResolve]; union {
		t.Fatalf("%s is registered as a union result; its response is the single OperationRecord", MethodEvenerHostOrphanResolve)
	}
	for _, m := range Methods {
		if m.Name != MethodEvenerHostOrphanResolve {
			continue
		}
		if _, ok := m.Params.(HostOrphanResolveParams); !ok {
			t.Fatalf("params type = %T, want HostOrphanResolveParams", m.Params)
		}
		if _, ok := m.Result.(OperationRecord); !ok {
			t.Fatalf("result type = %T, want OperationRecord", m.Result)
		}
		if m.Scope != ScopeHub {
			t.Fatalf("scope = %q, want %q", m.Scope, ScopeHub)
		}
		return
	}
	t.Fatalf("the catalog carries no entry for %s", MethodEvenerHostOrphanResolve)
}

// TestOrphanResolveParamsPinTheAttestationFieldForField pins §9's params shape:
// `{id: string, attestation?: {operator, statement, recordId, boundaryRef,
// observedAt}}` — the attestation optional, its fields exactly as spelled.
func TestOrphanResolveParamsPinTheAttestationFieldForField(t *testing.T) {
	params := HostOrphanResolveParams{
		ID: "00000000000000000042",
		Attestation: &HostOrphanResolveAttestation{
			Operator:    "operator-alpha",
			Statement:   "orphan-verified-absent",
			RecordID:    "00000000000000000042",
			BoundaryRef: "/state/operations.json.custody-1",
			ObservedAt:  "2026-09-28T10:00:00Z",
		},
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"id":"00000000000000000042","attestation":{"operator":"operator-alpha",` +
		`"statement":"orphan-verified-absent","recordId":"00000000000000000042",` +
		`"boundaryRef":"/state/operations.json.custody-1","observedAt":"2026-09-28T10:00:00Z"}}`
	if string(raw) != want {
		t.Fatalf("params = %s\nwant    %s", raw, want)
	}
	// The attestation is optional: an id-only call carries no attestation key.
	raw, err = json.Marshal(HostOrphanResolveParams{ID: "00000000000000000042"})
	if err != nil {
		t.Fatalf("marshal id-only: %v", err)
	}
	if string(raw) != `{"id":"00000000000000000042"}` {
		t.Fatalf("id-only params = %s", raw)
	}
	// The observedAt field is an RFC3339 string on the wire; the union of
	// optional fields round-trips.
	var decoded HostOrphanResolveParams
	if err := json.Unmarshal(wantBytes(t, want), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Attestation == nil || *decoded.Attestation != *params.Attestation {
		t.Fatalf("round-trip = %+v, want the original attestation", decoded.Attestation)
	}
}

func wantBytes(t *testing.T, s string) []byte {
	t.Helper()
	return []byte(s)
}

// TestBoundaryEntryUnionMarshalsEachArmExactly pins §9's five variants
// field-for-field, discriminator included.
func TestBoundaryEntryUnionMarshalsEachArmExactly(t *testing.T) {
	pid := 41
	pgid, session := 7, 9
	cases := []struct {
		name  string
		entry BoundaryEntry
		want  string
	}{
		{"local-linux", BoundaryEntry{BoundaryEntryLocalLinux: &BoundaryEntryLocalLinux{
			Kind: "local-linux", CgroupID: "/cg/n1", Nonce: "n1", PID: 41, StartTime: "777",
		}}, `{"kind":"local-linux","cgroupId":"/cg/n1","nonce":"n1","pid":41,"startTime":"777"}`},
		{"local-darwin", BoundaryEntry{BoundaryEntryLocalDarwin: &BoundaryEntryLocalDarwin{
			Kind: "local-darwin", PGID: 7, SessionID: 9, PID: 41, StartTime: "777", Nonce: "n1",
		}}, `{"kind":"local-darwin","pgid":7,"sessionId":9,"pid":41,"startTime":"777","nonce":"n1"}`},
		{"local-markerless linux", BoundaryEntry{BoundaryEntryLocalMarkerless: &BoundaryEntryLocalMarkerless{
			Kind: "local-markerless", Platform: "linux", CgroupID: "/cg/n1", Nonce: "n1",
		}}, `{"kind":"local-markerless","platform":"linux","cgroupId":"/cg/n1","nonce":"n1"}`},
		{"local-markerless darwin", BoundaryEntry{BoundaryEntryLocalMarkerless: &BoundaryEntryLocalMarkerless{
			Kind: "local-markerless", Platform: "darwin", PGID: &pgid, SessionID: &session, Nonce: "n1",
		}}, `{"kind":"local-markerless","platform":"darwin","pgid":7,"sessionId":9,"nonce":"n1"}`},
		{"remote-fencing", BoundaryEntry{BoundaryEntryRemoteFencing: &BoundaryEntryRemoteFencing{
			Kind:         "remote-fencing",
			FencingEpoch: FencingEpoch{BootID: "boot-1", OpSeq: 2},
			GuardEpoch:   3,
			LeaseEntries: []BoundaryLeaseEntry{{
				Command:      "deploy --now",
				RegisteredAt: "2026-09-28T10:00:00Z",
				Ownership:    BoundaryLeaseOwnership{PID: &pid, PIDStartTime: "777"},
			}},
		}}, `{"kind":"remote-fencing","fencingEpoch":{"bootId":"boot-1","opSeq":2},"guardEpoch":3,` +
			`"leaseEntries":[{"command":"deploy --now","registeredAt":"2026-09-28T10:00:00Z",` +
			`"ownership":{"pid":41,"pidStartTime":"777"}}]}`},
		{"remote-fencing nonce ownership", BoundaryEntry{BoundaryEntryRemoteFencing: &BoundaryEntryRemoteFencing{
			Kind:         "remote-fencing",
			FencingEpoch: FencingEpoch{BootID: "boot-1", OpSeq: 2},
			GuardEpoch:   3,
			LeaseEntries: []BoundaryLeaseEntry{{
				Command: "restart", RegisteredAt: "2026-09-28T10:00:01Z",
				Ownership: BoundaryLeaseOwnership{Nonce: "n2"},
			}},
		}}, `{"kind":"remote-fencing","fencingEpoch":{"bootId":"boot-1","opSeq":2},"guardEpoch":3,` +
			`"leaseEntries":[{"command":"restart","registeredAt":"2026-09-28T10:00:01Z",` +
			`"ownership":{"nonce":"n2"}}]}`},
		{"boundary-unavailable", BoundaryEntry{BoundaryEntryUnavailable: &BoundaryEntryUnavailable{
			Kind: "boundary-unavailable", Reason: "corrupt-store-custody", CustodyRef: "/state/custody.json",
		}}, `{"kind":"boundary-unavailable","reason":"corrupt-store-custody","custodyRef":"/state/custody.json"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.entry)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(raw) != tc.want {
				t.Fatalf("marshal = %s\nwant     %s", raw, tc.want)
			}
			var decoded BoundaryEntry
			if err := json.Unmarshal([]byte(tc.want), &decoded); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			round, err := json.Marshal(decoded)
			if err != nil {
				t.Fatalf("re-marshal: %v", err)
			}
			if string(round) != tc.want {
				t.Fatalf("round trip = %s\nwant        %s", round, tc.want)
			}
		})
	}
}

// TestBoundaryEntryUnionRefusesMalformedValues pins the fail-loud half: no arm,
// two arms, and an unknown or absent discriminator are refused, never read as a
// zero-valued arm.
func TestBoundaryEntryUnionRefusesMalformedValues(t *testing.T) {
	if _, err := json.Marshal(BoundaryEntry{}); err == nil {
		t.Fatal("marshaling a union carrying no arm succeeded, want an error")
	}
	if _, err := json.Marshal(BoundaryEntry{
		BoundaryEntryLocalLinux:  &BoundaryEntryLocalLinux{Kind: "local-linux"},
		BoundaryEntryUnavailable: &BoundaryEntryUnavailable{Kind: "boundary-unavailable"},
	}); err == nil {
		t.Fatal("marshaling a union carrying two arms succeeded, want an error")
	}
	for name, raw := range map[string]string{
		"unknown kind":  `{"kind":"local-solaris"}`,
		"absent kind":   `{"cgroupId":"/cg"}`,
		"malformed arm": `{"kind":"local-linux","pid":"41"}`,
	} {
		var decoded BoundaryEntry
		if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
			t.Fatalf("%s: decoded into %+v, want an error", name, decoded)
		}
	}
}

// TestBoundaryEntryArrayCarriesThePerMemberShape pins §9's multi-member array
// and the verified-empty spelling: an explicit `[]`, distinct from the
// boundary-unavailable sentinel.
func TestBoundaryEntryArrayCarriesThePerMemberShape(t *testing.T) {
	raw := `[{"kind":"local-linux","cgroupId":"/cg/n1","nonce":"n1","pid":41,"startTime":"777"},` +
		`{"kind":"local-linux","cgroupId":"/cg/n1","nonce":"n2","pid":42,"startTime":"778"}]`
	var members []BoundaryEntry
	if err := json.Unmarshal([]byte(raw), &members); err != nil {
		t.Fatalf("unmarshal array: %v", err)
	}
	if len(members) != 2 || members[0].BoundaryEntryLocalLinux == nil || members[1].BoundaryEntryLocalLinux == nil {
		t.Fatalf("array = %+v, want two local-linux members", members)
	}
	empty, err := json.Marshal([]BoundaryEntry{})
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if string(empty) != "[]" {
		t.Fatalf("empty boundary = %s, want []", empty)
	}
	unavailable, err := json.Marshal([]BoundaryEntry{{BoundaryEntryUnavailable: &BoundaryEntryUnavailable{
		Kind: "boundary-unavailable", Reason: "corrupt-store-custody", CustodyRef: "/state/custody.json",
	}}})
	if err != nil {
		t.Fatalf("marshal unavailable: %v", err)
	}
	if bytes.Equal(unavailable, empty) {
		t.Fatal("the boundary-unavailable sentinel rendered as the verified-empty array")
	}
}

// TestOperationRecordOrphanFields pins §9's presence rules on the wire record:
// orphanBoundary present exactly while orphan-unverified (an explicit `[]`
// stays present), orphanResolved present exactly on a resolved record, and the
// attestation beside it.
func TestOperationRecordOrphanFields(t *testing.T) {
	// Absent on every other state: the zero record carries no orphan keys.
	raw, err := json.Marshal(OperationRecord{ID: "00000000000000000001", State: OperationStateInterrupted})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"orphanBoundary", "orphanResolved", "attestation"} {
		if strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("an ordinary record carries %q: %s", key, raw)
		}
	}
	// Present on an orphan-unverified record — including the verified-empty
	// array, which must render `[]`, never be omitted.
	empty := &[]BoundaryEntry{}
	raw, err = json.Marshal(OperationRecord{
		ID: "00000000000000000001", State: OperationStateOrphanUnverified, OrphanBoundary: empty,
	})
	if err != nil {
		t.Fatalf("marshal orphan: %v", err)
	}
	if !strings.Contains(string(raw), `"orphanBoundary":[]`) {
		t.Fatalf("an orphan-unverified record with an empty boundary = %s, want an explicit []", raw)
	}
	multi := &[]BoundaryEntry{{BoundaryEntryLocalLinux: &BoundaryEntryLocalLinux{
		Kind: "local-linux", CgroupID: "/cg/n1", Nonce: "n1", PID: 41, StartTime: "777",
	}}}
	raw, err = json.Marshal(OperationRecord{
		ID: "00000000000000000001", State: OperationStateOrphanUnverified, OrphanBoundary: multi,
	})
	if err != nil {
		t.Fatalf("marshal multi: %v", err)
	}
	var decoded OperationRecord
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.OrphanBoundary == nil || len(*decoded.OrphanBoundary) != 1 || (*decoded.OrphanBoundary)[0].BoundaryEntryLocalLinux == nil {
		t.Fatalf("decoded boundary = %+v, want one local-linux member", decoded.OrphanBoundary)
	}
	// The resolved record: the marker present, the boundary absent, the
	// attestation beside it.
	raw, err = json.Marshal(OperationRecord{
		ID: "00000000000000000001", State: OperationStateInterrupted, OrphanResolved: true,
		Attestation: &HostOrphanResolveAttestation{
			Operator: "operator-alpha", Statement: "orphan-verified-absent",
			RecordID: "00000000000000000001", ObservedAt: "2026-09-28T10:00:00Z",
		},
	})
	if err != nil {
		t.Fatalf("marshal resolved: %v", err)
	}
	if !strings.Contains(string(raw), `"orphanResolved":true`) {
		t.Fatalf("a resolved record carries no orphanResolved marker: %s", raw)
	}
	if strings.Contains(string(raw), `"orphanBoundary"`) {
		t.Fatalf("a resolved record still carries orphanBoundary: %s", raw)
	}
	if !strings.Contains(string(raw), `"attestation":{`) {
		t.Fatalf("a resolved record carries no attestation: %s", raw)
	}
}

// TestFenceDiscriminatorsAndRecoveryMethodRegistration pins §8's four
// discriminators with their data shapes, and the assignment §10:205 pins:
// teardown-retry and teardown-recover carry orphan-fenced-busy; every other
// fenced call keeps host-busy-transient.
func TestFenceDiscriminatorsAndRecoveryMethodRegistration(t *testing.T) {
	if got := OrphanFenceRefusalMethods; !reflect.DeepEqual(got, []string{MethodEvenerHostTeardownRetry, MethodEvenerHostTeardownRecover}) {
		t.Fatalf("OrphanFenceRefusalMethods = %v, want exactly the two recovery mutations", got)
	}
	cases := []struct {
		name string
		err  WireError
		want ErrorInfo
		code int
		keys []string
	}{
		{"fencing-failure", FencingFailure("h1", "kill/wait timed out"), ErrorFencingFailure, CodeConflict, []string{"evenerErrorInfo", "host"}},
		{"fencing-helper-absent", FencingHelperAbsent("h1", "1", "helper absent"), ErrorFencingHelperAbsent, CodeConflict,
			[]string{"evenerErrorInfo", "host", "version"}},
		{"fencing-helper-untrusted", FencingHelperUntrusted("h1", "1", "helper untrusted"), ErrorFencingHelperUntrusted, CodeConflict,
			[]string{"evenerErrorInfo", "host", "version"}},
		{"orphan-fenced-busy", OrphanFencedBusy("00000000000000000042", "orphan fence"), ErrorOrphanFencedBusy, CodeConflict,
			[]string{"evenerErrorInfo", "recordId"}},
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
