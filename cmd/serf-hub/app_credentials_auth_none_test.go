package main

// Whether a provider authenticates at all is a registry fact. envvars/providers.go
// carries one row per provider with its auth modes, and envvars.RequiresNoCredential
// is the predicate kata nrv4 named for the "none" case — the one the launch
// preflight (validateProviderCredentials), credentials.Store.List and
// hubAuthController.instanceStatus all ask. credentialRequired, the other half of
// serf/auth/test's gate, answered the same question from the literal provider name
// "ollama", so the hub and the registry agreed only for as long as ollama stayed the
// only auth-none row. Kata 5hmq; the same duplicate-fact failure mode as kata f1zs,
// in a fourth place.
//
// The test states the property the registry is supposed to guarantee rather than the
// one provider that happens to hold it: for EVERY provider the registry says
// authenticates nothing, all four sites give one instance the same verdict with no
// credential anywhere. It walks the registry, so an auth-none provider added
// tomorrow — the event the kata predicts, and the mutation this was proven with — is
// covered here with no edit.

import (
	"context"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/llm/providercfg"
)

// TestAuthNoneProvider_EveryCredentialGateFollowsTheRegistry holds the four sites
// that decide whether an instance has a credential to a single answer for every
// auth-none provider in the registry. serf/auth/test is the one that used to answer
// from a name: with the other three exempting the instance and it alone demanding a
// key the provider cannot have, the credentials pane reported a provider the hub
// launches happily as unconfigured, and never issued the probe it exists to issue.
func TestAuthNoneProvider_EveryCredentialGateFollowsTheRegistry(t *testing.T) {
	// The instance is deliberately not named after its type: needing no credential
	// is a property of the provider, not of the name a user picked for it.
	const instanceName = "workhorse"

	covered := 0
	for _, p := range envvars.Providers() {
		if !envvars.RequiresNoCredential(p.Name) {
			continue
		}
		if err := providercfg.ValidateType(providercfg.Type(p.Name)); err != nil {
			// An auth-none row no instance can declare — a behavior tag rather
			// than a type, as "openai-compatible" is — has no instance to gate.
			// If it becomes a type, this covers it with no edit here.
			t.Logf("%q is an auth-none registry row but not a configurable instance type, so no instance can reach these gates: %v", p.Name, err)
			continue
		}
		covered++
		t.Run(p.Name, func(t *testing.T) {
			inst := providercfg.InstanceConfig{Name: instanceName, Type: providercfg.Type(p.Name)}
			cfg := providercfg.Config{Default: instanceName, Instances: []providercfg.InstanceConfig{inst}}

			// One store and one OAuth state dir behind all four gates, with no
			// provider key anywhere in the environment: an ambient key would
			// satisfy every gate below without the auth-mode rule ever applying,
			// and the test would pass having proven nothing.
			client := &credentialProbeFakeClient{}
			c := newStoreBackedProbeController(t, client, cfg)
			c.providersConfigPath = writeProvidersConfig(t, t.TempDir(), cfg)

			// The three sites that already ask the registry. They are asserted
			// first so that a failure below reads as the disagreement it is.
			if err := validateProviderCredentials(instanceName, c.creds, []string{}, c.providersConfigPath); err != nil {
				t.Fatalf("the launch preflight refused a %q instance with no credential: %v — %q declares auth mode \"none\", so there is no credential to find", p.Name, err, p.Name)
			}
			var storeRow credentials.Provider
			for _, row := range c.creds.List() {
				if row.Name == p.Name {
					storeRow = row
					break
				}
			}
			if storeRow.Source != credentials.SourceNone {
				t.Fatalf("credentials.Store.List() %q source = %q, want %q", p.Name, storeRow.Source, credentials.SourceNone)
			}
			status, err := c.Status(appwire.AuthStatusParams{Provider: instanceName})
			if err != nil {
				t.Fatalf("Status(%q) for a %q instance: %v", instanceName, p.Name, err)
			}
			if status.ActiveSource != string(credentials.SourceNone) {
				t.Fatalf("serf/auth/status %q activeSource = %q, want %q", instanceName, status.ActiveSource, credentials.SourceNone)
			}

			// And the gate this kata is about. The launch path starts this
			// instance, so "Test credentials" has something to test.
			resp, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: instanceName})
			if err != nil {
				t.Fatalf("TestCredentials(%q): %v", instanceName, err)
			}
			if resp.Status != appwire.AuthTestStatusSuccess {
				t.Errorf("serf/auth/test %q = %q (%q), want %q: the launch preflight, credentials.Store.List and serf/auth/status all report that this %q instance needs no credential, so serf/auth/test must not call it unconfigured",
					instanceName, resp.Status, resp.Message, appwire.AuthTestStatusSuccess, p.Name)
			}
			if got := client.callCount(); got != 1 {
				t.Errorf("probe calls = %d, want 1: %q authenticates nothing, so this instance has no credential to be missing and serf/auth/test must actually probe it", got, p.Name)
			}
		})

		// The exemption belongs to the provider, not to the name. An instance of
		// a credential-bearing type is still gated when a user names it after the
		// auth-none provider — the same direction kata nrv4 pinned for the launch
		// preflight.
		t.Run(p.Name+" is not a name that exempts a credential-bearing type", func(t *testing.T) {
			inst := providercfg.InstanceConfig{Name: p.Name, Type: "anthropic"}
			client := &credentialProbeFakeClient{}
			c := newStoreBackedProbeController(t, client, providercfg.Config{
				Default:   p.Name,
				Instances: []providercfg.InstanceConfig{inst},
			})

			resp, err := c.TestCredentials(context.Background(), appwire.AuthTestParams{Provider: p.Name})
			if err != nil {
				t.Fatalf("TestCredentials(%q): %v", p.Name, err)
			}
			if resp.Status != appwire.AuthTestStatusMissing {
				t.Errorf("serf/auth/test %q = %q (%q), want %q: this instance is type %q, which authenticates with a key, and it has none",
					p.Name, resp.Status, resp.Message, appwire.AuthTestStatusMissing, inst.Type)
			}
			if got := client.callCount(); got != 0 {
				t.Errorf("probe calls = %d, want 0: an instance with no credential must not be probed", got)
			}
		})
	}

	if covered == 0 {
		t.Fatal("no auth-none provider in the envvars registry is a configurable instance type, so this test asserted nothing — it must not stay in the suite reporting coverage it does not have")
	}
}
