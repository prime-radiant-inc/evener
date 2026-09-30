package hub

// A provider that rejects an instance's credential must read as an error in
// evener/auth/status and evener/auth/list (#3539), not "Key set": the hub
// records the rejection it saw on its own probe, keeps it only while the
// instance's credential configuration is the one that was rejected, and drops
// it on any credential write or a successful probe.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// rejectionSecret stands in for anything a provider's error body can carry: a
// key fragment, the request. None of it may reach the status.
const rejectionSecret = "sk-rejected-key-must-not-cross-boundary"

// rejectionInstances is one key-authenticated instance. Its key is stored in
// credentials.toml (newRejectionController), so replacing it is a credential
// write that leaves the resolved source, and so the configuration revision,
// unchanged: a test that clears the rejection by a write proves the write
// cleared it, not a revision change.
func rejectionInstances(baseURL string) map[string]registry.Provider {
	return map[string]registry.Provider{"gateway": {
		Base:      "openai-compatible",
		Transport: registry.Transport{BaseURL: baseURL},
	}}
}

// newRejectionController is newCredentialProbeController over a registry whose
// instances the test can replace and reload, so it can change the instance's
// configuration under a recorded rejection.
func newRejectionController(t *testing.T, client credentialProbeClient) (*hubAuthController, func(map[string]registry.Provider)) {
	t.Helper()
	clearProviderKeysFromEnvironment(t)
	stateDir := t.TempDir()
	store, err := credentials.LoadStore(t.TempDir() + "/credentials.toml")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("gateway", rejectionSecret); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	instances := rejectionInstances("http://provider.test/v1")
	holder := hubcore.NewProviderRegistry(func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		mu.Lock()
		current := instances
		mu.Unlock()
		opts := []registry.Option{
			registry.WithOffline(true),
			registry.WithoutCache(),
			registry.WithNoUserLayer(),
			registry.WithStateRoot(stateDir),
			registry.WithCredentials(cmdutil.StoreCredentialSource{Store: store}),
			registry.WithEnv(func(string) (string, bool) { return "", false }),
			registry.WithInstances(current),
		}
		r, err := registry.Load(append(opts, extra...)...)
		return r, store, err
	})
	if err := holder.Reload(); err != nil {
		t.Fatalf("registry: %v", err)
	}
	c := newHubAuthControllerWithStore(t.TempDir(), store)
	c.stateDir = stateDir
	c.reg = holder
	c.credentialTestLoader = func(string, bool) (credentialProbeClient, error) { return client, nil }
	reconfigure := func(next map[string]registry.Provider) {
		mu.Lock()
		instances = next
		mu.Unlock()
		if err := holder.Reload(); err != nil {
			t.Fatalf("registry reload: %v", err)
		}
	}
	return c, reconfigure
}

func testGateway(t *testing.T, c *hubAuthController) appwire.AuthTestResponse {
	t.Helper()
	resp, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: "gateway"})
	if err != nil {
		t.Fatalf("TestCredentials: %v", err)
	}
	return resp
}

