package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/clipboard"
	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
	"primeradiant.com/evener/internal/appserver"
)

// --- hub_browse.go ---

// TestCovEnterSessionBrowse exercises entering browse mode.
func TestCovEnterSessionBrowse(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.messages = []transcript.ChatMessage{
		{Kind: transcript.MsgUser, Text: "response"},
	}
	m.enterSessionBrowse(false)
	if !m.session.scrollMode {
		t.Fatal("should set scrollMode")
	}

	// With pageUp.
	m = newSessionHubModel(nil)
	m.session.messages = []transcript.ChatMessage{
		{Kind: transcript.MsgUser, Text: "response"},
	}
	m.enterSessionBrowse(true)
	if !m.session.scrollMode {
		t.Fatal("should set scrollMode with pageUp")
	}
}

// TestCovExitSessionBrowse exercises exiting browse mode.
func TestCovExitSessionBrowse(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.scrollMode = true
	m.exitSessionBrowse()
	if m.session.scrollMode {
		t.Fatal("should clear scrollMode")
	}
	if m.browseSelected != -1 {
		t.Fatal("should reset browseSelected")
	}
}

// TestCovReturnToDashboard exercises return to dashboard.
func TestCovReturnToDashboard(t *testing.T) {
	m := newSessionHubModel(nil)
	m.mode = hubModeSession
	m.returnToDashboard()
	if m.mode != hubModeDashboard {
		t.Fatal("should set mode to dashboard")
	}

	// From spawn mode: resets spawn form.
	m = newHubModel(nil, "http://hub.test")
	m.mode = hubModeSpawn
	m.openSpawnForm()
	m.returnToDashboard()
	if m.mode != hubModeDashboard {
		t.Fatal("should set mode to dashboard from spawn")
	}
}

// TestCovMoveBrowsePage exercises page movement.
func TestCovMoveBrowsePage(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.scrollMode = true
	m.session.viewport.Height = 10
	m.session.viewport.SetContent(strings.Repeat("line\n", 50))
	m.session.viewport.GotoBottom()
	bottom := m.session.viewport.YOffset

	// Up.
	m.moveBrowsePage(-1)
	if m.session.viewport.YOffset >= bottom {
		t.Fatalf("page up offset = %d, want below bottom %d", m.session.viewport.YOffset, bottom)
	}
	// Down.
	m.moveBrowsePage(1)
	if m.session.viewport.YOffset != bottom {
		t.Fatalf("page down offset = %d, want bottom %d", m.session.viewport.YOffset, bottom)
	}

	// With zero viewport height.
	m.session.viewport.Height = 0
	m.moveBrowsePage(-1)
	m.moveBrowsePage(1)
	if m.session.viewport.YOffset != bottom {
		t.Fatalf("zero-height page movement changed offset to %d, want %d", m.session.viewport.YOffset, bottom)
	}
}

// TestCovMoveBrowseSelection exercises selection movement.
func TestCovMoveBrowseSelection(t *testing.T) {
	m := newSessionHubModel(nil)
	m.width = 100
	m.height = 40
	m.session.messages = []transcript.ChatMessage{
		{Kind: transcript.MsgUser, Text: "first"},
		{Kind: transcript.MsgUser, Text: "second"},
		{Kind: transcript.MsgUser, Text: "third"},
	}
	m.session.scrollMode = true

	// Move up from the last message.
	m.browseSelected = 2
	m.moveBrowseSelection(-1)
	if m.browseSelected != 1 {
		t.Fatalf("selection after up = %d, want 1", m.browseSelected)
	}

	// Move back down.
	m.moveBrowseSelection(1)
	if m.browseSelected != 2 {
		t.Fatalf("selection after down = %d, want 2", m.browseSelected)
	}

	// Empty messages: resets selection.
	m.session.messages = nil
	m.moveBrowseSelection(1)
	if m.browseSelected != -1 {
		t.Fatal("should reset to -1 for empty messages")
	}
}

// TestCovSelectedBrowseMessage exercises selected message retrieval.
func TestCovSelectedBrowseMessage(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.messages = []transcript.ChatMessage{
		{Kind: transcript.MsgUser, Text: "hello"},
	}
	m.browseSelected = 0

	// Valid selection.
	idx, msg, ok := m.selectedBrowseMessage()
	if !ok || idx != 0 || msg.Text != "hello" {
		t.Fatalf("idx=%d msg=%+v ok=%v", idx, msg, ok)
	}

	// Out of range.
	m.browseSelected = 5
	_, _, ok = m.selectedBrowseMessage()
	if ok {
		t.Fatal("should return false for out-of-range")
	}

	// Negative.
	m.browseSelected = -1
	_, _, ok = m.selectedBrowseMessage()
	if ok {
		t.Fatal("should return false for negative")
	}
}

// TestCovToggleSelectedBrowseDetail exercises detail toggling.
func TestCovToggleSelectedBrowseDetail(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.messages = []transcript.ChatMessage{
		{Kind: transcript.MsgTool, Tool: &transcript.ToolCallInfo{Done: true, Expanded: false}},
	}
	m.browseSelected = 0
	m.toggleSelectedBrowseDetail()
	if !m.session.messages[0].Tool.Expanded {
		t.Fatal("should expand tool")
	}

	// Toggle back.
	m.toggleSelectedBrowseDetail()
	if m.session.messages[0].Tool.Expanded {
		t.Fatal("should collapse tool")
	}

	// Reasoning message.
	m.session.messages = []transcript.ChatMessage{
		{Kind: transcript.MsgReasoning, Done: true, Expanded: false},
	}
	m.browseSelected = 0
	m.toggleSelectedBrowseDetail()
	if !m.session.messages[0].Expanded {
		t.Fatal("should expand reasoning")
	}

	// No selection: no-op.
	m.browseSelected = -1
	m.toggleSelectedBrowseDetail()

	// Non-toggle-able message: no-op.
	m.session.messages = []transcript.ChatMessage{
		{Kind: transcript.MsgUser, Text: "hello"},
	}
	m.browseSelected = 0
	m.toggleSelectedBrowseDetail()
}

