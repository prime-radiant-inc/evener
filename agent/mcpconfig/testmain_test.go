package mcpconfig

import (
	"os"
	"testing"

	"primeradiant.com/evener/envvars"
)

// TestMain drops any ambient EVENER_NO_USER_SKILLS so a developer's exported
// environment cannot flip which MCP layers these tests exercise: with the
// variable set to "1", Discover skips the global layer (#3487), and the tests
// that deliberately cover that layer would stop seeing it. A test that pins
// the hermetic behavior sets the variable to "1" itself.
func TestMain(m *testing.M) {
	_ = os.Unsetenv(envvars.EVENERNoUserSkills.Name)
	os.Exit(m.Run())
}
