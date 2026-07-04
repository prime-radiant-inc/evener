package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/agent/mcpconfig"
)

// These tests cover NewManager's per-server failure arms that the happy-path
// in-memory and real-server tests never reach: a config whose transport can't
// be built, a transport whose Connect fails, and a connected server whose
// tools/list call returns an error. Since Task 2, NewManager treats each of
// these as a non-fatal per-server connect outcome rather than aborting the
// whole batch: the manager is always returned (never nil, for a non-empty
// config list) and the offending server is recorded in the returned
// []ServerOutcome and in Servers() as status "failed". All three are
// deterministic and hermetic (no subprocess, no network).

// intg_failConnectTransport is an mcpsdk.Transport whose Connect always fails,
// exercising the NewManager Connect-error arm.
type intg_failConnectTransport struct{ err error }

func (t intg_failConnectTransport) Connect(context.Context) (mcpsdk.Connection, error) {
	return nil, t.err
}

func TestIntgMCP_NewManager_TransportBuildError(t *testing.T) {
	// nil transports forces NewManager to build one from the config; a stdio
	// config with no command is invalid, so transportForConfig fails.
	mgr, outcomes := NewManager(context.Background(), []mcpconfig.ServerConfig{
		{Name: "nocmd", Type: "stdio"},
	}, nil)
	if mgr == nil {
		t.Fatal("expected a non-nil manager even when a server fails to build its transport")
	}
	defer mgr.Close()
	if len(outcomes) != 1 || outcomes[0].Name != "nocmd" || outcomes[0].Stage != "connect" {
		t.Fatalf("want one connect outcome for %q, got %+v", "nocmd", outcomes)
	}
	if outcomes[0].Err == nil || !strings.Contains(outcomes[0].Err.Error(), "command") {
		t.Errorf("outcome error %v does not mention the missing command", outcomes[0].Err)
	}
	if got := mgr.Servers(); len(got) != 1 || got[0].Status != "failed" {
		t.Errorf("failed server must still appear in Servers() as failed, got %+v", got)
	}
}

func TestIntgMCP_NewManager_ConnectError(t *testing.T) {
	sentinel := errors.New("intg: connect refused")
	mgr, outcomes := NewManager(context.Background(),
		[]mcpconfig.ServerConfig{{Name: "unreachable", Type: "stdio"}},
		[]func(context.Context) (mcpsdk.Transport, error){staticDial(intg_failConnectTransport{err: sentinel})})
	if mgr == nil {
		t.Fatal("expected a non-nil manager even when a server fails to connect")
	}
	defer mgr.Close()
	if len(outcomes) != 1 || outcomes[0].Name != "unreachable" || outcomes[0].Stage != "connect" {
		t.Fatalf("want one connect outcome for %q, got %+v", "unreachable", outcomes)
	}
	if !errors.Is(outcomes[0].Err, sentinel) {
		t.Errorf("outcome error %v does not wrap the sentinel", outcomes[0].Err)
	}
	if got := mgr.Servers(); len(got) != 1 || got[0].Status != "failed" {
		t.Errorf("failed server must still appear in Servers() as failed, got %+v", got)
	}
}

func TestIntgMCP_NewManager_ListToolsError(t *testing.T) {
	ctx := context.Background()

	// A real in-memory server whose receiving middleware rejects tools/list:
	// the initialize handshake (Connect) succeeds, but the subsequent
	// tools/list round-trip fails, exercising NewManager's ListTools-error arm.
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "s", Version: "v1"}, nil)
	server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method == "tools/list" {
				return nil, errors.New("intg: tools/list disabled")
			}
			return next(ctx, method, req)
		}
	})

	st, ct := mcpsdk.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}

	mgr, outcomes := NewManager(ctx, []mcpconfig.ServerConfig{
		{Name: "s", Type: "stdio"},
	}, []func(context.Context) (mcpsdk.Transport, error){staticDial(ct)})
	if mgr == nil {
		t.Fatal("expected a non-nil manager even when a server's tools/list fails")
	}
	defer mgr.Close()
	if len(outcomes) != 1 || outcomes[0].Name != "s" || outcomes[0].Stage != "connect" {
		t.Fatalf("want one connect outcome for %q, got %+v", "s", outcomes)
	}
	if outcomes[0].Err == nil {
		t.Error("expected a non-nil outcome error for the list-tools failure")
	}
	if got := mgr.Servers(); len(got) != 1 || got[0].Status != "failed" {
		t.Errorf("failed server must still appear in Servers() as failed, got %+v", got)
	}
}
