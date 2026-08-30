package hub

// Launch and the credentials pane must describe one instance's credential the
// same way, and the arbiter is the endpoint the adapter will actually contact.
// The registry resolves the credential the spawned child signs its requests
// with; if the launch preflight refuses that instance, or the pane names a
// different variable — or claims a sign-in for a child that will send no key at
// all — one of the two is lying about a session the user is running. Kata z1gm.
//
// Both hub surfaces now read the one registry the child resolves against, so
// each case seeds exactly one environment variable and requires the registry's
// own credential source, the pane, and the spawn gate to say the same thing.

import (
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
)

// TestCredentialAgreement_HubAgreesWithTheKeyTheChildSends holds the launch
// preflight and evener/auth/status to what the registry resolves, which is the
// only credential the spawned process has.
//
// Instances on the Codex transport are out of scope here: their OAuth record is
// a credential with no key at all. Every shape below authenticates with an API
// key or nothing.
func TestCredentialAgreement_HubAgreesWithTheKeyTheChildSends(t *testing.T) {
	// A base URL that is syntactically real and semantically unreachable: no
	// case here issues a request, and a loopback port nothing listens on keeps
	// it that way if one ever does.
	const gatewayBaseURL = "http://127.0.0.1:9/v1"

	// A chat-completions instance behind the openai row.
	compatInstance := func(baseURL string) registry.Provider {
		return registry.Provider{
			Base: "openai", Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric,
			Transport: registry.Transport{BaseURL: baseURL},
		}
	}

	for _, tt := range []struct {
		name         string
		instance     string
		provider     registry.Provider
		envVar       string
		value        string
		wantInjected bool
		why          string
	}{
		{
			name:         "openai-chat instance with no base_url",
			instance:     "local",
			provider:     compatInstance(""),
			envVar:       "OPENAI_API_KEY",
			value:        "sk-openai-only",
			wantInjected: true,
			why:          "with no base_url the adapter targets api.openai.com, so OPENAI_API_KEY is the key that signs its requests",
		},
		{
			name:         "an instance named after the provider it is based on",
			instance:     "openai",
			provider:     compatInstance(""),
			envVar:       "OPENAI_API_KEY",
			value:        "sk-openai-only",
			wantInjected: true,
			why:          "two clicks in the instance dialog reach this shape from the curated openai provider",
		},
		{
			name:         "custom gateway at its own base_url",
			instance:     "gateway",
			provider:     compatInstance(gatewayBaseURL),
			envVar:       "OPENAI_API_KEY",
			value:        "sk-openai-only",
			wantInjected: false,
			why:          "credential inheritance stops at the endpoint (spec §10): a gateway never receives the vendor key, so the child sends no Authorization header at all",
		},
		{
			name:         "gateway named after another vendor",
			instance:     "glm",
			provider:     compatInstance(gatewayBaseURL),
			envVar:       "GLM_API_KEY",
			value:        "sk-glm-only",
			wantInjected: true,
			why:          "the name-scoped <NAME>_API_KEY layer resolves for any instance whose name is not a registry id, base_url or not",
		},
		{
			name:         "instance named after another vendor with no base_url",
			instance:     "glm",
			provider:     compatInstance(""),
			envVar:       "GLM_API_KEY",
			value:        "sk-glm-only",
			wantInjected: true,
			why:          "the preflight must resolve the layers the child resolves; a key the provider then rejects is the provider's 401 to give, not the gate's refusal to make",
		},
		{
			name:         "anthropic instance under a name of its own",
			instance:     "work",
			provider:     registry.Provider{Base: "anthropic"},
			envVar:       "ANTHROPIC_API_KEY",
			value:        "sk-ant-only",
			wantInjected: true,
			why:          "the control: a base whose endpoint and key have never disagreed",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			oaitest.IsolateOpenAIAuth(t)
			clearProviderKeysFromEnvironment(t)

			dir := t.TempDir()
			stateDir := t.TempDir()
			store, err := credentials.LoadStore(dir + "/credentials.toml")
			if err != nil {
				t.Fatalf("LoadStore: %v", err)
			}
			reg := newProbeRegistry(t, stateDir, store,
				map[string]string{tt.envVar: tt.value},
				map[string]registry.Provider{tt.instance: tt.provider})

			// The credential the child process is actually built with.
			resolved := childCredentialSource(t, reg, tt.instance)
			if got := resolved != "" && resolved != "none"; got != tt.wantInjected {
				t.Fatalf("the registry resolves a credential = %v (source %q), want %v: %s", got, resolved, tt.wantInjected, tt.why)
			}

			c := newHubAuthControllerWithStore(dir, store)
			c.stateDir = stateDir
			c.reg = reg

			status, err := c.Status(appwire.AuthStatusParams{Provider: tt.instance})
			if err != nil {
				t.Fatalf("Status(%q): %v", tt.instance, err)
			}
			preflight := validateProviderCredentials(tt.instance, reg)

			if tt.wantInjected {
				if preflight != nil {
					t.Errorf("validateProviderCredentials(%q) = %v, want nil: the child authenticates with %s, so the gate in front of it must not refuse (%s)",
						tt.instance, preflight, tt.envVar, tt.why)
				}
				if !status.SignedIn {
					t.Errorf("Status(%q).SignedIn = false, want true: the child authenticates with %s (%s)", tt.instance, tt.envVar, tt.why)
				}
				if status.EnvVar != tt.envVar {
					t.Errorf("Status(%q).EnvVar = %q, want %q: the pane must name the variable the child signs with (%s)",
						tt.instance, status.EnvVar, tt.envVar, tt.why)
				}
				return
			}
			if preflight == nil {
				t.Errorf("validateProviderCredentials(%q) = nil, want a refusal: the child sends no key (%s)", tt.instance, tt.why)
			}
			if status.SignedIn {
				t.Errorf("Status(%q).SignedIn = true (EnvVar %q), want false: the registry resolves nothing here, so the child sends no key (%s)",
					tt.instance, status.EnvVar, tt.why)
			}
			if status.EnvVar != "" {
				t.Errorf("Status(%q).EnvVar = %q, want \"\": naming a variable the child never sends describes a different client (%s)",
					tt.instance, status.EnvVar, tt.why)
			}
		})
	}
}

// childCredentialSource is what the spawned child's registry resolves for one
// instance — the third party the two hub surfaces have to agree with.
func childCredentialSource(t *testing.T, reg *hubcore.ProviderRegistry, name string) string {
	t.Helper()
	inst, ok := reg.Get().Instance(name)
	if !ok {
		res, err := reg.Get().ResolveInstance(name)
		if err != nil {
			t.Fatalf("the registry has no instance %q: %v", name, err)
		}
		return res.Credential.Source
	}
	return inst.CredentialSource
}
