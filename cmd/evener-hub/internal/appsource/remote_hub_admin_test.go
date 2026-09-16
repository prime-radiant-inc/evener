package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

func TestRemoteHubSourceAdminCallReturnsRemoteResultVerbatim(t *testing.T) {
	source, calls := newScriptedRemote(t, "host", func(method string, params json.RawMessage) scriptedReply {
		if method != appwire.MethodEvenerInstanceList {
			t.Errorf("method = %q, want %q", method, appwire.MethodEvenerInstanceList)
		}
		return scriptedReply{result: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{{Name: "openai"}}}}
	})

	var out json.RawMessage
	if err := source.AdminCall(context.Background(), appwire.MethodEvenerInstanceList, json.RawMessage(`{"limit":1}`), &out); err != nil {
		t.Fatalf("AdminCall: %v", err)
	}
	if got := string(lastMethodCall(t, calls(), appwire.MethodEvenerInstanceList)); got != `{"limit":1}` {
		t.Fatalf("forwarded params = %s, want the caller's params unchanged", got)
	}
	var decoded appwire.InstanceListResponse
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decode result %s: %v", out, err)
	}
	if len(decoded.Instances) != 1 || decoded.Instances[0].Name != "openai" {
		t.Fatalf("result = +%v, want the remote's list", decoded.Instances)
	}
}

func TestRemoteHubSourceAdminCallPreservesSemanticWireError(t *testing.T) {
	refusal := appwire.InvalidParams("refused by the host")
	source, _ := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{wireErr: &refusal}
	})

	var out json.RawMessage
	err := source.AdminCall(context.Background(), appwire.MethodEvenerLaunchSetLayer, nil, &out)
	if err == nil {
		t.Fatal("AdminCall succeeded despite the remote's refusal")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("code = %d, want %d", wire.Code, appwire.CodeInvalidParams)
	}
}

// TestRemoteHubSourceAdminCallTransportFailureBecomesSessionUnavailable pins the
// component-07a error mapping: a channel that dies mid-call must reach the proxy
// as a typed SessionUnavailable, not a raw transport error that WireError would
// flatten to InternalError. It matches the non-proxy forwarding paths (e.g.
// TestRemoteHubSourceEOFBecomesSessionUnavailable).
func TestRemoteHubSourceAdminCallTransportFailureBecomesSessionUnavailable(t *testing.T) {
	source, _ := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{closeConn: true}
	})

	var out json.RawMessage
	err := source.AdminCall(context.Background(), appwire.MethodEvenerInstanceList, nil, &out)
	if err == nil {
		t.Fatal("AdminCall succeeded after the remote closed the pipe")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeUnavailable {
		t.Fatalf("code = %d, want %d", wire.Code, appwire.CodeUnavailable)
	}
	if info := wireErrorInfo(wire); info != string(appwire.ErrorSessionUnavailable) {
		t.Fatalf("evenerErrorInfo = %q, want %q", info, appwire.ErrorSessionUnavailable)
	}
}

// TestRemoteHubSourceAdminMutationCallReturnsRemoteResultVerbatim pins the
// success path of the mutating twin: params and result pass through exactly as
// AdminCall passes them.
func TestRemoteHubSourceAdminMutationCallReturnsRemoteResultVerbatim(t *testing.T) {
	source, calls := newScriptedRemote(t, "host", func(method string, params json.RawMessage) scriptedReply {
		if method != appwire.MethodEvenerPluginInstall {
			t.Errorf("method = %q, want %q", method, appwire.MethodEvenerPluginInstall)
		}
		return scriptedReply{result: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{{Name: "openai"}}}}
	})

	var out json.RawMessage
	if err := source.AdminMutationCall(context.Background(), appwire.MethodEvenerPluginInstall, json.RawMessage(`{"name":"p"}`), &out); err != nil {
		t.Fatalf("AdminMutationCall: %v", err)
	}
	if got := string(lastMethodCall(t, calls(), appwire.MethodEvenerPluginInstall)); got != `{"name":"p"}` {
		t.Fatalf("forwarded params = %s, want the caller's params unchanged", got)
	}
	if len(out) == 0 {
		t.Fatal("result is empty, want the remote's result verbatim")
	}
}

