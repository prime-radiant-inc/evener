package appwire

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestSessionToolsClosedOperationUnion(t *testing.T) {
	for _, raw := range []string{`{}`, `{"discover":{},"read":{"ref":"local:a"}}`, `{"method":"thread/read"}`, `{"discover":{},"caller":"local:forged"}`, `{"read":{"ref":"local:a","includeTurns":true}}`, `{"read":{"ref":"local:a","subscribe":true}}`} {
		t.Run(raw, func(t *testing.T) {
			var request SessionOperationRequest
			if err := json.Unmarshal([]byte(raw), &request); err == nil {
				t.Fatalf("accepted %s", raw)
			}
		})
	}
	for _, raw := range []string{`{"discover":{"sourceIds":["local"],"limit":5}}`, `{"read":{"ref":"local:a"}}`} {
		var request SessionOperationRequest
		if err := json.Unmarshal([]byte(raw), &request); err != nil {
			t.Fatalf("rejected %s: %v", raw, err)
		}
	}
}

type pendingWatchOwner struct {
	started chan context.Context
	ended   chan struct{}
}

type pendingInvokeOwner struct {
	started chan context.Context
	setup   <-chan struct{}
}

func (*pendingInvokeOwner) State(context.Context) (ManagerView, error) { return ManagerView{}, nil }
func (o *pendingInvokeOwner) Invoke(ctx context.Context, _ SessionOperationRequest) (SessionOperationResponse, error) {
	if o.setup != nil {
		<-o.setup
	}
	o.started <- ctx
	<-ctx.Done()
	return SessionOperationResponse{}, ctx.Err()
}
func (*pendingInvokeOwner) Watch(context.Context, SessionWatchRequest) (<-chan SessionWatchUpdate, error) {
	return nil, Unavailable("unused")
}

func TestSessionToolsCancelBeforeSetupAndAtCapacity(t *testing.T) {
	for _, capacity := range []int{1, SessionToolsPendingLimit} {
		t.Run(fmt.Sprint(capacity), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			transport := newMemoryTransport()
			transport.writes = make(chan Message, 128)
			client := NewClient(transport)
			setup := make(chan struct{})
			release := sync.OnceFunc(func() { close(setup) })
			defer release()
			owner := &pendingInvokeOwner{started: make(chan context.Context, capacity), setup: setup}
			client.SetSessionTools(SessionBinding{BindingID: "binding"}, owner)
			defer client.sessionTools.cancel()
			for i := 1; i <= capacity; i++ {
				request := RequestMessage(NewIntID(-int64(i)), MethodRuntimeSessionInvoke, SessionRuntimeRequest{BindingID: "binding", Operation: &SessionOperationRequest{Read: &ThreadReadParams{Ref: "local:workspace"}}})
				client.receiveSessionRequest(ctx, *request.Request)
			}
			// Cancel before the owners leave their setup gate. A wrong binding
			// must not cancel even a valid request ID; cleanup needs no free slot.
			wrong := RequestMessage(NewIntID(-100), MethodRuntimeSessionCancel, SessionRuntimeCancel{BindingID: "other", RequestID: -1})
			client.receiveSessionRequest(ctx, *wrong.Request)
			if response := <-transport.writes; response.Error == nil {
				t.Fatal("cross-binding cancel accepted")
			}
			request := RequestMessage(NewIntID(-101), MethodRuntimeSessionCancel, SessionRuntimeCancel{BindingID: "binding", RequestID: -1})
			client.receiveSessionRequest(ctx, *request.Request)
			if response := <-transport.writes; response.Error != nil {
				t.Fatalf("cancel refused at capacity: %+v", response)
			}
			release()
			cancelled := 0
			for range capacity {
				select {
				case ownerCtx := <-owner.started:
					if ownerCtx.Err() != nil {
						cancelled++
					}
				case <-ctx.Done():
					t.Fatal("owner setup did not finish")
				}
			}
			if cancelled != 1 {
				t.Fatalf("cancelled owners=%d, want only the identified call", cancelled)
			}
			// Slot release follows removal from the bounded call map. Fill every
			// free slot to synchronize with that cleanup without polling.
			for i := capacity - 1; i < SessionToolsPendingLimit; i++ {
				select {
				case client.sessionTools.slots <- struct{}{}:
				case <-ctx.Done():
					t.Fatal("cancelled owner retained its slot")
				}
			}
			client.sessionTools.mu.Lock()
			remaining := len(client.sessionTools.calls)
			client.sessionTools.mu.Unlock()
			if remaining != capacity-1 {
				t.Fatalf("remaining in-flight reservations=%d", remaining)
			}
			// Free one test-owned slot, then run a real fixed state operation.
			<-client.sessionTools.slots
			state := RequestMessage(NewIntID(-102), MethodRuntimeSessionState, SessionRuntimeRequest{BindingID: "binding"})
			client.receiveSessionRequest(ctx, *state.Request)
			for {
				select {
				case response := <-transport.writes:
					if response.IDString() == "-102" {
						if response.Error != nil {
							t.Fatalf("new operation refused: %+v", response)
						}
						return
					}
				case <-ctx.Done():
					t.Fatal("new operation did not finish")
				}
			}
		})
	}
}

func (*pendingWatchOwner) State(context.Context) (ManagerView, error) { return ManagerView{}, nil }
func (*pendingWatchOwner) Invoke(context.Context, SessionOperationRequest) (SessionOperationResponse, error) {
	return SessionOperationResponse{}, nil
}
func (o *pendingWatchOwner) Watch(ctx context.Context, _ SessionWatchRequest) (<-chan SessionWatchUpdate, error) {
	o.started <- ctx
	<-ctx.Done()
	close(o.ended)
	return nil, ctx.Err()
}

func TestSessionToolsUnwatchCancelsPendingSetupAtCapacity(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	transport := newMemoryTransport()
	client := NewClient(transport)
	owner := &pendingWatchOwner{started: make(chan context.Context, 1), ended: make(chan struct{})}
	client.SetSessionTools(SessionBinding{BindingID: "binding"}, owner)
	defer client.sessionTools.cancel()
	watch := RequestMessage(NewIntID(-1), MethodRuntimeSessionWatch, SessionRuntimeRequest{BindingID: "binding", WatchID: "watch", Watch: &SessionWatchRequest{Ref: "local:workspace"}})
	client.receiveSessionRequest(ctx, *watch.Request)
	select {
	case <-owner.started:
	case <-ctx.Done():
		t.Fatal("watch setup did not start")
	}
	for i := 1; i < SessionToolsPendingLimit; i++ {
		client.sessionTools.slots <- struct{}{}
	}
	unwatch := RequestMessage(NewIntID(-2), MethodRuntimeSessionUnwatch, SessionRuntimeRequest{BindingID: "binding", WatchID: "watch"})
	client.receiveSessionRequest(ctx, *unwatch.Request)
	select {
	case <-owner.ended:
	case <-ctx.Done():
		t.Fatal("unwatch could not cancel pending setup at capacity")
	}
	for i := 0; i < 2; i++ {
		select {
		case <-transport.writes:
		case <-ctx.Done():
			t.Fatal("missing fixed response")
		}
	}
}
