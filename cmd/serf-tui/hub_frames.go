package main

import (
	"sync"

	"primeradiant.com/serf/appwire"
)

// hubFrameFeed restores the hub connection's wire order for the two feeds the
// TUI folds into one model.
//
// A thread/read with subscribe:true is a cut: the hub captures the snapshot and
// takes the notifier cut in one projection transition, so the response carries
// every record the source had projected and every frame after it is new.
// appwire.Client keeps that order only for a client that asks for it — a
// response is correlated to the requesting goroutine while notifications go to
// a shared channel, and the two reach bubbletea from different goroutines with
// no ordering between them. A post-cut frame folded first is then destroyed by
// the replaceSessionTranscript that follows it: it is in neither the snapshot
// (it is post-cut) nor the transcript (it was replaced).
//
// The feed observes both kinds of frame in the receive loop
// (Client.SetOrderedFrameHandler) and owns notification delivery from there. A
// read that replaces the transcript opens a capture around itself; the feed
// then holds this connection's notifications and hands each side back to the
// side of the snapshot it belongs on.
type hubFrameFeed struct {
	// notifications is sized like appwire's own buffer, because it now stands
	// in the same place: the whole burst a source can push while the model
	// waits for a scheduling slice rides on it.
	notifications  chan appwire.Notification
	mu             sync.Mutex
	capture        *hubReadCapture
	closed         bool
	closeTransport func() error
}

// hubReadCapture is one transcript-replacing read's hold on the feed.
type hubReadCapture struct {
	feed *hubFrameFeed
	// id names the response frame that is this read's cut. The connection
	// carries concurrent requests, so every other response in the feed is
	// somebody else's.
	id      string
	cutSeen bool
	done    bool
	before  []appwire.Notification
	after   []appwire.Notification
}

func newHubFrameFeed() *hubFrameFeed {
	return &hubFrameFeed{notifications: make(chan appwire.Notification, appwire.NotificationBufferCap)}
}

// Notifications is what the model waits on. A nil feed never delivers, which is
// what a model with no hub connection wants.
func (f *hubFrameFeed) Notifications() <-chan appwire.Notification {
	if f == nil {
		return nil
	}
	return f.notifications
}

// SetTransportCloser names the connection the feed tears down when it
// overflows, mirroring what appwire does with its own buffer: a loud failure
// rather than a silent drop.
func (f *hubFrameFeed) SetTransportCloser(closeTransport func() error) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeTransport = closeTransport
}

// Observe is the appwire ordered-frame handler. It runs on the receive loop, so
// it must never block.
func (f *hubFrameFeed) Observe(message appwire.Message, err error) {
	if f == nil {
		return
	}
	f.mu.Lock()
	switch {
	case err != nil:
		// The connection is gone. Nothing else will arrive, so hand back
		// whatever a capture was still holding before ending the feed the way
		// appwire ends its own notification channel.
		f.emitLocked(f.abandonCaptureLocked())
		f.closeLocked()
	case message.Notification != nil:
		if f.capture != nil {
			f.capture.holdLocked(*message.Notification)
			break
		}
		f.emitLocked([]appwire.Notification{*message.Notification})
	case f.capture != nil && !f.capture.cutSeen && f.capture.id != "" && f.capture.id == message.IDString():
		f.capture.cutSeen = true
	}
	teardown := f.teardownLocked()
	f.mu.Unlock()
	if teardown != nil {
		_ = teardown()
	}
}

// BeginCapture starts holding this connection's notifications around a read
// whose response replaces the transcript. One capture is open at a time: a
// newer read supersedes an older one and inherits everything it still holds,
// because every one of those frames precedes the newer read's own cut. That is
// the web store's rule too — a newer hydration takes over the older one's
// buffer (transferPendingHydration in stores/threads.ts).
//
// Every frame is held, not only the read's own thread's: this feed's order is
// the connection's order, and holding one thread's frames while letting another
// thread's through would reorder frames the source committed in sequence. The
// hold lasts one read round trip.
func (f *hubFrameFeed) BeginCapture() *hubReadCapture {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	capture := &hubReadCapture{feed: f}
	if f.capture != nil {
		capture.before = f.capture.takeLocked()
	}
	f.capture = capture
	return capture
}

// CutOn names the response frame that closes this capture. Called before the
// request is sent, so the frame cannot be observed first.
func (c *hubReadCapture) CutOn(id appwire.ID) {
	if c == nil {
		return
	}
	c.feed.mu.Lock()
	defer c.feed.mu.Unlock()
	c.id = id.String()
}

// BeforeCut takes the frames the connection delivered ahead of the read's
// response. They precede the snapshot on the wire and belong under it: the
// snapshot's projection already carries their transcript records, but nothing
// else they do — a further resync, an escalation, a hub-wide panel refresh — is
// represented by it at all, so they are folded rather than dropped.
func (c *hubReadCapture) BeforeCut() []appwire.Notification {
	if c == nil {
		return nil
	}
	c.feed.mu.Lock()
	defer c.feed.mu.Unlock()
	if c.done || c.feed.capture != c {
		return nil
	}
	before := c.before
	c.before = nil
	return before
}

// Release hands back the frames the connection delivered after the read's
// response. The caller applies them on top of the snapshot, which is where the
// source committed them.
func (c *hubReadCapture) Release() {
	c.finish()
}

// Abandon ends a capture whose read never produced a snapshot. Everything held
// goes back to the feed in wire order: with no snapshot to subsume them, these
// frames are all the model is going to get.
func (c *hubReadCapture) Abandon() {
	c.finish()
}

func (c *hubReadCapture) finish() {
	if c == nil {
		return
	}
	feed := c.feed
	feed.mu.Lock()
	if !c.done && feed.capture == c {
		feed.emitLocked(c.takeLocked())
		feed.capture = nil
	}
	c.done = true
	teardown := feed.teardownLocked()
	feed.mu.Unlock()
	if teardown != nil {
		_ = teardown()
	}
}

func (c *hubReadCapture) holdLocked(notification appwire.Notification) {
	if c.cutSeen {
		c.after = append(c.after, notification)
		return
	}
	c.before = append(c.before, notification)
}

// takeLocked empties the capture, in wire order.
func (c *hubReadCapture) takeLocked() []appwire.Notification {
	held := make([]appwire.Notification, 0, len(c.before)+len(c.after))
	held = append(append(held, c.before...), c.after...)
	c.before, c.after = nil, nil
	return held
}

func (f *hubFrameFeed) abandonCaptureLocked() []appwire.Notification {
	if f.capture == nil {
		return nil
	}
	held := f.capture.takeLocked()
	f.capture.done = true
	f.capture = nil
	return held
}

// emitLocked delivers to the model without ever blocking the receive loop. A
// full buffer is the same loud failure appwire makes of its own: the connection
// is torn down rather than dropping frames nobody will ever ask for again.
func (f *hubFrameFeed) emitLocked(notifications []appwire.Notification) {
	for _, notification := range notifications {
		if f.closed {
			return
		}
		select {
		case f.notifications <- notification:
		default:
			f.closeLocked()
			return
		}
	}
}

func (f *hubFrameFeed) closeLocked() {
	if f.closed {
		return
	}
	f.closed = true
	close(f.notifications)
}

// teardownLocked reports the connection closer once, after the feed has ended,
// so the caller can run it with the lock released.
func (f *hubFrameFeed) teardownLocked() func() error {
	if !f.closed || f.closeTransport == nil {
		return nil
	}
	closeTransport := f.closeTransport
	f.closeTransport = nil
	return closeTransport
}
