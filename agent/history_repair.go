package agent

import (
	"context"
	"fmt"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func repairOrphanedToolResults(history []schema.Turn) ([]schema.Turn, int) {
	if len(history) == 0 {
		return history, 0
	}

	out := make([]schema.Turn, 0, len(history))
	var pending []llm.ToolCallData
	repairs := 0

	flushPending := func() {
		if len(pending) == 0 {
			return
		}
		out = append(out, syntheticToolResultsTurn(pending))
		repairs += len(pending)
		pending = nil
	}

	removePending := func(callID string) {
		if callID == "" || len(pending) == 0 {
			return
		}
		dst := pending[:0]
		for _, call := range pending {
			if call.ID != callID {
				dst = append(dst, call)
			}
		}
		pending = dst
	}

	for _, turn := range history {
		switch turn.Kind {
		case schema.TurnAssistant:
			flushPending()
			out = append(out, turn)
			pending = assistantToolCalls(turn.Message)
		case schema.TurnTool, schema.TurnToolResults:
			out = append(out, turn)
			for _, part := range turn.Message.Content {
				if part.Kind == llm.ContentToolResult && part.ToolResult != nil {
					removePending(part.ToolResult.ToolCallID)
				}
			}
		default:
			flushPending()
			out = append(out, turn)
		}
	}
	flushPending()

	if repairs == 0 {
		return history, 0
	}
	return out, repairs
}

func assistantToolCalls(msg llm.Message) []llm.ToolCallData {
	var calls []llm.ToolCallData
	for _, part := range msg.Content {
		if part.Kind == llm.ContentToolCall && part.ToolCall != nil && part.ToolCall.ID != "" {
			calls = append(calls, *part.ToolCall)
		}
	}
	return calls
}

func syntheticToolResultsTurn(calls []llm.ToolCallData) schema.Turn {
	parts := make([]llm.ContentPart, 0, len(calls))
	for _, call := range calls {
		content := fmt.Sprintf(
			"Tool result unavailable: Serf was interrupted before recording output for tool call %s (%s). The tool was not rerun during recovery; do not assume it ran successfully. Inspect current state before continuing.",
			call.ID,
			call.Name,
		)
		parts = append(parts, llm.ContentPart{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: call.ID,
				Name:       call.Name,
				Content:    content,
				IsError:    true,
			},
		})
	}
	return schema.Turn{
		Kind:      schema.TurnToolResults,
		Message:   llm.Message{Role: llm.RoleTool, Content: parts},
		Timestamp: time.Now().UTC(),
	}
}

func (s *Session) repairOrphanedToolResults(reason string) int {
	s.mu.Lock()
	repaired, repairs := repairOrphanedToolResults(s.history)
	if repairs > 0 {
		s.history = repaired
	}
	s.mu.Unlock()

	if repairs > 0 {
		msg := fmt.Sprintf("Recovered %d interrupted tool call(s)", repairs)
		if reason != "" {
			msg += ": " + reason
		}
		s.emit(events.EventWarning, events.WarningData{Message: msg})
		s.maybeAutoSave()
		s.retryPendingCallerWatchSendsAfterRepair(context.Background())
	}
	return repairs
}

func (s *Session) retryPendingCallerWatchSendsAfterRepair(ctx context.Context) {
	if err := s.retryPendingCallerWatchSendsAtBoundary(ctx); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: "watch send retry after history repair failed: " + err.Error()})
	}
}
