package agent

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
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
	adapter := newMessagingAdapter("GOLDEN-CHILD", "SCHEMA-CHILD")
	// A result_schema replaces communicate's output parameter, so the
	// delegate's wording must hold for any output shape, not only the
	// default envelope.
	verdictSchema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"verdict": map[string]any{"type": "string", "description": "pass or fail"}},
		"required":   []any{"verdict"},
	}
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("create", "GOLDEN-CHILD: report the repository name.")
		},
		func(llm.Request) llm.Response {
			return delegateToolResponseWithResultSchema("create-schema", "SCHEMA-CHILD: say whether the build passes.", verdictSchema)
		},
		func(llm.Request) llm.Response { return finalResponse("root done") },
	)
	adapter.script("GOLDEN-CHILD", func(llm.Request) llm.Response { return finalResponse("evener") })
	adapter.script("SCHEMA-CHILD", func(llm.Request) llm.Response { return finalResponse("pass") })
	root := newMessagingRoot(t, adapter, nil)
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	rootRequest := adapter.waitForRequests(t, "root", 1)[0]
	delegateRequest := adapter.waitForRequests(t, "GOLDEN-CHILD", 1)[0]
	schemaDelegateRequest := adapter.waitForRequests(t, "SCHEMA-CHILD", 1)[0]

	// The delegate gets communicate worded for its parent; the root keeps the
	// wording for its human partner.
	communicateDescription := func(req llm.Request) string {
		for _, def := range req.Tools {
			if def.Name == "communicate" {
				return def.Description
			}
		}
		t.Fatal("request offers no communicate tool")
		return ""
	}
	if got, want := communicateDescription(delegateRequest), toolpkg.WithCommunicateDelegatePointer(toolpkg.WithCommunicateDelegateUpdates(toolpkg.DefCommunicate())).Description; got != want {
		t.Fatalf("delegate communicate is not worded for its parent:\n got %s\nwant %s", got, want)
	}
	if got, want := communicateDescription(rootRequest), toolpkg.WithCommunicateDelegatePointer(toolpkg.DefCommunicate()).Description; got != want {
		t.Fatalf("root communicate wording changed:\n got %s\nwant %s", got, want)
	}

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
		{"delegate-result-schema", schemaDelegateRequest, nil},
	} {
		agenttest.CheckGolden(t, filepath.Join("testdata", "messagingprompt", tc.name+".md"), messagingGuidanceGolden(t, tc.req, tc.extra, "communicate"), *updatePromptGoldens,
			"Regenerate with `go test ./agent -run 'TestDelegateMessagingGuidanceGoldens$' -count=1 -update-prompt` and read the diff.")
	}
}

// A session whose result tool is renamed names that tool everywhere its
// guidance mentions it: the Reporting section, the delegate, delegate_send,
// job_list and job_watch descriptions.
func TestDelegateMessagingGuidanceGoldenRenamedResultTool(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter()
	adapter.script("root", func(llm.Request) llm.Response {
		resp := finalResponse("root done")
		resp.Message.Content[0].ToolCall.Name = "respond"
		return resp
	})
	root := newMessagingRoot(t, adapter, func(cfg *SessionConfig) { cfg.ResultToolName = "respond" })
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	rootRequest := adapter.waitForRequests(t, "root", 1)[0]
	// The live delegate tool's sandbox wording depends on the host, so the
	// golden renders it from fixed inputs.
	extra := []llm.ToolDefinition{toolpkg.DefDelegateWithResultTool(nil, toolpkg.DelegateSandboxSchema{
		Available: true,
		Modes:     []string{"off", "read-only", "workspace-write", "restricted"},
	}, "respond")}
	for _, def := range rootRequest.Tools {
		if slices.Contains([]string{"ask_user", "job_list", "job_watch"}, def.Name) {
			extra = append(extra, def)
		}
	}
	if len(extra) != 4 {
		t.Fatalf("root request offers %d of ask_user, job_list and job_watch, want all three", len(extra)-1)
	}
	agenttest.CheckGolden(t, filepath.Join("testdata", "messagingprompt", "root-renamed-result-tool.md"), messagingGuidanceGolden(t, rootRequest, extra, "respond"), *updatePromptGoldens,
		"Regenerate with `go test ./agent -run 'TestDelegateMessagingGuidanceGolden' -count=1 -update-prompt` and read the diff.")
}

// messagingGuidanceGolden renders the Reporting section of req's system
// prompt and the description of every messaging tool req offers, plus extra.
// resultTool is the name of the session's result tool.
func messagingGuidanceGolden(t *testing.T, req llm.Request, extra []llm.ToolDefinition, resultTool string) []byte {
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
		switch def.Name {
		case resultTool, "delegate_send":
			tools = append(tools, def)
		}
	}
	slices.SortFunc(tools, func(a, b llm.ToolDefinition) int { return strings.Compare(a.Name, b.Name) })
	var golden strings.Builder
	golden.WriteString("## Reporting\n" + section + "\n\n# Messaging tool descriptions\n")
	writeToolDescriptions(&golden, tools...)
	return []byte(golden.String())
}
