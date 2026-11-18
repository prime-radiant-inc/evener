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

// Identity-format entries name their own turn.
//
// Every entry a writer records through a transcript.Placement carries the
// identity format marker and the TurnID the writer resolved under its append
// lock (see agent/transcript/placement.go): the running execution, the fresh
// transcript's prelude, or a delivery turn of its own. That stamp is the
// authority on which turn an entry belongs to, and the bounded index already
// reads it that way (transcriptindex's applyIdentity places an entry by
// entry.TurnID). This grouper must use the same rule, or the same transcript
// answers with two different turn boundaries: the cold index under the stamped
// id, and this full read under an invented "turn_%d". A client that reconciles
// live items against a cold read then shows a phantom split or merge (#1166).
//
// An identity entry therefore always joins its stamped turn, and only a
// legacy entry (Format 0, no stamp) falls back to the kind-and-owner rules
// above. The identity branch leaves Open false because a stamped entry never
// needs to inherit the open group: the index likewise resets its legacy
// grouping at every identity entry, so a legacy continuation after one starts
// its own group here too.
//
// A shared stamp is not a shared event. Identity says WHICH turn an entry
// belongs to and the file says in what order its items arrived; it does not
// make two spans one span, nor two calls one call. Two consequences hold
// throughout: a turn list never repeats an id (the index keeps one summary per
// id, and a client keys turns by id), and a call id names an instance of a
// call, which a later entry of the same id may only settle while it is still
// awaiting its result. A provider reusing a call id across rounds therefore
// leaves two calls, exactly as the index's per-announcement settlement does.

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
	// entryIndexes is each entry's global 1-based index, parallel to entries.
	// A stamped ASSISTANT entry advances the settlement lookup even when it
	// projects no item, so the walk over this group needs the entries the
	// projector never reported.
	entryIndexes []int
	// items are the group's projected items in arrival order, before merge.
	items []appwire.ThreadItem
	// itemEntries is each item's announcing entry index, parallel to items.
	// A call's announcement batch is one entry's calls: the index builds its
	// settlement lookup from the awaiting entry alone (build.go's applyResults),
	// so the entry boundary is what decides which announcement a result settles.
	itemEntries []int
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
		// transcript-only, so they never reach here and never need to; they
		// still consume their entry index in the scan above.
		//
		// The one shape this linear grouper cannot render as the index does
		// is a turn reopened after OTHER turns have run: its entries carry
		// its old id again, so they open a second group here, while the
		// index appends them to the summary it already holds. That shape is
		// left as it was, and this change does not claim it.
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
// The header prelude and a LEADING group stamped with the same id are ONE
// turn. A fresh transcript holds its pre-execution entries under the prelude
// id the header prelude uses (agent/transcript/placement.go stamps them
// TurnSpanPrelude), and the bounded index coalesces the two by id: its
// prelude items rank before the indexed items, no summary holds the id, and
// the client reads one turn. Emitting them as two turns sharing one id would
// be worse than either boundary alone — a client reconciling by turn id would
// see one turn overwrite the other. So the header items lead the group's items
// in a single turn, each keeping its own position: header items at entry 0,
// the group's at its own ordinal.
//
// Only a LEADING group may merge. The header items hold entry position 0, so
// merging them into a group that follows another turn would emit the prompt
// after that turn's items — decreasing positions, which item paging rejects.
// A transcript that places another turn before its prelude-stamped entries
// (only an older file's shape: a delivery now ends the prelude at write time)
// therefore keeps the two turns as they are.
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
	// Settle every call of a turn against its announcing entry FIRST, across
	// all of that turn's runs. A cold writer can put a delivery between an
	// execution's announcement and its result, which splits one turn id into
	// two runs; the index still settles the call, because its summary spans
	// the runs. Merging per run would leave the announcement unsettled and
	// emit the result as a second item without the announcement's arguments.
	settled := settleTurnCalls(acc.turns)
	// A stamped turn's metadata belongs to the TURN, not to each run of it. A
	// cold delivery can split one turn id into several runs here while the
	// index keeps one summary for them, so stamping each run from its own
	// entries would show a pre-delivery run as completed with a share of the
	// usage while the turn really failed with the total — two readers
	// describing different turns (#1166 review). The entries of every run of
	// an id are folded once, and that one result stamps each visible run.
	stamps := make(map[string][]schema.Turn, len(acc.turns))
	for i := range acc.turns {
		if acc.turns[i].stamped {
			stamps[acc.turns[i].turnID] = append(stamps[acc.turns[i].turnID], acc.turns[i].entries...)
		}
	}
	preludeMerged := false
	for i := range acc.turns {
		group := &acc.turns[i]
		groupOrdinal := entryOrdinal
		entryOrdinal++
		items := settled[i]
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
		turn := appwire.Turn{ID: group.turnID, Items: positioned, ItemsView: "full", Status: appwire.TurnStatusCompleted}
		if group.stamped {
			StampGroupedTurn(&turn, stamps[group.turnID])
		} else {
			StampGroupedTurn(&turn, group.entries)
		}
		if prelude != nil && !preludeMerged && i == 0 && group.turnID == appwire.SystemPreludeTurnID {
			// The header items hold entry position 0, so they must stay
			// FIRST in the merged turn: a delivery recorded before the
			// prelude's first stamped entry has a later position, and
			// emitting it ahead of the prompt would flatten to decreasing
			// positions, which item paging rejects.
			merged := make([]appwire.ThreadItem, 0, len(prelude.Items)+len(turn.Items))
			merged = append(merged, prelude.Items...)
			turn.Items = append(merged, turn.Items...)
			preludeMerged = true
		}
		turns = append(turns, turn)
	}
	if prelude != nil && !preludeMerged {
		// No group claimed the prelude id: the header items are the turn.
		turns = append([]appwire.Turn{*prelude}, turns...)
	}
	turns = joinItemlessRuns(turns)
	markSplitTurnRuns(turns, acc.turns)
	return ItemTurnProjection{Turns: turns, NextEntry: entryOrdinal}, nil
}

