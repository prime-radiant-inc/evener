// cmd/serf-tui/model.go
package main

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

type model struct {
	addr      string
	connected bool
	err       error
	width     int
	height    int
}

func newModel(addr string) model {
	return model{addr: addr}
}

func (m model) Init() tea.Cmd {
	return nil // will connect to SSE in a later task
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	}
	return m, nil
}

func (m model) View() string {
	return fmt.Sprintf("serf-tui connecting to %s...\n\nPress Ctrl+C to quit.", m.addr)
}
