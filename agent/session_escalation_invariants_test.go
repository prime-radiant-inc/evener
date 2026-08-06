//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/llm"
)

// The four model-isolation walls (not triggerable / not approvable / not
// observable / not replayable) plus non-interactive parity, consolidated.

// Wall 1 — NOT TRIGGERABLE: no model-facing tool can raise an escalation; only the
// harness seam does.
func TestEscalationWall_NotTriggerableByAnyTool(t *testing.T) {
	s := newSession(t)
	for _, name := range s.reg.Names() {
		if strings.Contains(strings.ToLower(name), "escalat") {
			t.Fatalf("no model-facing tool may be named for escalation; found %q", name)
		}
	}
}

// Wall 2 — NOT APPROVABLE / NOT OBSERVABLE via a watch: the escalation event is
// absent from the model-facing watch allowlist, so no model-configured watch can
// see (or react to) it.
func TestEscalationWall_NotModelWatchable(t *testing.T) {
	for name, kind := range modelEventKinds {
		if kind == events.EventSandboxEscalationRequested {
			t.Fatalf("the escalation event must not be model-watchable (found under %q)", name)
		}
	}
}

// Wall 3 — NOT OBSERVABLE in history: after a full approve→re-run turn, the model's
// history contains only the tool round — the escalation id appears nowhere.
func TestEscalationWall_NotObservableInHistory(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("PAYLOAD"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess, _ := sbxRestrictedTurnSession(t, readFileTurn(t, outside)...)

	var mu sync.Mutex
	var sawID string
	go func() {
		for ev := range sess.Events() {
			if ev.Kind != events.EventSandboxEscalationRequested {
				continue
			}
			d := ev.Data.(events.SandboxEscalationRequestedData)
			mu.Lock()
			sawID = d.EscalationID
			mu.Unlock()
			_ = sess.ResolveSandboxEscalation(d.EscalationID, true)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "read", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	mu.Lock()
	id := sawID
	mu.Unlock()
	if id == "" {
		t.Fatal("expected an escalation to have been raised")
	}
	sess.mu.Lock()
	blob, _ := json.Marshal(sess.history)
	sess.mu.Unlock()
	if strings.Contains(string(blob), id) {
		t.Fatalf("the escalation id %q must never appear in the model-visible history", id)
	}
	// And the escalation event itself is never a persisted turn kind.
	if strings.Contains(string(blob), "SANDBOX_ESCALATION") {
		t.Fatal("no escalation must be recorded as a transcript turn")
	}
}

// Wall 3b — NOT OBSERVABLE via the snapshot: the thread/read PendingEscalations
// snapshot is a HUMAN-CLIENT field. While an escalation is pending it appears in the
// human snapshot (PendingEscalations) but NEVER in the model's history — the
// snapshot is a separate channel, not built from (or leaking into) what the model sees.
func TestEscalationWall_SnapshotIsHumanOnlyNotInHistory(t *testing.T) {
	s := escalatableSession(t)
	res, _ := deniedResult("/secret/path/xyz")
	done := make(chan tool.ExecResult, 1)
	go func() {
		done <- s.escalateOnSandboxDenial(context.Background(), "read_file", res, func(context.Context) tool.ExecResult { return succeededResult() })
	}()
	awaitPending(t, s, 1)

	snap := s.PendingEscalations()
	if len(snap) != 1 || snap[0].DeniedPath != "/secret/path/xyz" {
		t.Fatalf("the human snapshot must carry the pending escalation: %+v", snap)
	}
	s.mu.Lock()
	blob, _ := json.Marshal(s.history)
	s.mu.Unlock()
	if strings.Contains(string(blob), "/secret/path/xyz") || strings.Contains(string(blob), snap[0].EscalationID) {
		t.Fatalf("the escalation must never appear in the model's history despite the snapshot: %s", blob)
	}
	_ = s.ResolveSandboxEscalation(snap[0].EscalationID, false)
	<-done
}

// Wall 4 — NOT REPLAYABLE: an escalation blocked mid-wait resolves to the typed
// denial (an IsError result) when the session closes, exactly like an interrupted
// ask_user; nothing is persisted to replay. (The pending map is in-memory only.)
func TestEscalationWall_NotReplayable_CloseYieldsErrorPlaceholder(t *testing.T) {
	s := escalatableSession(t)
	res, _ := deniedResult("/etc/hosts")
	done := make(chan tool.ExecResult, 1)
	go func() {
		done <- s.escalateOnSandboxDenial(context.Background(), "read_file", res, noRerun(t))
	}()
	awaitPending(t, s, 1)
	s.Close() // interrupt mid-wait
	got := <-done
	if !got.IsError {
		t.Fatal("a mid-wait close must yield an IsError result (the orphan-repair placeholder)")
	}
	// Nothing persists: a fresh restart has no pending escalation to replay.
	if len(pendingIDs(s)) != 0 {
		t.Fatal("no escalation may remain pending after close")
	}
}

// Non-interactive parity: a --sandbox NON-interactive session's denial is unchanged
// from pre-M7 — the typed error is final, no escalation event is emitted, nothing
// blocks.
func TestEscalation_NonInteractiveDenialUnchanged(t *testing.T) {
	home := t.TempDir()
	worktree := filepath.Join(home, "wt")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	facts := sandbox.HostFacts{OS: "linux", Home: home, BwrapPath: "/usr/bin/bwrap", BwrapCapable: true}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeRestricted}, facts, worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(worktree)
	env.Sandbox = &rp
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{MaxSubagentDepth: 1, NonInteractive: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	// A live subscriber exists, so ONLY the NonInteractive flag makes this final.
	sess.SetSubscriberCountFunc(func() int { return 1 })

	var mu sync.Mutex
	var escalated bool
	go func() {
		for ev := range sess.Events() {
			if ev.Kind == events.EventSandboxEscalationRequested {
				mu.Lock()
				escalated = true
				mu.Unlock()
			}
		}
	}()

	outside := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(outside, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"file_path": outside})
	res := sess.execTool(context.Background(), llm.ToolCallData{ID: "c1", Name: "read_file", Arguments: args}, "")

	if !res.IsError {
		t.Fatal("a non-interactive out-of-root read must be denied")
	}
	if _, ok := sandbox.AsDenied(res.Err); !ok {
		t.Fatalf("the denial must be the typed sandbox error, got %v", res.Err)
	}
	if len(pendingIDs(sess)) != 0 {
		t.Fatal("a non-interactive session must never block on an escalation")
	}
	time.Sleep(20 * time.Millisecond) // let any stray event drain
	mu.Lock()
	defer mu.Unlock()
	if escalated {
		t.Fatal("a non-interactive session must emit NO escalation event")
	}
}
