package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// fixtureRegistry is the hermetic registry the profile tests resolve on: the
// embedded catalog and overlay with a handful of instances and no user layer,
// cache, or environment.
func fixtureRegistry(t *testing.T) *registry.Registry {
	return fixtureRegistryWith(t, nil)
}

// fixtureRegistryWith is fixtureRegistry with extra instances merged over the
// fixture set, for tests that need synthetic rows.
func fixtureRegistryWith(t *testing.T, extra map[string]registry.Provider) *registry.Registry {
	t.Helper()
	instances := map[string]registry.Provider{
		"anthropic":  {APIKey: "k"},
		"google":     {APIKey: "k"},
		"openrouter": {APIKey: "k"},
		// ollama is the instance with no curated cheap_model, so the
		// cheap-model fallthrough has something to fall through on.
		"ollama":   {},
		"work":     {Base: "openai", Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric, APIKey: "k", Transport: registry.Transport{BaseURL: "https://gw.example.com/v1"}, DefaultModel: "glm-5", CheapModel: "glm-5-flash"},
		"orclaude": {Base: "openrouter", Protocol: registry.ProtocolAnthropic, APIKey: "k", Models: map[string]registry.Model{"minimax/*": {Surface: registry.SurfaceAnthropic}}},
	}
	maps.Copy(instances, extra)
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(), registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(instances),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return r
}

func mustResolve(t *testing.T, r *registry.Registry, ref string) *Profile {
	t.Helper()
	p, err := Resolve(r, ref)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", ref, err)
	}
	return p
}

// assertTaskListEffortEnum requires the task_list tool to be advertised and
// its effort enum to match levels. Its absence is a failure in its own right:
// the workflow surface losing the tool would otherwise slip past a comparison
// that only runs when the tool is there.
func assertTaskListEffortEnum(t *testing.T, p *Profile, levels []string) {
	t.Helper()
	got := findToolDef(p, "task_list")
	if got == nil {
		t.Fatalf("%s/%s advertises no task_list tool", p.ID(), p.Model())
	}
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(tool.DefTaskList(levels))
	if !bytes.Equal(gb, wb) {
		t.Fatalf("task_list effort enum does not match the ladder %v\n got=%s\nwant=%s", levels, gb, wb)
	}
}

func findToolDef(p *Profile, name string) *llm.ToolDefinition {
	for _, td := range p.ToolDefinitions() {
		if td.Name == name {
			d := td
			return &d
		}
	}
	return nil
}

// TestResolveProfileSurfaceConventions pins the surface-keyed conventions
// (spec §7.5): the doc files, the renamed tools, and the web_search function
// tool that rides only with the google protocol.
func TestResolveProfileSurfaceConventions(t *testing.T) {
	r := fixtureRegistry(t)
	cases := []struct {
		ref, surface, protocol string
		docs                   []string
		shellTool              string
		webSearchTool          bool
	}{
		{"openai/gpt-5.5", registry.SurfaceOpenAI, registry.ProtocolOpenAIResponses, []string{"AGENTS.md", ".codex/instructions.md"}, "exec_command", false},
		{"anthropic/claude-opus-5", registry.SurfaceAnthropic, registry.ProtocolAnthropic, []string{"CLAUDE.md", "AGENTS.md"}, "", false},
		{"google/gemini-3-pro", registry.SurfaceGoogle, registry.ProtocolGoogle, []string{"GEMINI.md", "AGENTS.md"}, "run_shell_command", true},
		{"work/glm-5", registry.SurfaceGeneric, registry.ProtocolOpenAIChat, []string{"AGENTS.md"}, "", false},
		{"orclaude/minimax/minimax-m3", registry.SurfaceAnthropic, registry.ProtocolAnthropic, []string{"CLAUDE.md", "AGENTS.md"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			p := mustResolve(t, r, tc.ref)
			if p.Surface() != tc.surface || p.Protocol() != tc.protocol {
				t.Fatalf("surface/protocol: %s %s", p.Surface(), p.Protocol())
			}
			if strings.Join(p.ProjectDocFiles(), ",") != strings.Join(tc.docs, ",") {
				t.Fatalf("docs: %v", p.ProjectDocFiles())
			}
			if got := p.ToolNameMap()["shell"]; got != tc.shellTool {
				t.Fatalf("shell tool name: %q", got)
			}
			if got := findToolDef(p, "web_search") != nil; got != tc.webSearchTool {
				t.Fatalf("web_search function tool advertised: %v, want %v (google protocol + WebSearch only)", got, tc.webSearchTool)
			}
		})
	}
}