// TestCovStartForkDraft exercises fork draft creation.
func TestCovStartForkDraft(t *testing.T) {
	// No selection.
	m := newSessionHubModel(nil)
	m.startForkDraft()
	if m.forkDraft != nil {
		t.Fatal("should not create fork draft with no selection")
	}

	// Non-user message.
	m = newSessionHubModel(nil)
	m.session.messages = []transcript.ChatMessage{
		{Kind: transcript.MsgSystem, Text: "response"},
	}
	m.browseSelected = 0
	m.startForkDraft()
	if m.forkDraft != nil {
		t.Fatal("should not create fork draft for non-user message")
	}
	if got := m.session.messages[len(m.session.messages)-1].Text; got != "Select a user message to fork." {
		t.Fatalf("non-user refusal = %q", got)
	}

	// User message with no entry index.
	m = newSessionHubModel(nil)
	m.session.messages = []transcript.ChatMessage{
		{Kind: transcript.MsgUser, Text: "hello", TranscriptEntryIndex: 0},
	}
	m.browseSelected = 0
	m.startForkDraft()
	if m.forkDraft != nil {
		t.Fatal("should not create fork draft with no entry index")
	}

	// User message with entry index but fork not available.
	m = newSessionHubModel(nil)
	m.detail.Capabilities.Fork = false
	m.session.messages = []transcript.ChatMessage{
		{Kind: transcript.MsgUser, Text: "hello", TranscriptEntryIndex: 1},
	}
	m.browseSelected = 0
	m.startForkDraft()
	if m.forkDraft != nil {
		t.Fatal("should not create fork draft when fork not available")
	}

	// Valid fork draft.
	m = newSessionHubModel(nil)
	m.detail.Capabilities.Fork = true
	m.session.messages = []transcript.ChatMessage{
		{Kind: transcript.MsgUser, Text: "hello", TranscriptEntryIndex: 1},
	}
	m.browseSelected = 0
	m.startForkDraft()
	if m.forkDraft == nil {
		t.Fatal("should create fork draft")
	}
	if m.forkDraft.EntryIndex != 1 || m.forkDraft.OriginalText != "hello" {
		t.Fatalf("fork draft = %#v, want transcript entry 1 and original message", m.forkDraft)
	}
}

// TestCovRecordSessionError exercises error recording.
func TestCovRecordSessionError(t *testing.T) {
	m := hubModel{session: newModel(nil)}
	m.recordSessionError("test error")
	if m.sessionStatusError != "test error" {
		t.Fatalf("error = %q, want 'test error'", m.sessionStatusError)
	}
	if len(m.session.messages) == 0 {
		t.Fatal("should add system message")
	}

	// Empty: no-op.
	m = hubModel{session: newModel(nil)}
	m.recordSessionError("")
	if m.sessionStatusError != "" {
		t.Fatal("should not set error for empty text")
	}
}

// TestCovRemoveTrailingSessionSystem exercises trailing system removal.
func TestCovRemoveTrailingSessionSystem(t *testing.T) {
	m := hubModel{session: newModel(nil)}
	m.session.messages = []transcript.ChatMessage{
		{Kind: transcript.MsgUser, Text: "response"},
		{Kind: transcript.MsgSystem, Text: "temp"},
	}
	m.removeTrailingSessionSystem("temp")
	if len(m.session.messages) != 1 {
		t.Fatalf("len = %d, want 1", len(m.session.messages))
	}

	// Non-matching text: no-op.
	m = hubModel{session: newModel(nil)}
	m.session.messages = []transcript.ChatMessage{
		{Kind: transcript.MsgSystem, Text: "other"},
	}
	m.removeTrailingSessionSystem("temp")
	if len(m.session.messages) != 1 {
		t.Fatal("should not remove non-matching")
	}

	// Non-system message: no-op.
	m = hubModel{session: newModel(nil)}
	m.session.messages = []transcript.ChatMessage{
		{Kind: transcript.MsgUser, Text: "temp"},
	}
	m.removeTrailingSessionSystem("temp")
	if len(m.session.messages) != 1 {
		t.Fatal("should not remove non-system message")
	}

	// Empty messages: no-op.
	m = hubModel{session: newModel(nil)}
	m.removeTrailingSessionSystem("temp")
}

// TestCovAddSessionSystemOnce exercises deduplication.
func TestCovAddSessionSystemOnce(t *testing.T) {
	m := hubModel{session: newModel(nil)}
	m.addSessionSystemOnce("hello")
	if len(m.session.messages) != 1 {
		t.Fatalf("len = %d, want 1", len(m.session.messages))
	}

	// Same text: deduped.
	m.addSessionSystemOnce("hello")
	if len(m.session.messages) != 1 {
		t.Fatalf("len = %d, want 1 (deduped)", len(m.session.messages))
	}

	// Different text: added.
	m.addSessionSystemOnce("world")
	if len(m.session.messages) != 2 {
		t.Fatalf("len = %d, want 2", len(m.session.messages))
	}

	// Empty text: no-op.
	m.addSessionSystemOnce("")
	if len(m.session.messages) != 2 {
		t.Fatalf("len = %d, want 2 (empty no-op)", len(m.session.messages))
	}
}

// TestCovCurrentRef exercises ref parsing.
func TestCovCurrentRef(t *testing.T) {
	m := hubModel{detail: hubSessionDetail{Ref: "local:01TEST"}}
	ref, ok := m.currentRef()
	if !ok || ref.ThreadID != "01TEST" {
		t.Fatalf("ref = %+v, ok = %v", ref, ok)
	}

	// Invalid ref.
	m = hubModel{detail: hubSessionDetail{Ref: "invalid"}}
	_, ok = m.currentRef()
	if ok {
		t.Fatal("should return false for invalid ref")
	}
}

// TestCovMatchesAsyncSessionRef exercises async ref matching.
func TestCovMatchesAsyncSessionRef(t *testing.T) {
	m := hubModel{mode: hubModeSession, detail: hubSessionDetail{Ref: "local:01TEST"}}
	if !m.matchesAsyncSessionRef("local:01TEST") {
		t.Fatal("should match")
	}
	if m.matchesAsyncSessionRef("local:other") {
		t.Fatal("should not match other")
	}
	if m.matchesAsyncSessionRef("") {
		t.Fatal("should not match empty ref")
	}
	// Not in session mode.
	m.mode = hubModeDashboard
	if m.matchesAsyncSessionRef("local:01TEST") {
		t.Fatal("should not match when not in session mode")
	}
}

// --- hub_keys.go ---

