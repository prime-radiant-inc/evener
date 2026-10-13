package tui

import (
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
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
		// The re-read answers the rebuilding code again: the retry re-arms
		// under the grown delay.
		u2, c2 := reread.Update(hubSessionMsg{ref: ref.String(), err: rebuilding})
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