// joinItemlessRuns folds adjacent turns that share an id into the earlier one
// when an entry between them projected nothing.
//
// A stamped delivery need not project an item: an attention resolution is
// transcript-only bookkeeping (agent/schema/turn.go), and the writer stamps it
// a delivery of its own (agent/session_attention.go, PlaceAsync). It therefore
// splits the execution's groups here while contributing no visible turn — the
// index never creates a candidate for it, so its items of that execution stay
// one contiguous run and its summary is one turn. Left alone, the full read
// would emit two adjacent turns with the same id, and a client reconciling by
// turn id would see one overwrite the other (#1166 review).
//
// Positions still increase across the join: both runs belong to the same turn
// and their entries are contiguous in the file, apart from the item-less
// delivery, which consumed an entry ordinal no item references.
func joinItemlessRuns(turns []appwire.Turn) []appwire.Turn {
	joined := turns[:0]
	for _, turn := range turns {
		if len(turn.Items) == 0 {
			continue
		}
		if n := len(joined); n > 0 && joined[n-1].ID == turn.ID && joined[n-1].Status == turn.Status {
			joined[n-1].Items = append(joined[n-1].Items, turn.Items...)
			if joined[n-1].Usage == nil {
				joined[n-1].Usage = turn.Usage
			}
			joined[n-1].HasLaterItems = turn.HasLaterItems
			continue
		}
		joined = append(joined, turn)
	}
	return joined
}

