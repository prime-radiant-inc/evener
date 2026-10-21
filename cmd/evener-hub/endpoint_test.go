package hub

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostlock"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/internal/hubendpoint"
)

func endpointTestRoot(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "EVENER_") && key != "EVENER_HOST_TEMP_BASES" && key != liveStackWarmEnv {
			t.Setenv(key, "")
			_ = os.Unsetenv(key)
		}
	}
	state := filepath.Join(root, "hub-state")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	return root, state
}
func endpointTestConfig(t *testing.T, root, state, addr string) string {
	t.Helper()
	path := filepath.Join(root, "hub.toml")
	data := "addr = " + strconv.Quote(addr) + "\nhub_state_root = " + strconv.Quote(state) + "\nrun_dir = " + strconv.Quote(filepath.Join(root, "run")) + "\npast_index_db = " + strconv.Quote(filepath.Join(state, "index.db")) + "\nstate_glob = " + strconv.Quote(filepath.Join(root, "projects", "*")) + "\nplugin_auto_upgrade = false\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func endpointCommand(t *testing.T, path string) hubendpoint.Report {
	t.Helper()
	var out, errOut bytes.Buffer
	if err := runEndpoint([]string{"--json", "--config", path}, &out, &errOut, defaultMainDeps()); err != nil {
		t.Fatalf("endpoint: %v (stderr %s)", err, errOut.String())
	}
	report, err := hubendpoint.DecodeReport(&out)
	if err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	return report
}

func TestEndpointConfigFileIdentity(t *testing.T) {
	root, state := endpointTestRoot(t)
	config := endpointTestConfig(t, root, state, "127.0.0.1:0")
	if err := os.MkdirAll(filepath.Join(root, "physical", "inner"), 0700); err != nil {
		t.Fatal(err)
	}
	other := endpointTestConfig(t, filepath.Join(root, "physical"), state, "127.0.0.1:0")
	if err := os.Symlink(config, filepath.Join(root, "alias.toml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "physical", "inner"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root) // Relative selections belong to the hub process, not its controller.
	for _, tc := range []struct{ name, selected, want string }{
		{"absolute", config, config},
		{"relative", "hub.toml", config},
		{"symlink", "alias.toml", config},
		{"dotdot", "physical/../hub.toml", config},
		{"symlink-dotdot-equivalent", "link/../../hub.toml", config},
		{"symlink-dotdot-different", "link/../hub.toml", other},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := endpointCommand(t, tc.selected)
			want, err := filepath.EvalSymlinks(tc.want)
			if err != nil {
				t.Fatal(err)
			}
			if report.ConfigPath != want || !filepath.IsAbs(report.ConfigPath) || report.SchemaVersion != 1 {
				t.Fatalf("report config=%q schema=%d, want config=%q schema=1", report.ConfigPath, report.SchemaVersion, want)
			}
			selectedFile, err := os.Stat(tc.selected)
			if err != nil {
				t.Fatal(err)
			}
			reportedFile, err := os.Stat(report.ConfigPath)
			if err != nil || !os.SameFile(selectedFile, reportedFile) {
				t.Fatalf("report changed the kernel-selected file: %v", err)
			}
		})
	}
}

