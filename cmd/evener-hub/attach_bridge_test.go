package hub

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
)

// TestAttachBridgeRoundTripsInitializeAndThreadList drives the stdio bridge
// against an in-process hub over an in-memory pipe pair, the way a controller
// speaking AppWire over an SSH channel would. It asserts the handshake and a
// real list call round-trip, and that every byte the bridge wrote to its
// stdout was a framed AppWire Message.
func TestAttachBridgeRoundTripsInitializeAndThreadList(t *testing.T) {
	server := newHubAppServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, appsource.NewRegistry())
	// Capture the upgrade request's Authorization header: the bridge must send
	// the hub's bearer token. Without this the round trip passes even if the
	// header is dropped or malformed, because the test handler behind the
	// AuthGuard never inspects it.
	var authMu sync.Mutex
	var authHeader string
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authMu.Lock()
		if authHeader == "" {
			authHeader = r.Header.Get("Authorization")
		}
		authMu.Unlock()
		server.ServeWebSocket(w, r)
	}))
	defer httpSrv.Close()
	addr := strings.TrimPrefix(httpSrv.URL, "http://")

	bridgeEnd, controllerEnd := net.Pipe()
	recorder := &attachRecordingConn{ReadWriteCloser: bridgeEnd}
	stream := appwire.NewStreamTransport(recorder)

	ctx := t.Context()
	bridgeErr := make(chan error, 1)
	go func() { bridgeErr <- proxyAppWire(ctx, addr, "test-token", stream) }()

	client := appwire.NewClient(appwire.NewStreamTransport(controllerEnd))
	client.Start(ctx)

	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		_ = client.Close()
		t.Fatalf("initialize over bridge: %v", err)
	}
	if _, err := client.ThreadList(ctx, appwire.ThreadListParams{}); err != nil {
		_ = client.Close()
		t.Fatalf("thread/list over bridge: %v", err)
	}

	// Closing the controller end closes stdin on the bridge, which must exit
	// cleanly (exit 0 equivalent).
	if err := client.Close(); err != nil {
		t.Fatalf("close controller: %v", err)
	}
	select {
	case err := <-bridgeErr:
		if err != nil {
			t.Fatalf("bridge exited on clean close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bridge did not exit after the controller closed")
	}

	recorder.mu.Lock()
	stdout := recorder.buf.String()
	recorder.mu.Unlock()
	assertOnlyAppWireFrames(t, stdout)

	authMu.Lock()
	gotAuth := authHeader
	authMu.Unlock()
	if gotAuth != "Bearer test-token" {
		t.Fatalf("bridge authorization = %q, want %q", gotAuth, "Bearer test-token")
	}
}

// TestAttachBridgeErrorWritesNothingToStdout pins the stdout-discipline hard
// rule: when the bridge cannot reach a hub it reports to stderr and exits
// nonzero, and stdout stays byte-for-byte empty so a controller's stream is
// never corrupted by a diagnostic.
func TestAttachBridgeErrorWritesNothingToStdout(t *testing.T) {
	root := t.TempDir()
	if _, err := hubedge.LoadOrCreateAuthToken(root); err != nil {
		t.Fatalf("seed auth token: %v", err)
	}
	// A live listener that answers the upgrade with a plain HTTP error. The dial
	// then fails deterministically, without the bind-then-release race where
	// another process could claim the freed port.
	deadSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not a websocket endpoint", http.StatusInternalServerError)
	}))
	defer deadSrv.Close()
	addr := strings.TrimPrefix(deadSrv.URL, "http://")

	var stdout, stderr bytes.Buffer
	deps := defaultMainDeps()
	deps.stdin = strings.NewReader("")
	deps.stdout = &stdout
	deps.loadConfig = func(string) (Config, error) {
		return Config{Addr: addr, HubStateRoot: root}, nil
	}

	err := runAttach([]string{"--stdio", "--config", filepath.Join(root, "hub.toml")}, &stderr, deps)
	if err == nil {
		t.Fatal("attach to a hub-less address succeeded")
	}
	if stdout.Len() != 0 {
		t.Fatalf("error path wrote to stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "no hub at "+addr) {
		t.Fatalf("stderr missing no-hub diagnostic: %q", stderr.String())
	}
}

// TestAttachSubcommandDispatch proves the `attach` args reach the attach
// handler before normal hub flag parsing, and that rejecting a missing --stdio
// still keeps stdout clean.
func TestAttachSubcommandDispatch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"attach"}, strings.NewReader(""), &stdout, &stderr)
	if code == 0 {
		t.Fatal("attach without --stdio exited 0")
	}
	if stdout.Len() != 0 {
		t.Fatalf("attach usage error wrote to stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "--stdio") {
		t.Fatalf("stderr missing --stdio diagnostic: %q", stderr.String())
	}
}

func TestLoopbackAddrRewritesWildcardBinds(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"127.0.0.1:9180", "127.0.0.1:9180"},
		{"0.0.0.0:9180", "127.0.0.1:9180"},
		{"[::]:9180", "[::1]:9180"},
		{"192.168.1.5:9180", "192.168.1.5:9180"},
	} {
		if got := loopbackAddr(tc.in); got != tc.want {
			t.Errorf("loopbackAddr(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// attachRecordingConn records the bytes the bridge writes (its stdout) while
// forwarding them to the peer end of the in-memory pipe.
type attachRecordingConn struct {
	io.ReadWriteCloser
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *attachRecordingConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.buf.Write(p)
	c.mu.Unlock()
	return c.ReadWriteCloser.Write(p)
}

func assertOnlyAppWireFrames(t *testing.T, out string) {
	t.Helper()
	if strings.TrimSpace(out) == "" {
		t.Fatal("bridge wrote no stdout frames")
	}
	if !strings.HasSuffix(out, "\n") {
		t.Fatal("bridge stdout did not end a frame with a newline")
	}
	for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		var msg appwire.Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("stdout line %d is not an AppWire Message: %v (%q)", i, err, line)
		}
		if msg.Kind() == appwire.MessageInvalid {
			t.Fatalf("stdout line %d is an invalid AppWire Message: %q", i, line)
		}
	}
}
