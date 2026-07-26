//go:build serffuzz

package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

func FuzzMainBootstrapPass6(f *testing.F) {
	for mode := byte(0); mode < 10; mode++ {
		f.Add(mode)
	}
	f.Fuzz(func(t *testing.T, mode byte) {
		mode %= 10
		root := t.TempDir()
		t.Setenv("HOME", root)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
		t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))

		stop := errors.New("bootstrap stop")
		cfg := DefaultConfig()
		cfg.Addr = "127.0.0.1:0"
		cfg.RunDir = filepath.Join(root, "run")
		cfg.StateGlob = filepath.Join(root, "projects", "*")
		cfg.PastIndexDB = filepath.Join(root, "hub", "index.db")
		cfg.HubStateRoot = filepath.Join(root, "hub")
		cfg.PluginAutoUpgrade = false
		cfg.Providers = []ProviderConfig{{Name: "fake", Models: []string{"one", "two"}}}
		if err := os.MkdirAll(cfg.HubStateRoot, 0o700); err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		served := false
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
			serve: func(context.Context, hubHTTPServer, hubShutdowner) error {
				served = true
				if mode == 9 {
					return stop
				}
				return nil
			},
		}

		switch mode {
		case 0:
			deps.loadAuthToken = func(string) (string, error) { return "", stop }
		case 1:
			deps.loadCredentials = func(string) (*credentials.Store, error) { return nil, stop }
		case 2:
			deps.loadProviderConfig = func(string) (providercfg.Config, bool, error) {
				return providercfg.Config{}, false, stop
			}
		case 3:
			deps.loadProviderConfig = func(string) (providercfg.Config, bool, error) {
				return providercfg.Config{}, false, nil
			}
			deps.materializeConfig = func(string, ...llm.EnvOption) (providercfg.Config, error) {
				return providercfg.Config{}, stop
			}
		case 4:
			deps.loadProviderConfig = func(string) (providercfg.Config, bool, error) {
				return providercfg.Config{}, false, nil
			}
		case 5:
			cfg.RunDir, cfg.StateGlob, cfg.PastIndexDB = "", "", ""
			deps.loadConfig = func(string) (Config, error) { return cfg, nil }
		case 6:
			cfg.Addr = "0.0.0.0:0"
			deps.loadConfig = func(string) (Config, error) { return cfg, nil }
		case 7:
			cfg.Addr = "[::]:0"
			deps.loadConfig = func(string) (Config, error) { return cfg, nil }
		case 8:
			// Complete bootstrap using the existing provider config branch.
		case 9:
			// Complete bootstrap with a deterministic serving error.
		}

		var stderr bytes.Buffer
		err := runMain([]string{"-addr", cfg.Addr, "-config", filepath.Join(root, "hub.toml"), "-serf", "/bin/serf"}, &stderr, deps)
		if mode <= 3 || mode == 9 {
			if !errors.Is(err, stop) {
				t.Fatalf("mode %d: error = %v, output=%s", mode, err, stderr.String())
			}
			return
		}
		if err != nil {
			t.Fatalf("mode %d: runMain: %v, output=%s", mode, err, stderr.String())
		}
		if !served {
			t.Fatalf("mode %d: server boundary was not reached", mode)
		}
	})
}
