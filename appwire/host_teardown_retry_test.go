package appwire

// Protocol-shape tests for the teardown-retry result union (registry spec 08
// §11): all six outcome/hostKind arms round-trip through the one pointer they
// set, and a pair the protocol does not define is refused rather than decoded
// into a zero arm.

import (
	"encoding/json"
	"testing"
)

func TestHostTeardownRetryResultArmsRoundTrip(t *testing.T) {
	t.Parallel()
	row := HostRow{Name: "m4.example", Generation: 7}
	removedRow := RemovedRow{Name: "m4.example", Generation: 7}
	age := int64(90)
	cases := []struct {
		name string
		arm  any
		set  func(*HostTeardownRetryResult) bool
	}{
		{
			name: "complete live",
			arm:  HostTeardownRetryCompleteLive{Outcome: HostTeardownOutcomeComplete, HostKind: HostKindLive, Host: row, RemnantID: "r1", EscalationAgeSec: &age},
			set: func(u *HostTeardownRetryResult) bool {
				return u.HostTeardownRetryCompleteLive != nil && u.HostTeardownRetryCompleteRemoved == nil
			},
		},
		{
			name: "complete removed",
			arm:  HostTeardownRetryCompleteRemoved{Outcome: HostTeardownOutcomeComplete, HostKind: HostKindRemoved, Host: removedRow, RemnantID: "r2"},
			set: func(u *HostTeardownRetryResult) bool {
				return u.HostTeardownRetryCompleteRemoved != nil && u.HostTeardownRetryCompleteLive == nil
			},
		},
		{
			name: "cleared live",
			arm:  HostTeardownRetryClearedLive{Outcome: HostTeardownOutcomeCleared, HostKind: HostKindLive, Host: row, RemnantID: "r3"},
			set: func(u *HostTeardownRetryResult) bool {
				return u.HostTeardownRetryClearedLive != nil && u.HostTeardownRetryClearedRemoved == nil
			},
		},
		{
			name: "cleared removed",
			arm:  HostTeardownRetryClearedRemoved{Outcome: HostTeardownOutcomeCleared, HostKind: HostKindRemoved, Host: removedRow, RemnantID: "r4"},
			set: func(u *HostTeardownRetryResult) bool {
				return u.HostTeardownRetryClearedRemoved != nil && u.HostTeardownRetryClearedLive == nil
			},
		},
		{
			name: "failed live",
			arm:  HostTeardownRetryFailedLive{Outcome: HostTeardownOutcomeFailed, HostKind: HostKindLive, Host: row, RemnantID: "r5", Seam: "teardown"},
			set: func(u *HostTeardownRetryResult) bool {
				return u.HostTeardownRetryFailedLive != nil && u.HostTeardownRetryFailedRemoved == nil
			},
		},
		{
			name: "failed removed",
			arm:  HostTeardownRetryFailedRemoved{Outcome: HostTeardownOutcomeFailed, HostKind: HostKindRemoved, Host: removedRow, RemnantID: "r6", Seam: "teardown"},
			set: func(u *HostTeardownRetryResult) bool {
				return u.HostTeardownRetryFailedRemoved != nil && u.HostTeardownRetryFailedLive == nil
			},
		},
	}
	for _, tc := range cases {
		raw, err := json.Marshal(tc.arm)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		var decoded HostTeardownRetryResult
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
			t.Fatalf("%s: remarshaled %s is not an object: %v", tc.name, remarshaled, err)
		}
		if _, ok := again["outcome"]; !ok {
			t.Fatalf("%s: remarshaled %s carries no outcome", tc.name, remarshaled)
		}
	}
}

func TestHostTeardownRetryResultRefusesUndeclaredValues(t *testing.T) {
	t.Parallel()
	if _, err := json.Marshal(HostTeardownRetryResult{}); err == nil {
		t.Fatal("marshaling a union carrying no arm succeeded, want an error")
	}
	both := HostTeardownRetryResult{
		HostTeardownRetryCompleteLive:  &HostTeardownRetryCompleteLive{},
		HostTeardownRetryFailedRemoved: &HostTeardownRetryFailedRemoved{},
	}
	if _, err := json.Marshal(both); err == nil {
		t.Fatal("marshaling a union carrying two arms succeeded, want an error")
	}
	var decoded HostTeardownRetryResult
	if err := json.Unmarshal([]byte(`{"outcome":"teardown-complete","hostKind":"bogus"}`), &decoded); err == nil {
		t.Fatal("unmarshaling an undeclared outcome/hostKind pair succeeded, want an error")
	}
	if decoded.HostTeardownRetryCompleteLive != nil || decoded.HostTeardownRetryFailedRemoved != nil {
		t.Fatalf("a refused decode set an arm: %+v", decoded)
	}
	if err := json.Unmarshal([]byte(`{"outcome":`), &decoded); err == nil {
		t.Fatal("unmarshaling a body with no JSON value succeeded, want an error")
	}
}

// TestHostTeardownRetryResultRefusesMalformedArms pins that every declared pair
// decodes through its own arm type, so a body whose host is not the row shape
// the pair promises is refused instead of read as a zero arm.
func TestHostTeardownRetryResultRefusesMalformedArms(t *testing.T) {
	t.Parallel()
	for _, pair := range []struct{ outcome, hostKind string }{
		{HostTeardownOutcomeComplete, HostKindLive},
		{HostTeardownOutcomeComplete, HostKindRemoved},
		{HostTeardownOutcomeCleared, HostKindLive},
		{HostTeardownOutcomeCleared, HostKindRemoved},
		{HostTeardownOutcomeFailed, HostKindLive},
		{HostTeardownOutcomeFailed, HostKindRemoved},
	} {
		raw := `{"outcome":` + quote(pair.outcome) + `,"hostKind":` + quote(pair.hostKind) + `,"host":"not-a-row","remnantId":"r1"}`
		var decoded HostTeardownRetryResult
		if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
			t.Fatalf("decoding %s succeeded, want the arm's own decode error", raw)
		}
	}
}

// quote renders a JSON string literal for a machine-contract token.
func quote(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(raw)
}
