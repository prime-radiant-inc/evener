package main

import (
	tea "github.com/charmbracelet/bubbletea"
)

type textInputResultMsg struct {
	Tag       string
	Value     string
	Cancelled bool
}

type textInputModal struct {
	tag    string
	prompt string
	input  string
	mask   bool
	done   bool
}

func newTextInputModal(prompt, tag string) textInputModal {
	return textInputModal{prompt: prompt, tag: tag}
}

func newTextInputModalMasked(prompt, tag string) textInputModal {
	return textInputModal{prompt: prompt, tag: tag, mask: true}
}

func (m textInputModal) Init() tea.Cmd { return nil }

func (m textInputModal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.KeyMsg:
		switch v.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			m.done = true
			return m, func() tea.Msg { return textInputResultMsg{Tag: m.tag, Cancelled: true} }
		case tea.KeyEnter:
			m.done = true
			return m, func() tea.Msg { return textInputResultMsg{Tag: m.tag, Value: m.input} }
		case tea.KeyBackspace:
			if len(m.input) > 0 {
				m.input = m.input[:len(m.input)-1]
			}
		case tea.KeyRunes:
			m.input += string(v.Runes)
		}
	}
	return m, nil
}

func (m textInputModal) View() string {
	display := m.input
	if m.mask {
		display = ""
		for range m.input {
			display += "•"
		}
	}
	return m.prompt + "\n" + "> " + display + "_\n[Enter] confirm  [Esc] cancel"
}
