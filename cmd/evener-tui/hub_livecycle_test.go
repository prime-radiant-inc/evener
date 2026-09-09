package tui

// Tests for the alt+shift+right / alt+shift+left session-view bindings that
// switch the viewed session to the next/previous LIVE session in dashboard
// order (the web shell's session.liveNext/session.livePrevious counterpart,
// same chords). The selection walks buildDashboardRows(m.tree) filtered to
// live session rows; the switch reuses fetchHubSession's thread/read path.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/clipboard"
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
	// threadByRef overrides the returned thread per ref: a test can hand a
	// read's response a running subagent child so the recovery response's
	// child re-arm has something to find.
	threadByRef map[string]appwire.Thread
	// failRef, when set, makes reads for that ref fail (round-13 medium 2's
	// bounded-retry coverage).
	failRef string
}

func (r *liveCycleReads) record(params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	r.mu.Lock()
	r.refs = append(r.refs, params.Ref)
	r.replace = append(r.replace, params.ReplaceSubscription)
	thread := responseOnlyHubThread(params.Ref)
	if override, ok := r.threadByRef[params.Ref]; ok {
		thread = override
	}
	fail := r.failRef != "" && r.failRef == params.Ref
	r.mu.Unlock()
	if fail {
		return appwire.ThreadReadResponse{}, errors.New("thread/read failed")
	}
	return appwire.ThreadReadResponse{Thread: thread}, nil
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
			return reads.record(params)
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
	// subscription replacement culled it too, so the recovery response must
	// restore it. The child rides the recovery read's own response thread —
	// the re-arm scans the refreshed transcript (round-12 medium 1).
	reads.threadByRef = map[string]appwire.Thread{"local:01B": threadWithRunningChild("local:01B")}

	updated, resub := m1.Update(cmd())
	m2 := updated.(hubModel)
	if m2.detail.Ref != "local:01B" {
		t.Fatalf("live-nav read applied over a mid-flight draft: viewed ref = %q, want local:01B", m2.detail.Ref)
	}
	if got := m2.session.input.Value(); got != "typed while the read was in flight" {
		t.Fatalf("draft = %q, want preserved", got)
	}
	// Round 11, medium 2 (re-scoped by round 12): the dropped read's
	// ThreadRead still replaced the connection's subscriptions server-side
	// (main AND children), so the drop must re-establish the displayed
	// session's subscription. The resub is a single tagged recovery read;
	// the still-running child re-arms from the recovery response, after the
	// replacement completed (round-12 medium 1).
	if resub == nil {
		t.Fatal("draft drop returned no command: the displayed session's subscription was not re-established")
	}
	msg := resub()
	sessionMsg, ok := msg.(hubSessionMsg)
	if !ok {
		t.Fatalf("resub command result = %T, want hubSessionMsg", msg)
	}
	if sessionMsg.ref != "local:01B" {
		t.Fatalf("resub read ref = %q, want local:01B", sessionMsg.ref)
	}
	if !sessionMsg.liveNavRecovery {
		t.Fatal("resub read is not tagged as a recovery read")
	}
	// Deliver the recovery response: the transcript refreshes and the child
	// re-arm issues from the response, sequenced after the replacement.
	updated2, childCmd := m2.Update(sessionMsg)
	m3 := updated2.(hubModel)
	if m3.detail.Ref != "local:01B" {
		t.Fatalf("recovery read switched sessions: viewed ref = %q, want local:01B", m3.detail.Ref)
	}
	if childCmd == nil {
		t.Fatal("recovery response returned no command: the still-running child was not re-subscribed")
	}
	if batch, isBatch := childCmd().(tea.BatchMsg); isBatch {
		for _, child := range batch {
			_ = child()
		}
	}
	got, replaces := reads.get(), reads.replaces()
	// read 1: the cycling read (01C, replace). read 2: the recovery read
	// (01B, replace). read 3: the child re-arm (01CHILD, additive).
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
	// Round 11, medium 2 (re-scoped by round 12): same as the draft drop -
	// the subscription for the displayed session must be re-established
	// after the read dropped. The resub is a single tagged recovery read;
	// no watched children in this fixture, so its response just refreshes.
	if resub == nil {
		t.Fatal("overlay drop returned no command: the displayed session's subscription was not re-established")
	}
	msg := resub()
	sessionMsg, ok := msg.(hubSessionMsg)
	if !ok {
		t.Fatalf("resub command result = %T, want hubSessionMsg", msg)
	}
	if !sessionMsg.liveNavRecovery {
		t.Fatal("resub read is not tagged as a recovery read")
	}
	// The recovery response applies under the still-open palette: it
	// refreshes the transcript without closing the overlay or switching
	// sessions.
	updated2, _ := m2.Update(sessionMsg)
	m3 := updated2.(hubModel)
	if m3.detail.Ref != "local:01B" {
		t.Fatalf("recovery read switched sessions: viewed ref = %q, want local:01B", m3.detail.Ref)
	}
	if m3.commandPalette == nil {
		t.Fatal("recovery response closed the palette")
	}
	got, replaces := reads.get(), reads.replaces()
	if len(got) < 2 || got[1] != "local:01B" || !replaces[1] {
		t.Fatalf("resub read missing: reads = %v replaces = %v, want a replacing read for local:01B", got, replaces)
	}
}

