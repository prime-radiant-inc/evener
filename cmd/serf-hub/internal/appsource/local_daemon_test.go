package appsource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/rendezvous"
)

// fakeTimeoutError implements net.Error with Timeout()==true so we can drive
// localDaemonDialError without needing real socket timeouts.
type fakeTimeoutError struct{ msg string }

func (e fakeTimeoutError) Error() string   { return e.msg }
func (e fakeTimeoutError) Timeout() bool   { return true }
func (e fakeTimeoutError) Temporary() bool { return true }

func TestLocalDaemonDialErrorMapsTransportFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"ECONNREFUSED", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}},
		{"ECONNRESET", &net.OpError{Op: "read", Err: syscall.ECONNRESET}},
		{"EPIPE", &net.OpError{Op: "write", Err: syscall.EPIPE}},
		{"io.EOF", io.EOF},
		{"io.ErrUnexpectedEOF wrapped", fmt.Errorf("recv: %w", io.ErrUnexpectedEOF)},
		{"net.Error timeout", fakeTimeoutError{msg: "i/o timeout"}},
		{"context.DeadlineExceeded (transport-level)", fmt.Errorf("dial failed: %w", context.DeadlineExceeded)},
		{"websocket close error", websocket.CloseError{Code: websocket.StatusAbnormalClosure, Reason: "dropped"}},
		{"connection reset string match", errors.New("read tcp 127.0.0.1:1->127.0.0.1:2: connection reset by peer")},
		{"broken pipe string match", errors.New("write tcp: broken pipe")},
		{"use of closed network connection", errors.New("use of closed network connection")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := localDaemonDialError(tc.err)
			assertSessionUnavailable(t, got, tc.name)
		})
	}
}

func TestLocalDaemonDialErrorPassesThroughApplicationErrors(t *testing.T) {
	// JSON-RPC application-level error: should not be touched, since the
	// daemon is alive and signalling semantic failure.
	app := appwire.InvalidParams("missing ref")
	got := localDaemonDialError(app)
	var wire appwire.WireError
	if !errors.As(got, &wire) {
		t.Fatalf("got %T=%v, want WireError", got, got)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("wire=%+v, want InvalidParams code preserved", wire)
	}

	// A generic non-transport error should also pass through.
	plain := errors.New("some daemon-side problem")
	if got := localDaemonDialError(plain); !errors.Is(got, plain) {
		t.Fatalf("plain error rewritten: %v", got)
	}
}

func TestLocalDaemonSubscribeReadErrorPreservesApplicationWireErrors(t *testing.T) {
	app := appwire.InvalidParams("broken pipe is part of semantic error")
	got := localDaemonSubscribeReadError(app)
	var wire appwire.WireError
	if !errors.As(got, &wire) {
		t.Fatalf("got %T=%v, want WireError", got, got)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("wire=%+v, want InvalidParams preserved", wire)
	}
}

func TestLocalDaemonSubscribeReadErrorMapsInternalTransportWireErrors(t *testing.T) {
	got := localDaemonSubscribeReadError(appwire.InternalError("read failed: i/o timeout"))
	assertSessionUnavailable(t, got, "internal i/o timeout")
}

func TestLocalDaemonCallErrorMapsRawTransportFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"ECONNRESET", &net.OpError{Op: "write", Err: syscall.ECONNRESET}},
		{"broken pipe string", errors.New("write tcp: broken pipe")},
		{"closed connection string", errors.New("use of closed network connection")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := localDaemonCallError(tc.err)
			assertSessionUnavailable(t, got, tc.name)
		})
	}
}

func TestLocalDaemonInitializeErrorPreservesApplicationWireErrors(t *testing.T) {
	app := appwire.InvalidParams("broken pipe is part of semantic error")
	got := localDaemonInitializeError(app)
	var wire appwire.WireError
	if !errors.As(got, &wire) {
		t.Fatalf("got %T=%v, want WireError", got, got)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("wire=%+v, want InvalidParams preserved", wire)
	}
}

func TestLocalDaemonInitializeErrorMapsInternalTransportWireErrors(t *testing.T) {
	got := localDaemonInitializeError(appwire.InternalError("initialize failed: i/o timeout"))
	assertSessionUnavailable(t, got, "internal i/o timeout")
}

