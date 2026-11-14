package transcriptindex

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"slices"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// errNotDerivable reports that the committed records cannot reproduce a cut
// state exactly. reconstructAtCut answers it by returning nil, and the caller
// replays the prefix as before: the derivation only ever changes a read's
// cost, never its result.
var errNotDerivable = errors.New("transcript index cut is not derivable from the committed records")

// reconstructAtCut derives the private records of a historical read at length
// from the handle's committed state, so a cold read pays for the unpublished
// suffix and for the spans of the turns the suffix stamped instead of
// replaying the whole covered prefix. It returns nil when the cut cannot be
// derived exactly this way; the caller then replays the prefix as before.
//
// The derivation is exact because it rewrites only state that is a pure
// function of the committed records:
//
//   - An item's cut state is its contributor list truncated at the cut's
//     first uncovered entry. Contributors are recorded in the order entries
//     contributed, and the Middle reference names their encodings in that
//     order, so the truncated state is the leading bytes of the recorded
//     list: the surgery moves the latest kept contributor into Completer and
//     shortens the Middle reference without decoding an entry.
//   - A turn the suffix stamped is folded again from its own entries, the
//     same fold stampTurn performed at build time. Every other turn, item
//     and update-log row at or before the cut is committed as it was then.
//   - The builder state a cut read serves windows from is the deferred
//     communicate state (meta.CommCalls) and the sticky last-assistant values
//     (meta.LastAssistant*), adjusted for the suffix's own entries.
//
// The fallback is deliberately broad. A sidecar whose update log was ever cut
// cannot answer a ChangedSince below the kept log, which a full replay
// reconstructs. A suffix entry that consumed a deferred communicate call, or
// an assistant text that overwrote the last-assistant value a still-pending
// call's echo check needs, leaves builder state the committed records cannot
// name. A nameless tool result in a folded span whose announcing call the
// span does not hold, a model the folded span names that the committed
// summary does not, a new-format entry that shares a legacy group's turn id,
// a call the tail re-announced that an item below the cut already names, a
// span-bearing entry that may have re-opened its own id, and a suffix echo
// that reached a cut item's call field would all fold records a full replay
// never wrote. Each case is rare in a publication cut —
// the unpublished tail of one producer while another published past it — and
// each costs the replay it always cost. A tail that outnumbers the covered
// entries means the transcript and the committed records disagree, and the
// replay decides too.
func (x *Index) reconstructAtCut(length int64) *Index {
	if length <= 0 || length >= x.meta.Length || x.meta.UpdatesFrom > 0 || x.stale {
		return nil
	}
	// A cut before the header's end holds no entries; a publication cut is
	// also always the end of a complete line. Both answers fall back to the
	// replay, which serves the last complete line at or below the length.
	if x.meta.HeaderLength == 0 || length < x.meta.HeaderOffset+x.meta.HeaderLength || !x.cutAtLineEnd(length) {
		return nil
	}
	suffix, err := x.scanSuffixEffects(length)
	if err != nil {
		return nil
	}
	// cutOrdinal is the first entry ordinal the cut does not cover; it is
	// also the count of entries the cut covers.
	if suffix.entries > x.meta.Entries {
		// The tail holds more entries than the whole covered span ever did:
		// the transcript and the committed records disagree, and the
		// arithmetic below would underflow into a cut that copies the full
		// state. The replay decides.
		return nil
	}
	cutOrdinal := x.meta.Entries - suffix.entries
	pending := false
	for _, c := range x.meta.CommCalls {
		if c.Ordinal < cutOrdinal {
			pending = true
			break
		}
	}
	if pending && suffix.assistantText {
		// A call still pending at the cut needs the echo check's prior text;
		// a suffix assistant entry overwrote it, and no committed record
		// names the entry that held it. Everything the guard needs is known
		// here, before any table is built and then discarded.
		return nil
	}
	items, counts, err := x.cutItems(cutOrdinal, length, suffix)
	if err != nil {
		return nil
	}
	turns, latestExecution, err := x.cutTurns(cutOrdinal, length, counts)
	if err != nil {
		return nil
	}
	updates, err := x.cutUpdates(length)
	if err != nil {
		return nil
	}
	view := &Index{readView: true, transcript: x.transcript, path: x.path,
		strings: x.strings, prelude: x.prelude,
		meta: meta{Incarnation: x.meta.Incarnation, Length: length, Entries: cutOrdinal,
			HeaderOffset: x.meta.HeaderOffset, HeaderLength: x.meta.HeaderLength,
			LatestExecutionTurnSlot: latestExecution},
		items: items, turns: *turns, updates: updates}
	view.builder = newBuilder(view)
	restoreDeferredState(&view.builder, x.meta, cutOrdinal)
	view.meta.PendingCommunicate = len(view.builder.commCalls) > 0
	return view
}

