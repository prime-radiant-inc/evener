package hub

// Launch and the credentials pane must describe one instance's credential the
// same way, and the arbiter is the endpoint the adapter will actually contact.
// If the launch preflight refuses an instance the pane calls signed in, or the
// pane names a variable the child never sends, one of the two is lying about a
// session the user is running. Kata z1gm.
//
// The third party to this agreement — the credential the spawned child is
// actually built with — now comes from the registry rather than from
// providercfg injection, and the registry's rules are not yet the ones these
// cases encode (a gateway at its own base_url does resolve its base's
// api_key_env there). The hub moves onto the registry in step 3 task 9; until
// it does, this test holds the two hub surfaces to each other.

import (
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/providercfg"
)

// TestCredentialAgreement_HubAgreesWithTheKeyTheChildSends holds the launch
// preflight and evener/auth/status to one another. Each case seeds exactly one
// environment variable and requires both hub surfaces to describe the same
// credential.
//
// Instances whose behavior tag is "openai" are out of scope here: their OAuth
// record is a credential with no key to inject, so injection is not the whole
// answer for them. Every shape below authenticates with an API key or nothing.
func TestCredentialAgreement_HubAgreesWithTheKeyTheChildSends(t *testing.T) {
	// A base URL that is syntactically real and semantically unreachable: no
	// case here issues a request, and a loopback port nothing listens on keeps
	// it that way if one ever does.
	const gatewayBaseURL = "http://127.0.0.1:9/v1"

	for _, tt := range []struct {
		name         string
		inst         providercfg.InstanceConfig
		envVar       envvars.Var
		value        string
		wantInjected bool
		why          string
	}{
		{
			name:         "openai on chat-completions with no base_url",
			inst:         providercfg.InstanceConfig{Name: "local", Type: "openai", APIStyle: providercfg.StyleChatCompletions},
			envVar:       envvars.OpenAIAPIKey,
			value:        "sk-openai-only",
			wantInjected: true,
			why:          "with no base_url the adapter targets api.openai.com, so OPENAI_API_KEY is the key that signs its requests",
		},
		{
			name:         "the materialized openai instance flipped to chat-completions",
			inst:         providercfg.InstanceConfig{Name: "openai", Type: "openai", APIStyle: providercfg.StyleChatCompletions},
			envVar:       envvars.OpenAIAPIKey,
			value:        "sk-openai-only",
			wantInjected: true,
			why:          "two clicks in the instance dialog reach this shape from the hub's own default instance",
		},
		{
			name:         "custom gateway at its own base_url",
			inst:         providercfg.InstanceConfig{Name: "gateway", Type: "openai", APIStyle: providercfg.StyleChatCompletions, BaseURL: gatewayBaseURL},
			envVar:       envvars.OpenAICompatibleAPIKey,
			value:        "sk-compat-only",
			wantInjected: false,
			why:          "OPENAI_COMPATIBLE_API_KEY belongs to the host OPENAI_COMPATIBLE_BASE_URL names, which this gateway is not; the child sends no Authorization header at all",
		},
		{
			name:         "gateway named after another provider row",
			inst:         providercfg.InstanceConfig{Name: "glm", Type: "openai", APIStyle: providercfg.StyleChatCompletions, BaseURL: gatewayBaseURL},
			envVar:       envvars.GLMAPIKey,
			value:        "sk-glm-only",
			wantInjected: true,
			why:          "the name-scoped layer resolves for any instance, base_url or not",
		},
		{
			name:         "instance named after another provider row with no base_url",
			inst:         providercfg.InstanceConfig{Name: "glm", Type: "openai", APIStyle: providercfg.StyleChatCompletions},
			envVar:       envvars.GLMAPIKey,
			value:        "sk-glm-only",
			wantInjected: true,
			why:          "the preflight must resolve the layers ResolveKey resolves, name before tag; a key the provider then rejects is the provider's 401 to give, not the gate's refusal to make",
		},
		{
			name:         "anthropic instance under a name of its own",
			inst:         providercfg.InstanceConfig{Name: "work", Type: "anthropic"},
			envVar:       envvars.AnthropicAPIKey,
			value:        "sk-ant-only",
			wantInjected: true,
			why:          "the control: a type whose tag, endpoint and registry row have never disagreed",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// IsolateOpenAIAuth first: it clears OPENAI_* itself, so the seed
			// below has to come after it and after the registry sweep.
			oaitest.IsolateOpenAIAuth(t)
			clearProviderKeysFromEnvironment(t)

			dir := t.TempDir()
			cfgPath := writeProvidersConfig(t, dir, providercfg.Config{
				Default:   tt.inst.Name,
				Instances: []providercfg.InstanceConfig{tt.inst},
			})
			t.Setenv(tt.envVar.Name, tt.value)

			store, err := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
			if err != nil {
				t.Fatalf("LoadStore: %v", err)
			}
			c := newHubAuthControllerWithStore(dir, store)
			c.providersConfigPath = cfgPath

			status, err := c.Status(appwire.AuthStatusParams{Provider: tt.inst.Name})
			if err != nil {
				t.Fatalf("Status(%q): %v", tt.inst.Name, err)
			}
			// A nil launch env means the child inherits the hub's, which is
			// what these seeded variables are.
			preflight := validateProviderCredentials(tt.inst.Name, store, nil, cfgPath)

			if tt.wantInjected {
				if preflight != nil {
					t.Errorf("validateProviderCredentials(%q) = %v, want nil: this instance authenticates with %s, so the gate in front of it must not refuse (%s)",
						tt.inst.Name, preflight, tt.envVar.Name, tt.why)
				}
				if !status.SignedIn {
					t.Errorf("Status(%q).SignedIn = false, want true: the child authenticates with %s (%s)", tt.inst.Name, tt.envVar.Name, tt.why)
				}
				if status.EnvVar != tt.envVar.Name {
					t.Errorf("Status(%q).EnvVar = %q, want %q: the pane must name the variable the child signs with (%s)",
						tt.inst.Name, status.EnvVar, tt.envVar.Name, tt.why)
				}
				return
			}
			if status.SignedIn {
				t.Errorf("Status(%q).SignedIn = true (EnvVar %q), want false: nothing resolves here, so the client sends no key (%s)",
					tt.inst.Name, status.EnvVar, tt.why)
			}
			if status.EnvVar != "" {
				t.Errorf("Status(%q).EnvVar = %q, want \"\": naming a variable the child never sends describes a different client (%s)",
					tt.inst.Name, status.EnvVar, tt.why)
			}
		})
	}
}
