# Detached Shell Jobs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the shell tool's background boolean with a `foreground | background | detached` mode and implement detached commands that return a PID, create no Evener job, and survive session cleanup.

**Architecture:** Keep foreground and managed-background execution on the existing `runShell` path. Add a narrow optional `execenv.DetachedExecutor` capability for immediate disownership; the shell handler dispatches `mode: "detached"` directly to that capability without touching `jobManager`. The local Unix implementation starts a new session with all standard streams connected to the null device, never enrolls the process in `runningPIDs`, and asynchronously reaps it while Evener remains alive; unsupported platforms and enforced sandboxes reject before launch.

**Tech Stack:** Go, `os/exec`, platform-specific `syscall.SysProcAttr`, Evener tool registry and execution-environment interfaces, scripted-provider and process-plumbing tests.

## Global Constraints

- Read `docs/testing.md` before changing tests; default tests must remain deterministic and must not depend on provider credentials, network access, quota, or ambient model behavior.
- `mode` is exactly `foreground`, `background`, or `detached`; omitted mode means `foreground`.
- The previous model-facing `background` boolean is removed; backward compatibility is explicitly out of scope.
- Detached execution returns a positive PID and no `job_id`, transcript, notification, or job-store record.
- Detached stdin, stdout, and stderr must not retain session/client descriptors.
- Detached processes must never enter `jobManager.running` or `LocalExecutionEnvironment.runningPIDs`.
- Later sessions must not discover or control detached processes.
- Unsupported platforms or enforced sandbox environments reject detached execution before starting a process.
- Do not add readiness, health checks, restart, reconciliation, history, or service-specific tools.

---

### Task 1: Replace the shell background boolean with an execution mode

**Files:**
- Modify: `agent/internal/tool/definitions.go`
- Modify: `agent/internal/tool/definitions_test.go`
- Modify: `agent/job_shell.go`
- Modify: `agent/session_tools_shell.go`
- Modify: `agent/session_tools_shell_test.go`

**Interfaces:**
- Produces: `type shellMode string` with `shellModeForeground`, `shellModeBackground`, and `shellModeDetached`.
- Produces: `parseShellMode(args map[string]any) (shellMode, error)`.
- Produces: `shellArgs.Mode shellMode`; the existing internal `Background` and `RunningInBackground` booleans remain implementation details of the managed-job path.
- Produces: model-facing shell request property `mode` and result property `mode`; removes the shell request property `background` and shell result property `running_in_background`.

- [ ] **Step 1: Update schema tests to require the enum and reject the old boolean surface**

In `agent/internal/tool/definitions_test.go`, replace the assertions that require a boolean `background` property with:

```go
func TestDefShellHasExecutionMode(t *testing.T) {
	props := DefShell().Parameters["properties"].(map[string]any)
	mode, ok := props["mode"].(map[string]any)
	if !ok {
		t.Fatal("DefShell missing mode property")
	}
	if got := mode["type"]; got != "string" {
		t.Fatalf("mode type = %v, want string", got)
	}
	want := []any{"foreground", "background", "detached"}
	if got := mode["enum"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("mode enum = %#v, want %#v", got, want)
	}
	if _, exists := props["background"]; exists {
		t.Fatal("legacy background property is still exposed")
	}
}
```

Add `reflect` to the test imports if it is not already present.

- [ ] **Step 2: Add parser tests for omitted, valid, invalid, and wrongly typed modes**

In `agent/session_tools_shell_test.go`, add table tests that call `parseShellToolArgs`:

```go
func TestParseShellToolArgsMode(t *testing.T) {
	tests := []struct {
		name    string
		args    map[string]any
		want    shellMode
		wantErr string
	}{
		{name: "omitted", args: map[string]any{"command": "true"}, want: shellModeForeground},
		{name: "foreground", args: map[string]any{"command": "true", "mode": "foreground"}, want: shellModeForeground},
		{name: "background", args: map[string]any{"command": "true", "mode": "background"}, want: shellModeBackground},
		{name: "detached", args: map[string]any{"command": "true", "mode": "detached"}, want: shellModeDetached},
		{name: "unknown", args: map[string]any{"command": "true", "mode": "daemon"}, wantErr: `mode must be one of "foreground", "background", or "detached"`},
		{name: "boolean", args: map[string]any{"command": "true", "mode": true}, wantErr: `mode must be one of "foreground", "background", or "detached"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseShellToolArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got.Mode != tt.want {
				t.Fatalf("parse = (%q, %v), want (%q, nil)", got.Mode, err, tt.want)
			}
		})
	}
}
```

- [ ] **Step 3: Run the focused tests and confirm the expected failures**

Run:

```bash
go test ./agent/internal/tool -run TestDefShellHasExecutionMode -count=1
go test ./agent -run TestParseShellToolArgsMode -count=1
```

Expected: FAIL because `mode`, `shellMode`, and `shellArgs.Mode` do not exist and `background` remains in the schema.

- [ ] **Step 4: Implement the request mode and route existing managed execution through it**

In `agent/job_shell.go`, add:

```go
type shellMode string