// threadWithRunningChild is a response thread carrying one running subagent
// delegate, so a read's response re-arms the child subscription when applied
// (round-12 medium 1's sequencing contract).
func threadWithRunningChild(ref string) appwire.Thread {
	thread := responseOnlyHubThread(ref)
	thread.Turns = append(thread.Turns, appwire.Turn{
		ID: "turn-child",
		Items: []appwire.ThreadItem{{
			ID:       "item-child",
			TurnID:   "turn-child",
			Type:     "commandExecution",
			ToolName: "delegate",
			Raw:      json.RawMessage(`{"delegate_id":"dlg_child","type":"subagent","status":"running","transcript_ref":"local:01CHILD"}`),
		}},
	})
	return thread
}

// Round 12, medium 1: the replacing parent read and the additive child
// re-subscriptions were batched with tea.Batch, which has no ordering
// guarantee: if the replacing read executes after a child subscription, it
// culls that child server-side, and watchedChildRefs already marks it, so
// its live activity stays dead. The child re-arm must be issued from the
// recovery read's own response, after the replacement completed (roborev
// PR #1044 round-12 medium 1).
func TestHubSessionLiveCycleResubSequencesChildrenAfterParent(t *testing.T) {
	m, reads, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()
	// The displayed session's re-read carries the running child, so the
	// recovery response has something to re-arm.
	reads.threadByRef = map[string]appwire.Thread{"local:01B": threadWithRunningChild("local:01B")}

	m1, cmd := m.switchToAdjacentLiveSession(1) // pending: 01C
	m1.session.input.SetValue("typed while the read was in flight")

	updated, resub := m1.Update(cmd())
	m2 := updated.(hubModel)
	if resub == nil {
		t.Fatal("draft drop returned no command: the displayed session's subscription was not re-established")
	}
	msg := resub()
	// The recovery read is now the ONLY issued read: child re-arm moves to
	// the recovery response, so issuing the resub must not yet subscribe
	// the child.
	if _, ok := msg.(tea.BatchMsg); ok {
		t.Fatal("resub still batches child subscriptions with the replacing read")
	}
	sessionMsg, ok := msg.(hubSessionMsg)
	if !ok {
		t.Fatalf("resub command result = %T, want hubSessionMsg", msg)
	}
	if sessionMsg.ref != "local:01B" {
		t.Fatalf("resub read ref = %q, want local:01B", sessionMsg.ref)
	}
	// Deliver the recovery response: the child re-arm is sequenced AFTER
	// the replacement completed server-side, so it must issue now.
	updated2, childCmd := m2.Update(sessionMsg)
	m3 := updated2.(hubModel)
	if m3.detail.Ref != "local:01B" {
		t.Fatalf("recovery read switched sessions: viewed ref = %q, want local:01B", m3.detail.Ref)
	}
	if m3.session.input.Value() != "typed while the read was in flight" {
		t.Fatalf("draft = %q, want preserved through the recovery response", m3.session.input.Value())
	}
	if childCmd == nil {
		t.Fatal("recovery response returned no command: child re-subscription was not sequenced after the replacing read")
	}
	// One child collapses tea.Batch to the single subscribe command; a
	// batch (multiple children) holds lazy children to run.
	if batch, isBatch := childCmd().(tea.BatchMsg); isBatch {
		for _, child := range batch {
			_ = child()
		}
	}
	got, replaces := reads.get(), reads.replaces()
	// read 1: cycling read (01C, replace). read 2: recovery read (01B,
	// replace). read 3: child re-arm (01CHILD, additive) — after the
	// recovery response, not batched with the read.
	if len(got) < 3 || got[1] != "local:01B" || !replaces[1] {
		t.Fatalf("recovery read missing: reads = %v replaces = %v, want a replacing read for local:01B", got, replaces)
	}
	if got[len(got)-1] != "local:01CHILD" || replaces[len(replaces)-1] {
		t.Fatalf("child re-subscription missing or replacing: reads = %v replaces = %v, want an additive read for local:01CHILD last", got, replaces)
	}
}

