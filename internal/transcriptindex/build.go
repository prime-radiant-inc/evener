package transcriptindex

import (
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// errRebuild reports an entry the index cannot apply incrementally. One is a
// legacy tool result with no name of its own, for a call the open legacy turn
// does not know: the whole-file projection names it from every call seen
// before it, which only a full build has at hand. The other is a new-format
// tool result or communicate message replayed after a crash, whose turn's
// awaiting ASSISTANT entry an in-place rewrite already moved past it.
var errRebuild = errors.New("transcript index needs a full rebuild")

// toolName is the projection's tool-name state for one call id: the name its
// latest tool call registered, or deleted after a communicate result.
type toolName struct {
	name    string
	present bool
}

// commState is the deferred-communicate state for one open call: the raw
// argument bytes ProjectTurn's ToolCallRegistry.CommRawArgs would hold for it,
// the assistant entry that set them (for the paired result's Context), and
// the logical turn id that entry projected under.
type commState struct {
	rawArgs string
	pos     contributor
	turnID  string
}

// builder applies entries, in file order, to the index's records.
//
// Legacy entries are grouped as today's projection groups them. The builder
// holds the open legacy group: the grouping state, the group's call items,
// and the tool names it registered. A full build also keeps every tool name
// registered so far (global), as the whole-file projection does; an index
// that only extends does without it (see errRebuild).
//
// New-format entries join the turn their TurnID names. The builder holds the
// turns that can still take entries (open), so the common case finds its
// turn without a search; everything else a new-format turn needs, its status
// and its awaiting ASSISTANT entry, is in its summary record.
type builder struct {
	x        *Index
	grouper  apptranscript.TurnGrouper
	turnSlot uint64            // the open legacy group's summary
	calls    map[string]uint64 // open legacy group: call id -> item slot
	names    map[string]toolName
	global   map[string]string
	// open maps the TurnID of each new-format turn that can still take
	// entries to its summary slot: open executions, the latest gap turn and
	// the prelude. An entry of any other turn searches for it (findTurn).
	open map[string]uint64
	gap  string // the latest gap turn's id
	// summary caches the summary record at summarySlot, written through.
	summary     turnRecord
	summarySlot uint64
	hasSummary  bool
	// assistant is the latest ASSISTANT entry with tool calls this builder
	// applied, at assistantOffset, so the results after it need not read it
	// back.
	assistant       *schema.Turn
	assistantOffset int64
	models          map[string]strRef

	// commCalls and the lastAssistant* fields mirror ProjectTurn's
	// ToolCallRegistry.CommRawArgs/LastAssistantText/LastAssistantTurnID:
	// unlike names/calls, they are NOT reset at a new open legacy group,
	// because a communicate call's deferred state legitimately crosses
	// logical-turn/group boundaries (e.g. a standalone HOOK_COMPLETED
	// closing the assistant's group before its result arrives) — matching
	// the full read's single registry, threaded for the whole scan. This
	// state applies to legacy entries only (applyLegacy): a new-format
	// COMMUNICATE entry carries its message explicitly, so applyIdentity
	// never needs it. An item's own Context (see itemRecord) persists only
	// the position each is at, not the projected item content: the reader
	// replays the referenced raw entries to reconstruct the values, so build
	// time only needs enough to decide the affected items' structure
	// (existence/parts). meta.CommCalls/LastAssistant* separately persist
	// this whole struct's values, for restoreBuilder to reconstruct it
	// without a rebuild.
	commCalls           map[string]commState // call id -> its deferred communicate state
	lastAssistantText   string
	lastAssistantTurnID string
	lastAssistantPos    contributor
	// lastAssistantKnown is true once the sticky value above is
	// authoritative: always, for a full build (correct from its first
	// entry — "no text yet" is the ground truth, not an unknown), and for
	// restoreBuilder once it has restored meta.LastAssistantKnown. A
	// communicate call becoming pending while this is still false means the
	// echo check's prior text is unrecoverable without a rebuild (see
	// seed); kept as a defensive fallback now that restoreBuilder
	// ordinarily restores it.
	lastAssistantKnown bool
}

func newBuilder(x *Index) builder {
	return builder{
		x: x, calls: map[string]uint64{}, names: map[string]toolName{}, open: map[string]uint64{}, models: map[string]strRef{},
		commCalls: map[string]commState{},
	}
}

// apply indexes the entry at ordinal, whose line is length bytes at offset.
func (b *builder) apply(ordinal uint64, offset int64, length uint32, entry *schema.Turn) error {
	if entry.Format == schema.TurnFormatIdentity {
		return b.applyIdentity(ordinal, offset, length, entry)
	}
	return b.applyLegacy(ordinal, offset, length, entry)
}

// quarantine indexes an entry line that does not decode as one unreadable
// item in a completed turn of its own; the reader projects it from the line
// alone (reader.unreadable). It closes the open legacy group, as a
// new-format entry does, so a legacy entry after it starts a turn of its own.
func (b *builder) quarantine(ordinal uint64, offset int64, length uint32) error {
	b.grouper = apptranscript.TurnGrouper{}
	slot, err := b.openTurn(unreadableTurnID(ordinal), turnKindUnreadable, ordinal, offset)
	if err != nil {
		return err
	}
	b.summary.Version = ordinal + 1
	if err := b.x.turns.write(slot, encodeTurn(b.summary)); err != nil {
		return err
	}
	c := contributor{Offset: offset, Ordinal: ordinal, Length: length}
	_, err = b.x.items.append(encodeItem(itemRecord{Entry: ordinal + 1, Turn: uint32(slot), Version: ordinal + 1, Opener: c, Flags: itemUnreadable}))
	return err
}

// unreadableTurnID is the id of the turn holding the unreadable entry at
// ordinal.
func unreadableTurnID(ordinal uint64) string { return fmt.Sprintf("turn_unreadable_%d", ordinal) }

// applyLegacy indexes a legacy entry by today's rules.
func (b *builder) applyLegacy(ordinal uint64, offset int64, length uint32, entry *schema.Turn) error {
	if entry.Kind.TranscriptOnly() {
		// Today's projection passes over it: the entry takes its ordinal and
		// nothing else.
		return nil
	}
	entryIndex := int(ordinal) + 1
	version := ordinal + 1
	turnID, newTurn := b.grouper.Place(entry, entryIndex)
	if newTurn {
		slot, err := b.openTurn(turnID, turnKindLegacy, ordinal, offset)
		if err != nil {
			return err
		}
		b.turnSlot = slot
		b.calls = map[string]uint64{}
		b.names = map[string]toolName{}
	}
	seed, err := b.seed(entry)
	if err != nil {
		return err
	}
	reg := &apptranscript.ToolCallRegistry{
		Names:               maps.Clone(seed),
		CommRawArgs:         b.commRawArgsSnapshot(),
		LastAssistantText:   b.lastAssistantText,
		LastAssistantTurnID: b.lastAssistantTurnID,
	}
	items, parts := apptranscript.ProjectEntryParts(turnID, entryIndex, *entry, reg, apptranscript.AddressedImageProjector, apptranscript.ToolResultOutputImages)
	b.recordNames(entry, seed)
	base := contributor{Offset: offset, Ordinal: ordinal, Length: length}
	for i, item := range items {
		c := base
		merges := apptranscript.MergesByCallID(item)
		if merges {
			if name, ok := seed[item.CallID]; ok {
				if c.Name, err = b.x.strings.put([]byte(name)); err != nil {
					return err
				}
			}
			if slot, ok := b.calls[item.CallID]; ok {
				if err := b.addContributor(slot, c, version); err != nil {
					return err
				}
				continue
			}
		}
		var context strRef
		if callID, ok := communicateResultCallID(entry, parts[i], seed); ok {
			if context, err = b.communicateContext(callID); err != nil {
				return err
			}
		}
		slot, err := b.appendItemWithContext(item, parts[i], b.turnSlot, c, context)
		if err != nil {
			return err
		}
		if merges {
			b.calls[item.CallID] = slot
		}
	}
	b.recordCommState(entry, turnID, seed, base)
	return b.stampTurn(b.turnSlot, entry, ordinal, offset, length)
}

// commRawArgsSnapshot is the real CommRawArgs values ProjectTurn's registry
// needs to correctly decide, at build time, which items a communicate result
// entry projects (see commCalls' doc comment: build time needs values, not
// just positions).
func (b *builder) commRawArgsSnapshot() map[string]string {
	snapshot := make(map[string]string, len(b.commCalls))
	for id, state := range b.commCalls {
		snapshot[id] = state.rawArgs
	}
	return snapshot
}

// communicateResultCallID reports the call id a TOOL/TOOL_RESULTS entry's
// content part projects a communicate item for (a rejected or healed
// communicate result), matching the name resolution apptranscript.ProjectTurn
// itself applies (an explicit Name, or the seed's resolution of a blank one).
func communicateResultCallID(entry *schema.Turn, part int, seed map[string]string) (string, bool) {
	if entry.Kind != schema.TurnTool && entry.Kind != schema.TurnToolResults {
		return "", false
	}
	if part < 0 || part >= len(entry.Message.Content) {
		return "", false
	}
	content := entry.Message.Content[part]
	if content.Kind != llm.ContentToolResult || content.ToolResult == nil {
		return "", false
	}
	name := content.ToolResult.Name
	if name == "" {
		name = seed[content.ToolResult.ToolCallID]
	}
	if name != "communicate" {
		return "", false
	}
	return content.ToolResult.ToolCallID, true
}

// communicateContext builds the Context an item at communicateResultCallID
// needs: the assistant entry that set CommRawArgs for the call (always, if
// known) and, when it differs, the assistant entry that most recently set
// LastAssistantText (needed only by the healed-communicate echo check, but
// harmless to include for a rejected one too) — sorted in file order, so a
// reader replays them in the order they actually happened.
func (b *builder) communicateContext(callID string) (strRef, error) {
	var list []contextEntry
	seen := map[int64]bool{}
	add := func(pos contributor, turnID string) error {
		if pos.Length == 0 || seen[pos.Offset] {
			return nil
		}
		seen[pos.Offset] = true
		ref, err := b.x.strings.put([]byte(turnID))
		if err != nil {
			return err
		}
		list = append(list, contextEntry{contributor: pos, TurnID: ref})
		return nil
	}
	if state, ok := b.commCalls[callID]; ok {
		if err := add(state.pos, state.turnID); err != nil {
			return strRef{}, err
		}
	}
	if b.lastAssistantKnown {
		if err := add(b.lastAssistantPos, b.lastAssistantTurnID); err != nil {
			return strRef{}, err
		}
	}
	if len(list) == 0 {
		return strRef{}, nil
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Ordinal < list[j].Ordinal })
	return b.x.strings.put(encodeContextEntries(list))
}

