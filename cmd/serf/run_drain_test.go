package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm"
)

// requestFullText concatenates every request message's text, tool-call
// arguments, and tool-result content so a scripted step can route on any of
// them (delegate task text lives in the assistant tool-call arguments; the
// re-drive turn is marked by a <job-notification> user message).
func requestFullText(req llm.Request) string {
	var b strings.Builder
	for _, m := range req.Messages {
		b.WriteString(m.Text())
		b.WriteByte('\n')
		for _, p := range m.Content {
			if p.ToolCall != nil {
				b.Write(p.ToolCall.Arguments)
				b.WriteByte('\n')
			}
			if p.ToolResult != nil {
				fmt.Fprint(&b, p.ToolResult.Content)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

func scriptedDelegateCall(id, task string) llm.Response {
	args, _ := json.Marshal(map[string]any{"task": task})
	return scriptedToolCalls(llm.ToolCallData{ID: id, Name: "delegate", Arguments: args, Type: "function"})
}

type heldShellExecutor struct {
	command string
	output  string
	exit    int
	release chan struct{}
	once    sync.Once
}

func newHeldShellExecutor(command, output string, exit int) *heldShellExecutor {
	return &heldShellExecutor{
		command: command,
		output:  output,
		exit:    exit,
		release: make(chan struct{}),
	}
}

func (e *heldShellExecutor) StreamCommand(_ context.Context, command, _ string, _ map[string]string, out io.Writer) (*execenv.StreamHandle, error) {
	if command != e.command {
		return nil, fmt.Errorf("shell command = %q, want %q", command, e.command)
	}
	return &execenv.StreamHandle{
		Wait: func() (int, error) {
			<-e.release
			_, _ = io.WriteString(out, e.output)
			return e.exit, nil
		},
		Signal: e.releaseShell,
	}, nil
}

func (e *heldShellExecutor) releaseShell() {
	e.once.Do(func() { close(e.release) })
}

type shellExecutorEnvironment struct {
	execenv.ExecutionEnvironment
	executor execenv.StreamingExecutor
}

func (e *shellExecutorEnvironment) StreamCommand(ctx context.Context, command, workingDir string, envVars map[string]string, out io.Writer) (*execenv.StreamHandle, error) {
	return e.executor.StreamCommand(ctx, command, workingDir, envVars, out)
}

// releaseOnDrainStart holds a background shell's completion until the one-shot
// drain begins: a managed job that finishes while its own tool batch is still
// running makes the session yield so the completion is delivered as a
// notification turn (session_lifecycle.go post-tool seam), which would fold the
// notification into the tool-result request and race these scripted steps
// against a fast local process.
func releaseOnDrainStart(t *testing.T, release func()) {
	t.Helper()
	oldDrainJobTree := runDrainJobTree
	runDrainJobTree = func(sess *agent.Session, ctx context.Context) (string, error) {
		release()
		return oldDrainJobTree(sess, ctx)
	}
	t.Cleanup(func() {
		release()
		runDrainJobTree = oldDrainJobTree
	})
}

func installHeldRunShell(t *testing.T, executor *heldShellExecutor) {
	t.Helper()
	oldNewSession := runNewSession
	runNewSession = func(client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
		return oldNewSession(client, profile, &shellExecutorEnvironment{ExecutionEnvironment: env, executor: executor}, cfg)
	}
	t.Cleanup(func() { runNewSession = oldNewSession })
	releaseOnDrainStart(t, executor.releaseShell)
}

// TestRunDrainsDelegatedJobTreeBeforeExit is the PRI-2441 B1 regression: a
// one-shot `serf run` whose coordinator fires a fire-and-return delegate must
// keep re-driving until the delegated work completes, instead of SIGKILLing the
// child at Close(). The coordinator's real final answer (BUILD-COMPLETE) is only
// produced on the post-completion <delegate-notification> turn, so its presence on
// stdout proves the drain ran.
func TestRunDrainsDelegatedJobTreeBeforeExit(t *testing.T) {
	tmp := t.TempDir()
	childArtifact := filepath.Join(tmp, "child-artifact.txt")

	const (
		rootPrompt = "ROOT-BUILD-PROMPT"
		childTask  = "CHILD-TASK-WRITE the artifact"
		finalMsg   = "BUILD-COMPLETE"
	)

	step := func(req llm.Request) llm.Response {
		text := requestFullText(req)
		isRoot := strings.Contains(text, rootPrompt)
		if isRoot {
			switch {
			case strings.Contains(text, "<delegate-notification"):
				// The delegate finished and its completion was drained back to the
				// coordinator: emit the real final answer.
				return scriptedCommunicate(finalMsg)
			case strings.Contains(text, "CHILD-TASK-WRITE"):
				// Delegate already dispatched; end the coordinator's turn while the
				// child runs (this is where ProcessInput returns and Close() would
				// otherwise kill the child).
				return scriptedCommunicate("waiting on delegate")
			default:
				return scriptedDelegateCall("del_1", childTask)
			}
		}
		// Child session: write the artifact, then report done.
		if _, err := os.Stat(childArtifact); err != nil {
			return scriptedToolCalls(scriptedWriteFileCall("cw_1", childArtifact, "artifact"))
		}
		return scriptedCommunicate("child wrote artifact")
	}

	// One shared step function serves both root and child turns; supply plenty of
	// slots so neither session is starved.
	steps := make([]func(llm.Request) llm.Response, 0, 16)
	for range 16 {
		steps = append(steps, step)
	}
	installRunScriptedProvider(t, &scriptedProvider{name: "openai", steps: steps})

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		prompt:  rootPrompt + ": delegate the build to a subagent and report BUILD-COMPLETE when it finishes.",
		model:   "openai/gpt-test",
		workDir: tmp,
		verbose: true,
		stdout:  &stdout,
		stderr:  &stderr,
	})
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}

	if !strings.Contains(stdout.String(), finalMsg) {
		t.Fatalf("expected stdout to contain %q (proves the delegate completion was drained); got stdout=%q", finalMsg, stdout.String())
	}
	if _, statErr := os.Stat(childArtifact); statErr != nil {
		t.Fatalf("expected child artifact %s to exist (child ran to completion, was not killed); stat err: %v", childArtifact, statErr)
	}
	if strings.Contains(stderr.String(), "stopped_by_parent") {
		t.Fatalf("child was SIGKILLed by Close() (stopped_by_parent) instead of draining to completion; stderr=%s", stderr.String())
	}
}

func TestRunDrainsManagedShellBeforeExit(t *testing.T) {
	tests := []struct {
		name       string
		command    string
		wantStatus string
	}{
		{name: "completed", command: "printf shell-ok", wantStatus: "completed"},
		{name: "failed", command: "printf shell-failed >&2; exit 7", wantStatus: "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := "shell-ok"
			exit := 0
			if tt.wantStatus == "failed" {
				output = "shell-failed"
				exit = 7
			}
			installHeldRunShell(t, newHeldShellExecutor(tt.command, output, exit))
			adapter := &scriptedProvider{name: "openai", steps: []func(llm.Request) llm.Response{
				func(req llm.Request) llm.Response {
					if strings.Contains(requestFullText(req), "<job-notification") {
						t.Fatal("initial request unexpectedly contained a job notification")
					}
					return scriptedToolCalls(scriptedShellCall("shell_1", tt.command, "background"))
				},
				func(req llm.Request) llm.Response {
					if strings.Contains(requestFullText(req), "<job-notification") {
						t.Fatal("tool-result request unexpectedly contained a job notification")
					}
					return scriptedCommunicate("waiting for shell")
				},
				func(req llm.Request) llm.Response {
					text := requestFullText(req)
					if !strings.Contains(text, "<job-notification") || !strings.Contains(text, `job_type="shell"`) || !strings.Contains(text, `status="`+tt.wantStatus+`"`) {
						t.Fatalf("notification request missing terminal shell status %q:\n%s", tt.wantStatus, text)
					}
					return scriptedCommunicate("shell notification handled")
				},
			}}
			installRunScriptedProvider(t, adapter)

			var stdout, stderr bytes.Buffer
			err := run(context.Background(), runConfig{
				prompt:  "run the managed shell and handle its completion",
				model:   "openai/gpt-test",
				workDir: t.TempDir(),
				stdout:  &stdout,
				stderr:  &stderr,
			})
			if err != nil {
				t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
			}
			if got := stdout.String(); !strings.Contains(got, "shell notification handled") {
				t.Fatalf("stdout = %q, want final notification-turn result", got)
			}
			if got := len(adapter.Requests()); got != 3 {
				t.Fatalf("model requests = %d, want exactly three", got)
			}
		})
	}
}

