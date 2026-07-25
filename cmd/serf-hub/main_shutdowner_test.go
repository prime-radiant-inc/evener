package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/cmd/serf-hub/internal/codexlaunch"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

// serveHub guards its companion with `if companion != nil`, which is only a
// real guard if what reaches it is an honestly nil INTERFACE. Handing it a nil
// *CodexLauncher instead produces an interface with a type but no value: not
// equal to nil, so the guard passes, and CodexLauncher.Shutdown then takes
// l.Mu.Lock() on a nil receiver and segfaults.
//
// That is the default configuration - a hub with no codex launches configured
// leaves the pointer nil - so the crash landed on every graceful shutdown.
// The existing serveHub tests all pass a literal nil, which is a true nil
// interface, so none of them could ever have caught it.
func TestCodexShutdownerIsHonestlyNilWhenNoLauncherIsConfigured(t *testing.T) {
	var absent *codexlaunch.CodexLauncher
	if got := codexShutdowner(absent); got != nil {
		t.Fatalf("codexShutdowner(nil) = %#v, want an interface that compares equal to nil", got)
	}
}

func TestCodexShutdownerPassesARealLauncherThrough(t *testing.T) {
	launcher := codexlaunch.NewCodexLauncher(nil)
	got := codexShutdowner(launcher)
	if got == nil {
		t.Fatal("codexShutdowner(launcher) = nil, want the launcher itself")
	}
	if got != hubShutdowner(launcher) {
		t.Fatalf("codexShutdowner(launcher) = %#v, want the same launcher", got)
	}
}

// The two tests above prove the helper is right; this one proves it is USED.
// Without it, reverting runMain's call site to pass the raw pointer would leave
// them both green while the panic came straight back.
func TestRunMainHandsServeAnHonestlyNilCompanionWithNoCodexLaunches(t *testing.T) {
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
	// The default, and the whole point: no codex launches configured.
	cfg.CodexLaunches = nil
	if err := os.MkdirAll(cfg.HubStateRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var companion hubShutdowner
	var sawServe bool
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
		serve: func(_ context.Context, _ hubHTTPServer, c hubShutdowner) error {
			sawServe, companion = true, c
			return nil
		},
	}

	if err := runMain(nil, &bytes.Buffer{}, deps); err != nil {
		t.Fatalf("runMain: %v", err)
	}
	if !sawServe {
		t.Fatal("serve was never reached, so this test proved nothing")
	}
	// serveHub guards on exactly this comparison, so assert exactly it.
	if companion != nil {
		t.Fatalf("serve got companion %#v, want nil - serveHub's own nil guard is what breaks otherwise", companion)
	}
}
