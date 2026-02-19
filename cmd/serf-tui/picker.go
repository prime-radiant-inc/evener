package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/agent"
)

// sessionPicker is a bubbletea model that shows a list of sessions for the
// user to choose from. Returns the selected session ID when the user presses
// enter, or "" if they cancel with escape/ctrl-c.
type sessionPicker struct {
	sessions []agent.SessionSnapshot
	cursor   int
	width    int
	height   int
	selected string // set when user picks one
	quit     bool   // set on escape/ctrl-c
}

func newSessionPicker(sessions []agent.SessionSnapshot) sessionPicker {
	return sessionPicker{sessions: sessions}
}

func (m sessionPicker) Init() tea.Cmd { return nil }

func (m sessionPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.quit = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.sessions)-1 {
				m.cursor++
			}
		case "enter":
			if len(m.sessions) > 0 {
				m.selected = m.sessions[m.cursor].ID
			}
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	}
	return m, nil
}

func (m sessionPicker) View() string {
	if len(m.sessions) == 0 {
		return pickerTitle.Render("No saved sessions.") + "\n"
	}

	var b strings.Builder
	b.WriteString(pickerTitle.Render("Select a session to resume:"))
	b.WriteString("\n\n")

	for i, s := range m.sessions {
		cursor := "  "
		style := pickerNormal
		if i == m.cursor {
			cursor = "> "
			style = pickerSelected
		}

		firstInput := firstInputText(s)
		branch := s.EnvInfo.GitBranch
		if branch == "" {
			branch = "-"
		}

		line := fmt.Sprintf("%s%-20s  %-16s  %-14s  turns=%d",
			cursor,
			s.UpdatedAt.Format("2006-01-02 15:04"),
			s.Model,
			branch,
			s.TurnCount,
		)
		b.WriteString(style.Render(line))
		if firstInput != "" {
			b.WriteString("  ")
			b.WriteString(pickerDim.Render(firstInput))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(pickerDim.Render("↑/↓ navigate  enter select  esc quit"))
	return b.String()
}

func firstInputText(s agent.SessionSnapshot) string {
	for _, t := range s.History {
		if t.Kind == agent.TurnUserInput {
			text := t.Message.Text()
			if len(text) > 60 {
				text = text[:57] + "..."
			}
			return fmt.Sprintf("%q", text)
		}
	}
	return ""
}

// runSessionPicker shows an interactive session picker and returns the selected
// session ID. Returns "" if the user cancels or there are no sessions.
func runSessionPicker(sessions []agent.SessionSnapshot) (string, error) {
	if len(sessions) == 0 {
		return "", fmt.Errorf("no saved sessions")
	}

	m := newSessionPicker(sessions)
	p := tea.NewProgram(m)
	result, err := p.Run()
	if err != nil {
		return "", err
	}

	final := result.(sessionPicker)
	if final.quit {
		return "", nil
	}
	return final.selected, nil
}
