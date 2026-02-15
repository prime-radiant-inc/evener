package main

import (
	"io"
	"strings"
)

func readTaskFromArgsOrStdin(args []string, listSessions bool, stdin io.Reader, stdinIsCharDevice bool) string {
	task := strings.TrimSpace(strings.Join(args, " "))
	if task != "" {
		return task
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

