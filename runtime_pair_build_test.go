package serf_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRuntimePairBuildPublishesBothWithSameLinkerFlags(t *testing.T) {
	fixture := newRuntimeBuildFixture(t)
	if output, err := runRuntimePairBuild(fixture, ""); err != nil {
		t.Fatalf("build runtime pair: %v\n%s", err, output)
	}

	assertTextFile(t, filepath.Join(fixture.root, "serf"), "./cmd/serf/\n")
	assertTextFile(t, filepath.Join(fixture.root, "serf-hub"), "./cmd/serf-hub/\n")
	logData, err := os.ReadFile(fixture.logPath)
	if err != nil {
		t.Fatalf("read fake go log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	if len(lines) != 2 {
		t.Fatalf("fake go calls = %d, want 2; log = %q", len(lines), logData)
	}
	for _, line := range lines {
		if !strings.Contains(line, "ldflags=same-checkout-flags") {
			t.Fatalf("fake go call = %q, want shared linker flags", line)
		}
	}
}

func TestRuntimePairBuildFailureLeavesExistingPairUntouched(t *testing.T) {
	fixture := newRuntimeBuildFixture(t)
	writeTestFile(t, filepath.Join(fixture.root, "serf"), []byte("old-serf\n"), 0o755)
	writeTestFile(t, filepath.Join(fixture.root, "serf-hub"), []byte("old-serf-hub\n"), 0o755)

	if output, err := runRuntimePairBuild(fixture, "./cmd/serf-hub/"); err == nil {
		t.Fatalf("build runtime pair succeeded, want hub compiler failure; output = %q", output)
	}

	assertTextFile(t, filepath.Join(fixture.root, "serf"), "old-serf\n")
	assertTextFile(t, filepath.Join(fixture.root, "serf-hub"), "old-serf-hub\n")
}

func TestMakeRuntimeAliasesBuildThePair(t *testing.T) {
	for _, target := range []string{"build", "build-hub"} {
		t.Run(target, func(t *testing.T) {
			fixture := newBuildWebFixture(t)

			command := exec.Command("make", "LDFLAGS=make-test-flags", target)
			command.Dir = fixture.root
			command.Env = fixture.environment("")
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("make %s: %v\n%s", target, err, output)
			}

			assertTextFile(t, filepath.Join(fixture.root, "serf"), "./cmd/serf/\n")
			assertTextFile(t, filepath.Join(fixture.root, "serf-hub"), "./cmd/serf-hub/\n")

			// build must build the web too: build-runtime depends on
			// build-web (Makefile), so both aliases order the same way.
			assertNpmPrecedesHubGoBuild(t, fixture.logPath)
		})
	}

	// The -nt freshness gate in build-web's recipe: npm ci only runs when
	// node_modules is missing or older than package-lock.json, but the vite
	// build stays unconditional every run.
	t.Run("build-hub/repeat-run-skips-cached-npm-ci", func(t *testing.T) {
		fixture := newBuildWebFixture(t)

		// package-lock.json must exist BEFORE the first make run, backdated
		// well clear of it: the fake npm's node_modules mkdir (triggered by
		// that run's npm ci) is only microseconds behind this write, and an
		// mtime comparison that close to the wall clock is a coin flip on
		// filesystems/shells with whole-second mtime granularity — a real
		// flake, not a fixture quirk (Jesse: root-cause flakes, never rely
		// on timing). Backdating removes the race: node_modules is
		// unambiguously newer the instant it's created.
		frontendDir := filepath.Join(fixture.root, "cmd", "serf-hub", "frontend")
		writeTestFile(t, filepath.Join(frontendDir, "package-lock.json"), []byte("{}\n"), 0o644)
		writeTestFile(t, filepath.Join(frontendDir, "package.json"), []byte("{}\n"), 0o644)
		backdated := time.Now().Add(-1 * time.Hour)
		if err := os.Chtimes(filepath.Join(frontendDir, "package-lock.json"), backdated, backdated); err != nil {
			t.Fatalf("backdate package-lock.json: %v", err)
		}

		for run := 1; run <= 2; run++ {
			command := exec.Command("make", "LDFLAGS=make-test-flags", "build-hub")
			command.Dir = fixture.root
			command.Env = fixture.environment("")
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("make build-hub (run %d): %v\n%s", run, err, output)
			}
		}

		npmCiCount, npmBuildCount, logData := countNpmInvocations(t, fixture.logPath)
		if npmCiCount != 1 {
			t.Fatalf("npm ci ran %d times across two make build-hub runs, want 1 (the -nt gate should skip the second run); log = %q", npmCiCount, logData)
		}
		if npmBuildCount != 2 {
			t.Fatalf("npm run build ran %d times across two make build-hub runs, want 2 (the vite build is unconditional); log = %q", npmBuildCount, logData)
		}

		// Re-CI transition: a lockfile change must re-trigger npm ci, the
		// deterministic mirror of the backdate above. Advance
		// package-lock.json strictly past node_modules's mtime (rather than
		// relying on the wall clock advancing between here and the third
		// run) so the -nt check flips back to "run npm ci" without a race.
		nodeModulesInfo, err := os.Stat(filepath.Join(frontendDir, "node_modules"))
		if err != nil {
			t.Fatalf("stat node_modules: %v", err)
		}
		renewed := nodeModulesInfo.ModTime().Add(1 * time.Hour)
		if err := os.Chtimes(filepath.Join(frontendDir, "package-lock.json"), renewed, renewed); err != nil {
			t.Fatalf("renew package-lock.json: %v", err)
		}

		command := exec.Command("make", "LDFLAGS=make-test-flags", "build-hub")
		command.Dir = fixture.root
		command.Env = fixture.environment("")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("make build-hub (run 3): %v\n%s", err, output)
		}

		npmCiCount, npmBuildCount, logData = countNpmInvocations(t, fixture.logPath)
		if npmCiCount != 2 {
			t.Fatalf("npm ci ran %d times across three make build-hub runs, want 2 (the lockfile change before run 3 should re-trigger it); log = %q", npmCiCount, logData)
		}
		if npmBuildCount != 3 {
			t.Fatalf("npm run build ran %d times across three make build-hub runs, want 3 (the vite build is unconditional); log = %q", npmBuildCount, logData)
		}
	})

	// web-preflight's whole value is the builds it REFUSES: many agent
	// worktrees symlink node_modules to one shared install, and npm ci deletes
	// an existing node_modules before installing, so through a symlink it
	// empties that install for every worktree at once. Both cases below are
	// green under the old unguarded recipe.
	t.Run("build-web/refuses-npm-ci-through-a-symlink", func(t *testing.T) {
		fixture := newBuildWebFixture(t)
		frontendDir := filepath.Join(fixture.root, "cmd", "serf-hub", "frontend")
		writeTestFile(t, filepath.Join(frontendDir, "package-lock.json"), []byte("{}\n"), 0o644)

		// A shared install standing in for another worktree's node_modules,
		// backdated so the -nt gate would fire npm ci and destroy it.
		shared := filepath.Join(fixture.root, "shared-node-modules")
		writeTestFile(t, filepath.Join(shared, "left-behind.txt"), []byte("shared\n"), 0o644)
		if err := os.Symlink(shared, filepath.Join(frontendDir, "node_modules")); err != nil {
			t.Fatalf("symlink node_modules: %v", err)
		}
		backdated := time.Now().Add(-1 * time.Hour)
		if err := os.Chtimes(shared, backdated, backdated); err != nil {
			t.Fatalf("backdate shared install: %v", err)
		}

		command := exec.Command("make", "LDFLAGS=make-test-flags", "build-web")
		command.Dir = fixture.root
		command.Env = fixture.environment("")
		output, err := command.CombinedOutput()
		if err == nil {
			t.Fatalf("make build-web succeeded through a stale symlinked node_modules, want refusal; output = %s", output)
		}
		if !strings.Contains(string(output), "symlink") {
			t.Fatalf("refusal does not explain the symlink, so the reader cannot act on it; output = %s", output)
		}

		// The point of refusing: the other worktrees' install survives.
		if _, err := os.Stat(filepath.Join(shared, "left-behind.txt")); err != nil {
			t.Fatalf("shared install was destroyed despite the refusal: %v", err)
		}
		_, _, logData := countNpmInvocations(t, fixture.logPath)
		if strings.Contains(string(logData), "npm ci") {
			t.Fatalf("npm ci ran against a symlinked node_modules; log = %q", logData)
		}
	})

	// An empty node_modules that is merely NEWER than the lockfile skips npm ci
	// on the -nt gate, so without a health check the build proceeds against a
	// toolchain that isn't there.
	t.Run("build-web/refuses-an-install-with-no-real-tsc", func(t *testing.T) {
		fixture := newBuildWebFixture(t)
		frontendDir := filepath.Join(fixture.root, "cmd", "serf-hub", "frontend")
		writeTestFile(t, filepath.Join(frontendDir, "package-lock.json"), []byte("{}\n"), 0o644)
		if err := os.MkdirAll(filepath.Join(frontendDir, "node_modules"), 0o755); err != nil {
			t.Fatalf("mkdir empty node_modules: %v", err)
		}
		backdated := time.Now().Add(-1 * time.Hour)
		if err := os.Chtimes(filepath.Join(frontendDir, "package-lock.json"), backdated, backdated); err != nil {
			t.Fatalf("backdate package-lock.json: %v", err)
		}

		command := exec.Command("make", "LDFLAGS=make-test-flags", "build-web")
		command.Dir = fixture.root
		command.Env = fixture.environment("")
		output, err := command.CombinedOutput()
		if err == nil {
			t.Fatalf("make build-web succeeded with an empty node_modules, want refusal; output = %s", output)
		}
		if !strings.Contains(string(output), "tsc") {
			t.Fatalf("refusal does not name the toolchain check that failed; output = %s", output)
		}
	})

	// dist and install both gained the build-web prerequisite, so the
	// dependency graph itself (not just build/build-hub) must order the web
	// build before the hub go build. make -n prints recipes without running
	// them, so this is cheap and side-effect-free.
	for _, target := range []string{"dist", "install"} {
		t.Run(target+"/dry-run-orders-web-before-hub-build", func(t *testing.T) {
			fixture := newBuildWebFixture(t)

			command := exec.Command("make", "-n", target)
			command.Dir = fixture.root
			command.Env = fixture.environment("")
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("make -n %s: %v\n%s", target, err, output)
			}

			assertNpmBuildPrecedesHubGoBuild(t, target, output)
		})
	}
}

