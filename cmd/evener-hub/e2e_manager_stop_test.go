package hub

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// Only model responses are scripted. In particular, the typed stop callback must
// be installed by the built CLI, not by this fixture.
func TestE2E_ManagerLocalTypedJobStop(t *testing.T) {
	for _, mode := range []string{"job", "owned_delegate"} {
		t.Run(mode, func(t *testing.T) {
			stack, provider := startE2EHubStack(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			client := stack.dialRPC(ctx, t)
			managerRef := startManagerThread(ctx, t, client, stack, "TYPED-STOP-MANAGER")
			manager := typedStopNext(ctx, t, provider)
			started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
				Harness: "evener", CWD: stack.workDir, Model: stack.model,
				LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
				Input:           []appwire.InputItem{{Type: "text", Text: "TYPED-STOP-WORKER"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			workerRef := started.Thread.Evener.Ref
			shutdown := typedStopWorkerShutdown(t, stack, workerRef)
			worker := typedStopNext(ctx, t, provider)
			if !slices.Contains(worker.Texts(), "user: TYPED-STOP-WORKER") {
				t.Fatalf("unexpected worker request: %v", worker.Texts())
			}

			jobOwner := workerRef
			jobCaller := worker
			var parent, descendant, sibling appwire.SessionDelegate
			if mode == "owned_delegate" {
				worker.RespondToolCall("delegate", map[string]any{"prompt": "TYPED-STOP-PARENT"})
				parentCall, workerRound := typedStopDelegateRounds(ctx, t, provider, "TYPED-STOP-PARENT")
				parent = typedStopDelegate(ctx, t, client, workerRef, "TYPED-STOP-PARENT")
				parentCall.RespondToolCall("delegate", map[string]any{"prompt": "TYPED-STOP-DESCENDANT"})
				jobCaller, _ = typedStopDelegateRounds(ctx, t, provider, "TYPED-STOP-DESCENDANT")
				descendant = typedStopDelegate(ctx, t, client, workerRef, "TYPED-STOP-DESCENDANT")
				workerRound.RespondToolCall("delegate", map[string]any{"prompt": "TYPED-STOP-SIBLING"})
				typedStopDelegateRounds(ctx, t, provider, "TYPED-STOP-SIBLING")
				sibling = typedStopDelegate(ctx, t, client, workerRef, "TYPED-STOP-SIBLING")
				if descendant.ParentDelegateID != parent.DelegateID || sibling.ParentDelegateID != "" || parent.Terminal || descendant.Terminal || sibling.Terminal {
					t.Fatalf("invalid live subtree fixture: parent=%+v descendant=%+v sibling=%+v", parent, descendant, sibling)
				}
				jobOwner = descendant.ChildRef
			}
			process := typedStopStartShell(ctx, t, provider, jobCaller, shutdown).process
			jobs, err := client.ThreadJobsList(ctx, appwire.SessionActivityListParams{Ref: jobOwner})
			if err != nil || len(jobs.Jobs) != 1 || jobs.Jobs[0].Terminal || jobs.Jobs[0].OwnerRef != jobOwner {
				t.Fatalf("real background shell was not admitted: jobs=%+v err=%v", jobs, err)
			}
			jobID := jobs.Jobs[0].JobID
			arguments := map[string]any{"ref": workerRef, "maxWaitMs": 10000}
			target := jobID
			if mode == "job" {
				arguments["jobId"] = jobID
			} else {
				target = parent.DelegateID
				arguments["delegateId"] = target
			}
			manager.RespondToolCall("job_stop", map[string]any{"hub": map[string]any{"mode": mode, "arguments": arguments}})
			resultRound := typedStopNext(ctx, t, provider)
			var outcome appwire.SessionOperationResponse
			if !managerToolResult(resultRound, &outcome) || outcome.Stop == nil || outcome.Stop.Job == nil || outcome.Stop.Job.JobID != target || outcome.Actor.ManagerRef != managerRef {
				var detail any
				managerToolResult(resultRound, &detail)
				t.Fatalf("bound typed %s stop did not return the target outcome: outcome=%+v detail=%+v refusal=%q", mode, outcome, detail, managerToolRefusal(resultRound))
			}
			if !waitForProcessExit(process, 5*time.Second) {
				t.Fatalf("typed %s stop returned but owned shell PID %d still exists", mode, process.Pid)
			}
			jobStatus := "cancelled"
			if mode == "owned_delegate" {
				jobStatus = "command_killed" // Descendant Close tears down its environment.
			}
			jobs, err = client.ThreadJobsList(ctx, appwire.SessionActivityListParams{Ref: jobOwner})
			if err != nil || len(jobs.Jobs) != 1 || jobs.Jobs[0].JobID != jobID || !jobs.Jobs[0].Terminal || jobs.Jobs[0].Status != jobStatus {
				t.Fatalf("owning job did not settle after %s stop: jobs=%+v err=%v", mode, jobs, err)
			}
			if mode == "owned_delegate" {
				for _, marker := range []string{"TYPED-STOP-PARENT", "TYPED-STOP-DESCENDANT"} {
					row := typedStopDelegate(ctx, t, client, workerRef, marker)
					if !row.Terminal || row.Status != "idle" || row.Outcome != "stopped" || row.Reason != "stopped_by_parent" {
						t.Fatalf("owned subtree member did not stop: %+v", row)
					}
				}
				row := typedStopDelegate(ctx, t, client, workerRef, "TYPED-STOP-SIBLING")
				if row.DelegateID != sibling.DelegateID || row.Terminal || row.Status != "running" {
					t.Fatalf("recursive stop escaped its owned subtree: %+v", row)
				}
			}
			t.Logf("%s target %s settled, shell job %s %s and PID %d gone", mode, target, jobID, jobStatus, process.Pid)
		})
	}
}

func typedStopNext(ctx context.Context, t *testing.T, provider *fakellm.Server) *fakellm.Call {
	t.Helper()
	call, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	return call
}

func typedStopDelegateRounds(ctx context.Context, t *testing.T, provider *fakellm.Server, prompt string) (child, owner *fakellm.Call) {
	t.Helper()
	for range 2 {
		call := typedStopNext(ctx, t, provider)
		if slices.Contains(call.Texts(), "user: "+prompt) {
			child = call
		} else {
			owner = call
		}
	}
	if child == nil || owner == nil {
		t.Fatalf("delegate and owner rounds missing for %q", prompt)
	}
	return child, owner
}

func typedStopDelegate(ctx context.Context, t *testing.T, client *appwire.Client, ref, task string) appwire.SessionDelegate {
	t.Helper()
	rows, err := client.ThreadDelegatesList(ctx, appwire.SessionActivityListParams{Ref: ref, Scope: appwire.SessionActivityScopeSubtree})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows.Delegates {
		if row.Task == task {
			return row
		}
	}
	t.Fatalf("delegate task %q missing: %+v", task, rows)
	return appwire.SessionDelegate{}
}

type typedStopShell struct {
	pidPath  string
	process  *os.Process
	shutdown func()
}

func typedStopStartShell(ctx context.Context, t *testing.T, provider *fakellm.Server, call *fakellm.Call, shutdown func()) *typedStopShell {
	t.Helper()
	pidPath := filepath.Join(t.TempDir(), "shell.pid")
	shell := &typedStopShell{pidPath: pidPath, shutdown: shutdown}
	// Register before admission. The Session owns cancellation, even if setup
	// fails before we capture the shell PID for the settlement check.
	t.Cleanup(func() { shell.cleanup(t) })
	call.RespondToolCall("shell", map[string]any{
		"command": fmt.Sprintf("printf '%%s\\n' $$ > %s; exec sleep 300", "'"+strings.ReplaceAll(pidPath, "'", "'\"'\"'")+"'"),
		"mode":    "background", "intent": "Starting the owned process for typed stop verification.",
	})
	pid := typedStopAwaitPID(ctx, t, pidPath)
	process, err := os.FindProcess(pid)
	if err != nil || process.Signal(syscall.Signal(0)) != nil {
		t.Fatalf("admitted shell is not alive: pid=%d err=%v", pid, err)
	}
	shell.process = process
	typedStopNext(ctx, t, provider) // Hold the owner's next model round open.
	return shell
}

func (shell *typedStopShell) cleanup(t *testing.T) {
	t.Helper()
	shell.shutdown()
	process := shell.process
	if process == nil {
		// Setup may have failed before capture. Read a completed record only to
		// observe settlement, never to acquire destructive process authority.
		pid, err := typedStopReadPID(shell.pidPath)
		if err != nil {
			t.Errorf("cannot observe shell cleanup: %v", err)
			return
		}
		if pid == 0 {
			t.Error("owned worker shutdown requested, but no complete shell PID was published to prove settlement")
			return
		}
		process, err = os.FindProcess(pid)
		if err != nil {
			t.Errorf("find admitted shell for settlement check: %v", err)
			return
		}
	}
	if !waitForProcessExit(process, 5*time.Second) {
		t.Errorf("owned shell PID %d survived Session shutdown", process.Pid)
	} else {
		t.Logf("owned Session shutdown proved shell PID %d gone", process.Pid)
	}
}

func typedStopWorkerShutdown(t *testing.T, stack hubStack, workerRef string) func() {
	t.Helper()
	shutdown := sync.OnceFunc(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		cleanup := stack.dialRPC(cleanupCtx, t)
		if _, err := clientRequest[appwire.EmptyResponse](cleanupCtx, cleanup, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: workerRef}); err != nil {
			t.Errorf("worker shutdown: %v", err)
		}
	})
	t.Cleanup(shutdown)
	return shutdown
}

func typedStopReadPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return 0, nil // The publisher has not completed its record yet.
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("invalid published PID %q: %v", data, err)
	}
	return pid, nil
}

