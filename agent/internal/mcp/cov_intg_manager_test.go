package mcp

import (
	"context"
	"errors"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/agent/mcpconfig"
)

// These tests cover NewManager's failure arms that the happy-path in-memory and
// real-server tests never reach: a config whose transport can't be built, a
// transport whose Connect fails, and a connected server whose tools/list call
// returns an error. All three are deterministic and hermetic (no subprocess,
// no network).

// intg_failConnectTransport is an mcpsdk.Transport whose Connect always fails,
// exercising the NewManager Connect-error arm.
type intg_failConnectTransport struct{ err error }

func (t intg_failConnectTransport) Connect(context.Context) (mcpsdk.Connection, error) {
	return nil, t.err
}

func TestIntgMCP_NewManager_TransportBuildError(t *testing.T) {
	// nil transports forces NewManager to build one from the config; a stdio
	// config with no command is invalid, so transportForConfig fails.
	mgr, err := NewManager(context.Background(), []mcpconfig.ServerConfig{
		{Name: "nocmd", Type: "stdio"},
	}, nil)
	if err == nil {
		t.Fatal("expected transport-build error for stdio config without a command")
	}
	if mgr != nil {
		t.Errorf("expected nil manager on error, got %+v", mgr)
	}
}

func TestIntgMCP_NewManager_ConnectError(t *testing.T) {
	sentinel := errors.New("intg: connect refused")
	mgr, err := NewManager(context.Background(), []mcpconfig.ServerConfig{
		{Name: "unreachable", Type: "stdio"},
	}, []mcpsdk.Transport{intg_failConnectTransport{err: sentinel}})
	if err == nil {
		t.Fatal("expected connect error, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error %v does not wrap the sentinel connect error", err)
	}
	if mgr != nil {
		t.Errorf("expected nil manager on connect error, got %+v", mgr)
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

	mgr, err := NewManager(ctx, []mcpconfig.ServerConfig{
		{Name: "s", Type: "stdio"},
	}, []mcpsdk.Transport{ct})
	if err == nil {
		if mgr != nil {
			mgr.Close()
		}
		t.Fatal("expected a list-tools error, got nil")
	}
	if mgr != nil {
		t.Errorf("expected nil manager on list-tools error, got %+v", mgr)
	}
}