// TestProfileFactsComeFromCaps pins §7.5: every model fact the agent reads is
// the registry's merged Caps record, and an unknown model reports no window
// with the §7.3 warning rather than a made-up default.
func TestProfileFactsComeFromCaps(t *testing.T) {
	p := mustResolve(t, fixtureRegistry(t), "anthropic/claude-opus-5")
	res := p.Resolved()
	if res.Caps.ContextWindow == nil || p.ContextWindowSize() != *res.Caps.ContextWindow {
		t.Fatalf("context window: %d vs %+v", p.ContextWindowSize(), res.Caps.ContextWindow)
	}
	if res.Caps.MaxOutputTokens == nil || p.MaxOutputTokens() != *res.Caps.MaxOutputTokens {
		t.Fatalf("max output: %d", p.MaxOutputTokens())
	}
	if !p.SupportsReasoning() || strings.Join(p.ReasoningEffortLevels(), ",") != strings.Join(res.Caps.EffortValues, ",") {
		t.Fatalf("effort ladder: %v", p.ReasoningEffortLevels())
	}
	if p.Cost() == nil || p.KnowledgeCutoff() == "" || len(p.InputModalities()) == 0 {
		t.Fatalf("cost/cutoff/modalities: %v %q %v", p.Cost(), p.KnowledgeCutoff(), p.InputModalities())
	}
	if p.CheapModel() == "" || p.CheapModel() == p.Model() {
		t.Fatalf("anthropic carries a curated cheap_model: %q", p.CheapModel())
	}
	unknown := mustResolve(t, fixtureRegistry(t), "anthropic/claude-new-thing")
	if unknown.ContextWindowSize() != 0 || len(unknown.Warnings()) == 0 {
		t.Fatalf("an unknown model has no window and a warning (spec §7.3): %d %v", unknown.ContextWindowSize(), unknown.Warnings())
	}
	if !unknown.SupportsReasoning() {
		t.Fatal("unknown reasoning is not disabled reasoning")
	}
}

// TestProfileReasoningDisabledClearsTheLadder pins §7.4: an explicit
// reasoning = false row sends no reasoning control at all.
func TestProfileReasoningDisabledClearsTheLadder(t *testing.T) {
	p := mustResolve(t, fixtureRegistry(t), "work/glm-5")
	res := p.Resolved()
	res.Caps.Reasoning = new(false)
	res.Caps.EffortValues = []string{"low", "high"}
	q := p.WithResolved(res)
	if q.SupportsReasoning() || len(q.ReasoningEffortLevels()) != 0 {
		t.Fatalf("reasoning = false: %v %v", q.SupportsReasoning(), q.ReasoningEffortLevels())
	}
	assertTaskListEffortEnum(t, q, nil)
}

// TestProfileProviderOptionsByProtocol pins the protocol-keyed extras: the
// agent adds only what is not a capability the registry already carries.
func TestProfileProviderOptionsByProtocol(t *testing.T) {
	r := fixtureRegistry(t)
	openai := mustResolve(t, r, "openai/gpt-5.5")
	if opts, _ := openai.ProviderOptions()[registry.ProtocolOpenAIResponses].(map[string]any); opts["parallel_tool_calls"] != true {
		t.Fatalf("responses extras: %+v", openai.ProviderOptions())
	}
	google := mustResolve(t, r, "google/gemini-3-pro")
	if opts, _ := google.ProviderOptions()[registry.ProtocolGoogle].(map[string]any); opts["safetySettings"] == nil {
		t.Fatalf("google extras: %+v", google.ProviderOptions())
	}
	anthropic := mustResolve(t, r, "anthropic/claude-opus-5")
	if anthropic.ProviderOptions() != nil {
		t.Fatalf("anthropic sends no extras; caps carry max_tokens and betas: %+v", anthropic.ProviderOptions())
	}
}

