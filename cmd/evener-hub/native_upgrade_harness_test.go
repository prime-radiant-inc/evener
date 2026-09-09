package hub

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/selfupdate"
)

// TestNativeUpgradeHarness is a manual, loopback-only fixture for exercising
// the native upgrade flow against a real hub and real selfupdate.Upgrade.
// The default suite remains deterministic and never starts this service.
func TestNativeUpgradeHarness(t *testing.T) {
	if os.Getenv("EVENER_NATIVE_UPGRADE_HARNESS") != "1" {
		t.Skip("manual native upgrade harness")
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skipf("native upgrade fixture currently supports darwin-arm64, got %s-%s", runtime.GOOS, runtime.GOARCH)
	}
	evener := nativeUpgradeBinary(t, "EVENER_NATIVE_UPGRADE_EVENER")
	dev := nativeUpgradeBinary(t, "EVENER_NATIVE_UPGRADE_EVENER_DEV")
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	runDir := filepath.Join(root, "run")
	hubStateRoot := filepath.Join(root, "hub-state")
	pluginRoot := filepath.Join(root, "plugins")
	for _, dir := range []string{stateDir, runDir, hubStateRoot, pluginRoot} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	token := nativeUpgradeToken(t)
	tokenPath := filepath.Join(hubStateRoot, hubedge.TokenFileName)
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := newNativeUpgradeFixture(t, evener, dev)
	fixture.paths = map[string]string{"token": tokenPath, "prefix": filepath.Join(root, "prefix"), "bin": filepath.Join(root, "prefix", "bin"), "share_bin": filepath.Join(root, "prefix", "share", "evener", "bin")}
	releaseServer := httptest.NewServer(fixture.ReleaseHandler())
	t.Cleanup(releaseServer.Close)
	rpcListener := nativeUpgradeListener(t)
	controlListener := nativeUpgradeListener(t)

	cfg := hubcore.WebConfig{
		AuthToken:        token,
		HubAddr:          rpcListener.Addr().String(),
		HubStateRoot:     hubStateRoot,
		LaunchConfigRoot: filepath.Join(root, "config"),
		RunDir:           runDir,
		StateDir:         stateDir,
		PluginRoot:       pluginRoot,
		MCPConfigPath:    filepath.Join(root, "no-mcp.json"),
		Past:             hubcore.NewPastIndex(""),
		PastPerPage:      50,
	}
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "Native upgrade fixture", Version: Version, SourceID: "local"})
	appserver.HandleTyped(app.Router(), appwire.MethodEvenerUpgrade, hubUpgrade)
	appserver.HandleTyped(app.Router(), appwire.MethodEvenerSettingsOverview, func(ctx context.Context, _ appwire.EmptyParams) (appwire.SettingsOverviewResponse, error) {
		return hubSettingsOverview(ctx, cfg)
	})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadList, func(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{Data: []appwire.Thread{}}, nil
	})
	rpcMux := http.NewServeMux()
	rpcMux.HandleFunc("/rpc", app.ServeWebSocket)
	oldUpgrade := runHubSelfUpgrade
	runHubSelfUpgrade = func(ctx context.Context, opts selfupdate.Options) (selfupdate.Result, error) {
		opts.Prefix = filepath.Join(root, "prefix")
		opts.BinDir = filepath.Join(opts.Prefix, "bin")
		opts.ShareBinDir = filepath.Join(opts.Prefix, "share", "evener", "bin")
		opts.GOOS, opts.GOARCH = runtime.GOOS, runtime.GOARCH
		opts.RepoURL = releaseServer.URL
		result, err := selfupdate.Upgrade(ctx, opts)
		if err == nil {
			fixture.mu.Lock()
			fixture.counts.installs = len(result.Installed)
			fixture.lastErr = ""
			fixture.mu.Unlock()
		} else {
			fixture.mu.Lock()
			fixture.lastErr = "upgrade"
			fixture.mu.Unlock()
		}
		return result, err
	}
	t.Cleanup(func() { runHubSelfUpgrade = oldUpgrade })

	rpcServer := &http.Server{Handler: hubedge.AuthGuard(token)(rpcMux), ReadHeaderTimeout: 5 * time.Second}
	var rpcCloseOnce sync.Once
	var rpcCloseErr error
	controlServer := &http.Server{Handler: fixture.ControlHandler(func() error {
		rpcCloseOnce.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := app.Shutdown(ctx); err != nil {
				rpcCloseErr = err
				return
			}
			rpcCloseErr = rpcListener.Close()
		})
		return rpcCloseErr
	}), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = rpcServer.Serve(rpcListener) }()
	go func() { _ = controlServer.Serve(controlListener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		fixture.Release()
		if err := app.Shutdown(ctx); err != nil {
			t.Errorf("drain AppWire handlers: %v", err)
		}
		if err := rpcServer.Shutdown(ctx); err != nil {
			t.Errorf("shutdown RPC server: %v", err)
		}
		if err := controlServer.Shutdown(ctx); err != nil {
			t.Errorf("shutdown control server: %v", err)
		}
	})

	t.Logf("native upgrade fixture control=http://%s rpc=ws://%s/rpc", controlListener.Addr(), rpcListener.Addr())
	t.Logf("auth token file=%s", tokenPath)
	t.Logf("source evener=%s sha256=%s", evener, nativeUpgradeSHA256(t, evener))
	t.Logf("source evener-dev=%s sha256=%s", dev, nativeUpgradeSHA256(t, dev))
	<-fixture.stop
}

