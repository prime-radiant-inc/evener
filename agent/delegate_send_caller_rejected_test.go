package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/spf13/afero"

	toolpkg "primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// delegate_send reaches only a session's own delegates. Naming the parent with
// to="caller" gets an invalid_request error that points at communicate, and
// nothing reaches the parent. A root session gets the same error.
func TestDelegateSendRejectsTheCallerRoute(t *testing.T) {
	t.Parallel()
	const wantOutput = "invalid_request: delegate_send reaches only your own delegates; to message your parent, call communicate with end_turn=false"

	t.Run("delegate", func(t *testing.T) {
		t.Parallel()
		c, _ := newDelegateControllerTestHarness(t, 2, 2)
		seedDelegateControllerRunning(t, c, "dlg_parent", "")
		seedDelegateControllerRunning(t, c, "dlg_child", "dlg_parent")
		parentFS := afero.NewMemMapFs()
		attachDelegateSteerRuntime(t, c, "dlg_parent", parentFS)
		child := attachDelegateSteerRuntime(t, c, "dlg_child", afero.NewMemMapFs())
		child.id = "child-dlg_child"
		child.stateDir = t.TempDir()
		child.delegateController = c
		child.delegateRootSessionID = "root-session"
		child.owningDelegateID = "dlg_child"
		child.reg = toolpkg.NewRegistry()
		if err := registerDelegateTool(child.reg, child); err != nil {
			t.Fatalf("register stable delegate tool: %v", err)
		}
		ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, delegateLease{delegateID: "dlg_child", generation: 1})
		call := executeCallerSend(ctx, t, child)
		if !call.IsError || call.Output != wantOutput {
			t.Fatalf("caller send = error:%v output:%q, want error %q", call.IsError, call.Output, wantOutput)
		}
		if entries := decodeTranscriptEntries(t, parentFS, "/dlg_parent.jsonl"); len(entries) != 0 {
			t.Fatalf("parent transcript = %#v, want nothing delivered", entries)
		}
		c.mu.Lock()
		steers := len(c.live["dlg_parent"].pendingSteers)
		c.mu.Unlock()
		if steers != 0 {
			t.Fatalf("parent pending steers = %d, want none", steers)
		}
	})

	t.Run("root", func(t *testing.T) {
		t.Parallel()
		c, _ := newDelegateControllerTestHarness(t, 2, 2)
		writer, err := transcript.NewWriterWithFS(afero.NewMemMapFs(), "/root.jsonl", transcript.Header{SessionID: "root-session"})
		if err != nil {
			t.Fatalf("create root transcript: %v", err)
		}
		t.Cleanup(func() { _ = writer.Close() })
		root := &Session{
			id:                    "root-session",
			stateDir:              t.TempDir(),
			delegateController:    c,
			delegateRootSessionID: "root-session",
			reg:                   toolpkg.NewRegistry(),
		}
		root.attachTranscript(writer)
		c.rootRuntime = root
		if err := registerDelegateTool(root.reg, root); err != nil {
			t.Fatalf("register root stable delegate tool: %v", err)
		}
		call := executeCallerSend(context.Background(), t, root)
		if !call.IsError || call.Output != wantOutput {
			t.Fatalf("root caller send = error:%v output:%q, want error %q", call.IsError, call.Output, wantOutput)
		}
		if queue := root.SteeringQueueSnapshot(); len(queue) != 0 {
			t.Fatalf("root steering queue = %#v, want nothing queued", queue)
		}
	})
}

func executeCallerSend(ctx context.Context, t *testing.T, s *Session) toolpkg.ExecResult {
	t.Helper()
	args, err := json.Marshal(map[string]any{"to": runtimeMessageAliasCaller, "message": "need the schema"})
	if err != nil {
		t.Fatal(err)
	}
	return s.reg.ExecuteCall(ctx, s.env, llm.ToolCallData{ID: "caller-send", Name: "delegate_send", Arguments: args})
}
