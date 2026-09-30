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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
// A second instance, "other", is there to be written to while a gateway probe
// is in flight.
func rejectionInstances(baseURL string) map[string]registry.Provider {
	return map[string]registry.Provider{
		"gateway": {
			Base:      "openai-compatible",
			Transport: registry.Transport{BaseURL: baseURL},
		},
		"other": {
			Base:      "openai-compatible",
			Transport: registry.Transport{BaseURL: "http://other.test/v1"},
		},
	}
}

// newRejectionController is newCredentialProbeController over a registry whose
// instances the test can replace and reload, so it can change the instance's
// configuration under a recorded rejection.
func newRejectionController(t *testing.T, client credentialProbeClient) (*hubAuthController, func(map[string]registry.Provider)) {
	t.Helper()
	clearProviderKeysFromEnvironment(t)
	stateDir := t.TempDir()
	// The store sits in a directory of its own, so a test can make its saves
	// fail (breakCredentialsStore).
	store, err := credentials.LoadStore(filepath.Join(t.TempDir(), "creds", "credentials.toml"))
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
			status, err := c.Status(appwire.AuthStatusParams{Provider: "gateway"})
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
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
		{name: "conditional set", write: func(c *hubAuthController) error {
			resp, err := c.ApiKeyConditionalSet(appwire.ApiKeyConditionalSetParams{Provider: "gateway", Value: "sk-pushed"})
			if err == nil && resp.Action != appwire.ApiKeyConditionalSetActionUpdated {
				return fmt.Errorf("conditional set action = %q (%s), want it to write", resp.Action, resp.Reason)
			}
			return err
		}},
		{name: "logout", write: func(c *hubAuthController) error {
			_, err := c.Logout(appwire.AuthLogoutParams{Provider: "gateway"})
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

// Removing or renaming an instance moves or deletes its credential without a
// credential write, so those paths drop the rejection too. Without that, the
// record outlives the credential: putting the same key back under the same
// name out of band (here, straight into the store) brings the old rejection
// back with it.
func TestCredentialRejection_InstanceRemovalAndRenameDropIt(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*hubInstancesController) error
		undo   func(*credentials.Store) error
	}{
		{
			name: "remove",
			mutate: func(c *hubInstancesController) error {
				_, err := c.removeCredentials("gateway")
				return err
			},
			undo: func(store *credentials.Store) error { return store.Set("gateway", rejectionSecret) },
		},
		{
			name:   "rename",
			mutate: func(c *hubInstancesController) error { return c.moveCredentials("gateway", "renamed") },
			undo:   func(store *credentials.Store) error { return store.Move("renamed", "gateway") },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newRejectionController(t, &credentialProbeFakeClient{listErr: llm.ErrorFromHTTPStatus("gateway", 401, "bad key", nil, nil)})
			testGateway(t, c)
			if gatewayError(t, c) == "" {
				t.Fatal("precondition: the 401 was not recorded")
			}
			if err := tc.mutate(&hubInstancesController{auth: c}); err != nil {
				t.Fatalf("mutate: %v", err)
			}
			if err := tc.undo(c.creds); err != nil {
				t.Fatalf("undo: %v", err)
			}
			if err := c.reloadRegistry(); err != nil {
				t.Fatalf("reload: %v", err)
			}
			if got := gatewayError(t, c); got != "" {
				t.Fatalf("error = %q: the rejection outlived the %s", got, tc.name)
			}
		})
	}
}

// With no fingerprint key the hub serves no configuration revision, so a
// rejection could not tell its own configuration from another; it is not
// recorded at all.
func TestCredentialRejection_NoRevisionRecordsNothing(t *testing.T) {
	c, _ := newRejectionController(t, &credentialProbeFakeClient{listErr: llm.ErrorFromHTTPStatus("gateway", 401, "bad key", nil, nil)})
	// A state root that is a file, not a directory, is one no fingerprint
	// key can be resolved under.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c.stateDir = blocked
	if key, err := resolveEndpointFingerprintKey(c.stateDir); err == nil && len(key) > 0 {
		t.Fatal("precondition: a fingerprint key resolved under a file")
	}
	testGateway(t, c)
	if got := gatewayError(t, c); got != "" {
		t.Fatalf("error = %q recorded with no configuration revision", got)
	}
}

