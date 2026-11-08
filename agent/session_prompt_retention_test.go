package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SystemPromptAppend files are documented as always applied. A file that cannot
// be read is still skipped, but the omission must be disclosed through the
// existing render-warning channel instead of silently dropping the operator's
// instructions (CORE-08).
func TestRefreshSystemPromptCacheDisclosesUnreadableAppend(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "extra.md")
	const goodSentinel = "GOOD_APPEND_SENTINEL"
	if err := os.WriteFile(good, []byte(goodSentinel), 0o644); err != nil {
		t.Fatalf("write append file: %v", err)
	}
	missing := filepath.Join(dir, "does-not-exist.md")

	sess := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth:   1,
		SystemPromptAppend: []string{good, missing},
	}))
	warning := sess.refreshSystemPromptCache(sess.env)
	if warning == "" {
		t.Fatal("an unreadable SystemPromptAppend file was skipped without any diagnostic")
	}
	if !strings.Contains(sess.cachedSystemPrompt, goodSentinel) {
		t.Fatalf("the readable append file was not applied:\n%s", sess.cachedSystemPrompt)
	}
}

// A failed refresh must not replace a valid cached prompt with diagnostic
// prose: the model would then run on "Template rendering failed: ..." instead
// of the last prompt that rendered (CORE-08).
func TestRefreshSystemPromptCacheKeepsValidPromptOnRenderFailure(t *testing.T) {
	const cachedSentinel = "CACHED_VALID_PROMPT_SENTINEL"
	sess := newSession(t)
	sess.mu.Lock()
	sess.cachedSystemPrompt = cachedSentinel
	sess.mu.Unlock()

	forceSystemPromptRenderFailure(t)
	warning := sess.refreshSystemPromptCache(sess.env)
	if warning == "" {
		t.Fatal("the render failure was not reported")
	}
	if sess.cachedSystemPrompt != cachedSentinel {
		t.Fatalf("cached prompt after a failed refresh = %q, want the previous valid prompt", sess.cachedSystemPrompt)
	}
}
