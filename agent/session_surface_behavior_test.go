package agent

// Tests that session.go's provider-conditional sites key on the profile's
// surface and protocol rather than on s.profile.ID() or req.Provider: an
// instance under a user-assigned name keeps its vendor behavior, and a
// generic-surface instance does not get the OpenAI one. The prompt-cache site
// moved off this axis entirely — see session_openai_prompt_cache_test.go.

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

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
// function tool for the given session. It uses a fresh empty registry — not
// newProfileToolRegistry — so the result reflects only what registerCoreTools
// itself adds: a non-nil entry means the google-protocol branch was taken
// (session_tool_registry.go's webSearchEnabled); absence means it was not.
// This avoids relying on executor nil-deref behavior to distinguish real from
// placeholder.
func webSearchIsWired(t *testing.T, sess *Session) bool {
	t.Helper()
	reg := tool.NewRegistry()
	if err := registerCoreTools(reg, sess); err != nil {
		t.Fatalf("registerCoreTools: %v", err)
	}
	return reg.Get("web_search") != nil
}

// TestWebSearchToolWiredForANamedGoogleInstance verifies that a google
// instance under a user-assigned name (id "myai") still gets the web_search
// tool wired by registerCoreTools: the site keys on the protocol, not on a
// hard-coded instance name.
func TestWebSearchToolWiredForANamedGoogleInstance(t *testing.T) {
	t.Parallel()
	// A google instance under a user-assigned name; its surface and protocol
	// are still google.
	renamedGemini := namedInstanceProfile("myai", "google", "gemini-2.5-pro")
	if renamedGemini.ID() != "myai" {
		t.Fatalf("pre-condition: ID() = %q, want myai", renamedGemini.ID())
	}
	if renamedGemini.Protocol() != registry.ProtocolGoogle {
		t.Fatalf("pre-condition: Protocol() = %q, want google", renamedGemini.Protocol())
	}

	dir := t.TempDir()
	sess := &Session{
		profile: renamedGemini,
		env:     execenv.NewLocalExecutionEnvironment(dir),
	}

	if !webSearchIsWired(t, sess) {
		t.Fatalf("web_search not registered by registerCoreTools — renamed google instance (protocol %q) must get web_search wired",
			renamedGemini.Protocol())
	}
}

// TestWebSearchToolWiredForTheGoogleInstance is the baseline: an instance
// named for its own provider gets web_search wired the same way.
func TestWebSearchToolWiredForTheGoogleInstance(t *testing.T) {
	t.Parallel()
	geminiProfile := newGeminiProfile("gemini-2.5-pro")

	dir := t.TempDir()
	sess := &Session{
		profile: geminiProfile,
		env:     execenv.NewLocalExecutionEnvironment(dir),
	}

	if !webSearchIsWired(t, sess) {
		t.Fatalf("web_search not registered by registerCoreTools for unmodified gemini profile (protocol %q)",
			geminiProfile.Protocol())
	}
}

// TestWebSearchToolAbsentWhenTheRowServesNone pins the
// second half of the §7.5 rule: the function tool is registered for
// Protocol == google AND a row that serves web search. A google model whose
// live /models entry says it does not gets no web_search.
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

// TestReapplyProviderTools_GoogleWithoutWebSearch covers the same conjunct on
// the mid-session switch path: switching INTO a google profile that serves no
// web search registers nothing, and switching from one that does into one that
// does not removes the tool.
func TestReapplyProviderTools_GoogleWithoutWebSearch(t *testing.T) {
	t.Parallel()
	searching := newGeminiProfile("gemini-2.5-pro")
	if !searching.SupportsWebSearch() {
		t.Fatal("pre-condition: the google profile must serve web search")
	}
	quiet := withWebSearch(newGeminiProfile("gemini-2.5-pro"), false)

	s := &Session{reg: tool.NewRegistry(), env: execenv.NewLocalExecutionEnvironment(t.TempDir())}
	s.reapplyProviderSpecificTools(NewOpenAIProfile("gpt-5.4"), quiet)
	if s.reg.Get("web_search") != nil {
		t.Fatal("switching into a google profile that serves no web search registered web_search")
	}

	s.reapplyProviderSpecificTools(NewOpenAIProfile("gpt-5.4"), searching)
	if s.reg.Get("web_search") == nil {
		t.Fatal("switching into a web-searching google profile must register web_search")
	}
	s.reapplyProviderSpecificTools(searching, quiet)
	if s.reg.Get("web_search") != nil {
		t.Fatal("losing web search on a google profile must remove web_search")
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
	sess, err := NewSession(c, renamedProfile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
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
	sess, err := NewSession(c, compatProfile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	data, _ := sess.buildPromptData(sess.env)
	if got := data.Surface; got != registry.SurfaceGeneric {
		t.Fatalf("prompt surface = %q, want %q", got, registry.SurfaceGeneric)
	}
}
