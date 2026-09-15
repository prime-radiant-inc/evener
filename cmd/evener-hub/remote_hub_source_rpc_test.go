package hub

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/appitempaging"
)

type remoteHubCall struct {
	method string
	params json.RawMessage
}

// newScriptedRemoteHub wires an initialized AppWire client to an in-memory
// server whose replies the caller scripts, recording every request. No SSH, no
// network, no host.
func newScriptedRemoteHub(t *testing.T, handle func(method string, params json.RawMessage) any) (*appwire.Client, func() []remoteHubCall) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	server := appwire.NewStreamTransport(serverConn)

	var mu sync.Mutex
	var calls []remoteHubCall
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
			mu.Lock()
			calls = append(calls, remoteHubCall{method: msg.Request.Method, params: msg.Request.Params})
			mu.Unlock()
			reply := handle(msg.Request.Method, msg.Request.Params)
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
	return client, func() []remoteHubCall {
		mu.Lock()
		defer mu.Unlock()
		out := make([]remoteHubCall, len(calls))
		copy(out, calls)
		return out
	}
}

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

// The web client hydrates a thread with subscribe:true. Remote subscription
// fan-out is staged until 05b, so that read must still return its snapshot (the
// hub must not start a relay whose SubscribeThread is unimplemented) and the
// subscribe intent must not reach the remote hub, where it would register a
// subscription the controller can never retire.
func TestHubRPCThreadReadSubscribeServesRemoteSnapshot(t *testing.T) {
	remoteOlder := remoteHubPageCursor(t, 10)
	remote, calls := newScriptedRemoteHub(t, func(method string, _ json.RawMessage) any {
		switch method {
		case appwire.MethodInitialize:
			return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
		case appwire.MethodThreadRead:
			return appwire.ThreadReadResponse{
				Thread: appwire.Thread{
					ID:     "t1",
					Source: "local",
					Evener: appwire.EvenerThread{Ref: "local:t1"},
					Turns:  remoteHubItemPage(10, "").Data,
				},
				OlderCursor: remoteOlder,
			}
		default:
			return appwire.EmptyResponse{}
		}
	})
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

	resp, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:          "h1:t1",
		IncludeTurns: true,
		Subscribe:    true,
	})
	if err != nil {
		t.Fatalf("subscribed ThreadRead: %v", err)
	}
	if resp.Thread.Source != "h1" || resp.Thread.Evener.Ref != "h1:t1" {
		t.Fatalf("thread = %+v, want source h1 ref h1:t1", resp.Thread)
	}
	if resp.OlderCursor == "" {
		t.Fatalf("read response = %+v, want a controller-owned continuation cursor", resp)
	}
	for _, call := range calls() {
		if call.method != appwire.MethodThreadRead {
			continue
		}
		var remoteParams appwire.ThreadReadParams
		if err := json.Unmarshal(call.params, &remoteParams); err != nil {
			t.Fatalf("decode remote read params: %v", err)
		}
		if remoteParams.Subscribe || remoteParams.ReplaceSubscription {
			t.Fatalf("remote read received subscription intent: %+v", remoteParams)
		}
	}
}

