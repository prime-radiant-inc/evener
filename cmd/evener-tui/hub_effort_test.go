package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuipick"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/llm"
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
	if _, ok := noticeByCategory(got, "action-unavailable"); !ok {
		t.Fatalf("unavailable effort raised no action-unavailable notice: %+v", got.notices)
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
	ids := pickerItemIDs(*got.sessionEffortPicker)
	for _, level := range []string{"low", "medium", "high"} {
		if !slices.Contains(ids, level) {
			t.Fatalf("effort picker items = %v, missing %q", ids, level)
		}
	}
	if active := got.sessionEffortPicker.Active(); active != "medium" {
		t.Fatalf("effort picker active = %q, want the session's medium", active)
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
	if len(got.session.messages) != 1 || got.session.messages[0].Kind != transcript.MsgSystem {
		t.Fatalf("unsupported effort posted no explanation: %+v", got.session.messages)
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
	if len(m.notices) != 0 || len(m.session.messages) != 1 || m.session.messages[0].Kind != transcript.MsgSystem {
		t.Fatalf("effort update was not confirmed in the session: notices=%+v messages=%+v", m.notices, m.session.messages)
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
	if len(got.session.messages) != 1 || got.session.messages[0].Kind != transcript.MsgSystem {
		t.Fatalf("unknown level posted no rejection: %+v", got.session.messages)
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

	notice, ok := noticeByCategory(m, "action")
	if !ok || !strings.Contains(notice.Reason, "a turn is active on this session") {
		t.Fatalf("server rejection notice = %+v (found %v), want the server's message as its reason", notice, ok)
	}
}

// pickerItemIDs lists the IDs a picker offers, in order.
func pickerItemIDs(p tuipick.ModelPicker) []string {
	var ids []string
	for _, item := range p.Items() {
		ids = append(ids, item.ID)
	}
	return ids
}

func mustParseRef(t *testing.T, s string) appwire.Ref {
	t.Helper()
	ref, err := appwire.ParseRef(s)
	if err != nil {
		t.Fatalf("ParseRef(%q): %v", s, err)
	}
	return ref
}

// A reasoning model that states no ladder still takes an effort: the session
// sends one on every request and the request builder passes it through
// unclamped, so /effort must offer the canonical tiers rather than deny they
// exist. lunaroute/glm-5.3 is the shape — an uncataloged gateway row — and
// google/gemini-2.5-pro and anthropic/claude-sonnet-4-5 are the cataloged
// budget-only rows that land the same way.
func TestHubModelEffortOnARowWithNoLadderOffersTheCanonicalTiers(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.SupportsReasoning = true
	m.detail.ReasoningEffort = "medium"
	m.detail.ReasoningEffortLevels = nil
	m.session.setInputValue("/effort")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("bare /effort should build the picker from the cached snapshot without a round trip")
	}
	got := updated.(hubModel)
	if got.sessionEffortPicker == nil {
		t.Fatalf("expected a picker for a reasoning model with no stated ladder:\n%s", got.View())
	}
	ids := pickerItemIDs(*got.sessionEffortPicker)
	for _, level := range append(llm.ReasoningEffortVocabulary(), "none") {
		if !slices.Contains(ids, level) {
			t.Fatalf("effort picker items = %v, missing %q", ids, level)
		}
	}
}

// And a level typed directly is accepted on such a row, matching what
// --reasoning-effort accepts at launch.
func TestHubModelEffortLevelSettableOnARowWithNoLadder(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.SupportsReasoning = true
	m.detail.ReasoningEffortLevels = nil
	m.session.setInputValue("/effort high")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("/effort high must reach the wire on a reasoning row with no stated ladder:\n%s", updated.(hubModel).View())
	}
	if msgs := updated.(hubModel).session.messages; len(msgs) != 0 {
		t.Fatalf("/effort high was rejected: %+v", msgs)
	}
}
