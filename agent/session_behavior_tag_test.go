package agent

// Tests that session.go provider-conditional behavior sites key on
// s.profile.BehaviorTag() rather than s.profile.ID() or req.Provider.
// This ensures renamed provider instances (WithProviderID) keep the right
// behavior, and that chat-completions instances (tag "openai-compatible")
// correctly do NOT get the real-openai behavior.

import (
	_ "embed"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

// openAISectionContent holds the raw content of the embedded OpenAI-specific
// tools prompt section. The section resolver loads this file when
// provider="openai"; tests verify its presence or absence in rendered system
// prompts by comparing against the actual file content rather than a
// hardcoded phrase, so they track prose changes automatically.
//
//go:embed prompts/sections/tools.provider-openai_append.md
var openAISectionContent string

// ── Site 1: applyModelRequestMetadata (prompt-cache) ──────────────────────

// TestBehaviorTag_PromptCache_RenamedOpenAI verifies that a renamed OpenAI
// instance (id="work", tag="openai") is still prompt-cache eligible.
// Before the fix, applyModelRequestMetadata keyed on req.Provider == "openai";
// a renamed instance has req.Provider = s.profile.ID() = "work", so the cache
// was never activated.
func TestBehaviorTag_PromptCache_RenamedOpenAI(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	compatProfile := testOpenAICompatProfile("openai", "gpt-5.5", 0)
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
// than the placeholder that tool.NewRegistry installs ("tool executor not wired").
// We distinguish them by calling the executor and checking whether the error
// text is the sentinel placeholder string. The real executor may panic on nil
// client/context; we recover from that (it still means it was the real path).
//
// Used by provider_instance_integration_test.go and session_resolve_profile_test.go
// to inspect a live session's registry after profile switches. New tests in this
// file use webSearchIsWired instead, which avoids the panic-recovery heuristic.
func webSearchExecIsReal(t *testing.T, reg *tool.Registry) (isReal bool) {
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

// webSearchIsWired reports whether registerCoreTools registers the web_search
// function tool for the given session's behavior tag. It uses a fresh empty
// registry — not newProfileToolRegistry — so the result reflects only what
// registerCoreTools itself adds: a non-nil entry means the BehaviorTag=="google"
// branch was taken; absence means it was not. This avoids relying on executor
// nil-deref behavior to distinguish real from placeholder.
func webSearchIsWired(t *testing.T, sess *Session) bool {
	t.Helper()
	reg := tool.NewRegistry()
	if err := registerCoreTools(reg, sess); err != nil {
		t.Fatalf("registerCoreTools: %v", err)
	}
	return reg.Get("web_search") != nil
}

// TestBehaviorTag_Gemini_RenamedGoogleRegistersWebSearch verifies that a
// renamed Google instance (id="myai", tag="google") gets the web_search tool
// wired by registerCoreTools.
// Before the fix, the check was s.profile.ID() == "gemini", so a renamed
// instance (id="myai") would not get web_search registered.
func TestBehaviorTag_Gemini_RenamedGoogleRegistersWebSearch(t *testing.T) {
	t.Parallel()
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
		env:     execenv.NewLocalExecutionEnvironment(dir),
	}

	if !webSearchIsWired(t, sess) {
		t.Fatalf("web_search not registered by registerCoreTools — renamed google instance (BehaviorTag=%q) must get web_search wired",
			renamedGemini.BehaviorTag())
	}
}

// TestBehaviorTag_Gemini_OriginalGeminiRegistersWebSearch verifies the
// existing baseline: an unmodified gemini profile (id="gemini", tag="google")
// still gets web_search wired by registerCoreTools.
func TestBehaviorTag_Gemini_OriginalGeminiRegistersWebSearch(t *testing.T) {
	t.Parallel()
	geminiProfile := newGeminiProfile("gemini-2.5-pro")

	dir := t.TempDir()
	sess := &Session{
		profile: geminiProfile,
		env:     execenv.NewLocalExecutionEnvironment(dir),
	}

	if !webSearchIsWired(t, sess) {
		t.Fatalf("web_search not registered by registerCoreTools for unmodified gemini profile (BehaviorTag=%q)",
			geminiProfile.BehaviorTag())
	}
}

// TestBehaviorTag_Gemini_OpenAIDoesNotRegisterWebSearch verifies that
// registerCoreTools does NOT wire web_search for OpenAI
// (it uses native web search via req.WebSearch instead).
func TestBehaviorTag_Gemini_OpenAIDoesNotRegisterWebSearch(t *testing.T) {
	t.Parallel()
	openaiProfile := NewOpenAIProfile("gpt-5.5")

	dir := t.TempDir()
	sess := &Session{
		profile: openaiProfile,
		env:     execenv.NewLocalExecutionEnvironment(dir),
	}

	if webSearchIsWired(t, sess) {
		t.Fatalf("registerCoreTools wired web_search for openai (BehaviorTag=%q) — must use native web search instead",
			openaiProfile.BehaviorTag())
	}
}

// ── Site 3: renderSystemPrompt sectionResolver provider ───────────────────

// TestBehaviorTag_SectionResolver_RenamedOpenAILoadsOpenAISection verifies
// that a session with a renamed OpenAI profile (id="work", tag="openai")
// renders the tools.provider-openai_append.md section in the system prompt.
// Before the fix, sectionResolver.provider = s.profile.ID() = "work", so no
// openai-specific section would be loaded.
func TestBehaviorTag_SectionResolver_RenamedOpenAILoadsOpenAISection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "work", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("ok") },
	}}
	c.Register(f)

	// Renamed OpenAI instance: id="work", tag="openai".
	renamedProfile := WithProviderID(NewOpenAIProfile("gpt-5.5"), "work")
	sess, err := NewSession(c, renamedProfile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// The embedded tools.provider-openai_append.md content is present verbatim
	// in the rendered system prompt only when sectionResolver.provider="openai".
	// We compare against the actual file content so the test tracks prose
	// changes automatically rather than coupling to a specific phrase.
	openAISection := strings.TrimRight(openAISectionContent, "\n")
	prompt := sess.renderSystemPrompt(sess.env)
	if !strings.Contains(prompt, openAISection) {
		t.Fatalf("system prompt missing openai section — SectionResolver provider must be %q (behaviorTag), not %q (ID)",
			renamedProfile.BehaviorTag(), renamedProfile.ID())
	}
}

// TestBehaviorTag_SectionResolver_OpenAICompatibleDoesNotLoadOpenAISection
// verifies that a chat-completions instance (tag="openai-compatible") does
// NOT render the tools.provider-openai_append.md section.
func TestBehaviorTag_SectionResolver_OpenAICompatibleDoesNotLoadOpenAISection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai-compatible", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("ok") },
	}}
	c.Register(f)

	compatProfile := testOpenAICompatProfile("openai-compatible", "gpt-4o", 128_000)

	sess, err := NewSession(c, compatProfile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		NoProjectPrompts: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	openAISection := strings.TrimRight(openAISectionContent, "\n")
	prompt := sess.renderSystemPrompt(sess.env)
	if strings.Contains(prompt, openAISection) {
		t.Fatalf("system prompt contains openai section — openai-compatible must NOT load the openai section")
	}
}
