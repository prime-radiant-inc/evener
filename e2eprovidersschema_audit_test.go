package evener_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/llm/registry"
	"primeradiant.com/evener/test/e2e/providers"
)

// These e2e harnesses hand a disposable hub a providers.toml written by the
// one writer in test/e2e/providers (#4445). Issues #4411 and #4437: the
// hand-written copies had fallen behind the registry schema, leaving the hubs
// with no fake provider while hub liveness still passed.
const (
	e2eTurnControlsScript = "scripts/e2e/e2e-webui-turn-controls.sh"
	e2eRateLimitedScript  = "scripts/e2e/e2e-ratelimited-provider.sh"
	// oldSchemaBlock is the pre-registry block the turn-controls script wrote
	// until #4411. The audit asserts the registry still refuses it: if that
	// ever stops being true, the premise this audit rests on has changed and
	// someone should look, rather than have the audit pass for a reason that
	// is gone.
	oldSchemaBlock = `schema = 1
default = "fake"

[instances.fake]
type = "openai"
api_style = "chat-completions"
base_url = "http://127.0.0.1:54321/v1"
api_key = "fakellm-not-a-secret"
send_session_affinity_headers = true
`
)

// TestE2EProvidersSchema keeps the harnesses' now-shared config parseable by
// the registry the hub loads it with, and their fake instances resolvable.
// The shell scripts' chosen flags are pinned too: the config the writer emits
// is only the hub's when the harness asks for the right fake.
func TestE2EProvidersSchema(t *testing.T) {
	// The guard on the audit's premise: the pre-registry shape is refused.
	if _, err := registry.ParseConfig([]byte(oldSchemaBlock)); !errors.Is(err, registry.ErrOldSchema) {
		t.Errorf("the pre-registry block parses (err = %v), want %v: the audit's premise changed", err, registry.ErrOldSchema)
	}
	const wantBaseURL = "http://127.0.0.1:54321/v1"
	affinityOn := func(c registry.Caps) bool {
		return c.SessionAffinityHeaders != nil && *c.SessionAffinityHeaders
	}

	for _, tc := range []struct {
		name, source, instance, model, apiKey string
		sessionAffinity                       bool
		scriptFlags                           []string
	}{
		{
			name: "webui-turn-controls", source: e2eTurnControlsScript,
			instance: "fake", model: "fake-test-model",
			apiKey: "fakellm-not-a-secret", sessionAffinity: true,
			scriptFlags: []string{"--default fake", "--api-key fakellm-not-a-secret", "--session-affinity-headers"},
		},
		{
			name: "fake429", source: e2eRateLimitedScript,
			instance: "ratelimited", model: "fake-model",
			scriptFlags: []string{"--default ratelimited"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script, err := os.ReadFile(tc.source)
			if err != nil {
				t.Fatalf("read %s: %v", tc.source, err)
			}
			for _, flag := range tc.scriptFlags {
				if !strings.Contains(string(script), flag) {
					t.Fatalf("%s no longer passes %q to the providers.toml writer", tc.source, flag)
				}
			}
			rendered, err := providers.Render(providers.Config{
				Name:                   tc.instance,
				BaseURL:                wantBaseURL,
				APIKey:                 tc.apiKey,
				SessionAffinityHeaders: tc.sessionAffinity,
			})
			if err != nil {
				t.Fatalf("render the one writer's config: %v", err)
			}
			block := string(rendered)
			layer, err := registry.ParseConfig([]byte(block))
			if err != nil {
				t.Fatalf("the one writer's config does not parse as the schema the hub loads it with: %v", err)
			}
			if layer.Default != tc.instance {
				t.Errorf("default = %q, want %q", layer.Default, tc.instance)
			}
			fake, ok := layer.Providers[tc.instance]
			if !ok {
				t.Fatalf("the writer declares no [providers.%s]", tc.instance)
			}
			if fake.Transport.BaseURL != wantBaseURL {
				t.Errorf("base_url = %q, want %q", fake.Transport.BaseURL, wantBaseURL)
			}
			if tc.sessionAffinity && !affinityOn(fake.Caps) {
				t.Errorf("session_affinity_headers = %v, want true (fakellm identifies sessions by these headers)", fake.Caps.SessionAffinityHeaders)
			}

			// Exercise the hub's load/resolve boundary without network access:
			// no fetcher is injected and the fresh state root has no catalog cache.
			path := filepath.Join(t.TempDir(), "providers.toml")
			if err := os.WriteFile(path, []byte(block), 0o600); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
			reg, err := registry.Load(registry.WithConfigPath(path), registry.WithStateRoot(t.TempDir()))
			if err != nil {
				t.Fatalf("registry.Load on the writer's config: %v", err)
			}
			ref := tc.instance + "/" + tc.model
			resolved, err := reg.Resolve(ref)
			if err != nil {
				t.Fatalf("resolving %s from the writer's config: %v", ref, err)
			}
			if resolved.Protocol != "openai-chat" {
				t.Errorf("resolved protocol = %q, want openai-chat", resolved.Protocol)
			}
			if resolved.Transport.BaseURL != wantBaseURL {
				t.Errorf("resolved base_url = %q, want %q", resolved.Transport.BaseURL, wantBaseURL)
			}
			if tc.sessionAffinity && !affinityOn(resolved.Caps) {
				t.Errorf("resolved session_affinity_headers = %v, want true", resolved.Caps.SessionAffinityHeaders)
			}
			if resolved.Transport.Auth != registry.AuthOptionalBearer {
				t.Errorf("resolved auth = %q, want %q", resolved.Transport.Auth, registry.AuthOptionalBearer)
			}
			// Both fakes serve model listing and Chat Completions at these routes.
			for _, route := range []struct {
				what, got, want string
			}{
				{"completions", resolved.Transport.BaseURL + resolved.Transport.Endpoint, wantBaseURL + "/chat/completions"},
				{"streaming completions", resolved.Transport.BaseURL + resolved.Transport.StreamEndpoint, wantBaseURL + "/chat/completions"},
				{"models", resolved.Transport.BaseURL + resolved.Transport.ModelsEndpoint, wantBaseURL + "/models"},
			} {
				if route.got != route.want {
					t.Errorf("the hub would reach %s at %q, want %q", route.what, route.got, route.want)
				}
			}
		})
	}
}
