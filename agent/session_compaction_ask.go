package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

var errPendingAskSourceMissing = errors.New("pending ask execution absent from transcript")

// pendingAskReplayTailLocked reads persisted forms, not live tool results which
// can contain private evidence. The existing fold transaction replays these
// records past its marker with original ordinals, retaining one public item.
// The caller holds attentionMu so no append can move the read's durable cut.
func (s *Session) pendingAskReplayTailLocked() ([]schema.Turn, error) {
	ids := s.pendingAskTurnIDs()
	if len(ids) == 0 || s.stateDir == "" {
		return nil, nil
	}
	_, entries, _, err := readTranscript(context.Background(), transcriptPath(s.stateDir, s.id), s.stateDir)
	if err != nil {
		return nil, err
	}
	return pendingAskReplayTail(entries, ids)
}

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
