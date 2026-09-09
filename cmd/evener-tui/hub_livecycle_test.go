package tui

// Tests for the alt+shift+right / alt+shift+left session-view bindings that
// switch the viewed session to the next/previous LIVE session in dashboard
// order (the web shell's session.liveNext/session.livePrevious counterpart,
// same chords). The selection walks buildDashboardRows(m.tree) filtered to
// live session rows; the switch reuses fetchHubSession's thread/read path.

import (
	"context"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/clipboard"
	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
	"primeradiant.com/evener/internal/appserver"
)

// liveCycleTree holds three live sessions in one project (A, B, C, sorted by
// UpdatedAt desc) plus one non-live session (X) that must never be a cycling
// target.
func liveCycleTree() hubTreeResponse {
	return hubTreeResponse{Projects: []hubTreeProject{{
		Key:  "p1",
		Name: "One",
		Sessions: []hubTreeNode{
			{Ref: "local:01A", SessionID: "sess_a", Title: "A", State: "active", Live: true, UpdatedAt: 300},
			{Ref: "local:01B", SessionID: "sess_b", Title: "B", State: "active", Live: true, UpdatedAt: 200},
			{Ref: "local:01C", SessionID: "sess_c", Title: "C", State: "active", Live: true, UpdatedAt: 100},
			{Ref: "local:01X", SessionID: "sess_x", Title: "X", State: "ended", Live: false, UpdatedAt: 50},
		},
	}}}
}

type liveCycleReads struct {
	mu   sync.Mutex
	refs []string
	// replace marks each recorded read's ReplaceSubscription flag, parallel to
	// refs: a cycling/resub read replaces the connection's subscriptions; a
	// child-activity subscription is additive.
	replace []bool
}

func (r *liveCycleReads) record(params appwire.ThreadReadParams) appwire.ThreadReadResponse {
	r.mu.Lock()
	r.refs = append(r.refs, params.Ref)
	r.replace = append(r.replace, params.ReplaceSubscription)
	r.mu.Unlock()
	return appwire.ThreadReadResponse{Thread: responseOnlyHubThread(params.Ref)}
}

func (r *liveCycleReads) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.refs...)
}

func (r *liveCycleReads) replaces() []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bool(nil), r.replace...)
}

func newLiveCycleModel(t *testing.T, currentRef string, tree hubTreeResponse) (hubModel, *liveCycleReads, func()) {
	t.Helper()
	reads := &liveCycleReads{}
	client, feed, cleanup := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return reads.record(params), nil
		})
	})
	m := newHubModel(client, "")
	m.frames = feed
	m.mode = hubModeSession
	m.detail = hubSessionDetail{Ref: currentRef, SessionID: "sess_current"}
	m.tree = tree
	return m, reads, cleanup
}

// pressLiveCycleKey drives one key through updateSessionKey and synchronously
// runs the command it returned, returning the resulting hubSessionMsg.
func pressLiveCycleKey(t *testing.T, m hubModel, msg tea.KeyMsg) hubSessionMsg {
	t.Helper()
	_, cmd := m.updateSessionKey(msg)
	if cmd == nil {
		t.Fatal("expected a session-fetch command, got nil")
	}
	result, ok := cmd().(hubSessionMsg)
	if !ok {
		t.Fatalf("command result = %T, want hubSessionMsg", cmd())
	}
	if result.err != nil {
		t.Fatalf("session fetch err = %v", result.err)
	}
	if result.capture != nil {
		result.capture.Release()
	}
	return result
}

func TestHubSessionAltShiftRightSwitchesToNextLiveSession(t *testing.T) {
	m, reads, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	msg := pressLiveCycleKey(t, m, tea.KeyMsg{Type: tea.KeyShiftRight, Alt: true})
	if msg.ref != "local:01C" {
		t.Fatalf("switched to ref %q, want local:01C", msg.ref)
	}
	if got := reads.get(); len(got) != 1 || got[0] != "local:01C" {
		t.Fatalf("thread/read refs = %v, want [local:01C]", got)
	}
}

func TestHubSessionAltShiftLeftSwitchesToPreviousLiveSession(t *testing.T) {
	m, _, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	msg := pressLiveCycleKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft, Alt: true})
	if msg.ref != "local:01A" {
		t.Fatalf("switched to ref %q, want local:01A", msg.ref)
	}
}

