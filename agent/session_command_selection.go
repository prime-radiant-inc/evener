package agent

import (
	"context"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/command"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func commandInputRecordFromQueued(input queuedInput) *schema.CommandInputRecord {
	if len(input.CommandNames) == 0 {
		return nil
	}
	return &schema.CommandInputRecord{OriginalText: input.Text, Names: append([]string(nil), input.CommandNames...)}
}

// prepareSelectedCommands accepts only exact catalog keys. The complete list
// resolves before any installed plugin template can execute. Expansion is
// once per canonical command, with empty args, never over generated output.
func (s *Session) prepareSelectedCommands(ctx context.Context, names []string) ([]string, error) {
	for _, name := range names {
		if _, ok := s.pluginCommands[name]; !ok {
			return nil, fmt.Errorf("selected command %q is unavailable", name)
		}
	}
	seen := make(map[string]bool, len(names))
	var bodies []string
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		cmd := s.pluginCommands[name]
		body := command.ExpandArgs(cmd.Body, "")
		if cmd.Source == "plugin" {
			var err error
			body, err = command.Expand(ctx, cmd.Body, "", s.currentEnv())
			if err != nil {
				return nil, fmt.Errorf("expand selected command %q: %w", name, err)
			}
		}
		bodies = append(bodies, body)
	}
	return bodies, nil
}

// appendSelectedCommands keeps original input in its own content part. Bodies
// are recorded on the same turn, so restore delivers data, not new invocations.
func appendSelectedCommands(message llm.Message, input *schema.CommandInputRecord, bodies []string) llm.Message {
	if input == nil {
		return message
	}
	for _, body := range bodies {
		if strings.TrimSpace(body) != "" {
			message.Content = append(message.Content, llm.ContentPart{Kind: llm.ContentText, Text: "\n\n" + body})
		}
	}
	if strings.TrimSpace(message.Text()) == "" {
		message.Content = append(message.Content, llm.ContentPart{Kind: llm.ContentText, Text: "[command selection: " + strings.Join(input.Names, ", ") + "]"})
	}
	return message
}