// TestRemoteHubSourceAdminMutationCallMapsResponseLossToOutcomeUnknown pins the
// round-three retry-safety contract: a non-idempotent forwarded admin mutation
// whose response is lost must NOT reach the proxy as SessionUnavailable (which
// invites a blind retry), but as an explicit outcome-unknown error that says
// the change may or may not have been applied. The read path keeps the
// SessionUnavailable mapping pinned by
// TestRemoteHubSourceAdminCallTransportFailureBecomesSessionUnavailable.
func TestRemoteHubSourceAdminMutationCallMapsResponseLossToOutcomeUnknown(t *testing.T) {
	source, _ := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{closeConn: true}
	})

	var out json.RawMessage
	err := source.AdminMutationCall(context.Background(), appwire.MethodEvenerPluginInstall, nil, &out)
	if err == nil {
		t.Fatal("AdminMutationCall succeeded after the remote closed the pipe")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeInternalError {
		t.Fatalf("code = %d, want %d", wire.Code, appwire.CodeInternalError)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok {
		t.Fatalf("Data = %T %v, want appwire.ErrorData", wire.Data, wire.Data)
	}
	if data.EvenerErrorInfo != appwire.ErrorMutationOutcomeUnknown {
		t.Fatalf("evenerErrorInfo = %q, want %q", data.EvenerErrorInfo, appwire.ErrorMutationOutcomeUnknown)
	}
	if data.MutationOutcome != appwire.MutationOutcomeUnknown {
		t.Fatalf("mutationOutcome = %q, want %q", data.MutationOutcome, appwire.MutationOutcomeUnknown)
	}
	// Blocked, not Automatic: unlike a forwarded thread mutation, an admin
	// forward carries no clientMutationId (HostRequestParams has none) and no
	// admin method dedups, so the caller must not retry automatically.
	if data.RetryDisposition != appwire.RetryDispositionBlocked {
		t.Fatalf("retryDisposition = %q, want %q", data.RetryDisposition, appwire.RetryDispositionBlocked)
	}
	if !strings.Contains(wire.Message, "host") {
		t.Fatalf("message = %q, want it to name the host", wire.Message)
	}
}

// closedPipeTransport is a client transport whose send path fails the way a
// partially-written stream does: the write side is already gone, so the frame
// write is refused. Nothing reaches the peer, so the response the caller waits
// for can never arrive.
type closedPipeTransport struct{ err error }

func (t closedPipeTransport) Send(context.Context, appwire.Message) error { return t.err }

func (t closedPipeTransport) Recv(ctx context.Context) (appwire.Message, error) {
	<-ctx.Done()
	return appwire.Message{}, ctx.Err()
}

func (t closedPipeTransport) Close() error { return nil }

// TestRemoteHubSourceAdminMutationCallMapsClosedPipeWriteToOutcomeUnknown pins
// round eight's medium finding: io.ErrClosedPipe is the transport loss the send
// path reports when a write is refused on a connection that is already gone,
// and transportUnavailable must recognize it so the mutation path can re-label
// it. Unrecognized, the failure escaped as a raw error — which reads as
// "nothing happened, retry" for a forwarded instance/create or plugin/install
// that may well have been applied before the write side closed. Both the bare
// error and a wrapped one are covered, because a real transport reports it
// through its own message.
func TestRemoteHubSourceAdminMutationCallMapsClosedPipeWriteToOutcomeUnknown(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "bare", err: io.ErrClosedPipe},
		{name: "wrapped", err: fmt.Errorf("appwire send: %w", io.ErrClosedPipe)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
				return appwire.NewClient(closedPipeTransport{err: tc.err}), nil
			})

			var out json.RawMessage
			err := source.AdminMutationCall(context.Background(), appwire.MethodEvenerPluginInstall, nil, &out)
			if err == nil {
				t.Fatal("AdminMutationCall succeeded after the write side closed")
			}
			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("error = %T %v, want appwire.WireError", err, err)
			}
			if wire.Code != appwire.CodeInternalError {
				t.Fatalf("code = %d, want %d", wire.Code, appwire.CodeInternalError)
			}
			data, ok := wire.Data.(appwire.ErrorData)
			if !ok {
				t.Fatalf("Data = %T %v, want appwire.ErrorData", wire.Data, wire.Data)
			}
			if data.EvenerErrorInfo != appwire.ErrorMutationOutcomeUnknown {
				t.Fatalf("evenerErrorInfo = %q, want %q", data.EvenerErrorInfo, appwire.ErrorMutationOutcomeUnknown)
			}
			if data.MutationOutcome != appwire.MutationOutcomeUnknown {
				t.Fatalf("mutationOutcome = %q, want %q", data.MutationOutcome, appwire.MutationOutcomeUnknown)
			}
			// Blocked, not Automatic: the admin forward carries no idempotency
			// key, exactly as TestRemoteHubSourceAdminMutationCallMapsResponseLossToOutcomeUnknown pins.
			if data.RetryDisposition != appwire.RetryDispositionBlocked {
				t.Fatalf("retryDisposition = %q, want %q", data.RetryDisposition, appwire.RetryDispositionBlocked)
			}
			if !strings.Contains(wire.Message, "host") {
				t.Fatalf("message = %q, want it to name the host", wire.Message)
			}
		})
	}
}

// TestRemoteHubSourceAdminCallClosedPipeWriteStaysSessionUnavailable pins that
// the read path keeps AdminCall's mapping for the same write-side loss: a read
// is idempotent, so SessionUnavailable — a safe retry — is correct there, and
// the blocked disposition stays scoped to mutations.
func TestRemoteHubSourceAdminCallClosedPipeWriteStaysSessionUnavailable(t *testing.T) {
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return appwire.NewClient(closedPipeTransport{err: io.ErrClosedPipe}), nil
	})

	var out json.RawMessage
	err := source.AdminCall(context.Background(), appwire.MethodEvenerInstanceList, nil, &out)
	if err == nil {
		t.Fatal("AdminCall succeeded after the write side closed")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeUnavailable {
		t.Fatalf("code = %d, want %d", wire.Code, appwire.CodeUnavailable)
	}
	if info := wireErrorInfo(wire); info != string(appwire.ErrorSessionUnavailable) {
		t.Fatalf("evenerErrorInfo = %q, want %q", info, appwire.ErrorSessionUnavailable)
	}
}