// recordCommState applies the entry's effects on the deferred-communicate
// state ProjectTurn's ToolCallRegistry threads — the same effects
// recordNames applies to tool names, kept separate because unlike names this
// state is never reset at a legacy group boundary (see the builder doc
// comment). Called after the entry's items are recorded, so
// communicateContext above still sees the state as it stood when this entry
// was projected.
func (b *builder) recordCommState(entry *schema.Turn, turnID string, seed map[string]string, c contributor) {
	if entry.Kind == schema.TurnAssistant {
		var lastText string
		for _, part := range entry.Message.Content {
			switch part.Kind {
			case llm.ContentText:
				if part.Text != "" {
					lastText = strings.TrimSpace(part.Text)
				}
			case llm.ContentToolCall:
				if part.ToolCall != nil && part.ToolCall.Name == "communicate" {
					b.commCalls[part.ToolCall.ID] = commState{rawArgs: part.ToolCall.SentArguments(), pos: c, turnID: turnID}
				}
			}
		}
		// Mirrors ProjectTurn: only a non-empty text updates the sticky
		// value, and only the entry's own trailing text (never a healed
		// communicate — main's fix defers that entirely to the result turn).
		if lastText != "" {
			b.lastAssistantText, b.lastAssistantTurnID, b.lastAssistantPos, b.lastAssistantKnown = lastText, turnID, c, true
		}
		return
	}
	if entry.Kind != schema.TurnTool && entry.Kind != schema.TurnToolResults {
		return
	}
	for _, part := range entry.Message.Content {
		if part.Kind != llm.ContentToolResult || part.ToolResult == nil {
			continue
		}
		name := part.ToolResult.Name
		if name == "" {
			name = seed[part.ToolResult.ToolCallID]
		}
		if name == "communicate" {
			delete(b.commCalls, part.ToolResult.ToolCallID)
		}
	}
}