func TestEndpointReadOnly(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	report := endpointCommand(t, path)
	if report.State != "stopped" || report.Running != nil || report.ConfiguredListenAddr != "127.0.0.1:0" || report.ListenSource != "config" || !report.ConfigExists {
		t.Fatalf("stopped report: %+v", report)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("endpoint rewrote config")
	}
	for _, name := range []string{"hub.lock", "hub-endpoint.json", "auth-token"} {
		if _, err := os.Lstat(filepath.Join(state, name)); !os.IsNotExist(err) {
			t.Fatalf("created %s: %v", name, err)
		}
	}
	tokenPath := filepath.Join(state, "auth-token")
	const sentinel = "disposable-credential-sentinel"
	if err := os.WriteFile(tokenPath, []byte(sentinel), 0600); err != nil {
		t.Fatal(err)
	}
	beforeToken, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := runEndpoint([]string{"--json", "--config", path}, &stdout, &stderr, defaultMainDeps()); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stdout.Bytes(), []byte(sentinel)) || bytes.Contains(stderr.Bytes(), []byte(sentinel)) {
		t.Fatal("credential leaked during discovery")
	}
	afterToken, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != sentinel || !beforeToken.ModTime().Equal(afterToken.ModTime()) {
		t.Fatal("discovery modified token file")
	}
}
func TestEndpointBoundPortZero(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	release, err := hostlock.AcquireLock(filepath.Join(state, "hub.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			_ = json.NewEncoder(w).Encode(map[string]any{"boot_id": "boot-1", "hub_addr": ln.Addr().String(), "started_at": "2026-10-15T00:00:00Z", "version": "v", "backend_git_sha": "sha"})
			return
		}
		http.NotFound(w, r)
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ln) }()
	defer func() { _ = server.Close(); <-done }()
	record := hubendpoint.Record{SchemaVersion: 1, BootID: "boot-1", PID: os.Getpid(), StartedAt: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), ConfigPath: path, HubStateRoot: state, RequestedListenAddr: "127.0.0.1:0", ListenSource: "config", BoundAddr: ln.Addr().String(), LocalURL: "http://" + ln.Addr().String()}
	remove, err := hubendpoint.Publish(state, record)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := remove(); err != nil {
			t.Error(err)
		}
	}()
	report := endpointCommand(t, path)
	if report.State != "running" || report.Running == nil {
		t.Fatalf("expected verified running: %+v", report)
	}
	if report.ConfiguredListenAddr != "127.0.0.1:0" || report.Running.BoundAddr != ln.Addr().String() || report.Running.BootID != "boot-1" || report.Running.Version != "v" {
		t.Fatalf("configured vs bound: %+v", report)
	}
	_, port, _ := net.SplitHostPort(report.Running.BoundAddr)
	if port == "0" {
		t.Fatal("record reported port zero")
	}
	info, err := os.Stat(filepath.Join(state, "hub-endpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("record mode %v", info.Mode())
	}
}
func TestEndpointLifecycle(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	if got := endpointCommand(t, path); got.State != "stopped" {
		t.Fatalf("before lock: %+v", got)
	}
	release, err := hostlock.AcquireLock(filepath.Join(state, "hub.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if got := endpointCommand(t, path); got.State != "starting" || got.Reason != "lock_held_without_record" {
		t.Fatalf("lock before publish: %+v", got)
	}
	release()
	if got := endpointCommand(t, path); got.State != "stopped" {
		t.Fatalf("after release: %+v", got)
	}
}
func TestEndpointRejectsReusedPort(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	release, err := hostlock.AcquireLock(filepath.Join(state, "hub.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	old, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	oldAddr := old.Addr().String()
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", oldAddr)
	if err != nil {
		t.Fatalf("rebind old port: %v", err)
	}
	defer ln.Close()
	requests := make(chan http.Header, 2)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Header.Clone()
		_ = json.NewEncoder(w).Encode(map[string]any{"boot_id": "other-boot", "hub_addr": ln.Addr().String(), "started_at": "2026-10-15T00:00:00Z"})
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ln) }()
	defer func() { _ = server.Close(); <-done }()
	record := hubendpoint.Record{SchemaVersion: 1, BootID: "old-boot", PID: os.Getpid(), StartedAt: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), ConfigPath: path, HubStateRoot: state, RequestedListenAddr: "127.0.0.1:0", ListenSource: "config", BoundAddr: ln.Addr().String(), LocalURL: "http://" + ln.Addr().String()}
	remove, err := hubendpoint.Publish(state, record)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = remove() }()
	report := endpointCommand(t, path)
	if report.State == "running" || report.Running != nil {
		t.Fatalf("accepted old identity: %+v", report)
	}
	select {
	case h := <-requests:
		if h.Get("Authorization") != "" {
			t.Fatal("sent token on probe")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not probe replacement")
	}
}
func TestEndpointConfigChangedWhileRunning(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	release, err := hostlock.AcquireLock(filepath.Join(state, "hub.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	started := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"boot_id": "boot-unchanged", "hub_addr": ln.Addr().String(), "started_at": started})
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ln) }()
	defer func() { _ = server.Close(); <-done }()
	record := hubendpoint.Record{SchemaVersion: 1, BootID: "boot-unchanged", PID: os.Getpid(), StartedAt: started, ConfigPath: path, HubStateRoot: state, RequestedListenAddr: "127.0.0.1:0", ListenSource: "config", BoundAddr: ln.Addr().String(), LocalURL: "http://" + ln.Addr().String()}
	remove, err := hubendpoint.Publish(state, record)
	if err != nil {
		t.Fatal(err)
	}
	endpointTestConfig(t, root, state, "127.0.0.1:9999")
	report := endpointCommand(t, path)
	if report.State != "running" || report.Running == nil || report.Running.BoundAddr != ln.Addr().String() || report.ConfiguredListenAddr != "127.0.0.1:9999" {
		t.Fatalf("edited listener displaced actual instance: %+v", report)
	}
	otherRoot := filepath.Join(root, "other-owner")
	if err := os.MkdirAll(otherRoot, 0700); err != nil {
		t.Fatal(err)
	}
	endpointTestConfig(t, root, otherRoot, "127.0.0.1:9999")
	if got := endpointCommand(t, path); got.State != "stopped" || got.Running != nil || got.HubStateRoot != otherRoot {
		t.Fatalf("changed state root reused old owner: %+v", got)
	}
	endpointTestConfig(t, root, state, "127.0.0.1:9999")
	replacement := record
	replacement.BootID = "new-boot"
	newer, err := hubendpoint.Publish(state, replacement)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = newer() }()
	if err := remove(); err != nil {
		t.Fatal(err)
	}
	got, err := hubendpoint.Read(state)
	if err != nil || got.BootID != "new-boot" {
		t.Fatalf("old cleanup removed replacement: %+v, %v", got, err)
	}
}
func TestEndpointInvalidFlagDoesNotCreateState(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	var stdout, stderr bytes.Buffer
	err := runEndpoint([]string{"--json", "--config", path, "--addr="}, &stdout, &stderr, defaultMainDeps())
	var invalid *EndpointValidationError
	if !errors.As(err, &invalid) || invalid.Source != "flag" {
		t.Fatalf("invalid flag source: %T %+v", err, invalid)
	}
	if stdout.Len() != 0 {
		t.Fatal("partial report emitted")
	}
	if _, err := os.Lstat(filepath.Join(state, "hub.lock")); !os.IsNotExist(err) {
		t.Fatalf("invalid flag touched lock: %v", err)
	}
}

