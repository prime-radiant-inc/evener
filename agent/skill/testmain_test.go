package skill

import (
	"os"
	"testing"

	"primeradiant.com/evener/agent/sandbox/sandboxtest"
)

// TestMain keeps the bundled-skills cache these tests publish in the temp dir,
// which is shared by every Evener process of this user and deliberately outlives
// each one, inside a root of the run's own that is removed when the run ends.
func TestMain(m *testing.M) {
	os.Exit(sandboxtest.Run(m, "evener-skill-test-"))
}
