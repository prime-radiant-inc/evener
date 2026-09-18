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
		title = m.sessionWindowTitle()
	}
	if title == prev.windowTitle {
		return nil
	}
	nameChanged := m.mode == hubModeSession && prev.mode == hubModeSession && prev.detail.Title != m.detail.Title
	viewChanged := m.mode != prev.mode || prev.detail.Ref != m.detail.Ref
	if !nameChanged && !viewChanged {
		return nil
	}
	m.windowTitle = title
	return tea.SetWindowTitle(title)
}

// sessionWindowTitle is the active session's display name for the terminal
// title: the same chain the session header shows (Title → SessionID → Ref),
// which already carries the session name and its fallbacks (original prompt,
// then id). It omits the header's "untitled session" placeholder so an unnamed
// session leaves the title empty rather than putting a placeholder in the
// window.
func (m hubModel) sessionWindowTitle() string {
	return envvars.FirstNonEmpty(m.detail.Title, m.detail.SessionID, m.detail.Ref)
}
