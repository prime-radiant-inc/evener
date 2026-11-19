package agent

import (
	"os"
	"path/filepath"
	"slices"
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
	warning, _ := sess.refreshSystemPromptCache(sess.env)
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
	warning, _ := sess.refreshSystemPromptCache(sess.env)
	if warning == "" {
		t.Fatal("the render failure was not reported")
	}
	if sess.cachedSystemPrompt != cachedSentinel {
		t.Fatalf("cached prompt after a failed refresh = %q, want the previous valid prompt", sess.cachedSystemPrompt)
	}
}

// TestReachabilityRefreshHonorsAnAppendThatWentUnreadable pins that a reuse
// reports the load that actually happened, not the one that once succeeded.
//
// A complete load records every file. If one of those files then becomes
// unreadable, the next ordinary rebuild reads again, skips it, and renders the
// prompt without it. Keeping the older, complete snapshot through that rebuild
// made the NEXT reachability refresh — which reuses the snapshot instead of
// reading — put the vanished instructions back into the prompt, so the model
// ran on operator text the session can no longer supply and no omission was
// disclosed. The snapshot therefore follows every read, partial loads included.
func TestReachabilityRefreshHonorsAnAppendThatWentUnreadable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	appendPath := filepath.Join(dir, "extra.md")
	const body = "OPERATOR_INSTRUCTIONS_SENTINEL"
	if err := os.WriteFile(appendPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write append file: %v", err)
	}

	sess := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth:   1,
		SystemPromptAppend: []string{appendPath},
		AgentsDocPath:      filepath.Join(dir, "absent-AGENTS.md"),
	}))
	if got := sess.appendSnapshot; got == nil || len(got.loaded) != 1 || got.loaded[0].content != body {
		t.Fatalf("fixture is wrong: the startup render did not record the complete load, got %+v", got)
	}

	// The file goes away between rounds. The ordinary rebuild reads again, so it
	// renders without the instructions and discloses the omission.
	if err := os.Remove(appendPath); err != nil {
		t.Fatal(err)
	}
	warning, _ := sess.refreshSystemPromptCache(sess.env)
	if warning == "" {
		t.Fatal("the ordinary rebuild skipped an unreadable append file without disclosing it")
	}
	if strings.Contains(sess.cachedSystemPrompt, body) {
		t.Fatal("the ordinary rebuild rendered the append file it had just failed to read")
	}

	// The reachability refresh reuses instead of reading, and the load it must
	// reproduce is the one that skipped the file.
	data, _ := sess.buildPromptDataWithAppends(sess.env, true)
	if slices.Contains(data.CLIAppends, body) {
		t.Error("a reachability refresh reused an older snapshot and put back instructions the session can no longer read")
	}
	if want := []string{appendPath}; !slices.Equal(data.unreadableAppends, want) {
		t.Errorf("reused unreadable = %v, want %v: the reuse hid the omission", data.unreadableAppends, want)
	}
}
