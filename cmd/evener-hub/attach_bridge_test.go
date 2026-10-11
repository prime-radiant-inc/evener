package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	// The bridge must reach the hub through the edge the hub really serves: the
	// same /rpc route behind the same AuthGuard. Serving the WebSocket handler
	// directly would pass even if the bridge dialed the wrong path or sent a
	// wrong token, because nothing inspected either.
	var edgeMu sync.Mutex
	var authHeader string
	var bridgeHeader string
	var paths []string
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		edgeMu.Lock()
		if authHeader == "" {
			authHeader = r.Header.Get("Authorization")
		}
		if bridgeHeader == "" {
			bridgeHeader = r.Header.Get(bridgeOriginHeader)
		}
		paths = append(paths, r.URL.Path)
		edgeMu.Unlock()
		server.ServeWebSocket(w, r)
	})
	httpSrv := httptest.NewServer(hubedge.AuthGuard("test-token")(mux))
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

	edgeMu.Lock()
	gotAuth, gotBridge, gotPaths := authHeader, bridgeHeader, append([]string(nil), paths...)
	edgeMu.Unlock()
	if gotAuth != "Bearer test-token" {
		t.Fatalf("bridge authorization = %q, want %q", gotAuth, "Bearer test-token")
	}
	if gotBridge != "1" {
		t.Fatalf("bridge origin marker = %q, want %q", gotBridge, "1")
	}
	if len(gotPaths) == 0 || gotPaths[0] != "/rpc" {
		t.Fatalf("bridge dialed %v, want /rpc", gotPaths)
	}
}

// A wrong token must be refused by the real edge rather than by a test double:
// the guard is what keeps the hub's AppWire socket closed to callers that do not
// hold the capability token.
func TestAttachBridgeWrongTokenIsRefused(t *testing.T) {
	server := newHubAppServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, appsource.NewRegistry())
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", server.ServeWebSocket)
	httpSrv := httptest.NewServer(hubedge.AuthGuard("right-token")(mux))
	defer httpSrv.Close()
	addr := strings.TrimPrefix(httpSrv.URL, "http://")

	bridgeEnd, controllerEnd := net.Pipe()
	defer controllerEnd.Close() //nolint:errcheck // test cleanup
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	err := proxyAppWire(ctx, addr, "wrong-token", appwire.NewStreamTransport(bridgeEnd))
	if err == nil || !strings.Contains(err.Error(), "no hub at "+addr) {
		t.Fatalf("proxyAppWire with a wrong token = %v, want a refused dial", err)
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
	deps.loadConfig = func(string, bool) (Config, error) {
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
		{"localhost:9180", "127.0.0.1:9180"},
		{"192.168.1.5:9180", "192.168.1.5:9180"},
	} {
		if got := loopbackAddr(tc.in); got != tc.want {
			t.Errorf("loopbackAddr(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		// Names are never trusted here: loopbackAddr rewrites "localhost" to the
		// literal before this is consulted, so a resolver cannot redirect a
		// token-carrying dial.
		{"localhost", false},
		{"10.0.0.1", false},
		{"example.com", false},
		{"0.0.0.0", false},
	} {
		if got := isLoopbackHost(tc.host); got != tc.want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

// The bridge carries the hub's capability token over an unencrypted ws://
// connection, so an address that would leave the host must be refused rather
// than dialed.
func TestAttachRefusesNonLoopbackAddr(t *testing.T) {
	root := t.TempDir()
	if _, err := hubedge.LoadOrCreateAuthToken(root); err != nil {
		t.Fatalf("seed auth token: %v", err)
	}
	var stdout, stderr bytes.Buffer
	deps := defaultMainDeps()
	deps.stdin = strings.NewReader("")
	deps.stdout = &stdout
	deps.loadConfig = func(string, bool) (Config, error) {
		return Config{Addr: "10.1.2.3:9180", HubStateRoot: root}, nil
	}
	cfgPath := filepath.Join(root, "hub.toml")

	// Both the configured address and an explicit --addr must be checked.
	for _, args := range [][]string{
		{"--stdio", "--config", cfgPath},
		{"--stdio", "--addr", "example.com:9180", "--config", cfgPath},
		// The userinfo bypass: SplitHostPort reports host "localhost" here and
		// folds "@evil.example" into the port, so only validating the assembled
		// URL catches it.
		{"--stdio", "--addr", "localhost:9180@evil.example", "--config", cfgPath},
		{"--stdio", "--addr", "127.0.0.1:9180@evil.example", "--config", cfgPath},
	} {
		err := runAttach(args, &stderr, deps)
		if !errors.Is(err, errNonLoopbackAddr) {
			t.Fatalf("runAttach(%v) = %v, want errNonLoopbackAddr", args, err)
		}
	}
	if stdout.Len() != 0 {
		t.Fatalf("refusal wrote to stdout: %q", stdout.String())
	}
}

// resolveHubURL is the token's last gate before it crosses an unencrypted
// connection, so the shapes that try to move the dial off-host are tested
// directly.
func TestResolveHubURL(t *testing.T) {
	for _, tc := range []struct {
		addr string
		ok   bool
	}{
		{"127.0.0.1:9180", true},
		{"[::1]:9180", true},
		{"0.0.0.0:9180", true}, // rewritten to loopback before this is called
		{"10.0.0.1:9180", false},
		{"example.com:9180", false},
		// The userinfo bypass: "host:port@attacker" hides the real destination.
		{"localhost:9180@evil.example", false},
		{"127.0.0.1:9180@evil.example", false},
		{"127.0.0.1:0", false},
		{"127.0.0.1:99999", false},
		{"127.0.0.1:notaport", false},
		{"127.0.0.1:9180/extra", false},
		{"127.0.0.1:9180?x=1", false},
		{"127.0.0.1", false},
	} {
		got, err := resolveHubURL(loopbackAddr(tc.addr))
		if !tc.ok {
			if !errors.Is(err, errNonLoopbackAddr) {
				t.Errorf("resolveHubURL(%q) err = %v, want errNonLoopbackAddr", tc.addr, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolveHubURL(%q) = %v, want a URL", tc.addr, err)
			continue
		}
		if !strings.HasPrefix(got, "ws://") || !strings.HasSuffix(got, "/rpc") {
			t.Errorf("resolveHubURL(%q) = %q, want a ws:// URL ending in /rpc", tc.addr, got)
		}
	}
}

// A canceled context is a requested shutdown, and nothing else can end a pump
// parked on stdio (stdioStream.Close is a no-op), so pumpBoth must return nil
// promptly rather than the cancellation or a hang.
func TestPumpBothReturnsCleanlyOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	a, b := net.Pipe()
	defer a.Close() //nolint:errcheck // test cleanup
	defer b.Close() //nolint:errcheck // test cleanup

	done := make(chan error, 1)
	go func() {
		done <- pumpBoth(ctx, appwire.NewStreamTransport(a), appwire.NewStreamTransport(b))
	}()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("pumpBoth after cancel = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pumpBoth did not return after cancellation")
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
