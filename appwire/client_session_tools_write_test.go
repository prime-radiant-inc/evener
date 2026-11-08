package appwire

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
)

// The barrier delays a real WebSocket send after the client accepts the frame.
type observationWriteBarrier struct {
	Transport
	started chan context.Context
	release chan struct{}
}

func (b *observationWriteBarrier) Send(ctx context.Context, msg Message) error {
	if msg.Notification != nil && msg.Notification.Method == NotificationRuntimeSessionObservation {
		b.started <- ctx
		<-b.release
	}
	return b.Transport.Send(ctx, msg)
}

func TestSessionToolsObservationWriteSurvivesWatchCancellation(t *testing.T) {
	accepted := make(chan *WSTransport, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		accepted <- NewWSTransport(conn)
	}))
	defer server.Close()
	transport, err := DialWebSocket(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	peer := <-accepted
	barrier := &observationWriteBarrier{Transport: transport, started: make(chan context.Context, 1), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(barrier.release) })
	defer release()
	client := NewClient(barrier)
	client.SetSessionTools(SessionBinding{BindingID: "binding"}, nil)
	client.Start(t.Context())
	defer client.Close()       //nolint:errcheck // test cleanup
	defer peer.conn.CloseNow() //nolint:errcheck // test cleanup
	watchCtx, cancelWatch := context.WithCancel(t.Context())
	defer cancelWatch()
	written := make(chan error, 1)
	go func() {
		written <- client.sendSessionFrameOrClose(watchCtx, NotificationMessage(NotificationRuntimeSessionObservation, SessionRuntimeObservation{BindingID: "binding", WatchID: "watch", Ended: true}))
	}()
	writeCtx := <-barrier.started
	cancelWatch()
	if err := writeCtx.Err(); err != nil {
		t.Errorf("watch cancellation interrupted the accepted transport write: %v", err)
	}
	release()
	if err := <-written; err != nil {
		t.Fatalf("observation write after cancellation: %v", err)
	}
	observation, err := peer.Recv(t.Context())
	if err != nil || observation.Notification == nil || observation.Notification.Method != NotificationRuntimeSessionObservation {
		t.Fatalf("observation on real websocket = %+v, err=%v", observation, err)
	}
	result := make(chan error, 1)
	go func() {
		var reply ManagerView
		err := client.Request(t.Context(), MethodRuntimeSessionState, SessionRuntimeRequest{BindingID: "binding"}, &reply)
		if err == nil && (reply.Ref != "local:workspace" || !reply.Manager) {
			t.Errorf("state on retained websocket = %+v", reply)
		}
		result <- err
	}()
	request, err := peer.Recv(t.Context())
	if err != nil || request.Request == nil || request.Request.Method != MethodRuntimeSessionState {
		t.Fatalf("state request on retained websocket = %+v, err=%v", request, err)
	}
	if err := peer.Send(t.Context(), ResponseMessage(request.Request.ID, ManagerView{Ref: "local:workspace", Manager: true})); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("state on retained websocket: %v", err)
	}
}