func typedStopAwaitPID(ctx context.Context, t *testing.T, path string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		pid, err := typedStopReadPID(path)
		if err != nil {
			t.Fatal(err)
		}
		if pid != 0 {
			return pid
		}
		select {
		case <-ctx.Done():
			t.Fatalf("await complete owned shell PID: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func TestManagerTypedStopPIDPublication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shell.pid")
	publisher, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = publisher.Close() })

	// Keep the publisher open at each boundary. These synchronous reads cannot
	// race ahead to the next write or rely on scheduler timing.
	if pid, err := typedStopReadPID(path); pid != 0 || err != nil {
		t.Errorf("created but unwritten PID must remain pending: pid=%d err=%v", pid, err)
	}
	want := os.Getpid()
	if _, err := fmt.Fprint(publisher, want); err != nil {
		t.Fatal(err)
	}
	if pid, err := typedStopReadPID(path); pid != 0 || err != nil {
		t.Errorf("unterminated PID must remain pending: pid=%d err=%v", pid, err)
	}
	if _, err := fmt.Fprintln(publisher); err != nil {
		t.Fatal(err)
	}
	if err := publisher.Close(); err != nil {
		t.Fatal(err)
	}
	if pid, err := typedStopReadPID(path); pid != want || err != nil {
		t.Fatalf("completed PID publication: pid=%d want=%d err=%v", pid, want, err)
	}
}

