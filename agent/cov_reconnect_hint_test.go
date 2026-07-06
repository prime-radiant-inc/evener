package agent

import (
	"strings"
	"testing"
)

func TestReconnectRecoveryWarningKeepsRecoveryHint(t *testing.T) {
	in := reconnectRecoveryWarning("linear")
	out := enrichWarningData(in)
	if strings.Contains(strings.ToLower(out.Hint), "failed to connect") {
		t.Fatalf("recovery warning inherited the MCP-failure hint: %q", out.Hint)
	}
	if strings.TrimSpace(out.Hint) == "" {
		t.Fatalf("recovery warning must carry its own non-empty hint")
	}
	if !strings.Contains(out.Message, "linear") {
		t.Fatalf("recovery message should name the server: %q", out.Message)
	}
}
