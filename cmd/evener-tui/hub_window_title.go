package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/envvars"
)

// windowTitleCmd returns a tea.SetWindowTitle command when the update moved the
// terminal title, and records the new value. prev is the model as it was before
// the update.
//
// The title follows the active session's display name while a session view is
// open and is empty on every other surface (the dashboard, the spawn form) so
// the window is not left labelled with a session the user navigated away from.
// It moves only on a view transition — entering or leaving a session, or a
// change to the viewed session's display name (a user rename, the auto-namer's
// first name, or a compaction refresh) — never on an ordinary keypress or
// streaming frame, so the OSC escape stays off the hot path.
//
// tmux reflects OSC title escapes only for a window with allow-rename on; with
// the default (off) the title will not stick there.
func (m *hubModel) windowTitleCmd(prev hubModel) tea.Cmd {
	title := ""
	if m.mode == hubModeSession {
		title = m.sessionDisplayName()
	}
	if title == prev.windowTitle {
		return nil
	}
	// The title only needs to move when the view entered or left a session, or
	// the viewed session's name changed; any other update leaves it where it is.
	nameChanged := m.mode == hubModeSession && prev.mode == hubModeSession && prev.sessionDisplayName() != m.sessionDisplayName()
	viewChanged := m.mode != prev.mode
	if !nameChanged && !viewChanged {
		return nil
	}
	m.windowTitle = title
	return tea.SetWindowTitle(title)
}

// sessionDisplayName is the active session's display name: the name the hub
// reports, or its fallbacks (the original prompt the preview carries, then the
// session id and ref). The session header renders it verbatim and adds its
// "untitled session" placeholder; the terminal title uses it as-is so an
// unnamed session leaves the window title empty rather than a placeholder.
func (m hubModel) sessionDisplayName() string {
	return envvars.FirstNonEmpty(m.detail.Title, m.detail.SessionID, m.detail.Ref)
}
