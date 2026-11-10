package appserver

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// The installation wait must release the reader's lock without letting another
// bind or peer identification claim the connection before it commits.
func TestSessionToolsConcurrentBindsStayExclusive(t *testing.T) {
	server := NewServer(ServerConfig{})
	installing := make(chan struct{})
	release := make(chan struct{})
	releaseInstall := sync.OnceFunc(func() { close(release) })
	defer releaseInstall()
	var installs atomic.Int32
	server.InstallSessionTools(func(context.Context, appwire.SessionBinding, appwire.SessionHubClient) error {
		// Only the first installation blocks. The second is the bind that must be
		// refused before installing at all, so if it ever reaches here the test
		// should not hang on it.
		if installs.Add(1) > 1 {
			return nil
		}
		close(installing)
		<-release
		return nil
	})
	conn := server.NewConnection("concurrent-bind")
	conn.sessionLifetime = context.Background()
	conn.setInitialized()
	t.Cleanup(conn.closeSessionTools)

	localDone := make(chan appwire.Message, 1)
	go func() {
		request := appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodRuntimeSessionBind, appwire.SessionBinding{
			BindingID: "local-binding", Ref: "local:one", InstanceID: "local:one", HubID: "hub-a",
		})
		localDone <- conn.bindSessionTools(context.Background(), *request.Request)
	}()
	select {
	case <-installing:
	case result := <-localDone:
		t.Fatalf("local bind returned before installing: %+v", result.Error)
	case <-time.After(5 * time.Second):
		t.Fatal("local bind never reached the install callback")
	}

	// The pending bind already owns the single slot, before it is routable.
	localCtx := context.WithValue(context.Background(), connectionContextKey{}, conn)
	err := IdentifySessionToolsPeer(localCtx, "hub-a")
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("identify during local binding installation = %v, want conflict", err)
	}
	if got := SessionToolsPeerID(localCtx); got != "" {
		t.Fatalf("refused identification installed peer identity %q", got)
	}
	secondRequest := appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodRuntimeSessionBind, appwire.SessionBinding{
		BindingID: "second-binding", Ref: "local:two", InstanceID: "local:two", HubID: "hub-a",
	})
	second := conn.bindSessionTools(localCtx, *secondRequest.Request)
	if second.Error == nil || second.Error.Error.Code != appwire.CodeConflict {
		t.Fatalf("second bind during installation = %+v, want conflict", second)
	}
	if installs.Load() != 1 {
		t.Fatalf("concurrent installations = %d, want 1", installs.Load())
	}

	releaseInstall()
	select {
	case result := <-localDone:
		if result.Error != nil {
			t.Fatalf("first bind failed after the second was refused: %+v", result.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("local bind never completed")
	}
	if conn.sessionTools == nil || conn.sessionTools.binding.BindingID != "local-binding" {
		t.Fatal("local binding was not installed")
	}
	if conn.sessionToolsInstalling != nil {
		t.Fatal("completed installation retained its reservation")
	}
}
