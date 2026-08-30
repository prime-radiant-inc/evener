package hub

// How an instance authenticates is a registry fact: Transport.Auth names one
// of six schemes, and authModesFor turns that into the sign-in affordances the
// credentials pane offers (spec §11.3). A hub-side copy of that mapping
// silently disagreeing with the registry is what kata f1zs was filed for; a
// pane advertising "oauth" for an endpoint that has no OAuth flow at all is
// what kata jd5s was.

import (
	"reflect"
	"slices"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/llm/registry"
)

// TestAuthModesForCoversEveryScheme walks the registry's whole auth
// vocabulary, so a scheme added there is covered here with no edit.
func TestAuthModesForCoversEveryScheme(t *testing.T) {
	want := map[string][]string{
		registry.AuthBearer:           {"apiKey"},
		registry.AuthHeader:           {"apiKey"},
		registry.AuthOptionalBearer:   {"none", "apiKey"},
		registry.AuthNone:             {"none"},
		registry.AuthGCPADC:           {"adc"},
		registry.AuthOAuthOpenAICodex: {"oauth"},
	}
	for scheme, modes := range want {
		if got := authModesFor(scheme); !reflect.DeepEqual(got, modes) {
			t.Errorf("authModesFor(%q) = %v, want %v", scheme, got, modes)
		}
	}
	// Only the Codex transport has an OAuth flow, so no other scheme may
	// advertise the "Sign in with ChatGPT" affordance.
	for scheme := range want {
		if scheme == registry.AuthOAuthOpenAICodex {
			continue
		}
		if slices.Contains(authModesFor(scheme), "oauth") {
			t.Errorf("authModesFor(%q) advertises oauth", scheme)
		}
	}
	// An unknown scheme gets the credential-bearing default rather than a
	// claim that it needs nothing.
	if got := authModesFor("something-new"); !reflect.DeepEqual(got, []string{"apiKey"}) {
		t.Errorf("authModesFor(unknown) = %v, want [apiKey]", got)
	}
}

// TestAuthStatusAuthModesFollowTheInstance holds evener/auth/status to the
// same mapping for a real instance of each shape.
func TestAuthStatusAuthModesFollowTheInstance(t *testing.T) {
	for _, tt := range []struct {
		name  string
		toml  string
		env   map[string]string
		modes []string
	}{
		{
			name:  "bearer instance",
			toml:  "[providers.work]\nbase = \"anthropic\"\napi_key_env = [\"WORK_KEY\"]\n",
			env:   map[string]string{"WORK_KEY": "k"},
			modes: []string{"apiKey"},
		},
		{
			name:  "auth-none instance",
			toml:  "[providers.work]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:8080/v1\"\nauth = \"none\"\n",
			modes: []string{"none"},
		},
		{
			name:  "optional-bearer instance",
			toml:  "[providers.work]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:8080/v1\"\nauth = \"optional-bearer\"\n",
			modes: []string{"none", "apiKey"},
		},
		{
			name:  "gcp-adc instance",
			toml:  "[providers.work]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:8080/v1\"\nauth = \"gcp-adc\"\n",
			modes: []string{"adc"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			oaitest.IsolateOpenAIAuth(t)
			dir := t.TempDir()
			ctrl := newTestAuthController(t, dir, t.TempDir(), writeProvidersToml(t, dir, tt.toml), tt.env)
			got, err := ctrl.Status(appwire.AuthStatusParams{Provider: "work"})
			if err != nil {
				t.Fatalf("Status(work): %v", err)
			}
			if !got.Supported {
				t.Fatalf("Status(work).Supported = false: %+v", got)
			}
			if !reflect.DeepEqual(got.AuthModes, tt.modes) {
				t.Errorf("Status(work).AuthModes = %v, want %v", got.AuthModes, tt.modes)
			}
		})
	}
}

// TestAuthStatusListsCuratedImplicitProvidersWithoutCredentials is spec
// §11.3: the pane lists every curated implicit provider whether or not it has
// a credential, because that is where a fresh install enters its first key.
func TestAuthStatusListsCuratedImplicitProvidersWithoutCredentials(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	ctrl := newTestAuthController(t, t.TempDir(), t.TempDir(), "")

	got, err := ctrl.Status(appwire.AuthStatusParams{Provider: "anthropic"})
	if err != nil {
		t.Fatalf("Status(anthropic): %v", err)
	}
	if !got.Supported {
		t.Fatalf("a curated implicit provider is always supported: %+v", got)
	}
	if got.SignedIn || got.ActiveSource != "none" {
		t.Fatalf("with no key anywhere the source is none: %+v", got)
	}
	if !reflect.DeepEqual(got.AuthModes, []string{"apiKey"}) {
		t.Fatalf("AuthModes = %v, want [apiKey]", got.AuthModes)
	}

	list, err := ctrl.List(appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	seen := map[string]bool{}
	for _, p := range list.Providers {
		if seen[p.Provider] {
			t.Fatalf("List repeats %q", p.Provider)
		}
		seen[p.Provider] = true
	}
	for _, want := range []string{"anthropic", "openai", "openai-codex"} {
		if !seen[want] {
			t.Errorf("List omits the curated implicit provider %q: %v", want, seen)
		}
	}
}

// TestAuthListIncludesExplicitInstances: an authored instance appears
// alongside the curated providers, once.
func TestAuthListIncludesExplicitInstances(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	ctrl := newTestAuthController(t, dir, t.TempDir(),
		writeProvidersToml(t, dir, "[providers.work]\nbase = \"anthropic\"\napi_key_env = [\"WORK_KEY\"]\n"),
		map[string]string{"WORK_KEY": "k"})

	list, err := ctrl.List(appwire.EmptyParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := 0
	for _, p := range list.Providers {
		if p.Provider == "work" {
			found++
			if p.ActiveSource != "env:WORK_KEY" {
				t.Errorf("work ActiveSource = %q, want env:WORK_KEY", p.ActiveSource)
			}
			if p.EnvVar != "WORK_KEY" {
				t.Errorf("work EnvVar = %q, want WORK_KEY", p.EnvVar)
			}
		}
	}
	if found != 1 {
		t.Fatalf("work appears %d times in the auth list", found)
	}
}
