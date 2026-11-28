package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestRemoteHubSourceThreadStartIdempotencySupportedIfAttached(t *testing.T) {
	var dials atomic.Int32
	dial := func(context.Context, string) (*appwire.Client, error) {
		dials.Add(1)
		return nil, errors.New("manifest capability check must never dial")
	}

	capableClient := newScriptedClientWithFeatures(t, appwire.FeatureSet{ThreadStartIdempotency: true})
	olderClient := newScriptedClientWithFeatures(t, appwire.FeatureSet{ThreadStartIdempotency: false})

	noLookup := NewRemoteHubSource("alpha", nil, dial)
	if noLookup.ThreadStartIdempotencySupportedIfAttached() {
		t.Fatal("expected false without attached-only lookup")
	}

	var attached bool
	var online atomic.Bool
	online.Store(true)
	var current *appwire.Client

	source := NewRemoteHubSource("alpha", nil, dial)
	source.SetHostClientIfAttached(func(string) (*appwire.Client, bool) {
		return current, attached
	})
	source.SetHostOnline(online.Load)

	if source.ThreadStartIdempotencySupportedIfAttached() {
		t.Fatal("expected false while unattached")
	}

	attached = true
	current = olderClient
	if source.ThreadStartIdempotencySupportedIfAttached() {
		t.Fatal("expected false for attached older remote client")
	}

	current = capableClient
	if !source.ThreadStartIdempotencySupportedIfAttached() {
		t.Fatal("expected true for online attached capable remote client")
	}

	online.Store(false)
	if source.ThreadStartIdempotencySupportedIfAttached() {
		t.Fatal("expected false when source reports offline")
	}

	if dials.Load() != 0 {
		t.Fatalf("dials = %d, want 0", dials.Load())
	}
}

func newScriptedClientWithFeatures(t *testing.T, features appwire.FeatureSet) *appwire.Client {
	t.Helper()
	srv := appserver.NewServer(appserver.ServerConfig{ServerName: "remote", SourceID: "local", Features: features})
	srvHTTP := httptest.NewServer(http.HandlerFunc(srv.ServeWebSocket))
	t.Cleanup(srvHTTP.Close)
	transport, err := appwire.DialWebSocket(t.Context(), "ws"+strings.TrimPrefix(srvHTTP.URL, "http"), srvHTTP.Client())
	if err != nil {
		t.Fatal(err)
	}
	client := appwire.NewClient(transport)
	client.Start(t.Context())
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	return client
}

type sendFailAfterInitTransport struct {
	appwire.Transport
	armed atomic.Bool
	err   error
}

func (s *sendFailAfterInitTransport) Send(ctx context.Context, msg appwire.Message) error {
	if s.armed.Load() {
		return s.err
	}
	return s.Transport.Send(ctx, msg)
}

func newCapableScriptedRemote(t *testing.T, id string, handle func(method string, params json.RawMessage) scriptedReply) (*RemoteHubSource, func() []remoteCall) {
	t.Helper()
	return newScriptedRemoteWithFeatures(t, id, appwire.FeatureSet{ThreadStartIdempotency: true, TurnStart: true}, handle)
}

