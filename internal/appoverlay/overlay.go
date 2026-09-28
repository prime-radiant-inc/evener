// Package appoverlay holds one thread's live state that is not recorded
// history: model output streams by round and attempt, communicate previews,
// tool execution state keyed by the call's history key, the running turn,
// and the ephemeral notice ring. The daemon feeds it session events and
// recorded entries and publishes the notifications it returns; a read takes
// its snapshot.
package appoverlay

import (
	"cmp"
	"fmt"
	"slices"
	"sync"
	"unicode/utf8"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

// The overlay remembers at most this many recent rounds and calls (see
// Overlay.coveredRounds). Forgetting an old one is harmless: its round has
// ended, so its overlay/end is sent and clients hold its history. Stream and
// preview events only ever apply to the open round, and a tool event for a
// call the overlay no longer knows is dropped unless a round is open (see
// toolCall), so a late event for an ended round creates nothing.
const (
	maxRememberedRounds = 32
	maxRememberedCalls  = 256
)

// A call's output is capped at maxRunningOutputBytes. Passing the cap trims
// it to its last trimmedRunningOutputBytes and sends the whole item once;
// deltas resume until it passes the cap again, so a long-running command
// costs one full item per 64 KB of output rather than one per delta.
const (
	maxRunningOutputBytes     = 256 << 10
	trimmedRunningOutputBytes = 192 << 10
)

// Change is one notification the caller commits, in order.
type Change struct {
	Method string // appwire.NotifyOverlay*
	Params any    // the Overlay*Params without ThreadID/Ref (the server stamps them)
}

// Overlay is one thread's live state that is not recorded history. It is
// safe for concurrent use; its mutex is a leaf (Recorded runs under the
// transcript append lock): nothing under it blocks, does I/O, or takes a lock
// other than the Budget's (see Budget for the order).
type Overlay struct {
	mu     sync.Mutex
	budget *Budget
	closed bool

	runningTurnID string
	// roundID and attempt name the open model round's current attempt; a
	// reset ends an attempt and the next output starts attempt+1.
	roundID string
	attempt int
	// coveredRounds are the rounds whose ASSISTANT entry is recorded: their
	// text and reasoning are history now, so later deltas are dropped.
	// Round end forgets a round's entries in all three of coveredRounds,
	// discardedRounds and calls below. The caps only bound what round end
	// never sees: entries of rounds this overlay never runs (restored or
	// forked history recorded with their round ids) and calls no round
	// claimed. A thread runs its rounds one after another, so the rounds and
	// calls still live are always among the most recent.
	coveredRounds recentSet[struct{}]
	// discardedRounds are rounds whose uncovered streamed text a preview
	// reset discarded; unless an ASSISTANT entry covers them after all, they
	// still end interrupted.
	discardedRounds recentSet[struct{}]
	calls           recentSet[*call]
	// slots holds the streams, previews and tool states by overlay key.
	slots    map[string]*slot
	nextSlot uint64

	notices    noticeRing
	nextNotice uint32
	// anchorEntry is the last recorded entry's ordinal + 1, 0 before any.
	anchorEntry uint64
}

// call is what the overlay knows about one tool call id.
type call struct {
	// roundID is the round whose ASSISTANT entry declared the call, or the
	// round open when the overlay first heard of it.
	roundID string
	// historyKey is the call item's key, learned from the ASSISTANT entry;
	// empty when no entry declared it (no transcript).
	historyKey string
	// toolKey is the overlay key the call's tool state took at its start.
	toolKey string
	// covered reports that the entry settling the call (TOOL_RESULTS, or
	// COMMUNICATE for a communicate call) is recorded.
	covered bool
}

type slot struct {
	item appwire.OverlayItem
	// order is the slot's creation order, which Snapshot follows.
	order uint64
	// output is a tool call's output, kept apart from item so a delta
	// appends in amortized time under the leaf lock; view copies it in.
	output []byte
	// heldImages is a settled call's full image list while some of its
	// images cannot be served before the tool-result entry is written.
	heldImages []appwire.OutputImage
}

// view is a copy of the slot's item for a change or a snapshot, sharing
// nothing with the overlay.
func (s *slot) view() appwire.OverlayItem {
	item := appwire.CloneOverlayItem(s.item)
	if s.item.Kind == appwire.OverlayTool {
		item.Item.Output = string(s.output)
	}
	return item
}

// New returns an empty overlay whose notices count against budget.
func New(budget *Budget) *Overlay {
	return &Overlay{
		budget:          budget,
		coveredRounds:   newRecentSet[struct{}](maxRememberedRounds),
		discardedRounds: newRecentSet[struct{}](maxRememberedRounds),
		calls:           newRecentSet[*call](maxRememberedCalls),
		slots:           map[string]*slot{},
	}
}

// Close releases the overlay's notices from the budget. A closed overlay
// ignores everything after.
func (o *Overlay) Close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed = true
	o.notices.releaseAll(o.budget)
	clear(o.slots)
	o.calls.clear()
	o.coveredRounds.clear()
	o.discardedRounds.clear()
}

