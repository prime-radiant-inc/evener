package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/coder/websocket"
	"primeradiant.com/serf/appwire"
)

func exerciseAppserverStack(t rapidTB) {
	n := NewNotifier(0)
	for i := 0; i < 1001; i++ {
		n.Record("thread", "notice", i)
	}
	if got := n.ReplayAfter(999, "thread"); len(got) != 2 {
		t.Fatalf("notifier replay length = %d", len(got))
	}
	_ = n.ReplayAfter(0, "other")
	_ = n.ReplayAfter(1001, "")

	s := NewServer(ServerConfig{ServerName: "coverage", Version: "1", SourceID: "local"})
	r := s.Router()
	r.Handle("nil", func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	r.Handle("wire", func(context.Context, json.RawMessage) (any, error) { return nil, appwire.InvalidParams("bad") })
	_ = r.Methods()
	if _, err := r.Dispatch(context.Background(), appwire.Request{Method: "nil"}); err != nil {
		t.Fatalf("nil dispatch: %v", err)
	}
	_, _ = r.Dispatch(context.Background(), appwire.Request{Method: "missing"})
	_, err := r.Dispatch(context.Background(), appwire.Request{Method: "wire"})
	_ = WireError(err)
	_ = WireError(errors.New("plain"))

	c := s.NewConnection("c")
	_ = c.ID()
	_ = c.HandleMessage(context.Background(), appwire.Message{})
	_ = c.HandleMessage(context.Background(), appwire.NotificationMessage("other", nil))
	Subscribe(context.Background(), "thread")
	ReplaceSubscriptions(context.Background(), "thread")
	Notify(context.Background(), "notice", nil)
	ctx := context.WithValue(context.Background(), connectionContextKey{}, c)
	Subscribe(ctx, "")
	Subscribe(ctx, "old")
	ReplaceSubscriptions(ctx, "new")
	ReplaceSubscriptions(ctx, "")
	ReplaceSubscriptions(ctx, "new")
	Notify(ctx, "", nil)
	Notify(ctx, "notice", nil)
	_ = s.subs.IsSubscribed("c", "new")
	_ = s.subs.Threads("c")

	s.registerConnection(c)
	s.Broadcast("absent", "notice", nil)
	s.subs.Subscribe("ghost", "thread")
	s.Broadcast("thread", "notice", nil)
	s.BroadcastAll("all", nil)

	full := s.NewConnection("full")
	s.registerConnection(full)
	full.Subscribe("thread")
	for i := 0; i < cap(full.send); i++ {
		full.enqueue(appwire.Message{})
	}
	s.Broadcast("thread", "overflow", nil)

	fullAll := s.NewConnection("full-all")
	s.registerConnection(fullAll)
	for i := 0; i < cap(fullAll.send); i++ {
		fullAll.enqueue(appwire.Message{})
	}
	s.BroadcastAll("overflow", nil)

	cancelCtx, cancel := context.WithCancel(context.Background())
	c.setCancel(cancel)
	s.unregisterConnection(c)
	<-cancelCtx.Done()
	c.cancelContext()
	_ = c.enqueue(appwire.Message{})
	if err := c.enqueueResponse(context.Background(), appwire.Message{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("closed enqueue response = %v", err)
	}
	c.closeSend()
	s.unregisterConnection(nil)

	wait := s.NewConnection("wait")
	for i := 0; i < cap(wait.send); i++ {
		wait.enqueue(appwire.Message{})
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	_ = wait.enqueueResponse(canceled, appwire.Message{})
	<-wait.send
	if err := wait.enqueueResponse(context.Background(), appwire.Message{}); err != nil {
		t.Fatalf("available enqueue response: %v", err)
	}

	exerciseSendLoops(t)
	exerciseKeepalive(t)
	exerciseReceiveLoops(t)
	exerciseWebSocket(t)
}

type rapidTB interface {
	Fatalf(string, ...any)
}

type stackSender struct{ err error }

func (s stackSender) Send(context.Context, appwire.Message) error { return s.err }

func exerciseSendLoops(t rapidTB) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runWebSocketSendLoop(ctx, stackSender{}, make(chan appwire.Message))
	closed := make(chan appwire.Message)
	close(closed)
	runWebSocketSendLoop(context.Background(), stackSender{}, closed)
	for _, sender := range []stackSender{{}, {err: errors.New("send")}} {
		ch := make(chan appwire.Message, 1)
		ch <- appwire.Message{}
		close(ch)
		runWebSocketSendLoop(context.Background(), sender, ch)
	}
}

type stackTransport struct {
	messages []appwire.Message
	err      error
}

func (s *stackTransport) Send(context.Context, appwire.Message) error { return nil }
func (s *stackTransport) Recv(context.Context) (appwire.Message, error) {
	if len(s.messages) == 0 {
		return appwire.Message{}, s.err
	}
	msg := s.messages[0]
	s.messages = s.messages[1:]
	return msg, nil
}

type stackCloser struct{ closes int }

func (s *stackCloser) Close(websocket.StatusCode, string) error { s.closes++; return nil }

func exerciseReceiveLoops(t rapidTB) {
	s := NewServer(ServerConfig{})
	c := s.NewConnection("recv")
	closer := &stackCloser{}
	normal := websocket.CloseError{Code: websocket.StatusNormalClosure}
	runWebSocketReceiveLoop(context.Background(), closer, &stackTransport{
		messages: []appwire.Message{appwire.NotificationMessage("ignored", nil)},
		err:      normal,
	}, c)
	if closer.closes != 0 {
		t.Fatalf("normal receive close count = %d", closer.closes)
	}
	runWebSocketReceiveLoop(context.Background(), closer, &stackTransport{err: errors.New("recv")}, c)
	if closer.closes != 1 {
		t.Fatalf("abnormal receive close count = %d", closer.closes)
	}
	c.closeSend()
	runWebSocketReceiveLoop(context.Background(), closer, &stackTransport{
		messages: []appwire.Message{appwire.RequestMessage(appwire.NewIntID(1), appwire.MethodPing, nil)},
	}, c)
}

type stackPinger struct {
	called chan struct{}
	err    error
}

func (p stackPinger) Ping(context.Context) error {
	select {
	case p.called <- struct{}{}:
	default:
	}
	return p.err
}

func exerciseKeepalive(t rapidTB) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runWebSocketKeepalive(ctx, stackPinger{called: make(chan struct{}, 1)}, func() {}, time.Hour, time.Hour)

	failed := make(chan struct{})
	runWebSocketKeepalive(context.Background(), stackPinger{called: make(chan struct{}, 1), err: errors.New("ping")}, func() { close(failed) }, time.Nanosecond, time.Second)
	<-failed

	ctx, cancel = context.WithCancel(context.Background())
	called := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		runWebSocketKeepalive(ctx, stackPinger{called: called}, cancel, time.Nanosecond, time.Second)
		close(done)
	}()
	<-called
	cancel()
	<-done
}

