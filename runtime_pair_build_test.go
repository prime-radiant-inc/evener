package evener_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRuntimePairBuildPublishesBothWithSameLinkerFlags(t *testing.T) {
	fixture := newRuntimeBuildFixture(t)
	if output, err := runRuntimePairBuild(fixture, ""); err != nil {
		t.Fatalf("build runtime pair: %v\n%s", err, output)
	}

	assertTextFile(t, filepath.Join(fixture.root, "evener"), "./cmd/evener/\n")
	assertTextFile(t, filepath.Join(fixture.root, "evener-dev"), "./cmd/evener-dev/bin/\n")
	logData, err := os.ReadFile(fixture.logPath)
	if err != nil {
		t.Fatalf("read fake go log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	if len(lines) != 2 {
		t.Fatalf("fake go calls = %d, want 2; log = %q", len(lines), logData)
	}
	for _, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) != 9 || fields[0] != "go-env" || fields[2] != "same-checkout-flags" {
			t.Fatalf("fake go call = %q, want shared linker flags", line)
		}
	}
}

func TestRuntimePairBuildContainsProcessStateAndPreservesGoCaches(t *testing.T) {
	fixture := newRuntimeBuildFixture(t)
	if output, err := runRuntimePairBuild(fixture, ""); err != nil {
		t.Fatalf("build runtime pair: %v\n%s", err, output)
	}

	logData, err := os.ReadFile(fixture.logPath)
	if err != nil {
		t.Fatalf("read fake go log: %v", err)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(logData)), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 9 || fields[0] != "go-env" {
			t.Fatalf("fake go environment record = %q, want 9 tab-separated fields", line)
		}
		for i, name := range []string{"home", "xdg-config", "xdg-cache", "xdg-state"} {
			path := fields[i+3]
			if !strings.HasPrefix(path, fixture.root+string(os.PathSeparator)) {
				t.Fatalf("fake go %s = %q, want build-owned path beneath %q; log = %q", name, path, fixture.root, logData)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("build-owned %s survived successful build: stat err = %v; log = %q", name, err, logData)
			}
		}
		if got, want := fields[7], filepath.Join(fixture.root, "shared-gopath"); got != want {
			t.Fatalf("fake go GOPATH = %q, want reusable cache %q", got, want)
		}
		if got, want := fields[8], filepath.Join(fixture.root, "shared-gocache"); got != want {
			t.Fatalf("fake go GOCACHE = %q, want reusable cache %q", got, want)
		}
	}
}

func TestRuntimeBuildFixtureEnvironmentDropsAmbientHarnessControls(t *testing.T) {
	controlNames := []string{
		"GNUMAKEFLAGS",
		"MAKEFLAGS",
		"MAKELEVEL",
		"MFLAGS",
		"EVENER_TEST_PROCESS_STATE_DIR",
	}
	for _, name := range controlNames {
		t.Setenv(name, "ambient-value")
	}

	fixture := newRuntimeBuildFixture(t)
	for _, assignment := range fixture.environment("") {
		name, _, _ := strings.Cut(assignment, "=")
		if slices.Contains(controlNames, name) {
			t.Errorf("fixture environment inherited %s", name)
		}
	}
}