// gatewayError is the gateway row's error as evener/auth/status and
// evener/auth/list each report it; the two must agree.
func gatewayError(t *testing.T, c *hubAuthController) string {
	t.Helper()
	status, err := c.Status(appwire.AuthStatusParams{Provider: "gateway"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	list, err := c.List(appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	listed := ""
	found := false
	for _, row := range list.Providers {
		if row.Provider == "gateway" {
			listed, found = row.Error, true
		}
	}
	if !found {
		t.Fatalf("auth/list has no gateway row: %+v", list.Providers)
	}
	if listed != status.Error {
		t.Fatalf("auth/list error %q disagrees with auth/status error %q", listed, status.Error)
	}
	return status.Error
}

func TestCredentialRejection_TestConnectionRecordsARejectedCredential(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "unauthorized", err: llm.ErrorFromHTTPStatus("gateway", 401, "Incorrect API key provided: "+rejectionSecret, nil, nil), want: "The provider rejected this credential (HTTP 401)."},
		{name: "forbidden", err: llm.ErrorFromHTTPStatus("gateway", 403, "forbidden for "+rejectionSecret, nil, nil), want: "The provider rejected this credential (HTTP 403)."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newRejectionController(t, &credentialProbeFakeClient{listErr: tc.err})
			if resp := testGateway(t, c); resp.Status != appwire.AuthTestStatusAuthRejected {
				t.Fatalf("test status = %q, want auth_rejected", resp.Status)
			}
			got := gatewayError(t, c)
			if got != tc.want {
				t.Fatalf("error = %q, want %q", got, tc.want)
			}
			status, _ := c.Status(appwire.AuthStatusParams{Provider: "gateway"})
			encoded, err := json.Marshal(status)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), rejectionSecret) || strings.Contains(string(encoded), "Incorrect") {
				t.Fatalf("status carries provider text: %s", encoded)
			}
		})
	}
}

func TestCredentialRejection_OtherFailuresAreNotCredentialErrors(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		notLive bool
	}{
		{name: "rate limit", err: llm.ErrorFromHTTPStatus("gateway", 429, "slow down", nil, nil)},
		{name: "server", err: llm.ErrorFromHTTPStatus("gateway", 503, "overloaded", nil, nil)},
		{name: "not found", err: llm.ErrorFromHTTPStatus("gateway", 404, "no such endpoint", nil, nil)},
		{name: "network", err: errors.New("dial tcp 192.0.2.1:443: connection refused")},
		{name: "configuration", err: &llm.ConfigurationError{Message: "invalid provider configuration"}},
		{name: "unsupported", notLive: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newRejectionController(t, &credentialProbeFakeClient{listErr: tc.err, notLive: tc.notLive})
			testGateway(t, c)
			if got := gatewayError(t, c); got != "" {
				t.Fatalf("error = %q, want none for a failure that is not a rejected credential", got)
			}
		})
	}
}

// An inconclusive probe (the endpoint cannot be reached) says nothing about
// the credential, so a rejection recorded before it stands.
func TestCredentialRejection_AnInconclusiveProbeKeepsTheRejection(t *testing.T) {
	client := &credentialProbeFakeClient{listErr: llm.ErrorFromHTTPStatus("gateway", 401, "bad key", nil, nil)}
	c, _ := newRejectionController(t, client)
	testGateway(t, c)
	client.listErr = errors.New("dial tcp 192.0.2.1:443: connection refused")
	testGateway(t, c)
	if got := gatewayError(t, c); got == "" {
		t.Fatal("an unreachable endpoint cleared the recorded rejection")
	}
}

func TestCredentialRejection_ASuccessfulTestClearsIt(t *testing.T) {
	client := &credentialProbeFakeClient{listErr: llm.ErrorFromHTTPStatus("gateway", 401, "bad key", nil, nil)}
	c, _ := newRejectionController(t, client)
	testGateway(t, c)
	if gatewayError(t, c) == "" {
		t.Fatal("precondition: the 401 was not recorded")
	}
	client.listErr = nil
	if resp := testGateway(t, c); resp.Status != appwire.AuthTestStatusSuccess {
		t.Fatalf("test status = %q, want success", resp.Status)
	}
	if got := gatewayError(t, c); got != "" {
		t.Fatalf("error = %q after a successful test, want none", got)
	}
}

func TestCredentialRejection_CredentialWritesClearIt(t *testing.T) {
	cases := []struct {
		name  string
		write func(*hubAuthController) error
	}{
		{name: "set key", write: func(c *hubAuthController) error {
			_, err := c.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "gateway", Value: "sk-replacement"})
			return err
		}},
		{name: "clear key", write: func(c *hubAuthController) error {
			_, err := c.ApiKeyClear(appwire.AuthApiKeyClearParams{Provider: "gateway"})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newRejectionController(t, &credentialProbeFakeClient{listErr: llm.ErrorFromHTTPStatus("gateway", 401, "bad key", nil, nil)})
			testGateway(t, c)
			if gatewayError(t, c) == "" {
				t.Fatal("precondition: the 401 was not recorded")
			}
			if err := tc.write(c); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got := gatewayError(t, c); got != "" {
				t.Fatalf("error = %q after a credential write, want none", got)
			}
		})
	}
}