func exerciseWebSocket(t rapidTB) {
	s := NewServer(ServerConfig{})
	recorder := httptest.NewRecorder()
	s.ServeWebSocket(recorder, httptest.NewRequest(http.MethodGet, "http://example.test", nil))

	HandleTyped(s.Router(), appwire.MethodThreadRead, func(ctx context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		Subscribe(ctx, "thread")
		return appwire.ThreadReadResponse{}, nil
	})
	HandleTyped(s.Router(), appwire.MethodThreadClear, func(ctx context.Context, _ appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
		ctx.Value(connectionContextKey{}).(*Connection).cancelContext()
		return appwire.ThreadClearResponse{}, nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(s.ServeWebSocket))
	defer httpServer.Close()
	ctx := context.Background()
	transport, err := appwire.DialWebSocket(ctx, "ws"+httpServer.URL[len("http"):], httpServer.Client())
	if err != nil {
		t.Fatalf("websocket dial: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(ctx)
	if _, err = client.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		t.Fatalf("websocket initialize: %v", err)
	}
	if _, err = client.ThreadRead(ctx, appwire.ThreadReadParams{}); err != nil {
		t.Fatalf("websocket subscribe: %v", err)
	}
	s.Broadcast("thread", "notice", nil)
	select {
	case <-client.Notifications():
	case <-time.After(time.Second):
		t.Fatalf("websocket notification timeout")
	}
	if err = transport.Send(ctx, appwire.NotificationMessage("ignored", nil)); err != nil {
		t.Fatalf("websocket notification send: %v", err)
	}
	_, _ = client.ThreadClear(ctx, appwire.ThreadClearParams{})
	_ = transport.Close()

	raw, _, err := websocket.Dial(ctx, "ws"+httpServer.URL[len("http"):], nil)
	if err != nil {
		t.Fatalf("raw websocket dial: %v", err)
	}
	_ = raw.Close(websocket.StatusPolicyViolation, "test close")
}
