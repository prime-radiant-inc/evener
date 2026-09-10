package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/server"
)

// TestServeShutdownAndClearPublishOneClosedBoundaryForOldIdentity exercises
// the real shutdown/clear ordering through a scripted session, the lossless
// event bridge, and the AppWire projection. A production clear keeps the
// stable workspace ref, so replacement is announced by resync while the old
// session contributes exactly one closed boundary.
func TestServeShutdownAndClearPublishOneClosedBoundaryForOldIdentity(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	firstDrained := make(chan struct{})
	bridgeCount := 0
	deps.bridge = func(s serveServer, sess *agent.Session, observer func(events.SessionEvent), onDrained func()) {
		bridgeCount++
		first := bridgeCount == 1
		go func() {
			sess.ConsumeEventsLossless(func(ev events.SessionEvent) {
				server.BridgeEvent(s.(*clearIdentityServer).Server, ev, observer)
			}, func() {
				onDrained()
				if first {
					close(firstDrained)
				}
			})
		}()
	}

	cleared := make(chan error, 1)
	deps.serveHTTP = func(*http.Server, net.Listener) error {
		old := state.session(0)
		state.srv.shutdown()
		select {
		case <-firstDrained:
		case <-time.After(30 * time.Second):
			cleared <- errors.New("old session did not drain")
			return http.ErrServerClosed
		}
		cleared <- state.srv.clear(context.Background(), appwire.ThreadClearParams{
			Ref: "local:" + old.ID(), ClientMutationID: "clear-after-shutdown", ExpectedInstanceID: old.ID(),
		})
		return http.ErrServerClosed
	}

	if err := runServeWithDeps(args, deps); err != nil {
		t.Fatalf("runServeWithDeps: %v", err)
	}
	if err := <-cleared; err != nil {
		t.Fatalf("clear after shutdown: %v", err)
	}
	old := state.session(0)
	newSession := state.session(1)
	if newSession == nil || newSession.State() != agent.SessionClosed {
		t.Fatalf("replacement session state=%v, want closed", newSession)
	}
	oldRef := "local:" + old.ID()
	closed, resync := 0, 0
	for _, record := range state.srv.AppNotificationsAfter(0, oldRef) {
		switch record.Notification.Method {
		case appwire.NotifyThreadClosed:
			closed++
		case appwire.NotifyEvenerThreadResync:
			resync++
		}
	}
	if closed != 1 || resync != 1 {
		t.Fatalf("old ref boundary=(closed:%d,resync:%d), want exactly one of each", closed, resync)
	}
}
