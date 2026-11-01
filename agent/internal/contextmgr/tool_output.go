package contextmgr

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/searchresult"
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

func countLines(s string) int {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// searchSummary is a search result's one-line summary, "[head]", with the
// notes the tool put after its entries (searchresult.Split) added inside the
// brackets, each without brackets of its own, so a compacted search still
// says it was cut short or left paths out. The registry appends its breaker
// nudge the same way, after a blank line, so the notes end where that nudge
// begins (agent/internal/tool IsBreakerNudge): the nudge is not a note about
// the search, and a result with no notes of its own would otherwise compact
// with it read as one (#4223). Every block after the entries is kept, not
// only the first: a result can carry two when the registry's character cap
// cuts inside the tool's own note (#4460), and the marker it appends after
// that fragment is the part a compacted search must not lose.
func searchSummary(head, output string) string {
	_, notes := searchresult.Split(output)
	parts := []string{head}
	for line := range strings.SplitSeq(notes, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if tool.IsBreakerNudge(line) {
			break
		}
		parts = append(parts, strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
	}
	return "[" + strings.Join(parts, "; ") + "]"
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

func extractJSONField(s, field string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return ""
	}
	if v, ok := m[field]; ok {
		return fmt.Sprint(v)
	}
	return ""
}

func countJSONArrayElements(s string) int {
	var arr []any
	if err := json.Unmarshal([]byte(s), &arr); err != nil {
		return 0
	}
	return len(arr)
}
