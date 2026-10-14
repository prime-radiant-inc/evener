package tui

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

// The #4343 TUI contract: a session entry read rejected with the hub's typed
// transcriptIndexRebuilding code must not strand — the model arms a paced
// retry (the browser store's owner: base 500ms, cap 15s, retry-forever)
// scoped to the current navigation intent, and the error line shows the
// repair status instead of the error while the retry stands.

// rebuildingEntryError builds the wire error exactly as the hub sends it:
// the typed rebuilding answer, stamped with the daemonless history identity.
func rebuildingEntryError(t *testing.T) error {
	t.Helper()
	return appwire.WithHistoryReadIdentity(appwire.TranscriptIndexRebuilding(), appwire.DaemonlessBootGeneration, 0)
}

// historyFailedEntryError builds the budget's terminal answer the same way.
func historyFailedEntryError(t *testing.T) error {
	t.Helper()
	return appwire.WithHistoryReadIdentity(appwire.TranscriptHistoryFailed(3), appwire.DaemonlessBootGeneration, 0)
}

// captureEntryRepairWaits replaces the retry's wait for the test's duration
// and records every delay it is asked to sleep, so the pacing shape is
// asserted computationally, never lived.
func captureEntryRepairWaits(t *testing.T) *[]time.Duration {
	t.Helper()
	waits := &[]time.Duration{}
	old := hubEntryRepairWait
	hubEntryRepairWait = func(d time.Duration) { *waits = append(*waits, d) }
	t.Cleanup(func() { hubEntryRepairWait = old })
	return waits
}

// executeEntryRepairCmd runs an armed retry's command under the captured
// wait and returns the tick it produces: the one message the retry lands as.
func executeEntryRepairCmd(t *testing.T, cmd tea.Cmd) hubEntryRepairTickMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("the retry armed no command")
	}
	msg := cmd()
	tick, ok := msg.(hubEntryRepairTickMsg)
	if !ok {
		t.Fatalf("the armed command produced %T, want hubEntryRepairTickMsg", msg)
	}
	return tick
}

// assertStatusLine pins the error line's content while a retry stands: the
// repair status is the line's first account, and the raw entry error is not
// on it.
func assertStatusLine(t *testing.T, m hubModel, raw error) {
	t.Helper()
	errs := m.prominentErrors()
	if len(errs) == 0 || !errors.Is(errs[0], errHubEntryRepairing) {
		t.Fatalf("prominent errors = %v, want the repair status first while the retry stands", errs)
	}
	for _, e := range errs {
		if errors.Is(e, raw) {
			t.Fatalf("prominent errors = %v, want the raw rebuilding error off the line while the status stands", errs)
		}
	}
}

// jsonDecodedRebuildingError builds the wire error as the client actually
// receives it: the error frame is JSON, and encoding/json decodes the
// data object into a map — never the typed struct the hub built. An
// arming that only matches typed data works in-process and never over the
// wire.
func jsonDecodedRebuildingError(t *testing.T) error {
	t.Helper()
	return jsonDecodedWireError(t, appwire.WithHistoryReadIdentity(appwire.TranscriptIndexRebuilding(), appwire.DaemonlessBootGeneration, 0))
}

// jsonDecodedWireError round-trips a wire error through the JSON the client
// actually receives, so a matcher is proven against the map shape
// encoding/json produces, never the typed struct the hub built.
func jsonDecodedWireError(t *testing.T, original appwire.WireError) error {
	t.Helper()
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded appwire.WireError
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded.Data.(map[string]any); !ok {
		t.Fatalf("decoded data = %T, want the map shape a real client receives", decoded.Data)
	}
	return decoded
}

func TestAJSONDecodedRebuildingErrorArmsTheRetry(t *testing.T) {
	captureEntryRepairWaits(t)
	m := newHubModel(nil, "http://hub.test")
	ref := mustParseRef(t, "local:01EVENER")

	updated, cmd := m.Update(hubSessionMsg{ref: ref.String(), err: jsonDecodedRebuildingError(t)})
	next := updated.(hubModel)
	if next.entryRepairRef != ref {
		t.Fatal("a wire-decoded rebuilding error did not arm the retry: the matcher never sees the client's map shape")
	}
	assertStatusLine(t, next, nil)
	executeEntryRepairCmd(t, cmd)
}

// The typed lock-contention answer arms the paced entry retry too: the TUI
// has no ordinary-error retry, so a bare contention error would strand the
// entry — the typed answer is what the hub's no-repair contention branch
// sends, in the wire-decoded map shape a real client receives.
func TestAJSONDecodedLockedErrorArmsTheRetry(t *testing.T) {
	captureEntryRepairWaits(t)
	m := newHubModel(nil, "http://hub.test")
	ref := mustParseRef(t, "local:01EVENER")

	locked := jsonDecodedWireError(t, appwire.WithHistoryReadIdentity(appwire.TranscriptIndexLocked(), appwire.DaemonlessBootGeneration, 0))
	updated, cmd := m.Update(hubSessionMsg{ref: ref.String(), err: locked})
	next := updated.(hubModel)
	if next.entryRepairRef != ref {
		t.Fatal("a wire-decoded lock-contention error did not arm the retry: the entry would strand on it")
	}
	assertStatusLine(t, next, nil)
	executeEntryRepairCmd(t, cmd)
}

func TestAStaleTickForASupersededRetryLeavesTheCurrentRetryStanding(t *testing.T) {
	captureEntryRepairWaits(t)
	m := newHubModel(nil, "http://hub.test")
	refA := mustParseRef(t, "local:01A")
	refB := mustParseRef(t, "local:01B")

	// Session A's retry arms, its tick pending.
	updatedA, cmdA := m.Update(hubSessionMsg{ref: refA.String(), err: rebuildingEntryError(t)})
	armedA := updatedA.(hubModel)
	tickA := executeEntryRepairCmd(t, cmdA)

	// The user picks session B: the pick retires A's retry, and B's entry
	// read fails with the same code — B's retry arms, its tick pending.
	afterPick := armedA
	afterPick.entryRepairRetire()
	updatedB, cmdB := afterPick.Update(hubSessionMsg{ref: refB.String(), err: rebuildingEntryError(t)})
	armedB := updatedB.(hubModel)
	if armedB.entryRepairRef != refB {
		t.Fatal("the picked session's rebuilding error did not arm its retry")
	}
	tickB := executeEntryRepairCmd(t, cmdB)

	// A's stale tick lands after B armed: it belongs to a superseded retry
	// and must be ignored — retiring here would clear B's status and kill
	// B's retry out from under the session the user just picked.
	afterStaleTick, staleCmd := armedB.Update(tickA)
	stale := afterStaleTick.(hubModel)
	if stale.entryRepairRef != refB {
		t.Fatalf("a stale tick for %s retired the standing retry for %s", refA, refB)
	}
	assertStatusLine(t, stale, rebuildingEntryError(t))
	if staleCmd != nil {
		t.Fatal("a stale tick for a superseded retry issued a read")
	}

	// B's own tick still re-reads.
	afterTickB, read := stale.Update(tickB)
	serving := afterTickB.(hubModel)
	if read == nil {
		t.Fatal("the standing retry's tick re-issued no entry read")
	}
	if serving.entryRepairRef != refB {
		t.Fatal("the standing retry did not survive its own tick")
	}
}

// A superseded retry's re-read lands after the model moved on: the read
// executed server-side, but its response must be discarded whole — its
// success must not enter abandoned navigation (clearing the newer surface's
// composer and attachments with it), its error must not rearm it. The
// re-read is tagged with the retry's generation (the livecycle pattern), so
// the landing can tell superseded from current.
func TestASupersededRetryReadPreservesTheNewerSurfaceAndItsDraft(t *testing.T) {
	captureEntryRepairWaits(t)
	reads := &liveCycleReads{}
	client, feed, cleanup := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return reads.record(params)
		})
	})
	defer cleanup()
	m := newHubModel(client, "")
	m.frames = feed
	refA := mustParseRef(t, "local:01A")
	refB := mustParseRef(t, "local:01B")

	// Session A's retry arms; its tick re-issues A's entry read, which is
	// now in flight carrying A's generation.
	updatedA, cmdA := m.Update(hubSessionMsg{ref: refA.String(), err: rebuildingEntryError(t)})
	armedA := updatedA.(hubModel)
	armedSeq := armedA.entryRepairSeq
	_, readA := armedA.Update(executeEntryRepairCmd(t, cmdA))
	if readA == nil {
		t.Fatal("the retry's tick re-issued no read")
	}

	// The user picks session B, and B's read lands: B is displayed with
	// unsent work in its composer.
	afterPick := armedA
	afterPick.entryRepairRetire()
	enteredB, _ := afterPick.Update(hubSessionMsg{
		ref:    refB.String(),
		detail: hubSessionDetail{Ref: refB.String(), SessionID: "sess_b", State: appwire.ThreadStatusIdle},
	})
	shown := enteredB.(hubModel)
	if shown.mode != hubModeSession || shown.detail.Ref != refB.String() {
		t.Fatalf("mode = %v detail = %v, want session B displayed", shown.mode, shown.detail.Ref)
	}
	shown.session.input.SetValue("unsent work for B")

	// A's late read lands, tagged with A's superseded generation: discarded,
	// the newer surface and its draft preserved, the displayed session's
	// subscription re-established (A's read replaced it server-side).
	late := hubSessionMsg{
		ref:           refA.String(),
		detail:        hubSessionDetail{Ref: refA.String(), SessionID: "sess_a", State: appwire.ThreadStatusIdle},
		entryRetrySeq: armedSeq,
	}
	afterLate, reestablish := shown.Update(late)
	preserved := afterLate.(hubModel)
	if preserved.mode != hubModeSession || preserved.detail.Ref != refB.String() {
		t.Fatalf("a superseded retry read entered abandoned navigation: mode = %v detail = %v", preserved.mode, preserved.detail.Ref)
	}
	if got := preserved.session.input.Value(); got != "unsent work for B" {
		t.Fatalf("composer draft = %q, want the newer surface's unsent work preserved", got)
	}
	if reestablish == nil {
		t.Fatal("a superseded retry read re-pointed the subscription but re-established nothing")
	}
	recovery, ok := reestablish().(hubSessionMsg)
	if !ok {
		t.Fatalf("the re-establish command produced %T, want a session read", reestablish())
	}
	if !recovery.liveNavRecovery {
		t.Fatal("the re-established read is not recovery-tagged: a newer switch landing under it would revert")
	}
	if got := reads.get(); len(got) != 1 || got[0] != refB.String() {
		t.Fatalf("re-establish read %v, want the displayed session %v re-read", got, refB)
	}
}

