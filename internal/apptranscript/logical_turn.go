package apptranscript

import (
	"sort"

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
	// entryIndexes is each entry's global 1-based index, parallel to entries,
	// so the walk can consult an entry by its position in the file.
	entryIndexes []int
	// items are the group's projected items in arrival order, before merge.
	items []appwire.ThreadItem
	// itemEntries is each item's originating entry's global index, parallel to
	// items. A result settles the call of the LATEST assistant entry that
	// announced it, which needs both the entry a call was announced in and the
	// entry a result was recorded in.
	itemEntries []int
}

// logicalTurnAccumulator buffers entries across a scan and emits grouped
// logical turns. It is the two-phase engine of the full read
// (ItemTurnsFromFile/itemTurnsFromEntries): entries are appended in file order,
// then groupedAppTurns produces the logical turns. The bounded index build
// mirrors this state machine over persisted records
// (TurnKind/GroupItems/GroupCalls) so both paths agree by construction.
type logicalTurnAccumulator struct {
	turns   []groupedTurn
	grouper TurnGrouper
	// stampedStatus holds, per stamped turn id, the status the file's
	// transcript-only completion and reopen records fold in file order, the
	// same fold the index makes: a completion sets the status, a reopen
	// clears it to in progress, and the last record wins. They join no group,
	// but the status belongs to the turn they name.
	stampedStatus map[string]string
}

// foldStampedStatus folds a transcript-only TURN_COMPLETION or TURN_REOPEN
// into its stamped turn's status, in file order. Only an identity-format
// record carries a turn id to fold the status into.
func (a *logicalTurnAccumulator) foldStampedStatus(turn *schema.Turn) {
	if turn.Format != schema.TurnFormatIdentity || turn.TurnID == "" {
		return
	}
	var status string
	switch turn.Kind {
	case schema.TurnCompletion:
		status = appwire.TurnStatusCompleted
		if turn.Completion != nil {
			switch turn.Completion.Status {
			case schema.TurnFailed:
				status = appwire.TurnStatusFailed
			case schema.TurnInterrupted:
				status = appwire.TurnStatusInterrupted
			}
		}
	case schema.TurnReopen:
		status = appwire.TurnStatusInProgress
	default:
		return
	}
	if a.stampedStatus == nil {
		a.stampedStatus = map[string]string{}
	}
	a.stampedStatus[turn.TurnID] = status
}

// TurnGrouper applies the logical-turn grouping rule to entries in file order.
// Its zero value is the state before the first entry.
//
// A matching stamp extends only the current stamped run, and projection
// associates a turn's separate runs by its id. The transcript index restores
// just Open and TurnID (index.go's Open/OpenTurnID) because it calls Place for
// legacy entries only.
type TurnGrouper struct {
	// Open reports whether continuations join the latest group.
	Open bool
	// TurnID is the latest group's turn id.
	TurnID string
	// Stamped reports whether the latest group was opened by an
	// identity-format entry. A stamped entry joins a group by its id, but ONLY
	// a group that is itself stamped: the index gives an identity entry its own
	// summary regardless of the legacy group before it (build.go's
	// applyIdentity resets the grouper), so an id shared with a legacy group
	// must not make the two one turn (#1166 review).
	Stamped bool
}

