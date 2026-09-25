package evener_test

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
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
		"EVENER_TEST_NPM_FAIL_COMMAND",
		"EVENER_TEST_NPM_HOLD_COMMAND",
		"EVENER_TEST_NPM_HOLD_RELEASE",
		"EVENER_TEST_NPM_HOLD_TERM",
		"EVENER_TEST_NPM_PID",
		"EVENER_TEST_NPM_READY",
		"EVENER_TEST_NPM_TRACK_COMMAND",
		"EVENER_TEST_NPM_TRACK_PID",
		"EVENER_TEST_SHELL_KILLED_REAPED",
		"EVENER_TEST_SHELL_WAITED_REAPED",
		"EVENER_TEST_SHELL_WAIT_RELEASE",
		"EVENER_TEST_WEB_CLEANUP_READY",
		"EVENER_TEST_WEB_CLEANUP_RELEASE",
		"EVENER_TEST_WEB_CLEANUP_PID",
		"EVENER_TEST_WEB_WAIT_READY",
		"EVENER_TEST_WEB_WAIT_RELEASE",
		"EVENER_TEST_WEB_WAIT_REAPED",
		"EVENER_TEST_WEB_STALE_JOB",
		"EVENER_TEST_WEB_WAIT_USED",
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

func TestMakeWebCommandsContainNodeProcessState(t *testing.T) {
	fixture := newBuildWebFixture(t)
	frontendDir := filepath.Join(fixture.root, "cmd", "evener-hub", "frontend")
	processStateDir := filepath.Join(fixture.root, "process-state-records")
	if err := os.Mkdir(processStateDir, 0o755); err != nil {
		t.Fatalf("mkdir process-state records: %v", err)
	}
	writeTestFile(t, filepath.Join(frontendDir, "package-lock.json"), []byte("{}\n"), 0o644)
	writeTestFile(t, filepath.Join(frontendDir, "package.json"), []byte("{}\n"), 0o644)

	for _, target := range []string{"build-web", "test-web"} {
		command := exec.Command("make", target)
		command.Dir = fixture.root
		command.Env = append(fixture.environment(""), "EVENER_TEST_PROCESS_STATE_DIR="+processStateDir)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("make %s: %v\n%s", target, err, output)
		}
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
	wantNPMCommands := map[string]bool{
		"ci":            false,
		"run build":     false,
		"run typecheck": false,
		"run test":      false,
		"run lint":      false,
	}
	assertProcessState := func(tool, command string, fields []string, wantPrivateRoots bool) {
		t.Helper()
		if fields[2] != "1" {
			t.Errorf("%s %s NODE_DISABLE_COMPILE_CACHE = %q, want 1", tool, command, fields[2])
		}
		if !wantPrivateRoots {
			return
		}
		for i, name := range []string{"HOME", "TMPDIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
			path := fields[i+3]
			if path == "" {
				t.Errorf("%s %s %s is empty, want check-owned directory", tool, command, name)
				continue
			}
			if !strings.HasPrefix(path, fixture.root+string(os.PathSeparator)) {
				t.Errorf("%s %s %s = %q, want path beneath inherited TMPDIR %q", tool, command, name, path, fixture.root)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("%s %s left %s %q after success: stat err = %v", tool, command, name, path, err)
			}
		}
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(string(logData), "\n"), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 8 && len(fields) != 9 {
			continue
		}
		command := fields[1]
		if _, expected := wantNPMCommands[command]; fields[0] == "npm-env" && expected {
			wantNPMCommands[command] = true
			assertProcessState("npm", command, fields, strings.HasPrefix(command, "run ") && command != "run build")
		}
	}
	for command, seen := range wantNPMCommands {
		if !seen {
			t.Errorf("npm %s was not observed; log = %q", command, logData)
		}
	}
}

