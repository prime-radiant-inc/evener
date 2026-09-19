package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
)

// windowTitleCmds runs cmd (flattening every tea.Batch level) and returns every
// title the tea.SetWindowTitle commands it contains would set, plus every other
// message the batch produced. bubbletea's setWindowTitleMsg is unexported, so a
// title command is identified by its message type name. Collecting all of them
// (rather than stopping at the first) lets callers assert the exact command set:
// a batch with a duplicate or unexpected extra command must not pass.
func windowTitleCmds(cmd tea.Cmd) (titles []string, others []tea.Msg) {
	var walk func(tea.Cmd)
	walk = func(c tea.Cmd) {
		if c == nil {
			return
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			for _, child := range msg {
				walk(child)
			}
		default:
			if fmt.Sprintf("%T", msg) == "tea.setWindowTitleMsg" {
				titles = append(titles, fmt.Sprint(msg))
				return
			}
			others = append(others, msg)
		}
	}
	walk(cmd)
	return titles, others
}

// requireOnlyWindowTitle asserts cmd produced exactly one SetWindowTitle with
// want and no other command.
func requireOnlyWindowTitle(t *testing.T, cmd tea.Cmd, want string) {
	t.Helper()
	titles, others := windowTitleCmds(cmd)
	if len(titles) != 1 {
		t.Fatalf("got %d tea.SetWindowTitle commands (%q), want exactly 1", len(titles), titles)
	}
	if titles[0] != want {
		t.Fatalf("SetWindowTitle = %q, want %q", titles[0], want)
	}
	if len(others) != 0 {
		t.Fatalf("SetWindowTitle came with unexpected commands: %#v", others)
	}
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
	_, cmd := m.Update(hubSessionMsg{
		detail: hubSessionDetail{
			Ref:       "local:th_1",
			SessionID: "sess_1",
			Title:     "Fix the flaky test",
			State:     appwire.ThreadStatusIdle,
		},
		ref: "local:th_1",
	})
	requireOnlyWindowTitle(t, cmd, "Fix the flaky test")
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
	titles, others := windowTitleCmds(cmd)
	if len(titles) != 1 {
		t.Fatalf("got %d SetWindowTitle commands (%q), want exactly 1", len(titles), titles)
	}
	if titles[0] != "Compaction refresh name" {
		t.Fatalf("SetWindowTitle = %q, want %q", titles[0], "Compaction refresh name")
	}
	// The only other leg is the frame wait, primed above.
	if len(others) != 1 {
		t.Fatalf("unexpected commands beside the title: %#v", others)
	}
	if _, isDrain := others[0].(hubNotificationMsg); !isDrain {
		t.Fatalf("extra command = %T, want the primed frame drain", others[0])
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
	requireOnlyWindowTitle(t, cmd, "")
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
	requireOnlyWindowTitle(t, cmd, "01SESS")

	_, cmd2 := hm.Update(hubSessionMsg{
		detail: hubSessionDetail{Ref: "local:th_2", SessionID: "", State: appwire.ThreadStatusIdle},
		ref:    "local:th_2",
	})
	requireOnlyWindowTitle(t, cmd2, "local:th_2")
}

// An ordinary update that neither changes the view nor the name must not
// re-emit the OSC escape: Update runs on every keypress and streaming frame.
func TestWindowTitleNotReemittedWhenUnchanged(t *testing.T) {
	m := enterWindowTitleSession(t, "Stable name")
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if titles, others := windowTitleCmds(cmd); len(titles) != 0 || len(others) != 0 {
		t.Fatalf("unchanged title emitted a command: titles=%q others=%#v", titles, others)
	}
}

// No title is set while no session view is open.
func TestWindowTitleEmptyOutsideSessionView(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if titles, others := windowTitleCmds(cmd); len(titles) != 0 || len(others) != 0 {
		t.Fatalf("dashboard update emitted a command: titles=%q others=%#v", titles, others)
	}
}

// A name carrying terminal control characters must not reach the OSC title:
// C0 BEL/ESC would close the escape string early, and a UTF-8 terminal can read
// the C1 range (U+009B/U+009D as CSI/OSC introducers, U+009C as OSC end) the
// same way — injecting sequences (OSC 52 clipboard writes, mode changes) into
// the user's terminal.
func TestWindowTitleSanitizesControlSequences(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	_, cmd := m.Update(hubSessionMsg{
		detail: hubSessionDetail{
			Ref:       "local:th_1",
			SessionID: "sess_1",
			Title:     "evil\x1b]52;c;clip\x07name\u009b31m\u009d\u009cend",
			State:     appwire.ThreadStatusIdle,
		},
		ref: "local:th_1",
	})
	titles, others := windowTitleCmds(cmd)
	if len(titles) != 1 {
		t.Fatalf("got %d SetWindowTitle commands (%q), want exactly 1", len(titles), titles)
	}
	const want = "evil]52;c;clipname31mend"
	if titles[0] != want {
		t.Fatalf("SetWindowTitle = %q, want %q", titles[0], want)
	}
	if len(others) != 0 {
		t.Fatalf("SetWindowTitle came with unexpected commands: %#v", others)
	}
	for _, r := range titles[0] {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			t.Fatalf("SetWindowTitle %q still carries control rune %#U", titles[0], r)
		}
	}
}