func TestHubSessionLiveCycleWrapsBothEnds(t *testing.T) {
	m, _, cleanup := newLiveCycleModel(t, "local:01C", liveCycleTree())
	defer cleanup()

	if msg := pressLiveCycleKey(t, m, tea.KeyMsg{Type: tea.KeyShiftRight, Alt: true}); msg.ref != "local:01A" {
		t.Fatalf("next from last live session = %q, want wrap to local:01A", msg.ref)
	}
	if msg := pressLiveCycleKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft, Alt: true}); msg.ref != "local:01B" {
		t.Fatalf("previous from local:01C = %q, want local:01B", msg.ref)
	}

	mFirst, _, cleanupFirst := newLiveCycleModel(t, "local:01A", liveCycleTree())
	defer cleanupFirst()
	if msg := pressLiveCycleKey(t, mFirst, tea.KeyMsg{Type: tea.KeyShiftLeft, Alt: true}); msg.ref != "local:01C" {
		t.Fatalf("previous from first live session = %q, want wrap to local:01C", msg.ref)
	}
}

func TestHubSessionLiveCycleFromNonLiveSessionLandsOnAnEnd(t *testing.T) {
	m, _, cleanup := newLiveCycleModel(t, "local:01X", liveCycleTree())
	defer cleanup()

	if msg := pressLiveCycleKey(t, m, tea.KeyMsg{Type: tea.KeyShiftRight, Alt: true}); msg.ref != "local:01A" {
		t.Fatalf("next from a non-live session = %q, want first live local:01A", msg.ref)
	}
	if msg := pressLiveCycleKey(t, m, tea.KeyMsg{Type: tea.KeyShiftLeft, Alt: true}); msg.ref != "local:01C" {
		t.Fatalf("previous from a non-live session = %q, want last live local:01C", msg.ref)
	}
}

func TestHubSessionLiveCycleNoOpWithSingleLiveSession(t *testing.T) {
	tree := liveCycleTree()
	tree.Projects[0].Sessions = tree.Projects[0].Sessions[:1]
	m, reads, cleanup := newLiveCycleModel(t, "local:01A", tree)
	defer cleanup()

	_, cmd := m.updateSessionKey(tea.KeyMsg{Type: tea.KeyShiftRight, Alt: true})
	if cmd != nil {
		t.Fatalf("single live session: expected nil command, got one")
	}
	if got := reads.get(); len(got) != 0 {
		t.Fatalf("single live session issued thread/read for %v", got)
	}
}

func TestHubSessionLiveCycleNoOpWithoutTree(t *testing.T) {
	m, reads, cleanup := newLiveCycleModel(t, "local:01B", hubTreeResponse{})
	defer cleanup()

	_, cmd := m.updateSessionKey(tea.KeyMsg{Type: tea.KeyShiftRight, Alt: true})
	if cmd != nil {
		t.Fatal("empty tree: expected nil command, got one")
	}
	if got := reads.get(); len(got) != 0 {
		t.Fatalf("empty tree issued thread/read for %v", got)
	}
}

func TestHubCommandRegistryExposesLiveSessionCycling(t *testing.T) {
	for _, name := range []string{"next-live-session", "previous-live-session"} {
		cmd, ok := hubCommandByName(name)
		if !ok {
			t.Fatalf("no /%s command in the registry", name)
		}
		if cmd.Scopes&hubCommandSession == 0 {
			t.Fatalf("/%s must be session-scoped", name)
		}
		available, reason := hubCommandAvailable(cmd, hubCommandContext{mode: hubModeSession})
		if !available {
			t.Fatalf("/%s unavailable in session mode: %s", name, reason)
		}
	}
}