func TestASupersededRetryReadRejectionRearmsNothing(t *testing.T) {
	captureEntryRepairWaits(t)
	reads := &liveCycleReads{}
	client, feed, cleanup := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return reads.record(params)
		})
	})
	defer cleanup()
	m := newHubModel(client, "")
	m.frames = feed
	refA := mustParseRef(t, "local:01A")
	refB := mustParseRef(t, "local:01B")

	updatedA, cmdA := m.Update(hubSessionMsg{ref: refA.String(), err: rebuildingEntryError(t)})
	armedA := updatedA.(hubModel)
	armedSeq := armedA.entryRepairSeq
	armedA.Update(executeEntryRepairCmd(t, cmdA)) // A's re-read in flight

	afterPick := armedA
	afterPick.entryRepairRetire()
	enteredB, _ := afterPick.Update(hubSessionMsg{
		ref:    refB.String(),
		detail: hubSessionDetail{Ref: refB.String(), SessionID: "sess_b", State: appwire.ThreadStatusIdle},
	})
	shown := enteredB.(hubModel)

	// A's late rejection lands, tagged with A's superseded generation: no
	// rearm, no error surfaced, the newer surface preserved — and the same
	// subscription-reestablish obligation a superseded success carries, since
	// the rejected read re-pointed the connection's subscriptions server-side
	// too.
	late := hubSessionMsg{ref: refA.String(), err: rebuildingEntryError(t), entryRetrySeq: armedSeq}
	afterLate, reestablish := shown.Update(late)
	preserved := afterLate.(hubModel)
	if preserved.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("a superseded retry read's rejection rearmed abandoned navigation")
	}
	if preserved.err != nil {
		t.Fatalf("a superseded retry read's rejection surfaced %v on the newer surface", preserved.err)
	}
	if preserved.mode != hubModeSession || preserved.detail.Ref != refB.String() {
		t.Fatalf("a superseded retry read's rejection moved the model: mode = %v detail = %v", preserved.mode, preserved.detail.Ref)
	}
	if reestablish == nil {
		t.Fatal("a superseded retry read's rejection re-pointed the subscription but re-established nothing")
	}
	recovery, ok := reestablish().(hubSessionMsg)
	if !ok {
		t.Fatalf("the re-establish command produced %T, want a session read", reestablish())
	}
	if !recovery.liveNavRecovery {
		t.Fatal("the re-established read is not recovery-tagged: a newer switch landing under it would revert")
	}
	if got := reads.get(); len(got) != 1 || got[0] != refB.String() {
		t.Fatalf("re-establish read %v, want the displayed session %v re-read", got, refB)
	}
}

func TestARebuildingEntryErrorArmsThePacedRetryAndShowsTheStatus(t *testing.T) {
	captureEntryRepairWaits(t)
	m := newHubModel(nil, "http://hub.test")
	ref := mustParseRef(t, "local:01EVENER")
	raw := rebuildingEntryError(t)

	updated, cmd := m.Update(hubSessionMsg{ref: ref.String(), err: raw})
	next := updated.(hubModel)
	if next.entryRepairRef != ref {
		t.Fatalf("entry repair ref = %q, want %q armed", next.entryRepairRef, ref)
	}
	if next.entryRepairDelay != hubEntryRepairBaseDelay {
		t.Fatalf("entry repair delay = %v, want the base %v", next.entryRepairDelay, hubEntryRepairBaseDelay)
	}
	assertStatusLine(t, next, raw)
	tick := executeEntryRepairCmd(t, cmd)
	if tick.ref != ref {
		t.Fatalf("tick ref = %q, want %q", tick.ref, ref)
	}
}

func TestTheEntryRepairRetryPacesBaseThroughCap(t *testing.T) {
	waits := captureEntryRepairWaits(t)
	m := newHubModel(nil, "http://hub.test")
	ref := mustParseRef(t, "local:01EVENER")
	rebuilding := rebuildingEntryError(t)

	updated, cmd := m.Update(hubSessionMsg{ref: ref.String(), err: rebuilding})
	next := updated.(hubModel)
	for _, want := range []time.Duration{
		500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 15 * time.Second,
	} {
		// The armed command waits the current delay, then ticks.
		tick := executeEntryRepairCmd(t, cmd)
		// The tick's intent stands, so it re-issues the entry read and
		// grows the delay for the round after.
		ru, read := next.Update(tick)
		reread := ru.(hubModel)
		if read == nil {
			t.Fatalf("the tick re-issued no entry read at delay %v", want)
		}
		// The re-read answers the rebuilding code again, tagged with the
		// armed generation as the wrapper stamps it: the retry re-arms
		// under the grown delay.
		u2, c2 := reread.Update(hubSessionMsg{ref: ref.String(), err: rebuilding, entryRetrySeq: reread.entryRepairSeq})
		next = u2.(hubModel)
		cmd = c2
		if got := (*waits)[len(*waits)-1]; got != want {
			t.Fatalf("waited %v, want %v (waits so far: %v)", got, want, *waits)
		}
	}
	if len(*waits) != 7 {
		t.Fatalf("waits = %v, want the seven-step schedule", *waits)
	}
}

func TestHistoryFailedEndsTheEntryRepairRetryAndShowsTheError(t *testing.T) {
	captureEntryRepairWaits(t)
	m := newHubModel(nil, "http://hub.test")
	ref := mustParseRef(t, "local:01EVENER")
	updated, _ := m.Update(hubSessionMsg{ref: ref.String(), err: rebuildingEntryError(t)})
	next := updated.(hubModel)
	if next.entryRepairRef != ref {
		t.Fatal("the retry did not arm before the history-failed answer")
	}

	terminal := historyFailedEntryError(t)
	u2, cmd := next.Update(hubSessionMsg{ref: ref.String(), err: terminal})
	ended := u2.(hubModel)
	if ended.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("the history-failed answer left the retry armed")
	}
	if !errors.Is(ended.err, terminal) {
		t.Fatalf("model error = %v, want the history-failed answer on the error line", ended.err)
	}
	errs := ended.prominentErrors()
	if len(errs) == 0 || !errors.Is(errs[0], terminal) {
		t.Fatalf("prominent errors = %v, want the history-failed answer first", errs)
	}
	if cmd != nil {
		t.Fatal("the history-failed answer scheduled another retry")
	}
}

func TestAnOrdinaryTransientKeepsTheEntryRepairRetryArmed(t *testing.T) {
	captureEntryRepairWaits(t)
	m := newHubModel(nil, "http://hub.test")
	ref := mustParseRef(t, "local:01EVENER")
	updated, _ := m.Update(hubSessionMsg{ref: ref.String(), err: rebuildingEntryError(t)})
	next := updated.(hubModel)

	// Mid-repair lock contention with no repair in flight answers an
	// ordinary transient: the retry stays armed, the status stands, and
	// the transient error rides after the status on the line.
	ordinary := errors.New("thread history is unavailable at this moment")
	u2, cmd := next.Update(hubSessionMsg{ref: ref.String(), err: ordinary})
	still := u2.(hubModel)
	if still.entryRepairRef != ref {
		t.Fatal("an ordinary transient retired the armed retry")
	}
	if !errors.Is(still.err, ordinary) {
		t.Fatalf("model error = %v, want the ordinary transient beside the status", still.err)
	}
	assertStatusLine(t, still, rebuildingEntryError(t))
	executeEntryRepairCmd(t, cmd)
}

func TestNavigationRetiresTheRetryAndALateTickEntersNothing(t *testing.T) {
	captureEntryRepairWaits(t)
	m := newHubModel(nil, "http://hub.test")
	ref := mustParseRef(t, "local:01EVENER")
	updated, _ := m.Update(hubSessionMsg{ref: ref.String(), err: rebuildingEntryError(t)})
	next := updated.(hubModel)
	armedSeq := next.entryRepairSeq

	// The user picks another session and its read lands: a session switch,
	// the newer intent. The pending retry retires with it.
	switched := hubSessionMsg{
		ref:    "local:01OTHER",
		detail: hubSessionDetail{Ref: "local:01OTHER", SessionID: "sess_other", State: appwire.ThreadStatusIdle},
	}
	u2, _ := next.Update(switched)
	shown := u2.(hubModel)
	if shown.mode != hubModeSession || shown.detail.Ref != "local:01OTHER" {
		t.Fatalf("mode = %v detail = %v, want the other session entered", shown.mode, shown.detail.Ref)
	}
	if shown.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("a session switch left the departed ref's retry standing")
	}

	// The departed intent's late tick enters nothing: no re-read, no
	// status, the entered session unchanged.
	late := hubEntryRepairTickMsg{ref: ref, seq: armedSeq}
	u3, cmd := shown.Update(late)
	after := u3.(hubModel)
	if cmd != nil {
		t.Fatal("a late tick for a departed intent re-issued the entry read")
	}
	if after.mode != hubModeSession || after.detail.Ref != "local:01OTHER" {
		t.Fatalf("a late tick moved the model: mode = %v detail = %v", after.mode, after.detail.Ref)
	}
	if after.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("a late tick rearmed the departed ref's retry")
	}
}

// Focusing a project from the palette is a navigation departure like ctrl+O's
// dashboard return: the standing entry-repair retry retires and the
// navigation generation advances, so a re-read already on the wire enters
// nothing over the project dashboard (round-20 medium 3).
func TestAFocusedProjectRetiresTheStandingEntryRetry(t *testing.T) {
	captureEntryRepairWaits(t)
	m := newHubModel(nil, "http://hub.test")
	ref := mustParseRef(t, "local:01EVENER")
	updated, cmd := m.Update(hubSessionMsg{ref: ref.String(), err: rebuildingEntryError(t)})
	armed := updated.(hubModel)
	if armed.entryRepairRef != ref {
		t.Fatal("setup: the rebuilding answer did not arm the retry")
	}
	armedSeq := armed.entryRepairSeq
	before := armed.liveNavSeq

	// The retry's re-read is already on the wire when the user focuses a
	// project: the departure retires the abandoned session's retry and
	// advances the generation.
	tick := executeEntryRepairCmd(t, cmd)
	afterTick, rereadCmd := armed.Update(tick)
	flying := afterTick.(hubModel)
	if rereadCmd == nil {
		t.Fatal("setup: the tick re-issued no entry read")
	}
	flying.focusDashboardProject("proj")
	if flying.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("focusing a project left the abandoned session's retry armed")
	}
	if flying.liveNavSeq <= before {
		t.Fatal("focusing a project did not advance the navigation generation")
	}

	// The in-flight re-read's response lands: nothing enters over the
	// project dashboard.
	u2, entered := flying.Update(hubSessionMsg{
		ref:           ref.String(),
		detail:        hubSessionDetail{Ref: ref.String(), SessionID: "sess_a", State: appwire.ThreadStatusIdle},
		entryRetrySeq: armedSeq,
	})
	ended := u2.(hubModel)
	if ended.mode != hubModeDashboard {
		t.Fatalf("the in-flight re-read entered the abandoned session over the project dashboard: mode = %v detail = %v", ended.mode, ended.detail.Ref)
	}
	if ended.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("the superseded re-read's landing rearmed the departed ref's retry")
	}
	if entered != nil {
		t.Fatalf("the superseded re-read's landing issued a follow-up: %T", entered)
	}
}

func TestTheAlreadyOpenSurfacesArmNoRetry(t *testing.T) {
	captureEntryRepairWaits(t)
	m := newHubModel(nil, "http://hub.test")
	ref := mustParseRef(t, "local:01EVENER")

	// The raw transcript viewer's read answers the same code and surfaces
	// its one-shot system line, arming no retry and showing no status.
	target := appwire.ThreadTranscriptTarget{Ref: ref.String(), Title: "transcript"}
	u, _ := m.Update(hubTranscriptMsg{err: rebuildingEntryError(t), target: target})
	viewer := u.(hubModel)
	if viewer.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("the transcript viewer armed an entry repair retry")
	}

	// A status-refresh read of an already-displayed session answers the
	// same code as its ordinary error, arming nothing.
	entered, _ := m.Update(hubSessionMsg{
		ref:    ref.String(),
		detail: hubSessionDetail{Ref: ref.String(), SessionID: "sess_1", State: appwire.ThreadStatusIdle},
	})
	shown := entered.(hubModel)
	refresh := rebuildingEntryError(t)
	u2, _ := shown.Update(hubSessionMsg{ref: ref.String(), err: refresh, expectedState: "active", expectedRefreshToken: shown.statusRefreshToken})
	after := u2.(hubModel)
	if after.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("an already-open refresh armed an entry repair retry")
	}
	if !errors.Is(after.err, refresh) {
		t.Fatalf("model error = %v, want the refresh's own error", after.err)
	}
}