// newBuildWebFixture prepares a runtimeBuildFixture whose fixture root has
// the frontend toolchain stubbed and the Makefile plus the scripts its
// recipes invoke copied in, ready for any make target that reaches
// build-web.
func newBuildWebFixture(t *testing.T) runtimeBuildFixture {
	t.Helper()
	fixture := newRuntimeBuildFixture(t)
	installFrontendToolchainStubs(t, fixture)
	copyRepositoryFile(t, fixture.repoRoot, fixture.root, "Makefile", 0o644)
	copyRepositoryFile(t, fixture.repoRoot, fixture.root, "scripts/build-runtime-pair.sh", 0o755)
	copyRepositoryFile(t, fixture.repoRoot, fixture.root, "scripts/web-preflight.sh", 0o755)
	return fixture
}

type runtimeBuildFixture struct {
	repoRoot string
	root     string
	fakeBin  string
	logPath  string
}

func newRuntimeBuildFixture(t *testing.T) runtimeBuildFixture {
	t.Helper()
	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := t.TempDir()
	fakeBin := filepath.Join(root, "fake-bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	writeTestFile(t, filepath.Join(fakeBin, "go"), []byte(`#!/bin/sh
set -eu

output=
package=
ldflags=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) output=$2; shift 2 ;;
    -ldflags) ldflags=$2; shift 2 ;;
    *) package=$1; shift ;;
  esac
