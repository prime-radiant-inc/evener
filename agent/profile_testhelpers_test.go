package agent

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm/registry"
)

// Test shims for building provider profiles from package agent.
//
// Profile and its constructors live in agent/provider. Agent tests build
// profiles by resolving a reference on testRegistry: one hermetic registry
// carrying the curated instances the fixtures name plus the two custom shapes
// (a chat-completions gateway and spec §14.1's OpenRouter-over-Anthropic
// recipe). Loading parses the embedded catalog, so registries are memoized per
// distinct instance set — a test binary pays for each shape once.

// The profile decorators are re-exported so existing tests need not qualify
// every call.
var (
	NewOpenAIProfile            = provider.NewOpenAIProfile
	WithCheapModel              = provider.WithCheapModel
	WithContextWindow           = provider.WithContextWindow
	WithCommunicateOutputSchema = provider.WithCommunicateOutputSchema
	WithAllowedDecisions        = provider.WithAllowedDecisions
)

// testRegistryInstances are the fixture instances. A name that matches a
// registry id inherits it, so a curated instance needs nothing but a key; a
// custom name needs a base, a transport, and a key.
func testRegistryInstances() map[string]registry.Provider {
	return map[string]registry.Provider{
		"anthropic":       {APIKey: "test"},
		"google":          {APIKey: "test"},
		"minimax":         {APIKey: "test"},
		"kimi-for-coding": {APIKey: "test"},
		"openrouter":      {APIKey: "test"},
		"ollama":          {},
		"kimi":            {Base: "moonshotai", APIKey: "test"},
		"glm":             {Base: "zai", APIKey: "test"},
		// Two openai instances under user-assigned names: the renamed-instance
		// lifecycle the integration tests drive.
		"work":  {Base: "openai", APIKey: "test"},
		"work2": {Base: "openai", APIKey: "test"},
		// A gateway speaking chat-completions behind the openai id.
		"gw": {
			Base: "openai", Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric,
			APIKey: "test", Transport: registry.Transport{BaseURL: "https://gw.example.com/v1"},
			DefaultModel: "glm-5", CheapModel: "glm-5-flash",
		},
		// Spec §14.1: OpenRouter reached over the Anthropic protocol, so every
		// model it serves is an Anthropic-surface model.
		"orclaude": {
			Base: "openrouter", Protocol: registry.ProtocolAnthropic, Surface: registry.SurfaceAnthropic,
			APIKey: "test",
		},
	}
}

// testRegistryStateRoot is a path that does not exist: the fixtures must never
// read a developer's OAuth records or catalog cache, and nothing writes here.
var testRegistryStateRoot = sync.OnceValue(func() string {
	dir, err := os.MkdirTemp("", "evener-agent-registry-*")
	if err != nil {
		return filepath.Join(os.TempDir(), "evener-agent-registry-absent")
	}
	_ = os.RemoveAll(dir)
	return dir
})

var (
	testRegistryMu    sync.Mutex
	testRegistryCache = map[string]*registry.Registry{}
)

// testRegistryWith loads the fixture registry with extra instances merged in,
// memoized per distinct instance set.
func testRegistryWith(extra map[string]registry.Provider) (*registry.Registry, error) {
	instances := testRegistryInstances()
	maps.Copy(instances, extra)
	key, err := json.Marshal(instances)
	if err != nil {
		return nil, err
	}
	testRegistryMu.Lock()
	defer testRegistryMu.Unlock()
	if r, ok := testRegistryCache[string(key)]; ok {
		return r, nil
	}
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(testRegistryStateRoot()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(instances),
	)
	if err != nil {
		return nil, err
	}
	testRegistryCache[string(key)] = r
	return r, nil
}

// testRegistry is the fixture registry every agent profile helper resolves on.
func testRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	r, err := testRegistryWith(nil)
	if err != nil {
		t.Fatalf("testRegistry: %v", err)
	}
	return r
}

// mustTestRegistry is testRegistry for the profile helpers, which have no
// *testing.T: a fixture registry that fails to load is a broken fixture.
func mustTestRegistry(extra map[string]registry.Provider) *registry.Registry {
	r, err := testRegistryWith(extra)
	if err != nil {
		panic("agent: test registry: " + err.Error())
	}
	return r
}