// A remote item page the remote hub returned in full can still be too large for
// the controller's soft result limit. The hub's size packer then drops the
// oldest selected items and mints a continuation cursor; that cursor must be
// served from the retained page rather than failing as stale, and the remote
// hub must not be asked for a page it already said does not exist.
func TestHubRPCThreadReadPagesSizeTruncatedCompleteRemotePage(t *testing.T) {
	largeText := strings.Repeat("x", 700*1024)
	completePage := appwire.ThreadTurnsListResponse{Data: []appwire.Turn{{
		ID: "turn-1",
		Items: []appwire.ThreadItem{
			{Type: "text", ID: "item-0", TranscriptKey: "key-0", Position: &appwire.ThreadItemPosition{Entry: 0}, Text: largeText},
			{Type: "text", ID: "item-1", TranscriptKey: "key-1", Position: &appwire.ThreadItemPosition{Entry: 1}, Text: largeText},
		},
	}}}
	client, recorded := newScriptedRemoteHub(t, func(method string, _ json.RawMessage) any {
		switch method {
		case appwire.MethodInitialize:
			return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
		case appwire.MethodThreadRead:
			return appwire.ThreadReadResponse{
				Thread: appwire.Thread{
					ID:     "t1",
					Source: "local",
					Evener: appwire.EvenerThread{Ref: "local:t1"},
					Turns:  completePage.Data,
				},
			}
		case appwire.MethodThreadTurnsList:
			return completePage
		default:
			return appwire.EmptyResponse{}
		}
	})
	source := appsource.NewRemoteHubSource("h1", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	rpc := dialHubRPC(t, srv)
	defer rpc.Close()
	if _, err := rpc.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	read, err := rpc.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "h1:t1", IncludeTurns: true, ItemsView: "fragment"})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if read.OlderCursor == "" {
		t.Fatalf("read = %+v, want a continuation cursor from the size-truncated page", read)
	}

	turns, err := rpc.ThreadTurnsList(context.Background(), appwire.ThreadTurnsListParams{
		Ref:       "h1:t1",
		Cursor:    read.OlderCursor,
		ItemsView: "fragment",
	})
	if err != nil {
		t.Fatalf("ThreadTurnsList: %v", err)
	}
	if len(turns.Data) != 1 || len(turns.Data[0].Items) != 1 || turns.Data[0].Items[0].ID != "item-0" {
		t.Fatalf("continuation page = %+v, want the dropped oldest item", turns.Data)
	}
	for _, call := range recorded() {
		if call.method == appwire.MethodThreadTurnsList {
			t.Fatalf("complete remote page was continued remotely: %+v", call)
		}
	}
}

// A remote thread's CWD and tool-argument paths name the remote host's
// filesystem. The hub's file-backed output-image pass must not run against them
// as if they were controller-local: here the remote CWD coincides with a real
// local directory holding a matching image, and no descriptor may be invented
// from it.
func TestHubRPCThreadReadDoesNotEnrichRemoteFilesAsLocal(t *testing.T) {
	cwd := t.TempDir()
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'p', 'a', 'y'}
	if err := os.WriteFile(filepath.Join(cwd, "plot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	remoteThread := appwire.Thread{
		ID:        "t1",
		SessionID: "t1",
		CWD:       cwd,
		Source:    "local",
		Evener:    appwire.EvenerThread{Ref: "local:t1"},
		Turns: []appwire.Turn{{
			ID: "turn-1",
			Items: []appwire.ThreadItem{{
				Type:          "commandExecution",
				ID:            "item-shell",
				TranscriptKey: "key-shell",
				Position:      &appwire.ThreadItemPosition{Entry: 0},
				ToolName:      "shell",
				CallID:        "call-shell",
				ArgumentsJSON: `{}`,
				Output:        "created plot.png",
				Status:        appwire.TurnStatusCompleted,
			}},
			Status: appwire.TurnStatusCompleted,
		}},
	}
	client, _ := newScriptedRemoteHub(t, func(method string, _ json.RawMessage) any {
		switch method {
		case appwire.MethodInitialize:
			return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
		case appwire.MethodThreadRead:
			return appwire.ThreadReadResponse{Thread: remoteThread}
		default:
			return appwire.EmptyResponse{}
		}
	})
	source := appsource.NewRemoteHubSource("h1", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	rpc := dialHubRPC(t, srv)
	defer rpc.Close()
	if _, err := rpc.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := rpc.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "h1:t1", IncludeTurns: true, ItemsView: "fragment"})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if len(resp.Thread.Turns) != 1 || len(resp.Thread.Turns[0].Items) != 1 {
		t.Fatalf("turns = %+v, want the single remote command item", resp.Thread.Turns)
	}
	if images := resp.Thread.Turns[0].Items[0].OutputImages; len(images) != 0 {
		t.Fatalf("remote thread gained controller-local output images: %+v", images)
	}
}

