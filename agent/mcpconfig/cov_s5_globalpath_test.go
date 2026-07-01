package mcpconfig

import (
	"strings"
	"testing"
)

// globalMCPConfigPath honors XDG_CONFIG_HOME, falling back to ~/.config.
func TestCov_GlobalMCPConfigPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/config")
	got := globalMCPConfigPath()
	if !strings.HasPrefix(got, "/xdg/config") {
		t.Errorf("with XDG_CONFIG_HOME set, path = %q, want under /xdg/config", got)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	got = globalMCPConfigPath()
	if got == "" || !strings.Contains(got, ".config") {
		t.Errorf("fallback path = %q, want under ~/.config", got)
	}
}
