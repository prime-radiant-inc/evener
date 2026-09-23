//go:build unix

package hooks

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/internal/orphanpipe/orphanpipetest"
)

type hookRun struct {
	result hookResult
	err    error
}

func orphaningHook(command string) plugin.RegisteredHook {
	return plugin.RegisteredHook{Type: "command", Command: command, Timeout: 600, PluginDir: "/tmp"}
}

// A hook's timeout is its context, and a hook is a user's shell command: one
// that backgrounds a job hands that job its stdout and stderr, and killing
// bash when the context ends does not kill the job. Without a WaitDelay the
// hook then blocks the session for as long as the job lives, whatever its
// timeout says.
func TestCommandHookDeadlineDoesNotWaitForAnOrphanedPipeHolder(t *testing.T) {
	t.Parallel()
	h := orphanpipetest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		h.AwaitStarted()
		cancel()
	}()
	done := make(chan hookRun, 1)
	go func() {
		result, err := executeCommandHook(ctx, orphaningHook(h.Spawn()+"\nwait"), Input{CWD: "/tmp", HookEventName: "PreToolUse"})
		done <- hookRun{result, err}
	}()
	if got := orphanpipetest.Await(t, h, done); got.err == nil {
		t.Fatalf("hook after its context ended = nil error, result %+v", got.result)
	}
}

// The other side of the bound: a hook that printed its answer and exited 0
// while a job it backgrounded held its output answered all the same, and its
// stdout is the decision the runner parses.
func TestCommandHookSuccessSurvivesAnOrphanedPipeHolder(t *testing.T) {
	t.Parallel()
	h := orphanpipetest.New(t)
	done := make(chan hookRun, 1)
	go func() {
		result, err := executeCommandHook(context.Background(), orphaningHook("echo answer\n"+h.Spawn()), Input{CWD: "/tmp", HookEventName: "PreToolUse"})
		done <- hookRun{result, err}
	}()
	got := orphanpipetest.Await(t, h, done)
	if got.err != nil {
		t.Fatalf("a hook that answered and exited 0 was reported as: %v", got.err)
	}
	if got.result.ExitCode != 0 || got.result.Stdout != "answer\n" {
		t.Fatalf("result = %+v, want exit 0 with the hook's answer", got.result)
	}
}