// A remote thread's sha-addressed images live on the remote host. The hub's
// stamp pass mints /s/<session>/images/<sha>, which handleSessionImage resolves
// against this hub's own Past index; a remote session is never in it, so the
// stamp must not run for a remote source. The descriptor stays exactly as the
// remote hub returned it (here: no URL, because the remote hub could not mint
// one either).
func TestHubRPCThreadReadDoesNotStampRemoteImageURLs(t *testing.T) {
	sha := strings.Repeat("a", 64)
	remoteThread := appwire.Thread{
		ID:        "t1",
		SessionID: "t1",
		Source:    "local",
		Evener:    appwire.EvenerThread{Ref: "local:t1"},
		Turns: []appwire.Turn{{
			ID: "turn-1",
			Items: []appwire.ThreadItem{{
				Type:          "commandExecution",
				ID:            "item-image",
				TranscriptKey: "key-image",
				Position:      &appwire.ThreadItemPosition{Entry: 0},
				ToolName:      "shell",
				CallID:        "call-image",
				ArgumentsJSON: `{}`,
				Status:        appwire.TurnStatusCompleted,
				OutputImages:  []appwire.OutputImage{{Source: "tool-result", SHA: sha}},
				Images:        []appwire.InputItem{{Metadata: map[string]string{"sha": sha}}},
			}},
			Status: appwire.TurnStatusCompleted,
		}},
	}
	client, _ := newScriptedRemoteHub(t, func(method string, _ json.RawMessage) any {
		switch method {
		case appwire.MethodInitialize:
			return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
		case appwire.MethodThreadRead:
			return appwire.ThreadReadResponse{Thread: remoteThread}
		default:
			return appwire.EmptyResponse{}
		}
	})
	source := appsource.NewRemoteHubSource("h1", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	rpc := dialHubRPC(t, srv)
	defer rpc.Close()
	if _, err := rpc.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := rpc.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "h1:t1", IncludeTurns: true, ItemsView: "fragment"})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if len(resp.Thread.Turns) != 1 || len(resp.Thread.Turns[0].Items) != 1 {
		t.Fatalf("turns = %+v, want the single remote image item", resp.Thread.Turns)
	}
	item := resp.Thread.Turns[0].Items[0]
	if len(item.OutputImages) != 1 {
		t.Fatalf("output images = %+v, want the remote descriptor preserved", item.OutputImages)
	}
	if got := item.OutputImages[0].URL; got != "" {
		t.Fatalf("remote output image URL = %q, want it left unstamped (this hub cannot serve a remote session)", got)
	}
	if len(item.Images) != 1 || item.Images[0].URL != "" {
		t.Fatalf("remote input image = %+v, want it left unstamped", item.Images)
	}
}