func TestLocalDaemonCallErrorPreservesCallerCancellation(t *testing.T) {
	if got := localDaemonCallError(context.Canceled); !errors.Is(got, context.Canceled) {
		t.Fatalf("context.Canceled remapped: %v", got)
	}
	if got := localDaemonCallError(context.DeadlineExceeded); !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("context.DeadlineExceeded remapped: %v", got)
	}
}

func TestLocalDaemonDialErrorIgnoresNil(t *testing.T) {
	if got := localDaemonDialError(nil); got != nil {
		t.Fatalf("nil mapped to %v, want nil", got)
	}
}

func TestLocalDaemonSourceReadThreadMapsIOTimeoutToSessionUnavailable(t *testing.T) {
	// A listener that accepts TCP but never speaks the HTTP upgrade. The
	// dial will fail with i/o timeout once the caller's short deadline fires,
	// surfacing as a net.Error.Timeout()==true wrapped in OpError.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			// Hold the connection open without responding so the websocket
			// handshake stalls. The caller's ctx deadline ends the dial.
			go func(c net.Conn) {
				defer c.Close()
				time.Sleep(2 * time.Second)
			}(conn)
		}
	}()

	source := NewLocalDaemonSource("local", func() []rendezvous.Entry {
		return []rendezvous.Entry{{
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws://" + listener.Addr().String(),
			SourceID:  "local",
			ThreadID:  "th_1",
			SessionID: "sess_1",
		}}
	}, nil)

	// Use a context whose deadline expires while the daemon hangs. Because
	// ctx.Err() will be DeadlineExceeded at the moment dial returns, the
	// call site returns ctx.Err() unchanged — this exercises the
	// ctx-propagation branch.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err = source.ReadThread(ctx, appwire.ThreadReadParams{Ref: "local:th_1"})
	if err == nil {
		t.Fatalf("expected error from hung daemon, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %T=%v, want context.DeadlineExceeded propagated from caller ctx", err, err)
	}
}

func TestLocalDaemonSourceReadThreadMapsEOFDuringHandshake(t *testing.T) {
	// Server accepts the websocket upgrade, then immediately drops the
	// connection without responding to Initialize. The Initialize call
	// surfaces an EOF/abnormal-close, which should map to SessionUnavailable.
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		_ = conn.Close(websocket.StatusAbnormalClosure, "daemon died")
	}))
	defer httpServer.Close()

	source := NewLocalDaemonSource("local", func() []rendezvous.Entry {
		return []rendezvous.Entry{{
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws" + httpServer.URL[len("http"):],
			SourceID:  "local",
			ThreadID:  "th_1",
			SessionID: "sess_1",
		}}
	}, httpServer.Client())

	_, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "local:th_1"})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	assertSessionUnavailable(t, err, "ReadThread (EOF mid-handshake)")
}

func TestLocalDaemonSourceReadThreadReturnsCallerCtxCancellation(t *testing.T) {
	// Hang the upgrade indefinitely so the dial blocks until the caller
	// cancels its context.
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer httpServer.Close()

	source := NewLocalDaemonSource("local", func() []rendezvous.Entry {
		return []rendezvous.Entry{{
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws" + httpServer.URL[len("http"):],
			SourceID:  "local",
			ThreadID:  "th_1",
			SessionID: "sess_1",
		}}
	}, httpServer.Client())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := source.ReadThread(ctx, appwire.ThreadReadParams{Ref: "local:th_1"})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %T=%v, want context.Canceled (caller cancellation must not be remapped)", err, err)
	}
	// Belt-and-suspenders: must NOT be SessionUnavailable.
	var wire appwire.WireError
	if errors.As(err, &wire) && wire.Code == appwire.CodeUnavailable {
		t.Fatalf("ctx cancellation was remapped to SessionUnavailable: %+v", wire)
	}
}

func TestLocalDaemonSourceListsOnlyAppWireRendezvousThreads(t *testing.T) {
	source := NewLocalDaemonSource("local", func() []rendezvous.Entry {
		return []rendezvous.Entry{
			{
				Protocol:   appwire.ProtocolVersion,
				Endpoint:   "ws://127.0.0.1:1/rpc",
				SourceID:   "local",
				ThreadID:   "th_1",
				SessionID:  "sess_1",
				WorkingDir: "/tmp/project",
			},
			{
				PID:     2,
				Address: "127.0.0.1:2",
			},
		}
	}, nil)

	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "th_1" || resp.Data[0].Serf.Ref != "local:th_1" {
		t.Fatalf("threads=%+v", resp.Data)
	}
}

