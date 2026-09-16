package launchconfig

import (
	"testing"
	"time"
)

// daemonIdleArgValue returns the value following flag in args, or "".
func daemonIdleArgValue(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

// TestToArgsDaemonIdleTimeoutZeroRendersExplicitly pins the zero-safe launch
// contract: an unset DaemonIdleTimeout must still reach the child as an
// explicit "0s" (retirement disabled) rather than being omitted, so a daemon
// never mistakes "flag absent" for "use someone else's default".
func TestToArgsDaemonIdleTimeoutZeroRendersExplicitly(t *testing.T) {
	args := ToArgs(Resolved{})
	if got := daemonIdleArgValue(args, "--daemon-idle-timeout"); got != "0s" {
		t.Fatalf("ToArgs(Resolved{}) --daemon-idle-timeout = %q, want explicit %q (args %v)", got, "0s", args)
	}
}

// TestToArgsDaemonIdleTimeoutPositiveRendersDuration pins the positive-value
// rendering through time.Duration.String: one hour renders "1h0m0s".
func TestToArgsDaemonIdleTimeoutPositiveRendersDuration(t *testing.T) {
	args := ToArgs(Resolved{DaemonIdleTimeout: time.Hour})
	if got := daemonIdleArgValue(args, "--daemon-idle-timeout"); got != "1h0m0s" {
		t.Fatalf("ToArgs(Resolved{DaemonIdleTimeout: 1h}) --daemon-idle-timeout = %q, want %q (args %v)", got, "1h0m0s", args)
	}
}
