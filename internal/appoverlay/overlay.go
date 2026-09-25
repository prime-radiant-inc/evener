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

// maxRunningOutputBytes is how much of a running call's output the overlay
// keeps: the last 256 KB.
const maxRunningOutputBytes = 256 << 10

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
	coveredRounds map[string]struct{}
	calls         map[string]*call
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
	// heldImages is a settled call's full image list while some of its
	// images cannot be served before the tool-result entry is written.
	heldImages []appwire.OutputImage
}

// New returns an empty overlay whose notices count against budget.
func New(budget *Budget) *Overlay {
	return &Overlay{
		budget:        budget,
		coveredRounds: map[string]struct{}{},
		calls:         map[string]*call{},
		slots:         map[string]*slot{},
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
	clear(o.calls)
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
	o.notices.dropEvicted(o.budget)
	slots := make([]*slot, 0, len(o.slots))
	for _, s := range o.slots {
		slots = append(slots, s)
	}
	slices.SortFunc(slots, func(a, b *slot) int { return cmp.Compare(a.order, b.order) })
	items := make([]appwire.OverlayItem, 0, len(slots)+len(o.notices.notices))
	for _, s := range slots {
		items = append(items, s.item)
	}
	for _, n := range o.notices.notices {
		items = append(items, n.item)
	}
	return items
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
		return o.resetAttempt()
	case events.CommunicatePreviewStartData:
		return o.startPreview(data.CallID)
	case events.CommunicatePreviewDeltaData:
		return o.appendText(previewKey(data.CallID), data.Delta)
	case events.CommunicatePreviewResetData:
		// The attempt's overlay/reset or the round's overlay/end tells
		// clients; this only forgets it.
		delete(o.slots, previewKey(data.CallID))
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
	o.coveredRounds[roundID] = struct{}{}
	for key, s := range o.slots {
		if s.item.Kind == appwire.OverlayStream && s.item.RoundID == roundID {
			delete(o.slots, key)
		}
	}
}

func (o *Overlay) learnCalls(rec transcript.Record) {
	entry := rec.Turn
	for part, content := range entry.Message.Content {
		if content.Kind != llm.ContentToolCall || content.ToolCall == nil {
			continue
		}
		c := o.call(content.ToolCall.ID)
		if entry.RoundID != "" {
			c.roundID = entry.RoundID
		}
		c.historyKey = transcriptindex.ItemKey(entry.TurnID, appwire.ThreadItemPosition{Entry: rec.Ordinal + 1, Item: uint32(part)})
	}
}

// call is the overlay's record of callID, created in the open round.
func (o *Overlay) call(callID string) *call {
	c, ok := o.calls[callID]
	if !ok {
		c = &call{roundID: o.roundID}
		o.calls[callID] = c
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
	if _, covered := o.coveredRounds[o.roundID]; covered {
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

// resetAttempt discards the current attempt's streams and previews and moves
// to the next attempt.
func (o *Overlay) resetAttempt() []Change {
	if o.roundID == "" {
		return nil
	}
	streamID := o.streamID()
	o.attempt++
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
	c := o.call(data.CallID)
	if c.covered {
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
		Item:       appwire.ThreadItem{Type: "commandExecution", ID: c.toolKey, TurnID: o.runningTurnID, CallID: callID, Status: appwire.TurnStatusInProgress},
	})
}

// appendOutput appends to a running call's output, keeping the last
// maxRunningOutputBytes. A delta that trims sends the whole item instead, so
// clients hold the same tail.
func (o *Overlay) appendOutput(callID, text string) []Change {
	c, ok := o.calls[callID]
	if !ok || text == "" {
		return nil
	}
	s, ok := o.slots[c.toolKey]
	if !ok {
		return nil
	}
	output, trimmed := lastBytes(s.item.Item.Output+text, maxRunningOutputBytes)
	s.item.Item.Output = output
	if trimmed {
		return o.upsert(s)
	}
	return []Change{{Method: appwire.NotifyOverlayDelta, Params: appwire.OverlayDeltaParams{Key: s.item.Key, Field: appwire.OverlayDeltaOutput, Delta: text}}}
}

func (o *Overlay) endTool(data events.ToolCallEndData) []Change {
	if data.ToolName == "communicate" {
		return nil
	}
	c := o.call(data.CallID)
	if c.covered {
		return nil
	}
	s := o.toolSlot(data.CallID, c)
	item := &s.item.Item
	if item.ToolName == "" {
		item.ToolName = data.ToolName
	}
	item.Status = apptranscript.SettledToolStatus(data.Error != "")
	item.Error = data.Error
	if data.Output != "" {
		item.Output, _ = lastBytes(data.Output, maxRunningOutputBytes)
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
		c, ok := o.calls[callID]
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
	for callID, c := range o.calls {
		if c.roundID == roundID || c.roundID == "" {
			delete(o.calls, callID)
		}
	}
	delete(o.coveredRounds, roundID)
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
	o.notices.dropEvicted(o.budget)
	o.notices.add(&notice{item: overlayItem, bytes: size, charge: o.budget.charge(size)}, o.budget)
	return []Change{{Method: appwire.NotifyOverlayUpserted, Params: appwire.OverlayUpsertedParams{Item: overlayItem}}}
}

func (o *Overlay) newSlot(item appwire.OverlayItem) *slot {
	s := &slot{item: item, order: o.nextSlot}
	o.nextSlot++
	o.slots[item.Key] = s
	return s
}

func (o *Overlay) upsert(s *slot) []Change {
	return []Change{{Method: appwire.NotifyOverlayUpserted, Params: appwire.OverlayUpsertedParams{Item: s.item}}}
}

// lastBytes keeps at most limit trailing bytes of s, starting at a rune
// boundary, and reports whether it cut anything.
func lastBytes(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	cut := len(s) - limit
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return s[cut:], true
}
