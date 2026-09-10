package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	rvreg "primeradiant.com/evener/cmd/evener/internal/rvreg"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/server"
)

// TestCloseSupersededSessionForShutdownPreservesTerminalBoundary exercises
// the close ownership decision with a real session and its lossless event
// stream. An interrupted turn has already consumed the ordinary close path;
// the shutdown-owned close must still publish SESSION_END(closed).
func TestCloseSupersededSessionForShutdownPreservesTerminalBoundary(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	var releaseOnce sync.Once
	adapter := &shutdownBlockingAdapter{
		entered:   make(chan struct{}, 1),
		cancelled: make(chan struct{}, 1),
		release:   release,
	}
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := agent.NewSession(client, provider.NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(t.TempDir()), agent.SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		sess.Close()
	})

	var mu sync.Mutex
	var received []events.SessionEvent
	drained := make(chan struct{})
	sess.ConsumeEventsLossless(func(ev events.SessionEvent) {
		mu.Lock()
		received = append(received, ev)
		mu.Unlock()
	}, func() { close(drained) })
	turnCtx, cancelTurn := context.WithCancel(t.Context())
	t.Cleanup(cancelTurn)
	done := make(chan error, 1)
	go func() {
		_, processErr := sess.ProcessInput(turnCtx, "hello", nil)
		done <- processErr
	}()
	<-adapter.entered
	cancelTurn()
	<-adapter.cancelled
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("ProcessInput did not return after per-turn cancel")
	}

	closeSupersededSession(sess, true)
	select {
	case <-drained:
	case <-t.Context().Done():
		t.Fatal("session events did not drain after shutdown close")
	}

	mu.Lock()
	defer mu.Unlock()
	interrupted, closed := 0, 0
	for _, ev := range received {
		if ev.Kind != events.EventSessionEnd {
			continue
		}
		end, ok := ev.Data.(events.SessionEndData)
		if !ok {
			continue
		}
		if end.Reason == "interrupted" {
			interrupted++
		}
		if end.State == string(agent.SessionClosed) {
			closed++
		}
	}
	if interrupted != 1 || closed != 1 {
		t.Fatalf("session ends=(interrupted:%d,closed:%d), want exactly one of each: %+v", interrupted, closed, received)
	}
}

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
				if first && ev.Kind == events.EventSessionEnd {
					state.record("session-end-projected")
				}
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

// TestServeClearWaitsForOldSessionEndBeforeSwappingIdentity drives the bridge
// at the point where the old session's SESSION_END has been received but not
// projected. Clear must remain behind that drain: otherwise it can install a
// replacement identity while the old terminal event is still in flight.
func TestServeClearWaitsForOldSessionEndBeforeSwappingIdentity(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	sessionEndReceived := make(chan struct{})
	releaseSessionEnd := make(chan struct{})
	firstDrained := make(chan struct{})
	var bridgeCount int
	var bridgeMu sync.Mutex
	deps.bridge = func(s serveServer, sess *agent.Session, observer func(events.SessionEvent), onDrained func()) {
		bridgeMu.Lock()
		bridgeCount++
		first := bridgeCount == 1
		bridgeMu.Unlock()
		sess.ConsumeEventsLossless(func(ev events.SessionEvent) {
			if first && ev.Kind == events.EventSessionEnd {
				close(sessionEndReceived)
				<-releaseSessionEnd
			}
			server.BridgeEvent(s.(*clearIdentityServer).Server, ev, observer)
			if first && ev.Kind == events.EventSessionEnd {
				state.record("session-end-projected")
			}
		}, func() {
			onDrained()
			if first {
				close(firstDrained)
			}
		})
	}

	clearReachedRendezvous := make(chan struct{})
	updateSessionID := deps.updateSessionID
	deps.updateSessionID = func(reg *rvreg.Registration, id string) error {
		err := updateSessionID(reg, id)
		if err == nil {
			close(clearReachedRendezvous)
		}
		return err
	}
	clearDone := make(chan error, 1)
	clearStepStart := 0
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseSessionEnd) }) }
	deps.serveHTTP = func(*http.Server, net.Listener) error {
		defer release()
		state.srv.shutdown()
		select {
		case <-sessionEndReceived:
		case <-t.Context().Done():
			return t.Context().Err()
		}
		old := state.session(0)
		clearStepStart = len(state.recorded())
		go func() {
			clearDone <- state.srv.clear(context.Background(), appwire.ThreadClearParams{
				Ref: "local:" + old.ID(), ClientMutationID: "clear-before-old-end", ExpectedInstanceID: old.ID(),
			})
		}()
		select {
		case <-clearReachedRendezvous:
		case <-t.Context().Done():
			return t.Context().Err()
		}
		release()
		select {
		case err := <-clearDone:
			if err != nil {
				t.Errorf("clear after old SESSION_END projection: %v", err)
			}
		case <-t.Context().Done():
			return t.Context().Err()
		}
		select {
		case <-firstDrained:
		case <-t.Context().Done():
			return t.Context().Err()
		}
		return http.ErrServerClosed
	}

	if err := runServeWithDeps(args, deps); err != nil {
		t.Fatalf("runServeWithDeps: %v", err)
	}
	steps := state.recorded()[clearStepStart:]
	projected, replaced := -1, -1
	for i, step := range steps {
		if step == "session-end-projected" && projected < 0 {
			projected = i
		}
		if step == "replace" && replaced < 0 {
			replaced = i
		}
	}
	if projected < 0 || replaced < 0 || projected > replaced {
		t.Fatalf("clear ordering=%v, want old SESSION_END projection before identity replace", steps)
	}
	if replacement := state.session(1); replacement == nil || replacement.State() != agent.SessionClosed {
		if replacement == nil {
			t.Fatal("clear did not install a replacement session")
		}
		t.Fatalf("replacement session state=%q, want closed", replacement.State())
	}
	old := state.session(0)
	var methods []string
	for _, record := range state.srv.AppNotificationsAfter(0, "local:"+old.ID()) {
		if method := record.Notification.Method; method == appwire.NotifyThreadClosed || method == appwire.NotifyEvenerThreadResync {
			methods = append(methods, method)
		}
	}
	want := []string{appwire.NotifyThreadClosed, appwire.NotifyEvenerThreadResync}
	if len(methods) != len(want) || methods[0] != want[0] || methods[1] != want[1] {
		t.Fatalf("old identity notifications=%v, want exactly closed then resync", methods)
	}
}
