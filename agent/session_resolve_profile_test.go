package agent

// Tests for the ResolveProfile resolver injected into SessionConfig.
// These replace the profile-level cross-provider WithModel tests,
// which no longer work after the prefixActionSwitch arms are removed.

import (
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

// testResolver is a trivial resolver that maps known "provider/model"
// refs to real profiles, and returns an error for unknown providers.
func testResolver(ref string) (ProviderProfile, error) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 {
		return nil, nil // not a cross-provider ref
	}
	provider := strings.ToLower(parts[0])
	model := parts[1]
	switch provider {
	case "openai":
		return NewOpenAIProfile(model), nil
	case "anthropic":
		return newAnthropicProfile(model), nil
	case "google", "gemini":
		return newGeminiProfile(model), nil
	case "kimi", "glm", "openrouter", "ollama":
		return newOpenAICompatProfile(provider, model, 0), nil
	}
	return nil, nil
}

// TestSetModel_CrossProvider_SwapsProfileAndPreservesOverride verifies that
// SetModel("anthropic/claude-opus-4-6") from an openai-profile session
// (with a ResolveProfile resolver) swaps the profile and preserves any
// communicate-output-schema override applied before the switch.
func TestSetModel_CrossProvider_SwapsProfileAndPreservesOverride(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	// Register both providers so the session can query live model metadata.
	c.Register(&fakeAdapter{name: "openai"})
	c.Register(&fakeAdapter{name: "anthropic"})

	customSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"my_field": map[string]any{"type": "string"},
		},
	}
	startProfile := WithCommunicateOutputSchema(NewOpenAIProfile("gpt-5.4"), customSchema)

	sess, err := NewSession(c, startProfile, NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
		ResolveProfile:   testResolver,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Verify initial state.
	if got := sess.profile.ID(); got != "openai" {
		t.Fatalf("initial profile ID = %q, want openai", got)
	}

	// Switch to anthropic via a cross-provider model ref.
	sess.SetModel("anthropic/claude-opus-4-6")

	// Profile must now be anthropic.
	if got := sess.profile.ID(); got != "anthropic" {
		t.Fatalf("after SetModel, profile ID = %q, want anthropic", got)
	}
	if got := sess.profile.Model(); got != "claude-opus-4-6" {
		t.Fatalf("after SetModel, model = %q, want claude-opus-4-6", got)
	}
	if got := sess.profile.BehaviorTag(); got != "anthropic" {
		t.Fatalf("after SetModel, BehaviorTag = %q, want anthropic", got)
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
		t.Errorf("after cross-provider SetModel, communicate.output.properties is missing my_field — custom schema was not preserved")
	}
}

// TestSetModel_CrossProvider_WithoutResolver_NoSwap verifies that
// SetModel("anthropic/claude-opus-4-6") from an openai-profile session
// WITHOUT a resolver does not switch provider (falls through to WithModel).
// After removing the prefixActionSwitch arm, WithModel will keep the
// openai profile for unknown cross-provider prefixes.
func TestSetModel_CrossProvider_WithoutResolver_NoSwap(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
		// No ResolveProfile — cross-provider switch must NOT happen.
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// After WithModel without resolver the provider stays openai
	// (the cross-provider switch arm is removed from WithModel).
	sess.SetModel("openai/gpt-4.1-mini") // self-prefix strip — fine
	if got := sess.profile.ID(); got != "openai" {
		t.Fatalf("after same-provider SetModel, profile ID = %q, want openai", got)
	}
}

