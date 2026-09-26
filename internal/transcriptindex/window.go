package transcriptindex

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"slices"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// Window is one item-window read.
type Window struct {
	// Candidates are the items, oldest first. Each carries its whole turn's
	// scalars (Turn.Items is empty) and whether the turn has items before or
	// after it.
	Candidates []appitempaging.TranscriptItemCandidate
	// HasOlder reports whether items remain before the first candidate.
	HasOlder bool
	// Incarnation and Length are the window's snapshot identity: the index
	// incarnation and the transcript bytes it covered. Within one incarnation
	// the length only grows; a rebuild mints a new incarnation.
	Incarnation string
	Length      int64
}

// Latest returns the newest limit items (see appwire.NormalizeTranscriptItemLimit).
func (x *Index) Latest(limit int) (Window, error) {
	limit, err := appwire.NormalizeTranscriptItemLimit(limit)
	if err != nil {
		return Window{}, err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	var window Window
	err = x.locked(false, func() (err error) {
		window, err = x.window(x.preludeCount()+x.items.n, limit)
		return err
	})
	return window, err
}

// Before returns up to limit items immediately before the exclusive position.
// A position that names no item is appwire.TranscriptItemCursorStale().
func (x *Index) Before(before appwire.ThreadItemPosition, limit int) (Window, error) {
	limit, err := appwire.NormalizeTranscriptItemLimit(limit)
	if err != nil {
		return Window{}, err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	var window Window
	err = x.locked(false, func() error {
		rank, err := x.rank(before)
		if err != nil {
			return err
		}
		window, err = x.window(rank, limit)
		return err
	})
	return window, err
}

func (x *Index) preludeCount() uint64 {
	if x.prelude == nil {
		return 0
	}
	return uint64(len(x.prelude.Items))
}

// rank is the position's index among all items: prelude items first, then
// item records, which are sorted by position.
func (x *Index) rank(position appwire.ThreadItemPosition) (uint64, error) {
	prelude := x.preludeCount()
	if position.Entry == 0 {
		if uint64(position.Item) < prelude {
			return uint64(position.Item), nil
		}
		return 0, appwire.TranscriptItemCursorStale()
	}
	slot, err := x.items.search(func(buf []byte) bool {
		record := decodeItem(buf)
		return record.Entry < position.Entry || (record.Entry == position.Entry && record.Part < position.Item)
	})
	if err != nil {
		return 0, x.fail(err)
	}
	if slot < x.items.n {
		buf, err := x.items.read(slot, 1)
		if err != nil {
			return 0, x.fail(err)
		}
		if record := decodeItem(buf); record.Entry == position.Entry && record.Part == position.Item {
			return prelude + slot, nil
		}
	}
	return 0, appwire.TranscriptItemCursorStale()
}

// window reads the items ranked [end-limit, end).
func (x *Index) window(end uint64, limit int) (Window, error) {
	start := end - min(end, uint64(limit))
	window := Window{Candidates: make([]appitempaging.TranscriptItemCandidate, 0, end-start), HasOlder: start > 0, Incarnation: x.meta.Incarnation, Length: x.meta.Length}
	prelude := x.preludeCount()
	for rank := start; rank < min(end, prelude); rank++ {
		window.Candidates = append(window.Candidates, x.preludeCandidate(int(rank)))
	}
	if end <= prelude {
		return window, nil
	}
	candidates, err := x.span(newReader(x), max(start, prelude)-prelude, end-prelude)
	if err != nil {
		return Window{}, err
	}
	window.Candidates = append(window.Candidates, candidates...)
	return window, nil
}

// span projects item records [lo, hi).
func (x *Index) span(r *reader, lo, hi uint64) ([]appitempaging.TranscriptItemCandidate, error) {
	// One read covers the records plus a neighbour on each side, which say
	// whether the edge items' turns continue past the span.
	first, last := lo-min(lo, 1), min(hi+1, x.items.n)
	buf, err := x.items.read(first, int(last-first))
	if err != nil {
		return nil, x.fail(err)
	}
	records := make([]itemRecord, last-first)
	for i := range records {
		records[i] = decodeItem(buf[i*itemRecordSize:])
	}
	candidates := make([]appitempaging.TranscriptItemCandidate, 0, hi-lo)
	for slot := lo; slot < hi; slot++ {
		record := records[slot-first]
		turn, err := r.turn(record.Turn)
		if err != nil {
			return nil, x.fail(err)
		}
		item, err := r.item(record, turn.ID)
		if err != nil {
			return nil, x.fail(err)
		}
		candidates = append(candidates, appitempaging.TranscriptItemCandidate{
			TurnID:          turn.ID,
			Turn:            turn,
			Item:            item,
			Position:        *item.Position,
			HasEarlierItems: slot > 0 && records[slot-1-first].Turn == record.Turn,
			HasLaterItems:   slot+1 < x.items.n && records[slot+1-first].Turn == record.Turn,
		})
	}
	return candidates, nil
}

// Changes is what in-place updates changed after a snapshot: the current form
// of each item and turn they touched, in record order, each once.
type Changes struct {
	Items       []appitempaging.TranscriptItemCandidate
	Turns       []appwire.Turn
	Incarnation string
	Length      int64
}

// ChangedSince returns the items and turns that entries at or past length
// updated in place: a tool call a later TOOL_RESULTS completed, a turn a later
// entry restamped. A reader holding a snapshot at length learns of changes to
// what it holds without re-reading it.
func (x *Index) ChangedSince(length int64) (Changes, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	var changes Changes
	err := x.locked(false, func() error {
		changes = Changes{Incarnation: x.meta.Incarnation, Length: x.meta.Length}
		first, err := x.firstUpdateAt(length)
		if err != nil {
			return err
		}
		items, turns := map[uint64]bool{}, map[uint64]bool{}
		buf, err := x.updates.read(first, int(x.updates.n-first))
		if err != nil {
			return x.fail(err)
		}
		for at := 0; at < len(buf); at += updateRecordSize {
			switch update := decodeUpdate(buf[at:]); update.Kind {
			case updatedItem:
				items[update.Slot] = true
			case updatedTurn:
				turns[update.Slot] = true
			}
		}
		r := newReader(x)
		for _, slot := range slices.Sorted(maps.Keys(items)) {
			candidates, err := x.span(r, slot, slot+1)
			if err != nil {
				return err
			}
			changes.Items = append(changes.Items, candidates...)
		}
		for _, slot := range slices.Sorted(maps.Keys(turns)) {
			turn, err := r.turn(uint32(slot))
			if err != nil {
				return x.fail(err)
			}
			changes.Turns = append(changes.Turns, turn)
		}
		return nil
	})
	return changes, err
}

// firstUpdateAt binary-searches the update log for the first update an entry
// at or past offset caused.
func (x *Index) firstUpdateAt(offset int64) (uint64, error) {
	slot, err := x.updates.search(func(buf []byte) bool { return decodeUpdate(buf).Offset < offset })
	if err != nil {
		return 0, x.fail(err)
	}
	return slot, nil
}

// fail marks the index for a rebuild when a read found it inconsistent.
func (x *Index) fail(err error) error {
	if errors.Is(err, errCorrupt) {
		x.stale = true
	}
	return err
}

func (x *Index) preludeCandidate(i int) appitempaging.TranscriptItemCandidate {
	turn := *x.prelude
	turn.Items = nil
	item := x.prelude.Items[i]
	position := appwire.ThreadItemPosition{Entry: 0, Item: uint32(i)}
	item.Position = &position
	item.TranscriptKey = ItemKey(turn.ID, position)
	return appitempaging.TranscriptItemCandidate{
		TurnID:          turn.ID,
		Turn:            turn,
		Item:            item,
		Position:        position,
		HasEarlierItems: i > 0,
		HasLaterItems:   i+1 < len(x.prelude.Items),
	}
}

func newReader(x *Index) *reader {
	return &reader{x: x, entries: map[int64]*schema.Turn{}, projections: map[projectionKey]projection{}, turns: map[uint32]appwire.Turn{}}
}

// reader projects one read's items, decoding each entry, projecting each
// contributor and stamping each turn once.
type reader struct {
	x           *Index
	entries     map[int64]*schema.Turn
	projections map[projectionKey]projection
	turns       map[uint32]appwire.Turn
}

// projectionKey is one projection of an entry: its seed is the tool name of
// callID, when the contributor carries one.
type projectionKey struct {
	offset int64
	callID string
	named  bool
}

type projection struct {
	items []appwire.ThreadItem
	parts []int
}

func (r *reader) entry(offset int64, length uint32) (*schema.Turn, error) {
	if entry, ok := r.entries[offset]; ok {
		return entry, nil
	}
	line := make([]byte, length)
	if _, err := r.x.transcript.ReadAt(line, offset); err != nil {
		return nil, fmt.Errorf("%w: read transcript entry: %w", errCorrupt, err)
	}
	// The index strictly decoded this line when it indexed it, and
	// validation proves the bytes unchanged since.
	entry, err := transcript.DecodeValidatedEntry(bytes.TrimSpace(line))
	if err != nil {
		return nil, fmt.Errorf("%w: decode transcript entry at %d: %w", errCorrupt, offset, err)
	}
	r.entries[offset] = &entry.Turn
	return &entry.Turn, nil
}

// turn stamps a turn from its summary, as apptranscript.StampGroupedTurn
// stamps it from its entries.
func (r *reader) turn(slot uint32) (appwire.Turn, error) {
	if turn, ok := r.turns[slot]; ok {
		return turn, nil
	}
	buf, err := r.x.turns.read(uint64(slot), 1)
	if err != nil {
		return appwire.Turn{}, err
	}
	record := decodeTurn(buf)
	id, err := r.x.strings.get(record.ID)
	if err != nil {
		return appwire.Turn{}, err
	}
	turn := appwire.Turn{ID: string(id), ItemsView: appwire.TurnItemsViewFull, Status: appwire.TurnStatusCompleted}
	switch record.Status {
	case statusFailed:
		failure, err := r.entry(record.LifecycleOffset, record.LifecycleLength)
		if err != nil {
			return appwire.Turn{}, err
		}
		apptranscript.StampTurnFailure(&turn, *failure)
	case statusInterrupted:
		turn.Status = appwire.TurnStatusInterrupted
	}
	if record.Started {
		startedAt := record.StartedAt
		turn.StartedAt = &startedAt
	}
	cacheRead := int(record.Usage[2])
	turn.Usage = appwire.EvenerUsageFromLLM(llm.Usage{
		InputTokens:     int(record.Usage[0]),
		OutputTokens:    int(record.Usage[1]),
		CacheReadTokens: &cacheRead,
		TotalTokens:     int(record.Usage[3]),
	})
	r.turns[slot] = turn
	return turn, nil
}

// item projects one item from its contributor entries: the opener's item at
// the record's part, folded with every later item of the same call.
func (r *reader) item(record itemRecord, turnID string) (appwire.ThreadItem, error) {
	callID := ""
	if record.Call.Len > 0 {
		call, err := r.x.strings.get(record.Call)
		if err != nil {
			return appwire.ThreadItem{}, err
		}
		callID = string(call)
	}
	var items []appwire.ThreadItem
	var parts []int
	var err error
	if record.Context.Len > 0 {
		items, parts, err = r.projectWithContext(record, turnID)
	} else {
		items, parts, err = r.project(record.Opener, callID, turnID)
	}
	if err != nil {
		return appwire.ThreadItem{}, err
	}
	at := -1
	for i, part := range parts {
		if part == int(record.Part) {
			at = i
		}
	}
	if at < 0 {
		return appwire.ThreadItem{}, fmt.Errorf("%w: entry %d projects no item at part %d", errCorrupt, record.Entry-1, record.Part)
	}
	item := items[at]
	if callID != "" {
		item = foldCall(item, items[at+1:], callID)
		later, err := r.x.strings.get(record.Middle)
		if err != nil {
			return appwire.ThreadItem{}, err
		}
		contributors, err := decodeContributors(later)
		if err != nil {
			return appwire.ThreadItem{}, err
		}
		if record.Completer.Length > 0 {
			contributors = append(contributors, record.Completer)
		}
		for _, c := range contributors {
			items, _, err := r.project(c, callID, turnID)
			if err != nil {
				return appwire.ThreadItem{}, err
			}
			item = foldCall(item, items, callID)
		}
	}
	item.TurnID = turnID
	position := appwire.ThreadItemPosition{Entry: record.Entry, Item: record.Part}
	item.Position = &position
	item.TranscriptKey = ItemKey(turnID, position)
	return item, nil
}

// project projects a contributor entry with the tool name the whole-file
// projection held for the call when it projected the entry.
func (r *reader) project(c contributor, callID, turnID string) ([]appwire.ThreadItem, []int, error) {
	key := projectionKey{offset: c.Offset, callID: callID, named: c.Name.Len > 0}
	if cached, ok := r.projections[key]; ok {
		return cached.items, cached.parts, nil
	}
	entry, err := r.entry(c.Offset, c.Length)
	if err != nil {
		return nil, nil, err
	}
	seed := map[string]string{}
	if key.named {
		name, err := r.x.strings.get(c.Name)
		if err != nil {
			return nil, nil, err
		}
		seed[callID] = string(name)
	}
	items, parts := apptranscript.ProjectTurnParts(turnID, int(c.Ordinal)+1, *entry, &apptranscript.ToolCallRegistry{Names: seed, CommRawArgs: map[string]string{}}, nil, apptranscript.ToolResultOutputImages)
	r.projections[key] = projection{items: items, parts: parts}
	return items, parts, nil
}

// projectWithContext projects record.Opener for a deferred communicate result
// item: unlike project, Opener alone never carries enough ToolCallRegistry
// state (the assistant entry that issued the call renders no item of its
// own, so Names/CommRawArgs/LastAssistantText/LastAssistantTurnID are not
// recoverable from Opener). It replays record.Context's entries first — in
// file order, exactly as the whole-file projection's single threaded
// registry would have processed them — discarding their own items, then
// projects Opener with the registry those replays built. Not cached: these
// items are a small, rare subset of a read.
func (r *reader) projectWithContext(record itemRecord, turnID string) ([]appwire.ThreadItem, []int, error) {
	buf, err := r.x.strings.get(record.Context)
	if err != nil {
		return nil, nil, err
	}
	context, err := decodeContextEntries(buf)
	if err != nil {
		return nil, nil, err
	}
	reg := apptranscript.NewToolCallRegistry()
	for _, ce := range context {
		entry, err := r.entry(ce.Offset, ce.Length)
		if err != nil {
			return nil, nil, err
		}
		turnIDBytes, err := r.x.strings.get(ce.TurnID)
		if err != nil {
			return nil, nil, err
		}
		apptranscript.ProjectTurnParts(string(turnIDBytes), int(ce.Ordinal)+1, *entry, reg, nil, apptranscript.ToolResultOutputImages)
	}
	entry, err := r.entry(record.Opener.Offset, record.Opener.Length)
	if err != nil {
		return nil, nil, err
	}
	items, parts := apptranscript.ProjectTurnParts(turnID, int(record.Opener.Ordinal)+1, *entry, reg, nil, apptranscript.ToolResultOutputImages)
	return items, parts, nil
}

func foldCall(item appwire.ThreadItem, later []appwire.ThreadItem, callID string) appwire.ThreadItem {
	for _, next := range later {
		if apptranscript.MergesByCallID(next) && next.CallID == callID {
			item = apptranscript.MergeThreadItems(item, next)
		}
	}
	return item
}