func TestEndpointCommandListenerPrecedence(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "127.0.0.1:9180")
	t.Setenv("EVENER_HUB_LISTEN_ADDR", "127.0.0.1:9181")
	if got := endpointCommand(t, path); got.ListenSource != "env" || got.ConfiguredListenAddr != "127.0.0.1:9181" {
		t.Fatalf("environment precedence: %+v", got)
	}
	var stdout, stderr bytes.Buffer
	if err := runEndpoint([]string{"--json", "--config", path, "--addr", "127.0.0.1:0"}, &stdout, &stderr, defaultMainDeps()); err != nil {
		t.Fatal(err)
	}
	got, err := hubendpoint.DecodeReport(&stdout)
	if err != nil || got.ListenSource != "flag" || got.ConfiguredListenAddr != "127.0.0.1:0" || got.Running != nil {
		t.Fatalf("flag precedence: %+v, %v", got, err)
	}
}

func TestEndpointActualHubLifecycle(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan struct{})
	deps := defaultMainDeps()
	deps.ensureDirs = func() error { return nil }
	deps.loadRegistry = hermeticRegistryLoader
	deps.newToken = func() (string, error) { return "spawn-sentinel", nil }
	deps.loadAuthToken = func(string) (string, error) { return "auth-sentinel", nil }
	deps.loadCredentials = func(string) (*credentials.Store, error) { return &credentials.Store{}, nil }
	deps.startLivePrefetch = func(context.Context, *hubcore.ProviderRegistry, *hubAuthController, func(func()), func()) {}
	deps.startLaunchPrefetch = func(context.Context, *WebServer, func(func())) {}
	deps.notifyContext = func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) { return ctx, func() {} }
	deps.serve = func(ctx context.Context, srv hubHTTPServer) error { close(served); return serveHub(ctx, srv) }
	done := make(chan error, 1)
	go func() { done <- runMain([]string{"--config", path}, io.Discard, deps) }()
	select {
	case <-served:
	case err := <-done:
		t.Fatalf("hub did not reach serving: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("hub did not reach serving")
	}
	report := endpointCommand(t, path)
	if report.State != "running" || report.Running == nil || report.Running.BootID != hubBootID || report.ConfiguredListenAddr != "127.0.0.1:0" || report.Running.RequestedListenAddr != "127.0.0.1:0" {
		t.Fatalf("real hub endpoint: %+v", report)
	}
	_, port, err := net.SplitHostPort(report.Running.BoundAddr)
	if err != nil || port == "0" || report.Running.StartedAt.IsZero() || report.Running.PID != os.Getpid() {
		t.Fatalf("real hub binding: %+v, %v", report.Running, err)
	}
	if report.Running.Version == "" {
		t.Fatal("health did not supply running version")
	}
	if _, err := os.Stat(filepath.Join(state, "hub-endpoint.json")); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("hub shutdown did not complete")
	}
	if _, err := os.Lstat(filepath.Join(state, "hub-endpoint.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hub did not retire record before lock release: %v", err)
	}
	if got := endpointCommand(t, path); got.State != "stopped" {
		t.Fatalf("after shutdown: %+v", got)
	}
}

