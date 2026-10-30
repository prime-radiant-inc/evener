package execenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/envvars"
)

// ExecCommand runs command through the platform shell in its own process group,
// rooted at workingDir, defaulting to RootDir. Confined policies require cwd
// under RootDir; unrestricted off sessions may point elsewhere.
// The environment is built from EnvPolicy plus envVars, with any local
// virtualenv bin directory prepended to PATH. The command is terminated if ctx
// is cancelled or timeoutMS elapses (default 10000ms), escalating from SIGTERM
// to SIGKILL. It returns an ExecResult capturing stdout, stderr, exit code,
// timeout status, and duration.
func (e *LocalExecutionEnvironment) ExecCommand(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error) {
	return e.execPreparedCommand(ctx, e.commands().Shell(command), timeoutMS, workingDir, envVars)
}

// ExecArgv runs a command directly, without a shell, while preserving the same
// working-directory, environment, process-group, timeout, and result semantics
// as ExecCommand. Use it when the caller already has structured argv.
func (e *LocalExecutionEnvironment) ExecArgv(ctx context.Context, name string, args []string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error) {
	cmd := e.commands().Argv(name, args...)
	return e.execPreparedCommand(ctx, cmd, timeoutMS, workingDir, envVars)
}

func (e *LocalExecutionEnvironment) resolveCommandWorkingDir(workingDir string) (string, error) {
	dir := strings.TrimSpace(workingDir)
	if dir == "" {
		dir = e.RootDir
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(e.RootDir, dir)
	}
	if err := e.EnsureCommandWorkingDirectory(dir); err != nil {
		return "", fmt.Errorf("working directory %w", err)
	}
	return dir, nil
}

func (e *LocalExecutionEnvironment) execPreparedCommand(ctx context.Context, cmd commandRuntime, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error) {
	return e.execPreparedCommandStdout(ctx, cmd, timeoutMS, workingDir, envVars, nil)
}

// execPreparedCommandStdout is execPreparedCommand with stdout, when sink is
// not nil, streamed to sink as it arrives instead of captured: a caller that
// only counts or scans the stream lets any volume flow through without any
// of it being retained. The same lifecycle owns the run — cancellation, the
// timeout, process-group termination, the drain barrier, exit-code mapping —
// and Stdout stays empty on the result, since the buffer nothing streamed
// into is never read.
func (e *LocalExecutionEnvironment) execPreparedCommandStdout(ctx context.Context, cmd commandRuntime, timeoutMS int, workingDir string, envVars map[string]string, sink io.Writer) (ExecResult, error) {
	if timeoutMS <= 0 {
		timeoutMS = 10_000
	}
	dir, err := e.resolveCommandWorkingDir(workingDir)
	if err != nil {
		return ExecResult{ExitCode: 127}, err
	}

	start := time.Now()
	stdout := newCappedOutputBuffer(e.outputCaptureCapBytes())
	stderr := newCappedOutputBuffer(e.outputCaptureCapBytes())
	env := e.launchEnv(dir, envVars)
	config := commandRuntimeConfig{
		Dir:              dir,
		Env:              env,
		Stdout:           stdout,
		Stderr:           stderr,
		Wrapper:          e.Wrapper,
		TerminationGrace: e.terminationGraceDuration(),
	}
	if sink != nil {
		config.Stdout = sink
	}
	config.ExecutablePath = launchExecutable(cmd, env)
	cmd.Configure(config)

	if err := cmd.Start(); err != nil {
		return ExecResult{ExitCode: 127}, err
	}
	pid := cmd.PID()
	e.runningPIDs.Store(pid, cmd)
	defer e.runningPIDs.Delete(pid)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timedOut := false
	interrupted := false
	var waitErr error
	select {
	case <-ctx.Done():
		interrupted = true
		waitErr = ctx.Err()
		timedOut = errors.Is(waitErr, context.DeadlineExceeded)
	case err := <-done:
		waitErr = err
	case <-time.After(time.Duration(timeoutMS) * time.Millisecond):
		interrupted = true
		timedOut = true
		waitErr = context.DeadlineExceeded
	}

	if interrupted {
		cmd.Terminate()
		select {
		case <-done:
			// exited on SIGTERM
		case <-time.After(e.terminationGraceDuration()):
			cmd.Kill()
			// Best-effort: wait a bit for Wait() to return so we don't leak the goroutine.
			select {
			case <-done:
			case <-time.After(e.terminationGraceDuration()):
			}
		}
	}

	// Completion barrier: reading the buffers is only safe once Wait has
	// returned, because Wait is what joins os/exec's own copier (or the runtime's
	// owned one). When Wait is still outstanding after the bounded termination
	// waits — a writer outside the process group can hold the output pipe open —
	// force-close and join the runtime's owned copier first, so the read below
	// neither races a live copier nor releases PID ownership before the command's
	// output has drained.
	select {
	case <-done:
	default:
		if drainer, ok := cmd.(bufferedOutputDrainer); ok {
			_ = drainer.drainBufferedOutput()
		}
	}

	exitCode := 0
	if waitErr != nil {
		if code, ok := cmd.ExitCode(waitErr); ok {
			exitCode = code
		} else if timedOut {
			exitCode = 124
		} else if errors.Is(waitErr, context.Canceled) {
			exitCode = 130
		} else {
			exitCode = 1
		}
	}

	return ExecResult{
		Stdout:          stdout.String(),
		Stderr:          stderr.String(),
		ExitCode:        exitCode,
		TimedOut:        timedOut,
		DurationMS:      time.Since(start).Milliseconds(),
		StdoutTruncated: stdout.truncated(),
		StderrTruncated: stderr.truncated(),
	}, waitErr
}