func TestRuntimePairBuildFailureLeavesExistingPairUntouched(t *testing.T) {
	fixture := newRuntimeBuildFixture(t)
	writeTestFile(t, filepath.Join(fixture.root, "evener"), []byte("old-evener\n"), 0o755)

	if output, err := runRuntimePairBuild(fixture, "./cmd/evener/"); err == nil {
		t.Fatalf("build runtime pair succeeded, want evener compiler failure; output = %q", output)
	}

	assertTextFile(t, filepath.Join(fixture.root, "evener"), "old-evener\n")
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

			assertTextFile(t, filepath.Join(fixture.root, "evener"), "./cmd/evener/\n")

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
		frontendDir := filepath.Join(fixture.root, "cmd", "evener-hub", "frontend")
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
		frontendDir := filepath.Join(fixture.root, "cmd", "evener-hub", "frontend")
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

	// The mtime shortcut follows symlinks, so a shared install NEWER than this
	// worktree's lockfile used to short-circuit the symlink branch entirely and
	// let a mismatched install through. The symlink check now runs first, so a
	// newer-but-mismatched shared install must still be refused without any npm
	// ci.
	t.Run("build-web/refuses-npm-ci-through-a-newer-symlink", func(t *testing.T) {
		fixture := newBuildWebFixture(t)
		frontendDir := filepath.Join(fixture.root, "cmd", "evener-hub", "frontend")
		writeTestFile(t, filepath.Join(frontendDir, "package-lock.json"), []byte("{}\n"), 0o644)

		// A shared install standing in for another worktree's node_modules,
		// carrying its OWN lockfile so the comparison is content-based rather
		// than a missing-file failure.
		shared := filepath.Join(fixture.root, "shared-node-modules")
		writeTestFile(t, filepath.Join(shared, "package-lock.json"), []byte("{\"different\":true}\n"), 0o644)
		if err := os.Symlink(shared, filepath.Join(frontendDir, "node_modules")); err != nil {
			t.Fatalf("symlink node_modules: %v", err)
		}
		// Newer than this worktree's lockfile: the -nt shortcut would fire if
		// the symlink check did not run first.
		future := time.Now().Add(1 * time.Hour)
		if err := os.Chtimes(shared, future, future); err != nil {
			t.Fatalf("make shared install newer: %v", err)
		}

		command := exec.Command("make", "LDFLAGS=make-test-flags", "build-web")
		command.Dir = fixture.root
		command.Env = fixture.environment("")
		output, err := command.CombinedOutput()
		if err == nil {
			t.Fatalf("make build-web succeeded through a newer, mismatched symlinked node_modules, want refusal; output = %s", output)
		}
		if !strings.Contains(string(output), "symlink") {
			t.Fatalf("refusal does not explain the symlink, so the reader cannot act on it; output = %s", output)
		}

		// The point of refusing: the other worktrees' install survives.
		if _, err := os.Stat(filepath.Join(shared, "package-lock.json")); err != nil {
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
		frontendDir := filepath.Join(fixture.root, "cmd", "evener-hub", "frontend")
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

	// install gained the build-web prerequisite, so the dependency graph
	// itself (not just build/build-hub) must order the web build before the
	// hub go build. make -n prints recipes without running them, so this is
	// cheap and side-effect-free. dist defers to goreleaser, whose before
	// hook owns the same ordering — asserted from the repo's .goreleaser.yml
	// below.
	for _, target := range []string{"install"} {
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

	t.Run("dist/goreleaser-builds-web-first", func(t *testing.T) {
		fixture := newBuildWebFixture(t)

		command := exec.Command("make", "-n", "dist")
		command.Dir = fixture.root
		command.Env = fixture.environment("")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("make -n dist: %v\n%s", err, output)
		}
		if !strings.Contains(string(output), "goreleaser release --snapshot --clean") {
			t.Fatalf("make -n dist does not defer to a goreleaser snapshot build; output = %s", output)
		}

		cfg, err := os.ReadFile(filepath.Join(fixture.repoRoot, ".goreleaser.yml"))
		if err != nil {
			t.Fatalf("read .goreleaser.yml: %v", err)
		}
		hook := bytes.Index(cfg, []byte("before:"))
		buildWeb := bytes.Index(cfg, []byte("make build-web"))
		if hook == -1 || buildWeb == -1 || buildWeb < hook {
			t.Fatalf(".goreleaser.yml does not run make build-web as a before hook; the hub binary would embed a stale SPA:\n%s", cfg)
		}
	})
}

// TestMakeBuildWebDisablesNodeCompileCache pins that make build-web runs its
// npm steps with Node's compile cache off, so no build writes one into the
// shared home.
func TestMakeBuildWebDisablesNodeCompileCache(t *testing.T) {
	fixture := newBuildWebFixture(t)
	frontendDir := filepath.Join(fixture.root, "cmd", "evener-hub", "frontend")
	processStateDir := filepath.Join(fixture.root, "process-state-records")
	if err := os.Mkdir(processStateDir, 0o755); err != nil {
		t.Fatalf("mkdir process-state records: %v", err)
	}
	writeTestFile(t, filepath.Join(frontendDir, "package-lock.json"), []byte("{}\n"), 0o644)
	writeTestFile(t, filepath.Join(frontendDir, "package.json"), []byte("{}\n"), 0o644)

	command := exec.Command("make", "build-web")
	command.Dir = fixture.root
	command.Env = append(fixture.environment(""), "EVENER_TEST_PROCESS_STATE_DIR="+processStateDir)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("make build-web: %v\n%s", err, output)
	}

	records, err := os.ReadDir(processStateDir)
	if err != nil {
		t.Fatalf("read fake frontend process-state records: %v", err)
	}
	var logData []byte
	for _, record := range records {
		data, err := os.ReadFile(filepath.Join(processStateDir, record.Name()))
		if err != nil {
			t.Fatalf("read fake frontend process-state record %q: %v", record.Name(), err)
		}
		logData = append(logData, data...)
	}
	wantNPMCommands := map[string]bool{"ci": false, "run build": false}
	for line := range strings.SplitSeq(strings.TrimSuffix(string(logData), "\n"), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 8 || fields[0] != "npm-env" {
			continue
		}
		command := fields[1]
		if _, expected := wantNPMCommands[command]; expected {
			wantNPMCommands[command] = true
			if fields[2] != "1" {
				t.Errorf("npm %s NODE_DISABLE_COMPILE_CACHE = %q, want 1", command, fields[2])
			}
		}
	}
	for command, seen := range wantNPMCommands {
		if !seen {
			t.Errorf("npm %s was not observed; log = %q", command, logData)
		}
	}
}

func TestMakeBuildMetadataPreservesIndexAndTracksDirtyWorktree(t *testing.T) {
	fixture := newBuildWebFixture(t)
	if err := os.Remove(filepath.Join(fixture.fakeBin, "git")); err != nil {
		t.Fatalf("remove fake git: %v", err)
	}
	frontendDir := filepath.Join(fixture.root, "cmd", "evener-hub", "frontend")
	writeTestFile(t, filepath.Join(frontendDir, "package-lock.json"), []byte("{}\n"), 0o644)
	writeTestFile(t, filepath.Join(frontendDir, "package.json"), []byte("{}\n"), 0o644)
	marker := filepath.Join(fixture.root, "tracked-marker.txt")
	writeTestFile(t, marker, []byte("clean\n"), 0o644)
	runGit(t, fixture.root, "init", "-q")
	runGit(t, fixture.root, "add", ".")
	runGit(t, fixture.root, "-c", "user.name=Evener Test", "-c", "user.email=evener-test@example.invalid", "commit", "-qm", "fixture")

	indexPath := filepath.Join(fixture.root, ".git", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read fixture index: %v", err)
	}
	runMakeBuild(t, fixture)
	indexAfterClean, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read fixture index after clean build: %v", err)
	}
	if !bytes.Equal(indexBefore, indexAfterClean) {
		t.Fatal("clean make build changed Git index bytes")
	}
	cleanLog, err := os.ReadFile(fixture.logPath)
	if err != nil {
		t.Fatalf("read clean build log: %v", err)
	}
	if strings.Contains(string(cleanLog), "GitDirty=true") {
		t.Fatalf("clean build marked checkout dirty; log = %q", cleanLog)
	}

	writeTestFile(t, marker, []byte("dirty\n"), 0o644)
	writeTestFile(t, fixture.logPath, nil, 0o644)
	runMakeBuild(t, fixture)
	indexAfterDirty, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read fixture index after dirty build: %v", err)
	}
	if !bytes.Equal(indexBefore, indexAfterDirty) {
		t.Fatal("dirty make build changed Git index bytes")
	}
	dirtyLog, err := os.ReadFile(fixture.logPath)
	if err != nil {
		t.Fatalf("read dirty build log: %v", err)
	}
	if !strings.Contains(string(dirtyLog), "GitDirty=true") {
		t.Fatalf("dirty build did not mark tracked worktree dirty; log = %q", dirtyLog)
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
	copyMakefileSources(t, fixture.repoRoot, fixture.root)
	copyRepositoryFile(t, fixture.repoRoot, fixture.root, "scripts/ops/build-runtime-pair.sh", 0o755)
	copyRepositoryFile(t, fixture.repoRoot, fixture.root, "scripts/lib/private-go-home.sh", 0o644)
	copyRepositoryFile(t, fixture.repoRoot, fixture.root, "scripts/web/web-preflight.sh", 0o755)
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
	root := filepath.Join(t.TempDir(), "fixture with spaces")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir fixture root: %v", err)
	}
	// The scripts under test resolve TMPDIR with `pwd -P`
	// (scripts/lib/scratch-lib.sh) and report resolved paths back, so the
	// fixture root has to be the resolved spelling for those paths to compare
	// as children of it.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve fixture root: %v", err)
	}
	root = resolved
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
printf 'go-env\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
  "$package" "$ldflags" "$HOME" "${XDG_CONFIG_HOME:-}" "${XDG_CACHE_HOME:-}" "${XDG_STATE_HOME:-}" "${GOPATH:-}" "${GOCACHE:-}" >> "$EVENER_TEST_GO_LOG"