done
printf 'package=%s ldflags=%s\n' "$package" "$ldflags" >> "$SERF_TEST_GO_LOG"
if [ "${SERF_TEST_GO_FAIL_PACKAGE:-}" = "$package" ]; then
  exit 17
fi
printf '%s\n' "$package" > "$output"
`), 0o755)
	return runtimeBuildFixture{
		repoRoot: repoRoot,
		root:     root,
		fakeBin:  fakeBin,
		logPath:  filepath.Join(root, "fake-go.log"),
	}
}

// installFrontendToolchainStubs equips the fixture for make targets that
// reach build-web (Makefile:40-51): it creates the frontend directory the
// recipe cd's into, and shadows npm and git on the fixture PATH so the REAL
// build-web recipe runs end to end without touching the network or the
// checkout's actual git state.
func installFrontendToolchainStubs(t *testing.T, fixture runtimeBuildFixture) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(fixture.root, "cmd", "serf-hub", "frontend"), 0o755); err != nil {
		t.Fatalf("mkdir frontend: %v", err)
	}
	// The fake npm ci lays down the one thing web-preflight inspects to prove
	// the install is real: a node_modules/.bin/tsc that answers --version the
	// way the TypeScript compiler does. An empty node_modules is exactly the
	// broken state the preflight exists to catch, so a stub that only mkdir'd
	// the directory would (correctly) fail the build.
	writeTestFile(t, filepath.Join(fixture.fakeBin, "npm"), []byte(`#!/bin/sh
printf 'npm %s\n' "$*" >> "$SERF_TEST_GO_LOG"
if [ "$1" = "ci" ]; then
  mkdir -p node_modules/.bin
  printf '#!/bin/sh\necho "Version 6.0.3"\n' > node_modules/.bin/tsc
  chmod +x node_modules/.bin/tsc
fi
exit 0
`), 0o755)
	writeTestFile(t, filepath.Join(fixture.fakeBin, "git"), []byte(`#!/bin/sh
