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
	// items are the group's projected items in arrival order, before merge.
	items []appwire.ThreadItem
	// entryIndexes is each entry's global 1-based index, parallel to entries.
	// A stamped completion or reopen record is folded into its execution's
	// status in FILE ORDER, so the walk needs each entry's position.
	entryIndexes []int
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
	// controls maps a stamped turn id to its completion and reopen records, in
	// file order. Those kinds are transcript-only, so they project no item and
	// join no group — but a reopen clears an earlier terminal state and the
	// next completion sets the new one, and the fold can only apply that in
	// the order they were written (#1166 review).
	controls map[string][]indexedTurn
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
		//
		// A stamped completion or reopen is the exception, and only for its
		// STATUS: it opens no group and projects no item, but it decides
		// whether the execution it ends reads completed, failed or
		// interrupted. Discarding it here derived status from the remaining
		// entries instead, so a crash recovered as interrupted read completed
		// while its unanswered call read still in progress (#1166 review).
		acc.recordControl(turn, entryIndex)
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

// recordControl buffers a stamped completion or reopen record against the turn
// whose state it changes. Records are appended in file order and the fold
// replays them in that order, so a reopen after a failure clears it and the
// next completion decides the state the turn actually ended in.
func (a *logicalTurnAccumulator) recordControl(turn schema.Turn, entryIndex int) {
	if turn.Format != schema.TurnFormatIdentity || turn.TurnID == "" {
		return
	}
	if turn.Kind != schema.TurnCompletion && turn.Kind != schema.TurnReopen {
		return
	}
	if a.controls == nil {
		a.controls = map[string][]indexedTurn{}
	}
	a.controls[turn.TurnID] = append(a.controls[turn.TurnID], indexedTurn{index: entryIndex, turn: turn})
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
// indexedTurn is one entry with its global position, so a fold can apply
// entries in the order they were written.
type indexedTurn struct {
	index int
	turn  schema.Turn
}

// turnExecution is one stamped turn across every run of its id: what the
// bounded index keeps as one summary. Replay renders one turn per visible run,
// but every field it computes — item identity, item order, call settlement,
// usage, terminal status and the continuation flags — is a property of the
// EXECUTION, and of the items that SURVIVE settlement in it, not of a run. A
// cold writer can put a delivery between two runs of one execution, and
// deriving those fields from the runs independently is what produced a
// finding in a different field in every review of this change.
type turnExecution struct {
	// entries are every run's entries in file order.
	entries []schema.Turn
	// controls are this execution's completion and reopen records in file
	// order, including the ones that appear between its runs.
	controls []indexedTurn
	// items are every run's projected items in file order, before settlement.
	items        []appwire.ThreadItem
	itemEntries  []int
	itemAnnounce []bool
	// entriesByIndex maps each entry's global position to the entry, so the
	// ordered walk over the execution can consult an entry by position.
	entriesByIndex map[int]schema.Turn
	// groupOfItem is the GROUP each item came from, parallel to items. It is
	// the membership key: a legacy group that happens to share this id is not
	// part of the execution, and a run index of zero must not be confused with
	// "no run" (#1166 review).
	groupOfItem []int
	// runs are the group indexes of this execution's runs, in order.
	runs []int

	settled   bool
	merged    []appwire.ThreadItem
	mergedGrp []int
	// callIndex is, per announcing entry and call id, the merged item a result
	// settled. Settlement and the completion fold both read THIS table: one
	// statement of "which item a call owns" rather than a copy each.
	callIndex map[awaitKey]int
	// visible are the groups that still have items after settlement, in order.
	// Completeness counts these, not the raw runs: settlement can empty a run.
	visible []int
}

// settle folds the whole execution's items once. A result that arrives in a
// later run settles into the announcement's own item, which keeps its place
// and its identity — the index's rule: one settled item at the announcement.
func (ex *turnExecution) settle() {
	if ex.settled {
		return
	}
	ex.settled = true
	ex.merged, ex.mergedGrp, ex.callIndex = mergeExecutionItems(ex.items, ex.itemEntries, ex.itemAnnounce, ex.groupOfItem, ex.entriesByIndex)
	ex.interruptAwaitedCalls()
	seen := make(map[int]bool, len(ex.runs))
	for _, group := range ex.mergedGrp {
		if seen[group] {
			continue
		}
		seen[group] = true
		ex.visible = append(ex.visible, group)
	}
}

// interruptAwaitedCalls marks a call interrupted when the execution completed
// without a result for it. The index does exactly this
// (builder.interruptAwaitedCalls, applied to the completion record at
// window.go's item projection): a completion is the execution's statement that
// nothing else is coming, so a call still awaiting a result did not finish.
// Without it a crash whose recovery wrote an interrupted completion reported
// the turn interrupted and the call still in progress, which is the one thing
// a client reads to decide whether to wait (#1166 review).
//
// Only unanswered CALLS change: a call that already has a result keeps the
// status its result gave it, because a completion never retracts a recorded
// outcome.
func (ex *turnExecution) interruptAwaitedCalls() {
	if len(ex.controls) == 0 {
		return
	}
	// awaiting holds the call ids of the latest announcing entry — the batch
	// the index interrupts at a completion (builder.interruptAwaitedCalls).
	// A call awaiting when a completion arrives did not finish; a call from an
	// EARLIER batch that a later assistant entry superseded is not part of
	// what that completion ended, so it keeps its own status.
	awaiting := map[string]bool{}
	// byCall is the SAME table settle() used to decide what a result settles
	// (mergeExecutionItems' callIndex). Keying it by the announcing entry AND
	// the call id is what makes a retry that reuses an id a different call:
	// interrupting the first attempt's call must not touch the second
	// attempt's. A bare call-id match did exactly that, marking the running
	// retry interrupted (#1166 review). Rebuilding that rule here instead of
	// reading it is the duplicate implementation this now avoids: two copies
	// of one rule can drift, and a completion that settled a different item
	// than the result did is two readers disagreeing about one call.
	byCall := ex.callIndex
	// awaiting holds the announcing entry and its recorded part ids, so a
	// completion resolves only the announcements that batch made.
	var awaitingEntry int
	// Walk the entries in file order so each completion sees the batch that
	// was awaiting AT THAT POINT.
	//
	// A reopen does NOT clear the batch. A crash can land after an announcement
	// and before its result or the execution's completion, so recovery reopens
	// a turn whose batch was never answered; the reopen says the turn runs
	// again, not that the question was answered. Only a later announcing entry
	// replaces the batch, which is the index's rule too (transcriptindex
	// stampFold leaves Awaiting in place across a reopen and sets it again from
	// the next assistant entry; interruptAwaitedCalls interrupts that batch at
	// completion). Clearing it here left the crashed attempt's call in progress
	// forever while the index reported it interrupted.
	//
	// Control records are transcript-only, so they are NOT in entriesByIndex:
	// the two ordered sequences are merged here, which is the only place both
	// are visible.
	entries := ex.sortedEntryIndexes()
	controls := make([]indexedTurn, len(ex.controls))
	copy(controls, ex.controls)
	sort.SliceStable(controls, func(i, j int) bool { return controls[i].index < controls[j].index })
	controlAt, entryAt := 0, 0
	for controlAt < len(controls) || entryAt < len(entries) {
		controlNext := controlAt < len(controls)
		entryNext := entryAt < len(entries)
		// Take whichever position comes first; entries win a tie so an entry's
		// calls are awaiting before a completion at the same position changes
		// them.
		if controlNext && (!entryNext || controls[controlAt].index < entries[entryAt]) {
			switch controls[controlAt].turn.Kind {
			case schema.TurnReopen:
				// The batch stays awaiting: nothing has answered it yet.
			case schema.TurnCompletion:
				// The item a call owns is the one settlement recorded for it,
				// so this reads the same table the result fold wrote. A call the
				// entry announced but that a second part also recorded resolves
				// to the RESERVED part — ReservedCalls keeps the first — and the
				// duplicate keeps its own status (#1166 review).
				for call := range awaiting {
					at, recorded := byCall[awaitKey{entry: awaitingEntry, call: call}]
					if !recorded {
						continue
					}
					if status := ex.merged[at].Status; status == "" || status == appwire.TurnStatusInProgress {
						ex.merged[at].Status = appwire.TurnStatusInterrupted
					}
				}
			}
			controlAt++
			continue
		}
		entry := ex.entriesByIndex[entries[entryAt]]
		// ReservedCalls is the one statement of which calls an entry recorded —
		// the same call the index makes in applyResults. Reading it here rather
		// than re-scanning the message is what keeps one entry from reserving a
		// different batch in each reader.
		if reserved := ReservedCalls(&entry); len(reserved) > 0 {
			awaiting = make(map[string]bool, len(reserved))
			awaitingEntry = entries[entryAt]
			for callID := range reserved {
				awaiting[callID] = true
			}
		}
		entryAt++
	}
}

// itemsOfGroup returns the execution's settled items that belong to one group.
// ok is false when the group is not part of this execution at all, which is
// how a legacy group sharing the id stays on the legacy path.
func (ex *turnExecution) itemsOfGroup(group int) (items []appwire.ThreadItem, ok bool) {
	for _, run := range ex.runs {
		if run == group {
			ok = true
		}
	}
	if !ok {
		return nil, false
	}
	for at := range ex.merged {
		if ex.mergedGrp[at] == group {
			items = append(items, ex.merged[at])
		}
	}
	return items, true
}

// boundary reports a group's position among the execution's VISIBLE runs and
// whether a visible run precedes or follows it.
func (ex *turnExecution) boundary(group int) (position, visible int, earlier, later bool) {
	visible = len(ex.visible)
	for at, run := range ex.visible {
		if run == group {
			return at, visible, at > 0, at < visible-1
		}
	}
	return 0, visible, false, false
}

// groupExecutions collects every run of each stamped turn id into one
// execution.
func groupExecutions(groups []groupedTurn, controls map[string][]indexedTurn) map[string]*turnExecution {
	execs := make(map[string]*turnExecution, len(groups))
	for i := range groups {
		if !groups[i].stamped {
			continue
		}
		ex := execs[groups[i].turnID]
		if ex == nil {
			ex = &turnExecution{controls: controls[groups[i].turnID]}
			execs[groups[i].turnID] = ex
		}
		ex.runs = append(ex.runs, i)
		ex.entries = append(ex.entries, groups[i].entries...)
		if ex.entriesByIndex == nil {
			ex.entriesByIndex = map[int]schema.Turn{}
		}
		for k, entryIndex := range groups[i].entryIndexes {
			if k < len(groups[i].entries) {
				ex.entriesByIndex[entryIndex] = groups[i].entries[k]
			}
		}
		for j := range groups[i].items {
			ex.items = append(ex.items, groups[i].items[j])
			ex.itemEntries = append(ex.itemEntries, groups[i].itemEntries[j])
			ex.itemAnnounce = append(ex.itemAnnounce, groups[i].itemAnnounce[j])
			ex.groupOfItem = append(ex.groupOfItem, i)
		}
	}
	return execs
}

// terminalFailure returns the failure entry that ended the execution's CURRENT
// attempt: the last TURN_FAILURE recorded after the execution's last reopen.
// A reopen starts a new attempt, so a failure before it belongs to history. It
// reports false when the current attempt recorded none, which is a retry that
// ended without a diagnostic — not a reason to borrow an older attempt's.
func (ex *turnExecution) terminalFailure() (schema.Turn, bool) {
	start := -1
	for _, control := range ex.controls {
		if control.turn.Kind == schema.TurnReopen && control.index > start {
			start = control.index
		}
	}
	// The highest position after the reopen wins: that is the last failure of
	// this attempt. Map order is not iteration order, so the position is
	// compared rather than taken as it comes.
	best, found := -1, schema.Turn{}
	for index, entry := range ex.entriesByIndex {
		if index <= start || entry.Kind != schema.TurnFailure || index <= best {
			continue
		}
		best, found = index, entry
	}
	return found, best >= 0
}

// sortedEntryIndexes lists the execution's entry positions in file order, so a
// fold that depends on what had arrived by a given point can walk them.
func (ex *turnExecution) sortedEntryIndexes() []int {
	out := make([]int, 0, len(ex.entriesByIndex))
	for entryIndex := range ex.entriesByIndex {
		out = append(out, entryIndex)
	}
	sort.Ints(out)
	return out
}

// startsAnExecution reports whether this stamped turn began an EXECUTION — its
// first recorded span kind says so. Only an execution defaults to in progress
// when nothing has completed it; a delivery or prelude span is complete as
// recorded.
func (ex *turnExecution) startsAnExecution() bool {
	for _, entry := range ex.entries {
		if entry.TurnKind != "" {
			return entry.TurnKind == schema.TurnSpanExecution
		}
	}
	return false
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
	execs := groupExecutions(acc.turns, acc.controls)
	for _, ex := range execs {
		ex.settle()
	}
	// The header turn keeps entry position 0, so it is emitted in file order:
	// a delivery recorded before the prelude's first stamped entry keeps its
	// own position ahead of it. Merging the prompt into a LATER prelude run
	// would emit position 0 after that delivery and flatten the chronology,
	// which item paging rejects (#1166 review).
	preludeEmitted := false
	emittedAny := false
	// headerStandalone is true once the header has been emitted WITHOUT merging
	// into a leading prelude run. It is then its own visible fragment of the
	// prelude turn, and the runs after it must count it as one.
	headerStandalone := false
	// preludeRunFollows reports whether a stamped run of the prelude turn still
	// follows the header in file order. When one does, the header and that run
	// are two fragments of ONE turn, and the header must say so: emitted as a
	// complete turn it claimed the prelude ended, so a client keyed by turn id
	// saw two "turn_system" turns where the index keeps one (#1166 review).
	preludeRunFollows := func(from int) bool {
		ex := execs[appwire.SystemPreludeTurnID]
		if ex == nil {
			return false
		}
		// A group that EXISTS is not enough: settlement can empty a run, and a
		// header claiming later items nothing will deliver is the paging bug
		// this flag exists to prevent. The question is whether any run at or
		// after `from` still has items AFTER settlement (#1166 review).
		for _, run := range ex.visible {
			if run >= from {
				return true
			}
		}
		return false
	}
	emitPrelude := func(from int) {
		if prelude == nil || preludeEmitted {
			return
		}
		// The header is the FIRST visible fragment of the prelude turn whenever
		// stamped prelude items survive later in the file. Continuation flags
		// are what the bounded reader sets for the same shape, so a client
		// paging one run cannot mistake it for the whole turn.
		if preludeRunFollows(from) {
			prelude.ItemsView = appwire.TurnItemsViewFragment
			prelude.HasLaterItems = true
			prelude.HasEarlierItems = false
		}
		turns = append(turns, *prelude)
		preludeEmitted = true
	}
	for i := range acc.turns {
		group := &acc.turns[i]
		groupOrdinal := entryOrdinal
		entryOrdinal++
		var ex *turnExecution
		var items []appwire.ThreadItem
		if candidate := execs[group.turnID]; candidate != nil {
			// Membership decides the path, never an id match: a legacy group
			// that happens to carry a stamped turn's id is not one of its runs.
			if membership, ok := candidate.itemsOfGroup(i); ok {
				ex, items = candidate, membership
			}
		}
		if ex == nil {
			// Only a legacy group takes the call-id-wide merge. A stamped run's
			// items are already settled against the whole execution, so
			// merging them here computed a result the execution's own fold had
			// superseded (#1166 review).
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
		if ex != nil {
			// Usage and terminal status are the execution's, folded once from
			// every run's entries AND its completion and reopen records in file
			// order, so neither fragment understates or misstates the turn.
			//
			// A stamped EXECUTION starts in progress: nothing has said it
			// ended. The index opens one that way, so a process that exits
			// before writing its completion reads in progress from both
			// readers instead of completed from one of them. A non-execution
			// span (a delivery) keeps the completed default (#1166 review).
			if ex.startsAnExecution() {
				turn.Status = appwire.TurnStatusInProgress
			}
			StampGroupedTurn(&turn, ex.entries)
			if ex.startsAnExecution() {
				// StampGroupedTurn's failure stamp is the LEGACY rule: a
				// TURN_FAILURE entry makes the turn failed. The index does not
				// apply that to a stamped execution — stampFold changes an
				// execution's status only on TURN_COMPLETION and TURN_REOPEN
				// (internal/transcriptindex/build.go), so an execution that
				// persisted a failure and then exited before its completion
				// stays open, with the failure a history item and no turn-level
				// diagnostic. Without this reset a crash in that window read
				// failed here and in progress from the index (#1166 review).
				turn.Status = appwire.TurnStatusInProgress
				turn.Error = nil
			}
			stampExecutionControls(&turn, ex.controls)
			// The diagnostic belongs to the attempt that ended the execution,
			// and an ATTEMPT begins at a reopen. Scanning the whole execution
			// history picked the last TURN_FAILURE regardless of which attempt
			// wrote it, so a retry whose own buffered diagnostic write failed
			// reported the recovered attempt's error — a diagnostic naming the
			// wrong attempt, which is worse than none. The fold therefore
			// resolves the failure by position: only a failure recorded AFTER
			// the execution's last reopen describes this attempt. Earlier
			// failures keep their own items in history; they just no longer
			// name this turn's error (#1166 review).
			if turn.Status == appwire.TurnStatusFailed {
				if failure, ok := ex.terminalFailure(); ok {
					StampTurnFailure(&turn, failure)
				}
			}
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
				emitPrelude(i)
				headerStandalone = true
			}
		}
		// Every visible run of a split stamped turn is a FRAGMENT, and the
		// flags come from the runs that still have items AFTER settlement — a
		// run settlement emptied is not a boundary, so its neighbour must not
		// claim a continuation it does not have (#1166 review). An independent
		// legacy group keeps "full".
		//
		// A header emitted on its own is one of those visible fragments: the
		// prompt and the stamped prelude items are ONE turn, so a run that
		// follows the header has an earlier fragment even when it is the only
		// stamped run of its id. Counting raw runs alone made it read "full"
		// while the index kept the turn open (#1166 review).
		if ex != nil {
			_, visible, earlier, later := ex.boundary(i)
			if headerStandalone && group.turnID == appwire.SystemPreludeTurnID {
				visible++
				earlier = true
			}
			if visible > 1 {
				turn.ItemsView = appwire.TurnItemsViewFragment
				turn.HasEarlierItems = earlier
				turn.HasLaterItems = later
			}
		}
		turns = append(turns, turn)
		emittedAny = true
	}
	if prelude != nil && !preludeEmitted {
		// No visible run claimed the prelude id: the header items are the turn.
		// When stamped prelude items SURVIVE SETTLEMENT they follow a delivery,
		// so the header is the FIRST visible fragment of that turn and says so.
		// Asking whether the group exists is not enough — a stamped SYSTEM
		// entry that projects nothing would advertise a continuation behind a
		// prompt that is the whole turn (#1166 review).
		if ex := execs[appwire.SystemPreludeTurnID]; ex != nil && len(ex.visible) > 0 {
			prelude.ItemsView = appwire.TurnItemsViewFragment
			prelude.HasLaterItems = true
		}
		turns = append([]appwire.Turn{*prelude}, turns...)
		preludeEmitted = true
	}
	turns = coalesceAdjacentRuns(turns)
	return ItemTurnProjection{Turns: turns, NextEntry: entryOrdinal}, nil
}

// coalesceAdjacentRuns joins consecutive emitted turns of one execution when
// no visible item from another turn stands between them. An itemless entry
// splits an execution's runs without splitting what a reader sees, so two
// fragments whose items are adjacent are one fragment: they carry continuation
// flags that promise earlier or later items nothing delivered, and the index
// reports the execution's items as one turn (#1166 review).
//
// A turn whose id differs, or another turn's item in between, keeps the runs
// apart — only runs that are adjacent in the OUTPUT are joined.
func coalesceAdjacentRuns(turns []appwire.Turn) []appwire.Turn {
	out := make([]appwire.Turn, 0, len(turns))
	for _, turn := range turns {
		if n := len(out); n > 0 {
			prev := &out[n-1]
			// Two fragments of one stamped turn, adjacent in the output, are
			// one fragment. A legacy turn is never joined: its runs are
			// independent groups even when they share an id.
			if prev.ID == turn.ID &&
				prev.ItemsView == appwire.TurnItemsViewFragment &&
				turn.ItemsView == appwire.TurnItemsViewFragment {
				prev.Items = append(prev.Items, turn.Items...)
				// The joined turn's earlier/later flags describe the execution's
				// boundaries OUTSIDE the runs being joined, so the first run's
				// earlier flag and the last run's later flag are the only ones
				// that survive. The interior flags are what the join removes.
				prev.HasLaterItems = turn.HasLaterItems
				continue
			}
		}
		out = append(out, turn)
	}
	// A joined turn that is now the execution's only fragment is COMPLETE: it
	// has nothing earlier or later, so the flags it carried no longer describe
	// it.
	for at := range out {
		if out[at].ItemsView != appwire.TurnItemsViewFragment {
			continue
		}
		if !out[at].HasEarlierItems && !out[at].HasLaterItems {
			out[at].ItemsView = appwire.TurnItemsViewFull
		}
	}
	return out
}

// stampExecutionControls applies a stamped execution's completion and reopen
// records in the order they were written. A reopen clears the terminal state an
// earlier failure or interruption left — recovery reclaimed the turn and runs
// it again — and the next completion decides the new one. Folding these
// per run, or discarding them, reports a successful retry as the failure it
// recovered from (#1166 review). The failure itself stays in the transcript as
// history; only the turn's current state changes.
func stampExecutionControls(turn *appwire.Turn, controls []indexedTurn) {
	// File order is the fold's contract: a reopen clears the state an earlier
	// completion set, and only the position says which came first. The records
	// are buffered in scan order already, so the sort is a guarantee rather
	// than a fix — but the ORDER is what makes the result correct, so it is
	// stated here instead of assumed from the caller.
	ordered := make([]indexedTurn, len(controls))
	copy(ordered, controls)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].index < ordered[j].index })
	for _, control := range ordered {
		switch control.turn.Kind {
		case schema.TurnReopen:
			turn.Status = appwire.TurnStatusInProgress
			turn.Error = nil
			turn.CompletedAt = nil
			turn.DurationMS = nil
		case schema.TurnCompletion:
			// An omitted payload folds as zero-valued info, which is what the
			// index does (stampFold's TurnCompletion case takes the empty
			// CompletionInfo when the record carries none). Skipping the fold
			// instead left the run at its previous status while the index read
			// a completed turn with zero duration, and the interrupt pass below
			// still marked unanswered calls — an in-progress turn on one side
			// of the comparison and a completed one on the other (#1166 review).
			info := schema.TurnCompletionInfo{}
			if control.turn.Completion != nil {
				info = *control.turn.Completion
			}
			// The mapping is shared with the index (ExecutionStatusFromCompletion):
			// one completion must not read completed in one reader and
			// interrupted in the other.
			turn.Status = ExecutionStatusFromCompletion(info.Status)
			if info.Status != schema.TurnFailed {
				// A completion record carries no message of its own. A FAILED
				// one takes its diagnostic from the TURN_FAILURE entry of the
				// attempt that failed, which the caller re-derives after this
				// fold; every other terminal status reports none, so an
				// interrupted turn does not render the failure end cap the
				// index does not (#1166 review).
				turn.Error = nil
			}
			if !info.CompletedAt.IsZero() {
				ms := info.CompletedAt.UnixMilli()
				turn.CompletedAt = &ms
			}
			// A completion ALWAYS states its duration, including zero: a
			// recovered interruption records 0, and omitting it here left the
			// full read without a field the index reports. Only an execution
			// with no completion record has no duration (#1166 review).
			duration := info.DurationMS
			turn.DurationMS = &duration
		}
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

// ReservedCalls returns the calls an announcing entry reserved, keyed by call
// id to the first content part that recorded it.
//
// This is one settlement rule with two callers on purpose. The transcript index
// builds its awaiting batch this way (transcriptindex applyResults keeps the
// first part per id and settles a result only against that part); whole-file
// replay must reserve the same target or the two readers complete different
// items for one result. A later part recording an already-reserved id is its
// own announcement: it is never the target of a result or a completion.
// awaitKey names an announcement by the entry that recorded it and the call id
// it recorded. Two entries recording one id are two calls, and one entry
// recording an id twice reserves only the first part.
type awaitKey struct {
	entry int
	call  string
}

// reservationCache answers "which part did this entry record for that call?"
// once per entry. ReservedCalls allocates a map, so asking it per announcement
// made a batch of N calls do quadratic work and allocate N maps of size N;
// the merge folds thousands of items per long transcript, so this is a real
// per-read cost rather than a theoretical one (#1166 review).
type reservationCache struct {
	entries map[int]schema.Turn
	byEntry map[int]map[string]int
}

func newReservationCache(entries map[int]schema.Turn) *reservationCache {
	return &reservationCache{entries: entries, byEntry: make(map[int]map[string]int, len(entries))}
}

// reserved reports which content part an entry reserves for a call id, and
// whether the entry records that id at all.
func (c *reservationCache) reserved(entry int, callID string) (int, bool) {
	reserved, seen := c.byEntry[entry]
	if !seen {
		if turn, ok := c.entries[entry]; ok {
			reserved = ReservedCalls(&turn)
		}
		c.byEntry[entry] = reserved
	}
	part, recorded := reserved[callID]
	return part, recorded
}

// partProjectsItem reports whether the part an entry reserves for a call yields
// a projected item. A communicate call does not: its message is rendered from
// the paired result, which is what lets live and reload agree on a call the
// assistant entry alone cannot describe. Reading the part rather than the item
// stream is the difference between reserving what the entry recorded and
// reserving whatever happened to project (#1166 review).
func partProjectsItem(entries map[int]schema.Turn, entry, part int) bool {
	turn, ok := entries[entry]
	if !ok || part < 0 || part >= len(turn.Message.Content) {
		return false
	}
	content := turn.Message.Content[part]
	if content.Kind != llm.ContentToolCall || content.ToolCall == nil {
		return false
	}
	return content.ToolCall.Name != "communicate"
}

// reservedToolName is the tool name the entry recorded for the part it reserved
// for a call id, or "" when the entry does not record that id.
func reservedToolName(entries map[int]schema.Turn, entry, part int) string {
	turn, ok := entries[entry]
	if !ok || part < 0 || part >= len(turn.Message.Content) {
		return ""
	}
	content := turn.Message.Content[part]
	if content.Kind != llm.ContentToolCall || content.ToolCall == nil {
		return ""
	}
	return content.ToolCall.Name
}

func ReservedCalls(entry *schema.Turn) map[string]int {
	if entry == nil {
		return nil
	}
	reserved := make(map[string]int, len(entry.Message.Content))
	for part, content := range entry.Message.Content {
		if content.Kind != llm.ContentToolCall || content.ToolCall == nil {
			continue
		}
		if _, seen := reserved[content.ToolCall.ID]; !seen {
			reserved[content.ToolCall.ID] = part
		}
	}
	return reserved
}

// ResultSettlesReservedCall reports whether a projected RESULT belongs to the
// call its awaiting entry reserved, rather than to a different tool's call that
// merely reuses the id.
//
// One settlement guard, both readers: the index calls this before absorbing a
// result into the reserved item (transcriptindex applyResults) and whole-file
// replay calls it before folding a result into an announcement
// (mergeExecutionItems). A prevalidation-rejected communicate is a real row of
// its own — live never delivered it, so its failure is the only record that it
// happened — and absorbing it into a reservation another tool made omits that
// row in the index and overwrites the command's name and status in replay
// (#1166 review).
//
// A result with no name of its own carries no claim, so it settles the
// reservation: that is the case the reservation exists for. When it does name a
// tool, the claim must match the reserved call's name.
func ResultSettlesReservedCall(reservedTool, resultTool string) bool {
	if resultTool == "" {
		return true
	}
	return reservedTool == resultTool
}

// ExecutionStatusFromCompletion maps a recorded completion's status to the turn
// status both readers report for a stamped execution.
//
// The index applies this mapping in stampFold (transcriptindex build.go) and
// whole-file replay applies it in stampExecutionControls. Sharing it is what
// keeps one completion from reading completed in one reader and interrupted in
// the other: an absent or unrecognised status is completed, and only the two
// explicit non-successful terminal statuses differ.
func ExecutionStatusFromCompletion(status schema.TurnCompletionStatus) string {
	switch status {
	case schema.TurnFailed:
		return appwire.TurnStatusFailed
	case schema.TurnInterrupted:
		return appwire.TurnStatusInterrupted
	default:
		return appwire.TurnStatusCompleted
	}
}

// hasRecordedToolCall reports whether the entry's own content records a tool
// call. Such an entry is what the index calls the turn's AWAITING entry: its
// calls are the only ones a later result can settle, including calls that
// project no item because the provider hid them.
func hasRecordedToolCall(turn *schema.Turn) bool {
	return len(ReservedCalls(turn)) > 0
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
func mergeExecutionItems(items []appwire.ThreadItem, itemEntries []int, itemAnnounce []bool, groupOfItem []int, entries map[int]schema.Turn) ([]appwire.ThreadItem, []int, map[awaitKey]int) {
	merged := make([]appwire.ThreadItem, 0, len(items))
	mergedGrp := make([]int, 0, len(items))
	// callIndex keys an announcement by its awaiting entry and call id, which
	// is how the index decides what a result settles. The FIRST announcement
	// for a key owns it: a second part recording the same id is a distinct
	// announcement, and the result goes to the first recorded part — the part
	// ReservedCalls reserves for that entry.
	callIndex := map[awaitKey]int{}
	reservations := newReservationCache(entries)
	for at, item := range items {
		group := 0
		if at < len(groupOfItem) {
			group = groupOfItem[at]
		}
		entry := 0
		if at < len(itemEntries) {
			entry = itemEntries[at]
		}
		if MergesByCallID(item) {
			if at < len(itemAnnounce) && itemAnnounce[at] {
				key := awaitKey{entry: entry, call: item.CallID}
				// The reservation belongs to a recorded PART, not to whichever
				// item carries the id: ReservedCalls names the part a result
				// settles. When that part projects no item — a communicate call
				// is deferred to its result — the reservation has no item here,
				// so a LATER part reusing the id is a visible announcement of its
				// own and the result stays standalone. Reserving from projected
				// items instead did the opposite: it gave the hidden part's
				// reservation to the visible command and let a result overwrite
				// it, while the index left the command untouched (#1166 review).
				if reservedPart, announced := reservations.reserved(entry, item.CallID); announced && !partProjectsItem(entries, entry, reservedPart) {
					merged = append(merged, item)
					mergedGrp = append(mergedGrp, group)
					continue
				}
				if _, claimed := callIndex[key]; !claimed {
					callIndex[key] = len(merged)
				}
				merged = append(merged, item)
				mergedGrp = append(mergedGrp, group)
				continue
			}
			key := awaitKey{entry: entry, call: item.CallID}
			if prev, ok := callIndex[key]; ok {
				// A result that names a DIFFERENT tool than the reservation did
				// is not that call's result: a prevalidation-rejected communicate
				// reusing a read_file id is its own row. Folding it here would
				// overwrite the command's name and status, which the index does
				// not do. One guard, both readers (#1166 review).
				if reserved, announced := reservations.reserved(entry, item.CallID); announced &&
					!ResultSettlesReservedCall(reservedToolName(entries, entry, reserved), item.ToolName) {
					merged = append(merged, item)
					mergedGrp = append(mergedGrp, group)
					continue
				}
				// The item that was admitted keeps its identity: a settled call
				// does not change name because its result arrived in another
				// run or another entry. The incoming result adopts the
				// announcement's ID and round BEFORE merging, so provenance
				// derived from the ID agrees across projections (#1166 review).
				incoming := item
				incoming.ID = merged[prev].ID
				incoming.RoundID = merged[prev].RoundID
				merged[prev] = MergeThreadItems(merged[prev], incoming)
				continue
			}
		}
		merged = append(merged, item)
		mergedGrp = append(mergedGrp, group)
	}
	return merged, mergedGrp, callIndex
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
