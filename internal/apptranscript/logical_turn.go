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
	if len(items) > 0 {
		last.items = append(last.items, items...)
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
	preludeMerged := false
	for i := range acc.turns {
		group := &acc.turns[i]
		groupOrdinal := entryOrdinal
		entryOrdinal++
		items := mergeGroupedItems(group.items, group.stamped)
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
		StampGroupedTurn(&turn, group.entries)
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
	markSplitTurnRuns(turns)
	return ItemTurnProjection{Turns: turns, NextEntry: entryOrdinal}, nil
}

// markSplitTurnRuns flags the runs of a turn whose items another turn
// separates. An older file can hold one stamped id on two spans with a
// delivery between them (a prelude write, the delivery, another prelude
// write); the writer no longer produces that shape — a delivery ends the
// prelude's span — but a file already on disk keeps it, and no reader can
// join the spans: their items would sit at positions on both sides of the
// delivery's, so a joined turn would emit decreasing positions, which item
// paging rejects.
//
// The bounded index serves such a turn as fragments and says where each one
// stops: its candidate's HasEarlierItems/HasLaterItems follow the item's
// ordinal within the whole turn (transcriptindex window.go). This read
// reports the same boundaries, so a client that receives both — live item
// pages and a cold whole-turn read — is told the same thing about a turn
// that continues past another one. Without the flags the two runs share an id
// and nothing marks them as halves of one turn.
func markSplitTurnRuns(turns []appwire.Turn) {
	seen := make(map[string]int, len(turns))
	for i := range turns {
		seen[turns[i].ID]++
	}
	first := make(map[string]int, len(seen))
	last := make(map[string]int, len(seen))
	for i := range turns {
		if _, ok := first[turns[i].ID]; !ok {
			first[turns[i].ID] = i
		}
		last[turns[i].ID] = i
	}
	for i := range turns {
		id := turns[i].ID
		if seen[id] < 2 || len(turns[i].Items) == 0 {
			continue
		}
		turns[i].HasEarlierItems = i > first[id]
		turns[i].HasLaterItems = i < last[id]
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

// mergeGroupedItems folds a group's items into one item per call id: a later
// commandExecution item whose CallID was already introduced merges into the
// earlier one (the file counterpart of the live snapshot's call/result
// upsert). Ordinals are assigned AFTER merging, across the whole logical
// turn; items that do not merge keep arrival order.
//
// stamped scopes the merge to the call one announcement opened, which is how
// the bounded index settles an identity turn's calls: a call opens the item
// its announcing assistant entry projected, and a result attaches to THAT
// entry, so a provider reusing one call id in a later round opens a SECOND
// call, with its own arguments and result (#1166 review). Without that scope
// the second round's announcement merges into the first item and its arguments
// overwrite the first round's, losing that call as a distinct record.
//
// A second ANNOUNCEMENT is therefore never a merge: it is a second call, even
// when the first never got its result (a failed result write leaves the call
// in-progress and its id free to be reused). A result still joins the
// announcement it settles, and settles it.
//
// A legacy group keeps the merge it always had: it folds every later item of
// a call id, because the legacy reader has no announcement to consult.
func mergeGroupedItems(items []appwire.ThreadItem, stamped bool) []appwire.ThreadItem {
	merged := make([]appwire.ThreadItem, 0, len(items))
	callIndex := map[string]int{}
	for _, item := range items {
		if MergesByCallID(item) {
			// An announcement opens a call; a result settles one. Only a
			// legacy group may fold an announcement into an announcement.
			announcement := item.Status == appwire.TurnStatusInProgress
			if at, ok := callIndex[item.CallID]; ok && (!stamped || !announcement) {
				merged[at] = MergeThreadItems(merged[at], item)
				if stamped {
					// The result settled the call, so a later item of this id
					// announces a new one.
					delete(callIndex, item.CallID)
				}
				continue
			}
			// A stray result whose announcement lies outside this group opens
			// its own record too, so nothing is dropped.
			if !stamped || announcement {
				callIndex[item.CallID] = len(merged)
			}
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
