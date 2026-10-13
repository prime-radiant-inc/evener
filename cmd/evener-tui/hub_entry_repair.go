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

// isHubTranscriptIndexRebuilding reports whether err is the hub's typed
// answer for a daemonless read whose sidecar is being repaired in the
// background: retrying is the right response, and the read that follows
// the repair's commit succeeds. Matched on the wire-stamped and raw data
// shapes alike.
func isHubTranscriptIndexRebuilding(err error) bool {
	info, ok := hubWireErrorInfo(err)
	return ok && info == appwire.ErrorTranscriptIndexRebuilding
}

// isHubTranscriptHistoryFailed reports whether err is the repair budget's
// terminal answer: no retry converges it, so the entry shows the error.
func isHubTranscriptHistoryFailed(err error) bool {
	info, ok := hubWireErrorInfo(err)
	return ok && info == appwire.ErrorTranscriptHistoryFailed
}

// hubWireErrorInfo reads the evener discriminant out of a wire error, in
// either data shape a history read's error carries.
func hubWireErrorInfo(err error) (appwire.ErrorInfo, bool) {
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return "", false
	}
	switch data := wire.Data.(type) {
	case appwire.HistoryReadErrorData:
		return data.EvenerErrorInfo, true
	case appwire.ErrorData:
		return data.EvenerErrorInfo, true
	}
	return "", false
}

// entryRepairRetire ends the pending entry-repair retry and its status.
func (m *hubModel) entryRepairRetire() {
	m.entryRepairRef = appwire.Ref{}
	m.entryRepairDelay = 0
	m.entryRepairSeq = 0
}

// updateHubEntryRepairTick lands an armed retry. The intent it was armed
// under has stood — the armed ref is still the armed retry, no session
// switch or cycling pick has bumped the generation, and the user has not
// left for the spawn surface — so the entry read re-issues and the pace
// grows for the round after; or the intent has departed, and the retry
// retires entering nothing. A session may well be displayed while the
// retry stands (the palette can pick a session from inside another): the
// displayed ref is not the intent; the generation is.
func (m *hubModel) updateHubEntryRepairTick(msg hubEntryRepairTickMsg) (tea.Model, tea.Cmd) {
	if m.entryRepairRef != msg.ref || m.entryRepairSeq != msg.seq || m.entryRepairSeq != m.liveNavSeq || m.mode == hubModeSpawn {
		m.entryRepairRetire()
		return *m, nil
	}
	m.entryRepairDelay = min(m.entryRepairDelay*2, hubEntryRepairCapDelay)
	return *m, fetchHubSession(m.frames, m.client, msg.ref)
}
