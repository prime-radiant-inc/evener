package serf_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
			fixture := newRuntimeBuildFixture(t)
			installFrontendToolchainStubs(t, fixture)
			copyRepositoryFile(t, fixture.repoRoot, fixture.root, "Makefile", 0o644)
			copyRepositoryFile(t, fixture.repoRoot, fixture.root, "scripts/build-runtime-pair.sh", 0o755)

			command := exec.Command("make", "LDFLAGS=make-test-flags", target)
			command.Dir = fixture.root
			command.Env = fixture.environment("")
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("make %s: %v\n%s", target, err, output)
			}

			assertTextFile(t, filepath.Join(fixture.root, "serf"), "./cmd/serf/\n")
			assertTextFile(t, filepath.Join(fixture.root, "serf-hub"), "./cmd/serf-hub/\n")

			if target == "build-hub" {
				assertNpmPrecedesHubGoBuild(t, fixture.logPath)
			}
		})
	}
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
// reach build-web (Makefile:37-43): it creates the frontend directory the
// recipe cd's into, and shadows npm and git on the fixture PATH so the REAL
// build-web recipe runs end to end without touching the network or the
// checkout's actual git state.
func installFrontendToolchainStubs(t *testing.T, fixture runtimeBuildFixture) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(fixture.root, "cmd", "serf-hub", "frontend"), 0o755); err != nil {
		t.Fatalf("mkdir frontend: %v", err)
	}
	writeTestFile(t, filepath.Join(fixture.fakeBin, "npm"), []byte(`#!/bin/sh
printf 'npm %s\n' "$*" >> "$SERF_TEST_GO_LOG"
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

// assertNpmPrecedesHubGoBuild pins the load-bearing prerequisite order at
// Makefile:31-35: build-web must run before build-runtime so the serf-hub
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
			t.Fatalf("npm call %q ran after the serf-hub go build; build-web must run before build-runtime (Makefile:31-35); log = %q", line, logData)
		}
	}
	if !sawNpm {
		t.Fatalf("fake go/npm log has no npm invocation; log = %q", logData)
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
