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
	// A fold's replay copies travel with the rest of the transcript. The child
	// replays this prefix through ResumeHistory (session_init), which anchors
	// on the parent's last marker and discards everything before it — the
	// copies are the only record of the turns recorded while that fold ran, so
	// dropping them here hands the child a conversation missing its most
	// recent work. Both of resume's branches already answer correctly: with an
	// anchor the copies are kept, and with no anchor every original is still
	// present and the copies are dropped as the duplicates they are.
	entries := completedDelegateContext(data.Entries)
	out := make([]transcript.Entry, 0, len(entries))
	for _, entry := range entries {
		t := entry.Turn
		switch t.Kind {
		case schema.TurnHookCompleted, schema.TurnAttentionResolution, schema.TurnRoundTimings, schema.TurnContextCompaction, schema.TurnModelSwitch, schema.TurnFailure:
			continue
		}
		// Copy conversation and content provenance, without adopting the
		// parent's delivery receipts, client mutation IDs, usage, or server
		// continuation handles. Those belong to its execution, not the child.
		entry.Turn = schema.Turn{
			Kind:                 t.Kind,
			Message:              t.Message,
			Timestamp:            t.Timestamp,
			SteeringSource:       t.SteeringSource,
			ResponseProvider:     t.ResponseProvider,
			ResponseModel:        t.ResponseModel,
			ResponseRequestModel: t.ResponseRequestModel,
			ResponseProtocol:     t.ResponseProtocol,
			// A copy and the marker that claims it are one another's context:
			// the child replays this prefix, and a copy stripped of its role —
			// or a marker stripped of the id its copies carry — reads as an
			// anchor with nothing to keep, which discards the turns the copies
			// exist to carry past it.
			ContextReplay:    t.ContextReplay,
			CompactionFoldID: t.CompactionFoldID,
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
		case schema.TurnSteering, schema.TurnHookCompleted, schema.TurnAttentionResolution, schema.TurnRoundTimings, schema.TurnContextCompaction, schema.TurnModelSwitch:
			// Settings and telemetry can change while tools are executing.
		default:
			clear(pending)
		}
	}
	if len(pending) != 0 {
		return entries[:roundStart]
	}
	return entries
}