// newEntryPickModel builds a dashboard-mode model over the test feed with a
// two-live-session tree — the way a user actually arrives at a session — so
// the pick tests drive the real dashboard key path, including the read the
// pick issues and the tag that read carries.
func newEntryPickModel(t *testing.T, reads *liveCycleReads) (hubModel, func()) {
	t.Helper()
	client, feed, cleanup := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return reads.record(params)
		})
	})
	m := newHubModel(client, "")
	m.frames = feed
	m.tree = hubTreeResponse{Projects: []hubTreeProject{{
		Key:  "p1",
		Name: "One",
		Sessions: []hubTreeNode{
			{Ref: "local:01A", SessionID: "sess_a", Title: "A", State: "active", Live: true, UpdatedAt: 300},
			{Ref: "local:01B", SessionID: "sess_b", Title: "B", State: "active", Live: true, UpdatedAt: 200},
		},
	}}}
	m.rows = buildDashboardRows(m.tree)
	return m, cleanup
}

// A pick is a navigation intent another pick can supersede before its read
// answers: the user picks A, then enters B, and A's rebuilding rejection
// arrives late. Arming a retry for A then would stand the repair status on
// B's surface and eventually drive the user into abandoned navigation. The
// superseded rejection must arm nothing and surface nothing; B's re-pointed
// subscription (A's read replaced it server-side) is re-established.
func TestASupersededPickRejectionArmsNoRetry(t *testing.T) {
	captureEntryRepairWaits(t)
	reads := &liveCycleReads{wireErrorByRef: map[string]appwire.WireError{
		"local:01A": appwire.WithHistoryReadIdentity(appwire.TranscriptIndexRebuilding(), appwire.DaemonlessBootGeneration, 0),
	}}
	m, cleanup := newEntryPickModel(t, reads)
	defer cleanup()

	// The user picks A, then B, before either read answers.
	m.selected = dashboardRowIndex(m.dashboardRows(), "A")
	pickedA, cmdA := m.updateDashboardKey(tea.KeyMsg{Type: tea.KeyEnter})
	afterA := pickedA.(hubModel)
	afterA.selected = dashboardRowIndex(afterA.dashboardRows(), "B")
	pickedB, cmdB := afterA.updateDashboardKey(tea.KeyMsg{Type: tea.KeyEnter})

	// B's read lands first: B is displayed with unsent work in its composer.
	entered, _ := pickedB.Update(cmdB())
	shown := entered.(hubModel)
	if shown.mode != hubModeSession || shown.detail.Ref != "local:01B" {
		t.Fatalf("mode = %v detail = %v, want session B displayed", shown.mode, shown.detail.Ref)
	}
	shown.session.input.SetValue("unsent work for B")

	// A's rebuilding rejection lands late, its pick superseded by B's.
	afterLate, reestablish := shown.Update(cmdA())
	late := afterLate.(hubModel)
	if late.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("a superseded pick's rebuilding rejection armed a retry for abandoned navigation")
	}
	if late.err != nil {
		t.Fatalf("a superseded pick's rejection surfaced %v on the newer surface", late.err)
	}
	if late.mode != hubModeSession || late.detail.Ref != "local:01B" {
		t.Fatalf("a superseded pick's rejection moved the model: mode = %v detail = %v", late.mode, late.detail.Ref)
	}
	if got := late.session.input.Value(); got != "unsent work for B" {
		t.Fatalf("composer draft = %q, want the newer surface's unsent work preserved", got)
	}
	if reestablish == nil {
		t.Fatal("a superseded pick's rejection re-pointed the subscription but re-established nothing")
	}
	recovery, ok := reestablish().(hubSessionMsg)
	if !ok || !recovery.liveNavRecovery {
		t.Fatalf("the re-establish command produced %T, want a recovery-tagged session read", reestablish())
	}
	if got := reads.get(); got[len(got)-1] != "local:01B" {
		t.Fatalf("re-establish read %v, want the displayed session re-read", got)
	}
}

// A superseded pick's success is the same fence: it answers late, after B's
// entry already moved the user, and applying it would yank the user into
// abandoned navigation and clear the newer surface's composer.
func TestASupersededPickSuccessEntersNothing(t *testing.T) {
	captureEntryRepairWaits(t)
	reads := &liveCycleReads{}
	m, cleanup := newEntryPickModel(t, reads)
	defer cleanup()

	m.selected = dashboardRowIndex(m.dashboardRows(), "A")
	pickedA, cmdA := m.updateDashboardKey(tea.KeyMsg{Type: tea.KeyEnter})
	afterA := pickedA.(hubModel)
	afterA.selected = dashboardRowIndex(afterA.dashboardRows(), "B")
	pickedB, cmdB := afterA.updateDashboardKey(tea.KeyMsg{Type: tea.KeyEnter})

	entered, _ := pickedB.Update(cmdB())
	shown := entered.(hubModel)
	if shown.mode != hubModeSession || shown.detail.Ref != "local:01B" {
		t.Fatalf("mode = %v detail = %v, want session B displayed", shown.mode, shown.detail.Ref)
	}
	shown.session.input.SetValue("unsent work for B")

	afterLate, reestablish := shown.Update(cmdA())
	late := afterLate.(hubModel)
	if late.mode != hubModeSession || late.detail.Ref != "local:01B" {
		t.Fatalf("a superseded pick's success entered abandoned navigation: mode = %v detail = %v", late.mode, late.detail.Ref)
	}
	if got := late.session.input.Value(); got != "unsent work for B" {
		t.Fatalf("composer draft = %q, want the newer surface's unsent work preserved", got)
	}
	if reestablish == nil {
		t.Fatal("a superseded pick's success re-pointed the subscription but re-established nothing")
	}
}

// ctrl+o is the amendment's "opening another pane": departing to the
// dashboard retires the pending retry and its status, and the retired
// retry's late tick must enter nothing.
func TestCtrlODepartsAndRetiresThePendingEntryRetry(t *testing.T) {
	captureEntryRepairWaits(t)
	m := newHubModel(nil, "http://hub.test")
	ref := mustParseRef(t, "local:01EVENER")

	updated, cmd := m.Update(hubSessionMsg{ref: ref.String(), err: rebuildingEntryError(t)})
	armed := updated.(hubModel)
	tick := executeEntryRepairCmd(t, cmd)

	afterCtrlO, _ := armed.updateKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	left := afterCtrlO.(hubModel)
	if left.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("ctrl+o left the pending entry retry armed")
	}
	if errs := left.prominentErrors(); len(errs) != 0 {
		t.Fatalf("prominent errors = %v, want the repair status gone with the departure", errs)
	}

	afterTick, lateCmd := left.Update(tick)
	if lateCmd != nil {
		t.Fatal("a retired retry's tick issued a read")
	}
	if afterTick.(hubModel).entryRepairRef != (appwire.Ref{}) {
		t.Fatal("a retired retry's tick rearmed it")
	}
}

// A live-session cycling press is a newer navigation intent: its issuance
// takes the generation while the retry's re-read is in flight, and that
// re-read's late success must not override it — applying it would switch the
// user into the abandoned ref and clear the newer surface's composer.
func TestANewerCyclingPressSupersedesTheRetryReadInFlight(t *testing.T) {
	captureEntryRepairWaits(t)
	reads := &liveCycleReads{}
	client, feed, cleanup := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return reads.record(params)
		})
	})
	defer cleanup()
	m := newHubModel(client, "")
	m.frames = feed
	refA := mustParseRef(t, "local:01A")

	// The user is watching session C when A's pick fails with the rebuilding
	// code: the retry arms with C still displayed (the palette-from-inside-a-
	// session shape).
	enteredC, _ := m.Update(hubSessionMsg{
		ref:    "local:01C",
		detail: hubSessionDetail{Ref: "local:01C", SessionID: "sess_c", State: appwire.ThreadStatusIdle},
	})
	watching := enteredC.(hubModel)
	updated, cmd := watching.Update(hubSessionMsg{ref: refA.String(), err: rebuildingEntryError(t)})
	armed := updated.(hubModel)
	armedSeq := armed.entryRepairSeq
	armed.Update(executeEntryRepairCmd(t, cmd)) // the retry's re-read in flight
	armed.session.input.SetValue("unsent work for C")

	// The cycling press is the newer intent: its issuance takes the
	// generation (livecycle's bump) while the retry's re-read is in flight.
	cycled := armed
	cycled.liveNavSeq++

	// The retry's re-read succeeds late, tagged with the superseded
	// generation: discarded whole, C's re-pointed subscription re-established.
	late := hubSessionMsg{
		ref:           refA.String(),
		detail:        hubSessionDetail{Ref: refA.String(), SessionID: "sess_a", State: appwire.ThreadStatusIdle},
		entryRetrySeq: armedSeq,
	}
	afterLate, reestablish := cycled.Update(late)
	kept := afterLate.(hubModel)
	if kept.mode != hubModeSession || kept.detail.Ref != "local:01C" {
		t.Fatalf("the retry's superseded read overrode the newer cycling intent: mode = %v detail = %v", kept.mode, kept.detail.Ref)
	}
	if got := kept.session.input.Value(); got != "unsent work for C" {
		t.Fatalf("composer draft = %q, want the newer surface's unsent work preserved", got)
	}
	if kept.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("the superseded read's landing left the retry armed")
	}
	if reestablish == nil {
		t.Fatal("the superseded read re-pointed the subscription but re-established nothing")
	}
	recovery, ok := reestablish().(hubSessionMsg)
	if !ok || !recovery.liveNavRecovery {
		t.Fatalf("the re-establish command produced %T, want a recovery-tagged session read", reestablish())
	}
	if got := reads.get(); got[len(got)-1] != "local:01C" {
		t.Fatalf("re-establish read %v, want the displayed session re-read", got)
	}
}

// A hub reconnect is not a departure: the pending entry intent survives it.
// The armed retry rebinds to the replacement connection's generation, the
// dead connection's pending tick and late response land as superseded, and
// the paced read resumes on the replacement connection.
func TestAReconnectPreservesThePendingEntryRetry(t *testing.T) {
	captureEntryRepairWaits(t)
	reads := &liveCycleReads{}
	client, feed, cleanup := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return reads.record(params)
		})
	})
	defer cleanup()
	m := newHubModel(client, "")
	m.frames = feed
	ref := mustParseRef(t, "local:01EVENER")

	updated, _ := m.Update(hubSessionMsg{ref: ref.String(), err: rebuildingEntryError(t)})
	armed := updated.(hubModel)
	oldSeq := armed.entryRepairSeq

	// The connection dies and dials back: a fresh client and frames.
	freshClient, freshFeed, cleanup2 := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return reads.record(params)
		})
	})
	defer cleanup2()
	after, _ := armed.Update(hubReconnectMsg{client: freshClient, frames: freshFeed})
	rebound := after.(hubModel)
	if rebound.entryRepairRef != ref {
		t.Fatal("a reconnect dropped the pending entry retry")
	}
	if rebound.entryRepairSeq != rebound.liveNavSeq {
		t.Fatalf("the retry did not rebind: armed generation %d, model generation %d", rebound.entryRepairSeq, rebound.liveNavSeq)
	}

	// The dead connection's pending tick lands as superseded: it neither
	// re-reads nor drops the rebound retry.
	afterOldTick, oldTickCmd := rebound.Update(hubEntryRepairTickMsg{ref: ref, seq: oldSeq})
	if oldTickCmd != nil {
		t.Fatal("the dead connection's pending tick re-issued a read")
	}
	if afterOldTick.(hubModel).entryRepairRef != ref {
		t.Fatal("the dead connection's pending tick dropped the rebound retry")
	}

	// The dead connection's late read response is fenced by the rebind.
	afterOldRead, oldReadCmd := afterOldTick.(hubModel).Update(hubSessionMsg{
		ref:           ref.String(),
		detail:        hubSessionDetail{Ref: ref.String(), SessionID: "sess", State: appwire.ThreadStatusIdle},
		entryRetrySeq: oldSeq,
	})
	if oldReadCmd != nil || afterOldRead.(hubModel).detail.Ref != "" {
		t.Fatal("the dead connection's late read entered abandoned navigation")
	}

	// The replacement connection's tick — the rebound generation — re-reads
	// through the fresh client, tagged with the retry's generation as the
	// tick fences it (round-20 medium 1's fresh generation).
	afterBound, reread := afterOldRead.(hubModel).Update(hubEntryRepairTickMsg{ref: ref, seq: rebound.entryRepairSeq})
	if reread == nil {
		t.Fatal("the rebound retry's tick re-issued no read")
	}
	msg, ok := reread().(hubSessionMsg)
	if !ok {
		t.Fatalf("the re-read produced %T, want a session read", reread())
	}
	if msg.entryRetrySeq != afterBound.(hubModel).entryRepairSeq || msg.ref != ref.String() {
		t.Fatalf("the re-read landed tagged %d for %v, want the fenced generation %d for %v", msg.entryRetrySeq, msg.ref, afterBound.(hubModel).entryRepairSeq, ref)
	}
}

