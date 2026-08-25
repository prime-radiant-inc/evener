package appsource

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestRelaySessionLeaseReadNil covers the nil lease path in
// relaySessionLease.Read (lines 125-127).
func TestRelaySessionLeaseReadNil(t *testing.T) {
	var lease *relaySessionLease
	_, err := lease.Read(context.Background(), appwire.ThreadReadParams{})
	if err == nil {
		t.Fatal("nil lease Read should return error")
	}
}

// TestRelaySessionLeaseReadNilSession covers the nil session path.
func TestRelaySessionLeaseReadNilSession(t *testing.T) {
	lease := &relaySessionLease{}
	_, err := lease.Read(context.Background(), appwire.ThreadReadParams{})
	if err == nil {
		t.Fatal("nil session Read should return error")
	}
}

// TestRelaySessionLeaseListenNil covers the nil lease path in
// relaySessionLease.Listen (lines 132-134).
func TestRelaySessionLeaseListenNil(t *testing.T) {
	var lease *relaySessionLease
	_, err := lease.Listen(context.Background())
	if err == nil {
		t.Fatal("nil lease Listen should return error")
	}
}

// TestRelaySessionLeaseListenNilSession covers the nil session path.
func TestRelaySessionLeaseListenNilSession(t *testing.T) {
	lease := &relaySessionLease{}
	_, err := lease.Listen(context.Background())
	if err == nil {
		t.Fatal("nil session Listen should return error")
	}
}

// TestRelaySessionLeaseCloseNil covers the nil lease path in
// relaySessionLease.Close (lines 139-141).
func TestRelaySessionLeaseCloseNil(t *testing.T) {
	var lease *relaySessionLease
	// Should not panic
	lease.Close()
}

// TestRelaySessionLeaseCloseNilSession covers the nil session path.
func TestRelaySessionLeaseCloseNilSession(t *testing.T) {
	lease := &relaySessionLease{}
	// Should not panic
	lease.Close()
}

// TestForwardLocalDaemonNotificationDelivered covers the successful delivery
// path.
func TestForwardLocalDaemonNotificationDeliveredGaps(t *testing.T) {
	ctx := context.Background()
	out := make(chan appwire.Notification, 1)
	forwardLocalDaemonNotification(ctx, out, appwire.Notification{Method: "test"})
	if len(out) != 1 {
		t.Fatal("notification should be delivered")
	}
}

// TestRelaySessionListenCancelledContext covers the ctx.Err() early return
// path in relaySession.listen (lines 148-149).
func TestRelaySessionListenCancelledContext(t *testing.T) {
	s := newRelaySessionForTest()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.listen(ctx, 1)
	if err == nil {
		t.Fatal("listen with cancelled context should return error")
	}
}

// TestRelaySessionListenClosedSession covers the closed session path
// (lines 152-154).
func TestRelaySessionListenClosedSession(t *testing.T) {
	s := newRelaySessionForTest()
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	_, err := s.listen(context.Background(), 1)
	if err == nil {
		t.Fatal("listen on closed session should return error")
	}
}

// TestRelaySessionListenUnknownLease covers the unknown lease path
// (lines 152-154).
func TestRelaySessionListenUnknownLease(t *testing.T) {
	s := newRelaySessionForTest()
	_, err := s.listen(context.Background(), 999)
	if err == nil {
		t.Fatal("listen with unknown lease should return error")
	}
}

// TestRelaySessionReadCancelledContext covers the ctx.Done() path in
// relaySession.read (lines 204-205).
func TestRelaySessionReadCancelledContext(t *testing.T) {
	s := newRelaySessionForTest()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.read(ctx, appwire.ThreadReadParams{})
	if err == nil {
		t.Fatal("read with cancelled context should return error")
	}
}

// TestRelaySessionReadSessionClosed covers the s.ctx.Done() path (line 207).
func TestRelaySessionReadSessionClosed(t *testing.T) {
	s := newRelaySessionForTest()
	s.cancel()
	_, err := s.read(context.Background(), appwire.ThreadReadParams{})
	if err == nil {
		t.Fatal("read on closed session should return error")
	}
}

// TestRelaySessionEnsureConnectionCancelledContext covers the ctx.Err() path
// in ensureConnection (lines 279-280).
func TestRelaySessionEnsureConnectionCancelledContext(t *testing.T) {
	s := newRelaySessionForTest()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.ensureConnection(ctx)
	if err == nil {
		t.Fatal("ensureConnection with cancelled context should return error")
	}
}

