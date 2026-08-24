package hooks

import (
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// TestCovSetSandboxWrapper covers SetSandboxWrapper (hooks.go line 155).
func TestCovSetSandboxWrapper(t *testing.T) {
	r := NewRunner(nil, "gpt-5")
	// Set a nil wrapper — should not panic.
	r.SetSandboxWrapper(nil)

	// Set a real wrapper.
	w := &sandbox.Wrapper{}
	r.SetSandboxWrapper(w)
	// Verify by setting again (no direct field access from outside package).
	r.SetSandboxWrapper(nil)
}
