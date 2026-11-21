package agent

// Tests that session.go's provider-conditional sites key on the profile's
// surface and protocol rather than on s.profile.ID() or req.Provider: an
// instance under a user-assigned name keeps its vendor behavior, and a
// generic-surface instance does not get the OpenAI one. The prompt-cache site
// moved off this axis entirely — see session_openai_prompt_cache_test.go.

import (
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// ── Site 2: registerCoreTools (no legacy web_search function tool) ────────

// webSearchIsWired reports whether registerCoreTools registers the web_search
// function tool for the given session.
func webSearchIsWired(t *testing.T, sess *Session) bool {
	t.Helper()
	reg := tool.NewRegistry()
	if err := registerCoreTools(reg, sess); err != nil {
		t.Fatalf("registerCoreTools: %v", err)
	}
	return reg.Get("web_search") != nil
}

// TestWebSearchToolAbsentForANamedGoogleInstance verifies that even a named
// google instance with WebSearch enabled never gets a client-side web_search
// function tool registered.
func TestWebSearchToolAbsentForANamedGoogleInstance(t *testing.T) {
	t.Parallel()
	renamedGemini := withWebSearch(namedInstanceProfile("myai", "google", "gemini-2.5-pro"), true)

	dir := t.TempDir()
	sess := &Session{
		profile: renamedGemini,
		env:     execenv.NewLocalExecutionEnvironment(dir),
	}

	if webSearchIsWired(t, sess) {
		t.Fatalf("web_search unexpectedly registered by registerCoreTools for renamed google instance (protocol %q)",
			renamedGemini.Protocol())
	}
}

// TestWebSearchToolAbsentForTheGoogleInstance verifies the baseline google
// instance does not register web_search.
func TestWebSearchToolAbsentForTheGoogleInstance(t *testing.T) {
	t.Parallel()
	geminiProfile := withWebSearch(newGeminiProfile("gemini-2.5-pro"), true)

	dir := t.TempDir()
	sess := &Session{
		profile: geminiProfile,
		env:     execenv.NewLocalExecutionEnvironment(dir),
	}

	if webSearchIsWired(t, sess) {
		t.Fatalf("web_search unexpectedly registered by registerCoreTools for gemini profile (protocol %q)",
			geminiProfile.Protocol())
	}
}

// TestWebSearchToolAbsentWhenTheRowServesNone verifies no web_search tool is registered.
func TestWebSearchToolAbsentWhenTheRowServesNone(t *testing.T) {
	t.Parallel()
	profile := withWebSearch(newGeminiProfile("gemini-2.5-pro"), false)
	if profile.SupportsWebSearch() {
		t.Fatal("pre-condition: SupportsWebSearch = true, want false")
	}

	sess := &Session{
		profile: profile,
		env:     execenv.NewLocalExecutionEnvironment(t.TempDir()),
	}

	if webSearchIsWired(t, sess) {
		t.Fatal("registerCoreTools wired web_search for a google model that serves none")
	}
}

// TestWebSearchToolAbsentOnTheOpenAISurface verifies that registerCoreTools
// does NOT wire the web_search function tool on the OpenAI surface, which uses
// native web search via req.WebSearch instead.
func TestWebSearchToolAbsentOnTheOpenAISurface(t *testing.T) {
	t.Parallel()
	openaiProfile := NewOpenAIProfile("gpt-5.5")

	dir := t.TempDir()
	sess := &Session{
		profile: openaiProfile,
		env:     execenv.NewLocalExecutionEnvironment(dir),
	}

	if webSearchIsWired(t, sess) {
		t.Fatalf("registerCoreTools wired web_search for openai (protocol %q) — must use native web search instead",
			openaiProfile.Protocol())
	}
}

// ── Site 3: renderSystemPrompt surface ─────────────────────────────────────

// TestSystemPromptSurfaceFollowsTheVendorForANamedInstance: a session on an
// openai instance under a user-assigned name (id "work") renders its prompt
// for the openai surface. The prompt keys on the surface, so it follows the
// vendor rather than the name.
func TestSystemPromptSurfaceFollowsTheVendorForANamedInstance(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "work"})
	renamedProfile := namedOpenAIInstanceProfile("work", "gpt-5.5")
	sess := newSession(t, withClient(c), withProfile(renamedProfile), withDir(t.TempDir()), withConfig(SessionConfig{}))
	defer sess.Close()
	data, _ := sess.buildPromptData(sess.env)
	if got := data.Surface; got != registry.SurfaceOpenAI {
		t.Fatalf("prompt surface = %q, want %q for instance %q", got, registry.SurfaceOpenAI, renamedProfile.ID())
	}
}

// TestSystemPromptSurfaceIsGenericForACompatInstance: a chat-completions
// instance renders its prompt for the generic surface, so it gets none of the
// openai-only guidance.
func TestSystemPromptSurfaceIsGenericForACompatInstance(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai-compatible"})
	compatProfile := testOpenAICompatProfile("openai-compatible", "gpt-4o", 128_000)
	sess := newSession(t, withClient(c), withProfile(compatProfile), withDir(t.TempDir()), withConfig(SessionConfig{}))
	defer sess.Close()
	data, _ := sess.buildPromptData(sess.env)
	if got := data.Surface; got != registry.SurfaceGeneric {
		t.Fatalf("prompt surface = %q, want %q", got, registry.SurfaceGeneric)
	}
}