// TestRelaySessionReleaseLeaseUnknown covers the unknown lease path in
// releaseLease (lines 697-699).
func TestRelaySessionReleaseLeaseUnknown(t *testing.T) {
	s := newRelaySessionForTest()
	// Releasing a non-existent lease should be a no-op
	s.releaseLease(999)
}

// TestRelaySessionReleaseLeaseKnown covers releasing a known lease.
func TestRelaySessionReleaseLeaseKnown(t *testing.T) {
	s := newRelaySessionForTest()
	s.mu.Lock()
	lease := &relaySessionLease{session: s, id: 1}
	s.leases[1] = lease
	s.mu.Unlock()
	s.releaseLease(1)
	s.mu.Lock()
	if s.leases[1] != nil {
		t.Fatal("lease should be removed after release")
	}
	s.mu.Unlock()
}

// TestRelaySessionMaybeIdleActiveLease covers the path where maybeIdle returns
// without closing because there are active leases (line 722).
func TestRelaySessionMaybeIdleActiveLease(t *testing.T) {
	s := newRelaySessionForTest()
	s.mu.Lock()
	s.leases[1] = &relaySessionLease{session: s, id: 1}
	s.mu.Unlock()
	s.maybeIdle()
	s.mu.Lock()
	if s.closed {
		t.Fatal("session should not close with active lease")
	}
	s.mu.Unlock()
}

// TestRelaySessionMaybeIdleCommandOwner covers the path where maybeIdle
// returns without closing because there are command owners.
func TestRelaySessionMaybeIdleCommandOwner(t *testing.T) {
	s := newRelaySessionForTest()
	s.mu.Lock()
	s.commandOwners = 1
	s.mu.Unlock()
	s.maybeIdle()
	s.mu.Lock()
	if s.closed {
		t.Fatal("session should not close with active command owner")
	}
	s.mu.Unlock()
}

// TestRelaySessionMaybeIdleWithCapture covers the path where maybeIdle returns
// without closing because there is an active capture.
func TestRelaySessionMaybeIdleWithCapture(t *testing.T) {
	s := newRelaySessionForTest()
	s.mu.Lock()
	s.capture = &relayCapture{}
	s.mu.Unlock()
	s.maybeIdle()
	s.mu.Lock()
	if s.closed {
		t.Fatal("session should not close with active capture")
	}
	s.mu.Unlock()
}

// TestRelaySessionMaybeIdleCloses covers the path where maybeIdle closes the
// session when nothing is active.
func TestRelaySessionMaybeIdleCloses(t *testing.T) {
	s := newRelaySessionForTest()
	s.maybeIdle()
	s.mu.Lock()
	if !s.closed {
		t.Fatal("session should close when nothing is active")
	}
	s.mu.Unlock()
}

// TestRelayListenerClose covers the relayListener.close method.
func TestRelayListenerClose(t *testing.T) {
	l := &relayListener{done: make(chan struct{})}
	l.close()
	select {
	case <-l.done:
	default:
		t.Fatal("close should close the done channel")
	}
	// Double close should not panic
	l.close()
}

// TestRelaySessionRemoveListenerUnknown covers removeListener with an unknown
// id.
func TestRelaySessionRemoveListenerUnknown(t *testing.T) {
	s := newRelaySessionForTest()
	// Should not panic
	s.removeListener(999)
}

// TestRelaySessionRemoveListenerKnown covers removeListener with a known
// listener.
func TestRelaySessionRemoveListenerKnown(t *testing.T) {
	s := newRelaySessionForTest()
	l := &relayListener{id: 1, done: make(chan struct{})}
	s.mu.Lock()
	s.listeners[1] = l
	s.mu.Unlock()
	s.removeListener(1)
	s.mu.Lock()
	if s.listeners[1] != nil {
		t.Fatal("listener should be removed")
	}
	s.mu.Unlock()
	select {
	case <-l.done:
	default:
		t.Fatal("listener should be closed")
	}
}

// newRelaySessionForTest creates a relaySession for unit testing.
func newRelaySessionForTest() *relaySession {
	ctx, cancel := context.WithCancel(context.Background())
	return &relaySession{
		ctx:         ctx,
		cancel:      cancel,
		leases:      map[uint64]*relaySessionLease{},
		listeners:   map[uint64]*relayListener{},
		commandGate: make(chan struct{}, 1),
		// Pre-fill the command gate so read() can proceed past the gate
	}
}
