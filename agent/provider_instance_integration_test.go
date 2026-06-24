package agent

// Backstop integration test for the renamed-instance lifecycle (PRI-1880).
//
// Proves end-to-end at the session level that a provider instance whose NAME
// differs from its TYPE:
//   - identifies by NAME (ID(), req.Provider stamped by llm.Client)
//   - behaves by TAG (prompt-cache eligibility, system-prompt section, tool wiring)
//
// Coverage map:
//  1. Identity by name — WithProviderID(openai, "work") → ID="work", tag="openai";
//     session turn completes; error event reports provider "work".
//  2. Behavior by tag — same "work" instance gets the openai prompt section and
//     24h prompt-cache eligibility via renderSystemPrompt / applyModelRequestMetadata.
//  3. "any real openai" boundary — openai-compatible tag does NOT get the openai
//     section or prompt-cache eligibility (already tested individually; verified
//     cohesively here in a single subtest).
//  4. Cross-instance switch — resolver maps "work2/<model>" to a renamed openai
//     profile; SetModel("work2/gpt-5.2") swaps the profile, preserves a
//     WithCommunicateOutputSchema override, and keeps identity "work2".
//  5. Provider-conditional tool on switch — SetModel to a google-tag instance
//     wires real web_search; SetModel away removes it.

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm"
)

// ── Shared resolver for cross-instance switch tests ───────────────────────────

// instanceTestResolver maps "work/<model>" → renamed openai "work",
// "work2/<model>" → renamed openai "work2", "google/<model>" → gemini "google".
// Unknown prefixes return (nil, nil), falling through to WithModel.
func instanceTestResolver(ref string) (*provider.Profile, error) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 {
		return nil, nil
	}
	provider := strings.ToLower(parts[0])
	model := parts[1]
	switch provider {
	case "work":
		return WithProviderID(NewOpenAIProfile(model), "work"), nil
	case "work2":
		return WithProviderID(NewOpenAIProfile(model), "work2"), nil
	case "google", "gemini":
		return newGeminiProfile(model), nil
	case "openai":
		return NewOpenAIProfile(model), nil
	}
	return nil, nil
}

// ── Subtest 1+2: Identity by name AND behavior by tag ─────────────────────────

// TestProviderInstance_RenamedOpenAI_IdentityAndBehavior drives a complete
// session turn with a "work" (renamed openai) profile and asserts:
//  1. ID()=="work", BehaviorTag()=="openai" (identity fields).
//  2. ProcessInput returns the fake response (session is functional).
//  3. The error-path event reports provider "work" (llm.Client stamping).
//  4. renderSystemPrompt contains the openai section (behavior by tag).
//  5. applyModelRequestMetadata sets 24h prompt-cache (behavior by tag).
func TestProviderInstance_RenamedOpenAI_IdentityAndBehavior(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	// The fake adapter must be registered under the instance NAME ("work")
	// because that is what req.Provider is set to by the session main loop.
	f := &fakeAdapter{
		name: "work",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("hello from work") },
		},
	}
	c.Register(f)

	renamedProfile := WithProviderID(NewOpenAIProfile("gpt-5.2"), "work")

	// ── Pre-condition assertions (unit-level; fail fast before driving a session) ──
	if got := renamedProfile.ID(); got != "work" {
		t.Fatalf("ID() = %q, want work", got)
	}
	if got := renamedProfile.BehaviorTag(); got != "openai" {
		t.Fatalf("BehaviorTag() = %q, want openai", got)
	}

	sess, err := NewSession(c, renamedProfile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// ── Assertion 1: session turn completes with the right response ──
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hello", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !strings.Contains(out, "hello from work") {
		t.Fatalf("output = %q, want it to contain 'hello from work'", out)
	}

	// ── Assertion 2: the request was sent with Provider=="work" ──
	reqs := f.Requests()
	if len(reqs) == 0 {
		t.Fatal("no LLM requests recorded")
	}
	if got := reqs[0].Provider; got != "work" {
		t.Fatalf("req.Provider = %q, want work (llm.Client must stamp the instance name)", got)
	}

	// ── Assertion 3: behavior by tag — openai prompt section in system prompt ──
	const openAIMarker = "they execute in the order you"
	prompt := sess.renderSystemPrompt()
	if !strings.Contains(prompt, openAIMarker) {
		t.Fatalf("system prompt missing openai section marker %q — renamed openai instance must get openai-tagged behavior", openAIMarker)
	}

	// ── Assertion 4: behavior by tag — 24h prompt-cache eligibility ──
	req := llm.Request{
		Model:    renamedProfile.Model(),
		Provider: renamedProfile.ID(), // "work"
	}
	sess.applyModelRequestMetadata(sess.profile, &req)
	if strings.TrimSpace(req.PromptCacheKey) == "" {
		t.Fatalf("PromptCacheKey empty — renamed openai instance must be prompt-cache eligible by tag")
	}
	if got, want := req.PromptCacheRetention, "24h"; got != want {
		t.Fatalf("PromptCacheRetention = %q, want %q", got, want)
	}
}