// Switching sessions is asynchronous: until the read lands, m.detail.Ref
// still names the OLD session. A second press before then must step from
// the pending target, not the stale ref, or rapid presses collapse onto one
// session (roborev PR #1044 finding 2).
func TestHubSessionLiveCycleBasesRepeatedPressesOnPendingTarget(t *testing.T) {
	m, reads, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	m1, cmd1 := m.switchToAdjacentLiveSession(1)
	if cmd1 == nil {
		t.Fatal("first press: expected a fetch command")
	}
	m2, cmd2 := m1.switchToAdjacentLiveSession(1)
	if cmd2 == nil {
		t.Fatal("second press: expected a fetch command")
	}
	_ = m2
	cmd1()
	cmd2()
	if got := reads.get(); len(got) != 2 || got[0] != "local:01C" || got[1] != "local:01A" {
		t.Fatalf("thread/read refs = %v, want [local:01C local:01A] (second press based on the pending target)", got)
	}
}

// Two in-flight navigation reads can complete out of order. The stale one
// must be dropped, or the final session contradicts the key sequence.
func TestHubSessionLiveCycleDropsStaleRead(t *testing.T) {
	m, _, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	m1, cmd1 := m.switchToAdjacentLiveSession(1)   // next: 01C
	m2, cmd2 := m1.switchToAdjacentLiveSession(-1) // previous from pending 01C: 01B

	// The NEWER read lands first and applies.
	updated, _ := m2.Update(cmd2())
	m3 := updated.(hubModel)
	if m3.detail.Ref != "local:01B" {
		t.Fatalf("after the newer read, viewed ref = %q, want local:01B", m3.detail.Ref)
	}

	// The stale read arrives late; it must not move the view to 01C.
	updated, _ = m3.Update(cmd1())
	m4 := updated.(hubModel)
	if m4.detail.Ref != "local:01B" {
		t.Fatalf("stale live-nav read overwrote the newer session: viewed ref = %q, want local:01B", m4.detail.Ref)
	}
}

// A live-nav read in flight must not override a session the user opened by
// another path: the manual entry is the newer intent (roborev PR #1044
// round-2 medium 3).
func TestHubSessionLiveCycleManualEntryInvalidatesPendingNav(t *testing.T) {
	m, _, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	m1, cmd1 := m.switchToAdjacentLiveSession(1) // pending: 01C

	// The user opens another session directly (dashboard enter, palette pick -
	// any non-cycling entry read) while the cycling read is in flight.
	manual := hubSessionMsg{detail: hubDetailFromThread(responseOnlyHubThread("local:01X")), ref: "local:01X"}
	updated, _ := m1.Update(manual)
	m2 := updated.(hubModel)
	if m2.detail.Ref != "local:01X" {
		t.Fatalf("after manual entry, viewed ref = %q, want local:01X", m2.detail.Ref)
	}

	// The in-flight cycling read completes late; it must be dropped.
	updated, _ = m2.Update(cmd1())
	m3 := updated.(hubModel)
	if m3.detail.Ref != "local:01X" {
		t.Fatalf("live-nav read overrode the manual entry: viewed ref = %q, want local:01X", m3.detail.Ref)
	}

	// And the next cycling press steps from the viewed session, not the stale
	// pending ref.
	_, cmd := m3.switchToAdjacentLiveSession(1)
	if cmd == nil {
		t.Fatal("expected a fetch command after manual entry")
	}
	if msg := cmd().(hubSessionMsg); msg.ref != "local:01A" {
		t.Fatalf("press after manual entry targeted %q, want first live local:01A (01X is not live)", msg.ref)
	}
}

// Leaving session mode (ctrl+o to the dashboard) while a cycling read is in
// flight: the read's late response must not yank the view back into the
// fetched session (roborev PR #1044 round-3 medium 2).
func TestHubSessionLiveCycleDropsReadAfterModeExit(t *testing.T) {
	m, _, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	m1, cmd := m.switchToAdjacentLiveSession(1) // pending: 01C
	m1.returnToDashboard()
	if m1.mode != hubModeDashboard {
		t.Fatalf("mode = %v, want dashboard after returnToDashboard", m1.mode)
	}

	updated, _ := m1.Update(cmd())
	m2 := updated.(hubModel)
	if m2.mode != hubModeDashboard {
		t.Fatalf("in-flight live-nav read re-entered session mode after ctrl+o (viewing %q)", m2.detail.Ref)
	}
}

