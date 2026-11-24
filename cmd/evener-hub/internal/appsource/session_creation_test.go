package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

// The remote owner's error, not a successful response, carries the identity.
// The actual router drops the response when the handler returns an error.
func TestManagerCreatePostSpawnFailureRemoteRouter(t *testing.T) {
	owner := appserver.NewServer(appserver.ServerConfig{ServerName: "owner", SourceID: "local"})
	appserver.HandleTyped(owner.Router(), appwire.MethodThreadStart, func(_ context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
		if params.Source != "" || params.Harness != "evener" {
			t.Errorf("forwarded routing fields: %+v", params)
		}
		return appwire.ThreadStartResponse{Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: "local:should-not-be-used"}}}, appwire.WireError{
			Code: appwire.CodeUnavailable, Message: "owner read failed", Data: map[string]any{
				"evenerErrorInfo": appwire.ErrorSessionUnavailable, "cause": "ownerRead", "createdRef": "local:created", "createdInstanceId": "instance-1",
			},
		}
	})
	server := httptest.NewServer(http.HandlerFunc(owner.ServeWebSocket))
	defer server.Close()
	transport, err := appwire.DialWebSocket(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client := appwire.NewClient(transport)
	client.Start(t.Context())
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) { return client, nil })
	_, err = source.StartThread(t.Context(), appwire.ThreadStartParams{Source: "host"})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("start error %T: %v", err, err)
	}
	data, marshalErr := json.Marshal(wire.Data)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var partial struct {
		appwire.ErrorData
		CreatedRef        string `json:"createdRef"`
		CreatedInstanceID string `json:"createdInstanceId"`
	}
	if err := json.Unmarshal(data, &partial); err != nil {
		t.Fatal(err)
	}
	if wire.Code != appwire.CodeUnavailable || partial.EvenerErrorInfo != appwire.ErrorSessionUnavailable || partial.Cause != "ownerRead" || partial.CreatedRef != "host:created" || partial.CreatedInstanceID != "instance-1" || partial.RetryDisposition == appwire.RetryDispositionAutomatic {
		t.Fatalf("remote partial error lost original classification or translated identity: %+v, data=%s", wire, data)
	}
}

func TestRemoteThreadStartErrorPreservesIdempotencyFields(t *testing.T) {
	for _, tc := range []struct {
		name             string
		mutationOutcome  appwire.MutationOutcome
		retryDisposition appwire.RetryDisposition
	}{
		{name: "unknown blocked", mutationOutcome: appwire.MutationOutcomeUnknown, retryDisposition: appwire.RetryDispositionBlocked},
		{name: "target deleted none", mutationOutcome: appwire.MutationOutcomeTargetDeleted, retryDisposition: appwire.RetryDispositionNone},
		{name: "retryable partial empty", mutationOutcome: "", retryDisposition: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := appserver.NewServer(appserver.ServerConfig{
				ServerName: "owner",
				SourceID:   "local",
				Features:   appwire.FeatureSet{ThreadStartIdempotency: true, TurnStart: true},
			})
			appserver.HandleTyped(owner.Router(), appwire.MethodThreadStart, func(_ context.Context, _ appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
				return appwire.ThreadStartResponse{}, appwire.WireError{
					Code:    appwire.CodeUnavailable,
					Message: "post-spawn error",
					Data: appwire.ThreadStartPartialErrorData{
						ErrorData: appwire.ErrorData{
							EvenerErrorInfo:  appwire.ErrorSessionUnavailable,
							ClientMutationID: "mut-remote-1",
							MutationOutcome:  tc.mutationOutcome,
							RetryDisposition: tc.retryDisposition,
						},
						CreatedRef:        "local:created",
						CreatedInstanceID: "instance-1",
					},
				}
			})
			server := httptest.NewServer(http.HandlerFunc(owner.ServeWebSocket))
			defer server.Close()
			transport, err := appwire.DialWebSocket(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"), server.Client())
			if err != nil {
				t.Fatal(err)
			}
			client := appwire.NewClient(transport)
			client.Start(t.Context())
			defer client.Close()
			if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatal(err)
			}
			source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) { return client, nil })
			_, err = source.StartThread(t.Context(), appwire.ThreadStartParams{Source: "host", ClientMutationID: "mut-remote-1"})
			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("start error %T: %v", err, err)
			}
			raw, _ := json.Marshal(wire.Data)
			var got appwire.ThreadStartPartialErrorData
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.CreatedRef != "host:created" || got.CreatedInstanceID != "instance-1" ||
				got.ClientMutationID != "mut-remote-1" || got.MutationOutcome != tc.mutationOutcome || got.RetryDisposition != tc.retryDisposition {
				t.Fatalf("translated partial error = %+v, want ref=host:created id=mut-remote-1 outcome=%q retry=%q", got, tc.mutationOutcome, tc.retryDisposition)
			}
		})
	}
}