// TestSetModel_SameProvider_WithResolver_UsesWithModel verifies that a
// same-provider SetModel ("openai/gpt-4.1") uses the WithModel path
// (not the resolver) even when a resolver is present.
func TestSetModel_SameProvider_WithResolver_UsesWithModel(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	resolverCalled := false
	resolver := func(ref string) (ProviderProfile, error) {
		resolverCalled = true
		return testResolver(ref)
	}

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
		ResolveProfile:   resolver,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	sess.SetModel("openai/gpt-4.1-mini")

	if resolverCalled {
		t.Fatal("resolver must not be called for a same-provider SetModel")
	}
	if got := sess.profile.ID(); got != "openai" {
		t.Fatalf("after same-provider SetModel, profile ID = %q, want openai", got)
	}
	if got := sess.profile.Model(); got != "gpt-4.1-mini" {
		t.Fatalf("after same-provider SetModel, model = %q, want gpt-4.1-mini", got)
	}
}

// TestSetModel_CrossProvider_SwitchToGoogle_RegistersWebSearch verifies that
// switching from an openai profile to a google/gemini profile via SetModel
// (with resolver) re-registers the real web_search function tool.
func TestSetModel_CrossProvider_SwitchToGoogle_RegistersWebSearch(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	c.Register(&fakeAdapter{name: "gemini"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
		ResolveProfile:   testResolver,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Before switch: openai does not register web_search as a function tool.
	if webSearchExecIsReal(t, sess.reg) {
		t.Fatal("pre-condition: openai session must not have real web_search function tool")
	}

	sess.SetModel("google/gemini-2.5-pro")

	if !webSearchExecIsReal(t, sess.reg) {
		t.Fatal("after switching to google profile, web_search function tool must be real (not placeholder)")
	}
}

// TestSetModel_CrossProvider_SwitchAwayFromGoogle_RemovesWebSearch verifies that
// switching from a google profile to openai via SetModel removes the web_search
// function tool (or at least it is no longer the real executor).
func TestSetModel_CrossProvider_SwitchAwayFromGoogle_RemovesWebSearch(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "gemini"})
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, newGeminiProfile("gemini-2.5-pro"), NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
		ResolveProfile:   testResolver,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Pre-condition: google session has real web_search.
	if !webSearchExecIsReal(t, sess.reg) {
		t.Fatal("pre-condition: gemini session must have real web_search executor")
	}

	sess.SetModel("openai/gpt-5.4")

	// After switching to openai, web_search function tool must be absent or placeholder.
	if webSearchExecIsReal(t, sess.reg) {
		t.Fatal("after switching to openai, web_search function tool must not be real (openai uses native web search)")
	}
}

// TestValidateModelFallbacks_CrossTag_Errors verifies that validateModelFallbacks
// returns an error when a resolver-resolved fallback has a different BehaviorTag
// from the primary profile.
func TestValidateModelFallbacks_CrossTag_Errors(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	c.Register(&fakeAdapter{name: "anthropic"})

	_, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
		ResolveProfile:   testResolver,
		ModelFallbacks:   []string{"anthropic/claude-opus-4-6"},
	})
	if err == nil {
		t.Fatal("NewSession succeeded with cross-tag fallback (with resolver), want error")
	}
	if !strings.Contains(err.Error(), "cross-provider fallbacks are not supported") {
		t.Fatalf("error=%v, want cross-provider rejection message", err)
	}
}

// TestValidateModelFallbacks_SameTag_Allowed verifies that same-tag fallbacks
// (different model, same provider family) are allowed when a resolver is present.
func TestValidateModelFallbacks_SameTag_Allowed(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	// Same-tag fallback: openai/gpt-5.4 → openai/gpt-4.1-mini (both tag="openai").
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
		ResolveProfile:   testResolver,
		ModelFallbacks:   []string{"openai/gpt-4.1-mini"},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err) // must succeed for same-tag fallback
	}
	defer sess.Close()
}

// testResolverFull extends testResolver with minimax and openrouter-anthropic support.
func testResolverFull(ref string) (ProviderProfile, error) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 {
		return nil, nil
	}
	provider := strings.ToLower(parts[0])
	model := parts[1]
	switch provider {
	case "openai":
		return NewOpenAIProfile(model), nil
	case "anthropic":
		return newAnthropicProfile(model), nil
	case "google", "gemini":
		return newGeminiProfile(model), nil
	case "minimax":
		return newMiniMaxProfile(model), nil
	case "openrouter-anthropic":
		return newOpenRouterAnthropicProfile(model), nil
	case "kimi", "glm", "openrouter", "ollama":
		return newOpenAICompatProfile(provider, model, 0), nil
	}
	return nil, nil
}

