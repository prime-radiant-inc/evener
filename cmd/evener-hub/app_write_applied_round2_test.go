package hub

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/internal/plugins"
	"primeradiant.com/evener/llm/registry"
)

// The hub's classifier is what carries the manager's answer to the handler: a
// store the manager changed is an applied write, and a refusal is not. Without
// this the marketplace rename that leaves the store between the two names
// (internal/plugins, ErrStoreChanged) reaches the handler as a plain error and
// nothing is broadcast, so every client keeps a listing the store no longer
// matches.
func TestPluginWriteErrorMarksAChangedStore(t *testing.T) {
	changed := pluginWriteError(errors.Join(errors.New("saving known_marketplaces.json failed"), plugins.ErrStoreChanged))
	if !writeDidApply(changed) {
		t.Fatalf("pluginWriteError(store changed) = %v, want an applied write", changed)
	}
	refusal := pluginWriteError(plugins.ErrMarketplaceNotFound)
	if writeDidApply(refusal) {
		t.Fatalf("pluginWriteError(refusal) = %v, want a refusal that is not announced", refusal)
	}
	// The refusal class still reaches the wire through the same call.
	if _, ok := errors.AsType[appwire.WireError](refusal); !ok {
		t.Fatalf("pluginWriteError(refusal) = %v (%T), want the wire refusal class kept", refusal, refusal)
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
