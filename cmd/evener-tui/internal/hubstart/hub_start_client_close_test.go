package hubstart

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/e2ecap"
)

// closeRecordingTransport is an appwire.Transport that records how many times
// its connection was closed, so a test can assert the client that StartHubClient
// dialed was released on a post-dial failure.
type closeRecordingTransport struct {
	noopAppWireTransport
	mu     sync.Mutex
	closed int
}

func (t *closeRecordingTransport) Close() error {
	t.mu.Lock()
	t.closed++
	t.mu.Unlock()
	return nil
}
func (t *closeRecordingTransport) closeCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

// recordingHubServer speaks just enough of the appwire wire protocol to answer
// one Initialize, then signals on the returned channel when the peer closes the
// connection. It is the fixture-side observation for the close: a client that
// leaks keeps the socket open and never signals.
func recordingHubServer(t *testing.T) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	closed := make(chan struct{}, 1)
	var (
		connMu sync.Mutex
		active *websocket.Conn
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		connMu.Lock()
		active = conn
		connMu.Unlock()
		defer conn.Close(websocket.StatusNormalClosure, "")
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var msg appwire.Message
		if json.Unmarshal(data, &msg) != nil || msg.Request == nil {
			return
		}
		out, _ := json.Marshal(appwire.ResponseMessage(msg.Request.ID, appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion}))
		_ = conn.Write(r.Context(), websocket.MessageText, out)
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				select {
				case closed <- struct{}{}:
				default:
				}
				return
			}
		}
	}))
	// A hijacked WebSocket connection outlives httptest.Server.Close, so close
	// it explicitly: otherwise a failed assertion that never closed the client
	// leaves the handler blocked in Read for the rest of the test binary.
	t.Cleanup(func() {
		connMu.Lock()
		conn := active
		connMu.Unlock()
		if conn != nil {
			_ = conn.Close(websocket.StatusInternalError, "test cleanup")
		}
		srv.Close()
	})
	return srv, closed
}

// TestStartHubClientClosesRealClientOnEnvironmentFailure drives the healthy-dial
// path against a real AppWire/WebSocket fixture and forces CheckHubEnvironment
// to fail. The socket must close before StartHubClient returns its error, which
// the fixture observes as its drain read failing.
func TestStartHubClientClosesRealClientOnEnvironmentFailure(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	srv, closed := recordingHubServer(t)
	addr := HubAddress{BaseURL: srv.URL}

	dials := 0
	_, err := StartHubClient(context.Background(), HubStartConfig{
		RawAddr:       srv.URL,
		HealthTimeout: time.Second,
		DialHub: func(ctx context.Context, _ HubAddress, _ *http.Client) (*appwire.Client, error) {
			dials++
			return dialHubRPC(ctx, addr, srv.Client(), nil, nil)
		},
		CheckHubEnvironment: func(context.Context, HubAddress, *http.Client, string) error {
			return StartupError{Kind: StartupErrorStaleEnvironment, Detail: "state dir mismatch"}
		},
	})
	if err == nil {
		t.Fatal("StartHubClient returned nil error despite an environment mismatch")
	}
	if dials != 1 {
		t.Fatalf("dials = %d, want 1", dials)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("hub fixture never observed the leaked client connection close")
	}
}

// TestStartHubClientClosesClientOnAutoStartedEnvironmentFailure drives the
// auto-start path: the first dials fail, a local hub is started, the redial
// succeeds, and then environment validation fails. The client returned by that
// redial must be closed before the error is returned.
func TestStartHubClientClosesClientOnAutoStartedEnvironmentFailure(t *testing.T) {
	rec := &closeRecordingTransport{}
	started := false
	_, err := StartHubClient(context.Background(), HubStartConfig{
		RawAddr:       "127.0.0.1:9180",
		HubBin:        writeTempExecutable(t),
		AutoStart:     true,
		HealthTimeout: time.Millisecond,
		DialHub: func(context.Context, HubAddress, *http.Client) (*appwire.Client, error) {
			if !started {
				return nil, errors.New("connection refused")
			}
			return appwire.NewClient(rec), nil
		},
		StartLocalHub: func(HubStartRequest) error {
			started = true
			return nil
		},
		CheckHubEnvironment: func(context.Context, HubAddress, *http.Client, string) error {
			return StartupError{Kind: StartupErrorStaleEnvironment, Detail: "state dir mismatch"}
		},
	})
	if err == nil {
		t.Fatal("StartHubClient returned nil error despite an environment mismatch")
	}
	if got := rec.closeCount(); got != 1 {
		t.Fatalf("client close count = %d, want 1", got)
	}
}