// TestRemoteHubSourceAdminMutationCallPreservesSemanticWireError pins that the
// mutating twin launders nothing: a refusal the remote itself sends keeps its
// code and message, exactly as on AdminCall.
func TestRemoteHubSourceAdminMutationCallPreservesSemanticWireError(t *testing.T) {
	refusal := appwire.InvalidParams("refused by the host")
	source, _ := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{wireErr: &refusal}
	})

	var out json.RawMessage
	err := source.AdminMutationCall(context.Background(), appwire.MethodEvenerAuthApiKeySet, nil, &out)
	if err == nil {
		t.Fatal("AdminMutationCall succeeded despite the remote's refusal")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("code = %d, want %d", wire.Code, appwire.CodeInvalidParams)
	}
	if !strings.Contains(wire.Message, "refused by the host") {
		t.Fatalf("message = %q, want the remote's own text", wire.Message)
	}
}

// TestRemoteHubSourceAdminMutationCallClientFailureIsNotOutcomeUnknown pins the
// round-five medium finding: a connector failure — the host is offline, the
// attach is refused, the dial fails — happens before client.Request is ever
// issued, so the mutation provably did not reach the host. It must report a
// plain SessionUnavailable (a safe retry), never
// ErrorMutationOutcomeUnknown/RetryDispositionBlocked, which would tell the
// caller the change may have been applied and discourage a retry that cannot
// double-apply anything. The received-loss mapping is pinned separately by
// TestRemoteHubSourceAdminMutationCallMapsResponseLossToOutcomeUnknown.
func TestRemoteHubSourceAdminMutationCallClientFailureIsNotOutcomeUnknown(t *testing.T) {
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return nil, fmt.Errorf("dial remote host: %w", syscall.ECONNREFUSED)
	})

	var out json.RawMessage
	err := source.AdminMutationCall(context.Background(), appwire.MethodEvenerPluginInstall, nil, &out)
	if err == nil {
		t.Fatal("AdminMutationCall succeeded despite the connector failure")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeUnavailable {
		t.Fatalf("code = %d, want %d (session unavailable, a safe retry)", wire.Code, appwire.CodeUnavailable)
	}
	if info := wireErrorInfo(wire); info != string(appwire.ErrorSessionUnavailable) {
		t.Fatalf("evenerErrorInfo = %q, want %q", info, appwire.ErrorSessionUnavailable)
	}
	if data, ok := wire.Data.(appwire.ErrorData); ok && data.EvenerErrorInfo == appwire.ErrorMutationOutcomeUnknown {
		t.Fatalf("connector failure was reported as %q; the mutation never reached the host", data.EvenerErrorInfo)
	}
}

// blockingRemoteSource wires a scripted remote that records the forwarded
// request, signals receipt on the returned channel, and then parks until the
// test ends (its cleanup releases the handler). The caller therefore controls
// exactly when the call is torn down: everything after receipt is post-send,
// which is the window these tests are about.
func blockingRemoteSource(t *testing.T, forwardedMethod string) (*RemoteHubSource, <-chan struct{}, func() []remoteCall) {
	t.Helper()
	received := make(chan struct{})
	release := make(chan struct{})
	source, calls := newScriptedRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		if method != forwardedMethod {
			return scriptedReply{result: appwire.EmptyResponse{}}
		}
		close(received)
		<-release
		return scriptedReply{result: appwire.EmptyResponse{}}
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	return source, received, calls
}

