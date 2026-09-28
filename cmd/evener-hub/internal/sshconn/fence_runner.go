package sshconn

// FenceCommandRunner runs one raw remote command for the fencing helper over the
// manager's ssh process seam: the read-only `evener-fence` self-test and lease
// enumeration that `orphan-resolve` runs, plus any helper command a caller
// builds. It presents no epoch and writes no controller state — the helper
// decides — and it is never used for a mutating fencing step.
//
// The stream mapping keeps the classes apart the way the helper layer needs
// them: ssh forwards the remote command's exit status unchanged, so a nonzero
// exit (the helper's own refusal, or ssh failing to reach the host) is returned
// as the command's exit status with its two streams separate — never as a
// transport error the helper layer cannot classify — while a failure that never
// ran ssh stays an error.

import (
	"context"
	"errors"
	"os/exec"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// FenceCommandRunner is one host's one-shot remote-command channel for the
// fencing helper.
type FenceCommandRunner struct {
	manager *Manager
	host    hostreg.Host
}

// FenceCommandRunnerFor returns the one-shot remote runner for host.
func (m *Manager) FenceCommandRunnerFor(host hostreg.Host) FenceCommandRunner {
	return FenceCommandRunner{manager: m, host: host}
}

// Run executes one command on the host over the manager's ssh runner and maps
// its outcome onto (stdout, stderr, exit status, transport error).
func (r FenceCommandRunner) Run(ctx context.Context, command string) (stdout, stderr string, exitCode int, err error) {
	if r.manager == nil {
		return "", "", 0, errors.New("sshconn: no manager is configured for the fence runner")
	}
	out, runErr := r.manager.runner.Run(ctx, rawCommandArgv(r.manager.opts, r.host, command), nil)
	if runErr == nil {
		return string(out), "", 0, nil
	}
	var failed *RunError
	if !errors.As(runErr, &failed) {
		return "", "", 0, runErr
	}
	code := -1
	if exitErr, ok := errors.AsType[*exec.ExitError](failed.Err); ok {
		code = exitErr.ExitCode()
	}
	return string(failed.Stdout), string(failed.Stderr), code, nil
}
