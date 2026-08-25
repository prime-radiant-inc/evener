package hub

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providercfg"
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
		loadConfig: func(string) (Config, error) { return cfg, nil },
		ensureDirs: func() error { return nil },
		acquireLock: func(path string) (func(), error) {
			gotLockPath = path
			return func() {}, nil
		},
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
}
