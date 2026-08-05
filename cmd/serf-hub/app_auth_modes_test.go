package main

// What a provider authenticates with is a registry fact: envvars/providers.go
// carries one row per provider with its auth modes, and the hub hands them to
// the auth UI. These tests walk the registry instead of a literal list, so a row
// added there is covered here with no edit — a hub-side copy of the same table
// silently disagreeing with the registry is what kata f1zs was filed for.

import (
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/auth/openai/oaitest"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/llm/providercfg"
)

// newAuthModesController builds a controller over an empty credentials store and
// an empty OAuth state dir. Which modes a provider supports does not depend on
// whether anyone has signed in, so neither is seeded.
func newAuthModesController(t *testing.T) *hubAuthController {
	t.Helper()
	dir := t.TempDir()
	store, err := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	c := newHubAuthControllerWithStore(dir, store)
	c.stateDir = t.TempDir()
	return c
}

// TestAuthStatusAuthModesFollowRegistry checks the type-keyed Status path — the
// one taken when there is no providers.toml, or when the name is not an instance
// in it — against every row in the envvars registry. A provider the hub fails to
// place there is reported Supported:false, which tells the auth UI the provider
// cannot be authenticated at all.
func TestAuthStatusAuthModesFollowRegistry(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	for _, p := range envvars.Providers() {
		t.Run(p.Name, func(t *testing.T) {
			c := newAuthModesController(t)
			got, err := c.Status(appwire.AuthStatusParams{Provider: p.Name})
			if err != nil {
				t.Fatalf("Status(%q): %v", p.Name, err)
			}
			if !got.Supported {
				t.Fatalf("Status(%q).Supported = false: %q is a provider in the envvars registry, so the auth UI must not report it unauthenticable", p.Name, p.Name)
			}
			if !reflect.DeepEqual(got.AuthModes, p.AuthModes) {
				t.Errorf("Status(%q).AuthModes = %v, want the registry's %v", p.Name, got.AuthModes, p.AuthModes)
			}
		})
	}
}

// TestAuthInstanceStatusAuthModesFollowRegistry checks the instance-keyed path —
// the one the credentials pane and the instance list (hubInstancesController.List,
// which calls instanceStatus directly) take once providers.toml exists — for every
// registry row that a providers.toml instance can carry as its type. A type the
// hub fails to place there falls back to ["apiKey"], which would tell the UI that
// an instance needing no credential at all needs an API key.
//
// The instance is deliberately not named after its type: auth modes belong to the
// type, not to the name a user picked.
func TestAuthInstanceStatusAuthModesFollowRegistry(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	const instanceName = "workhorse"
	for _, p := range envvars.Providers() {
		t.Run(p.Name, func(t *testing.T) {
			if err := providercfg.ValidateType(providercfg.Type(p.Name)); err != nil {
				// "gemini" (an alias of google) and "openai-compatible" (a
				// behavior tag, not a type) are registry rows no instance can
				// declare. If either becomes a type, this subtest starts
				// covering it with no edit here.
				t.Skipf("%q is a registry row but not a configurable instance type: %v", p.Name, err)
			}
			cfgPath := writeProvidersConfig(t, t.TempDir(), providercfg.Config{
				Instances: []providercfg.InstanceConfig{{Name: instanceName, Type: providercfg.Type(p.Name)}},
			})
			c := newAuthModesController(t)
			c.providersConfigPath = cfgPath
			got, err := c.Status(appwire.AuthStatusParams{Provider: instanceName})
			if err != nil {
				t.Fatalf("Status(%q) for a %q instance: %v", instanceName, p.Name, err)
			}
			if !got.Supported {
				t.Fatalf("Status(%q).Supported = false for a %q instance", instanceName, p.Name)
			}
			if !reflect.DeepEqual(got.AuthModes, p.AuthModes) {
				t.Errorf("Status(%q).AuthModes = %v for a %q instance, want the registry's %v", instanceName, got.AuthModes, p.Name, p.AuthModes)
			}
		})
	}
}