func TestRunDrainContinuesWhenNotificationTurnStartsAnotherShell(t *testing.T) {
	// Shell A is gated so it cannot reach terminal completion until the drain
	// starts; otherwise its notification can beat the tool-result model round
	// and be folded into that request. Shell B needs no gate: it is launched
	// from A's notification turn, and a notification turn owns its current
	// batch (no post-tool yield), so B's completion always waits for the next
	// drain wake.
	gate := filepath.Join(t.TempDir(), "release-shell-a")
	shellACommand := "while [ ! -f " + gate + " ]; do sleep 0.02; done; printf shell-a"
	releaseOnDrainStart(t, func() {
		if err := os.WriteFile(gate, []byte("go\n"), 0o600); err != nil {
			t.Errorf("release shell A: %v", err)
		}
	})
	jobIDPattern := regexp.MustCompile(`job_id="([^"]+)"`)
	adapter := &scriptedProvider{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			if got := strings.Count(requestFullText(req), "<job-notification"); got != 0 {
				t.Fatalf("initial request notification count = %d, want 0", got)
			}
			return scriptedToolCalls(scriptedShellCall("shell_a", shellACommand, "background"))
		},
		func(req llm.Request) llm.Response {
			if got := strings.Count(requestFullText(req), "<job-notification"); got != 0 {
				t.Fatalf("shell A tool-result request notification count = %d, want 0", got)
			}
			return scriptedCommunicate("waiting for A")
		},
		func(req llm.Request) llm.Response {
			if got := strings.Count(requestFullText(req), "<job-notification"); got != 1 {
				t.Fatalf("shell A notification request count = %d, want 1", got)
			}
			return scriptedToolCalls(scriptedShellCall("shell_b", "printf shell-b", "background"))
		},
		func(req llm.Request) llm.Response {
			if got := strings.Count(requestFullText(req), "<job-notification"); got != 1 {
				t.Fatalf("shell B tool-result request notification count = %d, want only A's notification", got)
			}
			return scriptedCommunicate("waiting for B")
		},
		func(req llm.Request) llm.Response {
			text := requestFullText(req)
			if got := strings.Count(text, "<job-notification"); got != 2 {
				t.Fatalf("shell B notification request count = %d, want A and B", got)
			}
			matches := jobIDPattern.FindAllStringSubmatch(text, -1)
			if len(matches) != 2 || matches[0][1] == matches[1][1] {
				t.Fatalf("notification job ids = %v, want two distinct ids", matches)
			}
			return scriptedCommunicate("all shell work complete")
		},
	}}
	installRunScriptedProvider(t, adapter)

	var stdout, stderr bytes.Buffer
	err := run(context.Background(), runConfig{
		prompt:  "run chained managed shells and handle both completions",
		model:   "openai/gpt-test",
		workDir: t.TempDir(),
		stdout:  &stdout,
		stderr:  &stderr,
	})
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}
	if got := stdout.String(); got != "all shell work complete\n" {
		t.Fatalf("stdout = %q, want only the post-B completion result", got)
	}
	if got := len(adapter.Requests()); got != 5 {
		t.Fatalf("model requests = %d, want exactly five", got)
	}
}