// TestWithModelAndCrossProviderRef pins the model-switch contract: a
// namespaced id the instance serves stays on it, a re-resolve refreshes every
// cap, and the session resolver still owns real cross-instance switches.
func TestWithModelAndCrossProviderRef(t *testing.T) {
	r := fixtureRegistry(t)
	or := mustResolve(t, r, "openrouter/openai/gpt-5.5")
	if or.CrossProviderRef("anthropic/claude-opus-5") {
		t.Fatal("a namespaced id the instance serves stays on the instance")
	}
	if !or.CrossProviderRef("anthropic/model-nobody-has") {
		t.Fatal("an id the instance does not serve, under another instance's name, is cross-instance")
	}
	if or.CrossProviderRef("openrouter/anthropic/claude-opus-5") {
		t.Fatal("a redundant self-prefix is not cross-instance")
	}
	switched := or.WithModel("openrouter/anthropic/claude-opus-5")
	if switched.ID() != "openrouter" || switched.Model() != "anthropic/claude-opus-5" || switched.Surface() != registry.SurfaceAnthropic {
		t.Fatalf("self-prefix strip + re-resolve: %s/%s %s", switched.ID(), switched.Model(), switched.Surface())
	}
	ant := WithCheapModel(mustResolve(t, r, "anthropic/claude-opus-5"), "claude-haiku-4-5")
	next := ant.WithModel("claude-sonnet-4-5[1m]")
	if next.ContextWindowSize() != 1000000 || next.CheapModel() != "claude-haiku-4-5" || next.ID() != "anthropic" {
		t.Fatalf("WithModel re-resolves caps and keeps routing: %d %q %s", next.ContextWindowSize(), next.CheapModel(), next.ID())
	}
	kept := ant.WithModel("google/gemini-3-pro")
	if kept.ID() != "anthropic" || kept.Model() != "google/gemini-3-pro" {
		t.Fatalf("a cross-instance ref is the session resolver's job; WithModel keeps it verbatim: %s/%s", kept.ID(), kept.Model())
	}
	if same := ant.WithModel("  "); same.Model() != ant.Model() {
		t.Fatalf("an empty model keeps the active one: %q", same.Model())
	}
	// A window recorded for one model does not follow the session to the next
	// one: the new model's row does.
	pinned := withContextWindow(mustResolve(t, r, "anthropic/claude-opus-5"), 4096)
	switchedAway := pinned.WithModel("claude-sonnet-4-5")
	if switchedAway.ContextWindowSize() != mustResolve(t, r, "anthropic/claude-sonnet-4-5").ContextWindowSize() {
		t.Fatalf("WithModel carried the recorded window: %d", switchedAway.ContextWindowSize())
	}
}

// TestWithModelCarriesCommunicateOverrides pins that the re-resolve keeps the
// app layer's communicate schema: a model switch must not revert it.
func TestWithModelCarriesCommunicateOverrides(t *testing.T) {
	r := fixtureRegistry(t)
	base := WithAllowedDecisions(mustResolve(t, r, "anthropic/claude-opus-5"), []string{"ship", "hold"})
	want := findToolDef(base, "communicate")
	got := findToolDef(base.WithModel("claude-sonnet-4-5"), "communicate")
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if !bytes.Equal(gb, wb) {
		t.Fatalf("communicate schema lost across WithModel\n got=%s\nwant=%s", gb, wb)
	}
}

// TestWithResolvedReplacesFacts pins the live-listing seam: the profile is
// rebuilt from the fresh record, per-session overrides survive it.
func TestWithResolvedReplacesFacts(t *testing.T) {
	p := mustResolve(t, fixtureRegistry(t), "work/glm-5")
	res := p.Resolved()
	res.Caps.ContextWindow = new(272000)
	res.Caps.EffortValues = []string{"low", "high"}
	q := p.WithResolved(res)
	if q.ContextWindowSize() != 272000 || strings.Join(q.ReasoningEffortLevels(), ",") != "low,high" || p.ContextWindowSize() == 272000 {
		t.Fatalf("WithResolved clones with the new record: %d %v", q.ContextWindowSize(), q.ReasoningEffortLevels())
	}
	assertTaskListEffortEnum(t, q, []string{"low", "high"})
	cheap := WithCheapModel(p, "gw/tiny")
	kept := cheap.WithResolved(res)
	if kept.ContextWindowSize() != 272000 || kept.CheapModelRefString() != "gw/tiny" {
		t.Fatalf("routing survives WithResolved: %d %q", kept.ContextWindowSize(), kept.CheapModelRefString())
	}
	if (*Profile)(nil).WithResolved(res) != nil {
		t.Fatal("nil profile stays nil")
	}
}

