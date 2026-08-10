package execenv

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

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
	Dir              string
	Env              []string
	ExecutablePath   string
	Stdin            io.Reader
	Stdout           io.Writer
	Stderr           io.Writer
	CombinedOutput   io.Writer
	SysProcAttr      *syscall.SysProcAttr
	Wrapper          *sandbox.Wrapper
	TerminationGrace time.Duration
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
	cmd              *exec.Cmd
	combinedOutput   io.Writer
	outputReader     *os.File
	outputDone       chan error
	terminationGrace time.Duration
}

type commandOutputWriteError struct {
	err error
}

func (e *commandOutputWriteError) Error() string { return e.err.Error() }

func (e *commandOutputWriteError) Unwrap() error { return e.err }

type commandOutputWriter struct {
	destination io.Writer
}

func (w commandOutputWriter) Write(p []byte) (int, error) {
	n, err := w.destination.Write(p)
	if err != nil {
		return n, &commandOutputWriteError{err: err}
	}
	return n, nil
}

var processExitCode = (*exec.ExitError).ExitCode

func (c *systemCommandRuntime) Args() []string { return c.cmd.Args }

func (c *systemCommandRuntime) Configure(config commandRuntimeConfig) {
	c.cmd.Dir = config.Dir
	c.cmd.Stdin = config.Stdin
	if config.SysProcAttr != nil {
		c.cmd.SysProcAttr = config.SysProcAttr
	} else {
		c.cmd.SysProcAttr = processGroupSysProcAttr()
	}
	c.cmd.Env = config.Env
	if config.ExecutablePath != "" {
		c.cmd.Path = config.ExecutablePath
		c.cmd.Err = nil
	}
	wrapCommandForSandbox(c.cmd, config.Wrapper, config.Dir)
	if config.CombinedOutput == nil {
		c.cmd.Stdout = config.Stdout
		c.cmd.Stderr = config.Stderr
	} else {
		c.combinedOutput = config.CombinedOutput
		c.terminationGrace = config.TerminationGrace
	}
}

func (c *systemCommandRuntime) Start() error {
	if c.combinedOutput == nil {
		return c.cmd.Start()
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	c.cmd.Stdout = writer
	c.cmd.Stderr = writer
	if err := c.cmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return err
	}
	_ = writer.Close()

	c.outputReader = reader
	c.outputDone = make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(commandOutputWriter{destination: c.combinedOutput}, reader)
		c.outputDone <- copyErr
	}()
	return nil
}

func (c *systemCommandRuntime) Wait() error {
	processErr := c.cmd.Wait()
	if c.outputDone == nil {
		return processErr
	}

	outputDone, outputErr := c.outputResult()
	if outputDone && outputErr == nil {
		_ = c.outputReader.Close()
		return processErr
	}

	pipeClosed, canSignalGroup, pipeErr := waitForStreamPipeClose(c.outputReader, 0)
	if pipeErr != nil {
		_ = c.outputReader.Close()
		if !outputDone {
			outputErr = <-c.outputDone
		}
		return commandWaitError(processErr, outputErr, pipeErr)
	}
	if pipeClosed {
		if !outputDone {
			outputErr = <-c.outputDone
		}
		_ = c.outputReader.Close()
		return commandWaitError(processErr, outputErr, nil)
	}
	if !canSignalGroup {
		_ = c.outputReader.Close()
		if !outputDone {
			outputErr = <-c.outputDone
		}
		return commandWaitError(processErr, c.forcedCloseOutputError(outputErr), nil)
	}

	// Background commands remain owned by Serf. A live pipe writer after the
	// leader exits is a managed descendant; only DetachCommand disowns one.
	c.Terminate()
	pipeClosed, _, pipeErr = waitForStreamPipeClose(c.outputReader, c.terminationGrace)
	if pipeErr != nil {
		_ = c.outputReader.Close()
		if !outputDone {
			outputErr = <-c.outputDone
		}
		return commandWaitError(processErr, outputErr, pipeErr)
	}
	if pipeClosed {
		if !outputDone {
			outputErr = <-c.outputDone
		}
		_ = c.outputReader.Close()
		return commandWaitError(processErr, outputErr, nil)
	}
	c.Kill()
	_ = c.outputReader.Close()
	if !outputDone {
		outputErr = <-c.outputDone
	}
	return commandWaitError(processErr, c.forcedCloseOutputError(outputErr), nil)
}

func (c *systemCommandRuntime) outputResult() (bool, error) {
	select {
	case err := <-c.outputDone:
		return true, err
	default:
		return false, nil
	}
}

func (c *systemCommandRuntime) forcedCloseOutputError(err error) error {
	var writeErr *commandOutputWriteError
	if errors.As(err, &writeErr) {
		return err
	}
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

func commandWaitError(processErr, outputErr, lifecycleErr error) error {
	if outputErr != nil {
		return outputErr
	}
	if lifecycleErr != nil {
		return lifecycleErr
	}
	return processErr
}

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
