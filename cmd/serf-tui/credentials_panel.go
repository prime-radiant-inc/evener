package main

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/internal/appwire"
)

type credentialsActionMsg struct {
	Action   string // "set" | "logout" | "oauth"
	Provider string
}

type credentialsPanel struct {
	providers []appwire.AuthStatusResponse
	cursor    int
	err       error
	loading   bool
	done      bool
	cancelled bool
}

func newCredentialsPanel() credentialsPanel {
	return credentialsPanel{loading: true}
}

func (p credentialsPanel) Init() tea.Cmd { return nil }

func (p credentialsPanel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case authListResultMsg:
		p.loading = false
		p.err = m.Err
		p.providers = m.List.Providers
		if p.cursor >= len(p.providers) {
			p.cursor = 0
		}
	case tea.KeyMsg:
		switch m.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			p.cancelled = true
			p.done = true
			return p, nil
		case tea.KeyUp:
			if p.cursor > 0 {
				p.cursor--
			}
		case tea.KeyDown:
			if p.cursor < len(p.providers)-1 {
				p.cursor++
			}
		case tea.KeyEnter:
			if len(p.providers) == 0 {
				return p, nil
			}
			cur := p.providers[p.cursor]
			modes := strings.Join(cur.AuthModes, ",")
			if strings.Contains(modes, "apiKey") {
				return p, func() tea.Msg { return credentialsActionMsg{Action: "set", Provider: cur.Provider} }
			}
			if strings.Contains(modes, "oauth") {
				return p, func() tea.Msg { return credentialsActionMsg{Action: "oauth", Provider: cur.Provider} }
			}
		case tea.KeyRunes:
			s := string(m.Runes)
			if s == "c" || s == "C" {
				if len(p.providers) == 0 {
					return p, nil
				}
				cur := p.providers[p.cursor]
				return p, func() tea.Msg { return credentialsActionMsg{Action: "logout", Provider: cur.Provider} }
			}
			if s == "o" || s == "O" {
				if len(p.providers) == 0 {
					return p, nil
				}
				cur := p.providers[p.cursor]
				return p, func() tea.Msg { return credentialsActionMsg{Action: "oauth", Provider: cur.Provider} }
			}
		}
	}
	return p, nil
}

func (p credentialsPanel) sourceBadgeColor(source string) lipgloss.Color {
	th := activeThemeV2()
	switch source {
	case "oauth", "env":
		return th.StateIdle
	case "absent":
		return th.StateEnded
	default:
		return th.TextDim
	}
}

func (p credentialsPanel) View() string {
	th := activeThemeV2()
	var body string
	if p.loading {
		body = lipgloss.NewStyle().Foreground(th.TextDim).Render("Loading credentials…")
	} else if p.err != nil {
		body = lipgloss.NewStyle().Foreground(th.StateEnded).Render("Error: " + p.err.Error())
	} else {
		var rows []string
		for i, pv := range p.providers {
			cursor := "  "
			if i == p.cursor {
				cursor = "> "
			}
			name := lipgloss.NewStyle().Foreground(th.Text).Render(pv.Provider)
			badge := StatusBadge(p.sourceBadgeColor(pv.ActiveSource), pv.ActiveSource)
			rows = append(rows, cursor+name+"  "+badge)
		}
		body = strings.Join(rows, "\n")
	}
	width := 60
	footer := actionBarForWidth(width, KbdHint("enter", "set api key"), KbdHint("o", "OAuth"), KbdHint("c", "clear"), KbdHint("esc", "close"))
	return Overlay(OverlayOpts{Title: "Credentials", Width: width, Body: body, Footer: footer})
}
