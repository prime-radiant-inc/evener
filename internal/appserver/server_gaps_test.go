package appserver

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"primeradiant.com/evener/appwire"
)

// TestNotifyWithConnection covers the Notify function's happy path where a
// connection is present in the context.
func TestNotifyWithConnection(t *testing.T) {
	server := NewServer(ServerConfig{})
	conn := server.NewConnection("conn-notify")
	server.registerConnection(conn)
	ctx := context.WithValue(context.Background(), connectionContextKey{}, conn)
	Notify(ctx, "test/method", map[string]string{"key": "value"})
	// The notification should be in the send channel.
	select {
	case msg := <-conn.send:
		if msg.Notification == nil {
			t.Fatal("notification should not be nil")
		}
		if msg.Notification.Method != "test/method" {
			t.Fatalf("method = %q, want test/method", msg.Notification.Method)
		}
	default:
		t.Fatal("notification should have been enqueued")
	}
}

// TestNotifyWithoutConnection covers the path where no connection is in the context.
func TestNotifyWithoutConnection(t *testing.T) {
	// Should not panic.
	Notify(context.Background(), "test/method", nil)
}

// TestNotifyWithNilConnection covers the path where the connection value is nil.
func TestNotifyWithNilConnection(t *testing.T) {
	ctx := context.WithValue(context.Background(), connectionContextKey{}, (*Connection)(nil))
	Notify(ctx, "test/method", nil)
}

// TestNotifyWithEmptyMethod covers the path where method is empty.
func TestNotifyWithEmptyMethod(t *testing.T) {
	server := NewServer(ServerConfig{})
	conn := server.NewConnection("conn-empty-method")
	server.registerConnection(conn)
	ctx := context.WithValue(context.Background(), connectionContextKey{}, conn)
	Notify(ctx, "", nil)
	// Nothing should be enqueued.
	select {
	case <-conn.send:
		t.Fatal("nothing should have been enqueued with empty method")
	default:
	}
}

// TestNotifyWithNonConnectionValue covers the path where the context value is
// not a *Connection.
func TestNotifyWithNonConnectionValue(t *testing.T) {
	ctx := context.WithValue(context.Background(), connectionContextKey{}, "not a connection")
	Notify(ctx, "test/method", nil)
}

// TestSetBeforeSubscriptionGate covers the SetBeforeSubscriptionGate function.
func TestSetBeforeSubscriptionGate(t *testing.T) {
	server := NewServer(ServerConfig{})
	called := false
	server.SetBeforeSubscriptionGate(func() {
		called = true
	})
	conn := server.NewConnection("conn-gate")
	server.registerConnection(conn)
	ctx := context.WithValue(context.Background(), connectionContextKey{}, conn)
	if ok := Subscribe(ctx, "thread-1"); !ok {
		t.Fatal("Subscribe should succeed")
	}
	if !called {
		t.Fatal("beforeSubscriptionRegistration should have been called")
	}
}

// TestRouterMethods covers the Methods function on Router.
func TestRouterMethods(t *testing.T) {
	router := NewRouter()
	HandleTyped(router, appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{}, nil
	})
	HandleTyped(router, appwire.MethodInitialize, func(_ context.Context, _ appwire.InitializeParams) (appwire.InitializeResponse, error) {
		return appwire.InitializeResponse{}, nil
	})
	methods := router.Methods()
	if len(methods) < 2 {
		t.Fatalf("Methods returned %d, want at least 2", len(methods))
	}
	// Should contain MethodThreadList.
	if !slices.Contains(methods, appwire.MethodThreadList) {
		t.Fatalf("Methods should contain %q: %v", appwire.MethodThreadList, methods)
	}
}

// TestRouterMethodsEmpty covers Methods on a router with no handlers.
func TestRouterMethodsEmpty(t *testing.T) {
	router := NewRouter()
	methods := router.Methods()
	if len(methods) != 0 {
		t.Fatalf("Methods on empty router should return 0, got %d", len(methods))
	}
}

// TestRouterDispatchMethodNotFound covers the MethodNotFound error path.
func TestRouterDispatchMethodNotFound(t *testing.T) {
	router := NewRouter()
	_, err := router.Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: "nonexistent/method",
	})
	if err == nil {
		t.Fatal("Dispatch with unknown method should error")
	}
}

// TestRouterDispatchNilResponse covers the nil-response path in Dispatch.
func TestRouterDispatchNilResponse(t *testing.T) {
	router := NewRouter()
	router.Handle("test/null", func(_ context.Context, _ json.RawMessage) (any, error) {
		return nil, nil
	})
	resp, err := router.Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: "test/null",
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if _, ok := resp.(appwire.EmptyResponse); !ok {
		t.Fatalf("response type = %T, want EmptyResponse", resp)
	}
}

