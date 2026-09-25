package transcriptindex

import (
	"errors"
	"maps"
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
}

func newBuilder(x *Index) builder {
	return builder{x: x, calls: map[string]uint64{}, names: map[string]toolName{}, open: map[string]uint64{}, models: map[string]strRef{}}
}

// apply indexes the entry at ordinal, whose line is length bytes at offset.
func (b *builder) apply(ordinal uint64, offset int64, length uint32, entry *schema.Turn) error {
	if entry.Format == schema.TurnFormatIdentity {
		return b.applyIdentity(ordinal, offset, length, entry)
	}
	return b.applyLegacy(ordinal, offset, length, entry)
}

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
	items, parts := apptranscript.ProjectEntryParts(turnID, entryIndex, *entry, maps.Clone(seed), nil, apptranscript.ToolResultOutputImages)
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
		slot, err := b.appendItem(item, parts[i], b.turnSlot, c)
		if err != nil {
			return err
		}
		if merges {
			b.calls[item.CallID] = slot
		}
	}
	return b.stampTurn(b.turnSlot, entry, ordinal, offset, length)
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

// appendItems projects a new-format entry and appends an item record for each
// item absorb (when set) does not take. seed names the entry's nameless tool
// results.
func (b *builder) appendItems(slot uint64, entry *schema.Turn, c contributor, seed map[string]string, absorb func(appwire.ThreadItem) (bool, error)) error {
	items, parts := apptranscript.ProjectEntryParts(entry.TurnID, int(c.Ordinal)+1, *entry, seed, nil, apptranscript.ToolResultOutputImages)
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
	version := c.Ordinal + 1
	record := itemRecord{Entry: version, Part: uint32(part), Turn: uint32(turnSlot), Version: version, Opener: c}
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
