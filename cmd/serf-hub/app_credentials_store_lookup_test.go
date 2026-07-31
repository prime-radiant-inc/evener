package main

// serf/auth/test decides whether to probe at all by asking whether the instance
// has an effective credential. There is exactly one right answer to that
// question — the credential the launch path acts on, which is
// credentials.Store.ResolveKey keyed by the instance's behavior tag
// (cmdutil.LoadClient) — and instanceHasEffectiveCredential answered it from a
// narrower notion of its own that never consulted the store. A key living in
// the environment was invisible to it, so the "Test credentials" button told
// the user to add a key for an instance the hub was already running. Kata gpbz.

import (
	"context"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/auth/openai/oaitest"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm/providercfg"
)

// newStoreBackedProbeController builds a credential-test controller with an
// isolated credentials store, an isolated OAuth state dir, and no provider key
// anywhere in the environment. Each subtest then states the one credential it
// is about.
func newStoreBackedProbeController(t *testing.T, client credentialProbeClient, cfg providercfg.Config) *hubAuthController {
	t.Helper()
	stateDir := oaitest.IsolateOpenAIAuth(t)
	clearProviderKeysFromEnvironment(t)
	c := newCredentialProbeController(t, client, cfg)
	c.stateDir = stateDir
	return c
}

// TestAuthTestCredentials_ProbesTheCredentialTheLaunchPathResolves holds
// serf/auth/test to the launch path's answer. Each case seeds a credential in a
// layer ResolveKey resolves, checks ResolveKey does resolve it, and then
// requires the RPC to probe rather than report the instance unconfigured.
//
// The openai case is here because it is the one a tag-keyed rewrite alone would
// still get wrong: openAIInstanceStatus consults OPENAI_API_KEY only for an
// instance literally named "openai", while ResolveKey resolves the tag's key
// for any name — which is what launch does.
func TestAuthTestCredentials_ProbesTheCredentialTheLaunchPathResolves(t *testing.T) {
	for _, tt := range []struct {
		name string
		inst providercfg.InstanceConfig
		seed func(t *testing.T, c *hubAuthController)
	}{
		{
			name: "anthropic key in the environment",
			inst: providercfg.InstanceConfig{Name: "work", Type: "anthropic"},
			seed: func(t *testing.T, _ *hubAuthController) {
				t.Setenv(envvars.AnthropicAPIKey.Name, "sk-ant-probe")
			},
		},
		{
			name: "openai key in the environment for a named instance",
			inst: providercfg.InstanceConfig{Name: "openai-work", Type: "openai"},
			seed: func(t *testing.T, _ *hubAuthController) {
				t.Setenv(envvars.OpenAIAPIKey.Name, "sk-openai-probe")
			},
		},
		{
			// The third site kata jd5s left keyed on the raw type. This row
			// used to seed OPENAI_COMPATIBLE_API_KEY on the premise that an
			// instance with no base_url "takes the URL from the environment";
			// it does not. newOpenAIChatCompletionsInstance reads inst.BaseURL
			// and a compile-time constant, so with no base_url the adapter
			// targets api.openai.com and OPENAI_API_KEY is the key that signs
			// its requests — OPENAI_COMPATIBLE_API_KEY authenticates whatever
			// host OPENAI_COMPATIBLE_BASE_URL names, which this instance never
			// contacts (kata z1gm). The row still reaches the helper where the
			// openai-typed key would send it to the OAuth status path instead,
			// which is what it was written to catch.
			name: "openai key for an openai instance on chat-completions with no base_url",
			inst: providercfg.InstanceConfig{Name: "local", Type: "openai", APIStyle: providercfg.StyleChatCompletions},
			seed: func(t *testing.T, _ *hubAuthController) {
				t.Setenv(envvars.OpenAIAPIKey.Name, "sk-openai-probe")
			},
		},
		{
			name: "anthropic key stored in the credentials file",
			inst: providercfg.InstanceConfig{Name: "work", Type: "anthropic"},
			seed: func(t *testing.T, c *hubAuthController) {
				if err := c.creds.Set("work", "sk-ant-stored"); err != nil {
					t.Fatalf("Set(work): %v", err)
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &credentialProbeFakeClient{}
			c := newStoreBackedProbeController(t, client, providercfg.Config{
				Instances: []providercfg.InstanceConfig{tt.inst},
			})
			tt.seed(t, c)

			tag := providercfg.CredentialTag(string(tt.inst.Type), string(tt.inst.APIStyle), tt.inst.BaseURL)
			key, src := c.creds.ResolveKey(tt.inst.Name, tag)
			if key == "" {
				t.Fatalf("ResolveKey(%q, %q) resolved nothing, so this case has no credential to be wrong about", tt.inst.Name, tag)
			}

			resp, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: tt.inst.Name})
			if err != nil {
				t.Fatalf("TestCredentials(%q): %v", tt.inst.Name, err)
			}
			if resp.Status != appwire.AuthTestStatusSuccess {
				t.Errorf("TestCredentials(%q) = %q (%q), want %q: ResolveKey(%q, %q) resolves a key from %q, and that is the credential launch starts this instance with",
					tt.inst.Name, resp.Status, resp.Message, appwire.AuthTestStatusSuccess, tt.inst.Name, tag, src)
			}
			if got := client.callCount(); got != 1 {
				t.Errorf("probe calls = %d, want 1: this instance has a resolvable credential, so serf/auth/test must actually test it", got)
			}
		})
	}
}
