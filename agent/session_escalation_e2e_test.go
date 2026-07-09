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

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
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
	go func() {
		for ev := range sess.Events() {
			if ev.Kind != events.EventSandboxEscalationRequested {
				continue
			}
			d, ok := ev.Data.(events.SandboxEscalationRequestedData)
			if !ok {
				continue
			}
			*sawPath = d.DeniedPath
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
		func(llm.Request) llm.Response { return toolCallResponse(call) },
		func(llm.Request) llm.Response { return finalResponse("done") },
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

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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