// cutAtLineEnd reports whether the byte before length ends a transcript line:
// one byte of I/O, a boundary probe rather than a scanned region.
func (x *Index) cutAtLineEnd(length int64) bool {
	var last [1]byte
	if _, err := x.transcript.ReadAt(last[:], length-1); err != nil {
		return false
	}
	return last[0] == '\n'
}

// suffixEffects is what the unpublished tail does to the state a cut read
// serves: how many entries it holds, which deferred communicate calls its
// assistant entries announced — all of them, consumed or not — the ordinals
// of its entries that deliver a COMMUNICATE result, and whether any of its
// assistant entries carried the visible text that updates the sticky
// last-assistant value.
type suffixEffects struct {
	entries       uint64
	commAdds      map[string]bool
	commAnnounced map[string]bool
	commDelivers  []uint64
	assistantText bool
}

// commDelivered reports whether the entry at ordinal, which the cut does not
// cover, delivered a COMMUNICATE result. The delivery list is short and
// usually empty, so a scan over it beats the map it would otherwise need.
func (s suffixEffects) commDelivered(ordinal uint64) bool {
	return slices.Contains(s.commDelivers, ordinal)
}

// scanSuffixEffects walks the unpublished tail once, counting its entries and
// recording its builder-state effects: the deferred communicate calls its
// assistant entries announce, the entries that deliver a COMMUNICATE result,
// and the assistant entries whose trailing text updates the sticky
// last-assistant value. It answers errNotDerivable when an entry of the tail
// consumed a call that was pending before the tail began — the consumed
// call's cut state was pending, and its announcing entry is not recoverable
// from the committed records without replaying the prefix.
func (x *Index) scanSuffixEffects(length int64) (suffix suffixEffects, err error) {
	suffix.commAdds = map[string]bool{}
	suffix.commAnnounced = map[string]bool{}
	reader := bufio.NewReaderSize(io.NewSectionReader(x.transcript, length, x.meta.Length-length), 4<<10)
	for {
		line, complete, _, err := transcript.ReadLine(reader, maxLineBytes)
		if err != nil {
			return suffix, err
		}
		if !complete {
			break
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		suffix.entries++
		entry, err := transcript.DecodeEntry(trimmed)
		if err != nil {
			// A line that does not decode is quarantined: it takes its entry
			// ordinal and touches no deferred call.
			continue
		}
		if err := applySuffixEffects(&entry.Turn, suffix.entries, &suffix); err != nil {
			return suffix, err
		}
	}
	// The tail's entries are the file's last, so their ordinals follow the
	// cut's entry count, which only the completed scan can name.
	for i := range suffix.commDelivers {
		suffix.commDelivers[i] += x.meta.Entries - suffix.entries - 1
	}
	return suffix, nil
}

// applySuffixEffects folds one suffix entry's builder-state effects into
// suffix, at position: the entry's 1-based position in the tail. It answers
// errNotDerivable when the entry consumed a deferred communicate call that was
// pending before the tail began — the one case the committed records cannot
// reconstruct. A call the tail itself announced and then consumed never
// existed at the cut, so consuming it is not a fallback.
func applySuffixEffects(turn *schema.Turn, position uint64, suffix *suffixEffects) error {
	switch turn.Kind {
	case schema.TurnAssistant:
		for _, part := range turn.Message.Content {
			if deferredCommCall(turn, part) {
				suffix.commAdds[part.ToolCall.ID] = true
				suffix.commAnnounced[part.ToolCall.ID] = true
			}
		}
		// Whether the entry's trailing text updates the sticky last-assistant
		// value: only the projection knows how the formats segment text, so
		// project the entry the way the builder does and ask the same rule
		// recordCommState applies. One suffix entry that carried text decides
		// the guard, so the projection stops once one did. The projected
		// positions never reach the committed records and do not matter here.
		if !suffix.assistantText {
			items, _ := apptranscript.ProjectEntryParts(turn.TurnID, int(position), *turn,
				&apptranscript.ToolCallRegistry{}, nil, apptranscript.ToolResultOutputImages)
			if agentMessageText(items) != "" {
				suffix.assistantText = true
			}
		}
		return nil
	case schema.TurnTool, schema.TurnToolResults:
		for _, part := range turn.Message.Content {
			if part.Kind != llm.ContentToolResult || part.ToolResult == nil {
				continue
			}
			id := part.ToolResult.ToolCallID
			name := part.ToolResult.Name
			if name == "" {
				if suffix.commAdds[id] {
					// A deferred communicate call announces itself, so its
					// nameless result is one of its own.
					name = "communicate"
				} else {
					// The name resolution needs a call the prefix announced:
					// only the replay holds it.
					return errNotDerivable
				}
			}
			if name != "communicate" {
				continue
			}
			if suffix.commAdds[id] {
				delete(suffix.commAdds, id)
				continue
			}
			// The entry consumed a call deferred before the tail began; its
			// cut state was pending, and no committed record names it.
			return errNotDerivable
		}
		return nil
	case schema.TurnCommunicate:
		if turn.Communicate != nil {
			// The one contributor shape that can stamp a call-less item's
			// Call field: one the suffix delivered may have stamped it after
			// the cut, and the cut state cannot tell whether it did.
			suffix.commDelivers = append(suffix.commDelivers, position)
		}
		if turn.Format != schema.TurnFormatIdentity || turn.Communicate == nil {
			return nil // a legacy COMMUNICATE consumed nothing
		}
		if suffix.commAdds[turn.Communicate.CallID] {
			delete(suffix.commAdds, turn.Communicate.CallID)
			return nil
		}
		return errNotDerivable
	default:
		return nil
	}
}

// cutItems copies the item records an entry at or before the cut opened,
// truncating at the cut ordinal the contributors a suffix entry added. It
// also counts each turn's kept items, the count a stamped turn's fold needs
// for its summary, because the fold itself decodes no items.
func (x *Index) cutItems(cutOrdinal uint64, length int64, suffix suffixEffects) (table, []uint32, error) {
	first, err := x.firstItemCreatedAt(length)
	if err != nil {
		return table{size: itemRecordSize}, nil, err
	}
	view := table{size: itemRecordSize, buf: make([]byte, 0, int(first)*itemRecordSize)}
	// One count per turn slot, filled in a single pass; the fold consults a
	// stamped turn's count and never the map it would otherwise hash the
	// whole table into.
	counts := make([]uint32, x.turns.n)
	const chunk = 256
	var scratch [itemRecordSize]byte
	for start := uint64(0); start < first; start += chunk {
		count := min(uint64(chunk), first-start)
		buf, err := x.items.read(start, int(count))
		if err != nil {
			return view, nil, err
		}
		for i := range count {
			record := decodeItem(buf[i*itemRecordSize:])
			if len(suffix.commAnnounced) > 0 && record.Call.Len > 0 {
				call, err := x.strings.get(record.Call)
				if err != nil {
					return view, nil, err
				}
				if suffix.commAnnounced[string(call)] {
					// A call the tail announced that an item below the cut
					// already names was re-announced: the cut's pending state
					// for it and the tail's own announce are not separable
					// from the committed records. The replay decides.
					return view, nil, errNotDerivable
				}
			}
			if record.Version > cutOrdinal {
				record, err = x.truncateItemContributors(record, cutOrdinal, suffix)
				if err != nil {
					return view, nil, err
				}
				copy(buf[i*itemRecordSize:], encodeItemInto(scratch[:], record))
			}
			counts[record.Turn]++
		}
		view.buf = append(view.buf, buf...)
		view.n += count
	}
	return view, counts, nil
}

// truncateItemContributors rewrites record as it stood at the cut ordinal,
// reporting an error when the committed records could not derive it. The
// contributors the cut keeps are a prefix of the recorded list, in both the
// record fields and the encoded Middle bytes, so the surgery moves the latest
// kept contributor into Completer, shortens Middle to the remaining encodings,
// and stamps the version of the kept contributor — or the opener's, when the
// cut kept none.
func (x *Index) truncateItemContributors(record itemRecord, cutOrdinal uint64, suffix suffixEffects) (itemRecord, error) {
	// A record whose version the suffix stamped has its latest contributor in
	// the suffix; anything else means the tables disagree with the transcript,
	// and the replay decides.
	if record.Completer.Length == 0 || record.Completer.Ordinal < cutOrdinal {
		return itemRecord{}, errNotDerivable
	}
	middle, err := x.strings.get(record.Middle)
	if err != nil {
		return itemRecord{}, err
	}
	contributors, err := decodeContributors(middle)
	if err != nil {
		return itemRecord{}, err
	}
	if record.Call.Len > 0 {
		// A COMMUNICATE delivery that reaches a call-less item is the one
		// contributor that can stamp the Call field. One the suffix delivered
		// may have stamped it after the cut, and the cut state cannot tell
		// whether it did: the replay decides. The dropped contributors all lie
		// in the suffix, whose deliveries the tail scan already recorded.
		if suffix.commDelivered(record.Completer.Ordinal) {
			return itemRecord{}, errNotDerivable
		}
		for _, c := range contributors {
			if c.Ordinal >= cutOrdinal && suffix.commDelivered(c.Ordinal) {
				return itemRecord{}, errNotDerivable
			}
		}
	}
	kept := 0
	for kept < len(contributors) && contributors[kept].Ordinal < cutOrdinal {
		kept++
	}
	record.Completer = contributor{}
	record.Middle.Len = 0
	record.Version = record.Opener.Ordinal + 1
	if kept > 0 {
		record.Completer = contributors[kept-1]
		record.Middle.Len = uint32(kept-1) * contributorSize
		record.Version = contributors[kept-1].Ordinal + 1
	}
	return record, nil
}

// cutTurns copies the turn summaries an entry at or before the cut opened,
// folding again the ones a suffix entry stamped. It returns the copied table
// and the one-based slot of the latest kept execution summary.
func (x *Index) cutTurns(cutOrdinal uint64, length int64, counts []uint32) (*table, uint64, error) {
	first, err := x.firstTurnCreatedAt(length)
	if err != nil {
		return nil, 0, err
	}
	view := &table{size: turnRecordSize, buf: make([]byte, 0, int(first)*turnRecordSize)}
	latestExecution := uint64(0)
	const chunk = 256
	var scratch [turnRecordSize]byte
	for start := uint64(0); start < first; start += chunk {
		count := min(uint64(chunk), first-start)
		buf, err := x.turns.read(start, int(count))
		if err != nil {
			return nil, 0, err
		}
		for i := range count {
			record := decodeTurn(buf[i*turnRecordSize:])
			if record.Version > cutOrdinal {
				record, err = x.foldTurnAtCut(record, length, counts[start+i])
				if err != nil {
					return nil, 0, err
				}
				copy(buf[i*turnRecordSize:], encodeTurnInto(scratch[:], record))
			}
			if record.Kind == turnKindExecution {
				latestExecution = start + i + 1
			}
		}
		view.buf = append(view.buf, buf...)
		view.n += count
	}
	return view, latestExecution, nil
}

// foldTurnAtCut rebuilds one stamped turn's summary as it stood at the cut:
// the same fold stampTurn performed at build time, over the turn's own entries
// alone. Legacy turns are adjacency groups, so the scan ends where the next
// group opens; identity turns interleave with others, so their scan filters by
// turn id across the whole span. It answers an error when the span cannot
// reproduce the committed summary exactly — a nameless tool result whose
// announcing call the span does not hold, or a model the committed summary
// does not name.
func (x *Index) foldTurnAtCut(record turnRecord, length int64, itemCount uint32) (turnRecord, error) {
	id, err := x.strings.get(record.ID)
	if err != nil {
		return turnRecord{}, err
	}
	turnID := string(id)
	legacy := record.Kind == turnKindLegacy
	folded := turnRecord{ID: record.ID, Kind: record.Kind, FirstOffset: record.FirstOffset, FirstOrdinal: record.FirstOrdinal}
	if folded.Kind == turnKindExecution {
		folded.Status = statusOpen
	}
	failures := transcript.NewFailureCounter(0)
	grouper := apptranscript.TurnGrouper{}
	reader := bufio.NewReaderSize(io.NewSectionReader(x.transcript, record.FirstOffset, length-record.FirstOffset), 4<<10)
	offset := record.FirstOffset
	ordinal := record.FirstOrdinal
	spanModel := ""
span:
	for offset < length {
		line, complete, n, err := transcript.ReadLine(reader, maxLineBytes)
		if err != nil || !complete {
			return turnRecord{}, errNotDerivable
		}
		start := offset
		offset += n
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		here := ordinal
		ordinal++
		entry, err := transcript.DecodeEntry(trimmed)
		if err != nil {
			// A line that does not decode is quarantined: it joins no turn,
			// and it closes the open legacy group.
			if legacy {
				break span
			}
			continue
		}
		turn := entry.Turn
		if turn.OriginalOrdinal != nil {
			continue // a fold copy takes its ordinal and stamps nothing
		}
		// Every entry the replay's own counter observes reaches this fold's
		// counter too, member or not: a nameless result takes its name from
		// the LATEST announcement of its call, and inside an identity turn's
		// span an interleaved entry may hold the newer one. A non-member's
		// count never reaches the summary — it cancels in the next member's
		// delta — but its announcements must be seen for the members' names to
		// resolve the way the replay's do.
		before := failures.Count()
		unresolved := failures.ObserveEntry(turn)
		member := false
		if legacy {
			if turn.Format == schema.TurnFormatIdentity {
				if turn.TurnID == turnID {
					// A new-format entry with the group's own id can stamp
					// this summary through the shared id space, which the
					// span alone cannot decide: the replay knows.
					return turnRecord{}, errNotDerivable
				}
				break span // the grouping state reset: the group is closed
			}
			if turn.Kind.TranscriptOnly() {
				continue // the legacy application passes over it
			}
			_, newTurn := grouper.Place(&turn, int(here)+1)
			if newTurn && here != record.FirstOrdinal {
				break span // the next group opened: this turn's span is over
			}
			member = true
		} else {
			member = turn.Format == schema.TurnFormatIdentity && turn.TurnID == turnID
			if member && turn.TurnKind != "" && here != record.FirstOrdinal {
				// A span-bearing member after the first either re-stamped this
				// record — the id was still open — or opened a new record under
				// the same id; the span alone cannot tell, and the replay knows.
				return turnRecord{}, errNotDerivable
			}
		}
		if !member {
			continue
		}
		if unresolved {
			// The announcing call lies before the span: only the replay's
			// whole-file name map resolves it.
			return turnRecord{}, errNotDerivable
		}
		folded.FailedToolCalls += uint32(failures.Count() - before)
		folded.Version = here + 1
		stampFold(&folded, &turn, here, start, uint32(len(line)))
		if turn.Model != "" {
			spanModel = turn.Model
		}
	}
	if folded.Version == 0 {
		// The span stamped nothing into this turn: the committed summary
		// disagrees with the transcript, and the replay decides.
		return turnRecord{}, errNotDerivable
	}
	if spanModel != "" {
		model, err := x.strings.get(record.Model)
		if err != nil || spanModel != string(model) {
			// The committed summary names a different model than the span, so
			// the cut state needs a string the records do not hold.
			return turnRecord{}, errNotDerivable
		}
		folded.Model = record.Model
	}
	folded.ItemCount = itemCount
	return folded, nil
}

// cutUpdates copies the update-log rows entries at or before the cut wrote.
// Rows are offset-ordered, so the kept log is a prefix of the committed one:
// the cut keeps every row below both the cut's length and the committed row
// count, which a crashed extension's leftover rows — written past the count —
// never reach. reconstructAtCut only runs on a log that was never cut, whose
// rows reach the transcript's start, so the cut view's ChangedSince answers
// any length the replay's would.
func (x *Index) cutUpdates(length int64) (table, error) {
	first, err := x.firstUpdateAt(length)
	if err != nil {
		return table{size: updateRecordSize}, err
	}
	end := min(first, x.meta.Updates)
	if end == 0 {
		return table{size: updateRecordSize, buf: []byte{}}, nil
	}
	// read hands over a fresh, exactly-sized buffer of the kept rows.
	buf, err := x.updates.read(0, int(end))
	if err != nil {
		return table{size: updateRecordSize}, err
	}
	return table{size: updateRecordSize, buf: buf, n: end}, nil
}