// applyIdentity indexes a new-format entry: it joins the turn its TurnID
// names, and a tool result completes the call of its turn's awaiting
// ASSISTANT entry.
func (b *builder) applyIdentity(ordinal uint64, offset int64, length uint32, entry *schema.Turn) error {
	if entry.OriginalOrdinal != nil {
		// A fold copy is model history for resume; its original already
		// projected. It takes its ordinal and nothing else.
		return nil
	}
	// A legacy continuation after a new-format entry opens its own group.
	b.grouper = apptranscript.TurnGrouper{}
	slot, err := b.place(ordinal, offset, entry)
	if err != nil {
		return err
	}
	summary, err := b.load(slot)
	if err != nil {
		return err
	}
	switch entry.Kind {
	case schema.TurnTool, schema.TurnToolResults, schema.TurnCommunicate:
		if summary.AwaitingLength > 0 && summary.AwaitingOrdinal > ordinal {
			return errRebuild
		}
	}
	turnID := entry.TurnID
	c := contributor{Offset: offset, Ordinal: ordinal, Length: length}
	switch entry.Kind {
	case schema.TurnTool, schema.TurnToolResults:
		if err := b.applyResults(slot, summary, entry, c); err != nil {
			return err
		}
	case schema.TurnCommunicate:
		echoes, err := b.echoesAwaiting(summary, entry)
		if err != nil {
			return err
		}
		if !echoes {
			if err := b.appendItems(slot, entry, c, nil, nil); err != nil {
				return err
			}
		}
	default:
		if err := b.appendItems(slot, entry, c, nil, nil); err != nil {
			return err
		}
	}
	if entry.Kind == schema.TurnCompletion {
		delete(b.open, turnID)
		if err := b.interruptAwaitedCalls(summary, c); err != nil {
			return err
		}
	}
	if entry.Kind == schema.TurnAssistant && hasToolCall(entry) {
		b.assistant, b.assistantOffset = entry, offset
	}
	return b.stampTurn(slot, entry, ordinal, offset, length)
}