// The automatic completion holds for the displayed session's unsent work,
// exactly as the cycling switch does: a retry re-read that succeeds while
// the user is typing in the session still displayed must not switch away
// and destroy the draft or delete pending attachment files.
func TestASuccessfulRetryReadHoldsForTheDisplayedSessionsUnsentWork(t *testing.T) {
	captureEntryRepairWaits(t)
	reads := &liveCycleReads{}
	client, feed, cleanup := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return reads.record(params)
		})
	})
	defer cleanup()
	m := newHubModel(client, "")
	m.frames = feed
	refA := mustParseRef(t, "local:01A")
	refB := mustParseRef(t, "local:01B")

	// Session A is displayed with unsent work in its composer when the user
	// palette-picks B, whose sidecar is being repaired: B's retry arms with
	// A still displayed (the palette-from-inside-a-session shape).
	enteredA, _ := m.Update(hubSessionMsg{
		ref:    refA.String(),
		detail: hubSessionDetail{Ref: refA.String(), SessionID: "sess_a", State: appwire.ThreadStatusIdle},
	})
	shown := enteredA.(hubModel)
	shown.session.input.SetValue("unsent work for A")
	updated, cmd := shown.Update(hubSessionMsg{ref: refB.String(), err: rebuildingEntryError(t)})
	armed := updated.(hubModel)
	armedSeq := armed.entryRepairSeq
	armed.Update(executeEntryRepairCmd(t, cmd)) // B's re-read in flight

	// B's repair completes and its re-read succeeds — but the user's unsent
	// work in A outranks the automatic switch: the hold retires the retry,
	// A keeps its surface and draft, and A's re-pointed subscription (B's
	// read replaced it) is re-established.
	late := hubSessionMsg{
		ref:           refB.String(),
		detail:        hubSessionDetail{Ref: refB.String(), SessionID: "sess_b", State: appwire.ThreadStatusIdle},
		entryRetrySeq: armedSeq,
	}
	afterLate, reestablish := armed.Update(late)
	held := afterLate.(hubModel)
	if held.mode != hubModeSession || held.detail.Ref != refA.String() {
		t.Fatalf("the automatic entry switched away from drafted work: mode = %v detail = %v", held.mode, held.detail.Ref)
	}
	if got := held.session.input.Value(); got != "unsent work for A" {
		t.Fatalf("composer draft = %q, want the displayed session's unsent work preserved", got)
	}
	if held.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("the held entry retry stayed armed after declining the switch")
	}
	if reestablish == nil {
		t.Fatal("the declined read re-pointed the subscription but re-established nothing")
	}
	recovery, ok := reestablish().(hubSessionMsg)
	if !ok || !recovery.liveNavRecovery {
		t.Fatalf("the re-establish command produced %T, want a recovery-tagged session read", reestablish())
	}
	if got := reads.get(); got[len(got)-1] != refA.String() {
		t.Fatalf("re-establish read %v, want the displayed session re-read", got)
	}
}

// Departing to the dashboard before a pick's read answers invalidates the
// pick: its late rebuilding rejection must not arm a retry that enters the
// session the user left behind.
func TestCtrlOBeforeThePicksAnswerInvalidatesIt(t *testing.T) {
	captureEntryRepairWaits(t)
	reads := &liveCycleReads{wireErrorByRef: map[string]appwire.WireError{
		"local:01A": appwire.WithHistoryReadIdentity(appwire.TranscriptIndexRebuilding(), appwire.DaemonlessBootGeneration, 0),
	}}
	m, cleanup := newEntryPickModel(t, reads)
	defer cleanup()

	// The user picks A, then departs to the dashboard before A's read
	// answers.
	m.selected = dashboardRowIndex(m.dashboardRows(), "A")
	pickedA, cmdA := m.updateDashboardKey(tea.KeyMsg{Type: tea.KeyEnter})
	afterCtrlO, _ := pickedA.(hubModel).updateKey(tea.KeyMsg{Type: tea.KeyCtrlO})

	// A's rebuilding rejection arrives late: the departure invalidated the
	// pick, so it arms nothing and enters nothing.
	afterLate, lateCmd := afterCtrlO.Update(cmdA())
	late := afterLate.(hubModel)
	if late.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("a pick invalidated by the dashboard departure armed a retry")
	}
	if late.mode != hubModeDashboard {
		t.Fatalf("the late rejection moved the model: mode = %v", late.mode)
	}
	if lateCmd != nil {
		t.Fatal("the invalidated pick's rejection scheduled work")
	}
}

// targetDeletedEntryError builds the deletion refusal as the client receives
// it: the read preflight's permanent answer, stamped with the boot
// generation, JSON-decoded into the map shape a real client reads.
func targetDeletedEntryError(t *testing.T) error {
	t.Helper()
	original := appwire.WithReadBootGeneration(appwire.WireError{
		Code:    appwire.CodeConflict,
		Message: "thread target deleted",
		Data: appwire.ErrorData{
			MutationOutcome:  appwire.MutationOutcomeTargetDeleted,
			RetryDisposition: appwire.RetryDispositionNone,
		},
	}, appwire.DaemonlessBootGeneration)
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded appwire.WireError
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded.Data.(map[string]any); !ok {
		t.Fatalf("decoded data = %T, want the map shape a real client receives", decoded.Data)
	}
	return decoded
}

// A permanent answer ends the paced retry: a session deleted during its
// repair answers targetDeleted, which no retry converges — the retry
// retires and the error shows, instead of pacing forever behind the status.
func TestATargetDeletedAnswerEndsTheEntryRetry(t *testing.T) {
	captureEntryRepairWaits(t)
	m := newHubModel(nil, "http://hub.test")
	ref := mustParseRef(t, "local:01EVENER")
	updated, _ := m.Update(hubSessionMsg{ref: ref.String(), err: rebuildingEntryError(t)})
	armed := updated.(hubModel)
	if armed.entryRepairRef != ref {
		t.Fatal("the retry did not arm before the deletion answer")
	}

	terminal := targetDeletedEntryError(t)
	afterTerminal, cmd := armed.Update(hubSessionMsg{ref: ref.String(), err: terminal})
	ended := afterTerminal.(hubModel)
	if ended.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("a permanent targetDeleted answer left the paced retry armed")
	}
	if cmd != nil {
		t.Fatal("a permanent answer scheduled another read")
	}
	if wireMutationOutcome(ended.err) != appwire.MutationOutcomeTargetDeleted {
		t.Fatalf("model error = %v, want the deletion answer shown", ended.err)
	}
	errs := ended.prominentErrors()
	if len(errs) == 0 || errors.Is(errs[0], errHubEntryRepairing) {
		t.Fatalf("prominent errors = %v, want the repair status gone and the deletion shown", errs)
	}
}

// A retry's successful re-read for the session already displayed converges
// the entry it was armed for: the owner retires with it (its purpose is
// done, its status with it) and the watched children re-arm, since the
// re-read's replacement culled them server-side.
func TestASameRefRetryReadRetiresItsOwnerAndReArmsChildren(t *testing.T) {
	captureEntryRepairWaits(t)
	reads := &liveCycleReads{}
	client, feed, cleanup := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return reads.record(params)
		})
	})
	defer cleanup()
	m := newHubModel(client, "")
	m.frames = feed
	refB := mustParseRef(t, "local:01B")

	// Session B is displayed with a watched child when its own re-pick
	// answers rebuilding: the retry arms with B displayed (the re-pick of
	// the current session).
	entered, _ := m.Update(hubSessionMsg{
		ref:    refB.String(),
		detail: hubSessionDetail{Ref: refB.String(), SessionID: "sess_b", State: appwire.ThreadStatusIdle},
	})
	shown := entered.(hubModel)
	shown.watchedChildRefs = map[string]bool{"local:01CHILD": true}
	updated, cmd := shown.Update(hubSessionMsg{ref: refB.String(), err: rebuildingEntryError(t)})
	armed := updated.(hubModel)
	armedSeq := armed.entryRepairSeq
	armed.Update(executeEntryRepairCmd(t, cmd)) // B's re-read in flight

	// B's repair completes; the re-read succeeds for the displayed session:
	// a same-ref replacing read — the owner retires, the transcript
	// refreshes, and the children re-arm after the replacement.
	late := hubSessionMsg{
		ref:           refB.String(),
		detail:        hubSessionDetail{Ref: refB.String(), SessionID: "sess_b", State: appwire.ThreadStatusIdle},
		entryRetrySeq: armedSeq,
	}
	afterLate, _ := armed.Update(late)
	converged := afterLate.(hubModel)
	if converged.mode != hubModeSession || converged.detail.Ref != refB.String() {
		t.Fatalf("the same-ref read moved the model: mode = %v detail = %v", converged.mode, converged.detail.Ref)
	}
	if converged.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("the same-ref success left its repair owner armed")
	}
	if converged.watchedChildRefs != nil {
		t.Fatalf("watched children = %v, want the re-arm's cleared set", converged.watchedChildRefs)
	}
	if errs := converged.prominentErrors(); len(errs) != 0 {
		t.Fatalf("prominent errors = %v, want the repair status gone with the retired owner", errs)
	}
}

// Opening the spawn form is the same departure: a pick whose read is still
// in flight when the form opens must land superseded, not arm a retry over
// the open form.
func TestOpeningTheSpawnFormInvalidatesAnInFlightPick(t *testing.T) {
	captureEntryRepairWaits(t)
	reads := &liveCycleReads{wireErrorByRef: map[string]appwire.WireError{
		"local:01A": appwire.WithHistoryReadIdentity(appwire.TranscriptIndexRebuilding(), appwire.DaemonlessBootGeneration, 0),
	}}
	m, cleanup := newEntryPickModel(t, reads)
	defer cleanup()

	// The user picks A, then opens the spawn form before A's read answers.
	m.selected = dashboardRowIndex(m.dashboardRows(), "A")
	pickedA, cmdA := m.updateDashboardKey(tea.KeyMsg{Type: tea.KeyEnter})
	afterSpawn, _ := pickedA.(hubModel).updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})

	// A's rebuilding rejection arrives late: the departure invalidated the
	// pick, so it arms nothing over the open form.
	afterLate, lateCmd := afterSpawn.Update(cmdA())
	late := afterLate.(hubModel)
	if late.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("a pick invalidated by the spawn departure armed a retry")
	}
	if late.mode != hubModeSpawn {
		t.Fatalf("the late rejection moved the model: mode = %v", late.mode)
	}
	if lateCmd != nil {
		t.Fatal("the invalidated pick's rejection scheduled work")
	}
}