// Place reports the turn the entry belongs to and whether it starts a new
// logical turn. entryIndex is the entry's 1-based index (its ordinal + 1).
func (g *TurnGrouper) Place(entry *schema.Turn, entryIndex int) (turnID string, newTurn bool) {
	if entry.Format == schema.TurnFormatIdentity && entry.TurnID != "" {
		// A stamp extends the current run only if the run is stamped with
		// the same turn ID. Projection associates separate runs by turn ID.
		// The whole-file scanner skips TURN_REOPEN before grouping.
		if g.TurnID == entry.TurnID && g.Stamped {
			g.Open = false
			return g.TurnID, false
		}
		g.TurnID, g.Open, g.Stamped = entry.TurnID, false, true
		return g.TurnID, true
	}
	kind, owner := entry.Kind, entry.OwningTurnID
	switch {
	case opensLogicalTurn(kind, entry.GoalContinuation != nil):
		g.TurnID, g.Open, g.Stamped = persistedTurnID(*entry, entryIndex), true, false
		return g.TurnID, true
	case kind == schema.TurnSteering && owner != "":
		if g.Open && g.TurnID == owner {
			return g.TurnID, false
		}
		g.TurnID, g.Open, g.Stamped = owner, true, false
		return g.TurnID, true
	case continuesLogicalTurn(kind) && g.Open:
		return g.TurnID, false
	default:
		// Standalone kind, or a continuation with no open group: its own
		// group. A standalone closes it; a stray continuation stays open for
		// later continuations.
		g.TurnID, g.Open, g.Stamped = persistedTurnID(*entry, entryIndex), continuesLogicalTurn(kind), false
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
	last.entryIndexes = append(last.entryIndexes, entryIndex)
	if len(items) > 0 {
		last.items = append(last.items, items...)
		for range items {
			last.itemEntries = append(last.itemEntries, entryIndex)
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
		// projection passes over it as an item: it joins no group, closes
		// none, and projects nothing, though its line still takes an entry
		// index. A completion or reopen still names its turn's status.
		acc.foldStampedStatus(&turn)
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
	// runsByTurn are, per stamped turn id, the groups the file recorded under
	// it, in file order. A RUN is a window onto the turn, not a turn: a
	// standalone delivery can land between two runs of one execution, and both
	// the call-id merge and the turn's metadata belong to the turn, not to the
	// window (#1166 review).
	runsByTurn := map[string][]int{}
	for i := range acc.turns {
		if group := &acc.turns[i]; group.stamped {
			runsByTurn[group.turnID] = append(runsByTurn[group.turnID], i)
		}
	}
	runItems := make(map[string]map[int][]appwire.ThreadItem, len(runsByTurn))
	runStamps := make(map[string]executionStamp, len(runsByTurn))
	for turnID, runs := range runsByTurn {
		runItems[turnID] = executionItems(runs, acc.turns)
		runStamps[turnID] = stampOfRuns(runs, acc.turns)
	}
	// visibleRuns are, per stamped turn id, the runs that still emit items
	// AFTER the turn-wide merge: a run whose items all settled into an earlier
	// one is not a fragment, so its neighbour must not claim a continuation it
	// does not have. A stamped turn the file records in more than one visible
	// run must say so — each is a FRAGMENT of one turn, not a whole turn of its
	// own, or a client keyed by turn id holds two complete turns the index
	// reports as one (#1166).
	visibleRuns := map[string][]int{}
	for turnID, runs := range runsByTurn {
		for _, run := range runs {
			if len(runItems[turnID][run]) > 0 {
				visibleRuns[turnID] = append(visibleRuns[turnID], run)
			}
		}
	}
	emittedAny := false
	preludeEmitted := false
	// headerStandalone is true once the header was emitted WITHOUT merging
	// into a leading prelude run. It is then its own visible fragment of the
	// prelude turn, and the runs after it must count it as one.
	headerStandalone := false
	for i := range acc.turns {
		group := &acc.turns[i]
		groupOrdinal := entryOrdinal
		entryOrdinal++
		// A stamped run's items are the turn's, already merged across every run
		// of it; a legacy group's are its own.
		var items []appwire.ThreadItem
		if group.stamped {
			items = runItems[group.turnID][i]
		} else {
			items = mergeGroupedItems(group.items)
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
		StampGroupedTurn(&turn, group.entries)
		if group.stamped {
			// The turn's start and usage are the EXECUTION's, so every fragment
			// reports the same whole-turn figures. A fragment that reported only
			// its own run left a client reconciling turn metadata by id with the
			// later fragment's partial total, and the saved turn underreported
			// tokens and cost against the index (#1166 review).
			runStamps[group.turnID].apply(&turn)
			// The file's completion and reopen records set the execution's
			// status in entry order, and the index surfaces the failure
			// diagnostic only while that status is failed.
			if status, ok := acc.stampedStatus[group.turnID]; ok {
				turn.Status = status
				if status != appwire.TurnStatusFailed {
					turn.Error = nil
				}
			}
		}
		if prelude != nil && !preludeEmitted {
			// The header's items and a LEADING prelude run are ONE turn: the
			// writer stamps a fresh transcript's pre-execution entries with
			// the same id the header content is projected under. They merge
			// only when the prelude run is the first thing the read emits;
			// otherwise the header stands alone at position 0.
			if group.turnID == appwire.SystemPreludeTurnID && !emittedAny {
				merged := make([]appwire.ThreadItem, 0, len(prelude.Items)+len(turn.Items))
				merged = append(merged, prelude.Items...)
				turn.Items = append(merged, turn.Items...)
				preludeEmitted = true
			} else {
				// The header stands alone at position 0, but a stamped run of
				// the prelude turn still follows it: the prompt and that run
				// are two fragments of ONE turn. Emitted as a whole turn the
				// header claimed the prelude ended, so a client keyed by turn
				// id saw two "turn_system" turns where the index keeps one.
				if len(visibleRuns[appwire.SystemPreludeTurnID]) > 0 {
					prelude.ItemsView = appwire.TurnItemsViewFragment
					prelude.HasLaterItems = true
					prelude.HasEarlierItems = false
				}
				turns = append(turns, *prelude)
				preludeEmitted = true
				headerStandalone = true
			}
		}
		// Every visible run of a turn the file records in more than one span is
		// a FRAGMENT, and the flags come from the runs that still have items:
		// an itemless run is not a boundary, so its neighbour must not claim a
		// continuation it does not have. A turn the file records in one span
		// keeps "full".
		//
		// A header emitted on its own is one of those visible fragments: the
		// prompt and the stamped prelude items are ONE turn, so a run that
		// follows the header has an earlier fragment even when it is the only
		// stamped run of its id.
		if runs := visibleRuns[group.turnID]; group.stamped {
			at := 0
			for k, run := range runs {
				if run == i {
					at = k
				}
			}
			visible, earlier := len(runs), at > 0
			if headerStandalone && group.turnID == appwire.SystemPreludeTurnID {
				visible++
				earlier = true
			}
			if visible > 1 {
				turn.ItemsView = appwire.TurnItemsViewFragment
				turn.HasEarlierItems = earlier
				turn.HasLaterItems = at < len(runs)-1
			}
		}
		turns = append(turns, turn)
		emittedAny = true
	}
	if prelude != nil && !preludeEmitted {
		// No visible run claimed the prelude id: the header items are the turn.
		turns = append([]appwire.Turn{*prelude}, turns...)
	}
	return ItemTurnProjection{Turns: turns, NextEntry: entryOrdinal}, nil
}

// executionStamp is one stamped turn's whole-turn metadata: the earliest
// recorded start and the usage summed across every run of it.
type executionStamp struct {
	startedAt *int64
	usage     llm.Usage
}

// apply writes the turn's whole-turn metadata onto one of its fragments.
func (s executionStamp) apply(turn *appwire.Turn) {
	turn.StartedAt = s.startedAt
	if u := appwire.EvenerUsageFromLLM(s.usage); u != nil {
		turn.Usage = u
	}
}

// stampOfRuns folds the whole-turn metadata of one stamped turn from every run
// of it, in file order.
func stampOfRuns(runs []int, groups []groupedTurn) executionStamp {
	var stamp executionStamp
	for _, run := range runs {
		for _, entry := range groups[run].entries {
			if stamp.startedAt == nil && !entry.Timestamp.IsZero() {
				ms := entry.Timestamp.UnixMilli()
				stamp.startedAt = &ms
			}
			stamp.usage = stamp.usage.Add(entry.Usage)
		}
	}
	return stamp
}

// executionItems settles one stamped turn's items ACROSS its runs and returns,
// per run, the items that run emits.
//
// The unit is the turn, not the run, and the unit of a CALL is the assistant
// entry that announced it, not the call id: a provider may reuse a call id in a
// later round, and the index keeps those as two calls. A result settles the
// announcement of its call in the LATEST assistant entry at or before the
// result's own entry — build.go's applyResults matches against the turn's
// awaiting entry — so a delivery between an announcement and its result leaves
// the match intact, and a reused id across a delivery keeps both rows.
func executionItems(runs []int, groups []groupedTurn) map[int][]appwire.ThreadItem {
	// announcement is one item that OPENED a call: where it sits, and the entry
	// that announced it.
	type announcement struct {
		run, at, entry int
	}
	// entryKind is, per entry index, its kind: the index decides what a result
	// is from the entry's KIND (build.go's applyResults runs for TOOL_RESULTS
	// and TOOL entries, never for an announcement), not from the item's status,
	// so this asks the same question.
	entryKind := map[int]schema.TurnKind{}
	for _, run := range runs {
		for k, entryIndex := range groups[run].entryIndexes {
			if k < len(groups[run].entries) {
				entryKind[entryIndex] = groups[run].entries[k].Kind
			}
		}
	}
	isResult := func(entryIndex int) bool {
		kind := entryKind[entryIndex]
		return kind == schema.TurnToolResults || kind == schema.TurnTool
	}
	// announcingRounds is, per entry index, whether that entry announced any
	// tool call at all and the round it recorded. A call can be announced and
	// still project no row — a communicate call renders only when its result
	// arrives — so the ROW a result may settle is decided by the entry, not by
	// which rows exist. The round rides along because a projector that does not
	// apply the identity read model leaves it off the item, while the entry that
	// announced the call still recorded it.
	announcingRounds := map[int]string{}
	for _, run := range runs {
		for k, entryIndex := range groups[run].entryIndexes {
			if k >= len(groups[run].entries) || isResult(entryIndex) {
				continue
			}
			for _, part := range groups[run].entries[k].Message.Content {
				if part.Kind == llm.ContentToolCall && part.ToolCall != nil {
					announcingRounds[entryIndex] = groups[run].entries[k].RoundID
					break
				}
			}
		}
	}

	out := make(map[int][]appwire.ThreadItem, len(runs))
	announcements := map[string][]announcement{}
	// settled marks the result items that merged into an announcement, by their
	// position in the run's own item list, so they are dropped at the end
	// without shifting any index that was already recorded.
	settled := map[[2]int]bool{}
	for _, run := range runs {
		out[run] = make([]appwire.ThreadItem, 0, len(groups[run].items))
		for j, item := range groups[run].items {
			entry := groups[run].itemEntries[j]
			if MergesByCallID(item) && !isResult(entry) {
				announcements[item.CallID] = append(announcements[item.CallID], announcement{run: run, at: len(out[run]), entry: entry})
			}
			out[run] = append(out[run], item)
		}
	}
	// A result settles the announcement it answers: the latest ASSISTANT entry
	// at or before the result's own entry that announced a tool call, which is
	// how build.go's applyResults picks the turn's awaiting entry. That entry
	// may sit in an EARLIER run, which is exactly how a delivery between a call
	// and its result leaves one settled row rather than two. A reused call id
	// keeps both rows, each settled by the result that answers it.
	//
	// The row must belong to the SAME entry. When that entry announced the call
	// but projected no row for it — a rejected communicate renders only its
	// result — there is nothing to settle into, and folding the result into an
	// earlier row of the same call id would delete that row and move the
	// rejection ahead of the delivery. The result then keeps its own position.
	announcing := make([]int, 0, len(announcingRounds))
	for entryIndex := range announcingRounds {
		announcing = append(announcing, entryIndex)
	}
	sort.Ints(announcing)
	for _, run := range runs {
		for j, item := range groups[run].items {
			entry := groups[run].itemEntries[j]
			if !MergesByCallID(item) || !isResult(entry) {
				continue
			}
			latest := sort.Search(len(announcing), func(k int) bool { return announcing[k] > entry })
			if latest == 0 {
				continue // no announcement at or before this result
			}
			calls := announcements[item.CallID]
			at := sort.Search(len(calls), func(k int) bool { return calls[k].entry > announcing[latest-1] })
			if at == 0 || calls[at-1].entry != announcing[latest-1] {
				continue // that entry opened no row for this call: keep the position
			}
			target := calls[at-1]
			// Keep the opener's item and round identity while merging the
			// result's payload and status. The merge derives conversation
			// provenance from the incoming item ID. The projected opener may
			// carry no round (a projector that does not apply the identity read
			// model leaves it off), and the round the entry recorded is still
			// the identity a client reconciles by.
			opener := out[target.run][target.at]
			round := opener.RoundID
			if round == "" {
				round = announcingRounds[target.entry]
			}
			item.ID, item.RoundID = opener.ID, round
			merged := MergeThreadItems(opener, item)
			merged.TurnID = opener.TurnID
			out[target.run][target.at] = merged
			settled[[2]int{run, j}] = true
		}
	}
	for _, run := range runs {
		if len(settled) == 0 {
			break
		}
		kept := make([]appwire.ThreadItem, 0, len(out[run]))
		for j := range out[run] {
			if settled[[2]int{run, j}] {
				continue
			}
			kept = append(kept, out[run][j])
		}
		out[run] = kept
	}
	return out
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

// mergeGroupedItems folds a group's items into one item per call id: a later
// commandExecution item whose CallID was already introduced merges into the
// earlier one (the file counterpart of the live snapshot's call/result
// upsert). Ordinals are assigned AFTER merging, across the whole logical
// turn; items that do not merge keep arrival order.
func mergeGroupedItems(items []appwire.ThreadItem) []appwire.ThreadItem {
	merged := make([]appwire.ThreadItem, 0, len(items))
	callIndex := map[string]int{}
	for _, item := range items {
		if MergesByCallID(item) {
			if at, ok := callIndex[item.CallID]; ok {
				merged[at] = MergeThreadItems(merged[at], item)
				continue
			}
			callIndex[item.CallID] = len(merged)
		}
		merged = append(merged, item)
	}
	return merged
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
