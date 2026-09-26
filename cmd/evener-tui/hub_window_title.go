package tui

import (
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/envvars"
)

// maxWindowTitleRunes caps the OSC window title. A session name is short, but a
// remote source's preview is unbounded and would otherwise flood the title bar.
const maxWindowTitleRunes = 200

// windowTitleCmd returns a tea.SetWindowTitle command when this update moved the
// terminal title, and nil when it did not. prev is the model as it was before
// the update.
//
// The title is the active session's display name while a session view is open
// and empty on every other surface (the dashboard, the spawn form), so leaving
// a session clears it rather than leaving the window labelled with a session
// the user navigated away from. The comparison is between the title the view
// wants before and after this single update, so an ordinary keypress or
// streaming frame emits nothing, while entering or leaving a session, switching
// sessions, and a live rename (a user rename, the auto-namer's first name, or a
// compaction refresh) each emit exactly once. No cached "last set" value is
// kept, so there is no state that can go stale and silently suppress a later
// correction.
//
// tmux reflects OSC title escapes only for a window with allow-rename on; with
// the default (off) the title will not stick there.
func (m hubModel) windowTitleCmd(prev hubModel) tea.Cmd {
	title := m.desiredWindowTitle()
	if title == prev.desiredWindowTitle() {
		return nil
	}
	return tea.SetWindowTitle(terminalTitle(title))
}

// desiredWindowTitle is the title the current view wants: the session's display
// name in a session view, empty anywhere else.
func (m hubModel) desiredWindowTitle() string {
	if m.mode != hubModeSession {
		return ""
	}
	return m.sessionDisplayName()
}

// sessionDisplayName is the active session's display name: the name the hub
// reports, or its fallbacks (the original prompt the preview carries, then the
// session id and ref). Every candidate is sanitized, not just the reported
// name: the header, the chrome, and the dashboard row all render this string,
// and a control-only session id or ref would otherwise reach them raw and
// reopen the escape-injection surface the title sink already closes. The header
// adds its "untitled session" placeholder; the terminal title uses the result
// as-is so an unnamed session leaves the window title empty rather than a
// placeholder.
func (m hubModel) sessionDisplayName() string {
	return envvars.FirstNonEmpty(
		sanitizeDisplayName(m.detail.Title),
		sanitizeDisplayName(m.detail.SessionID),
		sanitizeDisplayName(m.detail.Ref),
	)
}

// terminalTitle is the OSC-safe form of a session display name: control
// characters are stripped and the length is capped. bubbletea v1.3.10 writes
// the title as a bare "\x1b]2;" + title + "\x07" with no escaping, so a name
// carrying BEL (0x07) or ESC (0x1b) would close the OSC string early and inject
// arbitrary escape sequences (OSC 52 clipboard writes, cursor or keyboard-mode
// changes) into the user's terminal.
func terminalTitle(name string) string {
	name = sanitizeDisplayName(name)
	if runes := []rune(name); len(runes) > maxWindowTitleRunes {
		name = string(runes[:maxWindowTitleRunes])
	}
	return name
}

// quitCmd clears the terminal window title and then quits. The Update wrapper
// emits a clear when a session view is left, but a quit never leaves the view:
// the model still reads as a session on the way out, so without this the
// terminal keeps the session title after the TUI exits. Sequence, not Batch, so
// the clear is written before the program tears down.
func quitCmd() tea.Cmd {
	return tea.Sequence(tea.SetWindowTitle(""), tea.Quit)
}

// sanitizeDisplayName strips terminal control characters from session display
// text: the full Unicode control range — C0, DEL, and C1 (U+0080–U+009F).
// Session names and previews arrive from the wire (thread.Name, thread.Preview,
// raw prompts, evener/thread/name/changed) and are written to the terminal —
// both into escape strings (the window title) and as rendered text (the session
// header, the dashboard row). C1 matters as much as C0 here: a UTF-8-aware
// terminal can read U+009B/U+009D as CSI/OSC introducers and U+009C as an OSC
// terminator, so leaving C1 through reopens the same injection the C0 strip
// closes. Stripping at the point the name is derived keeps every sink safe;
// printable text, including non-ASCII runes, is preserved.
func sanitizeDisplayName(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}
