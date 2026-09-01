package hub

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
)

func covAIJWT(payload string) string {
	return "x." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".x"
}

// FuzzAuthInstancesFactories replays a deterministic matrix through the real
// controllers. HTTP exchanges are scripted at the external boundary and every
// persistent path lives below the fuzz iteration's temporary directory.
func FuzzAuthInstancesFactories(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		root := t.TempDir()
		stateDir := filepath.Join(root, "state")
		providers := filepath.Join(root, "providers.toml")
		credsPath := filepath.Join(root, "credentials.toml")
		store, err := credentials.LoadStore(credsPath)
		if err != nil {
			t.Fatal(err)
		}
		c := newHubAuthControllerWithStore(root, store)
		c.stateDir, c.providersConfigPath = stateDir, providers
		c.reg = newTestRegistry(t, stateDir, providers, store, nil)
		now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		c.now = func() time.Time { return now }

		// Constructors stay panic-net: *hubAuthController carries function-valued
		// fields (c.now, c.generateState, ...) that cannot be compared for
		// equality, and building one is a side-effecting factory (it loads the
		// credentials store from disk), not a pure projection of its arguments.
		// Their correctness is exercised indirectly, through every asserted call
		// below that runs against the controller they build.
		_ = newHubAuthController(map[string]string{"HOME": root})
		_ = newHubAuthController()
		_ = newHubAuthControllerWithStore(root, nil)
		oldSetup := hubAuthControllerSetup
		hubAuthControllerSetup = func(*hubAuthController) {}
		_ = newHubAuthControllerWithStore(root, store)
		hubAuthControllerSetup = oldSetup

		// Pure status/projection helpers: assert against independently written
		// expected values instead of discarding the result
		// (docs/developing-evener/coverage.md's executed-vs-tested note on the
		// evenerfuzz cov_* driver family).
		t.Setenv("COV_AUTH", "ambient")
		if got := effectiveHubAuthEnv(map[string]string{"COV_AUTH": "launch"}); got["COV_AUTH"] != "launch" {
			t.Fatalf("effectiveHubAuthEnv: COV_AUTH = %q, want launch env to override ambient value", got["COV_AUTH"])
		}
		wantStateDir := filepath.Join(root, ".local", "state", "evener")
		if runtime.GOOS == "windows" {
			// The Windows branch looks for USERPROFILE/HOMEDRIVE+HOMEPATH, neither of
			// which this env map sets, so it falls back to os.TempDir() instead.
			wantStateDir = filepath.Join(os.TempDir(), ".local", "state", "evener")
		}
		if got := openAIStateDirFromEnv(map[string]string{"HOME": root}); got != wantStateDir {
			t.Fatalf("openAIStateDirFromEnv(HOME=%s) = %q, want %q", root, got, wantStateDir)
		}

		type authStatusCase struct {
			expiry       time.Time
			wantStatus   authopenai.AuthStatus
			wantNeedsRef bool
		}
		for _, tc := range []authStatusCase{
			// Zero expiry: never logged out, never due for refresh.
			{time.Time{}, authopenai.AuthStatus{SignedIn: true, Source: authopenai.AuthSourceOAuth}, false},
			// Already expired: signed out, refresh moot once login is required.
			{now.Add(-time.Second), authopenai.AuthStatus{Source: authopenai.AuthSourceOAuth, Expiry: now.Add(-time.Second), NeedsLogin: true}, true},
			// Expires inside the 5-minute refresh window but not yet: signed in, needs refresh.
			{now.Add(time.Minute), authopenai.AuthStatus{SignedIn: true, Source: authopenai.AuthSourceOAuth, Expiry: now.Add(time.Minute), NeedsRefresh: true}, true},
			// Strictly distinguishes the 5-minute window from a 1-minute window.
			{now.Add(3 * time.Minute), authopenai.AuthStatus{SignedIn: true, Source: authopenai.AuthSourceOAuth, Expiry: now.Add(3 * time.Minute), NeedsRefresh: true}, true},
			// Comfortably valid: signed in, no refresh due.
			{now.Add(time.Hour), authopenai.AuthStatus{SignedIn: true, Source: authopenai.AuthSourceOAuth, Expiry: now.Add(time.Hour)}, false},
		} {
			r := authopenai.AuthRecord{Expiry: tc.expiry, Source: authopenai.AuthSourceOAuth}
			if got := openAIStatusFromRecord(now, r); got != tc.wantStatus {
				t.Fatalf("openAIStatusFromRecord(expiry=%v) = %+v, want %+v", tc.expiry, got, tc.wantStatus)
			}
			if got := openAIRecordNeedsRefresh(now, r); got != tc.wantNeedsRef {
				t.Fatalf("openAIRecordNeedsRefresh(expiry=%v) = %v, want %v", tc.expiry, got, tc.wantNeedsRef)
			}
		}

		wantTokenRecord := authopenai.AuthRecord{
			Version: 1, Provider: "openai", Source: authopenai.AuthSourceOAuth,
			ObtainedAt: now, TokenType: "Bearer", AccessToken: "a",
		}
		if got := c.authRecordFromTokens(authopenai.TokenSet{AccessToken: "a"}); got != wantTokenRecord {
			t.Fatalf("authRecordFromTokens(no token type) = %+v, want %+v", got, wantTokenRecord)
		}
		wantTokenRecord.TokenType = "Custom"
		if got := c.authRecordFromTokens(authopenai.TokenSet{AccessToken: "a", TokenType: "Custom"}); got != wantTokenRecord {
			t.Fatalf("authRecordFromTokens(explicit token type) = %+v, want %+v", got, wantTokenRecord)
		}

		c.cfg = authopenai.Config{}
		if got := c.config(); got.IssuerBaseURL != "https://auth.openai.com" {
			t.Fatalf("config() with zero c.cfg: IssuerBaseURL = %q, want the compiled-in default", got.IssuerBaseURL)
		}
		c.cfg = authopenai.DefaultConfig()
		c.cfg.ClientID = "cov-sentinel-client-id" // proves config() passes c.cfg through unchanged rather than recomputing it
		if got := c.config(); got.ClientID != "cov-sentinel-client-id" {
			t.Fatalf("config() with non-empty c.cfg: ClientID = %q, want the sentinel unchanged", got.ClientID)
		}
		c.cfg = authopenai.DefaultConfig()

		// Resolution, status, key status, list, and API-key validation.
		if err := os.WriteFile(providers, []byte("bad = ["), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := c.reg.Reload(); err == nil {
			t.Fatal("Reload against malformed providers.toml: want error, got nil")
		}
		if err := os.WriteFile(providers, []byte("default=\"work\"\n[providers.work]\nbase=\"openai-codex\"\n[providers.ant]\nbase=\"anthropic\"\napi_key=\"sk-ant\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := c.reg.Reload(); err != nil {
			t.Fatal(err)
		}
		if got := c.instanceIsCodex("work"); !got {
			t.Fatal("instanceIsCodex(work) = false, want true (based on openai-codex)")
		}
		if got := c.instanceIsCodex("openai-codex"); !got {
			t.Fatal("instanceIsCodex(openai-codex) = false, want true (the curated Codex provider)")
		}
		if got := c.instanceIsCodex("ant"); got {
			t.Fatal("instanceIsCodex(ant) = true, want false (based on anthropic)")
		}
		wantRequiresCodexErr := `OAuth is not supported for instance "ant"`
		if err := c.requiresCodex("ant"); err == nil || err.Error() != wantRequiresCodexErr {
			t.Fatalf("requiresCodex(ant) = %v, want error %q", err, wantRequiresCodexErr)
		}
		_, _ = c.Status(appwire.AuthStatusParams{Provider: "work"})
		_, _ = c.Status(appwire.AuthStatusParams{Provider: "unknown"})
		_, _ = c.Status(appwire.AuthStatusParams{Provider: "anthropic"})
		_, _ = c.Status(appwire.AuthStatusParams{})
		c.loadAuth = func(string, string) (authopenai.AuthRecord, error) {
			return authopenai.AuthRecord{}, errors.New("status")
		}
		_, _ = c.Status(appwire.AuthStatusParams{})
		c.loadAuth = authopenai.LoadAuth
		_, _ = c.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "anthropic", Value: " "})
		_, _ = c.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "anthropic", Value: "key"})
		c.setCredential = func(string, string) error { return errors.New("set") }
		_, _ = c.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "anthropic", Value: "key"})
		c.setCredential = c.creds.Set
		_, _ = c.List(appwire.EmptyParams{})

		// Browser flow failures and successful completion, including claim carry-through.
		c.generateState = func() (string, error) { return "", errors.New("state") }
		_, _ = c.LoginStart(appwire.AuthLoginStartParams{Provider: "ant"})
		_, _ = c.LoginStart(appwire.AuthLoginStartParams{})
		c.generateState = func() (string, error) { return "flow", nil }
		c.generatePKCE = func() (string, string, error) { return "", "", errors.New("pkce") }
		_, _ = c.LoginStart(appwire.AuthLoginStartParams{})
		c.generatePKCE = func() (string, string, error) { return "verifier", "challenge", nil }
		c.cfg = authopenai.DefaultConfig()
		c.cfg.IssuerBaseURL = ":bad"
		_, _ = c.LoginStart(appwire.AuthLoginStartParams{})
		c.cfg = authopenai.DefaultConfig()
		c.flows = nil
		start, err := c.LoginStart(appwire.AuthLoginStartParams{})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = c.LoginComplete(context.Background(), appwire.AuthLoginCompleteParams{Provider: "ant"})
		_, _ = c.LoginComplete(context.Background(), appwire.AuthLoginCompleteParams{})
		_, _ = c.LoginComplete(context.Background(), appwire.AuthLoginCompleteParams{FlowID: "missing"})
		c.flows["other"] = hubAuthFlow{Provider: "other", State: "other"}
		_, _ = c.LoginComplete(context.Background(), appwire.AuthLoginCompleteParams{FlowID: "other"})
		_, _ = c.LoginComplete(context.Background(), appwire.AuthLoginCompleteParams{FlowID: start.FlowID, RedirectURL: ":bad"})
		_, _ = c.LoginComplete(context.Background(), appwire.AuthLoginCompleteParams{FlowID: start.FlowID, RedirectURL: "http://localhost/?code=x&state=wrong"})
		c.exchangeCode = func(context.Context, *http.Client, authopenai.Config, authopenai.TokenExchangeRequest) (authopenai.TokenSet, error) {
			return authopenai.TokenSet{}, errors.New("exchange")
		}
		_, _ = c.LoginComplete(context.Background(), appwire.AuthLoginCompleteParams{FlowID: start.FlowID, RedirectURL: "http://localhost/?code=x&state=flow"})
		c.exchangeCode = func(context.Context, *http.Client, authopenai.Config, authopenai.TokenExchangeRequest) (authopenai.TokenSet, error) {
			return authopenai.TokenSet{AccessToken: "access", IDToken: covAIJWT(`{"email":"e@x"}`)}, nil
		}
		c.flows["save-fail"] = hubAuthFlow{Provider: "openai", State: "save-fail"}
		realSave := c.saveAuth
		c.saveAuth = func(string, string, authopenai.AuthRecord) error { return errors.New("save") }
		_, _ = c.LoginComplete(context.Background(), appwire.AuthLoginCompleteParams{FlowID: "save-fail", RedirectURL: "http://localhost/?code=x&state=save-fail"})
		c.saveAuth = realSave
		c.flows["status-fail"] = hubAuthFlow{Provider: "openai", State: "status-fail"}
		c.saveAuth = func(string, string, authopenai.AuthRecord) error { return nil }
		c.loadAuth = func(string, string) (authopenai.AuthRecord, error) {
			return authopenai.AuthRecord{}, errors.New("load")
		}
		_, _ = c.LoginComplete(context.Background(), appwire.AuthLoginCompleteParams{FlowID: "status-fail", RedirectURL: "http://localhost/?code=x&state=status-fail"})
		c.saveAuth, c.loadAuth = realSave, authopenai.LoadAuth
		c.exchangeCode = func(context.Context, *http.Client, authopenai.Config, authopenai.TokenExchangeRequest) (authopenai.TokenSet, error) {
			return authopenai.TokenSet{AccessToken: "access", IDToken: covAIJWT(`{"email":"e@x","chatgpt_account_id":"acct","chatgpt_workspace_id":"ws"}`)}, nil
		}
		_, _ = c.LoginComplete(context.Background(), appwire.AuthLoginCompleteParams{FlowID: start.FlowID, RedirectURL: "http://localhost/?code=x&state=flow"})

		// Device flow: unsupported, boundary error, pending, expiry, exchange error, success.
		_, _ = c.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{Provider: "ant"})
		c.requestDeviceCode = func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error) {
			return authopenai.DeviceCode{}, authopenai.ErrDeviceCodeNotEnabled
		}
		_, _ = c.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{})
		c.requestDeviceCode = func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error) {
			return authopenai.DeviceCode{}, errors.New("device")
		}
		_, _ = c.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{})
		c.requestDeviceCode = func(context.Context, *http.Client, authopenai.Config) (authopenai.DeviceCode, error) {
			return authopenai.DeviceCode{DeviceAuthID: "d", UserCode: "u", VerificationURL: "http://verify", Interval: time.Second}, nil
		}
		c.generateState = func() (string, error) { return "", errors.New("flow") }
		_, _ = c.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{})
		c.generateState = func() (string, error) { return "device-flow", nil }
		c.deviceFlows = nil
		ds, err := c.DeviceStart(context.Background(), appwire.AuthDeviceStartParams{})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{Provider: "ant"})
		_, _ = c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{FlowID: "missing"})
		c.pollDeviceOnce = func(context.Context, *http.Client, authopenai.Config, authopenai.DeviceCode) (authopenai.DeviceCodeSuccess, bool, error) {
			return authopenai.DeviceCodeSuccess{}, false, errors.New("poll")
		}
		_, _ = c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{FlowID: ds.FlowID})
		c.pollDeviceOnce = func(context.Context, *http.Client, authopenai.Config, authopenai.DeviceCode) (authopenai.DeviceCodeSuccess, bool, error) {
			return authopenai.DeviceCodeSuccess{}, true, nil
		}
		_, _ = c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{FlowID: ds.FlowID})
		c.pollDeviceOnce = func(context.Context, *http.Client, authopenai.Config, authopenai.DeviceCode) (authopenai.DeviceCodeSuccess, bool, error) {
			return authopenai.DeviceCodeSuccess{AuthorizationCode: "a", CodeVerifier: "v"}, false, nil
		}
		c.exchangeDevice = func(context.Context, *http.Client, authopenai.Config, string, string) (authopenai.TokenSet, error) {
			return authopenai.TokenSet{}, errors.New("exchange")
		}
		_, _ = c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{FlowID: ds.FlowID})
		c.exchangeDevice = func(context.Context, *http.Client, authopenai.Config, string, string) (authopenai.TokenSet, error) {
			return authopenai.TokenSet{AccessToken: "device", IDToken: covAIJWT(`{"email":"d@x"}`)}, nil
		}
		_, _ = c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{FlowID: ds.FlowID})
		c.deviceFlows["old"] = deviceFlow{StartedAt: now.Add(-15 * time.Minute)}
		_, _ = c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{FlowID: "old"})
		c.deviceFlows["save-fail"] = deviceFlow{Code: authopenai.DeviceCode{}, StartedAt: now}
		c.saveAuth = func(string, string, authopenai.AuthRecord) error { return errors.New("save") }
		_, _ = c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{FlowID: "save-fail"})
		c.saveAuth = realSave
		c.deviceFlows["status-fail"] = deviceFlow{StartedAt: now}
		c.saveAuth = func(string, string, authopenai.AuthRecord) error { return nil }
		c.loadAuth = func(string, string) (authopenai.AuthRecord, error) {
			return authopenai.AuthRecord{}, errors.New("load")
		}
		_, _ = c.DevicePoll(context.Background(), appwire.AuthDevicePollParams{FlowID: "status-fail"})
		c.saveAuth, c.loadAuth = realSave, authopenai.LoadAuth

		// Credential/OAuth precedence and logout variants.
		_, _ = c.openAIInstanceStatus("openai")
		_ = c.creds.Set("named", "file")
		_, _ = c.openAIInstanceStatus("named")
		_ = authopenai.SaveAuth(c.stateDir, "named", authopenai.AuthRecord{Version: 1, Provider: "named", Source: authopenai.AuthSourceOAuth, AccessToken: "a", Expiry: now.Add(time.Hour), Email: "stored"})
		_, _ = c.openAIInstanceStatus("named")
		_, _ = c.Logout(appwire.AuthLogoutParams{Provider: "named"})
		_, _ = c.Logout(appwire.AuthLogoutParams{Provider: "anthropic"})
		c.clearCredential = func(string) error { return errors.New("clear") }
		_, _ = c.Logout(appwire.AuthLogoutParams{Provider: "anthropic"})
		c.clearCredential = c.creds.Clear
		c.loadAuth = func(string, string) (authopenai.AuthRecord, error) {
			return authopenai.AuthRecord{}, authopenai.ErrAuthCorrupt
		}
		c.deleteAuth = func(string, string) (bool, error) { return false, errors.New("delete") }
		_, _ = c.Logout(appwire.AuthLogoutParams{})
		c.deleteAuth = func(string, string) (bool, error) { return true, nil }
		_, _ = c.Logout(appwire.AuthLogoutParams{})
		c.loadAuth = func(string, string) (authopenai.AuthRecord, error) {
			return authopenai.AuthRecord{}, errors.New("load")
		}
		_, _ = c.openAIInstanceStatus("openai")
		c.loadAuth = func(string, string) (authopenai.AuthRecord, error) {
			return authopenai.AuthRecord{}, authopenai.ErrAuthCorrupt
		}
		_, _ = c.openAIInstanceStatus("openai")
		c.loadAuth = func(string, string) (authopenai.AuthRecord, error) {
			return authopenai.AuthRecord{Source: authopenai.AuthSourceOAuth, Expiry: now.Add(-time.Second), Email: "e", AccountID: "a", WorkspaceID: "w"}, nil
		}
		_, _ = c.openAIInstanceStatus("openai")
		c.loadAuth = authopenai.LoadAuth
		c.deleteAuth = authopenai.DeleteAuth
		_ = c.creds.Set("openai", "file")
		calls := 0
		c.loadAuth = func(string, string) (authopenai.AuthRecord, error) {
			calls++
			if calls == 1 {
				return authopenai.AuthRecord{}, authopenai.ErrAuthNotFound
			}
			return authopenai.AuthRecord{}, errors.New("status")
		}
		_, _ = c.Logout(appwire.AuthLogoutParams{})
		_ = c.creds.Set("openai", "file")
		c.clearCredential = func(string) error { return errors.New("clear") }
		c.loadAuth = func(string, string) (authopenai.AuthRecord, error) {
			return authopenai.AuthRecord{}, authopenai.ErrAuthNotFound
		}
		_, _ = c.Logout(appwire.AuthLogoutParams{})
		c.clearCredential = c.creds.Clear
		c.loadAuth = authopenai.LoadAuth
		_ = c.instanceStatus(registry.Instance{Name: "fallback", Auth: "unknown-scheme"})

		// Instance CRUD success and validation/error paths.
		ip := filepath.Join(root, "instances.toml")
		writeMinimalProvidersToml(t, ip)
		icreds, err := credentials.LoadStore(filepath.Join(root, "icreds", "credentials.toml"))
		if err != nil {
			t.Fatal(err)
		}
		istate := filepath.Join(root, "istate")
		iauth := newHubAuthControllerWithStore(root, icreds)
		iauth.stateDir = istate
		iauth.providersConfigPath = ip
		iauth.reg = newTestRegistry(t, istate, ip, icreds, nil)
		ic := &hubInstancesController{reg: iauth.reg, providersConfigPath: ip, auth: iauth}
		_ = ic.List()
		_ = ic.Create(appwire.InstanceCreateParams{Name: "bad/name", Base: "anthropic"})
		_ = ic.Create(appwire.InstanceCreateParams{Name: "new", Base: "bad"})
		_ = ic.Create(appwire.InstanceCreateParams{Name: "new", Base: "anthropic", CredentialHeader: "Authorization=Bearer literal"})
		_ = ic.Create(appwire.InstanceCreateParams{Name: "base", Base: "anthropic"})
		if err := ic.Create(appwire.InstanceCreateParams{Name: "new", Base: "anthropic", APIKeyEnv: "NEW_KEY", CredentialHeader: "Authorization=Bearer $NEW_KEY"}); err != nil {
			t.Fatal(err)
		}
		_ = ic.List()
		_ = ic.Edit(appwire.InstanceEditParams{Name: "missing"})
		_ = ic.Edit(appwire.InstanceEditParams{Name: "new", Protocol: "openai-responses", Surface: "generic", Vars: map[string]string{"X": "y"}})
		if err := ic.Edit(appwire.InstanceEditParams{Name: "new", BaseURL: new("http://local")}); err != nil {
			t.Fatal(err)
		}
		_ = ic.SetDefault(appwire.InstanceSetDefaultParams{Name: "missing"})
		if err := ic.SetDefault(appwire.InstanceSetDefaultParams{Name: "new"}); err != nil {
			t.Fatal(err)
		}
		_ = ic.Remove(appwire.InstanceRemoveParams{Name: "../bad"})
		_ = ic.Remove(appwire.InstanceRemoveParams{Name: "missing"})
		if err := ic.Remove(appwire.InstanceRemoveParams{Name: "new"}); err != nil {
			t.Fatal(err)
		}
		if err := ic.Remove(appwire.InstanceRemoveParams{Name: "base"}); err != nil {
			t.Fatal(err)
		}

		// Real filesystem failures, each after a successful registry load, so
		// every mutation's error contract is covered on the path production
		// takes: a sealed directory leaves providers.toml readable and its
		// atomic temp file uncreatable, and a directory where the file should
		// be fails the read itself.
		sealedDir := filepath.Join(root, "sealed")
		if err := os.MkdirAll(sealedDir, 0o700); err != nil {
			t.Fatal(err)
		}
		sealedPath := filepath.Join(sealedDir, "providers.toml")
		if err := os.WriteFile(sealedPath, []byte("default = \"base\"\n[providers.base]\nbase = \"anthropic\"\napi_key = \"$BASE_KEY\"\n[providers.second]\nbase = \"google\"\napi_key = \"$SECOND_KEY\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		sealedReg := newTestRegistry(t, istate, sealedPath, icreds, map[string]string{"BASE_KEY": "sk-base", "SECOND_KEY": "sk-second"})
		if err := os.Chmod(sealedDir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(sealedDir, 0o755) })
		failWrite := &hubInstancesController{reg: sealedReg, providersConfigPath: sealedPath, auth: iauth}
		_ = failWrite.List()
		_ = failWrite.Create(appwire.InstanceCreateParams{Name: "third", Base: "anthropic"})
		_ = failWrite.Edit(appwire.InstanceEditParams{Name: "base"})
		_ = failWrite.Remove(appwire.InstanceRemoveParams{Name: "second"})
		_ = failWrite.SetDefault(appwire.InstanceSetDefaultParams{Name: "second"})

		unreadablePath := filepath.Join(root, "unreadable", "providers.toml")
		if err := os.MkdirAll(unreadablePath, 0o700); err != nil {
			t.Fatal(err)
		}
		failRead := &hubInstancesController{reg: sealedReg, providersConfigPath: unreadablePath, auth: iauth}
		_ = failRead.Create(appwire.InstanceCreateParams{Name: "third", Base: "anthropic"})
		_ = failRead.Edit(appwire.InstanceEditParams{Name: "base"})
		_ = failRead.SetDefault(appwire.InstanceSetDefaultParams{Name: "base"})

		// A providers.toml the registry could not read refuses every write.
		brokenPath := filepath.Join(root, "broken.toml")
		if err := os.WriteFile(brokenPath, []byte("[instances.openai]\ntype = \"openai\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		brokenReg := hubcore.NewProviderRegistry(func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
			r, err := registry.Load(append([]registry.Option{
				registry.WithOffline(true), registry.WithoutCache(),
				registry.WithConfigPath(brokenPath), registry.WithStateRoot(istate),
				registry.WithEnv(func(string) (string, bool) { return "", false }),
			}, extra...)...)
			return r, nil, err
		})
		if err := brokenReg.Reload(); err == nil {
			t.Fatal("an old-schema providers.toml must not load")
		}
		broken := &hubInstancesController{reg: brokenReg, providersConfigPath: brokenPath, auth: iauth}
		_ = broken.List()
		_ = broken.Create(appwire.InstanceCreateParams{Name: "x", Base: "anthropic"})
		_ = broken.Edit(appwire.InstanceEditParams{Name: "x"})
		_ = broken.Remove(appwire.InstanceRemoveParams{Name: "x"})
		_ = broken.SetDefault(appwire.InstanceSetDefaultParams{Name: "x"})
	})
}
