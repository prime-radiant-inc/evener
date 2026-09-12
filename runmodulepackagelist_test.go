package evener_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This file pins the bounded package-list path of scripts/gate/run-module-tests.sh:
// the per-attempt timeout, the retry, the process-group stop, and the two
// refusals that must never become a retry. Those branches only run when
// discovery is slow or a process will not die, which no ordinary gate run
// reproduces, so they were verified by hand-run stubs through three review
// rounds and nothing stopped them regressing.
//
// It is a Go test rather than a shell suite on purpose. docs/developing-evener/testing.md
// bans building a shell fixture harness around a faked toolchain and names the
// remedy: a tool that can only be tested by faking `go` has outgrown shell, and
// the move is the port, because tooling that accumulates real logic belongs in
// Go under `go test`. runtime_pair_build_test.go is the same shape already in
// the tree - a fixture checkout, a stub `go` on PATH, the real scripts run
// against it - and this follows it.
//
// The same rule bans asserting on the argv a script hands a faked binary, so
// nothing here inspects what the script passed the stub. Every assertion is an
// outcome the operator sees: the exit status, the diagnostic lines, whether a
// retry happened, and whether a stopped attempt's child was still alive when
// the next attempt began.

// packageListFixture is a throwaway checkout the gate script can run inside:
// the script, the libraries it sources, and a `go` on PATH that answers only
// the three subcommands the bounded discovery path uses. The stub never execs
// the real toolchain, so the test cannot recurse into a real gate run.
type packageListFixture struct {
	root  string
	state string
}

// goStub answers `go env`, `go list` and `go test` and nothing else. Its
// behaviour is driven entirely by the environment so one stub serves every
// scenario: STUB_STALL_ATTEMPTS says how many `go list` attempts hang instead
// of answering, STUB_STALL_IGNORES_TERM makes a hanging attempt leave behind a
// child that refuses SIGTERM, and the first attempt that does answer records
// whether that child was still alive.
const goStub = `#!/bin/sh
state=${STUB_STATE:?stub go needs STUB_STATE}
case "$1" in
env)
	shift
	for key in "$@"; do
		case "$key" in
		GOCACHE) printf '%s\n' "$state/gocache" ;;
		GOMODCACHE) printf '%s\n' "$state/gomodcache" ;;
		GOPATH) printf '%s\n' "$state/gopath" ;;
		*) printf '\n' ;;
		esac
	done
	exit 0
	;;
list)
	count=$(cat "$state/attempts" 2>/dev/null || printf '0')
	count=$((count + 1))
	printf '%s' "$count" >"$state/attempts"
	if [ "$count" -le "${STUB_STALL_ATTEMPTS:-0}" ]; then
		if [ -n "${STUB_STALL_IGNORES_TERM:-}" ]; then
			( trap '' TERM; sleep 120 ) &
			printf '%s' "$!" >"$state/child.pid"
			trap '' TERM
		fi
		sleep 120
		exit 0
	fi
	if [ -s "$state/child.pid" ]; then
		child=$(cat "$state/child.pid")
		if kill -0 "$child" 2>/dev/null; then
			printf 'child-alive\n' >>"$state/observed"
		else
			printf 'child-gone\n' >>"$state/observed"
		fi
	fi
	printf 'primeradiant.com/fixture\n'
	exit 0
	;;
test)
	printf 'ok  \tprimeradiant.com/fixture\t0.01s\n'
	exit 0
	;;
esac
printf 'stub go: unexpected subcommand %s\n' "$1" >&2
exit 64
`

// psStub fails exactly the process listing the group-liveness probe uses and
// leaves every other ps invocation alone, so a scenario can ask what the gate
// does when it cannot tell whether a stopped attempt is gone.
const psStub = `#!/bin/sh
for arg in "$@"; do
	case "$arg" in
	-ax*)
		echo "ps: simulated process-table failure" >&2
		exit 1
		;;
	esac
done
exec /bin/ps "$@"
`

func newPackageListFixture(t *testing.T, withFailingPS bool) packageListFixture {
	t.Helper()
	for _, tool := range []string{"perl", "bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is required by the gate script and is not on PATH: %v", tool, err)
		}
	}
	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// scratch-lib resolves TMPDIR with `pwd -P` and the script reports paths
	// back, so the fixture root has to be the resolved spelling.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve fixture root: %v", err)
	}
	for path, mode := range map[string]os.FileMode{
		"scripts/gate/run-module-tests.sh": 0o755,
		"scripts/lib/private-go-home.sh":   0o644,
		"scripts/lib/scratch-lib.sh":       0o644,
		"scripts/lib/gate-surface-lib.sh":  0o644,
	} {
		copyRepositoryFile(t, repoRoot, root, path, mode)
	}
	fixture := packageListFixture{root: root, state: filepath.Join(root, "state")}
	for _, dir := range []string{fixture.state, filepath.Join(root, "tmp"), filepath.Join(root, "fake-bin")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	writeTestFile(t, filepath.Join(root, "fake-bin", "go"), []byte(goStub), 0o755)
	if withFailingPS {
		writeTestFile(t, filepath.Join(root, "fake-bin", "ps"), []byte(psStub), 0o755)
	}
	return fixture
}

