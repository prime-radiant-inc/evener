package server

import (
	"sync"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// The pulse meter (spec 16.4) draws seven one-minute bars. The meter keeps
// ten-second slots so each bar is a minute that ends when the row is listed,
// rather than a clock minute that starts empty every sixty seconds.
const (
	activitySlotSeconds = 10
	activitySlotsPerBar = 6
	activityBars        = 7
	activitySlotCount   = activityBars * activitySlotsPerBar
)

// activitySlot is one ten-second slot of the ring. index is the slot's absolute
// number (Unix seconds divided by activitySlotSeconds), so a slot left over from
// an earlier lap of the ring reads as empty.
type activitySlot struct {
	index int64
	count int
}

// activityMeter counts one root session tree's transcript motion for the pulse
// meter and the Quiet and May be stuck labels (spec 13.1, 16.4). The root and
// every in-process descendant feed it, so a coordinator's meter shows its whole
// tree (Jesse's ruling for S5). One fact on the sample is not tree-wide: the
// row's intent is the root session's own words, because the row names the root
// (noteIntent).
//
// RecordAppEvent and RecordDescendantAppEvent feed it the raw session event
// each call projects, inside their projection commits, and the thread list
// reads it; all three hold Server.mu. Observing the event itself, rather than
// the AppWire notification(s) it turns into, keeps the meter working the same
// way whether or not the thread has a transcript-backed history yet (phase 3's
// history/overlay notifications only exist once one is attached), and it is
// simpler: one session event is exactly one unit of transcript motion,
// independent of how many wire notifications a reader is told about it. It is
// not part of threadEnvelope: the envelope samples a fixed set of events and
// never a delta (facetsByEvent in thread_envelope.go), and the meter has to
// see every delta.
type activityMeter struct {
	now func() time.Time

	mu    sync.Mutex
	slots [activitySlotCount]activitySlot
	// servedAt is when restart last ran, and lastMoved the newest motion since
	// then (zero until the tree moves). The quiet clock reads the later of the
	// two; the moved time reads lastMoved alone, since a daemon beginning to
	// serve a session is not news to someone comparing it with what they last
	// saw.
	servedAt  time.Time
	lastMoved time.Time
	// intent is the newest intent a tool call of the meter's own root session
	// stated (noteIntent), empty until this turn states one.
	intent string
}

func (m *activityMeter) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// restart empties the meter and starts its quiet clock now. It runs when an
// identity is installed: a replaced identity is a different session, and a
// session that has not moved since its daemon began serving it has been quiet
// since then.
func (m *activityMeter) restart() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.slots = [activitySlotCount]activitySlot{}
	m.servedAt = m.clock()
	m.lastMoved = time.Time{}
	m.intent = ""
}

// observe records one session event. An item finishing (a user message, an
// assistant message, a tool call, or a delivered communicate) and a tool
// writing output each count toward the current bar ("transcript items and
// tool output events", spec 16.4). A turn or item starting and a message or
// reasoning summary streaming prove the tree is moving without finishing
// anything, so they only advance the quiet clock. Everything else (session
// lifecycle, environment, retries, tasks, goals, notes, jobs, delegates,
// notices and every other bookkeeping event) moves neither: a retry loop that
// makes no progress should read May be stuck.
func (m *activityMeter) observe(kind events.EventKind) {
	switch kind {
	case events.EventUserInput, events.EventAssistantTextEnd, events.EventToolCallEnd,
		events.EventCommunicate, events.EventToolCallOutputDelta:
		at := m.clock()
		m.mu.Lock()
		m.count(at)
		m.touch(at)
		m.mu.Unlock()
	case events.EventExecutionStarted, events.EventToolCallStart, events.EventAssistantTextStart,
		events.EventAssistantTextDelta, events.EventReasoningSummaryDelta:
		m.mu.Lock()
		m.touch(m.clock())
		m.mu.Unlock()
	}
}

// count and touch run under m.mu.
func (m *activityMeter) count(at time.Time) {
	index := at.Unix() / activitySlotSeconds
	slot := &m.slots[index%activitySlotCount]
	if slot.index != index {
		*slot = activitySlot{index: index}
	}
	slot.count++
}

func (m *activityMeter) touch(at time.Time) {
	if at.After(m.lastMoved) {
		m.lastMoved = at
	}
}

// noteIntent records what the meter's own root session last set out to do: the
// intent of the newest tool call it started, and nothing at all when a turn
// begins, so a row names this turn's work rather than the words of a turn that
// finished an hour ago. The agent promotes each tool call's intent to its
// event's description (session_tools.go). A call that states none leaves the
// line before it standing; blanking the row mid-turn would say less.
//
// Only RecordAppEvent calls this. The meter counts an in-process descendant's
// motion too, because a row's meter shows its whole tree, but a row names the
// root: a subagent's tool calls must not put their words on it.
func (m *activityMeter) noteIntent(event events.SessionEvent) {
	if event.Kind == events.EventExecutionStarted {
		m.mu.Lock()
		m.intent = ""
		m.mu.Unlock()
		return
	}
	if event.Kind != events.EventToolCallStart {
		return
	}
	start, ok := event.Data.(events.ToolCallStartData)
	if !ok {
		return
	}
	intent := appwire.Excerpt(start.Description, appwire.MaxIntentRunes)
	if intent == "" {
		return
	}
	m.mu.Lock()
	m.intent = intent
	m.mu.Unlock()
}

// snapshot reads the meter for a thread list row: seven bars, oldest first, the
// last ending in the current slot, and the time of the newest motion. It is nil
// until an identity has started the meter.
func (m *activityMeter) snapshot() *appwire.ThreadActivity {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.servedAt.IsZero() {
		return nil
	}
	current := m.clock().Unix() / activitySlotSeconds
	minutes := make([]int, activityBars)
	for _, slot := range m.slots {
		// A slot's age in slots picks its bar, the newest last. A slot a whole
		// lap old or more, or ahead of a clock that stepped back, counts for
		// nothing.
		if age := current - slot.index; age >= 0 && age < activitySlotCount {
			minutes[activityBars-1-age/activitySlotsPerBar] += slot.count
		}
	}
	lastMotion := m.servedAt
	if m.lastMoved.After(lastMotion) {
		lastMotion = m.lastMoved
	}
	activity := &appwire.ThreadActivity{Minutes: minutes, LastActivityAt: lastMotion.UnixMilli(), LatestIntent: m.intent}
	if !m.lastMoved.IsZero() {
		activity.LastMovedAt = m.lastMoved.UnixMilli()
	}
	return activity
}
