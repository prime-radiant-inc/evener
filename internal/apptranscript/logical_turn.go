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
	// itemEntries is each item's announcing entry index, parallel to items.
	// In a stamped turn the AWAITING assistant entry is the call-identity
	// boundary: the index settles a result only against a call of the turn's
	// awaiting ASSISTANT entry (build.go's applyResults), so a provider that
	// reuses a call id in a later round is two calls there and must be two
	// here. Each item carries the index of the assistant entry awaiting when
	// it was appended, so an announcement and its own results share a key
	// while a later round's announcement gets a new one.
	itemEntries []int
	// awaiting is the index of the ASSISTANT entry whose calls are awaiting
	// results, or 0 when the group has none yet. It is the key appended to
	// itemEntries for every item of that batch.
	awaiting int
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
	// A stamped assistant entry that records tool calls becomes the awaiting
	// entry: its own items and every following result entry's items key to
	// it, matching the index, which settles against the turn's awaiting
	// assistant entry alone.
	if entry.Format == schema.TurnFormatIdentity && hasRecordedToolCall(&entry) {
		last.awaiting = entryIndex
	}
	if len(items) > 0 {
		last.items = append(last.items, items...)
		for range items {
			if last.awaiting != 0 {
				last.itemEntries = append(last.itemEntries, last.awaiting)
			} else {
				last.itemEntries = append(last.itemEntries, entryIndex)
			}
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
	// emitted tracks which turn ids have already produced a run, so a later
	// run of one id is emitted as a fragment rather than a second full turn.
	emitted := make(map[string]bool, len(acc.turns))
	for i := range acc.turns {
		group := &acc.turns[i]
		groupOrdinal := entryOrdinal
		entryOrdinal++
		items := mergeGroupedItems(group.items, group.itemEntries, group.stamped)
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
		if prelude != nil && !preludeMerged && group.turnID == appwire.SystemPreludeTurnID {
			// The header's items and the prelude's stamped entries are ONE
			// turn: the writer stamps a fresh transcript's pre-execution
			// entries with the same id the header content is projected
			// under, so emitting both would show a client two turns with one
			// id, and a client that keys by turn id would lose one. The
			// header items hold entry position 0, so they stay FIRST: a
			// delivery recorded before the prelude's first stamped entry has
			// a later position, and emitting it ahead of the prompt would
			// flatten to decreasing positions, which item paging rejects.
			merged := make([]appwire.ThreadItem, 0, len(prelude.Items)+len(turn.Items))
			merged = append(merged, prelude.Items...)
			turn.Items = append(merged, turn.Items...)
			preludeMerged = true
		}
		// A later run of an id already emitted is a FRAGMENT of that turn, not
		// a second full turn. AppWire defines the continuation flags for
		// fragments, and the bounded reader labels these runs "fragment"; a
		// client told two full turns share one id cannot reconcile them at all
		// (#1166 review). This survives on replay of transcripts written before
		// the writer ended the prelude's span at a delivery.
		if emitted[group.turnID] {
			turn.ItemsView = appwire.TurnItemsViewFragment
			turn.HasEarlierItems = true
		}
		turns = append(turns, turn)
		emitted[group.turnID] = true
	}
	if prelude != nil && !preludeMerged {
		// No group claimed the prelude id: the header items are the turn.
		turns = append([]appwire.Turn{*prelude}, turns...)
	}
	// Every run but the last of a split id has more of that turn to come, and
	// the fragment view says so. Without this the earlier run advertises
	// completeness it does not have, and a client stops paging for the rest.
	lastRun := make(map[string]int, len(turns))
	for i := range turns {
		lastRun[turns[i].ID] = i
	}
	for i := range turns {
		if turns[i].ItemsView == appwire.TurnItemsViewFragment && lastRun[turns[i].ID] != i {
			turns[i].HasLaterItems = true
		}
	}
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
func mergeGroupedItems(items []appwire.ThreadItem, itemEntries []int, stamped bool) []appwire.ThreadItem {
	merged := make([]appwire.ThreadItem, 0, len(items))
	// callIndex is keyed by call id, and for a stamped turn also by the
	// announcing entry, so the same id from a different entry opens an item.
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