// An active fork draft is the same class of unsent work as composer text,
// even with the prefilled text deleted: the cycling switch holds for it,
// and the automatic completion must too — switching away would discard the
// fork target and its original text with the forkDraft the entry clears.
func TestASuccessfulRetryReadHoldsForAnActiveForkDraft(t *testing.T) {
	captureEntryRepairWaits(t)
	reads := &liveCycleReads{}
	client, feed, cleanup := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return reads.record(params)
		})
	})
	defer cleanup()
	m := newHubModel(client, "")
	m.frames = feed
	refA := mustParseRef(t, "local:01A")
	refB := mustParseRef(t, "local:01B")

	// Session A is displayed with an active fork draft — its prefilled text
	// deleted — when the user palette-picks B, whose repair is pending.
	enteredA, _ := m.Update(hubSessionMsg{
		ref:    refA.String(),
		detail: hubSessionDetail{Ref: refA.String(), SessionID: "sess_a", State: appwire.ThreadStatusIdle},
	})
	shown := enteredA.(hubModel)
	shown.forkDraft = &hubForkDraft{}
	updated, cmd := shown.Update(hubSessionMsg{ref: refB.String(), err: rebuildingEntryError(t)})
	armed := updated.(hubModel)
	armedSeq := armed.entryRepairSeq
	armed.Update(executeEntryRepairCmd(t, cmd)) // B's re-read in flight

	// B's re-read succeeds: the hold must fire for the fork draft exactly
	// as it does for composer text — the switch is declined, A keeps its
	// surface, and the retry retires.
	late := hubSessionMsg{
		ref:           refB.String(),
		detail:        hubSessionDetail{Ref: refB.String(), SessionID: "sess_b", State: appwire.ThreadStatusIdle},
		entryRetrySeq: armedSeq,
	}
	afterLate, _ := armed.Update(late)
	held := afterLate.(hubModel)
	if held.mode != hubModeSession || held.detail.Ref != refA.String() {
		t.Fatalf("the automatic entry switched away from a fork draft: mode = %v detail = %v", held.mode, held.detail.Ref)
	}
	if held.forkDraft == nil {
		t.Fatal("the automatic entry cleared the fork draft")
	}
	if held.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("the held entry retry stayed armed after declining the switch")
	}
}

// A live-session cycling press is an explicit navigation: the standing
// entry retry retires with it, immediately — not lazily at its next tick.
func TestACyclingPressRetiresTheStandingEntryRetry(t *testing.T) {
	captureEntryRepairWaits(t)
	m, _, cleanup := newLiveCycleModel(t, "local:01C", liveCycleTree())
	defer cleanup()
	refA := mustParseRef(t, "local:01A")

	// A palette pick of A from inside the displayed session answers
	// rebuilding: the retry arms with C displayed.
	updated, _ := m.Update(hubSessionMsg{ref: refA.String(), err: rebuildingEntryError(t)})
	armed := updated.(hubModel)
	if armed.entryRepairRef != refA {
		t.Fatal("the retry did not arm before the cycling press")
	}

	// The cycling press is the newer navigation intent: the retry retires
	// with it, and its late tick enters nothing.
	afterPress, _ := armed.updateSessionKey(tea.KeyMsg{Type: tea.KeyShiftRight, Alt: true})
	pressed := afterPress.(hubModel)
	if pressed.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("the cycling press left the superseded entry retry armed")
	}
}

// The reconnect rebind must not revive a retry a newer navigation already
// superseded: only a retry current before the reconnect's bump rebinds —
// a superseded one retires at its next tick instead of becoming current
// again.
func TestAReconnectDoesNotReviveASupersededEntryRetry(t *testing.T) {
	captureEntryRepairWaits(t)
	reads := &liveCycleReads{}
	client, feed, cleanup := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return reads.record(params)
		})
	})
	defer cleanup()
	m := newHubModel(client, "")
	m.frames = feed
	ref := mustParseRef(t, "local:01EVENER")

	updated, _ := m.Update(hubSessionMsg{ref: ref.String(), err: rebuildingEntryError(t)})
	armed := updated.(hubModel)
	oldSeq := armed.entryRepairSeq

	// A navigation generation bump the retry never caught (the pre-fix
	// cycling shape — the press retires immediately now, so this
	// constructs the defensive case directly).
	armed.liveNavSeq++

	freshClient, freshFeed, cleanup2 := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return reads.record(params)
		})
	})
	defer cleanup2()
	after, _ := armed.Update(hubReconnectMsg{client: freshClient, frames: freshFeed})
	rebound := after.(hubModel)

	// The superseded retry's own tick lands: it retires, entering nothing —
	// not a rebind that makes abandoned navigation current again.
	afterTick, tickCmd := rebound.Update(hubEntryRepairTickMsg{ref: ref, seq: oldSeq})
	ticked := afterTick.(hubModel)
	if ticked.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("the reconnect revived a superseded entry retry")
	}
	if tickCmd != nil {
		t.Fatal("the superseded retry's tick re-issued a read after the reconnect")
	}
}

// A spawn's own entry read may answer rebuilding: the session was just
// created and the user has not departed anywhere — the form still being
// displayed is not a departure, and the retry must converge the entry.
func TestASpawnLandingEntryMayRetryWhileTheFormStands(t *testing.T) {
	captureEntryRepairWaits(t)
	reads := &liveCycleReads{wireErrorByRef: map[string]appwire.WireError{
		"local:01S": appwire.WithHistoryReadIdentity(appwire.TranscriptIndexRebuilding(), appwire.DaemonlessBootGeneration, 0),
	}}
	m, cleanup := newEntryPickModel(t, reads)
	defer cleanup()
	refS := mustParseRef(t, "local:01S")

	// The user opens the spawn form and the spawn succeeds: its entry read
	// is issued while the form still stands.
	opened, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	spawned, read := opened.(hubModel).Update(hubSpawnMsg{resp: hubSpawnResponse{Ref: refS.String()}})
	if read == nil {
		t.Fatal("the spawn landing issued no entry read")
	}

	// The entry read answers rebuilding, tagged as the spawn's pick: the
	// retry arms.
	answer := read().(hubSessionMsg)
	armed, tick := spawned.Update(answer)
	armedModel := armed.(hubModel)
	if armedModel.entryRepairRef != refS {
		t.Fatalf("the spawn's rebuilding entry did not arm: ref = %v", armedModel.entryRepairRef)
	}

	// The retry's first tick must re-read — the standing form is not a
	// departure — and the eventual success enters the spawned session.
	afterTick, reread := armedModel.Update(executeEntryRepairCmd(t, tick))
	if reread == nil {
		t.Fatal("the spawn's entry retry died at its first tick: the standing form was read as a departure")
	}
	success := hubSessionMsg{
		ref:           refS.String(),
		detail:        hubSessionDetail{Ref: refS.String(), SessionID: "sess_s", State: appwire.ThreadStatusIdle},
		entryRetrySeq: afterTick.(hubModel).entryRepairSeq,
	}
	entered, _ := afterTick.(hubModel).Update(success)
	shown := entered.(hubModel)
	if shown.mode != hubModeSession || shown.detail.Ref != refS.String() {
		t.Fatalf("the spawned session never entered: mode = %v detail = %v", shown.mode, shown.detail.Ref)
	}
	if shown.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("the entered spawn's retry stayed armed")
	}
}

// A picked read supersedes an in-flight cycle: the obsolete pending target
// retires at issuance, so a stale cycling response reconciles the displayed
// session's subscription instead of deferring forever on it, and the next
// press steps from the session actually displayed.
func TestAPickedReadRetiresTheSupersededCyclingTarget(t *testing.T) {
	captureEntryRepairWaits(t)
	m, _, cleanup := newLiveCycleModel(t, "local:01C", liveCycleTree())
	defer cleanup()
	refB := mustParseRef(t, "local:01B")

	// A cycling press is in flight for its adjacent target when the user
	// palette-picks another session.
	afterPress, _ := m.updateSessionKey(tea.KeyMsg{Type: tea.KeyShiftRight, Alt: true})
	pressed := afterPress.(hubModel)
	if pressed.liveNavPendingRef == "" {
		t.Fatal("setup: the cycling press set no pending target")
	}
	cycleSeq := pressed.liveNavSeq
	cycleTarget := pressed.liveNavPendingRef

	// The pick supersedes the cycle: its pending target retires with the
	// issuance.
	picked := pressed
	picked.issuePickedSessionRead(refB)
	if picked.liveNavPendingRef != "" {
		t.Fatalf("the pick left the superseded cycling target %q as the stepping base", picked.liveNavPendingRef)
	}

	// The picked read answers rebuilding while the stale cycling response
	// is still in flight: the stale response must reconcile the displayed
	// session's subscription — not defer on the retired target forever.
	afterPick, _ := picked.Update(hubSessionMsg{ref: refB.String(), err: rebuildingEntryError(t)})
	armed := afterPick.(hubModel)
	staleCycle := hubSessionMsg{
		ref:        cycleTarget,
		detail:     hubSessionDetail{Ref: cycleTarget, SessionID: "sess_cycle", State: appwire.ThreadStatusIdle},
		liveNavSeq: cycleSeq,
	}
	afterStale, reconcile := armed.Update(staleCycle)
	stale := afterStale.(hubModel)
	if stale.detail.Ref != "local:01C" {
		t.Fatalf("the stale cycling response entered abandoned navigation: detail = %v", stale.detail.Ref)
	}
	if reconcile == nil {
		t.Fatal("the stale cycling response deferred reconciliation on the retired pending target")
	}
}

// An open overlay is the user's newer interaction with the displayed
// session: the automatic completion holds for it exactly as the focus trap
// holds navigation — the panel or picker survives, the retry retires, and
// the re-pointed subscription is restored.
func TestASuccessfulRetryReadHoldsForAnOpenOverlay(t *testing.T) {
	captureEntryRepairWaits(t)
	reads := &liveCycleReads{}
	client, feed, cleanup := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return reads.record(params)
		})
	})
	defer cleanup()
	m := newHubModel(client, "")
	m.frames = feed
	refA := mustParseRef(t, "local:01A")
	refB := mustParseRef(t, "local:01B")

	// Session A is displayed with its /details panel open when the user
	// palette-picked B and its entry went into repair.
	enteredA, _ := m.Update(hubSessionMsg{
		ref:    refA.String(),
		detail: hubSessionDetail{Ref: refA.String(), SessionID: "sess_a", State: appwire.ThreadStatusIdle},
	})
	shown := enteredA.(hubModel)
	panel := hubSessionPanel{Body: "the details the user is reading"}
	shown.sessionPanel = &panel
	updated, cmd := shown.Update(hubSessionMsg{ref: refB.String(), err: rebuildingEntryError(t)})
	armed := updated.(hubModel)
	armedSeq := armed.entryRepairSeq
	armed.Update(executeEntryRepairCmd(t, cmd)) // B's re-read in flight

	// B's re-read succeeds while the panel stands: the hold fires.
	late := hubSessionMsg{
		ref:           refB.String(),
		detail:        hubSessionDetail{Ref: refB.String(), SessionID: "sess_b", State: appwire.ThreadStatusIdle},
		entryRetrySeq: armedSeq,
	}
	afterLate, reestablish := armed.Update(late)
	held := afterLate.(hubModel)
	if held.mode != hubModeSession || held.detail.Ref != refA.String() {
		t.Fatalf("the automatic entry switched away from an open overlay: mode = %v detail = %v", held.mode, held.detail.Ref)
	}
	if held.sessionPanel == nil {
		t.Fatal("the automatic entry cleared the panel the user was reading")
	}
	if held.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("the held entry retry stayed armed after declining the switch")
	}
	if reestablish == nil {
		t.Fatal("the declined read re-pointed the subscription but re-established nothing")
	}
}