// TestRemoteHubSourceAdminMutationCallPostSendCancellationIsOutcomeUnknown pins
// the round-seven medium finding. A forwarded admin mutation can be written to
// the host and then report the caller's own context end (the browser
// disconnecting cancels the RPC handler's context), and appwire.Client reports
// that identically whether it stopped the frame write or the response wait, so
// the raw cancellation the mutating path used to return reads as "nothing
// happened, retry" — the blind retry that duplicates a forwarded
// instance/create or plugin/install.
//
// The remote here records the request and stays silent, so the cancellation
// provably lands after the frame is on the wire; the mapping must still be
// ErrorMutationOutcomeUnknown/RetryDispositionBlocked. Same assertion, same
// error shapes, as the lost-response case pinned by
// TestRemoteHubSourceAdminMutationCallMapsResponseLossToOutcomeUnknown.
func TestRemoteHubSourceAdminMutationCallPostSendCancellationIsOutcomeUnknown(t *testing.T) {
	source, received, calls := blockingRemoteSource(t, appwire.MethodEvenerPluginInstall)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		var out json.RawMessage
		done <- source.AdminMutationCall(ctx, appwire.MethodEvenerPluginInstall, json.RawMessage(`{"name":"p"}`), &out)
	}()

	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("the remote never received the forwarded mutation")
	}
	cancel()

	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AdminMutationCall did not return after its context was canceled")
	}

	// The request was delivered, so this is the post-send window the finding is
	// about — not a pre-call cancellation that provably sent nothing.
	lastMethodCall(t, calls(), appwire.MethodEvenerPluginInstall)

	if err == nil {
		t.Fatal("AdminMutationCall succeeded after the caller's context ended mid-call")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; the raw cancellation escapes, so a caller can blind-retry a mutation that may have been applied", err)
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeInternalError {
		t.Fatalf("code = %d, want %d", wire.Code, appwire.CodeInternalError)
	}
	if info := wireErrorInfo(wire); info != string(appwire.ErrorMutationOutcomeUnknown) {
		t.Fatalf("evenerErrorInfo = %q, want %q", info, appwire.ErrorMutationOutcomeUnknown)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok {
		t.Fatalf("Data = %T %v, want appwire.ErrorData", wire.Data, wire.Data)
	}
	if data.MutationOutcome != appwire.MutationOutcomeUnknown {
		t.Fatalf("mutationOutcome = %q, want %q", data.MutationOutcome, appwire.MutationOutcomeUnknown)
	}
	if data.RetryDisposition != appwire.RetryDispositionBlocked {
		t.Fatalf("retryDisposition = %q, want %q", data.RetryDisposition, appwire.RetryDispositionBlocked)
	}
	if !strings.Contains(wire.Message, "host") {
		t.Fatalf("message = %q, want it to name the host", wire.Message)
	}
}

// TestRemoteHubSourceAdminMutationCallPostSendDeadlineIsOutcomeUnknown pins the
// second half of the in-flight rule. A deadline that expires while the call is
// in flight loses the response exactly as a cancellation does, and this
// classification is not new: mapCallError mapped DeadlineExceeded to
// SessionUnavailable, which the mutation mapping already re-labelled as
// outcome-unknown. It is pinned here because the round-seven fix classifies
// both context ends explicitly instead of relying on that re-labelling, so a
// future change to either mapping cannot silently turn a possibly-applied
// mutation back into a retryable session failure.
//
// The context stub is the only way to end a call on demand with
// DeadlineExceeded rather than Canceled; the remote still receives the frame
// first, so the deadline is post-send.
func TestRemoteHubSourceAdminMutationCallPostSendDeadlineIsOutcomeUnknown(t *testing.T) {
	source, received, calls := blockingRemoteSource(t, appwire.MethodEvenerPluginInstall)

	ctx := &onDemandDeadlineContext{Context: context.Background(), done: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		var out json.RawMessage
		done <- source.AdminMutationCall(ctx, appwire.MethodEvenerPluginInstall, nil, &out)
	}()

	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("the remote never received the forwarded mutation")
	}
	ctx.expire()

	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AdminMutationCall did not return after its deadline expired")
	}
	lastMethodCall(t, calls(), appwire.MethodEvenerPluginInstall)

	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError (a lost response, not a retryable session failure)", err, err)
	}
	if info := wireErrorInfo(wire); info != string(appwire.ErrorMutationOutcomeUnknown) {
		t.Fatalf("evenerErrorInfo = %q, want %q", info, appwire.ErrorMutationOutcomeUnknown)
	}
}

// onDemandDeadlineContext is a context whose Err reports DeadlineExceeded once
// expire is called. A real WithTimeout would have to fire on a wall-clock
// schedule, which cannot be ordered against the scripted remote's receipt.
type onDemandDeadlineContext struct {
	context.Context
	done chan struct{}
}

func (c *onDemandDeadlineContext) Done() <-chan struct{} { return c.done }

func (c *onDemandDeadlineContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func (c *onDemandDeadlineContext) expire() {
	select {
	case <-c.done:
	default:
		close(c.done)
	}
}

// TestRemoteHubSourceAdminMutationCallPreCallCancellationStaysRaw pins the
// boundary the round-seven fix must not cross: a context already ended before
// the request is handed to the client provably sent nothing, so it keeps
// AdminCall's raw cancellation (a safe retry) instead of the blocked
// outcome-unknown. The recorded calls prove no frame went out.
func TestRemoteHubSourceAdminMutationCallPreCallCancellationStaysRaw(t *testing.T) {
	source, calls := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.EmptyResponse{}}
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out json.RawMessage
	err := source.AdminMutationCall(ctx, appwire.MethodEvenerPluginInstall, nil, &out)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %T %v, want the caller's own context.Canceled", err, err)
	}
	if got := calls(); len(got) != 1 {
		t.Fatalf("remote calls = %+v, want only initialize: a context canceled before the call sends nothing", got)
	}
}

// TestRemoteHubSourceAdminCallPostSendCancellationStaysRaw pins that the
// round-seven fix is scoped to mutations: a read whose response is lost to the
// caller's cancellation is a harmless retry, so AdminCall keeps returning the
// raw cancellation rather than the blocked outcome-unknown.
func TestRemoteHubSourceAdminCallPostSendCancellationStaysRaw(t *testing.T) {
	source, received, _ := blockingRemoteSource(t, appwire.MethodEvenerInstanceList)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		var out json.RawMessage
		done <- source.AdminCall(ctx, appwire.MethodEvenerInstanceList, nil, &out)
	}()

	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("the remote never received the forwarded read")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %T %v, want the raw context.Canceled for a read", err, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AdminCall did not return after its context was canceled")
	}
}

