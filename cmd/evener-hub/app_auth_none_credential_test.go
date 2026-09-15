package hub

// An instance whose auth scheme takes no credential has no key to resolve, so
// an empty resolution is not a missing credential. The registry says so in one
// place — Credential.Source is "none" and Transport.Auth is none or
// optional-bearer — and three RPCs report it: evener/auth/list,
// evener/auth/status and evener/instance/list. Kata ps28 was filed when they
// disagreed and the credentials pane rendered a working local endpoint as "Not
// configured" (credentialLabels.ts, unconfiguredLabel).
//
// These tests pin the agreement rather than any one field: three surfaces, one
// instance, one answer.

import (
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/openai/oaitest"
)

// authNoneProvidersToml is a local endpoint that authenticates nothing — the
// shape a machine with no provider keys still has.
const authNoneProvidersToml = `[providers.ollama]
base = "ollama"
auth = "none"
`

// newAuthNoneControllers builds the auth and instance controllers over ONE
// credentials store, one registry and one providers.toml — the same hub
// answering both RPCs, which is what makes a disagreement between them a bug
// rather than a fixture difference.
func newAuthNoneControllers(t *testing.T, env map[string]string) (*hubAuthController, *hubInstancesController) {
	t.Helper()
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, authNoneProvidersToml)
	auth := newTestAuthController(t, dir, t.TempDir(), tomlPath, env)
	return auth, &hubInstancesController{reg: auth.reg, providersConfigPath: tomlPath, auth: auth}
}

// authListRow returns the evener/auth/list entry for provider.
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
	t.Fatalf("evener/auth/list has no %q entry: %+v", provider, resp.Providers)
	return appwire.AuthStatusResponse{}
}

// instanceListRow returns the evener/instance/list entry for name.
func instanceListRow(t *testing.T, c *hubInstancesController, name string) appwire.InstanceEntry {
	t.Helper()
	resp := c.List()
	for _, inst := range resp.Instances {
		if inst.Name == name {
			return inst
		}
	}
	t.Fatalf("evener/instance/list has no %q entry: %+v", name, resp.Instances)
	return appwire.InstanceEntry{}
}

// TestAuthNoneInstance_EveryCredentialSurfaceAgrees holds evener/auth/list,
// evener/auth/status and evener/instance/list to one answer for an auth-none
// instance. The OLLAMA_API_KEY subtest matters because the rule is
// unconditional: an instance that authenticates nothing needs nothing whether
// or not a key happens to be sitting in the environment.
func TestAuthNoneInstance_EveryCredentialSurfaceAgrees(t *testing.T) {
	for _, tt := range []struct {
		name string
		env  map[string]string
	}{
		{name: "no key in the environment"},
		{name: "OLLAMA_API_KEY set", env: map[string]string{"OLLAMA_API_KEY": "sk-ollama-probe"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			auth, instances := newAuthNoneControllers(t, tt.env)

			inst, ok := auth.reg.Get().Instance("ollama")
			if !ok {
				t.Fatal("an auth-none instance exists with no credential at all")
			}
			if inst.Auth != "none" {
				t.Fatalf("the ollama fixture authenticates with %q, so this test proves nothing about auth = none", inst.Auth)
			}

			listRow := authListRow(t, auth, "ollama")
			status, err := auth.Status(appwire.AuthStatusParams{Provider: "ollama"})
			if err != nil {
				t.Fatalf("Status(ollama): %v", err)
			}
			instRow := instanceListRow(t, instances, "ollama")

			// evener/auth/status is the per-instance RPC the credentials pane
			// calls; evener/auth/list is the roster one. Same instance.
			if status.ActiveSource != listRow.ActiveSource {
				t.Errorf("evener/auth/status ollama activeSource = %q, but evener/auth/list says %q — one instance, two answers",
					status.ActiveSource, listRow.ActiveSource)
			}
			if status.SignedIn != listRow.SignedIn {
				t.Errorf("evener/auth/status ollama signedIn = %v, but evener/auth/list says %v", status.SignedIn, listRow.SignedIn)
			}
			if !reflect.DeepEqual(status.AuthModes, listRow.AuthModes) {
				t.Errorf("evener/auth/status ollama authModes = %v, but evener/auth/list says %v", status.AuthModes, listRow.AuthModes)
			}
			if status.EnvVar != listRow.EnvVar {
				t.Errorf("evener/auth/status ollama envVar = %q, but evener/auth/list says %q", status.EnvVar, listRow.EnvVar)
			}
			if status.HasStoredFile != listRow.HasStoredFile {
				t.Errorf("evener/auth/status ollama hasStoredFile = %v, but evener/auth/list says %v", status.HasStoredFile, listRow.HasStoredFile)
			}

			// evener/instance/list is what the settings pane renders. It
			// carries no signedIn field, so agreement there is over the rest.
			if instRow.ActiveSource != listRow.ActiveSource {
				t.Errorf("evener/instance/list ollama activeSource = %q, but evener/auth/list says %q — the settings pane renders the difference as \"Not configured\"",
					instRow.ActiveSource, listRow.ActiveSource)
			}
			if !reflect.DeepEqual(instRow.AuthModes, listRow.AuthModes) {
				t.Errorf("evener/instance/list ollama authModes = %v, but evener/auth/list says %v", instRow.AuthModes, listRow.AuthModes)
			}
			if instRow.EnvVar != listRow.EnvVar {
				t.Errorf("evener/instance/list ollama envVar = %q, but evener/auth/list says %q", instRow.EnvVar, listRow.EnvVar)
			}
			if instRow.HasStoredFile != listRow.HasStoredFile {
				t.Errorf("evener/instance/list ollama hasStoredFile = %v, but evener/auth/list says %v", instRow.HasStoredFile, listRow.HasStoredFile)
			}

			// And the answer they agree on is the registry's.
			if listRow.ActiveSource != inst.CredentialSource {
				t.Errorf("the credential surfaces agree on activeSource %q, but the registry resolved %q",
					listRow.ActiveSource, inst.CredentialSource)
			}
			if instRow.CredentialRequired {
				t.Error("an auth-none instance requires no credential, so an absent one is nothing missing")
			}
		})
	}
}

