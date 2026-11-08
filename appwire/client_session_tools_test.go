package appwire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"
)

type errorSessionOwner struct{ err error }

func (o errorSessionOwner) State(context.Context) (ManagerView, error) { return ManagerView{}, o.err }
func (o errorSessionOwner) Invoke(context.Context, SessionOperationRequest) (SessionOperationResponse, error) {
	return SessionOperationResponse{}, o.err
}
func (o errorSessionOwner) Watch(context.Context, SessionWatchRequest) (<-chan SessionWatchUpdate, error) {
	return nil, o.err
}

func TestSessionToolsReverseWrappedErrorPreservesRecoveryData(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"notAccepted", "created"} {
		for _, wrapped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/wrapped=%t", stage, wrapped), func(t *testing.T) {
				data := map[string]any{"acceptance": stage}
				if stage == "created" {
					data["createdRef"], data["createdInstanceID"] = "local:worker", "worker-instance"
				}
				wire := WireError{Code: -32042, Message: "fixture", Data: data}
				var err error = wire
				if wrapped {
					err = fmt.Errorf("creation: %w", wire)
				}
				got := reverseSessionError(t, err)
				if got.Code != wire.Code || !reflect.DeepEqual(got.Data, data) {
					t.Fatalf("reverse error lost recovery contract: %+v", got)
				}
			})
		}
	}
	t.Run("nonwire", func(t *testing.T) {
		got := reverseSessionError(t, errors.New("fixture"))
		want := Unavailable("unused")
		if got.Code != want.Code {
			t.Fatalf("nonwire code=%d, want %d", got.Code, want.Code)
		}
	})
}

func reverseSessionError(t *testing.T, ownerError error) WireError {
	t.Helper()
	transport := newMemoryTransport()
	client := NewClient(transport)
	client.SetSessionTools(SessionBinding{BindingID: "binding"}, errorSessionOwner{ownerError})
	t.Cleanup(client.sessionTools.cancel)
	request := RequestMessage(NewIntID(-1), MethodRuntimeSessionInvoke, SessionRuntimeRequest{BindingID: "binding", Operation: &SessionOperationRequest{Discover: &ThreadListParams{}}})
	client.receiveSessionRequest(t.Context(), *request.Request)
	select {
	case reply := <-transport.writes:
		if reply.Error == nil || reply.IDString() != "-1" {
			t.Fatalf("unexpected reverse response: %+v", reply)
		}
		raw, err := json.Marshal(reply.Error)
		if err != nil {
			t.Fatal(err)
		}
		var decoded Message
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		return decoded.Error.Error
	case <-t.Context().Done():
		t.Fatal("reverse response did not arrive")
		return WireError{}
	}
}

func TestSessionToolsClosedOperationUnion(t *testing.T) {
	for _, raw := range []string{`{}`, `{"discover":{},"read":{"ref":"local:a"}}`, `{"fetch":{"ref":"local:a","fetchCursor":"item"},"turns":{"ref":"local:a"}}`, `{"fetch":{"ref":"local:a","fetchCursor":"item"},"jobOutput":{"jobId":"job"}}`, `{"method":"thread/read"}`, `{"discover":{},"caller":"local:forged"}`, `{"read":{"ref":"local:a","includeTurns":true}}`, `{"read":{"ref":"local:a","subscribe":true}}`} {
		t.Run(raw, func(t *testing.T) {
			var request SessionOperationRequest
			if err := json.Unmarshal([]byte(raw), &request); err == nil {
				t.Fatalf("accepted %s", raw)
			}
		})
	}
	for _, raw := range []string{
		`{"discover":{"sourceIds":["local"],"limit":5}}`,
		`{"read":{"ref":"local:a"}}`,
		`{"turns":{"ref":"local:a","turnWindow":{"latest":1},"itemTypes":["messages"]}}`,
		`{"fetch":{"ref":"local:a","fetchCursor":"item"}}`,
	} {
		var request SessionOperationRequest
		if err := json.Unmarshal([]byte(raw), &request); err != nil {
			t.Fatalf("rejected %s: %v", raw, err)
		}
	}
}

