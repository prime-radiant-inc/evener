package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

// The "How you work" section of the system prompt carries the guidance on
// when to stop for context length, so a session's system prompt is rendered
// and the whole section pinned. Regenerate after a wording change with
//
//	go test ./agent -run 'TestWorkGuidanceGolden$' -count=1 -update-prompt
//
// and read the diff.
func TestWorkGuidanceGolden(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter()
	adapter.script("root", func(llm.Request) llm.Response { return agenttest.FinalResponse("root done") })
	root := newMessagingRoot(t, adapter, nil)
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx := tripwireCtx(t)
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	req := adapter.waitForRequests(t, "root", 1)[0]
	var system strings.Builder
	for _, msg := range req.Messages {
		if msg.Role == llm.RoleSystem {
			system.WriteString(msg.Text())
		}
	}
	_, section, ok := strings.Cut(system.String(), "\n## How you work\n")
	if !ok {
		t.Fatal("system prompt has no How you work section")
	}
	if next := strings.Index(section, "\n## "); next >= 0 {
		section = section[:next]
	}
	agenttest.CheckGolden(t, filepath.Join("testdata", "workprompt", "root.md"), []byte("## How you work\n"+section+"\n"), *updatePromptGoldens,
		"Regenerate with `go test ./agent -run 'TestWorkGuidanceGolden$' -count=1 -update-prompt` and read the diff.")
}