exit 0
`), 0o755)
}

func runRuntimePairBuild(fixture runtimeBuildFixture, failPackage string) ([]byte, error) {
	command := exec.Command("sh", filepath.Join(fixture.repoRoot, "scripts", "build-runtime-pair.sh"))
	command.Dir = fixture.root
	command.Env = fixture.environment(failPackage)
	return command.CombinedOutput()
}

func (fixture runtimeBuildFixture) environment(failPackage string) []string {
	environment := make([]string, 0, len(os.Environ())+4)
	for _, assignment := range os.Environ() {
		name := strings.SplitN(assignment, "=", 2)[0]
		if name == "PATH" || name == "LDFLAGS" || name == "SERF_TEST_GO_LOG" || name == "SERF_TEST_GO_FAIL_PACKAGE" {
			continue
		}
		environment = append(environment, assignment)
	}
	return append(environment,
		"PATH="+fixture.fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"LDFLAGS=same-checkout-flags",
		"SERF_TEST_GO_LOG="+fixture.logPath,
		"SERF_TEST_GO_FAIL_PACKAGE="+failPackage,
	)
}

func copyRepositoryFile(t *testing.T, repoRoot, fixtureRoot, relativePath string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, relativePath))
	if err != nil {
		t.Fatalf("read repository %s: %v", relativePath, err)
	}
	writeTestFile(t, filepath.Join(fixtureRoot, relativePath), data, mode)
}

func writeTestFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// countNpmInvocations tallies exact "npm ci" and "npm run build" log lines
// (see the fake npm stub in installFrontendToolchainStubs) and returns the
// raw log alongside the counts so callers can report it on failure.
func countNpmInvocations(t *testing.T, logPath string) (npmCiCount, npmBuildCount int, logData []byte) {
	t.Helper()
	logData, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		t.Fatalf("read fake go/npm log: %v", err)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(logData)), "\n") {
		switch line {
		case "npm ci":
			npmCiCount++
		case "npm run build":
			npmBuildCount++
		}
	}
	return npmCiCount, npmBuildCount, logData
}

// assertNpmPrecedesHubGoBuild pins the load-bearing prerequisite order at
// Makefile:23-29: build-web must run before build-runtime so the serf-hub
// go build embeds the dist build-web just produced. It tolerates the
// DIST_GOOS/DIST_GOARCH parse-time "go env" pollution lines that the
// Makefile's ?= assignments trigger against the fake go shim.
func assertNpmPrecedesHubGoBuild(t *testing.T, logPath string) {
	t.Helper()
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake go/npm log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")

	hubBuildLine := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "package=./cmd/serf-hub/ ") {
			hubBuildLine = i
			break
		}
	}
	if hubBuildLine == -1 {
		t.Fatalf("fake go/npm log has no serf-hub go build; log = %q", logData)
	}

	sawNpm := false
	for i, line := range lines {
		if !strings.HasPrefix(line, "npm ") {
			continue
		}
		sawNpm = true
		if i > hubBuildLine {
			t.Fatalf("npm call %q ran after the serf-hub go build; build-web must run before build-runtime (Makefile:23-29); log = %q", line, logData)
		}
	}
	if !sawNpm {
		t.Fatalf("fake go/npm log has no npm invocation; log = %q", logData)
	}
}

// assertNpmBuildPrecedesHubGoBuild pins the dist/install prerequisite graph:
// both now depend on build-web, so a `make -n` dry run must print the vite
// build before the serf-hub go build. make -n also forces the Makefile's
// parse-time `$(shell go env GOOS)`/GOARCH assignments (SERF_DIST_NAME's
// immediate `:=`) against the fake go shim, which doesn't understand the
// `env` subcommand; the resulting noise log lines and stderr complaints are
// expected and harmless here — this only checks relative order of the two
// substrings it cares about.
func assertNpmBuildPrecedesHubGoBuild(t *testing.T, target string, output []byte) {
	t.Helper()
	lines := strings.Split(string(output), "\n")

	npmBuildLine, hubBuildLine := -1, -1
	for i, line := range lines {
		if npmBuildLine == -1 && strings.Contains(line, "npm run build") {
			npmBuildLine = i
		}
		if hubBuildLine == -1 && strings.Contains(line, "./cmd/serf-hub/") {
			hubBuildLine = i
		}
	}
	if npmBuildLine == -1 {
		t.Fatalf("make -n %s has no npm run build line; output = %s", target, output)
	}
	if hubBuildLine == -1 {
		t.Fatalf("make -n %s has no ./cmd/serf-hub/ go build line; output = %s", target, output)
	}
	if npmBuildLine > hubBuildLine {
		t.Fatalf("make -n %s: npm run build (line %d) printed after the serf-hub go build (line %d); %s must build the web first; output = %s", target, npmBuildLine, hubBuildLine, target, output)
	}
}

func assertTextFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if got := string(data); got != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
