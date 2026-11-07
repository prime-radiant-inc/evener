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
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// Only model responses are scripted. In particular, the typed stop callback must
// be installed by the built CLI, not by this fixture.
func TestE2E_ManagerLocalTypedJobStop(t *testing.T) {
	for _, mode := range []string{"job", "owned_delegate"} {
		t.Run(mode, func(t *testing.T) {
			// Each mode builds its own provider, hub and session tree and
			// reaches no process-wide state, so the two stops overlap instead
			// of paying two full stack startups in sequence.
			t.Parallel()
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

			// Both modes stop delegate-owned work inside a live subtree: job
			// mode targets a direct delegate's shell through the session that
			// owns it, owned_delegate mode stops the root's owned subtree with
			// a deeper-owned shell.
			worker.RespondToolCall("delegate", map[string]any{"prompt": "TYPED-STOP-PARENT"})
			parentCall, workerRound := typedStopDelegateRounds(ctx, t, provider, "TYPED-STOP-PARENT")
			parent := typedStopDelegate(ctx, t, client, workerRef, "TYPED-STOP-PARENT")
			shellCaller, jobOwner := parentCall, parent.ChildRef
			var descendant, sibling appwire.SessionDelegate
			if mode == "owned_delegate" {
				parentCall.RespondToolCall("delegate", map[string]any{"prompt": "TYPED-STOP-DESCENDANT"})
				descendantCall, _ := typedStopDelegateRounds(ctx, t, provider, "TYPED-STOP-DESCENDANT")
				descendant = typedStopDelegate(ctx, t, client, workerRef, "TYPED-STOP-DESCENDANT")
				if descendant.ParentDelegateID != parent.DelegateID || parent.Terminal || descendant.Terminal {
					t.Fatalf("invalid live subtree fixture: parent=%+v descendant=%+v", parent, descendant)
				}
				shellCaller, jobOwner = descendantCall, descendant.ChildRef
				workerRound.RespondToolCall("delegate", map[string]any{"prompt": "TYPED-STOP-SIBLING"})
				typedStopDelegateRounds(ctx, t, provider, "TYPED-STOP-SIBLING")
				sibling = typedStopDelegate(ctx, t, client, workerRef, "TYPED-STOP-SIBLING")
				if sibling.ParentDelegateID != "" || sibling.Terminal {
					t.Fatalf("invalid live subtree fixture: sibling=%+v", sibling)
				}
			} else {
				// The worker's open round stays unanswered: the shared
				// provider is FIFO, so a round consumed here would be mistaken
				// for the Manager's result.
				_ = workerRound
			}
			process := typedStopStartShell(ctx, t, provider, shellCaller, shutdown).process
			jobs, err := client.ThreadJobsList(ctx, appwire.SessionActivityListParams{Ref: jobOwner})
			if err != nil || len(jobs.Jobs) != 1 || jobs.Jobs[0].Terminal || jobs.Jobs[0].OwnerRef != jobOwner {
				t.Fatalf("real background shell was not admitted: jobs=%+v err=%v", jobs, err)
			}
			jobID := jobs.Jobs[0].JobID
			// The hub's roster admits a fresh delegate alias asynchronously,
			// and the stop resolves the addressed session through that roster:
			// await the alias with the same activity read the stop uses.
			aliasDeadline := time.Now().Add(30 * time.Second)
			for {
				_, err := clientRequest[appwire.SessionActivitySummary](ctx, client, appwire.MethodEvenerThreadActivityRead, appwire.SessionActivityReadParams{Ref: jobOwner})
				if err == nil {
					break
				}
				if time.Now().After(aliasDeadline) {
					t.Fatalf("descendant alias never became addressable at the local hub: %v", err)
				}
				time.Sleep(200 * time.Millisecond)
			}
			arguments := map[string]any{"maxWaitMs": 10000}
			target := jobID
			if mode == "job" {
				// The job mode addresses the session that owns the shell.
				arguments["ref"] = jobOwner
				arguments["jobId"] = jobID
			} else {
				arguments["ref"] = workerRef
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

// TestE2E_ManagerRemoteDescendantJobStop sends stop through a controller hub to
// a real SSH-hosted hub. A direct delegate of the remote root owns the shell
// job; the Manager addresses that delegate.
func TestE2E_ManagerRemoteDescendantJobStop(t *testing.T) {
	if os.Getenv("EVENER_SSH_E2E") != "1" || os.Getenv("EVENER_SSH_E2E_DEPLOY") != "1" {
		t.Skip("requires EVENER_SSH_E2E=1 EVENER_SSH_E2E_DEPLOY=1 and the disposable localhost SSH fixture")
	}
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs two hubs + daemon")
	}
	probe, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if err := exec.CommandContext(probe, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=3", "localhost", "true").Run(); err != nil {
		t.Fatalf("required remote host unavailable: %v", err)
	}
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	local, controller, host, hostDir, addr, remoteHost := startManagerAcceptanceSSHFixture(ctx, t, provider)
	managerRef := startManagerThread(ctx, t, controller, local, "REMOTE-STOP-MANAGER")
	managerCall := typedStopNext(ctx, t, provider)
	if !slices.Contains(managerCall.Texts(), "user: REMOTE-STOP-MANAGER") {
		t.Fatalf("unexpected Manager request: %v", managerCall.Texts())
	}
	managerCall.RespondToolCall("model_list", map[string]any{"hub": map[string]any{"host": remoteHost, "refresh": true}})
	managerCall = typedStopNext(ctx, t, provider)
	var modelResult appwire.SessionOperationResponse
	if !managerToolResult(managerCall, &modelResult) || modelResult.Models == nil {
		t.Fatalf("remote model refresh failed: %v", managerCall.Texts())
	}
	selectedModel := ""
	for _, model := range modelResult.Models.Data {
		if model.Provider == "fake" && model.Model == fakellm.ModelID {
			selectedModel = model.Provider + "/" + model.Model
			break
		}
	}
	if selectedModel == "" {
		t.Fatalf("fake model missing from remote catalog: %+v", modelResult.Models.Data)
	}
	token, err := os.ReadFile(hostDir + "/state/auth-token")
	if err != nil {
		t.Fatal(err)
	}
	remoteStack := hubStack{addr: addr, token: strings.TrimSpace(string(token)), workDir: hostDir + "/workspace", home: hostDir + "/home"}
	remoteClient := remoteStack.dialRPC(ctx, t)
	remoteStack.model = selectedModel
	started, err := clientRequest[appwire.ThreadStartResponse](ctx, remoteClient, appwire.MethodThreadStart, appwire.ThreadStartParams{
		Harness: "evener", CWD: remoteStack.workDir, Model: remoteStack.model,
		LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off"},
		Input:           []appwire.InputItem{{Type: "text", Text: "REMOTE-STOP-WORKER"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	workerRef := started.Thread.Evener.Ref
	typedStopWorkerShutdown(t, remoteStack, workerRef)
	worker := typedStopNext(ctx, t, provider)
	if !slices.Contains(worker.Texts(), "user: REMOTE-STOP-WORKER") {
		t.Fatalf("unexpected remote worker request: %v", worker.Texts())
	}
	worker.RespondToolCall("delegate", map[string]any{"prompt": "REMOTE-STOP-PARENT"})
	parentCall, workerRound := typedStopDelegateRounds(ctx, t, provider, "REMOTE-STOP-PARENT")
	parent := typedStopDelegate(ctx, t, remoteClient, workerRef, "REMOTE-STOP-PARENT")
	// Store the PID on the remote host so the test proves remote process settlement.
	pidPath := hostDir + "/workspace/manager-stop.pid"
	parentCall.RespondToolCall("shell", map[string]any{
		"command": fmt.Sprintf("printf '%%s\\n' $$ > %s; exec sleep 300", shellquote.RemoteWord(pidPath)),
		"mode":    "background", "intent": "Starting the owned process for typed stop verification.",
	})
	// Hold the delegate's next model round open, exactly as the local stop
	// fixture holds the owner's: the shared provider is FIFO, so this
	// unconsumed round would otherwise be mistaken for the Manager's result.
	parentContinuation := typedStopNext(ctx, t, provider)
	_ = parentContinuation
	pidBytes, err := host.runStdout("for i in $(seq 1 300); do if test -s " + shellquote.RemoteWord(pidPath) + "; then cat " + shellquote.RemoteWord(pidPath) + "; exit 0; fi; sleep .1; done; exit 1")
	if err != nil {
		t.Fatalf("await remote shell PID: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil || pid < 1 {
		t.Fatalf("invalid remote shell PID %q: %v", pidBytes, err)
	}
	if _, err := host.run("kill -0 " + strconv.Itoa(pid)); err != nil {
		t.Fatalf("admitted remote shell is not alive: pid=%d err=%v", pid, err)
	}
	// The Manager addresses the owning delegate in the remote host's namespace.
	parentID, err := appwire.ParseRef(parent.ChildRef)
	if err != nil {
		t.Fatal(err)
	}
	controllerParentRef := (appwire.Ref{SourceID: remoteHost, ThreadID: parentID.ThreadID}).String()
	jobs, err := remoteClient.ThreadJobsList(ctx, appwire.SessionActivityListParams{Ref: workerRef, Scope: appwire.SessionActivityScopeSubtree})
	var parentJob *appwire.JobActivityJob
	for i := range jobs.Jobs {
		if jobs.Jobs[i].OwnerSessionID == parentID.ThreadID && !jobs.Jobs[i].Terminal {
			parentJob = &jobs.Jobs[i]
			break
		}
	}
	if err != nil || parentJob == nil {
		t.Fatalf("real delegate shell was not admitted: parent=%+v jobs=%+v err=%v", parent, jobs, err)
	}
	// The hosting hub's roster admits a fresh delegate alias asynchronously,
	// and the stop addresses that alias: await it through the same activity
	// read the stop control resolves, so the stop does not race the roster.
	aliasDeadline := time.Now().Add(30 * time.Second)
	for {
		_, err := clientRequest[appwire.SessionActivitySummary](ctx, controller, appwire.MethodEvenerThreadActivityRead, appwire.SessionActivityReadParams{Ref: controllerParentRef})
		if err == nil {
			break
		}
		if time.Now().After(aliasDeadline) {
			t.Fatalf("descendant alias never became addressable at the hosting hub: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	// workerRound is intentionally held open just like the owner's model round in
	// the local stop fixture; its pending delegate call is not part of this stop.
	_ = workerRound
	managerCall.RespondToolCall("job_stop", map[string]any{"hub": map[string]any{"mode": "job", "arguments": map[string]any{"ref": controllerParentRef, "jobId": parentJob.JobID, "maxWaitMs": 10000}}})
	resultRound := typedStopNext(ctx, t, provider)
	var stopOutcome appwire.SessionOperationResponse
	if !managerToolResult(resultRound, &stopOutcome) || stopOutcome.Stop == nil || stopOutcome.Stop.Job == nil || stopOutcome.Stop.Job.JobID != parentJob.JobID || stopOutcome.Actor.ManagerRef != managerRef {
		var detail any
		managerToolResult(resultRound, &detail)
		t.Fatalf("remote descendant job stop did not return the target outcome: outcome=%+v detail=%+v refusal=%q texts=%q", stopOutcome, detail, managerToolRefusal(resultRound), resultRound.Texts())
	}
	settled, err := host.run("for i in $(seq 1 50); do if ! kill -0 " + strconv.Itoa(pid) + " 2>/dev/null; then exit 0; fi; sleep .1; done; exit 1")
	if err != nil {
		t.Fatalf("remote descendant stop returned but PID %d did not exit: %s", pid, settled)
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
	if err != nil {
		return 0, fmt.Errorf("invalid published PID %q: %w", data, err)
	}
	if pid <= 0 {
		return 0, fmt.Errorf("invalid published PID %q", data)
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
		t.Run(phase, func(t *testing.T) {
			// Each phase builds its own provider, hub and session tree, so the
			// two reads of the same cleanup window can overlap.
			t.Parallel()
			typedStopCleanupOwnership(t, phase == "before_pid_capture")
		})
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
