//go:build linux || darwin

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const pairRevision = "0123456789abcdef0123456789abcdef01234567"

// TestInstallRuntimePairRefusesUnsafePairs drives the code path
// run-ui-tests.sh uses when the workflow supplies a pair: a pair from another
// revision, a manifest that omits a binary, or bytes that changed after the
// manifest was written must be refused without copying either binary, and a
// matching pair must be installed into the run directory.
func TestInstallRuntimePairRefusesUnsafePairs(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"bash", "jq", "shasum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	script, err := filepath.Abs("install-runtime-pair.sh")
	if err != nil {
		t.Fatal(err)
	}

	writePair := func(t *testing.T, dir, revision string) {
		t.Helper()
		writePairFixture(t, dir, revision, "#!/bin/sh\nexit 0\n")
	}
	writeIdentity := func(t *testing.T, revision string) string {
		t.Helper()
		identity := filepath.Join(t.TempDir(), "identity.json")
		writeFile(t, identity, fmt.Sprintf("{\"sourceRevision\":%q}\n", revision))
		return identity
	}
	newFixture := func(t *testing.T) (pair, identity, dest string) {
		t.Helper()
		pair = t.TempDir()
		writePair(t, pair, pairRevision)
		return pair, writeIdentity(t, pairRevision), t.TempDir()
	}
	run := func(t *testing.T, dir string, env []string, args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command("bash", append([]string{script}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	notCopied := func(t *testing.T, dest string) {
		t.Helper()
		for _, name := range []string{"evener", "fixture-provider"} {
			if _, err := os.Stat(filepath.Join(dest, name)); err == nil {
				t.Errorf("a refused pair still copied %s into the run directory", name)
			}
		}
	}
	refused := func(t *testing.T, out string, err error, dest string) {
		t.Helper()
		exit, ok := errors.AsType[*exec.ExitError](err)
		if !ok || exit.ExitCode() != 1 {
			t.Fatalf("install-runtime-pair.sh exit = %v, want exit 1; output:\n%s", err, out)
		}
		notCopied(t, dest)
	}
	usageRefused := func(t *testing.T, out string, err error, dest string) {
		t.Helper()
		exit, ok := errors.AsType[*exec.ExitError](err)
		if !ok || exit.ExitCode() != 2 {
			t.Fatalf("install-runtime-pair.sh exit = %v, want exit 2 for a usage error; output:\n%s", err, out)
		}
		notCopied(t, dest)
	}

	t.Run("matching pair installed", func(t *testing.T) {
		t.Parallel()
		pair, identity, dest := newFixture(t)
		out, err := run(t, "", nil, pair, identity, dest)
		if err != nil {
			t.Fatalf("install-runtime-pair.sh refused a matching pair: %v\n%s", err, out)
		}
		for _, name := range []string{"evener", "fixture-provider"} {
			installed := filepath.Join(dest, name)
			info, err := os.Stat(installed)
			if err != nil {
				t.Fatalf("%s was not copied into the run directory: %v", name, err)
			}
			if info.Mode()&0o111 == 0 {
				t.Errorf("%s is not executable", installed)
			}
			want, err := os.ReadFile(filepath.Join(pair, name))
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(installed)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s in the run directory differs from the verified pair", name)
			}
		}
	})
	t.Run("revision mismatch refused", func(t *testing.T) {
		t.Parallel()
		pair, identity, dest := newFixture(t)
		writeFile(t, filepath.Join(pair, "revision"), "fedcba9876543210fedcba9876543210fedcba98\n")
		out, err := run(t, "", nil, pair, identity, dest)
		refused(t, out, err, dest)
	})
	t.Run("identity without a revision refused", func(t *testing.T) {
		t.Parallel()
		pair, _, dest := newFixture(t)
		out, err := run(t, "", nil, pair, writeIdentity(t, ""), dest)
		refused(t, out, err, dest)
	})
	t.Run("manifest entry missing refused", func(t *testing.T) {
		t.Parallel()
		pair, identity, dest := newFixture(t)
		manifest, err := os.ReadFile(filepath.Join(pair, "SHA256SUMS"))
		if err != nil {
			t.Fatal(err)
		}
		var kept []string
		for line := range strings.SplitSeq(string(manifest), "\n") {
			if strings.HasSuffix(line, "fixture-provider") {
				continue
			}
			kept = append(kept, line)
		}
		writeFile(t, filepath.Join(pair, "SHA256SUMS"), strings.Join(kept, "\n"))
		out, err := run(t, "", nil, pair, identity, dest)
		refused(t, out, err, dest)
	})
	t.Run("changed bytes refused", func(t *testing.T) {
		t.Parallel()
		pair, identity, dest := newFixture(t)
		writeFile(t, filepath.Join(pair, "fixture-provider"), "#!/bin/sh\nexit 1\n")
		out, err := run(t, "", nil, pair, identity, dest)
		refused(t, out, err, dest)
	})
	t.Run("missing manifest refused", func(t *testing.T) {
		t.Parallel()
		pair, identity, dest := newFixture(t)
		if err := os.Remove(filepath.Join(pair, "SHA256SUMS")); err != nil {
			t.Fatal(err)
		}
		out, err := run(t, "", nil, pair, identity, dest)
		refused(t, out, err, dest)
	})
	t.Run("relative pair directory refused", func(t *testing.T) {
		t.Parallel()
		cwd := t.TempDir()
		writePair(t, filepath.Join(cwd, "pair"), pairRevision)
		dest := t.TempDir()
		out, err := run(t, cwd, nil, "pair", writeIdentity(t, pairRevision), dest)
		usageRefused(t, out, err, dest)
	})
	t.Run("relative destination directory refused", func(t *testing.T) {
		t.Parallel()
		pair, identity, dest := newFixture(t)
		out, err := run(t, filepath.Dir(dest), nil, pair, identity, filepath.Base(dest))
		usageRefused(t, out, err, dest)
	})
}

// Marker names deliberately do not start with EVENER_: the runner clears its
// own list of EVENER_* overrides before it starts the pair, and a fixture's
// bookkeeping must survive that.
const (
	providerMarkerEnv = "QUALIFICATION_TEST_PROVIDER_MARKER"
	makeMarkerEnv     = "QUALIFICATION_TEST_MAKE_MARKER"
	goMarkerEnv       = "QUALIFICATION_TEST_GO_MARKER"
	runLogEnv         = "QUALIFICATION_TEST_RUN_LOG"

	// simStateEnv makes the xcrun stub describe a device that already exists,
	// so a run adopting a pre-booted device is told apart from a cold one.
	simStateEnv = "QUALIFICATION_TEST_SIM_STATE"
)

// simUDID is the device the fixture identity, the simulated listing and the
// assertions all name.
const simUDID = "QUALIFICATION-TEST-SIM"

// providerStub is the pair's fixture-provider: it records that the run started
// it and exits, which ends the runner at provider readiness in every case.
const providerStub = `#!/bin/sh
printf 'provider\n' >> "$QUALIFICATION_TEST_RUN_LOG"
printf 'started\n' >> "$QUALIFICATION_TEST_PROVIDER_MARKER"
exit 1
`

// makeStub stands in for the local build; reaching it means the runner chose
// to rebuild instead of using the supplied pair.
const makeStub = `#!/bin/sh
printf '%s\n' "$*" >> "$QUALIFICATION_TEST_MAKE_MARKER"
exit 0
`

// goStub stands in for the local Go builds and writes the provider the runner
// starts, so the no-pair case reaches the same readiness failure.
const goStub = `#!/bin/sh
out=""
while [ $# -gt 0 ]; do
	case "$1" in
	-o) out="$2"; shift 2 ;;
	*) shift ;;
	esac
done
printf '%s\n' "$*" >> "$QUALIFICATION_TEST_GO_MARKER"
[ -n "$out" ] || exit 1
cat >"$out" <<'STUB'
#!/bin/sh
printf 'started\n' >> "$QUALIFICATION_TEST_PROVIDER_MARKER"
exit 1
STUB
chmod +x "$out"
`

// xcrunStub answers the simulator work the runner does before it starts the
// journeys. The warmup's call order is held by
// TestRunnerWarmsTheSimulatorBeforeTheJourneys; here the stub only has to let
// the runner reach the pair decision, and it shares the run log with the
// provider stub so the fixture can prove the warmup ran before the provider.
// A device listing in QUALIFICATION_TEST_SIM_STATE's state stands in for the
// one the workflow's pre-boot step started.
const xcrunStub = `#!/bin/sh
printf 'xcrun %s\n' "$*" >> "$QUALIFICATION_TEST_RUN_LOG"
case "$*" in
"simctl list --json devices")
	if [ -n "${QUALIFICATION_TEST_SIM_STATE:-}" ]; then
		printf '{"devices":{"ios-26-6":[{"udid":"%s","state":"%s"}]}}\n' "QUALIFICATION-TEST-SIM" "$QUALIFICATION_TEST_SIM_STATE"
	fi
	;;
esac
exit 0
`

// plutilStub answers the warmup's bundle-identifier read; the fixture app is
// an empty directory and carries no Info.plist of its own.
const plutilStub = `#!/bin/sh
printf '%s\n' 'com.example.evener'
`

// sleepStub keeps the fixture from paying the warmup's real-time settle. The
// settle is measured in lab runs, not here, and the provider stub still ends
// the run at exit 1.
const sleepStub = `#!/bin/sh
exit 0
`

// TestRunUITestsStartsTheSuppliedPairInsteadOfBuilding drives run-ui-tests.sh
// itself with a stub toolchain. A verified pair supplied through
// EVENER_QUALIFICATION_RUNTIME_BINARIES must be what the run starts, with
// neither make nor go invoked. A pair from another revision must be refused by
// the installer before anything starts. With no pair the runner must still
// build locally.
func TestRunUITestsStartsTheSuppliedPairInsteadOfBuilding(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"bash", "jq", "shasum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	runner, err := filepath.Abs(filepath.Join("..", "run-ui-tests.sh"))
	if err != nil {
		t.Fatal(err)
	}
	stubs := t.TempDir()
	writeExecutable(t, filepath.Join(stubs, "make"), makeStub)
	writeExecutable(t, filepath.Join(stubs, "go"), goStub)
	writeExecutable(t, filepath.Join(stubs, "xcrun"), xcrunStub)
	writeExecutable(t, filepath.Join(stubs, "plutil"), plutilStub)
	writeExecutable(t, filepath.Join(stubs, "sleep"), sleepStub)

	type fixture struct {
		identity, simState                                    string
		output                                                string
		providerMarker, makeMarker, goMarker, runLog, appPath string
	}
	newFixture := func(t *testing.T, identityRevision string) *fixture {
		t.Helper()
		root := t.TempDir()
		products := filepath.Join(root, "DerivedData", "Build", "Products", "Release-iphonesimulator")
		app := filepath.Join(products, "Evener.app")
		if err := os.MkdirAll(app, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{"home", "tmp", "output"} {
			if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		f := &fixture{
			identity:       filepath.Join(root, "identity.json"),
			output:         filepath.Join(root, "output"),
			providerMarker: filepath.Join(root, "provider-ran"),
			makeMarker:     filepath.Join(root, "make-ran"),
			goMarker:       filepath.Join(root, "go-ran"),
			runLog:         filepath.Join(root, "run-log"),
			appPath:        filepath.Join(app, "Evener"),
		}
		writeFile(t, f.identity, fmt.Sprintf(
			"{\"sourceRevision\":%q,\"appPath\":%q,\"simulatorUDID\":%q}\n",
			identityRevision, filepath.Join(app, "Evener"), simUDID))
		return f
	}
	run := func(t *testing.T, f *fixture, pair string) (string, error) {
		t.Helper()
		env := []string{
			"HOME=" + filepath.Join(filepath.Dir(f.identity), "home"),
			"TMPDIR=" + filepath.Join(filepath.Dir(f.identity), "tmp"),
			"PATH=" + stubs + string(os.PathListSeparator) + os.Getenv("PATH"),
			providerMarkerEnv + "=" + f.providerMarker,
			makeMarkerEnv + "=" + f.makeMarker,
			goMarkerEnv + "=" + f.goMarker,
			runLogEnv + "=" + f.runLog,
			simStateEnv + "=" + f.simState,
		}
		if pair != "" {
			env = append(env, "EVENER_QUALIFICATION_RUNTIME_BINARIES="+pair)
		}
		for _, entry := range os.Environ() {
			name, _, _ := strings.Cut(entry, "=")
			switch {
			case name == "HOME", name == "TMPDIR", name == "PATH",
				name == "EVENER_QUALIFICATION_RUNTIME_BINARIES",
				strings.HasPrefix(name, "QUALIFICATION_TEST_"):
				continue
			}
			env = append(env, entry)
		}
		cmd := exec.Command("bash", runner, "--build-identity", f.identity, "--output", f.output)
		cmd.Dir = filepath.Dir(f.identity)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	startedPair := func(t *testing.T, f *fixture) bool {
		t.Helper()
		_, err := os.Stat(f.providerMarker)
		return err == nil
	}
	notRun := func(t *testing.T, path, what string) {
		t.Helper()
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s ran even though a verified pair was supplied", what)
		}
	}
	// The stub fixture-provider always exits 1, so a run that started it fails
	// with exit 1; usage errors exit 2, which is never the answer here.
	failedAtProvider := func(t *testing.T, out string, err error) {
		t.Helper()
		exit, ok := errors.AsType[*exec.ExitError](err)
		if !ok || exit.ExitCode() != 1 {
			t.Fatalf("run-ui-tests.sh exit = %v, want exit 1 from the fixture provider; output:\n%s", err, out)
		}
	}

	t.Run("a supplied pair is what the run starts", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, pairRevision)
		pair := t.TempDir()
		writePairFixture(t, pair, pairRevision, providerStub)
		out, err := run(t, f, pair)
		failedAtProvider(t, out, err)
		if !startedPair(t, f) {
			t.Errorf("the supplied pair's fixture-provider was not started; output:\n%s", out)
		}
		notRun(t, f.makeMarker, "make build-runtime")
		notRun(t, f.goMarker, "go build")
		// The runner must warm the device before the measured journeys, and
		// the warmup must precede the provider it shares the device with. The
		// source audit holds the order; this holds that the block runs.
		warmup := []string{
			"xcrun simctl boot QUALIFICATION-TEST-SIM",
			"xcrun simctl bootstatus QUALIFICATION-TEST-SIM -b",
			"xcrun simctl install QUALIFICATION-TEST-SIM " + f.appPath,
			"xcrun simctl launch QUALIFICATION-TEST-SIM com.example.evener",
			"xcrun simctl terminate QUALIFICATION-TEST-SIM com.example.evener",
			"provider",
		}
		calls := readCalls(t, f.runLog)
		next := 0
		for _, call := range calls {
			if next < len(warmup) && call == warmup[next] {
				next++
			}
		}
		if next != len(warmup) {
			t.Fatalf("the run did not warm the simulator in order before the provider started; calls = %v, want %v", calls, warmup)
		}
		// A cold device is booted exactly once; a second boot would fail on a
		// device the run started itself.
		if boots := countCalls(calls, "xcrun simctl boot "+simUDID); boots != 1 {
			t.Errorf("cold run booted the device %d times, want exactly one; calls = %v", boots, calls)
		}
	})
	t.Run("a device the pre-boot step started is not booted twice", func(t *testing.T) {
		t.Parallel()
		// Booting is the state that matters: the pre-boot step returns as soon
		// as simctl starts the device, so the journeys usually see it before
		// bootstatus finishes.
		for _, state := range []string{"Booted", "Booting"} {
			t.Run(state, func(t *testing.T) {
				t.Parallel()
				f := newFixture(t, pairRevision)
				f.simState = state
				pair := t.TempDir()
				writePairFixture(t, pair, pairRevision, providerStub)
				out, err := run(t, f, pair)
				failedAtProvider(t, out, err)
				if !startedPair(t, f) {
					t.Errorf("the supplied pair's fixture-provider was not started; output:\n%s", out)
				}
				calls := readCalls(t, f.runLog)
				if boots := countCalls(calls, "xcrun simctl boot "+simUDID); boots != 0 {
					t.Errorf("a device in state %s was booted again %d time(s); calls = %v", state, boots, calls)
				}
				if waited := countCalls(calls, "xcrun simctl bootstatus "+simUDID+" -b"); waited != 1 {
					t.Errorf("the run did not wait on bootstatus for the %s device; calls = %v", state, calls)
				}
			})
		}
	})
	t.Run("a pair from another revision is refused before anything starts", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, strings.Repeat("f", 40))
		pair := t.TempDir()
		writePairFixture(t, pair, pairRevision, providerStub)
		out, err := run(t, f, pair)
		failedAtProvider(t, out, err)
		if startedPair(t, f) {
			t.Error("the fixture-provider from a refused pair was started")
		}
		notRun(t, f.makeMarker, "make build-runtime")
		notRun(t, f.goMarker, "go build")
	})
	t.Run("no pair still builds locally", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, pairRevision)
		out, err := run(t, f, "")
		failedAtProvider(t, out, err)
		if !startedPair(t, f) {
			t.Errorf("the locally built fixture-provider was not started; output:\n%s", out)
		}
		for path, what := range map[string]string{f.makeMarker: "make build-runtime", f.goMarker: "go build"} {
			if _, err := os.Stat(path); err != nil {
				t.Errorf("%s did not run without a supplied pair; output:\n%s", what, out)
			}
		}
	})
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// countCalls returns how many recorded calls equal want exactly.
func countCalls(calls []string, want string) int {
	n := 0
	for _, call := range calls {
		if call == want {
			n++
		}
	}
	return n
}

// writePairFixture writes the pair the setup job uploads: the two binaries,
// the checksum manifest, and the revision file. providerBody is the
// fixture-provider's contents, so the runner test can install a stub it can
// observe.
func writePairFixture(t *testing.T, dir, revision, providerBody string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(dir, "evener"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(dir, "fixture-provider"), providerBody)
	writeManifest(t, dir)
	writeFile(t, filepath.Join(dir, "revision"), revision+"\n")
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

// writeManifest records the two binaries' checksums the way the workflow's
// packaging step does.
func writeManifest(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("shasum", "-a", "256", "evener", "fixture-provider")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("shasum: %v", err)
	}
	writeFile(t, filepath.Join(dir, "SHA256SUMS"), string(out))
}
