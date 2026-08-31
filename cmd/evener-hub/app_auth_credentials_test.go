package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

type credentialProbeFakeClient struct {
	mu      sync.Mutex
	started chan struct{}
	release chan struct{}
	calls   int
	listErr error
	// notLive makes the probe report a listing the provider did not actually
	// serve, which is how "this endpoint has no model list" reaches the pane.
	notLive bool
}

func (f *credentialProbeFakeClient) Models(ctx context.Context, _ string) (llm.ModelListing, error) {
	f.mu.Lock()
	f.calls++
	started := f.calls == 1 && f.started != nil
	f.mu.Unlock()
	if started {
		close(f.started)
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return llm.ModelListing{}, ctx.Err()
		}
	}
	return llm.ModelListing{Live: !f.notLive}, f.listErr
}

func (f *credentialProbeFakeClient) Close() error { return nil }

func (f *credentialProbeFakeClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// clearProviderKeysFromEnvironment unsets every credential variable the
// envvars registry declares, for the duration of the test. The credentials
// store's Layers reads the ambient environment, so a test asserting an
// instance has no stored key means nothing unless it states that the
// environment holds none either (docs/developing-evener/testing.md: no
// ambient developer machine state). cmdutil's envvars_registry_test pins
// that this roster names every variable the registry reads.
func clearProviderKeysFromEnvironment(t *testing.T) {
	t.Helper()
	for _, v := range envvars.All() {
		if v.Secret {
			t.Setenv(v.Name, "")
		}
	}
}

// newCredentialProbeController builds an auth controller whose registry holds
// exactly instances, resolved against env, and whose probe returns client.
func newCredentialProbeController(t *testing.T, client credentialProbeClient, instances map[string]registry.Provider, env map[string]string) *hubAuthController {
	t.Helper()
	store, err := credentials.LoadStore(t.TempDir() + "/credentials.toml")
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	c := newHubAuthControllerWithStore(t.TempDir(), store)
	c.stateDir = stateDir
	c.reg = newProbeRegistry(t, stateDir, store, env, instances)
	c.credentialTestLoader = func(string, bool) (credentialProbeClient, error) { return client, nil }
	return c
}

// newProbeRegistry is a hermetic registry over exactly the injected instances.
func newProbeRegistry(t *testing.T, stateDir string, store *credentials.Store, env map[string]string, instances map[string]registry.Provider) *hubcore.ProviderRegistry {
	t.Helper()
	holder := hubcore.NewProviderRegistry(func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		opts := []registry.Option{
			registry.WithOffline(true),
			registry.WithoutCache(),
			registry.WithNoUserLayer(),
			registry.WithStateRoot(stateDir),
			registry.WithCredentials(cmdutil.StoreCredentialSource{Store: store}),
			registry.WithEnv(func(name string) (string, bool) {
				v, ok := env[name]
				return v, ok
			}),
			registry.WithInstances(instances),
		}
		r, err := registry.Load(append(opts, extra...)...)
		return r, store, err
	})
	if err := holder.Reload(); err != nil {
		t.Fatalf("registry: %v", err)
	}
	return holder
}

func TestAuthTestCredentialsClassifiesProviderOutcomesWithoutSecrets(t *testing.T) {
	secret := "sk-test-credential-must-not-cross-boundary"
	cases := []struct {
		name    string
		err     error
		notLive bool
		status  string
	}{
		{name: "success", status: appwire.AuthTestStatusSuccess},
		{name: "auth", err: llm.ErrorFromHTTPStatus("custom", 401, secret, nil, nil), status: appwire.AuthTestStatusAuthRejected},
		{name: "forbidden", err: llm.ErrorFromHTTPStatus("custom", 403, secret, nil, nil), status: appwire.AuthTestStatusAuthRejected},
		{name: "endpoint", err: errors.New("dial tcp 192.0.2.1:443: connection refused"), status: appwire.AuthTestStatusEndpointFailure},
		{name: "configuration", err: &llm.ConfigurationError{Message: "invalid provider configuration"}, status: appwire.AuthTestStatusConfigurationFailure},
		{name: "unsupported", notLive: true, status: appwire.AuthTestStatusUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &credentialProbeFakeClient{listErr: tc.err, notLive: tc.notLive}
			c := newCredentialProbeController(t, client, map[string]registry.Provider{"custom": {
				Base:      "openai-compatible",
				APIKey:    secret,
				Transport: registry.Transport{BaseURL: "http://provider.test/v1"},
			}}, nil)
			resp, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: "custom"})
			if err != nil {
				t.Fatalf("TestCredentials: %v", err)
			}
			if resp.Status != tc.status {
				t.Fatalf("status=%q, want %q (response=%+v)", resp.Status, tc.status, resp)
			}
			encoded, marshalErr := json.Marshal(resp)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if strings.Contains(string(encoded), secret) || strings.Contains(resp.Message, secret) {
				t.Fatalf("response leaked credential: %s", encoded)
			}
		})
	}
}