// The composer is a text surface: switching sessions mid-draft replaces
// m.session (input included) and discards an unsent prompt. The chords hold
// while a draft is present — the web side's allowInEditable:false policy
// mapped to the TUI (roborev PR #1044 round-4 medium 3).
func TestHubSessionLiveCycleSuppressedWithDraft(t *testing.T) {
	m, reads, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()
	m.session.input.SetValue("unsent draft")

	updated, cmd := m.updateSessionKey(tea.KeyMsg{Type: tea.KeyShiftRight, Alt: true})
	if cmd != nil {
		t.Fatal("expected no fetch command with a draft present")
	}
	m2 := updated.(hubModel)
	if got := m2.session.input.Value(); got != "unsent draft" {
		t.Fatalf("draft = %q, want preserved", got)
	}
	if got := reads.get(); len(got) != 0 {
		t.Fatalf("issued thread/read %v with a draft present", got)
	}
}

// Browse mode (esc) must not swallow the chords: unmodified arrows scroll and
// select there, but alt+shift+arrows still switch live sessions (roborev
// PR #1044 round-4 low).
func TestHubSessionLiveCycleWorksInBrowseMode(t *testing.T) {
	m, reads, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()
	m.enterSessionBrowse(false)

	_, cmd := m.updateSessionKey(tea.KeyMsg{Type: tea.KeyShiftRight, Alt: true})
	if cmd == nil {
		t.Fatal("browse mode: expected a fetch command")
	}
	msg := cmd().(hubSessionMsg)
	if msg.capture != nil {
		msg.capture.Release()
	}
	if msg.ref != "local:01C" {
		t.Fatalf("browse mode switched to %q, want local:01C", msg.ref)
	}
	if got := reads.get(); len(got) != 1 || got[0] != "local:01C" {
		t.Fatalf("thread/read refs = %v, want [local:01C]", got)
	}
}

// An attachment-only draft is as unsent as a text draft; the guard must hold
// for it too (roborev PR #1044 round-5 low).
func TestHubSessionLiveCycleSuppressedWithAttachmentOnly(t *testing.T) {
	m, reads, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()
	m.pendingAttachments = []*clipboard.PastedImage{{}}

	_, cmd := m.updateSessionKey(tea.KeyMsg{Type: tea.KeyShiftRight, Alt: true})
	if cmd != nil {
		t.Fatal("expected no fetch command with an attachment staged")
	}
	if got := reads.get(); len(got) != 0 {
		t.Fatalf("issued thread/read %v with an attachment staged", got)
	}
}

// Composing DURING the in-flight read: the guard checked at press time no
// longer covers the content, so application must re-check — the draft is
// newer intent than the navigation (roborev PR #1044 round-5 medium 2).
func TestHubSessionLiveCycleDropsReadWhenDraftAppearedMidFlight(t *testing.T) {
	m, reads, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	m1, cmd := m.switchToAdjacentLiveSession(1) // pending: 01C
	m1.session.input.SetValue("typed while the read was in flight")
	// A watched, still-running subagent child: the dropped read's server-side
	// subscription replacement culled it too, so the resub must restore it.
	m1.session.messages = append(m1.session.messages, transcript.ChatMessage{
		Kind: transcript.MsgTool,
		Tool: &transcript.ToolCallInfo{Subagent: &transcript.SubagentRunInfo{TranscriptRef: "local:01CHILD", Status: "running"}},
	})

	updated, resub := m1.Update(cmd())
	m2 := updated.(hubModel)
	if m2.detail.Ref != "local:01B" {
		t.Fatalf("live-nav read applied over a mid-flight draft: viewed ref = %q, want local:01B", m2.detail.Ref)
	}
	if got := m2.session.input.Value(); got != "typed while the read was in flight" {
		t.Fatalf("draft = %q, want preserved", got)
	}
	// Round 11, medium 2: the dropped read's ThreadRead still replaced the
	// connection's subscriptions server-side (main AND children), so the drop
	// must re-establish the displayed session's subscription and re-subscribe
	// the still-running child (roborev PR #1044 round-11 medium 2).
	if resub == nil {
		t.Fatal("draft drop returned no command: the displayed session's subscription was not re-established")
	}
	msg := resub()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("resub command result = %T, want tea.BatchMsg", msg)
	}
	if len(batch) < 2 {
		t.Fatalf("resub batch = %d cmds, want the session read plus the child re-subscription", len(batch))
	}
	// A BatchMsg holds the child commands; run each so its thread/read is
	// actually issued against the recorded fixture.
	for _, child := range batch {
		_ = child()
	}
	got, replaces := reads.get(), reads.replaces()
	// read 1: the cycling read (01C, replace). resub read 2: displayed 01B,
	// replace. child read 3: 01CHILD, additive.
	if len(got) < 3 || got[1] != "local:01B" || !replaces[1] {
		t.Fatalf("resub read missing: reads = %v replaces = %v, want a replacing read for local:01B", got, replaces)
	}
	if got[len(got)-1] != "local:01CHILD" || replaces[len(replaces)-1] {
		t.Fatalf("child re-subscription missing: reads = %v replaces = %v, want an additive read for local:01CHILD", got, replaces)
	}
}

