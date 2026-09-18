package tui

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
)

// windowTitleFromCmd runs cmd (and any tea.Batch it contains) and returns the
// title of the tea.SetWindowTitle command it finds. bubbletea's
// setWindowTitleMsg is unexported, so the command is identified by its message
// type name; the walk executes every command it is handed, so callers must
// pass a command whose legs do not block.
func windowTitleFromCmd(cmd tea.Cmd) (string, bool) {
	var title string
	var found bool
	var walk func(tea.Cmd)
	walk = func(c tea.Cmd) {
		if c == nil || found {
			return
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			for _, child := range msg {
				walk(child)
			}
		default:
			if fmt.Sprintf("%T", msg) == "tea.setWindowTitleMsg" {
				title = fmt.Sprint(msg)
				found = true
			}
		}
	}
	walk(cmd)
	return title, found
}

// enterWindowTitleSession drives a real session entry so the tests start from
// the same state the model reaches when the user opens a session.
func enterWindowTitleSession(t *testing.T, title string) hubModel {
	t.Helper()
	m := newHubModel(nil, "http://hub.test")
	updated, _ := m.Update(hubSessionMsg{
		detail: hubSessionDetail{
			Ref:       "local:th_1",
			SessionID: "sess_1",
			Title:     title,
			State:     appwire.ThreadStatusIdle,
		},
		ref: "local:th_1",
	})
	return updated.(hubModel)
}

// Entering a session must title the terminal with the session's display name.
func TestUpdateSetsWindowTitleOnSessionEntry(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	updated, cmd := m.Update(hubSessionMsg{
		detail: hubSessionDetail{
			Ref:       "local:th_1",
			SessionID: "sess_1",
			Title:     "Fix the flaky test",
			State:     appwire.ThreadStatusIdle,
		},
		ref: "local:th_1",
	})
	hm := updated.(hubModel)
	if got := hm.windowTitle; got != "Fix the flaky test" {
		t.Fatalf("recorded window title = %q, want %q", got, "Fix the flaky test")
	}
	title, ok := windowTitleFromCmd(cmd)
	if !ok {
		t.Fatal("session entry returned no tea.SetWindowTitle command")
	}
	if title != "Fix the flaky test" {
		t.Fatalf("SetWindowTitle = %q, want %q", title, "Fix the flaky test")
	}
}

// A session-name change (a user rename, the auto-namer's first name, or a
// compaction refresh) must retitle the terminal live.
func TestUpdateSetsWindowTitleOnNameChanged(t *testing.T) {
	m := enterWindowTitleSession(t, "Original name")

	// Prime the feed so the notification branch's wait command returns instead
	// of blocking when the test runs the batched commands.
	feed := newHubFrameFeed()
	feed.Observe(appwire.Message{Notification: &appwire.Notification{Method: "test/drain"}}, nil)
	m.frames = feed

	message := appwire.NotificationMessage(appwire.NotifyThreadNameChanged, appwire.ThreadNameChangedParams{
		ThreadID: "sess_1",
		Ref:      "local:th_1",
		Name:     "Compaction refresh name",
		Source:   "compaction",
	})
	updated, cmd := m.Update(hubNotificationMsg{ok: true, notification: *message.Notification})
	hm := updated.(hubModel)
	if got := hm.detail.Title; got != "Compaction refresh name" {
		t.Fatalf("detail title = %q, want %q", got, "Compaction refresh name")
	}
	title, ok := windowTitleFromCmd(cmd)
	if !ok {
		t.Fatal("name-changed event returned no tea.SetWindowTitle command")
	}
	if title != "Compaction refresh name" {
		t.Fatalf("SetWindowTitle = %q, want %q", title, "Compaction refresh name")
	}
}

// Leaving the session view must clear the title rather than leave the window
// labelled with the session the user navigated away from.
func TestUpdateClearsWindowTitleOnDashboardReturn(t *testing.T) {
	m := enterWindowTitleSession(t, "Named session")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	hm := updated.(hubModel)
	if hm.mode != hubModeDashboard {
		t.Fatalf("mode = %v, want hubModeDashboard after ctrl+o", hm.mode)
	}
	if got := hm.windowTitle; got != "" {
		t.Fatalf("recorded window title = %q, want empty", got)
	}
	title, ok := windowTitleFromCmd(cmd)
	if !ok {
		t.Fatal("dashboard return returned no tea.SetWindowTitle command")
	}
	if title != "" {
		t.Fatalf("SetWindowTitle = %q, want an empty (clearing) title", title)
	}
}

// The display name's fallback chain reaches the session id, then the ref, when
// a session has no name yet.
func TestWindowTitleFallsBackThroughSessionIdentity(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	updated, cmd := m.Update(hubSessionMsg{
		detail: hubSessionDetail{Ref: "local:th_1", SessionID: "01SESS", State: appwire.ThreadStatusIdle},
		ref:    "local:th_1",
	})
	hm := updated.(hubModel)
	if title, ok := windowTitleFromCmd(cmd); !ok || title != "01SESS" {
		t.Fatalf("SetWindowTitle = (%q, %v), want (\"01SESS\", true)", title, ok)
	}

	updated2, cmd2 := hm.Update(hubSessionMsg{
		detail: hubSessionDetail{Ref: "local:th_2", SessionID: "", State: appwire.ThreadStatusIdle},
		ref:    "local:th_2",
	})
	_ = updated2
	if title, ok := windowTitleFromCmd(cmd2); !ok || title != "local:th_2" {
		t.Fatalf("SetWindowTitle = (%q, %v), want (\"local:th_2\", true)", title, ok)
	}
}

// An ordinary update that neither changes the view nor the name must not
// re-emit the OSC escape: Update runs on every keypress and streaming frame.
func TestWindowTitleNotReemittedWhenUnchanged(t *testing.T) {
	m := enterWindowTitleSession(t, "Stable name")
	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	hm := updated.(hubModel)
	if hm.windowTitle != "Stable name" {
		t.Fatalf("recorded window title = %q, want %q", hm.windowTitle, "Stable name")
	}
	if _, ok := windowTitleFromCmd(cmd); ok {
		t.Fatal("an unchanged title re-emitted tea.SetWindowTitle")
	}
}

// No title is set while no session view is open.
func TestWindowTitleEmptyOutsideSessionView(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if _, ok := windowTitleFromCmd(cmd); ok {
		t.Fatal("dashboard update emitted a tea.SetWindowTitle command")
	}
}
