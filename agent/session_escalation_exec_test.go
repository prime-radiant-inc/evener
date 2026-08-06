//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/llm"
)

// sbxReadOnlySession builds an interactive root session whose file-tool layer
// enforces read-only mode (all writes denied) over a fresh worktree, with a live
// subscriber — the configuration that escalates. File tools run in-process, so
// setting env.Sandbox is enough; no kernel wrapper is needed.
func sbxReadOnlySession(t *testing.T) (*Session, string) {
	t.Helper()
	home := t.TempDir()
	worktree := filepath.Join(home, "wt")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	facts := sandbox.HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeReadOnly}, facts, worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{MaxSubagentDepth: 1})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	sess.SetSubscriberCountFunc(func() int { return 1 })
	return sess, worktree
}

func TestExecTool_SandboxWriteEscalatesApproveReruns(t *testing.T) {
	sess, worktree := sbxReadOnlySession(t)
	target := filepath.Join(worktree, "escalated.txt")
	call := writeFileCall("c1", target, "approved via escalation")

	done := make(chan tool.ExecResult, 1)
	go func() { done <- sess.execTool(context.Background(), call, "") }()

	ids := awaitPending(t, sess, 1)
	if err := sess.ResolveSandboxEscalation(ids[0], true); err != nil {
		t.Fatalf("resolve approve: %v", err)
	}
	res := <-done
	if res.IsError {
		t.Fatalf("approve must re-run the write successfully, got error: %s", res.FullOutput)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("granted write must land on disk: %v", err)
	}
	if string(got) != "approved via escalation" {
		t.Fatalf("on-disk content %q, want %q", got, "approved via escalation")
	}

	// Per-invocation proof: a SECOND identical write raises a FRESH escalation —
	// approving once created no standing allowance.
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	done2 := make(chan tool.ExecResult, 1)
	go func() { done2 <- sess.execTool(context.Background(), writeFileCall("c2", target, "again"), "") }()
	ids2 := awaitPending(t, sess, 1)
	_ = sess.ResolveSandboxEscalation(ids2[0], false) // deny this one
	res2 := <-done2
	if !res2.IsError {
		t.Fatal("a second write to the same path must escalate afresh (no standing grant); deny returns the typed error")
	}
}

func TestExecTool_SandboxWriteDenyReturnsTypedError(t *testing.T) {
	sess, worktree := sbxReadOnlySession(t)
	target := filepath.Join(worktree, "denied.txt")
	call := writeFileCall("c1", target, "should not be written")

	done := make(chan tool.ExecResult, 1)
	go func() { done <- sess.execTool(context.Background(), call, "") }()

	ids := awaitPending(t, sess, 1)
	if err := sess.ResolveSandboxEscalation(ids[0], false); err != nil {
		t.Fatalf("resolve deny: %v", err)
	}
	res := <-done
	if !res.IsError {
		t.Fatal("deny must return the typed sandbox error to the model")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("deny must not write the file")
	}
}

func TestExecTool_ApplyPatchDenialStaysFinal(t *testing.T) {
	sess, _ := sbxReadOnlySession(t)
	patch := "*** Begin Patch\n*** Add File: newfile.txt\n+hello\n*** End Patch\n"
	args, _ := json.Marshal(map[string]string{"patch": patch})
	call := llm.ToolCallData{ID: "c1", Name: "apply_patch", Arguments: args}

	// Must return synchronously (no escalation) with the typed denial.
	res := sess.execTool(context.Background(), call, "")
	if !res.IsError {
		t.Fatalf("an apply_patch write in read-only mode must be denied, got: %s", res.FullOutput)
	}
	if len(pendingIDs(sess)) != 0 {
		t.Fatalf("apply_patch denial must NOT raise an escalation, saw %v", pendingIDs(sess))
	}
}
