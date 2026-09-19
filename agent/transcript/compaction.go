package transcript

import (
	"errors"
	"fmt"

	"primeradiant.com/evener/agent/schema"
)

// MaxCompactionItems bounds the ordered retained-history manifest.
const MaxCompactionItems = 65536

type compactionSource struct {
	ambiguous  bool
	referenced bool
	attention  bool
	added      map[int]bool
}

// CompactionValidator validates backward local locators without retaining any
// message payload. Semantic readers use it even when they discard entries.
type CompactionValidator struct {
	sessionID string
	sources   map[int]compactionSource
}

// NewCompactionValidator binds source validation to one transcript owner.
func NewCompactionValidator(sessionID string) *CompactionValidator {
	return &CompactionValidator{sessionID: sessionID, sources: make(map[int]compactionSource)}
}

// Observe validates one physical entry before remembering its source metadata.
func (v *CompactionValidator) Observe(e Entry) error {
	source := compactionSource{attention: e.Turn.AttentionID != "" || e.Turn.AttentionResolution != nil}
	if m := e.Turn.Compaction; m != nil {
		if m.Version != 1 || m.SessionID == "" || m.SessionID != v.sessionID {
			return errors.New("invalid compaction version or transcript owner")
		}
		if e.Turn.Kind != schema.TurnSummary && e.Turn.Kind != schema.TurnCheckpoint {
			return errors.New("compaction manifest requires final marker")
		}
		if len(m.History) > MaxCompactionItems {
			return errors.New("too many compaction history items")
		}
		type key struct{ seq, index int }
		seen := make(map[key]bool, len(m.History))
		for i, item := range m.History {
			if (item.Source == nil) == (item.Added == nil) {
				return fmt.Errorf("compaction item %d requires exactly one arm", i)
			}
			if item.Added != nil {
				if err := schema.ValidateCompactionArtifact(*item.Added); err != nil {
					return fmt.Errorf("compaction item %d: %w", i, err)
				}
				if source.added == nil {
					source.added = make(map[int]bool)
				}
				source.added[i] = true
				continue
			}
			locator := item.Source
			target, ok := v.sources[locator.EntrySeq]
			if !ok || target.ambiguous || target.attention || locator.EntrySeq >= e.Seq {
				return fmt.Errorf("invalid compaction source %d", locator.EntrySeq)
			}
			k := key{locator.EntrySeq, -1}
			if locator.AddedIndex != nil {
				k.index = *locator.AddedIndex
				if k.index < 0 || !target.added[k.index] {
					return errors.New("compaction source is not a direct inline artifact")
				}
			}
			if seen[k] {
				return errors.New("duplicate compaction source")
			}
			seen[k] = true
			target.referenced = true
			v.sources[locator.EntrySeq] = target
		}
	}
	if prior, exists := v.sources[e.Seq]; exists {
		if prior.referenced {
			return fmt.Errorf("ambiguous compaction source %d", e.Seq)
		}
		source.ambiguous = true
	}
	v.sources[e.Seq] = source
	return nil
}

// ValidateCompactionManifests validates a complete raw transcript projection.
func ValidateCompactionManifests(sessionID string, entries []Entry) error {
	validator := NewCompactionValidator(sessionID)
	for _, entry := range entries {
		if err := validator.Observe(entry); err != nil {
			return fmt.Errorf("entry %d: %w", entry.Seq, err)
		}
	}
	return nil
}

// HistoryTurn retains the physical source of a projected occurrence. Index is
// the source's raw position, used for fork provenance independently of model order.
type HistoryTurn struct {
	Turn   schema.Turn
	Source schema.CompactionLocator
	Index  int
}

// ProjectHistory materializes the last committed marker and its exact retained
// tail. Referenced older markers are ordinary turns, never recursive projections.
func ProjectHistory(sessionID string, entries []Entry) ([]HistoryTurn, error) {
	if err := ValidateCompactionManifests(sessionID, entries); err != nil {
		return nil, err
	}
	start := 0
	positions := make(map[int]int, len(entries))
	for i := range entries {
		entries[i].Turn.EnsureOccurrence()
		positions[entries[i].Seq] = i
		if entries[i].Turn.Kind == schema.TurnCheckpoint || entries[i].Turn.Kind == schema.TurnSummary {
			start = i
		}
	}
	var result []HistoryTurn
	for i := start; i < len(entries); i++ {
		e := entries[i]
		result = append(result, HistoryTurn{Turn: e.Turn, Source: schema.CompactionLocator{EntrySeq: e.Seq}, Index: i})
		if i != start || e.Turn.Compaction == nil {
			continue
		}
		for j, item := range e.Turn.Compaction.History {
			if item.Added != nil {
				item.Added.EnsureOccurrence()
				index := j
				result = append(result, HistoryTurn{Turn: *item.Added, Source: schema.CompactionLocator{EntrySeq: e.Seq, AddedIndex: &index}, Index: i})
			} else {
				index := positions[item.Source.EntrySeq]
				turn := entries[index].Turn
				if item.Source.AddedIndex != nil {
					added := turn.Compaction.History[*item.Source.AddedIndex].Added
					added.EnsureOccurrence()
					turn = *added
				}
				result = append(result, HistoryTurn{Turn: turn, Source: *item.Source, Index: index})
			}
		}
	}
	return result, nil
}