// Closing the spawn form with Esc is a departure from the spawn surface: the
// pending entry retry retires with it, and neither its pending tick nor its
// in-flight read may enter the spawned session the user walked away from.
func TestEscClosingTheSpawnFormRetiresThePendingEntryRetry(t *testing.T) {
	captureEntryRepairWaits(t)
	reads := &liveCycleReads{wireErrorByRef: map[string]appwire.WireError{
		"local:01S": appwire.WithHistoryReadIdentity(appwire.TranscriptIndexRebuilding(), appwire.DaemonlessBootGeneration, 0),
	}}
	m, cleanup := newEntryPickModel(t, reads)
	defer cleanup()
	refS := mustParseRef(t, "local:01S")

	// The spawn's entry read answers rebuilding with the form standing: the
	// retry arms (round 14's rule).
	opened, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	spawned, read := opened.(hubModel).Update(hubSpawnMsg{resp: hubSpawnResponse{Ref: refS.String()}})
	answer := read().(hubSessionMsg)
	armed, _ := spawned.Update(answer)
	armedModel := armed.(hubModel)
	if armedModel.entryRepairRef != refS {
		t.Fatalf("setup: the spawn's rebuilding entry did not arm: ref = %v", armedModel.entryRepairRef)
	}
	armedSeq := armedModel.entryRepairSeq

	// The user presses Esc: the form closes, the retry retires.
	afterEsc, _ := armedModel.updateKey(tea.KeyMsg{Type: tea.KeyEsc})
	closed := afterEsc.(hubModel)
	if closed.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("esc closing the spawn form left the entry retry armed")
	}

	// The retired retry's pending tick enters nothing.
	afterTick, tickCmd := closed.Update(hubEntryRepairTickMsg{ref: refS, seq: armedSeq})
	if tickCmd != nil {
		t.Fatal("the retired retry's tick re-issued a read after the esc departure")
	}
	ticked := afterTick.(hubModel)

	// The in-flight re-read's late success is discarded with it: the
	// spawned session is not entered from the dashboard the user chose.
	late := hubSessionMsg{
		ref:           refS.String(),
		detail:        hubSessionDetail{Ref: refS.String(), SessionID: "sess_s", State: appwire.ThreadStatusIdle},
		entryRetrySeq: armedSeq,
	}
	afterLate, lateCmd := ticked.Update(late)
	ended := afterLate.(hubModel)
	if ended.mode != hubModeDashboard || ended.detail.Ref != "" {
		t.Fatalf("the in-flight read entered the spawned session after the esc departure: mode = %v detail = %v", ended.mode, ended.detail.Ref)
	}
	if ended.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("the in-flight read rearmed the retired retry")
	}
	if lateCmd != nil {
		t.Fatal("the discarded read scheduled work from the dashboard")
	}
}

// A superseded retry read's discard defers its subscription reconciliation
// while a newer cycling read is still pending: the recovery must not race
// the newer read's own replacement. The newer navigation's landing settles
// the recorded obligation through the tagged reconcile.
func TestASupersededDiscardDefersWhileACycleIsPending(t *testing.T) {
	captureEntryRepairWaits(t)
	m, _, cleanup := newLiveCycleModel(t, "local:01C", liveCycleTree())
	defer cleanup()
	refA := mustParseRef(t, "local:01A")

	// A palette pick of A answers rebuilding with C displayed: the retry
	// arms, and its re-read goes in flight.
	updated, cmd := m.Update(hubSessionMsg{ref: refA.String(), err: rebuildingEntryError(t)})
	armed := updated.(hubModel)
	armedSeq := armed.entryRepairSeq
	armed.Update(executeEntryRepairCmd(t, cmd))

	// The user cycles: the press retires the retry and puts a newer
	// cycling read in flight.
	afterPress, _ := armed.updateSessionKey(tea.KeyMsg{Type: tea.KeyShiftRight, Alt: true})
	pressed := afterPress.(hubModel)
	cycleSeq := pressed.liveNavSeq
	cycleTarget := pressed.liveNavPendingRef

	// A's superseded re-read lands: discarded, and its reconciliation
	// DEFERS on the pending cycle — no recovery read races the newer
	// navigation's replacement.
	afterStale, staleCmd := pressed.Update(hubSessionMsg{
		ref:           refA.String(),
		detail:        hubSessionDetail{Ref: refA.String(), SessionID: "sess_a", State: appwire.ThreadStatusIdle},
		entryRetrySeq: armedSeq,
	})
	stale := afterStale.(hubModel)
	if staleCmd != nil {
		t.Fatal("the superseded discard issued a recovery read that races the pending cycle")
	}
	if !stale.liveNavNeedsReconcile {
		t.Fatal("the superseded discard dropped its reconciliation obligation instead of recording it")
	}

	// The cycle's response lands and settles: B enters, and the recorded
	// obligation issues the tagged reconcile for the entered session.
	entered, settleCmd := stale.Update(hubSessionMsg{
		ref:        cycleTarget,
		detail:     hubSessionDetail{Ref: cycleTarget, SessionID: "sess_cycle", State: appwire.ThreadStatusIdle},
		liveNavSeq: cycleSeq,
	})
	shown := entered.(hubModel)
	if shown.mode != hubModeSession || shown.detail.Ref != cycleTarget {
		t.Fatalf("the cycle's response did not enter its target: mode = %v detail = %v", shown.mode, shown.detail.Ref)
	}
	if shown.liveNavNeedsReconcile {
		t.Fatal("the settle point did not consume the recorded obligation")
	}
	settled := false
	if settleCmd != nil {
		for _, c := range flattenBatch(t, settleCmd) {
			if recovery, ok := c().(hubSessionMsg); ok && recovery.liveNavRecovery && recovery.ref == cycleTarget {
				settled = true
			}
		}
	}
	if !settled {
		t.Fatal("the settle point issued no tagged reconcile for the entered session")
	}
}

// flattenBatch expands a command that may be a tea.Batch into its members so
// a test can inspect every command a landing returned.
func flattenBatch(t *testing.T, cmd tea.Cmd) []tea.Cmd {
	t.Helper()
	var out []tea.Cmd
	var walk func(tea.Cmd)
	walk = func(c tea.Cmd) {
		if c == nil {
			return
		}
		msg := c()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, member := range batch {
				walk(member)
			}
			return
		}
		out = append(out, func() tea.Msg { return msg })
	}
	walk(cmd)
	return out
}

// The defer covers a pending picked read the same way: a superseded retry
// read discarded while a pick is in flight defers, and the pick's landing
// settles the obligation.
func TestASupersededDiscardDefersWhileAPickIsPending(t *testing.T) {
	captureEntryRepairWaits(t)
	reads := &liveCycleReads{}
	client, feed, cleanup := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return reads.record(params)
		})
	})
	defer cleanup()
	m := newHubModel(client, "")
	m.frames = feed
	refA := mustParseRef(t, "local:01A")
	refB := mustParseRef(t, "local:01B")

	// Session C is displayed when a palette pick of A answers rebuilding:
	// the retry arms and its re-read goes in flight.
	enteredC, _ := m.Update(hubSessionMsg{
		ref:    "local:01C",
		detail: hubSessionDetail{Ref: "local:01C", SessionID: "sess_c", State: appwire.ThreadStatusIdle},
	})
	watching := enteredC.(hubModel)
	updated, cmd := watching.Update(hubSessionMsg{ref: refA.String(), err: rebuildingEntryError(t)})
	armed := updated.(hubModel)
	armedSeq := armed.entryRepairSeq
	armed.Update(executeEntryRepairCmd(t, cmd))

	// The user palette-picks B: the pick site retires the retry and puts
	// B's read in flight.
	picked := armed
	picked.entryRepairRetire()
	pickCmd := picked.issuePickedSessionRead(refB)
	pickSeq := picked.liveNavSeq

	// A's superseded re-read lands: discarded, deferring on the pending
	// pick — no recovery read races B's replacement.
	afterStale, staleCmd := picked.Update(hubSessionMsg{
		ref:           refA.String(),
		detail:        hubSessionDetail{Ref: refA.String(), SessionID: "sess_a", State: appwire.ThreadStatusIdle},
		entryRetrySeq: armedSeq,
	})
	stale := afterStale.(hubModel)
	if staleCmd != nil {
		t.Fatal("the superseded discard issued a recovery read that races the pending pick")
	}
	if !stale.liveNavNeedsReconcile {
		t.Fatal("the superseded discard dropped its reconciliation obligation instead of recording it")
	}
	if stale.liveNavEntryPendingSeq != pickSeq {
		t.Fatalf("the pick's pending generation = %d, want the issuance generation %d", stale.liveNavEntryPendingSeq, pickSeq)
	}

	// B's pick response lands and settles: B enters with the obligation
	// consumed, the tagged reconcile issued for it.
	entered, settleCmd := stale.Update(pickCmd().(hubSessionMsg))
	shown := entered.(hubModel)
	if shown.mode != hubModeSession || shown.detail.Ref != refB.String() {
		t.Fatalf("the pick's response did not enter B: mode = %v detail = %v", shown.mode, shown.detail.Ref)
	}
	if shown.liveNavNeedsReconcile {
		t.Fatal("the settle point did not consume the recorded obligation")
	}
	if shown.liveNavEntryPendingSeq != 0 {
		t.Fatal("the pick's pending generation outlived its landing")
	}
	settled := false
	if settleCmd != nil {
		for _, c := range flattenBatch(t, settleCmd) {
			if recovery, ok := c().(hubSessionMsg); ok && recovery.liveNavRecovery && recovery.ref == refB.String() {
				settled = true
			}
		}
	}
	if !settled {
		t.Fatal("the settle point issued no tagged reconcile for the entered session")
	}
}

// A cycling press supersedes a picked read: after the cycle lands, the
// obsolete pick's late response must not defer reconciliation on its stale
// pending generation — the displayed session's subscription is repaired
// immediately, since no current navigation remains in flight.
func TestASupersededPickResponseAfterTheCycleLandsReconcilesImmediately(t *testing.T) {
	captureEntryRepairWaits(t)
	m, _, cleanup := newLiveCycleModel(t, "local:01C", liveCycleTree())
	defer cleanup()
	refB := mustParseRef(t, "local:01B")

	// The user picks B, then cycles before B's read answers.
	picked := m
	pickCmd := picked.issuePickedSessionRead(refB)

	// The cycle supersedes the pick; its response lands and enters the
	// cycle's target.
	afterPress, _ := picked.updateSessionKey(tea.KeyMsg{Type: tea.KeyShiftRight, Alt: true})
	pressed := afterPress.(hubModel)
	cycleSeq := pressed.liveNavSeq
	cycleTarget := pressed.liveNavPendingRef
	entered, _ := pressed.Update(hubSessionMsg{
		ref:        cycleTarget,
		detail:     hubSessionDetail{Ref: cycleTarget, SessionID: "sess_cycle", State: appwire.ThreadStatusIdle},
		liveNavSeq: cycleSeq,
	})
	shown := entered.(hubModel)
	if shown.detail.Ref != cycleTarget {
		t.Fatalf("setup: the cycle's response did not enter its target: %v", shown.detail.Ref)
	}

	// The superseded pick's late response lands: discarded, and — with the
	// cycle settled and the pick's generation obsolete — reconciled NOW,
	// not deferred on the stale pending generation forever.
	afterLate, lateCmd := shown.Update(pickCmd().(hubSessionMsg))
	late := afterLate.(hubModel)
	if late.liveNavNeedsReconcile {
		t.Fatal("the superseded pick's response deferred reconciliation on its obsolete pending generation")
	}
	if lateCmd == nil {
		t.Fatal("the superseded pick's response did not reconcile the displayed session's subscription")
	}
	recovery, ok := lateCmd().(hubSessionMsg)
	if !ok || !recovery.liveNavRecovery || recovery.ref != cycleTarget {
		t.Fatalf("the reconciliation produced %T, want the displayed session's recovery read", lateCmd())
	}
}