// Round 12, medium 2: the recovery read is created with the ordinary
// fetchHubSession, so its response is an untagged hubSessionMsg. If the user
// navigates away while the recovery read is in flight, the late response is
// processed as a normal session entry and can hijack the UI back to the
// previously displayed session. Recovery reads must be tagged so Update
// discards them when the navigation intent has moved on (roborev PR #1044
// round-12 medium 2).
func TestHubSessionLiveCycleRecoveryReadDoesNotHijackNavigation(t *testing.T) {
	m, _, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	m1, cmd := m.switchToAdjacentLiveSession(1) // pending: 01C
	m1.session.input.SetValue("typed while the read was in flight")

	updated, resub := m1.Update(cmd())
	m2 := updated.(hubModel)
	if resub == nil {
		t.Fatal("draft drop returned no command: the displayed session's subscription was not re-established")
	}
	msg := resub()
	sessionMsg, ok := msg.(hubSessionMsg)
	if !ok {
		t.Fatalf("resub command result = %T, want hubSessionMsg", msg)
	}

	// The user clears the draft and navigates away while the recovery read
	// is in flight: a newer live-nav press for a different session, whose
	// response APPLIES (the newer intent takes the display).
	m2.session.input.SetValue("")
	m3, newer := m2.switchToAdjacentLiveSession(1) // from 01B, pending: 01C
	if newer == nil {
		t.Fatal("expected a newer live-nav command")
	}
	newerMsg := newer().(hubSessionMsg)
	if newerMsg.ref != "local:01C" {
		t.Fatalf("newer read ref = %q, want local:01C", newerMsg.ref)
	}
	updatedNewer, _ := m3.Update(newerMsg)
	mApplied := updatedNewer.(hubModel)
	if mApplied.detail.Ref != "local:01C" {
		t.Fatalf("newer navigation did not apply: viewed ref = %q, want local:01C", mApplied.detail.Ref)
	}

	// The recovery response now lands stale. It must be discarded, not
	// processed as an ordinary session entry that reverts the UI to 01B
	// while the user is viewing 01C.
	updated3, _ := mApplied.Update(sessionMsg)
	m4 := updated3.(hubModel)
	if m4.detail.Ref != "local:01C" {
		t.Fatalf("stale recovery response hijacked navigation: viewed ref = %q, want local:01C", m4.detail.Ref)
	}
}

// Round 14, low 1: the early alt+shift dispatch runs before the forkDraft
// handling. A user who deletes the prefilled fork text leaves the composer
// empty, so the draft guard passes and a chord switches sessions - the
// session-entry path then clears forkDraft, silently discarding the active
// fork target and original text. A non-nil forkDraft is a hold state for the
// live-nav chords, like a draft (roborev PR #1044 round-14 low 1).
func TestHubSessionLiveCycleChordHoldsWithForkDraft(t *testing.T) {
	m, reads, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	forkRef, err := appwire.ParseRef("local:01B")
	if err != nil {
		t.Fatal(err)
	}
	m.forkDraft = &hubForkDraft{
		Ref:          forkRef,
		EntryIndex:   3,
		OriginalText: "fork target message",
		Label:        "original before fork",
	}

	_, cmd := m.updateSessionKey(tea.KeyMsg{Type: tea.KeyShiftRight, Alt: true})
	if cmd != nil {
		t.Fatal("expected no live-nav command while a fork draft is active")
	}
	if got := reads.get(); len(got) != 0 {
		t.Fatalf("issued thread/read %v with a fork draft present", got)
	}
	updated := m
	if updated.forkDraft == nil {
		t.Fatal("chord handling cleared the fork draft")
	}
}