// place returns the summary slot of the new-format turn the entry joins,
// appending a summary for a turn's first entry.
func (b *builder) place(ordinal uint64, offset int64, entry *schema.Turn) (uint64, error) {
	id := entry.TurnID
	if slot, ok := b.open[id]; ok {
		return slot, nil
	}
	// Only a turn's first entry records its kind. An entry without one joins
	// a turn recorded earlier: a reopened turn, or resume completing a turn a
	// crash left open.
	if entry.TurnKind == "" {
		slot, found, err := b.findTurn(id)
		if err != nil {
			return 0, err
		}
		if found {
			b.open[id] = slot
			return slot, nil
		}
	}
	kind := turnKindOf(entry.TurnKind)
	slot, err := b.openTurn(id, kind, ordinal, offset)
	if err != nil {
		return 0, err
	}
	switch kind {
	case turnKindExecution, turnKindPrelude:
		b.open[id] = slot
	case turnKindGap:
		if b.gap != "" {
			delete(b.open, b.gap)
		}
		b.gap = id
		b.open[id] = slot
	}
	return slot, nil
}

// turnKindOf maps a recorded TurnKind to the summary's kind. A turn whose
// first entry the index never saw records none; it is an execution being
// reopened.
func turnKindOf(kind schema.TurnSpanKind) uint32 {
	switch kind {
	case schema.TurnSpanGap:
		return turnKindGap
	case schema.TurnSpanDelivery:
		return turnKindDelivery
	case schema.TurnSpanPrelude:
		return turnKindPrelude
	default:
		return turnKindExecution
	}
}