// RunningTurnID is the running execution for a thread nothing calls
// SetProcessingTurn for (a delegate).
func (o *Overlay) RunningTurnID() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.runningTurnID
}

// Snapshot is the overlay's current items, for a read's cut: streams,
// previews and tool states in the order they appeared, then the notices
// oldest first.
func (o *Overlay) Snapshot() []appwire.OverlayItem {
	o.mu.Lock()
	defer o.mu.Unlock()
	slots := make([]*slot, 0, len(o.slots))
	for _, s := range o.slots {
		slots = append(slots, s)
	}
	slices.SortFunc(slots, func(a, b *slot) int { return cmp.Compare(a.order, b.order) })
	items := make([]appwire.OverlayItem, 0, len(slots))
	for _, s := range slots {
		items = append(items, s.view())
	}
	return append(items, o.notices.items(o.budget)...)
}

// NoticeBytes reports this thread's notice ring's encoded bytes. For the
// acceptance measurement (spec: "Ephemeral notices stay within 64 KB per
// thread").
func (o *Overlay) NoticeBytes() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.notices.bytes
}

// Contains reports whether key is still in the overlay (a read drops captured
// items a recorded entry covered between the cut and the projection).
func (o *Overlay) Contains(key string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.slots[key]; ok {
		return true
	}
	o.notices.dropEvicted(o.budget)
	return o.notices.contains(key)
}

// Event applies one session event and returns the notifications it causes.
func (o *Overlay) Event(ev events.SessionEvent) []Change {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil
	}
	switch data := ev.Data.(type) {
	case events.ExecutionStartedData:
		o.runningTurnID = data.TurnID
	case events.ExecutionEndedData:
		if o.runningTurnID == data.TurnID {
			o.runningTurnID = ""
		}
	case events.RoundStartedData:
		o.roundID, o.attempt = data.RoundID, 1
	case events.RoundEndedData:
		return o.endRound(data.RoundID)
	case events.AssistantTextDeltaData:
		return o.streamDelta("agentMessage", data.Delta)
	case events.ReasoningSummaryDeltaData:
		return o.streamDelta("reasoning", data.Delta)
	case events.AssistantTextResetData:
		if o.roundID == "" {
			return nil
		}
		return o.resetStream(o.streamID())
	case events.CommunicatePreviewStartData:
		return o.startPreview(data.CallID)
	case events.CommunicatePreviewDeltaData:
		return o.appendText(previewKey(data.CallID), data.Delta)
	case events.CommunicatePreviewResetData:
		return o.resetPreview(data.CallID)
	case events.ToolCallStartData:
		return o.startTool(ev, data)
	case events.ToolCallOutputDeltaData:
		return o.appendOutput(data.CallID, data.Delta)
	case events.ToolCallEndData:
		return o.endTool(data)
	case events.ToolResultImagesPersistedData:
		return o.releaseImages(data.CallIDs)
	default:
		if announcement, ok := noticeAnnouncement(ev); ok {
			return o.addNotice(announcement)
		}
	}
	return nil
}

