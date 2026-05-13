package appsource

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nhooyr.io/websocket"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/rendezvous"
)

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

	_, err := source.StartTurn(context.Background(), appwire.TurnStartParams{Ref: "local:th_1", Prompt: "hi"})
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
