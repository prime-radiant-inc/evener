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
	"primeradiant.com/evener/cmd/evener-hub/internal/hostlock"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/internal/hubendpoint"
)

func TestHubRestartCommandWaitsForNewIncarnation(t *testing.T) {
	setBuild(t, "abc1234", "release")
	t.Cleanup(func() { hubUpdateMu = sync.Mutex{} })

	previousSupported := hubRestartSupported
	hubRestartSupported = func() bool { return true }
	t.Cleanup(func() { hubRestartSupported = previousSupported })

	previousPath := hubLaunchEntrypoint
	hubLaunchEntrypoint = writeProbeScript(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	t.Cleanup(func() { hubLaunchEntrypoint = previousPath })
	previousErr := errHubLaunchEntrypoint
	errHubLaunchEntrypoint = nil
	t.Cleanup(func() { errHubLaunchEntrypoint = previousErr })

	_, root := endpointTestRoot(t)
	token, err := hubedge.LoadOrCreateAuthToken(root)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	started := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	previousStarted := hubProcessStartedAt
	hubProcessStartedAt = started
	t.Cleanup(func() { hubProcessStartedAt = previousStarted })
	scheduled := false
	var scheduleErr error
	previousSchedule := scheduleHubRestartOnly
	scheduleHubRestartOnly = func(context.Context, restartPin, string, []string) {
		mu.Lock()
		started = started.Add(time.Second)
		scheduleErr = republishRestartFixture(t, root, started)
		scheduled = true
		mu.Unlock()
	}
	t.Cleanup(func() { scheduleHubRestartOnly = previousSchedule })

	mux := http.NewServeMux()
	appServer := newHubAppServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, appsource.NewRegistry())
	mux.HandleFunc("/rpc", appServer.ServeWebSocket)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		at := started
		mu.Unlock()
		restartFixtureHealth(t, w, r, at)
	})
	srv := httptest.NewServer(hubedge.AuthGuard(token)(mux))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	publishRestartFixture(t, root, addr, started)

	var stdout, stderr bytes.Buffer
	deps := defaultMainDeps()
	deps.loadConfig = func(string, bool) (Config, error) {
		return Config{Addr: "0.0.0.0:0", HubStateRoot: root}, nil
	}
	deps.stdout = &stdout

	if err := runHubRestart([]string{"--timeout", "5s"}, &stderr, deps); err != nil {
		t.Fatalf("runHubRestart: %v (stderr %q)", err, stderr.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if scheduleErr != nil {
		t.Fatal(scheduleErr)
	}
	if !scheduled {
		t.Fatal("restart was not sent to the hub")
	}
	if !strings.Contains(stdout.String(), "abc1234") {
		t.Fatalf("stdout = %q, want the target build", stdout.String())
	}
}

func TestHubRestartCommandUsesResponseIncarnation(t *testing.T) {
	setBuild(t, "abc1234", "release")
	t.Cleanup(func() { hubUpdateMu = sync.Mutex{} })

	previousSupported := hubRestartSupported
	hubRestartSupported = func() bool { return true }
	t.Cleanup(func() { hubRestartSupported = previousSupported })

	previousPath := hubLaunchEntrypoint
	hubLaunchEntrypoint = writeProbeScript(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	t.Cleanup(func() { hubLaunchEntrypoint = previousPath })
	previousErr := errHubLaunchEntrypoint
	errHubLaunchEntrypoint = nil
	t.Cleanup(func() { errHubLaunchEntrypoint = previousErr })

	_, root := endpointTestRoot(t)
	token, err := hubedge.LoadOrCreateAuthToken(root)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	healthStarted := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	responseStarted := healthStarted.Add(time.Second)
	previousStarted := hubProcessStartedAt
	hubProcessStartedAt = responseStarted
	t.Cleanup(func() { hubProcessStartedAt = previousStarted })

	mux := http.NewServeMux()
	appServer := newHubAppServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, appsource.NewRegistry())
	mux.HandleFunc("/rpc", appServer.ServeWebSocket)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		at := healthStarted
		mu.Unlock()
		restartFixtureHealth(t, w, r, at)
	})
	srv := httptest.NewServer(hubedge.AuthGuard(token)(mux))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	publishRestartFixture(t, root, addr, healthStarted)

	var scheduleErr error
	previousSchedule := scheduleHubRestartOnly
	scheduleHubRestartOnly = func(context.Context, restartPin, string, []string) {
		// Process B accepted the request but never restarted. Its health
		// answer is B, not the A the CLI read before sending.
		mu.Lock()
		healthStarted = responseStarted
		scheduleErr = republishRestartFixture(t, root, healthStarted)
		mu.Unlock()
	}
	t.Cleanup(func() { scheduleHubRestartOnly = previousSchedule })

	deps := defaultMainDeps()
	deps.loadConfig = func(string, bool) (Config, error) {
		return Config{Addr: addr, HubStateRoot: root}, nil
	}
	deps.stdout = io.Discard
	var stderr bytes.Buffer
	if err := runHubRestart([]string{"--addr", addr, "--timeout", "300ms"}, &stderr, deps); err == nil {
		t.Fatal("runHubRestart confirmed a process that never restarted")
	}
	mu.Lock()
	defer mu.Unlock()
	if scheduleErr != nil {
		t.Fatal(scheduleErr)
	}
	if stderr.Len() == 0 {
		t.Fatal("restart confirmation failure wrote no diagnostic")
	}
}

