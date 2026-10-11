package plugin

import (
	"os"
	"testing"

	"primeradiant.com/evener/agent/sandbox/sandboxtest"
	"primeradiant.com/evener/envvars"
)

// TestMain keeps the bundled-skills cache these tests publish, which is shared
// by every Evener process of this user and deliberately outlives each one (in
// the temp dir, or the user cache dir on Windows), inside a root of the run's
// own that is removed when the run ends. It also drops any ambient
// EVENER_NO_USER_SKILLS, which would otherwise flip DiscoverEvenerWideCommands
// away from the user-global commands dir the tests deliberately cover (#3487).
func TestMain(m *testing.M) {
	_ = os.Unsetenv(envvars.EVENERNoUserSkills.Name)
	os.Exit(sandboxtest.Run(m, "evener-plugin-test-"))
}
