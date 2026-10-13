package tui

import (
	"errors"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
)

// #4343's TUI contract: a session entry read rejected with the hub's typed
// transcriptIndexRebuilding code must not strand. The model arms a paced
// retry — the browser store's owner, translated to the TUI: base 500ms,
// doubling, capped at 15s, retry-forever — scoped to the current navigation
// intent, and the error line shows the repair status instead of the entry
// error while the retry stands. A history-failed answer is terminal: the
// retry retires and the error shows. The already-open surfaces (the
// displayed session's refresh re-reads, the raw transcript viewer) keep
// their existing re-entry points and arm nothing here.

const (
	// hubEntryRepairBaseDelay is the wait before the retry's first re-read.
	hubEntryRepairBaseDelay = 500 * time.Millisecond
	// hubEntryRepairCapDelay bounds every wait after the growth.
	hubEntryRepairCapDelay = 15 * time.Second
)

// hubEntryRepairWait stands between an armed retry and its tick: a real
// sleep in production, a captured instant in tests, so the pacing shape is
// asserted computationally rather than lived.
var hubEntryRepairWait = func(d time.Duration) { time.Sleep(d) }

// errHubEntryRepairing is the error line's repair status: the agreed text,
// standing in for the entry error while the retry holds. Capitalized on
// purpose — it renders as the pane's status line, not a log message.
var errHubEntryRepairing = errors.New("Repairing session history") //nolint:staticcheck // the agreed UI status text, not an error string

// hubEntryRepairTickMsg is the armed retry's landing: the navigation
// intent it was armed under has either stood — the entry read re-issues —
// or departed — the retry retires entering nothing.
type hubEntryRepairTickMsg struct {
	ref appwire.Ref
	seq int
}

// hubEntryRepairRetryCmd waits the current pace and lands the retry's tick.
func hubEntryRepairRetryCmd(delay time.Duration, ref appwire.Ref, seq int) tea.Cmd {
	return func() tea.Msg {
		hubEntryRepairWait(delay)
		return hubEntryRepairTickMsg{ref: ref, seq: seq}
	}
}

// isHubTranscriptEntryRetryable reports whether err is a typed answer a
// paced entry retry converges: the hub's rebuilding answer (a repair the
// hub owns in the background, whose commit the next read follows) or its
// lock-contention answer (an ordinary writer mid-extension or a foreign
// rebuild, with nothing registered — the TUI has no ordinary-error retry,
// so the typed answer is what keeps a first entry from stranding on it).
// Both carry the automatic retry disposition, and the status stands while
// the retry does. wireErrorInfo reads both data shapes — the typed struct
// an in-process error carries and the map the client's JSON decode
// produces — so a real wire error matches, not just a constructed one.
func isHubTranscriptEntryRetryable(err error) bool {
	switch wireErrorInfo(err) {
	case appwire.ErrorTranscriptIndexRebuilding, appwire.ErrorTranscriptIndexLocked:
		return true
	}
	return false
}

// isHubTranscriptEntryTerminal reports whether err is an answer no paced
// entry retry can converge (#4343's terminal set): the repair budget's
// history-failed answer, an upgrade-required refusal, or a target deletion —
// the permanent set the web owner reads (historyPaging's rule), with the
// budget's terminal answer. Both shapes classify: the typed form an
// in-process error carries and the map the client's JSON decode produces.
func isHubTranscriptEntryTerminal(err error) bool {
	switch wireErrorInfo(err) {
	case appwire.ErrorTranscriptHistoryFailed, appwire.ErrorUpgradeRequired:
		return true
	}
	return wireMutationOutcome(err) == appwire.MutationOutcomeTargetDeleted
}

// retryHubSessionRead re-issues the entry read for the armed retry,
// tagging its landing with the retry's generation (the livecycle wrapping
// pattern): a response that lands after the model moved on is discarded
// whole by the hubSessionMsg guard — its success must not enter abandoned
// navigation, its error must not rearm it.
func (m *hubModel) retryHubSessionRead(ref appwire.Ref) tea.Cmd {
	read := fetchHubSession(m.frames, m.client, ref)
	seq := m.entryRepairSeq
	return func() tea.Msg {
		msg := read()
		if sessionMsg, ok := msg.(hubSessionMsg); ok {
			sessionMsg.entryRetrySeq = seq
			return sessionMsg
		}
		return msg
	}
}

// entryRepairRetire ends the pending entry-repair retry and its status.
func (m *hubModel) entryRepairRetire() {
	m.entryRepairRef = appwire.Ref{}
	m.entryRepairDelay = 0
	m.entryRepairSeq = 0
	// A retired retry has no flight worth deferring for: its in-flight
	// read, when one still lands, is superseded and discarded on the
	// standing navigation's own terms (#4343 round-17 medium 3).
	m.entryRepairReadInFlight = false
}

// updateHubEntryRepairTick lands an armed retry. A tick for a superseded
// retry — another pick armed its own, or a landing retired this one — is
// ignored: retiring here would clear the standing retry's status out from
// under the session the user just picked. The matching retry's intent
// stands while its armed ref is still the armed retry and no session
// switch or navigation departure has bumped the generation — so the
// entry read re-issues, tagged, and the pace grows for the round after;
// or the intent has departed, and the retry retires entering nothing. A
// session may well be displayed while the retry stands (the palette can
// pick a session from inside another): the displayed ref is not the
// intent; the generation is.
func (m *hubModel) updateHubEntryRepairTick(msg hubEntryRepairTickMsg) (tea.Model, tea.Cmd) {
	if m.entryRepairRef != msg.ref || m.entryRepairSeq != msg.seq {
		// A tick belonging to another retry: the newer navigation owns the
		// model. Ignore it, never retire the standing one for it.
		return *m, nil
	}
	if m.entryRepairSeq != m.liveNavSeq {
		// The retry's navigation intent departed — a pick, a cycling press,
		// a departure to the dashboard or spawn form advances the
		// generation, and those paths retire the retry themselves. The
		// spawn surface no longer retires here: a spawn's own entry read
		// may answer rebuilding while the form still stands, and the
		// user has departed nowhere — the standing form is not a
		// departure, and the retry must converge the entry.
		m.entryRepairRetire()
		return *m, nil
	}
	m.entryRepairDelay = min(m.entryRepairDelay*2, hubEntryRepairCapDelay)
	// A recovery read issued during the backoff shares the armed
	// generation, so its landing would be accepted as current over this
	// flight — while its wire request re-pointed the server's
	// subscriptions after this re-read's cut. The re-read takes a fresh
	// generation: the recovery lands superseded, its reconciliation
	// obligation defers on this flight, and the landing below settles it
	// (roborev PR #1044 round-20 medium 1).
	m.liveNavSeq++
	m.entryRepairSeq = m.liveNavSeq
	// The re-read is a flight reconciliation must respect: a superseded
	// response landing while it runs defers to it (#4343 round-17 medium 3).
	m.entryRepairReadInFlight = true
	return *m, m.retryHubSessionRead(msg.ref)
}
