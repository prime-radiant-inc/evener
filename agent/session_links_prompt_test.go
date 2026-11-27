package agent

import (
	"strings"
	"testing"
)

// TestSessionLinksPrompt checks that the Session links heading renders when
// urls_add is callable (even while its schema is deferred) and is omitted when
// it is absent.
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
	if !sess.isDeferredTool("urls_add") {
		t.Fatal("expected urls_add schema to stay deferred on persistent root")
	}
	checkPromptInput(t, "HasTool urls_add (present)", data.HasTool("urls_add"), true)
	prompt, warning, _ := sess.renderSystemPrompt(sess.env)
	if warning != "" {
		t.Fatalf("render system prompt: %s", warning)
	}
	if section := systemPromptSection(t, prompt, heading); section == "" {
		t.Fatalf("expected non-empty %q section when urls_add is registered", heading)
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
