package transcriptindex

import (
	"bytes"
	"errors"
	"fmt"

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
	// Length is the transcript bytes the window was read from.
	Length int64
}

// Latest returns the newest limit items (see appwire.NormalizeTranscriptItemLimit).
func (x *Index) Latest(limit int) (Window, error) {
	limit, err := appwire.NormalizeTranscriptItemLimit(limit)
	if err != nil {
		return Window{}, err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.window(x.preludeCount()+x.items.n, limit)
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
	rank, err := x.rank(before)
	if err != nil {
		return Window{}, err
	}
	return x.window(rank, limit)
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
	lo, hi := uint64(0), x.items.n
	for lo < hi {
		mid := lo + (hi-lo)/2
		buf, err := x.items.read(mid, 1)
		if err != nil {
			return 0, x.fail(err)
		}
		record := decodeItem(buf)
		if record.Entry < position.Entry || (record.Entry == position.Entry && record.Part < position.Item) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < x.items.n {
		buf, err := x.items.read(lo, 1)
		if err != nil {
			return 0, x.fail(err)
		}
		if record := decodeItem(buf); record.Entry == position.Entry && record.Part == position.Item {
			return prelude + lo, nil
		}
	}
	return 0, appwire.TranscriptItemCursorStale()
}

// window reads the items ranked [end-limit, end).
func (x *Index) window(end uint64, limit int) (Window, error) {
	start := end - min(end, uint64(limit))
	window := Window{Candidates: make([]appitempaging.TranscriptItemCandidate, 0, end-start), HasOlder: start > 0, Length: x.meta.Length}
	prelude := x.preludeCount()
	for rank := start; rank < min(end, prelude); rank++ {
		window.Candidates = append(window.Candidates, x.preludeCandidate(int(rank)))
	}
	if end <= prelude {
		return window, nil
	}
	lo, hi := max(start, prelude)-prelude, end-prelude
	// One read covers the records plus a neighbour on each side, which say
	// whether the edge items' turns continue past the window.
	first, last := lo-min(lo, 1), min(hi+1, x.items.n)
	buf, err := x.items.read(first, int(last-first))
	if err != nil {
		return Window{}, x.fail(err)
	}
	records := make([]itemRecord, last-first)
	for i := range records {
		records[i] = decodeItem(buf[i*itemRecordSize:])
	}
	r := reader{x: x, entries: map[int64]*schema.Turn{}, projections: map[projectionKey]projection{}, turns: map[uint32]appwire.Turn{}}
	for slot := lo; slot < hi; slot++ {
		record := records[slot-first]
		turn, err := r.turn(record.Turn)
		if err != nil {
			return Window{}, x.fail(err)
		}
		item, err := r.item(record, turn.ID)
		if err != nil {
			return Window{}, x.fail(err)
		}
		window.Candidates = append(window.Candidates, appitempaging.TranscriptItemCandidate{
			TurnID:          turn.ID,
			Turn:            turn,
			Item:            item,
			Position:        *item.Position,
			HasEarlierItems: slot > 0 && records[slot-1-first].Turn == record.Turn,
			HasLaterItems:   slot+1 < x.items.n && records[slot+1-first].Turn == record.Turn,
		})
	}
	return window, nil
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

// reader projects one window's items, decoding each entry, projecting each
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
	if record.FailureLength > 0 {
		failure, err := r.entry(record.FailureOffset, record.FailureLength)
		if err != nil {
			return appwire.Turn{}, err
		}
		apptranscript.StampTurnFailure(&turn, *failure)
	}
	if record.Flags&turnInterrupted != 0 && turn.Status != appwire.TurnStatusFailed {
		turn.Status = appwire.TurnStatusInterrupted
	}
	if record.Flags&turnStarted != 0 {
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
	items, parts, err := r.project(record.Opener, callID, turnID)
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
	items, parts := apptranscript.ProjectTurnParts(turnID, int(c.Ordinal)+1, *entry, seed, nil, apptranscript.ToolResultOutputImages)
	r.projections[key] = projection{items: items, parts: parts}
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
