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
	"time"

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
	client, calls, _ := newPushableScriptedRemoteHub(t, handle)
	return client, calls
}

// newPushableScriptedRemoteHub is newScriptedRemoteHub plus the remote hub's side
// of a subscription fan-out: the returned push writes an unsolicited notification
// onto the same transport the scripted replies answer on. StreamTransport.Send is
// mutex-guarded, so a test push and the responder loop interleave safely.
func newPushableScriptedRemoteHub(t *testing.T, handle func(method string, params json.RawMessage) any) (*appwire.Client, func() []remoteHubCall, func(method string, params any) error) {
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
	recordedCalls := func() []remoteHubCall {
		mu.Lock()
		defer mu.Unlock()
		out := make([]remoteHubCall, len(calls))
		copy(out, calls)
		return out
	}
	push := func(method string, params any) error {
		return server.Send(ctx, appwire.NotificationMessage(method, params))
	}
	return client, recordedCalls, push
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
				// A hub mints NextCursor at the packed page's oldest item, so the
				// continuation the controller forwards asks for items strictly older
				// than that boundary and is answered with the next older page. A remote
				// re-serving the boundary's own page would be a stale answer and is
				// refused before it is cached.
				var params appwire.ThreadTurnsListParams
				if err := json.Unmarshal(msg.Request.Params, &params); err != nil {
					reply = appwire.EmptyResponse{}
					break
				}
				if params.Cursor == "" {
					reply = remoteHubItemPage(10, remoteHubPageCursor(t, 10))
					break
				}
				reply = remoteHubItemPage(9, remoteHubPageCursor(t, 9))
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

// A plain thread/read against a remote hub source must succeed and start a relay
// (the source reports the hub's default relay policy and 05b implements
// SubscribeThread, so the attach is a real subscription). An item-mode page
// carrying the remote hub's own cursor must also pack into a controller-owned
// continuation instead of failing the response.
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

// thread/shutdown for a ref the controller does not host must reach the owning
// host. The controller's shutdown path (shutdownThreadTolerateExited) resolves
// the remote source, gates on the thread's advertised capabilities, and forwards
// on the owning host's client — the same non-dialing client every remote read
// uses. Because the mask now carries Shutdown, the remote daemon's own claim
// decides the gate; with it masked off the controller refuses the action locally
// ("shutdown is not available for this session") without ever asking the host,
// which is what left a controller unable to stop a session it did not host.
func TestHubRPCThreadShutdownForwardsToRemoteHost(t *testing.T) {
	// The remote daemon's own claim carries Shutdown, which the mask must
	// preserve for the shutdown gate to pass.
	remote, calls := newScriptedRemoteHub(t, scriptedRemoteThreadT1With(appwire.ThreadCapabilities{Shutdown: true}))
	source := appsource.NewRemoteHubSource("h1", nil, func(context.Context, string) (*appwire.Client, error) {
		return remote, nil
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

	if err := rpc.ThreadShutdown(context.Background(), appwire.ThreadShutdownParams{Ref: "h1:t1"}); err != nil {
		t.Fatalf("ThreadShutdown on remote ref: %v", err)
	}

	var forwarded appwire.ThreadShutdownParams
	shutdowns := 0
	for _, call := range calls() {
		if call.method != appwire.MethodThreadShutdown {
			continue
		}
		if err := json.Unmarshal(call.params, &forwarded); err != nil {
			t.Fatalf("decode forwarded shutdown params: %v", err)
		}
		shutdowns++
	}
	if shutdowns != 1 {
		t.Fatalf("forwarded thread/shutdown calls = %d, want exactly 1: the controller stopped the session locally instead of on its host", shutdowns)
	}
	if forwarded.Ref != "local:t1" {
		t.Fatalf("forwarded shutdown ref = %q, want the remote hub's own local:t1", forwarded.Ref)
	}
}

// The web client hydrates a thread with subscribe:true. The read must still serve
// its translated snapshot, and the plain read behind it must still carry no
// controller subscription intent of its own: a forwarded Subscribe would
// register a remote subscription nothing on this side owns or can retire. What
// changed in 05b is the other half — the relay now runs for a remote source and
// attaches through SubscribeThread, which
// TestHubRPCThreadReadSubscribeStartsRemoteRelay pins. The controller-level
// ReplaceSubscription must never reach the wire at all: it is connection-scoped
// on the shared per-host client, so it would drop every other thread's remote
// subscription while their local routing entries stayed live.
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
	plainReads := 0
	for _, call := range calls() {
		if call.method != appwire.MethodThreadRead {
			continue
		}
		var remoteParams appwire.ThreadReadParams
		if err := json.Unmarshal(call.params, &remoteParams); err != nil {
			t.Fatalf("decode remote read params: %v", err)
		}
		if remoteParams.ReplaceSubscription {
			t.Fatalf("remote read received replacement intent: %+v", remoteParams)
		}
		if remoteParams.Subscribe {
			// The relay's own attach (SubscribeThread), pinned by
			// TestHubRPCThreadReadSubscribeStartsRemoteRelay.
			continue
		}
		plainReads++
	}
	if plainReads != 1 {
		t.Fatalf("plain remote reads = %d, want the one snapshot this read served", plainReads)
	}
}

// A browser subscribes to a remote thread with subscribe:true. The controller
// relay must run for a remote source: startRelay attaches through the source's
// own SubscribeThread, which is the thread/read{subscribe:true} the remote hub
// answers by attaching its relay. The plain read that produced the response still
// carries no controller subscription intent of its own — the relay owns the
// subscription and retires it, so the read must not register a second one.
func TestHubRPCThreadReadSubscribeStartsRemoteRelay(t *testing.T) {
	remote, calls, _ := newPushableScriptedRemoteHub(t, scriptedRemoteThreadT1)
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
		Ref:       "h1:t1",
		Subscribe: true,
	})
	if err != nil {
		t.Fatalf("subscribed ThreadRead: %v", err)
	}
	if resp.Thread.Source != "h1" || resp.Thread.Evener.Ref != "h1:t1" {
		t.Fatalf("thread = %+v, want source h1 ref h1:t1", resp.Thread)
	}

	// startRelay issues the subscribe synchronously and only answers the read
	// once the attach is registered, so both calls are on the wire by now.
	snapshots, attaches := 0, 0
	var attach appwire.ThreadReadParams
	for _, call := range calls() {
		if call.method != appwire.MethodThreadRead {
			continue
		}
		var params appwire.ThreadReadParams
		if err := json.Unmarshal(call.params, &params); err != nil {
			t.Fatalf("decode remote read params: %v", err)
		}
		// A controller-level replacement would scope the whole shared remote
		// connection to this thread and drop every other thread's subscription.
		if params.ReplaceSubscription {
			t.Errorf("remote read received replacement intent: %+v", params)
		}
		if params.Subscribe {
			attaches++
			attach = params
			continue
		}
		snapshots++
	}
	if snapshots != 1 {
		t.Fatalf("plain remote reads = %d, want the one snapshot this read served", snapshots)
	}
	if attaches != 1 {
		t.Fatalf("relay attach reads = %d, want exactly 1: a subscribed remote read must start the relay and reach SubscribeThread", attaches)
	}
	if attach.Ref != "local:t1" {
		t.Fatalf("relay attach ref = %q, want the remote hub's own local:t1", attach.Ref)
	}
}

