package apptranscript

import (
	"primeradiant.com/evener/agent/events"
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
//   - USER_INPUT and typed goal-continuation STEERING open a logical turn.
//     Ordinary STEERING follows its explicit OwningTurnID when present;
//     otherwise it joins the open turn because live steering attaches there.
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
// Turn ids are exact for client-mutation and goal turns (the opener's persisted
// StableTurnID or its entry-index fallback) and stable-but-not-live-identical
// for other daemon-minted turns.

// opensLogicalTurn distinguishes top-level goal input from ordinary steering.
func opensLogicalTurn(kind schema.TurnKind, goalContinuation bool) bool {
	return kind == schema.TurnUserInput || kind == schema.TurnSteering && goalContinuation
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

// groupedTurn is one logical turn: the group's first entry's identity plus
// every projected item of the group, in arrival order, before call-id merge.
type groupedTurn struct {
	// turnID is the logical turn's id: the group's first entry's
	// persistedTurnID.
	turnID string
	// stamped is true when the group was opened by an identity-format
	// entry's own TurnID rather than by the legacy kind-and-owner rules.
	stamped bool
	// entries are the group's decoded entries in arrival order, including
	// entries whose projector emitted no items (they still carry stamps).
	entries []schema.Turn
	// items are the group's projected items in arrival order, before merge.
	items []appwire.ThreadItem
	// itemEntries is each item's awaiting-entry key, parallel to items. In a
	// stamped turn the AWAITING assistant entry is the call-identity boundary:
	// the index settles a result only against a call of the turn's awaiting
	// ASSISTANT entry (build.go's applyResults), so a provider that reuses a
	// call id in a later round is two calls there and must be two here. The key
	// is the awaiting entry's global index, so it stays the same across runs of
	// one turn: a delivery that splits the turn must not lose the batch.
	itemEntries []int
	// itemAnnounce is true for an item that came from the turn's awaiting
	// ASSISTANT entry, i.e. an announcement rather than a result. An
	// announcement never merges into an earlier one: the index keeps every
	// recorded part and settles a result against the FIRST part carrying the
	// call id (#1166 review).
	itemAnnounce []bool
}

// logicalTurnAccumulator buffers entries across a scan and emits grouped
// logical turns. It is the two-phase engine of the full read
// (ItemTurnsFromFile/ItemTurnsFromEntries): entries are appended in file order,
// then groupedAppTurns produces the logical turns. The bounded index build
// mirrors this state machine over persisted records
// (TurnKind/GroupItems/GroupCalls) so both paths agree by construction.
type logicalTurnAccumulator struct {
	turns   []groupedTurn
	grouper TurnGrouper
	// awaiting maps a stamped turn id to the global index of the ASSISTANT
	// entry whose calls are awaiting results. It lives on the accumulator, not
	// on a group, because a cold writer can put a delivery between an
	// announcement and its result: the turn then has two runs, and the second
	// must settle against the batch the first announced (#1166 review).
	awaiting map[string]int
}

// TurnGrouper applies the logical-turn grouping rule to entries in file order.
// Its zero value is the state before the first entry. Open and TurnID are the
// whole state, so a caller that persists them can resume grouping later.
type TurnGrouper struct {
	// Open reports whether continuations join the latest group.
	Open bool
	// TurnID is the latest group's turn id.
	TurnID string
}

// Place reports the turn the entry belongs to and whether it starts a new
// logical turn. entryIndex is the entry's 1-based index (its ordinal + 1).
func (g *TurnGrouper) Place(entry *schema.Turn, entryIndex int) (turnID string, newTurn bool) {
	if entry.Format == schema.TurnFormatIdentity && entry.TurnID != "" {
		// A stamped entry joins the turn the writer named, and the id is the
		// whole join key — matching the index, whose summaries are keyed by
		// id across both formats. Entries of one turn are contiguous in the
		// file: the writer closes a turn with a completion entry, so the
		// next stamped entry carries a different id and opens the next
		// group. Both closing kinds (TURN_COMPLETION, TURN_REOPEN) are
		// transcript-only, so they never reach here and never need to.
		//
		// Without this branch a stamped entry fell through to the kind rules
		// below, which invent a turn id from the entry's ordinal instead of
		// using the owner the writer recorded. A session-start hook
		// completing before any turn is named then opened a group of its own
		// while the index kept it in the turn its stamp names, so the two
		// readers disagreed about a turn boundary on the same transcript
		// (#1166).
		if g.TurnID == entry.TurnID {
			g.Open = false
			return g.TurnID, false
		}
		g.TurnID, g.Open = entry.TurnID, false
		return g.TurnID, true
	}
	kind, owner := entry.Kind, entry.OwningTurnID
	switch {
	case opensLogicalTurn(kind, entry.GoalContinuation != nil):
		g.TurnID, g.Open = persistedTurnID(*entry, entryIndex), true
		return g.TurnID, true
	case kind == schema.TurnSteering && owner != "":
		if g.Open && g.TurnID == owner {
			return g.TurnID, false
		}
		g.TurnID, g.Open = owner, true
		return g.TurnID, true
	case continuesLogicalTurn(kind) && g.Open:
		return g.TurnID, false
	default:
		// Standalone kind, or a continuation with no open group: its own
		// group. A standalone closes it; a stray continuation stays open for
		// later continuations.
		g.TurnID, g.Open = persistedTurnID(*entry, entryIndex), continuesLogicalTurn(kind)
		return g.TurnID, true
	}
}

// appendEntry buffers one scanned entry with its projected items (which may
// be empty: the entry still carries failure/usage/timestamp stamps into its
// group).
func (a *logicalTurnAccumulator) appendEntry(entry schema.Turn, entryIndex int, items []appwire.ThreadItem) {
	if turnID, newTurn := a.grouper.Place(&entry, entryIndex); newTurn {
		a.turns = append(a.turns, groupedTurn{turnID: turnID, stamped: entry.Format == schema.TurnFormatIdentity && entry.TurnID != ""})
	}
	last := &a.turns[len(a.turns)-1]
	last.entries = append(last.entries, entry)
	// A stamped assistant entry that records tool calls becomes its TURN's
	// awaiting entry: its own items and every following result entry's items
	// key to it, matching the index, which settles against the turn's awaiting
	// assistant entry alone. Keyed by turn id so a delivery that splits the
	// turn does not end the batch.
	if entry.Format == schema.TurnFormatIdentity && hasRecordedToolCall(&entry) {
		if a.awaiting == nil {
			a.awaiting = map[string]int{}
		}
		a.awaiting[last.turnID] = entryIndex
	}
	awaiting := a.awaiting[last.turnID]
	if len(items) > 0 {
		last.items = append(last.items, items...)
		for range items {
			if awaiting != 0 {
				last.itemEntries = append(last.itemEntries, awaiting)
			} else {
				last.itemEntries = append(last.itemEntries, entryIndex)
			}
			last.itemAnnounce = append(last.itemAnnounce, awaiting != 0 && entryIndex == awaiting)
		}
	}
}

// appendProjectedEntry projects one entry through the EntryProjector under
// its logical-turn id (the group's turn id, matching the bounded read's
// group.turnID and the live projector's activeTurnID) and buffers the
// result for grouping. Using the group's turn id — not the per-entry
// persistedTurnID — keeps LastAssistantTurnID consistent across entries
// in the same logical turn, so the healed-communicate echo check scopes
// correctly on the full read (where per-entry IDs differ within a group).
func appendProjectedEntry(acc *logicalTurnAccumulator, project EntryProjector, turn schema.Turn, entryIndex int) {
	if turn.Kind.TranscriptOnly() {
		// Written for the history projection phase 3 introduces. Today's
		// projection passes over it: it joins no group, closes none, and
		// projects nothing, though its line still takes an entry index.
		return
	}
	if turn.OriginalOrdinal != nil {
		// A compaction fold copy is model history for resume; its original
		// already projects and already contributes its usage, so buffering the
		// copy would double a turn's usage (and a session's derived total). It
		// joins no group, matching the reference projector and the index.
		return
	}
	// Project under the id of the group appendEntry will buffer the entry
	// into: the same grouping decision, made on a copy so appendEntry still
	// places the entry itself. A steering opener takes its own group's id,
	// not the previously open group's (#2432).
	probe := acc.grouper
	turnID, _ := probe.Place(&turn, entryIndex)
	var items []appwire.ThreadItem
	if project != nil {
		items = project(turn, turnID, entryIndex)
	}
	acc.appendEntry(turn, entryIndex, items)
}

// groupedAppTurnProjection flushes the accumulator into AppWire turns under
// the logical-turn ordinals: with no prelude the first group is entry ordinal
// 0; a prelude turn occupies ordinal 0 and shifts every group by one. Every
// group consumes an ordinal even when it emits no visible turn, matching the
// live snapshot's turn-start allocation.
//
// Items get their Position/TranscriptKey here — the full item read, the
// bounded readers, and the item-window path must all agree on these keys (PR
// #822 F3).
// turnExecution is one stamped turn across every run of its id: what the
// bounded index keeps as one summary. Replay renders one turn per visible run,
// but every field it computes — item order, call settlement, usage and the
// continuation flags — is a property of the EXECUTION, not of a run. A cold
// writer can put a delivery between two runs of one execution, and treating
// the runs independently is what produced six rounds of findings in different
// fields (#1166 review).
type turnExecution struct {
	turnID string
	// entries are every run's entries in file order.
	entries []schema.Turn
	// items are every run's projected items in file order, before settlement.
	items        []appwire.ThreadItem
	itemEntries  []int
	itemAnnounce []bool
	// runOfItem is the run each item came from, parallel to items.
	runOfItem []int
	// runs are the group indexes of this execution's visible runs, in order.
	runs []int

	settled   bool
	merged    []appwire.ThreadItem
	mergedRun []int
}

// settle folds the whole execution's items once. A result that arrives in a
// later run settles into the announcement's own item, which stays in the run
// that announced it — the index's rule: one settled item at the announcement.
func (ex *turnExecution) settle() {
	if ex.settled {
		return
	}
	ex.settled = true
	ex.merged, ex.mergedRun = mergeExecutionItems(ex.items, ex.itemEntries, ex.itemAnnounce, ex.runOfItem)
}

// runIndex reports the position of group index i among the execution's runs.
func (ex *turnExecution) runIndex(i int) int {
	for run, group := range ex.runs {
		if group == i {
			return run
		}
	}
	return 0
}

// itemsOfRun returns the execution's settled items that belong to one run.
func (ex *turnExecution) itemsOfRun(run int) []appwire.ThreadItem {
	var items []appwire.ThreadItem
	for at := range ex.merged {
		if ex.mergedRun[at] == run {
			items = append(items, ex.merged[at])
		}
	}
	return items
}

// groupExecutions collects every run of each stamped turn id into one
// execution.
func groupExecutions(groups []groupedTurn) map[string]*turnExecution {
	execs := make(map[string]*turnExecution, len(groups))
	for i := range groups {
		if !groups[i].stamped {
			continue
		}
		ex := execs[groups[i].turnID]
		if ex == nil {
			ex = &turnExecution{turnID: groups[i].turnID}
			execs[groups[i].turnID] = ex
		}
		run := len(ex.runs)
		ex.runs = append(ex.runs, i)
		ex.entries = append(ex.entries, groups[i].entries...)
		for j := range groups[i].items {
			ex.items = append(ex.items, groups[i].items[j])
			ex.itemEntries = append(ex.itemEntries, groups[i].itemEntries[j])
			ex.itemAnnounce = append(ex.itemAnnounce, groups[i].itemAnnounce[j])
			ex.runOfItem = append(ex.runOfItem, run)
		}
	}
	return execs
}

// groupedAppTurnProjection flushes the accumulator into AppWire turns under
// the logical-turn ordinals: with no prelude the first group is entry ordinal
// 0; a prelude turn occupies ordinal 0 and shifts every group by one. Every
// group consumes an ordinal even when it emits no visible turn, matching the
// live snapshot's turn-start allocation.
//
// Items get their Position/TranscriptKey here — the full item read, the
// bounded readers, and the item-window path must all agree on these keys (PR
// #822 F3).
func groupedAppTurnProjection(acc *logicalTurnAccumulator, header transcript.Header) (ItemTurnProjection, error) {
	turns := []appwire.Turn{}
	entryOrdinal := uint64(0)
	var prelude *appwire.Turn
	if p := PreludeTurn(header); p != nil {
		positioned, err := positionPreludeItems(p.Items)
		if err != nil {
			return ItemTurnProjection{}, err
		}
		p.Items = positioned
		prelude = p
		entryOrdinal = 1
	}
	execs := groupExecutions(acc.turns)
	// The header turn keeps entry position 0, so it is emitted in file order:
	// a delivery recorded before the prelude's first stamped entry keeps its
	// own position ahead of it. Merging the prompt into a LATER prelude run
	// would emit position 0 after that delivery and flatten the chronology,
	// which item paging rejects (#1166 review).
	preludeEmitted := false
	emittedAny := false
	emitPrelude := func() {
		if prelude == nil || preludeEmitted {
			return
		}
		turns = append(turns, *prelude)
		preludeEmitted = true
	}
	for i := range acc.turns {
		group := &acc.turns[i]
		groupOrdinal := entryOrdinal
		entryOrdinal++
		var items []appwire.ThreadItem
		var run, runs int
		if ex := execs[group.turnID]; ex != nil {
			ex.settle()
			run = ex.runIndex(i)
			runs = len(ex.runs)
			items = ex.itemsOfRun(run)
		} else {
			items = mergeGroupedItems(group.items, group.itemEntries, group.itemAnnounce, false)
		}
		if len(items) == 0 {
			continue
		}
		// Every item of the logical turn carries the turn's id — the
		// per-entry EntryProjector contract let a continuation entry answer
		// under its own fallback id, which the live model never does.
		for j := range items {
			items[j].TurnID = group.turnID
		}
		positioned, err := positionProjectedItemsAt(items, group.turnID, groupOrdinal)
		if err != nil {
			return ItemTurnProjection{}, err
		}
		turn := appwire.Turn{ID: group.turnID, Items: positioned, ItemsView: appwire.TurnItemsViewFull, Status: appwire.TurnStatusCompleted}
		if ex := execs[group.turnID]; ex != nil {
			// Usage and terminal stamps are the execution's, folded once from
			// every run's entries, so neither fragment understates the turn.
			StampGroupedTurn(&turn, ex.entries)
		} else {
			StampGroupedTurn(&turn, group.entries)
		}
		if prelude != nil && !preludeEmitted {
			// The header's items and a LEADING prelude run are ONE turn: the
			// writer stamps a fresh transcript's pre-execution entries with the
			// same id the header content is projected under. They merge only
			// when the prelude run is the first thing the read emits; otherwise
			// the prompt stands alone at position 0 and the prelude run stays
			// its own fragment.
			if group.turnID == appwire.SystemPreludeTurnID && !emittedAny {
				merged := make([]appwire.ThreadItem, 0, len(prelude.Items)+len(turn.Items))
				merged = append(merged, prelude.Items...)
				turn.Items = append(merged, turn.Items...)
				preludeEmitted = true
			} else {
				emitPrelude()
			}
		}
		// Every visible run of a split stamped turn is a FRAGMENT, with the
		// continuation flags derived from whether a run precedes or follows it
		// in the SAME execution. A client reads these to decide whether to page
		// further, so a run that omits them advertises completeness it does not
		// have (#1166 review). An independent legacy group keeps "full".
		if runs > 1 {
			turn.ItemsView = appwire.TurnItemsViewFragment
			turn.HasEarlierItems = run > 0
			turn.HasLaterItems = run < runs-1
		}
		turns = append(turns, turn)
		emittedAny = true
	}
	emitPrelude()
	return ItemTurnProjection{Turns: turns, NextEntry: entryOrdinal}, nil
}

// StampGroupedTurn applies a group's terminal stamps: failure
// status from any TURN_FAILURE entry in the group, the earliest timestamp as
// the turn's start, and usage summed across the group's entries (the live
// projector accumulates a turn's usage the same way).
func StampGroupedTurn(turn *appwire.Turn, entries []schema.Turn) {
	var startedAt *int64
	var usage llm.Usage
	interrupted := false
	for _, entry := range entries {
		StampTurnFailure(turn, entry)
		if entry.Kind == schema.TurnSteering && entry.SteeringKind == events.SteeringKindInterrupted {
			interrupted = true
		}
		if startedAt == nil && !entry.Timestamp.IsZero() {
			ms := entry.Timestamp.UnixMilli()
			startedAt = &ms
		}
		usage = usage.Add(entry.Usage)
	}
	turn.StartedAt = startedAt
	if interrupted && turn.Status != appwire.TurnStatusFailed {
		turn.Status = appwire.TurnStatusInterrupted
	}
	if u := appwire.EvenerUsageFromLLM(usage); u != nil {
		turn.Usage = u
	}
}

// MergesByCallID reports whether a logical turn folds item with its later
// items of the same call: every commandExecution item that carries a call id.
func MergesByCallID(item appwire.ThreadItem) bool {
	return item.Type == "commandExecution" && item.CallID != ""
}

// hasRecordedToolCall reports whether the entry's own content records a tool
// call. Such an entry is what the index calls the turn's AWAITING entry: its
// calls are the only ones a later result can settle, including calls that
// project no item because the provider hid them.
func hasRecordedToolCall(turn *schema.Turn) bool {
	for _, part := range turn.Message.Content {
		if part.Kind == llm.ContentToolCall && part.ToolCall != nil {
			return true
		}
	}
	return false
}

// mergeGroupedItems folds a group's items into one item per call id: a later
// commandExecution item whose CallID was already introduced merges into the
// earlier one (the file counterpart of the live snapshot's call/result
// upsert). Ordinals are assigned AFTER merging, across the whole logical
// turn; items that do not merge keep arrival order.
//
// A STAMPED turn scopes the merge to the announcing entry: the index settles a
// result only against a call of the turn's awaiting ASSISTANT entry
// (build.go's applyResults), so one entry's calls are the whole settlement
// batch. A provider that reuses a call id in a later round of the same
// execution is therefore two calls — each keeps its own arguments and its own
// result — and merging them by call id alone would overwrite the first call's
// record with the second's. A legacy group keeps the call-id-wide rule it has
// always had.
func mergeGroupedItems(items []appwire.ThreadItem, itemEntries []int, itemAnnounce []bool, stamped bool) []appwire.ThreadItem {
	merged := make([]appwire.ThreadItem, 0, len(items))
	// callIndex is keyed by call id, and for a stamped turn also by the
	// awaiting entry that announced it, so the same id from a later round
	// opens an item instead of merging into the earlier round's.
	type callKey struct {
		entry int
		call  string
	}
	callIndex := map[callKey]int{}
	for at, item := range items {
		if MergesByCallID(item) {
			entry := 0
			if stamped && at < len(itemEntries) {
				entry = itemEntries[at]
			}
			key := callKey{entry: entry, call: item.CallID}
			if stamped && at < len(itemAnnounce) && itemAnnounce[at] {
				// An announcement is never a merge target: the index keeps
				// every recorded part and settles a result against the FIRST
				// part carrying the call id. Merging a second announcement into
				// the first would replace the earlier call's arguments before
				// either result was applied (#1166 review).
				callIndex[key] = len(merged)
				merged = append(merged, item)
				continue
			}
			if prev, ok := callIndex[key]; ok {
				merged[prev] = MergeThreadItems(merged[prev], item)
				continue
			}
			callIndex[key] = len(merged)
		}
		merged = append(merged, item)
	}
	return merged
}

// mergeExecutionItems folds one execution's items, across every run of its
// turn id, and reports which run each surviving item belongs to.
//
// The rules are the index's, applied to the execution rather than to a run:
//
//   - a call id identifies exactly one announcement, and an announcement never
//     merges into an earlier one (every recorded part is kept);
//   - a result settles into the announcement its AWAITING assistant entry
//     recorded, wherever in the execution that announcement sits, so a
//     delivery that splits the turn cannot orphan the result;
//   - a result whose call the execution never announced projects on its own.
func mergeExecutionItems(items []appwire.ThreadItem, itemEntries []int, itemAnnounce []bool, runOfItem []int) ([]appwire.ThreadItem, []int) {
	merged := make([]appwire.ThreadItem, 0, len(items))
	mergedRun := make([]int, 0, len(items))
	// callIndex keys an announcement by its awaiting entry and call id, which
	// is how the index decides what a result settles.
	type callKey struct {
		entry int
		call  string
	}
	callIndex := map[callKey]int{}
	for at, item := range items {
		run := 0
		if at < len(runOfItem) {
			run = runOfItem[at]
		}
		entry := 0
		if at < len(itemEntries) {
			entry = itemEntries[at]
		}
		if MergesByCallID(item) {
			key := callKey{entry: entry, call: item.CallID}
			if at < len(itemAnnounce) && itemAnnounce[at] {
				callIndex[key] = len(merged)
				merged = append(merged, item)
				mergedRun = append(mergedRun, run)
				continue
			}
			if prev, ok := callIndex[key]; ok {
				merged[prev] = MergeThreadItems(merged[prev], item)
				continue
			}
			callIndex[key] = len(merged)
		}
		merged = append(merged, item)
		mergedRun = append(mergedRun, run)
	}
	return merged, mergedRun
}

// MergeThreadItems merges a later projected item into an earlier one of
// the same call: absent fields fall back to the earlier item's values. This
// mirrors server mergeAppThreadItem's field precedence; apptranscript cannot
// import server, so the merge is local.
func MergeThreadItems(existing, incoming appwire.ThreadItem) appwire.ThreadItem {
	if incoming.Type == "" {
		incoming.Type = existing.Type
	}
	if incoming.TurnID == "" {
		incoming.TurnID = existing.TurnID
	}
	if incoming.Authorship == nil {
		incoming.Authorship = existing.Authorship
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
	if incoming.RoundID == "" {
		// A settled result carries no round of its own — the round names the
		// assistant entry that announced the call, and the result entry only
		// names the call. Without this fallback the merge drops the round the
		// announcement recorded, so the settled item loses the round a client
		// groups by (#1166 review).
		incoming.RoundID = existing.RoundID
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
	// Output and input images each have one rule: see appwire.MergeOutputImages
	// and appwire.MergeInputImages.
	incoming.OutputImages = appwire.MergeOutputImages(existing.OutputImages, incoming.OutputImages)
	incoming.Images = appwire.MergeInputImages(existing.Images, incoming.Images)
	if incoming.Text == "" {
		incoming.Text = existing.Text
	}
	if incoming.ID == "" {
		incoming.ID = existing.ID
	}
	if incoming.ConversationProvenance == nil {
		incoming.ConversationProvenance = existing.ConversationProvenance
	}
	if incoming.ConversationProvenance != nil {
		incoming.ConversationProvenance = appwire.ClonePointer(incoming.ConversationProvenance)
		incoming.ConversationProvenance.SourceItemID = incoming.ID
	}
	return incoming
}
