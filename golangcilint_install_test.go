package evener_test

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The installer script's paths, run for real: real curl, real bash, real go,
// and no network beyond loopback. Two shapes of loopback server stand in for
// the release CDN.
//
// The failure cases use a listener that stays up for the test and closes every
// connection without answering, which curl reports as an empty reply. It is
// held rather than reserved-and-released because releasing a port leaves a
// window in which anything on the machine can take it, and the test would then
// be measuring whatever took it.
//
// The version-check cases serve a real install script over that loopback,
// which writes a small executable printing a version line the test chooses.
// The script under test then does what it does with any installer: downloads
// it, runs it, and compares what the installed binary reports against the pin.
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
	// bash runs it, awk reads the pin out of .tool-versions, and go answers
	// where the bindir is -- the script reaches all three before it decides
	// anything, so a missing one is a skip and not a failure.
	for _, tool := range append([]string{"bash", "awk", "go"}, tools...) {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH: %v", tool, err)
		}
	}
	// And the floor the script itself enforces: below it every one of these
	// tests would be asserting the version refusal instead of its own subject.
	if major, minor, ok := curlVersion(t); !ok || major < 7 || (major == 7 && minor < 71) {
		t.Skipf("curl %d.%d is below the 7.71 this script requires", major, minor)
	}
}

// curlVersion reads the version the way the script reads it: the second field
// of the first line of `curl --version`.
func curlVersion(t *testing.T) (major, minor int, ok bool) {
	t.Helper()
	out, err := exec.Command("curl", "--version").Output()
	if err != nil {
		return 0, 0, false
	}
	line, _, _ := strings.Cut(string(out), "\n")
	field := strings.Fields(line)
	if len(field) < 2 {
		return 0, 0, false
	}
	parts := strings.Split(field[1], ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	minor, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// installerEnv is a minimal environment for the script: enough to find its
// tools, a HOME of its own so no ~/.curlrc or ~/.netrc joins in, and no proxy
// for the loopback address the offline case points at -- a proxied CI runner
// would otherwise hand the request to a proxy that is very much listening.
func installerEnv(t *testing.T, gopath string, extra ...string) []string {
	t.Helper()
	// GOPATH is set rather than left to follow HOME: the isolation the tests
	// depend on -- the installed binary landing somewhere temporary and not in
	// the developer's ~/go/bin -- should be stated here rather than derived
	// two steps away. GOENV=off keeps a `go env -w` file from redirecting it
	// back.
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"GOPATH=" + gopath,
		"GOENV=off",
		"no_proxy=127.0.0.1",
		"NO_PROXY=127.0.0.1",
		// Which path the script takes must not depend on whether someone has
		// run `make build-dev` in this worktree: these tests are about the
		// unbounded one. The bounded path gets its own test when bounded-list
		// reaches main (#1263).
		"EVENER_GOLANGCI_DEV_BIN=" + filepath.Join(t.TempDir(), "no-evener-dev-here"),
	}
	return append(env, extra...)
}

// runInstaller runs the script with that environment and returns its exit code
// and combined output.
func runInstaller(t *testing.T, env ...string) (int, string) {
	t.Helper()
	code, out, _ := runInstallerIn(t, env...)
	return code, out
}

// runInstallerIn is runInstaller for a case that wants to look at what was
// installed: it returns the GOPATH the run was given.
func runInstallerIn(t *testing.T, env ...string) (int, string, string) {
	t.Helper()
	gopath := t.TempDir()
	cmd := exec.Command("bash", repoScriptPath(t, "scripts/ops/install-golangci-lint.sh"))
	cmd.Env = installerEnv(t, gopath, env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		exit, ok := errors.AsType[*exec.ExitError](err)
		if !ok {
			t.Fatalf("running the installer: %v\n%s", err, out)
		}
		code = exit.ExitCode()
	}
	return code, string(out), gopath
}

// emptyReplyURL starts a listener that accepts connections and closes them
// without answering, and returns its URL. The listener stays up for the test,
// so the port cannot be taken by anything else between the reservation and the
// fetch, and every attempt gets the same real transport failure: a connection
// that is accepted and then hangs up, which curl reports as an empty reply.
func emptyReplyURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening on loopback: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return // the listener was closed at the end of the test
			}
			_ = conn.Close()
		}
	}()
	return "http://" + listener.Addr().String()
}

