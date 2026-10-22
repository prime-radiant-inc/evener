package appserver

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// TestSessionToolsConcurrentLocalAndPeerBindStayExclusive pins that releasing the
// connection lock across the installation wait did not let two binds claim the
// same connection.
//
// The local binding slot is singular: borrow the lock away and a local bind and an
// identified peer bind can both pass the "is it taken?" checks before either
// commits. The reservation keeps that exclusion while the wait happens outside the
// lock, which is the whole point of the change.
func TestSessionToolsConcurrentLocalAndPeerBindStayExclusive(t *testing.T) {
	server := NewServer(ServerConfig{})
	installing := make(chan struct{})
	release := make(chan struct{})
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

	// With the local bind still waiting, the connection is already claimed: even
	// identifying as a peer must be refused, and a peer bind certainly must.
	localCtx := context.WithValue(context.Background(), connectionContextKey{}, conn)
	if err := IdentifySessionToolsPeer(localCtx, "hub-a"); err == nil {
		t.Fatal("identified a peer while a local binding installation held the connection")
	}
	conn.peerMu.Lock()
	conn.peerHubID = "hub-a"
	conn.peerMu.Unlock()
	peerRequest := appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodRuntimeSessionBind, appwire.SessionBinding{
		BindingID: "peer-binding", Ref: "remote:one", InstanceID: "remote:one", HubID: "hub-a",
	})
	peer := conn.bindSessionTools(localCtx, *peerRequest.Request)
	if peer.Error == nil {
		t.Fatal("identified peer bind succeeded while a local bind held the connection")
	}
	if peer.Error.Error.Code != appwire.CodeConflict {
		t.Fatalf("peer refusal = %+v, want conflict", peer.Error.Error)
	}

	close(release)
	select {
	case result := <-localDone:
		if result.Error != nil {
			t.Fatalf("local bind failed after the peer was refused: %+v", result.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("local bind never completed")
	}
	if conn.sessionTools == nil || conn.sessionTools.binding.BindingID != "local-binding" {
		t.Fatal("local binding was not installed")
	}
	if len(conn.sessionBindings) != 0 {
		t.Fatalf("reservation leaked into installed bindings: %+v", conn.sessionBindings)
	}
}