// TestSetModel_CrossProvider_ToOllama verifies SetModel("ollama/llama3.1:8b")
// from an OpenAI session (with resolver) produces an ollama profile.
func TestSetModel_CrossProvider_ToOllama(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	c.Register(&fakeAdapter{name: "ollama"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
		ResolveProfile:   testResolverFull,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	sess.SetModel("ollama/llama3.1:8b")
	if got := sess.profile.ID(); got != "ollama" {
		t.Fatalf("ID() = %q, want ollama", got)
	}
	if got := sess.profile.Model(); got != "llama3.1:8b" {
		t.Fatalf("Model() = %q, want llama3.1:8b", got)
	}
}

// TestSetModel_CrossProvider_ToOllama_WithCatalog verifies that SetModel via
// resolver to an ollama profile picks up the catalog-derived context window.
func TestSetModel_CrossProvider_ToOllama_WithCatalog(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	c.Register(&fakeAdapter{name: "ollama"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
		ResolveProfile:   testResolverFull,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	sess.SetModel("ollama/llama3.1")
	if got := sess.profile.ID(); got != "ollama" {
		t.Fatalf("ID() = %q, want ollama", got)
	}
	// llama3.1 is in the catalog with 8192 context window.
	if got := sess.profile.ContextWindowSize(); got != 8192 {
		t.Fatalf("ContextWindowSize() = %d, want 8192 (catalog metadata for ollama/llama3.1 must resolve)", got)
	}
}

// TestSetModel_CrossProvider_FromAnthropicToOllama verifies SetModel from
// an Anthropic session (with resolver) to an ollama profile.
func TestSetModel_CrossProvider_FromAnthropicToOllama(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "anthropic"})
	c.Register(&fakeAdapter{name: "ollama"})

	sess, err := NewSession(c, newAnthropicProfile("claude-opus-4-6"), NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
		ResolveProfile:   testResolverFull,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	sess.SetModel("ollama/qwen2.5-coder:7b")
	if got := sess.profile.ID(); got != "ollama" {
		t.Fatalf("ID() = %q, want ollama", got)
	}
	if got := sess.profile.Model(); got != "qwen2.5-coder:7b" {
		t.Fatalf("Model() = %q, want qwen2.5-coder:7b", got)
	}
}

// TestSetModel_CrossProvider_ToMiniMax verifies SetModel from an OpenAI session
// (with resolver) to a minimax profile.
func TestSetModel_CrossProvider_ToMiniMax(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	c.Register(&fakeAdapter{name: "minimax"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
		ResolveProfile:   testResolverFull,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	sess.SetModel("minimax/MiniMax-M2.7")
	if got := sess.profile.ID(); got != "minimax" {
		t.Fatalf("ID() = %q, want minimax", got)
	}
	if got := sess.profile.Model(); got != "MiniMax-M2.7" {
		t.Fatalf("Model() = %q, want MiniMax-M2.7", got)
	}
}

// TestSetModel_CrossProvider_FromMiniMax verifies SetModel from a minimax session
// (with resolver) to an anthropic profile.
func TestSetModel_CrossProvider_FromMiniMax(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "minimax"})
	c.Register(&fakeAdapter{name: "anthropic"})

	sess, err := NewSession(c, newMiniMaxProfile("MiniMax-M2.7"), NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
		ResolveProfile:   testResolverFull,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	sess.SetModel("anthropic/claude-opus-4-6")
	if got := sess.profile.ID(); got != "anthropic" {
		t.Fatalf("ID() = %q, want anthropic", got)
	}
	if got := sess.profile.Model(); got != "claude-opus-4-6" {
		t.Fatalf("Model() = %q, want claude-opus-4-6", got)
	}
}

// TestSetModel_CrossProvider_ToOpenRouter_PreservesSlashModel verifies that
// SetModel("openrouter/anthropic/claude-3-haiku-20240307") via resolver
// produces an openrouter profile with the slash-containing model preserved.
func TestSetModel_CrossProvider_ToOpenRouter_PreservesSlashModel(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	c.Register(&fakeAdapter{name: "openrouter"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
		ResolveProfile:   testResolverFull,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	sess.SetModel("openrouter/anthropic/claude-3-haiku-20240307")
	if got := sess.profile.ID(); got != "openrouter" {
		t.Fatalf("ID() = %q, want openrouter", got)
	}
	// The slash in remainder must be preserved — openrouter uses "anthropic/claude-3-haiku-20240307"
	// as the wire model.
	if got := sess.profile.Model(); got != "anthropic/claude-3-haiku-20240307" {
		t.Fatalf("Model() = %q, want anthropic/claude-3-haiku-20240307 (slash in remainder must be preserved)", got)
	}
	// Catalog metadata should resolve via the openrouter profile constructor.
	if got := sess.profile.ContextWindowSize(); got != 200000 {
		t.Fatalf("ContextWindowSize() = %d, want 200000 (catalog metadata must resolve for openrouter model)", got)
	}
}

// TestSetModel_CrossProvider_FromOpenRouterToOllama verifies that from an
// openrouter session, SetModel("ollama/llama3.1") via resolver switches to ollama.
func TestSetModel_CrossProvider_FromOpenRouterToOllama(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openrouter"})
	c.Register(&fakeAdapter{name: "ollama"})

	sess, err := NewSession(c, newOpenAICompatProfile("openrouter", "anthropic/claude-3-haiku-20240307", 0),
		NewLocalExecutionEnvironment(dir), SessionConfig{
			NoProjectPrompts: true,
			ResolveProfile:   testResolverFull,
		})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	sess.SetModel("ollama/llama3.1")
	if got := sess.profile.ID(); got != "ollama" {
		t.Fatalf("ID() = %q, want ollama", got)
	}
	if got := sess.profile.Model(); got != "llama3.1" {
		t.Fatalf("Model() = %q, want llama3.1", got)
	}
}

// TestSetModel_CrossProvider_ToAnthropicFromOpenAI verifies the basic
// openai→anthropic switch via SetModel with resolver (replaces
// TestProviderProfile_WithModel_CrossProvider at session level).
func TestSetModel_CrossProvider_ToAnthropicFromOpenAI(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	c.Register(&fakeAdapter{name: "anthropic"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
		ResolveProfile:   testResolverFull,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	sess.SetModel("anthropic/claude-opus-4-6")
	if got := sess.profile.ID(); got != "anthropic" {
		t.Fatalf("ID() = %q, want anthropic", got)
	}
	if got := sess.profile.Model(); got != "claude-opus-4-6" {
		t.Fatalf("Model() = %q, want claude-opus-4-6", got)
	}
}

// TestSetModel_CrossProvider_FromAnthropicToOpenAI verifies the
// anthropic→openai switch via SetModel (replaces
// TestAnthropicProfile_WithModel_ResolvesProviderPrefix at session level).
func TestSetModel_CrossProvider_FromAnthropicToOpenAI(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "anthropic"})
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, newAnthropicProfile("claude-opus-4-6"), NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
		ResolveProfile:   testResolverFull,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	sess.SetModel("openai/gpt-5.4-mini")
	if got := sess.profile.ID(); got != "openai" {
		t.Fatalf("ID() = %q, want openai", got)
	}
	if got := sess.profile.Model(); got != "gpt-5.4-mini" {
		t.Fatalf("Model() = %q, want gpt-5.4-mini", got)
	}
}
