package evener_test

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// The installer script's failure paths, run for real: no stubbed curl, no
// stubbed go, and no network. A port nothing is listening on is a real
// transport failure every machine can produce, which is what makes these
// deterministic offline.
//
// #1205 declined the class these replace -- a PATH full of fake binaries,
// which proves what the fakes were written to prove and nothing about the
// tool.

// requireInstallerScriptTools is requireFuzzScriptTools' shape for this
// script: a Unix shell, and curl for the case that reaches a download.
func requireInstallerScriptTools(t *testing.T, tools ...string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("scripts/ops/install-golangci-lint.sh requires a Unix shell")
	}
	for _, tool := range append([]string{"bash"}, tools...) {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH: %v", tool, err)
		}
	}
}

// installerEnv is a minimal environment for the script: enough to find its
// tools, a HOME of its own so no ~/.curlrc or ~/.netrc joins in, and no proxy
// for the loopback address the offline case points at -- a proxied CI runner
// would otherwise hand the request to a proxy that is very much listening.
func installerEnv(t *testing.T, extra ...string) []string {
	t.Helper()
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"no_proxy=127.0.0.1",
		"NO_PROXY=127.0.0.1",
	}
	return append(env, extra...)
}

// runInstaller runs the script with that environment and returns its exit code
// and combined output.
func runInstaller(t *testing.T, env ...string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", repoScriptPath(t, "scripts/ops/install-golangci-lint.sh"))
	cmd.Env = installerEnv(t, env...)
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

// closedPortURL reserves a loopback port, releases it, and returns a URL for
// it. Between the release and the fetch nothing else will have taken it, and a
// connection there is refused rather than routed.
func closedPortURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a loopback port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// TestInstallerRefusesAnAttemptCountItCannotUse covers the guard that runs
// before anything is fetched: a bad knob must cost nothing and say what it
// wanted.
func TestInstallerRefusesAnAttemptCountItCannotUse(t *testing.T) {
	t.Parallel()
	requireInstallerScriptTools(t)
	code, out := runInstaller(t, "EVENER_GOLANGCI_INSTALL_ATTEMPTS=abc")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", code, out)
	}
	if !strings.Contains(out, "EVENER_GOLANGCI_INSTALL_ATTEMPTS must be a positive integer") {
		t.Fatalf("output = %q, want the knob and what it takes", out)
	}
	// Nothing was attempted: the script's own attempt diagnostics are the
	// evidence, since they are written per attempt and nothing else is.
	if strings.Contains(out, "install attempt") || strings.Contains(out, "did not install in") {
		t.Fatalf("output = %q, want no attempt made before the guard", out)
	}
}

// TestInstallerFailsAfterEveryAttemptWhenTheDownloadCannotStart pins the retry
// loop itself: a transport that refuses gets each attempt, and the script says
// how many it spent before giving up.
func TestInstallerFailsAfterEveryAttemptWhenTheDownloadCannotStart(t *testing.T) {
	t.Parallel()
	requireInstallerScriptTools(t, "curl")
	code, out := runInstaller(t,
		"EVENER_GOLANGCI_INSTALLER_URL="+closedPortURL(t),
		"EVENER_GOLANGCI_INSTALL_ATTEMPTS=2",
		// Without these the test would wait out the default retries and
		// backoff to prove exactly the same thing.
		"EVENER_GOLANGCI_CURL_RETRIES=0",
		"EVENER_GOLANGCI_INSTALL_BACKOFF=0",
	)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1\n%s", code, out)
	}
	// The script's own diagnostics, not curl's wording: one retry announced
	// between two attempts, then the line that gives up naming the count.
	if got := strings.Count(out, "install attempt 1 of 2 failed; retrying"); got != 1 {
		t.Fatalf("the retry was announced %d times, want once\n%s", got, out)
	}
	if !strings.Contains(out, "did not install in 2 attempt(s)") {
		t.Fatalf("output = %q, want the giving-up line to name the attempts", out)
	}
}