// Recorded applies one recorded entry: covers streams of its round, the
// preview of its communicate call, the tool state of its results; learns
// call history keys from ASSISTANT entries; advances the notice anchor.
// It returns nothing: clients apply the same coverage from history.
func (o *Overlay) Recorded(rec transcript.Record) {
	if !rec.Recorded {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	o.anchorEntry = max(o.anchorEntry, rec.Ordinal+1)
	entry := rec.Turn
	switch entry.Kind {
	case schema.TurnAssistant:
		o.coverRound(entry.RoundID)
		o.learnCalls(rec)
	case schema.TurnToolResults, schema.TurnTool:
		for _, part := range entry.Message.Content {
			if part.Kind == llm.ContentToolResult && part.ToolResult != nil {
				c := o.call(part.ToolResult.ToolCallID)
				c.covered = true
				delete(o.slots, c.toolKey)
			}
		}
	case schema.TurnCommunicate:
		if entry.Communicate != nil && entry.Communicate.CallID != "" {
			o.call(entry.Communicate.CallID).covered = true
			delete(o.slots, previewKey(entry.Communicate.CallID))
		}
	}
}

func (o *Overlay) coverRound(roundID string) {
	if roundID == "" {
		return
	}
	o.coveredRounds.put(roundID, struct{}{})
	for key, s := range o.slots {
		if s.item.Kind == appwire.OverlayStream && s.item.RoundID == roundID {
			delete(o.slots, key)
		}
	}
}

func (o *Overlay) learnCalls(rec transcript.Record) {
	entry := rec.Turn
	learned := map[string]bool{}
	for part, content := range entry.Message.Content {
		// A call id the entry repeats is keyed by its first part, as the
		// index keys it (internal/transcriptindex/build.go, awaited calls).
		if content.Kind != llm.ContentToolCall || content.ToolCall == nil || learned[content.ToolCall.ID] {
			continue
		}
		learned[content.ToolCall.ID] = true
		c := o.call(content.ToolCall.ID)
		if entry.RoundID != "" {
			c.roundID = entry.RoundID
		}
		c.historyKey = transcriptindex.ItemKey(entry.TurnID, appwire.ThreadItemPosition{Entry: rec.Ordinal + 1, Item: uint32(part)})
	}
}

// toolCall is the call a tool event may update: not settled by a recorded
// entry, and either known or arriving while a round is open. A tool event for
// an unknown call with no open round belongs to a round that already ended
// (its overlay/end sent, its memory forgotten or evicted as it aged out), so
// it must not bring state back; streams and previews follow the same rule
// by needing an open round.
func (o *Overlay) toolCall(callID string) (*call, bool) {
	if _, known := o.calls.get(callID); !known && o.roundID == "" {
		return nil, false
	}
	c := o.call(callID)
	return c, !c.covered
}

// call is the overlay's record of callID, created in the open round.
func (o *Overlay) call(callID string) *call {
	c, ok := o.calls.get(callID)
	if !ok {
		c = &call{roundID: o.roundID}
		o.calls.put(callID, c)
	}
	return c
}

func (o *Overlay) streamID() string {
	return fmt.Sprintf("%s/%d", o.roundID, o.attempt)
}

func previewKey(callID string) string {
	return "preview:" + callID
}

// streamDelta appends to the current attempt's stream of kind. A delta with
// no open round has no stream to belong to; one for a covered round is
// already in history.
func (o *Overlay) streamDelta(kind, text string) []Change {
	if text == "" || o.roundID == "" {
		return nil
	}
	if _, covered := o.coveredRounds.get(o.roundID); covered {
		return nil
	}
	streamID := o.streamID()
	key := "stream:" + streamID + ":" + kind
	if _, ok := o.slots[key]; ok {
		return o.appendText(key, text)
	}
	return o.upsert(o.newSlot(appwire.OverlayItem{
		Key:      key,
		Kind:     appwire.OverlayStream,
		TurnID:   o.runningTurnID,
		RoundID:  o.roundID,
		StreamID: streamID,
		Item:     appwire.ThreadItem{Type: kind, ID: key, TurnID: o.runningTurnID, RoundID: o.roundID, Text: text, Status: appwire.TurnStatusInProgress},
	}))
}

// resetStream discards one attempt's streams and previews, telling clients
// when it held any, and moves to the next attempt when it is the current one.
func (o *Overlay) resetStream(streamID string) []Change {
	if o.roundID != "" && streamID == o.streamID() {
		o.attempt++
	}
	discarded := false
	for key, s := range o.slots {
		if s.item.StreamID == streamID && (s.item.Kind == appwire.OverlayStream || s.item.Kind == appwire.OverlayPreview) {
			delete(o.slots, key)
			discarded = true
		}
	}
	if !discarded {
		return nil
	}
	return []Change{{Method: appwire.NotifyOverlayReset, Params: appwire.OverlayResetParams{StreamID: streamID}}}
}

func (o *Overlay) startPreview(callID string) []Change {
	if callID == "" || o.roundID == "" {
		return nil
	}
	key := previewKey(callID)
	if _, ok := o.slots[key]; ok || o.call(callID).covered {
		return nil
	}
	return o.upsert(o.newSlot(appwire.OverlayItem{
		Key:      key,
		Kind:     appwire.OverlayPreview,
		TurnID:   o.runningTurnID,
		RoundID:  o.roundID,
		StreamID: o.streamID(),
		CallID:   callID,
		Item:     appwire.ThreadItem{Type: "agentMessage", ID: key, TurnID: o.runningTurnID, RoundID: o.roundID, CallID: callID, Status: appwire.TurnStatusInProgress},
	}))
}

// resetPreview discards a failed provisional communicate call. When its
// attempt's text was reset first, the preview is already gone and the
// overlay/reset sent. Otherwise (a fallback group taking over with nothing to
// salvage) nothing else would tell clients, so the preview's attempt is reset
// here: its stream is being discarded too.
func (o *Overlay) resetPreview(callID string) []Change {
	preview, ok := o.slots[previewKey(callID)]
	if !ok {
		return nil
	}
	// A failed or cancelled input resets its previews before its round
	// ends, so the text discarded here is the round's partial reply.
	roundID := preview.item.RoundID
	if _, covered := o.coveredRounds.get(roundID); !covered {
		for _, s := range o.slots {
			if s.item.Kind == appwire.OverlayStream && s.item.StreamID == preview.item.StreamID && s.item.Item.Text != "" {
				o.discardedRounds.put(roundID, struct{}{})
				break
			}
		}
	}
	return o.resetStream(preview.item.StreamID)
}

// appendText appends to the text of the stream or preview at key.
func (o *Overlay) appendText(key, text string) []Change {
	s, ok := o.slots[key]
	if !ok || text == "" {
		return nil
	}
	s.item.Item.Text += text
	return []Change{{Method: appwire.NotifyOverlayDelta, Params: appwire.OverlayDeltaParams{Key: key, Field: appwire.OverlayDeltaText, Delta: text}}}
}

func (o *Overlay) startTool(ev events.SessionEvent, data events.ToolCallStartData) []Change {
	if data.ToolName == "communicate" {
		return nil
	}
	c, ok := o.toolCall(data.CallID)
	if !ok {
		return nil
	}
	s := o.toolSlot(data.CallID, c)
	s.item.Item.ToolName = data.ToolName
	s.item.Item.ArgumentsJSON = data.ArgumentsJSON
	s.item.Item.Description = data.Description
	if !ev.Timestamp.IsZero() {
		ms := ev.Timestamp.UnixMilli()
		s.item.Item.StartedAt = &ms
	}
	return o.upsert(s)
}

// toolSlot is the call's tool state, created in progress when it has none.
func (o *Overlay) toolSlot(callID string, c *call) *slot {
	if s, ok := o.slots[c.toolKey]; ok && c.toolKey != "" {
		return s
	}
	c.toolKey = "tool:call:" + callID
	if c.historyKey != "" {
		c.toolKey = "tool:" + c.historyKey
	}
	return o.newSlot(appwire.OverlayItem{
		Key:        c.toolKey,
		Kind:       appwire.OverlayTool,
		TurnID:     o.runningTurnID,
		RoundID:    c.roundID,
		CallID:     callID,
		HistoryKey: c.historyKey,
		Item:       appwire.ThreadItem{Type: "commandExecution", ID: c.toolKey, TurnID: o.runningTurnID, RoundID: c.roundID, CallID: callID, Status: appwire.TurnStatusInProgress},
	})
}

// appendOutput appends to a running call's output. A delta that takes it past
// the cap trims it and sends the whole item instead, so clients hold the same
// tail.
func (o *Overlay) appendOutput(callID, text string) []Change {
	c, ok := o.calls.get(callID)
	if !ok || text == "" {
		return nil
	}
	s, ok := o.slots[c.toolKey]
	if !ok {
		return nil
	}
	s.output = append(s.output, text...)
	if len(s.output) > maxRunningOutputBytes {
		s.output = keepTail(s.output, trimmedRunningOutputBytes)
		return o.upsert(s)
	}
	return []Change{{Method: appwire.NotifyOverlayDelta, Params: appwire.OverlayDeltaParams{Key: s.item.Key, Field: appwire.OverlayDeltaOutput, Delta: text}}}
}

func (o *Overlay) endTool(data events.ToolCallEndData) []Change {
	if data.ToolName == "communicate" {
		return nil
	}
	c, ok := o.toolCall(data.CallID)
	if !ok {
		return nil
	}
	s := o.toolSlot(data.CallID, c)
	item := &s.item.Item
	if item.ToolName == "" {
		item.ToolName = data.ToolName
	}
	item.Status = apptranscript.SettledToolStatus(data.Error != "")
	item.Error = data.Error
	item.PrevalOnly = data.PrevalOnly
	item.Raw = data.ToolState
	item.ExitCode = apptranscript.ExitCodeFromToolState(data.ToolState)
	if len(data.Output) > maxRunningOutputBytes {
		s.output = keepTail([]byte(data.Output), trimmedRunningOutputBytes)
	} else if data.Output != "" {
		s.output = []byte(data.Output)
	}
	images := apptranscript.LiveOutputImages(data.OutputImages)
	fetchable := slices.DeleteFunc(slices.Clone(images), apptranscript.UnfetchableUntilRecorded)
	s.heldImages = nil
	if len(fetchable) != len(images) {
		s.heldImages = images
	}
	item.OutputImages = fetchable
	return o.upsert(s)
}

// releaseImages sends each held call's item again with every image, now that
// the tool-result entry holding their bytes is written.
func (o *Overlay) releaseImages(callIDs []string) []Change {
	var changes []Change
	for _, callID := range callIDs {
		c, ok := o.calls.get(callID)
		if !ok {
			continue
		}
		s, ok := o.slots[c.toolKey]
		if !ok || s.heldImages == nil {
			continue
		}
		s.item.Item.OutputImages, s.heldImages = s.heldImages, nil
		changes = append(changes, o.upsert(s)...)
	}
	return changes
}

// endRound collapses a round that ended with unrecorded content (streamed
// text or reasoning its ASSISTANT entry never covered, or tool states no
// TOOL_RESULTS settled) into one interrupted notice, ends the round, and
// forgets everything the round held.
func (o *Overlay) endRound(roundID string) []Change {
	interrupted := false
	for key, s := range o.slots {
		if s.item.RoundID != roundID {
			continue
		}
		if s.item.Kind == appwire.OverlayTool || (s.item.Kind == appwire.OverlayStream && s.item.Item.Text != "") {
			interrupted = true
		}
		delete(o.slots, key)
	}
	o.calls.deleteFunc(func(_ string, c *call) bool { return c.roundID == roundID || c.roundID == "" })
	if _, discarded := o.discardedRounds.get(roundID); discarded {
		if _, covered := o.coveredRounds.get(roundID); !covered {
			interrupted = true
		}
	}
	o.discardedRounds.delete(roundID)
	o.coveredRounds.delete(roundID)
	if o.roundID == roundID {
		o.roundID, o.attempt = "", 0
	}
	var changes []Change
	if interrupted {
		changes = o.addNotice(interruptedAnnouncement())
	}
	return append(changes, Change{Method: appwire.NotifyOverlayEnd, Params: appwire.OverlayEndParams{RoundID: roundID}})
}

// addNotice appends a notice anchored after the last recorded entry.
func (o *Overlay) addNotice(announcement apptranscript.NoticeAnnouncement) []Change {
	key := fmt.Sprintf("notice:%d", o.nextNotice)
	item, ok := apptranscript.SystemMessage(announcement, key, o.runningTurnID)
	if !ok {
		return nil
	}
	overlayItem := appwire.OverlayItem{
		Key:    key,
		Kind:   appwire.OverlayNotice,
		TurnID: o.runningTurnID,
		Anchor: &appwire.ThreadItemPosition{Entry: o.anchorEntry, Item: appwire.NoticeAnchorItem, Sub: o.nextNotice},
		Item:   item,
	}
	o.nextNotice++
	size := encodedSize(overlayItem)
	// A notice no ring or budget could hold is neither kept nor sent: a
	// read could never show it.
	if size > maxNoticeBytes || size > o.budget.limit {
		return nil
	}
	// The ring makes room first, so its own caps evict this thread's oldest
	// before the budget evicts anyone else's; the budget's evictions (which
	// may be this thread's) are then forgotten.
	n := &notice{key: key, bytes: size, roundTimings: item.EventKind == appwire.ThreadItemEventKindRoundTimings}
	o.notices.dropEvicted(o.budget)
	o.notices.add(n, o.budget)
	stored := appwire.CloneOverlayItem(overlayItem)
	n.charge = o.budget.charge(&stored, size)
	o.notices.dropEvicted(o.budget)
	return []Change{{Method: appwire.NotifyOverlayUpserted, Params: appwire.OverlayUpsertedParams{Item: overlayItem}}}
}

func (o *Overlay) newSlot(item appwire.OverlayItem) *slot {
	s := &slot{item: item, order: o.nextSlot}
	o.nextSlot++
	o.slots[item.Key] = s
	return s
}

func (o *Overlay) upsert(s *slot) []Change {
	return []Change{{Method: appwire.NotifyOverlayUpserted, Params: appwire.OverlayUpsertedParams{Item: s.view()}}}
}

// keepTail returns at most limit trailing bytes of b, starting at a rune
// boundary, in a buffer of exactly that size, so a huge delta or settled
// output does not stay pinned by the capacity it arrived in.
func keepTail(b []byte, limit int) []byte {
	cut := len(b) - limit
	for cut < len(b) && !utf8.RuneStart(b[cut]) {
		cut++
	}
	tail := make([]byte, len(b)-cut)
	copy(tail, b[cut:])
	return tail
}
