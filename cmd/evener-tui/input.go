package main

import (
	"strings"
)

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
