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
	return renderStrategyHistoryRecords(buildStrategyHistoryRecords(turns, resultToolName))
}

type strategyHistoryRecord struct {
	label string
	text  string
}

func renderStrategyHistoryRecords(records []strategyHistoryRecord) string {
	var b strings.Builder
	for _, record := range records {
		b.WriteString(record.label)
		b.WriteString(record.text)
		b.WriteByte('\n')
	}
	return b.String()
}

func buildStrategyHistoryRecords(turns []schema.Turn, resultToolName string) []strategyHistoryRecord {
	const maxDetailChars = 30_000
	var records []strategyHistoryRecord
	detailChars := 0
	omitted := false
	writeDetail := func(text string) {
		if detailChars >= maxDetailChars {
			if !omitted {
				records = append(records, strategyHistoryRecord{text: "[Further assistant/tool detail omitted]"})
				omitted = true
			}
			return
		}
		records = append(records, strategyHistoryRecord{text: text})
		detailChars += len(text) + 1
	}
	for _, turn := range turns {
		switch turn.Kind {
		case schema.TurnUserInput:
			text := turn.Message.Text()
			label := "User: "
			if isStrategyCompactionText(text) {
				label = "Previous compaction: "
			}
			records = append(records, strategyHistoryRecord{label: label, text: text})
		case schema.TurnCheckpoint, schema.TurnSummary:
			records = append(records, strategyHistoryRecord{label: "Previous compaction: ", text: turn.Message.Text()})
		case schema.TurnSteering:
			label := "System: "
			if isPartnerSteering(turn) {
				label = "User: "
			} else if isParentSteering(turn) {
				label = "Parent: "
			}
			records = append(records, strategyHistoryRecord{label: label, text: turn.Message.Text()})
		case schema.TurnAssistant:
			if text := turn.Message.Text(); text != "" {
				writeDetail("Assistant: " + truncate(text, 500))
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
					writeDetail(label + ": " + truncate(message, 500))
				} else {
					writeDetail(fmt.Sprintf("  [tool_call: %s(%s)]", call.Name, truncate(string(call.Arguments), 200)))
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
				writeDetail(fmt.Sprintf("Tool(%s)%s: %s", result.Name, errTag, truncate(fmt.Sprint(result.Content), 300)))
			}
		}
	}
	return records
}

func isStrategyCompactionText(text string) bool {
	return strings.HasPrefix(text, "[CONTEXT CHECKPOINT]") ||
		strings.HasPrefix(text, "[CONTEXT CHECKPOINT - PREDICTIVE]") ||
		strings.HasPrefix(text, "[CONTEXT CHECKPOINT - SESSION LOG]") ||
		strings.HasPrefix(text, summaryMarkerStart)
}
