package main

import (
	"os"
	"path/filepath"
	"strings"

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
	paths  bool
	done   bool
}

func newTextInputModal(prompt, tag string) textInputModal {
	return textInputModal{prompt: prompt, tag: tag}
}

func newTextInputModalWithInput(prompt, tag, input string) textInputModal {
	return textInputModal{prompt: prompt, tag: tag, input: input}
}

func newPathTextInputModal(prompt, tag, input string) textInputModal {
	return textInputModal{prompt: prompt, tag: tag, input: input, paths: true}
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
		case tea.KeyTab:
			if m.paths {
				m.input = completeLastPathSegment(m.input)
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
	help := "[Enter] confirm  [Esc] cancel"
	if m.paths {
		help = "[Tab] complete path  " + help
	}
	return m.prompt + "\n" + "> " + display + "_\n" + help
}

func completeLastPathSegment(input string) string {
	start := strings.LastIndex(input, ",") + 1
	prefix := input[start:]
	leading := prefix[:len(prefix)-len(strings.TrimLeft(prefix, " \t"))]
	raw := strings.TrimSpace(prefix)
	if raw == "" {
		raw = os.Getenv("HOME")
	}
	if strings.HasPrefix(raw, "~/") || raw == "~" {
		raw = filepath.Join(os.Getenv("HOME"), strings.TrimPrefix(raw, "~"))
	}
	var listDir, filter string
	if strings.HasSuffix(raw, string(filepath.Separator)) {
		listDir = raw
	} else {
		listDir = filepath.Dir(raw)
		filter = filepath.Base(raw)
	}
	if listDir == "." {
		if cwd, err := os.Getwd(); err == nil {
			listDir = cwd
		}
	}
	entries, err := os.ReadDir(listDir)
	if err != nil {
		return input
	}
	for _, entry := range entries {
		if filter != "" && !strings.HasPrefix(strings.ToLower(entry.Name()), strings.ToLower(filter)) {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") && filter == "" {
			continue
		}
		full := filepath.Join(listDir, entry.Name())
		if entry.IsDir() {
			full += string(filepath.Separator)
		}
		return input[:start] + leading + full
	}
	return input
}