// The relay must not only attach: a status frame the remote hub pushes has to
// reach the subscribed browser with the capability set the read path answers
// with, not the remote daemon's own. A daemon stamps its real set on every status
// frame, and a client that re-enabled Send/Steer/Interrupt from one would hit an
// internal error the moment it used it.
func TestHubRPCThreadReadSubscribeDeliversMaskedRemoteStatus(t *testing.T) {
	remote, _, push := newPushableScriptedRemoteHub(t, scriptedRemoteThreadT1)
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
	if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:       "h1:t1",
		Subscribe: true,
	}); err != nil {
		t.Fatalf("subscribed ThreadRead: %v", err)
	}

	daemonSet := appwire.ThreadCapabilities{
		Send:         true,
		Steer:        true,
		Interrupt:    true,
		Queue:        true,
		ForkFromTurn: true,
		SkillInput:   true,
		Shutdown:     true,
	}
	if err := push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
		ThreadID:     "t1",
		Ref:          "local:t1",
		Status:       appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		Capabilities: &daemonSet,
	}); err != nil {
		t.Fatalf("push status: %v", err)
	}

	// The relay's own leading resync rides the same stream, so skip every frame
	// that is not the status transition under test.
	deadline := time.After(10 * time.Second)
	for {
		select {
		case notification, ok := <-client.Notifications():
			if !ok {
				t.Fatal("client notification stream closed before the status frame")
			}
			if notification.Method != appwire.NotifyThreadStatusChanged {
				continue
			}
			var status appwire.ThreadStatusChangedParams
			if err := json.Unmarshal(notification.Params, &status); err != nil {
				t.Fatalf("decode relayed status params %s: %v", notification.Params, err)
			}
			if status.Ref != "h1:t1" {
				t.Fatalf("relayed status ref = %q, want h1:t1", status.Ref)
			}
			if status.Capabilities == nil {
				t.Fatal("relayed status capabilities = nil, want the read path's masked set")
			}
			// The daemon claims Shutdown as well, so this pins both halves of the
			// mask: the one forwarded action survives, every unforwarded one is
			// dropped. An expectation of the empty set would only show that a claim
			// containing no forwarded action stays empty.
			if want := (appwire.ThreadCapabilities{Shutdown: true}); *status.Capabilities != want {
				t.Fatalf("relayed status capabilities = %+v, want %+v", *status.Capabilities, want)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for the relayed status frame: the subscription delivered no live update")
		}
	}
}

