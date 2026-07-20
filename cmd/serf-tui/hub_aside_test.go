package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appserver"
)

// TestSendHubAsideForksAtTip verifies the /aside dispatch sends thread/fork in
// aside mode — no source turn, no edited input — and reports the child ref.
func TestSendHubAsideForksAtTip(t *testing.T) {
	app := appserver.NewServer(appserver.ServerConfig{
		ServerName: "hub",
		SourceID:   "local",
		Features:   appwire.FeatureSet{},
	})
	var got appwire.ThreadForkParams
	appserver.HandleTyped(app.Router(), appwire.MethodThreadFork, func(_ context.Context, params appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
		got = params
		return appwire.ThreadForkResponse{Thread: appwire.Thread{
			ID:        "child1",
			SessionID: "child1",
			Source:    "local",
			Serf:      appwire.SerfThread{Ref: "local:child1"},
		}}, nil
	})
	client, cleanup := newTUIAppWireClient(t, app)
	defer cleanup()

	msg := sendHubAside(client, appwire.Ref{SourceID: "local", ThreadID: "01PARENT"})()
	forkMsg, ok := msg.(hubForkMsg)
	if !ok || forkMsg.err != nil {
		t.Fatalf("msg=%T err=%v", msg, forkMsg.err)
	}
	if !forkMsg.aside {
		t.Fatal("hubForkMsg.aside = false, want true for the /aside path")
	}
	if got.Ref != "local:01PARENT" {
		t.Fatalf("params.Ref=%q, want local:01PARENT", got.Ref)
	}
	if !got.Aside {
		t.Fatalf("params.Aside=%v, want true", got.Aside)
	}
	if got.SourceTurnID != "" || got.EditedInput != "" || got.Label != "" {
		t.Fatalf("aside must not carry divergent-fork fields: %+v", got)
	}
	if forkMsg.resp.Ref != "local:child1" {
		t.Fatalf("fork response ref=%q, want local:child1", forkMsg.resp.Ref)
	}
}

// TestHubModelAsideCommandDispatches verifies /aside runs from the session
// slash-command path when the source advertises fork.
func TestHubModelAsideCommandDispatches(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Capabilities.Fork = true
	m.session.setInputValue("/aside")

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("/aside should dispatch an async command when fork is available")
	}
}

// TestHubModelAsideCommandUnavailableWithoutFork pins the capability gate:
// without an advertised fork capability the command explains itself instead of
// issuing an RPC.
func TestHubModelAsideCommandUnavailableWithoutFork(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Capabilities.Fork = false
	m.session.setInputValue("/aside")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("/aside must not issue an RPC when fork is unavailable")
	}
	um := updated.(hubModel)
	got := um.sessionView()
	if !strings.Contains(got, "Aside is not available for this session.") {
		t.Fatalf("unavailable notice missing:\n%s", got)
	}
}

// TestHubModelAsideFailureMessage verifies an aside failure is attributed to
// the aside command, not to fork-from-turn.
func TestHubModelAsideFailureMessage(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Capabilities.Fork = true

	updated, _ := m.Update(hubForkMsg{aside: true, err: errors.New("appwire thread/fork: boom")})
	um := updated.(hubModel)
	got := um.sessionView()
	if !strings.Contains(got, "Aside failed: appwire thread/fork: boom") {
		t.Fatalf("aside failure message missing:\n%s", got)
	}
}

// TestHubModelHelpIncludesAside pins /aside in help and palette when fork is
// advertised, and its absence otherwise.
func TestHubModelHelpIncludesAside(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Capabilities = hubSessionCapabilities{Fork: true}
	help := hubSlashCommandHelp(m.detail.Capabilities)
	if !strings.Contains(help, "/aside") {
		t.Fatalf("help missing /aside with fork capability:\n%s", help)
	}

	m.detail.Capabilities = hubSessionCapabilities{Send: true}
	help = hubSlashCommandHelp(m.detail.Capabilities)
	if strings.Contains(help, "/aside") {
		t.Fatalf("help advertised /aside without fork capability:\n%s", help)
	}
}
