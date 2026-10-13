package evener_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// The e2e harness writes its disposable hub's providers.toml by hand, in a
// shell heredoc, so no Go code builds that file and, until this audit, no Go
// test read it. That is issue #4411: the block fell behind the registry schema
// the hub loads it with, the hub came up with no fakellm instance, and the
// script's own check — hub liveness — passed anyway.
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

// TestE2EWebuiTurnControlsProvidersSchema keeps the harness's hand-written
// providers.toml parseable by the registry the hub loads it with, and keeps
// the instance it names resolvable.
func TestE2EWebuiTurnControlsProvidersSchema(t *testing.T) {
	// A literal port stands in for the shell's $fakellm_port, which the
	// script expands as it writes the file. The assertions below read that
	// port back out, so a block that stopped interpolating the
	// kernel-assigned port fails here too.
	const port = "54321"
	const wantBaseURL = "http://127.0.0.1:" + port + "/v1"
	block := strings.ReplaceAll(scriptProvidersTOML(t, e2eTurnControlsScript), "$fakellm_port", port)

	// The guard on the audit's premise: the shape this script used to write is
	// refused, so nothing below can pass by exercising a block the hub would
	// take only by accident.
	if _, err := registry.ParseConfig([]byte(oldSchemaBlock)); !errors.Is(err, registry.ErrOldSchema) {
		t.Errorf("the pre-registry block %s wrote parses (err = %v), want %v: the audit's premise changed", e2eTurnControlsScript, err, registry.ErrOldSchema)
	}

	layer, err := registry.ParseConfig([]byte(block))
	if err != nil {
		t.Fatalf("the providers.toml %s writes does not parse as the schema the hub loads it with: %v", e2eTurnControlsScript, err)
	}
	if layer.Default != "fake" {
		t.Errorf("%s: default = %q, want \"fake\"", e2eTurnControlsScript, layer.Default)
	}
	fake, ok := layer.Providers["fake"]
	if !ok {
		t.Fatalf("%s: the block declares no [providers.fake]: %s", e2eTurnControlsScript, block)
	}
	if fake.Transport.BaseURL != wantBaseURL {
		t.Errorf("%s: providers.fake.base_url = %q, want %q", e2eTurnControlsScript, fake.Transport.BaseURL, wantBaseURL)
	}
	if fake.Caps.SessionAffinityHeaders == nil || !*fake.Caps.SessionAffinityHeaders {
		t.Errorf("%s: providers.fake.session_affinity_headers = %v, want true (fakellm names each round's session off those headers)", e2eTurnControlsScript, fake.Caps.SessionAffinityHeaders)
	}

	// The hub loads the file through registry.Load, so the audit does too: a
	// block the parser accepts but resolution refuses still leaves the
	// script's printed "model fake/fake-test-model" spawn line dead. Nothing
	// here reaches the network: no fetcher is injected, and the fresh state
	// root holds no catalog cache.
	path := filepath.Join(t.TempDir(), "providers.toml")
	if err := os.WriteFile(path, []byte(block), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	reg, err := registry.Load(registry.WithConfigPath(path), registry.WithStateRoot(t.TempDir()))
	if err != nil {
		t.Fatalf("registry.Load on the file %s writes: %v", e2eTurnControlsScript, err)
	}
	resolved, err := reg.Resolve("fake/fake-test-model")
	if err != nil {
		t.Fatalf("resolving fake/fake-test-model (the reference %s prints): %v", e2eTurnControlsScript, err)
	}
	if resolved.Protocol != "openai-chat" {
		t.Errorf("resolved protocol = %q, want \"openai-chat\" (the row base = \"openai-compatible\" inherits)", resolved.Protocol)
	}
	if resolved.Transport.BaseURL != wantBaseURL {
		t.Errorf("resolved base_url = %q, want %q", resolved.Transport.BaseURL, wantBaseURL)
	}
	// The resolved caps are what the protocol reads before it puts the
	// session-affinity headers on the wire (llm/providers/chatcompletions/
	// complete.go), and no layer above the block sets this cap, so a layer
	// that one day masks it shows up here rather than as anonymous rounds.
	if resolved.Caps.SessionAffinityHeaders == nil || !*resolved.Caps.SessionAffinityHeaders {
		t.Errorf("resolved session_affinity_headers = %v, want true", resolved.Caps.SessionAffinityHeaders)
	}
	// fakellm serves exactly /v1/models and /v1/chat/completions and answers
	// every other path with a 404 (test/e2e/fakellm/fakellm.go:112-114), so
	// the paths the protocol derives from the block's base URL are part of the
	// repair: the hub has to land on those two routes.
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
}