// A remote thread's CWD names the remote host's filesystem. When that path also
// exists on the controller (a shared layout such as /home/<user>/<repo>), the
// sidebar list must not resolve it locally and stamp a controller-local project
// identity over the one the remote hub already computed: that would group the
// thread under the wrong project and aim project-scoped actions at a controller
// path.
func TestHubRPCThreadListKeepsRemoteProjectIdentity(t *testing.T) {
	// A real controller-local directory that resolves to a controller project, so
	// an ungated annotation would overwrite the remote values below.
	localDir := t.TempDir()
	remoteThread := appwire.Thread{
		ID:          "t1",
		SessionID:   "t1",
		CWD:         localDir,
		Source:      "local",
		ProjectID:   "remote-project-id",
		ProjectPath: "/remote/repo",
		Evener:      appwire.EvenerThread{Ref: "local:t1"},
		Turns: []appwire.Turn{{
			ID: "turn-1",
			Items: []appwire.ThreadItem{{
				Type:          "text",
				ID:            "item-1",
				TranscriptKey: "key-1",
				Position:      &appwire.ThreadItemPosition{Entry: 1},
			}},
		}},
	}
	client, _ := newScriptedRemoteHub(t, func(method string, _ json.RawMessage) any {
		switch method {
		case appwire.MethodInitialize:
			return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
		case appwire.MethodThreadList:
			return appwire.ThreadListResponse{Data: []appwire.Thread{remoteThread}}
		default:
			return appwire.EmptyResponse{}
		}
	})
	source := appsource.NewRemoteHubSource("h1", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	rpc := dialHubRPC(t, srv)
	defer rpc.Close()
	if _, err := rpc.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := rpc.ThreadList(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ThreadList: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("thread list = %+v, want the one remote row", resp.Data)
	}
	row := resp.Data[0]
	if row.Source != "h1" {
		t.Fatalf("row source = %q, want the remote source h1", row.Source)
	}
	if row.ProjectID != "remote-project-id" || row.ProjectPath != "/remote/repo" {
		t.Fatalf("remote project = (%q, %q), want the remote-provided (%q, %q); the controller resolved a remote CWD against its own filesystem",
			row.ProjectID, row.ProjectPath, "remote-project-id", "/remote/repo")
	}
}

// A remote hub is expected to enforce the requested item limit, but the
// controller must not depend on it: the packer selects the newest itemLimit
// candidates before the response leaves this hub, so a mixed-version or
// misbehaving remote that returns an oversized page cannot widen it.
func TestHubRPCThreadReadBoundsRemoteItemPage(t *testing.T) {
	oversized := appwire.Thread{
		ID:        "t1",
		SessionID: "t1",
		Source:    "local",
		Evener:    appwire.EvenerThread{Ref: "local:t1"},
		Turns: []appwire.Turn{{
			ID:        "turn-1",
			ItemsView: appwire.TurnItemsViewFragment,
			Items: []appwire.ThreadItem{
				{Type: "text", ID: "item-1", TranscriptKey: "key-1", Position: &appwire.ThreadItemPosition{Entry: 1}},
				{Type: "text", ID: "item-2", TranscriptKey: "key-2", Position: &appwire.ThreadItemPosition{Entry: 2}},
				{Type: "text", ID: "item-3", TranscriptKey: "key-3", Position: &appwire.ThreadItemPosition{Entry: 3}},
			},
		}},
	}
	client, _ := newScriptedRemoteHub(t, func(method string, _ json.RawMessage) any {
		switch method {
		case appwire.MethodInitialize:
			return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
		case appwire.MethodThreadRead:
			return appwire.ThreadReadResponse{Thread: oversized}
		default:
			return appwire.EmptyResponse{}
		}
	})
	source := appsource.NewRemoteHubSource("h1", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	srv := httptest.NewUnstartedServer(nil)
	web := NewWebServer(hubcore.WebConfig{HubAddr: srv.Listener.Addr().String(), Past: hubcore.NewPastIndex("")})
	web.sources.Add(source)
	srv.Config.Handler = web.Handler()
	srv.Start()
	defer srv.Close()

	rpc := dialHubRPC(t, srv)
	defer rpc.Close()
	if _, err := rpc.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := rpc.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:          "h1:t1",
		IncludeTurns: true,
		ItemsView:    "fragment",
		ItemLimit:    1,
	})
	if err != nil {
		t.Fatalf("ThreadRead: %v", err)
	}
	if len(resp.Thread.Turns) != 1 || len(resp.Thread.Turns[0].Items) != 1 || resp.Thread.Turns[0].Items[0].ID != "item-3" {
		t.Fatalf("bounded read = %+v, want exactly the newest remote item", resp.Thread.Turns)
	}
}

// annotateThreadProjects must still resolve a local thread's controller-local
// project while leaving a remote thread's remote-computed identity untouched,
// even when both CWDs name a directory that exists here.
func TestAnnotateThreadProjectsSkipsRemoteThreads(t *testing.T) {
	localDir := t.TempDir()
	localProject, err := identifier.ResolveProject(localDir)
	if err != nil {
		t.Fatalf("resolve local project: %v", err)
	}
	threads := []appwire.Thread{
		{ID: "local-1", Source: "local", CWD: localDir},
		{ID: "remote-1", Source: "h1", CWD: localDir, ProjectID: "remote-project-id", ProjectPath: "/remote/repo"},
	}
	annotateThreadProjects(threads)
	if threads[0].ProjectID != localProject.ID || threads[0].ProjectPath != localProject.CanonicalPath {
		t.Fatalf("local project = (%q, %q), want (%q, %q)",
			threads[0].ProjectID, threads[0].ProjectPath, localProject.ID, localProject.CanonicalPath)
	}
	if threads[1].ProjectID != "remote-project-id" || threads[1].ProjectPath != "/remote/repo" {
		t.Fatalf("remote project = (%q, %q), want the remote-provided values",
			threads[1].ProjectID, threads[1].ProjectPath)
	}
}