func TestLocalDaemonSourceThreadTimestampsUseStartedAtAndZeroForMissing(t *testing.T) {
	startedAt := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	source := NewLocalDaemonSource("local", func() []rendezvous.Entry {
		return []rendezvous.Entry{
			{
				Protocol:  appwire.ProtocolVersion,
				Endpoint:  "ws://127.0.0.1:1/rpc",
				SourceID:  "local",
				ThreadID:  "01STARTED",
				SessionID: "01STARTED",
				StartedAt: startedAt,
			},
			{
				Protocol:  appwire.ProtocolVersion,
				Endpoint:  "ws://127.0.0.1:2/rpc",
				SourceID:  "local",
				ThreadID:  "02MISSING",
				SessionID: "02MISSING",
			},
		}
	}, nil)

	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}

	byID := map[string]appwire.Thread{}
	for _, thread := range resp.Data {
		byID[thread.ID] = thread
	}
	if byID["01STARTED"].CreatedAt != startedAt.Unix() || byID["01STARTED"].UpdatedAt != startedAt.Unix() {
		t.Fatalf("started timestamps=%+v", byID["01STARTED"])
	}
	if byID["02MISSING"].CreatedAt != 0 || byID["02MISSING"].UpdatedAt != 0 {
		t.Fatalf("missing timestamps=%+v", byID["02MISSING"])
	}
}

func TestLocalDaemonSourceReadsThreadOverAppWire(t *testing.T) {
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "th_1", SessionID: "sess_1", Serf: appwire.SerfThread{Ref: "local:th_1"}}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(app.ServeWebSocket))
	defer httpServer.Close()

	source := NewLocalDaemonSource("local", func() []rendezvous.Entry {
		return []rendezvous.Entry{{
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws" + httpServer.URL[len("http"):],
			SourceID:  "local",
			ThreadID:  "th_1",
			SessionID: "sess_1",
		}}
	}, httpServer.Client())

	resp, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "local:th_1"})
	if err != nil {
		t.Fatalf("ReadThread: %v", err)
	}
	if resp.Thread.ID != "th_1" || resp.Thread.Serf.Ref != "local:th_1" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestLocalDaemonSourceDrainUsesInputShapeDirectly(t *testing.T) {
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	var drained appwire.TurnDrainAsSteerParams
	appserver.HandleTyped(app.Router(), appwire.MethodTurnDrainAsSteer, func(_ context.Context, params appwire.TurnDrainAsSteerParams) (appwire.EmptyResponse, error) {
		drained = params
		return appwire.EmptyResponse{}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(app.ServeWebSocket))
	defer httpServer.Close()

	source := NewLocalDaemonSource("local", func() []rendezvous.Entry {
		return []rendezvous.Entry{{
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws" + httpServer.URL[len("http"):],
			SourceID:  "local",
			ThreadID:  "th_1",
			SessionID: "sess_1",
		}}
	}, httpServer.Client())

	err := source.DrainAsSteer(context.Background(), appwire.TurnDrainAsSteerParams{Ref: "local:th_1", Input: []appwire.InputItem{{Type: "text", Text: "composer payload"}}})
	if err != nil {
		t.Fatalf("DrainAsSteer: %v", err)
	}
	if drained.Ref != "local:th_1" || len(drained.Input) != 1 || drained.Input[0].Text != "composer payload" {
		t.Fatalf("drained=%+v", drained)
	}
}

// TestLocalDaemonSourceReadThreadIncludesQueue (kata r80p) covers the
// authoritative queue-state passthrough: ReadThread must surface the
// daemon's Queue (depth + first-line-truncated preview) verbatim so the
// hub/UIs render from wire data instead of mirroring locally.
func TestLocalDaemonSourceReadThreadIncludesQueue(t *testing.T) {
	wantQueue := appwire.QueueState{
		Depth:   2,
		Preview: []string{"first queued message", "second queued message"},
	}
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        "th_1",
			SessionID: "sess_1",
			Serf: appwire.SerfThread{
				Ref:   "local:th_1",
				Queue: wantQueue,
			},
		}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(app.ServeWebSocket))
	defer httpServer.Close()

	source := NewLocalDaemonSource("local", func() []rendezvous.Entry {
		return []rendezvous.Entry{{
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws" + httpServer.URL[len("http"):],
			SourceID:  "local",
			ThreadID:  "th_1",
			SessionID: "sess_1",
		}}
	}, httpServer.Client())

	resp, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "local:th_1"})
	if err != nil {
		t.Fatalf("ReadThread: %v", err)
	}
	got := resp.Thread.Serf.Queue
	if got.Depth != wantQueue.Depth {
		t.Fatalf("queue depth=%d, want %d", got.Depth, wantQueue.Depth)
	}
	if len(got.Preview) != len(wantQueue.Preview) {
		t.Fatalf("queue preview len=%d, want %d (%+v)", len(got.Preview), len(wantQueue.Preview), got.Preview)
	}
	for i, want := range wantQueue.Preview {
		if got.Preview[i] != want {
			t.Fatalf("queue preview[%d]=%q, want %q", i, got.Preview[i], want)
		}
	}
}

