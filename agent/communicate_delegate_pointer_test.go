package agent

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	toolpkg "primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// communicate points to delegate_send exactly in the sessions that offer
// delegate_send: the root and the delegates whose role carries it. Roles
// without it (explorer, doctor) get no pointer to a tool they cannot call.
func TestCommunicatePointsToDelegateSendOnlyWhereOffered(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter("PLAIN-CHILD", "EXPLORER-CHILD", "DOCTOR-CHILD")
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("plain", "PLAIN-CHILD: report the repository name.")
		},
		func(llm.Request) llm.Response {
			return delegateToolCall("explorer", map[string]any{"prompt": "EXPLORER-CHILD: list files.", "agent_type": "explorer"})
		},
		func(llm.Request) llm.Response {
			return delegateToolCall("doctor", map[string]any{"prompt": "DOCTOR-CHILD: diagnose.", "agent_type": "doctor"})
		},
		func(llm.Request) llm.Response { return agenttest.FinalResponse("root done") },
	)
	root := newMessagingRoot(t, adapter, nil)
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx := tripwireCtx(t)
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	pointer := toolpkg.WithCommunicateDelegatePointer(llm.ToolDefinition{}).Description
	for _, tc := range []struct {
		name        string
		key         string
		wantPointer bool
	}{
		{"root", "root", true},
		{"plain delegate", "PLAIN-CHILD", true},
		{"explorer", "EXPLORER-CHILD", false},
		{"doctor", "DOCTOR-CHILD", false},
	} {
		req := adapter.waitForRequests(t, tc.key, 1)[0]
		var communicate llm.ToolDefinition
		for _, def := range req.Tools {
			if def.Name == "communicate" {
				communicate = def
			}
		}
		if communicate.Name == "" {
			t.Fatalf("%s: request offers no communicate tool", tc.name)
		}
		if got := strings.HasSuffix(communicate.Description, pointer); got != tc.wantPointer {
			t.Errorf("%s: communicate description ends with the delegate_send pointer = %v, want %v:\n%s", tc.name, got, tc.wantPointer, communicate.Description)
		}
	}
}