func TestEndpointBindFailureAndConcurrentStarter(t *testing.T) {
	root, state := endpointTestRoot(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	path := endpointTestConfig(t, root, state, occupied.Addr().String())
	deps := defaultMainDeps()
	deps.ensureDirs = func() error { return nil }
	deps.loadRegistry = hermeticRegistryLoader
	deps.newToken = func() (string, error) { return "spawn-sentinel", nil }
	deps.loadAuthToken = func(string) (string, error) { return "auth-sentinel", nil }
	deps.loadCredentials = func(string) (*credentials.Store, error) { return &credentials.Store{}, nil }
	if err := runMain([]string{"--config", path}, io.Discard, deps); err == nil {
		t.Fatal("occupied bind unexpectedly succeeded")
	}
	if _, err := os.Lstat(filepath.Join(state, "hub-endpoint.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bind failure published record: %v", err)
	}
	release, err := hostlock.AcquireLock(filepath.Join(state, "hub.lock"))
	if err != nil {
		t.Fatalf("failed boot retained lock: %v", err)
	}
	defer release()
	if _, err := hostlock.AcquireLock(filepath.Join(state, "hub.lock")); err == nil {
		t.Fatal("concurrent starter acquired held lock")
	}
	if got := endpointCommand(t, path); got.State != "starting" {
		t.Fatalf("lock winner before record: %+v", got)
	}
	if err := os.WriteFile(filepath.Join(state, "hub-endpoint.json"), []byte("not JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := endpointCommand(t, path); got.State != "unavailable" || got.Running != nil {
		t.Fatalf("corrupt held metadata: %+v", got)
	}
}

func TestEndpointCommandDispatch(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	var stdout, stderr bytes.Buffer
	if got := Run([]string{"endpoint", "--json", "--config", path}, nil, &stdout, &stderr); got != 0 {
		t.Fatalf("command exit = %d, stderr %s", got, stderr.String())
	}
	report, err := hubendpoint.DecodeReport(&stdout)
	if err != nil || report.State != "stopped" {
		t.Fatalf("dispatched report: %+v, %v", report, err)
	}
	if _, err := os.Lstat(filepath.Join(state, "hub.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dispatch created lock: %v", err)
	}
}

// This process publishes one genuine socket while holding the real flock; its
// parent uses SIGKILL to simulate a crash and waits for process exit.
func TestEndpointCrashChild(t *testing.T) {
	if os.Getenv("ENDPOINT_CRASH_CHILD") != "1" {
		return
	}
	root := os.Getenv("ENDPOINT_CRASH_ROOT")
	state := filepath.Join(root, "hub-state")
	path := filepath.Join(root, "hub.toml")
	release, err := hostlock.AcquireLock(filepath.Join(state, "hub.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	started := time.Now().UTC()
	boot := "crash-boot-" + strconv.Itoa(os.Getpid())
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"boot_id": boot, "hub_addr": ln.Addr().String(), "started_at": started})
	})}
	go func() { _ = server.Serve(ln) }()
	record := hubendpoint.Record{SchemaVersion: 1, BootID: boot, PID: os.Getpid(), StartedAt: started, ConfigPath: path, HubStateRoot: state, RequestedListenAddr: "127.0.0.1:0", ListenSource: "config", BoundAddr: ln.Addr().String(), LocalURL: "http://" + ln.Addr().String()}
	remove, err := hubendpoint.Publish(state, record)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = remove() }()
	_, _ = fmt.Fprintln(os.Stdout, "READY")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	defer signal.Stop(sig)
	<-sig
}

