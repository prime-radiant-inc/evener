package main

import "strings"

type composerPanel struct {
	Label          string
	ReadOnlyReason string
	Draft          string
	Keys           []string
	ShowInput      bool
}

type hubComposerMode int

const (
	hubComposerModeSend hubComposerMode = iota
	hubComposerModeSteer
	hubComposerModeFork
	hubComposerModeReadOnly
)

func (m hubModel) sessionComposerMode() hubComposerMode {
	if m.forkDraft != nil {
		return hubComposerModeFork
	}
	if m.sessionTurnActionState() && !m.detail.Capabilities.Send {
		if m.detail.Capabilities.Steer && strings.TrimSpace(m.detail.ActiveTurnID) != "" {
			return hubComposerModeSteer
		}
		return hubComposerModeReadOnly
	}
	if m.detail.Capabilities.Send {
		return hubComposerModeSend
	}
	return hubComposerModeReadOnly
}

func (m hubModel) sessionComposerReadOnlyReason() string {
	if m.sessionTurnActionState() {
		if !m.detail.Capabilities.Steer {
			return "source does not advertise steer"
		}
		if strings.TrimSpace(m.detail.ActiveTurnID) == "" {
			return "no active turn is available for steer"
		}
	}
	if !m.detail.Capabilities.Send {
		return "source does not support send"
	}
	return ""
}

func (m hubModel) sessionTurnActionState() bool {
	switch stateLabel(m.detail.State) {
	case "processing", "awaiting":
		return true
	}
	switch strings.ToLower(strings.TrimSpace(m.detail.State)) {
	case "active", "running", "working":
		return true
	}
	return m.session.processing
}

func (m hubModel) sessionComposerPanel() composerPanel {
	keys := []string{"esc: browse", "ctrl+o: dashboard", hubCommandHint("help")}
	panel := composerPanel{
		Draft:     m.session.input.Value(),
		Keys:      keys,
		ShowInput: true,
	}
	switch m.sessionComposerMode() {
	case hubComposerModeFork:
		panel.Label = "fork draft"
		panel.Keys = []string{"enter: fork", "esc: cancel", "ctrl+o: dashboard"}
	case hubComposerModeSteer:
		panel.Label = "steer"
		panel.Keys = append([]string{"enter: steer"}, keys...)
	case hubComposerModeReadOnly:
		panel.Label = "read-only"
		panel.ReadOnlyReason = m.sessionComposerReadOnlyReason()
	default:
		panel.Label = "message"
		if m.detail.Capabilities.Send {
			panel.Keys = append([]string{"enter: send"}, keys...)
		}
	}
	return panel
}

func (p composerPanel) View() string {
	var b strings.Builder
	label := strings.TrimSpace(p.Label)
	reason := strings.TrimSpace(p.ReadOnlyReason)
	if reason != "" {
		if label == "" {
			label = "read-only"
		}
		b.WriteString(label)
		b.WriteString(": ")
		b.WriteString(reason)
		b.WriteString("\n")
	} else if label != "" {
		b.WriteString(label)
		b.WriteString("\n")
	}
	if p.ShowInput {
		b.WriteString(renderComposerDraft(p.Draft))
	}
	if len(p.Keys) > 0 {
		b.WriteString(strings.Join(p.Keys, "  "))
		b.WriteString("\n")
	}
	return b.String()
}

func renderComposerDraft(draft string) string {
	var b strings.Builder
	lines := strings.Split(draft, "\n")
	if len(lines) == 0 {
		lines = []string{""}
	}
	for i, line := range lines {
		if i == 0 {
			b.WriteString("> ")
		} else {
			b.WriteString("  ")
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}