// StreamCommand runs command through the platform shell in its own process
// group, streaming combined stdout/stderr to out and returning immediately with
// a handle for waiting on or signalling the command.
func (e *LocalExecutionEnvironment) StreamCommand(ctx context.Context, command, workingDir string, envVars map[string]string, out io.Writer) (*StreamHandle, error) {
	if e.runningPIDs == nil {
		e.runningPIDs = &sync.Map{}
	}
	dir, err := e.resolveCommandWorkingDir(workingDir)
	if err != nil {
		return nil, err
	}

	if ctx != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}

	cmd := e.commands().Shell(command)
	combinedOutput := out
	if combinedOutput == nil {
		combinedOutput = io.Discard
	}
	cmd.Configure(commandRuntimeConfig{
		Dir:              dir,
		Env:              e.launchEnv(dir, envVars),
		Stdout:           out,
		Stderr:           out,
		CombinedOutput:   combinedOutput,
		Wrapper:          e.Wrapper,
		TerminationGrace: e.terminationGraceDuration(),
	})

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pid := cmd.PID()
	e.runningPIDs.Store(pid, cmd)
	detachedStart := false
	if d, ok := ctx.(interface{ DetachAfterStart() }); ok {
		d.DetachAfterStart()
		detachedStart = true
	}

	done := make(chan struct{})
	var doneOnce sync.Once
	var signalOnce sync.Once
	signal := func() {
		select {
		case <-done:
			return
		default:
		}
		streamBeforeSignalOnce(func() { doneOnce.Do(func() { close(done) }) })
		signalOnce.Do(func() {
			select {
			case <-done:
				return
			default:
			}
			cmd.Terminate()
			go func() {
				timer := time.NewTimer(e.terminationGraceDuration())
				defer timer.Stop()
				select {
				case <-done:
					return
				case <-timer.C:
					streamAfterTimer(func() { doneOnce.Do(func() { close(done) }) })
					select {
					case <-done:
						return
					default:
						cmd.Kill()
					}
				}
			}()
		})
	}

	if ctx != nil && !detachedStart {
		go func() {
			select {
			case <-ctx.Done():
				signal()
			case <-done:
			}
		}()
	}

	wait := func() (int, error) {
		defer doneOnce.Do(func() { close(done) })
		defer e.runningPIDs.Delete(pid)
		if err := cmd.Wait(); err != nil {
			if code, ok := cmd.ExitCode(err); ok {
				return code, nil
			}
			return 127, err
		}
		return 0, nil
	}

	return &StreamHandle{
		Pid:        pid,
		Wait:       wait,
		Signal:     signal,
		SignalName: func() string { return cmdSignalName(cmd) },
	}, nil
}