// The sanitizer strips C0, DEL, and C1, preserves printable text including
// non-ASCII, and caps length so a hostile preview cannot flood the title bar.
func TestTerminalTitleStripsControlsAndCapsLength(t *testing.T) {
	if got := terminalTitle("a\tb\x1bc\x07d\x7fe\u009bf\u009cg"); got != "abcdefg" {
		t.Fatalf("terminalTitle = %q, want %q", got, "abcdefg")
	}
	if got := terminalTitle("café ☕"); got != "café ☕" {
		t.Fatalf("terminalTitle dropped printable runes: %q", got)
	}
	long := strings.Repeat("x", maxWindowTitleRunes+50)
	if got := terminalTitle(long); len([]rune(got)) != maxWindowTitleRunes {
		t.Fatalf("terminalTitle length = %d, want %d", len([]rune(got)), maxWindowTitleRunes)
	}
}

// The name is sanitized where detail.Title is derived from the wire, so the
// session header and dashboard row render safe text too, not just the title.
func TestHubDetailFromThreadSanitizesDisplayName(t *testing.T) {
	detail := hubDetailFromThread(appwire.Thread{
		ID:        "th_1",
		SessionID: "sess_1",
		Name:      "a\x07b\x1bc\u009bd",
	})
	if detail.Title != "abcd" {
		t.Fatalf("detail.Title = %q, want %q", detail.Title, "abcd")
	}
}

// The identity fallbacks (session id, ref) must be sanitized too: the header and
// chrome render sessionDisplayName verbatim, so a control-only id or ref would
// otherwise reach them raw even though the OSC title sink is protected.
func TestSessionDisplayNameSanitizesIdentityFallback(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: "local:th_1", SessionID: "sess\u009b31m"}
	if got := m.sessionDisplayName(); got != "sess31m" {
		t.Fatalf("sessionDisplayName = %q, want %q", got, "sess31m")
	}
	// A control-only session id falls through to the ref, also sanitized.
	m.detail = hubSessionDetail{Ref: "local:th\u009d1", SessionID: "\u009b"}
	if got := m.sessionDisplayName(); got != "local:th1" {
		t.Fatalf("sessionDisplayName = %q, want %q", got, "local:th1")
	}
}

// A rename frame that carries no identity must not relabel whichever session
// happens to be open: notificationMatchesCurrentSession treats an empty
// ref/threadId as a match, which is right for other frames but wrong here.
func TestThreadNameChangedWithoutIdentityDoesNotRelabelViewedSession(t *testing.T) {
	m := enterWindowTitleSession(t, "Viewed session")
	message := appwire.NotificationMessage(appwire.NotifyThreadNameChanged, appwire.ThreadNameChangedParams{
		Name:   "Anonymous rename",
		Source: "user",
	})
	updated, _ := m.Update(hubNotificationMsg{ok: true, notification: *message.Notification})
	hm := updated.(hubModel)
	if hm.detail.Title != "Viewed session" {
		t.Fatalf("unidentified rename relabelled the viewed session: %q", hm.detail.Title)
	}
}