// findTurn searches the summaries, newest first, for the new-format turn id.
// Legacy turns never match: a new-format entry never joins one.
func (b *builder) findTurn(id string) (uint64, bool, error) {
	const chunk = 256
	for end := b.x.turns.n; end > 0; {
		start := end - min(end, chunk)
		buf, err := b.x.turns.read(start, int(end-start))
		if err != nil {
			return 0, false, err
		}
		for slot := end; slot > start; slot-- {
			record := decodeTurn(buf[(slot-1-start)*turnRecordSize:])
			if record.Kind == turnKindLegacy || int(record.ID.Len) != len(id) {
				continue
			}
			name, err := b.x.strings.get(record.ID)
			if err != nil {
				return 0, false, err
			}
			if string(name) == id {
				return slot - 1, true, nil
			}
		}
		end = start
	}
	return 0, false, nil
}

// awaitedCall is one call of an awaiting ASSISTANT entry: its content part and
// tool name.
type awaitedCall struct {
	part int
	name string
}

// applyResults indexes a new-format TOOL_RESULTS (or TOOL) entry. Each result
// whose call id names a call of the turn's awaiting ASSISTANT entry completes
// that call's item; any other result projects as its own item.
func (b *builder) applyResults(slot uint64, summary turnRecord, entry *schema.Turn, c contributor) error {
	calls := map[string]awaitedCall{}
	if summary.AwaitingLength > 0 {
		awaiting, err := b.awaitingEntry(summary)
		if err != nil {
			return err
		}
		for part, content := range awaiting.Message.Content {
			if content.Kind != llm.ContentToolCall || content.ToolCall == nil {
				continue
			}
			if _, seen := calls[content.ToolCall.ID]; !seen {
				calls[content.ToolCall.ID] = awaitedCall{part: part, name: content.ToolCall.Name}
			}
		}
	}
	// A result with no name of its own takes its call's.
	seed := map[string]string{}
	for _, part := range entry.Message.Content {
		if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.Name == "" {
			if call, ok := calls[part.ToolResult.ToolCallID]; ok {
				seed[part.ToolResult.ToolCallID] = call.name
			}
		}
	}
	version := c.Ordinal + 1
	return b.appendItems(slot, entry, c, seed, func(item appwire.ThreadItem) (bool, error) {
		call, ok := calls[item.CallID]
		if !ok || !apptranscript.MergesByCallID(item) {
			return false, nil
		}
		// A communicate call projects no item, so its result has none to
		// complete.
		target, found, err := b.x.findItem(appwire.ThreadItemPosition{Entry: summary.AwaitingOrdinal + 1, Item: uint32(call.part)})
		if err != nil || !found {
			return false, err
		}
		completer := c
		if name, named := seed[item.CallID]; named {
			if completer.Name, err = b.x.strings.put([]byte(name)); err != nil {
				return false, err
			}
		}
		return true, b.addContributor(target, completer, version)
	})
}

// interruptAwaitedCalls makes an execution's completion a contributor of
// every call of its awaiting ASSISTANT entry that no TOOL_RESULTS completed:
// the call's item projects as interrupted (window.go), at the completion's
// version. That completion is the interrupted one resume records for an
// execution a crash left open, so a reload, a restart and a daemonless read
// all show the call interrupted rather than running forever.
func (b *builder) interruptAwaitedCalls(summary turnRecord, completion contributor) error {
	if summary.Kind != turnKindExecution || summary.AwaitingLength == 0 {
		return nil
	}
	awaiting, err := b.awaitingEntry(summary)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for part, content := range awaiting.Message.Content {
		if content.Kind != llm.ContentToolCall || content.ToolCall == nil || seen[content.ToolCall.ID] {
			continue
		}
		seen[content.ToolCall.ID] = true
		// A communicate call projects no item.
		slot, found, err := b.x.findItem(appwire.ThreadItemPosition{Entry: summary.AwaitingOrdinal + 1, Item: uint32(part)})
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		buf, err := b.x.items.read(slot, 1)
		if err != nil {
			return err
		}
		if record := decodeItem(buf); record.Completer.Length > 0 && record.Completer.Ordinal != completion.Ordinal {
			continue
		}
		if err := b.addContributor(slot, completion, completion.Ordinal+1); err != nil {
			return err
		}
	}
	return nil
}