// The other half of the same rule: the surfaces agree that an auth-none
// instance has no credential, so storing one under it is a write nothing
// reads. noneAuth (llm/authenticators.go) sends no credential at all, and the
// transport therefore never looks at what is in the store - a save reported as
// successful would describe a credential the launch cannot use and leave the
// secret under a name free to be re-pointed at a scheme that does read it.
func TestAuthNoneInstance_ApiKeySetRefusesAKeyNothingReads(t *testing.T) {
	auth, instances := newAuthNoneControllers(t, nil)

	_, err := auth.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "ollama", Value: "sk-nothing-reads-this"})
	if err == nil || !strings.Contains(err.Error(), "reads no API key") {
		t.Fatalf("ApiKeySet(ollama) = %v, want the refusal for an instance that authenticates without a credential", err)
	}
	if v, ok := auth.creds.Get("ollama"); ok || v != "" {
		t.Fatalf("credentials.toml[ollama] = %q/%v, want nothing stored", v, ok)
	}
	// And the pane's row still reads as configured: the refusal is about the
	// write, not about the instance having lost a credential it never had.
	if row := instanceListRow(t, instances, "ollama"); row.CredentialRequired {
		t.Error("an auth-none instance requires no credential, so the refused key is nothing missing")
	}
}

// The line is exactly auth = none, not "not bearer": optional-bearer sends the
// stored key when there is one (optionalBearerAuth), so the key field the pane
// offers that row is a write the transport reads.
func TestAuthNoneInstance_ApiKeySetAcceptsAKeyOptionalBearerReads(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	ctrl := newTestAuthController(t, dir, t.TempDir(), writeProvidersToml(t, dir, `[providers.work-opt]
base = "ollama"
auth = "optional-bearer"
`))

	got, err := ctrl.ApiKeySet(appwire.AuthApiKeySetParams{Provider: "work-opt", Value: "sk-optional"})
	if err != nil {
		t.Fatalf("ApiKeySet(work-opt) = %v, want the key stored for an optional-bearer instance", err)
	}
	if v, ok := ctrl.creds.Get("work-opt"); !ok || v != "sk-optional" {
		t.Fatalf("credentials.toml[work-opt] = %q/%v, want sk-optional/true", v, ok)
	}
	if !got.HasStoredFile || got.ActiveSource != "store" {
		t.Fatalf("status = %+v, want a stored key the row reports", got)
	}
}
