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
	// fail (breakCredentialWrites).
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
		{name: "unauthorized", err: llm.ErrorFromHTTPStatus("gateway", 401, "Incorrect API key provided: "+rejectionSecret, nil, nil), want: "The provider rejected this credential (HTTP 401). Replace the key or sign in again."},
		{name: "forbidden", err: llm.ErrorFromHTTPStatus("gateway", 403, "forbidden for "+rejectionSecret, nil, nil), want: "The provider rejected this credential (HTTP 403). Replace the key or sign in again."},
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

// Renaming an instance moves its credential without a credential write, so
// the rename drops the rejection too. Without that, the record outlives the
// credential: moving the same key back under the old name out of band brings
// the old rejection back with it.
func TestCredentialRejection_InstanceRenameDropsIt(t *testing.T) {
	c, _ := newRejectionController(t, &credentialProbeFakeClient{listErr: llm.ErrorFromHTTPStatus("gateway", 401, "bad key", nil, nil)})
	testGateway(t, c)
	if gatewayError(t, c) == "" {
		t.Fatal("precondition: the 401 was not recorded")
	}
	if err := (&hubInstancesController{auth: c}).moveCredentials("gateway", "renamed"); err != nil {
		t.Fatalf("moveCredentials: %v", err)
	}
	if err := c.creds.Move("renamed", "gateway"); err != nil {
		t.Fatalf("move back: %v", err)
	}
	if err := c.reloadRegistry(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := gatewayError(t, c); got != "" {
		t.Fatalf("error = %q: the rejection outlived the rename", got)
	}
}

// newRemovableRejection is an instances fixture holding "work", an instance
// with a stored key the provider rejected, and a reader for work's error.
func newRemovableRejection(t *testing.T) (*instancesFixture, func() string) {
	t.Helper()
	f := newInstancesFixture(t, nil)
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-compatible", BaseURL: "http://provider.test/v1"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	probe := f.ctl.auth.beginCredentialProbe("work")
	f.ctl.auth.settleCredentialProbe(probe, llm.ModelListing{}, llm.ErrorFromHTTPStatus("work", 401, "bad key", nil, nil))
	workError := func() string {
		t.Helper()
		status, err := f.ctl.auth.Status(appwire.AuthStatusParams{Provider: "work"})
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		return status.Error
	}
	if workError() == "" {
		t.Fatal("precondition: the 401 was not recorded")
	}
	return f, workError
}

// A removal that stands takes the rejection with the credential: recreating
// the same instance and putting the same key back out of band does not bring
// the old rejection back.
func TestCredentialRejection_InstanceRemovalDropsIt(t *testing.T) {
	f, workError := newRemovableRejection(t)
	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := f.ctl.Create(appwire.InstanceCreateParams{Name: "work", Base: "openai-compatible", BaseURL: "http://provider.test/v1"}); err != nil {
		t.Fatalf("Create again: %v", err)
	}
	if err := f.store.Set("work", "sk-stored"); err != nil {
		t.Fatalf("Set again: %v", err)
	}
	if err := f.ctl.auth.reloadRegistry(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := workError(); got != "" {
		t.Fatalf("error = %q: the rejection outlived the removal", got)
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
			requireWritableDirRefusal(t)
			c, _ := newRejectionController(t, &credentialProbeFakeClient{listErr: llm.ErrorFromHTTPStatus("gateway", 401, "bad key", nil, nil)})
			testGateway(t, c)
			if gatewayError(t, c) == "" {
				t.Fatal("precondition: the 401 was not recorded")
			}
			breakCredentialWrites(t, c.creds.Path())
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

// A removal can also fail after the credential is gone, on the config write
// or the reload, and then put the credential back. The rejection must come
// back with it: it is dropped only when the removal stands.
func TestCredentialRejection_ARemovalRolledBackAfterTheCredentialWentKeepsIt(t *testing.T) {
	f, workError := newRemovableRejection(t)
	// The config write fails once the credential is already deleted: the
	// OAuth deletion, the last step of that cleanup, replaces providers.toml
	// with a directory, as
	// TestInstances_RemoveRestoresCredentialsWhenTheConfigWriteFails does.
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

	if err := f.ctl.Remove(appwire.InstanceRemoveParams{Name: "work"}); err == nil {
		t.Fatal("Remove = nil, want the config write failure")
	}
	if v, _ := f.store.Get("work"); v != "sk-stored" {
		t.Fatalf("precondition: stored key = %q, want the rollback to have restored it", v)
	}
	if workError() == "" {
		t.Fatal("the rolled-back removal dropped the rejection of the key it put back")
	}
}

// A keyless instance sends no credential, so a 401 or 403 from it is not a
// credential the provider rejected, and nothing the user could replace.
func TestCredentialRejection_AKeylessInstanceRecordsNothing(t *testing.T) {
	for _, auth := range []string{registry.AuthNone, registry.AuthOptionalBearer} {
		t.Run(auth, func(t *testing.T) {
			clearProviderKeysFromEnvironment(t)
			client := &credentialProbeFakeClient{listErr: llm.ErrorFromHTTPStatus("local", 401, "unauthorized", nil, nil)}
			c := newCredentialProbeController(t, client, map[string]registry.Provider{"local": {
				Base:      "openai-compatible",
				Transport: registry.Transport{BaseURL: "http://localhost:8080/v1", Auth: auth},
			}}, nil)
			resp, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: "local"})
			if err != nil {
				t.Fatalf("TestCredentials: %v", err)
			}
			if client.callCount() != 1 {
				t.Fatalf("precondition: the probe dialed %d times, want once (test status %q)", client.callCount(), resp.Status)
			}
			status, err := c.Status(appwire.AuthStatusParams{Provider: "local"})
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if status.ActiveSource != "none" {
				t.Fatalf("precondition: active source = %q, want none", status.ActiveSource)
			}
			if status.Error != "" {
				t.Fatalf("error = %q for an instance that sent no credential", status.Error)
			}
		})
	}
}

// The status in the message is the one the rejection is about: a 401 or 403.
// A rejection classified from another status's message ("invalid key" in a
// 400) or a status mentioned inside a longer number is no HTTP 401 or 403.
func TestCredentialRejection_StatusIsOnlyA401Or403(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		wantRejected bool
		wantStatus   int
	}{
		{name: "invalid key in a 400", err: llm.ErrorFromHTTPStatus("gateway", 400, "Invalid key provided", nil, nil), wantRejected: true, wantStatus: 0},
		{name: "text 401", err: errors.New("models: HTTP 401: denied"), wantRejected: true, wantStatus: 401},
		{name: "text status=403", err: errors.New("request failed status=403"), wantRejected: true, wantStatus: 403},
		{name: "text 4010", err: errors.New("models: HTTP 4010 upstream"), wantRejected: false},
		{name: "text status=4031", err: errors.New("request failed status=4031"), wantRejected: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, rejected := credentialRejectionStatus(tc.err)
			if rejected != tc.wantRejected || (rejected && status != tc.wantStatus) {
				t.Fatalf("credentialRejectionStatus = (%d, %v), want (%d, %v)", status, rejected, tc.wantStatus, tc.wantRejected)
			}
		})
	}
}

