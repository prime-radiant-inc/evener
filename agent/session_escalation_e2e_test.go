//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// A live end-to-end proof of the M7 file-tool escalation through a REAL turn: a
// scripted model calls read_file on an out-of-worktree path (denied in restricted
// mode); the escalation blocks the turn's tool exec and rides the session event
// stream; a "human" (the test) watching that stream approves or denies; approve
// re-runs the ONE read through the real securepath grant and the content reaches
// the model, deny returns the typed denial. Ground-truth is asserted from the
// session history the model actually saw.
//
// The AppWire serialization hop (event → projector notification → daemon resolve
// handler → this same ResolveSandboxEscalation) is covered by the projector and
// server handler tests; here the human resolves in-process so the turn loop + the
// real fd-anchored grant are exercised end to end.

func sbxRestrictedTurnSession(t *testing.T, steps ...func(llm.Request) llm.Response) (*Session, string) {
	t.Helper()
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
	c.Register(&fakeAdapter{name: "openai", steps: steps})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{MaxSubagentDepth: 1})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	sess.SetSubscriberCountFunc(func() int { return 1 })
	return sess, worktree
}

// watchAndResolve drains the session event stream and answers the FIRST escalation
// with the given decision, recording the path shown on the card.
func watchAndResolve(sess *Session, approve bool, sawPath *string) {
	watchAndResolveCard(sess, approve, func(card events.SandboxEscalationRequestedData) {
		*sawPath = card.DeniedPath
	})
}

// watchAndResolveCard drains the session event stream and answers each escalation
// with the given decision after handing its card to beforeResolve, which runs
// while the tool call is still blocked on the human.
func watchAndResolveCard(sess *Session, approve bool, beforeResolve func(events.SandboxEscalationRequestedData)) {
	go func() {
		for ev := range sess.Events() {
			if ev.Kind != events.EventSandboxEscalationRequested {
				continue
			}
			d, ok := ev.Data.(events.SandboxEscalationRequestedData)
			if !ok {
				continue
			}
			beforeResolve(d)
			_ = sess.ResolveSandboxEscalation(d.EscalationID, approve)
		}
	}()
}

// toolResultsText concatenates every tool-result output in the session history —
// exactly what the model saw for its tool calls.
func toolResultsText(sess *Session) string {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	var b strings.Builder
	for _, turn := range sess.history {
		if turn.Kind != schema.TurnToolResults {
			continue
		}
		for _, part := range turn.Message.Content {
			if part.Kind == llm.ContentToolResult && part.ToolResult != nil {
				fmt.Fprintf(&b, "%v\n", part.ToolResult.Content)
			}
		}
	}
	return b.String()
}

func readFileTurn(t *testing.T, outside string) []func(llm.Request) llm.Response {
	t.Helper()
	call := llm.ToolCallData{
		ID:        "c1",
		Name:      "read_file",
		Arguments: json.RawMessage(`{"file_path":` + strconv.Quote(outside) + `}`),
		Type:      "function",
	}
	return []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.ToolCallResponse(call) },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("done") },
	}
}

func TestE2E_FileToolEscalation_ApproveReadSucceeds(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.txt")
	const secret = "TOP-SECRET-PAYLOAD-42"
	if err := os.WriteFile(outside, []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	sess, _ := sbxRestrictedTurnSession(t, readFileTurn(t, outside)...)

	var sawPath string
	watchAndResolve(sess, true, &sawPath)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second) // TRIPWIRE: scripted model adapter over a real sandboxed turn (bwrap + fd-anchored grant); only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "read the file", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// The card showed the FULL path (informed consent), and approve let the ONE
	// read succeed — the content reached the model.
	if sawPath != outside {
		t.Fatalf("card must show the full denied path, got %q want %q", sawPath, outside)
	}
	results := toolResultsText(sess)
	if !strings.Contains(results, secret) {
		t.Fatalf("approve must let the read succeed; the content must reach the model, got:\n%s", results)
	}
}

func TestE2E_FileToolEscalation_DenyReturnsTypedError(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.txt")
	const secret = "TOP-SECRET-PAYLOAD-42"
	if err := os.WriteFile(outside, []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	sess, _ := sbxRestrictedTurnSession(t, readFileTurn(t, outside)...)

	var sawPath string
	watchAndResolve(sess, false, &sawPath)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second) // TRIPWIRE: scripted model adapter over a real sandboxed turn (bwrap + fd-anchored grant); only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "read the file", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	results := toolResultsText(sess)
	if strings.Contains(results, secret) {
		t.Fatal("deny must NOT let the read succeed — the content must not reach the model")
	}
	if !strings.Contains(results, "sandbox") || !strings.Contains(results, "denied") {
		t.Fatalf("deny must return the typed sandbox denial to the model, got:\n%s", results)
	}
}

func fileToolTurn(name string, args map[string]any) []func(llm.Request) llm.Response {
	raw, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	call := llm.ToolCallData{ID: "c1", Name: name, Arguments: raw, Type: "function"}
	return []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.ToolCallResponse(call) },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("done") },
	}
}