// TestWebGateScriptsHaveNoLoopJumps pins that the web gates, which poll their
// guards with TERM and INT traps armed, never use break or continue. Bash
// skips every command while a break or continue is pending, including a trap
// that fires in that moment, so an interrupt landing there is swallowed and
// the gate runs to completion. No test can aim a signal at that gap, so this
// holds the scripts to the form that has none.
func TestWebGateScriptsHaveNoLoopJumps(t *testing.T) {
	loopJump := regexp.MustCompile(`(^|[\s;&|({])(break|continue)($|[\s;&|)}])`)
	for _, path := range []string{"scripts/web/test-web.sh", "scripts/lib/owned-jobs.sh"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for number, line := range strings.Split(string(data), "\n") {
			code := strings.TrimSpace(line)
			if strings.HasPrefix(code, "#") {
				continue
			}
			if comment := strings.Index(code, " #"); comment >= 0 {
				code = code[:comment]
			}
			if loopJump.MatchString(code) {
				t.Errorf("%s:%d uses a loop jump a trap can be lost behind: %s", path, number+1, strings.TrimSpace(line))
			}
		}
	}
}

func TestMakeTestWebRetainsFailedProcessStateWithinTMPDIR(t *testing.T) {
	fixture := newBuildWebFixture(t)
	retained := runFailedTestWeb(t, fixture)

	if !strings.HasPrefix(retained, fixture.root+string(os.PathSeparator)) {
		t.Fatalf("retained web root = %q, want child of inherited TMPDIR %q", retained, fixture.root)
	}
	for _, relative := range []string{"test.log", filepath.Join("test", "home"), filepath.Join("test", "xdg-cache")} {
		if _, statErr := os.Stat(filepath.Join(retained, relative)); statErr != nil {
			t.Errorf("retained web evidence %s: %v", relative, statErr)
		}
	}
}