// appendItems projects a new-format entry and appends an item record for each
// item absorb (when set) does not take. seed names the entry's nameless tool
// results.
func (b *builder) appendItems(slot uint64, entry *schema.Turn, c contributor, seed map[string]string, absorb func(appwire.ThreadItem) (bool, error)) error {
	// New-format entries never need CommRawArgs/LastAssistantText: a
	// COMMUNICATE entry carries its message explicitly, so seed's tool
	// names are all this registry ever needs.
	reg := &apptranscript.ToolCallRegistry{Names: seed}
	items, parts := apptranscript.ProjectEntryParts(entry.TurnID, int(c.Ordinal)+1, *entry, reg, apptranscript.AddressedImageProjector, apptranscript.ToolResultOutputImages)
	for i, item := range items {
		if absorb != nil {
			absorbed, err := absorb(item)
			if err != nil {
				return err
			}
			if absorbed {
				continue
			}
		}
		if _, err := b.appendItem(item, parts[i], slot, c); err != nil {
			return err
		}
	}
	return nil
}

// appendItem appends the record of an item the entry c opens at part.
func (b *builder) appendItem(item appwire.ThreadItem, part int, turnSlot uint64, c contributor) (uint64, error) {
	return b.appendItemWithContext(item, part, turnSlot, c, strRef{})
}

// appendItemWithContext is appendItem plus an explicit Context: only
// applyLegacy's deferred communicate results ever need one (see
// communicateContext); every other caller passes the zero strRef.
func (b *builder) appendItemWithContext(item appwire.ThreadItem, part int, turnSlot uint64, c contributor, context strRef) (uint64, error) {
	version := c.Ordinal + 1
	record := itemRecord{Entry: version, Part: uint32(part), Turn: uint32(turnSlot), Version: version, Opener: c, Context: context}
	if apptranscript.MergesByCallID(item) {
		var err error
		if record.Call, err = b.x.strings.put([]byte(item.CallID)); err != nil {
			return 0, err
		}
	}
	return b.x.items.append(encodeItem(record))
}

// echoesAwaiting reports whether a COMMUNICATE entry's message repeats the
// last text run of its turn's awaiting ASSISTANT entry, which already shows it.
func (b *builder) echoesAwaiting(summary turnRecord, entry *schema.Turn) (bool, error) {
	if entry.Communicate == nil || summary.AwaitingLength == 0 {
		return false, nil
	}
	awaiting, err := b.awaitingEntry(summary)
	if err != nil {
		return false, err
	}
	return apptranscript.EchoesAssistantText(lastTextRun(awaiting.Message.Content), entry.Communicate.Message), nil
}

// lastTextRun is the text of the last maximal run of consecutive text parts,
// the run the entry's last agentMessage shows.
func lastTextRun(content []llm.ContentPart) string {
	end := len(content)
	for end > 0 && content[end-1].Kind != llm.ContentText {
		end--
	}
	start := end
	for start > 0 && content[start-1].Kind == llm.ContentText {
		start--
	}
	var run strings.Builder
	for _, part := range content[start:end] {
		run.WriteString(part.Text)
	}
	return run.String()
}

func (b *builder) awaitingEntry(summary turnRecord) (*schema.Turn, error) {
	if b.assistant != nil && b.assistantOffset == summary.AwaitingOffset {
		return b.assistant, nil
	}
	entry, err := b.x.readEntry(summary.AwaitingOffset, summary.AwaitingLength)
	if err != nil {
		return nil, err
	}
	b.assistant, b.assistantOffset = entry, summary.AwaitingOffset
	return entry, nil
}

func hasToolCall(entry *schema.Turn) bool {
	for _, part := range entry.Message.Content {
		if part.Kind == llm.ContentToolCall && part.ToolCall != nil {
			return true
		}
	}
	return false
}

// openTurn appends the summary of a turn whose first entry is at ordinal.
func (b *builder) openTurn(turnID string, kind uint32, ordinal uint64, offset int64) (uint64, error) {
	id, err := b.x.strings.put([]byte(turnID))
	if err != nil {
		return 0, err
	}
	record := turnRecord{ID: id, Kind: kind, FirstOffset: offset, FirstOrdinal: ordinal}
	if kind == turnKindExecution {
		record.Status = statusOpen
	}
	slot, err := b.x.turns.append(encodeTurn(record))
	if err != nil {
		return 0, err
	}
	b.summary, b.summarySlot, b.hasSummary = record, slot, true
	return slot, nil
}

