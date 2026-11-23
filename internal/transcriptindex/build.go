package transcriptindex

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
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

// unexpectedQuietRebuildError triggers a rebuild with forceProvisional when a
// PreToolUse hook blanked a non-empty communicate call so TurnToolResults
// settled quietly after its assistant prose had already been committed.
type unexpectedQuietRebuildError struct {
	callID string
}

func (e unexpectedQuietRebuildError) Error() string {
	return fmt.Sprintf("transcript index needs rebuild for hook-blanked quiet call %q", e.callID)
}

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

type deliveredComm struct {
	pos    contributor
	turnID string
}

type provisionalQuietState struct {
	pendingQuiet  map[string]bool
	allCallIDs    []string
	delivered     []deliveredComm
	hasText       bool
	hasAuthorship bool
	flushed       bool
	settledQuiet  bool
	pos           contributor
	turnID        string
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
	// entryItemSlot and entryItemIndex carry the item ordinal the entry being
	// applied assigns within its turn; entryItemCount is that turn's count once
	// the entry's items are appended, which stampTurn folds into the summary.
	entryItemSlot  uint64
	entryItemIndex uint32
	entryItemCount uint32
	entryItems     bool
	// replayOrdinals is set only while a crash replay extends turns whose
	// summaries the interrupted extension already advanced: each committed
	// turn's item count as the replay has advanced it; see itemBase.
	replayOrdinals map[uint32]uint32
	// entryReplayTurn and entryReplayOrdinal record that the entry being
	// applied took its ordinal base from replayOrdinals, so each appended item
	// advances that turn's count.
	entryReplayTurn    uint32
	entryReplayOrdinal bool
	// assistant is the latest ASSISTANT entry with tool calls this builder
	// applied, at assistantOffset, so the results after it need not read it
	// back. It holds a copy, not the caller's pointer: scan decodes one entry
	// per line into a local, and retaining its address would put that whole
	// decoded entry on the heap for every line of a cold rebuild. hasAssistant
	// distinguishes the empty turn from no assistant at all.
	assistant       schema.Turn
	hasAssistant    bool
	assistantOffset int64
	models          map[string]strRef

	// failures applies the shared failure rule as the builder feeds every entry
	// in file order — the one bookkeeping the corrected file scans use, so the
	// per-turn FailedToolCalls it records matches DerivedTotalsFromFile. A fold
	// copy contributes nothing (the counter skips it), matching the scans. On
	// an extension this counter starts empty and sees only the appended
	// entries, so a nameless non-error result whose announcing call is in the
	// covered prefix resolves to the scan's own empty-name fallback rather than
	// to that call's name; the daemonless read's sidecar is a full rebuild,
	// where every call is seen, so its count is exact.
	failures *transcript.FailureCounter
	// entryFailures is the failures the entry currently being applied settled,
	// added to its turn's summary by stampTurn.
	entryFailures uint32
	// entryInexact reports that the entry currently being applied met a nameless
	// tool result whose announcing call the build could not resolve. A full
	// build never does; an extension does when the call lies in the covered
	// prefix. It rides on the turn's own FailedInexact bit, so the qualifier
	// commits atomically with the count it qualifies.
	entryInexact bool

	// commCalls and the lastAssistant* fields mirror ProjectTurn's
	// ToolCallRegistry.CommRawArgs/LastAssistantText/LastAssistantTurnID:
	// unlike names/calls, they are NOT reset at a new open legacy group,
	// because a communicate call's deferred state legitimately crosses
	// logical-turn/group boundaries (e.g. a standalone HOOK_COMPLETED
	// closing the assistant's group before its result arrives) — matching
	// the full read's single registry, threaded for the whole scan. This
	// state also covers inherited identity-format calls, whose transcript-only
	// COMMUNICATE records are excluded from forks. An item's Context persists only
	// the position each is at, not the projected item content: the reader
	// replays the referenced raw entries to reconstruct the values, so build
	// time only needs enough to decide the affected items' structure
	// (existence/parts). meta.CommCalls/LastAssistant* separately persist
	// this whole struct's values, for restoreBuilder to reconstruct it
	// without a rebuild.
	commCalls              map[string]commState // call id -> its deferred communicate state
	provisionalByCall      map[string]*provisionalQuietState
	provisional            []*provisionalQuietState
	forceProvisional       map[uint64]bool
	ignoredUnexpectedQuiet map[string]bool
	lastAssistantText      string
	lastAssistantTurnID    string
	lastAssistantPos       contributor
	// lastAssistantKnown is true once the sticky value above is
	// authoritative: always, for a full build (correct from its first
	// entry — "no text yet" is the ground truth, not an unknown), and for
	// restoreBuilder once it has restored meta.LastAssistantKnown. A
	// communicate call becoming pending while this is still false means the
	// echo check's prior text is unrecoverable without a rebuild (see
	// seed); kept as a defensive fallback now that restoreBuilder
	// ordinarily restores it.
	lastAssistantKnown bool

	// seedBuf, regNames and regCommArgs are the per-entry scratch maps apply
	// refills rather than allocates: seedBuf holds the entry's seed (seed),
	// regNames the copy of it the legacy projection may mutate, and regCommArgs
	// the CommRawArgs snapshot. All are cleared at each entry, so nothing of
	// one entry's state survives into the next's — the same freshness a fresh
	// map gave.
	seedBuf     map[string]string
	regNames    map[string]string
	regCommArgs map[string]string
	regForce    map[int]bool
	// itemBuf, turnBuf and updateBuf are the fixed-size encode scratch of the
	// hot loop: the encode*Into functions fill them, and the table layer
	// copies their bytes out synchronously, so reuse is safe.
	itemBuf   [itemRecordSize]byte
	turnBuf   [turnRecordSize]byte
	updateBuf [updateRecordSize]byte
}

