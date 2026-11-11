package contextmgr

import (
	"encoding/json"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// findToolCallArgs looks backward from the tool result to find the matching
// assistant tool call and return its arguments.
func findToolCallArgs(history []schema.Turn, toolCallID string) json.RawMessage {
	for _, t := range slices.Backward(history) {
		if t.Kind != schema.TurnAssistant {
			continue
		}
		for _, p := range t.Message.Content {
			if p.Kind == llm.ContentToolCall && p.ToolCall != nil && p.ToolCall.ID == toolCallID {
				return p.ToolCall.Arguments
			}
		}
	}
	return nil
}

func parseCommunicateArgs(args json.RawMessage) (endTurn bool, message string) {
	var payload struct {
		Message string `json:"message"`
		EndTurn *bool  `json:"end_turn"`
		Output  *struct {
			Message string `json:"message"`
		} `json:"output"`
	}
	if len(args) == 0 || json.Unmarshal(args, &payload) != nil {
		return false, ""
	}
	message = strings.TrimSpace(payload.Message)
	if message == "" && payload.Output != nil {
		message = strings.TrimSpace(payload.Output.Message)
	}
	if payload.EndTurn == nil {
		return false, ""
	}
	return *payload.EndTurn, message
}

func communicateArgsFromHistory(history []schema.Turn, toolCallID string) (endTurn bool, message string) {
	return parseCommunicateArgs(findToolCallArgs(history, toolCallID))
}

func parseExitCode(shellOutput string) string {
	// Look for "exit_code=N" (raw shell output) or "exit N" (masked summary format).
	for line := range strings.SplitSeq(shellOutput, "\n") {
		// Raw format: exit_code=0
		if _, rest, ok := strings.Cut(line, "exit_code="); ok {
			end := 0
			for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
				end++
			}
			if end > 0 {
				return rest[:end]
			}
		}
		// Masked format: → exit 0] or "exit 0"
		if _, rest, ok := strings.Cut(line, "exit "); ok {
			end := 0
			for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
				end++
			}
			if end > 0 {
				return rest[:end]
			}
		}
	}
	return "?"
}