// TestRemoteHubSourceHostSubscriptionClosesOnContextEnd pins the documented
// channel-closure contract: when the subscription's context ends the returned
// channel is closed, not merely unregistered. A caller ranging over it (the
// 07a fanOut) must observe the close so its pump goroutine ends with the cycle.
func TestRemoteHubSourceHostSubscriptionClosesOnContextEnd(t *testing.T) {
	client, _ := newScriptedClient(t, func(string, json.RawMessage) scriptedReply {
		return scriptedReply{}
	})
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	subCtx, cancel := context.WithCancel(t.Context())
	notifications, err := source.SubscribeHostNotifications(subCtx)
	if err != nil {
		t.Fatalf("SubscribeHostNotifications: %v", err)
	}
	cancel()

	select {
	case _, ok := <-notifications:
		if ok {
			t.Fatal("received a notification, want the subscription to close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subscription channel was not closed after its context ended")
	}
}

// TestRemoteHubSourceHostSubscriptionClosesWhenClientStreamEnds pins the other
// half of the contract: when the client's notification stream ends (a reconnect
// or a dead channel) the subscription is torn down and the returned channel is
// closed so the consumer rebinds the next client.
func TestRemoteHubSourceHostSubscriptionClosesWhenClientStreamEnds(t *testing.T) {
	source, _ := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{closeConn: true}
	})

	subCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	notifications, err := source.SubscribeHostNotifications(subCtx)
	if err != nil {
		t.Fatalf("SubscribeHostNotifications: %v", err)
	}

	// Kill the client's connection mid-subscription; the drain loop exits and
	// the subscription must follow.
	var out json.RawMessage
	_ = source.AdminCall(context.Background(), appwire.MethodEvenerInstanceList, nil, &out)

	select {
	case _, ok := <-notifications:
		if ok {
			t.Fatal("received a notification, want the subscription to close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subscription channel was not closed after the client's stream ended")
	}
}

// TestRemoteHubSourceHostFanOutDoesNotBlockOnFullConsumer pins the
// head-of-line property: a host-level consumer that stops reading must never
// stall the shared drain goroutine, which also routes every thread
// notification. Delivery to a full consumer is dropped rather than blocked.
func TestRemoteHubSourceHostFanOutDoesNotBlockOnFullConsumer(t *testing.T) {
	client, _ := newScriptedClient(t, func(string, json.RawMessage) scriptedReply {
		return scriptedReply{}
	})
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	subCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// The returned channel is deliberately never read, so its buffer fills and
	// the subscription's pump parks on the out send.
	if _, err := source.SubscribeHostNotifications(subCtx); err != nil {
		t.Fatalf("SubscribeHostNotifications: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range remoteHubSubBuffer * 4 {
			source.publishHostNotification(client, appwire.Notification{Method: appwire.NotifyEvenerAuthUpdated})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publishHostNotification blocked on a consumer that stopped reading")
	}
	if source.hostNotifyDropped.Load() == 0 {
		t.Fatal("no notification was dropped against a full consumer buffer")
	}
}

// TestRemoteHubSourceHostNotificationScopedToOwningClient pins the reconnect
// safety property behind the dedicated teardown signal: delivery is scoped to
// the client that owns the subscription, so the old client's teardown can never
// race a send from the new client's drain goroutine (the send-on-closed-channel
// panic), and one connection's traffic never leaks into another's subscription.
func TestRemoteHubSourceHostNotificationScopedToOwningClient(t *testing.T) {
	owner, _ := newScriptedClient(t, func(string, json.RawMessage) scriptedReply {
		return scriptedReply{}
	})
	other, _ := newScriptedClient(t, func(string, json.RawMessage) scriptedReply {
		return scriptedReply{}
	})
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return owner, nil
	})

	subCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	notifications, err := source.SubscribeHostNotifications(subCtx)
	if err != nil {
		t.Fatalf("SubscribeHostNotifications: %v", err)
	}

	// Another client's drain goroutine must not reach this subscription.
	source.publishHostNotification(other, appwire.Notification{Method: appwire.NotifyEvenerAuthUpdated})
	select {
	case notification := <-notifications:
		t.Fatalf("subscription received %q published by a foreign client", notification.Method)
	case <-time.After(100 * time.Millisecond):
	}

	// The owning client's drain goroutine still delivers.
	source.publishHostNotification(owner, appwire.Notification{Method: appwire.NotifyEvenerAuthUpdated})
	select {
	case notification := <-notifications:
		if notification.Method != appwire.NotifyEvenerAuthUpdated {
			t.Fatalf("notification method = %q", notification.Method)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the owning client's notification")
	}
}

// TestRemoteHubSourceHostSubscriptionClosesWhenClientStreamEndsWhilePumpBlocked
// pins the teardown path a full consumer buffer would otherwise hide: a pump
// parked on the out send must still observe the owning client's teardown and
// close the returned channel, or the fan-out could never rebind.
func TestRemoteHubSourceHostSubscriptionClosesWhenClientStreamEndsWhilePumpBlocked(t *testing.T) {
	source, _ := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{closeConn: true}
	})

	subCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// The returned channel is deliberately never read, so the pump fills out and
	// parks on the send.
	notifications, err := source.SubscribeHostNotifications(subCtx)
	if err != nil {
		t.Fatalf("SubscribeHostNotifications: %v", err)
	}
	client := source.hostSubClient(t)
	for range remoteHubSubBuffer * 3 {
		source.publishHostNotification(client, appwire.Notification{Method: appwire.NotifyEvenerAuthUpdated})
	}

	// Kill the client's connection mid-subscription; the drain loop exits and the
	// blocked pump must follow.
	var out json.RawMessage
	_ = source.AdminCall(context.Background(), appwire.MethodEvenerInstanceList, nil, &out)

	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-notifications:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("subscription channel was not closed while its pump was parked on a full out")
		}
	}
}

