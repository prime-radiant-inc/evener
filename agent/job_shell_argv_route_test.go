package agent

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// TestRunShellReachesTheArgvFastPath pins the runtime choice at the boundary that
// matters: the shell tool does not call ExecCommand. registerShellTools selects
// the StreamingExecutor and runShell calls StreamCommand, so a runtime choice made
// only in ExecCommand left every ordinary shell tool call paying the
// `bash -o pipefail -c` wrapper (#6447).
//
// The route is observed through $SHLVL, which only a shell sets. The program
// under test is printenv, not a script: a `#!/bin/sh` fixture would set SHLVL
// itself wherever /bin/sh is bash, which reports a shell on the argv route too.
// The child environment is built from a core-only policy that carries no SHLVL
// in, so a value means bash ran the command and an empty one means printenv ran
// as argv. A PID assertion cannot show this: bash execs a lone simple command, so
// the parent is the same either way.
func TestRunShellReachesTheArgvFastPath(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process tree only")
	}
	root := t.TempDir()
	if _, err := exec.LookPath("printenv"); err != nil {
		t.Skip("printenv not available")
	}

	jm := newTestJM(t)
	env := execenv.NewLocalExecutionEnvironment(root)
	// A controlled child environment: the platform's tools on PATH. It rides
	// LoginPATH because that is the session's developer-PATH override, so the
	// test depends on nothing this process happens to have exported.
	env.EnvPolicy = execenv.EnvPolicyCoreOnly
	env.LoginPATH = strings.Join([]string{"/usr/local/bin", "/usr/bin", "/bin"}, string(os.PathListSeparator))
	if err := env.Initialize(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(env.Cleanup)

	// A lone program runs as argv, and no shell ran, so the child environment
	// carries no SHLVL for printenv to print.
	res := runShell(context.Background(), jm, env, shellArgs{Command: "printenv", BlockTimeoutMS: 5000})
	if res.settle != nil {
		_ = res.settle(false)
	}
	if res.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("shell tool result = %+v, want completed", res)
	}
	if got := res.Output; strings.Contains(got, "SHLVL=") {
		t.Fatalf("`printenv` through the shell tool printed a SHLVL (%q) — a shell ran the command", got)
	}

	// A command with a pipe still needs the shell, so bash sets SHLVL itself.
	res = runShell(context.Background(), jm, env, shellArgs{Command: "printenv | grep ^SHLVL=", BlockTimeoutMS: 5000})
	if res.settle != nil {
		_ = res.settle(false)
	}
	if res.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("shell tool result = %+v, want completed", res)
	}
	if got := res.Output; !strings.Contains(got, "SHLVL=") {
		t.Fatalf("`printenv | grep ^SHLVL=` through the shell tool printed %q, want a SHLVL — a pipe must keep the shell", got)
	}
}