type nativeUpgradeFixture struct {
	mu            sync.Mutex
	mode          string
	released      chan struct{}
	stopOnce      sync.Once
	archive       []byte
	entered       chan struct{}
	enteredClosed bool
	stop          chan struct{}
	sources       map[string]string
	sha256        map[string]string
	paths         map[string]string
	counts        struct{ downloads, installs int }
	lastErr       string
}

func newNativeUpgradeFixture(t *testing.T, evener, dev string) *nativeUpgradeFixture {
	t.Helper()
	return &nativeUpgradeFixture{
		mode: "success", released: make(chan struct{}), stop: make(chan struct{}),
		archive: nativeUpgradeArchive(t, evener, dev),
		sources: map[string]string{"evener": evener, "evener-dev": dev},
		sha256:  map[string]string{"evener": nativeUpgradeSHA256(t, evener), "evener-dev": nativeUpgradeSHA256(t, dev)},
	}
}

func (f *nativeUpgradeFixture) Release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-f.released:
	default:
		close(f.released)
	}
}

func (f *nativeUpgradeFixture) SetMode(mode string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if mode == "hold" && f.mode == "hold" {
		select {
		case <-f.released:
		default:
			return
		}
	}
	f.mode = mode
	if mode == "hold" {
		f.released = make(chan struct{})
		f.entered = make(chan struct{})
		f.enteredClosed = false
	}
}

func (f *nativeUpgradeFixture) ReleaseHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == "checksums.txt" {
			sum := sha256.Sum256(f.archive)
			asset := "evener_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
			return
		}
		f.mu.Lock()
		mode := f.mode
		f.counts.downloads++
		released := f.released
		entered := f.entered
		enteredClosed := f.enteredClosed
		if mode == "hold" && !enteredClosed {
			f.enteredClosed = true
		}
		f.mu.Unlock()
		if mode == "fail" {
			f.mu.Lock()
			f.lastErr = "download"
			f.mu.Unlock()
			http.Error(w, "fixture download failure", http.StatusServiceUnavailable)
			return
		}
		if mode == "hold" {
			if entered != nil && !enteredClosed {
				close(entered)
			}
			select {
			case <-released:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(f.archive)
	})
}