// TestCovReplayKeyBurst exercises multi-rune replay.
func TestCovReplayKeyBurst(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeDashboard
	got, cmd := m.replayKeyBurst(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r', 'n'}})
	after := got.(hubModel)
	if after.mode != hubModeSpawn {
		t.Fatalf("mode = %v, want spawn after replayed n", after.mode)
	}
	if cmd != nil {
		t.Fatal("nil-client r+n burst should not return a command")
	}
}

// TestCovUpdateMouse exercises mouse handling.
func TestCovUpdateMouse(t *testing.T) {
	// Not in session mode: no-op.
	m := hubModel{}
	got, _ := m.updateMouse(tea.MouseMsg{})
	after := got.(hubModel)
	if after.mode != hubModeDashboard || after.session.scrollMode {
		t.Fatalf("non-session mouse changed model: mode=%v scroll=%v", after.mode, after.session.scrollMode)
	}

	// In session mode with scroll wheel.
	m = newSessionHubModel(nil)
	got, _ = m.updateMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	after = got.(hubModel)
	if !after.session.scrollMode {
		t.Fatal("session wheel should enter transcript browse mode")
	}
}

// TestCovMouseWheelScrollsTranscript exercises wheel detection.
func TestCovMouseWheelScrollsTranscript(t *testing.T) {
	if !mouseWheelScrollsTranscript(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp}) {
		t.Fatal("should detect wheel up")
	}
	if !mouseWheelScrollsTranscript(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown}) {
		t.Fatal("should detect wheel down")
	}
	if mouseWheelScrollsTranscript(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}) {
		t.Fatal("should not detect left button")
	}
	if mouseWheelScrollsTranscript(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonWheelUp}) {
		t.Fatal("should not detect release action")
	}
}

// TestCovUpdateHubFilterKey exercises filter key handling.
func TestCovUpdateHubFilterKey(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeDashboard
	m.enterHubFilter()

	// Esc: clears filter.
	got, _ := m.updateHubFilterKey(tea.KeyMsg{Type: tea.KeyEscape})
	after := got.(hubModel)
	if after.dashboardFilterActive {
		t.Fatal("should deactivate filter on esc")
	}

	// Enter: commits filter.
	m.enterHubFilter()
	got, _ = m.updateHubFilterKey(tea.KeyMsg{Type: tea.KeyEnter})
	after = got.(hubModel)
	if after.dashboardFilterActive {
		t.Fatal("should deactivate filter on enter")
	}

	// Typing.
	m.enterHubFilter()
	got, _ = m.updateHubFilterKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	after = got.(hubModel)
	if after.dashboardFilter.Value() != "x" {
		t.Fatalf("filter = %q, want 'x'", after.dashboardFilter.Value())
	}
}

// TestCovActivateDashboardRow exercises row activation.
func TestCovActivateDashboardRow(t *testing.T) {
	// Empty rows.
	m := hubModel{}
	got, _ := m.activateDashboardRow(nil)
	if got.(hubModel).mode != hubModeDashboard {
		t.Fatal("empty activation should preserve dashboard mode")
	}

	// Launch row.
	m = newHubModel(nil, "http://hub.test")
	rows := []hubRow{{kind: hubRowLaunch, title: "Launch"}}
	m.selected = 0
	got, _ = m.activateDashboardRow(rows)
	after := got.(hubModel)
	if after.mode != hubModeSpawn {
		t.Fatal("should open spawn form for launch row")
	}

	// Recent toggle row.
	m = newHubModel(nil, "http://hub.test")
	rows = []hubRow{{kind: hubRowRecentToggle, projectKey: "p1", groupKey: "g1"}}
	m.selected = 0
	got, _ = m.activateDashboardRow(rows)
	after = got.(hubModel)
	if !after.dashboardRecentOpen["g1"] {
		t.Fatal("recent toggle should open the selected group")
	}

	// Recent toggle with empty projectKey: no-op.
	m = newHubModel(nil, "http://hub.test")
	rows = []hubRow{{kind: hubRowRecentToggle, projectKey: ""}}
	m.selected = 0
	got, _ = m.activateDashboardRow(rows)
	if got.(hubModel).dashboardRecentOpen[""] {
		t.Fatal("empty recent project key should not mutate expansion state")
	}

	// Project row.
	m = newHubModel(nil, "http://hub.test")
	rows = []hubRow{{kind: hubRowProject, projectKey: "p1", groupKey: "g1"}}
	m.selected = 0
	got, _ = m.activateDashboardRow(rows)
	after = got.(hubModel)
	if !after.dashboardProjectClosed["g1"] {
		t.Fatal("project activation should collapse the selected group")
	}

	// Project row with empty key: no-op.
	m = newHubModel(nil, "http://hub.test")
	rows = []hubRow{{kind: hubRowProject, projectKey: ""}}
	m.selected = 0
	got, _ = m.activateDashboardRow(rows)
	if got.(hubModel).dashboardProjectClosed[""] {
		t.Fatal("empty project key should not mutate collapse state")
	}

	// Session row without client.
	m = hubModel{selected: 0, rows: []hubRow{{kind: hubRowSession, ref: appwire.Ref{SourceID: "local", ThreadID: "01TEST"}}}}
	got, _ = m.activateDashboardRow(m.rows)
	if got.(hubModel).mode != hubModeDashboard {
		t.Fatal("session activation without a client should preserve dashboard mode")
	}
}

// TestCovRunCommandPaletteCommand exercises command palette dispatch.
func TestCovRunCommandPaletteCommand(t *testing.T) {
	m := newSessionHubModel(nil)
	// Unknown command: no-op.
	got, _ := m.runCommandPaletteCommand("unknown")
	if len(got.(hubModel).session.messages) != 0 {
		t.Fatal("unknown command should not add a session message")
	}

	// Help command.
	got, _ = m.runCommandPaletteCommand("help")
	after := got.(hubModel)
	if len(after.session.messages) == 0 || !contains(after.session.messages[len(after.session.messages)-1].Text, "Available commands:") {
		t.Fatalf("help did not surface command help: %#v", after.session.messages)
	}
}

// --- hub_frames.go ---

// TestCovHubFrameFeedObserveNotification exercises notification observation.
func TestCovHubFrameFeedObserveNotification(t *testing.T) {
	f := newHubFrameFeed()
	n := appwire.Notification{Method: appwire.NotifyTurnStarted}
	f.Observe(appwire.Message{Notification: &n}, nil)
	if got, ok := takeHubNotification(f); !ok || got.Method != appwire.NotifyTurnStarted {
		t.Fatalf("notification = %#v, ok=%v", got, ok)
	}
}

