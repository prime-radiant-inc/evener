package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
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

func (p credentialsPanel) View() string {
	if p.loading {
		return "Loading credentials…"
	}
	if p.err != nil {
		return fmt.Sprintf("Error: %v\n[Esc] close", p.err)
	}
	var b strings.Builder
	b.WriteString("Credentials\n\n")
	for i, pv := range p.providers {
		cursor := "  "
		if i == p.cursor {
			cursor = "> "
		}
		fmt.Fprintf(&b, "%s%-22s  source: %-10s  modes: %s\n", cursor, pv.Provider, pv.ActiveSource, strings.Join(pv.AuthModes, ","))
	}
	b.WriteString("\n[Enter] set api key  [O] OAuth sign-in  [C] clear  [Esc] close")
	return b.String()
}