func TestLocalDaemonSourceListQueuesOnlyProcessingThreads(t *testing.T) {
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/idle", ThreadID: "th_idle", SessionID: "sess_idle"}, Status: "idle"},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/processing", ThreadID: "th_processing", SessionID: "sess_processing"}, Status: appwire.ThreadStatusActive},
		}
	}, nil)

	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("threads len=%d, want 2: %+v", len(resp.Data), resp.Data)
	}
	capsByID := map[string]appwire.ThreadCapabilities{}
	for _, thread := range resp.Data {
		capsByID[thread.ID] = thread.Serf.Capabilities
	}
	if capsByID["th_idle"].Queue {
		t.Fatalf("idle thread advertised queue capability: %+v", capsByID["th_idle"])
	}
	if !capsByID["th_processing"].Queue {
		t.Fatalf("processing thread did not advertise queue capability: %+v", capsByID["th_processing"])
	}
}

func TestLocalDaemonSourceSubscribeThreadRequestsSubscription(t *testing.T) {
	gotSubscribe := make(chan bool, 1)
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		gotSubscribe <- params.Subscribe
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "th_1", SessionID: "sess_1", Serf: appwire.SerfThread{Ref: "local:th_1"}}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(app.ServeWebSocket))
	defer httpServer.Close()

	source := NewLocalDaemonSource("local", func() []rendezvous.Entry {
		return []rendezvous.Entry{{
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws" + httpServer.URL[len("http"):],
			SourceID:  "local",
			ThreadID:  "th_1",
			SessionID: "sess_1",
		}}
	}, httpServer.Client())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notifications, err := source.SubscribeThread(ctx, appwire.ThreadReadParams{Ref: "local:th_1"})
	if err != nil {
		t.Fatalf("SubscribeThread: %v", err)
	}
	if notifications == nil {
		t.Fatal("notifications channel is nil")
	}
	select {
	case got := <-gotSubscribe:
		if !got {
			t.Fatal("SubscribeThread sent ThreadRead with Subscribe=false")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ThreadRead")
	}
}

func TestLocalDaemonSourceSubscribeThreadMapsConnectionRefused(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	endpoint := "ws://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	source := NewLocalDaemonSource("local", func() []rendezvous.Entry {
		return []rendezvous.Entry{{
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  endpoint,
			SourceID:  "local",
			ThreadID:  "th_1",
			SessionID: "sess_1",
		}}
	}, nil)

	_, err = source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: "local:th_1"})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("SubscribeThread error %T=%v, want WireError", err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok || wire.Code != appwire.CodeUnavailable || data.SerfErrorInfo != appwire.ErrorSessionUnavailable {
		t.Fatalf("wire=%+v", wire)
	}
}

func TestLocalDaemonSourceSubscribeThreadPreservesInitializeWireError(t *testing.T) {
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(app.Router(), appwire.MethodInitialize, func(_ context.Context, _ appwire.InitializeParams) (appwire.InitializeResponse, error) {
		return appwire.InitializeResponse{}, appwire.InvalidParams("broken pipe is semantic here")
	})
	httpServer := httptest.NewServer(http.HandlerFunc(app.ServeWebSocket))
	defer httpServer.Close()

	source := NewLocalDaemonSource("local", func() []rendezvous.Entry {
		return []rendezvous.Entry{{
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws" + httpServer.URL[len("http"):],
			SourceID:  "local",
			ThreadID:  "th_1",
			SessionID: "sess_1",
		}}
	}, httpServer.Client())

	_, err := source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: "local:th_1"})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("SubscribeThread error %T=%v, want WireError", err, err)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("wire=%+v, want InvalidParams preserved", wire)
	}
}

