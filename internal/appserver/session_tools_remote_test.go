package appserver

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

func TestSessionToolsIdentifiedPeerRefusesRuntimeBindings(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	server := NewServer(ServerConfig{})
	var installs atomic.Int32
	server.InstallSessionTools(func(context.Context, appwire.SessionBinding, appwire.SessionHubClient) error {
		installs.Add(1)
		return nil
	})
	HandleTyped(server.Router(), appwire.MethodHubSessionToolsIdentify, func(ctx context.Context, params appwire.SessionToolsPeerParams) (appwire.SessionToolsPeerResponse, error) {
		return appwire.SessionToolsPeerResponse{HubID: "hub-server"}, IdentifySessionToolsPeer(ctx, params.HubID)
	})
	endpoint := serveWebSocketHTTP(t, server)
	for range 2 { // Reconnect must repeat identification without inheriting a binding.
		client := appwire.NewClient(dialRawAppWire(t, endpoint))
		t.Cleanup(func() { _ = client.Close() })
		client.Start(ctx)
		if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatal(err)
		}
		conn := registeredConnection(t, server)
		peerCtx := context.WithValue(ctx, connectionContextKey{}, conn)
		if got := SessionToolsPeerID(peerCtx); got != "" {
			t.Fatalf("new connection inherited peer identity %q", got)
		}
		for range 2 { // Identifying as the same peer is idempotent.
			response, err := client.IdentifySessionToolsPeer(ctx, appwire.SessionToolsPeerParams{HubID: "hub-peer"})
			if err != nil || response.HubID != "hub-server" {
				t.Fatalf("identify response = %+v, error = %v", response, err)
			}
		}
		_, err := client.IdentifySessionToolsPeer(ctx, appwire.SessionToolsPeerParams{HubID: "another-hub"})
		wire, ok := errors.AsType[appwire.WireError](err)
		if !ok || wire.Code != appwire.CodeConflict {
			t.Fatalf("changing peer identity = %v, want conflict", err)
		}
		for _, hubID := range []string{"hub-peer", "another-hub"} {
			binding := appwire.SessionBinding{BindingID: "binding", Ref: "local:workspace", InstanceID: "instance", HubID: hubID}
			err := client.Request(ctx, appwire.MethodRuntimeSessionBind, binding, nil)
			wire, ok := errors.AsType[appwire.WireError](err)
			if !ok || wire.Code != appwire.CodeConflict {
				t.Fatalf("identified peer runtime bind for %q = %v, installs = %d, want conflict without installation", hubID, err, installs.Load())
			}
		}
		if installs.Load() != 0 {
			t.Fatalf("identified bridge installed %d runtime bindings", installs.Load())
		}
		if got := SessionToolsPeerID(peerCtx); got != "hub-peer" {
			t.Fatalf("refused binds changed peer identity to %q", got)
		}
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
		waitUnregistered(t, server)
	}
}

// The daemon's local binding and an identified hub peer are different
// authorities. The same physical connection cannot become both, in either
// order.
func TestSessionToolsRemoteBindingAuthorityOrdering(t *testing.T) {
	newConnection := func() (*Connection, context.Context) {
		t.Helper()
		server := NewServer(ServerConfig{})
		server.InstallSessionTools(func(context.Context, appwire.SessionBinding, appwire.SessionHubClient) error { return nil })
		conn := server.NewConnection("binding-order")
		conn.sessionLifetime = context.Background()
		conn.setInitialized()
		t.Cleanup(conn.closeSessionTools)
		return conn, context.WithValue(context.Background(), connectionContextKey{}, conn)
	}
	bind := func(conn *Connection, ctx context.Context, id, ref, hubID string) appwire.Message {
		t.Helper()
		request := appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodRuntimeSessionBind, appwire.SessionBinding{
			BindingID: id, Ref: ref, InstanceID: ref, HubID: hubID,
		})
		return conn.bindSessionTools(ctx, *request.Request)
	}
	assertConflict := func(t *testing.T, err appwire.WireError) {
		t.Helper()
		if err.Code != appwire.CodeConflict {
			t.Fatalf("authority conflict = %+v", err)
		}
	}
	t.Run("local then identify", func(t *testing.T) {
		conn, ctx := newConnection()
		if result := bind(conn, ctx, "local-one", "local:one", "hub-b"); result.Error != nil {
			t.Fatalf("local binding: %+v", result.Error)
		}
		if err := IdentifySessionToolsPeer(ctx, "hub-a"); err == nil {
			t.Fatal("identified peer after local binding")
		} else if wire, ok := errors.AsType[appwire.WireError](err); !ok {
			t.Fatalf("identify error = %T: %v", err, err)
		} else {
			assertConflict(t, wire)
		}
	})
	t.Run("identify then local", func(t *testing.T) {
		conn, ctx := newConnection()
		if err := IdentifySessionToolsPeer(ctx, "hub-a"); err != nil {
			t.Fatal(err)
		}
		result := bind(conn, ctx, "local-one", "local:one", "hub-b")
		if result.Error == nil {
			t.Fatal("local binding replaced identified peer authority")
		}
		assertConflict(t, result.Error.Error)
	})
}