func TestHubRestartCommandRediscoversSuccessor(t *testing.T) {
	for _, name := range []string{"listener-moved", "listener-moved-lost-reply", "hardlink-listener-moved", "hardlink-listener-moved-lost-reply", "equal-content-record-config", "replaced-selected-config", "relative-config-listener-moved", "symlink-config-listener-moved", "relative-symlink-config-listener-moved", "failed-restart", "wrong-record-config", "changed-selected-root", "changed-selected-config", "same-incarnation"} {
		t.Run(name, func(t *testing.T) {
			dir, root := endpointTestRoot(t)
			config := endpointTestConfig(t, dir, root, "127.0.0.1:0")
			recordConfig := config
			cliConfig := config
			if strings.HasPrefix(name, "hardlink-") {
				recordConfig, cliConfig = filepath.Join(dir, "a.toml"), filepath.Join(dir, "b.toml")
				for _, alias := range []string{recordConfig, cliConfig} {
					if err := os.Link(config, alias); err != nil {
						t.Fatal(err)
					}
				}
				recordConfig = restartStartupConfigPath(t, recordConfig)
			}
			if strings.Contains(name, "config-listener-moved") {
				selector := "./hub.toml"
				if strings.Contains(name, "symlink-config") {
					selector = filepath.Join(dir, "hub-link.toml")
					if err := os.Symlink(config, selector); err != nil {
						t.Fatal(err)
					}
					if strings.HasPrefix(name, "relative-") {
						selector = "./hub-link.toml"
					}
				}
				t.Chdir(dir)
				recordConfig = restartStartupConfigPath(t, selector)
				// The CLI must not resolve the owner's relative selector in its
				// own cwd, where the same basename names a different config.
				clientDir := t.TempDir()
				endpointTestConfig(t, clientDir, t.TempDir(), "127.0.0.1:1")
				t.Chdir(clientDir)
			}
			setBuild(t, "abc1234", "release")
			t.Cleanup(func() { hubUpdateMu = sync.Mutex{} })
			previousSupported := hubRestartSupported
			hubRestartSupported = func() bool { return true }
			t.Cleanup(func() { hubRestartSupported = previousSupported })
			previousPath, previousErr := hubLaunchEntrypoint, errHubLaunchEntrypoint
			hubLaunchEntrypoint, errHubLaunchEntrypoint = writeProbeScript(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'"), nil
			t.Cleanup(func() { hubLaunchEntrypoint, errHubLaunchEntrypoint = previousPath, previousErr })
			before := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
			previousStarted := hubProcessStartedAt
			hubProcessStartedAt = before
			t.Cleanup(func() { hubProcessStartedAt = previousStarted })
			after := before.Add(time.Second)
			if name == "same-incarnation" {
				after = before
			}
			token, err := hubedge.LoadOrCreateAuthToken(root)
			if err != nil {
				t.Fatal(err)
			}
			var successorHealth, attempts atomic.Int32
			var scheduled atomic.Bool
			successor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/health" || r.Header.Get("Authorization") != "" {
					t.Error("successor confirmation sent a credential or non-health request")
				}
				successorHealth.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"boot_id": "restart-successor", "hub_addr": r.Host, "started_at": after})
			}))
			defer successor.Close()
			newAddr := strings.TrimPrefix(successor.URL, "http://")
			mux := http.NewServeMux()
			appServer := newHubAppServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, appsource.NewRegistry())
			mux.HandleFunc("/rpc", appServer.ServeWebSocket)
			mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
				at := before
				if scheduled.Load() && (name == "wrong-record-config" || strings.HasPrefix(name, "changed-selected-")) {
					// A new timestamp at the old URL alone cannot prove selection.
					at = after
				}
				restartFixtureHealth(t, w, r, at)
			})
			rpcConn := make(chan net.Conn, 1)
			predecessor := httptest.NewUnstartedServer(hubedge.AuthGuard(token)(mux))
			predecessor.Config.ConnState = func(conn net.Conn, state http.ConnState) {
				if state == http.StateHijacked {
					rpcConn <- conn
				}
			}
			predecessor.Start()
			defer predecessor.Close()
			oldAddr := strings.TrimPrefix(predecessor.URL, "http://")
			endpointTestConfig(t, dir, root, oldAddr)
			release, err := hostlock.AcquireLock(filepath.Join(root, "hub.lock"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(release)
			record := hubendpoint.Record{SchemaVersion: hubendpoint.SchemaVersion, BootID: "restart-fixture", PID: os.Getpid(), StartedAt: before, ConfigPath: recordConfig, HubStateRoot: root, RequestedListenAddr: oldAddr, ListenSource: "config", BoundAddr: oldAddr, LocalURL: predecessor.URL, LoopbackURL: predecessor.URL}
			remove, err := hubendpoint.Publish(root, record)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := remove(); err != nil {
					t.Error(err)
				}
			})
			// The running process retains its fixed listener while TOML moves.
			endpointTestConfig(t, dir, root, newAddr)
			var mu sync.Mutex
			var scheduleErr error
			previousSchedule := scheduleHubRestartOnly
			scheduleHubRestartOnly = func(context.Context, restartPin, string, []string) {
				mu.Lock()
				defer mu.Unlock()
				attempts.Add(1)
				if name == "failed-restart" {
					return
				}
				next := record
				next.BootID, next.StartedAt = "restart-successor", after
				next.RequestedListenAddr, next.BoundAddr = newAddr, newAddr
				next.LocalURL, next.LoopbackURL = successor.URL, successor.URL
				if name == "wrong-record-config" {
					next.ConfigPath = filepath.Join(dir, "unrelated.toml")
				}
				if name == "equal-content-record-config" || name == "replaced-selected-config" {
					var data []byte
					data, scheduleErr = os.ReadFile(config)
					if scheduleErr != nil {
						return
					}
					other := filepath.Join(dir, "equal-content.toml")
					scheduleErr = os.WriteFile(other, data, 0600)
					if scheduleErr != nil {
						return
					}
					if name == "replaced-selected-config" {
						scheduleErr = os.Rename(other, config)
						if scheduleErr != nil {
							return
						}
					} else {
						next.ConfigPath = other
					}
				}
				if name == "changed-selected-root" {
					next.HubStateRoot = filepath.Join(dir, "other-root")
					scheduleErr = os.MkdirAll(next.HubStateRoot, 0700)
					if scheduleErr != nil {
						return
					}
					var releaseNext func()
					releaseNext, scheduleErr = hostlock.AcquireLock(filepath.Join(next.HubStateRoot, "hub.lock"))
					if scheduleErr != nil {
						return
					}
					t.Cleanup(releaseNext)
					endpointTestConfig(t, dir, next.HubStateRoot, newAddr)
				}
				if name == "changed-selected-config" {
					other := filepath.Join(dir, "other-config")
					scheduleErr = os.MkdirAll(other, 0700)
					if scheduleErr != nil {
						return
					}
					next.ConfigPath = endpointTestConfig(t, other, root, newAddr)
					scheduleErr = os.Remove(config)
					if scheduleErr != nil {
						return
					}
					scheduleErr = os.Symlink(next.ConfigPath, config)
					if scheduleErr != nil {
						return
					}
				}
				var retire func() error
				retire, scheduleErr = hubendpoint.Publish(next.HubStateRoot, next)
				if scheduleErr != nil {
					return
				}
				t.Cleanup(func() {
					if err := retire(); err != nil {
						t.Error(err)
					}
				})
				scheduled.Store(true)
				if strings.HasSuffix(name, "-lost-reply") {
					select {
					case conn := <-rpcConn:
						scheduleErr = conn.Close()
					default:
						scheduleErr = errors.New("restart RPC connection unavailable")
					}
				}
			}
			t.Cleanup(func() { scheduleHubRestartOnly = previousSchedule })
			deps := defaultMainDeps()
			var stdout, stderr bytes.Buffer
			deps.stdout = &stdout
			err = runHubRestart([]string{"--config", cliConfig, "--timeout", "1s"}, &stderr, deps)
			mu.Lock()
			defer mu.Unlock()
			if scheduleErr != nil || attempts.Load() != 1 {
				t.Fatalf("restart scheduling: attempts=%d err=%v", attempts.Load(), scheduleErr)
			}
			switch name {
			case "listener-moved", "listener-moved-lost-reply", "hardlink-listener-moved", "hardlink-listener-moved-lost-reply", "relative-config-listener-moved", "symlink-config-listener-moved", "relative-symlink-config-listener-moved":
				if err != nil || successorHealth.Load() == 0 {
					t.Fatalf("moved successor not confirmed: err=%v health=%d stderr=%q", err, successorHealth.Load(), stderr.String())
				}
			case "failed-restart", "same-incarnation":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("unchanged incarnation confirmed: %v", err)
				}
			default:
				if !errors.Is(err, hubendpoint.ErrIdentityMismatch) {
					t.Fatalf("different selected identity confirmed: %v", err)
				}
			}
		})
	}
}

