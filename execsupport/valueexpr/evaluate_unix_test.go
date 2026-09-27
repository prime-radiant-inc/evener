//go:build unix

package valueexpr

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The value contract is the evaluator's: trimmed verbatim output, and no
// output refused. These drive the real executor through evaluate. The
// commands are POSIX shell syntax (realRunCommand runs sh -c), so the tests
// take a unix tag: the windows executor runs cmd /c and these commands do
// not exist there.
func TestRealExecutorThroughEvaluate(t *testing.T) {
	t.Run("trims stdout", func(t *testing.T) {
		ResetForTest()
		res, err := evaluate("printf '  real-token \\n'")
		if err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if res.Value != "real-token" {
			t.Fatalf("got %q", res.Value)
		}
	})

	t.Run("keeps multi-line output verbatim once trimmed", func(t *testing.T) {
		ResetForTest()
		res, err := evaluate("printf 'line-one\\nline-two\\n'")
		if err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if res.Value != "line-one\nline-two" {
			t.Fatalf("got %q", res.Value)
		}
	})

	t.Run("no output is a failure", func(t *testing.T) {
		ResetForTest()
		_, err := evaluate("true")
		if err == nil || !strings.Contains(err.Error(), "no output") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("no stdout with a stderr complaint carries the complaint", func(t *testing.T) {
		ResetForTest()
		_, err := evaluate("echo boom >&2")
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("got %v; want the command's stderr as the diagnosis", err)
		}
	})

	t.Run("whitespace-only stdout with a stderr complaint carries the complaint", func(t *testing.T) {
		ResetForTest()
		_, err := evaluate("printf '  \\n'; echo boom >&2")
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("got %v; want the command's stderr as the diagnosis, not the generic no-output error", err)
		}
	})
}

// These drive the real executor directly for the failure taxonomy it owns.
func TestRealRunCommand(t *testing.T) {
	t.Run("non-zero exit is a failure with stderr's first line", func(t *testing.T) {
		_, err := realRunCommand("printf out; printf 'boom\\nnoise' >&2; exit 3")
		if err == nil {
			t.Fatal("non-zero exit reported success")
		}
		var cerr *CommandError
		if !errors.As(err, &cerr) || cerr.Status != 3 || !strings.Contains(cerr.Error(), "boom") || strings.Contains(cerr.Error(), "noise") {
			t.Fatalf("got %v (%T)", err, err)
		}
		if strings.Contains(cerr.Error(), "out") {
			t.Fatalf("stdout leaked into the error: %v", cerr)
		}
	})

	t.Run("oversized output is a failure", func(t *testing.T) {
		_, err := realRunCommand("head -c 1048577 /dev/zero")
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("timeout kills the command", func(t *testing.T) {
		ResetForTest()
		old := commandTimeout
		commandTimeout = 100 * time.Millisecond
		t.Cleanup(func() { commandTimeout = old; ResetForTest() })
		_, err := realRunCommand("sleep 5")
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("got %v", err)
		}
	})
}
