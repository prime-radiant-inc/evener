package appsource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/rendezvous"
)

func TestRelaySessionHealthyRejoinUsesCanonicalConnection(t *testing.T) {
	entry := rendezvous.Entry{
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws://daemon",
		SourceID:  "local",
		ThreadID:  "thread-1",
		SessionID: "thread-1",
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{{Entry: entry}}
	}, nil)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var dialMu sync.Mutex
	dialCount := 0
	source.dial = func(context.Context, string, *http.Client, http.Header) (appwire.Transport, error) {
		dialMu.Lock()
		dialCount++
		dialMu.Unlock()
		return respondingTransport(func(method string) (any, error) {
			switch method {
			case appwire.MethodInitialize:
				return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion}, nil
			case appwire.MethodThreadRead:
				return appwire.ThreadReadResponse{Thread: appwire.Thread{
					ID:     "thread-1",
					Source: "local",
					Evener: appwire.EvenerThread{Ref: "local:thread-1"},
				}}, nil
			default:
				return nil, fmt.Errorf("unexpected method %q", method)
			}
		}), nil
	}

	params := appwire.ThreadReadParams{Ref: "local:thread-1", Subscribe: true}
	lease, err := source.acquireRelaySession(params)
	if err != nil {
		t.Fatalf("AcquireRelaySession: %v", err)
	}
	defer lease.Close()
	deliveries, err := lease.Listen(ctx)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if deliveries == nil {
		t.Fatal("Listen returned a nil delivery stream")
	}
	for i := range 2 {
		result, readErr := lease.Read(ctx, params)
		if readErr != nil {
			t.Fatalf("Read %d: %v", i+1, readErr)
		}
		if !result.Handoff.Commit() {
			t.Fatalf("Read %d handoff did not commit", i+1)
		}
	}

	dialMu.Lock()
	got := dialCount
	dialMu.Unlock()
	if got != 1 {
		t.Fatalf("AppWire connection count = %d, want one canonical connection for snapshot and live continuation", got)
	}
}

// fakeTimeoutError implements net.Error with Timeout()==true so we can drive
// localDaemonDialError without needing real socket timeouts.
type fakeTimeoutError struct{ msg string }

func (e fakeTimeoutError) Error() string   { return e.msg }
func (e fakeTimeoutError) Timeout() bool   { return true }
func (e fakeTimeoutError) Temporary() bool { return true }

