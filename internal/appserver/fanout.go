package appserver

import (
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apphistory"
)

// notificationDelivery is one connection's routed copy of a recorded
// notification, with the content filter its subscription generation owns.
type notificationDelivery struct {
	connection *Connection
	content    *apphistory.Filter
}

// recordFanout is one recorded notification with every delivery routed for it.
type recordFanout struct {
	record     SequencedNotification
	deliveries []notificationDelivery
}

// notificationFanout carries one recorded notification's payload through the
// delivery gate. It decodes the payload at most once for the whole fanout and
// encodes at most one frame per distinct content selection, so a record with
// many subscribers at the same filter setting costs one encode rather than one
// per subscriber.
//
// The encode stays inside the delivery gate, deliberately. That gate is taken
// before projectionMu is released, and that handoff is what keeps each
// connection's enqueue order equal to the routing order; encoding before the
// gate would either widen projectionMu, which every capture also holds, or open
// a window where a later record's deliveries overtake this one. So the work is
// made once per distinct selection instead of once per subscriber, and no work
// moves to another lock or outside the gate.
//
// A fanout value lives exactly as long as the fanout it was built for, so it
// has nothing to invalidate: the next record builds its own.
type notificationFanout struct {
	notification appwire.Notification
	prepared     *apphistory.PreparedNotification
	frames       []notificationFrame
	encoded      int
}

// notificationFrame is one content selection's encoded result for one record.
type notificationFrame struct {
	selection    appwire.ThreadContentSelection
	notification appwire.Notification
	keep         bool
	err          error
}

// prepare decodes this record's payload once. A fanout whose subscribers hold
// no content filter never reaches here, so an unfiltered broadcast decodes
// nothing, as before.
func (f *notificationFanout) prepare() *apphistory.PreparedNotification {
	if f.prepared == nil {
		prepared := apphistory.PrepareNotification(f.notification)
		f.prepared = &prepared
	}
	return f.prepared
}

// frameFor returns the frame a subscriber with this selection receives,
// encoding it the first time any subscriber with that same selection asks.
// Encoding is a pure function of the payload and the selection — it reads no
// filter state — so every subscriber with the selection shares identical bytes
// and gets exactly what it would have been sent alone.
func (f *notificationFanout) frameFor(selection appwire.ThreadContentSelection) notificationFrame {
	for i := range f.frames {
		if f.frames[i].selection == selection {
			return f.frames[i]
		}
	}
	prepared := f.prepare()
	frame := notificationFrame{selection: selection}
	frame.notification, frame.keep, frame.err = prepared.Frame(selection)
	if frame.err == nil && frame.keep && prepared.Reencodes() {
		f.encoded++
	}
	f.frames = append(f.frames, frame)
	return frame
}

// deliverFanout runs with deliveryMu held, the gate that orders every
// connection's enqueues the way routing ordered the records. Its whole body is
// what the gate pays for one delivery batch, so it is also where the hold-time
// hook measures.
func (s *Server) deliverFanout(fanouts []recordFanout) []*Connection {
	measured := s.deliveryGateHeld != nil
	var started time.Time
	if measured {
		started = time.Now()
	}
	var disconnected []*Connection
	for i := range fanouts {
		fanout := notificationFanout{notification: fanouts[i].record.Notification}
		for _, delivery := range fanouts[i].deliveries {
			if s.afterBroadcastConnectionLookup != nil {
				s.afterBroadcastConnectionLookup(delivery.connection)
			}
			if !delivery.connection.enqueueFanout(&fanout, delivery.content) {
				disconnected = append(disconnected, delivery.connection)
			}
		}
		if s.afterFanoutEncoded != nil {
			s.afterFanoutEncoded(fanout.encoded)
		}
	}
	if measured {
		s.deliveryGateHeld(time.Since(started))
	}
	return disconnected
}

// enqueueHistory delivers one recorded notification to one connection through
// its own fanout: the one-subscriber form of deliverFanout, used by the
// hydration replay that releases a buffered subscription.
func (c *Connection) enqueueHistory(record SequencedNotification, filter *apphistory.Filter) bool {
	fanout := notificationFanout{notification: record.Notification}
	return c.enqueueFanout(&fanout, filter)
}

// enqueueFanout decides, encodes and enqueues this connection's copy of one
// recorded notification. Deciding and enqueueing are what the delivery gate
// orders; the encode is memoized on the fanout so a later subscriber at the
// same selection reuses the first one's bytes.
func (c *Connection) enqueueFanout(fanout *notificationFanout, filter *apphistory.Filter) bool {
	// A subscription with no content filter receives the recorded frame as it
	// was recorded and never decodes it.
	if filter == nil {
		return c.enqueue(appwire.Message{Notification: &fanout.notification})
	}
	// Decide is the one per-subscription step: it teaches the filter the
	// overlay identities a later delta depends on, so it runs in delivery
	// order, under the gate.
	prepared := fanout.prepare()
	keep, err := filter.Decide(prepared)
	if err != nil {
		c.server.logf("history notification selection: %v", err)
		return false
	}
	if !keep {
		return true
	}
	frame := fanout.frameFor(filter.Selection())
	if frame.err != nil {
		c.server.logf("history notification selection: %v", frame.err)
		return false
	}
	if !frame.keep {
		return true
	}
	notification := frame.notification
	return c.enqueue(appwire.Message{Notification: &notification})
}
