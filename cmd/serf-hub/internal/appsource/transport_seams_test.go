package appsource

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/rendezvous"
)

type scriptedAppwireTransport struct {
	recv      chan appwire.Message
	closed    chan struct{}
	closeOnce sync.Once
	recvDone  chan struct{}
	recvOnce  sync.Once
	send      func(context.Context, appwire.Message) error
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

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

func fuzzScenarioSourceDialSeamsPreserveCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dial := func(context.Context, string, *http.Client, http.Header) (appwire.Transport, error) {
		return nil, errors.New("dial failed")
	}

	codex := NewCodexSource(CodexSourceConfig{Endpoint: "ws://daemon"}, nil)
	codex.dial = dial
	if _, _, err := codex.connect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("codex connect error = %v", err)
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

func fuzzScenarioCodexConnectHandshakeFailures(t *testing.T) {
	t.Run("initialize error", func(t *testing.T) {
		transport := respondingTransport(func(method string) (any, error) {
			return nil, errors.New(method + " failed")
		})
		s := NewCodexSource(CodexSourceConfig{Endpoint: "ws://daemon"}, nil)
		s.dial = dialTransport(transport)
		if _, _, err := s.connect(context.Background()); err == nil {
			t.Fatal("connect returned nil")
		}
		select {
		case <-transport.closed:
		default:
			t.Fatal("transport was not closed")
		}
	})

	t.Run("initialize canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		transport := respondingTransport(func(string) (any, error) {
			cancel()
			return nil, errors.New("initialize failed")
		})
		s := NewCodexSource(CodexSourceConfig{Endpoint: "ws://daemon"}, nil)
		s.dial = dialTransport(transport)
		if _, _, err := s.connect(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("connect error = %v", err)
		}
	})

	for _, tc := range []struct {
		name   string
		cancel bool
	}{
		{name: "initialized notification error"},
		{name: "initialized notification canceled", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			transport := respondingTransport(func(string) (any, error) { return map[string]any{}, nil })
			transport.send = func(_ context.Context, msg appwire.Message) error {
				if msg.Request != nil {
					transport.recv <- appwire.ResponseMessage(msg.Request.ID, map[string]any{})
					return nil
				}
				if tc.cancel {
					cancel()
				}
				return errors.New("notify failed")
			}
			s := NewCodexSource(CodexSourceConfig{Endpoint: "ws://daemon"}, nil)
			s.dial = dialTransport(transport)
			_, _, err := s.connect(ctx)
			if err == nil || (tc.cancel && !errors.Is(err, context.Canceled)) {
				t.Fatalf("connect error = %v", err)
			}
		})
	}
}

func fuzzScenarioCodexRPCFailureAndValidationBranches(t *testing.T) {
	dialErr := errors.New("connection refused")
	dial := func(context.Context, string, *http.Client, http.Header) (appwire.Transport, error) {
		return nil, dialErr
	}
	s := NewCodexSource(CodexSourceConfig{Endpoint: "ws://daemon"}, nil)
	s.dial = dial
	ctx := context.Background()
	ref := "codex:thread"
	calls := []func() error{
		func() error { _, err := s.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref}); return err },
		func() error { _, err := s.ListTurns(ctx, appwire.ThreadTurnsListParams{Ref: ref}); return err },
		func() error { _, err := s.StartThread(ctx, appwire.ThreadStartParams{}); return err },
		func() error { _, err := s.ResumeThread(ctx, appwire.ThreadResumeParams{Ref: ref}); return err },
		func() error { _, err := s.ForkThread(ctx, appwire.ThreadForkParams{Ref: ref}); return err },
		func() error {
			_, err := s.StartTurn(ctx, appwire.TurnStartParams{ClientMutationID: "test-mutation", Ref: ref})
			return err
		},
		func() error { _, err := s.ListModels(ctx, appwire.ModelListParams{}); return err },
		func() error { _, err := s.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: ref}); return err },
	}
	for i, call := range calls {
		if err := call(); err == nil {
			t.Fatalf("call %d returned nil", i)
		}
	}

	badInput := []appwire.InputItem{{Type: "unsupported"}}
	if _, err := s.StartTurn(ctx, appwire.TurnStartParams{ClientMutationID: "test-mutation", Ref: ref, Input: badInput}); err == nil {
		t.Fatal("StartTurn accepted invalid input")
	}
	if _, err := s.startTurnWithClient(ctx, nil, appwire.TurnStartParams{ClientMutationID: "test-mutation", Ref: ref, Input: badInput}); err == nil {
		t.Fatal("startTurnWithClient accepted invalid input")
	}
	if _, err := s.SteerTurn(ctx, appwire.TurnSteerParams{ClientMutationID: "test-mutation", Ref: ref, ExpectedTurnID: "turn", Input: badInput}); err == nil {
		t.Fatal("SteerTurn accepted invalid input")
	}
}

