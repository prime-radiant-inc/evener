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
//  1. Routing by name     — a client registered with all five names routes
//                           each explicit req.Provider=<name> to its adapter.
//  2. Behavior by surface — each instance name resolves to the right
//                           surface/provider-id pair and routes by its own ID.
//  3. Real-openai boundary — compat-x (surface "generic") does NOT get the
//                            openai prompt section; work (surface "openai")
//                            does. Prompt-cache eligibility is no longer part
//                            of this boundary: the session stamps both fields
//                            and the resolved row's Fields decide (spec §7.5,
//                            session_openai_prompt_cache_test.go).
//  4. Per-instance OAuth  — SaveAuth("work", rec) writes auth/work.json; loading
//                           the work instance's OAuth reads it back independently
//                           of auth/openai.json.
//  5. SetModel override   — a registry resolver injected into SessionConfig;
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

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// ── Fixture ────────────────────────────────────────────────────────────────────

// phase1bInstances is the five-instance fixture: each name based on the
// curated provider it inherits from.
func phase1bInstances() map[string]registry.Provider {
	return map[string]registry.Provider{
		"anthro-corp": {Base: "anthropic", APIKey: "ant-key", Transport: registry.Transport{BaseURL: "https://anthropic.example.com"}},
		"compat-x":    {Base: "openai-compatible", APIKey: "compat-key", Transport: registry.Transport{BaseURL: "https://compat.example.com/v1"}},
		"kc":          {Base: "moonshotai", APIKey: "kimi-key"},
		"work":        {Base: "openai", APIKey: "sk-work"},
		"work2":       {Base: "openai", APIKey: "sk-work2"},
	}
}

// resolvePhase1bProfile resolves a reference on the phase-1b registry.
func resolvePhase1bProfile(ref string) (*provider.Profile, error) {
	return provider.Resolve(mustTestRegistry(phase1bInstances()), ref)
}

// buildPhase1bClient builds an llm.Client over the fixture by registering one
// fakeAdapter per instance name, so routing is testable without credentials
// or a network.
func buildPhase1bClient() *llm.Client {
	c := llm.NewClient()
	for name := range phase1bInstances() {
		c.Register(&fakeAdapter{name: name})
	}
	c.SetDefaultProvider("work")
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

// ── Assertion 2: Behavior by surface ─────────────────────────────────────────

// TestPhase1b_ResolveProfile_RegistryKeys verifies that each instance in the
// fixture resolves to the right surface and provider id while keeping its own
// name as ID.
func TestPhase1b_ResolveProfile_RegistryKeys(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ref                                 string
		wantID, wantProviderID, wantSurface string
		wantModel                           string
	}{
		{"work/gpt-5.2", "work", "openai", registry.SurfaceOpenAI, "gpt-5.2"},
		{"work2/gpt-5.4", "work2", "openai", registry.SurfaceOpenAI, "gpt-5.4"},
		{"compat-x/gpt-4o", "compat-x", "openai-compatible", registry.SurfaceGeneric, "gpt-4o"},
		{"anthro-corp/claude-opus-4-6", "anthro-corp", "anthropic", registry.SurfaceAnthropic, "claude-opus-4-6"},
		{"kc/kimi-k2", "kc", "moonshotai", registry.SurfaceGeneric, "kimi-k2"},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			p, err := resolvePhase1bProfile(tc.ref)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.ref, err)
			}
			if got := p.ID(); got != tc.wantID {
				t.Errorf("ID() = %q, want %q", got, tc.wantID)
			}
			if got := p.ProviderID(); got != tc.wantProviderID {
				t.Errorf("ProviderID() = %q, want %q", got, tc.wantProviderID)
			}
			if got := p.Surface(); got != tc.wantSurface {
				t.Errorf("Surface() = %q, want %q", got, tc.wantSurface)
			}
			if got := p.Model(); got != tc.wantModel {
				t.Errorf("Model() = %q, want %q", got, tc.wantModel)
			}
		})
	}
}

// ── Assertion 3: Real-openai boundary ─────────────────────────────────────────

