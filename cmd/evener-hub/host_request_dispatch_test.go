package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
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
	var mu sync.Mutex
	var calls []hostAdminCall
	answer := func(req *appwire.Request) {
		var result any = appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"}
		if req.Method != appwire.MethodInitialize {
			reply := handle(req.Method, req.Params)
			if reply.closeConn {
				t.Errorf("concurrentScriptedRemote cannot close the connection for %s", req.Method)
			}
			if reply.wireErr != nil {
				_ = server.Send(ctx, appwire.ErrorMessage(req.ID, *reply.wireErr))
				return
			}
			result = reply.result
		}
		data, _ := json.Marshal(result)
		_ = server.Send(ctx, appwire.ResponseMessage(req.ID, json.RawMessage(data)))
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
	sources, hosts := scriptedRemoteHost(t, remote, true)
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
	waitFor(t, func() bool {
		return slices.ContainsFunc(calls(), func(call hostAdminCall) bool { return call.method == method })
	}, "the remote never received "+method)
}

// remoteGate holds the scripted remote's answer to one method until opened,
// and returns open. Tests defer open: the hub's server shutdown waits for that
// held call, so a test that fails with it shut would hang its cleanup.
func remoteGate() (gate chan struct{}, open func()) {
	gate = make(chan struct{})
	return gate, sync.OnceFunc(func() { close(gate) })
}

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
	gate, openGate := remoteGate()
	client, calls := hostRequestDispatchHub(t, func(method string, _ json.RawMessage) hostAdminReply {
		if method == appwire.MethodEvenerMarketplaceList {
			<-gate
		}
		return okReply()
	})
	defer openGate()
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

// A forwarded mutation never leaves the serial worker, checked through a
// signal that answers either way: with the connection's pool of admitted
// requests full of reads held at the remote, a request admitted to the pool
// is refused Unavailable at once, while one kept on the worker runs. So a
// mutation answering OK beside a full pool was not admitted.
func TestHostRequestForwardedMutationNeverTakesAReadSlot(t *testing.T) {
	gate, openGate := remoteGate()
	client, calls := hostRequestDispatchHub(t, func(method string, _ json.RawMessage) hostAdminReply {
		if method == appwire.MethodEvenerMarketplaceList {
			<-gate
		}
		return okReply()
	})
	defer openGate()
	heldReads := func() int {
		n := 0
		for _, call := range calls() {
			if call.method == appwire.MethodEvenerMarketplaceList {
				n++
			}
		}
		return n
	}
	// Send held reads one at a time until the pool refuses one, so the test
	// finds the pool's size rather than assuming it. This leans on nothing
	// between the hub and the remote capping concurrent forwarded calls below
	// the pool's size; one that did would stall a read here short of refusal.
	for i := 1; ; i++ {
		if i > 1000 {
			t.Fatal("the pool of admitted requests never filled")
		}
		done := hostRequest(client, appwire.MethodEvenerMarketplaceList)
		waitFor(t, func() bool {
			return len(done) > 0 || heldReads() == i
		}, "a forwarded read to reach the remote or be refused")
		if len(done) == 0 {
			continue
		}
		var wireErr appwire.WireError
		if err := <-done; !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeUnavailable {
			t.Fatalf("read %d answered %v, want it held at the remote or refused Unavailable", i, err)
		}
		break
	}
	// A refusal with nothing held (an offline host, say) would leave the pool
	// empty, and the mutation below would run whatever its dispatch.
	if heldReads() == 0 {
		t.Fatal("the first forwarded read was refused, so the pool never held any")
	}
	if err := answerOf(t, hostRequest(client, appwire.MethodEvenerPluginEnable)); err != nil {
		t.Fatalf("a forwarded mutation beside a full pool answered %v, want it run on the serial worker", err)
	}
}

// answerOf waits for a forwarded request's answer.
func answerOf(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("a forwarded request never answered")
		return nil
	}
}

// forwardedHostRead admits only reads that are safe out of order:
// marketplace/refresh is retry-safe but writes the remote's clone, so it
// keeps its order against marketplace writes on the worker.
func TestForwardedHostReadAdmitsOnlyOrderFreeReads(t *testing.T) {
	forward := func(method string) json.RawMessage {
		params, _ := json.Marshal(appwire.HostRequestParams{Host: "m4", Method: method})
		return params
	}
	for _, tc := range []struct {
		method string
		params json.RawMessage
		want   bool
	}{
		{appwire.MethodEvenerHostRequest, forward(appwire.MethodEvenerPluginList), true},
		{appwire.MethodEvenerHostRequest, forward(appwire.MethodEvenerMarketplaceList), true},
		{appwire.MethodEvenerHostRequest, forward(appwire.MethodEvenerMarketplaceRefresh), false},
		{appwire.MethodEvenerHostRequest, forward(appwire.MethodEvenerPluginEnable), false},
		{appwire.MethodEvenerHostRequest, forward("evener/not/allowListed"), false},
		{appwire.MethodEvenerHostRequest, json.RawMessage(`{`), false},
		{appwire.MethodEvenerPluginList, forward(appwire.MethodEvenerPluginList), false},
	} {
		if got := forwardedHostRead(tc.method, tc.params); got != tc.want {
			t.Errorf("forwardedHostRead(%s, %s) = %v, want %v", tc.method, tc.params, got, tc.want)
		}
	}
}
