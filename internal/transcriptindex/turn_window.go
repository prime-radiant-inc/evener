package transcriptindex

import (
	"fmt"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

// TurnFrontier identifies the transcript's current recorded turn range.
// The ids are empty when TurnCount is zero.
type TurnFrontier struct {
	OldestTurnID string
	NewestTurnID string
	TurnCount    int
}

// TurnNotFoundError reports a named turn boundary that the current transcript
// does not contain. Frontier describes the snapshot searched.
type TurnNotFoundError struct {
	TurnID   string
	Frontier TurnFrontier
}

func (e *TurnNotFoundError) Error() string {
	return fmt.Sprintf("turn %q is outside the current transcript frontier", e.TurnID)
}

// ResolvedTurnWindow is a turn-addressed read at one index snapshot.
// TurnIDs name the selected turns in transcript order, including selected
// turns with no projected items. Candidates keeps projected items in transcript
// order. Frontier describes all turns available at the same snapshot.
type ResolvedTurnWindow struct {
	TurnIDs     []string
	Candidates  []appitempaging.TranscriptItemCandidate
	Frontier    TurnFrontier
	Incarnation string
	Length      int64
}

// ResolveTurnWindow resolves a Manager turn selector against the
// current index snapshot. Exact windows include their named turn. Since and
// before windows exclude their named boundary. Since returns the oldest unseen
// turns first; every result stays in transcript order.
func (x *Index) ResolveTurnWindow(selector appwire.ThreadTurnWindow) (ResolvedTurnWindow, error) {
	if err := appwire.ValidateThreadWindowParams(appwire.ThreadWindowParams{TurnWindow: &selector}, 0); err != nil {
		return ResolvedTurnWindow{}, err
	}
	x.mu.Lock()
	defer x.mu.Unlock()

	var resolved ResolvedTurnWindow
	_, err := x.readWindow(func(r *reader) (Window, error) {
		var err error
		resolved, err = x.resolveTurnWindow(r, selector)
		return Window{}, err
	})
	return resolved, err
}

type indexedTurn struct {
	id      string
	slot    uint32
	indexed bool
}

func (x *Index) resolveTurnWindow(r *reader, selector appwire.ThreadTurnWindow) (ResolvedTurnWindow, error) {
	turns, err := x.indexedTurnOrder()
	if err != nil {
		return ResolvedTurnWindow{}, x.fail(err)
	}
	frontier := frontierOf(turns)
	start, end, err := selectTurnRange(turns, frontier, selector)
	if err != nil {
		return ResolvedTurnWindow{}, err
	}
	selected := turns[start:end]
	result := ResolvedTurnWindow{
		TurnIDs:     make([]string, len(selected)),
		Frontier:    frontier,
		Incarnation: x.meta.Incarnation,
		Length:      x.meta.Length,
	}
	selectedSlots := make(map[uint32]bool, len(selected))
	selectedIDs := make(map[string]bool, len(selected))
	for i, turn := range selected {
		result.TurnIDs[i] = turn.id
		selectedIDs[turn.id] = true
		if turn.indexed {
			selectedSlots[turn.slot] = true
		}
	}

	if x.prelude != nil && selectedIDs[x.prelude.ID] {
		for i := range x.prelude.Items {
			result.Candidates = append(result.Candidates, x.preludeCandidate(i))
		}
	}
	candidates, err := x.candidatesForTurnSlots(r, selectedSlots)
	if err != nil {
		return ResolvedTurnWindow{}, err
	}
	result.Candidates = append(result.Candidates, candidates...)
	flush, err := x.pendingFlush(r)
	if err != nil {
		return ResolvedTurnWindow{}, err
	}
	var selectedFlush []appitempaging.TranscriptItemCandidate
	for _, candidate := range flush {
		if selectedIDs[candidate.TurnID] {
			selectedFlush = append(selectedFlush, candidate)
		}
	}
	if len(selectedFlush) > 0 && len(result.Candidates) > 0 && result.Candidates[len(result.Candidates)-1].TurnID == selectedFlush[0].TurnID {
		result.Candidates[len(result.Candidates)-1].HasLaterItems = true
	}
	result.Candidates = append(result.Candidates, selectedFlush...)
	return result, nil
}

// indexedTurnOrder returns turn summaries in first-recorded order. A header
// prelude has no summary of its own, so it is prepended unless an indexed
// turn_system summary already represents that same turn.
func (x *Index) indexedTurnOrder() ([]indexedTurn, error) {
	const chunk = uint64(256)
	turns := make([]indexedTurn, 0, x.turns.n+1)
	for start := uint64(0); start < x.turns.n; start += chunk {
		count := min(chunk, x.turns.n-start)
		buf, err := x.turns.read(start, int(count))
		if err != nil {
			return nil, err
		}
		for i := range count {
			record := decodeTurn(buf[i*turnRecordSize:])
			id, err := x.strings.get(record.ID)
			if err != nil {
				return nil, err
			}
			turns = append(turns, indexedTurn{id: string(id), slot: uint32(start + i), indexed: true})
		}
	}
	if x.prelude == nil {
		return turns, nil
	}
	for _, turn := range turns {
		if turn.id == x.prelude.ID {
			return turns, nil
		}
	}
	return append([]indexedTurn{{id: x.prelude.ID}}, turns...), nil
}

func frontierOf(turns []indexedTurn) TurnFrontier {
	frontier := TurnFrontier{TurnCount: len(turns)}
	if len(turns) > 0 {
		frontier.OldestTurnID = turns[0].id
		frontier.NewestTurnID = turns[len(turns)-1].id
	}
	return frontier
}

func selectTurnRange(turns []indexedTurn, frontier TurnFrontier, selector appwire.ThreadTurnWindow) (int, int, error) {
	if selector.Latest > 0 {
		return max(0, len(turns)-selector.Latest), len(turns), nil
	}
	boundary := selector.Turn
	if selector.Since != "" {
		boundary = selector.Since
	}
	if selector.Before != nil {
		boundary = selector.Before.TurnID
	}
	at := -1
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].id == boundary {
			at = i
			break
		}
	}
	if at < 0 {
		return 0, 0, &TurnNotFoundError{TurnID: boundary, Frontier: frontier}
	}
	switch {
	case selector.Turn != "":
		return at, at + 1, nil
	case selector.Since != "":
		start := at + 1
		return start, start + min(len(turns)-start, selector.Limit), nil
	default:
		end := at
		return max(0, end-selector.Before.Limit), end, nil
	}
}