func TestAuthTestCredentialsReportsMissingCredentialsBeforeProbe(t *testing.T) {
	clearProviderKeysFromEnvironment(t)
	client := &credentialProbeFakeClient{}
	c := newCredentialProbeController(t, client, map[string]registry.Provider{
		"anthropic-work": {Base: "anthropic", APIKeyEnv: []string{"ANTHROPIC_WORK_KEY"}},
	}, nil)

	resp, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: "anthropic-work"})
	if err != nil {
		t.Fatalf("TestCredentials: %v", err)
	}
	if resp.Status != appwire.AuthTestStatusMissing {
		t.Fatalf("status=%q, want missing", resp.Status)
	}
	if got := client.callCount(); got != 0 {
		t.Fatalf("probe calls=%d, want 0 for missing credentials", got)
	}
}

// TestAuthTestCredentialsIgnoresOrdinaryHeaders: an ordinary header is not a
// credential, so an instance carrying only one is still unconfigured.
func TestAuthTestCredentialsIgnoresOrdinaryHeaders(t *testing.T) {
	clearProviderKeysFromEnvironment(t)
	client := &credentialProbeFakeClient{}
	c := newCredentialProbeController(t, client, map[string]registry.Provider{
		"anthropic-work": {
			Base:      "anthropic",
			APIKeyEnv: []string{"ANTHROPIC_WORK_KEY"},
			Headers:   map[string]string{"X-Trace": "request-id"},
		},
	}, nil)

	resp, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: "anthropic-work"})
	if err != nil {
		t.Fatalf("TestCredentials: %v", err)
	}
	if resp.Status != appwire.AuthTestStatusMissing {
		t.Fatalf("status=%q, want missing", resp.Status)
	}
	if got := client.callCount(); got != 0 {
		t.Fatalf("probe calls=%d, want 0 for ordinary-header-only instance", got)
	}
}

// TestAuthTestCredentialsTreatsUnresolvedAPIKeyReferenceAsMissing: an api_key
// naming a variable nothing sets resolves to no credential (spec §10).
func TestAuthTestCredentialsTreatsUnresolvedAPIKeyReferenceAsMissing(t *testing.T) {
	clearProviderKeysFromEnvironment(t)
	client := &credentialProbeFakeClient{}
	c := newCredentialProbeController(t, client, map[string]registry.Provider{
		"anthropic-work": {Base: "anthropic", APIKey: "$EVENER_ZR5R_MISSING_API_KEY"},
	}, nil)

	resp, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: "anthropic-work"})
	if err != nil {
		t.Fatalf("TestCredentials: %v", err)
	}
	if resp.Status != appwire.AuthTestStatusMissing {
		t.Fatalf("status=%q, want missing", resp.Status)
	}
	if got := client.callCount(); got != 0 {
		t.Fatalf("probe calls=%d, want 0 for unresolved API key", got)
	}
}

func TestAuthTestCredentialsReportsLoaderFailureAsConfigurationFailure(t *testing.T) {
	c := newCredentialProbeController(t, nil, map[string]registry.Provider{
		"work": {Base: "anthropic", APIKey: "configured"},
	}, nil)
	c.credentialTestLoader = func(string, bool) (credentialProbeClient, error) {
		return nil, errors.New("providers config is unreadable")
	}

	resp, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: "work"})
	if err != nil {
		t.Fatalf("TestCredentials: %v", err)
	}
	if resp.Status != appwire.AuthTestStatusConfigurationFailure {
		t.Fatalf("status=%q, want configuration_failure", resp.Status)
	}
	if resp.Message != credentialTestConfigurationMessage {
		t.Fatalf("message=%q, want fixed configuration message", resp.Message)
	}
}

// TestAuthTestCredentialsRejectsUnknownInstance: a name the registry cannot
// resolve at all is a configuration failure, not a missing credential.
func TestAuthTestCredentialsRejectsUnknownInstance(t *testing.T) {
	c := newCredentialProbeController(t, &credentialProbeFakeClient{}, nil, nil)
	resp, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: "nowhere"})
	if err != nil {
		t.Fatalf("TestCredentials: %v", err)
	}
	if resp.Status != appwire.AuthTestStatusConfigurationFailure {
		t.Fatalf("status=%q, want configuration_failure", resp.Status)
	}
}

