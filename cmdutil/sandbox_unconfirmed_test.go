package cmdutil

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/sandbox"
)

// A session whose host bwrap check has not answered starts with its sandbox
// unconfirmed; a host that answered "no" still refuses to start sandboxed.
func TestProvisionSandboxStartsUnconfirmedOnlyWhileTheHostCheckHasNotAnswered(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		recheck     *sandbox.BackendRecheck
		wantRefusal bool
	}{
		{"check has not answered", sandbox.NewBackendRecheck(func() sandbox.CheckOutcome { return sandbox.CheckInconclusive }), false},
		{"check answered no", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			host := sandbox.HostFacts{OS: "linux", Home: t.TempDir(), BwrapPath: "/usr/bin/bwrap", BwrapRecheck: tc.recheck}
			env := execenv.NewLocalExecutionEnvironment(t.TempDir())
			t.Cleanup(func() { env.Cleanup(); env.DisposeSandboxScratch() })
			cfg := agent.SessionConfig{Sandbox: sandbox.ModeWorkspaceWrite.String()}

			err := ProvisionSandboxWithHost(env, &cfg, env.WorkingDirectory(), host)
			if tc.wantRefusal {
				var refusal *sandbox.RefusalError
				if !errors.As(err, &refusal) || refusal.Kind != sandbox.RefusalDefinitive {
					t.Fatalf("a definitive no must refuse the session, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("an unanswered check must not refuse the session: %v", err)
			}
			if env.Sandbox == nil || !env.Sandbox.Enforced() || !env.Sandbox.Unconfirmed() {
				t.Fatalf("the session must start with its sandbox enforced but unconfirmed, got %+v", env.Sandbox)
			}
		})
	}
}
