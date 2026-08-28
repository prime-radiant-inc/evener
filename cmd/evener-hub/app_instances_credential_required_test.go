package hub

// Whether an instance needs a credential at all is one registry fact — its
// transport auth scheme — and evener/instance/list must carry it, because the
// credentials pane cannot rederive it: the entry's baseUrl is the sanitized
// copy, empty whenever the authored URL does not parse. Without the bit, the
// pane saw only a credential source of "none" and rendered a working local
// endpoint as "Not configured" (credentialLabels.ts, unconfiguredLabel).
// Kata bg3n.

import (
	"testing"

	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/llm/registry"
)

// credentialShapesToml carries one instance per credential-requirement shape:
// the two auth schemes that need nothing, a gateway that needs a key it does
// not inherit, and a plain key-bearing instance.
const credentialShapesToml = `[providers.llama]
base     = "openai-compatible"
base_url = "http://127.0.0.1:8080/v1"
auth     = "none"

[providers.maybe]
base     = "openai-compatible"
base_url = "http://127.0.0.1:8081/v1"
auth     = "optional-bearer"

[providers.gateway]
base     = "openai"
base_url = "http://127.0.0.1:9/v1"

[providers.work]
base = "anthropic"
`

func TestInstanceList_CredentialRequiredFollowsTheAuthScheme(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	dir := t.TempDir()
	tomlPath := writeProvidersToml(t, dir, credentialShapesToml)
	// The gateway and the plain instance need a key that is nowhere, so the
	// environment must be empty of one for the "none" source below to mean
	// what it says.
	auth := newTestAuthController(t, dir, t.TempDir(), tomlPath)
	instances := &hubInstancesController{reg: auth.reg, providersConfigPath: tomlPath, auth: auth}

	for _, tt := range []struct {
		name         string
		wantAuth     string
		wantRequired bool
		why          string
	}{
		{
			name:         "llama",
			wantAuth:     registry.AuthNone,
			wantRequired: false,
			why:          "auth = none: there is no key to be missing",
		},
		{
			name:         "maybe",
			wantAuth:     registry.AuthOptionalBearer,
			wantRequired: false,
			why:          "optional-bearer sends a key when there is one and works without",
		},
		{
			name:         "gateway",
			wantAuth:     registry.AuthBearer,
			wantRequired: true,
			why:          "a gateway inherits no vendor key (spec §10) but still authenticates with one of its own",
		},
		{
			name:         "work",
			wantAuth:     registry.AuthHeader,
			wantRequired: true,
			why:          "anthropic authenticates with an x-api-key header",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			row := instanceListRow(t, instances, tt.name)
			if row.Auth != tt.wantAuth {
				t.Fatalf("evener/instance/list %q auth = %q, want %q — the fixture is not the shape this case names", tt.name, row.Auth, tt.wantAuth)
			}
			if row.CredentialRequired != tt.wantRequired {
				t.Errorf("evener/instance/list %q credentialRequired = %v, want %v: %s",
					tt.name, row.CredentialRequired, tt.wantRequired, tt.why)
			}
			// The bit and the source are what the pane pairs: "none" plus
			// required=false is the optional label, and "none" plus
			// required=true is the missing-credential one.
			if row.ActiveSource != "none" {
				t.Errorf("evener/instance/list %q activeSource = %q, want none — nothing in this fixture supplies a credential", tt.name, row.ActiveSource)
			}
		})
	}
}