// A removal that stands although it returns an error (the credential is gone
// and could not be put back) takes the rejection with the credential, as a
// clean removal does. The registry stays unloadable after this failure, so
// the record itself is what is asserted.
func TestCredentialRejection_AStandingRemovalDropsIt(t *testing.T) {
	f := newFlakyReloadFixture(t, "groq", func(load int) bool { return load >= 2 })
	probe := f.ctl.auth.beginCredentialProbe("groq")
	f.ctl.auth.settleCredentialProbe(probe, llm.ModelListing{}, llm.ErrorFromHTTPStatus("groq", 401, "bad key", nil, nil))
	status, err := f.ctl.auth.Status(appwire.AuthStatusParams{Provider: "groq"})
	if err != nil || status.Error == "" {
		t.Fatalf("precondition: groq status = %+v (%v), want the 401 recorded", status, err)
	}
	// The put-back the rollback performs cannot land, so the removal stands.
	f.ctl.auth.setCredential = func(string, string) error {
		return errors.New("credentials.toml: write: read-only")
	}

	err = f.ctl.Remove(appwire.InstanceRemoveParams{Name: "groq"})
	if _, standing := errors.AsType[removeAppliedError](err); !standing {
		t.Fatalf("precondition: Remove = %v, want a standing removal", err)
	}
	f.ctl.auth.rejections.mu.Lock()
	_, kept := f.ctl.auth.rejections.byName["groq"]
	f.ctl.auth.rejections.mu.Unlock()
	if kept {
		t.Fatal("the standing removal kept the rejection of the credential it deleted")
	}
}

// A rejection with no HTTP status (an adapter that reports only the kind)
// still says what went wrong and what to do, naming no status.
func TestCredentialRejection_AMessageWithNoStatusStillSaysWhatToDo(t *testing.T) {
	if got, want := credentialRejectedMessage(0), "The provider rejected this credential. Replace the key or sign in again."; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}