func fuzzScenarioCodexRPCResponseErrors(t *testing.T) {
	newSource := func() *CodexSource {
		transport := respondingTransport(func(method string) (any, error) {
			if method == appwire.MethodInitialize {
				return map[string]any{}, nil
			}
			return nil, errors.New("rpc failed")
		})
		s := NewCodexSource(CodexSourceConfig{Endpoint: "ws://daemon"}, nil)
		s.dial = dialTransport(transport)
		return s
	}
	ctx := context.Background()
	ref := "codex:thread"
	turnTransport := respondingTransport(func(string) (any, error) { return nil, errors.New("rpc failed") })
	turnCtx, cancelTurn := context.WithCancel(ctx)
	turnClient := appwire.NewClient(turnTransport)
	turnClient.Start(turnCtx)
	calls := []func() error{
		func() error { _, err := newSource().StartThread(ctx, appwire.ThreadStartParams{}); return err },
		func() error {
			_, err := newSource().startTurnWithClient(ctx, turnClient, appwire.TurnStartParams{ClientMutationID: "test-mutation", Ref: ref})
			return err
		},
		func() error {
			_, err := newSource().ResumeThread(ctx, appwire.ThreadResumeParams{Ref: ref})
			return err
		},
		func() error { _, err := newSource().ForkThread(ctx, appwire.ThreadForkParams{Ref: ref}); return err },
		func() error { _, err := newSource().ListModels(ctx, appwire.ModelListParams{}); return err },
		func() error {
			_, err := newSource().SubscribeThread(ctx, appwire.ThreadReadParams{Ref: ref})
			return err
		},
	}
	for i, call := range calls {
		if err := call(); err == nil {
			cancelTurn()
			_ = turnClient.Close()
			<-turnTransport.recvDone
			t.Fatalf("call %d returned nil", i)
		}
	}
	cancelTurn()
	_ = turnClient.Close()
	<-turnTransport.recvDone
}

func fuzzScenarioCodexInitialAndResumedTurnFailures(t *testing.T) {
	result := func(method string) (any, error) {
		switch method {
		case appwire.MethodInitialize:
			return map[string]any{}, nil
		case appwire.MethodThreadStart:
			return map[string]any{"thread": map[string]any{"id": "thread"}}, nil
		case appwire.MethodThreadResume:
			return map[string]any{"thread": map[string]any{"id": "thread"}}, nil
		default:
			return nil, errors.New("turn failed")
		}
	}
	newSource := func() *CodexSource {
		s := NewCodexSource(CodexSourceConfig{Endpoint: "ws://daemon"}, nil)
		s.dial = dialTransport(respondingTransport(result))
		return s
	}
	input := []appwire.InputItem{{Type: "text", Text: "hello"}}
	if _, err := newSource().StartThread(context.Background(), appwire.ThreadStartParams{Input: input}); err == nil {
		t.Fatal("StartThread returned nil")
	}
	if _, err := newSource().StartTurn(context.Background(), appwire.TurnStartParams{ClientMutationID: "test-mutation", Ref: "codex:thread", Input: input}); err == nil {
		t.Fatal("StartTurn returned nil")
	}

	live := &codexLiveThread{done: make(chan struct{}), subscribers: map[chan appwire.Notification]struct{}{}, closed: true}
	s := newTestCodexSource()
	s.live["thread"] = live
	if got := s.liveThread("thread"); got != nil {
		t.Fatalf("closed live thread = %p", got)
	}

	for _, mapper := range []func(error) error{codexSourceDialError, localDaemonDialError} {
		var wire appwire.WireError
		if err := mapper(context.DeadlineExceeded); !errors.As(err, &wire) {
			t.Fatalf("deadline error = %v", err)
		}
	}
	if err := codexSourceDialError(fakeTimeoutError{}); err == nil {
		t.Fatal("timeout error mapped nil")
	}
}

func fuzzScenarioLocalDaemonRemainingTransportBranches(t *testing.T) {
	ctx := context.Background()
	entry := rendezvousEntry("ws://daemon")
	entry.ThreadID = "thread"
	entry.SourceID = "local"
	entry.Protocol = appwire.ProtocolVersion

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
		transport := respondingTransport(func(method string) (any, error) {
			if method == appwire.MethodInitialize {
				return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion}, nil
			}
			return map[string]any{}, nil
		})
		s := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return []LocalDaemonEntry{{Entry: entry}} }, nil)
		s.dial = dialTransport(transport)
		out, err := s.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "local:thread"})
		if err != nil {
			t.Fatal(err)
		}
		_ = transport.Close()
		<-transport.recvDone
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

func fuzzScenarioLocalDaemonRESTDefaultClientAndRequestError(t *testing.T) {
	s := NewLocalDaemonSource("local", nil, nil)
	if err := s.restInterrupt(context.Background(), rendezvous.Entry{Address: "%"}); err == nil {
		t.Fatal("invalid URL returned nil")
	}
	s.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}
	if err := s.restInterrupt(context.Background(), rendezvous.Entry{Address: "daemon"}); err == nil {
		t.Fatal("transport error returned nil")
	}
	s.client = nil
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	if err := s.restInterrupt(context.Background(), rendezvous.Entry{Address: strings.TrimPrefix(server.URL, "http://")}); err != nil {
		t.Fatal(err)
	}
	if got := (&LocalDaemonSource{}).liveEntries(); got != nil {
		t.Fatalf("nil entries = %#v", got)
	}
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
