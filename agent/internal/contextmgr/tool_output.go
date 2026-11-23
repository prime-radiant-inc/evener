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
	endTurn, _, ok := schema.CommunicateArgsOutcome(args)
	if !ok {
		return false, ""
	}
	var payload struct {
		Message string `json:"message"`
		Output  *struct {
			Message string `json:"message"`
		} `json:"output"`
	}
	if json.Unmarshal(args, &payload) != nil {
		return false, ""
	}
	message = strings.TrimSpace(payload.Message)
	if message == "" && payload.Output != nil {
		message = strings.TrimSpace(payload.Output.Message)
	}
	return endTurn, message
}

func communicateArgsFromHistory(history []schema.Turn, toolCallID string) (endTurn bool, message string) {
	return parseCommunicateArgs(findToolCallArgs(history, toolCallID))
}

func parseExitCode(shellOutput string) string {
	// Look for "exit_code=N" in the raw shell output.
	for line := range strings.SplitSeq(shellOutput, "\n") {
		if _, rest, ok := strings.Cut(line, "exit_code="); ok {
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