func TestManagerTypedStopCleanupOwnership(t *testing.T) {
	for _, phase := range []string{"before_pid_capture", "before_manager_stop"} {
		t.Run(phase, func(t *testing.T) { typedStopCleanupOwnership(t, phase == "before_pid_capture") })
	}
}

func typedStopCleanupOwnership(t *testing.T, beforePIDCapture bool) {
	t.Helper()
	stack, provider := startE2EHubStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client := stack.dialRPC(ctx, t)
	started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness: "evener", CWD: stack.workDir, Model: stack.model,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
		Input:           []appwire.InputItem{{Type: "text", Text: "CLEANUP-OWNERSHIP-WORKER"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	shutdown := typedStopWorkerShutdown(t, stack, started.Thread.Evener.Ref)
	call := typedStopNext(ctx, t, provider)
	shell := typedStopStartShell(ctx, t, provider, call, shutdown)
	admittedProcess := shell.process

	sentinel := exec.Command("sleep", "300")
	if err := sentinel.Start(); err != nil {
		t.Fatal(err)
	}
	sentinelDone := make(chan struct{})
	go func() {
		_ = sentinel.Wait()
		close(sentinelDone)
	}()
	t.Cleanup(func() {
		_ = sentinel.Process.Kill() // This is our exec.Cmd's owned handle, not a reread PID.
		select {
		case <-sentinelDone:
			t.Logf("test-owned sentinel PID %d reaped", sentinel.Process.Pid)
		case <-time.After(5 * time.Second):
			t.Error("test-owned sentinel did not reap")
		}
	})
	if beforePIDCapture {
		// Model a setup failure before the fixture records the process handle.
		// The real Session and its admitted live shell are unchanged.
		shell.process = nil
	} else {
		// A stale announcement must not transfer ownership to another process.
		if err := os.WriteFile(shell.pidPath, fmt.Appendf(nil, "%d\n", sentinel.Process.Pid), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	shell.cleanup(t)
	select {
	case <-sentinelDone:
		t.Fatalf("cleanup killed test-owned sentinel %d: %v", sentinel.Process.Pid, sentinel.ProcessState)
	default:
		if err := sentinel.Process.Signal(syscall.Signal(0)); err != nil {
			t.Fatalf("test-owned sentinel %d is not alive: %v", sentinel.Process.Pid, err)
		}
	}
	if !waitForProcessExit(admittedProcess, 5*time.Second) {
		t.Fatalf("cleanup left the real Session's shell PID %d alive", admittedProcess.Pid)
	}
	t.Logf("cleanup left sentinel PID %d alive and settled owned shell PID %d", sentinel.Process.Pid, admittedProcess.Pid)
}