// Read the real startup publication rather than duplicating its config-path
// policy in the restart fixture. Serving/prefetches are hermetic test seams.
func restartStartupConfigPath(t *testing.T, selector string) string {
	t.Helper()
	_, cfg, deps := newTraceMainTestDeps(t)
	var record hubendpoint.Record
	deps.serve = func(context.Context, hubHTTPServer) error {
		var err error
		record, err = hubendpoint.Read(cfg.HubStateRoot)
		return err
	}
	var stderr bytes.Buffer
	if err := runMain([]string{"--config", selector, "--evener", "/bin/evener"}, &stderr, deps); err != nil {
		t.Fatalf("startup publication: %v stderr=%q", err, stderr.String())
	}
	if record.ConfigPath == "" {
		t.Fatal("startup did not publish a config path")
	}
	return record.ConfigPath
}

func TestHubRestartSubcommandDispatch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"restart", "--not-a-flag"}, strings.NewReader(""), &stdout, &stderr); code == 0 {
		t.Fatal("restart with an unknown flag exited 0")
	}
	if stdout.Len() != 0 {
		t.Fatalf("usage error wrote to stdout: %q", stdout.String())
	}
}

func TestHubRestartCommandReportsRefusal(t *testing.T) {
	setBuild(t, "abc1234", "release")
	t.Cleanup(func() { hubUpdateMu = sync.Mutex{} })

	previousSupported := hubRestartSupported
	hubRestartSupported = func() bool { return false }
	t.Cleanup(func() { hubRestartSupported = previousSupported })

	previousPath := hubLaunchEntrypoint
	hubLaunchEntrypoint = writeProbeScript(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	t.Cleanup(func() { hubLaunchEntrypoint = previousPath })
	previousErr := errHubLaunchEntrypoint
	errHubLaunchEntrypoint = nil
	t.Cleanup(func() { errHubLaunchEntrypoint = previousErr })

	_, root := endpointTestRoot(t)
	token, err := hubedge.LoadOrCreateAuthToken(root)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	appServer := newHubAppServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, appsource.NewRegistry())
	mux.HandleFunc("/rpc", appServer.ServeWebSocket)
	started := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		restartFixtureHealth(t, w, r, started)
	})
	srv := httptest.NewServer(hubedge.AuthGuard(token)(mux))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	publishRestartFixture(t, root, addr, started)

	deps := defaultMainDeps()
	deps.loadConfig = func(string, bool) (Config, error) {
		return Config{Addr: addr, HubStateRoot: root}, nil
	}
	deps.stdout = io.Discard
	var stderr bytes.Buffer
	if err := runHubRestart([]string{"--addr", addr, "--timeout", "2s"}, &stderr, deps); err == nil {
		t.Fatal("runHubRestart accepted a refused restart")
	}
}