// load returns the summary at slot.
func (b *builder) load(slot uint64) (turnRecord, error) {
	if b.hasSummary && b.summarySlot == slot {
		return b.summary, nil
	}
	buf, err := b.x.turns.read(slot, 1)
	if err != nil {
		return turnRecord{}, err
	}
	b.summary, b.summarySlot, b.hasSummary = decodeTurn(buf), slot, true
	return b.summary, nil
}

// seed resolves, for each tool result in the entry that carries no name, the
// name the whole-file projection's tool-name map holds for its call.
func (b *builder) seed(entry *schema.Turn) (map[string]string, error) {
	if entry.Kind != schema.TurnTool && entry.Kind != schema.TurnToolResults {
		return nil, nil
	}
	seed := map[string]string{}
	for _, part := range entry.Message.Content {
		if part.Kind != llm.ContentToolResult || part.ToolResult == nil {
			continue
		}
		id := part.ToolResult.ToolCallID
		name := part.ToolResult.Name
		if name == "" {
			resolved, known := b.lookup(id)
			if !known {
				return nil, errRebuild
			}
			if resolved.present {
				seed[id] = resolved.name
				name = resolved.name
			}
		}
		if name != "communicate" {
			continue
		}
		// A rebuild-free extend also needs the deferred-communicate state
		// this call's result reads (see recordCommState/communicateContext):
		// known always during a full build (b.global set), and otherwise
		// only within the current scan's own local state.
		state, ok := b.commCalls[id]
		if !ok && b.global == nil {
			return nil, errRebuild
		}
		// The healed (non-error) branch alone reads LastAssistantText, and
		// only once it has non-empty raw args to repair into a message.
		if !part.ToolResult.IsError && state.rawArgs != "" && !b.lastAssistantKnown && b.global == nil {
			return nil, errRebuild
		}
	}
	return seed, nil
}

func (b *builder) lookup(id string) (toolName, bool) {
	if b.global != nil {
		name, ok := b.global[id]
		return toolName{name: name, present: ok}, true
	}
	name, ok := b.names[id]
	return name, ok
}

// recordNames applies the entry's effects on the tool-name map, the ones
// apptranscript.ProjectTurn makes: a tool call registers its name, and a
// result whose name resolves to communicate deletes it.
func (b *builder) recordNames(entry *schema.Turn, seed map[string]string) {
	for _, part := range entry.Message.Content {
		switch {
		case entry.Kind == schema.TurnAssistant && part.Kind == llm.ContentToolCall && part.ToolCall != nil:
			b.setName(part.ToolCall.ID, toolName{name: part.ToolCall.Name, present: true})
		case (entry.Kind == schema.TurnTool || entry.Kind == schema.TurnToolResults) && part.Kind == llm.ContentToolResult && part.ToolResult != nil:
			name := part.ToolResult.Name
			if name == "" {
				name = seed[part.ToolResult.ToolCallID]
			}
			if name == "communicate" {
				b.setName(part.ToolResult.ToolCallID, toolName{})
			}
		}
	}
}

func (b *builder) setName(id string, name toolName) {
	b.names[id] = name
	if b.global == nil {
		return
	}
	if name.present {
		b.global[id] = name.name
	} else {
		delete(b.global, id)
	}
}

// addContributor records a later entry's contribution to a call item. The
// previous completer, if any, moves to the middle list.
func (b *builder) addContributor(slot uint64, c contributor, version uint64) error {
	buf, err := b.x.items.read(slot, 1)
	if err != nil {
		return err
	}
	record := decodeItem(buf)
	if version <= record.Version {
		// This entry already contributed: before a crash, whose update-log
		// record the extension truncated away, or earlier in this entry.
		// Redo the log record; readers take each slot once.
		if version == record.Opener.Ordinal+1 {
			return nil // the entry that created the record logs nothing
		}
		return b.logUpdate(updatedItem, slot, c.Offset)
	}
	if record.Completer.Length > 0 {
		middle, err := b.x.strings.get(record.Middle)
		if err != nil {
			return err
		}
		if record.Middle, err = b.x.strings.put(append(middle, encodeContributors([]contributor{record.Completer})...)); err != nil {
			return err
		}
	}
	record.Completer = c
	record.Version = version
	if err := b.x.items.write(slot, encodeItem(record)); err != nil {
		return err
	}
	return b.logUpdate(updatedItem, slot, c.Offset)
}