func TestSessionFetchResponseCarriesRangedCanonicalItem(t *testing.T) {
	want := SessionOperationResponse{Fetch: &SessionFetchResponse{
		Position:    ThreadItemPosition{Entry: 7, Item: 2},
		Version:     4,
		TotalLength: 260_000,
		Offset:      180_000,
		Data:        "Y2Fub25pY2Fs",
		FetchCursor: "next-range",
	}}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got SessionOperationResponse
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got.Fetch == nil || *got.Fetch != *want.Fetch {
		t.Fatalf("fetch response = %+v, want %+v", got.Fetch, want.Fetch)
	}
}

func TestSessionOperationRequestAcceptsWindowedContentSelection(t *testing.T) {
	includeThinking := false
	request := SessionOperationRequest{Turns: &ThreadTurnsListParams{
		IncludeThinking: &includeThinking,
		Ref:             "local:session",
		ItemLimit:       10,
		TurnWindow:      &ThreadTurnWindow{Latest: 2},
	}}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate rejected windowed content selection: %v", err)
	}
}

func TestSessionOperationRequestWindowGrammar(t *testing.T) {
	for _, tc := range []struct {
		name  string
		raw   string
		valid bool
	}{
		{"legacy", `{"turns":{"ref":"local:a"}}`, true},
		{"latest", `{"turns":{"ref":"local:a","turnWindow":{"latest":2}}}`, true},
		{"turn", `{"turns":{"ref":"local:a","turnWindow":{"turn":"turn_m2"}}}`, true},
		{"since", `{"turns":{"ref":"local:a","turnWindow":{"since":"turn_m2","limit":3}}}`, true},
		{"before", `{"turns":{"ref":"local:a","turnWindow":{"before":{"turnId":"turn_m2","limit":3}}}}`, true},
		{"selectors", `{"turns":{"ref":"local:a","turnWindow":{"latest":1},"itemTypes":["messages","reasoning","commandExecution"],"previewBytes":256,"messageBytes":512,"windowBudgetBytes":1024,"itemLimit":40}}`, true},
		{"empty_window", `{"turns":{"ref":"local:a","turnWindow":{}}}`, false},
		{"zero_latest", `{"turns":{"ref":"local:a","turnWindow":{"latest":0}}}`, false},
		{"mixed_window", `{"turns":{"ref":"local:a","turnWindow":{"latest":1,"turn":"turn_m2"}}}`, false},
		{"since_without_limit", `{"turns":{"ref":"local:a","turnWindow":{"since":"turn_m2"}}}`, false},
		{"before_without_turn", `{"turns":{"ref":"local:a","turnWindow":{"before":{"limit":3}}}}`, false},
		{"before_without_limit", `{"turns":{"ref":"local:a","turnWindow":{"before":{"turnId":"turn_m2"}}}}`, false},
		{"types_without_window", `{"turns":{"ref":"local:a","itemTypes":["messages"]}}`, false},
		{"bounds_without_window", `{"turns":{"ref":"local:a","previewBytes":256}}`, false},
		{"negative_bound", `{"turns":{"ref":"local:a","turnWindow":{"latest":1},"messageBytes":-1}}`, false},
		{"item_limit_over_max", `{"turns":{"ref":"local:a","turnWindow":{"latest":1},"itemLimit":41}}`, false},
		{"unknown_item_type", `{"turns":{"ref":"local:a","turnWindow":{"latest":1},"itemTypes":["unknown"]}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var request SessionOperationRequest
			err := json.Unmarshal([]byte(tc.raw), &request)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, want %v: %v", err == nil, tc.valid, err)
			}
		})
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
		t.Run(strconv.Itoa(capacity), func(t *testing.T) {
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
	for range SessionToolsPendingLimit - 1 {
		client.sessionTools.slots <- struct{}{}
	}
	unwatch := RequestMessage(NewIntID(-2), MethodRuntimeSessionUnwatch, SessionRuntimeRequest{BindingID: "binding", WatchID: "watch"})
	client.receiveSessionRequest(ctx, *unwatch.Request)
	select {
	case <-owner.ended:
	case <-ctx.Done():
		t.Fatal("unwatch could not cancel pending setup at capacity")
	}
	for range 2 {
		select {
		case <-transport.writes:
		case <-ctx.Done():
			t.Fatal("missing fixed response")
		}
	}
}

type gatedSessionReplyTransport struct {
	started chan struct{}
	closed  chan struct{}
	once    sync.Once
	mu      sync.Mutex
	sends   int
	closes  int
}

type failedSessionReplyTransport struct{ *gatedSessionReplyTransport }

func (*failedSessionReplyTransport) Send(context.Context, Message) error {
	return errors.New("reply write failed")
}

func (g *gatedSessionReplyTransport) Send(ctx context.Context, _ Message) error {
	g.mu.Lock()
	g.sends++
	g.mu.Unlock()
	select {
	case g.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}
func (g *gatedSessionReplyTransport) Recv(ctx context.Context) (Message, error) {
	select {
	case <-ctx.Done():
		return Message{}, ctx.Err()
	case <-g.closed:
		return Message{}, errClientClosed
	}
}
func (g *gatedSessionReplyTransport) Close() error {
	g.once.Do(func() { close(g.closed) })
	g.mu.Lock()
	g.closes++
	g.mu.Unlock()
	return nil
}

func TestSessionToolsReplyWriteBudgetClassifiesTransmissionBoundary(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(fmt.Sprintf("bound=%t", bound), func(t *testing.T) {
			transport := &gatedSessionReplyTransport{started: make(chan struct{}, 1), closed: make(chan struct{})}
			client := NewClient(transport)
			if bound {
				client.SetSessionTools(SessionBinding{BindingID: "binding"}, nil)
				defer client.sessionTools.cancel()
			}
			frame := ResponseMessage(NewIntID(-1), EmptyResponse{})
			if err := client.acquireSendSlot(t.Context()); err != nil {
				t.Fatal(err)
			}
			err := client.sendSessionFrameWithBudgets(t.Context(), frame, 20*time.Millisecond, 20*time.Millisecond)
			client.releaseSendSlot()
			var notSent RequestNotSentError
			if !errors.As(err, &notSent) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("slot budget error = %T %v, want not-sent deadline", err, err)
			}
			transport.mu.Lock()
			sends := transport.sends
			transport.mu.Unlock()
			if sends != 0 {
				t.Fatalf("slot-expired reply reached transport %d times", sends)
			}
			err = client.sendSessionFrameWithBudgets(t.Context(), frame, time.Second, 20*time.Millisecond)
			if !errors.Is(err, context.DeadlineExceeded) || errors.As(err, &notSent) {
				t.Fatalf("after-send budget error = %T %v, want uncertain in-flight deadline", err, err)
			}
			transport.mu.Lock()
			sends = transport.sends
			transport.mu.Unlock()
			if sends != 1 {
				t.Fatalf("after-send reply reached transport %d times, want 1", sends)
			}
		})
	}
}

func TestSessionToolsBindingCancellationStopsAcceptedWrite(t *testing.T) {
	transport := &gatedSessionReplyTransport{started: make(chan struct{}, 1), closed: make(chan struct{})}
	client := NewClient(transport)
	client.SetSessionTools(SessionBinding{BindingID: "binding"}, nil)
	defer client.sessionTools.cancel()
	done := make(chan error, 1)
	go func() {
		done <- client.sendSessionFrameWithBudgets(t.Context(), ResponseMessage(NewIntID(-1), EmptyResponse{}), time.Second, time.Second)
	}()
	<-transport.started
	client.sessionTools.cancel()
	err := <-done
	var notSent RequestNotSentError
	if !errors.Is(err, context.Canceled) || errors.As(err, &notSent) {
		t.Fatalf("binding cancellation after admission = %T %v, want in-flight cancellation", err, err)
	}
}

func TestSessionToolsFailedReplyRetiresClientAndPendingCalls(t *testing.T) {
	inner := &gatedSessionReplyTransport{started: make(chan struct{}, 1), closed: make(chan struct{})}
	client := NewClient(&failedSessionReplyTransport{gatedSessionReplyTransport: inner})
	client.SetSessionTools(SessionBinding{BindingID: "binding"}, nil)
	client.pendingMu.Lock()
	pending := make(chan pendingResult, 1)
	client.pending["7"] = pendingRequest{id: NewIntID(7), ch: pending}
	client.pendingMu.Unlock()
	err := client.sendSessionFrameOrClose(t.Context(), ResponseMessage(NewIntID(-1), EmptyResponse{}))
	if err == nil || err.Error() != "reply write failed" {
		t.Fatalf("failed reply = %v", err)
	}
	select {
	case <-client.closed:
	default:
		t.Fatal("failed reply did not retire client")
	}
	if client.sessionTools.ctx.Err() == nil {
		t.Fatal("failed reply left reverse owners running")
	}
	select {
	case result := <-pending:
		if !result.local || result.msg.Error == nil {
			t.Fatalf("pending call received %+v, want local failure", result)
		}
	default:
		t.Fatal("failed reply did not fail pending call")
	}
	select {
	case <-inner.closed:
	default:
		t.Fatal("failed reply did not close transport")
	}
}

func TestSessionToolsCanceledObservationDoesNotRetireHealthyConnection(t *testing.T) {
	transport := &gatedSessionReplyTransport{started: make(chan struct{}, 1), closed: make(chan struct{})}
	client := NewClient(transport)
	client.SetSessionTools(SessionBinding{BindingID: "binding"}, nil)
	defer client.sessionTools.cancel()
	if err := client.acquireSendSlot(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := client.sendSessionFrameOrClose(ctx, NotificationMessage(NotificationRuntimeSessionObservation, struct{}{}))
	client.releaseSendSlot()
	var notSent RequestNotSentError
	if !errors.As(err, &notSent) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled observation = %T %v, want not-sent cancellation", err, err)
	}
	transport.mu.Lock()
	closes, sends := transport.closes, transport.sends
	transport.mu.Unlock()
	if closes != 0 || sends != 0 {
		t.Fatalf("healthy connection: closes=%d sends=%d, want both zero", closes, sends)
	}
}

func TestSessionToolsReplyWaitingForSlotStopsOnClientClose(t *testing.T) {
	transport := &gatedSessionReplyTransport{started: make(chan struct{}, 1), closed: make(chan struct{})}
	client := NewClient(transport)
	client.Start(t.Context())
	if err := client.acquireSendSlot(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer client.releaseSendSlot()
	done := make(chan error, 1)
	atSlot := make(chan struct{}, 1)
	go func() {
		atSlot <- struct{}{}
		done <- client.sendSessionFrameWithBudgets(t.Context(), ResponseMessage(NewIntID(-1), EmptyResponse{}), time.Second, time.Second)
	}()
	select {
	case <-atSlot:
	case <-time.After(2 * time.Second):
		t.Fatal("reply did not start while the send slot was occupied")
	}
	// The writer started the send while the slot was held and the client was
	// still open. Its result must stay blocked until the client closes.
	select {
	case err := <-done:
		t.Fatalf("reply finished before client close: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if _, ok := errors.AsType[RequestNotSentError](err); !ok {
			t.Fatalf("reply after close = %T %v, want not-sent", err, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closed connection left reply waiting for send slot")
	}
	transport.mu.Lock()
	sends := transport.sends
	transport.mu.Unlock()
	if sends != 0 {
		t.Fatalf("closed connection wrote %d replies", sends)
	}
}
