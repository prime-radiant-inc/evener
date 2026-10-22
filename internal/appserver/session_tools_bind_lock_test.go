package appserver

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// TestSessionToolsBindDoesNotBlockFrameReception pins that a slow binding
// installation cannot stop the connection's frame reader.
//
// bindSessionTools took sessionMu and held it across sessionToolsBind, whose
// context is the connection lifetime, not the request's. The hub's callback
// waits for the daemon to install the binding with no deadline. So a Manager
// daemon that could not install its binding parked every inbound frame on that
// connection until the connection itself closed — including reverse responses
// for unrelated in-flight work.
//
// The reservation is what makes this safe to fix: the slot is claimed under the
// lock, the wait happens outside it, and the commit or rollback rechecks that the
// reservation still holds.
func TestSessionToolsBindDoesNotBlockFrameReception(t *testing.T) {
	server := NewServer(ServerConfig{})
	installing := make(chan struct{})
	release := make(chan struct{})
	var installReturned bool
	server.InstallSessionTools(func(context.Context, appwire.SessionBinding, appwire.SessionHubClient) error {
		close(installing)
		<-release
		installReturned = true
		return nil
	})
	conn := server.NewConnection("slow-bind")
	conn.sessionLifetime = context.Background()
	conn.setInitialized()
	t.Cleanup(conn.closeSessionTools)

	done := make(chan struct{})
	go func() {
		defer close(done)
		request := appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodRuntimeSessionBind, appwire.SessionBinding{
			BindingID: "binding-slow", Ref: "local:one", InstanceID: "local:one", HubID: "hub-a",
		})
		_ = conn.bindSessionTools(context.Background(), *request.Request)
	}()
	select {
	case <-installing:
	case <-time.After(5 * time.Second):
		t.Fatal("bind never reached the install callback")
	}

	// An unrelated frame arrives while installation is still blocked. It must be
	// handled, not queued behind the lock.
	handled := make(chan bool, 1)
	go func() {
		handled <- conn.receiveSessionFrame(appwire.ResponseMessage(appwire.NewIntID(99), appwire.EmptyResponse{}))
	}()
	select {
	case <-handled:
	case <-time.After(2 * time.Second):
		t.Fatal("frame reception blocked behind an unfinished binding installation")
	}

	// The binding is not usable until its installation succeeds. A frame that
	// needed it must not be routed into an uninstalled binding. This connection
	// has no identified peer, so the binding commits as the connection's own.
	if conn.sessionTools != nil {
		t.Fatal("an uninstalled binding was routable before its installation finished")
	}

	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("bind never completed after its installation finished")
	}
	if !installReturned {
		t.Fatal("install callback did not run")
	}
	if conn.sessionTools == nil || conn.sessionTools.binding.BindingID != "binding-slow" {
		t.Fatal("completed binding was not installed")
	}
}
