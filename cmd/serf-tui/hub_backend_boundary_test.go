package main

import (
	"os"
	"strings"
	"testing"
)

func TestHubModelDisablesLegacySessionBackends(t *testing.T) {
	// The model struct used to carry addr, stateDir, embedded, and
	// authController fields that the standalone TUI populated. All four are
	// gone from the type now; their absence is enforced at compile time, a
	// stronger guarantee than the nil/empty-string checks this test used to
	// make. Construction parity (hub creates a session) is still exercised
	// indirectly by every hub_*_test that newHubModel a session.
	_ = newHubModel(nil, "http://hub.test")
}

func TestHubCommandRoutingStaysInsideAppWireClientBoundary(t *testing.T) {
	// Routes can be split across hub_commands.go and the smaller helper
	// files that grew out of it (queue_send.go for kata 111a/0bq1, etc.).
	// Concatenate any file that registers `tea.Cmd` helpers and assert
	// against the combined surface.
	combined := readSourceFile(t, "hub_commands.go") + "\n" + readSourceFile(t, "queue_send.go")
	for _, want := range []string{
		"client.ThreadList(",
		"client.ThreadRead(",
		"client.ThreadTranscriptList(",
		"client.ThreadStart(",
		"client.TurnStart(",
		"client.TasksList(",
		"client.AuthStatus(",
		"client.AuthLoginStart(",
		"client.AuthLoginComplete(",
		"client.AuthLogout(",
		"client.ModelList(",
		"client.TurnInterrupt(",
		"client.ThreadCompactStart(",
		"client.ThreadClear(",
		"client.ThreadShutdown(",
		"client.ThreadModelSet(",
		"client.TurnSteer(",
		"client.TurnQueue(",
		"client.TurnDrainAsSteer(",
		"client.ThreadFork(",
	} {
		if !strings.Contains(combined, want) {
			t.Fatalf("session-action sources missing AppWire route %q", want)
		}
	}

	for _, forbidden := range []string{
		"sendInput(",
		"sendSteer(",
		"startEmbedded(",
		"authopenai.NewService(",
		"authopenai.LoadAuth(",
		"cmdutil.ResolveSessionMeta(",
		"agent.RestoreSessionFromMeta(",
		"agent.NewSession(",
		"server.NewServer(",
		"http.Post(",
		"\"/input\"",
		"\"/steer\"",
		"\"/queue\"",
		"\"/drain-as-steer\"",
	} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("session-action sources must not bypass Hub/AppWire with %q", forbidden)
		}
	}
}

func readSourceFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