func fuzzScenarioLocalDaemonDialErrorMapsTransportFailures(t *testing.T) {
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

func fuzzScenarioLocalDaemonDialErrorPassesThroughApplicationErrors(t *testing.T) {
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

func fuzzScenarioLocalDaemonSubscribeReadErrorPreservesApplicationWireErrors(t *testing.T) {
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

func fuzzScenarioLocalDaemonSubscribeReadErrorMapsInternalTransportWireErrors(t *testing.T) {
	got := localDaemonSubscribeReadError(appwire.InternalError("read failed: i/o timeout"))
	assertSessionUnavailable(t, got, "internal i/o timeout")
}

func fuzzScenarioLocalDaemonCallErrorMapsRawTransportFailures(t *testing.T) {
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

func fuzzScenarioLocalDaemonInitializeErrorPreservesApplicationWireErrors(t *testing.T) {
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

func fuzzScenarioLocalDaemonInitializeErrorMapsInternalTransportWireErrors(t *testing.T) {
	got := localDaemonInitializeError(appwire.InternalError("initialize failed: i/o timeout"))
	assertSessionUnavailable(t, got, "internal i/o timeout")
}

func fuzzScenarioLocalDaemonCallErrorPreservesCallerCancellation(t *testing.T) {
	if got := localDaemonCallError(context.Canceled); !errors.Is(got, context.Canceled) {
		t.Fatalf("context.Canceled remapped: %v", got)
	}
	if got := localDaemonCallError(context.DeadlineExceeded); !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("context.DeadlineExceeded remapped: %v", got)
	}
}

func fuzzScenarioLocalDaemonDialErrorIgnoresNil(t *testing.T) {
	if got := localDaemonDialError(nil); got != nil {
		t.Fatalf("nil mapped to %v, want nil", got)
	}
}

func fuzzScenarioLocalDaemonSourceReadThreadMapsIOTimeoutToSessionUnavailable(t *testing.T) {
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
			// Event-driven hold: the dial abort closes the client side, which
			// surfaces here as EOF/RST and releases this goroutine at the
			// ~200ms ctx deadline instead of after a fixed sleep. The read
			// deadline keeps the previous 2s bound so a stalled conn can
			// never outlive it.
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
				_, _ = io.Copy(io.Discard, c)
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

func fuzzScenarioLocalDaemonSourceReadThreadMapsEOFDuringHandshake(t *testing.T) {
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

func fuzzScenarioLocalDaemonSourceReadThreadReturnsCallerCtxCancellation(t *testing.T) {
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

func fuzzScenarioLocalDaemonSourceListsOnlyAppWireRendezvousThreads(t *testing.T) {
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
			// Has valid Endpoint and ThreadID but wrong Protocol — must be excluded by
			// the Protocol filter, not by the Endpoint/ThreadID guards.
			{
				Protocol: "legacy-protocol",
				Endpoint: "ws://127.0.0.1:2/rpc",
				SourceID: "local",
				ThreadID: "th_2",
			},
			// Missing both Endpoint and Protocol — covers the empty-Endpoint guard.
			{
				PID:     3,
				Address: "127.0.0.1:3",
			},
		}
	}, nil)

	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "th_1" || resp.Data[0].Evener.Ref != "local:th_1" {
		t.Fatalf("threads=%+v", resp.Data)
	}
}

func fuzzScenarioLocalDaemonSourceThreadTimestampsUseStartedAtAndZeroForMissing(t *testing.T) {
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

func fuzzScenarioLocalDaemonSourceReadsThreadOverAppWire(t *testing.T) {
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "th_1", SessionID: "sess_1", Evener: appwire.EvenerThread{Ref: "local:th_1"}}}, nil
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
	if resp.Thread.ID != "th_1" || resp.Thread.Evener.Ref != "local:th_1" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func fuzzScenarioLocalDaemonSourceJobsOverAppWire(t *testing.T) {
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	var listParams appwire.JobsListParams
	appserver.HandleTyped(app.Router(), appwire.MethodEvenerJobsList, func(_ context.Context, params appwire.JobsListParams) (appwire.JobsListResponse, error) {
		listParams = params
		return appwire.JobsListResponse{Data: appwire.JobActivityTree{Root: appwire.JobActivitySession{SessionID: "sess_1", Ref: "local:sess_1"}}}, nil
	})
	var outputParams appwire.JobsOutputParams
	appserver.HandleTyped(app.Router(), appwire.MethodEvenerJobsOutput, func(_ context.Context, params appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
		outputParams = params
		return appwire.JobsOutputResponse{Data: map[string]any{"jobId": params.JobID, "output": "hello"}}, nil
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

	ctx := context.Background()
	list, err := source.ListJobs(ctx, appwire.JobsListParams{Ref: "local:th_1", Continuation: "next-page"})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	tree, ok := list.Data.(map[string]any)
	if !ok {
		t.Fatalf("ListJobs data = %#v, want activity tree payload", list.Data)
	}
	root, ok := tree["root"].(map[string]any)
	if !ok || root["sessionId"] != "sess_1" || root["ref"] != "local:sess_1" {
		t.Fatalf("tree root = %#v", tree["root"])
	}
	if listParams.Ref != "local:th_1" || listParams.Continuation != "next-page" {
		t.Fatalf("list params forwarded = %+v", listParams)
	}

	out, err := source.JobOutput(ctx, appwire.JobsOutputParams{Ref: "local:th_1", JobID: "job_1", MaxBytes: 1024})
	if err != nil {
		t.Fatalf("JobOutput: %v", err)
	}
	tail, ok := out.Data.(map[string]any)
	if !ok || tail["jobId"] != "job_1" || tail["output"] != "hello" {
		t.Fatalf("JobOutput data = %#v, want the daemon's own tail payload", out.Data)
	}
	if outputParams.JobID != "job_1" || outputParams.MaxBytes != 1024 {
		t.Fatalf("params forwarded = %+v", outputParams)
	}
}

func fuzzScenarioLocalDaemonSourceDrainUsesInputShapeDirectly(t *testing.T) {
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

	_, err := source.DrainAsSteer(context.Background(), appwire.TurnDrainAsSteerParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "sess_1", ExpectedQueueRevision: 0, Ref: "local:th_1", Input: []appwire.InputItem{{Type: "text", Text: "composer payload"}}})
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
func fuzzScenarioLocalDaemonSourceReadThreadIncludesQueue(t *testing.T) {
	wantQueue := appwire.QueueState{
		Depth:   2,
		Preview: []string{"first queued message", "second queued message"},
	}
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        "th_1",
			SessionID: "sess_1",
			Evener: appwire.EvenerThread{
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
	got := resp.Thread.Evener.Queue
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

// harnessSupportRosterEntries is the idle/active/closed roster the
// harness-support list tests share: one entry per status the capability
// derivation keys on.
func harnessSupportRosterEntries() []LocalDaemonEntry {
	return []LocalDaemonEntry{
		{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/idle", ThreadID: "th_idle", SessionID: "sess_idle"}, Status: "idle"},
		{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/processing", ThreadID: "th_processing", SessionID: "sess_processing"}, Status: appwire.ThreadStatusActive},
		{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/closed", ThreadID: "th_closed", SessionID: "sess_closed"}, Status: appwire.ThreadStatusClosed},
	}
}

func fuzzScenarioLocalDaemonSourceListAdvertisesQueueAsHarnessSupport(t *testing.T) {
	source := NewLocalDaemonSourceWithEntries("local", harnessSupportRosterEntries, nil)

	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(resp.Data) != 3 {
		t.Fatalf("threads len=%d, want 3: %+v", len(resp.Data), resp.Data)
	}
	capsByID := map[string]appwire.ThreadCapabilities{}
	for _, thread := range resp.Data {
		capsByID[thread.ID] = thread.Evener.Capabilities
	}
	// Queue is harness support, not "a turn in flight" (#1375): every open
	// entry advertises it, and the client applies the status. A status-folded
	// projection here made ListThreads disagree with ThreadRead for one session.
	for _, id := range []string{"th_idle", "th_processing"} {
		if !capsByID[id].Queue {
			t.Fatalf("%s did not advertise the queue capability: %+v", id, capsByID[id])
		}
	}
	// A closed entry is the exception: the daemon withholds steer, interrupt
	// and queue support once closed (appCapabilitiesLocked's `!closed`), so the
	// roster must too, or the same session reads differently from ListThreads
	// and from ThreadRead.
	closed := capsByID["th_closed"]
	if closed.Queue || closed.Steer || closed.Interrupt {
		t.Fatalf("closed entry advertised turn actions: %+v", closed)
	}
}

// TestLocalDaemonSourceListAdvertisesSharedNotes guards the roster path: a live
// local session supports shared notes, so ListThreads must advertise the
// capability instead of making list-derived models report it unsupported until
// hydration. A read-only alias must not; a restart-required session keeps only
// the read capability, because its saved notes are still readable even though
// every mutation is refused.
func TestLocalDaemonSourceListAdvertisesSharedNotes(t *testing.T) {
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/live", ThreadID: "th_live", SessionID: "sess_live"}, Status: "idle"},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/alias", ThreadID: "th_alias"}, SessionID: "sess_alias", OwnerSessionID: "sess_live", Status: "idle", ReadOnlyAlias: true},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/restart", ThreadID: "th_restart", SessionID: "sess_restart"}, Status: appwire.ThreadStatusRestartRequired},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/aliasrestart", ThreadID: "th_alias_restart"}, SessionID: "sess_alias_restart", OwnerSessionID: "sess_live", Status: appwire.ThreadStatusRestartRequired, ReadOnlyAlias: true},
		}
	}, nil)

	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	// Keyed by session: a read-only alias derives its thread id from the session
	// it mirrors, which is not the id this test names it by.
	capsBySession := map[string]appwire.ThreadCapabilities{}
	for _, thread := range resp.Data {
		capsBySession[thread.SessionID] = thread.Evener.Capabilities
	}
	if live, ok := capsBySession["sess_live"]; !ok || !live.SharedNotes {
		t.Fatalf("live local session did not advertise shared notes: %+v (all: %+v)", live, capsBySession)
	}
	if alias, ok := capsBySession["sess_alias"]; !ok || alias.SharedNotes {
		t.Fatalf("read-only alias advertised shared notes: %+v (all: %+v)", alias, capsBySession)
	}
	restart, ok := capsBySession["sess_restart"]
	if !ok || !restart.SharedNotes {
		t.Fatalf("restart-required session did not keep shared notes readable: %+v (all: %+v)", restart, capsBySession)
	}
	if restart.Send || restart.Steer || restart.Rename {
		t.Fatalf("restart-required session advertised mutations: %+v", restart)
	}
	// The two branches compose: a read-only alias that also needs a restart keeps
	// the alias's empty capability set, so the restart branch's read-only notes
	// advertisement must not leak through the alias's own gate.
	aliasRestart, ok := capsBySession["sess_alias_restart"]
	if !ok {
		t.Fatalf("read-only restart-required alias missing from the roster: %+v", capsBySession)
	}
	if aliasRestart != (appwire.ThreadCapabilities{}) {
		t.Fatalf("read-only restart-required alias advertised capabilities: %+v", aliasRestart)
	}
}

// TestLocalDaemonSourceListAdvertisesSkillInput guards the roster path the
// same way the shared-notes pin does: a live local session's harness supports
// skill selections, so ListThreads must advertise the capability instead of
// making list-derived models report it unsupported until hydration.
func TestLocalDaemonSourceListAdvertisesSkillInput(t *testing.T) {
	source := NewLocalDaemonSourceWithEntries("local", harnessSupportRosterEntries, nil)

	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	capsByID := map[string]appwire.ThreadCapabilities{}
	for _, thread := range resp.Data {
		capsByID[thread.ID] = thread.Evener.Capabilities
	}
	for _, id := range []string{"th_idle", "th_processing"} {
		if !capsByID[id].SkillInput {
			t.Fatalf("%s did not advertise the skillInput capability: %+v", id, capsByID[id])
		}
	}
	// A closed row keeps the capability for the same reason the daemon's read
	// does, while the actions that would carry a selection stay withheld.
	if !capsByID["th_closed"].SkillInput {
		t.Fatalf("closed entry withheld skillInput: %+v", capsByID["th_closed"])
	}
	// The daemon does not close-gate skillInput, but it does close-gate
	// changeVisionModel (appCapabilitiesLocked), so the closed row must not
	// advertise the one while keeping the other — a closed row that differs
	// from the read of the same session is the drift this file exists to
	// remove.
	if capsByID["th_closed"].ChangeVisionModel {
		t.Fatalf("closed entry advertised changeVisionModel: %+v", capsByID["th_closed"])
	}
}

// TestLocalDaemonSourceListFallbackFoldsDaemonStatus pins the unprobed
// fallback's folding against the daemon's own derivations
// (appCapabilitiesLocked, clearBlockedReasonLocked): activity withholds
// Send and Clear but no turn action (#1363, #1375), closed withholds
// every mutating bit while Shutdown and skillInput — the two the daemon
// does not close-gate — stay advertised, and unresolved approval work (an
// unanswered ask, a blocked escalation) withholds Clear the way the
// daemon's clear gate does. A fallback that overstated these offered the
// same offer-then-refuse drift the probed path was fixed for.
func TestLocalDaemonSourceListFallbackFoldsDaemonStatus(t *testing.T) {
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/processing", ThreadID: "th_processing", SessionID: "sess_processing"}, Status: appwire.ThreadStatusActive},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/closed", ThreadID: "th_closed", SessionID: "sess_closed"}, Status: appwire.ThreadStatusClosed},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/ask", ThreadID: "th_ask", SessionID: "sess_ask"}, Status: appwire.ThreadStatusAwaiting, PendingAsk: true},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/escalation", ThreadID: "th_escalation", SessionID: "sess_escalation"}, Status: appwire.ThreadStatusIdle, PendingEscalation: true},
		}
	}, nil)

	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	capsByID := map[string]appwire.ThreadCapabilities{}
	for _, thread := range resp.Data {
		capsByID[thread.ID] = thread.Evener.Capabilities
	}

	// Every expected literal is the daemon's whole answer at that row's
	// status, so no bit — present or added later — goes unpinned. Awaiting
	// keeps Send (only activity and closure move it) but withholds Clear on
	// the unanswered ask, and the escalation row folds Clear on the
	// roster's escalation flag alone.
	wantActive := appwire.ThreadCapabilities{
		Steer: true, Interrupt: true, Compact: true, Shutdown: true,
		ChangeModel: true, ChangeVisionModel: true, Queue: true,
		Goal: true, SharedNotes: true, Rename: true, SkillInput: true,
	}
	wantClearWithheld := appwire.ThreadCapabilities{
		Send: true, Steer: true, Interrupt: true, Compact: true, Shutdown: true,
		ChangeModel: true, ChangeVisionModel: true, Queue: true,
		Goal: true, SharedNotes: true, Rename: true, SkillInput: true,
	}
	if got := capsByID["th_processing"]; got != wantActive {
		t.Fatalf("active row = %+v, want the daemon's active answer (Send and Clear folded): %+v", got, wantActive)
	}
	if got := capsByID["th_ask"]; got != wantClearWithheld {
		t.Fatalf("awaiting row with an unanswered ask = %+v, want Clear folded on the approval work: %+v", got, wantClearWithheld)
	}
	if got := capsByID["th_escalation"]; got != wantClearWithheld {
		t.Fatalf("idle row with a blocked escalation = %+v, want Clear folded on the approval work: %+v", got, wantClearWithheld)
	}
	if got := capsByID["th_closed"]; got != (appwire.ThreadCapabilities{Shutdown: true, SkillInput: true}) {
		t.Fatalf("closed row = %+v, want only Shutdown and SkillInput, the bits the daemon does not close-gate", got)
	}
}