// markSplitTurnRuns flags the runs of a turn whose items another turn
// separates, and labels them fragments. An older file can hold one stamped id
// on two spans with a delivery between them (a prelude write, the delivery,
// another prelude write); the writer no longer produces that shape — a
// delivery ends the prelude's span — but a file already on disk keeps it, and
// no reader can join the spans: their items would sit at positions on both
// sides of the delivery's, so a joined turn would emit decreasing positions,
// which item paging rejects.
//
// The bounded index serves such a turn as fragments and says where each one
// stops, counting ALL of the turn's items — the header prelude's included —
// so a run is open only at a boundary the whole turn really continues past
// (transcriptindex window.go: a prelude candidate is open earlier at i > 0
// and later at i+1 < len(prelude.Items), and an item record's flags follow
// its ordinal in the summary). This read must count the same set: the header
// items share the prelude id, so a split prelude that counted only the
// stamped run would mark a prompt "continues later" that the index considers
// whole (#1166 review).
//
// The flags are defined for fragments, so a split run is one: a client that
// receives both readers is told the same thing about a turn that continues
// past another one, and a whole turn keeps the full view.
func markSplitTurnRuns(turns []appwire.Turn, groups []groupedTurn) {
	// Only a STAMPED turn's runs are fragments of one summary: the index keys
	// its summaries by id for new-format entries, so a stamped id split by a
	// delivery is one turn to it. Two LEGACY groups can share an id too (a
	// hook completion closes one, an explicitly owned steering entry later
	// reopens it), and the index keeps a separate summary for each, so each is
	// whole. Marking those partial would tell a client a turn continues where
	// the index says it ends (#1166 review).
	stamped := make(map[string]bool, len(groups))
	for i := range groups {
		if groups[i].stamped {
			stamped[groups[i].turnID] = true
		}
	}
	seen := make(map[string]int, len(turns))
	for i := range turns {
		if !stamped[turns[i].ID] {
			continue
		}
		seen[turns[i].ID]++
	}
	first := make(map[string]int, len(seen))
	last := make(map[string]int, len(seen))
	for i := range turns {
		if !stamped[turns[i].ID] {
			continue
		}
		if _, ok := first[turns[i].ID]; !ok {
			first[turns[i].ID] = i
		}
		last[turns[i].ID] = i
	}
	for i := range turns {
		id := turns[i].ID
		if !stamped[id] || seen[id] < 2 || len(turns[i].Items) == 0 {
			continue
		}
		// A run opens at the front only when it is not the turn's first, and
		// at the back only when it is not the turn's last. Within a run the
		// run's own end is a boundary the whole turn continues past.
		turns[i].HasEarlierItems = i > first[id]
		turns[i].HasLaterItems = i < last[id]
		turns[i].ItemsView = appwire.TurnItemsViewFragment
	}
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

// hasRecordedToolCall reports whether an entry's recorded message carries a
// tool call. It is the full read's copy of transcriptindex's hasToolCall
// (build.go:921), which decides whether an ASSISTANT entry becomes its turn's
// awaiting entry. The test is on the ENTRY, not on the items it projects: a
// communicate call is deferred to its result and projects nothing, so an
// item-based test would miss the entry that ends the previous awaiting.
func hasRecordedToolCall(turn *schema.Turn) bool {
	for _, part := range turn.Message.Content {
		if part.Kind == llm.ContentToolCall && part.ToolCall != nil {
			return true
		}
	}
	return false
}

// settleTurnCalls folds every logical turn's calls and returns, per turn, the
// items to emit. It is the full read's counterpart of the index's
// per-entry settlement, and it makes the decisions the index makes
// (transcriptindex.build.applyResults over the awaiting entry it records):
//
//   - An announcement opens its own item, always. The index projects one item
//     per announcing part, so two parts of one entry that carry the same call
//     id are two items: a duplicate announcement is still an announcement.
//   - A result settles an announcement of the AWAITING entry, the most recent
//     entry that announced calls. The index reads that entry's parts, records
//     each call id once (first matching part wins) and attaches the result to
//     the item at that part's position. So a call id a later round reuses
//     settles the later round's announcement, and when one entry announces an
//     id twice every matching result part attaches to the FIRST of them.
//   - A result with no announcement of its id in the awaiting entry is a
//     stray: it opens its own record here too, so nothing is dropped.
//
// A legacy group keeps the merge it always had — every later item of a call
// id folds into the first — because a legacy file has no entry identities to
// consult, and it carries no state across the groups of one turn id.
//
// The lookup spans EVERY run of a turn id, not one run. A cold writer can
// record a delivery between an execution's announcement and its result, which
// splits the execution's entries into two runs here while the index still
// holds one summary for them; settling per run would leave the announcement
// unsettled and emit the result as an item without its arguments (#1166
// review). Positions are assigned after this, so the result keeps its own
// arrival position inside whichever run it landed in.
func settleTurnCalls(groups []groupedTurn) [][]appwire.ThreadItem {
	settled := make([][]appwire.ThreadItem, len(groups))
	// slot names one emitted item: the group it belongs to and its index there.
	type slot struct{ group, at int }
	// awaiting is a turn's current awaiting entry: the entry that announced
	// calls most recently, and the item each of its call ids announced first.
	type awaiting struct {
		entry int
		calls map[string]slot
	}
	open := make(map[string]*awaiting)
	for g := range groups {
		group := &groups[g]
		merged := make([]appwire.ThreadItem, 0, len(group.items))
		if !group.stamped {
			local := make(map[string]int)
			for _, item := range group.items {
				if !MergesByCallID(item) {
					merged = append(merged, item)
					continue
				}
				if at, ok := local[item.CallID]; ok {
					merged[at] = MergeThreadItems(merged[at], item)
					continue
				}
				local[item.CallID] = len(merged)
				merged = append(merged, item)
			}
			settled[g] = merged
			continue
		}
		// The stamped walk runs over ENTRIES, not items, because the awaitings
		// advance per entry: the index records a turn's awaiting assistant
		// entry whenever that entry carries a tool call (build.go:1144-1146),
		// and a communicate call carries none of its own, so an entry that
		// projects no item still ends the previous awaiting. An item-only walk
		// misses that entry and keeps settling later results into an earlier
		// call that merely shares the id (#1166 review).
		itemAt := 0
		for ei := range group.entries {
			entryIndex := group.entryIndexes[ei]
			start := itemAt
			for itemAt < len(group.items) && group.itemEntries[itemAt] == entryIndex {
				itemAt++
			}
			entryItems := group.items[start:itemAt]
			turn := &group.entries[ei]
			if turn.Kind == schema.TurnAssistant && hasRecordedToolCall(turn) {
				// The awaiting entry is this one, and its settlement targets
				// are the items its parts announced. A part that projects no
				// item contributes none: the index looks the item up by part
				// and finds nothing, so a result for that id settles nowhere.
				aw := open[group.turnID]
				if aw == nil || aw.entry != entryIndex {
					aw = &awaiting{entry: entryIndex, calls: make(map[string]slot)}
					open[group.turnID] = aw
				}
				at := len(merged)
				for _, item := range entryItems {
					if MergesByCallID(item) && item.Status == appwire.TurnStatusInProgress {
						if _, seen := aw.calls[item.CallID]; !seen {
							aw.calls[item.CallID] = slot{group: g, at: at}
						}
					}
					at++
				}
			}
			for _, item := range entryItems {
				if !MergesByCallID(item) {
					merged = append(merged, item)
					continue
				}
				// A result settles the awaiting entry's announcement of this
				// id. Every matching part of one results entry attaches there,
				// as the index contributes each of them to the same item.
				if item.Status != appwire.TurnStatusInProgress {
					if aw := open[group.turnID]; aw != nil {
						if target, ok := aw.calls[item.CallID]; ok {
							// The group being built has no published slice
							// yet, so its slot is read from merged directly.
							var holder *appwire.ThreadItem
							if target.group == g {
								holder = &merged[target.at]
							} else {
								holder = &settled[target.group][target.at]
							}
							// The announcement is the item a client already
							// saw, so the settled item keeps ITS identity: the
							// index never replaces it, it adds a contributor to
							// the item at the announcing part's position
							// (build.go's applyResults -> addContributor).
							// Merging the result's own ID here would make one
							// item vanish and another appear for the same call,
							// which is what a client reconciles by. RoundID has
							// no general fallback either: a results entry
							// carries none, so a merge that does not force it
							// drops the round the announcement belonged to
							// (#1166 review).
							identity, round := holder.ID, holder.RoundID
							*holder = MergeThreadItems(*holder, item)
							holder.ID, holder.RoundID = identity, round
							continue
						}
					}
				}
				// An announcement opens its own item: a duplicate announcement
				// is still an announcement. A stray result opens its own
				// record, so nothing is dropped.
				merged = append(merged, item)
			}
		}
		settled[g] = merged
	}
	return settled
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