// TestInstallerRefusesABackoffBashWouldReadAsOctal covers the other guard: 08
// passes a naive digits-only check and then fails inside the arithmetic that
// uses it, which is a failure in the retry rather than in the validation.
func TestInstallerRefusesABackoffItCannotUse(t *testing.T) {
	t.Parallel()
	requireInstallerScriptTools(t)
	// 08 is the one worth naming: it passes a digits-only check and then fails
	// inside the arithmetic that uses it, because bash reads a leading zero as
	// octal. The others are the same table its siblings have.
	for _, value := range []string{"08", "abc", "21"} {
		code, out := runInstaller(t, "EVENER_GOLANGCI_INSTALL_BACKOFF="+value)
		if code != 2 {
			t.Fatalf("EVENER_GOLANGCI_INSTALL_BACKOFF=%s: exit code = %d, want 2\n%s", value, code, out)
		}
		if !strings.Contains(out, "EVENER_GOLANGCI_INSTALL_BACKOFF must be a whole number") {
			t.Fatalf("EVENER_GOLANGCI_INSTALL_BACKOFF=%s: output = %q, want the knob and what it takes", value, out)
		}
		if strings.Contains(out, "install attempt") || strings.Contains(out, "did not install in") {
			t.Fatalf("EVENER_GOLANGCI_INSTALL_BACKOFF=%s: output = %q, want no attempt made before the guard", value, out)
		}
	}
}

// TestInstallerRefusesAnAttemptCountItCannotUse covers the guard that runs
// before anything is fetched: a bad knob must cost nothing and say what it
// wanted.
func TestInstallerRefusesAnAttemptCountItCannotUse(t *testing.T) {
	t.Parallel()
	requireInstallerScriptTools(t)
	for _, value := range []string{"abc", "0", "21", "99999999999999999999"} {
		code, out := runInstaller(t, "EVENER_GOLANGCI_INSTALL_ATTEMPTS="+value)
		if code != 2 {
			t.Fatalf("EVENER_GOLANGCI_INSTALL_ATTEMPTS=%s: exit code = %d, want 2\n%s", value, code, out)
		}
		if !strings.Contains(out, "EVENER_GOLANGCI_INSTALL_ATTEMPTS must be a whole number") {
			t.Fatalf("EVENER_GOLANGCI_INSTALL_ATTEMPTS=%s: output = %q, want the knob and what it takes", value, out)
		}
		if strings.Contains(out, "install attempt") || strings.Contains(out, "did not install in") {
			t.Fatalf("EVENER_GOLANGCI_INSTALL_ATTEMPTS=%s: output = %q, want no attempt made before the guard", value, out)
		}
	}
}