// A stale cycling response defers while the newer PICK is pending: the old
// branches checked only the cycling target, which the pick cleared at
// issuance — the recovery must not race the pick's own replacement, and
// the pick's landing settles the recorded obligation.
func TestAStaleCycleResponseDefersWhileTheNewerPickIsPending(t *testing.T) {
	captureEntryRepairWaits(t)
	m, _, cleanup := newLiveCycleModel(t, "local:01C", liveCycleTree())
	defer cleanup()
	refB := mustParseRef(t, "local:01B")

	// A cycling press is in flight when the user picks B: the pick
	// supersedes the cycle and puts B's read in flight.
	afterPress, _ := m.updateSessionKey(tea.KeyMsg{Type: tea.KeyShiftRight, Alt: true})
	pressed := afterPress.(hubModel)
	cycleSeq := pressed.liveNavSeq
	cycleTarget := pressed.liveNavPendingRef
	pickCmd := pressed.issuePickedSessionRead(refB)

	// The stale cycling response lands: discarded, and its reconciliation
	// DEFERS on the pending pick — no recovery read races B's replacement.
	afterStale, staleCmd := pressed.Update(hubSessionMsg{
		ref:        cycleTarget,
		detail:     hubSessionDetail{Ref: cycleTarget, SessionID: "sess_cycle", State: appwire.ThreadStatusIdle},
		liveNavSeq: cycleSeq,
	})
	stale := afterStale.(hubModel)
	if staleCmd != nil {
		t.Fatal("the stale cycling response issued a recovery read that races the pending pick")
	}
	if !stale.liveNavNeedsReconcile {
		t.Fatal("the stale cycling response dropped its reconciliation obligation instead of recording it")
	}

	// B's response lands and settles: B enters with the obligation consumed.
	entered, settleCmd := stale.Update(pickCmd().(hubSessionMsg))
	shown := entered.(hubModel)
	if shown.mode != hubModeSession || shown.detail.Ref != refB.String() {
		t.Fatalf("the pick's response did not enter B: mode = %v detail = %v", shown.mode, shown.detail.Ref)
	}
	if shown.liveNavNeedsReconcile {
		t.Fatal("the settle point did not consume the recorded obligation")
	}
	settled := false
	if settleCmd != nil {
		for _, c := range flattenBatch(t, settleCmd) {
			if recovery, ok := c().(hubSessionMsg); ok && recovery.liveNavRecovery && recovery.ref == refB.String() {
				settled = true
			}
		}
	}
	if !settled {
		t.Fatal("the settle point issued no tagged reconcile for the entered session")
	}
}

// The retry's own re-read is a flight reconciliation must respect: an older
// retry response landing while a newer retry read is running defers, and
// the newer read's landing settles the obligation.
func TestAnOlderRetryResponseDefersWhileTheNewerRetryReadRuns(t *testing.T) {
	captureEntryRepairWaits(t)
	reads := &liveCycleReads{}
	client, feed, cleanup := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return reads.record(params)
		})
	})
	defer cleanup()
	m := newHubModel(client, "")
	m.frames = feed
	refA := mustParseRef(t, "local:01A")

	// Session C is displayed when a palette pick of A answers rebuilding:
	// the retry arms and its first re-read goes in flight.
	enteredC, _ := m.Update(hubSessionMsg{
		ref:    "local:01C",
		detail: hubSessionDetail{Ref: "local:01C", SessionID: "sess_c", State: appwire.ThreadStatusIdle},
	})
	watching := enteredC.(hubModel)
	updated, cmd := watching.Update(hubSessionMsg{ref: refA.String(), err: rebuildingEntryError(t)})
	armed := updated.(hubModel)
	oldSeq := armed.entryRepairSeq
	armed.Update(executeEntryRepairCmd(t, cmd)) // the older re-read in flight

	// A reconnect rebinds the retry and paces a fresh tick: its re-read is
	// the newer flight.
	freshClient, freshFeed, cleanup2 := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return reads.record(params)
		})
	})
	defer cleanup2()
	after, _ := armed.Update(hubReconnectMsg{client: freshClient, frames: freshFeed})
	rebound := after.(hubModel)
	newSeq := rebound.entryRepairSeq
	afterTick, reread := rebound.Update(hubEntryRepairTickMsg{ref: refA, seq: newSeq})
	if reread == nil {
		t.Fatal("setup: the rebound tick issued no read")
	}

	// The older re-read's late response lands, tagged with the superseded
	// generation: discarded, and its reconciliation DEFERS on the newer
	// retry read's flight — no recovery read races it.
	afterOld, oldCmd := afterTick.(hubModel).Update(hubSessionMsg{
		ref:           refA.String(),
		err:           rebuildingEntryError(t),
		entryRetrySeq: oldSeq,
	})
	stale := afterOld.(hubModel)
	if oldCmd != nil {
		t.Fatal("the older retry response issued a recovery read that races the newer retry's read")
	}
	if !stale.liveNavNeedsReconcile {
		t.Fatal("the older retry response dropped its reconciliation obligation instead of recording it")
	}

	// The newer re-read succeeds: it enters its target with the obligation
	// settled through the tagged reconcile.
	msg := reread().(hubSessionMsg)
	entered, settleCmd := stale.Update(msg)
	shown := entered.(hubModel)
	if shown.mode != hubModeSession || shown.detail.Ref != refA.String() {
		t.Fatalf("the newer re-read did not enter its target: mode = %v detail = %v", shown.mode, shown.detail.Ref)
	}
	if shown.liveNavNeedsReconcile {
		t.Fatal("the newer read's landing did not consume the recorded obligation")
	}
	settled := false
	if settleCmd != nil {
		for _, c := range flattenBatch(t, settleCmd) {
			if recovery, ok := c().(hubSessionMsg); ok && recovery.liveNavRecovery && recovery.ref == refA.String() {
				settled = true
			}
		}
	}
	if !settled {
		t.Fatal("the landing issued no tagged reconcile for the entered session")
	}
}

// A navigation that ends in a terminal error must not strand the
// reconciliation obligation an earlier superseded read recorded while this
// read was pending: the displayed session's subscription is repaired now,
// with the terminal error still surfacing.
func TestATerminalNavigationErrorSettlesTheDeferredReconciliation(t *testing.T) {
	captureEntryRepairWaits(t)
	reads := &liveCycleReads{}
	client, feed, cleanup := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return reads.record(params)
		})
	})
	defer cleanup()
	m := newHubModel(client, "")
	m.frames = feed
	refA := mustParseRef(t, "local:01A")
	refB := mustParseRef(t, "local:01B")

	// Session C is displayed when the user picks A, then B before either
	// answers.
	enteredC, _ := m.Update(hubSessionMsg{
		ref:    "local:01C",
		detail: hubSessionDetail{Ref: "local:01C", SessionID: "sess_c", State: appwire.ThreadStatusIdle},
	})
	watching := enteredC.(hubModel)
	pickA := watching.issuePickedSessionRead(refA)
	watching.issuePickedSessionRead(refB)

	// A's superseded response lands first: discarded, its reconciliation
	// deferred on B's pending flight.
	afterA, _ := watching.Update(pickA().(hubSessionMsg))
	deferred := afterA.(hubModel)
	if !deferred.liveNavNeedsReconcile {
		t.Fatal("setup: the superseded pick's discard recorded no obligation")
	}

	// B's answer is terminal — the session was deleted while its repair
	// ran: the navigation ends, and the recorded obligation settles NOW
	// (no landing is left to settle it), with the deletion still shown.
	// The delivery carries B's own pick tags: a tracked flight's conclusion
	// is the only shape that can carry a deferred obligation.
	terminal := targetDeletedEntryError(t)
	afterB, settle := deferred.Update(hubSessionMsg{
		ref:          refB.String(),
		err:          terminal,
		liveNavSeq:   deferred.liveNavSeq,
		liveNavEntry: true,
	})
	ended := afterB.(hubModel)
	if ended.liveNavNeedsReconcile {
		t.Fatal("the terminal navigation stranded the deferred reconciliation")
	}
	if ended.mode != hubModeSession || ended.detail.Ref != "local:01C" {
		t.Fatalf("the terminal answer moved the model: mode = %v detail = %v", ended.mode, ended.detail.Ref)
	}
	if settle == nil {
		t.Fatal("the terminal navigation settled no reconciliation for the displayed session")
	}
	recovery, ok := settle().(hubSessionMsg)
	if !ok || !recovery.liveNavRecovery || recovery.ref != "local:01C" {
		t.Fatalf("the settlement produced %T, want the displayed session's recovery read", settle())
	}
	if wireMutationOutcome(ended.err) != appwire.MutationOutcomeTargetDeleted {
		t.Fatalf("model error = %v, want the deletion answer still shown", ended.err)
	}
	if got := reads.get(); got[len(got)-1] != "local:01C" {
		t.Fatalf("recovery read %v, want the displayed session re-read", got)
	}
}

// The retry's own re-read is a tracked flight too: an obligation deferred
// onto it (a stale cycle landing while the re-read ran) must settle when the
// re-read's answer is terminal, or it outlives every flight — the re-read arm
// of the round-18 medium's settlement.
func TestATerminalRetryRereadSettlesTheDeferredReconciliation(t *testing.T) {
	captureEntryRepairWaits(t)
	m, _, cleanup := newLiveCycleModel(t, "local:01C", liveCycleTree())
	defer cleanup()
	refA := mustParseRef(t, "local:01A")

	// A cycling press is in flight when the user picks A: the pick
	// supersedes the cycle and puts A's read in flight.
	afterPress, _ := m.updateSessionKey(tea.KeyMsg{Type: tea.KeyShiftRight, Alt: true})
	pressed := afterPress.(hubModel)
	cycleSeq := pressed.liveNavSeq
	cycleTarget := pressed.liveNavPendingRef
	pressed.issuePickedSessionRead(refA)

	// A's response is the rebuilding rejection: the retry arms over the
	// displayed C, its re-read pending — and with nothing deferred yet,
	// the arming settles nothing.
	afterArm, armCmd := pressed.Update(hubSessionMsg{
		ref:          refA.String(),
		err:          rebuildingEntryError(t),
		liveNavSeq:   pressed.liveNavSeq,
		liveNavEntry: true,
	})
	armed := afterArm.(hubModel)
	if armed.entryRepairRef != refA {
		t.Fatal("setup: the rebuilding answer did not arm the retry")
	}
	if armed.liveNavNeedsReconcile {
		t.Fatal("setup: the arming settled an obligation nothing had recorded")
	}

	// The re-read goes in flight, and the stale cycling response lands
	// while it runs: discarded, its reconciliation deferred onto the
	// pending re-read — the flight whose landing must settle it.
	afterTick, reReadCmd := armed.Update(executeEntryRepairCmd(t, armCmd))
	ticking := afterTick.(hubModel)
	if reReadCmd == nil {
		t.Fatal("setup: the retry's tick re-issued no read")
	}
	afterStale, staleCmd := ticking.Update(hubSessionMsg{
		ref:        cycleTarget,
		detail:     hubSessionDetail{Ref: cycleTarget, SessionID: "sess_cycle", State: appwire.ThreadStatusIdle},
		liveNavSeq: cycleSeq,
	})
	stale := afterStale.(hubModel)
	if staleCmd != nil {
		t.Fatal("the stale cycling response issued a recovery read that races the pending re-read")
	}
	if !stale.liveNavNeedsReconcile {
		t.Fatal("setup: the stale cycling response recorded no obligation")
	}

	// The re-read's answer is terminal — A was deleted while its repair
	// ran: the retry ends, and the obligation settles NOW, with the
	// deletion still shown and C still displayed.
	terminal := reReadCmd().(hubSessionMsg)
	terminal.err = targetDeletedEntryError(t)
	afterEnd, settle := stale.Update(terminal)
	ended := afterEnd.(hubModel)
	if ended.liveNavNeedsReconcile {
		t.Fatal("the terminal re-read stranded the deferred reconciliation")
	}
	if ended.entryRepairRef != (appwire.Ref{}) {
		t.Fatal("the terminal re-read left the retry armed")
	}
	if ended.mode != hubModeSession || ended.detail.Ref != "local:01C" {
		t.Fatalf("the terminal re-read moved the model: mode = %v detail = %v", ended.mode, ended.detail.Ref)
	}
	recovery, ok := settle().(hubSessionMsg)
	if !ok || !recovery.liveNavRecovery || recovery.ref != "local:01C" {
		t.Fatalf("the settlement produced %T, want the displayed session's recovery read", settle())
	}
	if wireMutationOutcome(ended.err) != appwire.MutationOutcomeTargetDeleted {
		t.Fatalf("model error = %v, want the deletion answer still shown", ended.err)
	}
}

