package apptranscript

import (
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// Logical-turn grouping.
//
// The live snapshot allocates ONE entry ordinal per logical turn and numbers
// that turn's items across the whole turn (appTurnSnapshot semantics), but a
// session persists each logical turn as SEVERAL transcript entries: the user
// input, then assistant, tool-call, and tool-result entries as the turn runs.
// Re-projecting those entries one-per-turn drifts the file keys away from the
// live keys — a live-rendered item then changes identity on reload and the
// frontend, which reconciles by transcriptKey, duplicates it (PR #822 F3).
//
// The grouping rule reproduces the live allocation from the file alone:
//
//   - USER_INPUT opens a logical turn. STEERING joins that open turn because
//     live steering is attached to the active turn; only a steer with no open
//     group starts a group of its own.
//   - CONTINUATIONS extend the open logical turn: ASSISTANT, TOOL,
//     TOOL_RESULTS, and TURN_FAILURE (a failure closes nothing — the daemon
//     may retry after a failure, and grouping it into the opener's turn keeps
//     StampTurnFailure applied to that turn). A continuation with no open
//     group (a transcript that starts mid-turn) starts its own group.
//   - Every other kind (SYSTEM, ENVIRONMENT, CHECKPOINT, SUMMARY,
//     MODEL_SWITCH, HOOK_COMPLETED, ATTENTION_RESOLUTION) is its own logical
//     turn, grouped with nothing, and CLOSES the open group. This matches live
//     gap-turn semantics.
//
// Turn ids are exact for client-mutation turns (the opener's persisted
// StableTurnID or its entry-index fallback) and stable-but-not-live-identical
// for daemon-minted/continuation turns.

// opensLogicalTurn reports whether a turn kind starts a new logical turn.
func opensLogicalTurn(kind schema.TurnKind) bool {
	return kind == schema.TurnUserInput
}

// continuesLogicalTurn reports whether a turn kind extends the open logical
// turn (the entry the group's opener started).
func continuesLogicalTurn(kind schema.TurnKind) bool {
	switch kind {
	case schema.TurnAssistant, schema.TurnTool, schema.TurnToolResults, schema.TurnFailure, schema.TurnSteering:
		return true
	default:
		return false
	}
}

// groupOpenAfter reports whether the logical-turn group is open for
// continuations once this kind has been appended: openers and continuations
// leave a group open; standalone kinds close it.
func groupOpenAfter(kind schema.TurnKind) bool {
	return opensLogicalTurn(kind) || continuesLogicalTurn(kind)
}

// recordStartsGroup reports whether a record of this kind starts a new
// logical group given the kind of the record immediately before it ("" when
// there is none). Openers always start a group; continuations join the open
// group (start one only when the previous record closed it); standalone kinds
// always start — and close — their own group.
func recordStartsGroup(kind, prevKind schema.TurnKind) bool {
	if opensLogicalTurn(kind) {
		return true
	}
	if continuesLogicalTurn(kind) {
		return !groupOpenAfter(prevKind)
	}
	return true
}

// groupedTurn is one logical turn: the group's first entry's identity plus
// every projected item of the group, in arrival order, before call-id merge.
type groupedTurn struct {
	// turnID is the logical turn's id: the group's first entry's
	// persistedTurnID.
	turnID string
	// entries are the group's decoded entries in arrival order, including
	// entries whose projector emitted no items (they still carry stamps).
	entries []schema.Turn
	// items are the group's projected items in arrival order, before merge.
	items []appwire.ThreadItem
}

// logicalTurnAccumulator buffers entries across a scan and emits grouped
// logical turns. It is the two-phase engine of the full read
// (ItemTurnsFromFile/ItemTurnsFromEntries): entries are appended in file order,
// then groupedAppTurns produces the logical turns. The bounded index build
// mirrors this state machine over persisted records
// (TurnKind/GroupItems/GroupCalls) so both paths agree by construction.
type logicalTurnAccumulator struct {
	turns []groupedTurn
	open  bool
}

// appendEntry buffers one scanned entry with its projected items (which may
// be empty: the entry still carries failure/usage/timestamp stamps into its
// group).
func (a *logicalTurnAccumulator) appendEntry(entry schema.Turn, entryIndex int, items []appwire.ThreadItem) {
	kind := entry.Kind
	switch {
	case opensLogicalTurn(kind):
		a.turns = append(a.turns, groupedTurn{turnID: persistedTurnID(entry, entryIndex)})
		a.open = true
	case continuesLogicalTurn(kind) && a.open && len(a.turns) > 0:
		// Join the open group.
	default:
		// Standalone kind, or a continuation with no open group: its own
		// group. A standalone closes it; a stray continuation stays open for
		// later continuations.
		a.turns = append(a.turns, groupedTurn{turnID: persistedTurnID(entry, entryIndex)})
		a.open = continuesLogicalTurn(kind)
	}
	last := &a.turns[len(a.turns)-1]
	last.entries = append(last.entries, entry)
	if len(items) > 0 {
		last.items = append(last.items, items...)
	}
}

// appendProjectedEntry projects one entry through the EntryProjector under
// its per-entry turn id (the per-entry contract unchanged) and buffers the
// result for grouping.
func appendProjectedEntry(acc *logicalTurnAccumulator, project EntryProjector, turn schema.Turn, entryIndex int) {
	turnID := persistedTurnID(turn, entryIndex)
	var items []appwire.ThreadItem
	if project != nil {
		items = project(turn, turnID, entryIndex)
	}
	acc.appendEntry(turn, entryIndex, items)
}

// groupedAppTurns flushes the accumulator into AppWire turns under the
// logical-turn ordinals: with no prelude the first emitted group is entry
// ordinal 0; a prelude turn occupies ordinal 0 and shifts every group by one.
// Groups whose merged items are empty emit no turn and consume no ordinal.
// Items get their Position/TranscriptKey here — the full item read, the
// bounded readers, and the item-window path must all agree on these keys (PR
// #822 F3).
func groupedAppTurns(acc *logicalTurnAccumulator, header transcript.Header) ([]appwire.Turn, error) {
	turns := []appwire.Turn{}
	entryOrdinal := uint64(0)
	if prelude := PreludeTurn(header); prelude != nil {
		positioned, err := positionPreludeItems(prelude.Items)
		if err != nil {
			return nil, err
		}
		prelude.Items = positioned
		turns = append(turns, *prelude)
		entryOrdinal = 1
	}
	for i := range acc.turns {
		group := &acc.turns[i]
		items := mergeGroupedItems(group.items)
		if len(items) == 0 {
			continue
		}
		// Every item of the logical turn carries the turn's id — the
		// per-entry EntryProjector contract let a continuation entry answer
		// under its own fallback id, which the live model never does.
		for j := range items {
			items[j].TurnID = group.turnID
		}
		positioned, err := positionProjectedItemsAt(items, group.turnID, entryOrdinal)
		if err != nil {
			return nil, err
		}
		turn := appwire.Turn{ID: group.turnID, Items: positioned, ItemsView: "full", Status: appwire.TurnStatusCompleted}
		stampGroupedTurnFromEntries(&turn, group.entries)
		turns = append(turns, turn)
		entryOrdinal++
	}
	return turns, nil
}

// stampGroupedTurnFromEntries applies a group's terminal stamps: failure
// status from any TURN_FAILURE entry in the group, the earliest timestamp as
// the turn's start, and usage summed across the group's entries (the live
// projector accumulates a turn's usage the same way).
func stampGroupedTurnFromEntries(turn *appwire.Turn, entries []schema.Turn) {
	var startedAt *int64
	var usage llm.Usage
	for _, entry := range entries {
		StampTurnFailure(turn, entry)
		if startedAt == nil && !entry.Timestamp.IsZero() {
			ms := entry.Timestamp.UnixMilli()
			startedAt = &ms
		}
		usage = usage.Add(entry.Usage)
	}
	turn.StartedAt = startedAt
	if u := appwire.EvenerUsageFromLLM(usage); u != nil {
		turn.Usage = u
	}
}

// mergeGroupedItems folds a group's items into one item per call id: a later
// commandExecution item whose CallID was already introduced merges into the
// earlier one (the file counterpart of the live snapshot's call/result
// upsert). Ordinals are assigned AFTER merging, across the whole logical
// turn; items that do not merge keep arrival order.
func mergeGroupedItems(items []appwire.ThreadItem) []appwire.ThreadItem {
	merged := make([]appwire.ThreadItem, 0, len(items))
	callIndex := map[string]int{}
	for _, item := range items {
		if item.Type == "commandExecution" && item.CallID != "" {
			if at, ok := callIndex[item.CallID]; ok {
				merged[at] = mergeAppThreadItems(merged[at], item)
				continue
			}
			callIndex[item.CallID] = len(merged)
		}
		merged = append(merged, item)
	}
	return merged
}

// mergedContribution is the counting form of mergeGroupedItems for the index
// scan: it reports how many of the entry's projected items survive merging
// into calls already in the set, and which new call ids the items introduce.
// calls is mutated to include the introduced ids.
func mergedContribution(items []appwire.ThreadItem, calls map[string]bool) (count int, introduced []string) {
	for _, item := range items {
		if item.Type == "commandExecution" && item.CallID != "" {
			if calls[item.CallID] {
				continue
			}
			calls[item.CallID] = true
			introduced = append(introduced, item.CallID)
		}
		count++
	}
	return count, introduced
}

// mergeAppThreadItems merges a later projected item into an earlier one of
// the same call: absent fields fall back to the earlier item's values. This
// mirrors server mergeAppThreadItem's field precedence; apptranscript cannot
// import server, so the merge is local.
func mergeAppThreadItems(existing, incoming appwire.ThreadItem) appwire.ThreadItem {
	if incoming.Type == "" {
		incoming.Type = existing.Type
	}
	if incoming.TurnID == "" {
		incoming.TurnID = existing.TurnID
	}
	if incoming.CallID == "" {
		incoming.CallID = existing.CallID
	}
	if incoming.ToolName == "" {
		incoming.ToolName = existing.ToolName
	}
	if incoming.ArgumentsJSON == "" {
		incoming.ArgumentsJSON = existing.ArgumentsJSON
	}
	if incoming.Description == "" {
		incoming.Description = existing.Description
	}
	if incoming.Output == "" {
		incoming.Output = existing.Output
	}
	if incoming.Error == "" {
		incoming.Error = existing.Error
	}
	if incoming.Status == "" {
		incoming.Status = existing.Status
	}
	if incoming.StartedAt == nil {
		incoming.StartedAt = existing.StartedAt
	}
	if incoming.CompletedAt == nil {
		incoming.CompletedAt = existing.CompletedAt
	}
	if len(incoming.Raw) == 0 {
		incoming.Raw = existing.Raw
	}
	if incoming.ExitCode == nil {
		incoming.ExitCode = existing.ExitCode
	}
	if len(incoming.OutputImages) == 0 {
		incoming.OutputImages = existing.OutputImages
	}
	if len(incoming.Images) == 0 {
		incoming.Images = existing.Images
	}
	if incoming.Text == "" {
		incoming.Text = existing.Text
	}
	if incoming.ID == "" {
		incoming.ID = existing.ID
	}
	return incoming
}
