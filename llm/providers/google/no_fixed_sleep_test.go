package google

import (
	"testing"

	"primeradiant.com/evener/llm/providers/internal/testaudit"
)

// TestCancelHandlerHasNoFixedSleep pins issue #164: the
// TestComplete_WrapsContextCanceled handler must wait on <-r.Context().Done()
// like its sibling deadline test, never on a fixed-duration time.Sleep.
func TestCancelHandlerHasNoFixedSleep(t *testing.T) {
	testaudit.RequireHandlerWaitsOnContextDone(t, "adapter_test.go", "TestComplete_WrapsContextCanceled")
}
