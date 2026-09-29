package execenv

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/execsupport/procgroup"
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
	signalName       string

	// Buffered mode owns its stdout/stderr pipes the same way streaming owns
	// the combined pipe, so a copier left holding a pipe open by a writer
	// outside the process group can be force-closed and joined before the
	// captured buffers are read.
	bufferedStdout   io.Writer
	bufferedStderr   io.Writer
	bufferedOutputs  []*bufferedOutputPipe
	bufferedDrain    sync.Once
	bufferedDrainErr error
}

// bufferedOutputPipe is one owned stdout/stderr capture: the child writes to
// writer (an *os.File it inherits directly, so os/exec installs no copier of
// its own), a goroutine copies reader into destination, and done reports that
// copier's completion.
type bufferedOutputPipe struct {
	destination io.Writer
	reader      *os.File
	writer      *os.File
	done        chan error
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
		c.cmd.SysProcAttr = procgroup.SysProcAttr()
	}
	c.cmd.Env = config.Env
	if config.ExecutablePath != "" {
		c.cmd.Path = config.ExecutablePath
		c.cmd.Err = nil
	}
	wrapCommandForSandbox(c.cmd, config.Wrapper, config.Dir)
	c.terminationGrace = config.TerminationGrace
	if config.CombinedOutput == nil {
		c.cmd.Stdout = config.Stdout
		c.cmd.Stderr = config.Stderr
		c.bufferedStdout = config.Stdout
		c.bufferedStderr = config.Stderr
	} else {
		c.combinedOutput = config.CombinedOutput
	}
}

func (c *systemCommandRuntime) Start() error {
	if c.combinedOutput != nil {
		return c.startCombined()
	}
	if !bufferedCaptureNeedsPipe(c.bufferedStdout) && !bufferedCaptureNeedsPipe(c.bufferedStderr) {
		return c.cmd.Start()
	}
	return c.startBuffered()
}

// bufferedCaptureNeedsPipe reports whether a buffered destination requires an
// owned pipe. An *os.File (the detached-command /dev/null stdio) is passed
// straight to the child by os/exec with no copier, so it stays untouched; any
// other writer (the bytes.Buffer capture) needs a copier we can close and join.
func bufferedCaptureNeedsPipe(destination io.Writer) bool {
	if destination == nil {
		return false
	}
	_, isFile := destination.(*os.File)
	return !isFile
}

func (c *systemCommandRuntime) startCombined() error {
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
		streamOutputCopyStart()
		_, copyErr := io.Copy(commandOutputWriter{destination: c.combinedOutput}, reader)
		c.outputDone <- copyErr
	}()
	return nil
}

// startBuffered installs owned stdout/stderr pipes before Start and starts one
// copier per non-nil destination once the child holds the only write end. An
// inherited *os.File makes os/exec pass the fd straight through, so Wait
// returns on process exit instead of blocking on a copier, and the copier is
// ours to close and join.
func (c *systemCommandRuntime) startBuffered() error {
	for _, target := range []struct {
		destination io.Writer
		fd          *io.Writer
	}{
		{c.bufferedStdout, &c.cmd.Stdout},
		{c.bufferedStderr, &c.cmd.Stderr},
	} {
		if !bufferedCaptureNeedsPipe(target.destination) {
			continue
		}
		reader, writer, err := os.Pipe()
		if err != nil {
			c.closeUnstartedBufferedPipes()
			return err
		}
		*target.fd = writer
		c.bufferedOutputs = append(c.bufferedOutputs, &bufferedOutputPipe{
			destination: target.destination,
			reader:      reader,
			writer:      writer,
			done:        make(chan error, 1),
		})
	}
	if err := c.cmd.Start(); err != nil {
		c.closeUnstartedBufferedPipes()
		c.bufferedOutputs = nil
		return err
	}
	for _, pipe := range c.bufferedOutputs {
		_ = pipe.writer.Close()
		pipe.writer = nil
		go func(p *bufferedOutputPipe) {
			_, copyErr := io.Copy(commandOutputWriter{destination: p.destination}, p.reader)
			p.done <- copyErr
		}(pipe)
	}
	return nil
}

func (c *systemCommandRuntime) closeUnstartedBufferedPipes() {
	for _, pipe := range c.bufferedOutputs {
		_ = pipe.reader.Close()
		if pipe.writer != nil {
			_ = pipe.writer.Close()
		}
	}
}

// drainBufferedOutput joins the owned buffered copiers so the captured buffers
// are stable. It waits terminationGrace for a copier to reach EOF, then closes
// the reader to end one whose pipe a writer outside the command's process group
// still holds open. The first genuine copy error wins; forcedCloseOutputError
// suppresses the close-induced read error. Wait calls it once the process is
// reaped, and execPreparedCommand calls it as a completion barrier when Wait is
// still outstanding. Safe to call repeatedly and concurrently.
func (c *systemCommandRuntime) drainBufferedOutput() error {
	c.bufferedDrain.Do(func() {
		var copyErr error
		for _, pipe := range c.bufferedOutputs {
			var err error
			select {
			case err = <-pipe.done:
			case <-time.After(c.terminationGrace):
				_ = pipe.reader.Close()
				err = <-pipe.done
			}
			_ = pipe.reader.Close()
			if copyErr == nil {
				copyErr = c.forcedCloseOutputError(err)
			}
		}
		c.bufferedDrainErr = copyErr
	})
	return c.bufferedDrainErr
}

