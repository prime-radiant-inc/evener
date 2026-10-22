package appserver

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
)

// The daemon's local binding and an identified hub peer are different
// authorities. The same physical connection cannot become both, in either
// order, but two workspaces of the *same* identified peer may share it.
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
		if err.Code != appwire.CodeConflict || err.Message != sessionToolsAuthorityConflict {
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
		} else if wire, ok := err.(appwire.WireError); !ok {
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
	t.Run("two peer workspaces", func(t *testing.T) {
		conn, ctx := newConnection()
		if err := IdentifySessionToolsPeer(ctx, "hub-a"); err != nil {
			t.Fatal(err)
		}
		for _, binding := range []struct{ id, ref string }{{"peer-one", "local:one"}, {"peer-two", "local:two"}} {
			if result := bind(conn, ctx, binding.id, binding.ref, "hub-a"); result.Error != nil {
				t.Fatalf("peer workspace %s refused: %+v", binding.ref, result.Error)
			}
			if got, ok := SessionToolsPeerBinding(ctx, binding.id); !ok || got.Ref != binding.ref {
				t.Fatalf("peer binding lookup: %+v, found=%v", got, ok)
			}
		}
		other, otherCtx := newConnection()
		if err := IdentifySessionToolsPeer(otherCtx, "hub-a"); err != nil {
			t.Fatal(err)
		}
		for _, lookup := range []struct {
			ctx context.Context
			id  string
		}{
			{ctx, ""}, {ctx, "unknown"}, {otherCtx, "peer-one"},
		} {
			if got, ok := SessionToolsPeerBinding(lookup.ctx, lookup.id); ok {
				t.Fatalf("unknown or foreign binding %q resolved to %+v", lookup.id, got)
			}
		}
		if result := bind(other, otherCtx, "foreign-owner", "local:foreign", "hub-b"); result.Error == nil {
			t.Fatal("peer installed binding naming another hub")
		} else {
			assertConflict(t, result.Error.Error)
		}
		if result := bind(conn, ctx, "local-three", "local:three", "hub-b"); result.Error == nil {
			t.Fatal("local binding installed alongside peer bindings")
		} else {
			assertConflict(t, result.Error.Error)
		}
	})
}