// TestCovHubFrameFeedObserveError exercises error observation.
func TestCovHubFrameFeedObserveError(t *testing.T) {
	f := newHubFrameFeed()
	closed := false
	f.SetTransportCloser(func() error {
		closed = true
		return nil
	})
	n := appwire.Notification{Method: appwire.NotifyItemStarted}
	f.Observe(appwire.Message{Notification: &n}, nil)
	f.Observe(appwire.Message{}, errors.New("connection lost"))
	if !closed {
		t.Fatal("transport closer was not called when the feed ended")
	}
	if got, ok := takeHubNotification(f); !ok || got.Method != appwire.NotifyItemStarted {
		t.Fatalf("queued notification = %#v, ok=%v", got, ok)
	}
	if _, ok := takeHubNotification(f); ok {
		t.Fatal("notification channel should be closed after connection error")
	}
}

// TestCovHubFrameFeedBeginCapture exercises capture start.
func TestCovHubFrameFeedBeginCapture(t *testing.T) {
	f := newHubFrameFeed()
	c := f.BeginCapture()
	if c == nil {
		t.Fatal("should return capture")
	}

	n := appwire.Notification{Method: appwire.NotifyItemStarted}
	f.Observe(appwire.Message{Notification: &n}, nil)

	// Second capture supersedes the first and inherits its held frames.
	c2 := f.BeginCapture()
	if c2 == nil {
		t.Fatal("should return second capture")
	}
	frames := c2.BeforeCut()
	if len(frames) != 1 || frames[0].Method != appwire.NotifyItemStarted {
		t.Fatalf("inherited frames = %#v", frames)
	}
	if frames := c.BeforeCut(); frames != nil {
		t.Fatalf("superseded capture returned %#v", frames)
	}
	c2.Release()
}

// TestCovHubReadCaptureCutOn exercises cut ID setting.
func TestCovHubReadCaptureCutOn(t *testing.T) {
	f := newHubFrameFeed()
	c := f.BeginCapture()
	c.CutOn(appwire.NewIntID(42))
	before := appwire.Notification{Method: appwire.NotifyTurnStarted}
	after := appwire.Notification{Method: appwire.NotifyTurnCompleted}
	f.Observe(appwire.Message{Notification: &before}, nil)
	f.Observe(appwire.Message{Response: &appwire.Response{ID: appwire.NewIntID(7)}}, nil)
	f.Observe(appwire.Message{Response: &appwire.Response{ID: appwire.NewIntID(42)}}, nil)
	f.Observe(appwire.Message{Notification: &after}, nil)
	frames := c.BeforeCut()
	if len(frames) != 1 || frames[0].Method != appwire.NotifyTurnStarted {
		t.Fatalf("before-cut frames = %#v", frames)
	}
	c.Release()
	if got, ok := takeHubNotification(f); !ok || got.Method != appwire.NotifyTurnCompleted {
		t.Fatalf("released post-cut notification = %#v, ok=%v", got, ok)
	}
}

// TestCovHubReadCaptureBeforeCut exercises frame retrieval.
func TestCovHubReadCaptureBeforeCut(t *testing.T) {
	f := newHubFrameFeed()
	c := f.BeginCapture()
	if frames := c.BeforeCut(); frames != nil {
		t.Fatalf("got %+v, want nil for empty capture", frames)
	}
}

// TestCovHubReadCaptureRelease exercises release.
func TestCovHubReadCaptureRelease(t *testing.T) {
	f := newHubFrameFeed()
	c := f.BeginCapture()
	n := appwire.Notification{Method: appwire.NotifyItemCompleted}
	f.Observe(appwire.Message{Notification: &n}, nil)
	c.Release()
	if got, ok := takeHubNotification(f); !ok || got.Method != appwire.NotifyItemCompleted {
		t.Fatalf("released notification = %#v, ok=%v", got, ok)
	}
}

// TestCovHubReadCaptureAbandon exercises abandon.
func TestCovHubReadCaptureAbandon(t *testing.T) {
	f := newHubFrameFeed()
	c := f.BeginCapture()
	n := appwire.Notification{Method: appwire.NotifyItemStarted}
	f.Observe(appwire.Message{Notification: &n}, nil)
	c.Abandon()
	if got, ok := takeHubNotification(f); !ok || got.Method != appwire.NotifyItemStarted {
		t.Fatalf("abandoned notification = %#v, ok=%v", got, ok)
	}
}

// TestCovHubFrameFeedObserveWithCapture exercises observation while capturing.
func TestCovHubFrameFeedObserveWithCapture(t *testing.T) {
	f := newHubFrameFeed()
	c := f.BeginCapture()
	n := appwire.Notification{Method: appwire.NotifyTurnStarted}
	f.Observe(appwire.Message{Notification: &n}, nil)
	if _, ok := takeHubNotification(f); ok {
		t.Fatal("captured notification escaped before release")
	}
	c.Release()
	if got, ok := takeHubNotification(f); !ok || got.Method != appwire.NotifyTurnStarted {
		t.Fatalf("released captured notification = %#v, ok=%v", got, ok)
	}
}

func takeHubNotification(f *hubFrameFeed) (appwire.Notification, bool) {
	select {
	case notification, ok := <-f.Notifications():
		return notification, ok
	default:
		return appwire.Notification{}, false
	}
}

// --- hub_status.go ---

// TestCovCompactDuration exercises duration formatting.
func TestCovCompactDuration(t *testing.T) {
	if got := compactDuration(0); got != "1s" {
		t.Fatalf("got %q, want '1s'", got)
	}
	if got := compactDuration(30 * 1e9); got != "30s" {
		t.Fatalf("got %q, want '30s'", got)
	}
	if got := compactDuration(90 * 1e9); got != "1m" {
		t.Fatalf("got %q, want '1m'", got)
	}
	if got := compactDuration(3900 * 1e9); got != "1h 5m" {
		t.Fatalf("got %q, want '1h 5m'", got)
	}
	// Negative: clamped to 0, which gives 1s (min 1 second).
	if got := compactDuration(-1); got != "1s" {
		t.Fatalf("got %q, want '1s'", got)
	}
}

// TestCovShortStatusJobID exercises job ID abbreviation.
func TestCovShortStatusJobID(t *testing.T) {
	// Short: returned as-is.
	if got := shortStatusJobID("short"); got != "short" {
		t.Fatalf("got %q, want 'short'", got)
	}

	// Job prefix: abbreviated.
	got := shortStatusJobID("job_02wMz5TxvEMzoJEDTDGOTil_000000000001")
	if got == "job_02wMz5TxvEMzoJEDTDGOTil_000000000001" {
		t.Fatal("should abbreviate job_ prefixed IDs")
	}

	// Long non-job: truncated to 8 chars.
	if got := shortStatusJobID("abcdefghijklmnop"); got != "abcdefgh" {
		t.Fatalf("got %q, want 'abcdefgh'", got)
	}
}