// resolveTestProfile resolves instance/model on the fixture registry, adding
// the instance first when the fixture does not already carry it.
func resolveTestProfile(instance string, extra map[string]registry.Provider, model string) *provider.Profile {
	p, err := provider.Resolve(mustTestRegistry(extra), instance+"/"+model)
	if err != nil {
		panic("resolveTestProfile: " + err.Error())
	}
	return p
}

func newAnthropicProfile(model string) *provider.Profile {
	return resolveTestProfile("anthropic", nil, model)
}

func newGeminiProfile(model string) *provider.Profile {
	return resolveTestProfile("google", nil, model)
}

func newMiniMaxProfile(model string) *provider.Profile {
	return resolveTestProfile("minimax", nil, model)
}

func newOpenRouterAnthropicProfile(model string) *provider.Profile {
	return resolveTestProfile("orclaude", nil, model)
}

func newKimiAnthropicProfile(model string) *provider.Profile {
	return resolveTestProfile("kimi-for-coding", nil, model)
}

// newOpenAICompatProfile resolves a generic-surface, chat-completions instance
// by name. The window argument the old constructor took is gone: a row's
// window is the registry's, and WithContextWindow overrides it.
func newOpenAICompatProfile(id, model string, _ int) *provider.Profile {
	return resolveTestProfile(id, openAICompatInstance(id), model)
}

// openaiInstance is the openai instance pointed at a test server, for the
// session tests that dispatch over the Responses protocol for real.
func openaiInstance(srvURL string) registry.Provider {
	return registry.Provider{Base: "openai", APIKey: "test-key", Transport: registry.Transport{BaseURL: srvURL}}
}

// openAICompatInstance is the instance an openai-compatible fixture id needs
// when the fixture registry does not already carry it: a custom gateway.
func openAICompatInstance(id string) map[string]registry.Provider {
	if _, ok := testRegistryInstances()[id]; ok {
		return nil
	}
	return map[string]registry.Provider{id: {
		Base: "openai-compatible", APIKey: "test",
		Transport: registry.Transport{BaseURL: "https://" + id + ".example.com/v1"},
	}}
}

// namedInstanceProfile resolves model on an instance called name that is
// backed by base. It is the registry-native form of a user-assigned instance
// name, which the deleted WithProviderID used to fake.
func namedInstanceProfile(name, base, model string) *provider.Profile {
	return resolveTestProfile(name, map[string]registry.Provider{name: {Base: base, APIKey: "test"}}, model)
}

// namedOpenAIInstanceProfile is the fixture's renamed-openai instance profile:
// an instance whose name differs from the provider id behind it.
func namedOpenAIInstanceProfile(name, model string) *provider.Profile {
	return resolveTestProfile(name, nil, model)
}

// withEffortLevels returns a copy of p whose row advertises the given effort
// ladder. It stands in for what a live listing does to a profile: the registry
// merges the advertised levels and the profile rebuilds from the record.
func withEffortLevels(p *provider.Profile, levels ...string) *provider.Profile {
	res := p.Resolved()
	res.Caps.EffortValues = levels
	res.Caps.Reasoning = new(true)
	return p.WithResolved(res)
}

// withWebSearch returns a copy of p whose row does (or does not) serve
// provider-native web search.
func withWebSearch(p *provider.Profile, on bool) *provider.Profile {
	res := p.Resolved()
	res.Caps.WebSearch = new(on)
	return p.WithResolved(res)
}

// testProfile builds a profile for the given instance and model, with the
// context window overridden when contextWindow > 0. The common fixture for
// context-manager, strategy, and transcript tests.
func testProfile(instance, model string, contextWindow int) *provider.Profile {
	p := resolveTestProfile(instance, nil, model)
	if contextWindow > 0 {
		p = provider.WithContextWindow(p, contextWindow)
	}
	return p
}

// toStringSlice converts a JSON-schema array field (either []string directly or
// []any after a JSON round-trip) to []string, for asserting on tool-schema
// fields in tests.
func toStringSlice(v any) []string {
	switch val := v.(type) {
	case []string:
		return val
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// testOpenAICompatProfile builds a generic-surface chat-completions profile
// with the given instance id, model, and window.
func testOpenAICompatProfile(id, model string, contextWindow int) *provider.Profile {
	p := newOpenAICompatProfile(id, model, 0)
	if contextWindow > 0 {
		p = provider.WithContextWindow(p, contextWindow)
	}
	return p
}