// Round 14, medium 1: a stale-dropped recovery response's ThreadRead still
// replaced the server-side subscription, and concurrent RPCs have no
// ordering guarantee - the read can complete after the newer navigation's,
// leaving the subscription on the OLDER session while the UI shows the new
// one. Dropping the stale response must also reconcile: when no newer read
// is in flight, re-establish the displayed session's subscription (roborev
// PR #1044 round-14 medium 1).
func TestHubSessionLiveCycleStaleRecoveryDropReEstablishesSubscription(t *testing.T) {
	m, reads, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	m1, cmd := m.switchToAdjacentLiveSession(1) // pending: 01C
	m1.session.input.SetValue("typed while the read was in flight")

	updated, resub := m1.Update(cmd())
	m2 := updated.(hubModel)
	if resub == nil {
		t.Fatal("draft drop returned no command: the displayed session's subscription was not re-established")
	}
	sessionMsg, ok := resub().(hubSessionMsg)
	if !ok {
		t.Fatalf("resub command result = %T, want hubSessionMsg", resub())
	}

	// The user clears the draft and navigates away while the recovery read
	// is in flight; the newer press's response APPLIES (UI on 01C).
	m2.session.input.SetValue("")
	m3, newer := m2.switchToAdjacentLiveSession(1) // from 01B, pending: 01C
	if newer == nil {
		t.Fatal("expected a newer live-nav command")
	}
	newerMsg := newer().(hubSessionMsg)
	updatedNewer, _ := m3.Update(newerMsg)
	mApplied := updatedNewer.(hubModel)
	if mApplied.detail.Ref != "local:01C" {
		t.Fatalf("newer navigation did not apply: viewed ref = %q, want local:01C", mApplied.detail.Ref)
	}

	// The stale recovery response lands: dropped for the transcript, but its
	// server-side replacement may have landed AFTER the newer read's - the
	// subscription can be on the older session. The drop must reconcile by
	// re-establishing the DISPLAYED session's subscription.
	updated3, reconcile := mApplied.Update(sessionMsg)
	m4 := updated3.(hubModel)
	if m4.detail.Ref != "local:01C" {
		t.Fatalf("stale recovery response hijacked navigation: viewed ref = %q, want local:01C", m4.detail.Ref)
	}
	if reconcile == nil {
		t.Fatal("stale recovery drop returned no command: the displayed session's subscription was not reconciled")
	}
	// The reconcile read targets the displayed session and carries the
	// recovery tag (so IT cannot hijack in turn).
	reconcileMsg, ok := reconcile().(hubSessionMsg)
	if !ok {
		t.Fatalf("reconcile command result = %T, want hubSessionMsg", reconcile())
	}
	if reconcileMsg.ref != "local:01C" {
		t.Fatalf("reconcile read ref = %q, want local:01C", reconcileMsg.ref)
	}
	if !reconcileMsg.liveNavRecovery {
		t.Fatal("reconcile read is not tagged as a recovery read")
	}
	if reconcileMsg.capture != nil {
		reconcileMsg.capture.Release()
	}
	got := reads.get()
	if got[len(got)-1] != "local:01C" {
		t.Fatalf("last read = %v, want the reconcile read for local:01C", got)
	}
}

