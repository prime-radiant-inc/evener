//go:build linux

package valueexpr

import (
	"errors"
	"testing"
	"time"
)

// The command timeout must be a hard bound even when a descendant escapes
// the process group and keeps the captured pipes open: the group SIGKILL
// reaches the direct child, but a setsid'd grandchild survives it holding
// stdout, which would leave Wait — and a whole credential resolve or config
// load — blocked until that grandchild exits. WaitDelay closes the pipes
// once the budget elapses instead. The 5s bound is generous on purpose: the
// test pins that evaluate RETURNS, not the exact moment it returns.
func TestCommandTimeoutHardBoundDespiteEscapedDescendant(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	commandTimeout = 500 * time.Millisecond

	start := time.Now()
	_, err := evaluate(`setsid sh -c "sleep 10" & exec sleep 30`)
	if err == nil {
		t.Fatal("evaluate returned nil; want the timeout error")
	}
	if cmdErr, ok := errors.AsType[*CommandError](err); !ok || !cmdErr.Timeout {
		t.Fatalf("err = %v; want the command timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("evaluate blocked %v; the timeout is not a hard bound", elapsed)
	}
}
