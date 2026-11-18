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
	return parseCommunicatePayload(args, strings.TrimSpace)
}

// parseCommunicatePayload decodes a communicate tool call. trim is the only
// thing the two entry points disagree about: the parser contract normalizes
// with TrimSpace, and the checkpoint capture passes trimBlockEdges so a reply
// keeps the indentation that decides whether its first line is code.
func parseCommunicatePayload(args json.RawMessage, trim func(string) string) (endTurn bool, message string) {
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
	message = trim(payload.Message)
	if message == "" && payload.Output != nil {
		message = trim(payload.Output.Message)
	}
	if payload.EndTurn == nil {
		return false, ""
	}
	return *payload.EndTurn, message
}

// checkpointReplyText returns the reply a checkpoint carries for the given
// tool call, keeping its first content line's indentation.
//
// This is a capture-site rule, not a parser contract. parseCommunicateArgs
// normalizes its fields with TrimSpace, and FuzzGlobalContextmgrTails pins that
// behavior, so it stays. But a reply opening with four-space-indented literal
// backticks is a model quoting a code example, which the frame escape
// deliberately spares: trimming the indent turns those backticks into a real
// fence opener, and the escape then reads a frame below them as fenced and
// leaves it unescaped in the user-role checkpoint (#6493 review).
//
// The end_turn decision and the field precedence are the parser's; only the
// edge whitespace differs, because only this site places the text where
// indentation decides whether a line is code.
func checkpointReplyText(history []schema.Turn, toolCallID string) (endTurn bool, message string) {
	return parseCommunicatePayload(findToolCallArgs(history, toolCallID), trimBlockEdges)
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
