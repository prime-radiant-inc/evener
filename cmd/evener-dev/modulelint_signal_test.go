package dev

import (
	"strconv"
	"syscall"
	"testing"
)

func itoa(n int) string { return strconv.Itoa(n) }

// TestLintSignalNameAndExit covers all branches of lintSignalNameAndExit,
// including the default case (unknown signal).
func TestLintSignalNameAndExit(t *testing.T) {
	cases := []struct {
		sig      syscall.Signal
		wantName string
		wantCode int
	}{
		{syscall.SIGHUP, "SIGHUP", 129},
		{syscall.SIGINT, "SIGINT", 130},
		{syscall.SIGTERM, "SIGTERM", 143},
		{syscall.SIGUSR1, "SIG" + itoa(int(syscall.SIGUSR1)), 128 + int(syscall.SIGUSR1)},
		{syscall.SIGUSR2, "SIG" + itoa(int(syscall.SIGUSR2)), 128 + int(syscall.SIGUSR2)},
	}
	for _, c := range cases {
		name, code := lintSignalNameAndExit(c.sig)
		if name != c.wantName || code != c.wantCode {
			t.Errorf("lintSignalNameAndExit(%d) = %q, %d, want %q, %d", c.sig, name, code, c.wantName, c.wantCode)
		}
	}
}