func newBuilder(x *Index) builder {
	return builder{
		x: x, calls: map[string]uint64{}, names: map[string]toolName{}, open: map[string]uint64{}, models: map[string]strRef{},
		commCalls: map[string]commState{}, provisionalByCall: map[string]*provisionalQuietState{},
		seedBuf: map[string]string{}, regNames: map[string]string{}, regCommArgs: map[string]string{},
		failures: transcript.NewFailureCounter(0),
	}
}

// apply indexes the entry at ordinal, whose line is length bytes at offset.
func (b *builder) apply(ordinal uint64, offset int64, length uint32, entry *schema.Turn) error {
	if entry.OriginalOrdinal != nil {
		// A compaction fold copy re-appends an entry the file already holds.
		// The projection and the corrected derived scans skip it, so it takes
		// its ordinal and contributes no turn, usage, or failure.
		return nil
	}
	before := b.failures.Count()
	b.entryInexact = b.failures.ObserveEntry(*entry)
	b.entryFailures = uint32(b.failures.Count() - before)
	if entry.Format == schema.TurnFormatIdentity {
		return b.applyIdentity(ordinal, offset, length, entry)
	}
	return b.applyLegacy(ordinal, offset, length, entry)
}

// quarantine indexes an entry line that does not decode as one unreadable
// item in a completed turn of its own; the reader projects it from the line
// alone (reader.unreadable). It closes the open legacy group, as a
// new-format entry does, so a legacy entry after it starts a turn of its own.
func (b *builder) quarantine(ordinal uint64, offset int64, length uint32) error {
	b.grouper = apptranscript.TurnGrouper{}
	slot, err := b.openTurn(unreadableTurnID(ordinal), turnKindUnreadable, ordinal, offset)
	if err != nil {
		return err
	}
	b.summary.Version = ordinal + 1
	b.summary.ItemCount = 1
	if err := b.x.turns.write(slot, encodeTurnInto(b.turnBuf[:], b.summary)); err != nil {
		return err
	}
	c := contributor{Offset: offset, Ordinal: ordinal, Length: length}
	record := itemRecord{Entry: ordinal + 1, Turn: uint32(slot), Version: ordinal + 1, Opener: c, Flags: itemUnreadable}
	_, err = b.x.items.append(encodeItemInto(b.itemBuf[:], record))
	return err
}

