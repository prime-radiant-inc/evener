package hub

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/credentials"
)

// kata 68fm: hub ports were handed out in prose (a dispatch prompt listing
// 8953-8961), and nothing stopped two agents from picking the same one - a
// hub answering on the expected port passes every check an agent makes, so
// the collision produces a silently wrong measurement rather than an error.
//
// "-addr 127.0.0.1:0" asks the kernel for a free port instead - a port
// derived from something already unique by construction, no convention
// required. Before this fix that request was accepted but useless: cfg.Addr
// stayed the literal string "127.0.0.1:0" all the way through - into
// WebConfig.HubAddr, into the startup log line, into the advertised auth
// URL - because http.Server.ListenAndServe() binds lazily and nothing ever
// read back what it actually bound. An agent parsing the log for "the port"
// would get ":0", not a dialable address.
//
// This test proves both halves: the reported address is the real bound
// port, and that address is genuinely listening.
func TestRunMainAddrZeroReportsAndBindsTheRealPort(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))

	cfg := DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	cfg.RunDir = filepath.Join(root, "run")
	cfg.StateGlob = filepath.Join(root, "projects", "*")
	cfg.PastIndexDB = filepath.Join(root, "hub", "index.db")
	cfg.HubStateRoot = filepath.Join(root, "hub")
	cfg.PluginAutoUpgrade = false
	if err := os.MkdirAll(cfg.HubStateRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var gotSrv hubHTTPServer
	served := make(chan struct{})
	deps := mainDeps{
		loadRegistry:    hermeticRegistryLoader,
		loadConfig:      func(string, bool) (Config, error) { return cfg, nil },
		ensureDirs:      func() error { return nil },
		acquireLock:     func(string) (func(), error) { return func() {}, nil },
		newToken:        func() (string, error) { return "hub-token", nil },
		loadAuthToken:   func(string) (string, error) { return "auth-token", nil },
		loadCredentials: func(string) (*credentials.Store, error) { return &credentials.Store{}, nil },
		startLivePrefetch: func(context.Context, *hubcore.ProviderRegistry, *hubAuthController, time.Duration, func(func()), func()) {
		},
		startLaunchPrefetch: func(context.Context, *WebServer, time.Duration, func(func())) {},
		notifyContext: func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
			return ctx, func() {}
		},
		listen: func(ctx context.Context, network, addr string) (net.Listener, error) {
			var lc net.ListenConfig
			return lc.Listen(ctx, network, addr)
		},
		serve: func(_ context.Context, srv hubHTTPServer) error {
			gotSrv = srv
			close(served)
			// Run the real server so the test below can dial it. It's
			// released when the test cancels ctx.
			go func() { _ = srv.ListenAndServe() }()
			<-ctx.Done()
			return srv.Shutdown(context.Background())
		},
	}

	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- runMain(nil, &stderr, deps) }()

	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("deps.serve was never reached")
	}
	if gotSrv == nil {
		t.Fatal("deps.serve saw a nil hubHTTPServer")
	}

	// The startup banners (including "listening on") are written to stderr
	// before deps.serve is called, so they are already there once served
	// fires. Reading the buffer here is race-free not because of when we read
	// it but because nothing in this hostless test writes to stderr after the
	// banners: runMain is parked inside deps.serve, and the only background
	// holder of this writer is the sshconn logger, which has no configured
	// host to log about.
	captured := stderr.String()

	// The log line must carry a real, non-zero port - not the literal ":0"
	// the caller asked for.
	m := hubListeningLine.FindStringSubmatch(captured)
	if m == nil {
		t.Fatalf("no 'listening on <addr>' line in stderr:\n%s", captured)
	}
	reportedAddr := m[1]
	if reportedAddr == "127.0.0.1:0" || reportedAddr == ":0" {
		t.Fatalf("reported address is still the unresolved request %q, want the real bound port", reportedAddr)
	}

	// And it must be genuinely listening: a real HTTP request should reach
	// it (401 unauthenticated is fine - the point is the TCP connection and
	// the hub's own handler answered, proving the reported address is not a
	// stale or unrelated port).
	var resp *http.Response
	var reqErr error
	for range 50 {
		resp, reqErr = http.Get("http://" + reportedAddr + "/")
		if reqErr == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if reqErr != nil {
		t.Fatalf("dial reported address %s: %v", reportedAddr, reqErr)
	}
	_ = resp.Body.Close()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runMain: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runMain did not return after ctx cancel")
	}
}