// TestLocalDaemonSourceListUsesProbedCapabilities pins the probe-carried row:
// when the roster's probe captured the daemon's own capability set, the list
// row mirrors it rather than the fallback approximation — the same one-answer
// rule the status follows (#1840), fork included: the daemon hardwires that
// bit false and the hub's applyHubForkCapability owns turning it on. The
// restart-required and read-only alias branches keep replacing the set
// wholesale.
func TestLocalDaemonSourceListUsesProbedCapabilities(t *testing.T) {
	// An under-wired daemon's idle answer, a set the fallback approximation
	// would never produce: no steer, no queue, no skill-input surface, but its
	// vision-model seam is wired.
	probed := appwire.ThreadCapabilities{
		Send: true, Compact: true, Clear: true, Shutdown: true,
		ChangeModel: true, ChangeVisionModel: true, Rename: true,
		Goal: true, SharedNotes: true,
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/probed", ThreadID: "th_probed", SessionID: "sess_probed"},
				Status: "idle", Capabilities: probed, CapabilitiesKnown: true},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/unprobed", ThreadID: "th_unprobed", SessionID: "sess_unprobed"},
				Status: "idle"},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/probedrestart", ThreadID: "th_probed_restart", SessionID: "sess_probed_restart"},
				Status: appwire.ThreadStatusRestartRequired, Capabilities: probed, CapabilitiesKnown: true},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/probedalias", ThreadID: "th_probed_alias"},
				SessionID: "sess_probed_alias", OwnerSessionID: "sess_probed", Status: "idle",
				ReadOnlyAlias: true, Capabilities: probed, CapabilitiesKnown: true},
		}
	}, nil)

	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	// Keyed by session: an alias's thread id derives from the session it
	// mirrors, which is not the id this test names it by.
	capsBySession := map[string]appwire.ThreadCapabilities{}
	for _, thread := range resp.Data {
		capsBySession[thread.SessionID] = thread.Evener.Capabilities
	}
	if got := capsBySession["sess_probed"]; got != probed {
		t.Fatalf("probed row capabilities = %+v, want the probe's set mirrored verbatim %+v", got, probed)
	}
	unprobed := capsBySession["sess_unprobed"]
	if !unprobed.SkillInput || !unprobed.Queue || !unprobed.ChangeVisionModel {
		t.Fatalf("unprobed row lost the fallback advertisement: %+v", unprobed)
	}
	if restart := capsBySession["sess_probed_restart"]; restart != (appwire.ThreadCapabilities{SharedNotes: true}) {
		t.Fatalf("restart-required row must replace even a probed set with the read-only one: %+v", restart)
	}
	if alias := capsBySession["sess_probed_alias"]; alias != (appwire.ThreadCapabilities{}) {
		t.Fatalf("read-only alias must zero even a probed set: %+v", alias)
	}
}

