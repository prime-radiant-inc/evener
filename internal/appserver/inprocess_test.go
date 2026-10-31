package appserver

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

func inProcessRequest(t *testing.T, transport appwire.Transport, id int64, method string, params any) appwire.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := transport.Send(ctx, appwire.RequestMessage(appwire.NewIntID(id), method, params)); err != nil {
		t.Fatal(err)
	}
	msg, err := transport.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if (msg.Response == nil && msg.Error == nil) || msg.IDString() != appwire.NewIntID(id).String() {
		t.Fatalf("response = %+v", msg)
	}
	return msg
}

func openInProcessTest(t *testing.T, server *Server) appwire.Transport {
	t.Helper()
	transport, err := server.NewInProcessTransport(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	msg := inProcessRequest(t, transport, 1, appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion})
	if msg.Error != nil {
		t.Fatal(msg.Error)
	}
	return transport
}

func TestInProcessProtocolAndErrors(t *testing.T) {
	server := NewServer(ServerConfig{})
	transport, err := server.NewInProcessTransport(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	if msg := inProcessRequest(t, transport, 1, "test/read", struct{}{}); msg.Error == nil || msg.Error.Error.Code != appwire.CodeInvalidRequest {
		t.Fatalf("uninitialized = %+v", msg)
	}
	if msg := inProcessRequest(t, transport, 2, appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: "incompatible"}); msg.Error == nil {
		t.Fatal("accepted incompatible protocol")
	}
	if msg := inProcessRequest(t, transport, 3, appwire.MethodInitialize, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); msg.Error != nil {
		t.Fatal(msg.Error)
	}
	HandleTyped(server.Router(), "test/error", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, appwire.InvalidParams("preserved error")
	})
	if msg := inProcessRequest(t, transport, 4, "test/error", struct{}{}); msg.Error == nil || msg.Error.Error.Code != appwire.CodeInvalidParams {
		t.Fatalf("handler error = %+v", msg)
	}
}

