package agent

// Tests that session.go provider-conditional behavior sites key on
// s.profile.BehaviorTag() rather than s.profile.ID() or req.Provider.
// This ensures renamed provider instances (WithProviderID) keep the right
// behavior, and that chat-completions instances (tag "openai-compatible")
// correctly do NOT get the real-openai behavior.

import (
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

// taggedProfile is a test-only ProviderProfile implementation that allows
// the behavior tag to differ from the profile ID, simulating a renamed
// instance or a chat-completions-style instance.
type taggedProfile struct {
	tinyProfile
	behaviorTag string
}

func (p taggedProfile) BehaviorTag() string { return p.behaviorTag }

// ── Site 1: applyModelRequestMetadata (prompt-cache) ──────────────────────

// TestBehaviorTag_PromptCache_RenamedOpenAI verifies that a renamed OpenAI
// instance (id="work", tag="openai") is still prompt-cache eligible.
// Before the fix, applyModelRequestMetadata keyed on req.Provider == "openai";
// a renamed instance has req.Provider = s.profile.ID() = "work", so the cache
// was never activated.
func TestBehaviorTag_PromptCache_RenamedOpenAI(t *testing.T) {
	// WithProviderID(NewOpenAIProfile("gpt-5.5"), "work") → id="work", tag="openai"
	renamedProfile := WithProviderID(NewOpenAIProfile("gpt-5.5"), "work")
	if renamedProfile.ID() != "work" {
		t.Fatalf("pre-condition: ID() = %q, want work", renamedProfile.ID())
	}
	if renamedProfile.BehaviorTag() != "openai" {
		t.Fatalf("pre-condition: BehaviorTag() = %q, want openai", renamedProfile.BehaviorTag())
	}

	sess := &Session{id: "sess-abc", profile: renamedProfile}
	req := llm.Request{
		Model:    "gpt-5.5",
		Provider: renamedProfile.ID(), // "work" — what the main loop sets
	}

	sess.applyModelRequestMetadata(sess.profile, &req)

	if strings.TrimSpace(req.PromptCacheKey) == "" {
		t.Fatalf("PromptCacheKey is empty — renamed openai instance must be prompt-cache eligible")
	}
	if got, want := req.PromptCacheRetention, "24h"; got != want {
		t.Fatalf("PromptCacheRetention = %q, want %q", got, want)
	}
}

// TestBehaviorTag_PromptCache_OpenAICompatible verifies that a chat-completions
// instance (tag="openai-compatible") does NOT get prompt-cache set.
func TestBehaviorTag_PromptCache_OpenAICompatible(t *testing.T) {
	compatProfile := taggedProfile{
		tinyProfile: tinyProfile{id: "openai", mod: "gpt-5.5"},
		behaviorTag: "openai-compatible",
	}
	if compatProfile.BehaviorTag() != "openai-compatible" {
		t.Fatalf("pre-condition: BehaviorTag() = %q, want openai-compatible", compatProfile.BehaviorTag())
	}

	sess := &Session{id: "sess-xyz", profile: compatProfile}
	req := llm.Request{
		// Use a cache-eligible model; eligibility must be blocked by the behavior
		// tag ("openai-compatible"), not by the model.
		Model:    "gpt-5.5",
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

// ── Site 2: registerCoreTools (gemini web_search) ─────────────────────────

// webSearchExecIsReal returns true if the web_search tool in reg has a real
// executor (the one registered by registerCoreTools for the google path) rather
// than the placeholder that newToolRegistry installs ("tool executor not wired").
// We distinguish them by calling the executor and checking whether the error
// text is the sentinel placeholder string. The real executor may panic on nil
// client/context; we recover from that (it still means it was the real path).
func webSearchExecIsReal(t *testing.T, reg *toolRegistry) (isReal bool) {
	t.Helper()
	tool := reg.Get("web_search")
	if tool == nil || tool.Exec == nil {
		return false
	}
	defer func() {
		if r := recover(); r != nil {
			// A panic means the real executor ran (e.g. nil client dereference);
			// the placeholder never panics — it just returns an error.
			isReal = true
		}
	}()
	_, err := tool.Exec(nil, nil, map[string]any{"query": "test"}) //nolint:staticcheck
	// Placeholder returns exactly "tool executor not wired".
	return err == nil || !strings.Contains(err.Error(), "tool executor not wired")
}

// TestBehaviorTag_Gemini_RenamedGoogleRegistersWebSearch verifies that a
// renamed Google instance (id="myai", tag="google") gets the real web_search
// executor wired by registerCoreTools.
// Before the fix, the check was s.profile.ID() == "gemini", so a renamed
// instance (id="myai") would retain only the placeholder executor.
func TestBehaviorTag_Gemini_RenamedGoogleRegistersWebSearch(t *testing.T) {
	// newGeminiProfile has id="gemini", tag="google".
	// WithProviderID renames the id to "myai" while preserving tag="google".
	renamedGemini := WithProviderID(newGeminiProfile("gemini-2.5-pro"), "myai")
	if renamedGemini.ID() != "myai" {
		t.Fatalf("pre-condition: ID() = %q, want myai", renamedGemini.ID())
	}
	if renamedGemini.BehaviorTag() != "google" {
		t.Fatalf("pre-condition: BehaviorTag() = %q, want google", renamedGemini.BehaviorTag())
	}

	dir := t.TempDir()
	sess := &Session{
		profile: renamedGemini,
		env:     NewLocalExecutionEnvironment(dir),
	}

	reg := newProfileToolRegistry(renamedGemini)
	if err := registerCoreTools(reg, sess); err != nil {
		t.Fatalf("registerCoreTools: %v", err)
	}

	if !webSearchExecIsReal(t, reg) {
		t.Fatalf("web_search executor is placeholder — renamed google instance must get real web_search executor")
	}
}

// TestBehaviorTag_Gemini_OriginalGeminiRegistersWebSearch verifies the
// existing baseline: an unmodified gemini profile (id="gemini", tag="google")
// still gets the real web_search executor.
func TestBehaviorTag_Gemini_OriginalGeminiRegistersWebSearch(t *testing.T) {
	geminiProfile := newGeminiProfile("gemini-2.5-pro")

	dir := t.TempDir()
	sess := &Session{
		profile: geminiProfile,
		env:     NewLocalExecutionEnvironment(dir),
	}

	reg := newProfileToolRegistry(geminiProfile)
	if err := registerCoreTools(reg, sess); err != nil {
		t.Fatalf("registerCoreTools: %v", err)
	}

	if !webSearchExecIsReal(t, reg) {
		t.Fatalf("web_search executor is placeholder for unmodified gemini profile — must have real executor")
	}
}

// TestBehaviorTag_Gemini_OpenAIDoesNotRegisterWebSearch verifies that
// registerCoreTools does NOT wire a real web_search executor for OpenAI
// (it uses native web search via req.WebSearch instead).
func TestBehaviorTag_Gemini_OpenAIDoesNotRegisterWebSearch(t *testing.T) {
	openaiProfile := NewOpenAIProfile("gpt-5.5")

	dir := t.TempDir()
	sess := &Session{
		profile: openaiProfile,
		env:     NewLocalExecutionEnvironment(dir),
	}

	// Use a fresh empty registry to isolate registerCoreTools behavior from
	// what newToolRegistry pre-populates from profile.ToolDefinitions().
	emptyReg := newToolRegistry()
	if err := registerCoreTools(emptyReg, sess); err != nil {
		t.Fatalf("registerCoreTools (empty reg): %v", err)
	}
	if tool := emptyReg.Get("web_search"); tool != nil {
		t.Fatalf("web_search registered by registerCoreTools for openai — must use native web search instead")
	}
}

// ── Site 3: renderSystemPrompt sectionResolver provider ───────────────────

// TestBehaviorTag_SectionResolver_RenamedOpenAILoadsOpenAISection verifies
// that a session with a renamed OpenAI profile (id="work", tag="openai")
// renders the tools.provider-openai_append.md section in the system prompt.
// Before the fix, sectionResolver.provider = s.profile.ID() = "work", so no
// openai-specific section would be loaded.
func TestBehaviorTag_SectionResolver_RenamedOpenAILoadsOpenAISection(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "work", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("ok") },
	}}
	c.Register(f)

	// Renamed OpenAI instance: id="work", tag="openai".
	renamedProfile := WithProviderID(NewOpenAIProfile("gpt-5.5"), "work")
	sess, err := NewSession(c, renamedProfile, NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// The embedded tools.provider-openai_append.md contains the text
	// "they execute in the order you" — present only when provider="openai".
	const openAIMarker = "they execute in the order you"
	prompt := sess.renderSystemPrompt()
	if !strings.Contains(prompt, openAIMarker) {
		t.Fatalf("system prompt missing openai section marker %q — SectionResolver provider must be %q (behaviorTag), not %q (ID)",
			openAIMarker, renamedProfile.BehaviorTag(), renamedProfile.ID())
	}
}

// TestBehaviorTag_SectionResolver_OpenAICompatibleDoesNotLoadOpenAISection
// verifies that a chat-completions instance (tag="openai-compatible") does
// NOT render the tools.provider-openai_append.md section.
func TestBehaviorTag_SectionResolver_OpenAICompatibleDoesNotLoadOpenAISection(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai-compatible", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("ok") },
	}}
	c.Register(f)

	compatProfile := taggedProfile{
		tinyProfile: tinyProfile{
			id:  "openai-compatible",
			mod: "gpt-4o",
			cw:  128_000,
		},
		behaviorTag: "openai-compatible",
	}

	sess, err := NewSession(c, compatProfile, NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	const openAIMarker = "they execute in the order you"
	prompt := sess.renderSystemPrompt()
	if strings.Contains(prompt, openAIMarker) {
		t.Fatalf("system prompt contains openai section marker %q — openai-compatible must NOT load the openai section",
			openAIMarker)
	}
}
