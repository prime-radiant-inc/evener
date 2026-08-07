//go:build linux || darwin

package execenv

import (
	"context"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestCommandRuntimeFactoryDrivesExecArgvWithoutForking pins the narrow command
// boundary used by deterministic fuzz programs. It proves the real ExecArgv
// plumbing configures and consumes a scripted runtime rather than requiring a
// helper process or an ambient executable.
func TestCommandRuntimeFactoryDrivesExecArgvWithoutForking(t *testing.T) {
	root := t.TempDir()
	command := &scriptedCommandRuntime{
		args:   []string{"tool", "--check"},
		stdout: "scripted stdout",
		stderr: "scripted stderr",
		pid:    4242,
	}
	env := NewLocalExecutionEnvironment(root)
	env.EnvPolicy = EnvPolicyNone
	env.commandFactory = scriptedCommandFactory{argv: command}

	result, err := env.ExecArgv(context.Background(), "tool", []string{"--check"}, 100, "", map[string]string{"VISIBLE": "yes"})
	if err != nil {
		t.Fatalf("ExecArgv: %v", err)
	}
	if result.Stdout != command.stdout || result.Stderr != command.stderr || result.ExitCode != 0 {
		t.Fatalf("ExecArgv result = %+v", result)
	}
	if command.config.Dir != root {
		t.Fatalf("runtime dir = %q, want %q", command.config.Dir, root)
	}
	if !strings.Contains(strings.Join(command.config.Env, "\n"), "VISIBLE=yes") {
		t.Fatalf("runtime env = %v, want explicit variable", command.config.Env)
	}
	if command.startCalls != 1 || command.waitCalls != 1 {
		t.Fatalf("runtime lifecycle start=%d wait=%d, want 1/1", command.startCalls, command.waitCalls)
	}
}

// TestSystemCommandRuntimeConfigurationMatchesDirectCommandSetup locks the
// production adapter to the direct *exec.Cmd configuration it replaced. It does
// not start a command: argv, cwd, environment, process-group, and fd hygiene are
// all inspectable before exec, which keeps this regression deterministic. The
// comparison target is exec.Command, not exec.CommandContext: Argv must not
// carry its own ctx-triggered kill (see Argv's doc comment) — this pins that
// exec.Command, not exec.CommandContext, is what actually gets built.
func TestSystemCommandRuntimeConfigurationMatchesDirectCommandSetup(t *testing.T) {
	root := t.TempDir()
	command := "printf runtime"
	configured := systemCommandRuntimeFactory{}.Argv("/fixture/shell", "-c", command).(*systemCommandRuntime)
	direct := exec.Command("/fixture/shell", "-c", command)

	config := commandRuntimeConfig{
		Dir:            root,
		Env:            []string{"PATH=/fixture/bin", "VISIBLE=yes"},
		ExecutablePath: "/fixture/bin/sh",
	}
	configured.Configure(config)

	direct.Dir = config.Dir
	direct.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	direct.Env = config.Env
	direct.Path = config.ExecutablePath
	direct.Err = nil
	wrapCommandForSandbox(direct, nil, config.Dir)

	got := configured.cmd
	if got.Path != direct.Path || got.Dir != direct.Dir || !reflect.DeepEqual(got.Args, direct.Args) || !reflect.DeepEqual(got.Env, direct.Env) {
		t.Fatalf("runtime command differs from direct setup:\n got=%+v\nwant=%+v", got, direct)
	}
	if got.SysProcAttr == nil || direct.SysProcAttr == nil || got.SysProcAttr.Setpgid != direct.SysProcAttr.Setpgid {
		t.Fatalf("runtime SysProcAttr = %#v, want %#v", got.SysProcAttr, direct.SysProcAttr)
	}
	if got.ExtraFiles != nil || direct.ExtraFiles != nil {
		t.Fatalf("runtime fd hygiene got=%v want=%v", got.ExtraFiles, direct.ExtraFiles)
	}
}

// TestCleanupUsesTrackedCommandRuntime ensures the runtime boundary remains the
// owner of process-group teardown after command execution became injectable. A
// zero PID makes the pre-fix raw syscall path a harmless no-op, while the
// scripted runtime records the behavior Cleanup is required to dispatch.
func TestCleanupUsesTrackedCommandRuntime(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	zeroGrace := time.Duration(0)
	env.terminationGrace = &zeroGrace
	command := &scriptedCommandRuntime{pid: 0}
	env.runningPIDs.Store(command.pid, command)

	env.Cleanup()

	if command.terminateCalls != 1 || command.killCalls != 1 {
		t.Fatalf("Cleanup runtime signals terminate=%d kill=%d, want 1/1", command.terminateCalls, command.killCalls)
	}
}

type scriptedCommandFactory struct {
	argv *scriptedCommandRuntime
}

func (f scriptedCommandFactory) Shell(string) commandRuntime { return f.argv }

func (f scriptedCommandFactory) Argv(string, ...string) commandRuntime {
	return f.argv
}

type scriptedCommandRuntime struct {
	args           []string
	stdout         string
	stderr         string
	pid            int
	config         commandRuntimeConfig
	startCalls     int
	waitCalls      int
	terminateCalls int
	killCalls      int
}

func (c *scriptedCommandRuntime) Args() []string { return c.args }

func (c *scriptedCommandRuntime) Configure(config commandRuntimeConfig) {
	c.config = config
}

func (c *scriptedCommandRuntime) Start() error {
	c.startCalls++
	if c.config.Stdout != nil {
		_, _ = c.config.Stdout.Write([]byte(c.stdout))
	}
	if c.config.Stderr != nil {
		_, _ = c.config.Stderr.Write([]byte(c.stderr))
	}
	return nil
}

func (c *scriptedCommandRuntime) Wait() error {
	c.waitCalls++
	return nil
}

func (c *scriptedCommandRuntime) PID() int { return c.pid }

func (c *scriptedCommandRuntime) ExitCode(error) (int, bool) { return 0, false }

func (c *scriptedCommandRuntime) Terminate() { c.terminateCalls++ }

func (c *scriptedCommandRuntime) Kill() { c.killCalls++ }
