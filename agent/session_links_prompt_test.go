package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
)

// TestSessionLinksPromptGolden pins the Session links section whole, so a
// wording change is reviewed as one unit. The urls_add gate itself is covered
// across shipped agents by TestShippedPromptsOnlyNameToolsTheSessionHas.
func TestSessionLinksPromptGolden(t *testing.T) {
	t.Parallel()
	const heading = "## Session links"
	sess := newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true},
	}))
	prompt, warning, _ := sess.renderSystemPrompt(sess.env)
	if warning != "" {
		t.Fatalf("render system prompt: %s", warning)
	}
	// Drop the helper's appended framing newline, not rendered content.
	section := strings.TrimSuffix(systemPromptSection(t, prompt, heading), "\n")
	agenttest.CheckGolden(t, filepath.Join("testdata", "sessionlinks", "with-urls-add.md"),
		[]byte(section), *updatePromptGoldens,
		"Regenerate with `go test ./agent -run '^TestSessionLinksPromptGolden$' -count=1 -update-prompt` and read the diff.")
}
