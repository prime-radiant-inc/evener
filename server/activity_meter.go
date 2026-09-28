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
// tree (Jesse's ruling for S5).
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

	mu         sync.Mutex
	slots      [activitySlotCount]activitySlot
	lastMotion time.Time
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
	m.lastMotion = m.clock()
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
	if at.After(m.lastMotion) {
		m.lastMotion = at
	}
}

// snapshot reads the meter for a thread list row: seven bars, oldest first, the
// last ending in the current slot, and the time of the newest motion. It is nil
// until an identity has started the meter.
func (m *activityMeter) snapshot() *appwire.ThreadActivity {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastMotion.IsZero() {
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
	return &appwire.ThreadActivity{Minutes: minutes, LastActivityAt: m.lastMotion.UnixMilli()}
}