func TestAuthTestCredentialsSuppressesDuplicateSameInstance(t *testing.T) {
	client := &credentialProbeFakeClient{started: make(chan struct{}), release: make(chan struct{})}
	c := newCredentialProbeController(t, client, map[string]registry.Provider{"custom": {
		Base:      "openai-compatible",
		APIKey:    "configured",
		Transport: registry.Transport{BaseURL: "http://provider.test/v1"},
	}}, nil)

	first := make(chan appwire.AuthTestResponse, 1)
	go func() {
		resp, _ := c.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: "custom"})
		first <- resp
	}()
	<-client.started

	second := make(chan appwire.AuthTestResponse, 1)
	secondJoined := make(chan struct{})
	c.credentialTestJoined = func() { close(secondJoined) }
	go func() {
		resp, _ := c.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: "custom"})
		second <- resp
	}()
	<-secondJoined
	close(client.release)

	if got := (<-first).Status; got != appwire.AuthTestStatusSuccess {
		t.Fatalf("first status=%q, want success", got)
	}
	if got := (<-second).Status; got != appwire.AuthTestStatusSuccess {
		t.Fatalf("second status=%q, want success", got)
	}
	if got := client.callCount(); got != 1 {
		t.Fatalf("probe calls=%d, want one shared call", got)
	}
}

func TestAuthTestCredentialsUsesConfiguredBaseURLAndHeadersAtFakeHTTPBoundary(t *testing.T) {
	apiKey := "sk-test-credential"
	headerSecret := "header-secret"
	var gotAuthorization, gotHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotHeader = r.Header.Get("X-Test-Credential")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"test-model"}]}`))
	}))
	t.Cleanup(server.Close)

	instances := map[string]registry.Provider{"gateway": {
		Base:              "openai-compatible",
		APIKey:            apiKey,
		Transport:         registry.Transport{BaseURL: server.URL},
		CredentialHeaders: map[string]string{"X-Test-Credential": headerSecret},
	}}
	// No override: the probe reaches the fake server through the registry's
	// own transport, which is what a spawned session would use.
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(instances),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	c := newCredentialProbeController(t, llm.NewClient(llm.WithRegistry(r)), instances, nil)

	resp, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: "gateway"})
	if err != nil {
		t.Fatalf("TestCredentials: %v", err)
	}
	if resp.Status != appwire.AuthTestStatusSuccess {
		t.Fatalf("status=%q, want success", resp.Status)
	}
	if gotAuthorization != "Bearer "+apiKey || gotHeader != headerSecret {
		t.Fatalf("fake HTTP headers authorization=%q credential=%q", gotAuthorization, gotHeader)
	}
	encoded, _ := json.Marshal(resp)
	if strings.Contains(string(encoded), apiKey) || strings.Contains(string(encoded), headerSecret) {
		t.Fatalf("response leaked credential material: %s", encoded)
	}
}

// TestAuthTestCredentialsAcceptsStoredOAuthForCodexInstance: on the Codex
// transport the record is the credential, so the probe runs.
func TestAuthTestCredentialsAcceptsStoredOAuthForCodexInstance(t *testing.T) {
	clearProviderKeysFromEnvironment(t)
	stateDir := t.TempDir()
	if err := authopenai.SaveAuth(stateDir, "openai-work", authopenai.AuthRecord{
		ObtainedAt:   time.Now().Add(-time.Minute),
		Version:      1,
		Provider:     "openai",
		Source:       authopenai.AuthSourceOAuth,
		AccessToken:  "oauth-access-token",
		RefreshToken: "oauth-refresh-token",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	store, err := credentials.LoadStore(t.TempDir() + "/credentials.toml")
	if err != nil {
		t.Fatal(err)
	}
	c := newHubAuthControllerWithStore(stateDir, store)
	c.stateDir = stateDir
	c.reg = newProbeRegistry(t, stateDir, store, nil, map[string]registry.Provider{
		"openai-work": {Base: "openai-codex"},
	})
	c.credentialTestLoader = func(string, bool) (credentialProbeClient, error) {
		return &credentialProbeFakeClient{}, nil
	}

	resp, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: "openai-work"})
	if err != nil {
		t.Fatalf("TestCredentials: %v", err)
	}
	if resp.Status != appwire.AuthTestStatusSuccess {
		t.Fatalf("status=%q, want success", resp.Status)
	}
}

func TestAuthTestRPCUsesSharedContract(t *testing.T) {
	client := &credentialProbeFakeClient{}
	controller := newCredentialProbeController(t, client, map[string]registry.Provider{"gateway": {
		Base:      "openai-compatible",
		APIKey:    "configured",
		Transport: registry.Transport{BaseURL: "http://provider.test/v1"},
	}}, nil)
	server := appserver.NewServer(appserver.ServerConfig{})
	registerAuthHandlers(server, controller)

	raw, err := server.Router().Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodEvenerAuthTest,
		Params: mustMarshal(t, appwire.AuthTestParams{Provider: "gateway"}),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	resp, ok := raw.(appwire.AuthTestResponse)
	if !ok || resp.Provider != "gateway" || resp.Status != appwire.AuthTestStatusSuccess {
		t.Fatalf("response=%T %+v", raw, raw)
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