// TestNewOpenAIProfileUsesTheEmbeddedRegistry pins the bare-client fixture:
// no registry, no credentials, one process-wide resolver.
func TestNewOpenAIProfileUsesTheEmbeddedRegistry(t *testing.T) {
	p := NewOpenAIProfile("gpt-5.5")
	if p.ID() != "openai" || p.Model() != "gpt-5.5" || p.Surface() != registry.SurfaceOpenAI || p.ContextWindowSize() == 0 {
		t.Fatalf("%s/%s %s %d", p.ID(), p.Model(), p.Surface(), p.ContextWindowSize())
	}
	first, second := EmbeddedRegistry(), EmbeddedRegistry()
	if first != second {
		t.Fatal("one embedded registry per process")
	}
	if q := FromResolved(p.Resolved(), nil); q.ID() != "openai" || q.WithModel("gpt-5.4").Model() != "gpt-5.4" {
		t.Fatalf("a nil registry falls back to the embedded one: %s/%s", q.ID(), q.Model())
	}
}

func TestJobControlCapabilityIncludesDelegateAndSend(t *testing.T) {
	defs := toolDefinitionsForCapabilities([]toolCapability{capabilityJobControl}, nil)
	have := map[string]bool{}
	for _, d := range defs {
		have[d.Name] = true
	}
	for _, name := range []string{"delegate", "job_watch", "delegate_send", "job_status", "job_list", "job_stop"} {
		if !have[name] {
			t.Errorf("capabilityJobControl missing %q", name)
		}
	}
}

func TestStandardProfilesAdvertiseJobControlWithoutLegacyAgentControl(t *testing.T) {
	profiles := map[string][]toolCapability{
		"openai":    openAICodexCapabilities,
		"anthropic": anthropicStyleCapabilities,
		"gemini":    geminiStyleCapabilities,
	}

	for profile, capabilities := range profiles {
		t.Run(profile, func(t *testing.T) {
			defs := toolDefinitionsForCapabilities(capabilities, nil)
			have := map[string]bool{}
			for _, d := range defs {
				have[d.Name] = true
			}

			for _, name := range []string{"delegate", "job_watch", "delegate_send", "job_status", "job_list", "job_stop"} {
				if !have[name] {
					t.Errorf("profile missing job-control tool %q", name)
				}
			}
			for _, legacy := range []string{"agent_control", "agent_list"} {
				if have[legacy] {
					t.Errorf("profile must NOT advertise legacy tool %q", legacy)
				}
			}
		})
	}
}

// TestContextWindowSource pins where the window a profile budgets against came
// from, as one value of the documented set, one exact value per case.
func TestContextWindowSource(t *testing.T) {
	size := 12345
	r := fixtureRegistryWith(t, map[string]registry.Provider{
		"pinned": {Base: "ollama", Models: map[string]registry.Model{"pinned-model": {Caps: registry.Caps{ContextWindow: &size}}}},
	})
	for _, tc := range []struct{ ref, want string }{
		{"ollama/qwen3:8b", "assumed"},
		{"pinned/pinned-model", "config"},
		{"anthropic/claude-opus-5", "snapshot"},
		// A cross-provider alias reports the source of its target's figure.
		{"anthropic/claude-mythos-5", "snapshot"},
	} {
		if got := mustResolve(t, r, tc.ref).ContextWindowSource(); got != tc.want {
			t.Errorf("%s: source = %q, want %q", tc.ref, got, tc.want)
		}
	}
	r.ApplyLive("ollama", []registry.Model{{ID: "qwen3:8b", Caps: registry.Caps{ContextWindow: new(8192)}}})
	if got := mustResolve(t, r, "ollama/qwen3:8b").ContextWindowSource(); got != "live" {
		t.Errorf("a window the live listing reported: source = %q, want live", got)
	}
}

// A window from the refreshed models.dev cache is catalog data like the
// embedded snapshot's.
func TestContextWindowSourceOfRefreshedCatalog(t *testing.T) {
	data, err := os.ReadFile("../../llm/registry/testdata/models.dev.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "catalog")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := fmt.Sprintf("{\"fetched_at\":%q,\"etag\":\"fixture\",\"source\":\"test\"}\n", time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(dir, "models.dev.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.dev.meta.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithNoUserLayer(), registry.WithStateRoot(root),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{"anthropic": {APIKey: "k"}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	p := mustResolve(t, r, "anthropic/claude-opus-5")
	if got := p.Resolved().Provenance["ContextWindow"]; !strings.HasPrefix(got, registry.LayerCache) {
		t.Fatalf("fixture must resolve its window from the cache layer, got %q", got)
	}
	if got := p.ContextWindowSource(); got != "snapshot" {
		t.Fatalf("source = %q, want snapshot", got)
	}
}
