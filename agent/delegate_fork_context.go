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
	data, err := readStrictChildTranscript(transcriptPath(s.stateDir, s.id), s.stateDir, s.id, s.strictTranscriptMaxLineBytes)
	if err != nil {
		return nil, fmt.Errorf("fork delegate context: %w", err)
	}
	return delegateContextEntries(data.Entries), nil
}

// delegateContextEntries is the conversation a forked delegate inherits from
// its parent's entries: the completed rounds, as conversation only.
func delegateContextEntries(parent []transcript.Entry) []transcript.Entry {
	entries := completedDelegateContext(parent)
	out := make([]transcript.Entry, 0, len(entries))
	// position maps a parent entry's index to the index it takes among the
	// child's recorded entries. The child records out in order, so this is the
	// coordinate space a fold copy's OriginalOrdinal names below.
	position := make(map[uint64]int, len(entries))
	type foldedCopy struct{ at, original int }
	var copied []foldedCopy
	for i, entry := range entries {
		t := entry.Turn
		switch t.Kind {
		case schema.TurnHookCompleted, schema.TurnModelSwitch, schema.TurnFailure:
			continue
		}
		if !t.Kind.PublicTranscript() {
			continue // never conversation
		}
		// Copy conversation and content provenance, without adopting the
		// parent's delivery receipts, client mutation IDs, usage, or server
		// continuation handles. Those belong to its execution, not the child.
		entry.Turn = schema.Turn{
			Kind:                 t.Kind,
			Authorship:           inheritedTranscriptAuthorship(t.Authorship),
			Message:              t.Message,
			Timestamp:            t.Timestamp,
			SteeringSource:       t.SteeringSource,
			SteeringKind:         t.SteeringKind,
			ResponseProvider:     t.ResponseProvider,
			ResponseModel:        t.ResponseModel,
			ResponseRequestModel: t.ResponseRequestModel,
			ResponseProtocol:     t.ResponseProtocol,
		}
		position[uint64(i)] = len(out)
		if t.OriginalOrdinal != nil {
			copied = append(copied, foldedCopy{at: len(out), original: int(*t.OriginalOrdinal)})
		}
		out = append(out, entry)
	}
	// Preserve the replay relationship across the fork. A copy inherited
	// together with its original still names that original, in the child's own
	// entry coordinates, so the child's public view holds the inherited turn
	// once while the post-marker record its resume anchors on stays in the
	// file. A copy whose original was not inherited keeps no ordinal: it is the
	// child's only record of that content, and the inherited prelude records it
	// as conversation, so it renders once as any other inherited turn does.
	for _, folded := range copied {
		if original, ok := position[uint64(folded.original)]; ok {
			ordinal := uint64(original)
			out[folded.at].Turn.OriginalOrdinal = &ordinal
		}
	}
	return out
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
		case schema.TurnSteering, schema.TurnHookCompleted, schema.TurnAttentionResolution, schema.TurnModelSwitch:
			// Settings and telemetry can change while tools are executing.
		default:
			if t.Kind.TranscriptOnly() {
				continue // recorded while tools run, and not conversation
			}
			clear(pending)
		}
	}
	if len(pending) != 0 {
		return entries[:roundStart]
	}
	return entries
}