// TestLocalDaemonSourceListCarriesAskPending guards the TUI attach path (Task
// 29's per-row ask marker): when the hub's entries() feed reports PendingAsk
// on a LocalDaemonEntry, threadFromEntry must carry it through to
// appwire.EvenerThread.AskPending, since that's the field the TUI dashboard
// reads (cmd/evener-tui/hub_types.go).
func fuzzScenarioLocalDaemonSourceListCarriesAskPending(t *testing.T) {
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/ask", ThreadID: "th_ask", SessionID: "sess_ask"}, Status: appwire.ThreadStatusAwaiting, PendingAsk: true},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/idle", ThreadID: "th_idle", SessionID: "sess_idle"}, Status: "idle", PendingAsk: false},
		}
	}, nil)

	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	askByID := map[string]bool{}
	for _, thread := range resp.Data {
		askByID[thread.ID] = thread.Evener.AskPending
	}
	if !askByID["th_ask"] {
		t.Fatalf("ask-pending thread must carry Evener.AskPending=true: %+v", resp.Data)
	}
	if askByID["th_idle"] {
		t.Fatalf("non-ask-pending thread must carry Evener.AskPending=false: %+v", resp.Data)
	}
}

func TestLocalDaemonSourceListCarriesRunningNonAgentJobs(t *testing.T) {
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{{
			Entry: rendezvous.Entry{
				Protocol:  appwire.ProtocolVersion,
				Endpoint:  "ws://127.0.0.1/jobs",
				ThreadID:  "th_jobs",
				SessionID: "sess_jobs",
			},
			Status: appwire.ThreadStatusActive,
			RunningJobs: []appwire.EvenerJobInfo{{
				JobID: "job_shell", JobType: "shell", Status: "running",
			}},
			CompletedJobs: []appwire.EvenerJobInfo{{
				JobID: "job_done", JobType: "shell", Status: "completed",
			}},
		}}
	}, nil)

	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Evener.Diagnostics == nil || len(resp.Data[0].Evener.Diagnostics.Jobs) != 2 {
		t.Fatalf("thread list diagnostics = %+v, want active and completed shell jobs", resp.Data)
	}
	job := resp.Data[0].Evener.Diagnostics.Jobs[0]
	if job.JobID != "job_shell" || job.JobType != "shell" || job.Status != "running" {
		t.Fatalf("running job = %+v, want shell identity and status", job)
	}
}