// TestCovAuthSummary exercises auth summary rendering.
func TestCovAuthSummary(t *testing.T) {
	// Not supported.
	auth := appwire.AuthStatusResponse{Provider: "openai", Supported: false}
	if got := authSummary(auth); got != "openai not supported" {
		t.Fatalf("got %q, want 'openai not supported'", got)
	}

	// Not signed in.
	auth = appwire.AuthStatusResponse{Provider: "openai", Supported: true, SignedIn: false}
	if got := authSummary(auth); got != "openai signed out" {
		t.Fatalf("got %q, want 'openai signed out'", got)
	}

	// Signed in, no source.
	auth = appwire.AuthStatusResponse{Provider: "openai", Supported: true, SignedIn: true}
	if got := authSummary(auth); got != "openai signed in" {
		t.Fatalf("got %q, want 'openai signed in'", got)
	}

	// With source.
	auth = appwire.AuthStatusResponse{Provider: "openai", Supported: true, SignedIn: true, ActiveSource: "oauth"}
	if got := authSummary(auth); got != "openai oauth" {
		t.Fatalf("got %q, want 'openai oauth'", got)
	}

	// With email.
	auth = appwire.AuthStatusResponse{Provider: "openai", Supported: true, SignedIn: true, ActiveSource: "oauth", Email: "user@test.com"}
	if got := authSummary(auth); got != "openai oauth user@test.com" {
		t.Fatalf("got %q, want 'openai oauth user@test.com'", got)
	}

	// With stored email (no email).
	auth = appwire.AuthStatusResponse{Provider: "openai", Supported: true, SignedIn: true, StoredEmail: "stored@test.com"}
	if got := authSummary(auth); got != "openai signed in stored@test.com" {
		t.Fatalf("got %q, want 'openai signed in stored@test.com'", got)
	}

	// No provider: defaults to "auth".
	auth = appwire.AuthStatusResponse{Supported: false}
	if got := authSummary(auth); got != "auth not supported" {
		t.Fatalf("got %q, want 'auth not supported'", got)
	}
}

// TestCovAuthProviderForStatus exercises provider selection.
func TestCovAuthProviderForStatus(t *testing.T) {
	// From profile.
	detail := hubSessionDetail{Profile: "anthropic"}
	if got := authProviderForStatus(detail); got != "anthropic" {
		t.Fatalf("got %q, want 'anthropic'", got)
	}

	// Default.
	detail = hubSessionDetail{}
	if got := authProviderForStatus(detail); got != "openai" {
		t.Fatalf("got %q, want 'openai'", got)
	}
}

// TestCovHubErrorReason exercises error reason extraction.
func TestCovHubErrorReason(t *testing.T) {
	// Nil.
	if got := hubErrorReason(nil); got != "" {
		t.Fatalf("got %q, want empty", got)
	}

	// Non-appwire error.
	if got := hubErrorReason(errors.New("some error")); got != "some error" {
		t.Fatalf("got %q, want 'some error'", got)
	}
}

// --- hub_types.go ---

// TestCovBanner exercises banner rendering.
func TestCovBanner(t *testing.T) {
	// With source.
	v := hubTranscriptViewState{Title: "My Transcript", Source: "evener"}
	if got := v.banner(); !contains(got, "My Transcript") || !contains(got, "evener") {
		t.Fatalf("got %q", got)
	}

	// Without source.
	v = hubTranscriptViewState{Title: "My Transcript"}
	if got := v.banner(); !contains(got, "My Transcript") || contains(got, "[") {
		t.Fatalf("got %q, should not contain brackets", got)
	}
}

// TestCovGitBranchFromThread exercises branch extraction.
func TestCovGitBranchFromThread(t *testing.T) {
	// Nil GitInfo.
	thread := appwire.Thread{}
	if got := gitBranchFromThread(thread); got != "" {
		t.Fatalf("got %q, want empty", got)
	}

	// With GitInfo.
	thread.GitInfo = &appwire.GitInfo{Branch: "main"}
	if got := gitBranchFromThread(thread); got != "main" {
		t.Fatalf("got %q, want 'main'", got)
	}
}

// TestCovRecentTurnErrors exercises error extraction.
func TestCovRecentTurnErrors(t *testing.T) {
	// No errors.
	thread := appwire.Thread{}
	if got := recentTurnErrors(thread); got != nil {
		t.Fatalf("got %+v, want nil", got)
	}

	// With errors.
	thread.Turns = []appwire.Turn{
		{ID: "turn1", Error: &appwire.TurnError{Message: "failed"}},
		{ID: "turn2", Error: &appwire.TurnError{Message: "also failed"}},
		{ID: "turn3"},
	}
	got := recentTurnErrors(thread)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}

	// Error with no message: skipped.
	thread.Turns = []appwire.Turn{
		{ID: "turn1", Error: &appwire.TurnError{}},
	}
	got = recentTurnErrors(thread)
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0 (no message)", len(got))
	}

	// More than 3: capped at 3.
	thread.Turns = []appwire.Turn{
		{ID: "t1", Error: &appwire.TurnError{Message: "a"}},
		{ID: "t2", Error: &appwire.TurnError{Message: "b"}},
		{ID: "t3", Error: &appwire.TurnError{Message: "c"}},
		{ID: "t4", Error: &appwire.TurnError{Message: "d"}},
	}
	got = recentTurnErrors(thread)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (capped)", len(got))
	}
}

// TestCovProjectNameFromCWD exercises project name extraction.
func TestCovProjectNameFromCWD(t *testing.T) {
	if got := projectNameFromCWD("/tmp/myproject"); got != "myproject" {
		t.Fatalf("got %q, want 'myproject'", got)
	}
	if got := projectNameFromCWD(""); got != "(no project)" {
		t.Fatalf("got %q, want '(no project)'", got)
	}
	if got := projectNameFromCWD("/"); got != "/" {
		t.Fatalf("got %q, want '/'", got)
	}
	if got := projectNameFromCWD("  /tmp/proj  "); got != "proj" {
		t.Fatalf("got %q, want 'proj'", got)
	}
}

// --- hub_transcripts.go ---