// symlinkedOutsideDir builds, outside every sandbox root, a real directory and a
// symlink naming it, and returns both.
func symlinkedOutsideDir(t *testing.T) (realDir, link string) {
	t.Helper()
	base := t.TempDir()
	realDir = filepath.Join(base, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(base, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	// The card names the canonical location, whatever the temp dir crosses.
	realDir, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatal(err)
	}
	return realDir, link
}

func runApprovalTurn(t *testing.T, sess *Session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second) // TRIPWIRE: scripted model adapter over a real sandboxed turn (fd-anchored grant); only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "change the file", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
}

// An approved write requested through a symlinked directory lands at the
// resolved location the card showed.
func TestE2E_FileToolEscalation_ApprovedWriteThroughSymlinkLandsAtResolvedTarget(t *testing.T) {
	realDir, link := symlinkedOutsideDir(t)
	const content = "APPROVED-THROUGH-SYMLINK"
	requested := filepath.Join(link, "target.txt")
	sess, _ := sbxRestrictedTurnSession(t, fileToolTurn("write_file", map[string]any{
		"file_path": requested, "content": content,
	})...)
	cards := make(chan events.SandboxEscalationRequestedData, 1)
	watchAndResolveCard(sess, true, func(card events.SandboxEscalationRequestedData) { cards <- card })
	runApprovalTurn(t, sess)

	card := <-cards
	if card.DeniedPath != filepath.Join(realDir, "target.txt") || card.RequestedPath != requested {
		t.Fatalf("card shows target %q requested as %q; want target %q requested as %q", card.DeniedPath, card.RequestedPath, filepath.Join(realDir, "target.txt"), requested)
	}
	got, err := os.ReadFile(filepath.Join(realDir, "target.txt"))
	if err != nil || string(got) != content {
		t.Fatalf("approved write did not land at the resolved target: data=%q err=%v\nmodel saw:\n%s", got, err, toolResultsText(sess))
	}
}

// An approved edit requested through a symlinked directory edits the file at
// the resolved location the card showed.
func TestE2E_FileToolEscalation_ApprovedEditThroughSymlinkLandsAtResolvedTarget(t *testing.T) {
	realDir, link := symlinkedOutsideDir(t)
	if err := os.WriteFile(filepath.Join(realDir, "target.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess, _ := sbxRestrictedTurnSession(t, fileToolTurn("edit_file", map[string]any{
		"file_path": filepath.Join(link, "target.txt"), "old_string": "before", "new_string": "after",
	})...)
	watchAndResolveCard(sess, true, func(events.SandboxEscalationRequestedData) {})
	runApprovalTurn(t, sess)

	got, err := os.ReadFile(filepath.Join(realDir, "target.txt"))
	if err != nil || string(got) != "after\n" {
		t.Fatalf("approved edit did not change the resolved target: data=%q err=%v\nmodel saw:\n%s", got, err, toolResultsText(sess))
	}
}

// A symlink retargeted while the card waits never redirects the approved write:
// the requested spelling no longer resolves to the approved target, so the
// write is refused and neither directory gains the file.
func TestE2E_FileToolEscalation_SymlinkSwappedAfterCardRefusesWrite(t *testing.T) {
	realDir, link := symlinkedOutsideDir(t)
	other := filepath.Join(filepath.Dir(realDir), "other")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	sess, _ := sbxRestrictedTurnSession(t, fileToolTurn("write_file", map[string]any{
		"file_path": filepath.Join(link, "target.txt"), "content": "SWAPPED",
	})...)
	swapErr := make(chan error, 1)
	watchAndResolveCard(sess, true, func(events.SandboxEscalationRequestedData) {
		err := os.Remove(link)
		if err == nil {
			err = os.Symlink(other, link)
		}
		swapErr <- err
	})
	runApprovalTurn(t, sess)
	if err := <-swapErr; err != nil {
		t.Fatalf("retarget the symlink: %v", err)
	}

	for _, dir := range []string{realDir, other} {
		if _, err := os.Lstat(filepath.Join(dir, "target.txt")); !os.IsNotExist(err) {
			t.Fatalf("a write approved for %s must not land in %s after the swap (err=%v)", realDir, dir, err)
		}
	}
	if !toolResultIsError(sess, "c1") {
		t.Fatalf("the refused write must reach the model as an error, got:\n%s", toolResultsText(sess))
	}
}

// toolResultIsError reports whether the model saw callID's result as an error.
func toolResultIsError(sess *Session, callID string) bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for _, turn := range sess.history {
		if turn.Kind != schema.TurnToolResults {
			continue
		}
		for _, part := range turn.Message.Content {
			if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID == callID {
				return part.ToolResult.IsError
			}
		}
	}
	return false
}

// An approved edit of a file outside the roots changes that file.
func TestE2E_FileToolEscalation_ApprovedEditOutsideRootsSucceeds(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "target.txt")
	if err := os.WriteFile(outside, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess, _ := sbxRestrictedTurnSession(t, fileToolTurn("edit_file", map[string]any{
		"file_path": outside, "old_string": "before", "new_string": "after",
	})...)
	watchAndResolveCard(sess, true, func(events.SandboxEscalationRequestedData) {})
	runApprovalTurn(t, sess)

	got, err := os.ReadFile(outside)
	if err != nil || string(got) != "after\n" {
		t.Fatalf("approved edit did not change the file: data=%q err=%v\nmodel saw:\n%s", got, err, toolResultsText(sess))
	}
}