// An overlay (palette, modal, panel) opened while the read was in flight is
// the same class of newer intent: applying the read would swap the session
// under the overlay, leaving it open against stale session context (roborev
// PR #1044 round-6 low).
func TestHubSessionLiveCycleDropsReadWhenOverlayOpenedMidFlight(t *testing.T) {
	m, reads, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	m1, cmd := m.switchToAdjacentLiveSession(1) // pending: 01C
	palette := newCommandPalette("Test", nil, 80)
	m1.commandPalette = &palette
	if got := topmostOverlayName(m1); got != "command-palette" {
		t.Fatalf("test setup invalid: topmostOverlayName = %q, want command-palette", got)
	}

	updated, resub := m1.Update(cmd())
	m2 := updated.(hubModel)
	if m2.detail.Ref != "local:01B" {
		t.Fatalf("live-nav read applied under an open overlay: viewed ref = %q, want local:01B", m2.detail.Ref)
	}
	if m2.commandPalette == nil {
		t.Fatal("expected the palette to remain open")
	}
	// Round 11, medium 2: same as the draft drop - the subscription for the
	// displayed session must be re-established after the read dropped.
	if resub == nil {
		t.Fatal("overlay drop returned no command: the displayed session's subscription was not re-established")
	}
	msg := resub()
	// No watched children in this fixture, so the batch may collapse to the
	// single session read.
	switch m := msg.(type) {
	case hubSessionMsg:
	case tea.BatchMsg:
		for _, child := range m {
			_ = child()
		}
	default:
		t.Fatalf("resub command result = %T, want hubSessionMsg or tea.BatchMsg", msg)
	}
	got, replaces := reads.get(), reads.replaces()
	if len(got) < 2 || got[1] != "local:01B" || !replaces[1] {
		t.Fatalf("resub read missing: reads = %v replaces = %v, want a replacing read for local:01B", got, replaces)
	}
}

// A pending live-nav target must not survive a dashboard round-trip: the
// mode-exit drop (ctrl+o while the read is in flight) returns without
// clearing it, and nothing on re-entry clears it either, so the next press
// steps from the stale target instead of the session actually displayed
// (roborev PR #1044 round-7 medium 1: B -> C fails, next press skips C).
func TestHubSessionLiveCycleClearsPendingRefAcrossDashboardRoundTrip(t *testing.T) {
	m, _, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	m1, cmd := m.switchToAdjacentLiveSession(1) // pending: 01C, read in flight
	m1.returnToDashboard()
	// The in-flight read lands and is dropped (mode exit). The pending ref
	// it keyed must be gone: the user re-entered the same session B.
	updated, _ := m1.Update(cmd())
	m2 := updated.(hubModel)
	if m2.mode != hubModeDashboard {
		t.Fatalf("mode = %v, want dashboard", m2.mode)
	}
	if m2.liveNavPendingRef != "" {
		t.Fatalf("liveNavPendingRef = %q after mode-exit drop, want cleared", m2.liveNavPendingRef)
	}

	// Re-enter session B (the dashboard's ordinary entry path), then press
	// next: it must step from B to C, not from the stale pending C to A.
	m2.mode = hubModeSession
	m2.detail.Ref = "local:01B"
	m3, cmd2 := m2.switchToAdjacentLiveSession(1)
	if cmd2 == nil {
		t.Fatal("expected a fetch command for the next live session")
	}
	updated2, _ := m3.Update(cmd2())
	m4 := updated2.(hubModel)
	if m4.detail.Ref != "local:01C" {
		t.Fatalf("next press after dashboard round-trip viewed %q, want local:01C (stepped from the displayed session)", m4.detail.Ref)
	}
}