// TestLocalDaemonSourceListCarriesWatches guards the local thread/list
// compatibility path: a LocalDaemonEntry carrying the roster's watches must
// surface them in the typed thread diagnostics, the same snapshot navigation
// already serves, with inner slices the bridge owns.
func TestLocalDaemonSourceListCarriesWatches(t *testing.T) {
	entry := LocalDaemonEntry{
		Entry: rendezvous.Entry{
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws://127.0.0.1/watches",
			ThreadID:  "th_watch",
			SessionID: "sess_watch",
		},
		Status: appwire.ThreadStatusActive,
		Watches: []appwire.EvenerWatchInfo{{
			ID:            "watch_1",
			Note:          "poll queue",
			Cadence:       []appwire.EvenerWatchCadence{{Kind: "every", Seconds: 600}},
			Events:        []string{"job.completed"},
			DeliveryTimes: []string{"2026-08-05T14:58:00Z"},
		}},
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{entry}
	}, nil)

	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Evener.Diagnostics == nil || len(resp.Data[0].Evener.Diagnostics.Watches) != 1 {
		t.Fatalf("thread list diagnostics = %+v, want one watch", resp.Data)
	}
	got := resp.Data[0].Evener.Diagnostics.Watches[0]
	if got.ID != "watch_1" || got.Note != "poll queue" {
		t.Fatalf("watch = %+v, want identity and note", got)
	}
	// Mutate the entry's inner slices after the bridge; the diagnostics snapshot
	// must already own its rows.
	entry.Watches[0].Cadence[0].Kind = "mutated"
	entry.Watches[0].Events[0] = "mutated"
	entry.Watches[0].DeliveryTimes[0] = "mutated"
	if got.Cadence[0].Kind != "every" || got.Events[0] != "job.completed" || got.DeliveryTimes[0] != "2026-08-05T14:58:00Z" {
		t.Fatalf("entry watch mutation reached the bridged diagnostics: %+v", got)
	}
}

