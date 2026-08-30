package hub

// Launch and the credentials pane must describe one instance's credential the
// same way, and the arbiter is the endpoint the adapter will actually contact.
// The registry resolves the credential the spawned child signs its requests
// with; if the launch preflight refuses that instance, or the pane names a
// different variable — or claims a sign-in for a child that will send no key at
// all — one of the two is lying about a session the user is running. Kata z1gm.
//
// The two hub surfaces still read the legacy providercfg file, so each case
// carries both views of one instance: the descriptor the hub reads and the
// registry twin the child resolves. They must agree.

import (
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/providercfg"
	"primeradiant.com/evener/llm/registry"
)

// TestCredentialAgreement_HubAgreesWithTheKeyTheChildSends holds the launch
// preflight and evener/auth/status to what the registry resolves, which is the
// only credential the spawned process has. Each case seeds exactly one
// environment variable, asks the registry what the child would authenticate
// with, and requires both hub surfaces to agree.
//
// Instances whose behavior tag is "openai" are out of scope here: their OAuth
// record is a credential with no key to inject, so injection is not the whole
// answer for them. Every shape below authenticates with an API key or nothing.
func TestCredentialAgreement_HubAgreesWithTheKeyTheChildSends(t *testing.T) {
	// A base URL that is syntactically real and semantically unreachable: no
	// case here issues a request, and a loopback port nothing listens on keeps
	// it that way if one ever does.
	const gatewayBaseURL = "http://127.0.0.1:9/v1"

	// A chat-completions instance behind the openai row: the registry twin of
	// providercfg's Type "openai" + APIStyle "chat-completions".
	compatTwin := func(baseURL string) registry.Provider {
		return registry.Provider{
			Base: "openai", Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric,
			Transport: registry.Transport{BaseURL: baseURL},
		}
	}

	for _, tt := range []struct {
		name         string
		inst         providercfg.InstanceConfig
		twin         registry.Provider
		envVar       envvars.Var
		value        string
		wantInjected bool
		why          string
	}{
		{
			name:         "openai on chat-completions with no base_url",
			inst:         providercfg.InstanceConfig{Name: "local", Type: "openai", APIStyle: providercfg.StyleChatCompletions},
			twin:         compatTwin(""),
			envVar:       envvars.OpenAIAPIKey,
			value:        "sk-openai-only",
			wantInjected: true,
			why:          "with no base_url the adapter targets api.openai.com, so OPENAI_API_KEY is the key that signs its requests",
		},
		{
			name:         "the materialized openai instance flipped to chat-completions",
			inst:         providercfg.InstanceConfig{Name: "openai", Type: "openai", APIStyle: providercfg.StyleChatCompletions},
			twin:         compatTwin(""),
			envVar:       envvars.OpenAIAPIKey,
			value:        "sk-openai-only",
			wantInjected: true,
			why:          "two clicks in the instance dialog reach this shape from the hub's own default instance",
		},
		{
			name:         "custom gateway at its own base_url",
			inst:         providercfg.InstanceConfig{Name: "gateway", Type: "openai", APIStyle: providercfg.StyleChatCompletions, BaseURL: gatewayBaseURL},
			twin:         compatTwin(gatewayBaseURL),
			envVar:       envvars.OpenAICompatibleAPIKey,
			value:        "sk-compat-only",
			wantInjected: false,
			why:          "OPENAI_COMPATIBLE_API_KEY belongs to the host OPENAI_COMPATIBLE_BASE_URL names, which this gateway is not; the child sends no Authorization header at all",
		},
		{
			name:         "gateway named after another provider row",
			inst:         providercfg.InstanceConfig{Name: "glm", Type: "openai", APIStyle: providercfg.StyleChatCompletions, BaseURL: gatewayBaseURL},
			twin:         compatTwin(gatewayBaseURL),
			envVar:       envvars.GLMAPIKey,
			value:        "sk-glm-only",
			wantInjected: true,
			why:          "the name-scoped layer resolves for any instance, base_url or not",
		},
		{
			name:         "instance named after another provider row with no base_url",
			inst:         providercfg.InstanceConfig{Name: "glm", Type: "openai", APIStyle: providercfg.StyleChatCompletions},
			twin:         compatTwin(""),
			envVar:       envvars.GLMAPIKey,
			value:        "sk-glm-only",
			wantInjected: true,
			why:          "the preflight must resolve the layers ResolveKey resolves, name before tag; a key the provider then rejects is the provider's 401 to give, not the gate's refusal to make",
		},
		{
			name:         "anthropic instance under a name of its own",
			inst:         providercfg.InstanceConfig{Name: "work", Type: "anthropic"},
			twin:         registry.Provider{Base: "anthropic"},
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

			// The credential the child process is actually built with.
			resolved := childCredentialSource(t, tt.inst.Name, tt.twin, tt.envVar.Name, tt.value)
			if got := resolved != "" && resolved != "none"; got != tt.wantInjected {
				t.Fatalf("the registry resolves a credential = %v (source %q), want %v: %s", got, resolved, tt.wantInjected, tt.why)
			}

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
					t.Errorf("validateProviderCredentials(%q) = %v, want nil: the child authenticates with %s, so the gate in front of it must not refuse (%s)",
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
				t.Errorf("Status(%q).SignedIn = true (EnvVar %q), want false: the registry resolves nothing here, so the child sends no key (%s)",
					tt.inst.Name, status.EnvVar, tt.why)
			}
			if status.EnvVar != "" {
				t.Errorf("Status(%q).EnvVar = %q, want \"\": naming a variable the child never sends describes a different client (%s)",
					tt.inst.Name, status.EnvVar, tt.why)
			}
		})
	}
}

// childCredentialSource is what the spawned child's registry resolves for one
// instance with exactly one environment variable seeded — the third party the
// two hub surfaces have to agree with. It loads offline against a temp state
// root so nothing but the seeded variable can answer.
func childCredentialSource(t *testing.T, name string, twin registry.Provider, envVar, value string) string {
	t.Helper()
	env := map[string]string{envVar: value}
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok }),
		registry.WithInstances(map[string]registry.Provider{name: twin}),
	)
	if err != nil {
		t.Fatalf("registry twin for %q: %v", name, err)
	}
	inst, ok := r.Instance(name)
	if !ok {
		t.Fatalf("the registry twin has no instance %q", name)
	}
	return inst.CredentialSource
}
