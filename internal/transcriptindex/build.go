package transcriptindex

import (
	"errors"
	"maps"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// errRebuild reports an entry the index cannot apply incrementally: a tool
// result with no name of its own, for a call the open turn does not know. The
// whole-file projection names it from every call seen before it, which only
// a full build has at hand.
var errRebuild = errors.New("transcript index needs a full rebuild")

// toolName is the projection's tool-name state for one call id: the name its
// latest tool call registered, or deleted after a communicate result.
type toolName struct {
	name    string
	present bool
}

// builder applies entries, in file order, to the index's records. Its state
// is the open turn: the grouping state, the open turn's summary and call
// items, and the tool names the open turn registered. A full build also keeps
// every tool name registered so far (global), as the whole-file projection
// does; an index that only extends does without it (see errRebuild).
type builder struct {
	x        *Index
	grouper  apptranscript.TurnGrouper
	turnSlot uint64
	turn     turnRecord
	calls    map[string]uint64 // open turn: call id -> item slot
	names    map[string]toolName
	global   map[string]string
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
	items, parts := apptranscript.ProjectTurnParts(turnID, entryIndex, *entry, maps.Clone(seed), nil, apptranscript.ToolResultOutputImages)
	b.recordNames(entry, seed)
	for i, item := range items {
		c := contributor{Offset: offset, Ordinal: ordinal, Length: length}
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
		slot, err := b.x.items.append(encodeItem(record))
		if err != nil {
			return err
		}
		if merges {
			b.calls[item.CallID] = slot
		}
	}
	return b.stampTurn(entry, version, offset, length)
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
// name the whole-file projection's tool-name map holds for its call.
func (b *builder) seed(entry *schema.Turn) (map[string]string, error) {
	if entry.Kind != schema.TurnTool && entry.Kind != schema.TurnToolResults {
		return nil, nil
	}
	seed := map[string]string{}
	for _, part := range entry.Message.Content {
		if part.Kind != llm.ContentToolResult || part.ToolResult == nil || part.ToolResult.Name != "" {
			continue
		}
		id := part.ToolResult.ToolCallID
		name, known := b.lookup(id)
		if !known {
			return nil, errRebuild
		}
		if name.present {
			seed[id] = name.name
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
		return nil // this entry already contributed
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
	if version <= r.Version {
		return nil // already accounted before an interrupted catch-up
	}
	// The turn's first entry creates its summary; every later one rewrites it.
	if r.Version != 0 {
		if err := b.logUpdate(updatedTurn, b.turnSlot, offset); err != nil {
			return err
		}
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
