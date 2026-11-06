package hub

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// Only provider decisions are scripted. The delegate, installed environment,
// worktree transitions, daemon projection and both serving paths are real.
type childDocumentAction struct {
	tool string
	args map[string]any
}
type childDocumentReady struct{ id, toolError string }
type childDocumentAdapter struct {
	rootID    string
	isolation string
	spawned   bool
	ready     chan childDocumentReady
	actions   chan childDocumentAction
}

func (*childDocumentAdapter) Name() string { return "openai" }
func (*childDocumentAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
func (a *childDocumentAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if req.ResponseFormat != nil || req.SessionID == "" {
		return llm.Response{Message: llm.Assistant(`{"name":"child documents"}`)}, nil
	}
	if req.SessionID == a.rootID {
		if !a.spawned {
			a.spawned = true
			// A non-isolated coordinator may manage worktrees. A leaf may not;
			// an isolated coordinator has only the dispose operation.
			args := map[string]any{"prompt": "exercise child documents", "delegation_allowance": 1}
			if a.isolation == "worktree" {
				args["isolation"] = a.isolation
			}
			return activityRelayTool("delegate", args), nil
		}
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	}
	ready := childDocumentReady{id: req.SessionID}
	if len(req.Messages) > 0 {
		for _, part := range req.Messages[len(req.Messages)-1].Content {
			if part.ToolResult != nil && part.ToolResult.IsError {
				ready.toolError = fmt.Sprint(part.ToolResult.Content)
			}
		}
	}
	select {
	case a.ready <- ready:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	select {
	case action := <-a.actions:
		return activityRelayTool(action.tool, action.args), nil
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
}

func TestDocumentWorktree_ChildCurrentDirectory(t *testing.T) {
	for _, isolation := range []string{"none", "worktree"} {
		t.Run(isolation, func(t *testing.T) {
			adapter := &childDocumentAdapter{isolation: isolation, ready: make(chan childDocumentReady), actions: make(chan childDocumentAction)}
			f := newWorktreeDocumentFixture(t, adapter)
			adapter.rootID = f.sessionID
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			done := make(chan error, 1)
			go func() { _, err := f.session.ProcessInput(ctx, "spawn document child", nil); done <- err }()
			t.Cleanup(func() { cancel(); <-done })
			await := func() string {
				t.Helper()
				select {
				case ready := <-adapter.ready:
					if ready.toolError != "" {
						t.Fatalf("real child tool failed: %s", ready.toolError)
					}
					return ready.id
				case <-ctx.Done():
					t.Fatal(ctx.Err())
					return ""
				}
			}
			childID := await()
			action := func(tool string, args map[string]any) {
				t.Helper()
				select {
				case adapter.actions <- childDocumentAction{tool, args}:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if got := await(); got != childID {
					t.Fatalf("child changed identity: %s", got)
				}
				t.Logf("child coordinator %s completed %s %v", childID, tool, args)
			}
			readCWD := func() string {
				t.Helper()
				read, err := f.daemonClient.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:" + childID})
				if err != nil || read.Thread.ID != childID || read.Thread.CWD == "" {
					t.Fatalf("direct child read: %+v, %v", read.Thread, err)
				}
				return read.Thread.CWD
			}
			initial := readCWD()
			if (isolation == "none" && initial != f.root) || (isolation == "worktree" && initial == f.root) {
				t.Fatalf("isolation %s child root=%q parent=%q", isolation, initial, f.root)
			}
			verify := func(phase int) string {
				t.Helper()
				// pwd is executed by the child's installed execution environment, not
				// supplied by an event or by the test's current-directory expectation.
				body := fmt.Sprintf("child document phase %d", phase)
				action("exec_command", map[string]any{"command": fmt.Sprintf("pwd > actual-cwd.txt; printf '%%s' '%s' > child.md", body), "mode": "foreground"})
				cwd := readCWD()
				actual, err := os.ReadFile(filepath.Join(cwd, "actual-cwd.txt"))
				if err != nil || strings.TrimSpace(string(actual)) != cwd {
					t.Fatalf("projection cwd=%q actual pwd=%q err=%v", cwd, actual, err)
				}
				t.Logf("phase=%d child=%s sourceCWD=%q actualPwd=%q parentCWD=%q", phase, childID, cwd, strings.TrimSpace(string(actual)), f.root)
				for name, target := range map[string]struct{ url, id string }{"local": {f.documentURL, childID}, "remote": {f.remoteDocumentURL, "h1:" + childID}} {
					status, got := readWorktreeDocumentHTTP(t, target.url, target.id, "child.md")
					t.Logf("phase=%d %s read status=%d body=%q", phase, name, status, got)
					if status != http.StatusOK || string(got) != body {
						t.Fatalf("%s child file = %d %q, want %q", name, status, got, body)
					}
				}
				root, err := f.daemonClient.ThreadRead(ctx, appwire.ThreadReadParams{Ref: f.ref})
				if err != nil || root.Thread.CWD != f.root {
					t.Fatalf("child mutated root cwd: %+v, %v", root.Thread, err)
				}
				return cwd
			}
			verify(0)
			if isolation == "none" {
				action("manage_worktree", map[string]any{"operation": "create", "name": "child-documents"})
				changed := verify(1)
				if changed == initial {
					t.Fatal("worktree create did not move child")
				}
				for _, target := range []struct{ url, id string }{{f.documentURL, childID}, {f.remoteDocumentURL, "h1:" + childID}} {
					status, _ := readWorktreeDocumentHTTP(t, target.url, target.id, filepath.Join(initial, "child.md"))
					t.Logf("old absolute root session=%s path=%q status=%d", target.id, filepath.Join(initial, "child.md"), status)
					if status != http.StatusForbidden {
						t.Fatalf("old child root absolute path = %d, want forbidden", status)
					}
				}
				action("manage_worktree", map[string]any{"operation": "exit"})
				if got := verify(2); got != initial {
					t.Fatalf("exit cwd=%q want %q", got, initial)
				}
			} else {
				t.Log("isolated coordinator remains in its spawn lane, create/exit are not supported")
			}
		})
	}
}
