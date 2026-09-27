package server

import (
	"sync"
	"time"

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
// RecordAppEvent and RecordDescendantAppEvent feed it the overlay changes they
// publish, inside their projection commits, and history's own publish hook
// (commitHistoryNotification) feeds it a recorded item or turn finishing; the
// thread list reads it. Those three sources run under different locks (the
// event commits hold Server.mu, the history publish hook does not), so the
// meter guards its own state with its own mutex rather than Server.mu. It is
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

// observe records one published AppWire notification. A recorded item or turn
// finishing (history/updated) and a tool writing output (overlay/delta on the
// output field) each count toward the current bar ("transcript items and tool
// output events", spec 16.4). An item starting (overlay/upserted) and a
// message or reasoning summary streaming (overlay/delta on the text field)
// prove the tree is moving without finishing anything, so they only advance
// the quiet clock. Everything else (status, queue, usage, task, goal, job and
// retry notifications, overlay/reset, overlay/end) is bookkeeping and moves
// neither: a retry loop that makes no progress should read May be stuck.
func (m *activityMeter) observe(method string, params any) {
	switch method {
	case appwire.NotifyHistoryUpdated:
		at := m.clock()
		m.mu.Lock()
		m.count(at)
		m.touch(at)
		m.mu.Unlock()
	case appwire.NotifyOverlayDelta:
		delta, ok := params.(appwire.OverlayDeltaParams)
		at := m.clock()
		m.mu.Lock()
		if ok && delta.Field == appwire.OverlayDeltaOutput {
			m.count(at)
		}
		m.touch(at)
		m.mu.Unlock()
	case appwire.NotifyOverlayUpserted:
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