// TestInstallerFailsAfterEveryAttemptWhenTheDownloadCannotStart pins the retry
// loop itself: a transport that refuses gets each attempt, and the script says
// how many it spent before giving up.
func TestInstallerFailsAfterEveryAttemptWhenTheDownloadCannotStart(t *testing.T) {
	t.Parallel()
	requireInstallerScriptTools(t, "curl")
	code, out := runInstaller(t,
		"EVENER_GOLANGCI_INSTALLER_URL="+emptyReplyURL(t),
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

// firstOf drops the args file for the cases that do not inspect it.
func firstOf(url, _ string) string { return url }

// pinnedGolangciVersion reads the pin the script will check against, from the
// same file the script reads it from.
func pinnedGolangciVersion(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(repoScriptPath(t, ".tool-versions"))
	if err != nil {
		t.Fatalf("reading .tool-versions: %v", err)
	}
	for line := range strings.Lines(string(raw)) {
		if field := strings.Fields(line); len(field) == 2 && field[0] == "golangci-lint" {
			return field[1]
		}
	}
	t.Fatal("no golangci-lint row in .tool-versions")
	return ""
}

// installerServing starts a loopback server whose install.sh writes an
// executable into the bindir it is given, printing reports as its version
// line. An empty reports writes nothing, which is the installer that ran and
// produced no binary.
// installerScript is the install.sh the loopback servers serve: it writes an
// executable printing reports into the bindir it is given, and records the
// arguments it was handed when a caller asked for them.
func installerScript(t *testing.T, reports string, argsFile ...string) string {
	t.Helper()
	script := "#!/bin/sh\n"
	if len(argsFile) == 1 {
		script += "printf '%s\\n' \"$*\" > " + shellQuote(argsFile[0]) + "\n"
	}
	script += "bindir=\"\"\nwhile [ $# -gt 0 ]; do\n  case \"$1\" in -b) bindir=\"$2\"; shift 2 ;; *) shift ;; esac\ndone\nmkdir -p \"$bindir\"\n"
	if reports != "" {
		script += "cat > \"$bindir/golangci-lint\" <<EOF\n#!/bin/sh\necho '" + reports + "'\nEOF\nchmod +x \"$bindir/golangci-lint\"\n"
	}
	return script
}

// installerServing starts a loopback server whose install.sh writes an
// executable into the bindir it is given, printing reports as its version
// line. An empty reports writes nothing, which is the installer that ran and
// produced no binary.
func installerServing(t *testing.T, reports string) (url, argsFile string) {
	t.Helper()
	argsFile = filepath.Join(t.TempDir(), "installer-args")
	script := installerScript(t, reports, argsFile)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, script)
	}))
	t.Cleanup(server.Close)
	return server.URL, argsFile
}

// shellQuote is enough quoting for a temporary directory's path inside the
// served script.
func shellQuote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

func TestInstallerAcceptsTheBinaryThatReportsThePin(t *testing.T) {
	t.Parallel()
	requireInstallerScriptTools(t, "curl")
	version := pinnedGolangciVersion(t)
	url, argsFile := installerServing(t, "golangci-lint has version "+version+" built with go1.27.0")
	code, out, gopath := runInstallerIn(t,
		"EVENER_GOLANGCI_INSTALLER_URL="+url,
		"EVENER_GOLANGCI_INSTALL_ATTEMPTS=1",
		"EVENER_GOLANGCI_CURL_RETRIES=0",
	)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 for a binary reporting the pin\n%s", code, out)
	}
	// The installer was asked for the pinned release, not whatever it felt
	// like: the tag is the other half of what this script is for.
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("reading what the installer was given: %v", err)
	}
	if !strings.Contains(string(args), "v"+version) {
		t.Fatalf("the installer was given %q, want the pinned v%s", strings.TrimSpace(string(args)), version)
	}
	// And it landed in the GOPATH this run was given, which is the isolation
	// the whole test depends on: nothing here writes to the developer's.
	if _, err := os.Stat(filepath.Join(gopath, "bin", "golangci-lint")); err != nil {
		t.Fatalf("the binary is not under the run's own GOPATH: %v", err)
	}
}

func TestInstallerRefusesABinaryThatReportsAnotherVersion(t *testing.T) {
	t.Parallel()
	requireInstallerScriptTools(t, "curl")
	version := pinnedGolangciVersion(t)
	code, out := runInstaller(t,
		"EVENER_GOLANGCI_INSTALLER_URL="+firstOf(installerServing(t, "golangci-lint has version 0.0.1 built with go1.27.0")),
		"EVENER_GOLANGCI_INSTALL_ATTEMPTS=1",
		"EVENER_GOLANGCI_CURL_RETRIES=0",
	)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 for a binary reporting another version\n%s", code, out)
	}
	if !strings.Contains(out, "not the pinned v"+version) {
		t.Fatalf("output = %q, want the mismatch named against the pin", out)
	}
}