func TestThreadFromEntryCarriesStableRefAndLiveInstance(t *testing.T) {
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return nil }, nil)
	thread := source.threadFromEntry(LocalDaemonEntry{Entry: rendezvous.Entry{
		Protocol:     appwire.ProtocolVersion,
		Endpoint:     "ws://127.0.0.1/rpc",
		ThreadID:     "instance-new",
		SessionID:    "instance-new",
		WorkspaceRef: "local:workspace",
		InstanceID:   "instance-new",
	}})

	if thread.ID != "instance-new" {
		t.Fatalf("thread id = %q, want live instance id", thread.ID)
	}
	if thread.Evener.Ref != "local:workspace" {
		t.Fatalf("thread ref = %q, want stable workspace ref", thread.Evener.Ref)
	}
	if thread.Evener.InstanceID != "instance-new" {
		t.Fatalf("thread instance id = %q, want live instance id", thread.Evener.InstanceID)
	}
}

func fuzzScenarioLocalDaemonSourceSubscribeThreadRequestsSubscription(t *testing.T) {
	gotSubscribe := make(chan bool, 1)
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		gotSubscribe <- params.Subscribe
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "th_1", SessionID: "sess_1", Evener: appwire.EvenerThread{Ref: "local:th_1"}}}, nil
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

func fuzzScenarioLocalDaemonSourceSubscribeThreadMapsConnectionRefused(t *testing.T) {
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
	if !ok || wire.Code != appwire.CodeUnavailable || data.EvenerErrorInfo != appwire.ErrorSessionUnavailable {
		t.Fatalf("wire=%+v", wire)
	}
}

func fuzzScenarioLocalDaemonSourceSubscribeThreadPreservesInitializeWireError(t *testing.T) {
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

func fuzzScenarioLocalDaemonSourceSubscribeThreadPreservesThreadReadWireError(t *testing.T) {
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

func fuzzScenarioLocalDaemonSourceStartTurnMapsDroppedTransportToMutationOutcomeUnknown(t *testing.T) {
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
		if err := transport.Send(r.Context(), appwire.ResponseMessage(msg.Request.ID, appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion})); err != nil {
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

	_, err := source.StartTurn(context.Background(), appwire.TurnStartParams{ClientMutationID: "test-mutation", ExpectedInstanceID: "sess_1", Ref: "local:th_1", Input: []appwire.InputItem{{Type: "text", Text: "hi"}}})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("StartTurn error %T=%v, want WireError", err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok ||
		wire.Code != appwire.CodeInternalError ||
		data.EvenerErrorInfo != appwire.ErrorMutationOutcomeUnknown ||
		data.ClientMutationID != "test-mutation" ||
		data.MutationOutcome != appwire.MutationOutcomeUnknown ||
		data.RetryDisposition != appwire.RetryDispositionAutomatic {
		t.Fatalf("wire=%+v", wire)
	}
}

func fuzzScenarioLocalDaemonSourceSendsHubTokenBearer(t *testing.T) {
	gotAuth := make(chan string, 1)
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "th_1", SessionID: "sess_1", Evener: appwire.EvenerThread{Ref: "local:th_1"}}}, nil
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

// TestThreadFromEntryReadOnlyAliasCarriesKindAndParentRef guards ledger #112:
// a ReadOnlyAlias entry addresses an in-process descendant (subagent) served
// through its owner's daemon endpoint. threadFromEntry must mark it as a
// subagent with a non-empty ParentRef so hub views (web_api_tree.go's
// IsSubagent/parent-lookup) can distinguish it from a top-level session,
// instead of leaving Evener.Kind/ParentRef at their zero values.
func TestThreadFromEntryReadOnlyAliasCarriesKindAndParentRef(t *testing.T) {
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry { return nil }, nil)
	item := LocalDaemonEntry{
		Entry: rendezvous.Entry{
			Protocol: appwire.ProtocolVersion,
			Endpoint: "ws://127.0.0.1/rpc",
			ThreadID: "th_root",
		},
		SessionID:      "sess_child",
		OwnerSessionID: "sess_root",
		ReadOnlyAlias:  true,
	}

	thread := source.threadFromEntry(item)

	if thread.Evener.Kind != "subagent" {
		t.Fatalf("Evener.Kind = %q, want %q", thread.Evener.Kind, "subagent")
	}
	if thread.Evener.ParentRef == "" {
		t.Fatal("Evener.ParentRef is empty, want a non-empty parent reference")
	}
}

func TestLocalDaemonRootAndReadOnlyAliasShareRelaySession(t *testing.T) {
	entry := rendezvous.Entry{
		Protocol:     appwire.ProtocolVersion,
		Endpoint:     "ws://127.0.0.1/rpc",
		SourceID:     "local",
		ThreadID:     "sess_root",
		SessionID:    "sess_root",
		WorkspaceRef: "local:sess_root",
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: entry, SessionID: "sess_root"},
			{Entry: entry, SessionID: "sess_child", OwnerSessionID: "sess_root", ReadOnlyAlias: true},
		}
	}, nil)
	rootRef, err := source.ResolveRelaySession(appwire.ThreadReadParams{Ref: "local:sess_root"})
	if err != nil {
		t.Fatalf("resolve root relay: %v", err)
	}
	rootValue, err := source.AcquireRelaySession(rootRef)
	if err != nil {
		t.Fatalf("acquire root relay: %v", err)
	}
	defer rootValue.Close()
	childRef, err := source.ResolveRelaySession(appwire.ThreadReadParams{Ref: "local:sess_child"})
	if err != nil {
		t.Fatalf("resolve child relay: %v", err)
	}
	childValue, err := source.AcquireRelaySession(childRef)
	if err != nil {
		t.Fatalf("acquire child relay: %v", err)
	}
	defer childValue.Close()

	root, ok := rootValue.(*relaySessionLease)
	if !ok {
		t.Fatalf("root lease type = %T, want *relaySessionLease", rootValue)
	}
	child, ok := childValue.(*relaySessionLease)
	if !ok {
		t.Fatalf("child lease type = %T, want *relaySessionLease", childValue)
	}
	if root.session != child.session {
		t.Fatal("root and read-only child aliases acquired different relay sessions")
	}
	source.relayMu.Lock()
	actors := len(source.relaySessions)
	source.relayMu.Unlock()
	if actors != 1 {
		t.Fatalf("relay session actors = %d, want 1", actors)
	}
}

