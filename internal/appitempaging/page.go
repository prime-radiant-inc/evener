package appitempaging

import (
	"fmt"

	"primeradiant.com/evener/appwire"
)

type TranscriptItemCandidate struct {
	TurnID          string
	Turn            appwire.Turn
	Item            appwire.ThreadItem
	Position        appwire.ThreadItemPosition
	HasEarlierItems bool
	HasLaterItems   bool
}

type TranscriptItemWindow struct {
	Candidates  []TranscriptItemCandidate
	OlderCursor string
}

// CandidatesFromTurns converts positioned item-mode turn fragments into the
// source-neutral chronological candidate representation used by hub paging.
func CandidatesFromTurns(turns []appwire.Turn) ([]TranscriptItemCandidate, error) {
	candidates := make([]TranscriptItemCandidate, 0)
	for turnIndex, turn := range turns {
		if turn.ID == "" {
			return nil, fmt.Errorf("item-mode response contains a turn without an id at ordinal %d", turnIndex)
		}
		for itemIndex, item := range turn.Items {
			if item.TranscriptKey == "" || item.Position == nil {
				return nil, fmt.Errorf("item-mode response turn %q contains an unpositioned item at ordinal %d", turn.ID, itemIndex)
			}
			candidates = append(candidates, TranscriptItemCandidate{
				TurnID:          turn.ID,
				Turn:            turn,
				Item:            item,
				Position:        *item.Position,
				HasEarlierItems: turn.HasEarlierItems || itemIndex > 0,
				HasLaterItems:   turn.HasLaterItems || itemIndex+1 < len(turn.Items),
			})
		}
	}
	return candidates, nil
}

// NormalizeProjectedItemCompleteness returns a candidate slice whose item
// completeness flags describe the boundaries of each contiguous turn fragment.
// The input slice is not modified; only the candidate structs are copied.
func NormalizeProjectedItemCompleteness(candidates []TranscriptItemCandidate) []TranscriptItemCandidate {
	normalized := append([]TranscriptItemCandidate(nil), candidates...)
	for start := 0; start < len(normalized); {
		end := start + 1
		for end < len(normalized) && normalized[end].TurnID == normalized[start].TurnID {
			end++
		}
		hasEarlier := normalized[start].HasEarlierItems
		hasLater := normalized[end-1].HasLaterItems
		for i := start; i < end; i++ {
			normalized[i].HasEarlierItems = false
			normalized[i].HasLaterItems = false
		}
		normalized[start].HasEarlierItems = hasEarlier
		normalized[end-1].HasLaterItems = hasLater
		start = end
	}
	return normalized
}

// HasTranscriptKeysPrefix reports whether old is an unchanged prefix of
// current. An appended transcript is compatible; a rewrite or truncation is
// not.
func HasTranscriptKeysPrefix(old, current []string) bool {
	if len(old) > len(current) {
		return false
	}
	for i, key := range old {
		if key != current[i] {
			return false
		}
	}
	return true
}

// SelectCandidates validates a chronological source and selects the nearest
// older atomic items before an exclusive boundary. The result remains
// chronological, while hasOlder reports whether another older position exists.
func SelectCandidates(candidates []TranscriptItemCandidate, before *appwire.ThreadItemPosition, limit int) ([]TranscriptItemCandidate, bool, error) {
	if err := validateCandidates(candidates); err != nil {
		return nil, false, err
	}
	normalized, err := appwire.NormalizeTranscriptItemLimit(limit)
	if err != nil {
		return nil, false, err
	}
	hi := len(candidates)
	if before != nil {
		if err := ValidateCursorBoundary(candidates, *before); err != nil {
			return nil, false, err
		}
		hi = 0
		for hi < len(candidates) && comparePosition(candidates[hi].Position, *before) < 0 {
			hi++
		}
	}
	lo := max(0, hi-normalized)
	selected := append([]TranscriptItemCandidate(nil), candidates[lo:hi]...)
	return selected, lo > 0, nil
}