func TestWaitForNewIncarnationTimesOut(t *testing.T) {
	_, root := endpointTestRoot(t)
	fixed := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		restartFixtureHealth(t, w, r, fixed)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	publishRestartFixture(t, root, addr, fixed)
	deps := defaultMainDeps()
	deps.loadConfig = func(string, bool) (Config, error) {
		return Config{Addr: addr, HubStateRoot: root}, nil
	}
	opts := endpointOptions{configPath: DefaultConfigPath()}
	selected, err := resolveHubEndpoint(t.Context(), opts, deps)
	if err != nil {
		t.Fatal(err)
	}
	configIdentity, err := os.Stat(selected.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := waitForNewIncarnation(ctx, opts, deps, selected, configIdentity, fixed); err == nil {
		t.Fatal("waitForNewIncarnation accepted an unchanged process")
	}
}

func TestIsAmbiguousRestartFailure(t *testing.T) {
	if !isAmbiguousRestartFailure(appwire.TransportFailureError{}) {
		t.Fatal("transport failure was treated as a refusal")
	}
	if isAmbiguousRestartFailure(appwire.WireError{}) {
		t.Fatal("server refusal was treated as ambiguous")
	}
	if isAmbiguousRestartFailure(appwire.RequestNotSentError{Err: errors.New("context canceled")}) {
		t.Fatal("a request that never left was treated as ambiguous")
	}
	if !isAmbiguousRestartFailure(errors.New("write: broken pipe")) {
		t.Fatal("a raw transport send failure was treated as final")
	}
}

func TestHubRestartCommandRefusesNonLoopbackBeforeSendingToken(t *testing.T) {
	root := t.TempDir()
	if _, err := hubedge.LoadOrCreateAuthToken(root); err != nil {
		t.Fatal(err)
	}
	deps := defaultMainDeps()
	deps.loadConfig = func(string, bool) (Config, error) {
		t.Fatal("non-loopback address reached endpoint discovery")
		return Config{Addr: "192.168.1.5:9180", HubStateRoot: root}, nil
	}
	deps.stdout = io.Discard
	var stderr bytes.Buffer
	err := runHubRestart([]string{"--addr", "192.168.1.5:9180", "--timeout", "1s"}, &stderr, deps)
	if !errors.Is(err, errNonLoopbackAddr) {
		t.Fatalf("runHubRestart error = %v, want %v", err, errNonLoopbackAddr)
	}
}

func TestHubRestartCommandRefusesUserinfoAddress(t *testing.T) {
	root := t.TempDir()
	if _, err := hubedge.LoadOrCreateAuthToken(root); err != nil {
		t.Fatal(err)
	}
	deps := defaultMainDeps()
	deps.loadConfig = func(string, bool) (Config, error) {
		t.Fatal("userinfo address reached endpoint discovery")
		return Config{Addr: "127.0.0.1:9180@evil.example", HubStateRoot: root}, nil
	}
	deps.stdout = io.Discard
	var stderr bytes.Buffer
	err := runHubRestart([]string{"--addr", "127.0.0.1:9180@evil.example", "--timeout", "1s"}, &stderr, deps)
	if !errors.Is(err, errNonLoopbackAddr) {
		t.Fatalf("runHubRestart error = %v, want %v", err, errNonLoopbackAddr)
	}
}

func TestHubRestartCommandRefusesDifferentEndpointBeforeTokenRead(t *testing.T) {
	_, root := endpointTestRoot(t) // No token exists, so reading it would mask the refusal.
	started := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			t.Error("mismatched endpoint received a control request")
		}
		restartFixtureHealth(t, w, r, started)
	}))
	defer srv.Close()
	publishRestartFixture(t, root, strings.TrimPrefix(srv.URL, "http://"), started)
	deps := defaultMainDeps()
	deps.loadConfig = func(string, bool) (Config, error) {
		return Config{Addr: "0.0.0.0:0", HubStateRoot: root}, nil
	}
	err := runHubRestart([]string{"--addr", "127.0.0.1:1"}, io.Discard, deps)
	if !errors.Is(err, hubendpoint.ErrIdentityMismatch) {
		t.Fatalf("different endpoint: %v", err)
	}
}