// Round 8, medium 1: a STALE live-nav read (one a newer press superseded)
// must not clear the pending target the newer read still needs as its
// stepping base. Two presses in flight, the older lands first: the older is
// dropped WITHOUT clearing, so the newer's target survives (roborev PR #1044
// round-8 medium 1).
func TestHubSessionLiveCycleStaleReadKeepsNewerPendingTarget(t *testing.T) {
	m, _, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	m1, cmd1 := m.switchToAdjacentLiveSession(1)  // pending: 01C
	m2, cmd2 := m1.switchToAdjacentLiveSession(1) // pending: 01A (newer intent)

	// The STALE read (01C) lands first: dropped, and the newer pending (01A)
	// must survive it.
	updated, _ := m2.Update(cmd1())
	m3 := updated.(hubModel)
	if m3.detail.Ref != "local:01B" {
		t.Fatalf("stale read applied: viewed ref = %q, want local:01B", m3.detail.Ref)
	}
	if m3.liveNavPendingRef != "local:01A" {
		t.Fatalf("stale read cleared the newer pending target: liveNavPendingRef = %q, want local:01A", m3.liveNavPendingRef)
	}

	// The NEWER read (01A) then lands and applies.
	updated2, _ := m3.Update(cmd2())
	m4 := updated2.(hubModel)
	if m4.detail.Ref != "local:01A" {
		t.Fatalf("newer read did not apply: viewed ref = %q, want local:01A", m4.detail.Ref)
	}
	if m4.liveNavPendingRef != "" {
		t.Fatalf("liveNavPendingRef = %q after the newer read applied, want cleared", m4.liveNavPendingRef)
	}
}

// Round 10, medium: the stale-read drop's re-subscription must not fire
// while a NEWER live-nav read is still pending. The resub targets the session
// currently displayed - the OLDER one - so its untagged response would land
// after the newer read applied and be processed as an ordinary session entry,
// reverting the UI to the older session; its own ThreadRead also re-points
// the server-side subscription at the older session (roborev PR #1044 round-10
// medium). While a newer read is pending, that read owns the subscription
// when it lands, so the drop must return no command.
func TestHubSessionLiveCycleStaleReadDoesNotResubWhileNewerReadPending(t *testing.T) {
	m, reads, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	m1, cmd1 := m.switchToAdjacentLiveSession(1)  // pending: 01C
	m2, cmd2 := m1.switchToAdjacentLiveSession(1) // pending: 01A (newer, still in flight)

	// The STALE read (01C) lands while the newer one is pending: dropped,
	// and it must NOT launch a re-subscription read for the displayed 01B.
	updated, resub := m2.Update(cmd1())
	m3 := updated.(hubModel)
	if m3.detail.Ref != "local:01B" {
		t.Fatalf("stale read applied: viewed ref = %q, want local:01B", m3.detail.Ref)
	}
	if m3.liveNavPendingRef != "local:01A" {
		t.Fatalf("stale read cleared the newer pending target: liveNavPendingRef = %q, want local:01A", m3.liveNavPendingRef)
	}
	if resub != nil {
		msg := resub()
		if _, ok := msg.(hubSessionMsg); ok {
			t.Fatal("stale-read drop launched a re-subscription read while a newer live-nav read was pending")
		}
	}

	// The newer read (01A) lands and applies; the UI must stay on it - no
	// in-flight resub response can revert it.
	updated2, _ := m3.Update(cmd2())
	m4 := updated2.(hubModel)
	if m4.detail.Ref != "local:01A" {
		t.Fatalf("newer read did not apply: viewed ref = %q, want local:01A", m4.detail.Ref)
	}

	// Only the two cycling reads were issued: no resub read for 01B.
	got := reads.get()
	if len(got) != 2 {
		t.Fatalf("thread/read calls = %v, want exactly the two cycling reads (no resub while pending)", got)
	}
}

