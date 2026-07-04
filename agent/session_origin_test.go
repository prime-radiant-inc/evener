package agent

import (
	"testing"

	"primeradiant.com/serf/envvars"
)

// TestSessionOriginFromEnv proves a fresh session captures SERF_SESSION_ORIGIN
// at creation time, so the hub can later classify an all-"test" project into
// the "Test runs" group (Task 15).
func TestSessionOriginFromEnv(t *testing.T) {
	t.Setenv(envvars.SERFSessionOrigin.Name, "test")
	sess := newTestSession(t)
	if got := sess.Meta().Origin; got != "test" {
		t.Fatalf("Origin should come from SERF_SESSION_ORIGIN, got %q", got)
	}
}