func TestHubRestartControlRefusesChangedIdentityBeforeSendingToken(t *testing.T) {
	_, root := endpointTestRoot(t)
	started := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			t.Error("changed identity received a control request")
		}
		restartFixtureHealth(t, w, r, started.Add(time.Second))
	}))
	defer srv.Close()
	publishRestartFixture(t, root, strings.TrimPrefix(srv.URL, "http://"), started)
	record, err := hubendpoint.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := attachOrigin(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := dialHubControl(t.Context(), origin, "restart-test-token", record)
	if client != nil {
		_ = client.Close()
		t.Fatal("changed identity was attached")
	}
	if !errors.Is(err, hubendpoint.ErrIdentityMismatch) {
		t.Fatalf("changed identity: %v", err)
	}
}

func TestHubRestartCommandRequiresSelectedConfigIdentity(t *testing.T) {
	_, root := endpointTestRoot(t) // No token: identity refusal must precede its read.
	started := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	var controls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			controls.Add(1)
			t.Error("unverified config received a control request")
		}
		restartFixtureHealth(t, w, r, started)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	publishRestartFixture(t, root, addr, started)
	if err := os.Remove(DefaultConfigPath()); err != nil {
		t.Fatal(err)
	}
	deps := defaultMainDeps()
	deps.loadConfig = func(string, bool) (Config, error) {
		return Config{Addr: addr, HubStateRoot: root}, nil
	}
	err := runHubRestart([]string{"--timeout", "1s"}, io.Discard, deps)
	if !errors.Is(err, hubendpoint.ErrIdentityMismatch) || controls.Load() != 0 {
		t.Fatalf("missing selected config: err=%v controls=%d", err, controls.Load())
	}
}