// Round 8, medium 2: every cycling read replaces the server-side
// subscription. Two rapid presses issue two ThreadReads; when the older
// lands AFTER the newer, its subscription replacement must not stand - the
// re-read of the now-current session re-issues it (roborev PR #1044 round-8
// medium 2).
func TestHubSessionLiveCycleStaleReadReEstablishesCurrentSubscription(t *testing.T) {
	m, reads, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	m1, cmd1 := m.switchToAdjacentLiveSession(1)  // pending: 01C
	m2, cmd2 := m1.switchToAdjacentLiveSession(1) // pending: 01A (applies)

	// The newer read lands first and applies.
	updated, _ := m2.Update(cmd2())
	m3 := updated.(hubModel)
	if m3.detail.Ref != "local:01A" {
		t.Fatalf("newer read did not apply: viewed ref = %q, want local:01A", m3.detail.Ref)
	}

	// The STALE read (01C, subscription-replacing) lands after: dropped for
	// the transcript, but its server-side subscription must be re-pointed at
	// the session now displayed.
	updated2, resub := m3.Update(cmd1())
	m4 := updated2.(hubModel)
	if m4.detail.Ref != "local:01A" {
		t.Fatalf("stale read applied over the newer: viewed ref = %q, want local:01A", m4.detail.Ref)
	}
	if resub == nil {
		t.Fatal("expected a re-subscription read for the displayed session after a stale read dropped")
	}
	// Running it re-issues thread/read for the session now displayed.
	msg := resub()
	if _, ok := msg.(hubSessionMsg); !ok {
		t.Fatalf("resub command result = %T, want hubSessionMsg", msg)
	}
	got := reads.get()
	if len(got) < 3 || got[len(got)-1] != "local:01A" {
		t.Fatalf("final thread/read = %v, want the last read re-targeting local:01A (subscription re-established)", got)
	}
}

// Round 8, medium 4: a live-nav read started on the pre-reconnect client
// must not overwrite the fresh connection's resynchronized session. The
// reconnect replaces client and frames; the stale read then lands and must
// be dropped (roborev PR #1044 round-8 medium 4).
func TestHubSessionLiveCycleDropsReadFromBeforeReconnect(t *testing.T) {
	m, _, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	m1, cmd := m.switchToAdjacentLiveSession(1) // pending: 01C on the OLD client
	// The read SUCCEEDS on the old connection and its message is in flight;
	// bubbletea can deliver it after the reconnect has already replaced the
	// client and frames.
	msg := cmd()
	if _, ok := msg.(hubSessionMsg); !ok {
		t.Fatalf("command result = %T, want hubSessionMsg", msg)
	}

	// A reconnect lands: applyHubReconnect replaces client and frames and
	// resynchronizes the viewed session.
	reconnected := applyHubReconnectOnCopy(t, m1)
	if reconnected.detail.Ref != "local:01B" {
		t.Fatalf("resync changed the viewed session: %q, want local:01B", reconnected.detail.Ref)
	}

	// The pre-reconnect read lands after the reconnect: it must be dropped.
	updated, _ := reconnected.Update(msg)
	m4 := updated.(hubModel)
	if m4.detail.Ref != "local:01B" {
		t.Fatalf("pre-reconnect live-nav read overwrote the resynchronized session: viewed ref = %q, want local:01B", m4.detail.Ref)
	}
	if m4.liveNavPendingRef != "" {
		t.Fatalf("liveNavPendingRef = %q after dropping a pre-reconnect read, want cleared", m4.liveNavPendingRef)
	}
}

// applyHubReconnectOnCopy drives a successful reconnect through the real
// applyHubReconnect path on a copy of the model: a fresh client/frames pair
// (the same feed's channel keeps the resync's read answerable), the viewed
// session resynchronized. The resync read is drained so the model is stable
// for assertions.
func applyHubReconnectOnCopy(t *testing.T, m hubModel) hubModel {
	t.Helper()
	_, feed, cleanup2 := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: responseOnlyHubThread(params.Ref)}, nil
		})
	})
	t.Cleanup(cleanup2)
	msg := hubReconnectMsg{client: m.client, frames: feed}
	cmd := m.applyHubReconnect(msg)
	// Drain the reconnect's commands (tree fetch, resync read) so their
	// messages cannot interleave with the test's own Update calls.
	if cmd != nil {
		_ = cmd()
	}
	return m
}