// A recovery read issued during the retry's backoff shares the armed
// generation: the retry's own re-read must fence it with a fresh one, or the
// recovery's late landing is accepted as current and the retry enters its
// target with the server-side subscriptions still pointing where the
// recovery left them (round-20 medium 1).
func TestTheRetryRereadFencesARecoveryIssuedDuringBackoff(t *testing.T) {
	captureEntryRepairWaits(t)
	m, _, cleanup := newLiveCycleModel(t, "local:01C", liveCycleTree())
	defer cleanup()
	refA := mustParseRef(t, "local:01A")

	// A's pick answers rebuilding: the retry arms over the displayed C,
	// its tick pending — the backoff window.
	updated, retryCmd := m.Update(hubSessionMsg{ref: refA.String(), err: rebuildingEntryError(t)})
	armed := updated.(hubModel)
	if armed.entryRepairRef != refA {
		t.Fatal("setup: the rebuilding answer did not arm the retry")
	}

	// During the backoff, a displayed-session recovery read issues (the
	// stale-read reconciliation path): it shares the armed generation.
	armedRecovery, recoveryCmd := armed.reestablishDisplayedSubscription()
	if recoveryCmd == nil {
		t.Fatal("setup: the recovery read issued nothing")
	}

	// The retry's tick re-issues the entry read — fencing the in-flight
	// recovery with a fresh generation.
	tick := executeEntryRepairCmd(t, retryCmd)
	afterTick, rereadCmd := armedRecovery.Update(tick)
	flying := afterTick.(hubModel)
	if rereadCmd == nil {
		t.Fatal("setup: the tick re-issued no entry read")
	}

	// The recovery's response lands first: superseded by the re-read's
	// generation, discarded, and its reconciliation DEFERS on the re-read's
	// flight — it must not be accepted as current over the retry.
	recoveryMsg := recoveryCmd().(hubSessionMsg)
	afterRecovery, recoveryFollow := flying.Update(recoveryMsg)
	deferred := afterRecovery.(hubModel)
	if recoveryFollow != nil {
		t.Fatal("the recovery's landing was accepted as current and issued follow-ups that race the retry's re-read")
	}
	if !deferred.liveNavNeedsReconcile {
		t.Fatal("the superseded recovery's landing recorded no reconciliation obligation")
	}

	// The re-read succeeds: A enters, and the settle point restores the
	// displayed session's subscription.
	rereadMsg := rereadCmd().(hubSessionMsg)
	entered, settleCmd := deferred.Update(rereadMsg)
	shown := entered.(hubModel)
	if shown.mode != hubModeSession || shown.detail.Ref != refA.String() {
		t.Fatalf("the re-read did not enter its target: mode = %v detail = %v", shown.mode, shown.detail.Ref)
	}
	if shown.liveNavNeedsReconcile {
		t.Fatal("the re-read's landing did not settle the recorded obligation")
	}
	settled := false
	if settleCmd != nil {
		for _, c := range flattenBatch(t, settleCmd) {
			if recovery, ok := c().(hubSessionMsg); ok && recovery.liveNavRecovery && recovery.ref == refA.String() {
				settled = true
			}
		}
	}
	if !settled {
		t.Fatal("the landing issued no tagged reconcile for the entered session")
	}
}

// A pick's rebuilding answer settles the deferred reconciliation its
// flight inherited: with C displayed and picks of A then B in flight, A's
// superseded success leaves the subscription re-pointed while the
// obligation defers onto B — and B's rebuilding landing must reconcile C
// alongside arming its paced retry, or C stays unsubscribed through every
// retry the repair paces (roborev PR #4497 round-25 medium 1).
func TestAPicksRebuildingAnswerReconcilesTheDisplayedSessionAlongsideItsRetry(t *testing.T) {
	captureEntryRepairWaits(t)
	reads := &liveCycleReads{}
	client, feed, cleanup := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return reads.record(params)
		})
	})
	defer cleanup()
	m := newHubModel(client, "")
	m.frames = feed
	refA := mustParseRef(t, "local:01A")
	refB := mustParseRef(t, "local:01B")

	enteredC, _ := m.Update(hubSessionMsg{
		ref:    "local:01C",
		detail: hubSessionDetail{Ref: "local:01C", SessionID: "sess_c", State: appwire.ThreadStatusIdle},
	})
	watching := enteredC.(hubModel)

	// The user palette-picks A, then B: B's issuance supersedes A's
	// pending pick.
	staged := watching
	pickA := staged.issuePickedSessionRead(refA)
	msgA := pickA().(hubSessionMsg)
	pickB := staged.issuePickedSessionRead(refB)
	msgB := pickB().(hubSessionMsg)
	msgB.err = rebuildingEntryError(t)

	// A's late success lands superseded: the discard defers the displayed
	// session's reconciliation onto B's pending pick.
	afterStale, staleCmd := staged.Update(msgA)
	stale := afterStale.(hubModel)
	if staleCmd != nil {
		t.Fatal("the superseded pick issued a recovery read that races the pending pick")
	}
	if !stale.liveNavNeedsReconcile {
		t.Fatal("the superseded pick dropped its reconciliation obligation instead of recording it")
	}

	// B's landing answers rebuilding: the paced retry arms, and the
	// inherited obligation settles NOW — C is still the displayed session,
	// and its subscription must not wait out the retries.
	after, cmd := stale.Update(msgB)
	shown := after.(hubModel)
	if shown.liveNavNeedsReconcile {
		t.Fatal("the rebuilding answer left the inherited reconciliation unconsumed")
	}
	if shown.mode != hubModeSession || shown.detail.Ref != "local:01C" {
		t.Fatalf("the rebuilding answer moved off the displayed session: mode = %v detail = %v", shown.mode, shown.detail.Ref)
	}
	if shown.entryRepairRef != refB {
		t.Fatalf("the rebuilding answer disarmed its retry: entryRepairRef = %v, want %v", shown.entryRepairRef, refB)
	}
	reconciled := false
	retried := false
	if cmd != nil {
		for _, c := range flattenBatch(t, cmd) {
			msg := c()
			if recovery, ok := msg.(hubSessionMsg); ok && recovery.liveNavRecovery && recovery.ref == "local:01C" {
				reconciled = true
			}
			if _, ok := msg.(hubEntryRepairTickMsg); ok {
				retried = true
			}
		}
	}
	if !reconciled {
		t.Fatal("the rebuilding answer issued no reconcile for the displayed session")
	}
	if !retried {
		t.Fatal("the rebuilding answer dropped its paced retry")
	}
}

// The retry's own re-read settles the reconciliation it inherits the same
// way: B's rebuilding answer arms the retry first (nothing deferred yet),
// the re-read goes in flight, A's superseded success defers onto it, and
// the re-read's ordinary transient — the armed-failure path's own traffic
// — must reconcile the displayed session alongside keeping the retry armed.
func TestTheArmedRetryReadReconcilesTheDisplayedSessionOnItsOwnRebuildingAnswer(t *testing.T) {
	captureEntryRepairWaits(t)
	reads := &liveCycleReads{}
	client, feed, cleanup := newTestHubClientWithFeed(t, func(app *appserver.Server) {
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return reads.record(params)
		})
	})
	defer cleanup()
	m := newHubModel(client, "")
	m.frames = feed
	refA := mustParseRef(t, "local:01A")
	refB := mustParseRef(t, "local:01B")

	enteredC, _ := m.Update(hubSessionMsg{
		ref:    "local:01C",
		detail: hubSessionDetail{Ref: "local:01C", SessionID: "sess_c", State: appwire.ThreadStatusIdle},
	})
	watching := enteredC.(hubModel)

	staged := watching
	pickA := staged.issuePickedSessionRead(refA)
	msgA := pickA().(hubSessionMsg)
	pickB := staged.issuePickedSessionRead(refB)
	msgB := pickB().(hubSessionMsg)
	msgB.err = rebuildingEntryError(t)

	// B's landing answers rebuilding before A's success lands: the retry
	// arms with nothing deferred, and its re-read goes in flight.
	afterB, cmdB := staged.Update(msgB)
	armed := afterB.(hubModel)
	if armed.entryRepairRef != refB {
		t.Fatalf("the rebuilding answer did not arm its retry: entryRepairRef = %v, want %v", armed.entryRepairRef, refB)
	}
	if armed.liveNavNeedsReconcile {
		t.Fatal("no superseded landing preceded the arming: nothing should have deferred")
	}
	afterTick, reReadCmd := armed.Update(executeEntryRepairCmd(t, cmdB))
	ticking := afterTick.(hubModel)
	if reReadCmd == nil {
		t.Fatal("the retry's tick re-issued no read")
	}

	// A's late success lands superseded by the armed generation: the
	// discard defers onto the pending re-read.
	afterStale, staleCmd := ticking.Update(msgA)
	stale := afterStale.(hubModel)
	if staleCmd != nil {
		t.Fatal("the superseded pick issued a recovery read that races the pending re-read")
	}
	if !stale.liveNavNeedsReconcile {
		t.Fatal("the superseded pick dropped its reconciliation obligation instead of recording it")
	}

	// The re-read answers an ordinary transient — mid-repair contention,
	// the armed-failure path's own traffic — tagged with its fresh
	// generation: the settle fires alongside keeping the retry, and the
	// transient rides after the status on the line.
	ordinary := errors.New("thread history is unavailable at this moment")
	reread := reReadCmd().(hubSessionMsg)
	reread.err = ordinary
	after, cmd := stale.Update(reread)
	shown := after.(hubModel)
	if shown.liveNavNeedsReconcile {
		t.Fatal("the armed failure left the inherited reconciliation unconsumed")
	}
	if !errors.Is(shown.err, ordinary) {
		t.Fatalf("the settle displaced the ordinary transient: model error = %v", shown.err)
	}
	assertStatusLine(t, shown, rebuildingEntryError(t))
	if shown.mode != hubModeSession || shown.detail.Ref != "local:01C" {
		t.Fatalf("the armed failure moved off the displayed session: mode = %v detail = %v", shown.mode, shown.detail.Ref)
	}
	if shown.entryRepairRef != refB {
		t.Fatalf("the armed failure disarmed its retry: entryRepairRef = %v, want %v", shown.entryRepairRef, refB)
	}
	reconciled := false
	retried := false
	if cmd != nil {
		for _, c := range flattenBatch(t, cmd) {
			msg := c()
			if recovery, ok := msg.(hubSessionMsg); ok && recovery.liveNavRecovery && recovery.ref == "local:01C" {
				reconciled = true
			}
			if _, ok := msg.(hubEntryRepairTickMsg); ok {
				retried = true
			}
		}
	}
	if !reconciled {
		t.Fatal("the armed failure issued no reconcile for the displayed session")
	}
	if !retried {
		t.Fatal("the armed failure dropped its paced retry")
	}
}
