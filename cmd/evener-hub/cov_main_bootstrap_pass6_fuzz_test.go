//go:build evenerfuzz

package hub

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
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
			loadRegistry:    hermeticRegistryLoader,
			loadConfig:      func(string) (Config, error) { return cfg, nil },
			ensureDirs:      func() error { return nil },
			acquireLock:     func(string) (func(), error) { return func() {}, nil },
			newToken:        func() (string, error) { return "hub-token", nil },
			loadAuthToken:   func(string) (string, error) { return "auth-token", nil },
			loadCredentials: func(string) (*credentials.Store, error) { return &credentials.Store{}, nil },
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
			// A providers.toml the registry cannot read is a diagnostic, not a
			// stop: the hub starts on implicit instances alone (spec §14.1).
			deps.loadRegistry = func(...registry.Option) (*registry.Registry, *credentials.Store, error) {
				return nil, nil, stop
			}
		case 3:
			// The user-layer load fails but the implicit-only fallback works.
			deps.loadRegistry = func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
				if len(extra) == 0 {
					return nil, nil, stop
				}
				return hermeticRegistryLoader(extra...)
			}
		case 4:
			deps.loadRegistry = hermeticRegistryLoader
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
			// Complete bootstrap on a registry that loads cleanly.
		case 9:
			// Complete bootstrap with a deterministic serving error.
		}

		var stderr bytes.Buffer
		err := runMain([]string{"-addr", cfg.Addr, "-config", filepath.Join(root, "hub.toml"), "-evener", "/bin/evener"}, &stderr, deps)
		if mode == 2 {
			if err != nil {
				t.Fatalf("mode 2: an unparseable providers.toml must not stop the hub: %v", err)
			}
			if !strings.Contains(stderr.String(), "providers config:") {
				t.Fatalf("mode 2: the hub must announce the degraded config: %s", stderr.String())
			}
			return
		}
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