func TestRemoteThreadStartKeyedPostSendTransportDropIsOutcomeUnknown(t *testing.T) {
	t.Parallel()

	t.Run("response loss on closed transport", func(t *testing.T) {
		t.Parallel()
		received := make(chan struct{})
		source, calls := newCapableScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
			if method == appwire.MethodThreadStart {
				close(received)
				return scriptedReply{closeConn: true}
			}
			return scriptedReply{result: map[string]any{}}
		})
		_, err := source.StartThread(t.Context(), appwire.ThreadStartParams{Source: "host", ClientMutationID: "mut-drop"})
		select {
		case <-received:
		case <-time.After(5 * time.Second):
			t.Fatal("remote never received thread/start")
		}
		lastMethodCall(t, calls(), appwire.MethodThreadStart)
		assertMutationOutcomeUnknownAutomatic(t, err, "mut-drop")
	})

	t.Run("mid-send closed stream error", func(t *testing.T) {
		t.Parallel()
		srv := appserver.NewServer(appserver.ServerConfig{
			ServerName: "capable",
			SourceID:   "local",
			Features:   appwire.FeatureSet{ThreadStartIdempotency: true, TurnStart: true},
		})
		httpSrv := httptest.NewServer(http.HandlerFunc(srv.ServeWebSocket))
		defer httpSrv.Close()
		raw, err := appwire.DialWebSocket(t.Context(), "ws"+strings.TrimPrefix(httpSrv.URL, "http"), httpSrv.Client())
		if err != nil {
			t.Fatal(err)
		}
		transport := &sendFailAfterInitTransport{Transport: raw, err: fmt.Errorf("send: %w", io.ErrClosedPipe)}
		client := appwire.NewClient(transport)
		client.Start(t.Context())
		defer client.Close()
		if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatal(err)
		}
		transport.armed.Store(true)
		source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) { return client, nil })
		_, err = source.StartThread(t.Context(), appwire.ThreadStartParams{Source: "host", ClientMutationID: "mut-send-drop"})
		assertMutationOutcomeUnknownAutomatic(t, err, "mut-send-drop")
	})
}

func TestRemoteThreadStartKeyedPostSendForwardingDeadlineIsOutcomeUnknown(t *testing.T) {
	t.Parallel()
	received := make(chan struct{})
	release := make(chan struct{})
	source, calls := newCapableScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method == appwire.MethodThreadStart {
			close(received)
			<-release
		}
		return scriptedReply{result: map[string]any{}}
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	ctx := &onDemandDeadlineContext{Context: t.Context(), done: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := source.StartThread(ctx, appwire.ThreadStartParams{Source: "host", ClientMutationID: "mut-deadline"})
		done <- err
	}()
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("remote never received thread/start")
	}
	ctx.expire()
	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StartThread did not return after deadline expired")
	}
	lastMethodCall(t, calls(), appwire.MethodThreadStart)
	assertMutationOutcomeUnknownAutomatic(t, err, "mut-deadline")
}

func TestRemoteThreadStartKeyedPreservesCallerCancellationRaw(t *testing.T) {
	t.Parallel()
	received := make(chan struct{})
	release := make(chan struct{})
	source, calls := newCapableScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method == appwire.MethodThreadStart {
			close(received)
			<-release
		}
		return scriptedReply{result: map[string]any{}}
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := source.StartThread(ctx, appwire.ThreadStartParams{Source: "host", ClientMutationID: "mut-cancel"})
		done <- err
	}()
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("remote never received thread/start")
	}
	cancel()
	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StartThread did not return after caller cancellation")
	}
	lastMethodCall(t, calls(), appwire.MethodThreadStart)
	assertRawContextError(t, err, context.Canceled)
}

func TestRemoteThreadStartKeyedPreservesRequestNotSentDeadlineAsNotAccepted(t *testing.T) {
	t.Parallel()
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
	deadlineCtx := &onDemandDeadlineContext{Context: t.Context(), done: make(chan struct{})}
	ctx := appwire.WithRequestIDObserver(deadlineCtx, func(appwire.ID) { deadlineCtx.expire() })
	_, err = source.StartThread(ctx, appwire.ThreadStartParams{Source: "host", ClientMutationID: "mut-notsent-deadline"})
	close(transport.releaseFirst)
	if _, ok := errors.AsType[appwire.RequestNotSentError](err); !ok {
		t.Fatalf("StartThread err = %T (%v), want appwire.RequestNotSentError", err, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("StartThread err = %v, want context.DeadlineExceeded in chain", err)
	}
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("StartThread err = %T (%v), want WireError in chain", err, err)
	}
	data, ok := appwire.DecodedErrorData(wire.Data)
	if wire.Code != appwire.CodeUnavailable || !ok || data.MutationOutcome != appwire.MutationOutcomeNotAccepted || data.RetryDisposition != appwire.RetryDispositionNone || data.ClientMutationID != "mut-notsent-deadline" {
		t.Fatalf("RequestNotSent deadline WireError = %+v data=%+v, want CodeUnavailable+NotAccepted+RetryNone", wire, data)
	}
}

