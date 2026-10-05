package hub

import (
	"context"
	"encoding/json"
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

// hostRequestDispatchHub serves a real hub RPC server whose one remote host,
// m4, answers through handle, and returns a client on it plus the remote's
// recorded calls.
func hostRequestDispatchHub(t *testing.T, handle func(method string, params json.RawMessage) hostAdminReply) (*appwire.Client, func() []hostAdminCall) {
	t.Helper()
	remote, calls, _ := newScriptedAdminClient(t, handle)
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

// A forwarded mutation keeps its place on the serial worker: nothing queued
// behind it runs until it answers, so two mutations reach the remote in the
// order they were sent.
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

	ordered := make(chan struct{})
	go func() {
		_, _ = client.ThreadList(context.Background(), appwire.ThreadListParams{})
		close(ordered)
	}()
	select {
	case <-ordered:
		t.Fatal("an ordered request ran while a forwarded mutation ahead of it was still on the wire")
	case <-time.After(300 * time.Millisecond):
	}

	gate.open()
	for _, done := range []<-chan error{first, second} {
		if err := <-done; err != nil {
			t.Fatalf("forwarded mutation: %v", err)
		}
	}
	<-ordered
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
