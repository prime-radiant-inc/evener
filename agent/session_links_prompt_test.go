package agent

import (
	"strings"
	"testing"
)

// TestSessionLinksPrompt checks that the Session links heading renders when
// urls_add is advertised upfront and is omitted when deferred or absent.
func TestSessionLinksPrompt(t *testing.T) {
	t.Parallel()
	const heading = "## Session links"
	sess := newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true},
	}))
	data, _ := sess.buildPromptData(sess.env)
	if sess.reg.Get("urls_add") == nil {
		t.Fatal("expected urls_add to be registered on persistent root")
	}
	checkPromptInput(t, "HasTool urls_add (deferred on Turn 1)", data.HasTool("urls_add"), false)
	prompt, warning, _ := sess.renderSystemPrompt(sess.env)
	if warning != "" {
		t.Fatalf("render system prompt: %s", warning)
	}
	if strings.Contains(prompt, "\n"+heading+"\n") {
		t.Fatalf("Turn 1 prompt unexpectedly renders %q while urls_add is deferred", heading)
	}

	sess.reg.Remove("urls_add")
	refreshModelFacingCaches(sess)
	data, _ = sess.buildPromptData(sess.env)
	checkPromptInput(t, "HasTool urls_add (removed)", data.HasTool("urls_add"), false)
	prompt, warning, _ = sess.renderSystemPrompt(sess.env)
	if warning != "" {
		t.Fatalf("render system prompt after removing urls_add: %s", warning)
	}
	if strings.Contains(prompt, "\n"+heading+"\n") {
		t.Fatalf("system prompt still renders %q after removing urls_add", heading)
	}
}
