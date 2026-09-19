package agent

import (
	"errors"
	"fmt"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

var errFoldDurabilityPending = errors.New("compaction marker is recorded but its durability is unresolved")

type pendingFoldPublication struct {
	seq         int
	applyLocked func()
	commit      *foldCommit
}

// foldMarkerLocked selects only winning canonical fold artifacts. Existing
// occurrences always reference their persisted forms, even when live masking
// or private evidence changed their model messages.
func (s *Session) foldMarkerLocked(history []schema.Turn, commit *foldCommit) (schema.Turn, int, error) {
	markerIndex := -1
	for i, t := range history {
		if _, ok := commit.artifacts[t.Occurrence()]; ok && (t.Kind == schema.TurnCheckpoint || t.Kind == schema.TurnSummary) {
			markerIndex = i
			break
		}
	}
	if markerIndex < 0 {
		if s.stateDir != "" {
			for _, turn := range history {
				if _, staged := commit.artifacts[turn.Occurrence()]; staged && !turn.IsStrategyArtifact() {
					return schema.Turn{}, -1, fmt.Errorf("generated fold history has no new final compaction marker")
				}
			}
		}
		return schema.Turn{}, -1, nil
	}
	marker := commit.artifacts[history[markerIndex].Occurrence()]
	if err := schema.ValidateCompactionArtifact(marker); err != nil {
		return schema.Turn{}, -1, err
	}
	manifest := &schema.CompactionManifest{Version: 1, SessionID: s.id}
	for i, t := range history {
		if i == markerIndex {
			continue
		}
		if t.AttentionID != "" || t.AttentionResolution != nil || t.IsHistoryRepair() {
			continue
		}
		if i < markerIndex {
			return schema.Turn{}, -1, fmt.Errorf("retained history precedes final compaction marker")
		}
		if source, ok := s.historyOrigins[t.Occurrence()]; ok {
			manifest.History = append(manifest.History, schema.CompactionHistoryItem{Source: &source})
		} else if canonical, ok := commit.artifacts[t.Occurrence()]; ok {
			if err := schema.ValidateCompactionArtifact(canonical); err != nil {
				return schema.Turn{}, -1, err
			}
			manifest.History = append(manifest.History, schema.CompactionHistoryItem{Added: &canonical})
		} else if t.IsStrategyArtifact() {
			if err := schema.ValidateCompactionArtifact(t); err != nil {
				return schema.Turn{}, -1, err
			}
			manifest.History = append(manifest.History, schema.CompactionHistoryItem{Added: &t})
		} else if s.stateDir != "" {
			return schema.Turn{}, -1, fmt.Errorf("retained %s occurrence has no recorded transcript origin", t.Kind)
		}
	}
	if len(manifest.History) > transcript.MaxCompactionItems {
		return schema.Turn{}, -1, fmt.Errorf("too many retained compaction occurrences")
	}
	marker.Compaction = manifest
	return marker, markerIndex, nil
}

func (s *Session) bindFoldOriginsLocked(history []schema.Turn, markerIndex int, marker schema.Turn, seq int) {
	if s.historyOrigins == nil {
		s.historyOrigins = make(map[*schema.TurnOccurrence]schema.CompactionLocator)
	}
	s.historyOrigins[history[markerIndex].Occurrence()] = schema.CompactionLocator{EntrySeq: seq}
	for i, item := range marker.Compaction.History {
		if item.Added != nil {
			index := i
			s.historyOrigins[item.Added.Occurrence()] = schema.CompactionLocator{EntrySeq: seq, AddedIndex: &index}
		}
	}
}

func (s *Session) finishFoldPublication(commit *foldCommit) {
	s.surfaceTranscriptWarnings()
	if hook := s.cfg.testOnly.beforeFoldSideEffectsFlush; hook != nil {
		hook()
	}
	commit.flush()
	s.commitSkillCompactionPublication(commit)
}

// settlePendingFold uses the writer's existing barrier on the same recorded
// marker. It never repeats summary work, appends, claims, or effects.
func (s *Session) settlePendingFold() (bool, error) {
	s.attentionMu.Lock()
	pending := s.pendingFold
	if pending == nil {
		s.attentionMu.Unlock()
		return false, nil
	}
	if err := s.attachedTranscript().EstablishDurability(); err != nil {
		s.attentionMu.Unlock()
		return false, fmt.Errorf("%w (entry %d): %w", errFoldDurabilityPending, pending.seq, err)
	}
	s.mu.Lock()
	pending.applyLocked()
	s.pendingFold = nil
	s.mu.Unlock()
	s.attentionMu.Unlock()
	s.finishFoldPublication(pending.commit)
	return true, nil
}