// A rename push must also refresh the cached dashboard row and tree node, or a
// return to the dashboard shows the old name until the next tree fetch.
func TestThreadNameChangedUpdatesCachedDashboardTitle(t *testing.T) {
	m := enterWindowTitleSession(t, "Old name")
	ref, err := appwire.ParseRef("local:th_1")
	if err != nil {
		t.Fatalf("parse ref: %v", err)
	}
	m.rows = []hubRow{{kind: hubRowSession, ref: ref, title: "Old name"}}
	m.tree.Live = []hubTreeNode{{Ref: "local:th_1", SessionID: "sess_1", Title: "Old name"}}

	message := appwire.NotificationMessage(appwire.NotifyThreadNameChanged, appwire.ThreadNameChangedParams{
		ThreadID: "sess_1",
		Ref:      "local:th_1",
		Name:     "New\x07 name",
		Source:   "user",
	})
	updated, _ := m.Update(hubNotificationMsg{ok: true, notification: *message.Notification})
	hm := updated.(hubModel)
	if hm.rows[0].title != "New name" {
		t.Fatalf("row title = %q, want %q", hm.rows[0].title, "New name")
	}
	if hm.tree.Live[0].Title != "New name" {
		t.Fatalf("tree node title = %q, want %q", hm.tree.Live[0].Title, "New name")
	}
}

// A rename that arrives while the dashboard is showing (another client, or an
// auto-namer finishing) must still refresh the cached row and tree node: the
// dashboard has no periodic refresh.
func TestThreadNameChangedUpdatesDashboardOutsideSessionView(t *testing.T) {
	m := newHubModel(nil, "http://hub.test") // dashboard mode
	ref, err := appwire.ParseRef("local:th_1")
	if err != nil {
		t.Fatalf("parse ref: %v", err)
	}
	m.rows = []hubRow{{kind: hubRowSession, ref: ref, title: "Old name"}}
	m.tree.Live = []hubTreeNode{{Ref: "local:th_1", SessionID: "sess_1", Title: "Old name"}}
	m.detail = hubSessionDetail{Ref: "local:th_2", SessionID: "other", Title: "Other session"}

	message := appwire.NotificationMessage(appwire.NotifyThreadNameChanged, appwire.ThreadNameChangedParams{
		ThreadID: "sess_1",
		Ref:      "local:th_1",
		Name:     "New name",
		Source:   "user",
	})
	updated, _ := m.Update(hubNotificationMsg{ok: true, notification: *message.Notification})
	hm := updated.(hubModel)
	if hm.mode != hubModeDashboard {
		t.Fatalf("mode = %v, want dashboard", hm.mode)
	}
	if hm.rows[0].title != "New name" {
		t.Fatalf("row title = %q, want %q", hm.rows[0].title, "New name")
	}
	if hm.tree.Live[0].Title != "New name" {
		t.Fatalf("tree node title = %q, want %q", hm.tree.Live[0].Title, "New name")
	}
	if hm.detail.Title != "Other session" {
		t.Fatalf("renamed a different session's detail: %q", hm.detail.Title)
	}
}

// Quitting from a session must clear the terminal title before the program
// exits, or the terminal keeps the session title.
func TestQuitClearsWindowTitleBeforeQuitting(t *testing.T) {
	cmd := quitCmd()
	msg := cmd()
	// tea.Sequence returns an unexported ordered cmd slice.
	seq := reflect.ValueOf(msg)
	if seq.Kind() != reflect.Slice {
		t.Fatalf("quitCmd produced %T, want an ordered sequence", msg)
	}
	var titles []string
	sawQuit := false
	for i := 0; i < seq.Len(); i++ {
		child, ok := reflect.TypeAssert[tea.Cmd](seq.Index(i))
		if !ok {
			t.Fatalf("sequence element %d is not a tea.Cmd", i)
		}
		switch childMsg := child().(type) {
		case tea.QuitMsg:
			if len(titles) == 0 {
				t.Fatal("quit ran before the window title was cleared")
			}
			sawQuit = true
		default:
			if fmt.Sprintf("%T", childMsg) == "tea.setWindowTitleMsg" {
				titles = append(titles, fmt.Sprint(childMsg))
			}
		}
	}
	if len(titles) != 1 || titles[0] != "" {
		t.Fatalf("titles = %q, want one empty clear", titles)
	}
	if !sawQuit {
		t.Fatal("quitCmd did not quit")
	}
}
