package agent

// Phase 1b backstop: config-driven custom-instance lifecycle (PRI-1880).
//
// Covers a five-instance providers.toml fixture end-to-end:
//   work         openai/responses
//   work2        openai/responses
//   compat-x     openai/chat-completions + custom base_url
//   anthro-corp  anthropic          + custom base_url
//   kc           kimi
//
// Assertions (all must pass against current code with no production changes):
//  1. Routing by name     — NewFromProviders registers all five; each explicit
//                           req.Provider=<name> reaches its adapter.
//  2. Behavior by tag     — ResolveProfileFromConfig: each instance name maps to
//                           the correct behavior tag and routes by its own ID.
//  3. Real-openai boundary — compat-x (tag "openai-compatible") does NOT get the
//                            openai prompt section or 24h prompt-cache; work (tag
//                            "openai") does.
//  4. Per-instance OAuth  — SaveAuth("work", rec) writes auth/work.json; loading
//                           the work instance's OAuth reads it back independently
//                           of auth/openai.json.
//  5. SetModel override   — BuildResolveProfile resolver injected into SessionConfig;
//                           SetModel("work2/<model>") swaps to ID=="work2" and
//                           preserves a WithCommunicateOutputSchema override.
//  6. Resume              — s.Meta().ProfileID == "work" for a session on the
//                           "work" instance; SaveSessionMeta / LoadSessionMeta
//                           round-trip preserves the instance name.

import (
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/auth/openai"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

// ── Fixture ────────────────────────────────────────────────────────────────────

// phase1bCfg is the five-instance config used across all 1b subtests.
var phase1bCfg = providercfg.Config{
	Default: "work",
	Instances: []providercfg.InstanceConfig{
		{Name: "anthro-corp", Type: providercfg.Type("anthropic"), BaseURL: "https://anthropic.example.com", APIKey: "ant-key"},
		{Name: "compat-x", Type: providercfg.Type("openai"), APIStyle: providercfg.StyleChatCompletions, BaseURL: "https://compat.example.com/v1", APIKey: "compat-key"},
		{Name: "kc", Type: providercfg.Type("kimi"), APIKey: "kimi-key"},
		{Name: "work", Type: providercfg.Type("openai"), APIStyle: providercfg.StyleResponses, APIKey: "sk-work"},
		{Name: "work2", Type: providercfg.Type("openai"), APIStyle: providercfg.StyleResponses, APIKey: "sk-work2"},
	},
}

// buildPhase1bClient builds an llm.Client for the phase1bCfg fixture by
// registering one fakeAdapter per instance and calling SetNameToTag — exactly
// what llm.NewFromProviders does internally. This lets us test the routing and
// tag wiring without real network credentials.
func buildPhase1bClient() *llm.Client {
	c := llm.NewClient()
	for _, inst := range phase1bCfg.Instances {
		c.Register(&fakeAdapter{name: inst.Name})
	}
	c.SetDefaultProvider(phase1bCfg.Default)
	c.SetNameToTag(providercfg.NameToTag(phase1bCfg))
	return c
}

// ── Assertion 1: Routing by name ──────────────────────────────────────────────

// TestPhase1b_ClientRouting_AllFiveInstances verifies that a client built with
// the same wiring as NewFromProviders (register adapter per instance, SetNameToTag)
// lists all five instance names and routes each explicit req.Provider to the
// correct adapter.
func TestPhase1b_ClientRouting_AllFiveInstances(t *testing.T) {
	t.Parallel()
	c := buildPhase1bClient()

	// All five names must be present.
	names := c.ProviderNames()
	sort.Strings(names)
	want := []string{"anthro-corp", "compat-x", "kc", "work", "work2"}
	if len(names) != len(want) {
		t.Fatalf("ProviderNames() = %v (len %d), want %v (len %d)", names, len(names), want, len(want))
	}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("ProviderNames()[%d] = %q, want %q", i, names[i], w)
		}
	}

	// Each name must route to its own adapter (verified via resp.Provider).
	for _, name := range want {
		resp, err := c.Complete(t.Context(), llm.Request{
			Provider: name,
			Model:    "test-model",
			Messages: []llm.Message{llm.User("ping")},
		})
		if err != nil {
			t.Errorf("Complete(provider=%q): %v", name, err)
			continue
		}
		if resp.Provider != name {
			t.Errorf("routing to %q: resp.Provider = %q, want %q", name, resp.Provider, name)
		}
	}
}

