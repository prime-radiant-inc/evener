package appwire

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSandboxEscalationWireKeys pins the camelCase wire spelling of the M7
// escalation pair (the appwire tree speaks the codex/appwire camelCase protocol,
// enforced by serf-namingcheck) and that the resolve params round-trip.
func TestSandboxEscalationWireKeys(t *testing.T) {
	req := SandboxEscalationRequested{
		EscalationID: "esc_1", Mode: "read-only", Tool: "write_file", Kind: "file_tool",
		DeniedPath: "hosts", Command: "cmd", OutputSoFar: "out", PartiallyRan: true,
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"escalationId", "mode", "tool", "kind", "deniedPath", "command", "outputSoFar", "partiallyRan"} {
		if !strings.Contains(string(b), `"`+key+`":`) {
			t.Errorf("SandboxEscalationRequested is missing wire key %q in %s", key, b)
		}
	}

	rp := SandboxEscalationResolveParams{ThreadID: "t1", Ref: "r1", EscalationID: "esc_1", Approve: true}
	b2, err := json.Marshal(rp)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"threadId", "escalationId", "approve"} {
		if !strings.Contains(string(b2), `"`+key+`":`) {
			t.Errorf("SandboxEscalationResolveParams is missing wire key %q in %s", key, b2)
		}
	}
	var got SandboxEscalationResolveParams
	if err := json.Unmarshal(b2, &got); err != nil {
		t.Fatalf("resolve params round-trip: %v", err)
	}
	if got.EscalationID != "esc_1" || !got.Approve {
		t.Fatalf("resolve params round-trip lost data: %+v", got)
	}
}