// candidatesForTurnSlots scans the compact item records once and projects only
// contiguous runs that belong to selected turns. A turn can have several runs
// when another turn was recorded inside it; appending the runs preserves the
// transcript's item order.
func (x *Index) candidatesForTurnSlots(r *reader, selected map[uint32]bool) ([]appitempaging.TranscriptItemCandidate, error) {
	if len(selected) == 0 || x.items.n == 0 {
		return nil, nil
	}
	const chunk = uint64(256)
	var candidates []appitempaging.TranscriptItemCandidate
	var runStart uint64
	inRun := false
	appendRun := func(end uint64) error {
		span, err := x.span(r, runStart, end)
		if err != nil {
			return err
		}
		candidates = append(candidates, span...)
		return nil
	}
	for start := uint64(0); start < x.items.n; start += chunk {
		count := min(chunk, x.items.n-start)
		buf, err := x.items.read(start, int(count))
		if err != nil {
			return nil, x.fail(err)
		}
		for i := range count {
			slot := start + i
			included := selected[decodeItem(buf[i*itemRecordSize:]).Turn]
			if included && !inRun {
				runStart, inRun = slot, true
			}
			if !included && inRun {
				if err := appendRun(slot); err != nil {
					return nil, err
				}
				inRun = false
			}
		}
	}
	if inRun {
		if err := appendRun(x.items.n); err != nil {
			return nil, err
		}
	}
	return candidates, nil
}
