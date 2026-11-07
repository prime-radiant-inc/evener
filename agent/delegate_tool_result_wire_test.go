package agent

import (
	"encoding/json"
	"testing"
)

// The delegate tool results carry the worktree and sandbox reports on the wire
// that clients (the hub transcript renderer, the model) read by key. This pins
// the exact bytes of each embedded object.
func TestDelegateToolResultsEmbedReportsWithStableWireKeys(t *testing.T) {
	wt := &delegateWorktreeReport{Path: "/lane", Branch: "b", HeadSHA: "abc", Ahead: 2, Dirty: true, DisposalHint: "hint"}
	sb := &delegateSandboxReport{Mode: "workspace-write", Network: true}

	const wantWorktree = `{"path":"/lane","branch":"b","head_sha":"abc","ahead_commits":2,"dirty":true,"disposal_hint":"hint"}`
	const wantSandbox = `{"mode":"workspace-write","network":true}`

	subObject := func(t *testing.T, state any, key string) string {
		t.Helper()
		blob, err := json.Marshal(state)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(blob, &fields); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return string(fields[key])
	}

	send := delegateSendResultFrom(sendMessageResult{Worktree: wt})
	if got := subObject(t, send, "worktree"); got != wantWorktree {
		t.Fatalf("send worktree = %s, want %s", got, wantWorktree)
	}
	create := delegateCreateResult{Sandbox: sb, Worktree: wt}
	if got := subObject(t, create, "worktree"); got != wantWorktree {
		t.Fatalf("create worktree = %s, want %s", got, wantWorktree)
	}
	if got := subObject(t, create, "sandbox"); got != wantSandbox {
		t.Fatalf("create sandbox = %s, want %s", got, wantSandbox)
	}
	stop := jobStopResult{Worktree: wt}
	if got := subObject(t, stop, "worktree"); got != wantWorktree {
		t.Fatalf("stop worktree = %s, want %s", got, wantWorktree)
	}
}