func TestLocalDaemonResolveRelaySessionCanonicalizesAliasesWithoutAcquiring(t *testing.T) {
	entry := rendezvous.Entry{
		Protocol:     appwire.ProtocolVersion,
		Endpoint:     "ws://127.0.0.1/rpc",
		SourceID:     "local",
		ThreadID:     "sess_root",
		SessionID:    "sess_root",
		WorkspaceRef: "local:sess_root",
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: entry, SessionID: "sess_root"},
			{Entry: entry, SessionID: "sess_child", OwnerSessionID: "sess_root", ReadOnlyAlias: true},
		}
	}, nil)

	root, err := source.ResolveRelaySession(appwire.ThreadReadParams{Ref: "local:sess_root"})
	if err != nil {
		t.Fatalf("resolve root relay: %v", err)
	}
	child, err := source.ResolveRelaySession(appwire.ThreadReadParams{Ref: "local:sess_child"})
	if err != nil {
		t.Fatalf("resolve child relay: %v", err)
	}
	byThreadID, err := source.ResolveRelaySession(appwire.ThreadReadParams{ThreadID: "sess_child"})
	if err != nil {
		t.Fatalf("resolve thread-ID-only relay: %v", err)
	}
	want := appwire.Ref{SourceID: "local", ThreadID: "sess_root"}
	for name, got := range map[string]appwire.Ref{"root": root, "child": child, "thread-ID-only": byThreadID} {
		if got != want {
			t.Errorf("%s canonical ref = %#v, want %#v", name, got, want)
		}
		if got.String() == "" {
			t.Errorf("%s canonical ref is empty", name)
		}
	}

	for _, params := range []appwire.ThreadReadParams{
		{Ref: "not-a-ref"},
		{Ref: "other:sess_root"},
		{Ref: "local:missing"},
		{ThreadID: "missing"},
	} {
		if _, err := source.ResolveRelaySession(params); err == nil {
			t.Errorf("ResolveRelaySession(%+v) succeeded for invalid target", params)
		}
	}
	source.relayMu.Lock()
	acquired := len(source.relaySessions)
	source.relayMu.Unlock()
	if acquired != 0 {
		t.Fatalf("resolution acquired %d relay sessions, want none", acquired)
	}
}

func TestLocalDaemonListPreservesRestartRequiredStatus(t *testing.T) {
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{{
			Entry:  rendezvous.Entry{Protocol: "evener-appwire-v4", Endpoint: "ws://daemon", SourceID: "local", ThreadID: "owner", SessionID: "owner"},
			Status: appwire.ThreadStatusRestartRequired,
		}}
	}, nil)
	dials := 0
	source.dial = func(context.Context, string, *http.Client, http.Header) (appwire.Transport, error) {
		dials++
		return nil, errors.New("unexpected incompatible daemon dial")
	}
	response, err := source.ListThreads(t.Context(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 1 {
		t.Fatalf("threads=%d", len(response.Data))
	}
	thread := response.Data[0]
	if thread.Status.Type != appwire.ThreadStatusRestartRequired {
		t.Fatalf("status=%s", thread.Status.Type)
	}
	if thread.Evener.Capabilities != (appwire.ThreadCapabilities{SharedNotes: true}) {
		t.Fatalf("capabilities=%+v", thread.Evener.Capabilities)
	}
	if _, err := source.ReadThread(t.Context(), appwire.ThreadReadParams{Ref: "local:owner"}); err == nil {
		t.Fatal("incompatible daemon was readable through a live route")
	}
	if _, err := source.ListModels(t.Context(), appwire.ModelListParams{}); err != nil {
		t.Fatal(err)
	}
	if dials != 0 {
		t.Fatalf("incompatible daemon dials=%d", dials)
	}

}

func TestLocalDaemonListsSessionOnlyRestartRequiredEntry(t *testing.T) {
	for _, observedID := range []string{"", "owner"} {
		t.Run("observed="+observedID, func(t *testing.T) {
			source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
				return []LocalDaemonEntry{{Entry: rendezvous.Entry{Protocol: "evener-appwire-v4", Endpoint: "ws://daemon", SourceID: "local", SessionID: "owner"}, SessionID: observedID, Status: appwire.ThreadStatusRestartRequired}}
			}, nil)
			response, err := source.ListThreads(t.Context(), appwire.ThreadListParams{})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Data) != 1 {
				t.Fatalf("threads=%+v", response.Data)
			}
			thread := response.Data[0]
			if thread.ID != "owner" || thread.Evener.Ref != "local:owner" || thread.Status.Type != appwire.ThreadStatusRestartRequired {
				t.Fatalf("thread=%+v", thread)
			}
			if thread.Evener.Capabilities != (appwire.ThreadCapabilities{SharedNotes: true}) {
				t.Fatalf("capabilities=%+v", thread.Evener.Capabilities)
			}
		})
	}
}