// TestRemoteHubSourceHostSubscriptionClosesWhenThreadDeliveryBackpressures pins
// the round-four lifecycle fix: the shared drain goroutine must keep observing
// the client's own notification stream while a thread subscription's consumer is
// backpressured, so the client's teardown still runs the drain's deferred cleanup
// and closes every host subscription. Without that, a full thread in buffer parks
// the drain on the thread send, clientDone is never closed, and the 07a host
// fan-out stays attached to a dead client across reconnect.
func TestRemoteHubSourceHostSubscriptionClosesWhenThreadDeliveryBackpressures(t *testing.T) {
	remote := newPushableRemote(t, "host", func(method string, _ json.RawMessage) scriptedReply {
		switch method {
		case appwire.MethodThreadRead:
			return scriptedReply{result: appwire.ThreadReadResponse{}}
		case appwire.MethodModelList:
			// Fired on demand, once the drain is parked, to close the pipe.
			return scriptedReply{closeConn: true}
		default:
			t.Errorf("unexpected method %q", method)
			return scriptedReply{result: appwire.EmptyResponse{}}
		}
	})
	ctx := t.Context()

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Reject every notification at publish time: this test is about the returned
	// channel closing, not about which host notifications are delivered, and a
	// rejecting filter keeps the host pump from parking on its own out buffer
	// first.
	remote.source.SetHostNotificationFilter(func(string) bool { return false })
	notifications, err := remote.source.SubscribeHostNotifications(subCtx)
	if err != nil {
		t.Fatalf("SubscribeHostNotifications: %v", err)
	}

	// A live thread subscription whose consumer is deliberately never read.
	out, err := remote.source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "host:S"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}
	_ = out

	// Fill the thread pump's out buffer and then its in buffer, so the drain
	// goroutine parks on the thread send.
	for i := range remoteHubSubBuffer*2 + 16 {
		if err := remote.push(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{ThreadID: "S", Ref: "local:S"}); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
	}

	// Kill the client's connection while the drain is parked on the thread send.
	_, _ = remote.source.ListModels(context.Background(), appwire.ModelListParams{})

	select {
	case _, ok := <-notifications:
		if ok {
			t.Fatal("received a notification, want the subscription to close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("host subscription was not closed after the client died while thread delivery was backpressured")
	}
}

// TestRemoteHubSourceHostNotificationFilterShortCircuitsBeforeSnapshot pins the
// publish-time filter's contract and the hot-path ordering it depends on: the
// filter is what keeps the high-frequency thread/streaming families — the
// overwhelming majority of the shared drain goroutine's traffic — from building
// a per-notification snapshot of the host subscriptions.
//
// A rejected notification must not reach a consumer, must not count as a drop,
// and must leave the reject path allocation-free with a live subscription. (The
// snapshot slice is stack-allocated in this function — escape analysis reports
// "does not escape" — so the allocation check guards the property rather than
// pinning a heap regression; see the round-three PR comment.) An accepted
// notification still delivers.
func TestRemoteHubSourceHostNotificationFilterShortCircuitsBeforeSnapshot(t *testing.T) {
	client, _ := newScriptedClient(t, func(string, json.RawMessage) scriptedReply {
		return scriptedReply{}
	})
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})
	source.SetHostNotificationFilter(func(method string) bool {
		return method == appwire.NotifyEvenerAuthUpdated
	})

	subCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	notifications, err := source.SubscribeHostNotifications(subCtx)
	if err != nil {
		t.Fatalf("SubscribeHostNotifications: %v", err)
	}

	// A subscription is live, so a snapshot of it is real work rather than an
	// empty iteration.
	rejected := appwire.Notification{Method: appwire.NotifyThreadStatusChanged}
	if allocs := testing.AllocsPerRun(100, func() {
		source.publishHostNotification(client, rejected)
	}); allocs > 0 {
		t.Fatalf("publishHostNotification allocated %v per call for a filtered-out notification; want 0", allocs)
	}
	if dropped := source.hostNotifyDropped.Load(); dropped != 0 {
		t.Fatalf("hostNotifyDropped = %d after filtered-out publishes, want 0 (a filter rejection is not a drop)", dropped)
	}
	select {
	case notification := <-notifications:
		t.Fatalf("subscription received filtered-out notification %q", notification.Method)
	case <-time.After(100 * time.Millisecond):
	}

	// The accepted family still reaches the consumer.
	source.publishHostNotification(client, appwire.Notification{Method: appwire.NotifyEvenerAuthUpdated})
	select {
	case notification := <-notifications:
		if notification.Method != appwire.NotifyEvenerAuthUpdated {
			t.Fatalf("notification method = %q", notification.Method)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the accepted notification")
	}
}

// hostSubClient returns the client of the single registered host subscription.
func (s *RemoteHubSource) hostSubClient(t *testing.T) *appwire.Client {
	t.Helper()
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for sub := range s.hostSubs {
		return sub.client
	}
	t.Fatal("no host subscription registered")
	return nil
}

