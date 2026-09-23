package plugin

import (
	"os"
	"testing"

	"primeradiant.com/evener/agent/sandbox/sandboxtest"
)

// TestMain keeps the bundled-skills cache these tests publish, which is shared
// by every Evener process of this user and deliberately outlives each one (in
// the temp dir, or the user cache dir on Windows), inside a root of the run's
// own that is removed when the run ends.
func TestMain(m *testing.M) {
	os.Exit(sandboxtest.Run(m, "evener-plugin-test-"))
}