func TestLocalDaemonResolveSubscriptionAdmissionSingleSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name      string
		params    appwire.ThreadReadParams
		want      string
		snapshots int
	}{
		{"stable_root", appwire.ThreadReadParams{Ref: "local:stable"}, "local:stable", 1},
		{"current_root", appwire.ThreadReadParams{ThreadID: "current"}, "local:stable", 1},
		{"current_root_ref", appwire.ThreadReadParams{Ref: "local:current"}, "local:stable", 1},
		{"child_ref", appwire.ThreadReadParams{Ref: "local:child"}, "local:child", 1},
		{"child_id", appwire.ThreadReadParams{ThreadID: "child"}, "local:child", 1},
		{"root_ref_precedence", appwire.ThreadReadParams{Ref: "local:stable", ThreadID: "child"}, "local:stable", 1},
		{"child_ref_precedence", appwire.ThreadReadParams{Ref: "local:child", ThreadID: "current"}, "local:child", 1},
		{"missing_ref_precedence", appwire.ThreadReadParams{Ref: "local:missing", ThreadID: "current"}, "", 1},
		{"missing_id", appwire.ThreadReadParams{ThreadID: "missing"}, "", 1},
		{"invalid_ref_precedence", appwire.ThreadReadParams{Ref: "invalid", ThreadID: "current"}, "", 0},
		{"foreign_ref_precedence", appwire.ThreadReadParams{Ref: "other:stable", ThreadID: "current"}, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/rpc", SourceID: "local", ThreadID: "stable", SessionID: "current", WorkspaceRef: "local:stable"}
			snapshots := 0
			source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
				snapshots++
				if snapshots > 1 {
					return nil // A second inventory read cannot resolve this target.
				}
				return []LocalDaemonEntry{
					{Entry: entry},
					{Entry: entry, SessionID: "child", OwnerSessionID: "current", ReadOnlyAlias: true},
				}
			}, nil)
			got, err := source.ResolveSubscriptionAdmission(tc.params)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("invalid admission resolved as %q", got.String())
				}
			} else if err != nil || got.String() != tc.want {
				t.Fatalf("admission = %q, %v; want %q", got.String(), err, tc.want)
			}
			if snapshots != tc.snapshots {
				t.Fatalf("inventory snapshots = %d, want %d", snapshots, tc.snapshots)
			}
			if len(source.relaySessions) != 0 {
				t.Fatal("admission resolution acquired a relay session")
			}
		})
	}
}

func TestLocalDaemonListKeepsChildReferencesDistinctFromOwnerWorkspace(t *testing.T) {
	for _, workspaceRef := range []string{"local:stable", ""} {
		t.Run(workspaceRef, func(t *testing.T) {
			entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/rpc", SourceID: "local", ThreadID: "current", SessionID: "current", WorkspaceRef: workspaceRef}
			source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
				return []LocalDaemonEntry{
					{Entry: entry},
					{Entry: entry, SessionID: "child", OwnerSessionID: "current", ReadOnlyAlias: true},
				}
			}, nil)
			response, err := source.ListThreads(context.Background(), appwire.ThreadListParams{IncludeSubagents: true})
			if err != nil {
				t.Fatal(err)
			}
			byRef := map[string]appwire.Thread{}
			for _, thread := range response.Data {
				byRef[thread.Evener.Ref] = thread
			}
			if len(byRef) != 2 {
				t.Fatalf("root and child must have distinct references: %+v", response.Data)
			}
			rootRef := workspaceRef
			if rootRef == "" {
				rootRef = "local:current"
			}
			child, ok := byRef["local:child"]
			if !ok || child.Evener.ParentRef != rootRef || child.Evener.Kind != "subagent" {
				t.Fatalf("child must target its own transcript under %s: %+v", rootRef, child)
			}
			if child.Evener.Capabilities != (appwire.ThreadCapabilities{}) {
				t.Fatalf("child alias must remain read-only: %+v", child.Evener.Capabilities)
			}
			admission, err := source.ResolveSubscriptionAdmission(appwire.ThreadReadParams{Ref: child.Evener.Ref})
			if err != nil || admission.String() != child.Evener.Ref {
				t.Fatalf("listed child reference must resolve to its own subscription: %v, %v", admission, err)
			}
		})
	}
}

func TestLocalDaemonSourceListThreadsHonorsCanceledContext(t *testing.T) {
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, ThreadID: "thread-1", SessionID: "session-1"}}}
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := source.ListThreads(ctx, appwire.ThreadListParams{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ListThreads error=%v, want context cancellation", err)
	}
}
