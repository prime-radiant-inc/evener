package agent

import (
	"errors"
	"fmt"
	"reflect"

	"primeradiant.com/evener/llm"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

// bindTranscriptOrigin records only an actual append receipt, never a payload.
// The caller holds attentionMu (or is constructing an unpublished session).
func (s *Session) bindTranscriptOrigin(turn schema.Turn, seq int) {
	if turn.Occurrence() == nil {
		return
	}
	s.mu.Lock()
	if s.historyOrigins == nil {
		s.historyOrigins = make(map[*schema.TurnOccurrence]schema.CompactionLocator)
	}
	s.historyOrigins[turn.Occurrence()] = schema.CompactionLocator{EntrySeq: seq}
	s.mu.Unlock()
}

func (s *Session) appendIndexedTranscript(turn schema.Turn, durable bool) error {
	w := s.attachedTranscript()
	var seq int
	var recorded bool
	var err error
	if durable {
		seq, recorded, err = w.AppendDurableEntry(turn)
	} else {
		seq, recorded, err = w.AppendEntry(turn)
	}
	if recorded {
		s.bindTranscriptOrigin(turn, seq)
	}
	return err
}

func (s *Session) appendSyncedTranscriptEntry(turn schema.Turn) (int, error) {
	seq, err := s.attachedTranscript().AppendSyncedEntry(turn)
	if err == nil || errors.Is(err, transcript.ErrRetainedUnsynced) {
		s.bindTranscriptOrigin(turn, seq)
	}
	return seq, err
}

func (s *Session) seedHistoryOrigins(sources map[*schema.TurnOccurrence]transcript.HistoryTurn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.historyOrigins == nil {
		s.historyOrigins = make(map[*schema.TurnOccurrence]schema.CompactionLocator)
	}
	for occurrence, source := range sources {
		s.historyOrigins[occurrence] = source.Source
	}
}

func (s *Session) pruneHistoryOriginsLocked() {
	keep := make(map[*schema.TurnOccurrence]schema.CompactionLocator, len(s.history))
	for _, turn := range s.history {
		if source, ok := s.historyOrigins[turn.Occurrence()]; ok {
			keep[turn.Occurrence()] = source
		}
	}
	s.historyOrigins = keep
	for occurrence := range s.canonicalFoldMessages {
		if _, ok := keep[occurrence]; !ok {
			delete(s.canonicalFoldMessages, occurrence)
		}
	}
}

func (s *Session) markCanonicalFoldMessageLocked(live, persisted schema.Turn) {
	if live.Kind != schema.TurnToolResults && live.Kind != schema.TurnTool {
		return
	}
	if reflect.DeepEqual(live.Message, persisted.Message) {
		return
	}
	if s.canonicalFoldMessages == nil {
		s.canonicalFoldMessages = make(map[*schema.TurnOccurrence]bool)
	}
	s.canonicalFoldMessages[live.Occurrence()] = true
}

// canonicalFoldHistory substitutes only recorded private tool-result messages.
// Notes/provenance projections and every other live field remain unchanged.
// Metadata is snapshotted under mu; the single streaming validation/read is
// outside it and retains only the exact messages needed by this fold snapshot.
func (s *Session) canonicalFoldHistory(history []schema.Turn) ([]schema.Turn, error) {
	if s.stateDir == "" {
		return history, nil
	}
	positions := make(map[int][]int)
	s.mu.Lock()
	for i, turn := range history {
		if !s.canonicalFoldMessages[turn.Occurrence()] {
			continue
		}
		source, ok := s.historyOrigins[turn.Occurrence()]
		if !ok || source.AddedIndex != nil {
			s.mu.Unlock()
			return nil, fmt.Errorf("private fold input has no canonical recorded source")
		}
		positions[source.EntrySeq] = append(positions[source.EntrySeq], i)
	}
	s.mu.Unlock()
	if len(positions) == 0 {
		return history, nil
	}
	messages := make(map[int]llm.Message, len(positions))
	ambiguous := false
	data, err := readSemanticTranscriptVisit(s.TranscriptPath(), transcript.DefaultMaxLineBytes, false, false, nil, func(entry transcript.Entry) {
		if _, wanted := positions[entry.Seq]; wanted {
			if _, exists := messages[entry.Seq]; exists {
				ambiguous = true
			}
			messages[entry.Seq] = entry.Turn.Message
		}
	})
	if err != nil {
		return nil, fmt.Errorf("read canonical fold input: %w", err)
	}
	if ambiguous {
		return nil, fmt.Errorf("canonical fold input source is ambiguous")
	}
	if data.Header.SessionID != s.id {
		return nil, fmt.Errorf("canonical fold input transcript owner mismatch")
	}
	for seq, indices := range positions {
		message, ok := messages[seq]
		if !ok {
			return nil, fmt.Errorf("canonical fold input entry %d missing", seq)
		}
		for _, index := range indices {
			history[index].Message = message
		}
	}
	return history, nil
}
