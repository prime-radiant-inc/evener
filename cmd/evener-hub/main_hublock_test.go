package hub

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/envvars/userdirs"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/internal/plugins"
)

// TestRunMainHubLockDerivesFromConfiguredHubStateRoot guards against
// hub.lock's path silently reverting to a raw home-dir join: a configured
// hub_state_root (cfg.HubStateRoot) must relocate the lock alongside the
// rest of the hub's machine state (auth-token, index.db, deletions/), not
// leave it pinned under the real home directory.
func TestRunMainHubLockDerivesFromConfiguredHubStateRoot(t *testing.T) {
	root := t.TempDir()
	// HOME/XDG point somewhere hub.lock must NOT end up, so a raw home-dir
	// join would be caught rather than accidentally matching HubStateRoot.
	t.Setenv("HOME", filepath.Join(root, "unused-home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "unused-config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "unused-state"))

	hubStateRoot := filepath.Join(root, "configured-hub-state-root")

	cfg := DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	cfg.RunDir = filepath.Join(root, "run")
	cfg.StateGlob = filepath.Join(root, "projects", "*")
	cfg.PastIndexDB = filepath.Join(hubStateRoot, "index.db")
	cfg.HubStateRoot = hubStateRoot
	cfg.PluginAutoUpgrade = false
	if err := os.MkdirAll(cfg.HubStateRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var gotLockPath string
	deps := mainDeps{
		loadRegistry: hermeticRegistryLoader,
		loadConfig:   func(string, bool) (Config, error) { return cfg, nil },
		ensureDirs:   func() error { return nil },
		acquireLock: func(path string) (func(), error) {
			gotLockPath = path
			return func() {}, nil
		},
		newToken:        func() (string, error) { return "hub-token", nil },
		loadAuthToken:   func(string) (string, error) { return "auth-token", nil },
		loadCredentials: func(string) (*credentials.Store, error) { return &credentials.Store{}, nil },
		startLivePrefetch: func(context.Context, *hubcore.ProviderRegistry, *hubAuthController, func(func()), func()) {
		},
		startLaunchPrefetch: func(context.Context, *WebServer, func(func())) {},
		notifyContext: func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
			return ctx, func() {}
		},
		listen: func(ctx context.Context, network, addr string) (net.Listener, error) {
			var lc net.ListenConfig
			return lc.Listen(ctx, network, addr)
		},
		serve: func(_ context.Context, srv hubHTTPServer) error {
			cancel()
			return nil
		},
	}

	if err := runMain(nil, os.Stderr, deps); err != nil {
		t.Fatalf("runMain: %v", err)
	}

	want := filepath.Join(hubStateRoot, "hub.lock")
	if gotLockPath != want {
		t.Fatalf("lockPath = %q, want %q (must derive from cfg.HubStateRoot, not a raw home-dir join)", gotLockPath, want)
	}
	for _, root := range []string{hubStateRoot, filepath.Join(root, "unused-state", "evener")} {
		if _, err := os.Lstat(filepath.Join(root, "hub-endpoint.json")); !os.IsNotExist(err) {
			t.Fatalf("stopped hub retained a live endpoint under %s: %v", root, err)
		}
	}
}

// TestRunMainFixesThePluginRegistryRootBeforeLaunchingChildren proves the
// bootstrap boundary holds one concrete registry root. A child inherits an
// environment that launch config may override, so passing an empty root here
// would let it resolve a different registry than the hub used for validation.
func TestRunMainFixesThePluginRegistryRootBeforeLaunchingChildren(t *testing.T) {
	root := t.TempDir()
	configRoot := filepath.Join(root, "config")
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", configRoot)
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
	cancel()
	var webConfig hubcore.WebConfig
	deps := mainDeps{
		loadRegistry:    hermeticRegistryLoader,
		loadConfig:      func(string, bool) (Config, error) { return cfg, nil },
		ensureDirs:      func() error { return nil },
		acquireLock:     func(string) (func(), error) { return func() {}, nil },
		newToken:        func() (string, error) { return "hub-token", nil },
		loadAuthToken:   func(string) (string, error) { return "auth-token", nil },
		loadCredentials: func(string) (*credentials.Store, error) { return &credentials.Store{}, nil },
		startLivePrefetch: func(context.Context, *hubcore.ProviderRegistry, *hubAuthController, func(func()), func()) {
		},
		startLaunchPrefetch: func(context.Context, *WebServer, func(func())) {},
		notifyContext: func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
			return ctx, func() {}
		},
		listen: func(ctx context.Context, network, addr string) (net.Listener, error) {
			var lc net.ListenConfig
			return lc.Listen(ctx, network, addr)
		},
		serve: func(context.Context, hubHTTPServer) error { return nil },
		afterWeb: func(web *WebServer) {
			webConfig = web.cfg
		},
	}

	if err := runMain(nil, os.Stderr, deps); err != nil {
		t.Fatalf("runMain: %v", err)
	}

	wantRoot := plugins.NewManager("").Root
	if webConfig.PluginRoot != wantRoot {
		t.Fatalf("WebConfig.PluginRoot = %q, want %q", webConfig.PluginRoot, wantRoot)
	}
	args := buildSpawnArgs(hubcore.SpawnRequest{PluginRoot: webConfig.PluginRoot})
	rootFlag := slices.Index(args, "--plugin-root")
	if rootFlag < 0 || rootFlag+1 == len(args) || args[rootFlag+1] != wantRoot {
		t.Fatalf("spawn args = %v, want --plugin-root %q", args, wantRoot)
	}
}

// A hub cannot start without a state home even when its config names every
// path itself: its registry cache and OAuth records live under the default
// state root. It must say so on stderr rather than exit silently.
func TestRunMainReportsDefaultPathsWithoutStateHome(t *testing.T) {
	for name, blank := range map[string]func(*Config){
		"run dir":    func(cfg *Config) { cfg.RunDir = "" },
		"state glob": func(cfg *Config) { cfg.StateGlob = "" },
		"past index": func(cfg *Config) { cfg.PastIndexDB = "" },
		"nothing":    func(*Config) {},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", "")
			t.Setenv("XDG_STATE_HOME", "")
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

			cfg := DefaultConfig()
			cfg.Addr = "127.0.0.1:0"
			cfg.HubStateRoot = filepath.Join(root, "state")
			cfg.PluginAutoUpgrade = false
			if err := os.MkdirAll(cfg.HubStateRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			cfg.RunDir = filepath.Join(root, "run")
			cfg.StateGlob = filepath.Join(root, "projects", "*")
			cfg.PastIndexDB = filepath.Join(cfg.HubStateRoot, "index.db")
			blank(&cfg)

			deps := defaultMainDeps()
			deps.loadConfig = func(string, bool) (Config, error) { return cfg, nil }
			deps.ensureDirs = func() error { return nil }
			deps.acquireLock = func(string) (func(), error) { return func() {}, nil }
			deps.loadRegistry = hermeticRegistryLoader
			deps.newToken = func() (string, error) { return "hub-token", nil }
			deps.loadAuthToken = func(string) (string, error) { return "auth-token", nil }
			deps.loadCredentials = func(string) (*credentials.Store, error) { return &credentials.Store{}, nil }

			var stderr bytes.Buffer
			err := runMain(nil, &stderr, deps)
			if !errors.Is(err, userdirs.ErrNoStateHome) {
				t.Fatalf("runMain error = %v, want ErrNoStateHome", err)
			}
			if stderr.Len() == 0 {
				t.Fatal("runMain returned the error without telling the operator")
			}
		})
	}
}