// unreadableTurnID is the id of the turn holding the unreadable entry at
// ordinal.
func unreadableTurnID(ordinal uint64) string { return fmt.Sprintf("turn_unreadable_%d", ordinal) }

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
	if err := b.checkUnexpectedQuiet(entry); err != nil {
		return err
	}
	seed, err := b.seed(entry)
	if err != nil {
		return err
	}
	reg := &apptranscript.ToolCallRegistry{
		Names:               b.registryNames(seed),
		CommRawArgs:         b.registryCommArgs(),
		ForceProvisional:    b.registryForceProvisional(),
		LastAssistantText:   b.lastAssistantText,
		LastAssistantTurnID: b.lastAssistantTurnID,
	}
	provList, err := b.seedRegistryProvisional(reg, entry)
	if err != nil {
		return err
	}
	items, parts := apptranscript.ProjectEntryParts(turnID, entryIndex, *entry, reg, apptranscript.AddressedImageProjector, apptranscript.ToolResultOutputImages)
	b.recordNames(entry, seed)
	base := contributor{Offset: offset, Ordinal: ordinal, Length: length}
	summary, err := b.load(b.turnSlot)
	if err != nil {
		return err
	}
	b.entryItemSlot, b.entryItemIndex, b.entryItems = b.turnSlot, b.itemBase(b.turnSlot, summary, version), true
	for i, item := range items {
		c := base
		merges := apptranscript.MergesByCallID(item)
		if merges {
			if name, ok := seed[item.CallID]; ok {
				if c.Name, err = b.x.strings.putString(name); err != nil {
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
		var context strRef
		if callID, needsProv, ok := b.resultContextTarget(entry, item, seed); ok {
			if context, err = b.communicateContext(callID, needsProv, provList); err != nil {
				return err
			}
		}
		slot, err := b.appendItemWithContext(item, parts[i], b.turnSlot, c, context)
		if err != nil {
			return err
		}
		if merges {
			b.calls[item.CallID] = slot
		}
	}
	b.entryItemCount = b.entryItemIndex
	b.recordCommState(entry, turnID, seed, base, items)
	return b.stampTurn(b.turnSlot, entry, ordinal, offset, length)
}

// registryNames copies seed into the builder's scratch Names map, the copy
// applyLegacy hands the projection in place of today's per-entry
// maps.Clone(seed). The copy matters: the projection deletes communicate names
// from its registry, and recordNames still reads seed after it.
func (b *builder) registryNames(seed map[string]string) map[string]string {
	clear(b.regNames)
	maps.Copy(b.regNames, seed)
	return b.regNames
}

// registryCommArgs refills the builder's scratch CommRawArgs snapshot from
// commCalls, in place of today's per-entry clone. The projection deletes from
// it as results consume calls; the refill restores the snapshot for the next
// entry.
func (b *builder) registryCommArgs() map[string]string {
	clear(b.regCommArgs)
	for id, state := range b.commCalls {
		b.regCommArgs[id] = state.rawArgs
	}
	return b.regCommArgs
}

func (b *builder) registryForceProvisional() map[int]bool {
	if len(b.forceProvisional) == 0 {
		return nil
	}
	if b.regForce == nil {
		b.regForce = make(map[int]bool, len(b.forceProvisional))
	} else {
		clear(b.regForce)
	}
	for ord := range b.forceProvisional {
		b.regForce[int(ord)+1] = true
	}
	return b.regForce
}

func (b *builder) checkUnexpectedQuiet(entry *schema.Turn) error {
	if entry.Kind != schema.TurnTool && entry.Kind != schema.TurnToolResults {
		return nil
	}
	for _, part := range entry.Message.Content {
		if part.Kind == llm.ContentToolResult && part.ToolResult != nil && apptranscript.IsQuietCommunicate(part.ToolResult) {
			id := part.ToolResult.ToolCallID
			if b.provisionalByCall[id] == nil && !b.ignoredUnexpectedQuiet[id] {
				return unexpectedQuietRebuildError{callID: id}
			}
		}
	}
	return nil
}

// resultContextTarget reports whether item produced by entry needs a
// Context contributor list for window.projectWithContext: a flushed provisional
// agentMessage, a quiet_yield notice, or a communicate tool result.
func (b *builder) resultContextTarget(entry *schema.Turn, item appwire.ThreadItem, seed map[string]string) (callID string, needsProvisional bool, ok bool) {
	if entry.Kind == schema.TurnCompletion {
		if item.Type == "agentMessage" && item.CallID == "" {
			return "", true, true
		}
		return "", false, false
	}
	if entry.Kind != schema.TurnTool && entry.Kind != schema.TurnToolResults {
		return "", false, false
	}
	if item.Type == "agentMessage" && item.CallID == "" {
		return "", true, true
	}
	if item.EventKind == appwire.ThreadItemEventKindQuietYield {
		return item.CallID, true, true
	}
	if item.CallID == "" {
		return "", false, false
	}
	for _, content := range entry.Message.Content {
		if content.Kind != llm.ContentToolResult || content.ToolResult == nil || content.ToolResult.ToolCallID != item.CallID {
			continue
		}
		name := content.ToolResult.Name
		if name == "" {
			name = seed[content.ToolResult.ToolCallID]
		}
		if apptranscript.IsCommunicateTool(name, entry.ResultToolName) || apptranscript.IsQuietCommunicate(content.ToolResult) {
			return item.CallID, false, true
		}
	}
	return "", false, false
}

// communicateContext builds the Context an item at communicateResultCallID
// needs: the assistant entry that set CommRawArgs for the call (always, if
// known) and, when it differs, the assistant entry that most recently set
// LastAssistantText (needed only by the healed-communicate echo check, but
// harmless to include for a rejected one too) — sorted in file order, so a
// reader replays them in the order they actually happened.
func (b *builder) communicateContext(callID string, needsProvisional bool, provList []*provisionalQuietState) (strRef, error) {
	var list []contextEntry
	seen := map[int64]bool{}
	add := func(pos contributor, turnID string) error {
		if pos.Length == 0 || seen[pos.Offset] {
			return nil
		}
		seen[pos.Offset] = true
		ref, err := b.x.strings.putString(turnID)
		if err != nil {
			return err
		}
		list = append(list, contextEntry{contributor: pos, TurnID: ref})
		return nil
	}
	if callID != "" {
		if state, ok := b.commCalls[callID]; ok {
			if err := add(state.pos, state.turnID); err != nil {
				return strRef{}, err
			}
		}
		if q, ok := b.provisionalByCall[callID]; ok && (q.hasText || q.hasAuthorship) {
			if err := add(q.pos, q.turnID); err != nil {
				return strRef{}, err
			}
		}
	}
	if needsProvisional {
		for _, q := range provList {
			if q.hasText || q.hasAuthorship {
				if err := add(q.pos, q.turnID); err != nil {
					return strRef{}, err
				}
				for _, d := range q.delivered {
					if err := add(d.pos, d.turnID); err != nil {
						return strRef{}, err
					}
				}
			}
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
// state is never reset at a legacy group boundary (see the builder doc
// comment). Called after the entry's items are recorded, so
// communicateContext above still sees the state as it stood when this entry
// was projected.
// deferredCommCall reports whether an assistant entry's tool call announces a
// deferred communicate call — the one predicate both recordCommState records
// pending calls by and the cut reconstruction's suffix scan answers, so the
// two can never disagree on what counts as one.
func deferredCommCall(entry *schema.Turn, part llm.ContentPart) bool {
	return part.Kind == llm.ContentToolCall && part.ToolCall != nil && apptranscript.IsCommunicateTool(part.ToolCall.Name, entry.ResultToolName) &&
		(entry.Format != schema.TurnFormatIdentity || entry.Authorship != nil && entry.Authorship.Inherited)
}

// agentMessageText answers an entry's trailing agent-message text from its
// projected items: the projected messages rather than the raw entry, because
// legacy text parts and identity text runs segment differently, and empty
// runs show nothing.
func agentMessageText(items []appwire.ThreadItem) string {
	var lastText string
	for _, item := range items {
		if item.Type == "agentMessage" {
			lastText = strings.TrimSpace(item.Text)
		}
	}
	return lastText
}

func flushedAgentMessageText(items []appwire.ThreadItem) string {
	var lastText string
	for _, item := range items {
		if item.Type == "agentMessage" && item.CallID == "" {
			lastText = strings.TrimSpace(item.Text)
		}
	}
	return lastText
}

func assistantEntryHasText(entry *schema.Turn) bool {
	for _, part := range entry.Message.Content {
		if part.Kind == llm.ContentText && part.Text != "" {
			return true
		}
	}
	return false
}

func (b *builder) hasProvisionalAtOrdinal(ordinal uint64) bool {
	for _, q := range b.provisional {
		if q.pos.Ordinal == ordinal && q.hasText {
			return true
		}
	}
	return false
}

func (b *builder) hasUnflushedProvisionalForTurn(turnID string) bool {
	for _, q := range b.provisional {
		if q.turnID == turnID && q.hasText && !q.flushed {
			return true
		}
	}
	return false
}

func (b *builder) seedRegistryProvisional(reg *apptranscript.ToolCallRegistry, entry *schema.Turn) ([]*provisionalQuietState, error) {
	if len(b.provisional) == 0 && len(b.provisionalByCall) == 0 {
		return nil, nil
	}
	var selected []*provisionalQuietState
	seen := map[uint64]bool{}
	switch entry.Kind {
	case schema.TurnTool, schema.TurnToolResults:
		for _, part := range entry.Message.Content {
			if part.Kind != llm.ContentToolResult || part.ToolResult == nil {
				continue
			}
			if q := b.provisionalByCall[part.ToolResult.ToolCallID]; q != nil && !seen[q.pos.Ordinal] {
				seen[q.pos.Ordinal] = true
				selected = append(selected, q)
			}
		}
	case schema.TurnCompletion:
		for _, q := range b.provisional {
			if q.turnID == entry.TurnID && q.hasText && !q.flushed && !seen[q.pos.Ordinal] {
				seen[q.pos.Ordinal] = true
				selected = append(selected, q)
			}
		}
	default:
		return nil, nil
	}
	if len(selected) == 0 {
		return nil, nil
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].pos.Ordinal < selected[j].pos.Ordinal })
	for _, q := range selected {
		assistantEntry, err := b.x.readEntry(q.pos.Offset, q.pos.Length)
		if err != nil {
			return nil, err
		}
		tmpReg := &apptranscript.ToolCallRegistry{ForceProvisional: map[int]bool{int(q.pos.Ordinal) + 1: true}}
		apptranscript.ProjectEntryParts(q.turnID, int(q.pos.Ordinal)+1, *assistantEntry, tmpReg, nil, apptranscript.ToolResultOutputImages)
		for _, d := range q.delivered {
			delEntry, err := b.x.readEntry(d.pos.Offset, d.pos.Length)
			if err != nil {
				return nil, err
			}
			apptranscript.ProjectEntryParts(d.turnID, int(d.pos.Ordinal)+1, *delEntry, tmpReg, nil, nil)
		}
		for _, id := range q.allCallIDs {
			if auth, ok := tmpReg.CommAuthorship[id]; ok {
				if reg.CommAuthorship == nil {
					reg.CommAuthorship = map[string]*schema.TranscriptAuthorship{}
				}
				if _, exists := reg.CommAuthorship[id]; !exists {
					reg.CommAuthorship[id] = auth
				}
			}
		}
		for _, id := range q.allCallIDs {
			if qentry := tmpReg.QuietCalls[id]; qentry != nil {
				qentry.PendingQuiet = maps.Clone(q.pendingQuiet)
				qentry.Flushed = q.flushed
				if reg.QuietCalls == nil {
					reg.QuietCalls = map[string]*apptranscript.QuietAssistantEntry{}
				}
				for _, cid := range q.allCallIDs {
					reg.QuietCalls[cid] = qentry
				}
				if len(qentry.ProvisionalItems) > 0 && len(qentry.PendingQuiet) > 0 && !qentry.Flushed {
					reg.Provisional = append(reg.Provisional, qentry)
				}
				break
			}
		}
	}
	return selected, nil
}

func (b *builder) pruneProvisional() {
	if len(b.provisional) == 0 {
		return
	}
	active := map[*provisionalQuietState]bool{}
	for _, q := range b.provisionalByCall {
		active[q] = true
	}
	kept := b.provisional[:0]
	for _, q := range b.provisional {
		if active[q] || (q.settledQuiet && q.hasText) {
			kept = append(kept, q)
		}
	}
	b.provisional = kept
}

// recordCommState applies the entry's effects on the deferred-communicate
// state ProjectTurn's ToolCallRegistry threads — the same effects
// recordNames applies to tool names, kept separate because unlike names this
// state is never reset at a legacy group boundary (see the builder doc
// comment). Called after the entry's items are recorded, so
// communicateContext above still sees the state as it stood when this entry
// was projected.
func (b *builder) recordCommState(entry *schema.Turn, turnID string, seed map[string]string, c contributor, items []appwire.ThreadItem) {
	switch entry.Kind {
	case schema.TurnAssistant:
		hasCommunicate, quietIDs := apptranscript.AssistantCommunicateCallIDs(*entry, b.forceProvisional[c.Ordinal])
		for _, part := range entry.Message.Content {
			if deferredCommCall(entry, part) {
				b.commCalls[part.ToolCall.ID] = commState{rawArgs: part.ToolCall.SentArguments(), pos: c, turnID: turnID}
			}
		}
		if hasCommunicate && len(quietIDs) > 0 {
			hasText := assistantEntryHasText(entry)
			hasAuthorship := entry.Authorship != nil
			var allCallIDs []string
			for _, part := range entry.Message.Content {
				if part.Kind == llm.ContentToolCall && part.ToolCall != nil {
					allCallIDs = append(allCallIDs, part.ToolCall.ID)
				}
			}
			pending := make(map[string]bool, len(quietIDs))
			for _, id := range quietIDs {
				pending[id] = true
			}
			q := &provisionalQuietState{
				pendingQuiet:  pending,
				allCallIDs:    allCallIDs,
				hasText:       hasText,
				hasAuthorship: hasAuthorship,
				flushed:       !hasText,
				pos:           c,
				turnID:        turnID,
			}
			b.provisional = append(b.provisional, q)
			for _, id := range allCallIDs {
				b.provisionalByCall[id] = q
			}
		}
		// Mirrors ProjectTurn: only a non-empty text updates the sticky
		// value, and only the entry's own trailing text (never a healed
		// communicate — main's fix defers that entirely to the result turn).
		if lastText := agentMessageText(items); lastText != "" {
			b.lastAssistantText, b.lastAssistantTurnID, b.lastAssistantPos, b.lastAssistantKnown = lastText, turnID, c, true
		}
	case schema.TurnTool, schema.TurnToolResults:
		for _, part := range entry.Message.Content {
			if part.Kind == llm.ContentToolResult && part.ToolResult != nil && apptranscript.IsQuietCommunicate(part.ToolResult) {
				if q := b.provisionalByCall[part.ToolResult.ToolCallID]; q != nil {
					q.flushed = true
					q.settledQuiet = true
					delete(q.pendingQuiet, part.ToolResult.ToolCallID)
				}
			}
		}
		var lastFlushedQ *provisionalQuietState
		for _, part := range entry.Message.Content {
			if part.Kind != llm.ContentToolResult || part.ToolResult == nil {
				continue
			}
			id := part.ToolResult.ToolCallID
			if !apptranscript.IsQuietCommunicate(part.ToolResult) {
				if q := b.provisionalByCall[id]; q != nil && !q.flushed {
					delete(q.pendingQuiet, id)
					if len(q.pendingQuiet) == 0 {
						q.flushed = true
						if q.hasText && (lastFlushedQ == nil || q.pos.Ordinal > lastFlushedQ.pos.Ordinal) {
							lastFlushedQ = q
						}
					}
				}
			}
			name := part.ToolResult.Name
			if name == "" {
				name = seed[id]
			}
			if apptranscript.IsCommunicateTool(name, entry.ResultToolName) || apptranscript.IsQuietCommunicate(part.ToolResult) {
				delete(b.commCalls, id)
			}
			delete(b.provisionalByCall, id)
		}
		if lastFlushedQ != nil {
			if lastText := flushedAgentMessageText(items); lastText != "" {
				b.lastAssistantText, b.lastAssistantTurnID, b.lastAssistantPos, b.lastAssistantKnown = lastText, lastFlushedQ.turnID, lastFlushedQ.pos, true
			}
		}
		b.pruneProvisional()
	case schema.TurnCompletion:
		var lastFlushedQ *provisionalQuietState
		kept := b.provisional[:0]
		for _, q := range b.provisional {
			if q.turnID == turnID {
				if q.hasText && !q.flushed && (lastFlushedQ == nil || q.pos.Ordinal > lastFlushedQ.pos.Ordinal) {
					lastFlushedQ = q
				}
				for _, id := range q.allCallIDs {
					delete(b.provisionalByCall, id)
				}
				continue
			}
			kept = append(kept, q)
		}
		b.provisional = kept
		if lastFlushedQ != nil {
			if lastText := flushedAgentMessageText(items); lastText != "" {
				b.lastAssistantText, b.lastAssistantTurnID, b.lastAssistantPos, b.lastAssistantKnown = lastText, lastFlushedQ.turnID, lastFlushedQ.pos, true
			}
		}
	}
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
		if err := b.completeCommunications(slot, entry, c); err != nil {
			return err
		}
		if err := b.applyResults(slot, summary, entry, c); err != nil {
			return err
		}
	case schema.TurnCommunicate:
		// A dedicated delivery consumes inherited reconstruction even when
		// its visible message is suppressed as an assistant-text echo.
		if entry.Communicate != nil {
			delete(b.commCalls, entry.Communicate.CallID)
		}
		echoes, err := b.echoesAwaiting(summary, entry)
		if err != nil {
			return err
		}
		if !echoes && entry.Communicate != nil && entry.Communicate.Message != "" {
			if q := b.provisionalByCall[entry.Communicate.CallID]; q != nil {
				q.delivered = append(q.delivered, deliveredComm{pos: c, turnID: entry.TurnID})
			}
		}
		if !echoes {
			if err := b.appendItems(slot, entry, c, nil, nil); err != nil {
				return err
			}
		} else if err := b.attachCommunicationEcho(summary, entry, c); err != nil {
			return err
		}
	default:
		if entry.Kind != schema.TurnCompletion || b.hasUnflushedProvisionalForTurn(turnID) {
			if err := b.appendItems(slot, entry, c, nil, nil); err != nil {
				return err
			}
		} else {
			b.recordCommState(entry, turnID, nil, c, nil)
		}
	}
	if entry.Kind == schema.TurnCompletion {
		if completion := entry.Completion; completion != nil && completion.AcceptedCommunicateCallID != "" {
			if err := b.linkCommunicationEvidence(slot, completion.AcceptedCommunicateCallID, c); err != nil {
				return err
			}
		}
		delete(b.open, turnID)
		if err := b.interruptAwaitedCalls(summary, c); err != nil {
			return err
		}
	}
	if entry.Kind == schema.TurnAssistant && hasToolCall(entry) {
		b.assistant, b.assistantOffset, b.hasAssistant = *entry, offset, true
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
	if kind == turnKindExecution {
		b.x.meta.LatestExecutionTurnSlot = slot + 1
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

// findTurn searches the summaries, newest first, for the turn id. Persisted
// identity and legacy ids share one address space for transcript reads.
func (b *builder) findTurn(id string) (uint64, bool, error) {
	return b.x.findTurn(context.Background(), id)
}

func (x *Index) findTurn(ctx context.Context, id string) (uint64, bool, error) {
	const chunk = 256
	for end := x.turns.n; end > 0; {
		if err := ctx.Err(); err != nil {
			return 0, false, err
		}
		start := end - min(end, chunk)
		buf, err := x.turns.read(start, int(end-start))
		if err != nil {
			return 0, false, err
		}
		for slot := end; slot > start; slot-- {
			record := decodeTurn(buf[(slot-1-start)*turnRecordSize:])
			if int(record.ID.Len) != len(id) {
				continue
			}
			name, err := x.strings.get(record.ID)
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
	clear(b.seedBuf)
	seed := b.seedBuf
	for _, part := range entry.Message.Content {
		if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.Name == "" {
			if call, ok := calls[part.ToolResult.ToolCallID]; ok {
				seed[part.ToolResult.ToolCallID] = call.name
			} else if _, ok := b.commCalls[part.ToolResult.ToolCallID]; ok {
				seed[part.ToolResult.ToolCallID] = "communicate"
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
			if completer.Name, err = b.x.strings.putString(name); err != nil {
				return false, err
			}
		}
		return true, b.addContributor(target, completer, version)
	})
}

// interruptAwaitedCalls makes an execution's completion a contributor of
// every call of its awaiting ASSISTANT entry that no TOOL_RESULTS completed:
// the call's item projects as interrupted (window.go), at the completion's
// version. That completion is the interrupted one resume records for an
// execution a crash left open, so a reload, a restart and a daemonless read
// all show the call interrupted rather than running forever.
func (b *builder) interruptAwaitedCalls(summary turnRecord, completion contributor) error {
	if summary.Kind != turnKindExecution || summary.AwaitingLength == 0 {
		return nil
	}
	awaiting, err := b.awaitingEntry(summary)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for part, content := range awaiting.Message.Content {
		if content.Kind != llm.ContentToolCall || content.ToolCall == nil || seen[content.ToolCall.ID] {
			continue
		}
		seen[content.ToolCall.ID] = true
		// A communicate call projects no item.
		slot, found, err := b.x.findItem(appwire.ThreadItemPosition{Entry: summary.AwaitingOrdinal + 1, Item: uint32(part)})
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		buf, err := b.x.items.read(slot, 1)
		if err != nil {
			return err
		}
		if record := decodeItem(buf); record.Completer.Length > 0 && record.Completer.Ordinal != completion.Ordinal {
			continue
		}
		if err := b.addContributor(slot, completion, completion.Ordinal+1); err != nil {
			return err
		}
	}
	return nil
}

// appendItems projects a new-format entry and appends an item record for each
// item absorb (when set) does not take. seed names the entry's nameless tool
// results.
func (b *builder) appendItems(slot uint64, entry *schema.Turn, c contributor, seed map[string]string, absorb func(appwire.ThreadItem) (bool, error)) error {
	if err := b.checkUnexpectedQuiet(entry); err != nil {
		return err
	}
	reg := &apptranscript.ToolCallRegistry{
		Names: b.registryNames(seed), CommRawArgs: b.registryCommArgs(),
		ForceProvisional:  b.registryForceProvisional(),
		LastAssistantText: b.lastAssistantText, LastAssistantTurnID: b.lastAssistantTurnID,
	}
	provList, err := b.seedRegistryProvisional(reg, entry)
	if err != nil {
		return err
	}
	items, parts := apptranscript.ProjectEntryParts(entry.TurnID, int(c.Ordinal)+1, *entry, reg, apptranscript.AddressedImageProjector, apptranscript.ToolResultOutputImages)
	summary, err := b.load(slot)
	if err != nil {
		return err
	}
	b.entryItemSlot, b.entryItemIndex, b.entryItems = slot, b.itemBase(slot, summary, c.Ordinal+1), true
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
		var context strRef
		if callID, needsProv, ok := b.resultContextTarget(entry, item, seed); ok {
			var err error
			if context, err = b.communicateContext(callID, needsProv, provList); err != nil {
				return err
			}
		}
		if _, err := b.appendItemWithContext(item, parts[i], slot, c, context); err != nil {
			return err
		}
	}
	b.entryItemCount = b.entryItemIndex
	b.recordCommState(entry, entry.TurnID, seed, c, items)
	return nil
}

// appendItemWithContext appends the record of an item the entry c opens at
// part. Only deferred communicate results need Context (communicateContext).
func (b *builder) appendItemWithContext(item appwire.ThreadItem, part int, turnSlot uint64, c contributor, context strRef) (uint64, error) {
	version := c.Ordinal + 1
	record := itemRecord{Entry: version, Part: uint32(part), Turn: uint32(turnSlot), Version: version, Opener: c, ItemIndex: b.entryItemIndex, Context: context}
	b.entryItemIndex++
	if b.entryReplayOrdinal {
		b.replayOrdinals[b.entryReplayTurn]++
	}
	if apptranscript.MergesByCallID(item) || item.Type == "agentMessage" && item.Observation != nil && item.CallID != "" {
		var err error
		if record.Call, err = b.x.strings.putString(item.CallID); err != nil {
			return 0, err
		}
	}
	return b.x.items.append(encodeItemInto(b.itemBuf[:], record))
}

// itemBase is the ordinal the entry being applied gives its first item. A turn
// summary advanced past this entry is left over from an extension interrupted
// before its meta commit; its count already includes items this replay has not
// re-appended, so the committed records' count is the correct base.
func (b *builder) itemBase(slot uint64, summary turnRecord, version uint64) uint32 {
	// A summary at or past this entry was rewritten by the interrupted
	// extension, which already counted the items this replay appends; its
	// count is not the committed one. The replay's own running count is.
	if summary.Version >= version {
		if count, ok := b.replayOrdinals[uint32(slot)]; ok {
			b.entryReplayTurn, b.entryReplayOrdinal = uint32(slot), true
			return count
		}
	}
	b.entryReplayOrdinal = false
	return summary.ItemCount
}

// completeCommunications links a typed result to the earlier COMMUNICATE item
// of the same logical turn. Looking up recorded item rows rather than an
// in-memory pending map also works after an index handle reload or catch-up.
func (b *builder) completeCommunications(slot uint64, entry *schema.Turn, c contributor) error {
	for _, part := range entry.Message.Content {
		if part.Kind != llm.ContentToolResult || part.ToolResult == nil || part.ToolResult.ToolCallID == "" {
			continue
		}
		id := part.ToolResult.ToolCallID
		if apptranscript.CommunicateTerminalAccepted(*entry, id) == nil {
			continue
		}
		if err := b.linkCommunicationEvidence(slot, id, c); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) linkCommunicationEvidence(slot uint64, id string, c contributor) error {
	const batch = 128
	for end := b.x.items.n; end > 0; {
		start := end - min(end, batch)
		buf, err := b.x.items.read(start, int(end-start))
		if err != nil {
			return err
		}
		for position := end; position > start; position-- {
			record := decodeItem(buf[(position-1-start)*itemRecordSize:])
			if uint64(record.Turn) != slot || record.Call.Len == 0 || record.Opener.Ordinal >= c.Ordinal {
				continue
			}
			matches, err := b.communicationMatches(record, id)
			if err != nil {
				return err
			}
			if matches {
				return b.addContributor(position-1, c, c.Ordinal+1)
			}
		}
		end = start
	}
	return nil
}

// A visible assistant row can carry several echoing calls. Its Call field is
// only the first, so match the recorded contributors rather than that shortcut.
func (b *builder) communicationMatches(record itemRecord, id string) (bool, error) {
	data, err := b.x.strings.get(record.Middle)
	if err != nil {
		return false, err
	}
	contributors, err := decodeContributors(data)
	if err != nil {
		return false, err
	}
	contributors = append(contributors, record.Opener)
	if record.Completer.Length > 0 {
		contributors = append(contributors, record.Completer)
	}
	for _, c := range contributors {
		entry, err := b.x.readEntry(c.Offset, c.Length)
		if err != nil {
			return false, err
		}
		if entry.Kind == schema.TurnCommunicate && entry.Communicate != nil && entry.Communicate.CallID == id {
			return true, nil
		}
	}
	return false, nil
}

// attachCommunicationEcho keeps the visible assistant item and records the
// suppressed message as its contributor, so acceptance can still find its call.
func (b *builder) attachCommunicationEcho(summary turnRecord, entry *schema.Turn, c contributor) error {
	awaiting, err := b.awaitingEntry(summary)
	if err != nil {
		return err
	}
	items, parts := apptranscript.ProjectEntryParts(entry.TurnID, int(summary.AwaitingOrdinal)+1, *awaiting, nil, nil, nil)
	for i := range slices.Backward(items) {
		if items[i].Type != "agentMessage" {
			continue
		}
		slot, found, err := b.x.findItem(appwire.ThreadItemPosition{Entry: summary.AwaitingOrdinal + 1, Item: uint32(parts[i])})
		if err != nil {
			return err
		}
		if found {
			return b.addContributor(slot, c, c.Ordinal+1)
		}
	}
	return nil
}

// echoesAwaiting reports whether a COMMUNICATE entry's message repeats the
// last text run of its turn's awaiting ASSISTANT entry, which already shows it.
func (b *builder) echoesAwaiting(summary turnRecord, entry *schema.Turn) (bool, error) {
	if entry.Communicate == nil || summary.AwaitingLength == 0 || b.hasProvisionalAtOrdinal(summary.AwaitingOrdinal) {
		return false, nil
	}
	awaiting, err := b.awaitingEntry(summary)
	if err != nil {
		return false, err
	}
	return apptranscript.EchoesAssistantText(lastTextRun(awaiting.Message.Content), entry.Communicate.Message), nil
}

// lastTextRun is the text of the run the entry's last agentMessage shows.
func lastTextRun(content []llm.ContentPart) string {
	end := len(content)
	for end > 0 && content[end-1].Kind != llm.ContentText {
		end--
	}
	start := end
	for start > 0 && (content[start-1].Kind == llm.ContentText || apptranscript.MetadataOnlyResponsesReasoning(content[start-1])) {
		start--
	}
	var run strings.Builder
	for _, part := range content[start:end] {
		if part.Kind == llm.ContentText {
			run.WriteString(part.Text)
		}
	}
	return run.String()
}

func (b *builder) awaitingEntry(summary turnRecord) (*schema.Turn, error) {
	if b.hasAssistant && b.assistantOffset == summary.AwaitingOffset {
		return &b.assistant, nil
	}
	entry, err := b.x.readEntry(summary.AwaitingOffset, summary.AwaitingLength)
	if err != nil {
		return nil, err
	}
	b.assistant, b.assistantOffset, b.hasAssistant = *entry, summary.AwaitingOffset, true
	return &b.assistant, nil
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
	id, err := b.x.strings.putString(turnID)
	if err != nil {
		return 0, err
	}
	record := turnRecord{ID: id, Kind: kind, FirstOffset: offset, FirstOrdinal: ordinal}
	if kind == turnKindExecution {
		record.Status = statusOpen
	}
	slot, err := b.x.turns.append(encodeTurnInto(b.turnBuf[:], record))
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
	clear(b.seedBuf)
	seed := b.seedBuf
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
		if !apptranscript.IsCommunicateTool(name, entry.ResultToolName) || apptranscript.IsQuietCommunicate(part.ToolResult) {
			continue
		}
		// A rebuild-free extend also needs the deferred-communicate state
		// this call's result reads (see recordCommState/communicateContext):
		// known always during a full build (b.global set), and otherwise
		// only within the current scan's own local state.
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
			if apptranscript.IsCommunicateTool(name, entry.ResultToolName) || apptranscript.IsQuietCommunicate(part.ToolResult) {
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
	if record.Call.Len == 0 {
		entry, err := b.x.readEntry(c.Offset, c.Length)
		if err != nil {
			return err
		}
		if entry.Kind == schema.TurnCommunicate && entry.Communicate != nil {
			if record.Call, err = b.x.strings.putString(entry.Communicate.CallID); err != nil {
				return err
			}
		}
	}
	// Logged before the write, like stampTurn: an extension interrupted
	// between the two must leave a leftover update-log row behind it, or a
	// later CatchUpTo for a length short of this entry could truncate the
	// log away and find nothing telling it the committed slot below was
	// already overwritten (Index.leftoverUpdatesToCommittedSlots reads
	// exactly that row to force a rebuild rather than leak the overwrite
	// past Window.Length).
	if err := b.logUpdate(updatedItem, slot, c.Offset); err != nil {
		return err
	}
	if testKillAfterItemUpdateLog != nil {
		if err := testKillAfterItemUpdateLog(); err != nil {
			return err
		}
	}
	return b.x.items.write(slot, encodeItemInto(b.itemBuf[:], record))
}

// logUpdate records an in-place update, so a reader holding an older snapshot
// can find what changed after it.
func (b *builder) logUpdate(kind uint32, slot uint64, offset int64) error {
	_, err := b.x.updates.append(encodeUpdateInto(b.updateBuf[:], updateRecord{Kind: kind, Slot: slot, Offset: offset}))
	return err
}

// stampFold folds the entry's summary fields into the turn record: the
// failure stamp, the legacy and execution status transitions, the awaiting
// ASSISTANT entry, the started latch, and the usage totals. It is the shared
// per-entry fold of stampTurn and the cut reconstruction's span fold, which
// must stay identical for a cut view to deep-equal the replay it replaces;
// the model reference, the failure totals and the version belong to each
// caller's own accounting.
func stampFold(r *turnRecord, entry *schema.Turn, ordinal uint64, offset int64, length uint32) {
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
		if testKillAtTurnUpdateLog != nil {
			if err := testKillAtTurnUpdateLog(); err != nil {
				return err
			}
		}
		if err := b.logUpdate(updatedTurn, slot, offset); err != nil {
			return err
		}
	}
	if version <= r.Version {
		return nil // already accounted before an interrupted catch-up
	}
	stampFold(&r, entry, ordinal, offset, length)
	if entry.Model != "" {
		if r.Model, err = b.modelRef(entry.Model); err != nil {
			return err
		}
	}
	r.FailedToolCalls += b.entryFailures
	if b.entryInexact {
		r.FailedInexact = true
	}
	if b.entryItems && b.entryItemSlot == slot {
		r.ItemCount = b.entryItemCount
	}
	r.Version = version
	if err := b.x.turns.write(slot, encodeTurnInto(b.turnBuf[:], r)); err != nil {
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
	ref, err := b.x.strings.putString(model)
	if err != nil {
		return strRef{}, err
	}
	b.models[model] = ref
	return ref, nil
}
