//go:build evenerfuzz

package hub

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/codexlaunch"
	"primeradiant.com/evener/internal/credentials"
)

func FuzzFinalMainBootstrap(f *testing.F) {
	for mode := byte(0); mode < 8; mode++ {
		f.Add(mode)
	}
	f.Fuzz(func(t *testing.T, mode byte) {
		mode %= 8
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
		cfg.PastIndexRebuild = time.Millisecond
		if err := os.MkdirAll(cfg.HubStateRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if mode == 1 {
			cfg.StateGlob = "["
		}
		if mode == 2 {
			cfg.CodexLaunches = []codexlaunch.CodexLaunchConfig{{ID: "local", Binary: "codex"}}
		}
		if mode == 6 {
			oldExecutable := hubExecutable
			hubExecutable = func() (string, error) { return filepath.Join(root, "evener"), nil }
			t.Cleanup(func() { hubExecutable = oldExecutable })
			if err := os.WriteFile(filepath.Join(root, "evener"), []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}

		ctx, cancel := context.WithCancel(context.Background())
		if mode == 0 || mode == 1 || mode == 2 {
			cancel()
		}
		stop := errors.New("serve stop")
		deps := mainDeps{
			loadRegistry:    hermeticRegistryLoader,
			loadConfig:      func(string) (Config, error) { return cfg, nil },
			ensureDirs:      func() error { return nil },
			acquireLock:     func(string) (func(), error) { return func() {}, nil },
			newToken:        func() (string, error) { return "hub-token", nil },
			loadAuthToken:   func(string) (string, error) { return "auth-token", nil },
			loadCredentials: func(string) (*credentials.Store, error) { return &credentials.Store{}, nil },
			notifyContext: func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
				return ctx, cancel
			},
			listen: func(ctx context.Context, network, addr string) (net.Listener, error) {
				var lc net.ListenConfig
				return lc.Listen(ctx, network, addr)
			},
			serve: func(_ context.Context, _ hubHTTPServer, _ hubShutdowner) error {
				if mode >= 3 {
					time.Sleep(3 * time.Millisecond)
					cancel()
					for i := 0; i < 20; i++ {
						runtime.Gosched()
					}
				}
				if mode == 7 {
					return stop
				}
				return nil
			},
			afterWeb: func(web *WebServer) {
				web.cfg.PokeAttention()
				web.cfg.PokeAttention()
			},
		}

		args := []string{"-config", filepath.Join(root, "hub.toml"), "-evener", "/bin/evener"}
		if mode == 6 {
			args = []string{"-config", filepath.Join(root, "hub.toml")}
		}
		err := runMain(args, &bytes.Buffer{}, deps)
		if mode == 7 {
			if !errors.Is(err, stop) {
				t.Fatalf("runMain error = %v", err)
			}
		} else if err != nil {
			t.Fatalf("runMain mode %d: %v", mode, err)
		}
	})
}

func FuzzFinalMainProcessBoundary(f *testing.F) {
	f.Add(false)
	f.Add(true)
	f.Fuzz(func(t *testing.T, fail bool) {
		oldRun := hubRunMain
		t.Cleanup(func() { hubRunMain = oldRun })
		hubRunMain = func([]string, io.Writer, mainDeps) error {
			if fail {
				return errors.New("stop")
			}
			return nil
		}
		var stderr bytes.Buffer
		exited := Run(nil, nil, nil, &stderr)
		if fail && exited != 1 {
			t.Fatalf("exit code = %d", exited)
		}
		if !fail && exited != 0 {
			t.Fatalf("unexpected exit code = %d", exited)
		}
	})
}

func FuzzFinalMainExecutableFallbacks(f *testing.F) {
	for mode := byte(0); mode < 6; mode++ {
		f.Add(mode)
	}
	f.Fuzz(func(t *testing.T, mode byte) {
		mode %= 6
		oldExecutable, oldArgs := hubExecutable, hubProcessArgs
		t.Cleanup(func() { hubExecutable, hubProcessArgs = oldExecutable, oldArgs })
		switch mode {
		case 0:
			hubExecutable = func() (string, error) { return "/tmp/evener-hub", nil }
		case 1:
			hubExecutable = func() (string, error) { return "", errors.New("missing") }
			hubProcessArgs = func() []string { return []string{"./evener-hub"} }
		case 2:
			hubExecutable = func() (string, error) { return "", errors.New("missing") }
			hubProcessArgs = func() []string { return nil }
		case 3:
			if got := resolveEvenerBinaryPath("", "", func(string) (string, error) { return "", errors.New("missing") }); got != "" {
				t.Fatalf("unexpected resolved path %q", got)
			}
		case 4:
			deps := defaultMainDeps()
			if deps.loadConfig == nil || deps.serve == nil {
				t.Fatal("default dependencies incomplete")
			}
		case 5:
			hubExecutable = func() (string, error) { return "", errors.New("missing") }
			_ = currentExecutable()
		}
		if mode < 3 {
			_ = currentExecutable()
		}
	})
}