func TestLocalDaemonSourceSubscribeThreadPreservesThreadReadWireError(t *testing.T) {
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{}, appwire.InvalidParams("broken pipe is semantic here")
	})
	httpServer := httptest.NewServer(http.HandlerFunc(app.ServeWebSocket))
	defer httpServer.Close()

	source := NewLocalDaemonSource("local", func() []rendezvous.Entry {
		return []rendezvous.Entry{{
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws" + httpServer.URL[len("http"):],
			SourceID:  "local",
			ThreadID:  "th_1",
			SessionID: "sess_1",
		}}
	}, httpServer.Client())

	_, err := source.SubscribeThread(context.Background(), appwire.ThreadReadParams{Ref: "local:th_1"})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("SubscribeThread error %T=%v, want WireError", err, err)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("wire=%+v, want InvalidParams preserved", wire)
	}
}

func TestLocalDaemonSourceStartTurnMapsDroppedTransportToSessionUnavailable(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		transport := appwire.NewWSTransport(conn)
		msg, err := transport.Recv(r.Context())
		if err != nil {
			t.Errorf("receive initialize: %v", err)
			return
		}
		if msg.Request == nil {
			t.Errorf("initialize message=%+v", msg)
			return
		}
		if err := transport.Send(r.Context(), appwire.ResponseMessage(msg.Request.ID, appwire.InitializeResponse{})); err != nil {
			t.Errorf("send initialize response: %v", err)
			return
		}
		_, _ = transport.Recv(r.Context())
		_ = conn.Close(websocket.StatusAbnormalClosure, "dropped")
	}))
	defer httpServer.Close()

	source := NewLocalDaemonSource("local", func() []rendezvous.Entry {
		return []rendezvous.Entry{{
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws" + httpServer.URL[len("http"):],
			SourceID:  "local",
			ThreadID:  "th_1",
			SessionID: "sess_1",
		}}
	}, httpServer.Client())

	_, err := source.StartTurn(context.Background(), appwire.TurnStartParams{Ref: "local:th_1", Input: []appwire.InputItem{{Type: "text", Text: "hi"}}})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("StartTurn error %T=%v, want WireError", err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok || wire.Code != appwire.CodeUnavailable || data.SerfErrorInfo != appwire.ErrorSessionUnavailable {
		t.Fatalf("wire=%+v", wire)
	}
}

func TestLocalDaemonSourceSendsHubTokenBearer(t *testing.T) {
	gotAuth := make(chan string, 1)
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "th_1", SessionID: "sess_1", Serf: appwire.SerfThread{Ref: "local:th_1"}}}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth <- r.Header.Get("Authorization")
		app.ServeWebSocket(w, r)
	}))
	defer httpServer.Close()

	source := NewLocalDaemonSource("local", func() []rendezvous.Entry {
		return []rendezvous.Entry{{
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws" + httpServer.URL[len("http"):],
			SourceID:  "local",
			ThreadID:  "th_1",
			SessionID: "sess_1",
			HubToken:  "secret-token",
		}}
	}, httpServer.Client())

	if _, err := source.ReadThread(context.Background(), appwire.ThreadReadParams{Ref: "local:th_1"}); err != nil {
		t.Fatalf("ReadThread: %v", err)
	}
	select {
	case auth := <-gotAuth:
		if auth != "Bearer secret-token" {
			t.Fatalf("Authorization=%q, want bearer token", auth)
		}
	default:
		t.Fatal("daemon did not receive websocket request")
	}
}

func TestLocalDaemonSourceInterruptWithoutTurnIDUsesRESTInterrupt(t *testing.T) {
	var called bool
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/interrupt" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s, want POST", r.Method)
		}
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer daemon.Close()

	entry := rendezvous.Entry{
		Address:  daemon.Listener.Addr().String(),
		Endpoint: "ws://" + daemon.Listener.Addr().String() + "/rpc",
		Protocol: appwire.ProtocolVersion,
		SourceID: "local",
		ThreadID: "01INT",
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{{Entry: entry, SessionID: "01INT"}}
	}, daemon.Client())

	if err := source.InterruptTurn(context.Background(), appwire.TurnInterruptParams{Ref: "local:01INT"}); err != nil {
		t.Fatalf("InterruptTurn: %v", err)
	}
	if !called {
		t.Fatal("REST /interrupt was not called")
	}
}