func TestRemoteThreadStartPreservesRequestNotSentError(t *testing.T) {
	capableServer := appserver.NewServer(appserver.ServerConfig{
		ServerName: "capable",
		SourceID:   "local",
		Features:   appwire.FeatureSet{ThreadStartIdempotency: true, TurnStart: true},
	})
	httpSrv := httptest.NewServer(http.HandlerFunc(capableServer.ServeWebSocket))
	defer httpSrv.Close()

	rawTransport, err := appwire.DialWebSocket(t.Context(), "ws"+strings.TrimPrefix(httpSrv.URL, "http"), httpSrv.Client())
	if err != nil {
		t.Fatal(err)
	}
	transport := &gatedWrapTransport{Transport: rawTransport, firstSendEntered: make(chan struct{}), releaseFirst: make(chan struct{})}
	client := appwire.NewClient(transport)
	client.Start(t.Context())
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	transport.armGate()
	go func() { _ = client.Notify(t.Context(), appwire.NotifyEvenerAuthUpdated, map[string]string{}) }()
	<-transport.firstSendEntered

	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) { return client, nil })
	ctx, cancel := context.WithCancel(t.Context())
	ctx = appwire.WithRequestIDObserver(ctx, func(appwire.ID) { cancel() })
	_, err = source.StartThread(ctx, appwire.ThreadStartParams{Source: "host", ClientMutationID: "mut-notsent"})
	close(transport.releaseFirst)
	if _, ok := errors.AsType[appwire.RequestNotSentError](err); !ok {
		t.Fatalf("StartThread err = %T (%v), want appwire.RequestNotSentError", err, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("StartThread err = %v, want context.Canceled in chain", err)
	}
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("StartThread err = %T (%v), want WireError in chain", err, err)
	}
	data, ok := appwire.DecodedErrorData(wire.Data)
	if wire.Code != appwire.CodeUnavailable || !ok || data.MutationOutcome != appwire.MutationOutcomeNotAccepted || data.ClientMutationID != "mut-notsent" {
		t.Fatalf("RequestNotSent WireError = %+v data=%+v, want CodeUnavailable+NotAccepted", wire, data)
	}
}

type gatedWrapTransport struct {
	appwire.Transport
	armed            atomic.Bool
	firstSendEntered chan struct{}
	releaseFirst     chan struct{}
}

func (g *gatedWrapTransport) armGate() { g.armed.Store(true) }

func (g *gatedWrapTransport) Send(ctx context.Context, msg appwire.Message) error {
	if g.armed.Load() {
		select {
		case <-g.firstSendEntered:
		default:
			close(g.firstSendEntered)
		}
		<-g.releaseFirst
		return ctx.Err()
	}
	return g.Transport.Send(ctx, msg)
}

func TestRemoteThreadStartResolvesClientOnce(t *testing.T) {
	owner := appserver.NewServer(appserver.ServerConfig{
		ServerName: "capable",
		SourceID:   "local",
		Features:   appwire.FeatureSet{ThreadStartIdempotency: true, TurnStart: true},
	})
	appserver.HandleTyped(owner.Router(), appwire.MethodThreadStart, func(_ context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
		return appwire.ThreadStartResponse{
			Thread:           appwire.Thread{ID: "sess-once", SessionID: "sess-once", Source: "local", Evener: appwire.EvenerThread{Ref: "local:sess-once", InstanceID: "sess-once"}},
			ClientMutationID: params.ClientMutationID,
			Disposition:      appwire.MutationDispositionApplied,
		}, nil
	})
	srv := httptest.NewServer(http.HandlerFunc(owner.ServeWebSocket))
	defer srv.Close()
	raw, err := appwire.DialWebSocket(t.Context(), "ws"+strings.TrimPrefix(srv.URL, "http"), srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	client := appwire.NewClient(raw)
	client.Start(t.Context())
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	var resolves atomic.Int32
	src := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		resolves.Add(1)
		return client, nil
	})
	if _, err := src.StartThread(t.Context(), appwire.ThreadStartParams{Source: "host", ClientMutationID: "mut-once"}); err != nil {
		t.Fatalf("StartThread: %v", err)
	}
	if resolves.Load() != 1 {
		t.Fatalf("resolveClient called %d times, want 1", resolves.Load())
	}
}

func TestRemoteThreadStartRefusesOlderAndOfflineDirectly(t *testing.T) {
	// Offline remote hub returns NotAccepted(id).
	offlineSrc := NewRemoteHubSource("host", nil, nil)
	offlineSrc.SetHostClientIfAttached(func(string) (*appwire.Client, bool) { return nil, false })
	_, err := offlineSrc.StartThread(t.Context(), appwire.ThreadStartParams{Source: "host", ClientMutationID: "mut-offline"})
	wire, ok := errors.AsType[appwire.WireError](err)
	data, dataOK := appwire.DecodedErrorData(wire.Data)
	if !ok || !dataOK || wire.Code != appwire.CodeUnavailable || data.MutationOutcome != appwire.MutationOutcomeNotAccepted || data.ClientMutationID != "mut-offline" {
		t.Fatalf("offline keyed StartThread = err:%v wire:%+v data:%+v", err, wire, data)
	}

	// Older remote hub returns InvalidParams + NotAccepted(id) with zero RPCs sent.
	olderSrc, calls := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		t.Fatalf("unexpected RPC %q sent to older remote hub", method)
		return scriptedReply{}
	})
	_, err = olderSrc.StartThread(t.Context(), appwire.ThreadStartParams{Source: "host", ClientMutationID: "mut-older"})
	wire, ok = errors.AsType[appwire.WireError](err)
	data, dataOK = appwire.DecodedErrorData(wire.Data)
	if !ok || !dataOK || wire.Code != appwire.CodeInvalidParams || data.MutationOutcome != appwire.MutationOutcomeNotAccepted || data.ClientMutationID != "mut-older" {
		t.Fatalf("older keyed StartThread = err:%v wire:%+v data:%+v", err, wire, data)
	}
	if got := wireCalls(calls()); len(got) != 0 {
		t.Fatalf("wireCalls to older remote = %+v, want 0", got)
	}
}