// ── Subtest 3: "any real openai" boundary ─────────────────────────────────────

// TestProviderInstance_OpenAICompatible_NoOpenAIBehavior verifies cohesively
// that a profile with tag "openai-compatible" does NOT get:
//   - the OpenAI prompt section in the system prompt
//   - 24h prompt-cache eligibility
func TestProviderInstance_OpenAICompatible_NoOpenAIBehavior(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai-compatible"})

	compatProfile := testOpenAICompatProfile("openai-compatible", "gpt-4o", 128_000)

	sess, err := NewSession(c, compatProfile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// System prompt must NOT contain the openai-only section.
	const openAIMarker = "they execute in the order you"
	prompt := sess.renderSystemPrompt()
	if strings.Contains(prompt, openAIMarker) {
		t.Fatalf("system prompt contains openai section marker %q — openai-compatible must NOT load the openai section", openAIMarker)
	}

	// Prompt-cache eligibility must be blocked by the tag.
	req := llm.Request{
		Model:    "gpt-4o",
		Provider: "openai", // even if provider says "openai", tag must override
	}
	sess.applyModelRequestMetadata(sess.profile, &req)
	if got := strings.TrimSpace(req.PromptCacheKey); got != "" {
		t.Fatalf("PromptCacheKey = %q, want empty — openai-compatible must NOT be prompt-cache eligible", got)
	}
	if got := req.PromptCacheRetention; got != "" {
		t.Fatalf("PromptCacheRetention = %q, want empty", got)
	}
}

// ── Subtest 4: Cross-instance switch ─────────────────────────────────────────

// TestProviderInstance_CrossInstanceSwitch_PreservesOverrideAndIdentity verifies
// that SetModel("work2/gpt-5.2") via instanceTestResolver:
//   - swaps the active profile to a "work2" openai instance
//   - preserves a WithCommunicateOutputSchema override applied before the switch
//   - keeps the instance identity ID()=="work2" after the switch
func TestProviderInstance_CrossInstanceSwitch_PreservesOverrideAndIdentity(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "work"})
	c.Register(&fakeAdapter{name: "work2"})

	customSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"my_field": map[string]any{"type": "string"},
		},
	}
	startProfile := WithCommunicateOutputSchema(
		WithProviderID(NewOpenAIProfile("gpt-5.4"), "work"),
		customSchema,
	)

	sess, err := NewSession(c, startProfile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
		ResolveProfile:   instanceTestResolver,
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

	// Switch to "work2" via resolver.
	sess.SetModel("work2/gpt-5.2")

	// Identity must now be "work2".
	if got := sess.profile.ID(); got != "work2" {
		t.Fatalf("after SetModel, profile ID = %q, want work2", got)
	}
	if got := sess.profile.Model(); got != "gpt-5.2" {
		t.Fatalf("after SetModel, model = %q, want gpt-5.2", got)
	}
	if got := sess.profile.BehaviorTag(); got != "openai" {
		t.Fatalf("after SetModel, BehaviorTag = %q, want openai", got)
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
		t.Fatal("communicate tool not found in switched profile")
	}
	props, _ := communicateDef.Parameters["properties"].(map[string]any)
	output, _ := props["output"].(map[string]any)
	outProps, _ := output["properties"].(map[string]any)
	if _, ok := outProps["my_field"]; !ok {
		t.Errorf("after cross-instance SetModel, communicate.output.properties is missing my_field — custom schema was not preserved")
	}
}

// ── Subtest 5: Provider-conditional tool on switch ────────────────────────────

// TestProviderInstance_ProviderConditionalTool_GoogleSwitchWiresWebSearch verifies
// that switching from a renamed openai instance ("work") to a google instance
// wires the real web_search function tool, and switching away removes it.
func TestProviderInstance_ProviderConditionalTool_GoogleSwitchWiresWebSearch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "work"})
	c.Register(&fakeAdapter{name: "google"})

	startProfile := WithProviderID(NewOpenAIProfile("gpt-5.4"), "work")

	sess, err := NewSession(c, startProfile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
		ResolveProfile:   instanceTestResolver,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Pre-condition: renamed openai instance must NOT have real web_search.
	if webSearchExecIsReal(t, sess.reg) {
		t.Fatal("pre-condition: renamed openai session must not have real web_search function tool")
	}

	// Switch to google via resolver.
	sess.SetModel("google/gemini-2.5-pro")

	if got := sess.profile.BehaviorTag(); got != "google" {
		t.Fatalf("after SetModel, BehaviorTag = %q, want google", got)
	}
	if !webSearchExecIsReal(t, sess.reg) {
		t.Fatal("after switching to google profile, web_search function tool must be real (not placeholder)")
	}

	// Switch back to a renamed openai instance — web_search must be removed.
	sess.SetModel("work2/gpt-5.2")

	if got := sess.profile.ID(); got != "work2" {
		t.Fatalf("after second SetModel, profile ID = %q, want work2", got)
	}
	if webSearchExecIsReal(t, sess.reg) {
		t.Fatal("after switching back to openai, web_search function tool must not be real (openai uses native web search)")
	}
}