func TestRemoteThreadStartKeyedPreservesDeliveredVerdicts(t *testing.T) {
	t.Parallel()

	t.Run("delivered internal error with transport-like text stays internal verdict", func(t *testing.T) {
		t.Parallel()
		delivered := appwire.InternalError("cannot parse transcript: unexpected EOF")
		source, _ := newCapableScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
			if method == appwire.MethodThreadStart {
				return scriptedReply{wireErr: &delivered}
			}
			return scriptedReply{result: map[string]any{}}
		})
		_, err := source.StartThread(t.Context(), appwire.ThreadStartParams{Source: "host", ClientMutationID: "mut-delivered-int"})
		wire := requireWireError(t, err, appwire.CodeInternalError)
		if appwire.IsTransportFailure(err) {
			t.Fatalf("delivered InternalError misclassified as transport failure: %v", err)
		}
		if data, ok := appwire.DecodedErrorData(wire.Data); ok && data.MutationOutcome == appwire.MutationOutcomeUnknown {
			t.Fatalf("delivered InternalError became mutationOutcomeUnknown: %+v", data)
		}
	})

	t.Run("delivered invalidParams refusal stays invalidParams", func(t *testing.T) {
		t.Parallel()
		delivered := appwire.InvalidParams("invalid remote cwd").NotAccepted("mut-delivered-invalid")
		source, _ := newCapableScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
			if method == appwire.MethodThreadStart {
				return scriptedReply{wireErr: &delivered}
			}
			return scriptedReply{result: map[string]any{}}
		})
		_, err := source.StartThread(t.Context(), appwire.ThreadStartParams{Source: "host", ClientMutationID: "mut-delivered-invalid"})
		wire := requireWireError(t, err, appwire.CodeInvalidParams)
		data, ok := appwire.DecodedErrorData(wire.Data)
		if !ok || data.EvenerErrorInfo != appwire.ErrorInvalidParams || data.MutationOutcome != appwire.MutationOutcomeNotAccepted || data.ClientMutationID != "mut-delivered-invalid" {
			t.Fatalf("delivered invalidParams lost fields: wire=%+v data=%+v", wire, data)
		}
	})

	t.Run("delivered partial verdict translates createdRef and createdInstanceId", func(t *testing.T) {
		t.Parallel()
		verdict := appwire.WireError{
			Code:    appwire.CodeUnavailable,
			Message: "post-spawn failure",
			Data: appwire.ThreadStartPartialErrorData{
				ErrorData: appwire.ErrorData{
					EvenerErrorInfo:  appwire.ErrorSessionUnavailable,
					ClientMutationID: "mut-delivered-partial",
					MutationOutcome:  appwire.MutationOutcomeUnknown,
					RetryDisposition: appwire.RetryDispositionBlocked,
				},
				CreatedRef:        "local:created-session",
				CreatedInstanceID: "inst-delivered",
			},
		}
		source, _ := newCapableScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
			if method == appwire.MethodThreadStart {
				return scriptedReply{wireErr: &verdict}
			}
			return scriptedReply{result: map[string]any{}}
		})
		_, err := source.StartThread(t.Context(), appwire.ThreadStartParams{Source: "host", ClientMutationID: "mut-delivered-partial"})
		wire := requireWireError(t, err, appwire.CodeUnavailable)
		raw, merr := json.Marshal(wire.Data)
		if merr != nil {
			t.Fatal(merr)
		}
		var got appwire.ThreadStartPartialErrorData
		if uerr := json.Unmarshal(raw, &got); uerr != nil {
			t.Fatal(uerr)
		}
		if got.CreatedRef != "host:created-session" || got.CreatedInstanceID != "inst-delivered" ||
			got.ClientMutationID != "mut-delivered-partial" || got.MutationOutcome != appwire.MutationOutcomeUnknown ||
			got.RetryDisposition != appwire.RetryDispositionBlocked {
			t.Fatalf("delivered partial verdict lost fields: %+v", got)
		}
	})
}
