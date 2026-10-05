package hub

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// concurrentScriptedRemote is a remote host that records each request as it
// arrives and answers each through handle on a goroutine of its own, so a
// request held in handle never hides a later one: what it records is what the
// hub actually sent, in the order it sent it.
func concurrentScriptedRemote(t *testing.T, handle func(method string, params json.RawMessage) hostAdminReply) (*appwire.Client, func() []hostAdminCall) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	server := appwire.NewStreamTransport(serverConn)
	ctx, cancel := context.WithCancel(context.Background())
	var mu, sendMu sync.Mutex
	var calls []hostAdminCall
	send := func(msg appwire.Message) {
		sendMu.Lock()
		defer sendMu.Unlock()
		_ = server.Send(ctx, msg)
	}
	answer := func(req *appwire.Request) {
		var result any = appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
		if req.Method != appwire.MethodInitialize {
			result = handle(req.Method, req.Params).result
		}
		data, _ := json.Marshal(result)
		send(appwire.ResponseMessage(req.ID, json.RawMessage(data)))
	}
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
			calls = append(calls, hostAdminCall{method: msg.Request.Method, params: msg.Request.Params})
			mu.Unlock()
			go answer(msg.Request)
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
	return client, func() []hostAdminCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]hostAdminCall(nil), calls...)
	}
}

// hostRequestDispatchHub serves a real hub RPC server whose one remote host,
// m4, answers through handle (concurrentScriptedRemote), and returns a client
// on it plus the remote's recorded calls.
func hostRequestDispatchHub(t *testing.T, handle func(method string, params json.RawMessage) hostAdminReply) (*appwire.Client, func() []hostAdminCall) {
	t.Helper()
	remote, calls := concurrentScriptedRemote(t, handle)
	source := appsource.NewRemoteHubSource("m4", nil, func(context.Context, string) (*appwire.Client, error) {
		return remote, nil
	})
	source.SetHostOnline(func() bool { return true })
	sources := appsource.NewRegistry()
	sources.Add(source)
	hosts, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	server := newHubAppServer(hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex(""), RemoteHostRegistry: hosts}, sources)
	wire := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	t.Cleanup(wire.Close)
	client := dialHubRPC(t, wire)
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	return client, calls
}

// forwardedCallSeen waits until the remote has received method.
func forwardedCallSeen(t *testing.T, calls func() []hostAdminCall, method string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, call := range calls() {
			if call.method == method {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the remote never received %s", method)
}

// remoteGate holds the scripted remote's answer to one method until opened.
// Tests defer open: the hub's server shutdown waits for that held call, so a
// test that fails with it shut would hang its cleanup.
type remoteGate struct {
	ch   chan struct{}
	once sync.Once
}

func newRemoteGate() *remoteGate { return &remoteGate{ch: make(chan struct{})} }
func (g *remoteGate) wait()      { <-g.ch }
func (g *remoteGate) open()      { g.once.Do(func() { close(g.ch) }) }

func hostRequest(client *appwire.Client, method string) <-chan error {
	done := make(chan error, 1)
	go func() {
		var out json.RawMessage
		done <- client.Request(context.Background(), appwire.MethodEvenerHostRequest, appwire.HostRequestParams{Host: "m4", Method: method, Params: json.RawMessage(`{}`)}, &out)
	}()
	return done
}

// A forwarded read waits on a remote host (a plugin update check can take
// minutes); it must not hold the browser's later requests behind it.
func TestHostRequestSlowForwardedReadDoesNotHoldALaterThreadRead(t *testing.T) {
	gate := newRemoteGate()
	client, calls := hostRequestDispatchHub(t, func(method string, _ json.RawMessage) hostAdminReply {
		if method == appwire.MethodEvenerMarketplaceList {
			gate.wait()
		}
		return okReply()
	})
	defer gate.open()
	read := hostRequest(client, appwire.MethodEvenerMarketplaceList)
	forwardedCallSeen(t, calls, appwire.MethodEvenerMarketplaceList)

	answered := make(chan struct{})
	go func() {
		_, _ = client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:th_1"})
		close(answered)
	}()
	select {
	case <-answered:
	case <-time.After(5 * time.Second):
		t.Fatal("a thread read waited behind a forwarded read the remote has not answered")
	}
	select {
	case err := <-read:
		t.Fatalf("the forwarded read answered before the remote did: %v", err)
	default:
	}
}

// A forwarded mutation keeps its place on the serial worker: the remote
// receives nothing sent after it until it answers, so a caller's writes land
// in the order it sent them.
func TestHostRequestForwardedMutationsKeepTheirOrder(t *testing.T) {
	gate := newRemoteGate()
	client, calls := hostRequestDispatchHub(t, func(method string, _ json.RawMessage) hostAdminReply {
		if method == appwire.MethodEvenerPluginEnable {
			gate.wait()
		}
		return okReply()
	})
	defer gate.open()
	first := hostRequest(client, appwire.MethodEvenerPluginEnable)
	forwardedCallSeen(t, calls, appwire.MethodEvenerPluginEnable)
	second := hostRequest(client, appwire.MethodEvenerPluginDisable)

	time.Sleep(300 * time.Millisecond)
	for _, call := range calls() {
		if call.method == appwire.MethodEvenerPluginDisable {
			t.Fatal("a second forwarded mutation reached the remote while the first was still unanswered")
		}
	}

	gate.open()
	for _, done := range []<-chan error{first, second} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("forwarded mutation: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a forwarded mutation never answered")
		}
	}
	var forwarded []string
	for _, call := range calls() {
		if call.method == appwire.MethodEvenerPluginEnable || call.method == appwire.MethodEvenerPluginDisable {
			forwarded = append(forwarded, call.method)
		}
	}
	if len(forwarded) != 2 || forwarded[0] != appwire.MethodEvenerPluginEnable || forwarded[1] != appwire.MethodEvenerPluginDisable {
		t.Fatalf("the remote received %v, want enable then disable", forwarded)
	}
}