// logUpdate records an in-place update, so a reader holding an older snapshot
// can find what changed after it.
func (b *builder) logUpdate(kind uint32, slot uint64, offset int64) error {
	_, err := b.x.updates.append(encodeUpdate(updateRecord{Kind: kind, Slot: slot, Offset: offset}))
	return err
}

// stampTurn folds the entry into its turn's summary: the way
// apptranscript.StampGroupedTurn folds a legacy group's entries, or, for a
// new-format turn, its status from completions and reopen markers and its
// awaiting ASSISTANT entry.
func (b *builder) stampTurn(slot uint64, entry *schema.Turn, ordinal uint64, offset int64, length uint32) error {
	r, err := b.load(slot)
	if err != nil {
		return err
	}
	version := ordinal + 1
	// The turn's first entry creates its summary; every later one rewrites
	// it, and logs that it did. The log record is redone even when a crashed
	// extension already applied the entry: the truncation before this
	// extension removed its log record, and readers take each slot once.
	if version > r.FirstOrdinal+1 {
		if err := b.logUpdate(updatedTurn, slot, offset); err != nil {
			return err
		}
	}
	if version <= r.Version {
		return nil // already accounted before an interrupted catch-up
	}
	if entry.Kind == schema.TurnFailure {
		r.FailureOffset, r.FailureLength = offset, length
	}
	switch r.Kind {
	case turnKindLegacy:
		if entry.Kind == schema.TurnFailure {
			r.Status = statusFailed
		}
		if entry.Kind == schema.TurnSteering && entry.SteeringKind == events.SteeringKindInterrupted && r.Status != statusFailed {
			r.Status = statusInterrupted
		}
	case turnKindExecution:
		switch entry.Kind {
		case schema.TurnCompletion:
			info := schema.TurnCompletionInfo{}
			if entry.Completion != nil {
				info = *entry.Completion
			}
			r.Status, r.HasCompletion, r.DurationMS, r.CompletedAt = completionStatus(info.Status), true, info.DurationMS, 0
			if !info.CompletedAt.IsZero() {
				r.CompletedAt = info.CompletedAt.UnixMilli()
			}
		case schema.TurnReopen:
			r.Status, r.HasCompletion, r.DurationMS, r.CompletedAt = statusOpen, false, 0, 0
		}
	}
	if r.Kind != turnKindLegacy && entry.Kind == schema.TurnAssistant && hasToolCall(entry) {
		r.AwaitingOffset, r.AwaitingLength, r.AwaitingOrdinal = offset, length, ordinal
	}
	if !r.Started && !entry.Timestamp.IsZero() {
		r.StartedAt, r.Started = entry.Timestamp.UnixMilli(), true
	}
	usage := entry.Usage
	r.Usage[0] += int64(usage.InputTokens)
	r.Usage[1] += int64(usage.OutputTokens)
	if usage.CacheReadTokens != nil {
		r.Usage[2] += int64(*usage.CacheReadTokens)
	}
	r.Usage[3] += int64(usage.TotalTokens)
	if entry.Model != "" {
		if r.Model, err = b.modelRef(entry.Model); err != nil {
			return err
		}
	}
	r.Version = version
	if err := b.x.turns.write(slot, encodeTurn(r)); err != nil {
		return err
	}
	b.summary, b.summarySlot, b.hasSummary = r, slot, true
	return nil
}

// completionStatus maps a completion entry's status to the summary's.
func completionStatus(status schema.TurnCompletionStatus) uint32 {
	switch status {
	case schema.TurnFailed:
		return statusFailed
	case schema.TurnInterrupted:
		return statusInterrupted
	default:
		return statusCompleted
	}
}

// modelRef stores each model name once per builder.
func (b *builder) modelRef(model string) (strRef, error) {
	if ref, ok := b.models[model]; ok {
		return ref, nil
	}
	ref, err := b.x.strings.put([]byte(model))
	if err != nil {
		return strRef{}, err
	}
	b.models[model] = ref
	return ref, nil
}
