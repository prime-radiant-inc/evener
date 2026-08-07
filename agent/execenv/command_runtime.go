//go:build darwin || linux

package execenv

import (
	"errors"
	"io"
	"os/exec"
	"syscall"

	"primeradiant.com/serf/agent/sandbox"
)

// commandRuntime is the narrow boundary between local command preparation and
// os/exec. The default implementation wraps a real *exec.Cmd; deterministic
// tests can supply a scripted implementation that exercises the same command
// plumbing without launching a helper process.
type commandRuntime interface {
	Args() []string
	Configure(commandRuntimeConfig)
	Start() error
	Wait() error
	PID() int
	ExitCode(error) (int, bool)
	Terminate()
	Kill()
}

type commandRuntimeConfig struct {
	Dir            string
	Env            []string
	ExecutablePath string
	Stdout         io.Writer
	Stderr         io.Writer
	Wrapper        *sandbox.Wrapper
}

type commandRuntimeFactory interface {
	Shell(command string) commandRuntime
	Argv(name string, args ...string) commandRuntime
}

type systemCommandRuntimeFactory struct{}

func (systemCommandRuntimeFactory) Shell(command string) commandRuntime {
	return &systemCommandRuntime{cmd: shellCommand(command)}
}

// Argv deliberately builds with plain exec.Command, not exec.CommandContext:
// CommandContext installs its own ctx-triggered kill (a single-process
// os.Process.Kill, not a process-group signal) that would race
// execPreparedCommand's SIGTERM->SIGKILL process-group escalation on the same
// ctx.Done(). Two independent killers on one process tree is how a git hook
// or helper child survives cancellation — execPreparedCommand must be the
// sole owner of cancellation and termination, exactly like shellCommand's
// existing (and already-documented) rationale for the shell path.
func (systemCommandRuntimeFactory) Argv(name string, args ...string) commandRuntime {
	return &systemCommandRuntime{cmd: execCommand(name, args...)} //nolint:noctx // lifecycle managed by execPreparedCommand's process-group kill
}

type systemCommandRuntime struct {
	cmd *exec.Cmd
}

var processExitCode = (*exec.ExitError).ExitCode

func (c *systemCommandRuntime) Args() []string { return c.cmd.Args }

func (c *systemCommandRuntime) Configure(config commandRuntimeConfig) {
	c.cmd.Dir = config.Dir
	c.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.cmd.Env = config.Env
	if config.ExecutablePath != "" {
		c.cmd.Path = config.ExecutablePath
		c.cmd.Err = nil
	}
	wrapCommandForSandbox(c.cmd, config.Wrapper, config.Dir)
	c.cmd.Stdout = config.Stdout
	c.cmd.Stderr = config.Stderr
}

func (c *systemCommandRuntime) Start() error { return c.cmd.Start() }

func (c *systemCommandRuntime) Wait() error { return c.cmd.Wait() }

func (c *systemCommandRuntime) PID() int {
	if c.cmd.Process == nil {
		return 0
	}
	return c.cmd.Process.Pid
}

func (c *systemCommandRuntime) ExitCode(err error) (int, bool) {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return processExitCode(exitErr), true
	}
	return 0, false
}

func (c *systemCommandRuntime) Terminate() { terminateProcessGroup(c.PID()) }

func (c *systemCommandRuntime) Kill() { killProcessGroup(c.PID()) }

func wrapCommandForSandbox(cmd *exec.Cmd, wrapper *sandbox.Wrapper, dir string) {
	cmd.ExtraFiles = nil
	if wrapper == nil {
		return
	}
	cmd.Env = sandbox.ApplyEnvFloor(cmd.Env, wrapper.Policy(), wrapper.SessionTmp())
	wrapper.Confine(cmd, dir)
}