func TestInstallerRefusesAnInstallThatProducedNoBinary(t *testing.T) {
	t.Parallel()
	requireInstallerScriptTools(t, "curl")
	code, out := runInstaller(t,
		// The installer exits 0 and writes nothing, which is the hole pipefail
		// cannot see: the pipeline succeeded and there is no binary.
		"EVENER_GOLANGCI_INSTALLER_URL="+firstOf(installerServing(t, "")),
		"EVENER_GOLANGCI_INSTALL_ATTEMPTS=1",
		"EVENER_GOLANGCI_CURL_RETRIES=0",
	)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 when nothing was installed\n%s", code, out)
	}
	if !strings.Contains(out, "did not run after installation") {
		t.Fatalf("output = %q, want the branch that says the binary does not run", out)
	}
}

// installerServingAfterOneFailure is the same served installer behind one
// failure: the first request gets its connection closed without a reply, and
// the second is served. An empty reply is not one of the transient HTTP
// statuses curl retries on its own, so only --retry-all-errors recovers from
// it -- which is what makes this a test of that flag rather than of --retry.
func installerServingAfterOneFailure(t *testing.T, reports string) string {
	t.Helper()
	script := installerScript(t, reports)
	var mu sync.Mutex
	served := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		served++
		first := served == 1
		mu.Unlock()
		if first {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijacking the first connection: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		_, _ = io.WriteString(w, script)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func TestInstallerRetriesADownloadThatDiedInTransit(t *testing.T) {
	t.Parallel()
	requireInstallerScriptTools(t, "curl")
	version := pinnedGolangciVersion(t)
	code, out := runInstaller(t,
		"EVENER_GOLANGCI_INSTALLER_URL="+installerServingAfterOneFailure(t, "golangci-lint has version "+version+" built with go1.27.0"),
		// One attempt of the script's own loop, one retry of curl's: whatever
		// recovers here is curl retrying an empty reply, which is what
		// --retry-all-errors buys and what nothing else in this script does.
		"EVENER_GOLANGCI_INSTALL_ATTEMPTS=1",
		"EVENER_GOLANGCI_CURL_RETRIES=1",
	)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: curl retries a download that died in transit\n%s", code, out)
	}
	if strings.Contains(out, "install attempt") {
		t.Fatalf("output = %q, want the script's own loop not to have been used", out)
	}
}

// installerServingTruncatedThenWhole answers the first request with a partial
// script body and closes the connection, then serves the whole thing. curl
// treats the short read as a failure worth retrying, and the retry writes the
// file from the start -- where a pipe would have handed the shell the half it
// had already started running, followed by the whole script.
func installerServingTruncatedThenWhole(t *testing.T, reports string) string {
	t.Helper()
	script := installerScript(t, reports)
	var mu sync.Mutex
	served := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		served++
		first := served == 1
		mu.Unlock()
		if first {
			// A length the body never reaches, so the reply ends early.
			w.Header().Set("Content-Length", strconv.Itoa(len(script)))
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, script[:len(script)/2])
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijacking the truncated response: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		_, _ = io.WriteString(w, script)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func TestInstallerRunsOnlyAWholeInstaller(t *testing.T) {
	t.Parallel()
	requireInstallerScriptTools(t, "curl")
	version := pinnedGolangciVersion(t)
	code, out := runInstaller(t,
		"EVENER_GOLANGCI_INSTALLER_URL="+installerServingTruncatedThenWhole(t, "golangci-lint has version "+version+" built with go1.27.0"),
		"EVENER_GOLANGCI_INSTALL_ATTEMPTS=1",
		"EVENER_GOLANGCI_CURL_RETRIES=1",
	)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: the retry fetches the whole installer\n%s", code, out)
	}
	// One install: the truncated half was never run, so nothing installed
	// twice and nothing ran a fragment.
	if strings.Contains(out, "install attempt") {
		t.Fatalf("output = %q, want the script's own loop not to have been used", out)
	}
}
