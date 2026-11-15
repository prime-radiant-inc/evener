package hooks

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/sandbox"
)

func TestHookCommandWaitsForAnUnconfirmedSandbox(t *testing.T) {
	t.Parallel()
	outcomes := []sandbox.CheckOutcome{sandbox.CheckInconclusive, sandbox.CheckPassed}
	recheck := sandbox.NewBackendRecheck(func() sandbox.CheckOutcome {
		outcome := outcomes[0]
		outcomes = outcomes[1:]
		return outcome
	})
	host := sandbox.HostFacts{OS: "linux", Home: t.TempDir(), BwrapPath: "/usr/bin/bwrap", BwrapRecheck: recheck}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite}, host, t.TempDir())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	w, err := sandbox.NewWrapper(rp, host.BwrapPath, t.TempDir())
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}

	var spawned []string
	runtime := systemCommandHookRuntime{run: func(_ context.Context, _ commandHookInvocation, cmd *exec.Cmd) error {
		spawned = append(spawned, cmd.Path)
		return nil
	}}
	invocation := commandHookInvocation{Program: "scripted-hook", Timeout: time.Second, SandboxWrapper: w}

	_, err = runtime.Run(context.Background(), invocation)
	var refusal *sandbox.RefusalError
	if !errors.As(err, &refusal) || refusal.Kind != sandbox.RefusalUnconfirmed || len(spawned) != 0 {
		t.Fatalf("an unconfirmed sandbox must refuse the hook before spawning: err=%v spawned=%v", err, spawned)
	}

	if _, err := runtime.Run(context.Background(), invocation); err != nil {
		t.Fatalf("the hook must run once the check passes: %v", err)
	}
	if len(spawned) != 1 || spawned[0] != host.BwrapPath {
		t.Fatalf("the confirmed hook must spawn confined under bwrap, spawned %v", spawned)
	}
}

// TestUnconfirmedSandboxDeniesTheToolAGuardHookCouldNotCheck: a PreToolUse
// hook that cannot run because its sandbox refused the spawn, whether the host
// check has not answered or answered no, refuses the tool call it guards, so
// the call never proceeds without the user's guard.
func TestUnconfirmedSandboxDeniesTheToolAGuardHookCouldNotCheck(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		outcome sandbox.CheckOutcome
	}{
		{"check has not answered", sandbox.CheckInconclusive},
		{"check answered no", sandbox.CheckFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			recheck := sandbox.NewBackendRecheck(func() sandbox.CheckOutcome { return tc.outcome })
			host := sandbox.HostFacts{OS: "linux", Home: t.TempDir(), BwrapPath: "/usr/bin/bwrap", BwrapRecheck: recheck}
			rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite}, host, t.TempDir())
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			w, err := sandbox.NewWrapper(rp, host.BwrapPath, t.TempDir())
			if err != nil {
				t.Fatalf("NewWrapper: %v", err)
			}
			spawned := 0
			runner := newRunner(nil, "")
			runner.commandHookRuntime = systemCommandHookRuntime{run: func(context.Context, commandHookInvocation, *exec.Cmd) error {
				spawned++
				return nil
			}}
			runner.SetSandboxWrapper(w)
			runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{Matcher: "*", Type: "command", Timeout: 30, Command: "guard"})

			for range 2 {
				result := runner.RunPreToolUse(context.Background(), Input{CWD: t.TempDir(), HookEventName: "PreToolUse", ToolName: "Bash"})
				if !result.Denied || result.DenyMessage == "" || spawned != 0 {
					t.Fatalf("a guard its sandbox refused must deny the tool with a reason and spawn nothing: denied=%v message=%q spawned=%d",
						result.Denied, result.DenyMessage, spawned)
				}
			}
		})
	}
}