// ── Assertion 2: Behavior by tag ──────────────────────────────────────────────

// TestPhase1b_ResolveProfileFromConfig_BehaviorTags verifies that each instance
// in the fixture resolves to the correct behavior tag and its own instance name
// as ID.
func TestPhase1b_ResolveProfileFromConfig_BehaviorTags(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ref       string
		wantID    string
		wantTag   string
		wantModel string
	}{
		{"work/gpt-5.2", "work", "openai", "gpt-5.2"},
		{"work2/gpt-5.4", "work2", "openai", "gpt-5.4"},
		{"compat-x/gpt-4o", "compat-x", "openai-compatible", "gpt-4o"},
		{"anthro-corp/claude-opus-4-6", "anthro-corp", "anthropic", "claude-opus-4-6"},
		{"kc/kimi-k2", "kc", "kimi", "kimi-k2"},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			p, err := ResolveProfileFromConfig(phase1bCfg, tc.ref)
			if err != nil {
				t.Fatalf("ResolveProfileFromConfig(%q): %v", tc.ref, err)
			}
			if got := p.ID(); got != tc.wantID {
				t.Errorf("ID() = %q, want %q", got, tc.wantID)
			}
			if got := p.BehaviorTag(); got != tc.wantTag {
				t.Errorf("BehaviorTag() = %q, want %q", got, tc.wantTag)
			}
			if got := p.Model(); got != tc.wantModel {
				t.Errorf("Model() = %q, want %q", got, tc.wantModel)
			}
		})
	}
}

// TestPhase1b_NameToTag_AllFive verifies that providercfg.NameToTag maps each
// instance to the right behavior tag — which is what NewFromProviders passes to
// c.SetNameToTag for error stamping and BehaviorTagOf lookups.
func TestPhase1b_NameToTag_AllFive(t *testing.T) {
	t.Parallel()
	m := providercfg.NameToTag(phase1bCfg)
	want := map[string]string{
		"work":        "openai",
		"work2":       "openai",
		"compat-x":    "openai-compatible",
		"anthro-corp": "anthropic",
		"kc":          "kimi",
	}
	for name, wantTag := range want {
		if got := m[name]; got != wantTag {
			t.Errorf("NameToTag[%q] = %q, want %q", name, got, wantTag)
		}
	}
	if len(m) != len(want) {
		t.Errorf("NameToTag len = %d, want %d; map = %v", len(m), len(want), m)
	}
}

// ── Assertion 3: Real-openai boundary ─────────────────────────────────────────

// TestPhase1b_CompatX_NoOpenAIBehavior verifies cohesively that compat-x
// (tag "openai-compatible") does NOT get the openai prompt section or 24h cache,
// while work (tag "openai") does.
func TestPhase1b_CompatX_NoOpenAIBehavior(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "work"})
	c.Register(&fakeAdapter{name: "compat-x"})

	workProfile, err := ResolveProfileFromConfig(phase1bCfg, "work/gpt-5.2")
	if err != nil {
		t.Fatalf("ResolveProfileFromConfig(work): %v", err)
	}
	compatProfile, err := ResolveProfileFromConfig(phase1bCfg, "compat-x/gpt-4o")
	if err != nil {
		t.Fatalf("ResolveProfileFromConfig(compat-x): %v", err)
	}

	// Pre-conditions: tags must be as expected.
	if got := workProfile.BehaviorTag(); got != "openai" {
		t.Fatalf("workProfile.BehaviorTag() = %q, want openai", got)
	}
	if got := compatProfile.BehaviorTag(); got != "openai-compatible" {
		t.Fatalf("compatProfile.BehaviorTag() = %q, want openai-compatible", got)
	}

	const openAIMarker = "they execute in the order you"

	// ── work instance gets openai behavior ──
	workSess, err := NewSession(c, workProfile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
	})
	if err != nil {
		t.Fatalf("NewSession(work): %v", err)
	}
	defer workSess.Close()

	workPrompt := workSess.renderSystemPrompt()
	if !strings.Contains(workPrompt, openAIMarker) {
		t.Errorf("work session (tag=openai): system prompt missing openai section marker %q", openAIMarker)
	}

	workReq := llm.Request{Model: "gpt-5.2", Provider: workProfile.ID()}
	workSess.applyModelRequestMetadata(workSess.profile, &workReq)
	if strings.TrimSpace(workReq.PromptCacheKey) == "" {
		t.Error("work session (tag=openai): PromptCacheKey empty — must be prompt-cache eligible")
	}
	if workReq.PromptCacheRetention != "24h" {
		t.Errorf("work session (tag=openai): PromptCacheRetention = %q, want 24h", workReq.PromptCacheRetention)
	}

	// ── compat-x instance does NOT get openai behavior ──
	compatSess, err := NewSession(c, compatProfile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
	})
	if err != nil {
		t.Fatalf("NewSession(compat-x): %v", err)
	}
	defer compatSess.Close()

	compatPrompt := compatSess.renderSystemPrompt()
	if strings.Contains(compatPrompt, openAIMarker) {
		t.Errorf("compat-x session (tag=openai-compatible): system prompt must NOT contain openai section marker %q", openAIMarker)
	}

	compatReq := llm.Request{Model: "gpt-4o", Provider: compatProfile.ID()}
	compatSess.applyModelRequestMetadata(compatSess.profile, &compatReq)
	if got := strings.TrimSpace(compatReq.PromptCacheKey); got != "" {
		t.Errorf("compat-x session (tag=openai-compatible): PromptCacheKey = %q, want empty", got)
	}
	if compatReq.PromptCacheRetention != "" {
		t.Errorf("compat-x session (tag=openai-compatible): PromptCacheRetention = %q, want empty", compatReq.PromptCacheRetention)
	}
}

