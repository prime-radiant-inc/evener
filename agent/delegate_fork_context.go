package agent

import (
	"fmt"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

// snapshotDelegateContext takes an independent, durable conversation snapshot.
// attentionMu excludes both appends and compaction publication while we read;
// decoding the transcript gives the child its own message and content objects.
func (s *Session) snapshotDelegateContext() ([]transcript.Entry, error) {
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	data, err := readStrictChildTranscript(transcriptPath(s.stateDir, s.id), s.id, s.strictTranscriptMaxLineBytes)
	if err != nil {
		return nil, fmt.Errorf("fork delegate context: %w", err)
	}
	entries := completedDelegateContext(data.Entries)
	out := make([]transcript.Entry, 0, len(entries))
	for _, entry := range entries {
		t := entry.Turn
		switch t.Kind {
		case schema.TurnHookCompleted, schema.TurnAttentionResolution, schema.TurnModelSwitch, schema.TurnFailure:
			continue
		}
		// Copy conversation and content provenance, without adopting the
		// parent's delivery receipts, client mutation IDs, usage, or server
		// continuation handles. Those belong to its execution, not the child.
		entry.Turn = schema.Turn{
			Kind: t.Kind, Message: t.Message, Timestamp: t.Timestamp,
			SteeringSource:   t.SteeringSource,
			ResponseProvider: t.ResponseProvider, ResponseModel: t.ResponseModel,
			ResponseRequestModel: t.ResponseRequestModel, ResponseProtocol: t.ResponseProtocol,
		}
		out = append(out, entry)
	}
	return out, nil
}

// completedDelegateContext cuts before an unfinished assistant tool round.
// In particular the call creating this delegate has no result yet, and cannot
// be inherited as a pending action for the child to resume or repair.
func completedDelegateContext(entries []transcript.Entry) []transcript.Entry {
	pending := make(map[string]bool)
	roundStart := 0
	for i, entry := range entries {
		t := entry.Turn
		switch t.Kind {
		case schema.TurnAssistant:
			clear(pending)
			roundStart = i
			for _, call := range assistantToolCalls(t.Message) {
				pending[call.ID] = true
			}
		case schema.TurnTool, schema.TurnToolResults:
			for _, part := range t.Message.Content {
				if part.ToolResult != nil {
					delete(pending, part.ToolResult.ToolCallID)
				}
			}
		case schema.TurnSteering, schema.TurnHookCompleted, schema.TurnAttentionResolution:
		default:
			clear(pending)
		}
	}
	if len(pending) != 0 {
		return entries[:roundStart]
	}
	return entries
}
