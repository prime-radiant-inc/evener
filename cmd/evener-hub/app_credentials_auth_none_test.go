package hub

// Whether an instance has a credential to find is now one registry fact —
// Transport.Auth plus the resolved Credential.Source (spec §10) — and three
// hub surfaces read it: the launch preflight, evener/auth/status, and
// evener/auth/test. Kata 5hmq was filed when evener/auth/test answered from a
// literal provider name instead, so the pane called a provider the hub
// launches happily unconfigured and never issued the probe it exists to issue.
// These tests hold all three to the same verdict.

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/llm/registry"
)

// newStoreBackedProbeController builds a credential-test controller with an
// isolated credentials store, an isolated OAuth state dir, and no provider key
// anywhere in the environment. Each case then states the one credential it is
// about.
func newStoreBackedProbeController(t *testing.T, client credentialProbeClient, instances map[string]registry.Provider, env map[string]string) *hubAuthController {
	t.Helper()
	oaitest.IsolateOpenAIAuth(t)
	clearProviderKeysFromEnvironment(t)
	return newCredentialProbeController(t, client, instances, env)
}

// TestAuthSchemesNeedingNothing_EveryCredentialGateAgrees: an instance whose
// auth scheme takes no credential launches, reports "none" without being
// unconfigured, and is probed rather than refused.
func TestAuthSchemesNeedingNothing_EveryCredentialGateAgrees(t *testing.T) {
	const instanceName = "workhorse"
	for _, auth := range []string{registry.AuthNone, registry.AuthOptionalBearer} {
		t.Run(auth, func(t *testing.T) {
			client := &credentialProbeFakeClient{}
			c := newStoreBackedProbeController(t, client, map[string]registry.Provider{
				instanceName: {
					Base:      "openai-compatible",
					Transport: registry.Transport{BaseURL: "http://127.0.0.1:9/v1", Auth: auth},
				},
			}, nil)

			if err := validateProviderCredentials(instanceName, c.reg); err != nil {
				t.Fatalf("the launch preflight refused an auth = %q instance with no credential: %v", auth, err)
			}
			status, err := c.Status(appwire.AuthStatusParams{Provider: instanceName})
			if err != nil {
				t.Fatalf("Status(%q): %v", instanceName, err)
			}
			if status.ActiveSource != "none" {
				t.Fatalf("evener/auth/status %q activeSource = %q, want none", instanceName, status.ActiveSource)
			}

			resp, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: instanceName})
			if err != nil {
				t.Fatalf("TestCredentials(%q): %v", instanceName, err)
			}
			if resp.Status != appwire.AuthTestStatusSuccess {
				t.Errorf("evener/auth/test %q = %q (%q), want %q: the launch preflight and evener/auth/status both report that this instance needs no credential",
					instanceName, resp.Status, resp.Message, appwire.AuthTestStatusSuccess)
			}
			if got := client.callCount(); got != 1 {
				t.Errorf("probe calls = %d, want 1: this instance has no credential to be missing, so evener/auth/test must actually probe it", got)
			}
		})
	}
}

// TestAuthSchemeExemptionBelongsToTheInstance: the exemption is a property of
// the instance's auth scheme, not of the name a user picked for it.
func TestAuthSchemeExemptionBelongsToTheInstance(t *testing.T) {
	client := &credentialProbeFakeClient{}
	c := newStoreBackedProbeController(t, client, map[string]registry.Provider{
		// Named after an auth-none vendor, but authenticating with a key.
		"ollama": {Base: "anthropic", APIKeyEnv: []string{"OLLAMA_SHAPED_KEY"}},
	}, nil)

	resp, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: "ollama"})
	if err != nil {
		t.Fatalf("TestCredentials: %v", err)
	}
	if resp.Status != appwire.AuthTestStatusMissing {
		t.Errorf("evener/auth/test = %q (%q), want %q: this instance authenticates with a key, and it has none",
			resp.Status, resp.Message, appwire.AuthTestStatusMissing)
	}
	if got := client.callCount(); got != 0 {
		t.Errorf("probe calls = %d, want 0: an instance with no credential must not be probed", got)
	}
	if err := validateProviderCredentials("ollama", c.reg); err == nil {
		t.Error("the launch preflight accepted a key-authenticated instance with no key")
	}
}

// TestAuthTestCredentials_ProbesTheCredentialTheLaunchPathResolves holds
// evener/auth/test to the launch path's answer. Each case seeds a credential in
// one layer of spec §10's resolution order and requires the RPC to probe rather
// than report the instance unconfigured. Kata gpbz: a key living in the
// environment used to be invisible to this gate, so the pane told the user to
// add a key for an instance the hub was already running.
func TestAuthTestCredentials_ProbesTheCredentialTheLaunchPathResolves(t *testing.T) {
	for _, tt := range []struct {
		name     string
		instance string
		provider registry.Provider
		env      map[string]string
		wantSrc  string
		seed     func(t *testing.T, c *hubAuthController)
	}{
		{
			name:     "vendor key inherited from the base",
			instance: "work",
			provider: registry.Provider{Base: "anthropic"},
			env:      map[string]string{"ANTHROPIC_API_KEY": "sk-ant-probe"},
			wantSrc:  "env:ANTHROPIC_API_KEY",
		},
		{
			name:     "name-scoped key for an instance that is not a registry id",
			instance: "openai-work",
			provider: registry.Provider{Base: "openai", Transport: registry.Transport{BaseURL: "http://127.0.0.1:9/v1"}},
			env:      map[string]string{"OPENAI_WORK_API_KEY": "sk-openai-probe"},
			wantSrc:  "env:OPENAI_WORK_API_KEY",
		},
		{
			name:     "inline api_key on the instance",
			instance: "work",
			provider: registry.Provider{Base: "anthropic", APIKey: "sk-ant-inline"},
			wantSrc:  "api_key",
		},
		{
			name:     "key stored in credentials.toml under the instance name",
			instance: "work",
			provider: registry.Provider{Base: "anthropic"},
			wantSrc:  "store",
			seed: func(t *testing.T, c *hubAuthController) {
				if err := c.creds.Set("work", "sk-ant-stored"); err != nil {
					t.Fatalf("Set(work): %v", err)
				}
				if err := c.reg.Reload(); err != nil {
					t.Fatalf("Reload: %v", err)
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &credentialProbeFakeClient{}
			c := newStoreBackedProbeController(t, client,
				map[string]registry.Provider{tt.instance: tt.provider}, tt.env)
			if tt.seed != nil {
				tt.seed(t, c)
			}

			inst, ok := c.reg.Get().Instance(tt.instance)
			if !ok {
				t.Fatalf("the registry has no instance %q, so this case has no credential to be wrong about", tt.instance)
			}
			if inst.CredentialSource != tt.wantSrc {
				t.Fatalf("credential source = %q, want %q — the case is not exercising the layer it names", inst.CredentialSource, tt.wantSrc)
			}

			resp, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: tt.instance})
			if err != nil {
				t.Fatalf("TestCredentials(%q): %v", tt.instance, err)
			}
			if resp.Status != appwire.AuthTestStatusSuccess {
				t.Errorf("TestCredentials(%q) = %q (%q), want %q: the registry resolves a credential from %q, and that is what launch starts this instance with",
					tt.instance, resp.Status, resp.Message, appwire.AuthTestStatusSuccess, tt.wantSrc)
			}
			if got := client.callCount(); got != 1 {
				t.Errorf("probe calls = %d, want 1: this instance has a resolvable credential, so evener/auth/test must actually test it", got)
			}
		})
	}
}