func TestInProcessClientAndFailedHandoff(t *testing.T) {
	server := NewServer(ServerConfig{SourceID: "local"})
	transport, err := server.NewInProcessTransport(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	client := appwire.NewClient(transport)
	client.Start(t.Context())
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := client.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	var commits, aborts atomic.Int32
	HandleTyped(server.Router(), "test/failed-capture", func(ctx context.Context, _ struct{}) (struct{}, error) {
		if !CaptureSubscriptionWithHandoff(ctx, false, func() string { return "target" }, func() uint64 { return 0 }, func() bool { return true }, CaptureSubscriptionHandoff{Commit: func() { commits.Add(1) }, Abort: func() { aborts.Add(1) }}) {
			return struct{}{}, appwire.InternalError("capture failed")
		}
		return struct{}{}, appwire.InvalidParams("read failed")
	})
	if err := client.Request(ctx, "test/failed-capture", struct{}{}, nil); err == nil {
		t.Fatal("failed read succeeded")
	}
	// Closing joins teardown, including any finalizer still completing after
	// its response was dequeued by the client.
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	if commits.Load() != 0 || aborts.Load() != 1 || server.SubscriberCount("target") != 0 {
		t.Fatalf("commit=%d abort=%d subscribers=%d", commits.Load(), aborts.Load(), server.SubscriberCount("target"))
	}
}

func TestInProcessUsesBoundedRequestAdmission(t *testing.T) {
	server := NewServer(ServerConfig{})
	server.requestQueueCapacity = 1
	transport := openInProcessTest(t, server)
	entered := make(chan struct{})
	HandleTyped(server.Router(), "test/block", func(ctx context.Context, _ struct{}) (struct{}, error) {
		close(entered)
		<-ctx.Done()
		return struct{}{}, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := transport.Send(ctx, appwire.RequestMessage(appwire.NewIntID(2), "test/block", struct{}{})); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := transport.Send(ctx, appwire.RequestMessage(appwire.NewIntID(3), "test/queued", struct{}{})); err != nil {
		t.Fatal(err)
	}
	msg := inProcessRequest(t, transport, 4, "test/refused", struct{}{})
	if msg.Error == nil {
		t.Fatal("full inbound queue accepted request")
	}
	msg = inProcessRequest(t, transport, 5, appwire.MethodPing, struct{}{})
	if msg.Response == nil {
		t.Fatal("ping did not bypass full request queue")
	}
}

func TestInProcessAtomicHandoffAndRelease(t *testing.T) {
	server := NewServer(ServerConfig{})
	transport := openInProcessTest(t, server)
	captured, release := make(chan struct{}), make(chan struct{})
	var commits, aborts atomic.Int32
	HandleTyped(server.Router(), "test/capture", func(ctx context.Context, _ struct{}) (struct{}, error) {
		if !CaptureSubscriptionWithHandoff(ctx, false, func() string { return "target" }, func() uint64 { return 0 }, func() bool { return true }, CaptureSubscriptionHandoff{Commit: func() { commits.Add(1) }, Abort: func() { aborts.Add(1) }}) {
			return struct{}{}, appwire.InternalError("capture failed")
		}
		close(captured)
		select {
		case <-release:
		case <-ctx.Done():
			return struct{}{}, ctx.Err()
		}
		return struct{}{}, nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := transport.Send(ctx, appwire.RequestMessage(appwire.NewIntID(2), "test/capture", struct{}{})); err != nil {
		t.Fatal(err)
	}
	select {
	case <-captured:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	server.Broadcast("target", "test/post-cut", struct{}{})
	if commits.Load() != 0 {
		t.Fatal("commit before response")
	}
	close(release)
	first, err := transport.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := transport.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.Response == nil || first.IDString() != "2" || second.Notification == nil || second.Notification.Method != "test/post-cut" {
		t.Fatalf("handoff = %+v, %+v", first, second)
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	if commits.Load() != 1 || aborts.Load() != 0 || server.SubscriberCount("target") != 0 {
		t.Fatalf("commit=%d abort=%d subscribers=%d", commits.Load(), aborts.Load(), server.SubscriberCount("target"))
	}
}

func TestInProcessCancellationAbortsCapture(t *testing.T) {
	server := NewServer(ServerConfig{})
	transport := openInProcessTest(t, server)
	captured, finished := make(chan struct{}), make(chan struct{})
	var aborted atomic.Int32
	HandleTyped(server.Router(), "test/cancel", func(ctx context.Context, _ struct{}) (struct{}, error) {
		defer close(finished)
		if !CaptureSubscriptionWithHandoff(ctx, false, func() string { return "target" }, func() uint64 { return 0 }, func() bool { return true }, CaptureSubscriptionHandoff{Abort: func() { aborted.Add(1) }}) {
			return struct{}{}, appwire.InternalError("capture failed")
		}
		close(captured)
		<-ctx.Done()
		return struct{}{}, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := transport.Send(ctx, appwire.RequestMessage(appwire.NewIntID(2), "test/cancel", struct{}{})); err != nil {
		t.Fatal(err)
	}
	select {
	case <-captured:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if aborted.Load() != 1 || server.SubscriberCount("target") != 0 {
		t.Fatalf("abort=%d subscribers=%d", aborted.Load(), server.SubscriberCount("target"))
	}
	if _, err := transport.Recv(ctx); err == nil {
		t.Fatal("receive after close succeeded")
	}
	if err := transport.Send(ctx, appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodPing, struct{}{})); err == nil {
		t.Fatal("send after close succeeded")
	}
}

func TestInProcessSlowConsumerAndShutdown(t *testing.T) {
	server := NewServer(ServerConfig{})
	transport := openInProcessTest(t, server)
	HandleTyped(server.Router(), "test/subscribe", func(ctx context.Context, _ struct{}) (struct{}, error) {
		if !CaptureSubscriptionWithHandoff(ctx, false, func() string { return "target" }, func() uint64 { return 0 }, func() bool { return true }, CaptureSubscriptionHandoff{}) {
			return struct{}{}, appwire.InternalError("capture failed")
		}
		return struct{}{}, nil
	})
	if msg := inProcessRequest(t, transport, 2, "test/subscribe", struct{}{}); msg.Error != nil {
		t.Fatal(msg.Error)
	}
	for range appwire.NotificationBufferCap + 1 {
		server.Broadcast("target", "test/burst", struct{}{})
	}
	if server.SubscriberCount("target") != 0 {
		t.Fatal("slow consumer retained subscription")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := server.NewInProcessTransport(ctx); err == nil {
		t.Fatal("accepted connection after shutdown")
	}
}
