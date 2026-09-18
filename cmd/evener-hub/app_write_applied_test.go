package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
)

// blockProvidersWrites leaves a directory where WriteConfigFile stages its
// bytes, so every later write of providers.toml fails on a real filesystem
// refusal (the technique breakCredentialWrites uses on the credentials file).
func blockProvidersWrites(t *testing.T, tomlPath string) {
	t.Helper()
	// The reload this runs from can be attempted more than once in a call, and
	// the path only has to be occupied, not freshly created.
	if err := os.Mkdir(tomlPath+".tmp", 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		t.Errorf("blocking the providers.toml temp path: %v", err)
	}
}

// instanceRollbackFixture serves an RPC hub whose registry reloads normally
// until failReload is set, and then fails the reload after blocking the
// rollback write that follows it — the double failure #1543 is about, which
// leaves providers.toml carrying a change no rollback could undo.
type instanceRollbackFixture struct {
	hub        *httptest.Server
	tomlPath   string
	failReload *atomic.Bool
}

func newInstanceRollbackFixture(t *testing.T) *instanceRollbackFixture {
	t.Helper()
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "providers.toml")
	writeMinimalProvidersToml(t, tomlPath)
	credsStore := newTestCredentialsStore(t)
	failReload := &atomic.Bool{}
	load := testRegistryLoader(t.TempDir(), tomlPath, credsStore, nil)
	reg := hubcore.NewProviderRegistry(func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		if failReload.Load() {
			blockProvidersWrites(t, tomlPath)
			return nil, nil, errors.New("the registry refused to load")
		}
		return load(extra...)
	})
	if err := reg.Reload(); err != nil {
		t.Fatalf("registry: %v", err)
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past:                hubcore.NewPastIndex(""),
		Registry:            reg,
		ProvidersConfigPath: tomlPath,
		HubStateRoot:        dir,
		CredsStore:          credsStore,
	})
	t.Cleanup(hub.Close)
	return &instanceRollbackFixture{hub: hub, tomlPath: tomlPath, failReload: failReload}
}

// waitForAuthUpdatedBroadcast fails the test unless evener/auth/updated
// arrives, which is what tells every other client its instance list is stale.
func waitForAuthUpdatedBroadcast(t *testing.T, client *appwire.Client, what string) {
	t.Helper()
	for {
		select {
		case got := <-client.Notifications():
			if got.Method == appwire.NotifyEvenerAuthUpdated {
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for evener/auth/updated after %s", what)
		}
	}
}

// A create whose reload failed and whose rollback could not be written leaves
// the new instance in providers.toml: the config changed, so every other
// client's list is stale and the hub owes them evener/auth/updated, even
// though this caller is told the create failed (#1543).
func TestHubRPCInstanceCreateBroadcastsWhenTheRollbackCouldNotBeWritten(t *testing.T) {
	f := newInstanceRollbackFixture(t)
	client := dialHubRPC(t, f.hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	f.failReload.Store(true)

	var resp appwire.InstanceListResponse
	err := client.Request(context.Background(), appwire.MethodEvenerInstanceCreate,
		appwire.InstanceCreateParams{Base: "anthropic", Name: "mywork"}, &resp)
	if err == nil {
		t.Fatal("evener/instance/create = nil, want the failed rollback reported")
	}
	if !strings.Contains(err.Error(), "restoring the previous config failed") {
		t.Fatalf("evener/instance/create = %v, want the double failure this test is about", err)
	}
	waitForAuthUpdatedBroadcast(t, client, "a create whose rollback could not be written")
}

// The removal sibling of the create case above: the entry is gone from the
// config and the rollback could not put it back, so the removal stands and
// every other client is holding a list with an instance that no longer exists.
func TestHubRPCInstanceRemoveBroadcastsWhenTheRollbackCouldNotBeWritten(t *testing.T) {
	f := newInstanceRollbackFixture(t)
	client := dialHubRPC(t, f.hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	var created appwire.InstanceListResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerInstanceCreate,
		appwire.InstanceCreateParams{Base: "anthropic", Name: "doomed"}, &created); err != nil {
		t.Fatalf("evener/instance/create: %v", err)
	}
	waitForAuthUpdatedBroadcast(t, client, "the create this removal undoes")
	f.failReload.Store(true)

	var resp appwire.InstanceListResponse
	err := client.Request(context.Background(), appwire.MethodEvenerInstanceRemove,
		appwire.InstanceRemoveParams{Name: "doomed"}, &resp)
	if err == nil {
		t.Fatal("evener/instance/remove = nil, want the failed rollback reported")
	}
	if !strings.Contains(err.Error(), "the removal stands in the config") {
		t.Fatalf("evener/instance/remove = %v, want the double failure this test is about", err)
	}
	waitForAuthUpdatedBroadcast(t, client, "a removal whose rollback could not be written")
}

// The cleanup's own failure can also fail to restore what it already
// deleted: TestInstances_RemoveRestoresTheStoredKeyWhenTheOAuthRecordCannotBeDeleted
// covers the OAuth delete failing after the stored key is already gone, with
// a clean restore of that key. This is its double-failure sibling - putting
// the key back also fails - so the key stays deleted even though [providers.work]
// never moved, and every other client's credential status for it is stale.
func TestInstances_RemoveMarksAppliedWhenTheDeletedCredentialCannotBeRestored(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := authopenai.SaveAuth(f.stateDir, "work", makeOAuthRecord("work", "")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	f.ctl.auth.deleteAuth = func(string, string) (bool, error) { return false, errors.New("delete refused") }
	f.ctl.auth.setCredential = func(string, string) error { return errors.New("restore refused") }

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})
	if err == nil {
		t.Fatal("Remove = nil, want the cleanup and restore failures reported")
	}
	if !writeDidApply(err) {
		t.Fatalf("Remove = %v (%T), want an applied write: the stored key stayed deleted", err, err)
	}
}

// TestInstances_RemoveRestoresCredentialsWhenTheConfigWriteFails covers the
// config write failing with a clean credential restore. This is its
// double-failure sibling: the credentials stay deleted even though
// [providers.work] never moved, so every other client's credential status
// for it is stale.
func TestInstances_RemoveMarksAppliedWhenTheConfigWriteFailsAndTheCredentialCannotBeRestored(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-codex"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := authopenai.SaveAuth(f.stateDir, "work", makeOAuthRecord("work", "")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	originalDelete := f.ctl.auth.deleteAuth
	f.ctl.auth.deleteAuth = func(dir, name string) (bool, error) {
		if err := os.Remove(f.tomlPath); err != nil {
			t.Errorf("Remove(%s): %v", f.tomlPath, err)
		}
		if err := os.Mkdir(f.tomlPath, 0o700); err != nil {
			t.Errorf("Mkdir(%s): %v", f.tomlPath, err)
		}
		return originalDelete(dir, name)
	}
	f.ctl.auth.setCredential = func(string, string) error { return errors.New("restore refused") }

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})
	if err == nil {
		t.Fatal("Remove = nil, want the config write failure reported")
	}
	if !writeDidApply(err) {
		t.Fatalf("Remove = %v (%T), want an applied write: the credentials stayed deleted", err, err)
	}
}