if [ "${EVENER_TEST_GO_FAIL_PACKAGE:-}" = "$package" ]; then
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
// reach build-web (make/building.mk:78): it creates the frontend directory the
// recipe cd's into, and shadows npm and git on the fixture PATH so the REAL
// build-web recipe runs end to end without touching the network or the
// checkout's actual git state.
func installFrontendToolchainStubs(t *testing.T, fixture runtimeBuildFixture) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(fixture.root, "cmd", "evener-hub", "frontend"), 0o755); err != nil {
		t.Fatalf("mkdir frontend: %v", err)
	}
	// The fake npm ci lays down the one thing web-preflight inspects to prove
	// the install is real: a node_modules/.bin/tsc that answers --version the
	// way the TypeScript compiler does. An empty node_modules is exactly the
	// broken state the preflight exists to catch, so a stub that only mkdir'd
	// the directory would (correctly) fail the build.
	writeTestFile(t, filepath.Join(fixture.fakeBin, "npm"), []byte(`#!/bin/sh
if [ -n "${EVENER_TEST_PROCESS_STATE_DIR:-}" ]; then
  record=$(mktemp "$EVENER_TEST_PROCESS_STATE_DIR/npm.XXXXXX")
  printf 'npm-env\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$*" "${NODE_DISABLE_COMPILE_CACHE:-}" "${HOME:-}" "${TMPDIR:-}" "${XDG_CONFIG_HOME:-}" "${XDG_CACHE_HOME:-}" "${XDG_STATE_HOME:-}" > "$record"
else
  printf 'npm-env\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$*" "${NODE_DISABLE_COMPILE_CACHE:-}" "${HOME:-}" "${TMPDIR:-}" "${XDG_CONFIG_HOME:-}" "${XDG_CACHE_HOME:-}" "${XDG_STATE_HOME:-}" >> "$EVENER_TEST_GO_LOG"
  printf 'npm %s\n' "$*" >> "$EVENER_TEST_GO_LOG"
fi
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
	command := exec.Command("sh", filepath.Join(fixture.repoRoot, "scripts", "ops", "build-runtime-pair.sh"))
	command.Dir = fixture.root
	command.Env = fixture.environment(failPackage)
	return command.CombinedOutput()
}

func runMakeBuild(t *testing.T, fixture runtimeBuildFixture) {
	t.Helper()
	command := exec.Command("make", "build")
	command.Dir = fixture.root
	command.Env = fixture.environment("")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("make build: %v\n%s", err, output)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func (fixture runtimeBuildFixture) environment(failPackage string) []string {
	environment := make([]string, 0, len(os.Environ())+4)
	for _, assignment := range os.Environ() {
		name, _, _ := strings.Cut(assignment, "=")
		switch name {
		case "PATH", "TMPDIR", "LDFLAGS", "GOPATH", "GOCACHE", "NODE_DISABLE_COMPILE_CACHE",
			"GNUMAKEFLAGS", "MAKEFLAGS", "MAKELEVEL", "MFLAGS",
			"EVENER_TEST_GO_LOG", "EVENER_TEST_GO_FAIL_PACKAGE",
			"EVENER_TEST_PROCESS_STATE_DIR":
			continue
		}
		environment = append(environment, assignment)
	}
	return append(environment,
		"PATH="+fixture.fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TMPDIR="+fixture.root,
		"LDFLAGS=same-checkout-flags",
		"GOPATH="+filepath.Join(fixture.root, "shared-gopath"),
		"GOCACHE="+filepath.Join(fixture.root, "shared-gocache"),
		"EVENER_TEST_GO_LOG="+fixture.logPath,
		"EVENER_TEST_GO_FAIL_PACKAGE="+failPackage,
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
	// Every fixture write lands here, including the scripts/*.sh copies and
	// the go/npm/node/git toolchain shims that make test-web and make build
	// exec by relative path (issue #609). os.WriteFile leaves an open write
	// fd that a concurrent fork inherits until it execs, failing that exec
	// with ETXTBSY (golang/go#22315); holding ForkLock for reading across
	// the write excludes such a fork, as writeExecutable (install_test.go)
	// does for its own callers. Nothing reaching this helper runs parallel
	// today, so taking the lock on every write forecloses the hazard by
	// construction instead of leaning on test ordering.
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
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
// make/building.mk:34: build-web must run before build-runtime so the evener
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
		if strings.HasPrefix(line, "go-env\t./cmd/evener/\t") {
			hubBuildLine = i
			break
		}
	}
	if hubBuildLine == -1 {
		t.Fatalf("fake go/npm log has no evener go build; log = %q", logData)
	}

	sawNpm := false
	for i, line := range lines {
		if !strings.HasPrefix(line, "npm ") {
			continue
		}
		sawNpm = true
		if i > hubBuildLine {
			t.Fatalf("npm call %q ran after the evener go build; build-web must run before build-runtime (make/building.mk:34); log = %q", line, logData)
		}
	}
	if !sawNpm {
		t.Fatalf("fake go/npm log has no npm invocation; log = %q", logData)
	}
}

// assertNpmBuildPrecedesHubGoBuild pins the dist/install prerequisite graph:
// both now depend on build-web, so a `make -n` dry run must print the vite
// build before the evener go build. make -n also forces the Makefile's
// parse-time `$(shell go env GOOS)`/GOARCH assignments (EVENER_DIST_NAME's
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
		if hubBuildLine == -1 && strings.Contains(line, "./cmd/evener/") {
			hubBuildLine = i
		}
	}
	if npmBuildLine == -1 {
		t.Fatalf("make -n %s has no npm run build line; output = %s", target, output)
	}
	if hubBuildLine == -1 {
		t.Fatalf("make -n %s has no ./cmd/evener/ go build line; output = %s", target, output)
	}
	if npmBuildLine > hubBuildLine {
		t.Fatalf("make -n %s: npm run build (line %d) printed after the evener go build (line %d); %s must build the web first; output = %s", target, npmBuildLine, hubBuildLine, target, output)
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
