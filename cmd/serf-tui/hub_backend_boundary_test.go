package main

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestHubModelDisablesLegacySessionBackends(t *testing.T) {
	// addr, embedded, and authController are legacy standalone-TUI fields that
	// must never come back. Use reflect so the test actually fails if one is
	// reintroduced — a compile-only call-and-not-panic is coverage theater.
	typ := reflect.TypeOf(hubModel{})
	for _, banned := range []string{"addr", "embedded", "authController"} {
		if _, ok := typ.FieldByName(banned); ok {
			t.Errorf("hubModel must not carry legacy field %q", banned)
		}
	}
}

func TestHubCommandRoutingStaysInsideAppWireClientBoundary(t *testing.T) {
	// Scan every non-test .go file in the package so a new file (e.g.
	// hub_bypass.go) cannot silently violate the boundary.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var sb strings.Builder
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		sb.WriteString(readSourceFile(t, name))
		sb.WriteByte('\n')
	}
	combined := sb.String()

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
		// Steering is routed through turn/drainAsSteer (kata 0bq1); the TUI no
		// longer issues a bare client.TurnSteer( from a session-action helper.
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
