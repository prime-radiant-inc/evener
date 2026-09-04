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
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// requestFullText concatenates every request message's text, tool-call
// arguments, and tool-result content so a scripted step can route on any of
// them (delegate task text lives in the assistant tool-call arguments).
//
// It includes the system prompt, so it answers "does this text appear anywhere
// in the request", never "was this delivered to me". Route the session's own
// task text on it; ask about arriving notification frames with
// requestDeliveredText instead.
func requestFullText(req llm.Request) string {
	return messagesText(req.Messages)
}

// requestDeliveredText concatenates everything the session delivered to the
// model after the opening message — the view a scripted step needs to ask
// whether a notification frame has arrived.
//
// The system prompt is text in the request like any other, so matching a wire
// frame over the whole request cannot tell a delivered frame from a prompt
// section that merely names one; that is kata zzpw, which cost two mystifying
// end-to-end failures. The prompt cannot be excluded by role: under
// SystemPromptAsUser buildModelRequest fuses it into the opening user turn,
// where it looks like any other user message. What is true in every layout is
// that buildModelRequest (agent/session_model_call.go) puts the prompt in
// message 0 and nowhere else, and a notification is never the opening message.
// So the boundary is positional, and because it is an invariant of the assembly
// rather than a property of the request,
// TestSystemPromptOccupiesOnlyTheOpeningMessage pins it: if it ever stops
// holding, that test fails loudly instead of this helper quietly skipping a real
// delivered frame.
func requestDeliveredText(req llm.Request) string {
	if len(req.Messages) == 0 {
		return ""
	}
	return messagesText(req.Messages[1:])
}