// TestRemoteHubSourceSubscribeHostNotificationsDeliversAndUnregisters covers
// the component-07a broker seam: registration starts the drain and delivers
// host-level notifications that carry no thread route, and a cancelled context
// removes the consumer so a departed reader cannot wedge the shared drain.
func TestRemoteHubSourceSubscribeHostNotificationsDeliversAndUnregisters(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	server := appwire.NewStreamTransport(serverConn)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			msg, err := server.Recv(ctx)
			if err != nil {
				return
			}
			if msg.Request == nil || msg.Request.Method != appwire.MethodInitialize {
				continue
			}
			data, _ := json.Marshal(appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"})
			if err := server.Send(ctx, appwire.ResponseMessage(msg.Request.ID, json.RawMessage(data))); err != nil {
				return
			}
		}
	}()

	client := appwire.NewClient(appwire.NewStreamTransport(clientConn))
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		t.Fatalf("initialize scripted remote: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		<-done
	})

	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	subCtx, stopSub := context.WithCancel(context.Background())
	notifications, err := source.SubscribeHostNotifications(subCtx)
	if err != nil {
		t.Fatalf("SubscribeHostNotifications: %v", err)
	}

	if err := server.Send(ctx, appwire.NotificationMessage(appwire.NotifyEvenerAuthUpdated, map[string]string{"provider": "openai"})); err != nil {
		t.Fatalf("send notification: %v", err)
	}
	select {
	case notification := <-notifications:
		if notification.Method != appwire.NotifyEvenerAuthUpdated {
			t.Fatalf("notification method = %q, want %q", notification.Method, appwire.NotifyEvenerAuthUpdated)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the host-level notification")
	}

	stopSub()
	deadline := time.Now().Add(5 * time.Second)
	for {
		source.subMu.Lock()
		remaining := len(source.hostSubs)
		source.subMu.Unlock()
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("hostSubs = %d after cancel, want 0", remaining)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// gatedSendTransport holds a client's single frame-write slot open: the first
// Send parks inside the transport until the test releases it, so a second
// request on the same client is provably still queued on Client's write slot
// when the test ends its context. Later Sends refuse a canceled context the way
// appwire.StreamTransport.Send does — before any byte of the frame is written —
// and every Send is counted, so a test can prove which frames reached a
// transport at all.
type gatedSendTransport struct {
	firstSendEntered chan struct{}
	releaseFirst     chan struct{}

	mu    sync.Mutex
	sends int
}

func (t *gatedSendTransport) Send(ctx context.Context, _ appwire.Message) error {
	t.mu.Lock()
	t.sends++
	first := t.sends == 1
	t.mu.Unlock()
	if first {
		close(t.firstSendEntered)
		<-t.releaseFirst
		return nil
	}
	// The stream transport's own shape: a context that ended before the write
	// stops it, having emitted nothing.
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (t *gatedSendTransport) Recv(ctx context.Context) (appwire.Message, error) {
	<-ctx.Done()
	return appwire.Message{}, ctx.Err()
}

func (t *gatedSendTransport) Close() error { return nil }

func (t *gatedSendTransport) sendCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sends
}

// TestRemoteHubSourceAdminMutationCallPreSendCancellationStaysRetryable pins
// round eight's medium finding on the mutating path. appwire.Client serializes
// frame writes on one write slot, so a forwarded mutation can be queued behind another
// call on the same client when the caller's context ends; that request never
// reaches the transport, so the mutation provably did not happen and the caller
// must NOT be told the outcome is unknown (which is what blocks a retry that is
// in fact safe).
//
// The transport proves the claim: it counts every Send, and only the queued
// holder's frame is ever offered to it. The post-send half of the contract — a
// cancellation after the frame went out, which is genuinely ambiguous — stays
// pinned by TestRemoteHubSourceAdminMutationCallPostSendCancellationIsOutcomeUnknown,
// which asserts the blocked mapping for exactly that window.
func TestRemoteHubSourceAdminMutationCallPreSendCancellationStaysRetryable(t *testing.T) {
	transport := &gatedSendTransport{
		firstSendEntered: make(chan struct{}),
		releaseFirst:     make(chan struct{}),
	}
	client := appwire.NewClient(transport)
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	// Park a Notify inside the transport so the client's write slot stays taken
	// for the whole window below.
	holderCtx := t.Context()
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- client.Notify(holderCtx, appwire.NotifyEvenerAuthUpdated, map[string]string{"provider": "openai"})
	}()
	select {
	case <-transport.firstSendEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the queued send holder never reached the transport")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		var out json.RawMessage
		done <- source.AdminMutationCall(ctx, appwire.MethodEvenerPluginInstall, json.RawMessage(`{"name":"p"}`), &out)
	}()
	// Queue the mutation on the write slot, then end its context there. The
	// outcome does not depend on this sleep — the mutation is behind the holder
	// either way — but the sleep is what makes the window under test the queued
	// one rather than "canceled before the call started".
	time.Sleep(50 * time.Millisecond)
	cancel()
	close(transport.releaseFirst)

	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AdminMutationCall did not return after its context was canceled while queued")
	}
	if err == nil {
		t.Fatal("AdminMutationCall succeeded after its context was canceled while queued")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %T %v, want the caller's own cancellation (a safe retry): the mutation never reached the transport", err, err)
	}
	if wire, ok := errors.AsType[appwire.WireError](err); ok {
		t.Fatalf("error = %+v, want no wire error: a request that was never transmitted has no unknown outcome", wire)
	}
	if got := transport.sendCount(); got != 1 {
		t.Fatalf("transport saw %d sends, want 1 (the queued mutation must never be offered to the transport)", got)
	}
}

