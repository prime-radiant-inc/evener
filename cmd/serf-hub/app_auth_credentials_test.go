package main

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

	"primeradiant.com/serf/appwire"
	authopenai "primeradiant.com/serf/auth/openai"
	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/llm/providers/openaicompat"
)

type credentialProbeFakeClient struct {
	mu      sync.Mutex
	started chan struct{}
	release chan struct{}
	calls   int
	listErr error
}

func (f *credentialProbeFakeClient) ListModels(ctx context.Context, _ string) ([]llm.ModelInfo, error) {
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
			return nil, ctx.Err()
		}
	}
	return nil, f.listErr
}

func (f *credentialProbeFakeClient) Close() error { return nil }

func (f *credentialProbeFakeClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newCredentialProbeController(t *testing.T, client credentialProbeClient, cfg providercfg.Config) *hubAuthController {
	t.Helper()
	store, err := credentials.LoadStore(t.TempDir() + "/credentials.toml")
	if err != nil {
		t.Fatal(err)
	}
	c := newHubAuthControllerWithStore(t.TempDir(), store)
	c.credentialTestLoader = func(string) (credentialProbeClient, providercfg.Config, error) {
		return client, cfg, nil
	}
	return c
}

func TestAuthTestCredentialsClassifiesProviderOutcomesWithoutSecrets(t *testing.T) {
	secret := "sk-test-credential-must-not-cross-boundary"
	cases := []struct {
		name   string
		err    error
		status string
	}{
		{name: "success", status: appwire.AuthTestStatusSuccess},
		{name: "auth", err: llm.ErrorFromHTTPStatus("custom", 401, secret, nil, nil), status: appwire.AuthTestStatusAuthRejected},
		{name: "forbidden", err: llm.ErrorFromHTTPStatus("custom", 403, secret, nil, nil), status: appwire.AuthTestStatusAuthRejected},
		{name: "endpoint", err: errors.New("dial tcp 192.0.2.1:443: connection refused"), status: appwire.AuthTestStatusEndpointFailure},
		{name: "unsupported", err: &llm.ConfigurationError{Message: "provider custom does not support listing models"}, status: appwire.AuthTestStatusUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &credentialProbeFakeClient{listErr: tc.err}
			cfg := providercfg.Config{Instances: []providercfg.InstanceConfig{{Name: "custom", Type: "openai-compatible", BaseURL: "http://provider.test/v1", APIKey: secret}}}
			c := newCredentialProbeController(t, client, cfg)
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
	client := &credentialProbeFakeClient{}
	cfg := providercfg.Config{Instances: []providercfg.InstanceConfig{{Name: "anthropic-work", Type: "anthropic"}}}
	c := newCredentialProbeController(t, client, cfg)

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

func TestAuthTestCredentialsReportsMissingWhenClientConstructionSkipsInstance(t *testing.T) {
	cfg := providercfg.Config{Instances: []providercfg.InstanceConfig{{Name: "anthropic-work", Type: "anthropic"}}}
	store, err := credentials.LoadStore(t.TempDir() + "/credentials.toml")
	if err != nil {
		t.Fatal(err)
	}
	c := newHubAuthControllerWithStore(t.TempDir(), store)
	c.credentialTestLoader = func(string) (credentialProbeClient, providercfg.Config, error) {
		return nil, cfg, errors.New("no providers initialized")
	}

	resp, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: "anthropic-work"})
	if err != nil {
		t.Fatalf("TestCredentials: %v", err)
	}
	if resp.Status != appwire.AuthTestStatusMissing {
		t.Fatalf("status=%q, want missing", resp.Status)
	}
}

func TestAuthTestCredentialsSuppressesDuplicateSameInstance(t *testing.T) {
	client := &credentialProbeFakeClient{started: make(chan struct{}), release: make(chan struct{})}
	cfg := providercfg.Config{Instances: []providercfg.InstanceConfig{{Name: "custom", Type: "openai-compatible", BaseURL: "http://provider.test/v1", APIKey: "configured"}}}
	c := newCredentialProbeController(t, client, cfg)

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

	adapter := openaicompat.NewForInstance(openaicompat.OpenAICompatInstanceParams{
		Name:              "gateway",
		BaseURL:           server.URL,
		APIKey:            apiKey,
		CredentialHeaders: map[string]string{"X-Test-Credential": headerSecret},
	})
	client := llm.NewClient()
	client.Register(adapter)
	cfg := providercfg.Config{Instances: []providercfg.InstanceConfig{{
		Name:              "gateway",
		Type:              "openai-compatible",
		BaseURL:           server.URL,
		APIKey:            apiKey,
		CredentialHeaders: map[string]string{"X-Test-Credential": headerSecret},
	}}}
	c := newCredentialProbeController(t, client, cfg)

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

func TestAuthTestCredentialsAcceptsStoredOAuthForNamedOpenAIInstance(t *testing.T) {
	stateDir := t.TempDir()
	store, err := credentials.LoadStore(t.TempDir() + "/credentials.toml")
	if err != nil {
		t.Fatal(err)
	}
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
	c := newHubAuthControllerWithStore(stateDir, store)
	c.stateDir = stateDir
	c.credentialTestLoader = func(string) (credentialProbeClient, providercfg.Config, error) {
		return &credentialProbeFakeClient{}, providercfg.Config{Instances: []providercfg.InstanceConfig{{
			Name: "openai-work",
			Type: "openai",
		}}}, nil
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
	cfg := providercfg.Config{Instances: []providercfg.InstanceConfig{{Name: "gateway", Type: "openai-compatible", BaseURL: "http://provider.test/v1", APIKey: "configured"}}}
	controller := newCredentialProbeController(t, client, cfg)
	server := appserver.NewServer(appserver.ServerConfig{})
	registerAuthHandlers(server, controller)

	raw, err := server.Router().Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodSerfAuthTest,
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