func endpointCrashEnv(root string) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "EVENER_") || key == "EVENER_HOST_TEMP_BASES" || key == liveStackWarmEnv {
			env = append(env, entry)
		}
	}
	env = append(env, "ENDPOINT_CRASH_CHILD=1", "ENDPOINT_CRASH_ROOT="+root)
	return env
}

func TestEndpointCrashLeavesStaleRecordForNextOwner(t *testing.T) {
	root, state := endpointTestRoot(t)
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	cmd := exec.Command(os.Args[0], "-test.run=^TestEndpointCrashChild$")
	cmd.Env = endpointCrashEnv(root)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(pipe).ReadString('\n')
		if err == nil && line != "READY\n" {
			err = errors.New("child readiness marker mismatch")
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("child did not publish: %v, stderr %s", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child publication timeout")
	}
	if got := endpointCommand(t, path); got.State != "running" || got.Running == nil || got.Running.PID != cmd.Process.Pid {
		t.Fatalf("live child: %+v", got)
	}
	if _, err := hostlock.AcquireLock(filepath.Join(state, "hub.lock")); err == nil {
		t.Fatal("concurrent starter acquired child's lock")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("killed child returned success")
	}
	if _, err := hubendpoint.Read(state); err != nil {
		t.Fatalf("crash lost stale record: %v", err)
	}
	if got := endpointCommand(t, path); got.State != "stopped" || got.Running != nil {
		t.Fatalf("stale record after process exit: %+v", got)
	}
	release, err := hostlock.AcquireLock(filepath.Join(state, "hub.lock"))
	if err != nil {
		t.Fatal(err)
	}
	replacement := hubendpoint.Record{SchemaVersion: 1, BootID: "next-owner", PID: os.Getpid(), StartedAt: time.Now().UTC(), ConfigPath: path, HubStateRoot: state, RequestedListenAddr: "127.0.0.1:0", ListenSource: "config", BoundAddr: "127.0.0.1:8181", LocalURL: "http://127.0.0.1:8181"}
	remove, err := hubendpoint.Publish(state, replacement)
	if err != nil {
		release()
		t.Fatal(err)
	}
	got, err := hubendpoint.Read(state)
	if err != nil || got.BootID != "next-owner" {
		t.Fatalf("next owner did not replace stale record: %+v, %v", got, err)
	}
	if err := remove(); err != nil {
		t.Error(err)
	}
	release()
}
