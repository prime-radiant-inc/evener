package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/command"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// The accepted input owns these bytes before transcript admission. An in-progress
// row without a completed save is uncertain: external shell effects and our
// snapshot cannot commit atomically, so that row never authorizes another run.
type selectedCommandPreparation struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Body  string `json:"body,omitempty"`
}

func (s *clientMutationStore) commandPreparations(id string) []selectedCommandPreparation {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return slices.Clone(s.state.Journal[id].CommandPreparations)
}

func (s *clientMutationStore) saveCommandPreparation(id string, prepared selectedCommandPreparation) error {
	return s.mutate(func(snapshot *clientMutationSnapshot) error {
		record, ok := snapshot.Journal[id]
		if !ok {
			return fmt.Errorf("selected command input %q has no durable owner", id)
		}
		index := slices.IndexFunc(record.CommandPreparations, func(row selectedCommandPreparation) bool { return row.Name == prepared.Name })
		if index < 0 {
			record.CommandPreparations = append(record.CommandPreparations, prepared)
		} else {
			if prepared.State == "in_progress" || record.CommandPreparations[index].State == "completed" {
				return fmt.Errorf("selected command %q already has preparation evidence", prepared.Name)
			}
			record.CommandPreparations[index] = prepared
		}
		snapshot.Journal[id] = record
		return nil
	})
}

// A transformation changes input ownership, not the external effects already
// attempted by its sources. Uncertainty dominates completed evidence for the
// same name, regardless of queue order: dedup must never conceal an unknown run.
func carrySelectedCommandPreparations(snapshot *clientMutationSnapshot, record *clientMutationRecord, entries []clientMutationQueueEntry) {
	for _, entry := range entries {
		for _, prepared := range snapshot.Journal[entry.ClientMutationID].CommandPreparations {
			index := slices.IndexFunc(record.CommandPreparations, func(row selectedCommandPreparation) bool { return row.Name == prepared.Name })
			if index < 0 {
				record.CommandPreparations = append(record.CommandPreparations, prepared)
			} else if prepared.State != "completed" {
				record.CommandPreparations[index] = prepared
			}
		}
	}
}

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
	if len(names) == 0 {
		return nil, nil
	}
	owner := queuedClientMutationFromContext(ctx).ClientMutationID
	prepared := make(map[string]selectedCommandPreparation)
	if owner != "" {
		for _, row := range s.clientMutations.commandPreparations(owner) {
			prepared[row.Name] = row
		}
	}
	for _, name := range names {
		if row, ok := prepared[name]; ok {
			if row.State != "completed" {
				if row.State == "in_progress" {
					row.State = "uncertain"
					if err := s.clientMutations.saveCommandPreparation(owner, row); err != nil {
						return nil, fmt.Errorf("selected command %q has an uncertain outcome, preserve evidence: %w", name, err)
					}
				}
				return nil, fmt.Errorf("selected command %q has an uncertain outcome, not re-executed", name)
			}
			continue
		}
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
		if row, ok := prepared[name]; ok {
			bodies = append(bodies, row.Body)
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cmd := s.pluginCommands[name]
		body := command.ExpandArgs(cmd.Body, "")
		if cmd.Source == "plugin" {
			if owner != "" {
				if err := s.clientMutations.saveCommandPreparation(owner, selectedCommandPreparation{Name: name, State: "in_progress"}); err != nil {
					return nil, fmt.Errorf("persist selected command %q before expansion: %w", name, err)
				}
			}
			var err error
			body, err = command.Expand(ctx, cmd.Body, "", s.currentEnv())
			if err == nil {
				err = ctx.Err()
			}
			if err != nil {
				return nil, fmt.Errorf("expand selected command %q: %w", name, err)
			}
		}
		if owner != "" {
			if err := s.clientMutations.saveCommandPreparation(owner, selectedCommandPreparation{Name: name, State: "completed", Body: body}); err != nil {
				return nil, fmt.Errorf("persist selected command %q completion, outcome uncertain until durable evidence is confirmed: %w", name, err)
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
