package appsource

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

type scriptedAppwireTransport struct {
	recv      chan appwire.Message
	closed    chan struct{}
	closeOnce sync.Once
	recvDone  chan struct{}
	recvOnce  sync.Once
	send      func(context.Context, appwire.Message) error
}

func newScriptedAppwireTransport(send func(context.Context, appwire.Message) error) *scriptedAppwireTransport {
	return &scriptedAppwireTransport{
		recv: make(chan appwire.Message, 16), closed: make(chan struct{}), recvDone: make(chan struct{}), send: send,
	}
}

func (t *scriptedAppwireTransport) Send(ctx context.Context, msg appwire.Message) error {
	if t.send != nil {
		return t.send(ctx, msg)
	}
	return nil
}

func (t *scriptedAppwireTransport) Recv(ctx context.Context) (appwire.Message, error) {
	select {
	case msg := <-t.recv:
		return msg, nil
	case <-t.closed:
		t.recvOnce.Do(func() { close(t.recvDone) })
		return appwire.Message{}, io.EOF
	case <-ctx.Done():
		t.recvOnce.Do(func() { close(t.recvDone) })
		return appwire.Message{}, ctx.Err()
	}
}

func (t *scriptedAppwireTransport) Close() error {
	t.closeOnce.Do(func() { close(t.closed) })
	return nil
}

func respondingTransport(result func(string) (any, error)) *scriptedAppwireTransport {
	var transport *scriptedAppwireTransport
	transport = newScriptedAppwireTransport(func(_ context.Context, msg appwire.Message) error {
		if msg.Request == nil {
			return nil
		}
		value, err := result(msg.Request.Method)
		if err != nil {
			transport.recv <- appwire.ErrorMessage(msg.Request.ID, appwire.InternalError(err.Error()))
		} else {
			transport.recv <- appwire.ResponseMessage(msg.Request.ID, value)
		}
		return nil
	})
	return transport
}

func dialTransport(transport appwire.Transport) appwireDialFunc {
	return func(context.Context, string, *http.Client, http.Header) (appwire.Transport, error) {
		return transport, nil
	}
}

func fuzzScenarioLocalDaemonDialSeamPreservesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dial := func(context.Context, string, *http.Client, http.Header) (appwire.Transport, error) {
		return nil, errors.New("dial failed")
	}

	local := NewLocalDaemonSource("local", nil, nil)
	local.dial = dial
	if err := local.withClient(ctx, rendezvousEntry("ws://daemon"), func(*appwire.Client) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("local withClient error = %v", err)
	}
	entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon", SourceID: "local", ThreadID: "thread"}
	local = NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, nil)
	local.dial = dial
	if _, err := local.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "local:thread"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("local SubscribeThread error = %v", err)
	}
}

func rendezvousEntry(endpoint string) rendezvous.Entry {
	return rendezvous.Entry{Endpoint: endpoint}
}

func assertSessionUnavailable(t *testing.T, err error, label string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected error, got nil", label)
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("%s: error %T=%v, want appwire.WireError", label, err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok || wire.Code != appwire.CodeUnavailable || data.EvenerErrorInfo != appwire.ErrorSessionUnavailable {
		t.Fatalf("%s: wire=%+v, want SessionUnavailable", label, wire)
	}
}

func fuzzScenarioLocalDaemonRemainingTransportBranches(t *testing.T) {
	ctx := context.Background()
	entry := rendezvousEntry("ws://daemon")
	entry.ThreadID = "thread"
	entry.SourceID = "local"
	entry.Protocol = appwire.ProtocolVersion
	var wire appwire.WireError
	if err := localDaemonDialError(context.DeadlineExceeded); !errors.As(err, &wire) {
		t.Fatalf("deadline error = %v", err)
	}

	for _, cancelAt := range []string{"initialize", "read"} {
		t.Run("subscribe canceled during "+cancelAt, func(t *testing.T) {
			callCtx, cancel := context.WithCancel(ctx)
			transport := respondingTransport(func(method string) (any, error) {
				if method == appwire.MethodInitialize && cancelAt != "initialize" {
					return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion}, nil
				}
				cancel()
				return nil, errors.New("failed")
			})
			s := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, nil)
			s.dial = dialTransport(transport)
			if _, err := s.SubscribeThread(callCtx, appwire.ThreadReadParams{Ref: "local:thread"}); !errors.Is(err, context.Canceled) {
				t.Fatalf("SubscribeThread error = %v", err)
			}
		})
	}

	t.Run("withClient initialize cancellation", func(t *testing.T) {
		callCtx, cancel := context.WithCancel(ctx)
		transport := respondingTransport(func(string) (any, error) { cancel(); return nil, errors.New("failed") })
		s := NewLocalDaemonSource("local", nil, nil)
		s.dial = dialTransport(transport)
		if err := s.withClient(callCtx, entry, func(*appwire.Client) error { return nil }); !errors.Is(err, context.Canceled) {
			t.Fatalf("withClient error = %v", err)
		}
	})

	t.Run("dial failures", func(t *testing.T) {
		dial := func(context.Context, string, *http.Client, http.Header) (appwire.Transport, error) {
			return nil, errors.New("connection refused")
		}
		s := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, nil)
		s.dial = dial
		if _, err := s.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "local:thread"}); err == nil {
			t.Fatal("SubscribeThread returned nil")
		}
		if err := s.withClient(ctx, entry, func(*appwire.Client) error { return nil }); err == nil {
			t.Fatal("withClient returned nil")
		}
	})

	t.Run("subscription closes and cancels", func(t *testing.T) {
		for _, publish := range []bool{false, true} {
			callCtx, cancel := context.WithCancel(ctx)
			transport := respondingTransport(func(method string) (any, error) {
				if method == appwire.MethodInitialize {
					return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion}, nil
				}
				return map[string]any{}, nil
			})
			s := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, nil)
			s.dial = dialTransport(transport)
			out, err := s.SubscribeThread(callCtx, appwire.ThreadReadParams{Ref: "local:thread"})
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			if publish {
				transport.recv <- appwire.NotificationMessage("event", nil)
				<-out
			}
			cancel()
			<-transport.recvDone
			if _, ok := <-out; ok {
				t.Fatal("subscription remained open")
			}
		}
	})

	t.Run("notification source closes", func(t *testing.T) {
		callCtx, cancel := context.WithCancel(ctx)
		transport := respondingTransport(func(method string) (any, error) {
			if method == appwire.MethodInitialize {
				return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion}, nil
			}
			return map[string]any{}, nil
		})
		s := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, nil)
		s.dial = dialTransport(transport)
		out, err := s.SubscribeThread(callCtx, appwire.ThreadReadParams{Ref: "local:thread"})
		if err != nil {
			t.Fatal(err)
		}
		_ = transport.Close()
		<-transport.recvDone
		cancel()
		if _, ok := <-out; ok {
			t.Fatal("subscription remained open")
		}
	})

}

func fuzzScenarioForwardLocalDaemonNotificationCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	forwardLocalDaemonNotification(ctx, make(chan appwire.Notification), appwire.Notification{})
}

func fuzzScenarioLocalDaemonInternalHandshakeErrorFallbacks(t *testing.T) {
	internal := appwire.InternalError("semantic failure")
	if got := localDaemonInitializeError(internal); got == nil {
		t.Fatal("initialize error mapped nil")
	}
	if got := localDaemonSubscribeReadError(internal); got == nil {
		t.Fatal("subscribe error mapped nil")
	}
}
