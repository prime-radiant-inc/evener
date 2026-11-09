package contextmgr

import (
	"flag"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
)

var updateSummaryPromptGoldens = flag.Bool("update-prompt", false,
	"rewrite agent/internal/contextmgr/testdata summary prompt and search summary goldens")

// The summary prompt is pinned whole, per shape, in testdata/summaryprompt:
// the default mandatory-sections prompt and the caller-instruction prompt that
// replaces those sections. Opaque sentinels stand in for the history and the
// caller's instructions. This is the repository's pattern for prompt text
// (agent/testdata/memoryprompt is the same): the golden pins the whole prompt
// as reviewed output, so a wording change shows up as a reviewed diff instead
// of a sentence-level assertion; behavior that depends on the prompt's
// structure is pinned separately (TestSummaryPromptSectionsComeFromThePrompt).
// Regenerate after an intended wording change with
//
//	go test ./agent/internal/contextmgr -run 'TestSummaryPromptGolden$' -count=1 -update-prompt
//
// and read the diff.
func TestSummaryPromptGolden(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, instructions string
	}{
		{"default", ""},
		{"caller-instructions", "CALLER_INSTRUCTIONS_SENTINEL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			prompt := buildSummaryPrompt("HISTORY_SENTINEL\n", tc.instructions)
			agenttest.CheckGolden(t, filepath.Join("testdata", "summaryprompt", tc.name+".md"), []byte(prompt), *updateSummaryPromptGoldens,
				"Regenerate with `go test ./agent/internal/contextmgr -run 'TestSummaryPromptGolden$' -count=1 -update-prompt` and read the diff.")
		})
	}
}
