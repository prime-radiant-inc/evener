package appserver

import (
	"context"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// TestSessionToolsBindDoesNotBlockFrameReception pins that a slow binding
// installation cannot stop the connection's frame reader.
//
// The slot is claimed under sessionMu, but the callback waits outside it. The
// connection lifetime bounds installation, not an individual request deadline.
func TestSessionToolsBindDoesNotBlockFrameReception(t *testing.T) {
	server := NewServer(ServerConfig{})
	installing := make(chan struct{})
	release := make(chan struct{})
	releaseInstall := sync.OnceFunc(func() { close(release) })
	defer releaseInstall()
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

	releaseInstall()
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

func TestSessionToolsFailedBindReleasesReservation(t *testing.T) {
	server := NewServer(ServerConfig{})
	var failed context.Context
	server.InstallSessionTools(func(ctx context.Context, _ appwire.SessionBinding, _ appwire.SessionHubClient) error {
		if failed == nil {
			failed = ctx
			return appwire.Unavailable("installation refused")
		}
		return nil
	})
	conn := server.NewConnection("retry-bind")
	conn.sessionLifetime = t.Context()
	t.Cleanup(conn.closeSessionTools)
	request := appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodRuntimeSessionBind, appwire.SessionBinding{
		BindingID: "binding", Ref: "local:one", InstanceID: "instance", HubID: "hub-a",
	})
	result := conn.bindSessionTools(t.Context(), *request.Request)
	if result.Error == nil || result.Error.Error.Code != appwire.CodeUnavailable {
		t.Fatalf("failed installation = %+v, want unavailable", result)
	}
	if failed.Err() == nil || conn.sessionTools != nil || conn.sessionToolsInstalling != nil {
		t.Fatal("failed installation retained its context or binding slot")
	}
	result = conn.bindSessionTools(t.Context(), *request.Request)
	if result.Error != nil || conn.sessionTools == nil || conn.sessionToolsInstalling != nil {
		t.Fatalf("retry did not install a single binding: %+v", result)
	}
}

func TestSessionToolsCloseCancelsInFlightBind(t *testing.T) {
	server := NewServer(ServerConfig{})
	installing := make(chan context.Context, 1)
	release := make(chan struct{})
	releaseInstall := sync.OnceFunc(func() { close(release) })
	defer releaseInstall()
	server.InstallSessionTools(func(ctx context.Context, _ appwire.SessionBinding, _ appwire.SessionHubClient) error {
		installing <- ctx
		<-release
		return nil
	})
	conn := server.NewConnection("close-bind")
	conn.sessionLifetime = t.Context()
	t.Cleanup(conn.closeSessionTools)
	done := make(chan appwire.Message, 1)
	go func() {
		request := appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodRuntimeSessionBind, appwire.SessionBinding{
			BindingID: "binding", Ref: "local:one", InstanceID: "instance", HubID: "hub-a",
		})
		done <- conn.bindSessionTools(t.Context(), *request.Request)
	}()
	var installCtx context.Context
	select {
	case installCtx = <-installing:
	case <-time.After(5 * time.Second):
		t.Fatal("bind never reached the install callback")
	}
	conn.closeSessionTools()
	if installCtx.Err() == nil {
		t.Fatal("connection close did not cancel the installation")
	}
	releaseInstall()
	select {
	case result := <-done:
		if result.Error == nil || result.Error.Error.Code != appwire.CodeConflict {
			t.Fatalf("abandoned installation = %+v, want conflict", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bind never returned after the connection closed")
	}
	if conn.sessionTools != nil || conn.sessionToolsInstalling != nil {
		t.Fatal("abandoned installation restored the closed connection's binding")
	}
}