// ── Assertion 4: Per-instance OAuth round-trip ────────────────────────────────

// TestPhase1b_PerInstanceOAuth_RoundTrip verifies that SaveAuth("work", rec)
// writes auth/work.json and can be loaded back independently of auth/openai.json.
func TestPhase1b_PerInstanceOAuth_RoundTrip(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()

	workRec := openai.AuthRecord{
		Version:      1,
		Provider:     "openai",
		Source:       "api-key",
		ObtainedAt:   time.Now().UTC().Round(time.Second),
		TokenType:    "Bearer",
		Scope:        "all",
		AccessToken:  "sk-work-token",
		RefreshToken: "rk-work-refresh",
		Expiry:       time.Now().Add(24 * time.Hour).UTC().Round(time.Second),
	}

	// Save as "work" instance.
	if err := openai.SaveAuth(stateDir, "work", workRec); err != nil {
		t.Fatalf("SaveAuth(work): %v", err)
	}

	// auth/work.json must exist.
	workPath := openai.AuthFilePath(stateDir, "work")
	if workPath == "" {
		t.Fatal("AuthFilePath(work) returned empty path")
	}

	// Load back as "work".
	loaded, err := openai.LoadAuth(stateDir, "work")
	if err != nil {
		t.Fatalf("LoadAuth(work): %v", err)
	}
	if loaded.AccessToken != workRec.AccessToken {
		t.Errorf("loaded.AccessToken = %q, want %q", loaded.AccessToken, workRec.AccessToken)
	}

	// auth/openai.json must NOT exist — "work" is a separate instance.
	_, err = openai.LoadAuth(stateDir, "openai")
	if err == nil {
		t.Error("LoadAuth(openai) succeeded — must not exist; work and openai must be independent auth stores")
	}
	if err != nil && !isAuthNotFound(err) {
		t.Errorf("LoadAuth(openai) returned unexpected error %v (want ErrAuthNotFound)", err)
	}
}

// isAuthNotFound reports whether err wraps openai.ErrAuthNotFound.
func isAuthNotFound(err error) bool {
	return errors.Is(err, openai.ErrAuthNotFound)
}

// ── Assertion 5: SetModel override preservation ───────────────────────────────

