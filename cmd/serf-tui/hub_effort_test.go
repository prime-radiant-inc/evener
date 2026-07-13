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

// (a) /effort is registered and gated on ChangeModel, same as /model.
func TestHubModelEffortCommandGatedOnChangeModel(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Capabilities.ChangeModel = false
	m.session.setInputValue("/effort")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("unavailable effort command should not return a command")
	}
	got := updated.(hubModel)
	if got.sessionEffortPicker != nil {
		t.Fatal("unavailable effort picker should stay closed")
	}
	if view := got.View(); !strings.Contains(view, "not available for this session") {
		t.Fatalf("missing unavailable effort message:\n%s", view)
	}
}

// (b) Bare /effort opens a picker of the current model's levels, snapshot-first.
func TestHubModelBareEffortOpensPickerFromSnapshot(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.SupportsReasoning = true
	m.detail.ReasoningEffort = "medium"
	m.detail.ReasoningEffortLevels = []string{"low", "medium", "high"}
	m.session.setInputValue("/effort")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("bare /effort should build the picker from the cached snapshot without a round trip")
	}
	got := updated.(hubModel)
	if got.sessionEffortPicker == nil {
		t.Fatalf("expected session effort picker:\n%s", got.View())
	}
	view := got.View()
	if !strings.Contains(view, "low") || !strings.Contains(view, "medium") || !strings.Contains(view, "high") {
		t.Fatalf("effort picker view missing levels:\n%s", view)
	}
	if !strings.Contains(view, "(active)") {
		t.Fatalf("effort picker should mark the active level:\n%s", view)
	}
}

// (b) supportsReasoning === false means known-empty: no picker, informative message.
func TestHubModelBareEffortWithoutReasoningSupportShowsMessage(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.SupportsReasoning = false
	m.detail.ReasoningEffortLevels = nil
	m.session.setInputValue("/effort")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("unsupported effort should not return a command")
	}
	got := updated.(hubModel)
	if got.sessionEffortPicker != nil {
		t.Fatal("effort picker should not open when the model does not support reasoning")
	}
	if view := got.View(); !strings.Contains(view, "does not support reasoning effort") {
		t.Fatalf("missing unsupported-reasoning message:\n%s", view)
	}
}

// (c) /effort high validates client-side against the snapshot list and sends
// thread/reasoning-effort/set.
func TestHubModelEffortArgSendsReasoningEffortSet(t *testing.T) {
	var gotEffort appwire.ThreadReasoningEffortSetParams
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadReasoningEffortSet, func(_ context.Context, params appwire.ThreadReasoningEffortSetParams) (appwire.EmptyResponse, error) {
			gotEffort = params
			return appwire.EmptyResponse{}, nil
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.detail.SupportsReasoning = true
	m.detail.ReasoningEffortLevels = []string{"low", "medium", "high"}
	m.session.setInputValue("/effort high")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("/effort high should send thread/reasoning-effort/set")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	m = updated.(hubModel)

	if gotEffort.Ref != "local:01SEND" || gotEffort.ReasoningEffort != "high" {
		t.Fatalf("effort set params=%+v, want local:01SEND high", gotEffort)
	}
	if view := m.View(); !strings.Contains(view, "Reasoning effort updated.") {
		t.Fatalf("missing effort updated message:\n%s", view)
	}
}

// (c) An unknown level is rejected client-side without a round trip.
func TestHubModelEffortArgRejectsUnknownLevel(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.SupportsReasoning = true
	m.detail.ReasoningEffortLevels = []string{"low", "medium", "high"}
	m.session.setInputValue("/effort extreme")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("unknown effort level should not send a request")
	}
	got := updated.(hubModel)
	if view := got.View(); !strings.Contains(view, "extreme") {
		t.Fatalf("missing rejection message for unknown level:\n%s", view)
	}
}

// (d) thread/model/changed updates m.detail.Model, the cached levels, and
// the dashboard row's Model column.
func TestHubModelThreadModelChangedNotificationUpdatesModelAndLevels(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Model = "gpt-5"
	m.detail.ReasoningEffortLevels = []string{"low", "high"}
	m.detail.SupportsReasoning = true
	m.rows = []hubRow{{
		kind:  hubRowSession,
		ref:   mustParseRef(t, "local:01SEND"),
		model: "gpt-5",
	}}

	notification := appwire.NotificationMessage(appwire.NotifyThreadModelChanged, appwire.ThreadModelChangedParams{
		ThreadID:              "01SEND",
		Ref:                   "local:01SEND",
		ModelProvider:         "anthropic",
		Model:                 "claude-opus-4-6",
		ReasoningEffortLevels: []string{"low", "medium", "high"},
		SupportsReasoning:     true,
	}).Notification
	m.applyHubNotification(*notification)

	if m.detail.Model != "claude-opus-4-6" {
		t.Fatalf("detail.Model = %q, want claude-opus-4-6", m.detail.Model)
	}
	if strings.Join(m.detail.ReasoningEffortLevels, ",") != "low,medium,high" {
		t.Fatalf("detail.ReasoningEffortLevels = %v", m.detail.ReasoningEffortLevels)
	}
	if !m.detail.SupportsReasoning {
		t.Fatal("detail.SupportsReasoning should be true")
	}
	if m.rows[0].model != "claude-opus-4-6" {
		t.Fatalf("dashboard row model = %q, want claude-opus-4-6", m.rows[0].model)
	}
}

// (f) The session header renders an effort part beside model.
func TestHubModelSessionHeaderRendersEffortPart(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Model = "claude-opus-4-6"
	m.detail.ReasoningEffort = "high"

	lines := m.sessionHeaderLines()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "effort") || !strings.Contains(joined, "high") {
		t.Fatalf("session header missing effort part:\n%s", joined)
	}
}

// (g) A rejected switch (turn active / validation error) renders the
// server's message as a notice.
func TestHubModelEffortRejectionRendersNotice(t *testing.T) {
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadReasoningEffortSet, func(context.Context, appwire.ThreadReasoningEffortSetParams) (appwire.EmptyResponse, error) {
			return appwire.EmptyResponse{}, errors.New("a turn is active on this session")
		})
	})
	defer cleanup()

	m := newSessionHubModel(client)
	m.detail.SupportsReasoning = true
	m.detail.ReasoningEffortLevels = []string{"low", "medium", "high"}
	m.session.setInputValue("/effort high")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("/effort high should send a request")
	}
	updated, _ = updated.(hubModel).Update(cmd())
	m = updated.(hubModel)

	if view := m.View(); !strings.Contains(view, "turn is active") {
		t.Fatalf("expected the server's rejection message rendered as a notice:\n%s", view)
	}
}

func mustParseRef(t *testing.T, s string) appwire.Ref {
	t.Helper()
	ref, err := appwire.ParseRef(s)
	if err != nil {
		t.Fatalf("ParseRef(%q): %v", s, err)
	}
	return ref
}
