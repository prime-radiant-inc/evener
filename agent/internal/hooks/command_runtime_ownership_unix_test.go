//go:build linux || darwin

package hooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/execsupport/shellquote"
)

// A hook is a user's shell command, so one that backgrounds a job leaves a
// descendant the direct command does not speak for. The hook's whole process
// group is owned and reaped when the invocation ends, so cancelling the hook
// (as session close does) takes the descendant with it.
func TestCommandHookCancellationReapsTheWholeProcessGroup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	shellPIDFile := filepath.Join(dir, "shell.pid")
	childPIDFile := filepath.Join(dir, "child.pid")
	script := fmt.Sprintf("echo $$ > %s; sleep 300 & echo $! > %s; wait",
		shellquote.Literal(shellPIDFile), shellquote.Literal(childPIDFile))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan hookRun, 1)
	go func() {
		result, err := executeCommandHook(ctx, orphaningHook(script), Input{CWD: "/tmp", HookEventName: "PreToolUse"})
		done <- hookRun{result, err}
	}()

	shellPID := awaitRecordedPID(t, shellPIDFile)
	childPID := awaitRecordedPID(t, childPIDFile)
	cancel()

	got := <-done
	if got.err == nil {
		t.Fatalf("hook after its context ended = nil error, result %+v", got.result)
	}
	awaitGroupGone(t, shellPID)
	awaitProcessGone(t, childPID)
}

// The other side of ownership: a hook that answered and exited 0 while a job it
// backgrounded held its output is still a success, but the job does not outlive
// the invocation. Without the post-reap only cancellation would reap, and a
// completed hook would leave its descendant running past session close.
func TestCommandHookCompletionReapsABackgroundedDescendant(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	childPIDFile := filepath.Join(dir, "child.pid")
	script := "echo answer; sleep 300 & echo $! > " + shellquote.Literal(childPIDFile)

	result, err := executeCommandHook(context.Background(), orphaningHook(script), Input{CWD: "/tmp", HookEventName: "PreToolUse"})
	if err != nil {
		t.Fatalf("a hook that answered and exited 0 was reported as: %v", err)
	}
	if result.ExitCode != 0 || result.Stdout != "answer\n" {
		t.Fatalf("result = %+v, want exit 0 with the hook's answer", result)
	}
	childPID := awaitRecordedPID(t, childPIDFile)
	awaitProcessGone(t, childPID)
}

// SessionEnd hooks are inside their own invocation's owned lifetime: their
// output/announcement is captured before the group is reaped, so a backgrounded
// descendant is gone while the HOOK_END diagnostic is still delivered.
func TestSessionEndHookDescendantIsReapedWhileDiagnosticsRemain(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	childPIDFile := filepath.Join(dir, "child.pid")
	script := "sleep 300 & echo $! > " + shellquote.Literal(childPIDFile)

	var mu sync.Mutex
	var hookEnds []events.HookEndData
	r := NewRunner(nil, "")
	r.SetEventCallback(func(kind events.EventKind, data events.EventData) {
		if kind != events.EventHookEnd {
			return
		}
		if end, ok := data.(events.HookEndData); ok {
			mu.Lock()
			hookEnds = append(hookEnds, end)
			mu.Unlock()
		}
	})
	r.Add(plugin.HookSessionEnd, plugin.RegisteredHook{Type: "command", Command: script, Timeout: 30, PluginDir: "/tmp"})

	r.RunSessionEnd(context.Background(), Input{CWD: "/tmp", HookEventName: "SessionEnd"})

	mu.Lock()
	got := len(hookEnds)
	mu.Unlock()
	if got != 1 {
		t.Fatalf("SessionEnd hook diagnostics = %d HOOK_END events, want 1", got)
	}
	childPID := awaitRecordedPID(t, childPIDFile)
	awaitProcessGone(t, childPID)
}

// awaitRecordedPID polls for a pid file a hook's script writes. The script
// writes it synchronously before the interesting state, so the wait only covers
// process scheduling, never the mechanism under test.
func awaitRecordedPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("process id file %s was not written", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// awaitProcessGone polls until pid names no process. A just-killed descendant
// can linger as a zombie until its reaper collects it, so this bounds on ESRCH
// rather than on the signal returning.
func awaitProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("process %d survived the hook's owned lifetime", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// awaitGroupGone polls until process group pgid holds no process.
func awaitGroupGone(t *testing.T, pgid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for syscall.Kill(-pgid, 0) != syscall.ESRCH {
		if time.Now().After(deadline) {
			t.Fatalf("process group %d survived the hook's owned lifetime", pgid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