// Round 15, medium 2: the apply-time mid-flight guard tested only input
// text and attachments - a fork draft created while the cycling read was in
// flight was applied over and cleared by the session-entry path, silently
// discarding the fork target. The mid-flight guard must re-check forkDraft
// too, clearing the pending state and re-establishing the displayed
// subscription like the draft drop (roborev PR #1044 round-15 medium 2).
func TestHubSessionLiveCycleDropsReadWhenForkDraftAppearedMidFlight(t *testing.T) {
	m, reads, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	m1, cmd := m.switchToAdjacentLiveSession(1) // pending: 01C
	forkRef, err := appwire.ParseRef("local:01B")
	if err != nil {
		t.Fatal(err)
	}
	m1.forkDraft = &hubForkDraft{
		Ref:          forkRef,
		EntryIndex:   2,
		OriginalText: "mid-flight fork target",
		Label:        "original before fork",
	}

	updated, resub := m1.Update(cmd())
	m2 := updated.(hubModel)
	if m2.detail.Ref != "local:01B" {
		t.Fatalf("live-nav read applied over a mid-flight fork draft: viewed ref = %q, want local:01B", m2.detail.Ref)
	}
	if m2.forkDraft == nil {
		t.Fatal("live-nav read cleared the mid-flight fork draft")
	}
	if m2.liveNavPendingRef != "" {
		t.Fatalf("liveNavPendingRef = %q after dropping over a fork draft, want cleared", m2.liveNavPendingRef)
	}
	// The dropped read's replacement must be reconciled: a tagged recovery
	// read for the displayed session.
	if resub == nil {
		t.Fatal("fork-draft drop returned no command: the displayed session's subscription was not re-established")
	}
	sessionMsg, ok := resub().(hubSessionMsg)
	if !ok {
		t.Fatalf("resub command result = %T, want hubSessionMsg", resub())
	}
	if sessionMsg.ref != "local:01B" {
		t.Fatalf("resub read ref = %q, want local:01B", sessionMsg.ref)
	}
	if !sessionMsg.liveNavRecovery {
		t.Fatal("resub read is not tagged as a recovery read")
	}
	got := reads.get()
	if len(got) < 2 || got[1] != "local:01B" {
		t.Fatalf("resub read missing: reads = %v, want a read for local:01B", got)
	}
}

