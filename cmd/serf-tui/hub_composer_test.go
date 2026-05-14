package main

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appwire"
)

func TestHubModelSessionComposerIsVisibleWhenEmpty(t *testing.T) {
	m := newSessionHubModel(nil)

	got := m.sessionView()
	for _, want := range []string{"message", "> "} {
		if !strings.Contains(got, want) {
			t.Fatalf("session composer missing %q:\n%s", want, got)
		}
	}
}

func TestHubModelSessionComposerShowsReadOnlyReasonAndDraft(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Capabilities.Send = false
	m.detail.Capabilities.Steer = false
	m.session.setInputValue("keep this draft")

	got := m.sessionView()
	for _, want := range []string{"read-only:", "source does not support send", "> keep this draft"} {
		if !strings.Contains(got, want) {
			t.Fatalf("read-only composer missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "enter: send") {
		t.Fatalf("read-only composer advertised send:\n%s", got)
	}
}

func TestHubModelBusyComposerShowsSteerOrReadOnlyMode(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.State = "processing"
	m.detail.Capabilities.Send = false
	m.detail.Capabilities.Steer = true
	m.session.processing = true
	m.session.setInputValue("nudge the running turn")

	got := m.sessionView()
	for _, want := range []string{"steer", "enter: steer", "> nudge the running turn"} {
		if !strings.Contains(got, want) {
			t.Fatalf("busy steer composer missing %q:\n%s", want, got)
		}
	}

	m.detail.Capabilities.Steer = false
	got = m.sessionView()
	for _, want := range []string{"read-only:", "source does not advertise steer", "> nudge the running turn"} {
		if !strings.Contains(got, want) {
			t.Fatalf("busy read-only composer missing %q:\n%s", want, got)
		}
	}
}

func TestHubModelBusyEnterRoutesToSteerAndPreservesDraft(t *testing.T) {
	var got appwire.TurnSteerParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnSteer, func(_ context.Context, params appwire.TurnSteerParams) (appwire.EmptyResponse, error) {
			got = params
			return appwire.EmptyResponse{}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.detail.State = "processing"
	m.detail.Capabilities.Send = false
	m.detail.Capabilities.Steer = true
	m.detail.ActiveTurnID = "turn_busy"
	m.session.processing = true
	m.session.setInputValue("please keep going")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("busy enter should steer through hub")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	steered := updated.(hubModel)

	if got.Ref != "local:01SEND" || got.TurnID != "turn_busy" || got.Text != "please keep going" {
		t.Fatalf("steer params=%+v", got)
	}
	if steered.session.input.Value() != "please keep going" {
		t.Fatalf("busy steer should preserve draft, got %q", steered.session.input.Value())
	}
	if view := steered.View(); !strings.Contains(view, "Steering sent.") {
		t.Fatalf("missing steer confirmation:\n%s", view)
	}
}

func TestHubModelSessionComposerCtrlJInsertsNewline(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.setInputValue("line one")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if cmd != nil {
		t.Fatal("ctrl+j newline should be synchronous")
	}
	m = updated.(hubModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line two")})
	m = updated.(hubModel)

	if got := m.session.input.Value(); got != "line one\nline two" {
		t.Fatalf("composer draft=%q, want multiline draft", got)
	}
	if got := m.sessionView(); !strings.Contains(got, "> line one\n  line two") {
		t.Fatalf("multiline composer did not render continuation line:\n%s", got)
	}
}

func TestHubModelSessionComposerUsesHistoryNavigation(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.history = []string{"first request", "second request"}
	m.session.setInputValue("current draft")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if cmd != nil {
		t.Fatal("history up should be synchronous")
	}
	m = updated.(hubModel)
	if got := m.session.input.Value(); got != "second request" {
		t.Fatalf("history up input=%q, want second request", got)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(hubModel)
	if got := m.session.input.Value(); got != "first request" {
		t.Fatalf("second history up input=%q, want first request", got)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(hubModel)
	if got := m.session.input.Value(); got != "second request" {
		t.Fatalf("history down input=%q, want second request", got)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(hubModel)
	if got := m.session.input.Value(); got != "current draft" {
		t.Fatalf("history restored draft=%q, want current draft", got)
	}
}

func TestHubModelSessionComposerAddsSentPromptsToHistory(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_sent", Status: appwire.TurnStatusRunning}}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.session.setInputValue("remember this")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("send returned nil command")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	m = updated.(hubModel)

	if len(m.session.history) == 0 || unescapeHistory(m.session.history[len(m.session.history)-1]) != "remember this" {
		t.Fatalf("history=%v, want sent prompt appended", m.session.history)
	}
}

func TestHubModelSessionPickerOverlayKeepsComposerDraftVisible(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.setInputValue("draft survives overlay")
	picker := newModelPicker([]modelPickerItem{{id: "openai/gpt-5", display: "openai/gpt-5"}}, "", 80)
	m.sessionModelPicker = &picker

	got := m.sessionView()
	for _, want := range []string{"Select model", "openai/gpt-5", "> draft survives overlay"} {
		if !strings.Contains(got, want) {
			t.Fatalf("session picker overlay missing %q:\n%s", want, got)
		}
	}
}

func TestHubModelSpawnModelPickerKeepsFormDraftVisible(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.openSpawnForm()
	m.spawnModels = []modelPickerItem{{id: "openai/gpt-5", display: "openai/gpt-5"}}
	m.spawnModel = "openai/gpt-5"
	m.session.setInputValue("spawn draft survives overlay")
	m.openSpawnModelPicker(m.spawnModels)

	got := m.spawnView()
	for _, want := range []string{"Select spawn model", "Prompt", "> spawn draft survives overlay"} {
		if !strings.Contains(got, want) {
			t.Fatalf("spawn model picker overlay missing %q:\n%s", want, got)
		}
	}
}