// DetachSupported reports whether DetachCommand can disown a process here: the
// platform must supply detached-process attributes AND no sandbox wrapper may be
// provisioned, because a command that runs through a wrapper cannot be disowned
// from it. DetachCommand refuses on exactly this predicate, so the advertised
// capability and the actual refusal cannot drift.
func (e *LocalExecutionEnvironment) DetachSupported() bool {
	_, ok := detachedProcessSysProcAttr()
	return ok && e.Wrapper == nil
}

// DetachCommand starts command outside the environment's managed process set
// with its standard streams disconnected from the launching session.
func (e *LocalExecutionEnvironment) DetachCommand(ctx context.Context, command, workingDir string, envVars map[string]string) (DetachedProcess, error) {
	dir, err := e.resolveCommandWorkingDir(workingDir)
	if err != nil {
		return DetachedProcess{}, err
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return DetachedProcess{}, ctx.Err()
		default:
		}
	}

	sysProcAttr, _ := detachedProcessSysProcAttr()
	if !e.DetachSupported() {
		return DetachedProcess{}, ErrDetachUnsupported
	}

	nullDevice, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return DetachedProcess{}, err
	}
	cmd := e.commands().Shell(command)
	env := e.launchEnv(dir, envVars)
	// A detached process outlives the session, and the session's scratch is
	// deleted when the session is archived, so the process must not be told
	// about the scratch, and its TMPDIR must be the world-usable container the
	// session's end keeps for it — never the scratch, which a confined env's
	// TMPDIR names.
	// With no container, keep the inherited TMPDIR unless this env names scratch.
	container := e.unsandboxedTmpDir()
	dropTmpDir := container != "" || e.tmpDirNamesScratch()
	env = slices.DeleteFunc(env, func(kv string) bool {
		return strings.HasPrefix(kv, envvars.EVENERScratchDir.Name+"=") ||
			(dropTmpDir && strings.HasPrefix(kv, envvars.TmpDir.Name+"="))
	})
	if container != "" {
		env = append(env, envvars.TmpDir.Assignment(container))
	}
	config := commandRuntimeConfig{
		Dir:         dir,
		Env:         env,
		Stdin:       nullDevice,
		Stdout:      nullDevice,
		Stderr:      nullDevice,
		SysProcAttr: sysProcAttr,
	}
	config.ExecutablePath = launchExecutable(cmd, env)
	cmd.Configure(config)

	if err := cmd.Start(); err != nil {
		_ = nullDevice.Close()
		return DetachedProcess{}, err
	}
	pid := cmd.PID()
	_ = nullDevice.Close()
	e.scratchMu.Lock()
	e.detachedSpawned = true
	e.scratchMu.Unlock()
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	return DetachedProcess{PID: pid, Done: done}, nil
}

// launchEnv is the environment a launched command receives: the session's
// command environment with the virtualenv bin directories of the working
// directory and of the root placed on PATH.
func (e *LocalExecutionEnvironment) launchEnv(dir string, envVars map[string]string) []string {
	return injectLocalVenvPath(e.commandEnvironment(envVars), []string{dir, e.RootDir})
}

// launchExecutable is the path cmd's first argument resolves to on env's PATH,
// or "" when it is empty, names a path, or is not found, leaving the runtime to
// resolve it itself.
func launchExecutable(cmd commandRuntime, env []string) string {
	args := cmd.Args()
	if len(args) == 0 {
		return ""
	}
	resolved, _ := lookPathInEnv(args[0], env)
	return resolved
}

