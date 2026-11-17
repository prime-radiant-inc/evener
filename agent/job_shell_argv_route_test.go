package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// TestRunShellReachesTheArgvFastPath covers finding 1 at the boundary that
// matters: the shell tool does not call ExecCommand. registerShellTools selects
// the StreamingExecutor and runShell calls StreamCommand, so a runtime choice made
// only in ExecCommand left every ordinary shell tool call paying the
// `bash -o pipefail -c` wrapper (#6447).
//
// The route is observed through $SHLVL, which only a shell sets. The fixture
// prints it, and the test's child environment is built from a core-only policy
// that carries no SHLVL in, so a value means bash ran the command and an empty
// one means the program ran as argv. A PID assertion cannot show this: bash execs
// a lone simple command, so the parent is the same either way.
func TestRunShellReachesTheArgvFastPath(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process tree only")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "fixtures")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// Prints SHLVL, which the child environment below does not set.
	syscall.ForkLock.RLock()
	err := os.WriteFile(filepath.Join(bin, "shlvl"), []byte("#!/bin/sh\nprintf 'shlvl:[%s]\\n' \"${SHLVL-unset}\"\n"), 0o755)
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatal(err)
	}

	jm := newTestJM(t)
	env := execenv.NewLocalExecutionEnvironment(root)
	// A controlled child environment: the fixture first on PATH, the platform's
	// tools after it. It rides LoginPATH because that is the session's
	// developer-PATH override, so the test depends on nothing this process
	// happens to have exported.
	env.EnvPolicy = execenv.EnvPolicyCoreOnly
	env.LoginPATH = strings.Join([]string{bin, "/usr/local/bin", "/usr/bin", "/bin"}, string(os.PathListSeparator))
	if err := env.Initialize(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(env.Cleanup)

	// A bare word list: the fixture runs as argv, so no shell has set SHLVL.
	res := runShell(context.Background(), jm, env, shellArgs{Command: "shlvl", BlockTimeoutMS: 5000})
	if res.settle != nil {
		_ = res.settle(false)
	}
	if res.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("shell tool result = %+v, want completed", res)
	}
	if got := strings.TrimSpace(res.Output); got != "shlvl:[unset]" {
		t.Fatalf("`shlvl` through the shell tool reported %q, want shlvl:[unset] — a SHLVL means a shell ran the command", got)
	}

	// A command with a pipe still needs the shell, so bash sets SHLVL itself.
	res = runShell(context.Background(), jm, env, shellArgs{Command: "shlvl | cat", BlockTimeoutMS: 5000})
	if res.settle != nil {
		_ = res.settle(false)
	}
	if res.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("shell tool result = %+v, want completed", res)
	}
	if got := strings.TrimSpace(res.Output); got != "shlvl:[1]" {
		t.Fatalf("`shlvl | cat` through the shell tool reported %q, want shlvl:[1] — a pipe must keep the shell", got)
	}
}
