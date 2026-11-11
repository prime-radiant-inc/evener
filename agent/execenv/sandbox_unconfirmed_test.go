//go:build unix

package execenv

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// passThroughBwrap writes a stand-in bwrap that drops every flag up to the "--"
// before the command and runs the command, so a test can observe whether a
// sandboxed command was spawned without a host that can namespace.
func passThroughBwrap(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bwrap")
	script := "#!/bin/sh\nwhile [ \"$#\" -gt 0 ] && [ \"$1\" != \"--\" ]; do shift; done\nshift\nexec \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// unconfirmedSandboxEnv is a workspace-write env on a Linux host whose bwrap
// user-namespace check answers from outcomes, one per recheck.
func unconfirmedSandboxEnv(t *testing.T, outcomes ...sandbox.CheckOutcome) *LocalExecutionEnvironment {
	t.Helper()
	worktree := t.TempDir()
	runs := 0
	recheck := sandbox.NewBackendRecheck(func() sandbox.CheckOutcome {
		outcome := outcomes[min(runs, len(outcomes)-1)]
		runs++
		return outcome
	})
	host := sandbox.HostFacts{OS: "linux", Home: t.TempDir(), BwrapPath: passThroughBwrap(t), BwrapRecheck: recheck}
	rp, err := sandbox.Resolve(sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite}, host, worktree)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	env := NewLocalExecutionEnvironment(worktree)
	t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch() })
	if err := env.EnableSandbox(&rp); err != nil {
		t.Fatalf("EnableSandbox: %v", err)
	}
	return env
}

func refusalKind(t *testing.T, err error) sandbox.RefusalKind {
	t.Helper()
	var refusal *sandbox.RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("want a sandbox refusal, got %v", err)
	}
	return refusal.Kind
}

func TestUnconfirmedSandboxRunsCommandsOnceTheCheckPasses(t *testing.T) {
	t.Parallel()
	env := unconfirmedSandboxEnv(t, sandbox.CheckInconclusive, sandbox.CheckPassed)
	marker := filepath.Join(env.WorkingDirectory(), "ran")
	command := "echo ran > " + marker

	_, err := env.ExecCommand(context.Background(), command, 10_000, "", nil)
	if refusalKind(t, err) != sandbox.RefusalUnconfirmed {
		t.Fatalf("an unanswered check must refuse the command as retryable, got %v", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("a refused command must not run, marker stat: %v", statErr)
	}

	result, err := env.ExecCommand(context.Background(), command, 10_000, "", nil)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("the retry after the check passed must run: exit=%d err=%v", result.ExitCode, err)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("the confirmed command did not run: %v", statErr)
	}
}

func TestUnconfirmedSandboxRefusesStreamsUntilConfirmed(t *testing.T) {
	t.Parallel()
	env := unconfirmedSandboxEnv(t, sandbox.CheckInconclusive)
	handle, err := env.StreamCommand(context.Background(), "true", "", nil, io.Discard)
	if handle != nil || refusalKind(t, err) != sandbox.RefusalUnconfirmed {
		t.Fatalf("an unconfirmed sandbox must refuse a stream before spawning: handle=%v err=%v", handle, err)
	}
}

func TestUnconfirmedSandboxKeepsADefinitiveNo(t *testing.T) {
	t.Parallel()
	env := unconfirmedSandboxEnv(t, sandbox.CheckFailed, sandbox.CheckPassed)
	for range 2 {
		_, err := env.ExecCommand(context.Background(), "true", 10_000, "", nil)
		if refusalKind(t, err) != sandbox.RefusalDefinitive {
			t.Fatalf("a recheck that answered no must refuse definitively every time, got %v", err)
		}
	}
}
