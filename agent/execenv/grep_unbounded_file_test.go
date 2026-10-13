//go:build linux || darwin

package execenv

import (
	"errors"
	"testing"
)

// Grep on a real never-ending file terminates and reports nothing. /dev/zero
// reads forever while stat reports no size to warn the reader — the shape the
// 4GB-container OOM hit through /proc/kcore (#4480) — so this test only
// finishes because the capped read stops after grepMaxFileBytes and the walk
// skips the file. Naming the device also pins that the /dev skip a walk
// applies never blocks reading a file the caller explicitly targeted.
func TestGrepTerminatesOnADeviceFileNamedAsTheTarget(t *testing.T) {
	t.Parallel()
	env := NewLocalExecutionEnvironment(t.TempDir())
	defer env.Cleanup()
	env.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }

	got, err := env.Grep(t.Context(), "needle", "/dev/zero", "", false, 100, "content")
	if err != nil {
		t.Fatalf("grep /dev/zero: %v", err)
	}
	if got != "" {
		t.Fatalf("grep /dev/zero = %q, want no matches from a zero stream", got)
	}

	got, err = env.Grep(t.Context(), "needle", "/dev/null", "", false, 100, "content")
	if err != nil {
		t.Fatalf("grep /dev/null: %v", err)
	}
	if got != "" {
		t.Fatalf("grep /dev/null = %q, want the empty file's absence of matches", got)
	}
}