// TestRemoteHubSourceClosedFileWriteIsTransportLoss pins the second round-eight
// medium finding. io.ErrClosedPipe was recognized, but the closed-write errors
// the SSH stdio path actually produces were not: os.ErrClosed is what a write
// to an already-closed stdio pipe reports, net.ErrClosed is its network-conn
// twin, and both can arrive wrapped. Unrecognized, they escaped the mutation
// path as raw errors — read as "nothing happened, retry" for a forwarded
// instance/create or plugin/install that may have been applied.
//
// The last case is the real shape rather than a stand-in: an
// appwire.StreamTransport whose pipe was closed the way the SSH manager closes
// a host's channel, so the classifier is fed the error the SSH stdio path
// itself produces.
func TestRemoteHubSourceClosedFileWriteIsTransportLoss(t *testing.T) {
	for _, tc := range []struct {
		name      string
		transport func(t *testing.T) appwire.Transport
	}{
		{
			name:      "os.ErrClosed",
			transport: func(*testing.T) appwire.Transport { return closedPipeTransport{err: os.ErrClosed} },
		},
		{
			name: "wrapped os.ErrClosed",
			transport: func(*testing.T) appwire.Transport {
				return closedPipeTransport{err: fmt.Errorf("appwire send: %w", os.ErrClosed)}
			},
		},
		{
			name:      "net.ErrClosed",
			transport: func(*testing.T) appwire.Transport { return closedPipeTransport{err: net.ErrClosed} },
		},
		{
			name: "wrapped net.ErrClosed",
			transport: func(*testing.T) appwire.Transport {
				return closedPipeTransport{err: fmt.Errorf("appwire send: %w", net.ErrClosed)}
			},
		},
		{
			name:      "ssh stdio write end closed",
			transport: closedStdioTransport,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := tc.transport(t)
			source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
				return appwire.NewClient(transport), nil
			})

			// A mutation is what a blind retry can double-apply, so the closed
			// write must be reported as an unknown outcome with retries blocked.
			var out json.RawMessage
			mutationErr := source.AdminMutationCall(context.Background(), appwire.MethodEvenerPluginInstall, nil, &out)
			assertMutationOutcomeBlocked(t, mutationErr, "forwarded mutation")

			// The read path stays SessionUnavailable: an idempotent read is safe
			// to retry, and the blocked disposition must stay scoped to mutations.
			readErr := source.AdminCall(context.Background(), appwire.MethodEvenerInstanceList, nil, &out)
			assertSessionUnavailable(t, readErr, "forwarded read")
		})
	}
}

// closedStdioTransport is the transport the SSH stdio path really has: an
// appwire.StreamTransport writing frames into a pipe whose write end was
// closed, which is what the SSH manager does when it tears a host's channel
// down. The next frame write reports os.ErrClosed ("file already closed") —
// not io.ErrClosedPipe — through the same path a live host's write takes.
func closedStdioTransport(t *testing.T) appwire.Transport {
	t.Helper()
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = readEnd.Close() })
	transport := appwire.NewStreamTransport(writeEnd)
	if err := transport.Close(); err != nil {
		t.Fatalf("close ssh stdio transport: %v", err)
	}
	return transport
}

// assertMutationOutcomeBlocked asserts err is the explicit
// ErrorMutationOutcomeUnknown / RetryDispositionBlocked error every lost
// forwarded admin mutation is reported as, with no retention of a retry hint.
func assertMutationOutcomeBlocked(t *testing.T, err error, label string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected error, got nil", label)
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("%s: error %T=%v, want appwire.WireError", label, err, err)
	}
	if wire.Code != appwire.CodeInternalError {
		t.Fatalf("%s: code = %d, want %d", label, wire.Code, appwire.CodeInternalError)
	}
	if info := wireErrorInfo(wire); info != string(appwire.ErrorMutationOutcomeUnknown) {
		t.Fatalf("%s: evenerErrorInfo = %q, want %q", label, info, appwire.ErrorMutationOutcomeUnknown)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok {
		t.Fatalf("%s: Data = %T %v, want appwire.ErrorData", label, wire.Data, wire.Data)
	}
	if data.MutationOutcome != appwire.MutationOutcomeUnknown {
		t.Fatalf("%s: mutationOutcome = %q, want %q", label, data.MutationOutcome, appwire.MutationOutcomeUnknown)
	}
	if data.RetryDisposition != appwire.RetryDispositionBlocked {
		t.Fatalf("%s: retryDisposition = %q, want %q", label, data.RetryDisposition, appwire.RetryDispositionBlocked)
	}
	if !strings.Contains(wire.Message, "host") {
		t.Fatalf("%s: message = %q, want it to name the host", label, wire.Message)
	}
}
