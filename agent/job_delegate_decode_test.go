package agent

import (
	"strings"
	"testing"
)

// TestDecodeDelegateArgs_Sandbox: the sandbox/sandbox_net params decode into
// delegateArgs, and an ABSENT sandbox_net stays nil (inherit) rather than
// defaulting to false — the tri-state that lets a delegate inherit the parent's
// network instead of silently forcing it off.
func TestDecodeDelegateArgs_Sandbox(t *testing.T) {
	t.Parallel()

	a, err := decodeDelegateArgs(map[string]any{"task": "t", "sandbox": "restricted", "sandbox_net": false})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if a.Sandbox != "restricted" {
		t.Errorf("Sandbox = %q, want restricted", a.Sandbox)
	}
	if a.SandboxNet == nil || *a.SandboxNet {
		t.Errorf("SandboxNet = %v, want explicit false", a.SandboxNet)
	}

	a, err = decodeDelegateArgs(map[string]any{"task": "t", "sandbox_net": true})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if a.SandboxNet == nil || !*a.SandboxNet {
		t.Errorf("SandboxNet = %v, want explicit true", a.SandboxNet)
	}

	a, err = decodeDelegateArgs(map[string]any{"task": "t"})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if a.Sandbox != "" {
		t.Errorf("absent sandbox must be empty, got %q", a.Sandbox)
	}
	if a.SandboxNet != nil {
		t.Errorf("absent sandbox_net must stay nil (inherit), got %v", *a.SandboxNet)
	}
}

// TestDecodeDelegateArgs_SandboxNetMalformed: a present-but-non-boolean sandbox_net
// (e.g. the string "false" from a non-strict provider) is refused with an
// invalid_request error, NOT silently decoded as nil=inherit — that silent no-op is
// the exact class this surface refuses elsewhere.
func TestDecodeDelegateArgs_SandboxNetMalformed(t *testing.T) {
	t.Parallel()
	if _, err := decodeDelegateArgs(map[string]any{"task": "t", "sandbox_net": "false"}); err == nil {
		t.Fatal("a string sandbox_net must be refused, not decoded as inherit")
	} else if !strings.Contains(err.Error(), "invalid_request:") || !strings.Contains(err.Error(), "sandbox_net must be a boolean") {
		t.Errorf("refusal must name the boolean requirement, got %v", err)
	}
}
