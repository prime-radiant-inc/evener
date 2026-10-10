package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
	"primeradiant.com/evener/rendezvous"
)

const retainedProviderConfig = `default = "work"
[providers.work]
base = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"
default_model = "house"
[providers.work.models.house]
wire_id = "wire-house"
`

const retainedProviderDaemonScript = `#!/bin/sh
if [ "$1" = "launch-check" ]; then
 printf '{"protocol":"evener-appwire-v7","launch_flags":["api-log"]}\n'
 exit 0
fi
mkdir -p "$EVENER_RUN_DIR"
printf 'EVENER_PROVIDERS_CONFIG=%s\nEVENER_CREDENTIALS_CONFIG=%s\n' "$EVENER_PROVIDERS_CONFIG" "$EVENER_CREDENTIALS_CONFIG" > "$EVENER_RUN_DIR/child-env"
cat > "$EVENER_RUN_DIR/$$.json" <<RENDEZVOUS
{"pid":$$,"address":"127.0.0.1:1","started_at":"2999-01-01T00:00:00Z"}
RENDEZVOUS
exec tail -f /dev/null
`

func retainedProviderFixture(t *testing.T, config string) *instancesFixture {
	t.Helper()
	clearProviderKeysFromEnvironment(t)
	f := newInstancesFixture(t, map[string]string{})
	if err := f.store.Set("work", "fixture-key"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.tomlPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.tomlPath, []byte("[providers.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.reg.Reload(); err == nil {
		t.Fatal("invalid edit loaded")
	}
	return f
}

func resolveChildProvider(t *testing.T, env []string, ref string) registry.Resolved {
	t.Helper()
	values := map[string]string{}
	for _, value := range env {
		key, val, ok := strings.Cut(value, "=")
		if ok {
			values[key] = val
		}
	}
	store, err := credentials.LoadStore(values["EVENER_CREDENTIALS_CONFIG"])
	if err != nil {
		t.Fatal(err)
	}
	opts := []registry.Option{registry.WithConfigPath(values["EVENER_PROVIDERS_CONFIG"]), registry.WithEnv(func(k string) (string, bool) { v, ok := values[k]; return v, ok }), registry.WithCredentials(cmdutil.StoreCredentialSource{Store: store}), registry.WithStateRoot(t.TempDir()), registry.WithOffline(true), registry.WithoutCache()}
	if values["EVENER_PROVIDERS_CONFIG"] == "" {
		opts = append(opts, registry.WithNoUserLayer())
	}
	r, err := registry.Load(opts...)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := r.Resolve(ref)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func assertRetainedProvider(t *testing.T, env []string) string {
	t.Helper()
	got := resolveChildProvider(t, env, "work/house")
	if got.Instance != "work" || got.ModelID != "house" || got.WireID != "wire-house" || got.Transport.BaseURL != "http://127.0.0.1:9/v1" || got.Credential.Source != "store" || got.Credential.Value != "fixture-key" {
		t.Fatal("retained provider resolution changed identity, model, endpoint or credential source")
	}
	path, _ := envLookup(env, "EVENER_PROVIDERS_CONFIG")
	return path
}

func TestHubModelListResolvesRetainedProviderAndCleansItsSnapshot(t *testing.T) {
	f := retainedProviderFixture(t, retainedProviderConfig)
	old := listEvenerLaunchModelContractFn
	t.Cleanup(func() { listEvenerLaunchModelContractFn = old })
	var snapshot string
	listEvenerLaunchModelContractFn = func(_ context.Context, _ string, env []string) (appwire.ModelListResponse, error) {
		snapshot = assertRetainedProvider(t, env)
		if filepath.Dir(filepath.Dir(snapshot)) != filepath.Dir(f.tomlPath) {
			t.Fatal("snapshot escaped original config directory visibility")
		}
		file, err := os.Stat(snapshot)
		if err != nil || file.Mode().Perm() != 0o600 {
			t.Fatal("snapshot file is not private")
		}
		dir, err := os.Stat(filepath.Dir(snapshot))
		if err != nil || dir.Mode().Perm() != 0o700 {
			t.Fatal("snapshot directory is not private")
		}
		return appwire.ModelListResponse{}, nil
	}
	h := &HubSpawner{Registry: f.ctl.reg, ProvidersConfigPath: f.tomlPath, CredentialsPath: f.credsPath}
	if _, err := h.ListLaunchModelContract(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(snapshot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probe snapshot survives its child: %v", err)
	}
	if _, err := h.ListLaunchModelContractForWorkingDir(t.Context(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(snapshot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scoped probe snapshot survives: %v", err)
	}
	raw, err := os.ReadFile(f.tomlPath)
	if err != nil || string(raw) != "[providers.invalid\n" {
		t.Fatal("invalid source bytes were overwritten")
	}
	if err := os.WriteFile(f.tomlPath, []byte(retainedProviderConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatal(err)
	}
	listEvenerLaunchModelContractFn = func(_ context.Context, _ string, env []string) (appwire.ModelListResponse, error) {
		if path, _ := envLookup(env, "EVENER_PROVIDERS_CONFIG"); path != f.tomlPath {
			t.Fatal("repair did not return to source path")
		}
		assertRetainedProvider(t, env)
		return appwire.ModelListResponse{}, nil
	}
	if _, err := h.ListLaunchModelContract(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestHubSpawnAndResumeResolveRetainedProviderForChildLifetime(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "spawn"
		if resume {
			name = "resume"
		}
		t.Run(name, func(t *testing.T) {
			f := retainedProviderFixture(t, retainedProviderConfig)
			runDir := filepath.Join(t.TempDir(), "run")
			binary := filepath.Join(t.TempDir(), "evener")
			writeFakeEvener(t, binary, retainedProviderDaemonScript)
			h := &HubSpawner{Cfg: DefaultConfig(), Registry: f.ctl.reg, ProvidersConfigPath: f.tomlPath, CredentialsPath: f.credsPath, RunDir: runDir, EvenerBinary: binary}
			removed := make(chan string, 8)
			old := spawnRemoveAll
			spawnRemoveAll = func(path string) error {
				err := old(path)
				if strings.HasPrefix(filepath.Base(path), "retained-providers-") {
					removed <- path
				}
				return err
			}
			t.Cleanup(func() { spawnRemoveAll = old })
			var entry rendezvous.Entry
			var err error
			resolved := launchconfig.Resolved{Effective: launchconfig.Layer{Model: "work/house"}}
			if resume {
				entry, err = h.Resume(t.Context(), hubcore.ResumeRequest{SessionID: "01JRETAINED", Provider: "work", StateDir: f.stateDir, WorkingDir: t.TempDir(), Resolved: resolved})
			} else {
				entry, err = h.Spawn(t.Context(), hubcore.SpawnRequest{Provider: "work", StateDir: f.stateDir, WorkingDir: t.TempDir(), Resolved: resolved})
			}
			if err != nil {
				t.Fatal(err)
			}
			process, err := os.FindProcess(entry.PID)
			if err != nil {
				t.Fatal(err)
			}
			reaped := false
			t.Cleanup(func() {
				_ = process.Kill()
				if !reaped {
					select {
					case <-removed:
					case <-time.After(10 * time.Second):
						t.Error("child cleanup did not finish")
					}
				}
			})
			raw, err := os.ReadFile(filepath.Join(runDir, "child-env"))
			if err != nil {
				t.Fatal(err)
			}
			path := assertRetainedProvider(t, strings.Split(strings.TrimSpace(string(raw)), "\n"))
			if _, err := os.Stat(path); err != nil {
				t.Fatal("snapshot disappeared while child survives")
			}
			if err := process.Kill(); err != nil {
				t.Fatal(err)
			}
			select {
			case dir := <-removed:
				if dir != filepath.Dir(path) {
					t.Fatal("wrong snapshot cleanup")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("child waiter did not clean provider snapshot")
			}
			reaped = true
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("reaped child retained its provider snapshot")
			}
		})
	}
}

func TestRetainedProviderSnapshotCreationFailureCleansDirectory(t *testing.T) {
	f := retainedProviderFixture(t, retainedProviderConfig)
	oldList := listEvenerLaunchModelContractFn
	t.Cleanup(func() { listEvenerLaunchModelContractFn = oldList })
	listEvenerLaunchModelContractFn = func(context.Context, string, []string) (appwire.ModelListResponse, error) {
		return appwire.ModelListResponse{}, nil
	}
	old := spawnWriteFile
	t.Cleanup(func() { spawnWriteFile = old })
	failure := errors.New("fixture snapshot write failure")
	spawnWriteFile = func(path string, raw []byte, mode os.FileMode) error {
		if err := old(path, raw[:len(raw)/2], mode); err != nil {
			return err
		}
		return failure
	}
	h := &HubSpawner{Registry: f.ctl.reg, ProvidersConfigPath: f.tomlPath, CredentialsPath: f.credsPath}
	if _, err := h.ListLaunchModelContract(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("error = %v, want creation failure", err)
	}
	dirs, err := filepath.Glob(filepath.Join(filepath.Dir(f.tomlPath), "retained-providers-*"))
	if err != nil || len(dirs) != 0 {
		t.Fatal("failed creation left snapshot directory")
	}
}

func TestRetainedProviderCredentialProbeResolvesAndCleansSnapshot(t *testing.T) {
	for _, credentialSource := range []string{"explicit override", "original sibling"} {
		t.Run(credentialSource, func(t *testing.T) {
			authorization := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authorization <- r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[{"id":"house"}]}`))
			}))
			t.Cleanup(server.Close)
			config := strings.ReplaceAll(retainedProviderConfig, "http://127.0.0.1:9/v1", server.URL+"/v1")
			f := retainedProviderFixture(t, config)
			t.Setenv("EVENER_PROVIDERS_CONFIG", f.tomlPath)
			t.Setenv("EVENER_CREDENTIALS_CONFIG", f.credsPath)
			if credentialSource == "original sibling" {
				raw, err := os.ReadFile(f.credsPath)
				if err != nil {
					t.Fatal(err)
				}
				sibling := filepath.Join(filepath.Dir(f.tomlPath), "credentials.toml")
				if err := os.WriteFile(sibling, raw, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Unsetenv("EVENER_CREDENTIALS_CONFIG"); err != nil {
					t.Fatal(err)
				}
				if cmdutil.CredentialsPath() != sibling {
					t.Fatal("probe fallback lost the original credential store")
				}
			}
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			var snapshot string
			loader := func(path string, noUser bool) (credentialProbeClient, error) {
				if noUser {
					t.Fatal("retained credential probe discarded user layer")
				}
				snapshot = path
				client, err := loadCredentialTestClient(path, noUser)
				if err != nil {
					return nil, err
				}
				resolved, err := client.Registry().Resolve("work/house")
				if err != nil || resolved.Credential.Source != "store" || resolved.Credential.Value != "fixture-key" {
					t.Fatal("probe did not preserve original credential source")
				}
				return client, nil
			}
			result, err := f.ctl.auth.runCredentialTest(t.Context(), "work", "", loader)
			if err != nil || result.Status != appwire.AuthTestStatusSuccess {
				t.Fatalf("probe result=%s error=%v", result.Status, err)
			}
			if gotAuthorization := <-authorization; gotAuthorization != "Bearer fixture-key" {
				t.Fatal("probe sent a different credential")
			}
			if _, err := os.Stat(snapshot); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("closed probe retained snapshot")
			}
		})
	}
}

func TestRetainedProviderLaunchHonorsProjectOverrides(t *testing.T) {
	for _, kind := range []string{"provider", "credentials", "no-user-layer"} {
		t.Run(kind, func(t *testing.T) {
			f := retainedProviderFixture(t, retainedProviderConfig)
			root := t.TempDir()
			binary := filepath.Join(root, "evener")
			runDir := filepath.Join(root, "run")
			capture := filepath.Join(runDir, "child-env")
			writeFakeEvener(t, binary, retainedProviderDaemonScript)
			removed := make(chan string, 1)
			oldRemove := spawnRemoveAll
			spawnRemoveAll = func(path string) error {
				err := oldRemove(path)
				if strings.HasPrefix(filepath.Base(path), "retained-providers-") {
					removed <- path
				}
				return err
			}
			t.Cleanup(func() { spawnRemoveAll = oldRemove })
			projectEnv := map[string]string{}
			wantEndpoint := "http://127.0.0.1:9/v1"
			wantKey := "fixture-key"
			switch kind {
			case "provider":
				projectPath := filepath.Join(root, "providers.toml")
				config := strings.ReplaceAll(retainedProviderConfig, wantEndpoint, "http://127.0.0.1:10/v1")
				if err := os.WriteFile(projectPath, []byte(config), 0o600); err != nil {
					t.Fatal(err)
				}
				projectEnv["EVENER_PROVIDERS_CONFIG"] = projectPath
				wantEndpoint = "http://127.0.0.1:10/v1"
			case "credentials":
				projectPath := filepath.Join(root, "credentials.toml")
				store, err := credentials.LoadStore(projectPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Set("work", "project-key"); err != nil {
					t.Fatal(err)
				}
				projectEnv["EVENER_CREDENTIALS_CONFIG"] = projectPath
				wantKey = "project-key"
			case "no-user-layer":
				projectEnv["EVENER_PROVIDERS_CONFIG"] = ""
			}
			h := &HubSpawner{Registry: f.ctl.reg, ProvidersConfigPath: f.tomlPath, CredentialsPath: f.credsPath, EvenerBinary: binary, RunDir: runDir}
			entry, err := h.Spawn(t.Context(), hubcore.SpawnRequest{Provider: "work", StateDir: f.stateDir, WorkingDir: root, Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{Model: "work/house", Env: projectEnv}}})
			if err != nil {
				t.Fatal(err)
			}
			process, err := os.FindProcess(entry.PID)
			if err != nil {
				t.Fatal(err)
			}
			reaped := false
			t.Cleanup(func() {
				_ = process.Kill()
				if kind == "credentials" && !reaped {
					select {
					case <-removed:
					case <-time.After(10 * time.Second):
						t.Error("child snapshot cleanup did not finish")
					}
				}
			})
			raw, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			env := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if kind == "no-user-layer" {
				if path, ok := envLookup(env, "EVENER_PROVIDERS_CONFIG"); !ok || path != "" {
					t.Fatal("project explicit no-user-layer lost")
				}
			} else {
				got := resolveChildProvider(t, env, "work/house")
				if got.Transport.BaseURL != wantEndpoint || got.Credential.Value != wantKey || got.Credential.Source != "store" {
					t.Fatal("project provider or credential override lost")
				}
			}
			if err := process.Kill(); err != nil {
				t.Fatal(err)
			}
			if kind == "credentials" {
				select {
				case <-removed:
				case <-time.After(10 * time.Second):
					t.Fatal("child snapshot cleanup did not finish")
				}
				reaped = true
			}
			dirs, err := filepath.Glob(filepath.Join(filepath.Dir(f.tomlPath), "retained-providers-*"))
			if err != nil || len(dirs) != 0 {
				t.Fatal("exited child retained provider directory")
			}
		})
	}
}

func TestRetainedProviderFailedDaemonLaunchCleansAfterChildExit(t *testing.T) {
	f := retainedProviderFixture(t, retainedProviderConfig)
	binary := filepath.Join(t.TempDir(), "evener")
	writeFakeEvener(t, binary, `#!/bin/sh
if [ "$1" = "launch-check" ]; then
 printf '{"protocol":"evener-appwire-v7","launch_flags":["api-log"]}\n'
 exit 0
fi
exit 1
`)
	removed := make(chan string, 1)
	old := spawnRemoveAll
	spawnRemoveAll = func(path string) error {
		err := old(path)
		if strings.HasPrefix(filepath.Base(path), "retained-providers-") {
			removed <- path
		}
		return err
	}
	t.Cleanup(func() { spawnRemoveAll = old })
	h := &HubSpawner{Registry: f.ctl.reg, ProvidersConfigPath: f.tomlPath, CredentialsPath: f.credsPath, EvenerBinary: binary, RunDir: filepath.Join(t.TempDir(), "run")}
	_, err := h.Spawn(t.Context(), hubcore.SpawnRequest{Provider: "work", StateDir: f.stateDir, WorkingDir: t.TempDir(), Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{Model: "work/house"}}})
	if err == nil {
		t.Fatal("failed daemon launch succeeded")
	}
	select {
	case dir := <-removed:
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("exited failed child retained snapshot")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("failed child waiter did not clean snapshot")
	}
}

func TestRetainedProviderResumeKeepsSnapshotWhenChildCleanupIsUnconfirmed(t *testing.T) {
	f := retainedProviderFixture(t, retainedProviderConfig)
	binary := filepath.Join(t.TempDir(), "evener")
	writeFakeEvener(t, binary, `#!/bin/sh
printf '{"protocol":"evener-appwire-v7","launch_flags":["api-log"]}\n'
`)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	exit := make(chan struct{})
	var finish sync.Once
	removed := make(chan string, 1)
	oldRemove, oldStart := spawnRemoveAll, startResumeChild
	spawnRemoveAll = func(path string) error {
		err := oldRemove(path)
		if strings.HasPrefix(filepath.Base(path), "retained-providers-") {
			removed <- path
		}
		return err
	}
	t.Cleanup(func() { spawnRemoveAll = oldRemove; startResumeChild = oldStart })
	var env []string
	startResumeChild = func(cmd *exec.Cmd) (resumeChild, error) {
		env = cmd.Env
		cancel()
		return resumeChild{pid: 999999, kill: func() error { return errors.New("fixture kill unconfirmed") }, wait: func() error { <-exit; return nil }}, nil
	}
	t.Cleanup(func() {
		finish.Do(func() { close(exit) })
		select {
		case <-removed:
		case <-time.After(10 * time.Second):
			t.Error("surviving child did not release its snapshot after exit")
		}
	})
	h := &HubSpawner{Registry: f.ctl.reg, ProvidersConfigPath: f.tomlPath, CredentialsPath: f.credsPath, EvenerBinary: binary, RunDir: filepath.Join(t.TempDir(), "run")}
	_, err := h.Resume(ctx, hubcore.ResumeRequest{SessionID: "01JRETAINEDFAIL", Provider: "work", StateDir: f.stateDir, WorkingDir: t.TempDir(), Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{Model: "work/house"}}})
	if _, ok := errors.AsType[*resumeCleanupError](err); !ok {
		t.Fatalf("error=%v, want unconfirmed child cleanup", err)
	}
	snapshot := assertRetainedProvider(t, env)
	if _, err := os.Stat(snapshot); err != nil {
		t.Fatal("unconfirmed child cleanup discarded provider bytes")
	}
	select {
	case <-removed:
		t.Fatal("surviving child snapshot was cleaned")
	default:
	}
	finish.Do(func() { close(exit) })
}

func TestRetainedProviderChildKeepsOriginalCredentialFallback(t *testing.T) {
	f := retainedProviderFixture(t, retainedProviderConfig)
	source := filepath.Join(filepath.Dir(f.tomlPath), "credentials.toml")
	raw, err := os.ReadFile(f.credsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVENER_CREDENTIALS_CONFIG", "")
	old := listEvenerLaunchModelContractFn
	t.Cleanup(func() { listEvenerLaunchModelContractFn = old })
	listEvenerLaunchModelContractFn = func(_ context.Context, _ string, env []string) (appwire.ModelListResponse, error) {
		assertRetainedProvider(t, env)
		return appwire.ModelListResponse{}, nil
	}
	h := &HubSpawner{Registry: f.ctl.reg, ProvidersConfigPath: f.tomlPath}
	if _, err := h.ListLaunchModelContract(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVENER_CREDENTIALS_CONFIG", f.credsPath)
	if _, err := h.ListLaunchModelContract(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedProviderPreflightFailureCleansSnapshot(t *testing.T) {
	f := retainedProviderFixture(t, retainedProviderConfig)
	binary := filepath.Join(t.TempDir(), "evener")
	writeFakeEvener(t, binary, "#!/bin/sh\nexit 1\n")
	h := &HubSpawner{Registry: f.ctl.reg, ProvidersConfigPath: f.tomlPath, CredentialsPath: f.credsPath, EvenerBinary: binary}
	_, err := h.Spawn(t.Context(), hubcore.SpawnRequest{Provider: "work", StateDir: f.stateDir, WorkingDir: t.TempDir(), Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{Model: "work/house"}}})
	if err == nil {
		t.Fatal("failing preflight succeeded")
	}
	dirs, err := filepath.Glob(filepath.Join(filepath.Dir(f.tomlPath), "retained-providers-*"))
	if err != nil || len(dirs) != 0 {
		t.Fatal("failed preflight retained snapshot")
	}
}

func TestRetainedProviderSnapshotSurvivesDifferentChildWorkingDirectory(t *testing.T) {
	f := retainedProviderFixture(t, retainedProviderConfig)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, f.tomlPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.tomlPath, []byte(retainedProviderConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := hubcore.NewProviderRegistry(func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		r, err := registry.Load(registry.WithConfigPath(relative), registry.WithEnv(func(string) (string, bool) { return "", false }), registry.WithOffline(true), registry.WithoutCache(), registry.WithStateRoot(t.TempDir()))
		return r, nil, err
	})
	if err := reg.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.tomlPath, []byte("[providers.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reg.Reload(); err == nil {
		t.Fatal("invalid edit loaded")
	}
	child, err := prepareChildProviderConfig(relative, false, reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer child.cleanup()
	path := child.path
	if !filepath.IsAbs(path) {
		path = filepath.Join(t.TempDir(), path)
	}
	env := []string{"EVENER_PROVIDERS_CONFIG=" + path, "EVENER_CREDENTIALS_CONFIG=" + f.credsPath}
	assertRetainedProvider(t, env)
}
