package appwire

import (
	"context"
	"encoding/json"
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