// TestRouterDispatchError covers the handler-error path in Dispatch.
func TestRouterDispatchError(t *testing.T) {
	router := NewRouter()
	router.Handle("test/err", func(_ context.Context, _ json.RawMessage) (any, error) {
		return nil, appwire.InvalidRequest("bad request")
	})
	_, err := router.Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: "test/err",
	})
	if err == nil {
		t.Fatal("Dispatch with erroring handler should return error")
	}
}

// TestWireErrorWithWireError covers the errors.As path in WireError.
func TestWireErrorWithWireError(t *testing.T) {
	orig := appwire.InvalidRequest("bad")
	result := WireError(orig)
	if result.Message != "bad" {
		t.Fatalf("WireError message = %q, want 'bad'", result.Message)
	}
}

// TestWireErrorWithGenericError covers the InternalError path in WireError.
func TestWireErrorWithGenericError(t *testing.T) {
	result := WireError(errGeneric)
	if result.Code != appwire.CodeInternalError {
		t.Fatalf("WireError code = %d, want %d", result.Code, appwire.CodeInternalError)
	}
}

var errGeneric = newGenericError("something went wrong")

type genericError struct{ msg string }

func (e *genericError) Error() string { return e.msg }

func newGenericError(msg string) error { return &genericError{msg: msg} }

// TestSubscribeNoConnection covers the path where no connection is in the context.
func TestSubscribeNoConnection(t *testing.T) {
	if ok := Subscribe(context.Background(), "thread-1"); !ok {
		t.Fatal("Subscribe with no connection should return true")
	}
}

// TestSubscribeEmptyThreadID covers the path where threadID is empty.
func TestSubscribeEmptyThreadID(t *testing.T) {
	server := NewServer(ServerConfig{})
	conn := server.NewConnection("conn-sub-empty")
	server.registerConnection(conn)
	ctx := context.WithValue(context.Background(), connectionContextKey{}, conn)
	if ok := Subscribe(ctx, ""); ok {
		t.Fatal("Subscribe with empty threadID should return false")
	}
}

// TestSubscribeReplacedConnection covers the path where the connection has been
// replaced (server.conns[conn.id] != conn).
func TestSubscribeReplacedConnection(t *testing.T) {
	server := NewServer(ServerConfig{})
	conn := server.NewConnection("conn-replaced")
	server.registerConnection(conn)
	// Replace the connection with a new one.
	newConn := server.NewConnection("conn-replaced")
	server.registerConnection(newConn)
	ctx := context.WithValue(context.Background(), connectionContextKey{}, conn)
	if ok := Subscribe(ctx, "thread-1"); ok {
		t.Fatal("Subscribe with replaced connection should return false")
	}
}

// TestReplaceSubscriptionsNoConnection covers the path where no connection is
// in the context for ReplaceSubscriptions.
func TestReplaceSubscriptionsNoConnection(t *testing.T) {
	if ok := ReplaceSubscriptions(context.Background(), "thread-1"); !ok {
		t.Fatal("ReplaceSubscriptions with no connection should return true")
	}
}

// TestNewNotifierZeroLimit covers the limit<=0 default path in NewNotifier.
func TestNewNotifierZeroLimit(t *testing.T) {
	n := NewNotifier(0)
	if n.limit != 1000 {
		t.Fatalf("limit = %d, want 1000", n.limit)
	}
}

// TestNewNotifierNegativeLimit covers the limit<0 default path.
func TestNewNotifierNegativeLimit(t *testing.T) {
	n := NewNotifier(-5)
	if n.limit != 1000 {
		t.Fatalf("limit = %d, want 1000", n.limit)
	}
}

// TestRetainedWindowEmptyHistoryWithSeq covers the branch in RetainedWindow
// where history is empty but nextSeq > 0. This happens when limit=0 (but
// NewNotifier clamps to 1000) — actually it's hard to get an empty history
// with nextSeq > 0 because Record always appends. The only way is if
// history is trimmed to 0, which doesn't happen (limit is at least 1 after
// clamping). So this branch is for the case where the notifier was
// constructed but never had Record called — but then nextSeq is 0. The
// `n.nextSeq > 0` branch with empty history is unreachable in practice
// because Record always appends at least one entry before trimming.
func TestRetainedWindowEmptyHistoryWithSeqUnreachable(t *testing.T) {
	t.Skip("the nextSeq > 0 with empty history branch in RetainedWindow is unreachable: Record always appends before trimming")
}

// TestRetainedWindowNoHistoryNoSeq covers the branch where history is empty
// and nextSeq is 0.
func TestRetainedWindowNoHistoryNoSeq(t *testing.T) {
	n := NewNotifier(10)
	window := n.RetainedWindow("")
	if window.LowerSeq != 0 {
		t.Fatalf("LowerSeq = %d, want 0", window.LowerSeq)
	}
	if window.UpperSeq != 0 {
		t.Fatalf("UpperSeq = %d, want 0", window.UpperSeq)
	}
}
