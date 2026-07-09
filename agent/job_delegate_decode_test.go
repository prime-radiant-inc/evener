package agent

import "testing"

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
