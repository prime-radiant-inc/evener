package dev

// test-cpu-exec is the `go test -exec` wrapper the gate's timing capture runs
// every test binary under. It runs the binary with the caller's stdio,
// forwards the signals that stop a run, and once the binary exits prints the
// CPU time it spent (user plus system, including every child it waited for) as
// one line of the package's own output. In a `go test -json` stream that line
// is an output event of the package, so timing-parse turns it into the
// package's CPU row without any other bookkeeping.
//
// CPU time is the budget's measure because the package's wall time is mostly
// how long its threads waited for a core: the gate oversubscribes the runner,
// and a serial package's wall time follows what happens to run beside it.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// testCPUMarker starts the line the wrapper prints; the seconds follow a tab.
const testCPUMarker = "evener-test-cpu-seconds"

func testCPUExecMain(args []string) int {
	if len(args) < 1 {
		_, _ = fmt.Fprint(os.Stderr, "usage: evener-dev test-cpu-exec BINARY [ARGS...]\n\n"+
			"Run a test binary (as go test -exec) and print the CPU seconds it spent,\n"+
			"children included, as a "+testCPUMarker+" line after it exits.\n")
		return 2
	}
	cmd := exec.Command(args[0], args[1:]...) //nolint:noctx // the binary runs until it exits; stopping it is the forwarded signals' job
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)
	defer signal.Stop(signals)
	if err := cmd.Start(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "evener-dev test-cpu-exec: %v\n", err)
		return 1
	}
	go func() {
		for s := range signals {
			_ = cmd.Process.Signal(s)
		}
	}()
	err := cmd.Wait()
	state := cmd.ProcessState
	cpu := state.UserTime() + state.SystemTime()
	_, _ = fmt.Fprintf(os.Stdout, "%s\t%s\n", testCPUMarker, formatSeconds(cpu.Seconds()))
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return exit.ExitCode()
	default:
		_, _ = fmt.Fprintf(os.Stderr, "evener-dev test-cpu-exec: %v\n", err)
		return 1
	}
}