// TestPhase1b_SetModel_Work2_PreservesOutputSchema verifies that with the config
// resolver injected, SetModel("work2/<model>") from a "work" session swaps to
// ID()=="work2" while preserving a WithCommunicateOutputSchema override.
func TestPhase1b_SetModel_Work2_PreservesOutputSchema(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "work"})
	c.Register(&fakeAdapter{name: "work2"})

	workProfile, err := ResolveProfileFromConfig(phase1bCfg, "work/gpt-5.2")
	if err != nil {
		t.Fatalf("ResolveProfileFromConfig(work): %v", err)
	}

	customSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"my_field": map[string]any{"type": "string"},
		},
	}
	startProfile := WithCommunicateOutputSchema(workProfile, customSchema)

	// Build the resolver from the config (mirrors how cmdutil.BuildResolveProfile works).
	resolver := func(ref string) (*provider.Profile, error) {
		return ResolveProfileFromConfig(phase1bCfg, ref)
	}

	sess, err := NewSession(c, startProfile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
		ResolveProfile:   resolver,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Verify initial state.
	if got := sess.profile.ID(); got != "work" {
		t.Fatalf("initial profile ID = %q, want work", got)
	}
	if got := sess.profile.BehaviorTag(); got != "openai" {
		t.Fatalf("initial BehaviorTag = %q, want openai", got)
	}

	// Switch to work2 via resolver.
	sess.SetModel("work2/gpt-5.4")

	if got := sess.profile.ID(); got != "work2" {
		t.Fatalf("after SetModel, profile ID = %q, want work2", got)
	}
	if got := sess.profile.Model(); got != "gpt-5.4" {
		t.Fatalf("after SetModel, model = %q, want gpt-5.4", got)
	}
	if got := sess.profile.BehaviorTag(); got != "openai" {
		t.Fatalf("after SetModel, BehaviorTag = %q, want openai (work2 is also openai/responses)", got)
	}

	// The communicate-output-schema override must have been preserved.
	var communicateDef *llm.ToolDefinition
	for _, td := range sess.profile.ToolDefinitions() {
		if td.Name == "communicate" {
			td := td
			communicateDef = &td
			break
		}
	}
	if communicateDef == nil {
		t.Fatal("communicate tool not found after cross-instance SetModel to work2")
	}
	props, _ := communicateDef.Parameters["properties"].(map[string]any)
	output, _ := props["output"].(map[string]any)
	outProps, _ := output["properties"].(map[string]any)
	if _, ok := outProps["my_field"]; !ok {
		t.Error("communicate.output.properties missing my_field — custom schema was not preserved across SetModel(work2)")
	}
}

// ── Assertion 6: Resume via SessionMeta.ProfileID ─────────────────────────────

// TestPhase1b_Resume_ProfileIDPreserved verifies that a session on the "work"
// instance persists ProfileID=="work" in SessionMeta, and that
// SaveSessionMeta/LoadSessionMeta round-trips the instance name correctly
// (so the hub can reconstruct the work/<model> ref on resume).
func TestPhase1b_Resume_ProfileIDPreserved(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "work"})

	workProfile, err := ResolveProfileFromConfig(phase1bCfg, "work/gpt-5.2")
	if err != nil {
		t.Fatalf("ResolveProfileFromConfig(work): %v", err)
	}

	sess, err := NewSession(c, workProfile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Meta() must carry the instance name as ProfileID.
	meta := sess.Meta()
	if meta.ProfileID != "work" {
		t.Fatalf("Meta().ProfileID = %q, want work", meta.ProfileID)
	}
	if meta.Model != "gpt-5.2" {
		t.Errorf("Meta().Model = %q, want gpt-5.2", meta.Model)
	}

	// SaveSessionMeta / LoadSessionMeta must round-trip ProfileID.
	if err := schema.SaveSessionMeta(dir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	loaded, err := schema.LoadSessionMeta(dir, meta.ID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if loaded.ProfileID != "work" {
		t.Errorf("loaded ProfileID = %q, want work — instance name must survive persistence round-trip", loaded.ProfileID)
	}

	// The loaded ProfileID can be used to reconstruct the work/<model> ref,
	// confirming that ResolveProfileFromConfig can reconstitute the profile.
	reconstructRef := loaded.ProfileID + "/" + loaded.Model
	reconstructed, err := ResolveProfileFromConfig(phase1bCfg, reconstructRef)
	if err != nil {
		t.Fatalf("ResolveProfileFromConfig(%q) from resumed meta: %v", reconstructRef, err)
	}
	if reconstructed.ID() != "work" {
		t.Errorf("reconstructed profile ID = %q, want work", reconstructed.ID())
	}
	if reconstructed.BehaviorTag() != "openai" {
		t.Errorf("reconstructed BehaviorTag = %q, want openai", reconstructed.BehaviorTag())
	}
}
