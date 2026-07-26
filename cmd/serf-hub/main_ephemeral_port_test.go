package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
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
		loadConfig:      func(string) (Config, error) { return cfg, nil },
		ensureDirs:      func() error { return nil },
		acquireLock:     func(string) (func(), error) { return func() {}, nil },
		newToken:        func() (string, error) { return "hub-token", nil },
		loadAuthToken:   func(string) (string, error) { return "auth-token", nil },
		loadCredentials: func(string) (*credentials.Store, error) { return &credentials.Store{}, nil },
		loadProviderConfig: func(string) (providercfg.Config, bool, error) {
			return providercfg.Config{}, true, nil
		},
		materializeConfig: func(string, ...llm.EnvOption) (providercfg.Config, error) {
			return providercfg.Config{}, nil
		},
		notifyContext: func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
			return ctx, func() {}
		},
		listen: func(ctx context.Context, network, addr string) (net.Listener, error) {
			var lc net.ListenConfig
			return lc.Listen(ctx, network, addr)
		},
		serve: func(_ context.Context, srv hubHTTPServer, _ hubShutdowner) error {
			gotSrv = srv
			close(served)
			// Run the real server so the test below can dial it. It's
			// released when the test cancels ctx.
			go func() { _ = srv.ListenAndServe() }()
			<-ctx.Done()
			return srv.Shutdown(context.Background())
		},
	}

	// The startup log line this test asserts on is written straight to
	// os.Stderr (not the io.Writer runMain takes), so capture the real fd.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origStderr := os.Stderr
	os.Stderr = w
	defer r.Close()
	logOut := make(chan string, 1)
	go func() {
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 4096)
		for {
			n, readErr := r.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if readErr != nil {
				break
			}
		}
		logOut <- string(buf)
	}()

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

	// The startup log line (including "listening on") is written before
	// deps.serve is called, so it's already in the pipe by now.
	os.Stderr = origStderr
	_ = w.Close()
	var captured string
	select {
	case captured = <-logOut:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out reading captured stderr")
	}

	// The log line must carry a real, non-zero port - not the literal ":0"
	// the caller asked for.
	m := regexp.MustCompile(`listening on (\S+)`).FindStringSubmatch(captured)
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
	for i := 0; i < 50; i++ {
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
