package calc

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// init hangs before any test when FIXTURE_INIT_HANG is set, where the testing
// package's own timeout cannot reach it.
func init() {
	if os.Getenv("FIXTURE_INIT_HANG") != "" {
		time.Sleep(time.Hour)
	}
}

// TestMain exits successfully without running a test when FIXTURE_EXIT_EARLY
// is set, so the binary writes no coverage profile.
func TestMain(m *testing.M) {
	if os.Getenv("FIXTURE_EXIT_EARLY") != "" {
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// recordRun appends the running test's name to the file FIXTURE_RUN_LOG names,
// so a test of the profile cache can see which tests really ran.
func recordRun(t *testing.T) {
	path := os.Getenv("FIXTURE_RUN_LOG")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(t.Name() + "\n"); err != nil {
		t.Fatal(err)
	}
}

func TestAbsNegative(t *testing.T) {
	recordRun(t)
	if Abs(-2) != 2 {
		t.Fatal("Abs(-2)")
	}
}

func TestAbsNegativeAgain(t *testing.T) {
	recordRun(t)
	if Abs(-3) != 3 {
		t.Fatal("Abs(-3)")
	}
}

func TestSignZero(t *testing.T) {
	recordRun(t)
	if Sign(0) != 0 {
		t.Fatal("Sign(0)")
	}
}

func TestSignPositiveUnchecked(t *testing.T) {
	recordRun(t)
	_ = Sign(5)
}

func TestFailsWhenAsked(t *testing.T) {
	if os.Getenv("FIXTURE_FAIL") != "" {
		t.Fatal("asked to fail")
	}
}

// A panic ends the binary: no later test runs, and the profile is not written.
func TestPanicsWhenAsked(t *testing.T) {
	if os.Getenv("FIXTURE_PANIC") != "" {
		panic("asked to panic")
	}
}

// A child that outlives the test and holds its output open.
func TestLeavesAChildWhenAsked(t *testing.T) {
	if os.Getenv("FIXTURE_LEAK") == "" {
		return
	}
	cmd := exec.Command("sleep", "60")
	cmd.Stdout = os.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
}