func messagesText(messages []llm.Message) string {
	var b strings.Builder
	for _, m := range messages {
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
	args, _ := json.Marshal(map[string]any{"prompt": task})
	return scriptedToolCalls(llm.ToolCallData{ID: id, Name: "delegate", Arguments: args, Type: "function"})
}

type heldShellExecutor struct {
	command          string
	output           string
	exit             int
	release          chan struct{}
	waitReturned     chan struct{}
	releaseOnce      sync.Once
	waitReturnedOnce sync.Once
}

func newHeldShellExecutor(command, output string, exit int) *heldShellExecutor {
	return &heldShellExecutor{
		command:      command,
		output:       output,
		exit:         exit,
		release:      make(chan struct{}),
		waitReturned: make(chan struct{}),
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
		// runShell invokes SignalName only after Wait returns, so this is a
		// completion barrier for the executor rather than a timing assumption.
		SignalName: func() string {
			e.waitReturnedOnce.Do(func() { close(e.waitReturned) })
			return ""
		},
	}, nil
}

func (e *heldShellExecutor) releaseShell() {
	e.releaseOnce.Do(func() { close(e.release) })
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
// against a fast local process. waitForCompletion, when supplied, blocks until
// the job manager has finalized the shell (its terminal record is durable and
// it has left the running map), not merely until the executor reported
// completion, so the drain never starts mid-finalization.
func releaseOnDrainStart(t *testing.T, release func(), waitForCompletion func(*agent.Session)) {
	t.Helper()
	oldDrainJobTree := runDrainJobTree
	runDrainJobTree = func(sess *agent.Session, ctx context.Context) (string, error) {
		release()
		if waitForCompletion != nil {
			waitForCompletion(sess)
		}
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
	releaseOnDrainStart(t, executor.releaseShell, func(sess *agent.Session) {
		<-executor.waitReturned
		awaitDurableJobCompletion(t, sess)
	})
}

// awaitDurableJobCompletion waits until every managed job the session started
// has committed a terminal record AND been released by the job manager. That is
// the state the drain has to start from, and the executor's own completion event
// does not establish it: runShell calls SignalName from the wait goroutine
// BEFORE it hands the result to finalizeShellWhenDone (agent/job_shell.go), so
// waitReturned closes while the terminal record is still uncommitted and no
// owner notification exists yet.
//
// A drain that starts inside that window sees a job that is merely running. It
// parks, arms the undisposed-background-job ladder on the pass that found it,
// and on the next recheck tick announces to the model instead of delivering the
// completion — a different turn from the one these scripts answer.
//
// The terminal record alone would keep the drain from announcing — a job with
// its terminal record written is no longer a background candidate even while
// it remains in the running map — but the barrier still waits for the running
// entry to go, so the drain starts with the completion on its way to the queue
// rather than mid-finalization. ManagedJobsFinalizedForTest is that half: the
// running entry is deleted only after the durable owner notification has been
// appended.
func awaitDurableJobCompletion(t *testing.T, sess *agent.Session) {
	t.Helper()
	// TRIPWIRE: finalization is one goroutine hop and one store append past a
	// process that has already exited, so this bound only fires when the
	// terminal record is never going to be committed at all.
	deadline := time.Now().Add(30 * time.Second)
	for {
		done, err := managedJobsTerminal(sess)
		if err != nil {
			t.Fatalf("read job activity tree: %v", err)
		}
		if done && sess.ManagedJobsFinalizedForTest() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("managed job never finished finalizing before the drain started")
		}
		time.Sleep(time.Millisecond)
	}
}

// managedJobsTerminal reports whether the session has at least one managed job
// and every one of them is terminal.
func managedJobsTerminal(sess *agent.Session) (bool, error) {
	tree, err := sess.JobActivityTree(appwire.JobsListParams{})
	if err != nil {
		return false, err
	}
	seen := false
	for _, entry := range tree.Root.Entries {
		if entry.Job == nil {
			continue
		}
		seen = true
		if !entry.Job.Terminal {
			return false, nil
		}
	}
	return seen, nil
}

// TestRunDrainsDelegatedJobTreeBeforeExit is the PRI-2441 B1 regression: a
// one-shot `evener run` whose coordinator fires a fire-and-return delegate must
// keep re-driving until the delegated work completes, instead of SIGKILLing the
// child at Close(). The coordinator's real final answer (BUILD-COMPLETE) is only
// produced on the post-completion <delegate-notification> turn, so its presence on
// stdout proves the drain ran.
func TestRunDrainsDelegatedJobTreeBeforeExit(t *testing.T) {
	delegateDrainScenario(t, nil)
}

// delegateDrainScenario drives the delegate-drain scenario and asserts the
// delegate ran to completion and its notification produced the coordinator's
// real final answer. tweak, when non-nil, adjusts the run config before the run
// so a variant can change how the request is assembled without restating the
// choreography. It returns the scripted provider so a variant can inspect the
// requests the session actually built.
func delegateDrainScenario(t *testing.T, tweak func(*runConfig)) *scriptedProvider {
	tmp := t.TempDir()
	childArtifact := filepath.Join(tmp, "child-artifact.txt")

	const (
		rootPrompt = "ROOT-BUILD-PROMPT"
		childTask  = "CHILD-TASK-WRITE the artifact"
		finalMsg   = "BUILD-COMPLETE"
	)

	step := func(req llm.Request) llm.Response {
		text := requestFullText(req)
		delivered := requestDeliveredText(req)
		isRoot := strings.Contains(text, rootPrompt)
		if isRoot {
			switch {
			case strings.Contains(delivered, "<delegate-notification"):
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
	adapter := &scriptedProvider{name: "openai", steps: steps}
	installRunScriptedProvider(t, adapter)

	var stdout, stderr bytes.Buffer
	cfg := runConfig{
		prompt:  rootPrompt + ": delegate the build to a subagent and report BUILD-COMPLETE when it finishes.",
		model:   "openai/gpt-test",
		workDir: tmp,
		verbose: true,
		stdout:  &stdout,
		stderr:  &stderr,
	}
	if tweak != nil {
		tweak(&cfg)
	}
	err := run(context.Background(), cfg)
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
	return adapter
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
			managedShellDrainScenario(t, tt.command, tt.wantStatus, nil)
		})
	}
}

// managedShellDrainScenario drives the managed-shell drain and asserts the
// terminal job notification reached the model on its own third round. tweak,
// when non-nil, adjusts the run config before the run so a variant can change
// how the request is assembled without restating the choreography. It returns
// the scripted provider so a variant can inspect the requests the session
// actually built.
func managedShellDrainScenario(t *testing.T, command, wantStatus string, tweak func(*runConfig)) *scriptedProvider {
	output := "shell-ok"
	exit := 0
	if wantStatus == "failed" {
		output = "shell-failed"
		exit = 7
	}
	installHeldRunShell(t, newHeldShellExecutor(command, output, exit))
	adapter := &scriptedProvider{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			if strings.Contains(requestDeliveredText(req), "<job-notification") {
				t.Fatal("initial request unexpectedly contained a job notification")
			}
			return scriptedToolCalls(scriptedShellCall("shell_1", command, "background"))
		},
		func(req llm.Request) llm.Response {
			if strings.Contains(requestDeliveredText(req), "<job-notification") {
				t.Fatal("tool-result request unexpectedly contained a job notification")
			}
			return scriptedCommunicate("waiting for shell")
		},
		func(req llm.Request) llm.Response {
			text := requestDeliveredText(req)
			if !strings.Contains(text, "<job-notification") || !strings.Contains(text, `job_type="shell"`) || !strings.Contains(text, `status="`+wantStatus+`"`) {
				t.Fatalf("notification request missing terminal shell status %q:\n%s", wantStatus, text)
			}
			return scriptedCommunicate("shell notification handled")
		},
	}}
	installRunScriptedProvider(t, adapter)

	var stdout, stderr bytes.Buffer
	cfg := runConfig{
		prompt:  "run the managed shell and handle its completion",
		model:   "openai/gpt-test",
		workDir: t.TempDir(),
		stdout:  &stdout,
		stderr:  &stderr,
	}
	if tweak != nil {
		tweak(&cfg)
	}
	if err := run(context.Background(), cfg); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "shell notification handled") {
		t.Fatalf("stdout = %q, want final notification-turn result", got)
	}
	if got := len(adapter.Requests()); got != 3 {
		t.Fatalf("model requests = %d, want exactly three", got)
	}
	return adapter
}