func lookPathInEnv(name string, env []string) (string, bool) {
	if name == "" || strings.ContainsAny(name, `/\\`) {
		return "", false
	}

	pathPrefix := envvars.Path.Name + "="
	pathValue := ""
	for _, e := range slices.Backward(env) {
		if rest, ok := strings.CutPrefix(e, pathPrefix); ok {
			pathValue = rest
			break
		}
	}
	if pathValue == "" {
		return "", false
	}

	for _, dir := range filepath.SplitList(pathValue) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		if runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0 {
			return candidate, true
		}
	}
	return "", false
}

func injectLocalVenvPath(env []string, roots []string) []string {
	if len(env) == 0 || len(roots) == 0 {
		return env
	}

	seenRoots := map[string]struct{}{}
	var uniqueRoots []string
	for _, r := range roots {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		r = filepath.Clean(r)
		if _, ok := seenRoots[r]; ok {
			continue
		}
		seenRoots[r] = struct{}{}
		uniqueRoots = append(uniqueRoots, r)
	}
	if len(uniqueRoots) == 0 {
		return env
	}

	binDir := "bin"
	if runtimeGOOS == "windows" {
		binDir = "Scripts"
	}

	seenDirs := map[string]struct{}{}
	var prefixDirs []string
	for _, root := range uniqueRoots {
		candidates := venvCandidateDirs(root, binDir)
		for _, cand := range candidates {
			info, err := os.Stat(cand)
			if err != nil || !info.IsDir() {
				continue
			}
			if _, ok := seenDirs[cand]; ok {
				continue
			}
			seenDirs[cand] = struct{}{}
			prefixDirs = append(prefixDirs, cand)
		}
	}
	if len(prefixDirs) == 0 {
		return env
	}

	sep := string(os.PathListSeparator)
	pathPrefix := envvars.Path.Name + "="
	findPath := func(env []string) (int, string) {
		for i, kv := range env {
			if rest, ok := strings.CutPrefix(kv, pathPrefix); ok {
				return i, rest
			}
		}
		return -1, ""
	}

	idx, existing := findPath(env)
	if existing != "" {
		parts := strings.Split(existing, sep)
		existingSet := map[string]struct{}{}
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			existingSet[p] = struct{}{}
		}
		var filteredPrefix []string
		for _, d := range prefixDirs {
			if _, ok := existingSet[d]; ok {
				continue
			}
			filteredPrefix = append(filteredPrefix, d)
		}
		prefixDirs = filteredPrefix
	}
	if len(prefixDirs) == 0 {
		return env
	}

	prefix := strings.Join(prefixDirs, sep)
	var newPath string
	if existing == "" {
		newPath = prefix
	} else {
		newPath = prefix + sep + existing
	}

	if idx >= 0 {
		env[idx] = envvars.Path.Assignment(newPath)
		return env
	}
	return append(env, envvars.Path.Assignment(newPath))
}

// shellCommand returns an *exec.Cmd that runs the given command string through
// the platform shell. POSIX invocations use Bash explicitly because pipefail
// is part of the shell contract; falling back to /bin/sh would silently make
// the command invalid on shells such as dash. If /bin/bash is unavailable,
// resolve Bash through the caller's effective PATH and otherwise leave the
// explicit /bin/bash path in place so command start fails instead of running
// with different pipeline semantics. The command's lifecycle (cancellation,
// timeout) is managed by the caller (ExecCommand) via its own process-group
// SIGTERM->SIGKILL escalation, so CommandContext is deliberately not used here.
func shellCommand(command string) *exec.Cmd {
	if runtimeGOOS == "windows" {
		return exec.Command("cmd.exe", "/c", command) //nolint:noctx // lifecycle managed by ExecCommand's process-group kill
	}
	shell := "/bin/bash"
	if _, err := shellStat(shell); err != nil {
		if resolved, err := execLookPath("bash"); err == nil {
			shell = resolved
		}
	}
	return exec.Command(shell, "-o", "pipefail", "-c", command) //nolint:noctx // lifecycle managed by ExecCommand's process-group kill
}
