package main

// A provider whose only auth mode is "none" has no key to resolve, so an empty
// resolution is not a missing credential. credentials.Store.List has carried
// that rule since kata nrv4 gave it a name — envvars.RequiresNoCredential — and
// reports SourceNone rather than SourceAbsent. The instance-keyed status path
// had no equivalent, so the hub answered one question about one provider two
// ways: serf/auth/list said "none" for ollama while serf/auth/status and
// serf/instance/list said "absent", and the credentials pane renders that
// difference as "Not configured" instead of "No credentials required"
// (credentialLabels.ts, unconfiguredLabel). Kata ps28.
//
// These tests pin the agreement rather than any one field: three surfaces, one
// provider, one answer, anchored to the store that owns the rule.

import (
	"reflect"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/auth/openai/oaitest"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/internal/credentials"
)

// ollamaProvidersToml is the config the hub materializes for itself when none
// exists and no provider credentials are in the environment: one instance,
// ollama, which registers unconditionally and authenticates nothing.
const ollamaProvidersToml = `schema = 1
default = "ollama"

[instances.ollama]
type = "ollama"
`

// newAuthNoneControllers builds the auth and instance controllers over ONE
// credentials store and one providers.toml — the same hub answering both RPCs,
// which is what makes a disagreement between them a bug rather than a fixture
// difference.
func newAuthNoneControllers(t *testing.T) (*hubAuthController, *hubInstancesController) {
	t.Helper()
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, ollamaProvidersToml)
	auth := newTestAuthController(t, dir, t.TempDir(), tomlPath)
	return auth, &hubInstancesController{providersConfigPath: tomlPath, auth: auth}
}

// authListRow returns the serf/auth/list entry for provider.
func authListRow(t *testing.T, c *hubAuthController, provider string) appwire.AuthStatusResponse {
	t.Helper()
	resp, err := c.List(appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("auth List: %v", err)
	}
	for _, p := range resp.Providers {
		if p.Provider == provider {
			return p
		}
	}
	t.Fatalf("serf/auth/list has no %q entry: %+v", provider, resp.Providers)
	return appwire.AuthStatusResponse{}
}

// instanceListRow returns the serf/instance/list entry for name.
func instanceListRow(t *testing.T, c *hubInstancesController, name string) appwire.InstanceEntry {
	t.Helper()
	resp := c.List()
	for _, inst := range resp.Instances {
		if inst.Name == name {
			return inst
		}
	}
	t.Fatalf("serf/instance/list has no %q entry: %+v", name, resp.Instances)
	return appwire.InstanceEntry{}
}

// TestAuthNoneInstance_EveryCredentialSurfaceAgrees holds serf/auth/list,
// serf/auth/status and serf/instance/list to one answer for an auth-none
// instance, and holds that answer to the credentials store's own rule. The
// OLLAMA_API_KEY subtest matters because the store's rule is unconditional: it
// reports SourceNone whether or not a key happens to resolve, so a status path
// that reports what it resolved disagrees exactly when a key is present.
func TestAuthNoneInstance_EveryCredentialSurfaceAgrees(t *testing.T) {
	for _, tt := range []struct {
		name      string
		ollamaKey string
	}{
		{"no key in the environment", ""},
		{"OLLAMA_API_KEY set", "sk-ollama-probe"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			auth, instances := newAuthNoneControllers(t)
			t.Setenv(envvars.OllamaAPIKey.Name, tt.ollamaKey)

			// The store owns the rule; the RPCs must report it, not the
			// reverse.
			rows := auth.creds.List()
			var storeRow credentials.Provider
			for _, p := range rows {
				if p.Name == "ollama" {
					storeRow = p
					break
				}
			}
			if storeRow.Name == "" {
				t.Fatalf("credentials.Store.List() has no ollama row: %+v", rows)
			}
			if storeRow.Source != credentials.SourceNone {
				t.Fatalf("credentials.Store.List() ollama source = %q, want %q — envvars.RequiresNoCredential(ollama) = %v",
					storeRow.Source, credentials.SourceNone, envvars.RequiresNoCredential("ollama"))
			}

			listRow := authListRow(t, auth, "ollama")
			status, err := auth.Status(appwire.AuthStatusParams{Provider: "ollama"})
			if err != nil {
				t.Fatalf("Status(ollama): %v", err)
			}
			instRow := instanceListRow(t, instances, "ollama")

			// serf/auth/status is the per-instance RPC the credentials pane
			// calls; serf/auth/list is the provider-keyed one. Same provider.
			if status.ActiveSource != listRow.ActiveSource {
				t.Errorf("serf/auth/status ollama activeSource = %q, but serf/auth/list says %q — one provider, two answers",
					status.ActiveSource, listRow.ActiveSource)
			}
			if status.SignedIn != listRow.SignedIn {
				t.Errorf("serf/auth/status ollama signedIn = %v, but serf/auth/list says %v",
					status.SignedIn, listRow.SignedIn)
			}
			if !reflect.DeepEqual(status.AuthModes, listRow.AuthModes) {
				t.Errorf("serf/auth/status ollama authModes = %v, but serf/auth/list says %v",
					status.AuthModes, listRow.AuthModes)
			}
			if status.EnvVar != listRow.EnvVar {
				t.Errorf("serf/auth/status ollama envVar = %q, but serf/auth/list says %q",
					status.EnvVar, listRow.EnvVar)
			}
			if status.HasStoredFile != listRow.HasStoredFile {
				t.Errorf("serf/auth/status ollama hasStoredFile = %v, but serf/auth/list says %v",
					status.HasStoredFile, listRow.HasStoredFile)
			}

			// serf/instance/list is what the settings pane renders. It carries
			// no signedIn field, so agreement there is over the rest.
			if instRow.ActiveSource != listRow.ActiveSource {
				t.Errorf("serf/instance/list ollama activeSource = %q, but serf/auth/list says %q — the settings pane renders %q as \"Not configured\"",
					instRow.ActiveSource, listRow.ActiveSource, instRow.ActiveSource)
			}
			if !reflect.DeepEqual(instRow.AuthModes, listRow.AuthModes) {
				t.Errorf("serf/instance/list ollama authModes = %v, but serf/auth/list says %v",
					instRow.AuthModes, listRow.AuthModes)
			}
			if instRow.EnvVar != listRow.EnvVar {
				t.Errorf("serf/instance/list ollama envVar = %q, but serf/auth/list says %q",
					instRow.EnvVar, listRow.EnvVar)
			}
			if instRow.HasStoredFile != listRow.HasStoredFile {
				t.Errorf("serf/instance/list ollama hasStoredFile = %v, but serf/auth/list says %v",
					instRow.HasStoredFile, listRow.HasStoredFile)
			}

			// And the answer they agree on is the store's.
			if listRow.ActiveSource != string(storeRow.Source) {
				t.Errorf("the credential surfaces agree on activeSource %q, but credentials.Store.List() says %q",
					listRow.ActiveSource, storeRow.Source)
			}
		})
	}
}
