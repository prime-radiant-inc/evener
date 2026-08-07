// serf-test-dev-tooling runs the scripts/<name>-selftest.sh suites as one
// parallel wave: every suite at once, each in its own process group with a
// private TMPDIR, quiet on success, a failing suite's whole log replayed. A
// suite that passes but leaves files in its TMPDIR fails — suites clean up
// after themselves. HUP/INT/TERM forward to every running suite's process
// group, escalating to KILL after -kill-grace. Invoked by the Makefile
// test-dev-tooling target as
// `go run ./cmd/serf-test-dev-tooling $(DEV_TOOLING_TEST_SCRIPTS)`.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	scriptsDir := flag.String("scripts-dir", "scripts", "directory holding <name>-selftest.sh suites")
	killGrace := flag.Duration("kill-grace", 5*time.Second, "how long a TERMed suite gets before KILL")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: serf-test-dev-tooling [-scripts-dir dir] [-kill-grace d] suite...")
		os.Exit(2)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	os.Exit(runWave(waveConfig{
		ScriptsDir: *scriptsDir,
		Suites:     flag.Args(),
		KillGrace:  *killGrace,
		Out:        os.Stdout,
		Signals:    signals,
	}))
}