// breakCredentialsStore makes every later save of c's credentials store fail:
// its directory becomes a regular file, so the save can neither create it nor
// write into it. The store then leaves its entries as they were.
func breakCredentialsStore(t *testing.T, c *hubAuthController) {
	t.Helper()
	dir := filepath.Dir(c.creds.Path())
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A removal or rename that fails leaves the rejected credential where it was,
// so its rejection stands.
func TestCredentialRejection_AFailedRemovalOrRenameKeepsIt(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*hubInstancesController) error
	}{
		{name: "remove", mutate: func(c *hubInstancesController) error {
			_, err := c.removeCredentials("gateway")
			return err
		}},
		{name: "rename", mutate: func(c *hubInstancesController) error { return c.moveCredentials("gateway", "renamed") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newRejectionController(t, &credentialProbeFakeClient{listErr: llm.ErrorFromHTTPStatus("gateway", 401, "bad key", nil, nil)})
			testGateway(t, c)
			if gatewayError(t, c) == "" {
				t.Fatal("precondition: the 401 was not recorded")
			}
			breakCredentialsStore(t, c)
			if err := tc.mutate(&hubInstancesController{auth: c}); err == nil {
				t.Fatalf("%s succeeded on a store that cannot save", tc.name)
			}
			if _, stored := c.storedKey("gateway"); !stored {
				t.Fatal("precondition: the failed mutation did not leave the key in place")
			}
			if got := gatewayError(t, c); got == "" {
				t.Fatalf("the failed %s dropped the rejection of a key still in place", tc.name)
			}
		})
	}
}

// Only a write to the probed instance can replace the key it dialed: a write
// to another instance leaves the probe's outcome standing.
func TestCredentialRejection_AnotherInstancesWriteKeepsTheProbe(t *testing.T) {
	c, _ := newRejectionController(t, &credentialProbeFakeClient{})
	probe := c.beginCredentialProbe("gateway")
	if _, err := c.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "other", Value: "sk-other"}); err != nil {
		t.Fatalf("ApiKeySet(other): %v", err)
	}
	c.settleCredentialProbe(probe, llm.ModelListing{}, llm.ErrorFromHTTPStatus("gateway", 401, "bad key", nil, nil))
	if got := gatewayError(t, c); got == "" {
		t.Fatal("a write to another instance voided the gateway probe's rejection")
	}
}

// A probe settles only if the instance still has the configuration it
// probed: an outcome about the old endpoint must neither clear nor replace a
// rejection recorded for the new one.
func TestCredentialRejection_AProbeOfAnOldConfigurationChangesNothing(t *testing.T) {
	client := &credentialProbeFakeClient{listErr: llm.ErrorFromHTTPStatus("gateway", 403, "forbidden", nil, nil)}
	c, reconfigure := newRejectionController(t, client)
	stale := c.beginCredentialProbe("gateway")
	reconfigure(rejectionInstances("http://elsewhere.test/v1"))
	testGateway(t, c)
	want := gatewayError(t, c)
	if want == "" {
		t.Fatal("precondition: the new configuration's 403 was not recorded")
	}

	c.settleCredentialProbe(stale, llm.ModelListing{Live: true}, nil)
	if got := gatewayError(t, c); got != want {
		t.Fatalf("a success about the old endpoint changed the error to %q, want %q", got, want)
	}
	c.settleCredentialProbe(stale, llm.ModelListing{}, llm.ErrorFromHTTPStatus("gateway", 401, "bad key", nil, nil))
	if got := gatewayError(t, c); got != want {
		t.Fatalf("a rejection of the old endpoint changed the error to %q, want %q", got, want)
	}
}