func TestRunDrainContinuesWhenNotificationTurnStartsAnotherShell(t *testing.T) {
	chainedShellDrainScenario(t, nil)
}

// chainedShellDrainScenario drives two chained managed shells and asserts the
// running count of delivered job notifications at every round. tweak, when
// non-nil, adjusts the run config before the run so a variant can change how the
// request is assembled without restating the choreography. It returns the
// scripted provider so a variant can inspect the requests the session actually
// built.
func chainedShellDrainScenario(t *testing.T, tweak func(*runConfig)) *scriptedProvider {
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
	}, nil)
	jobIDPattern := regexp.MustCompile(`job_id="([^"]+)"`)
	adapter := &scriptedProvider{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			if got := strings.Count(requestDeliveredText(req), "<job-notification"); got != 0 {
				t.Fatalf("initial request notification count = %d, want 0", got)
			}
			return scriptedToolCalls(scriptedShellCall("shell_a", shellACommand, "background"))
		},
		func(req llm.Request) llm.Response {
			if got := strings.Count(requestDeliveredText(req), "<job-notification"); got != 0 {
				t.Fatalf("shell A tool-result request notification count = %d, want 0", got)
			}
			return scriptedCommunicate("waiting for A")
		},
		func(req llm.Request) llm.Response {
			if got := strings.Count(requestDeliveredText(req), "<job-notification"); got != 1 {
				t.Fatalf("shell A notification request count = %d, want 1", got)
			}
			return scriptedToolCalls(scriptedShellCall("shell_b", "printf shell-b", "background"))
		},
		func(req llm.Request) llm.Response {
			if got := strings.Count(requestDeliveredText(req), "<job-notification"); got != 1 {
				t.Fatalf("shell B tool-result request notification count = %d, want only A's notification", got)
			}
			return scriptedCommunicate("waiting for B")
		},
		func(req llm.Request) llm.Response {
			text := requestDeliveredText(req)
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
	cfg := runConfig{
		prompt:  "run chained managed shells and handle both completions",
		model:   "openai/gpt-test",
		workDir: t.TempDir(),
		stdout:  &stdout,
		stderr:  &stderr,
	}
	if tweak != nil {
		tweak(&cfg)
	}
	if err := run(context.Background(), cfg); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}
	if got := stdout.String(); got != "all shell work complete\n" {
		t.Fatalf("stdout = %q, want only the post-B completion result", got)
	}
	if got := len(adapter.Requests()); got != 5 {
		t.Fatalf("model requests = %d, want exactly five", got)
	}
	return adapter
}
