package main

import (
	"os"
	"strings"
	"testing"
)

func TestHubModelDisablesLegacySessionBackends(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")

	if m.session.addr != "" {
		t.Fatalf("hub session addr = %q, want empty direct server address", m.session.addr)
	}
	if m.session.stateDir != "" {
		t.Fatalf("hub session stateDir = %q, want no direct state dir", m.session.stateDir)
	}
	if m.session.embedded != nil {
		t.Fatal("hub session should not carry an embedded daemon")
	}
	if m.session.authController != nil {
		t.Fatal("hub session should not carry the legacy auth controller")
	}
}

func TestHubCommandRoutingStaysInsideAppWireClientBoundary(t *testing.T) {
	source := readSourceFile(t, "hub_commands.go")
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
		"client.ThreadFork(",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("hub_commands.go missing AppWire route %q", want)
		}
	}

	for _, forbidden := range []string{
		"sendInput(",
		"sendSteer(",
		"startEmbedded(",
		"agent.ReadTranscript(",
		"authopenai.NewService(",
		"authopenai.LoadAuth(",
		"cmdutil.ResolveSessionMeta(",
		"agent.RestoreSessionFromMeta(",
		"agent.NewSession(",
		"server.NewServer(",
		"http.Post(",
		"\"/input\"",
		"\"/steer\"",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("hub_commands.go must not bypass Hub/AppWire with %q", forbidden)
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
