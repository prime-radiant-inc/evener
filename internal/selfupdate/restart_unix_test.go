//go:build unix

package selfupdate

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// TestRestartHelper is the exec target: when invoked with EVENER_RESTART_HELPER
// set, it prints its argv and pid, then exits. Otherwise it is a no-op test.
func TestRestartHelper(t *testing.T) {
	if os.Getenv("EVENER_RESTART_HELPER") == "" {
		return
	}
	mode := os.Getenv("EVENER_RESTART_HELPER")
	if mode == "exec" {
		// First hop: exec ourselves again in "print" mode. The pid must survive.
		os.Setenv("EVENER_RESTART_HELPER", "print")
		if err := Restart(os.Args[0], []string{"-test.run=TestRestartHelper", "hub", "-addr", "127.0.0.1:0"}); err != nil {
			os.Stderr.WriteString("restart failed: " + err.Error())
			os.Exit(3)
		}
	}
	os.Stdout.WriteString("pid=" + strconv.Itoa(os.Getpid()) + " argv=" + strings.Join(os.Args[1:], " ") + "\n")
	os.Exit(0)
}

func TestRestartKeepsPIDAndPassesArgs(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestRestartHelper")
	cmd.Env = append(os.Environ(), "EVENER_RESTART_HELPER=exec")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helper: %v\n%s", err, out)
	}
	if cmd.Process == nil {
		t.Fatal("no process")
	}
	want := "pid=" + strconv.Itoa(cmd.Process.Pid) + " argv=-test.run=TestRestartHelper hub -addr 127.0.0.1:0\n"
	if string(out) != want {
		t.Fatalf("helper output = %q, want %q", out, want)
	}
}

func TestRestartMissingBinaryReturnsError(t *testing.T) {
	err := Restart("/nonexistent/evener", []string{"hub"})
	if err == nil {
		t.Fatal("expected error")
	}
}
