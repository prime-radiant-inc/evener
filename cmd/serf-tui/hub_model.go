package main

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/hubapi"
)

type hubModel struct {
	client *hubapi.Client
	hubURL string
	width  int
	height int
}

func newHubModel(client *hubapi.Client, hubURL string) hubModel {
	return hubModel{client: client, hubURL: hubURL}
}

func (m hubModel) Init() tea.Cmd {
	return nil
}

func (m hubModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	}
	return m, nil
}

func (m hubModel) View() string {
	return fmt.Sprintf("serf hub\n\nConnected to %s\n\nLoading sessions...", m.hubURL)
}
