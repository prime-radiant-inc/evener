package transcriptindex

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"

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
	return x.readWindow(func(r *reader) (Window, error) {
		flush, err := x.pendingFlush(r)
		if err != nil {
			return Window{}, err
		}
		return x.window(r, x.preludeCount()+x.items.n+uint64(len(flush)), limit, flush)
	})
}

// LatestSince is Latest together with what changed since held, read under one
// lock so both describe the same snapshot: a reader that took them apart could
// miss a change another handle extended over between the two. The changes
// are nil when held names another incarnation or predates the kept update
// log; the caller then sends the window as a full replacement.
func (x *Index) LatestSince(limit int, held appwire.SnapshotIdentity) (Window, *Changes, error) {
	limit, err := appwire.NormalizeTranscriptItemLimit(limit)
	if err != nil {
		return Window{}, nil, err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	var window Window
	var changes *Changes
	window, err = x.readWindow(func(r *reader) (Window, error) {
		flush, ferr := x.pendingFlush(r)
		if ferr != nil {
			return Window{}, ferr
		}
		return x.window(r, x.preludeCount()+x.items.n+uint64(len(flush)), limit, flush)
	})
	if err != nil || held.Incarnation != x.meta.Incarnation {
		return window, nil, err
	}
	err = x.locked(false, func() error {
		since, sinceErr := x.changedSince(held.Length)
		if errors.Is(sinceErr, ErrUpdateLogTruncated) {
			return nil
		}
		changes = &since
		return sinceErr
	})
	return window, changes, err
}

// Incarnation is the index's current incarnation, as the sidecar holds it:
// a backfill cursor naming any other is stale.
func (x *Index) Incarnation() (string, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	var incarnation string
	err := x.locked(false, func() error {
		incarnation = x.meta.Incarnation
		return nil
	})
	return incarnation, err
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
	return x.readWindow(func(r *reader) (Window, error) {
		flush, err := x.pendingFlush(r)
		if err != nil {
			return Window{}, err
		}
		rank, err := x.rank(before, flush)
		if err != nil {
			return Window{}, err
		}
		return x.window(r, rank, limit, flush)
	})
}

// readWindow runs compute under a shared lock, the common case for a read.
// compute must not run when the builder is stale (adopt sets builderStale
// without repairing x.builder — see repairBuilder), so a stale builder skips
// straight to repair under the exclusive lock that can need, then retries.
// The non-stale case — every read once a staleness is repaired — takes the
// shared lock exactly once.
func (x *Index) readWindow(compute func(r *reader) (Window, error)) (Window, error) {
	for {
		var window Window
		stale := false
		err := x.locked(false, func() error {
			if x.builderStale {
				stale = true
				return nil
			}
			w, err := compute(newReader(x))
			window = w
			return err
		})
		if err != nil {
			return Window{}, err
		}
		if !stale {
			return window, nil
		}
		if err := x.locked(true, func() error { return x.repairBuilder(x.meta.Length) }); err != nil {
			return Window{}, err
		}
	}
}

func (x *Index) preludeCount() uint64 {
	if x.prelude == nil {
		return 0
	}
	return uint64(len(x.prelude.Items))
}

// rank is the position's index among all items: prelude items first, then
// item records, then flush's pending-communicate items, if any, at the very
// end (see window).
func (x *Index) rank(position appwire.ThreadItemPosition, flush []appitempaging.TranscriptItemCandidate) (uint64, error) {
	prelude := x.preludeCount()
	// Checked before the Entry==0 prelude case below: a flushed item can
	// itself sit at Entry 0 when the transcript's tail flushes into the
	// prelude turn (no item record exists yet; see pendingFlush).
	for i, f := range flush {
		if f.Position == position {
			return prelude + x.items.n + uint64(i), nil
		}
	}
	if position.Entry == 0 {
		if uint64(position.Item) < prelude {
			return uint64(position.Item), nil
		}
		return 0, appwire.TranscriptItemCursorStale()
	}
	slot, found, err := x.findItem(position)
	if err != nil {
		return 0, x.fail(err)
	}
	if !found {
		return 0, appwire.TranscriptItemCursorStale()
	}
	return prelude + slot, nil
}

// findItem binary-searches the item records for the item at position.
func (x *Index) findItem(position appwire.ThreadItemPosition) (uint64, bool, error) {
	slot, err := x.items.search(func(buf []byte) bool {
		record := decodeItem(buf)
		return record.Entry < position.Entry || (record.Entry == position.Entry && record.Part < position.Item)
	})
	if err != nil || slot >= x.items.n {
		return 0, false, err
	}
	buf, err := x.items.read(slot, 1)
	if err != nil {
		return 0, false, err
	}
	record := decodeItem(buf)
	return slot, record.Entry == position.Entry && record.Part == position.Item, nil
}

// window reads the items ranked [end-limit, end): prelude items, then item
// records, then flush's pending-communicate items, if any, at the very end
// (see rank). end may reach past prelude+items.n by up to len(flush).
func (x *Index) window(r *reader, end uint64, limit int, flush []appitempaging.TranscriptItemCandidate) (Window, error) {
	start := end - min(end, uint64(limit))
	window := Window{Candidates: make([]appitempaging.TranscriptItemCandidate, 0, end-start), HasOlder: start > 0, Incarnation: x.meta.Incarnation, Length: x.meta.Length}
	prelude := x.preludeCount()
	for rank := start; rank < min(end, prelude); rank++ {
		window.Candidates = append(window.Candidates, x.preludeCandidate(int(rank)))
	}
	if x.items.n == 0 && len(flush) > 0 && end >= prelude && len(window.Candidates) > 0 {
		// No item record exists at all: the flushed items continue directly
		// from the prelude (see pendingFlush), the same rule the real-items
		// branch below applies for its own last candidate.
		window.Candidates[len(window.Candidates)-1].HasLaterItems = true
	}
	indexed := prelude + x.items.n
	realEnd := min(end, indexed)
	if start < realEnd {
		candidates, err := x.span(r, max(start, prelude)-prelude, realEnd-prelude)
		if err != nil {
			return Window{}, err
		}
		if realEnd == indexed && len(flush) > 0 && len(candidates) > 0 {
			// The last real item's turn continues into the flushed
			// items, whether or not this window's limit reaches them.
			candidates[len(candidates)-1].HasLaterItems = true
		}
		window.Candidates = append(window.Candidates, candidates...)
	}
	if end > indexed {
		window.Candidates = append(window.Candidates, flush[max(start, indexed)-indexed:end-indexed]...)
	}
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
		stamped, err := r.turn(record.Turn)
		if err != nil {
			return nil, x.fail(err)
		}
		turn := stamped.turn
		item, err := r.item(record, turn.ID)
		if err != nil {
			return nil, x.fail(err)
		}
		item.Version = record.Version
		candidates = append(candidates, appitempaging.TranscriptItemCandidate{
			TurnID:          turn.ID,
			Turn:            turn,
			Item:            item,
			Position:        *item.Position,
			HasEarlierItems: slot > 0 && records[slot-1-first].Turn == record.Turn,
			HasLaterItems:   slot+1 < x.items.n && records[slot+1-first].Turn == record.Turn,
			Model:           stamped.model,
		})
	}
	return candidates, nil
}

