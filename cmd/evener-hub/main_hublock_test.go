package hub

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostlock"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/internal/interactiveartifacts"
	"primeradiant.com/evener/internal/plugins"
)

func TestRunMainHubStartupRecoveryChild(t *testing.T) {
	if !slices.Contains(os.Args, "startup-recovery-child") && !slices.Contains(os.Args, "startup-recovery-replay-child") {
		return
	}
	index := slices.Index(os.Args, "startup-recovery-child")
	barrier := true
	if index < 0 {
		index = slices.Index(os.Args, "startup-recovery-replay-child")
		barrier = false
	}
	if index < 0 || index+1 >= len(os.Args) {
		t.Fatal("startup recovery child missing state root")
	}
	stateRoot := os.Args[index+1]
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var events, resume *os.File
	if barrier {
		events = os.NewFile(3, "startup-recovery-events")
		resume = os.NewFile(4, "startup-recovery-resume")
		defer events.Close()
		defer resume.Close()
	}
	cfg := DefaultConfig()
	cfg.Addr = "127.0.0.1:0"
	cfg.RunDir = filepath.Join(stateRoot, "run")
	cfg.StateGlob = filepath.Join(stateRoot, "projects", "*")
	cfg.PastIndexDB = filepath.Join(stateRoot, "index.db")
	cfg.HubStateRoot = stateRoot
	cfg.PluginAutoUpgrade = false
	deps := mainDeps{
		loadRegistry: hermeticRegistryLoader,
		loadConfig:   func(string) (Config, error) { return cfg, nil },
		ensureDirs:   func() error { return nil },
		acquireLock:  hostlock.AcquireLock,
		newToken:     func() (string, error) { return "hub-token", nil },
		loadAuthToken: func(string) (string, error) {
			return "auth-token", nil
		},
		loadCredentials: func(string) (*credentials.Store, error) { return &credentials.Store{}, nil },
		startLivePrefetch: func(context.Context, *hubcore.ProviderRegistry, time.Duration, func(func()), func()) {
		},
		notifyContext: func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
			return ctx, func() {}
		},
		listen: func(ctx context.Context, network, addr string) (net.Listener, error) {
			var lc net.ListenConfig
			return lc.Listen(ctx, network, addr)
		},
		serve: func(context.Context, hubHTTPServer) error { return nil },
	}
	if barrier {
		deps.afterDeletionStore = func(*hubcore.DeletionStore) error {
			if _, err := events.Write([]byte{1}); err != nil {
				return err
			}
			var signal [1]byte
			_, err := io.ReadFull(resume, signal[:])
			return err
		}
	}
	if err := runMain(nil, os.Stderr, deps); err != nil {
		t.Fatal(err)
	}
}

func TestRunMainImportsPendingProjectDeletionAfterOwnedStartupBarrier(t *testing.T) {
	root := t.TempDir()
	cfgRoot := filepath.Join(root, "hub")
	if err := os.MkdirAll(cfgRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	sessionID := identifier.MustNewSessionID()
	authority, err := interactiveartifacts.OpenHostAuthority(filepath.Join(cfgRoot, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	installation := authority.Installation()
	if _, err := authority.PrepareRoot(t.Context(), interactiveartifacts.RootRequest{SessionID: sessionID, ProjectID: "project-0123456789", RealmID: installation.RealmID, HumanOwnerID: installation.HumanOwnerID}); err != nil {
		t.Fatal(err)
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	deletions, err := hubcore.NewDeletionStore(cfgRoot)
	if err != nil {
		t.Fatal(err)
	}
	ref := appwire.Ref{SourceID: "local", ThreadID: sessionID}.String()
	if _, err := deletions.BeginProject("project-0123456789", []hubcore.DeletionTarget{{Ref: ref, ThreadID: sessionID}}, true); err != nil {
		t.Fatal(err)
	}
	eventRead, eventWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	resumeRead, resumeWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer eventRead.Close()
	defer eventWrite.Close()
	defer resumeRead.Close()
	defer resumeWrite.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunMainHubStartupRecoveryChild$", "--", "startup-recovery-child", cfgRoot)
	cmd.ExtraFiles = []*os.File{eventWrite, resumeRead}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := eventWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := resumeRead.Close(); err != nil {
		t.Fatal(err)
	}
	barrierCtx, barrierCancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer barrierCancel()
	reached := make(chan error, 1)
	go func() {
		var signal [1]byte
		_, err := io.ReadFull(eventRead, signal[:])
		reached <- err
	}()
	select {
	case err := <-reached:
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatal(err)
		}
	case <-barrierCtx.Done():
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("startup helper did not reach deletion import barrier")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("startup helper exited before crash barrier was reaped")
	}
	replay := exec.Command(os.Args[0], "-test.run=^TestRunMainHubStartupRecoveryChild$", "--", "startup-recovery-replay-child", cfgRoot)
	replay.Stderr = os.Stderr
	if err := replay.Run(); err != nil {
		t.Fatal(err)
	}
	reopened, err := interactiveartifacts.OpenHostAuthority(filepath.Join(cfgRoot, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	policy, err := reopened.Policy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(policy) != 1 || !policy[0].Tombstone {
		t.Fatalf("startup did not import pending project deletion: %+v", policy)
	}
}

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
		loadConfig:   func(string) (Config, error) { return cfg, nil },
		ensureDirs:   func() error { return nil },
		acquireLock: func(path string) (func(), error) {
			gotLockPath = path
			return func() {}, nil
		},
		newToken:        func() (string, error) { return "hub-token", nil },
		loadAuthToken:   func(string) (string, error) { return "auth-token", nil },
		loadCredentials: func(string) (*credentials.Store, error) { return &credentials.Store{}, nil },
		startLivePrefetch: func(context.Context, *hubcore.ProviderRegistry, time.Duration, func(func()), func()) {
		},
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
	authorityPath := filepath.Join(hubStateRoot, "artifacts", "authority.json")
	info, err := os.Stat(authorityPath)
	if err != nil {
		t.Fatalf("artifact authority was not opened under the configured state root: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("artifact authority permissions = %04o, want 0600", got)
	}
}

func TestRunMainDoesNotOpenAuthorityWithoutRealHubLock(t *testing.T) {
	root := t.TempDir()
	cfg := DefaultConfig()
	cfg.HubStateRoot = filepath.Join(root, "hub")
	if err := os.MkdirAll(cfg.HubStateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := hostlock.AcquireLock(filepath.Join(cfg.HubStateRoot, "hub.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	deps := mainDeps{
		loadConfig:  func(string) (Config, error) { return cfg, nil },
		ensureDirs:  func() error { return nil },
		acquireLock: hostlock.AcquireLock,
	}
	if err := runMain(nil, os.Stderr, deps); err == nil {
		t.Fatal("runMain acquired a second real Hub lock")
	}
	if _, err := os.Stat(filepath.Join(cfg.HubStateRoot, "artifacts", "authority.json")); !os.IsNotExist(err) {
		t.Fatalf("authority opened despite lock refusal: %v", err)
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
		loadConfig:      func(string) (Config, error) { return cfg, nil },
		ensureDirs:      func() error { return nil },
		acquireLock:     func(string) (func(), error) { return func() {}, nil },
		newToken:        func() (string, error) { return "hub-token", nil },
		loadAuthToken:   func(string) (string, error) { return "auth-token", nil },
		loadCredentials: func(string) (*credentials.Store, error) { return &credentials.Store{}, nil },
		startLivePrefetch: func(context.Context, *hubcore.ProviderRegistry, time.Duration, func(func()), func()) {
		},
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