func (c *systemCommandRuntime) Wait() error {
	processErr := c.cmd.Wait()
	c.signalName = processSignalName(c.cmd.ProcessState)
	if c.outputDone != nil {
		return c.waitCombined(processErr)
	}
	if len(c.bufferedOutputs) > 0 {
		return commandWaitError(processErr, c.drainBufferedOutput(), nil)
	}
	return processErr
}

func (c *systemCommandRuntime) waitCombined(processErr error) error {
	outputDone, outputErr := c.outputResult()
	if outputDone && outputErr == nil {
		_ = c.outputReader.Close()
		return processErr
	}

	pipeClosed, canSignalGroup, pipeErr := waitForStreamPipeClose(c.outputReader, 0)
	if pipeErr != nil || pipeClosed {
		return c.drainClosedPipe(processErr, outputDone, outputErr, pipeErr)
	}
	if !canSignalGroup {
		return c.forceCloseOutput(processErr, outputDone, outputErr)
	}

	// Background commands remain owned by Evener. A live pipe writer after the
	// leader exits is a managed descendant; only DetachCommand disowns one.
	c.Terminate()
	pipeClosed, _, pipeErr = waitForStreamPipeClose(c.outputReader, c.terminationGrace)
	if pipeErr != nil || pipeClosed {
		return c.drainClosedPipe(processErr, outputDone, outputErr, pipeErr)
	}
	c.Kill()
	// A SIGKILL'd group releases the pipe as the kernel reaps it, which runs
	// the copier to EOF; wait for it so output the leader wrote before exiting
	// is delivered. Closing the reader first would truncate whatever the copier
	// had not read yet. The window is bounded so an unkillable writer cannot
	// hang Wait.
	if !outputDone {
		drainTimer := time.NewTimer(killedWriterDrainWindow)
		defer drainTimer.Stop()
		select {
		case outputErr = <-c.outputDone:
			outputDone = true
		case <-drainTimer.C:
		}
	}
	if outputDone {
		_ = c.outputReader.Close()
		return commandWaitError(processErr, outputErr, nil)
	}
	return c.forceCloseOutput(processErr, outputDone, outputErr)
}

// killedWriterDrainWindow bounds how long Wait allows a SIGKILL'd process group
// to release the output pipe before force-closing the reader. Kernel teardown
// normally closes the pipe within milliseconds; the bound only matters for a
// writer stuck beyond SIGKILL (for example uninterruptible sleep).
const killedWriterDrainWindow = 2 * time.Second

// drainClosedPipe finishes Wait once the output pipe closed (or polling it
// failed): join the copier before closing the reader so buffered output is
// delivered, except after a poll failure, where closing first unblocks the
// copier.
func (c *systemCommandRuntime) drainClosedPipe(processErr error, outputDone bool, outputErr, pipeErr error) error {
	if pipeErr != nil {
		_ = c.outputReader.Close()
		if !outputDone {
			outputErr = <-c.outputDone
		}
		return commandWaitError(processErr, outputErr, pipeErr)
	}
	if !outputDone {
		outputErr = <-c.outputDone
	}
	_ = c.outputReader.Close()
	return commandWaitError(processErr, outputErr, nil)
}

// forceCloseOutput abandons a still-open pipe writer: close the reader to end
// the copier, then suppress the local-close error it reports.
func (c *systemCommandRuntime) forceCloseOutput(processErr error, outputDone bool, outputErr error) error {
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
	if _, ok := errors.AsType[*commandOutputWriteError](err); ok {
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
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return processExitCode(exitErr), true
	}
	return 0, false
}

// SignalName reports the signal recorded in the child's wait status. It is an
// optional command-runtime capability so scripted and non-local runtimes keep
// the existing commandRuntime contract. Wait must have returned first.
func (c *systemCommandRuntime) SignalName() string { return c.signalName }

func cmdSignalName(cmd commandRuntime) string {
	reporter, ok := cmd.(interface{ SignalName() string })
	if !ok {
		return ""
	}
	return reporter.SignalName()
}

func (c *systemCommandRuntime) Terminate() { procgroup.Terminate(c.PID()) }

func (c *systemCommandRuntime) Kill() { procgroup.Kill(c.PID()) }

func wrapCommandForSandbox(cmd *exec.Cmd, wrapper *sandbox.Wrapper, dir string) {
	cmd.ExtraFiles = nil
	if wrapper == nil {
		return
	}
	cmd.Env = sandbox.ApplyEnvFloor(cmd.Env, wrapper.Policy(), wrapper.SessionTmp())
	wrapper.Confine(cmd, dir)
}
