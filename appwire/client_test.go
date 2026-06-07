package appwire

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type memoryTransport struct {
	writes chan Message
	reads  chan Message
}

func newMemoryTransport() *memoryTransport {
	return &memoryTransport{
		writes: make(chan Message, 8),
		reads:  make(chan Message, 8),
	}
}

func (m *memoryTransport) Send(_ context.Context, msg Message) error {
	m.writes <- msg
	return nil
}

func (m *memoryTransport) Recv(ctx context.Context) (Message, error) {
	select {
	case msg := <-m.reads:
		return msg, nil
	case <-ctx.Done():
		return Message{}, ctx.Err()
	}
}

func (m *memoryTransport) Close() error { return nil }

func TestClientRoutesResponsesAndNotifications(t *testing.T) {
	transport := newMemoryTransport()
	client := NewClient(transport)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.Start(ctx)

	done := make(chan struct {
		resp ThreadListResponse
		err  error
	}, 1)
	go func() {
		resp, err := client.ThreadList(ctx, ThreadListParams{Limit: 10})
		done <- struct {
			resp ThreadListResponse
			err  error
		}{resp: resp, err: err}
	}()

	var written Message
	select {
	case written = <-transport.writes:
	case <-time.After(time.Second):
		t.Fatal("request was not written")
	}
	if written.Request.Method != MethodThreadList {
		t.Fatalf("method=%q", written.Request.Method)
	}

	transport.reads <- NotificationMessage(NotifyThreadStatusChanged, map[string]string{"threadId": "th_1"})
	transport.reads <- ResponseMessage(written.Request.ID, ThreadListResponse{Data: []Thread{{ID: "th_1", Source: "serf"}}})

	select {
	case notif := <-client.Notifications():
		if notif.Method != NotifyThreadStatusChanged {
			t.Fatalf("notification method=%q", notif.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("notification was not routed")
	}

	var result struct {
		resp ThreadListResponse
		err  error
	}
	select {
	case result = <-done:
	case <-time.After(time.Second):
		t.Fatal("response was not routed")
	}
	if result.err != nil {
		t.Fatalf("ThreadList: %v", result.err)
	}
	if len(result.resp.Data) != 1 || result.resp.Data[0].ID != "th_1" {
		t.Fatalf("resp=%+v", result.resp)
	}

	var params ThreadListParams
	if err := json.Unmarshal(written.Request.Params, &params); err != nil {
		t.Fatalf("params decode: %v", err)
	}
	if params.Limit != 10 {
		t.Fatalf("limit=%d, want 10", params.Limit)
	}
}

func TestClientGoalSetRoundTrip(t *testing.T) {
	transport := newMemoryTransport()
	client := NewClient(transport)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.Start(ctx)

	done := make(chan struct {
		resp GoalSetResponse
		err  error
	}, 1)
	go func() {
		resp, err := client.GoalSet(ctx, GoalSetParams{Ref: "local:th_1", Objective: "improve coverage"})
		done <- struct {
			resp GoalSetResponse
			err  error
		}{resp: resp, err: err}
	}()

	var written Message
	select {
	case written = <-transport.writes:
	case <-time.After(time.Second):
		t.Fatal("request was not written")
	}
	if written.Request.Method != MethodGoalSet {
		t.Fatalf("method=%q", written.Request.Method)
	}
	var params GoalSetParams
	if err := json.Unmarshal(written.Request.Params, &params); err != nil {
		t.Fatalf("params decode: %v", err)
	}
	if params.Ref != "local:th_1" || params.Objective != "improve coverage" {
		t.Fatalf("params=%+v", params)
	}

	transport.reads <- ResponseMessage(written.Request.ID, GoalSetResponse{Started: true})

	var result struct {
		resp GoalSetResponse
		err  error
	}
	select {
	case result = <-done:
	case <-time.After(time.Second):
		t.Fatal("response was not routed")
	}
	if result.err != nil {
		t.Fatalf("GoalSet: %v", result.err)
	}
	if !result.resp.Started {
		t.Fatalf("started=%v, want true", result.resp.Started)
	}
}

func TestClientFailsPendingWhenNotificationsOverflow(t *testing.T) {
	transport := &memoryTransport{
		writes: make(chan Message, 1),
		reads:  make(chan Message, 256),
	}
	client := NewClient(transport)

	startCtx, stop := context.WithCancel(context.Background())
	defer stop()
	client.Start(startCtx)

	requestCtx, cancelRequest := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancelRequest()
	done := make(chan struct {
		resp ThreadListResponse
		err  error
	}, 1)
	go func() {
		resp, err := client.ThreadList(requestCtx, ThreadListParams{Limit: 1})
		done <- struct {
			resp ThreadListResponse
			err  error
		}{resp: resp, err: err}
	}()

	var written Message
	select {
	case written = <-transport.writes:
	case <-time.After(time.Second):
		t.Fatal("request was not written")
	}
	for i := 0; i < cap(client.notifications)+1; i++ {
		transport.reads <- NotificationMessage(NotifyThreadStatusChanged, map[string]int{"seq": i})
	}
	transport.reads <- ResponseMessage(written.Request.ID, ThreadListResponse{Data: []Thread{{ID: "th_backpressure", Source: "serf"}}})

	select {
	case result := <-done:
		if result.err == nil || !strings.Contains(result.err.Error(), "notification buffer overflow") {
			t.Fatalf("ThreadList err=%v resp=%+v, want notification overflow", result.err, result.resp)
		}
	case <-time.After(time.Second):
		t.Fatal("pending request was not failed after notification buffer overflow")
	}
}

func TestClientFailPendingPreservesRequestID(t *testing.T) {
	client := NewClient(newMemoryTransport())
	id := NewIntID(42)
	ch := make(chan Message, 1)
	client.pending[id.String()] = pendingRequest{id: id, ch: ch}

	client.failPending(context.Canceled)

	select {
	case msg := <-ch:
		if msg.Error == nil {
			t.Fatalf("message=%+v, want error response", msg)
		}
		if msg.Error.ID.String() != id.String() {
			t.Fatalf("error id=%s, want %s", msg.Error.ID.String(), id.String())
		}
	case <-time.After(time.Second):
		t.Fatal("pending request was not failed")
	}
}
