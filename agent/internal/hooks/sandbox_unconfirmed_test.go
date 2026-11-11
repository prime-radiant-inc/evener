package hooks

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

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
	host := sandbox.HostFacts{OS: "linux", Home: t.TempDir(), BwrapPath: "/usr/bin/bwrap", BwrapRecheck: recheck, InconclusiveChecks: []string{"bwrap user namespaces"}}
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
