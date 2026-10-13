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
	original := appwire.WithHistoryReadIdentity(appwire.TranscriptIndexRebuilding(), appwire.DaemonlessBootGeneration, 0)
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
