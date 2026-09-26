package transcriptindex

import (
	"errors"
	"maps"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// errRebuild reports an entry the index cannot apply incrementally: a tool
// result with no name of its own, for a call the open turn does not know, or
// a deferred communicate result whose CommRawArgs/LastAssistantText state
// might live in the previously indexed (unscanned) prefix. The whole-file
// projection resolves these from every entry seen before it, which only a
// full build has at hand.
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

// builder applies entries, in file order, to the index's records. Its state
// is the open turn: the grouping state, the open turn's summary and call
// items, and the tool names the open turn registered. A full build also keeps
// every tool name registered so far (global), as the whole-file projection
// does; an index that only extends does without it (see errRebuild).
//
// commCalls and the lastAssistant* fields mirror ProjectTurn's
// ToolCallRegistry.CommRawArgs/LastAssistantText/LastAssistantTurnID: unlike
// names/calls, they are NOT reset at a new open turn, because a communicate
// call's deferred state legitimately crosses logical-turn/group boundaries
// (e.g. a standalone HOOK_COMPLETED closing the assistant's group before its
// result arrives) — matching the full read's single registry, threaded for
// the whole scan. Only the position each is at, not the projected item
// content, gets persisted (as an itemRecord's Context): the reader replays
// the referenced raw entries to reconstruct the values, so build time only
// needs enough to decide the affected items' structure (existence/parts).
type builder struct {
	x        *Index
	grouper  apptranscript.TurnGrouper
	turnSlot uint64
	turn     turnRecord
	calls    map[string]uint64 // open turn: call id -> item slot
	names    map[string]toolName
	global   map[string]string

	commCalls           map[string]commState // call id -> its deferred communicate state
	lastAssistantText   string
	lastAssistantTurnID string
	lastAssistantPos    contributor
	lastAssistantKnown  bool // true once the sticky value above is authoritative
}

// apply indexes the entry at ordinal, whose line is length bytes at offset.
func (b *builder) apply(ordinal uint64, offset int64, length uint32, entry *schema.Turn) error {
	entryIndex := int(ordinal) + 1
	version := ordinal + 1
	turnID, newTurn := b.grouper.Place(entry, entryIndex)
	if newTurn {
		if err := b.openTurn(turnID, ordinal, offset); err != nil {
			return err
		}
	}
	seed, err := b.seed(entry)
	if err != nil {
		return err
	}
	base := contributor{Offset: offset, Ordinal: ordinal, Length: length}
	reg := &apptranscript.ToolCallRegistry{
		Names:               maps.Clone(seed),
		CommRawArgs:         b.commRawArgsSnapshot(),
		LastAssistantText:   b.lastAssistantText,
		LastAssistantTurnID: b.lastAssistantTurnID,
	}
	items, parts := apptranscript.ProjectTurnParts(turnID, entryIndex, *entry, reg, nil, apptranscript.ToolResultOutputImages)
	b.recordNames(entry, seed)
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
		record := itemRecord{Entry: version, Part: uint32(parts[i]), Turn: uint32(b.turnSlot), Version: version, Opener: c}
		if merges {
			if record.Call, err = b.x.strings.put([]byte(item.CallID)); err != nil {
				return err
			}
		}
		if callID, ok := communicateResultCallID(entry, parts[i], seed); ok {
			if record.Context, err = b.communicateContext(callID); err != nil {
				return err
			}
		}
		slot, err := b.x.items.append(encodeItem(record))
		if err != nil {
			return err
		}
		if merges {
			b.calls[item.CallID] = slot
		}
	}
	b.recordCommState(entry, turnID, seed, base)
	return b.stampTurn(entry, version, offset, length)
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
// state is never reset at a group boundary (see the builder doc comment).
// Called after the entry's items are recorded, so communicateContext above
// still sees the state as it stood when this entry was projected.
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
		// value, and only the entry's own trailing text (not a healed
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

func (b *builder) openTurn(turnID string, ordinal uint64, offset int64) error {
	id, err := b.x.strings.put([]byte(turnID))
	if err != nil {
		return err
	}
	b.turn = turnRecord{ID: id, FirstOffset: offset, FirstOrdinal: ordinal}
	b.turnSlot, err = b.x.turns.append(encodeTurn(b.turn))
	if err != nil {
		return err
	}
	b.calls = map[string]uint64{}
	b.names = map[string]toolName{}
	return nil
}

// seed resolves, for each tool result in the entry that carries no name, the
// name the whole-file projection's tool-name map holds for its call. It also
// guards a communicate result's deferred state the same way: known globally
// during a full build, but only within the current scan's own local state
// during an incremental extend — an extend that cannot answer either
// authoritatively needs a full rebuild instead of silently misprojecting.
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

// stampTurn folds the entry into the open turn's summary, the way
// apptranscript.StampGroupedTurn folds a group's entries.
func (b *builder) stampTurn(entry *schema.Turn, version uint64, offset int64, length uint32) error {
	r := &b.turn
	// The turn's first entry creates its summary; every later one rewrites
	// it, and logs that it did. The log record is redone even when a crashed
	// extension already applied the entry: the truncation before this
	// extension removed its log record, and readers take each slot once.
	if version > r.FirstOrdinal+1 {
		if err := b.logUpdate(updatedTurn, b.turnSlot, offset); err != nil {
			return err
		}
	}
	if version <= r.Version {
		return nil // already accounted before an interrupted catch-up
	}
	if entry.Kind == schema.TurnFailure {
		r.LifecycleOffset, r.LifecycleLength, r.Status = offset, length, statusFailed
	}
	if entry.Kind == schema.TurnSteering && entry.SteeringKind == events.SteeringKindInterrupted && r.Status != statusFailed {
		r.Status = statusInterrupted
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
	r.Version = version
	return b.x.turns.write(b.turnSlot, encodeTurn(*r))
}
