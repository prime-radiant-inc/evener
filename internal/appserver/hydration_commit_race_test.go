package appserver

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

func TestHydrationCommitSerializesAgainstReplacementCapture(t *testing.T) {
	server := NewServer(ServerConfig{ServerName: "test", SourceID: "local"})
	conn := server.NewConnection("conn-hydration-commit")
	server.registerConnection(conn)
	conn.setInitialized()

	notifier := NewNotifier(10)
	HandleTyped(server.Router(), "test/subscribe", func(ctx context.Context, _ struct{}) (struct{}, error) {
		if !CaptureSubscription(
			ctx,
			false,
			func() string { return "th_1" },
			notifier.CurrentSequence,
			func() bool { return true },
		) {
			return struct{}{}, errors.New("subscription capture was rejected")
		}
		return struct{}{}, nil
	})

	initialResponse := conn.HandleMessage(
		context.Background(),
		appwire.RequestMessage(appwire.NewIntID(1), "test/subscribe", struct{}{}),
	)
	responseQueued := make(chan struct{})
	releaseInitialCommit := make(chan struct{})
	var holdInitialCommit sync.Once
	server.beforeHydrationCommit = func() {
		holdInitialCommit.Do(func() {
			close(responseQueued)
			<-releaseInitialCommit
		})
	}
	initialEnqueued := make(chan error, 1)
	go func() {
		initialEnqueued <- conn.enqueueResponse(context.Background(), initialResponse)
	}()
	select {
	case <-responseQueued:
	case <-time.After(time.Second):
		t.Fatal("initial response did not reach the post-enqueue/pre-commit boundary")
	}

	resyncGate := make(chan struct{})
	releaseResyncCapture := make(chan struct{})
	server.beforeSubscriptionRegistration = func() {
		close(resyncGate)
		<-releaseResyncCapture
	}
	resyncResponseReady := make(chan appwire.Message, 1)
	go func() {
		resyncResponseReady <- conn.HandleMessage(
			context.Background(),
			appwire.RequestMessage(appwire.NewIntID(2), "test/subscribe", struct{}{}),
		)
	}()
	select {
	case <-resyncGate:
	case <-time.After(time.Second):
		t.Fatal("replacement capture did not reach its registration gate")
	}

	server.Broadcast("th_1", "test/pre-cut", struct{}{})
	close(releaseResyncCapture)
	var resyncResponse appwire.Message
	resyncReadyBeforeCommit := false
	// Before the fix, enqueueResponse does not hold hydrationMu, so the
	// replacement can finish while the initial commit is held. After the fix,
	// the lock is held and releasing the initial commit lets it proceed.
	if conn.hydrationMu.TryLock() {
		conn.hydrationMu.Unlock()
		select {
		case resyncResponse = <-resyncResponseReady:
			resyncReadyBeforeCommit = true
		case <-time.After(time.Second):
			t.Fatal("replacement capture did not complete before the initial commit")
		}
	}
	close(releaseInitialCommit)
	select {
	case err := <-initialEnqueued:
		if err != nil {
			t.Fatalf("initial response enqueue: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("initial response enqueue did not complete")
	}

	if !resyncReadyBeforeCommit {
		select {
		case resyncResponse = <-resyncResponseReady:
		case <-time.After(time.Second):
			t.Fatal("replacement capture did not complete")
		}
	}
	if resyncResponse.Error != nil {
		t.Fatalf("replacement response: %+v", resyncResponse.Error)
	}
	if err := conn.enqueueResponse(context.Background(), resyncResponse); err != nil {
		t.Fatalf("replacement response enqueue: %v", err)
	}

	var got []string
	for len(conn.send) > 0 {
		msg := <-conn.send
		switch {
		case msg.Response != nil:
			got = append(got, "response:"+msg.IDString())
		case msg.Notification != nil:
			got = append(got, msg.Notification.Method)
		}
	}
	want := []string{"response:1", "test/pre-cut", "response:2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("response/notification sequence = %v, want %v", got, want)
	}
}