// Round 15, medium 1: a stale recovery response dropped while a newer read
// was still PENDING recorded nothing - the newer response then applied and
// cleared the pending state, and no later read reconciled, so the stale
// replacement could leave the subscription on the older session forever.
// The settle point must issue the tagged recovery (roborev PR #1044
// round-15 medium 1).
func TestHubSessionLiveCycleReconcilesAfterPendingNavigationSettles(t *testing.T) {
	m, reads, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	m1, cmd := m.switchToAdjacentLiveSession(1) // pending: 01C
	m1.session.input.SetValue("typed while the read was in flight")

	updated, resub := m1.Update(cmd())
	m2 := updated.(hubModel)
	if resub == nil {
		t.Fatal("draft drop returned no command: the displayed session's subscription was not re-established")
	}
	sessionMsg, ok := resub().(hubSessionMsg)
	if !ok {
		t.Fatalf("resub command result = %T, want hubSessionMsg", resub())
	}

	// The user clears the draft and presses again while the recovery read
	// is in flight: the newer read is PENDING (not yet applied).
	m2.session.input.SetValue("")
	m3, newer := m2.switchToAdjacentLiveSession(1) // from 01B, pending: 01C
	if newer == nil {
		t.Fatal("expected a newer live-nav command")
	}

	// The stale recovery response drops while the newer read is pending.
	updated3, dropped := m3.Update(sessionMsg)
	m4 := updated3.(hubModel)
	if m4.detail.Ref != "local:01B" {
		t.Fatalf("stale recovery response hijacked: viewed ref = %q, want local:01B", m4.detail.Ref)
	}
	if m4.liveNavPendingRef != "local:01C" {
		t.Fatalf("stale drop cleared the pending target: liveNavPendingRef = %q, want local:01C", m4.liveNavPendingRef)
	}
	_ = dropped

	// The newer read's response now applies: it clears the pending state,
	// and because a stale replacement occurred while the read was pending,
	// the settle point must issue a tagged recovery for the displayed
	// session's subscription.
	newerMsg := newer().(hubSessionMsg)
	updatedNewer, settle := m4.Update(newerMsg)
	m5 := updatedNewer.(hubModel)
	if m5.detail.Ref != "local:01C" {
		t.Fatalf("newer navigation did not apply: viewed ref = %q, want local:01C", m5.detail.Ref)
	}
	if settle == nil {
		t.Fatal("settle point returned no command: the stale replacement was not reconciled")
	}
	// Unwrap layers: the settle may batch the child re-arm with the recovery
	// read. Find the recovery read for the displayed session.
	var reconcileMsg hubSessionMsg
	var collect func(msg tea.Msg)
	collect = func(msg tea.Msg) {
		switch inner := msg.(type) {
		case hubSessionMsg:
			if inner.ref == "local:01C" {
				reconcileMsg = inner
			}
		case tea.BatchMsg:
			for _, child := range inner {
				collect(child())
			}
		}
	}
	collect(settle())
	if reconcileMsg.ref == "" {
		t.Fatal("settle point issued no recovery read for the displayed session")
	}
	if !reconcileMsg.liveNavRecovery {
		t.Fatal("settle recovery read is not tagged as a recovery read")
	}
	if reconcileMsg.capture != nil {
		reconcileMsg.capture.Release()
	}
	got := reads.get()
	if got[len(got)-1] != "local:01C" {
		t.Fatalf("last read = %v, want the settle recovery for local:01C", got)
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
	sessionMsg, ok := msg.(hubSessionMsg)
	if !ok {
		t.Fatalf("resub command result = %T, want hubSessionMsg", msg)
	}
	// Round 13, medium 1: the settled-display resub must be a TAGGED recovery
	// read — an untagged response can land after a newer live-nav response
	// and revert the UI through the ordinary session-entry branch.
	if !sessionMsg.liveNavRecovery {
		t.Fatal("settled-display resub is not tagged as a recovery read")
	}
	if sessionMsg.ref != "local:01A" {
		t.Fatalf("resub read ref = %q, want local:01A", sessionMsg.ref)
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

// Round 13, medium 3: the recovery branch returned before the common
// msg.beforeCut fold, so notifications the connection delivered AHEAD of the
// recovery read — escalations, resync requests, other hub updates — were
// silently discarded. The fold must run before the recovery apply, with any
// resulting commands batched alongside the child re-arm (roborev PR #1044
// round-13 medium 3).
func TestHubSessionLiveCycleRecoveryFoldsBeforeCut(t *testing.T) {
	m, reads, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	m1, cmd := m.switchToAdjacentLiveSession(1) // pending: 01C
	m1.session.input.SetValue("typed while the read was in flight")

	updated, resub := m1.Update(cmd())
	m2 := updated.(hubModel)
	if resub == nil {
		t.Fatal("draft drop returned no command: the displayed session's subscription was not re-established")
	}
	sessionMsg, ok := resub().(hubSessionMsg)
	if !ok {
		t.Fatalf("resub command result = %T, want hubSessionMsg", resub())
	}

	// An escalation the connection delivered ahead of the recovery read: it
	// rides the response's beforeCut and must be enqueued when the recovery
	// applies.
	escalationParams, err := json.Marshal(appwire.SandboxEscalationRequested{
		Ref:          "local:01B",
		EscalationID: "esc-1",
		Tool:         "exec_command",
		DeniedPath:   "/tmp/denied",
		Mode:         "write",
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionMsg.beforeCut = []appwire.Notification{{
		Method: appwire.NotifyEvenerSandboxEscalationRequested,
		Params: escalationParams,
	}}

	updated2, _ := m2.Update(sessionMsg)
	m3 := updated2.(hubModel)
	if m3.detail.Ref != "local:01B" {
		t.Fatalf("recovery read switched sessions: viewed ref = %q, want local:01B", m3.detail.Ref)
	}
	if len(m3.escalationsByRef["local:01B"]) != 1 {
		t.Fatalf("beforeCut escalation was not folded: escalationsByRef = %v, want one for local:01B", m3.escalationsByRef)
	}
	_ = reads
}

// Round 13, low 4: the transcriptView early return handles only esc/i/q and
// passes everything else to the viewport, so the Alt+Shift+Arrow live-nav
// chords were unreachable while viewing a child transcript (roborev PR #1044
// round-13 low 4).
func TestHubSessionLiveCycleChordWorksInTranscriptView(t *testing.T) {
	m, reads, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	m.transcriptView = &hubTranscriptViewState{
		Ref:    "local:01CHILD",
		Title:  "Child transcript",
		Source: "subagent",
	}

	_, cmd := m.updateSessionKey(tea.KeyMsg{Type: tea.KeyShiftRight, Alt: true})
	if cmd == nil {
		t.Fatal("transcript view: expected a live-nav fetch command, got none")
	}
	msg := cmd().(hubSessionMsg)
	if msg.ref != "local:01C" {
		t.Fatalf("transcript view chord switched to %q, want local:01C", msg.ref)
	}
	if msg.capture != nil {
		msg.capture.Release()
	}
	if got := reads.get(); len(got) != 1 || got[0] != "local:01C" {
		t.Fatalf("thread/read refs = %v, want [local:01C]", got)
	}
	if m.transcriptView == nil {
		t.Fatal("chord handling cleared the transcript view")
	}
}

// runBatchChildren executes a command's result children (tea.Batch holds
// them lazily), collecting whatever finishes within the timeout. The
// notification-wait child blocks on its channel and simply times out; the
// session read the caller wants responds immediately.
func runBatchChildren(t *testing.T, cmd tea.Cmd, timeout time.Duration) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	results := make(chan tea.Msg, len(batch))
	for _, child := range batch {
		go func(child tea.Cmd) {
			if child != nil {
				results <- child()
			}
		}(child)
	}
	var collected []tea.Msg
	deadline := time.After(timeout)
collect:
	for len(collected) < len(batch) {
		select {
		case result := <-results:
			collected = append(collected, result)
		case <-deadline:
			break collect
		}
	}
	return collected
}

// Round 13, medium 1: the reconnect resync re-reads the viewed session with
// an untagged resyncHubSession. If the user live-navigates while that resync
// response is in flight (a newer live-nav read applies), the late resync
// response can land afterwards and revert the UI through the ordinary
// session-entry branch, replacing the newer subscription. The reconnect resync
// must carry the recovery tag so a late response drops as stale (roborev PR
// #1044 round-13 medium 1).
func TestHubSessionLiveCycleReconnectResyncIsTaggedRecovery(t *testing.T) {
	m, _, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()

	_, feed, cleanup2 := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: responseOnlyHubThread(params.Ref)}, nil
		})
	})
	defer cleanup2()
	msg := hubReconnectMsg{client: m.client, frames: feed}
	cmd := m.applyHubReconnect(msg)

	// Collect the reconnect batch's responding children and find the
	// session read for the displayed session.
	var resyncMsg tea.Msg
	for _, result := range runBatchChildren(t, cmd, 2*time.Second) {
		if sessionMsg, ok := result.(hubSessionMsg); ok && sessionMsg.ref == "local:01B" {
			resyncMsg = sessionMsg
		}
	}
	if resyncMsg == nil {
		t.Fatal("reconnect issued no session resync read for the displayed session")
	}
	sessionMsg := resyncMsg.(hubSessionMsg)
	if !sessionMsg.liveNavRecovery {
		t.Fatal("reconnect resync is not tagged as a recovery read")
	}
	if sessionMsg.capture != nil {
		sessionMsg.capture.Release()
	}
}

