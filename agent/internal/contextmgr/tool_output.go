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
	message = trimReplyEdges(payload.Message)
	if message == "" && payload.Output != nil {
		message = trimReplyEdges(payload.Output.Message)
	}
	if payload.EndTurn == nil {
		return false, ""
	}
	return *payload.EndTurn, message
}

// trimReplyEdges drops a reply's leading and trailing blank lines and reports
// "" for one that holds nothing else. TrimSpace would strip the first content
// line's indentation, and that indentation is load-bearing: a reply opening
// with four-space-indented literal backticks is a model quoting a code
// example, and the escape spares it. Removing the indent turns the backticks
// into a real fence opener, and the scan then treats a frame below them as
// fenced and leaves it unescaped in a user-role checkpoint (#6493 review).
func trimReplyEdges(text string) string {
	lines := strings.Split(text, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
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
