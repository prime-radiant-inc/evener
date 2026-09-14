package evener_test

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The installer script's failure paths, run for real: no stubbed curl, no
// stubbed go, and no network. A refused local port is a real transport
// failure that every machine can produce, which is what makes these
// deterministic offline.
//
// #1205 declined the class these replace — a PATH full of fake binaries, which
// proves what the fakes were written to prove and nothing about the tool.

func runInstaller(t *testing.T, env ...string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", "scripts/ops/install-golangci-lint.sh")
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		exit, ok := errors.AsType[*exec.ExitError](err)
		if !ok {
			t.Fatalf("running the installer: %v\n%s", err, out)
		}
		code = exit.ExitCode()
	}
	return code, string(out)
}

// TestInstallerRefusesAnAttemptCountItCannotUse covers the guard that runs
// before anything is fetched: a bad knob must cost nothing and say what it
// wanted.
func TestInstallerRefusesAnAttemptCountItCannotUse(t *testing.T) {
	t.Parallel()
	code, out := runInstaller(t, "EVENER_GOLANGCI_INSTALL_ATTEMPTS=abc")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", code, out)
	}
	if !strings.Contains(out, "EVENER_GOLANGCI_INSTALL_ATTEMPTS must be a positive integer") {
		t.Fatalf("output = %q, want the knob and what it takes", out)
	}
	// Nothing was fetched: the script never reached curl, so there is no
	// transport diagnostic in the output.
	if strings.Contains(out, "curl") {
		t.Fatalf("output = %q, want no download attempted before the guard", out)
	}
}

// TestInstallerFailsAfterEveryAttemptWhenTheDownloadCannotStart pins the retry
// loop itself: a transport that refuses gets each attempt, and the script says
// how many it spent before giving up.
func TestInstallerFailsAfterEveryAttemptWhenTheDownloadCannotStart(t *testing.T) {
	t.Parallel()
	code, out := runInstaller(t,
		// Port 1 is reserved and refuses: a real curl failure, offline.
		"EVENER_GOLANGCI_INSTALLER_URL=http://127.0.0.1:1",
		"EVENER_GOLANGCI_INSTALL_ATTEMPTS=2",
		// Without these the test would wait out the default retries and
		// backoff to prove exactly the same thing.
		"EVENER_GOLANGCI_CURL_RETRIES=0",
		"EVENER_GOLANGCI_INSTALL_BACKOFF=0",
	)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1\n%s", code, out)
	}
	if got := strings.Count(out, "Failed to connect to 127.0.0.1 port 1"); got != 2 {
		t.Fatalf("the download was attempted %d times, want 2\n%s", got, out)
	}
	if !strings.Contains(out, "install attempt 1 of 2 failed; retrying") {
		t.Fatalf("output = %q, want the retry said out loud", out)
	}
	if !strings.Contains(out, "did not install in 2 attempt(s)") {
		t.Fatalf("output = %q, want the giving-up line to name the attempts", out)
	}
}