const (
	shellModeForeground shellMode = "foreground"
	shellModeBackground shellMode = "background"
	shellModeDetached   shellMode = "detached"
)
```

Add `Mode shellMode` to `shellArgs` and retain `Background bool` as an internal managed-execution flag. After parsing, set `Background = Mode == shellModeBackground`. This keeps `runShell`, its fuzz coverage, and nested-job internals unchanged. Do not route `shellModeDetached` into `runShell`.

In `agent/session_tools_shell.go`, parse mode strictly:

```go
func parseShellMode(args map[string]any) (shellMode, error) {
	raw, exists := args["mode"]
	if !exists {
		return shellModeForeground, nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", errors.New(`mode must be one of "foreground", "background", or "detached"`)
	}
	switch shellMode(value) {
	case shellModeForeground, shellModeBackground, shellModeDetached:
		return shellMode(value), nil
	default:
		return "", errors.New(`mode must be one of "foreground", "background", or "detached"`)
	}
}
```

Set `shellArgs.Mode` from this helper. Remove `shellBoolArg` if no other caller remains.

In `agent/internal/tool/definitions.go`, replace `background` with:

```go
"mode": map[string]any{
	"type": "string",
	"enum": []any{"foreground", "background", "detached"},
	"description": "foreground (default) waits inline, background creates a session-owned job, and detached starts an unmanaged process that survives this Evener session and returns only its PID.",
},
```

- [ ] **Step 5: Replace shell result serialization with `mode`**

Change `shellToolResult` to contain:

```go
Mode string `json:"mode"`
```

and remove its public `RunningInBackground` field. Keep `shellResult.RunningInBackground` internal. In `marshalShellToolResult`, derive the public mode as `background` when `res.RunningInBackground` is true and `foreground` otherwise. `marshalCompleteOrHandleResult` always emits `foreground` for inline completion and changes to `background` only when it keeps a promoted durable handle. Update assertions in `agent/session_tools_shell_test.go`; do not modify delegate result fields or transcript-render fixtures, which are a separate API.

- [ ] **Step 6: Run the focused shell and schema suites**

Run:

```bash
go test ./agent/internal/tool -count=1
go test ./agent -run 'Test(ParseShellToolArgs|Shell|MarshalShell|RunShell)' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit the mode migration**

```bash
git status --short
git add agent/internal/tool/definitions.go agent/internal/tool/definitions_test.go agent/job_shell.go agent/session_tools_shell.go agent/session_tools_shell_test.go
git commit -m "refactor(shell): use one execution mode"
```

### Task 2: Add an execution-environment detach capability

**Files:**
- Modify: `agent/execenv/execenv.go`
- Modify: `agent/execenv/command_runtime.go`
- Modify: `agent/execenv/local.go`
- Create: `agent/execenv/detach_unix.go`
- Create: `agent/execenv/detach_other.go`
- Modify: `agent/execenv/command_runtime_test.go`
- Create: `agent/execenv/detach_test.go`

**Interfaces:**
- Produces: `type DetachedExecutor interface { DetachCommand(ctx context.Context, command, workingDir string, envVars map[string]string) (DetachedProcess, error) }`.
- Produces: `type DetachedProcess struct { PID int }`.
- Produces: package error `ErrDetachUnsupported`.
- Produces: platform helper `detachedProcessSysProcAttr() (*syscall.SysProcAttr, bool)`.

- [ ] **Step 1: Write capability and unsupported-platform tests**

In `agent/execenv/detach_test.go`, add compile-time and behavior assertions:

```go
func TestLocalExecutionEnvironmentImplementsDetachedExecutor(t *testing.T) {
	var _ DetachedExecutor = (*LocalExecutionEnvironment)(nil)
}

func TestDetachCommandRejectsCancelledContextBeforeStart(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := env.DetachCommand(ctx, "true", "", nil)
	if !errors.Is(err, context.Canceled) || got.PID != 0 {
		t.Fatalf("DetachCommand = (%+v, %v), want zero/context.Canceled", got, err)
	}
}
```

Add a platform helper unit test in the corresponding build-tagged file: Unix expects `ok == true`, non-Unix expects `ok == false`.

- [ ] **Step 2: Run the tests and verify they fail to compile**

Run:

```bash
go test ./agent/execenv -run 'Test(LocalExecutionEnvironmentImplementsDetachedExecutor|DetachCommand)' -count=1
```

Expected: FAIL because the new interface, result, error, and method are undefined.

- [ ] **Step 3: Define the optional capability**

In `agent/execenv/execenv.go`, add:

```go
var ErrDetachUnsupported = errors.New("detached commands are not supported by this execution environment")

type DetachedProcess struct {
	PID int `json:"pid"`
}

type DetachedExecutor interface {
	DetachCommand(ctx context.Context, command, workingDir string, envVars map[string]string) (DetachedProcess, error)
}
```

Add `errors` to the imports.

- [ ] **Step 4: Add platform-specific process attributes**

Create `agent/execenv/detach_unix.go`:

```go
//go:build linux || darwin

package execenv

import "syscall"

func detachedProcessSysProcAttr() (*syscall.SysProcAttr, bool) {
	return &syscall.SysProcAttr{Setsid: true}, true
}
```

Create `agent/execenv/detach_other.go`:

```go
//go:build !linux && !darwin

package execenv

import "syscall"

func detachedProcessSysProcAttr() (*syscall.SysProcAttr, bool) { return nil, false }
```

- [ ] **Step 5: Extend the command runtime configuration without changing managed execution**

Add `Stdin io.Reader` and `SysProcAttr *syscall.SysProcAttr` to `commandRuntimeConfig`. In `systemCommandRuntime.Configure`, set `cmd.Stdin = config.Stdin`; use `config.SysProcAttr` when non-nil, otherwise retain `processGroupSysProcAttr()`. Update the direct-runtime parity test in `command_runtime_test.go` to cover stdin and the explicit attributes.

- [ ] **Step 6: Implement detached launch in `LocalExecutionEnvironment`**

Add `DetachCommand` beside `StreamCommand` in `agent/execenv/local.go`. It must:

1. Resolve and validate `workingDir` using the same code as `StreamCommand`.
2. Return `ctx.Err()` before opening files or starting a process when already cancelled.
3. Return `ErrDetachUnsupported` when `detachedProcessSysProcAttr` reports false.
4. Return `ErrDetachUnsupported` when `e.Wrapper != nil` because the current enforced sandbox wrappers are session-lifetime infrastructure.
5. Open `os.DevNull` with `os.O_RDWR` and use it for stdin, stdout, and stderr.
6. Configure the shell command with the detached `SysProcAttr`, normal working directory, environment, executable policy, and no sandbox wrapper.
7. Start the command, capture its positive PID, close the parent's null-device descriptor, and launch `go func() { _ = cmd.Wait() }()` to reap it while Evener remains alive.
8. Never insert the command into `e.runningPIDs`.

Use this signature:

```go
func (e *LocalExecutionEnvironment) DetachCommand(ctx context.Context, command, workingDir string, envVars map[string]string) (DetachedProcess, error)
```

On every error after opening the null device, close it. If `Start` fails, return a zero `DetachedProcess`. Since success occurs only after `Start`, there is no post-start fallible ownership-transfer step.

- [ ] **Step 7: Add real process tests without sleeps or PID-only assertions**

In `agent/execenv/detach_test.go` on Unix, launch a shell command that writes its PID to a caller-owned temporary file, waits on a second caller-owned release file, then writes a completion file.

Use this local condition helper; its deadline is a failure tripwire and progress depends on the file condition:

```go
func waitForTestFile(path string, timeout time.Duration) ([]byte, bool) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if data, err := os.ReadFile(path); err == nil {
			return data, true
		}
		select {
		case <-deadline.C:
			return nil, false
		case <-ticker.C:
		}
	}
}
```

