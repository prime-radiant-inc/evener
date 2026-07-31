package main

// Whether an instance needs a credential at all is a fact the hub already
// derives: credentialRequired is the gate serf/auth/test asks before it decides
// an instance is unconfigured, and it answers false for a gateway — an
// openai-compatible instance carrying a base_url — because such an instance
// inherits no type-level key (providercfg.CredentialTag returns the empty tag
// for it) and many gateways, llama.cpp among them, accept requests with no key.
//
// serf/instance/list did not carry that bit, so the credentials pane saw only
// activeSource "absent" and rendered a working local gateway as "Not
// configured" (credentialLabels.ts, unconfiguredLabel). The pane cannot rederive
// the rule: its baseUrl is the sanitized copy, empty whenever the authored URL
// does not parse. Kata bg3n.
//
// The test pins the wire bit to the gate rather than to the one shape that
// prompted it, so the pane and the probe can never describe one instance two
// ways.

import (
	"testing"

	"primeradiant.com/serf/auth/openai/oaitest"
	"primeradiant.com/serf/llm/providercfg"
)

// credentialShapesToml carries one instance per credential-requirement shape:
// the two openai-compatible behaviors that part company over base_url, a
// plain key-bearing type, and the auth-none type whose exemption comes from
// the registry instead.
const credentialShapesToml = `schema = 1
default = "work"

[instances.llama]
type = "openai"
api_style = "chat-completions"
base_url = "http://127.0.0.1:8080/v1"

[instances.gpt]
type = "openai"
api_style = "chat-completions"

[instances.work]
type = "anthropic"

[instances.local]
type = "ollama"
`

func TestInstanceList_CredentialRequiredFollowsTheProbeGate(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	clearProviderKeysFromEnvironment(t)
	dir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, credentialShapesToml)
	auth := newTestAuthController(t, dir, t.TempDir(), tomlPath)
	instances := &hubInstancesController{providersConfigPath: tomlPath, auth: auth}

	cfg, exists, err := providercfg.LoadFile(tomlPath)
	if err != nil || !exists {
		t.Fatalf("LoadFile(%q) = exists %v, err %v", tomlPath, exists, err)
	}

	for _, tt := range []struct {
		name             string
		wantRequired     bool
		wantActiveSource string
		why              string
	}{
		{
			name:             "llama",
			wantRequired:     false,
			wantActiveSource: "absent",
			why:              "an openai-compatible gateway named by base_url inherits no type key, so it has no key to be missing",
		},
		{
			name:             "gpt",
			wantRequired:     true,
			wantActiveSource: "absent",
			why:              "openai-compatible with no base_url is api.openai.com, which authenticates with a key",
		},
		{
			name:             "work",
			wantRequired:     true,
			wantActiveSource: "absent",
			why:              "anthropic authenticates with a key",
		},
		{
			name:             "local",
			wantRequired:     false,
			wantActiveSource: "none",
			why:              "ollama declares auth mode \"none\" in the registry",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inst, ok := configuredInstance(cfg, tt.name)
			if !ok {
				t.Fatalf("providers.toml has no %q instance", tt.name)
			}
			row := instanceListRow(t, instances, tt.name)

			if row.CredentialRequired != tt.wantRequired {
				t.Errorf("serf/instance/list %q credentialRequired = %v, want %v: %s",
					tt.name, row.CredentialRequired, tt.wantRequired, tt.why)
			}
			// The gate serf/auth/test asks owns this answer; the wire must
			// report it rather than a second derivation of its own.
			if got := credentialRequired(inst); row.CredentialRequired != got {
				t.Errorf("serf/instance/list %q credentialRequired = %v, but the serf/auth/test gate says %v — one instance, two answers",
					tt.name, row.CredentialRequired, got)
			}
			// activeSource is what the pane pairs the bit with: "absent" plus
			// required=false is the optional-gateway label, and the auth-none
			// case must keep reaching its own "none" answer instead.
			if row.ActiveSource != tt.wantActiveSource {
				t.Errorf("serf/instance/list %q activeSource = %q, want %q",
					tt.name, row.ActiveSource, tt.wantActiveSource)
			}
		})
	}
}
