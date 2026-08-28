package agent

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"primeradiant.com/evener/agent/command"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/skill"
)

// expandSlashCommand checks whether input invokes a loaded slash command or
// skill and, if so, returns its body as the replacement input (ok=true).
// Anything else (plain chat text, or a "/"-prefixed word that names no loaded
// command or skill, e.g. a client-side-only UI command like /model) is returned
// unchanged with ok=false, so the caller leaves input untouched and it flows
// on as ordinary text.
func (s *Session) expandSlashCommand(ctx context.Context, input string) (string, bool) {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") {
		return input, false
	}
	commandInput := strings.TrimLeftFunc(trimmed[1:], unicode.IsSpace)
	name := commandInput
	args := ""
	if i := strings.IndexFunc(commandInput, unicode.IsSpace); i >= 0 {
		name, args = commandInput[:i], strings.TrimSpace(commandInput[i:])
	}
	if name == "" {
		return input, false
	}
	if cmd, ok := plugin.ResolveCommand(s.pluginCommands, name); ok {
		if cmd.Source != "plugin" {
			// Evener-wide commands expand inert: arguments substitute as text,
			// nothing executes or reads (docs/skills.md).
			return command.ExpandArgs(cmd.Body, args), true
		}
		expanded, err := command.Expand(ctx, cmd.Body, args, s.currentEnv())
		if err != nil {
			// A genuine Expand failure (as opposed to "not a plugin command",
			// handled above) must not fail silently: without this, the user's
			// "/name args" was submitted to the model as literal text with no
			// indication their command never expanded. Still fall back to that
			// literal-text submission (ok=false) — surfacing the failure doesn't
			// mean blocking the input — but now with a visible warning.
			s.emit(events.EventWarning, events.WarningData{
				Message: fmt.Sprintf("expanding slash command /%s failed: %v", name, err),
			})
			return input, false
		}
		return expanded, true
	}

	catalogName, meta, ok := skill.ResolveSkill(s.skills, name)
	if !ok {
		return input, false
	}
	body, err := skill.LoadSkillBody(meta)
	if err != nil {
		s.emit(events.EventWarning, events.WarningData{
			Message: fmt.Sprintf("loading slash skill /%s failed: %v", name, err),
		})
		return input, false
	}
	s.emit(events.EventSkillActivated, events.SkillActivatedData{Name: catalogName})
	if args != "" {
		body += "\n\nUser context:\n" + args
	}
	return body, true
}