// TestPhase1b_CompatX_NoOpenAIBehavior verifies cohesively that compat-x
// (surface "generic") does NOT get the openai prompt section while work
// (surface "openai") does, and that the prompt-cache fields are no longer part
// of that boundary: the session stamps them for every instance and
// llm.ShapeRequest drops what the resolved row cannot send.
func TestPhase1b_CompatX_NoOpenAIBehavior(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "work"})
	c.Register(&fakeAdapter{name: "compat-x"})

	workProfile, err := resolvePhase1bProfile("work/gpt-5.2")
	if err != nil {
		t.Fatalf("Resolve(work): %v", err)
	}
	compatProfile, err := resolvePhase1bProfile("compat-x/gpt-4o")
	if err != nil {
		t.Fatalf("Resolve(compat-x): %v", err)
	}

	// Pre-conditions: surfaces must be as expected.
	if got := workProfile.Surface(); got != registry.SurfaceOpenAI {
		t.Fatalf("workProfile.Surface() = %q, want openai", got)
	}
	if got := compatProfile.Surface(); got != registry.SurfaceGeneric {
		t.Fatalf("compatProfile.Surface() = %q, want generic", got)
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

	workPrompt, _ := workSess.renderSystemPrompt(workSess.env)
	if !strings.Contains(workPrompt, openAIMarker) {
		t.Errorf("work session (surface=openai): system prompt missing openai section marker %q", openAIMarker)
	}

	workReq := llm.Request{Model: "gpt-5.2", Provider: workProfile.ID()}
	workSess.applyModelRequestMetadata(&workReq)
	if strings.TrimSpace(workReq.PromptCacheKey) == "" {
		t.Error("work session: PromptCacheKey empty — the session stamps it for every instance")
	}
	if workReq.PromptCacheRetention != "24h" {
		t.Errorf("work session: PromptCacheRetention = %q, want 24h", workReq.PromptCacheRetention)
	}

	// ── compat-x instance does NOT get openai behavior ──
	compatSess, err := NewSession(c, compatProfile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
	})
	if err != nil {
		t.Fatalf("NewSession(compat-x): %v", err)
	}
	defer compatSess.Close()

	compatPrompt, _ := compatSess.renderSystemPrompt(compatSess.env)
	if strings.Contains(compatPrompt, openAIMarker) {
		t.Errorf("compat-x session (surface=generic): system prompt must NOT contain openai section marker %q", openAIMarker)
	}

	// The prompt-cache fields are stamped here for compat-x too; what the
	// endpoint may carry is the row's decision at dispatch, not the profile's.
	compatReq := llm.Request{Model: "gpt-4o", Provider: compatProfile.ID()}
	compatSess.applyModelRequestMetadata(&compatReq)
	if strings.TrimSpace(compatReq.PromptCacheKey) == "" || compatReq.PromptCacheRetention != "24h" {
		t.Errorf("compat-x session: prompt-cache fields = key %q retention %q, want both stamped", compatReq.PromptCacheKey, compatReq.PromptCacheRetention)
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

	workProfile, err := resolvePhase1bProfile("work/gpt-5.2")
	if err != nil {
		t.Fatalf("Resolve(work): %v", err)
	}

	customSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"my_field": map[string]any{"type": "string"},
		},
	}
	startProfile := WithCommunicateOutputSchema(workProfile, customSchema)

	// The resolver is the registry resolution itself (mirrors
	// cmdutil.BuildResolveProfile).
	resolver := resolvePhase1bProfile

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
	if got := sess.profile.ProviderID(); got != "openai" {
		t.Fatalf("initial ProviderID = %q, want openai", got)
	}

	// Switch to work2 via resolver.
	sess.SetModel("work2/gpt-5.4")

	if got := sess.profile.ID(); got != "work2" {
		t.Fatalf("after SetModel, profile ID = %q, want work2", got)
	}
	if got := sess.profile.Model(); got != "gpt-5.4" {
		t.Fatalf("after SetModel, model = %q, want gpt-5.4", got)
	}
	if got := sess.profile.ProviderID(); got != "openai" {
		t.Fatalf("after SetModel, ProviderID = %q, want openai (work2 is also an openai instance)", got)
	}

	// The communicate-output-schema override must have been preserved.
	var communicateDef *llm.ToolDefinition
	for _, td := range sess.profile.ToolDefinitions() {
		if td.Name == "communicate" {
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

	workProfile, err := resolvePhase1bProfile("work/gpt-5.2")
	if err != nil {
		t.Fatalf("Resolve(work): %v", err)
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
	// confirming that the registry can reconstitute the profile.
	reconstructRef := loaded.ProfileID + "/" + loaded.Model
	reconstructed, err := resolvePhase1bProfile(reconstructRef)
	if err != nil {
		t.Fatalf("Resolve(%q) from resumed meta: %v", reconstructRef, err)
	}
	if reconstructed.ID() != "work" {
		t.Errorf("reconstructed profile ID = %q, want work", reconstructed.ID())
	}
	if reconstructed.ProviderID() != "openai" {
		t.Errorf("reconstructed ProviderID = %q, want openai", reconstructed.ProviderID())
	}
}
