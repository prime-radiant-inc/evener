package cliprompt

import (
	"io"
	"strings"
)

// Read resolves the prompt for a run. It prefers the joined command-line args;
// when those are empty and the invocation is neither a session listing nor an
// interactive terminal, it falls back to reading the prompt from stdin.
func Read(args []string, listSessions bool, stdin io.Reader, stdinIsCharDevice bool) string {
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