func TestMakeTestWebRetainsFailedProcessStateUnderSymlinkedTMPDIR(t *testing.T) {
	// t.TempDir caches its base directory on the first call for the
	// remainder of the test, so TMPDIR has to name the symlink before the
	// fixture makes its first t.TempDir call; t.TempDir itself can't be
	// used to build that symlink.
	tempRoot, err := os.MkdirTemp("", "evener-symlinked-tmpdir")
	if err != nil {
		t.Fatalf("make temp root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempRoot) })
	realTemp := filepath.Join(tempRoot, "real")
	if err := os.Mkdir(realTemp, 0o755); err != nil {
		t.Fatalf("mkdir real temp root: %v", err)
	}
	linked := filepath.Join(tempRoot, "link")
	if err := os.Symlink(realTemp, linked); err != nil {
		t.Fatalf("symlink temp root: %v", err)
	}
	t.Setenv("TMPDIR", linked)

	fixture := newBuildWebFixture(t)

	resolvedTemp, err := filepath.EvalSymlinks(realTemp)
	if err != nil {
		t.Fatalf("resolve temp root: %v", err)
	}
	if !strings.HasPrefix(fixture.root, resolvedTemp+string(os.PathSeparator)) {
		t.Fatalf("fixture root = %q, want a path beneath the resolved temp root %q", fixture.root, resolvedTemp)
	}

	retained := runFailedTestWeb(t, fixture)
	if !strings.HasPrefix(retained, fixture.root+string(os.PathSeparator)) {
		t.Fatalf("retained web root = %q, want child of inherited TMPDIR %q", retained, fixture.root)
	}
}

func TestMakeTestWebInterruptRetainsEvidenceAndReapsChecks(t *testing.T) {
	fixture := newBuildWebFixture(t)
	frontendDir := filepath.Join(fixture.root, "cmd", "evener-hub", "frontend")
	writeTestFile(t, filepath.Join(frontendDir, "package-lock.json"), []byte("{}\n"), 0o644)
	writeTestFile(t, filepath.Join(frontendDir, "package.json"), []byte("{}\n"), 0o644)
	readyPath := filepath.Join(fixture.root, "held-npm.ready")
	pidPath := filepath.Join(fixture.root, "held-npm.pid")

	command := exec.Command("make", "test-web")
	command.Dir = fixture.root
	command.Env = append(fixture.environment(""),
		"EVENER_TEST_NPM_HOLD_COMMAND=run test",
		"EVENER_TEST_NPM_READY="+readyPath,
		"EVENER_TEST_NPM_PID="+pidPath,
	)
	var output syncBuffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatalf("start make test-web: %v", err)
	}
	// One waiter, started with the child: waitForPathOrExit races readiness
	// against it, and the interrupt assertion below reads the same result.
	run := startChild(command)
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			<-run.done
		}
	})
	if err := waitForPathOrExit(readyPath, run, readinessTripwire); err != nil {
		t.Fatalf("held npm check did not become ready: %v; output = %s", err, output.String())
	}
	if err := exec.Command("kill", "-TERM", strconv.Itoa(command.Process.Pid)).Run(); err != nil {
		t.Fatalf("signal make test-web: %v", err)
	}
	if err := waitForChildExit(run, 5*time.Second); err == nil {
		t.Fatalf("interrupted make test-web exited zero; output = %s", output.String())
	} else if errors.Is(err, errChildExitTimeout) {
		t.Fatalf("interrupted make test-web did not reap checks: %v; output = %s", err, output.String())
	}

	retained := fullLogsPath([]byte(output.String()))
	if retained == "" || !strings.HasPrefix(retained, fixture.root+string(os.PathSeparator)) {
		t.Fatalf("interrupted web logs = %q, want retained path beneath %q; output = %s", retained, fixture.root, output.String())
	}
	pidData, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read held npm pid: %v", err)
	}
	heldPID := strings.TrimSpace(string(pidData))
	deadline := time.Now().Add(2 * time.Second)
	for exec.Command("kill", "-0", heldPID).Run() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if exec.Command("kill", "-0", heldPID).Run() == nil {
		t.Fatalf("interrupted web check pid %s is still alive", heldPID)
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
	copyRepositoryFile(t, fixture.repoRoot, fixture.root, "scripts/lib/scratch-lib.sh", 0o644)
	copyRepositoryFile(t, fixture.repoRoot, fixture.root, "scripts/lib/load-aware-workers.sh", 0o644)
	copyRepositoryFile(t, fixture.repoRoot, fixture.root, "scripts/lib/owned-jobs.sh", 0o644)
	copyRepositoryFile(t, fixture.repoRoot, fixture.root, "scripts/web/web-preflight.sh", 0o755)
	copyRepositoryFile(t, fixture.repoRoot, fixture.root, "scripts/web/test-web.sh", 0o755)
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
  printf 'npm-env\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$*" "${NODE_DISABLE_COMPILE_CACHE:-}" "${HOME:-}" "${TMPDIR:-}" "${XDG_CONFIG_HOME:-}" "${XDG_CACHE_HOME:-}" "${XDG_STATE_HOME:-}" "${BROWSER_GUARD_VITE_CACHE_DIR:-}" > "$record"
else
  printf 'npm-env\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$*" "${NODE_DISABLE_COMPILE_CACHE:-}" "${HOME:-}" "${TMPDIR:-}" "${XDG_CONFIG_HOME:-}" "${XDG_CACHE_HOME:-}" "${XDG_STATE_HOME:-}" >> "$EVENER_TEST_GO_LOG"
  printf 'npm %s\n' "$*" >> "$EVENER_TEST_GO_LOG"
fi
if [ "$1" = "ci" ]; then
  mkdir -p node_modules/.bin
  printf '#!/bin/sh\necho "Version 6.0.3"\n' > node_modules/.bin/tsc
  chmod +x node_modules/.bin/tsc
fi
[ "${EVENER_TEST_NPM_HOLD_COMMAND:-}" != "$*" ] || {
  on_term() {
    : > "$EVENER_TEST_NPM_HOLD_TERM"
    exit 143
  }
  trap on_term TERM INT
  printf '%s\n' "$$" > "$EVENER_TEST_NPM_PID"
  [ -z "${EVENER_TEST_NPM_READY:-}" ] || printf 'ready\n' > "$EVENER_TEST_NPM_READY"
  [ -n "${EVENER_TEST_NPM_HOLD_RELEASE:-}" ] || exec sleep 1000
  exec 9<> "$EVENER_TEST_NPM_HOLD_RELEASE"
  while :; do read -r _ <&9 && break; done
}
[ "${EVENER_TEST_NPM_TRACK_COMMAND:-}" != "$*" ] || printf '%s\n' "$$" > "$EVENER_TEST_NPM_TRACK_PID"
[ "${EVENER_TEST_NPM_FAIL_COMMAND:-}" = "$*" ] && exit 17
exit 0
`), 0o755)
	writeTestFile(t, filepath.Join(fixture.fakeBin, "git"), []byte(`#!/bin/sh
exit 0
`), 0o755)
}

func fullLogsPath(output []byte) string {
	const prefix = "full logs: "
	for line := range strings.SplitSeq(string(output), "\n") {
		if after, ok := strings.CutPrefix(line, prefix); ok {
			return after
		}
	}
	return ""
}

// readinessTripwire bounds a wait for a child's readiness file. It is a
// tripwire, never the synchronisation mechanism: the child's own exit is what
// says the file is never coming (see waitForPathOrExit). Generous on purpose so
// a loaded machine never trips it before the child gets going — a five-second
// ceiling doing the real work is what made these tests flake.
const readinessTripwire = 90 * time.Second

// exitGrace bounds how long a readiness file may still land after the child
// exits, for a grandchild that outlived it.
const exitGrace = 2 * time.Second

// waitForPathOrExit polls for path until it appears, the child exits, or the
// tripwire fires. It returns nil once the path exists.
//
// The child's exit is the awaitable completion a bare waitForPath ignores: a
// `make` that dies on startup will never create the file, and waiting out a
// fixed deadline turns that into "did not become ready; output = " with an
// empty output instead of naming the real failure. Exit demotes the poll to a
// short grace rather than ending it, so a file written by a descendant that
// outlived the child is still seen.
// childRun is a started child process whose exit any number of waiters can
// observe. A bare exit channel can be received only once, so the first readiness
// wait that consulted it would steal the result a later wait (or the interrupt
// assertion) still needs.
type childRun struct {
	done chan struct{}
	err  error
}

// startChild begins reaping command in the background.
func startChild(command *exec.Cmd) *childRun {
	run := &childRun{done: make(chan struct{})}
	go func() {
		run.err = command.Wait()
		close(run.done)
	}()
	return run
}

// wait blocks until the child exits and returns its exit error.
func (c *childRun) wait() error {
	<-c.done
	return c.err
}

// syncBuffer guards a live child's captured stdout/stderr with a mutex. Every
// helper below wires one buffer as both Stdout and Stderr of a still-running
// command and then reads it from a failure path — a readiness tripwire, a
// lost-signal assertion — that can fire before the command's own Wait (via
// childRun's run.done, or an inline waitDone channel) confirms the process
// has exited. Wait does not return until the goroutine exec.Cmd runs to copy
// the pipe into the buffer has itself finished, so a read that races ahead
// of that signal races the copy under -race. A bare bytes.Buffer here is
// what PR #766's race-root job caught; this type keeps every read and write
// serialized regardless of which side gets there first.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitForPathOrExit(path string, run *childRun, tripwire time.Duration) error {
	found := make(chan struct{})
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			if _, err := os.Stat(path); err == nil {
				close(found)
				return
			}
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()

	select {
	case <-found:
		return nil
	case <-run.done:
		select {
		case <-found:
			return nil
		case <-time.After(exitGrace):
			return fmt.Errorf("child exited (%w) and %s never appeared within %s of its exit",
				run.err, filepath.Base(path), exitGrace)
		}
	case <-time.After(tripwire):
		return fmt.Errorf("%s did not appear within %s and the child is still running",
			filepath.Base(path), tripwire)
	}
}

// readFIFORecord waits for one line from a shell rendezvous. The deadline is
// only a hang tripwire; readiness is the FIFO write itself, not a polling loop.
func readFIFORecord(reader *os.File, run *childRun, tripwire time.Duration) (string, error) {
	record := make(chan string, 1)
	readErr := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(reader).ReadString('\n')
		if err != nil {
			readErr <- err
			return
		}
		record <- strings.TrimSpace(line)
	}()
	timer := time.NewTimer(tripwire)
	defer timer.Stop()
	select {
	case line := <-record:
		return line, nil
	case err := <-readErr:
		return "", err
	case <-run.done:
		return "", fmt.Errorf("child exited (%w) before FIFO readiness", run.err)
	case <-timer.C:
		return "", fmt.Errorf("FIFO readiness did not arrive within %s", tripwire)
	}
}

var errChildExitTimeout = errors.New("child exit tripwire")

func waitForChildExit(run *childRun, tripwire time.Duration) error {
	timer := time.NewTimer(tripwire)
	defer timer.Stop()
	select {
	case <-run.done:
		return run.err
	case <-timer.C:
		return fmt.Errorf("%w: child did not exit within %s", errChildExitTimeout, tripwire)
	}
}

// TestWaitForPathOrExitReportsChildExit pins the mechanism behind two flakes in
// this file. A fixed five-second waitForPath ignored the child entirely, so a
// `make` that never got going produced "held npm check did not become ready;
// output = " with an empty output — a timeout mystery rather than a diagnosis.
//
// Dropping the child-exit arm, or restoring a bare deadline poll, fails this.
func TestWaitForPathOrExitReportsChildExit(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "never-created")
	dead := &childRun{done: make(chan struct{}), err: errors.New("exit status 2")}
	close(dead.done)

	start := time.Now()
	err := waitForPathOrExit(missing, dead, time.Minute)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected an error when the child exited without creating the file")
	}
	if !strings.Contains(err.Error(), "exit status 2") {
		t.Errorf("error must carry the child's exit so the failure is diagnosable, got: %v", err)
	}
	if elapsed > 30*time.Second {
		t.Errorf("took %v: fell through to the tripwire instead of noticing the child had exited", elapsed)
	}
}

// TestWaitForPathOrExitAcceptsFileWrittenAfterExit guards the other half: a
// descendant that outlives the child may still create the file, so exit demotes
// the poll to a grace rather than ending it.
func TestWaitForPathOrExitAcceptsFileWrittenAfterExit(t *testing.T) {
	t.Parallel()
	late := filepath.Join(t.TempDir(), "late")
	dead := &childRun{done: make(chan struct{})}
	close(dead.done)

	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = os.WriteFile(late, []byte("ready"), 0o600)
	}()

	if err := waitForPathOrExit(late, dead, time.Minute); err != nil {
		t.Fatalf("file written after the child exited should still count: %v", err)
	}
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
			"EVENER_TEST_NPM_FAIL_COMMAND", "EVENER_TEST_NPM_HOLD_COMMAND", "EVENER_TEST_NPM_PID", "EVENER_TEST_NPM_READY",
			"EVENER_TEST_NPM_HOLD_RELEASE", "EVENER_TEST_NPM_HOLD_TERM",
			"EVENER_TEST_NPM_TRACK_COMMAND", "EVENER_TEST_NPM_TRACK_PID", "EVENER_TEST_SHELL_KILLED_REAPED", "EVENER_TEST_SHELL_WAITED_REAPED",
			"EVENER_TEST_SHELL_WAIT_RELEASE",
			"EVENER_TEST_WEB_CLEANUP_READY", "EVENER_TEST_WEB_CLEANUP_RELEASE", "EVENER_TEST_WEB_CLEANUP_PID",
			"EVENER_TEST_WEB_WAIT_READY", "EVENER_TEST_WEB_WAIT_RELEASE", "EVENER_TEST_WEB_WAIT_REAPED", "EVENER_TEST_WEB_STALE_JOB", "EVENER_TEST_WEB_WAIT_USED",
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

// runFailedTestWeb runs `make test-web` in fixture with the frontend test step
// failing, and returns the retained evidence root the run named.
func runFailedTestWeb(t *testing.T, fixture runtimeBuildFixture) string {
	t.Helper()
	frontendDir := filepath.Join(fixture.root, "cmd", "evener-hub", "frontend")
	writeTestFile(t, filepath.Join(frontendDir, "package-lock.json"), []byte("{}\n"), 0o644)
	writeTestFile(t, filepath.Join(frontendDir, "package.json"), []byte("{}\n"), 0o644)

	command := exec.Command("make", "test-web")
	command.Dir = fixture.root
	command.Env = append(fixture.environment(""), "EVENER_TEST_NPM_FAIL_COMMAND=run test")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("make test-web succeeded despite injected npm failure; output = %s", output)
	}

	retained := fullLogsPath(output)
	if retained == "" {
		t.Fatalf("make test-web did not name retained evidence; output = %s", output)
	}
	return retained
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
