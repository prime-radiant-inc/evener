package agent

import (
	"context"
	"fmt"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/invariant"
	"primeradiant.com/evener/llm"
)

type pendingWatchSendDrainFaultKey struct{}

func (s *Session) drainPendingWatchSendsAtBoundary(ctx context.Context) error {
	if err, _ := ctx.Value(pendingWatchSendDrainFaultKey{}).(error); err != nil {
		return err
	}
	return s.drainPendingWatchSends(ctx)
}

func repairOrphanedToolResults(history []schema.Turn) ([]schema.Turn, int) {
	out, repairs, _ := repairOrphanedToolResultsIndexed(history)
	return out, repairs
}

// repairOrphanedToolResultsIndexed is repairOrphanedToolResults, also
// reporting each synthetic turn's insertion index in the returned slice
// (ascending, post-repair coordinates), so a caller tracking the N4
// in-flight-turn boundary can shift it per insertion at or before the
// boundary.
func repairOrphanedToolResultsIndexed(history []schema.Turn) ([]schema.Turn, int, []int) {
	if len(history) == 0 {
		return history, 0, nil
	}

	out := make([]schema.Turn, 0, len(history))
	var pending []llm.ToolCallData
	repairs := 0
	var insertedAt []int

	flushPending := func() {
		if len(pending) == 0 {
			return
		}
		insertedAt = append(insertedAt, len(out))
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
		case schema.TurnHookCompleted, schema.TurnAttentionResolution, schema.TurnSteering:
			// Presentational markers and client steering can legally be recorded
			// while a tool is still running. Preserve their chronological order
			// without treating the pending call as interrupted. expandHistory
			// keeps model-visible steering after the matching result on the wire.
			out = append(out, turn)
		default:
			flushPending()
			out = append(out, turn)
		}
	}
	flushPending()

	if repairs == 0 {
		return history, 0, nil
	}
	// Repair must produce well-formed history: every assistant tool call now has a
	// matching tool result, so a second pass finds nothing left to repair. If this
	// trips, the synthetic-result insertion missed a call and a downstream provider
	// would reject the transcript. The re-scan is a full pass, so it is gated out of
	// production builds.
	if invariant.Enabled {
		_, again := repairOrphanedToolResults(out)
		invariant.Hold(again == 0, "repairOrphanedToolResults left %d orphaned tool call(s) after repair", again)
	}
	return out, repairs, insertedAt
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
			"Tool result unavailable: Evener was interrupted before recording output for tool call %s (%s). The tool was not rerun during recovery; do not assume it ran successfully. Inspect current state before continuing.",
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

// repairOrphanedToolResults captures, repairs, and publishes s.history under
// ONE critical section: a turn appended concurrently is either inside the
// captured history (and so preserved by the repair) or lands after the
// publish, and a fold publishing concurrently either wins before the capture
// (so the repair operates on its result) or conflicts on the revision this
// bump moves (a snapshot/repair/replace split across two lock acquisitions
// would drop concurrent appends and clobber concurrent publishes). ctx
// scopes only the post-repair watch-send retry; boundary callers with no
// turn context pass context.Background().
func (s *Session) repairOrphanedToolResults(ctx context.Context, reason string) int {
	if hook := s.cfg.testOnly.beforeHistoryRepairPublish; hook != nil {
		hook()
	}
	s.mu.Lock()
	repaired, repairs, insertedAt := repairOrphanedToolResultsIndexed(s.history)
	if repairs > 0 {
		s.history = repaired
		// A synthetic turn spliced at or before the N4 boundary shifts every
		// in-flight turn right by one, so the boundary moves with them —
		// atomically with the mutation, in this same locked section. "At or
		// before" is deliberate: a synthetic landing exactly at the boundary
		// completes the pre-boundary call it repairs
		// (a call at or after the boundary always yields a synthetic
		// strictly past it), so the boundary must still move past the
		// insertion. insertedAt ascends in post-repair coordinates, matching
		// this incremental comparison.
		for _, idx := range insertedAt {
			if idx <= s.turnHistoryBaseline {
				s.turnHistoryBaseline++
			}
		}
		// Repair splices a synthetic turn wherever the orphaned tool call
		// was, not just at the end — a fold snapshotted before this must not
		// be able to publish over it.
		s.bumpHistoryRevisionLocked()
	}
	s.mu.Unlock()

	if repairs > 0 {
		msg := fmt.Sprintf("Recovered %d interrupted tool call(s)", repairs)
		if reason != "" {
			msg += ": " + reason
		}
		s.emit(events.EventWarning, events.WarningData{Message: msg})
		s.maybeAutoSave()
		s.retryPendingCallerWatchSendsAfterRepair(ctx)
	}
	return repairs
}

func (s *Session) retryPendingCallerWatchSendsAfterRepair(ctx context.Context) {
	if err := s.drainPendingWatchSendsAtBoundary(ctx); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: "watch send retry after history repair failed: " + err.Error()})
	}
}
