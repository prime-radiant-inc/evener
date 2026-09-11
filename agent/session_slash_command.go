package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"primeradiant.com/evener/agent/command"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
)

// slashInputResult is the explicit internal outcome of slash expansion, so a
// known skill failure cannot silently fall through to ordinary chat input.
//
// Text is the input the turn should carry: the ORIGINAL user text for skill
// routes (rendered instructions attach as separately identifiable typed
// context), the expanded command body for ordinary commands, and the unchanged
// input for anything not handled. Handled reports whether this expansion owns
// the input. Selection records the typed user selection for skill routes.
// Activations carries the prepared batch awaiting final dispatch admission.
// Err is the visible failure of a known skill route; when set, the caller
// records the failed input and dispatches no dependent work.
type slashInputResult struct {
	Text        string
	Handled     bool
	Selection   *schema.SkillInputRecord
	Activations *skillActivationBatch
	Err         error
}

// expandSlashCommand checks whether input invokes a loaded slash command or
// skill. Ordinary command expansion keeps its existing behavior (body as
// replacement input). A skill route resolves against the full catalog
// (hidden-but-authorized names stay resolvable), prepares the activation
// through the shared loader/renderer, and keeps the original input as the
// turn's text. Anything else (plain chat text, or a "/"-prefixed word that
// names no loaded command or skill, e.g. a client-side-only UI command like
// /model) is returned unhandled so the caller leaves input untouched and it
// flows on as ordinary text.
func (s *Session) expandSlashCommand(ctx context.Context, input string) slashInputResult {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") {
		return slashInputResult{Text: input}
	}
	commandInput := strings.TrimLeftFunc(trimmed[1:], unicode.IsSpace)
	name := commandInput
	args := ""
	if i := strings.IndexFunc(commandInput, unicode.IsSpace); i >= 0 {
		name, args = commandInput[:i], strings.TrimSpace(commandInput[i:])
	}
	if name == "" {
		return slashInputResult{Text: input}
	}
	if cmd, ok := plugin.ResolveCommand(s.pluginCommands, name); ok {
		if cmd.Source != "plugin" {
			// Evener-wide commands expand inert: arguments substitute as text,
			// nothing executes or reads (docs/skills.md).
			return slashInputResult{Text: command.ExpandArgs(cmd.Body, args), Handled: true}
		}
		expanded, err := command.Expand(ctx, cmd.Body, args, s.currentEnv())
		if err != nil {
			// A genuine Expand failure (as opposed to "not a plugin command",
			// handled above) must not fail silently: without this, the user's
			// "/name args" was submitted to the model as literal text with no
			// indication their command never expanded. Still fall back to that
			// literal-text submission (unhandled) — surfacing the failure doesn't
			// mean blocking the input — but now with a visible warning.
			s.emit(events.EventWarning, events.WarningData{
				Message: fmt.Sprintf("expanding slash command /%s failed: %v", name, err),
			})
			return slashInputResult{Text: input}
		}
		return slashInputResult{Text: expanded, Handled: true}
	}

	descriptor, resolveErr := s.skills.Resolve(name)
	if resolveErr != nil {
		var resolution *skill.ResolutionError
		if errors.As(resolveErr, &resolution) && resolution.Kind == "ambiguous" {
			// A known but ambiguous suffix is a visible activation failure that
			// reports candidates, never a silent ordinary chat message.
			return slashInputResult{Text: input, Handled: true, Err: resolveErr}
		}
		return slashInputResult{Text: input}
	}

	// A genuine user leading slash: construct the invocation from trusted
	// call-site provenance. The invocation identity is the session's own
	// monotonically persisted operation identity, never a client field.
	invocationID := s.mintSkillOperationID()
	invocation := skillInvocation{
		Name:          descriptor.CatalogName,
		Route:         "user_slash",
		InvocationID:  invocationID,
		AtomicGroupID: invocationID,
	}
	batch, err := s.prepareSkillActivations(ctx, []skillInvocation{invocation})
	return slashInputResult{
		Text:        input,
		Handled:     true,
		Activations: batch,
		Err:         err,
		Selection: &schema.SkillInputRecord{
			OriginalText:  input,
			Arguments:     args,
			Names:         []string{descriptor.CatalogName},
			AtomicGroupID: invocationID,
		},
	}
}
