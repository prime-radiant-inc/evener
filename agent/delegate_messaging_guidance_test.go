package agent

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	toolpkg "primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// The Reporting section and the messaging tools' descriptions are prompt
// text, pinned whole per session kind in testdata/messagingprompt and never
// by substring. Root and delegate text come from the requests a real root and
// its real delegate send. The delegate and job tools' observer guidance comes
// from their definitions with fixed inputs, since the live delegate tool's
// sandbox wording depends on the host. Regenerate after an intended wording
// change with
//
//	go test ./agent -run 'TestDelegateMessagingGuidanceGoldens$' -count=1 -update-prompt
//
// and read the diff.
func TestDelegateMessagingGuidanceGoldens(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter("GOLDEN-CHILD")
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("create", "GOLDEN-CHILD: report the repository name.")
		},
		func(llm.Request) llm.Response { return finalResponse("root done") },
	)
	adapter.script("GOLDEN-CHILD", func(llm.Request) llm.Response { return finalResponse("evener") })
	root := newMessagingRoot(t, adapter)
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	rootRequest := adapter.waitForRequests(t, "root", 1)[0]
	delegateRequest := adapter.waitForRequests(t, "GOLDEN-CHILD", 1)[0]

	observerTools := []llm.ToolDefinition{
		toolpkg.DefDelegate(nil),
		toolpkg.DefJobList(),
		toolpkg.DefJobWatch([]string{"assistant.tool", "communicate", "job.notification"}),
	}
	for _, tc := range []struct {
		name  string
		req   llm.Request
		extra []llm.ToolDefinition
	}{
		{"root", rootRequest, observerTools},
		{"delegate", delegateRequest, nil},
	} {
		checkGolden(t, filepath.Join("testdata", "messagingprompt", tc.name+".md"), messagingGuidanceGolden(t, tc.req, tc.extra), *updatePromptGoldens,
			"Regenerate with `go test ./agent -run 'TestDelegateMessagingGuidanceGoldens$' -count=1 -update-prompt` and read the diff.")
	}
}

// messagingGuidanceGolden renders the Reporting section of req's system
// prompt and the description of every messaging tool req offers, plus extra.
func messagingGuidanceGolden(t *testing.T, req llm.Request, extra []llm.ToolDefinition) []byte {
	t.Helper()
	var system strings.Builder
	for _, msg := range req.Messages {
		if msg.Role == llm.RoleSystem {
			system.WriteString(msg.Text())
		}
	}
	_, section, ok := strings.Cut(system.String(), "\n## Reporting\n")
	if !ok {
		t.Fatal("system prompt has no Reporting section")
	}
	if next := strings.Index(section, "\n## "); next >= 0 {
		section = section[:next]
	}
	tools := slices.Clone(extra)
	for _, def := range req.Tools {
		if def.Name == "communicate" || def.Name == "delegate_send" {
			tools = append(tools, def)
		}
	}
	slices.SortFunc(tools, func(a, b llm.ToolDefinition) int { return strings.Compare(a.Name, b.Name) })
	var golden strings.Builder
	golden.WriteString("## Reporting\n" + section + "\n\n# Messaging tool descriptions\n")
	writeToolDescriptions(&golden, tools...)
	return []byte(golden.String())
}