// ValidateCandidates verifies that candidates are complete, uniquely keyed,
// and strictly chronological without selecting or regrouping them.
func ValidateCandidates(candidates []TranscriptItemCandidate) error {
	return validateCandidates(candidates)
}

// RegroupTurnFragments emits one fragment for each adjacent turn run and
// replaces the source turn's item list with only the selected candidates.
func RegroupTurnFragments(candidates []TranscriptItemCandidate) ([]appwire.Turn, error) {
	if err := validateCandidates(candidates); err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return []appwire.Turn{}, nil
	}
	turns := make([]appwire.Turn, 0, len(candidates))
	for i := 0; i < len(candidates); {
		first := candidates[i]
		turn := cloneTurnWithoutItems(first.Turn)
		turn.ID = first.TurnID
		turn.ItemsView = appwire.TurnItemsViewFragment
		turn.HasEarlierItems = false
		turn.HasLaterItems = false
		j := i
		for j < len(candidates) && candidates[j].TurnID == first.TurnID {
			candidate := candidates[j]
			turn.Items = append(turn.Items, cloneThreadItem(candidate.Item))
			turn.HasEarlierItems = turn.HasEarlierItems || candidate.HasEarlierItems
			turn.HasLaterItems = turn.HasLaterItems || candidate.HasLaterItems
			j++
		}
		turns = append(turns, turn)
		i = j
	}
	return turns, nil
}

// ValidateCursorBoundary verifies that a decoded exclusive boundary still
// names a reconstructible item in the current source projection. A missing
// boundary is stale rather than a request to clamp or restart from newest.
func ValidateCursorBoundary(candidates []TranscriptItemCandidate, before appwire.ThreadItemPosition) error {
	if err := validateCandidates(candidates); err != nil {
		return err
	}
	for _, candidate := range candidates {
		if candidate.Position == before {
			return nil
		}
	}
	return appwire.TranscriptItemCursorStale()
}

func validateCandidates(candidates []TranscriptItemCandidate) error {
	seenKeys := make(map[string]struct{}, len(candidates))
	for i, candidate := range candidates {
		if candidate.TurnID == "" {
			return fmt.Errorf("candidate %d has empty turn id", i)
		}
		if candidate.Item.TranscriptKey == "" {
			return fmt.Errorf("candidate %d has empty transcript key", i)
		}
		if candidate.Item.Position == nil {
			return fmt.Errorf("candidate %d has nil item position", i)
		}
		if *candidate.Item.Position != candidate.Position {
			return fmt.Errorf("candidate %d item position disagrees with candidate position", i)
		}
		if _, exists := seenKeys[candidate.Item.TranscriptKey]; exists {
			return fmt.Errorf("candidate %d repeats transcript key", i)
		}
		seenKeys[candidate.Item.TranscriptKey] = struct{}{}
		if i > 0 && comparePosition(candidates[i-1].Position, candidate.Position) >= 0 {
			return fmt.Errorf("candidate positions are not strictly increasing at %d", i)
		}
	}
	return nil
}

func comparePosition(a, b appwire.ThreadItemPosition) int {
	if a.Entry < b.Entry || (a.Entry == b.Entry && a.Item < b.Item) {
		return -1
	}
	if a == b {
		return 0
	}
	return 1
}

func cloneTurnWithoutItems(turn appwire.Turn) appwire.Turn {
	turn.Items = nil
	return turn
}

func cloneThreadItem(item appwire.ThreadItem) appwire.ThreadItem {
	cloned := item
	if item.Position != nil {
		position := *item.Position
		cloned.Position = &position
	}
	if item.Images != nil {
		cloned.Images = append([]appwire.InputItem(nil), item.Images...)
	}
	if item.OutputImages != nil {
		cloned.OutputImages = append([]appwire.OutputImage(nil), item.OutputImages...)
	}
	if item.Raw != nil {
		cloned.Raw = append([]byte(nil), item.Raw...)
	}
	return cloned
}