// A rejection belongs to the configuration it was seen under: pointing the
// instance somewhere else is not the credential the provider refused.
func TestCredentialRejection_AConfigurationChangeRetiresIt(t *testing.T) {
	c, reconfigure := newRejectionController(t, &credentialProbeFakeClient{listErr: llm.ErrorFromHTTPStatus("gateway", 401, "bad key", nil, nil)})
	testGateway(t, c)
	if gatewayError(t, c) == "" {
		t.Fatal("precondition: the 401 was not recorded")
	}
	reconfigure(rejectionInstances("http://elsewhere.test/v1"))
	if got := gatewayError(t, c); got != "" {
		t.Fatalf("error = %q after the endpoint changed, want none", got)
	}
}

// A key written while a probe of the old key is in flight must not inherit
// that probe's rejection.
func TestCredentialRejection_AProbeThatRacedAWriteRecordsNothing(t *testing.T) {
	client := &credentialProbeFakeClient{
		listErr: llm.ErrorFromHTTPStatus("gateway", 401, "bad key", nil, nil),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	c, _ := newRejectionController(t, client)
	done := make(chan appwire.AuthTestResponse, 1)
	go func() {
		resp, _ := c.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: "gateway"})
		done <- resp
	}()
	<-client.started
	if _, err := c.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "gateway", Value: "sk-replacement"}); err != nil {
		t.Fatalf("ApiKeySet: %v", err)
	}
	close(client.release)
	if resp := <-done; resp.Status != appwire.AuthTestStatusAuthRejected {
		t.Fatalf("test status = %q, want auth_rejected", resp.Status)
	}
	if got := gatewayError(t, c); got != "" {
		t.Fatalf("error = %q: the old key's rejection landed on the key written during the probe", got)
	}
}

// Clients re-read statuses on evener/auth/updated, so a rejection appearing or
// clearing is announced; a probe that changes nothing is not.
func TestCredentialRejection_AnnouncesOnlyChanges(t *testing.T) {
	client := &credentialProbeFakeClient{listErr: llm.ErrorFromHTTPStatus("gateway", 401, "bad key", nil, nil)}
	c, _ := newRejectionController(t, client)
	var announced []string
	c.credentialRejectionChanged = func(name string) { announced = append(announced, name) }

	testGateway(t, c)
	testGateway(t, c)
	client.listErr = nil
	testGateway(t, c)
	testGateway(t, c)

	if strings.Join(announced, ",") != "gateway,gateway" {
		t.Fatalf("announced = %v, want one for the rejection and one for its clearing", announced)
	}
}

// The RPC wiring turns a change into the evener/auth/updated broadcast every
// client already re-reads statuses on.
func TestCredentialRejection_TestRPCBroadcastsAuthUpdated(t *testing.T) {
	c, _ := newRejectionController(t, &credentialProbeFakeClient{listErr: llm.ErrorFromHTTPStatus("gateway", 401, "bad key", nil, nil)})
	server := appserver.NewServer(appserver.ServerConfig{})
	registerAuthHandlers(server, c)
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	t.Cleanup(httpServer.Close)
	client := dialHubRPC(t, httpServer)
	t.Cleanup(func() { client.Close() })

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	var resp appwire.AuthTestResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerAuthTest, appwire.AuthTestParams{Provider: "gateway"}, &resp); err != nil {
		t.Fatalf("auth/test: %v", err)
	}
	got := waitForAuthUpdated(t, client)
	if got.Provider != "gateway" {
		t.Fatalf("auth/updated = %+v, want it to name gateway", got)
	}
}
