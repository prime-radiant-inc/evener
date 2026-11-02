package evener_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// These e2e harnesses write their disposable hubs' providers.toml by hand.
// Issues #4411 and #4437: those configs fell behind the registry schema,
// leaving the hubs with no fake provider while hub liveness still passed.
const (
	e2eTurnControlsScript = "scripts/e2e/e2e-webui-turn-controls.sh"
	// providersHeredocOpen is the line that starts the block. Matching the
	// line itself, rather than the heredoc's ordinal in the file, keeps an
	// edit above it from retargeting the audit at the script's other heredoc
	// (the banner's `cat >&2 <<EOF`).
	providersHeredocOpen = `cat >"$HOME/.config/evener/providers.toml" <<EOF`
	providersHeredocEnd  = "EOF"
	// oldSchemaBlock is the pre-registry block this script wrote until #4411.
	// The audit asserts the registry still refuses it: if that ever stops
	// being true, the premise this audit rests on has changed and someone
	// should look, rather than have the audit pass for a reason that is gone.
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

// scriptProvidersTOML returns the body of the providers.toml heredoc in the
// script at path. Every way of finding nothing is fatal: an opener or
// terminator that moved would otherwise leave the audit parsing an empty
// string, and a check that scans nothing passes.
func scriptProvidersTOML(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(string(body), "\n")
	openAt := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == providersHeredocOpen {
			openAt = i
			break
		}
	}
	if openAt < 0 {
		t.Fatalf("%s no longer writes its providers.toml with the heredoc %q", path, providersHeredocOpen)
	}
	for i := openAt + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != providersHeredocEnd {
			continue
		}
		block := strings.Join(lines[openAt+1:i], "\n")
		if strings.TrimSpace(block) == "" {
			t.Fatalf("%s writes an empty providers.toml", path)
		}
		return block
	}
	t.Fatalf("%s's providers.toml heredoc is never terminated by %q", path, providersHeredocEnd)
	return ""
}

// TestE2EProvidersSchema keeps both hand-written configs parseable by the
// registry the hub loads them with, and their fake instances resolvable.
func TestE2EProvidersSchema(t *testing.T) {
	// The guard on the audit's premise: the pre-registry shape is refused.
	if _, err := registry.ParseConfig([]byte(oldSchemaBlock)); !errors.Is(err, registry.ErrOldSchema) {
		t.Errorf("the pre-registry block %s wrote parses (err = %v), want %v: the audit's premise changed", e2eTurnControlsScript, err, registry.ErrOldSchema)
	}

	for _, tc := range []struct {
		name, source, instance, model, token, replacement string
		heredoc, sessionAffinity                          bool
	}{
		{
			name: "webui-turn-controls", source: e2eTurnControlsScript,
			instance: "fake", model: "fake-test-model",
			token: "$fakellm_port", replacement: "54321",
			heredoc: true, sessionAffinity: true,
		},
		{
			name: "fake429", source: "test/e2e/fake429/providers.toml",
			instance: "ratelimited", model: "fake-model",
			token: "FAKE429_ADDR", replacement: "127.0.0.1:54321",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var block string
			if tc.heredoc {
				block = scriptProvidersTOML(t, tc.source)
			} else {
				body, err := os.ReadFile(tc.source)
				if err != nil {
					t.Fatalf("read %s: %v", tc.source, err)
				}
				block = string(body)
			}
			// Stand in for shell expansion or sed substitution. The URL assertion
			// also catches a missing token or a hardcoded endpoint in either source.
			if !strings.Contains(block, tc.token) {
				t.Fatalf("%s has no address substitution token %q", tc.source, tc.token)
			}
			block = strings.ReplaceAll(block, tc.token, tc.replacement)
			const wantBaseURL = "http://127.0.0.1:54321/v1"
			layer, err := registry.ParseConfig([]byte(block))
			if err != nil {
				t.Fatalf("%s does not parse as the schema the hub loads it with: %v", tc.source, err)
			}
			if layer.Default != tc.instance {
				t.Errorf("%s: default = %q, want %q", tc.source, layer.Default, tc.instance)
			}
			fake, ok := layer.Providers[tc.instance]
			if !ok {
				t.Fatalf("%s declares no [providers.%s]", tc.source, tc.instance)
			}
			if fake.Transport.BaseURL != wantBaseURL {
				t.Errorf("%s: base_url = %q, want %q", tc.source, fake.Transport.BaseURL, wantBaseURL)
			}
			if tc.sessionAffinity && (fake.Caps.SessionAffinityHeaders == nil || !*fake.Caps.SessionAffinityHeaders) {
				t.Errorf("%s: session_affinity_headers = %v, want true (fakellm identifies sessions by these headers)", tc.source, fake.Caps.SessionAffinityHeaders)
			}

			// Exercise the hub's load/resolve boundary without network access:
			// no fetcher is injected and the fresh state root has no catalog cache.
			path := filepath.Join(t.TempDir(), "providers.toml")
			if err := os.WriteFile(path, []byte(block), 0o600); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
			reg, err := registry.Load(registry.WithConfigPath(path), registry.WithStateRoot(t.TempDir()))
			if err != nil {
				t.Fatalf("registry.Load on %s: %v", tc.source, err)
			}
			ref := tc.instance + "/" + tc.model
			resolved, err := reg.Resolve(ref)
			if err != nil {
				t.Fatalf("resolving %s from %s: %v", ref, tc.source, err)
			}
			if resolved.Protocol != "openai-chat" {
				t.Errorf("resolved protocol = %q, want openai-chat", resolved.Protocol)
			}
			if resolved.Transport.BaseURL != wantBaseURL {
				t.Errorf("resolved base_url = %q, want %q", resolved.Transport.BaseURL, wantBaseURL)
			}
			if tc.sessionAffinity && (resolved.Caps.SessionAffinityHeaders == nil || !*resolved.Caps.SessionAffinityHeaders) {
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