func restartFixtureHealth(t *testing.T, w http.ResponseWriter, r *http.Request, started time.Time) {
	t.Helper()
	if r.Header.Get("Authorization") != "" {
		t.Error("health probe sent a credential")
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"boot_id": "restart-fixture", "hub_addr": r.Host, "started_at": started})
}

func publishRestartFixture(t *testing.T, root, addr string, started time.Time) {
	t.Helper()
	release, err := hostlock.AcquireLock(filepath.Join(root, "hub.lock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	config, err := filepath.Abs(DefaultConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
		t.Fatal(err)
	}
	endpointTestConfig(t, filepath.Dir(config), root, "0.0.0.0:0")
	remove, err := hubendpoint.Publish(root, hubendpoint.Record{
		SchemaVersion: hubendpoint.SchemaVersion, BootID: "restart-fixture", PID: os.Getpid(),
		StartedAt: started, ConfigPath: config, HubStateRoot: root,
		RequestedListenAddr: "0.0.0.0:0", ListenSource: "config", BoundAddr: addr,
		LocalURL: "http://" + addr, LoopbackURL: "http://" + addr,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := remove(); err != nil {
			t.Error(err)
		}
	})
}

func republishRestartFixture(t *testing.T, root string, started time.Time) error {
	t.Helper()
	record, err := hubendpoint.Read(root)
	if err != nil {
		return err
	}
	record.StartedAt = started
	remove, err := hubendpoint.Publish(root, record)
	if err != nil {
		return err
	}
	t.Cleanup(func() {
		if err := remove(); err != nil {
			t.Error(err)
		}
	})
	return nil
}
