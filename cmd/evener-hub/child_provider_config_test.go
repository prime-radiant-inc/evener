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

func loadChildRegistry(t *testing.T, env []string) *registry.Registry {
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
	return r
}

func resolveChildProvider(t *testing.T, env []string, ref string) registry.Resolved {
	t.Helper()
	r := loadChildRegistry(t, env)
	resolved, err := r.Resolve(ref)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func resolveChildInstanceTransport(t *testing.T, env []string, name string) registry.Resolved {
	t.Helper()
	resolved, err := loadChildRegistry(t, env).ResolveInstanceTransport(name)
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

// endpointVarProviderConfig carries a custom endpoint variable: the URL
// template reads WORK_ENDPOINT, vars_env lets the environment choose it, and
// the provider file supplies the safe default the project layer must not
// override.
const endpointVarProviderConfig = `default = "work"
[providers.work]
base = "openai-compatible"
base_url = "{WORK_ENDPOINT}"
vars = { WORK_ENDPOINT = "http://127.0.0.1:9/v1" }
vars_env = { WORK_ENDPOINT = "WORK_ENDPOINT_URL" }
default_model = "house"
[providers.work.models.house]
wire_id = "wire-house"
`

const endpointDaemonScriptTemplate = `#!/bin/sh
if [ "$1" = "launch-check" ]; then
 printf '@VAR@=%s\nPROJECT_KEPT=%s\n' "$@VAR@" "$PROJECT_KEPT" > "$(dirname "$0")/launchcheck-env"
 printf '{"protocol":"evener-appwire-v7","launch_flags":["api-log"]}\n'
 exit 0
fi
mkdir -p "$EVENER_RUN_DIR"
printf 'EVENER_PROVIDERS_CONFIG=%s\nEVENER_CREDENTIALS_CONFIG=%s\n@VAR@=%s\nPROJECT_KEPT=%s\n' "$EVENER_PROVIDERS_CONFIG" "$EVENER_CREDENTIALS_CONFIG" "$@VAR@" "$PROJECT_KEPT" > "$EVENER_RUN_DIR/child-env"
cat > "$EVENER_RUN_DIR/$$.json" <<RENDEZVOUS
{"pid":$$,"address":"127.0.0.1:1","started_at":"2999-01-01T00:00:00Z"}
RENDEZVOUS
exec tail -f /dev/null
`

func endpointDaemonScript(variable string) string {
	return strings.ReplaceAll(endpointDaemonScriptTemplate, "@VAR@", variable)
}

// alternateCopilotProviderConfig is a trusted child provider file whose
// Copilot endpoint reads COPILOT_UPSTREAM_URL instead of the built-in
// GITHUB_COPILOT_BASE_URL. The hub's own registry never sees this mapping.
const alternateCopilotProviderConfig = `[providers.cp]
base = "github-copilot"
vars_env = { BASE_URL = "COPILOT_UPSTREAM_URL" }
`

// TestSpawnProjectLayerCannotRedirectCustomProviderEndpoint is the reported
// bypass: the built-in Copilot endpoint and provider-file keys are refused from
// the project layer, but a provider config can route its base URL through any
// environment variable. A checkout-controlled launch file must not choose that
// variable either, for the child environment, the launch-contract check, or the
// registry resolution both of them see.
func TestSpawnProjectLayerCannotRedirectCustomProviderEndpoint(t *testing.T) {
	f := retainedProviderFixture(t, endpointVarProviderConfig)
	root := t.TempDir()
	binary := filepath.Join(root, "evener")
	runDir := filepath.Join(root, "run")
	writeFakeEvener(t, binary, endpointDaemonScript("WORK_ENDPOINT_URL"))

	projectEnv := map[string]string{
		"WORK_ENDPOINT_URL": "http://127.0.0.1:10/v1",
		"PROJECT_KEPT":      "kept",
	}
	h := &HubSpawner{Cfg: DefaultConfig(), Registry: f.ctl.reg, ProvidersConfigPath: f.tomlPath, CredentialsPath: f.credsPath, EvenerBinary: binary, RunDir: runDir}
	entry, err := h.Spawn(t.Context(), hubcore.SpawnRequest{
		Provider: "work", StateDir: f.stateDir, WorkingDir: root,
		Resolved: launchconfig.Resolved{
			Effective: launchconfig.Layer{Model: "work/house", Env: projectEnv},
			Layers: map[launchconfig.LayerName]launchconfig.Layer{
				launchconfig.LayerProject: {Env: projectEnv},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(entry.PID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Kill() })

	raw, err := os.ReadFile(filepath.Join(runDir, "child-env"))
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if got, _ := envLookup(env, "WORK_ENDPOINT_URL"); got != "" {
		t.Fatalf("child WORK_ENDPOINT_URL = %q, want unset: a checkout-controlled launch file chose the provider endpoint", got)
	}
	if got, _ := envLookup(env, "PROJECT_KEPT"); got != "kept" {
		t.Fatalf("child PROJECT_KEPT = %q, want the unrelated project key preserved", got)
	}
	if got := resolveChildProvider(t, env, "work/house"); got.Transport.BaseURL != "http://127.0.0.1:9/v1" {
		t.Fatalf("child resolved provider endpoint = %q, want the user provider file's endpoint", got.Transport.BaseURL)
	}

	launchRaw, err := os.ReadFile(filepath.Join(root, "launchcheck-env"))
	if err != nil {
		t.Fatal(err)
	}
	launchEnv := strings.Split(strings.TrimSpace(string(launchRaw)), "\n")
	if got, _ := envLookup(launchEnv, "WORK_ENDPOINT_URL"); got != "" {
		t.Fatalf("launch-check WORK_ENDPOINT_URL = %q, want unset: the preflight judged a redirected endpoint", got)
	}
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}
}

func readDaemonEnv(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func assertAlternateProviderChildEnv(t *testing.T, childEnvPath, launchCheckPath, key, wantEnv, wantResolved string) {
	t.Helper()
	childEnv := readDaemonEnv(t, childEnvPath)
	if got, _ := envLookup(childEnv, key); got != wantEnv {
		t.Fatalf("child %s = %q, want %q: a checkout-controlled launch file chose the child's provider endpoint", key, got, wantEnv)
	}
	if got, _ := envLookup(childEnv, "PROJECT_KEPT"); got != "kept" {
		t.Fatalf("child PROJECT_KEPT = %q, want the unrelated project key preserved", got)
	}
	if got := resolveChildInstanceTransport(t, childEnv, "cp").Transport.BaseURL; got != wantResolved {
		t.Fatalf("child resolved Copilot endpoint = %q, want %q", got, wantResolved)
	}
	launchCheckEnv := readDaemonEnv(t, launchCheckPath)
	if got, _ := envLookup(launchCheckEnv, key); got != wantEnv {
		t.Fatalf("launch-check %s = %q, want %q: the preflight judged a redirected endpoint", key, got, wantEnv)
	}
	if got, _ := envLookup(launchCheckEnv, "PROJECT_KEPT"); got != "kept" {
		t.Fatalf("launch-check PROJECT_KEPT = %q, want the unrelated project key preserved", got)
	}
}

// TestSpawnAlternateProviderFileKeepsTrustedCopilotEndpoint is the reported
// bypass for a child whose trusted provider file is not the hub's own. The
// alternate file maps the Copilot endpoint to COPILOT_UPSTREAM_URL, but
// exclusions derived from the hub registry never saw that mapping, so a
// checkout-controlled launch file chooses the child's endpoint, the
// launch-check's endpoint, and the registry resolution both of them make.
func TestSpawnAlternateProviderFileKeepsTrustedCopilotEndpoint(t *testing.T) {
	clearProviderKeysFromEnvironment(t)
	if value, ok := os.LookupEnv("COPILOT_UPSTREAM_URL"); ok {
		t.Cleanup(func() { _ = os.Setenv("COPILOT_UPSTREAM_URL", value) })
		if err := os.Unsetenv("COPILOT_UPSTREAM_URL"); err != nil {
			t.Fatal(err)
		}
	}
	const (
		key          = "COPILOT_UPSTREAM_URL"
		trusted      = "https://trusted.example.test"
		checkout     = "https://checkout.example.test"
		launch       = "https://launch.example.test"
		global       = "https://global.example.test"
		defaultCPURL = "https://api.githubcopilot.com"
	)
	f := newInstancesFixture(t, map[string]string{})
	if err := os.WriteFile(f.tomlPath, []byte(`default = "work"
[providers.work]
base = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.reg.Reload(); err != nil {
		t.Fatalf("hub registry reload: %v", err)
	}
	alternate := filepath.Join(t.TempDir(), "providers.toml")
	if err := os.WriteFile(alternate, []byte(alternateCopilotProviderConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	projectEnv := map[string]string{key: checkout, "PROJECT_KEPT": "kept"}
	launchEnv := map[string]string{key: launch, "PROJECT_KEPT": "kept"}
	cases := []struct {
		name         string
		layers       map[launchconfig.LayerName]launchconfig.Layer
		effectiveEnv map[string]string
		parent       string
		wantEnv      string
		wantResolved string
	}{
		{
			name: "global override",
			layers: map[launchconfig.LayerName]launchconfig.Layer{
				launchconfig.LayerGlobal:  {Env: map[string]string{key: trusted, "GLOBAL_KEPT": "global"}},
				launchconfig.LayerProject: {Env: projectEnv},
			},
			effectiveEnv: projectEnv,
			wantEnv:      trusted,
			wantResolved: trusted,
		},
		{
			name: "trusted repo override",
			layers: map[launchconfig.LayerName]launchconfig.Layer{
				launchconfig.LayerGlobal:  {Env: map[string]string{key: global, "GLOBAL_KEPT": "global"}},
				launchconfig.LayerRepo:    {Env: map[string]string{key: trusted}},
				launchconfig.LayerProject: {Env: projectEnv},
			},
			effectiveEnv: projectEnv,
			wantEnv:      trusted,
			wantResolved: trusted,
		},
		{
			name: "per-launch override",
			layers: map[launchconfig.LayerName]launchconfig.Layer{
				launchconfig.LayerProject: {Env: projectEnv},
				launchconfig.LayerLaunch:  {Env: launchEnv},
			},
			effectiveEnv: launchEnv,
			wantEnv:      launch,
			wantResolved: launch,
		},
		{
			name: "parent override",
			layers: map[launchconfig.LayerName]launchconfig.Layer{
				launchconfig.LayerProject: {Env: projectEnv},
			},
			effectiveEnv: projectEnv,
			parent:       trusted,
			wantEnv:      trusted,
			wantResolved: trusted,
		},
		{
			name: "project only",
			layers: map[launchconfig.LayerName]launchconfig.Layer{
				launchconfig.LayerProject: {Env: projectEnv},
			},
			effectiveEnv: projectEnv,
			wantEnv:      "",
			wantResolved: defaultCPURL,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.parent != "" {
				t.Setenv(key, tc.parent)
			}
			root := t.TempDir()
			binary := filepath.Join(root, "evener")
			runDir := filepath.Join(root, "run")
			writeFakeEvener(t, binary, endpointDaemonScript(key))
			h := &HubSpawner{Cfg: DefaultConfig(), Registry: f.ctl.reg, ProvidersConfigPath: alternate, CredentialsPath: f.credsPath, EvenerBinary: binary, RunDir: runDir}
			resolved := launchconfig.Resolved{
				Effective: launchconfig.Layer{Model: "cp", Env: tc.effectiveEnv},
				Layers:    tc.layers,
			}
			entry, err := h.Spawn(t.Context(), hubcore.SpawnRequest{
				StateDir: f.stateDir, WorkingDir: root,
				Resolved: resolved,
			})
			if err != nil {
				t.Fatal(err)
			}
			process, err := os.FindProcess(entry.PID)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = process.Kill() })

			assertAlternateProviderChildEnv(t, filepath.Join(runDir, "child-env"), filepath.Join(root, "launchcheck-env"), key, tc.wantEnv, tc.wantResolved)

			resumeRunDir := filepath.Join(root, "resume-run")
			h.RunDir = resumeRunDir
			entry, err = h.Resume(t.Context(), hubcore.ResumeRequest{
				SessionID: "01JALTERNATE", StateDir: f.stateDir, WorkingDir: root,
				Resolved: resolved,
			})
			if err != nil {
				t.Fatal(err)
			}
			process, err = os.FindProcess(entry.PID)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = process.Kill() })
			assertAlternateProviderChildEnv(t, filepath.Join(resumeRunDir, "child-env"), filepath.Join(root, "launchcheck-env"), key, tc.wantEnv, tc.wantResolved)
		})
	}
}
