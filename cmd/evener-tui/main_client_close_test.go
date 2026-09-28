package tui

import (
	"bytes"
	"context"
	"io"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/appwire/appwiretest"
	"primeradiant.com/evener/cmd/evener-tui/internal/hubstart"
)

func stubRunForClientClose(t *testing.T, startHub func(context.Context, hubstart.HubStartConfig) (hubstart.HubRuntime, error), newProgram func(tea.Model, ...tea.ProgramOption) tuiProgram) {
	t.Helper()
	oldArgs, oldOut, oldErr, oldGetenv, oldDirs, oldStart := processArgs, standardOutput, standardError, processGetenv, ensureUserConfigDirs, startHubClient
	oldProbe, oldInit, oldApply, oldReset, oldProgram := probeTerminalDefaults, initThemeFromStateDir, applyTerminalBg, resetTerminalBg, newTUIProgram
	processArgs = func() []string { return []string{"evener-tui", "--state-dir=x"} }
	standardOutput = io.Discard
	standardError = &bytes.Buffer{}
	processGetenv = func(string) string { return "" }
	ensureUserConfigDirs = func() error { return nil }
	startHubClient = startHub
	probeTerminalDefaults = func() bool { return true }
	initThemeFromStateDir = func(string) {}
	applyTerminalBg = func() {}
	resetTerminalBg = func() {}
	newTUIProgram = newProgram
	t.Cleanup(func() {
		processArgs, standardOutput, standardError, processGetenv, ensureUserConfigDirs = oldArgs, oldOut, oldErr, oldGetenv, oldDirs
		startHubClient = oldStart
		probeTerminalDefaults, initThemeFromStateDir, applyTerminalBg, resetTerminalBg, newTUIProgram = oldProbe, oldInit, oldApply, oldReset, oldProgram
	})
}

// assertTransportClosed proves the client over this transport was closed:
// ScriptedTransport.Send refuses once its connection is closed.
func assertTransportClosed(t *testing.T, transport *appwiretest.ScriptedTransport, label string) {
	t.Helper()
	if err := transport.Send(context.Background(), appwire.Message{}); err == nil {
		t.Fatalf("%s transport was not closed", label)
	}
}

// TestRunClosesHubClientOnExit proves tui.Run releases the initial connection
// when the program returns, so a normal quit does not leak the WebSocket.
func TestRunClosesHubClientOnExit(t *testing.T) {
	transport := appwiretest.NewScriptedTransport()
	stubRunForClientClose(t,
		func(context.Context, hubstart.HubStartConfig) (hubstart.HubRuntime, error) {
			return hubstart.HubRuntime{Address: hubstart.HubAddress{BaseURL: "http://hub"}, Client: appwire.NewClient(transport)}, nil
		},
		func(model tea.Model, _ ...tea.ProgramOption) tuiProgram {
			return &scriptedCovProgram{model: model}
		},
	)
	if code := run(); code != 0 {
		t.Fatalf("run = %d, want 0", code)
	}
	assertTransportClosed(t, transport, "initial client")
}

// TestRunClosesCurrentClientAfterReconnect proves the exit close follows a
// reconnect replacement: reconnection closes the superseded connection itself,
// and Run must still close whichever connection is current when it returns.
func TestRunClosesCurrentClientAfterReconnect(t *testing.T) {
	var transports []*appwiretest.ScriptedTransport
	startHub := func(context.Context, hubstart.HubStartConfig) (hubstart.HubRuntime, error) {
		transport := appwiretest.NewScriptedTransport()
		transports = append(transports, transport)
		return hubstart.HubRuntime{Address: hubstart.HubAddress{BaseURL: "http://hub"}, Client: appwire.NewClient(transport)}, nil
	}
	stubRunForClientClose(t, startHub,
		func(model tea.Model, _ ...tea.ProgramOption) tuiProgram {
			m := model.(hubModel)
			replacement, frames, err := m.dialHub(context.Background())
			if err != nil {
				t.Fatalf("reconnect dial: %v", err)
			}
			m.applyHubReconnect(hubReconnectMsg{client: replacement, frames: frames})
			return &scriptedCovProgram{model: m}
		},
	)
	if code := run(); code != 0 {
		t.Fatalf("run = %d, want 0", code)
	}
	if len(transports) != 2 {
		t.Fatalf("dials = %d, want 2 (initial + replacement)", len(transports))
	}
	assertTransportClosed(t, transports[0], "superseded client")
	assertTransportClosed(t, transports[1], "replacement client")
}
