package main

import (
	"io"
	"strings"
)

func readPromptFromArgsOrStdin(args []string, listSessions bool, stdin io.Reader, stdinIsCharDevice bool) string {
	prompt := strings.TrimSpace(strings.Join(args, " "))
	if prompt != "" {
		return prompt
	}
	if listSessions || stdinIsCharDevice {
		return ""
	}
	b, err := io.ReadAll(stdin)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
