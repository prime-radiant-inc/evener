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
		ThreadID: "t1", Ref: "local:t1",
		EscalationID: "esc_1", Mode: "read-only", Tool: "write_file", Kind: "file_tool",
		DeniedPath: "hosts", Command: "cmd", OutputSoFar: "out", PartiallyRan: true,
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"threadId", "ref", "escalationId", "mode", "tool", "kind", "deniedPath", "command", "outputSoFar", "partiallyRan"} {
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

// TestSandboxEscalationResolvedWireKeys (wire-honesty spec Part B) pins the
// camelCase wire spelling of the serf/sandbox/escalation/resolved
// notification, and — the spec's binding design decision — that it carries
// NO reason or approved: the sole consumer clears its card by id identically
// regardless of outcome, and the producer cannot reliably distinguish
// close-cancel from interrupt anyway. Additive later if a "resolved
// elsewhere" toast ever wants more.
func TestSandboxEscalationResolvedWireKeys(t *testing.T) {
	resolved := SandboxEscalationResolved{ThreadID: "t1", Ref: "local:t1", EscalationID: "esc_1"}
	b, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"threadId", "ref", "escalationId"} {
		if !strings.Contains(string(b), `"`+key+`":`) {
			t.Errorf("SandboxEscalationResolved is missing wire key %q in %s", key, b)
		}
	}
	for _, absent := range []string{"reason", "approved"} {
		if strings.Contains(string(b), `"`+absent+`"`) {
			t.Errorf("SandboxEscalationResolved must not carry %q, got %s", absent, b)
		}
	}
}
