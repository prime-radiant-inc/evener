package mcpconfig

import (
	"os"
	"strings"
	"testing"
)

// The global MCP config path honors XDG_CONFIG_HOME, falling back to the
// home directory's ~/.config — resolved through the expansion environment's
// lookup, the same seam discovery uses.
func TestCov_GlobalMCPConfigPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/config")
	got := globalMCPConfigPathInEnv(os.LookupEnv)
	if !strings.HasPrefix(got, "/xdg/config") {
		t.Errorf("with XDG_CONFIG_HOME set, path = %q, want under /xdg/config", got)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	got = globalMCPConfigPathInEnv(os.LookupEnv)
	if got == "" || !strings.Contains(got, ".config") {
		t.Errorf("fallback path = %q, want under ~/.config", got)
	}
}
