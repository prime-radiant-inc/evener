package execenv

import (
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

func TestOutputCaptureCapRidesSandboxInvocationGrant(t *testing.T) {
	t.Parallel()
	small := 4096
	env := &LocalExecutionEnvironment{
		RootDir:          t.TempDir(),
		Sandbox:          &sandbox.ResolvedPolicy{Mode: sandbox.ModeWorkspaceWrite},
		outputCaptureCap: &small,
	}
	granted, ok := env.WithSandboxInvocationGrant("/granted/path").(*LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("WithSandboxInvocationGrant returned %T, want *LocalExecutionEnvironment", granted)
	}
	if got := granted.outputCaptureCapBytes(); got != small {
		t.Fatalf("granted clone capture cap = %d, want the parent's %d", got, small)
	}
}
