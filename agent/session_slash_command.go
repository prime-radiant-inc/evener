package agent

import (
	"context"
	"strings"

	"primeradiant.com/serf/agent/command"
	"primeradiant.com/serf/agent/plugin"
)

// expandSlashCommand checks whether input invokes a loaded plugin slash
// command — a leading "/" followed by a command name known to
// s.pluginCommands — and, if so, returns the expanded command body as the
// replacement input (ok=true). Anything else (plain chat text, or a
// "/"-prefixed word that names no loaded plugin command, e.g. a
// client-side-only UI command like /model) is returned unchanged with
// ok=false, so the caller leaves input untouched and it flows on as ordinary
// text.
func (s *Session) expandSlashCommand(ctx context.Context, input string) (string, bool) {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") {
		return input, false
	}
	name, args, _ := strings.Cut(trimmed[1:], " ")
	if name == "" {
		return input, false
	}
	cmd, ok := plugin.ResolveCommand(s.pluginCommands, name)
	if !ok {
		return input, false
	}
	expanded, err := command.Expand(ctx, cmd.Body, strings.TrimSpace(args), s.currentEnv())
	if err != nil {
		return input, false
	}
	return expanded, true
}