// scriptedRemoteThreadT1 answers a remote hub's attach-bridge requests for one
// thread named "t1" in the remote hub's own local namespace.
func scriptedRemoteThreadT1(method string, params json.RawMessage) any {
	return scriptedRemoteThreadT1With(appwire.ThreadCapabilities{})(method, params)
}

// scriptedRemoteThreadT1With is scriptedRemoteThreadT1 with the daemon's own
// capability claim attached to the thread it reports.
func scriptedRemoteThreadT1With(caps appwire.ThreadCapabilities) func(string, json.RawMessage) any {
	return func(method string, _ json.RawMessage) any {
		switch method {
		case appwire.MethodInitialize:
			return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
		case appwire.MethodThreadRead:
			return appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID:     "t1",
				Source: "local",
				Evener: appwire.EvenerThread{Ref: "local:t1", Capabilities: caps},
			}}
		default:
			return appwire.EmptyResponse{}
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

// A remote hub stamps its own origin-relative image routes before replying:
// /s/<session>/images/<sha> for sha-addressed bytes and /doc/image?session=<...>
// &path=<...> for file-backed output images. Such a route is only meaningful
// against the remote origin: if it reaches the browser, the browser resolves it
// against this hub's own origin for a session this hub does not have, which 404s
// or, on a session-id collision, serves another session's bytes. While the
// controller-side proxy is staged, every root-relative route must be
// neutralized; external and data: URLs still resolve directly and must survive.
func TestHubRPCThreadReadNeutralizesRemoteImageRoutes(t *testing.T) {
	sha := strings.Repeat("a", 64)
	const external = "https://images.example.test/plot.png"
	const docImage = "/doc/image?session=remote-session&path=shot.png"
	const inline = "data:image/png;base64,iVBORw0KGgo="
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
				OutputImages: []appwire.OutputImage{
					{Source: "tool-result", SHA: sha, URL: "/s/remote-session/images/" + sha},
					{Source: "external", URL: external},
					{Source: "written-file", Path: "shot.png", URL: docImage},
					{Source: "inline", URL: inline},
				},
				Images: []appwire.InputItem{{
					Metadata: map[string]string{"sha": sha},
					URL:      "/s/remote-session/images/" + sha,
				}, {
					Metadata: map[string]string{"sha": sha},
					URL:      docImage,
				}},
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
	if len(item.OutputImages) != 4 {
		t.Fatalf("output images = %+v, want all remote descriptors preserved", item.OutputImages)
	}
	if got := item.OutputImages[0].URL; got != "" {
		t.Fatalf("remote output image route = %q, want the controller-relative route neutralized", got)
	}
	if got := item.OutputImages[0].SHA; got != sha {
		t.Fatalf("output image sha = %q, want %q preserved for the staged proxy", got, sha)
	}
	if got := item.OutputImages[1].URL; got != external {
		t.Fatalf("external output image URL = %q, want %q preserved", got, external)
	}
	if got := item.OutputImages[2].URL; got != "" {
		t.Fatalf("remote /doc/image URL = %q, want it neutralized (it would resolve against the controller origin)", got)
	}
	if got := item.OutputImages[2].Path; got != "shot.png" {
		t.Fatalf("remote /doc/image path = %q, want the relative path preserved for the staged proxy", got)
	}
	if got := item.OutputImages[3].URL; got != inline {
		t.Fatalf("data: output image URL = %q, want %q preserved", got, inline)
	}
	if len(item.Images) != 2 || item.Images[0].URL != "" || item.Images[1].URL != "" {
		t.Fatalf("remote input images = %+v, want both controller-relative routes neutralized", item.Images)
	}
}

// evener/subagentPreview returns a remote thread's items exactly as thread/read
// does, so it must run the same origin-relative image neutralization. Without it
// a remote item's root-relative route reaches the browser, which resolves it
// against the controller origin (404, or another session's bytes on a collision).
func TestHubRPCSubagentPreviewNeutralizesRemoteImageRoutes(t *testing.T) {
	const external = "https://images.example.test/plot.png"
	sha := strings.Repeat("a", 64)
	remoteRoute := "/s/remote-session/images/" + sha
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
				Images: []appwire.InputItem{
					{Metadata: map[string]string{"sha": sha}, URL: remoteRoute},
					{URL: external},
				},
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

	var preview appwire.EvenerSubagentPreviewResponse
	if err := rpc.Request(context.Background(), appwire.MethodEvenerSubagentPreview, appwire.EvenerSubagentPreviewParams{Ref: "h1:t1"}, &preview); err != nil {
		t.Fatalf("SubagentPreview: %v", err)
	}
	if len(preview.Items) != 1 || len(preview.Items[0].Images) != 2 {
		t.Fatalf("preview items = %+v, want the single remote image item with both input images", preview.Items)
	}
	if got := preview.Items[0].Images[0].URL; got != "" {
		t.Fatalf("remote preview image route = %q, want it neutralized", got)
	}
	if got := preview.Items[0].Images[1].URL; got != external {
		t.Fatalf("external preview image URL = %q, want %q preserved", got, external)
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
