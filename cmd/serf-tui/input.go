package main

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type asyncMsg struct{ msg tea.Msg }

func waitForAsync(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return asyncMsg{msg: msg}
	}
}

// parseSlashCommand returns the command name and arguments if the input starts
// with a slash command. Returns ("", "") if not a slash command.
func parseSlashCommand(input string) (cmd, args string) {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "/") {
		return "", ""
	}
	parts := strings.SplitN(input[1:], " ", 2)
	cmd = parts[0]
	if len(parts) > 1 {
		args = parts[1]
	}
	return cmd, args
}

func hubSlashCommandHelp(caps hubSessionCapabilities) string {
	return hubCommandHelp(caps)
}
