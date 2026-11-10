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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/internal/hubendpoint"
)

func TestAttachRejectsReusedPort(t *testing.T) {
	var disclosures atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			disclosures.Add(1)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	a, b := net.Pipe()
	defer a.Close() //nolint:errcheck // test cleanup
	defer b.Close() //nolint:errcheck // test cleanup
	err := proxyAppWire(t.Context(), srv.URL, "attach-sentinel", attachTestRecord(t, srv.URL), appwire.NewStreamTransport(a))
	if err == nil || disclosures.Load() != 0 {
		t.Fatalf("replacement listener: err=%v, bearer requests=%d", err, disclosures.Load())
	}
}

func TestAttachRejectsRedirect(t *testing.T) {
	var requests atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer destination.Close()
	var record hubendpoint.Record
	source := httptest.NewServer(attachTestHealth(&record, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/rpc", http.StatusTemporaryRedirect)
	})))
	record = attachTestRecord(t, source.URL)
	defer source.Close()
	a, b := net.Pipe()
	defer a.Close() //nolint:errcheck // test cleanup
	defer b.Close() //nolint:errcheck // test cleanup
	err := proxyAppWire(t.Context(), source.URL, "attach-sentinel", record, appwire.NewStreamTransport(a))
	if err == nil || requests.Load() != 0 {
		t.Fatalf("redirect: err=%v, destination requests=%d", err, requests.Load())
	}
}

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
	var record hubendpoint.Record
	httpSrv := httptest.NewServer(attachTestHealth(&record, hubedge.AuthGuard("test-token")(mux)))
	record = attachTestRecord(t, httpSrv.URL)
	defer httpSrv.Close()
	addr := httpSrv.URL

	bridgeEnd, controllerEnd := net.Pipe()
	recorder := &attachRecordingConn{ReadWriteCloser: bridgeEnd}
	stream := appwire.NewStreamTransport(recorder)

	ctx := t.Context()
	bridgeErr := make(chan error, 1)
	go func() { bridgeErr <- proxyAppWire(ctx, addr, "test-token", record, stream) }()

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
	var record hubendpoint.Record
	httpSrv := httptest.NewServer(attachTestHealth(&record, hubedge.AuthGuard("right-token")(mux)))
	record = attachTestRecord(t, httpSrv.URL)
	defer httpSrv.Close()
	addr := httpSrv.URL

	bridgeEnd, controllerEnd := net.Pipe()
	defer controllerEnd.Close() //nolint:errcheck // test cleanup
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	err := proxyAppWire(ctx, addr, "wrong-token", record, appwire.NewStreamTransport(bridgeEnd))
	if !errors.Is(err, errNoHubAt) {
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
	if !errors.Is(err, errNoHubAt) {
		t.Fatalf("attach = %v, stderr = %q; want the no-hub refusal naming %s on stderr", err, stderr.String(), addr)
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
	if stderr.Len() == 0 {
		t.Fatal("missing stderr diagnostic")
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

	for _, raw := range []string{"http://example.com:9180", "http://localhost:9180@evil.example", "http://127.0.0.1:9180@evil.example", "http://10.1.2.3:9180", "http://localhost:9180", "https://127.0.0.1:9180", ""} {
		err := runAttach([]string{"--stdio", "--hub-url", raw, "--config", cfgPath}, &stderr, deps)
		if !errors.Is(err, errNonLoopbackAddr) {
			t.Fatalf("runAttach(%q) = %v, want errNonLoopbackAddr", raw, err)
		}
	}
	if stdout.Len() != 0 {
		t.Fatalf("refusal wrote to stdout: %q", stdout.String())
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

// The record is selected before the actual authenticated socket opens. The health
// handler is the external network boundary, not a replacement for the verifier.
func attachTestRecord(t *testing.T, origin string) hubendpoint.Record {
	t.Helper()
	root := t.TempDir()
	return hubendpoint.Record{SchemaVersion: 1, BootID: "attach-boot", PID: os.Getpid(), StartedAt: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), ConfigPath: filepath.Join(root, "hub.toml"), HubStateRoot: root, RequestedListenAddr: "127.0.0.1:0", ListenSource: "config", BoundAddr: strings.TrimPrefix(origin, "http://"), LocalURL: origin, LoopbackURL: origin}
}

func TestAttachOrigin(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:9180", "http://[::1]:9180", "http://127.0.0.2:9181"} {
		if _, err := attachOrigin(raw); err != nil {
			t.Errorf("attachOrigin(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{
		"http://0.0.0.0:9180", "http://[::]:9180", "http://localhost:9180",
		"http://10.0.0.1:9180", "http://example.com:9180", "https://127.0.0.1:9180",
		"http://localhost:9180@attacker.example", "http://127.0.0.1:9180@attacker.example",
		"http://127.0.0.1:0", "http://127.0.0.1:99999", "http://127.0.0.1:notaport",
		"http://127.0.0.1:9180/extra", "http://127.0.0.1:9180?x=1", "127.0.0.1:9180",
	} {
		if _, err := attachOrigin(raw); !errors.Is(err, errNonLoopbackAddr) {
			t.Errorf("attachOrigin(%q) = %v, want errNonLoopbackAddr", raw, err)
		}
	}
}

func attachTestHealth(record *hubendpoint.Record, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			_ = json.NewEncoder(w).Encode(hubapi.HealthResponse{BootID: record.BootID, HubAddr: record.BoundAddr, StartedAt: record.StartedAt, Version: "test"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