// TestCovHubTranscriptPickerItems exercises item building.
func TestCovHubTranscriptPickerItems(t *testing.T) {
	targets := []appwire.ThreadTranscriptTarget{
		{Ref: "local:01A", Title: "Session A", Source: "evener", Status: "active"},
		{Ref: "local:01B", Title: "Session B", Kind: "subagent", TurnsUsed: 5},
		{Ref: "", Title: "No Ref"}, // skipped
	}
	items := hubTranscriptPickerItems(targets)
	if len(items) != 2 {
		t.Fatalf("len = %d, want 2 (empty ref skipped)", len(items))
	}

	// With source and status.
	if !contains(items[0].Display, "evener") || !contains(items[0].Display, "active") {
		t.Fatalf("display = %q, want 'evener' and 'active'", items[0].Display)
	}

	// Subagent with turns.
	if !contains(items[1].Display, "5 turns") {
		t.Fatalf("display = %q, want '5 turns'", items[1].Display)
	}

	// Empty title: uses ref as display (with source label appended).
	targets = []appwire.ThreadTranscriptTarget{
		{Ref: "local:01C"},
	}
	items = hubTranscriptPickerItems(targets)
	if len(items) != 1 {
		t.Fatalf("len = %d, want 1", len(items))
	}
	if !contains(items[0].Display, "local:01C") {
		t.Fatalf("display = %q, want to contain 'local:01C'", items[0].Display)
	}
}

// TestCovTranscriptTargetSourceLabel exercises source label.
func TestCovTranscriptTargetSourceLabel(t *testing.T) {
	// From source.
	target := appwire.ThreadTranscriptTarget{Source: "custom"}
	if got := transcriptTargetSourceLabel(target); got != "custom" {
		t.Fatalf("got %q, want 'custom'", got)
	}

	// From ref.
	target = appwire.ThreadTranscriptTarget{Ref: "local:01TEST"}
	if got := transcriptTargetSourceLabel(target); got != "evener" {
		t.Fatalf("got %q, want 'evener'", got)
	}

	// Empty.
	target = appwire.ThreadTranscriptTarget{}
	if got := transcriptTargetSourceLabel(target); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// TestCovHubTranscriptTargetByRef exercises target lookup.
func TestCovHubTranscriptTargetByRef(t *testing.T) {
	targets := []appwire.ThreadTranscriptTarget{
		{Ref: "local:01A", Title: "A"},
		{Ref: "local:01B", Title: "B"},
	}
	target, ok := hubTranscriptTargetByRef(targets, "local:01A")
	if !ok || target.Title != "A" {
		t.Fatalf("target = %+v, ok = %v", target, ok)
	}

	_, ok = hubTranscriptTargetByRef(targets, "local:unknown")
	if ok {
		t.Fatal("should return false for unknown ref")
	}
}

// --- hub_command_registry.go ---

// TestCovFetchCurrentHubSession exercises session fetch.
func TestCovFetchCurrentHubSession(t *testing.T) {
	// Invalid ref.
	m := hubModel{session: newModel(nil)}
	if cmd := fetchCurrentHubSession(&m, ""); cmd != nil {
		t.Fatal("should return nil for invalid ref")
	}
	if len(m.session.messages) == 0 {
		t.Fatal("should add system message for invalid ref")
	}

	// Valid ref.
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: params.Ref}}}, nil
		})
	})
	defer cleanup()
	m = newSessionHubModel(client)
	m.frames = newHubFrameFeed()
	cmd := fetchCurrentHubSession(&m, "")
	if cmd == nil {
		t.Fatal("should produce a cmd for valid ref")
	}
	msg, ok := cmd().(hubSessionMsg)
	if !ok || msg.err != nil || msg.ref != m.detail.Ref || msg.capture == nil {
		t.Fatalf("current session result = %#v", msg)
	}
	msg.capture.Release()
}

// TestCovFetchCurrentHubStatus exercises status fetch.
func TestCovFetchCurrentHubStatus(t *testing.T) {
	// Invalid ref.
	m := hubModel{session: newModel(nil)}
	if cmd := fetchCurrentHubStatus(&m, ""); cmd != nil {
		t.Fatal("should return nil for invalid ref")
	}

	// Valid ref.
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: params.Ref}}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerTasksList, func(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
			return appwire.TaskListResponse{}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodEvenerAuthStatus, func(context.Context, appwire.AuthStatusParams) (appwire.AuthStatusResponse, error) {
			return appwire.AuthStatusResponse{Provider: "openai", Supported: true}, nil
		})
	})
	defer cleanup()
	m = newSessionHubModel(client)
	cmd := fetchCurrentHubStatus(&m, "")
	if cmd == nil {
		t.Fatal("should produce a cmd for valid ref")
	}
	msg, ok := cmd().(hubStatusMsg)
	if !ok || msg.err != nil || msg.taskErr != nil || msg.authErr != nil || !msg.auth.Supported {
		t.Fatalf("current status result = %#v", msg)
	}
}

// TestCovRunHubCommandDefinition exercises command execution.
func TestCovRunHubCommandDefinition(t *testing.T) {
	m := newSessionHubModel(nil)

	// Help command: adds system message, returns nil.
	def, ok := hubCommandByName("help")
	if !ok {
		t.Fatal("help command should exist")
	}
	cmd := runHubCommandDefinition(&m, def, "")
	if cmd != nil {
		t.Fatal("help should return nil cmd")
	}
	if len(m.session.messages) == 0 {
		t.Fatal("help should add system message")
	}

	// Command with nil Run: returns nil.
	def2 := hubCommandDefinition{Name: "test", Run: nil}
	cmd = runHubCommandDefinition(&m, def2, "")
	if cmd != nil {
		t.Fatal("nil Run should return nil cmd")
	}
}

// --- hub_view_layout.go ---

// TestCovTruncateSessionLine exercises line truncation.
func TestCovTruncateSessionLine(t *testing.T) {
	// Normal truncation.
	long := "this is a very long line that exceeds the width"
	got := truncateSessionLine(long, 10)
	if len([]rune(got)) > 10 {
		t.Fatalf("len = %d, want <= 10", len([]rune(got)))
	}

	// Zero width: no truncation.
	got = truncateSessionLine("hello", 0)
	if got != "hello" {
		t.Fatalf("got %q, want 'hello'", got)
	}

	// Negative width: no truncation.
	got = truncateSessionLine("hello", -1)
	if got != "hello" {
		t.Fatalf("got %q, want 'hello'", got)
	}
}

// --- hub_update.go / hub_model.go / hub_reconnect.go ---