// TestInstances_RemoveRestoresTheCredentialBeforeTheRollbackReload covers the
// removal's reload failing, the config rollback landing, and the rollback's
// own reload succeeding, with a clean credential restore - the removal never
// applied, so that test wants a plain refusal. This is its double-failure
// sibling: the credential restore itself fails, so the key stays deleted
// even though [providers.work] is back, and every other client's credential
// status for it is stale.
func TestInstances_RemoveMarksAppliedWhenTheRollbackSucceedsButTheCredentialCannotBeRestored(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// The layer the removal's reload is made to fail on: an entry that parses
	// but cannot resolve an endpoint (#711), the same technique
	// TestInstances_RemoveRestoresTheCredentialBeforeTheRollbackReload uses.
	brokenPath := filepath.Join(filepath.Dir(f.tomlPath), "broken.toml")
	if err := os.WriteFile(brokenPath, []byte("[providers.standalone]\nprotocol = \"openai-chat\"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	var loads int
	loadFn := func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		loads++
		store, err := credentials.LoadStore(f.credsPath)
		if err != nil {
			return nil, nil, err
		}
		path := f.tomlPath
		if loads == 2 {
			path = brokenPath
		}
		opts := append(
			testProbeRegistryOptions(f.stateDir, store, func(string) (string, bool) { return "", false }),
			registry.WithConfigPath(path),
		)
		r, err := registry.Load(append(opts, extra...)...)
		return r, store, err
	}
	replacement := hubcore.NewProviderRegistry(loadFn)
	f.ctl.reg = replacement
	f.ctl.auth.reg = replacement
	if err := replacement.Reload(); err != nil {
		t.Fatalf("prime Reload: %v", err)
	}
	f.ctl.auth.setCredential = func(string, string) error { return errors.New("restore refused") }

	err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"})
	if err == nil {
		t.Fatal("Remove = nil, want the reload failure reported")
	}
	if !writeDidApply(err) {
		t.Fatalf("Remove = %v (%T), want an applied write: the stored key stayed deleted despite the config rollback", err, err)
	}
}

// SetLayer persists the layer and then resolves the effective view. The file is
// written before the resolution runs, so a resolution that fails leaves every
// other client's launch config stale — the save applied.
func TestLaunch_SetLayerWhoseResolutionFailsIsStillApplied(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()
	ctl := &hubLaunchController{stateRoot: root, getenv: func(string) string { return "" }}
	original := hubLaunchResolve
	t.Cleanup(func() { hubLaunchResolve = original })
	hubLaunchResolve = func(string, string, launchconfig.Layer) (launchconfig.Resolved, error) {
		return launchconfig.Resolved{}, errors.New("the launch config could not be resolved")
	}

	_, err := ctl.SetLayer(context.Background(), appwire.LaunchConfigSetLayerParams{
		CWD:    cwd,
		Layer:  "global",
		Config: appwire.LaunchConfigLayer{},
	})
	if err == nil {
		t.Fatal("SetLayer = nil, want the failed resolution reported")
	}
	if !writeDidApply(err) {
		t.Fatalf("SetLayer = %v (%T), want an applied write: the layer is already on disk", err, err)
	}
}

// SetDefault writes the new default and then reloads the registry. A failed
// reload leaves the new default on disk, so it is applied however the call ends.
func TestInstances_SetDefaultWhoseReloadFailsIsStillApplied(t *testing.T) {
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	before, err := os.ReadFile(f.tomlPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// An entry that parses but cannot resolve an endpoint (#711): the registry
	// loaded before it appeared, so the write still lands, and the reload that
	// follows it fails.
	raw := append(append([]byte(nil), before...), []byte("\n[providers.standalone]\nprotocol = \"openai-chat\"\n")...)
	if err := os.WriteFile(f.tomlPath, raw, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	err = f.ctl.SetDefault(appwire.InstanceSetDefaultParams{Name: "work"})
	if err == nil {
		t.Fatal("SetDefault = nil, want the failed reload reported")
	}
	if !writeDidApply(err) {
		t.Fatalf("SetDefault = %v (%T), want an applied write: the default is already on disk", err, err)
	}
	after, _, err := registry.ReadConfigFile(f.tomlPath)
	if err != nil {
		t.Fatalf("re-reading providers.toml: %v", err)
	}
	if after.Default != "work" {
		t.Fatalf("providers.toml default = %q, want the write this call made to stand", after.Default)
	}
}

// LoginComplete saves the OAuth record and then reads the instance's status.
// The record is on disk before the read, so a read that fails still leaves
// every other client's provider list stale.
func TestAuth_LoginCompleteWhoseStatusReadFailsIsStillApplied(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	ctrl := newHubAuthController()
	ctrl.stateDir = t.TempDir()
	attachTestRegistry(t, ctrl)
	ctrl.cfg = authopenai.Config{IssuerBaseURL: "https://auth.example.test"}
	ctrl.client = &http.Client{}
	ctrl.exchangeCode = func(_ context.Context, _ *http.Client, _ authopenai.Config, _ authopenai.TokenExchangeRequest) (authopenai.TokenSet, error) {
		return authopenai.TokenSet{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			IDToken:      hubAuthTestJWT(t, map[string]any{"email": "oauth@example.com"}),
			TokenType:    "Bearer",
			Scope:        "openid profile email",
			Expiry:       time.Now().Add(time.Hour),
		}, nil
	}
	start, err := ctrl.LoginStart(appwire.AuthLoginStartParams{Provider: "openai-codex"})
	if err != nil {
		t.Fatalf("LoginStart: %v", err)
	}
	state := mustAuthorizeState(t, start.URL)
	// The status read runs after the record is saved, and this is the read it
	// makes: a record that cannot be read at all (neither absent nor corrupt)
	// is the failure the status call passes on.
	saved := false
	realSave := ctrl.saveAuth
	ctrl.saveAuth = func(dir, name string, rec authopenai.AuthRecord) error {
		err := realSave(dir, name, rec)
		if err == nil {
			saved = true
		}
		return err
	}
	realLoad := ctrl.loadAuth
	ctrl.loadAuth = func(dir, name string) (authopenai.AuthRecord, error) {
		if saved {
			return authopenai.AuthRecord{}, errors.New("the auth record could not be read")
		}
		return realLoad(dir, name)
	}

	_, err = ctrl.LoginComplete(context.Background(), appwire.AuthLoginCompleteParams{
		Provider:    "openai-codex",
		FlowID:      start.FlowID,
		RedirectURL: "http://localhost:1455/auth/callback?code=auth-code&state=" + url.QueryEscape(state),
	})
	if err == nil {
		t.Fatal("LoginComplete = nil, want the failed status read reported")
	}
	if !writeDidApply(err) {
		t.Fatalf("LoginComplete = %v (%T), want an applied write: the record is already saved", err, err)
	}
	// Read through the real loader, so this is the state directory's answer and
	// not the failing hook's: without a saved record this would not be the
	// applied-then-failed case at all.
	if _, loadErr := realLoad(ctrl.stateDir, "openai-codex"); loadErr != nil {
		t.Fatalf("the OAuth record is not readable (%v), so this test is not the applied-then-failed case", loadErr)
	}
}

// mustAuthorizeState pulls the flow state out of an authorize URL.
func mustAuthorizeState(t *testing.T, authorizeURL string) string {
	t.Helper()
	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatalf("authorize URL %q carries no state", authorizeURL)
	}
	return state
}
