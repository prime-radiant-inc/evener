package agent

import (
	"errors"
	"fmt"
	"slices"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

var errPendingAskSourceMissing = errors.New("pending ask execution absent from transcript")

// pendingAskReplayTail selects persisted forms from the fold publisher's
// immutable transcript snapshot, never private live tool results.
func pendingAskReplayTail(entries []transcript.Entry, ids []string) ([]schema.Turn, error) {
	var tail []schema.Turn
	found := map[string]bool{}
	started := false
	for i, entry := range entries {
		turn := entry.Turn
		if turn.OriginalOrdinal != nil {
			continue
		}
		if slices.Contains(ids, turn.TurnID) {
			started = true
			if turn.Kind == schema.TurnAssistant {
				found[turn.TurnID] = true
			}
		}
		if !started || turn.Kind.TranscriptOnly() || turn.Kind == schema.TurnCheckpoint || turn.Kind == schema.TurnSummary {
			continue
		}
		ordinal := uint64(i)
		turn.OriginalOrdinal = &ordinal
		tail = append(tail, turn)
	}
	for _, id := range ids {
		if !found[id] {
			return nil, fmt.Errorf("%w: %s", errPendingAskSourceMissing, id)
		}
	}
	return tail, nil
}