// TestCovHubModelInit exercises Init.
func TestCovHubModelInit(t *testing.T) {
	// No client: nil cmd.
	m := hubModel{}
	if cmd := m.Init(); cmd != nil {
		t.Fatal("Init should return nil without client")
	}

	// With client: batch cmd.
	client, cleanup := newTestHubClient(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadList, func(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
			return appwire.ThreadListResponse{}, nil
		})
	})
	defer cleanup()
	m = newHubModel(client, "http://hub.test")
	m.frames = newHubFrameFeed()
	notification := appwire.Notification{Method: appwire.NotifyTurnStarted}
	m.frames.Observe(appwire.Message{Notification: &notification}, nil)
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init should return cmd with client")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("Init result = %#v, want two-command batch", batch)
	}
	seenTree, seenNotification := false, false
	for _, command := range batch {
		switch msg := command().(type) {
		case hubTreeMsg:
			if msg.err != nil {
				t.Fatalf("tree fetch failed: %v", msg.err)
			}
			seenTree = true
		case hubNotificationMsg:
			if !msg.ok || msg.notification.Method != appwire.NotifyTurnStarted {
				t.Fatalf("notification result = %#v", msg)
			}
			seenNotification = true
		default:
			t.Fatalf("Init command returned %T", msg)
		}
	}
	if !seenTree || !seenNotification {
		t.Fatalf("Init outcomes: tree=%v notification=%v", seenTree, seenNotification)
	}
}

// TestCovReconnectHub exercises reconnect.
func TestCovReconnectHub(t *testing.T) {
	// Zero delay: immediate.
	cmd := reconnectHub(func(ctx context.Context) (*appwire.Client, *hubFrameFeed, error) {
		return nil, nil, nil
	}, 1, 0)
	if cmd == nil {
		t.Fatal("should produce a cmd")
	}
	msg, ok := cmd().(hubReconnectMsg)
	if !ok || msg.err != nil || msg.attempt != 1 || msg.client != nil || msg.frames != nil {
		t.Fatalf("reconnect result = %#v", msg)
	}
}

// TestCovHubReconnectDelay exercises delay calculation.
func TestCovHubReconnectDelay(t *testing.T) {
	if got := hubReconnectDelay(1); got != 0 {
		t.Fatalf("delay(1) = %v, want 0", got)
	}
	if got := hubReconnectDelay(2); got == 0 {
		t.Fatal("delay(2) should be non-zero")
	}
	// Capped at max.
	if got := hubReconnectDelay(20); got != hubReconnectMaxDelay {
		t.Fatalf("delay(20) = %v, want %v", got, hubReconnectMaxDelay)
	}
}

// --- hub_auth.go ---

// TestCovAuthProviderArg exercises auth provider arg parsing.
func TestCovAuthProviderArg(t *testing.T) {
	if got := authProviderArg(""); got != "openai" {
		t.Fatalf("got %q, want 'openai'", got)
	}
	if got := authProviderArg("anthropic"); got != "anthropic" {
		t.Fatalf("got %q, want 'anthropic'", got)
	}
	if got := authProviderArg("  anthropic  "); got != "anthropic" {
		t.Fatalf("got %q, want 'anthropic'", got)
	}
}

// --- hub_session_view.go ---

// TestCovSessionAuthReadinessLabel exercises auth readiness label.
func TestCovSessionAuthReadinessLabel(t *testing.T) {
	// authStatusSeen with provider.
	m := hubModel{authStatusSeen: true, authStatus: authStatus{Provider: "openai", ActiveSource: "oauth"}}
	if got := m.sessionAuthReadinessLabel(); !contains(got, "openai") || !contains(got, "oauth") {
		t.Fatalf("got %q, want 'openai' and 'oauth'", got)
	}

	// authStatusSeen with signed-out.
	m = hubModel{authStatusSeen: true, authStatus: authStatus{Provider: "openai", ActiveSource: "signed-out"}}
	if got := m.sessionAuthReadinessLabel(); !contains(got, "signed out") {
		t.Fatalf("got %q, want 'signed out'", got)
	}

	// authStatusSeen with empty source.
	m = hubModel{authStatusSeen: true, authStatus: authStatus{Provider: "openai"}}
	if got := m.sessionAuthReadinessLabel(); !contains(got, "unknown") {
		t.Fatalf("got %q, want 'unknown'", got)
	}

	// Not seen, with profile.
	m = hubModel{authStatusSeen: false, detail: hubSessionDetail{Profile: "anthropic"}}
	if got := m.sessionAuthReadinessLabel(); !contains(got, "anthropic") {
		t.Fatalf("got %q, want 'anthropic'", got)
	}

	// Not seen, with model provider.
	m = hubModel{authStatusSeen: false, detail: hubSessionDetail{Model: "anthropic/claude"}}
	if got := m.sessionAuthReadinessLabel(); !contains(got, "anthropic") {
		t.Fatalf("got %q, want 'anthropic'", got)
	}

	// Not seen, nothing: unknown.
	m = hubModel{authStatusSeen: false}
	if got := m.sessionAuthReadinessLabel(); !contains(got, "unknown") {
		t.Fatalf("got %q, want 'unknown'", got)
	}
}

// TestCovSessionPanelOverlay exercises panel overlay rendering.
func TestCovSessionPanelOverlay(t *testing.T) {
	// Nil panel.
	m := hubModel{}
	if got := m.sessionPanelOverlay(); got != "" {
		t.Fatalf("got %q, want empty for nil panel", got)
	}

	// With panel.
	m = hubModel{sessionPanel: &hubSessionPanel{Body: "test content"}}
	m.width = 80
	if got := m.sessionPanelOverlay(); got == "" {
		t.Fatal("should produce overlay for non-nil panel")
	}
}

// --- hub_attachments.go ---

// TestCovAddPendingAttachment exercises adding attachments.
func TestCovAddPendingAttachment(t *testing.T) {
	m := hubModel{session: newModel(nil)}

	// Nil: no-op.
	m.addPendingAttachment(nil)
	if len(m.pendingAttachments) != 0 {
		t.Fatal("should not add nil attachment")
	}

	// Valid attachment.
	m.addPendingAttachment(&clipboard.PastedImage{Path: "/tmp/img.png"})
	if len(m.pendingAttachments) != 1 {
		t.Fatalf("len = %d, want 1", len(m.pendingAttachments))
	}
	if m.pendingAttachments[0].MarkerN != 1 {
		t.Fatalf("MarkerN = %d, want 1", m.pendingAttachments[0].MarkerN)
	}

	// Second attachment increments marker.
	m.addPendingAttachment(&clipboard.PastedImage{Path: "/tmp/img2.png"})
	if m.pendingAttachments[1].MarkerN != 2 {
		t.Fatalf("MarkerN = %d, want 2", m.pendingAttachments[1].MarkerN)
	}
}