// run executes the gate script over a single module named "." and returns its
// combined output and exit status. The attempt count and the per-attempt budget
// are the two knobs the branches under test turn on.
func (f packageListFixture) run(t *testing.T, attempts int, stubEnv ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(filepath.Join(f.root, "scripts", "gate", "run-module-tests.sh"), "-short", "-count=1")
	cmd.Dir = f.root
	cmd.Env = append(os.Environ(),
		"PATH="+filepath.Join(f.root, "fake-bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TMPDIR="+filepath.Join(f.root, "tmp"),
		"MODULES=.",
		"WEB=0",
		"STUB_STATE="+f.state,
		"EVENER_ROOT_PACKAGE_LIST_TIMEOUT=2",
		fmt.Sprintf("EVENER_ROOT_PACKAGE_LIST_ATTEMPTS=%d", attempts),
	)
	cmd.Env = append(cmd.Env, stubEnv...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run gate script: %v\noutput:\n%s", err, out)
	}
	return string(out), exitErr.ExitCode()
}

func requireContains(t *testing.T, out, want, why string) {
	t.Helper()
	if !strings.Contains(out, want) {
		t.Errorf("%s\nwanted output to contain %q\noutput:\n%s", why, want, out)
	}
}

func requireNotContains(t *testing.T, out, unwanted, why string) {
	t.Helper()
	if strings.Contains(out, unwanted) {
		t.Errorf("%s\nwanted output NOT to contain %q\noutput:\n%s", why, unwanted, out)
	}
}

// TestPackageListRetriesThenSucceeds pins the flake this whole path exists for:
// a first attempt slower than the budget must not fail the gate, and a run that
// only passed because of the retry must say so.
func TestPackageListRetriesThenSucceeds(t *testing.T) {
	t.Parallel()
	fixture := newPackageListFixture(t, false)
	out, status := fixture.run(t, 3, "STUB_STALL_ATTEMPTS=1")
	if status != 0 {
		t.Errorf("a package list that succeeds on the second attempt must pass the gate; exit %d\noutput:\n%s", status, out)
	}
	requireContains(t, out, "go list ./... attempt 1 of 3 timed out after 2s; retrying.",
		"the retry itself has to be reported, or a degrading host stays invisible behind a green run")
	requireContains(t, out, "WARNING:",
		"the replayed notice is a warning, printed before the module's verdict")
	requireContains(t, out, "PASS  .",
		"the promoted package list is the one the tests ran against")
}

// TestPackageListExhaustsAttempts pins the other end: discovery that never
// completes fails, and fails with the evidence rather than a bare status.
func TestPackageListExhaustsAttempts(t *testing.T) {
	t.Parallel()
	fixture := newPackageListFixture(t, false)
	out, status := fixture.run(t, 2, "STUB_STALL_ATTEMPTS=9")
	if status == 0 {
		t.Fatalf("a package list that never completes must fail the gate\noutput:\n%s", out)
	}
	requireContains(t, out, "go list ./... timed out after 2s on each of 2 attempts.",
		"the diagnostic states the budget and how many attempts were actually made")
	requireContains(t, out, "retained package-list log:",
		"the retained stderr is the operator's evidence")
	requireContains(t, out, "retained partial package lists:",
		"so is the partial list each stopped attempt wrote")
	requireContains(t, out, "effective GOCACHE:",
		"the configured caches are named, because a stalled volume is one of the two causes")
}

// TestPackageListStopsAChildThatIgnoresSIGTERM pins the guarantee the retry
// rests on. A retry is only safe if the attempt it replaces is really gone,
// including a child that refuses SIGTERM and would otherwise still be holding
// Go's cache locks when the next attempt starts.
func TestPackageListStopsAChildThatIgnoresSIGTERM(t *testing.T) {
	t.Parallel()
	fixture := newPackageListFixture(t, false)
	out, status := fixture.run(t, 3, "STUB_STALL_ATTEMPTS=1", "STUB_STALL_IGNORES_TERM=1")
	if status != 0 {
		t.Errorf("the retry must still succeed once the stopped attempt is gone; exit %d\noutput:\n%s", status, out)
	}
	observed, err := os.ReadFile(filepath.Join(fixture.state, "observed"))
	if err != nil {
		t.Fatalf("the second attempt never ran, so nothing observed the stopped attempt's child: %v\noutput:\n%s", err, out)
	}
	if got := strings.TrimSpace(string(observed)); got != "child-gone" {
		t.Errorf("the stopped attempt's child was still alive when the next attempt began: observed %q\noutput:\n%s", got, out)
	}
}

// TestPackageListRefusesToRetryWhenLivenessIsUnknown pins the fail-closed rule.
// An empty answer from a process listing that could not run is not proof the
// attempt is gone, and treating it as proof is how the next attempt ends up
// racing the previous one.
func TestPackageListRefusesToRetryWhenLivenessIsUnknown(t *testing.T) {
	t.Parallel()
	fixture := newPackageListFixture(t, true)
	out, status := fixture.run(t, 3, "STUB_STALL_ATTEMPTS=9")
	if status == 0 {
		t.Fatalf("the gate must fail when it cannot show the stopped attempt is gone\noutput:\n%s", out)
	}
	requireContains(t, out, "cannot be shown to have stopped",
		"the refusal names what could not be determined, not a survivor it never saw")
	requireNotContains(t, out, "retrying.",
		"a retry that cannot see the previous attempt would race it, so there must be no retry")
}