// Changes is what changed after a snapshot: the current form of every item
// and turn a later entry created or updated in place, in record order, each
// once.
type Changes struct {
	Items       []appitempaging.TranscriptItemCandidate
	Turns       []appwire.Turn
	Incarnation string
	Length      int64
}

// ChangedSince returns the items and turns whose record an entry at or past
// length created or touched: an item a later entry opened or a call a later
// TOOL_RESULTS completed, a turn a later entry opened or restamped. A reader
// holding a snapshot at length learns everything past it changed, without
// re-reading what it already holds. Items are in position order, turns in
// slot order (the order their first entry created them), each once.
func (x *Index) ChangedSince(length int64) (Changes, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	var changes Changes
	err := x.locked(false, func() (err error) {
		changes, err = x.changedSince(length)
		return err
	})
	return changes, err
}

func (x *Index) changedSince(length int64) (Changes, error) {
	if length < x.meta.UpdatesFrom {
		return Changes{}, ErrUpdateLogTruncated
	}
	first, err := x.firstUpdateAt(length)
	if err != nil {
		return Changes{}, err
	}
	items, turns := map[uint64]bool{}, map[uint64]bool{}
	buf, err := x.updates.read(first, int(x.updates.n-first))
	if err != nil {
		return Changes{}, x.fail(err)
	}
	for at := 0; at < len(buf); at += updateRecordSize {
		switch update := decodeUpdate(buf[at:]); update.Kind {
		case updatedItem:
			items[update.Slot] = true
		case updatedTurn:
			turns[update.Slot] = true
		}
	}
	firstItem, err := x.firstItemCreatedAt(length)
	if err != nil {
		return Changes{}, err
	}
	for slot := firstItem; slot < x.items.n; slot++ {
		items[slot] = true
	}
	firstTurn, err := x.firstTurnCreatedAt(length)
	if err != nil {
		return Changes{}, err
	}
	for slot := firstTurn; slot < x.turns.n; slot++ {
		turns[slot] = true
	}
	changes := Changes{Incarnation: x.meta.Incarnation, Length: x.meta.Length}
	r := newReader(x)
	for _, slot := range slices.Sorted(maps.Keys(items)) {
		candidates, err := x.span(r, slot, slot+1)
		if err != nil {
			return Changes{}, err
		}
		changes.Items = append(changes.Items, candidates...)
	}
	for _, slot := range slices.Sorted(maps.Keys(turns)) {
		stamped, err := r.turn(uint32(slot))
		if err != nil {
			return Changes{}, x.fail(err)
		}
		changes.Turns = append(changes.Turns, stamped.turn)
	}
	return changes, nil
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

// firstItemCreatedAt binary-searches the item records, sorted by position and
// so by their opener's offset, for the first one an entry at or past offset
// opened.
func (x *Index) firstItemCreatedAt(offset int64) (uint64, error) {
	slot, err := x.items.search(func(buf []byte) bool { return decodeItem(buf).Opener.Offset < offset })
	if err != nil {
		return 0, x.fail(err)
	}
	return slot, nil
}

// firstTurnCreatedAt binary-searches the turn summaries, appended in the file
// order their first entry created them, for the first one an entry at or past
// offset opened.
func (x *Index) firstTurnCreatedAt(offset int64) (uint64, error) {
	slot, err := x.turns.search(func(buf []byte) bool { return decodeTurn(buf).FirstOffset < offset })
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

// lastCandidate is the very last item overall (prelude or indexed), or nil
// when the index holds none yet.
func (x *Index) lastCandidate(r *reader) (*appitempaging.TranscriptItemCandidate, error) {
	total := x.preludeCount() + x.items.n
	if total == 0 {
		return nil, nil
	}
	if x.items.n == 0 {
		c := x.preludeCandidate(int(total - 1))
		return &c, nil
	}
	candidates, err := x.span(r, x.items.n-1, x.items.n)
	if err != nil {
		return nil, x.fail(err)
	}
	return &candidates[0], nil
}

// lastTurnItemCount is how many items the last item's turn holds overall
// (not just within its opening entry): apptranscript.FlushUnpairedCommunicates
// positions a flushed item at this count, matching the full-file projection's
// appwire.Turn.Items length, which — unlike every other item's entry-relative
// Position.Item — counts every item the turn has accumulated. Items are
// appended in file order, so the last turn's are exactly the table's tail;
// counting backward from x.items.n-1 while the turn slot matches is bounded
// by that one turn's own item count.
func (x *Index) lastTurnItemCount(turnSlot uint32) (uint64, error) {
	var count uint64
	for slot := x.items.n; slot > 0; slot-- {
		buf, err := x.items.read(slot-1, 1)
		if err != nil {
			return 0, x.fail(err)
		}
		if decodeItem(buf).Turn != turnSlot {
			break
		}
		count++
	}
	return count, nil
}

// remapFlushPositions reassigns flushed's positions from
// apptranscript.FlushUnpairedCommunicates' own dense count (last.Items'
// append order) to a namespace reserved for this package's v2 positions.
// That count is not a safe Item value here: content parts can be sparse (a
// hidden echoed communicate call, an empty text part), so the same count can
// already be a persisted item's real part index within the entry — e.g. a
// single assistant entry [communicate, read_file] flushing its deferred
// communicate alongside its own part-1 read_file item. Real part indices
// never approach the top of uint32, so a flushed item positioned there can
// never collide with a persisted {Entry, Part} position (roborev finding on
// PR #2303, round 4). Scoped to this package: FlushUnpairedCommunicates'
// own numbering is production's v1 contract (server/appwire_turns.go and
// the bounded item-window/turn-window readers agree on it independently),
// left untouched.
func remapFlushPositions(turnID string, flushed []appwire.ThreadItem) {
	base := uint32(math.MaxUint32) - uint32(len(flushed)) + 1
	for i := range flushed {
		position := appwire.ThreadItemPosition{Entry: flushed[i].Position.Entry, Item: base + uint32(i)}
		flushed[i].Position = &position
		flushed[i].TranscriptKey = ItemKey(turnID, position)
	}
}

// pendingFlush computes the trailing items a communicate call the transcript
// ends on (no result yet) contributes: the same rendering
// apptranscript.FlushUnpairedCommunicates gives the whole-file projection,
// reproduced here so Latest/Before can include it — matching every production
// reader (server/appwire_turns.go, cmd/evener-hub/app_threadread.go), which
// all flush a read that reaches the transcript's tail. Returns nil when
// nothing is pending. Not persisted: recomputed from the builder's live
// commCalls/lastAssistant* state, which a pending call forces extend to keep
// accurate (see meta.PendingCommunicate).
func (x *Index) pendingFlush(r *reader) ([]appitempaging.TranscriptItemCandidate, error) {
	if len(x.builder.commCalls) == 0 {
		return nil, nil
	}
	var turnID, model string
	var stub appwire.Turn
	var items []appwire.ThreadItem
	if x.items.n == 0 {
		// No item record exists yet: every entry so far produced no visible
		// item — for instance the transcript opens on a bare deferred
		// communicate call. The whole-file projection still flushes into
		// the prelude turn when one exists (a system prompt); with neither
		// a prelude nor any item, there is nothing for
		// FlushUnpairedCommunicates to attach to, matching its own
		// len(*turns)==0 no-op.
		if x.prelude == nil {
			return nil, nil
		}
		turnID = x.prelude.ID
		stub = *x.prelude
		stub.Items = nil
		items = append([]appwire.ThreadItem(nil), x.prelude.Items...)
	} else {
		last, err := x.lastCandidate(r)
		if err != nil || last == nil {
			return nil, err
		}
		buf, err := x.items.read(x.items.n-1, 1)
		if err != nil {
			return nil, x.fail(err)
		}
		itemCount, err := x.lastTurnItemCount(decodeItem(buf).Turn)
		if err != nil {
			return nil, err
		}
		turnID = last.TurnID
		stub = last.Turn
		model = last.Model
		items = make([]appwire.ThreadItem, itemCount)
		items[itemCount-1] = last.Item
	}
	reg, err := x.pendingRegistry(r)
	if err != nil {
		return nil, err
	}
	before := len(items)
	turns := []appwire.Turn{{ID: turnID, Items: items}}
	if !apptranscript.FlushUnpairedCommunicates(&turns, reg) {
		return nil, nil
	}
	flushed := turns[0].Items[before:]
	remapFlushPositions(turnID, flushed)
	candidates := make([]appitempaging.TranscriptItemCandidate, len(flushed))
	for i := range flushed {
		candidates[i] = appitempaging.TranscriptItemCandidate{
			TurnID:          turnID,
			Turn:            stub,
			Item:            flushed[i],
			Position:        *flushed[i].Position,
			HasEarlierItems: true,
			HasLaterItems:   i+1 < len(flushed),
			Model:           model,
		}
	}
	return candidates, nil
}

// pendingRegistry rebuilds the ToolCallRegistry state pendingFlush needs by
// replaying, in file order, every still-open communicate call's assistant
// entry (for CommRawArgs) and the entry that most recently set
// LastAssistantText (for the echo check) — the same replay projectWithContext
// does for one item's Context, generalized to the builder's whole live
// pending state.
func (x *Index) pendingRegistry(r *reader) (*apptranscript.ToolCallRegistry, error) {
	type pending struct {
		pos    contributor
		turnID string
	}
	seen := map[int64]bool{}
	var replay []pending
	add := func(pos contributor, turnID string) {
		if pos.Length == 0 || seen[pos.Offset] {
			return
		}
		seen[pos.Offset] = true
		replay = append(replay, pending{pos, turnID})
	}
	for _, state := range x.builder.commCalls {
		add(state.pos, state.turnID)
	}
	if x.builder.lastAssistantKnown {
		add(x.builder.lastAssistantPos, x.builder.lastAssistantTurnID)
	}
	sort.Slice(replay, func(i, j int) bool { return replay[i].pos.Ordinal < replay[j].pos.Ordinal })
	reg := apptranscript.NewToolCallRegistry()
	for _, p := range replay {
		entry, err := r.entry(p.pos.Offset, p.pos.Length)
		if err != nil {
			return nil, err
		}
		apptranscript.ProjectTurnParts(p.turnID, int(p.pos.Ordinal)+1, *entry, reg, nil, apptranscript.ToolResultOutputImages)
	}
	// The lastAssistant entry replayed above for its text may itself carry a
	// communicate call that a later (unreplayed) result entry already
	// consumed — replaying it in isolation reintroduces that resolved call's
	// CommRawArgs, since ProjectTurnParts cannot know a later entry deleted
	// it. Prune to exactly the calls commCalls still tracks as pending.
	for id := range reg.CommRawArgs {
		if _, ok := x.builder.commCalls[id]; !ok {
			delete(reg.CommRawArgs, id)
		}
	}
	return reg, nil
}

func newReader(x *Index) *reader {
	return &reader{x: x, entries: map[int64]*schema.Turn{}, projections: map[projectionKey]projection{}, turns: map[uint32]stampedTurn{}}
}

// reader projects one read's items, decoding each entry, projecting each
// contributor and stamping each turn once.
type reader struct {
	x           *Index
	entries     map[int64]*schema.Turn
	projections map[projectionKey]projection
	turns       map[uint32]stampedTurn
}

// stampedTurn is a turn stamped from its summary, and the turn's model.
type stampedTurn struct {
	turn  appwire.Turn
	model string
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
	entry, err := r.x.readEntry(offset, length)
	if err != nil {
		return nil, err
	}
	r.entries[offset] = entry
	return entry, nil
}

// readEntry reads and decodes the entry line of length bytes at offset.
func (x *Index) readEntry(offset int64, length uint32) (*schema.Turn, error) {
	line := make([]byte, length)
	if _, err := x.transcript.ReadAt(line, offset); err != nil {
		return nil, fmt.Errorf("%w: read transcript entry: %w", errCorrupt, err)
	}
	// The index strictly decoded this line when it indexed it, and
	// validation proves the bytes unchanged since.
	entry, err := transcript.DecodeValidatedEntry(bytes.TrimSpace(line))
	if err != nil {
		return nil, fmt.Errorf("%w: decode transcript entry at %d: %w", errCorrupt, offset, err)
	}
	return &entry.Turn, nil
}

// turn stamps a turn from its summary: a legacy turn as
// apptranscript.StampGroupedTurn stamps it from its entries, a new-format
// turn by the read model's status rules.
func (r *reader) turn(slot uint32) (stampedTurn, error) {
	if stamped, ok := r.turns[slot]; ok {
		return stamped, nil
	}
	buf, err := r.x.turns.read(uint64(slot), 1)
	if err != nil {
		return stampedTurn{}, err
	}
	record := decodeTurn(buf)
	id, err := r.x.strings.get(record.ID)
	if err != nil {
		return stampedTurn{}, err
	}
	model, err := r.x.strings.get(record.Model)
	if err != nil {
		return stampedTurn{}, err
	}
	turn := appwire.Turn{ID: string(id), ItemsView: appwire.TurnItemsViewFull, Status: appwire.TurnStatusCompleted, Version: record.Version}
	switch record.Status {
	case statusFailed:
		turn.Status = appwire.TurnStatusFailed
		if record.FailureLength > 0 {
			failure, err := r.entry(record.FailureOffset, record.FailureLength)
			if err != nil {
				return stampedTurn{}, err
			}
			apptranscript.StampTurnFailure(&turn, *failure)
		}
	case statusInterrupted:
		turn.Status = appwire.TurnStatusInterrupted
	case statusOpen:
		turn.Status = appwire.TurnStatusInProgress
	}
	if record.HasCompletion {
		duration := record.DurationMS
		turn.DurationMS = &duration
		if record.CompletedAt != 0 {
			completedAt := record.CompletedAt
			turn.CompletedAt = &completedAt
		}
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
	stamped := stampedTurn{turn: turn, model: string(model)}
	r.turns[slot] = stamped
	return stamped, nil
}

// item projects one item from its contributor entries: the opener's item at
// the record's part, folded with every later item of the same call.
func (r *reader) item(record itemRecord, turnID string) (appwire.ThreadItem, error) {
	if record.Flags&itemUnreadable != 0 {
		return r.unreadable(record, turnID)
	}
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
		opener, err := r.entry(record.Opener.Offset, record.Opener.Length)
		if err != nil {
			return appwire.ThreadItem{}, err
		}
		// A legacy entry's later items of the same call merge into its
		// first; each new-format item is its own part's.
		identity := opener.Format == schema.TurnFormatIdentity
		if !identity {
			item = foldCall(item, items[at+1:], callID)
		}
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
		var completedAtEntry uint64
		for _, c := range contributors {
			entry, err := r.entry(c.Offset, c.Length)
			if err != nil {
				return appwire.ThreadItem{}, err
			}
			switch entry.Kind {
			case schema.TurnCompletion:
				// The execution completed with no results for the call:
				// the call was interrupted (builder.interruptAwaitedCalls).
				item.Status = appwire.TurnStatusInterrupted
				continue
			case schema.TurnTool, schema.TurnToolResults:
				completedAtEntry = c.Ordinal + 1
			}
			items, _, err := r.project(c, callID, turnID)
			if err != nil {
				return appwire.ThreadItem{}, err
			}
			item = foldCall(item, items, callID)
		}
		item.CompletedAtEntry = completedAtEntry
		// A new-format item that spans entries keeps its opener's identity.
		if identity {
			item.ID, item.RoundID = items[at].ID, items[at].RoundID
		}
	}
	item.TurnID = turnID
	position := appwire.ThreadItemPosition{Entry: record.Entry, Item: record.Part}
	item.Position = &position
	item.TranscriptKey = ItemKey(turnID, position)
	return item, nil
}

// unreadable projects a quarantined entry: one error notice naming its
// ordinal and why it does not decode, which decoding the line again tells.
func (r *reader) unreadable(record itemRecord, turnID string) (appwire.ThreadItem, error) {
	ordinal := record.Opener.Ordinal
	line := make([]byte, record.Opener.Length)
	if _, err := r.x.transcript.ReadAt(line, record.Opener.Offset); err != nil {
		return appwire.ThreadItem{}, fmt.Errorf("%w: read transcript entry: %w", errCorrupt, err)
	}
	reason := "it does not decode"
	if _, err := transcript.DecodeEntry(bytes.TrimSpace(line)); err != nil {
		reason = err.Error()
	}
	position := appwire.ThreadItemPosition{Entry: record.Entry, Item: record.Part}
	return appwire.ThreadItem{
		Type:          "systemMessage",
		ID:            fmt.Sprintf("item_unreadable_%d", ordinal),
		TranscriptKey: ItemKey(turnID, position),
		Position:      &position,
		TurnID:        turnID,
		Description:   "Unreadable transcript entry",
		Text:          fmt.Sprintf("transcript entry %d could not be read: %s", ordinal, reason),
		Status:        appwire.TurnStatusCompleted,
		EventKind:     appwire.ThreadItemEventKindError,
		Version:       record.Version,
	}, nil
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
	items, parts := apptranscript.ProjectEntryParts(turnID, int(c.Ordinal)+1, *entry, &apptranscript.ToolCallRegistry{Names: seed}, apptranscript.AddressedImageProjector, apptranscript.ToolResultOutputImages)
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
// items are a small, rare subset of a read. Only ever called for a legacy
// (non-identity-format) opener: applyLegacy is the only writer of Context.
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
