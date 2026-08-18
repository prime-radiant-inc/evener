package main

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

	"primeradiant.com/serf/appwire"
	authopenai "primeradiant.com/serf/auth/openai"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/llm/providercfg"
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
		c.authEnv = map[string]string{}
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
		// expected values instead of discarding the result (docs/testing.md's
		// executed-vs-tested note on the serffuzz cov_* driver family).
		if got := effectiveHubAuthEnv(map[string]string{"COV_AUTH": "1"}); got["COV_AUTH"] != "1" {
			t.Fatalf("effectiveHubAuthEnv: COV_AUTH = %q, want \"1\"", got["COV_AUTH"])
		}
		wantStateDir := filepath.Join(root, ".local", "state", "serf")
		if runtime.GOOS == "windows" {
			// The Windows branch looks for USERPROFILE/HOMEDRIVE+HOMEPATH, neither of
			// which this env map sets, so it falls back to os.TempDir() instead.
			wantStateDir = filepath.Join(os.TempDir(), ".local", "state", "serf")
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

		// Resolution, legacy status, key status, list, and API-key validation.
		if _, err := c.resolveInstanceBehaviorTag("x"); err == nil {
			t.Fatal("resolveInstanceBehaviorTag(x) before providers.toml exists: want error, got nil")
		}
		if err := os.WriteFile(providers, []byte("bad = ["), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := c.resolveInstanceBehaviorTag("x"); err == nil {
			t.Fatal("resolveInstanceBehaviorTag(x) against malformed providers.toml: want error, got nil")
		}
		if err := os.WriteFile(providers, []byte("schema=1\ndefault=\"work\"\n[instances.work]\ntype=\"openai\"\n[instances.ant]\ntype=\"anthropic\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if tag, err := c.resolveInstanceBehaviorTag("work"); err != nil || tag != "openai" {
			t.Fatalf("resolveInstanceBehaviorTag(work) = %q, %v, want \"openai\", nil", tag, err)
		}
		if _, err := c.resolveInstanceBehaviorTag("missing"); err == nil {
			t.Fatal("resolveInstanceBehaviorTag(missing): want error, got nil")
		}
		if got := c.instanceIsOpenAI("work"); !got {
			t.Fatal("instanceIsOpenAI(work) = false, want true (declared type \"openai\")")
		}
		if got := c.instanceIsOpenAI("openai"); !got {
			t.Fatal("instanceIsOpenAI(openai) = false, want true (no such instance; falls back to name==\"openai\")")
		}
		if got := c.instanceIsOpenAI("ant"); got {
			t.Fatal("instanceIsOpenAI(ant) = true, want false (declared type \"anthropic\")")
		}
		wantRequiresOpenAIErr := `OAuth is not supported for instance "ant"`
		if err := c.requiresOpenAI("ant"); err == nil || err.Error() != wantRequiresOpenAIErr {
			t.Fatalf("requiresOpenAI(ant) = %v, want error %q", err, wantRequiresOpenAIErr)
		}
		_, _ = c.Status(appwire.AuthStatusParams{Provider: "work"})
		_, _ = c.Status(appwire.AuthStatusParams{Provider: "unknown"})
		c.providersConfigPath = ""
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
		c.authEnv["OPENAI_API_KEY"] = "env"
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
		_ = c.instanceStatus("fallback", "unknown-type", "unknown-type")

		// Instance CRUD success and validation/error paths.
		ip := filepath.Join(root, "instances.toml")
		writeMinimalProvidersToml(t, ip)
		ic := newTestInstancesController(t, ip, filepath.Join(root, "icreds"), filepath.Join(root, "istate"))
		_ = ic.List()
		_ = ic.Create(appwire.InstanceCreateParams{Name: "bad/name", Type: "anthropic"})
		_ = ic.Create(appwire.InstanceCreateParams{Name: "new", Type: "bad"})
		_ = ic.Create(appwire.InstanceCreateParams{Name: "new", Type: "anthropic", APIStyle: "responses"})
		_ = ic.Create(appwire.InstanceCreateParams{Name: "base", Type: "anthropic"})
		if err := ic.Create(appwire.InstanceCreateParams{Name: "new", Type: "anthropic"}); err != nil {
			t.Fatal(err)
		}
		_ = ic.List()
		_ = ic.Edit(appwire.InstanceEditParams{Name: "missing"})
		_ = ic.Edit(appwire.InstanceEditParams{Name: "new", APIStyle: "responses"})
		if err := ic.Edit(appwire.InstanceEditParams{Name: "new", BaseURL: "http://local"}); err != nil {
			t.Fatal(err)
		}
		_ = ic.SetDefault(appwire.InstanceSetDefaultParams{Name: "missing"})
		if err := ic.SetDefault(appwire.InstanceSetDefaultParams{Name: "new"}); err != nil {
			t.Fatal(err)
		}
		_ = ic.Remove(appwire.InstanceRemoveParams{Name: "../bad"})
		if err := ic.Remove(appwire.InstanceRemoveParams{Name: "new"}); err != nil {
			t.Fatal(err)
		}
		if err := ic.Remove(appwire.InstanceRemoveParams{Name: "base"}); err != nil {
			t.Fatal(err)
		}
		ic.providersConfigPath = filepath.Join(root, "absent", "providers.toml")
		_, _ = ic.reloadFromDisk()
		_ = ic.Create(appwire.InstanceCreateParams{Name: "fresh", Type: "anthropic"})
		ic.providersConfigPath = root // reading a directory forces the reload error path.
		_, _ = ic.reloadFromDisk()
		_ = ic.Create(appwire.InstanceCreateParams{Name: "x", Type: "anthropic"})
		_ = ic.Edit(appwire.InstanceEditParams{Name: "x"})
		_ = ic.Remove(appwire.InstanceRemoveParams{Name: "x"})
		_ = ic.SetDefault(appwire.InstanceSetDefaultParams{Name: "x"})

		// Inject filesystem failures after successful reloads to cover each
		// mutation's error contract independently of host permissions.
		baseCfg := providercfg.Config{Default: "base", Instances: []providercfg.InstanceConfig{{Name: "base", Type: "anthropic"}, {Name: "second", Type: "google"}}}
		failWrite := &hubInstancesController{auth: ic.auth,
			loadFile:  func(string) (providercfg.Config, bool, error) { return baseCfg, true, nil },
			writeFile: func(string, providercfg.Config) error { return errors.New("write") },
		}
		_ = failWrite.List()
		_ = failWrite.Create(appwire.InstanceCreateParams{Name: "third", Type: "anthropic"})
		_ = failWrite.Edit(appwire.InstanceEditParams{Name: "base"})
		_ = failWrite.Remove(appwire.InstanceRemoveParams{Name: "second"})
		_ = failWrite.SetDefault(appwire.InstanceSetDefaultParams{Name: "second"})
		failRemove := &hubInstancesController{auth: ic.auth,
			loadFile: func(string) (providercfg.Config, bool, error) {
				return providercfg.Config{Default: "base", Instances: []providercfg.InstanceConfig{{Name: "base", Type: "anthropic"}}}, true, nil
			},
			removeFile: func(string) error { return errors.New("remove") },
		}
		_ = failRemove.Remove(appwire.InstanceRemoveParams{Name: "base"})
		blockedState := filepath.Join(root, "blocked-state")
		if err := os.WriteFile(blockedState, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		deleteAuth := newHubAuthControllerWithStore(root, ic.auth.creds)
		deleteAuth.stateDir = blockedState
		deleteFail := &hubInstancesController{auth: deleteAuth,
			loadFile:  func(string) (providercfg.Config, bool, error) { return baseCfg, true, nil },
			writeFile: func(string, providercfg.Config) error { return nil },
		}
		_ = deleteFail.Remove(appwire.InstanceRemoveParams{Name: "second"})
	})
}
