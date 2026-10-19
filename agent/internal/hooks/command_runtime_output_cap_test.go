package hooks

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/cappedoutput"
	"primeradiant.com/evener/agent/plugin"
)

// floodCommand is a bash fragment that writes head, then n filler bytes, then
// tail to the stream redirect names ("" for stdout, ">&2" for stderr).
func floodCommand(head string, n int, tail, redirect string) string {
	return fmt.Sprintf(`printf %%s %q %[4]s; head -c %[2]d /dev/zero | tr '\0' x %[4]s; printf %%s %[3]q %[4]s`, head, n, tail, redirect)
}

// assertCappedCapture fails unless got stays within capBytes plus the
// omission marker for a total-byte stream, and still starts with head and
// ends with tail: the cap bounds memory without losing either end.
func assertCappedCapture(t *testing.T, stream, got string, capBytes, total int, head, tail string) {
	t.Helper()
	if limit := capBytes + len(fmt.Sprintf(cappedoutput.OmissionMarker, total)); len(got) > limit {
		t.Fatalf("%s capture = %d bytes for a %d-byte stream, want at most %d (the %d-byte cap plus the omission marker)", stream, len(got), total, limit, capBytes)
	}
	if !strings.HasPrefix(got, head) {
		t.Fatalf("%s capture lost the stream's head %q; starts with %q", stream, head, got[:min(len(got), 32)])
	}
	if !strings.HasSuffix(got, tail) {
		t.Fatalf("%s capture lost the stream's tail %q; ends with %q", stream, tail, got[max(0, len(got)-32):])
	}
}

// TestCommandHookCaptureIsBoundedPerStream pins #4561: a hook that floods its
// stdout and stderr far past the cap still runs to completion with its exit
// code, and each stream's capture holds at most the cap, keeping the stream's
// head and tail. The production default applies when no override is set.
func TestCommandHookCaptureIsBoundedPerStream(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		override int
		capBytes int
	}{
		{name: "override", override: 4096, capBytes: 4096},
		{name: "production default", capBytes: maxHookOutputBytes},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			filler := 2*tc.capBytes + 1
			command := floodCommand("OUT-HEAD", filler, "OUT-TAIL", "") + "; " +
				floodCommand("ERR-HEAD", filler, "ERR-TAIL", ">&2") + "; exit 3"
			runtime := newSystemCommandHookRuntime()
			runtime.outputCapBytes = tc.override

			hr, err := executeCommandHookWithRuntime(context.Background(),
				plugin.RegisteredHook{Type: "command", Command: command, Timeout: 30},
				Input{CWD: t.TempDir(), HookEventName: string(plugin.HookPostToolUse)}, runtime)
			if err != nil {
				t.Fatalf("flooding hook returned an infrastructure error: %v", err)
			}
			if hr.ExitCode != 3 {
				t.Fatalf("ExitCode = %d, want the hook's own exit 3", hr.ExitCode)
			}
			total := len("OUT-HEAD") + filler + len("OUT-TAIL")
			assertCappedCapture(t, "stdout", hr.Stdout, tc.capBytes, total, "OUT-HEAD", "OUT-TAIL")
			assertCappedCapture(t, "stderr", hr.Stderr, tc.capBytes, total, "ERR-HEAD", "ERR-TAIL")
		})
	}
}

// TestPreToolUseExit2StillBlocksWhenItsStderrOverflowsTheCap pins that the cap
// never changes a hook's decision: a PreToolUse hook exiting 2 blocks the tool
// on its exit code, and its capped stderr still carries the reason's opening.
func TestPreToolUseExit2StillBlocksWhenItsStderrOverflowsTheCap(t *testing.T) {
	t.Parallel()
	const capBytes = 4096
	runtime := newSystemCommandHookRuntime()
	runtime.outputCapBytes = capBytes
	runner := newRunner(nil, "")
	runner.commandHookRuntime = runtime
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{
		Matcher: "*", Type: "command", Timeout: 30,
		Command: floodCommand("REASON-HEAD", 1<<20, "REASON-TAIL", ">&2") + "; exit 2",
	})

	r := runner.RunPreToolUse(context.Background(), Input{CWD: t.TempDir(), HookEventName: "PreToolUse", ToolName: "Bash"})
	if !r.Denied {
		t.Fatal("an exit-2 PreToolUse hook must block even when its stderr overflows the capture cap")
	}
	total := len("REASON-HEAD") + 1<<20 + len("REASON-TAIL")
	assertCappedCapture(t, "deny reason", r.DenyMessage, capBytes, total, "REASON-HEAD", "REASON-TAIL")
}