Launch a command built with `fmt.Sprintf` and `strconv.Quote` so test paths are not interpolated unsafely:

```go
command := fmt.Sprintf(
	"printf '%%s' $$ > %s; while test ! -e %s; do sleep 0.01; done; printf done > %s",
	strconv.Quote(pidPath), strconv.Quote(releasePath), strconv.Quote(donePath),
)
```

Assert:

- returned PID matches the PID file;
- `env.Cleanup()` does not prevent the completion file after creating the release file;
- the command's output is absent because streams use the null device;
- `env.runningPIDs` never contains the detached PID.

Register cleanup immediately after launch that signals the returned PID only if the test fails before releasing the command, so a failing test cannot leak a process.

- [ ] **Step 8: Run execenv tests and platform compile checks**

Run:

```bash
go test ./agent/execenv -run 'Test(Detach|LocalExecutionEnvironmentImplementsDetachedExecutor)' -count=1
GOOS=windows GOARCH=amd64 go test ./agent/execenv -run '^$'
```

Expected: PASS; the Windows compile succeeds and runtime detach remains unsupported there.

- [ ] **Step 9: Commit the execution primitive**

```bash
git status --short
git add agent/execenv/execenv.go agent/execenv/command_runtime.go agent/execenv/local.go agent/execenv/detach_unix.go agent/execenv/detach_other.go agent/execenv/command_runtime_test.go agent/execenv/detach_test.go
git commit -m "feat(execenv): launch detached commands"
```

### Task 3: Route detached shell mode outside job management

**Files:**
- Modify: `agent/session_tools_shell.go`
- Modify: `agent/session_tools_shell_test.go`
- Modify: `agent/internal/agenttest/agenttest.go` only if its shell-result decoder requires the removed result field

**Interfaces:**
- Consumes: `shellModeDetached`, `execenv.DetachedExecutor`, `execenv.DetachedProcess`, and `execenv.ErrDetachUnsupported`.
- Produces: `detachedShellToolResult` with `type`, `mode`, `status`, and `pid` only.
- Produces: `runDetachedShell(ctx, env, shellArgs) (tool.StateResult, error)`.

- [ ] **Step 1: Write a fake-boundary test proving detached mode bypasses the job manager**

Add a test execution environment implementing `execenv.DetachedExecutor` that records the command/cwd and returns PID 4242. Execute the registered shell tool with `{"command":"serve","mode":"detached"}` and assert structured state equals:

```go
detachedShellToolResult{
	Type:   "shell",
	Mode:   "detached",
	Status: "started",
	PID:    4242,
}
```

Also assert the session job manager has zero running jobs and no job-store event was appended.

- [ ] **Step 2: Write rejection tests**

Add cases proving:

- a streaming environment without `DetachedExecutor` returns `execenv.ErrDetachUnsupported`;
- a detached executor returning PID 0 is rejected as `detached command started without a valid pid`;
- invalid cwd is rejected by `resolveShellWorkingDir` before `DetachCommand` is invoked.

- [ ] **Step 3: Run the focused tests and verify failure**

Run:

```bash
go test ./agent -run 'TestShellDetached' -count=1
```

Expected: FAIL because the detached branch and result type do not exist.

- [ ] **Step 4: Implement the detached result and dispatch branch**

In `agent/session_tools_shell.go`, add:

```go
type detachedShellToolResult struct {
	Type   string `json:"type"`
	Mode   string `json:"mode"`
	Status string `json:"status"`
	PID    int    `json:"pid"`
}

func runDetachedShell(ctx context.Context, env execenv.ExecutionEnvironment, args shellArgs) (tool.StateResult, error) {
	detacher, ok := env.(execenv.DetachedExecutor)
	if !ok {
		return tool.StateResult{}, execenv.ErrDetachUnsupported
	}
	started, err := detacher.DetachCommand(ctx, args.Command, args.WorkingDir, nil)
	if err != nil {
		return tool.StateResult{}, err
	}
	if started.PID <= 0 {
		return tool.StateResult{}, errors.New("detached command started without a valid pid")
	}
	state := detachedShellToolResult{Type: "shell", Mode: string(shellModeDetached), Status: "started", PID: started.PID}
	b, _ := json.Marshal(state)
	return tool.StateResult{Output: string(b), State: state}, nil
}
```