// Round 13, medium 2: a failed recovery read immediately started another
// recovery read with no backoff, retry limit, error reporting, or
// connection-state check. A persistent RPC failure would loop recovery
// forever. Recovery retries must be bounded; after the bound the failure
// surfaces through the model's error path and the loop stops (roborev PR
// #1044 round-13 medium 2).
func TestHubSessionLiveCycleRecoveryRetryIsBounded(t *testing.T) {
	m, reads, cleanup := newLiveCycleModel(t, "local:01B", liveCycleTree())
	defer cleanup()
	// Every read for the displayed session fails.
	reads.failRef = "local:01B"

	m1, cmd := m.switchToAdjacentLiveSession(1) // pending: 01C
	m1.session.input.SetValue("typed while the read was in flight")

	updated, resub := m1.Update(cmd())
	m2 := updated.(hubModel)
	if resub == nil {
		t.Fatal("draft drop returned no command: the displayed session's subscription was not re-established")
	}

	// Drive the recovery retry loop to its bound: each failed recovery
	// response may retry, but only a bounded number of times.
	model := m2
	retries := 0
	cmd2 := resub
	for cmd2 != nil && retries < 10 {
		msg := cmd2()
		sessionMsg, ok := msg.(hubSessionMsg)
		if !ok {
			break
		}
		if sessionMsg.capture != nil {
			sessionMsg.capture.Release()
		}
		updated2, next := model.Update(sessionMsg)
		model = updated2.(hubModel)
		cmd2 = next
		retries++
	}
	if retries >= 10 {
		t.Fatalf("recovery retried %d times without bound", retries)
	}
	// The bound must be small and the loop must end with the failure
	// surfaced (m.err set) rather than silently dropped.
	if retries == 0 {
		t.Fatal("recovery did not retry at all")
	}
	if model.err == nil {
		t.Fatal("persistent recovery failure did not surface an error")
	}
	// And no further reads were issued past the bound.
	before := len(reads.get())
	if cmd2 != nil {
		_ = cmd2()
	}
	if after := len(reads.get()); after > before {
		t.Fatalf("recovery issued another read after surfacing failure: %d -> %d", before, after)
	}
}
