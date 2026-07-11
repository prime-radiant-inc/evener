package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/sandbox"
)

// commandHookInvocation is the fully prepared external command boundary. Its
// fields make the launch contract explicit without exposing an exec.Cmd to the
// runner: command form, marshaled stdin, scrubbed hook environment (including
// CLAUDE_PROJECT_DIR), timeout, and optional sandbox wrapper.
type commandHookInvocation struct {
	Program        string
	Args           []string
	InputJSON      []byte
	Env            []string
	Timeout        time.Duration
	SandboxWrapper *sandbox.Wrapper
}

// commandHookRuntime owns the two ambient/external effects of a command hook:
// capturing the inherited environment and launching its process. Runner carries
// one runtime per instance so deterministic tests can inject a recording fake
// without changing process-wide state.
type commandHookRuntime interface {
	Environ() []string
	Run(context.Context, commandHookInvocation) (hookResult, error)
}

// systemCommandHookRuntime preserves the historical command-hook behavior for
// production callers. It is stateless; every Runner receives its own value.
type systemCommandHookRuntime struct{}

func (systemCommandHookRuntime) Environ() []string { return os.Environ() }

func (systemCommandHookRuntime) Run(ctx context.Context, invocation commandHookInvocation) (hookResult, error) {
	ctx, cancel := context.WithTimeout(ctx, invocation.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, invocation.Program, invocation.Args...)
	cmd.Stdin = bytes.NewReader(invocation.InputJSON)

	// In a sandboxed session, kernel-confine the hook and its descendants to the
	// session policy, raise the sandbox env floor on top of the secret scrub, and
	// empty ExtraFiles so the hook inherits no serf fds beyond stdio.
	env := invocation.Env
	if sbx := invocation.SandboxWrapper; sbx != nil {
		env = sandbox.ApplyEnvFloor(env, sbx.Policy(), sbx.SessionTmp())
		// Confine wraps the argv and, for Seatbelt, sets cmd.Dir to the worktree
		// (sandbox-exec has no chdir flag, unlike bwrap's --chdir).
		sbx.Confine(cmd, sbx.Policy().Git.WorktreeRoot)
		cmd.ExtraFiles = nil
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := hookResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
	if err == nil {
		return result, nil
	}

	// Check if context timed out or was canceled — report as infrastructure error.
	if ctx.Err() != nil {
		return result, fmt.Errorf("hook command killed: %w", ctx.Err())
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return result, fmt.Errorf("running hook command: %w", err)
}

// executeCommandHook runs a command hook with the given input piped as JSON to
// stdin. It remains the production/default entry point used by existing callers;
// the runtime-aware helper below is only how Runner supplies its per-instance
// external boundary.
func executeCommandHook(ctx context.Context, hook plugin.RegisteredHook, input Input, wrapper ...*sandbox.Wrapper) (hookResult, error) {
	return executeCommandHookWithRuntime(ctx, hook, input, systemCommandHookRuntime{}, wrapper...)
}

func executeCommandHookWithRuntime(ctx context.Context, hook plugin.RegisteredHook, input Input, runtime commandHookRuntime, wrapper ...*sandbox.Wrapper) (hookResult, error) {
	if runtime == nil {
		runtime = systemCommandHookRuntime{}
	}
	var sbx *sandbox.Wrapper
	if len(wrapper) > 0 {
		sbx = wrapper[0]
	}

	invocation, err := prepareCommandHookInvocation(hook, input, runtime.Environ(), sbx)
	if err != nil {
		return hookResult{}, err
	}
	return runtime.Run(ctx, invocation)
}

// prepareCommandHookInvocation validates the hook's command form and prepares
// its deterministic launch inputs. It is deliberately separate from process
// creation so callers can test dispatch and policy wiring without spawning a
// shell or reading an ambient environment.
func prepareCommandHookInvocation(hook plugin.RegisteredHook, input Input, inheritedEnv []string, sbx *sandbox.Wrapper) (commandHookInvocation, error) {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return commandHookInvocation{}, fmt.Errorf("marshaling hook input: %w", err)
	}

	invocation := commandHookInvocation{
		InputJSON:      inputJSON,
		Timeout:        time.Duration(hook.Timeout) * time.Second,
		SandboxWrapper: sbx,
	}
	if len(hook.Args) > 0 {
		// Exec form: direct spawn, no shell interpretation.
		invocation.Program = hook.Command
		invocation.Args = slices.Clone(hook.Args)
	} else {
		// Shell form: select shell.
		switch hook.Shell {
		case "", "bash":
			invocation.Program = "bash"
			invocation.Args = []string{"-c", hook.Command}
		case "powershell":
			return commandHookInvocation{}, errors.New("powershell shell not supported on this platform")
		default:
			return commandHookInvocation{}, fmt.Errorf("unsupported shell %q: only \"bash\" is supported", hook.Shell)
		}
	}

	// Hook commands historically built their env straight from os.Environ(),
	// bypassing the *KEY*/*SECRET*/*TOKEN*/*PASSWORD*/*CREDENTIAL* scrub that shell
	// tools already apply — so a hook saw serf's provider API key regardless of
	// sandboxing. Scrub it here so hook commands get the same secret hygiene as
	// every other spawned command (reconciliation #5).
	env := sandbox.ScrubSecretEnv(inheritedEnv)
	env = append(env,
		"CLAUDE_PLUGIN_ROOT="+hook.PluginDir,
		"PLUGIN_ROOT="+hook.PluginDir,
		"CLAUDE_PROJECT_DIR="+input.CWD,
	)
	// CLAUDE_EFFORT: set only when the session has a configured effort level.
	// The inherited value is stripped either way — when serf itself runs under
	// an agent that exports CLAUDE_EFFORT, the parent's level must not leak into
	// hooks as this session's.
	// CLAUDE_CODE_REMOTE is intentionally not set here: serf has no remote/serve
	// signal reachable at the hook exec site; fabricating a value is forbidden by
	// the diagnostics spec (07 §"Common environment variables for command hooks").
	invocation.Env = slices.DeleteFunc(env, func(kv string) bool {
		return strings.HasPrefix(kv, "CLAUDE_EFFORT=")
	})
	if input.Effort != "" {
		invocation.Env = append(invocation.Env, "CLAUDE_EFFORT="+input.Effort)
	}
	return invocation, nil
}