After parsing and resolving cwd in the shell tool handler, branch on `shellArgs.Mode == shellModeDetached` before asserting `StreamingExecutor` or accessing `s.jobManager`. Foreground/background continue through the existing paths.

- [ ] **Step 5: Run shell tests**

Run:

```bash
go test ./agent -run 'TestShellDetached|TestParseShellToolArgsMode|TestShell' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit detached shell dispatch**

```bash
git status --short
git add agent/session_tools_shell.go agent/session_tools_shell_test.go
git add agent/internal/agenttest/agenttest.go 2>/dev/null || true
git commit -m "feat(shell): expose detached execution mode"
```

Do not stage `agent/internal/agenttest/agenttest.go` unless it actually changed.

### Task 4: Prove one-shot process survival and finish documentation

**Files:**
- Create: `cmd/evener/run_detached_test.go`
- Modify: `docs/job-control.md`
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-08-09-detached-shell-jobs-design.md` only if implementation reveals a factual mismatch

**Interfaces:**
- Consumes: the public shell `mode` enum and detached result contract.
- Produces: end-to-end proof that `evener run` can exit while its detached command continues.

- [ ] **Step 1: Write a scripted-provider CLI integration test**

Use the existing scripted provider helpers and `waitForFileContent` from `cmd/evener/scripted_provider_test.go`. The provider must call shell once with a detached command that writes its PID to a caller-owned file, waits for a caller-owned release file, and then writes a completion marker; the next scripted response communicates completion of the agent task. Build the command with `fmt.Sprintf` and `strconv.Quote` exactly as in Task 2 so paths remain literal shell arguments.

Run `run(...)` in-process and assert:

- it returns without waiting for the detached command;
- stdout contains the scripted final response;
- the detached PID file exists and contains the PID returned in the shell tool result captured by the scripted request history;
- after `run` has called `sess.Close`, creating the release file causes the completion marker to appear;
- cleanup terminates any unreleased child on failure.

Do not use fixed sleeps in Go. Synchronize through the files and the existing `waitForFileContent` condition helper; its timeout is only a failure tripwire.

- [ ] **Step 2: Run the CLI test and verify it passes**

Run:

```bash
go test ./cmd/evener -run TestRunDetachedCommandSurvivesExit -count=1
```

Expected: PASS.

- [ ] **Step 3: Update job-control documentation**

In `docs/job-control.md`, update shell guidance to describe:

```text
mode="foreground" (default): wait inline; a long-running command may promote to a session-owned job.
mode="background": immediately create a session-owned job with job_id, output, notification, and stop control.
mode="detached": immediately disown the process and return only its PID; it is not a job and is not discoverable or controllable through job tools.
```

Remove model-facing statements that describe the old `background` boolean. Do not change delegate background semantics.

- [ ] **Step 4: Update the shell example in README**

Replace shell examples using `background: true` with `mode: "background"`. Add one concise detached example and state that the command must redirect its own logs when they are needed.

- [ ] **Step 5: Run focused and full deterministic verification**

Run:

```bash
gofmt -w agent/internal/tool/definitions.go agent/internal/tool/definitions_test.go agent/job_shell.go agent/session_tools_shell.go agent/session_tools_shell_test.go agent/execenv/execenv.go agent/execenv/command_runtime.go agent/execenv/local.go agent/execenv/detach_unix.go agent/execenv/detach_other.go agent/execenv/command_runtime_test.go agent/execenv/detach_test.go cmd/evener/run_detached_test.go
go test ./agent/execenv -count=1
go test ./agent -count=1
go test ./cmd/evener -count=1
make lint
make build-go
ROOT_FULL=1 WEB=0 make test
```

Expected: every command exits 0. If any flake appears, stop and root-cause it under `docs/testing.md`; do not widen a timeout.

- [ ] **Step 6: Commit documentation and integration proof**

```bash
git status --short
git add cmd/evener/run_detached_test.go docs/job-control.md README.md
git add docs/superpowers/specs/2026-08-09-detached-shell-jobs-design.md
git commit -m "test(shell): prove detached process survival"
```

Do not stage the spec unless it actually changed.

- [ ] **Step 7: Request final review**

Record the base and head revisions:

```bash
git rev-parse 64a7a7389
git rev-parse HEAD
git status --short
```

Request review of the complete range against the design spec. Resolve every critical or important finding before declaring implementation complete.
