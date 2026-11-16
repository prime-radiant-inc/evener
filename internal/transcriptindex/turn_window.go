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

// turnBoundary is one turn in transcript order: its id and its turn-table
// slot, if the slot holds an indexed summary. A header prelude has no summary
// of its own. first is the first entry ordinal the turn can open and version
// the last one it accounts, both zero on a turn with no summary.
type turnBoundary struct {
	id      string
	slot    uint32
	indexed bool
	first   uint64
	version uint64
}

func (x *Index) resolveTurnWindow(r *reader, selector appwire.ThreadTurnWindow) (ResolvedTurnWindow, error) {
	start, end, frontier, err := x.selectTurnRange(selector)
	if err != nil {
		return ResolvedTurnWindow{}, err
	}
	selected, err := x.turnOrder(start, end-start)
	if err != nil {
		return ResolvedTurnWindow{}, x.fail(err)
	}
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
	candidates, err := x.candidatesForTurnSlots(r, selected)
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

// turnCount is how many turns the frontier names: every indexed summary, plus
// the prelude when no indexed summary represents it.
func (x *Index) turnCount() (int, error) {
	prelude, err := x.preludeBoundary()
	if err != nil {
		return 0, err
	}
	count := int(x.turns.n)
	if prelude != nil {
		count++
	}
	return count, nil
}

// turnOrder returns count turns starting at position in transcript order: the
// prelude first when no indexed summary represents it, then the turn table. It
// reads only the summaries it returns.
func (x *Index) turnOrder(position, count int) ([]turnBoundary, error) {
	if count <= 0 {
		return nil, nil
	}
	prelude, err := x.preludeBoundary()
	if err != nil {
		return nil, err
	}
	turns := make([]turnBoundary, 0, count)
	tableStart := position
	if prelude != nil {
		tableStart--
		if position == 0 {
			turns = append(turns, *prelude)
			tableStart, count = 0, count-1
		}
	}
	if count <= 0 {
		return turns, nil
	}
	if tableStart < 0 || uint64(tableStart) > x.turns.n || uint64(count) > x.turns.n-uint64(tableStart) {
		return nil, fmt.Errorf("%w: turns %d..%d past %d", errCorrupt, position, position+count, x.turns.n)
	}
	buf, err := x.turns.read(uint64(tableStart), count)
	if err != nil {
		return nil, err
	}
	for i := range count {
		slot := uint64(tableStart) + uint64(i)
		record := decodeTurn(buf[i*turnRecordSize:])
		id, err := x.strings.get(record.ID)
		if err != nil {
			return nil, err
		}
		turns = append(turns, turnBoundary{
			id: string(id), slot: uint32(slot), indexed: true,
			first: record.FirstOrdinal + 1, version: record.Version,
		})
	}
	return turns, nil
}

// preludeBoundary returns the header prelude's boundary, or nil when the
// covered transcript has no header or a turn summary already represents that
// same turn. The check is by id across every summary, which the turn-slot
// table answers as a lookup; a table that cannot name it reads the turn table.
func (x *Index) preludeBoundary() (*turnBoundary, error) {
	if x.prelude == nil {
		return nil, nil
	}
	if x.turns.n > 0 {
		_, found, err := x.turnSlotOf(x.prelude.ID)
		if err != nil {
			return nil, err
		}
		if found {
			return nil, nil
		}
	}
	return &turnBoundary{id: x.prelude.ID}, nil
}

// selectTurnRange resolves a selector to a half-open position range in
// transcript order, together with the frontier those positions index into. It
// finds the boundary through the turn-slot table, so a constant-sized window
// costs a lookup and a handful of record reads rather than a walk of every
// summary.
func (x *Index) selectTurnRange(selector appwire.ThreadTurnWindow) (int, int, TurnFrontier, error) {
	total, err := x.turnCount()
	if err != nil {
		return 0, 0, TurnFrontier{}, x.fail(err)
	}
	frontier := TurnFrontier{TurnCount: total}
	if total > 0 {
		oldest, err := x.turnOrder(0, 1)
		if err != nil {
			return 0, 0, TurnFrontier{}, x.fail(err)
		}
		newest, err := x.turnOrder(total-1, 1)
		if err != nil {
			return 0, 0, TurnFrontier{}, x.fail(err)
		}
		if len(oldest) == 1 && len(newest) == 1 {
			frontier.OldestTurnID, frontier.NewestTurnID = oldest[0].id, newest[0].id
		}
	}
	if selector.Latest > 0 {
		return max(0, total-selector.Latest), total, frontier, nil
	}
	boundary := selector.Turn
	if selector.Since != "" {
		boundary = selector.Since
	}
	if selector.Before != nil {
		boundary = selector.Before.TurnID
	}
	at, found, err := x.turnPosition(boundary)
	if err != nil {
		return 0, 0, TurnFrontier{}, x.fail(err)
	}
	if !found {
		return 0, 0, TurnFrontier{}, &TurnNotFoundError{TurnID: boundary, Frontier: frontier}
	}
	switch {
	case selector.Turn != "":
		return at, at + 1, frontier, nil
	case selector.Since != "":
		start := at + 1
		return start, start + min(total-start, selector.Limit), frontier, nil
	default:
		end := at
		return max(0, end-selector.Before.Limit), end, frontier, nil
	}
}

// turnPosition names the transcript position of the last turn whose id is id.
// The header prelude is a position of its own that no turn record holds, so it
// is checked before the table. Every other id is answered by the turn-slot
// table; a table that cannot name it falls back to reading the turn table, so
// the answer is exactly the one a backwards scan gives.
func (x *Index) turnPosition(id string) (int, bool, error) {
	prelude, err := x.preludeBoundary()
	if err != nil {
		return 0, false, err
	}
	if prelude != nil && prelude.id == id {
		return 0, true, nil
	}
	slot, found, err := x.turnSlotOf(id)
	if err != nil {
		return 0, false, err
	}
	if !found {
		return 0, false, nil
	}
	position := int(slot)
	if prelude != nil {
		position++
	}
	return position, true, nil
}

// candidatesForTurnSlots projects the items of the selected turns in
// transcript order. A turn can have several runs when another turn was
// recorded inside it; appending the runs preserves the transcript's item
// order.
//
// Item records are sorted by position and an item's Entry names its opener's
// entry ordinal plus one, so every item of a selected turn lies between that
// turn's first entry ordinal and its last. The scan therefore starts at the
// earliest selected turn's first item, found by binary search, and walks only
// that bounded span: a window selects a contiguous run of turns, so the span is
// the window's own entries, not the session's.
func (x *Index) candidatesForTurnSlots(r *reader, selected []turnBoundary) ([]appitempaging.TranscriptItemCandidate, error) {
	if len(selected) == 0 || x.items.n == 0 {
		return nil, nil
	}
	slots := make(map[uint32]bool, len(selected))
	// lo is the first entry ordinal a selected turn can open, hi the last one
	// it accounts; both are inclusive. They are taken independently so a
	// selected turn that accounts no entry (Version 0, an interrupted open)
	// cannot narrow the span another selected turn needs.
	var lo, hi uint64
	spanned := false
	for _, turn := range selected {
		if !turn.indexed {
			continue
		}
		slots[turn.slot] = true
		if !spanned {
			lo, hi, spanned = turn.first, turn.version, true
		} else {
			lo, hi = min(lo, turn.first), max(hi, turn.version)
		}
	}
	if !spanned {
		return nil, nil
	}
	start, err := x.items.search(func(buf []byte) bool { return decodeItem(buf).Entry < lo })
	if err != nil {
		return nil, x.fail(err)
	}
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
	// end is the first item slot past the selected turns' entries: the scan
	// stops there, and an open run ends there, so no item outside the window
	// is ever projected.
	end := x.items.n
	const chunk = uint64(256)
	for base := start; base < x.items.n; base += chunk {
		count := min(chunk, x.items.n-base)
		buf, err := x.items.read(base, int(count))
		if err != nil {
			return nil, x.fail(err)
		}
		for i := range count {
			itemSlot := base + i
			item := decodeItem(buf[i*itemRecordSize:])
			included := item.Entry <= hi && slots[item.Turn]
			if included && !inRun {
				runStart, inRun = itemSlot, true
			}
			if !included && inRun {
				if err := appendRun(itemSlot); err != nil {
					return nil, err
				}
				inRun = false
			}
			if item.Entry > hi {
				end = itemSlot
				break
			}
		}
		if end != x.items.n {
			break
		}
	}
	if inRun {
		if err := appendRun(end); err != nil {
			return nil, err
		}
	}
	return candidates, nil
}
