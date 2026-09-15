package hub

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appitempaging"
)

// remoteHubPageCursor mints the kind of opaque cursor a real remote hub
// returns: an itempaging cursor carrying the remote hub's own identity.
func remoteHubPageCursor(t *testing.T, entry uint64) string {
	t.Helper()
	cursor, err := appitempaging.EncodeCursor(appitempaging.CursorIdentity{
		ThreadRef:         "local:t1",
		Incarnation:       "remote-incarnation-1",
		ProjectionVersion: 1,
	}, appwire.ThreadItemPosition{Entry: entry})
	if err != nil {
		t.Fatalf("encode remote cursor: %v", err)
	}
	return cursor
}

func remoteHubItemPage(entry uint64, next string) appwire.ThreadTurnsListResponse {
	return appwire.ThreadTurnsListResponse{
		Data: []appwire.Turn{{
			ID:        "turn-1",
			ItemsView: appwire.TurnItemsViewFragment,
			Items: []appwire.ThreadItem{{
				Type:          "text",
				ID:            "item-1",
				TranscriptKey: "key-1",
				Position:      &appwire.ThreadItemPosition{Entry: entry},
			}},
		}},
		NextCursor: next,
	}
}

// newScriptedRemoteClient wires an initialized AppWire client to an in-memory
// server answering the remote hub read path. No SSH, no network, no host.
func newScriptedRemoteClient(t *testing.T) *appwire.Client {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	server := appwire.NewStreamTransport(serverConn)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			msg, err := server.Recv(ctx)
			if err != nil {
				return
			}
			if msg.Request == nil {
				continue
			}
			var reply any
			switch msg.Request.Method {
			case appwire.MethodInitialize:
				reply = appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
			case appwire.MethodThreadRead:
				page := remoteHubItemPage(10, "")
				reply = appwire.ThreadReadResponse{
					Thread: appwire.Thread{
						ID:     "t1",
						Source: "local",
						Evener: appwire.EvenerThread{Ref: "local:t1"},
						Turns:  page.Data,
					},
					OlderCursor: remoteHubPageCursor(t, 10),
				}
			case appwire.MethodThreadTurnsList:
				reply = remoteHubItemPage(10, remoteHubPageCursor(t, 10))
			default:
				reply = appwire.EmptyResponse{}
			}
			data, err := json.Marshal(reply)
			if err != nil {
				return
			}
			if err := server.Send(ctx, appwire.ResponseMessage(msg.Request.ID, json.RawMessage(data))); err != nil {
				return
			}
		}
	}()

	client := appwire.NewClient(appwire.NewStreamTransport(clientConn))
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		cancel()
		t.Fatalf("initialize scripted remote: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		<-done
	})
	return client
}

// A plain thread/read against a remote hub source must succeed: the source does
// not implement SubscribeThread until 05b, so the hub must not start a relay on
// read. An item-mode page carrying the remote hub's own cursor must also pack
// into a controller-owned continuation instead of failing the response.
func TestHubRPCThreadReadServesRemoteHubSource(t *testing.T) {
	remote := newScriptedRemoteClient(t)
	source := appsource.NewRemoteHubSource("h1", nil, func(context.Context, string) (*appwire.Client, error) {
		return remote, nil
	})

	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	client := dialHubRPC(t, srv)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "h1:t1", IncludeTurns: true})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if resp.Thread.Source != "h1" || resp.Thread.Evener.Ref != "h1:t1" {
		t.Fatalf("thread = %+v, want source h1 ref h1:t1", resp.Thread)
	}
	if resp.OlderCursor == "" {
		t.Fatalf("read response = %+v, want a controller-owned continuation cursor", resp)
	}

	turns, err := client.ThreadTurnsList(context.Background(), appwire.ThreadTurnsListParams{
		Ref:       "h1:t1",
		Cursor:    resp.OlderCursor,
		ItemsView: "fragment",
	})
	if err != nil {
		t.Fatalf("ThreadTurnsList: %v", err)
	}
	if turns.NextCursor == "" {
		t.Fatalf("turns = %+v, want a controller-owned continuation cursor", turns)
	}
}
