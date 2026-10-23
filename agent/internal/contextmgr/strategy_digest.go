package contextmgr

import (
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// buildStrategyHistoryDigest is the opt-in summarizers' shared source record.
// Only ordinary assistant/tool detail spends the soft budget. Keep scanning:
// a later user message, attributed steer or previous compaction may be the
// only record of a permission or hold. Never shorten those source records.
// The default summarizer retains its existing renderer and budgets.
func buildStrategyHistoryDigest(turns []schema.Turn, resultToolName string) string {
	const maxDetailChars = 30_000
	var b strings.Builder
	detailChars := 0
	omitted := false
	writeDetail := func(text string) {
		if detailChars >= maxDetailChars {
			if !omitted {
				b.WriteString("[Further assistant/tool detail omitted]\n")
				omitted = true
			}
			return
		}
		b.WriteString(text)
		detailChars += len(text)
	}
	for _, turn := range turns {
		switch turn.Kind {
		case schema.TurnUserInput:
			text := turn.Message.Text()
			label := "User: "
			if isStrategyCompactionText(text) {
				label = "Previous compaction: "
			}
			b.WriteString(label)
			b.WriteString(text)
			b.WriteString("\n")
		case schema.TurnCheckpoint, schema.TurnSummary:
			b.WriteString("Previous compaction: ")
			b.WriteString(turn.Message.Text())
			b.WriteString("\n")
		case schema.TurnSteering:
			label := "System: "
			if isPartnerSteering(turn) {
				label = "User: "
			} else if isParentSteering(turn) {
				label = "Parent: "
			}
			b.WriteString(label)
			b.WriteString(turn.Message.Text())
			b.WriteString("\n")
		case schema.TurnAssistant:
			if text := turn.Message.Text(); text != "" {
				writeDetail("Assistant: " + truncate(text, 500) + "\n")
			}
			for _, part := range turn.Message.Content {
				if part.Kind != llm.ContentToolCall || part.ToolCall == nil {
					continue
				}
				call := part.ToolCall
				if call.Name == resultToolName {
					endTurn, message := parseCommunicateArgs(call.Arguments)
					label := "Agent Status"
					if endTurn {
						label = "Agent Message"
					}
					writeDetail(label + ": " + truncate(message, 500) + "\n")
				} else {
					writeDetail(fmt.Sprintf("  [tool_call: %s(%s)]\n", call.Name, truncate(string(call.Arguments), 200)))
				}
			}
		case schema.TurnTool, schema.TurnToolResults:
			for _, part := range turn.Message.Content {
				if part.Kind != llm.ContentToolResult || part.ToolResult == nil {
					continue
				}
				result := part.ToolResult
				errTag := ""
				if result.IsError {
					errTag = " ERROR"
				}
				writeDetail(fmt.Sprintf("Tool(%s)%s: %s\n", result.Name, errTag, truncate(fmt.Sprint(result.Content), 300)))
			}
		}
	}
	return b.String()
}

func isStrategyCompactionText(text string) bool {
	return strings.HasPrefix(text, "[CONTEXT CHECKPOINT]") ||
		strings.HasPrefix(text, "[CONTEXT CHECKPOINT - PREDICTIVE]") ||
		strings.HasPrefix(text, "[CONTEXT CHECKPOINT - SESSION LOG]") ||
		strings.HasPrefix(text, summaryMarkerStart)
}