// TestCovClearSubmittedAttachments exercises clearing.
func TestCovClearSubmittedAttachments(t *testing.T) {
	m := hubModel{session: newModel(nil)}
	img1 := &clipboard.PastedImage{Path: "/tmp/img1.png"}
	img2 := &clipboard.PastedImage{Path: "/tmp/img2.png"}
	m.addPendingAttachment(img1)
	m.addPendingAttachment(img2)

	// Clear with no submitted: no-op.
	m.clearSubmittedAttachments(nil, true)
	if len(m.pendingAttachments) != 2 {
		t.Fatal("should not change for nil submitted")
	}

	// Clear submitted[0].
	m.clearSubmittedAttachments([]*clipboard.PastedImage{img1}, false)
	if len(m.pendingAttachments) != 1 {
		t.Fatalf("len = %d, want 1", len(m.pendingAttachments))
	}
}

// TestCovRestoreSubmittedAttachments exercises restoring.
func TestCovRestoreSubmittedAttachments(t *testing.T) {
	m := hubModel{session: newModel(nil)}
	img1 := &clipboard.PastedImage{Path: "/tmp/img1.png", MarkerN: 5}

	// Empty: no-op.
	m.restoreSubmittedAttachments(nil)
	if len(m.pendingAttachments) != 0 {
		t.Fatal("should not change for nil submitted")
	}

	// Restore.
	m.restoreSubmittedAttachments([]*clipboard.PastedImage{img1})
	if len(m.pendingAttachments) != 1 {
		t.Fatalf("len = %d, want 1", len(m.pendingAttachments))
	}
	if m.nextAttachmentMarker < 5 {
		t.Fatalf("nextAttachmentMarker = %d, want >= 5", m.nextAttachmentMarker)
	}

	// Restore already-present: deduped.
	m.restoreSubmittedAttachments([]*clipboard.PastedImage{img1})
	if len(m.pendingAttachments) != 1 {
		t.Fatalf("len = %d, want 1 (deduped)", len(m.pendingAttachments))
	}
}

// TestCovNoteUnrestoredFailedComposerPayload exercises notification.
func TestCovNoteUnrestoredFailedComposerPayload(t *testing.T) {
	m := hubModel{session: newModel(nil)}

	// With draft text.
	m.noteUnrestoredFailedComposerPayload("send", "failed draft", nil)
	if len(m.session.messages) == 0 {
		t.Fatal("should add system message")
	}

	// With only images.
	m.session.messages = nil
	m.noteUnrestoredFailedComposerPayload("send", "", []*clipboard.PastedImage{{Path: "/tmp/x"}})
	if len(m.session.messages) == 0 {
		t.Fatal("should add system message for images")
	}

	// With multiple images.
	m.session.messages = nil
	m.noteUnrestoredFailedComposerPayload("send", "", []*clipboard.PastedImage{{Path: "/x"}, {Path: "/y"}})
	if len(m.session.messages) == 0 {
		t.Fatal("should add system message for multiple images")
	}

	// Nothing: no-op.
	m.session.messages = nil
	m.noteUnrestoredFailedComposerPayload("send", "", nil)
	if len(m.session.messages) != 0 {
		t.Fatal("should not add message for nothing")
	}
}

// TestCovFinishAttachmentSubmit exercises submit finishing.
func TestCovFinishAttachmentSubmit(t *testing.T) {
	m := hubModel{}

	// No in-flight: just decrements to 0.
	m.finishAttachmentSubmit()
	if m.attachmentSubmitsInFlight != 0 {
		t.Fatalf("inFlight = %d, want 0", m.attachmentSubmitsInFlight)
	}

	// With in-flight: decrements.
	m.attachmentSubmitsInFlight = 2
	m.finishAttachmentSubmit()
	if m.attachmentSubmitsInFlight != 1 {
		t.Fatalf("inFlight = %d, want 1", m.attachmentSubmitsInFlight)
	}

	// Last one: cleans up deferred.
	m.attachmentSubmitsInFlight = 1
	m.deferredAttachmentCleanup = []*clipboard.PastedImage{{Path: "/tmp/deferred.png", Origin: "clipboard-image"}}
	m.finishAttachmentSubmit()
	if m.attachmentSubmitsInFlight != 0 {
		t.Fatalf("inFlight = %d, want 0", m.attachmentSubmitsInFlight)
	}
}

// TestCovClearPendingAttachments exercises clearing all pending.
func TestCovClearPendingAttachments(t *testing.T) {
	m := hubModel{session: newModel(nil)}
	m.addPendingAttachment(&clipboard.PastedImage{Path: "/tmp/img.png"})

	// Without cleanup.
	m.clearPendingAttachments(false)
	if len(m.pendingAttachments) != 0 {
		t.Fatal("should clear pending")
	}
	if m.nextAttachmentMarker != 0 {
		t.Fatalf("marker = %d, want 0", m.nextAttachmentMarker)
	}
}

// --- hub_session_keys.go ---

// TestCovRestoreInstructionMessage exercises message restoration.
func TestCovRestoreInstructionMessage(t *testing.T) {
	// With hubURL and ref.
	m := hubModel{hubURL: "http://hub.test", detail: hubSessionDetail{Ref: "local:01TEST"}}
	got := m.restoreInstructionMessage()
	if !contains(got, "http://hub.test") || !contains(got, "local:01TEST") {
		t.Fatalf("got %q", got)
	}

	// No hubURL: uses default.
	m = hubModel{detail: hubSessionDetail{Ref: "local:01TEST"}}
	got = m.restoreInstructionMessage()
	if !contains(got, "local:01TEST") {
		t.Fatalf("got %q", got)
	}

	// No ref, with session ID.
	m = hubModel{hubURL: "http://hub.test", session: newModel(nil)}
	m.session.sessionID = "01TEST"
	got = m.restoreInstructionMessage()
	if !contains(got, "01TEST") {
		t.Fatalf("got %q", got)
	}

	// Nothing: empty.
	m = hubModel{}
	got = m.restoreInstructionMessage()
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// ensure imports used
var _ = errors.New