func (f *nativeUpgradeFixture) WaitEntered(ctx context.Context) error {
	f.mu.Lock()
	entered := f.entered
	f.mu.Unlock()
	if entered == nil {
		return errors.New("hold request was not observed")
	}
	select {
	case <-entered:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *nativeUpgradeFixture) ControlHandler(closeRPC func() error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"downloads": f.counts.downloads, "installs": f.counts.installs, "mode": f.mode, "error_class": f.lastErr, "source_paths": f.sources, "sha256": f.sha256, "owned_paths": f.paths})
	})
	for _, mode := range []string{"fail", "success", "hold"} {
		mux.HandleFunc("POST /mode/"+mode, func(w http.ResponseWriter, _ *http.Request) {
			f.SetMode(mode)
			if mode != "hold" {
				f.Release()
			}
			w.WriteHeader(http.StatusNoContent)
		})
	}
	mux.HandleFunc("POST /release", func(w http.ResponseWriter, _ *http.Request) { f.Release(); w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("POST /close-rpc", func(w http.ResponseWriter, _ *http.Request) {
		if err := closeRPC(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, _ *http.Request) {
		_ = closeRPC()
		f.stopOnce.Do(func() { close(f.stop) })
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func nativeUpgradeListener(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return l
}
func nativeUpgradeBinary(t *testing.T, name string) string {
	p := os.Getenv(name)
	if p == "" || !filepath.IsAbs(p) {
		t.Fatalf("%s must be an absolute binary path", name)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("%s must be a regular executable file", name)
	}
	return p
}
func nativeUpgradeToken(t *testing.T) string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}
func nativeUpgradeSHA256(t *testing.T, path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func nativeUpgradeArchive(t *testing.T, evener, dev string) []byte {
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	root := "evener_" + runtime.GOOS + "_" + runtime.GOARCH
	for _, item := range []struct{ name, path string }{{"evener", evener}, {"evener-dev", dev}} {
		data, err := os.ReadFile(item.path)
		if err != nil {
			t.Fatal(err)
		}
		h := &tar.Header{Name: filepath.ToSlash(filepath.Join(root, item.name)), Mode: 0o755, Size: int64(len(data))}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestNativeUpgradeArchiveContainsBothAbsoluteInputs(t *testing.T) {
	evener := filepath.Join(t.TempDir(), "evener")
	dev := filepath.Join(t.TempDir(), "evener-dev")
	if err := os.WriteFile(evener, []byte("evener-fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dev, []byte("evener-dev-fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive := nativeUpgradeArchive(t, evener, dev)
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	got := map[string]string{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		got[h.Name] = string(body)
	}
	rootName := "evener_" + runtime.GOOS + "_" + runtime.GOARCH
	if got[rootName+"/evener"] != "evener-fixture" || got[rootName+"/evener-dev"] != "evener-dev-fixture" {
		t.Fatalf("archive entries = %+v", got)
	}
}

func TestNativeUpgradeFixtureModesUseRealSelfUpdate(t *testing.T) {
	if (runtime.GOOS != "darwin" || runtime.GOARCH != "arm64") && (runtime.GOOS != "linux" || runtime.GOARCH != "amd64") {
		t.Skipf("self-update fixture unsupported on %s-%s", runtime.GOOS, runtime.GOARCH)
	}
	root := t.TempDir()
	evener, dev := filepath.Join(root, "evener"), filepath.Join(root, "evener-dev")
	if err := os.WriteFile(evener, []byte("evener-fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dev, []byte("evener-dev-fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := newNativeUpgradeFixture(t, evener, dev)
	server := httptest.NewServer(fixture.ReleaseHandler())
	t.Cleanup(server.Close)
	options := func(prefix string) selfupdate.Options {
		return selfupdate.Options{CurrentChannel: "snapshot", Prefix: prefix, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, RepoURL: server.URL}
	}
	fixture.SetMode("fail")
	failed := filepath.Join(root, "failed")
	if _, err := selfupdate.Upgrade(t.Context(), options(failed)); err == nil {
		t.Fatal("failure mode unexpectedly succeeded")
	}
	if _, err := os.Stat(failed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed prefix exists: %v", err)
	}
	fixture.SetMode("hold")
	ctx, cancel := context.WithCancel(t.Context())
	cancelled := filepath.Join(root, "cancelled")
	doneCancel := make(chan error, 1)
	go func() { _, err := selfupdate.Upgrade(ctx, options(cancelled)); doneCancel <- err }()
	if err := fixture.WaitEntered(t.Context()); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-doneCancel; err == nil {
		t.Fatal("cancelled hold unexpectedly succeeded")
	}
	if _, err := os.Stat(cancelled); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled prefix exists: %v", err)
	}
	fixture.Release()
	fixture.SetMode("hold")
	done := make(chan error, 1)
	installedCtx, cancelInstall := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelInstall()
	installed := filepath.Join(root, "installed")
	go func() {
		_, err := selfupdate.Upgrade(installedCtx, options(installed))
		done <- err
	}()
	if err := fixture.WaitEntered(t.Context()); err != nil {
		t.Fatal(err)
	}
	fixture.SetMode("hold") // Repeated control requests must retain the held download.
	fixture.Release()
	if err := <-done; err != nil {
		t.Fatalf("released hold: %v", err)
	}
	for _, bin := range []string{"evener", "evener-dev"} {
		path := filepath.Join(installed, "share", "evener", "bin", bin)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != bin+"-fixture" {
			t.Fatalf("%s has unexpected contents %q", path, data)
		}
		link := filepath.Join(installed, "bin", bin)
		target, err := os.Readlink(link)
		if err != nil {
			t.Fatal(err)
		}
		if target != path {
			t.Fatalf("%s -> %s, want %s", link, target, path)
		}
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.counts.downloads < 2 {
		t.Fatalf("downloads = %d, want failure and hold requests", fixture.counts.downloads)
	}
}

func TestNativeUpgradeHeldHandlerAcceptsRepeatedCancelledRequests(t *testing.T) {
	fixture := &nativeUpgradeFixture{released: make(chan struct{})}
	fixture.SetMode("hold")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/releases/download/snapshot/evener.tar.gz", nil)
	for range 2 {
		fixture.ReleaseHandler().ServeHTTP(httptest.NewRecorder(), request)
	}
	fixture.Release()
}
