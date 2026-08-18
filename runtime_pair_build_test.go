package evener_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRuntimePairBuildPublishesBothWithSameLinkerFlags(t *testing.T) {
	fixture := newRuntimeBuildFixture(t)
	if output, err := runRuntimePairBuild(fixture, ""); err != nil {
		t.Fatalf("build runtime pair: %v\n%s", err, output)
	}

	assertTextFile(t, filepath.Join(fixture.root, "evener"), "./cmd/evener/\n")
	assertTextFile(t, filepath.Join(fixture.root, "evener-hub"), "./cmd/evener-hub/\n")
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
		"SERF_TEST_NPM_FAIL_COMMAND",
		"SERF_TEST_NPM_HOLD_COMMAND",
		"SERF_TEST_NPM_PID",
		"SERF_TEST_NPM_READY",
		"SERF_TEST_NPM_TRACK_COMMAND",
		"SERF_TEST_NPM_TRACK_PID",
		"SERF_TEST_SHELL_KILLED_REAPED",
		"SERF_TEST_SHELL_WAITED_REAPED",
		"SERF_TEST_NODE_HOLD_COMMAND",
		"SERF_TEST_NODE_FAIL_COMMAND",
		"SERF_TEST_NODE_PID",
		"SERF_TEST_NODE_READY",
		"SERF_TEST_NODE_TERM",
		"SERF_TEST_NODE_RELEASE",
		"SERF_TEST_NODE_READY_FD",
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
	writeTestFile(t, filepath.Join(fixture.root, "evener-hub"), []byte("old-evener-hub\n"), 0o755)

	if output, err := runRuntimePairBuild(fixture, "./cmd/evener-hub/"); err == nil {
		t.Fatalf("build runtime pair succeeded, want hub compiler failure; output = %q", output)
	}

	assertTextFile(t, filepath.Join(fixture.root, "evener"), "old-evener\n")
	assertTextFile(t, filepath.Join(fixture.root, "evener-hub"), "old-evener-hub\n")
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
			assertTextFile(t, filepath.Join(fixture.root, "evener-hub"), "./cmd/evener-hub/\n")

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

func TestMakeWebCommandsContainNodeProcessState(t *testing.T) {
	fixture := newBuildWebFixture(t)
	frontendDir := filepath.Join(fixture.root, "cmd", "evener-hub", "frontend")
	writeTestFile(t, filepath.Join(frontendDir, "package-lock.json"), []byte("{}\n"), 0o644)
	writeTestFile(t, filepath.Join(frontendDir, "package.json"), []byte("{}\n"), 0o644)

	for _, target := range []string{"build-web", "test-web", "test-web-browser"} {
		command := exec.Command("make", target)
		command.Dir = fixture.root
		command.Env = fixture.environment("")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("make %s: %v\n%s", target, err, output)
		}
	}

	logData, err := os.ReadFile(fixture.logPath)
	if err != nil {
		t.Fatalf("read fake frontend process log: %v", err)
	}
	wantNPMCommands := map[string]bool{
		"ci":            false,
		"run build":     false,
		"run typecheck": false,
		"run test":      false,
		"run lint":      false,
	}
	wantNodeCommands := map[string]bool{
		"scripts/layoutguard/run.mjs":   false,
		"scripts/overflowguard/run.mjs": false,
		"scripts/spawnguard/run.mjs":    false,
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
	for line := range strings.SplitSeq(strings.TrimSpace(string(logData)), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 8 {
			continue
		}
		command := fields[1]
		switch fields[0] {
		case "npm-env":
			if _, expected := wantNPMCommands[command]; expected {
				wantNPMCommands[command] = true
				assertProcessState("npm", command, fields, strings.HasPrefix(command, "run ") && command != "run build")
			}
		case "node-env":
			if _, expected := wantNodeCommands[command]; expected {
				wantNodeCommands[command] = true
				assertProcessState("node", command, fields, true)
			}
		}
	}
	for command, seen := range wantNPMCommands {
		if !seen {
			t.Errorf("npm %s was not observed; log = %q", command, logData)
		}
	}
	for command, seen := range wantNodeCommands {
		if !seen {
			t.Errorf("node %s was not observed; log = %q", command, logData)
		}
	}
}

func TestMakeTestWebBrowserInterruptWaitsForNodeCleanup(t *testing.T) {
	const hangWatchdog = 30 * time.Second

	fixture := newBuildWebFixture(t)
	frontendDir := filepath.Join(fixture.root, "cmd", "evener-hub", "frontend")
	writeTestFile(t, filepath.Join(frontendDir, "package-lock.json"), []byte("{}\n"), 0o644)
	writeTestFile(t, filepath.Join(frontendDir, "package.json"), []byte("{}\n"), 0o644)
	heldCommand := "scripts/layoutguard/run.mjs"
	readyPath := filepath.Join(fixture.root, "held-node.ready")
	pidPath := filepath.Join(fixture.root, "held-node.pid")
	termPath := filepath.Join(fixture.root, "held-node.term")
	releasePath := filepath.Join(fixture.root, "held-node.release")

	command := exec.Command("make", "test-web-browser")
	command.Dir = fixture.root
	command.Env = append(fixture.environment(""),
		"SERF_TEST_NODE_HOLD_COMMAND="+heldCommand,
		"SERF_TEST_NODE_READY="+readyPath,
		"SERF_TEST_NODE_PID="+pidPath,
		"SERF_TEST_NODE_TERM="+termPath,
		"SERF_TEST_NODE_RELEASE="+releasePath,
		"SERF_TEST_NODE_READY_FD=3",
	)
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create held Node readiness pipe: %v", err)
	}
	command.ExtraFiles = []*os.File{readyWriter}
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		_ = readyReader.Close()
		_ = readyWriter.Close()
		t.Fatalf("start make test-web-browser: %v", err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	readyDone, lifecycleDone := observeHeldBrowserFixtureLifecycle(readyReader)
	if err := readyWriter.Close(); err != nil {
		_, cleanupErr := cleanupHeldBrowserFixture(command, waitDone, false, lifecycleDone, releasePath, hangWatchdog)
		readerErr := readyReader.Close()
		if cleanupErr != nil {
			cleanupErr = errors.Join(cleanupErr, readerErr)
			t.Fatalf("close parent held Node readiness writer: %v; cleanup: %v", err, cleanupErr)
		}
		if readerErr != nil {
			t.Fatalf("close parent held Node readiness writer: %v; close readiness reader: %v", err, readerErr)
		}
		t.Fatalf("close parent held Node readiness writer: %v", err)
	}
	t.Cleanup(func() { _ = readyReader.Close() })
	makeWaited := false
	lifecycleObserved := false
	t.Cleanup(func() {
		if lifecycleObserved {
			return
		}
		observed, err := cleanupHeldBrowserFixture(command, waitDone, makeWaited, lifecycleDone, releasePath, hangWatchdog)
		lifecycleObserved = observed
		if err != nil {
			t.Errorf("clean held browser fixture: %v; output = %s", err, output.String())
		}
	})
	select {
	case err := <-readyDone:
		if err != nil {
			t.Fatalf("receive held browser Node readiness: %v; output = %s", err, output.String())
		}
	case waitErr := <-waitDone:
		makeWaited = true
		t.Fatalf("make test-web-browser returned before Node became ready: %v; output = %s", waitErr, output.String())
	case <-time.After(hangWatchdog):
		t.Fatalf("held browser Node readiness did not arrive within hang watchdog %s; output = %s", hangWatchdog, output.String())
	}

	browserRoot := browserEvidenceRoot(t, fixture, heldCommand)
	if _, err := os.Stat(browserRoot); err != nil {
		t.Fatalf("private browser root %q was absent while its Node owner was running: %v", browserRoot, err)
	}

	if err := exec.Command("kill", "-TERM", strconv.Itoa(command.Process.Pid)).Run(); err != nil {
		t.Fatalf("signal make test-web-browser: %v", err)
	}
	if !waitForPath(termPath, 5*time.Second) {
		t.Fatalf("Make did not deliver TERM to the browser Node owner; output = %s", output.String())
	}
	select {
	case waitErr := <-waitDone:
		makeWaited = true
		t.Fatalf("make test-web-browser returned before Node released cleanup: %v; output = %s", waitErr, output.String())
	default:
	}
	writeTestFile(t, releasePath, nil, 0o644)

	select {
	case waitErr := <-waitDone:
		makeWaited = true
		if waitErr == nil {
			t.Fatalf("interrupted make test-web-browser exited zero; output = %s", output.String())
		}
	case <-time.After(hangWatchdog):
		t.Fatalf("make test-web-browser did not return within hang watchdog %s after Node completed cleanup; output = %s", hangWatchdog, output.String())
	}
	observed, err := cleanupHeldBrowserFixture(command, waitDone, makeWaited, lifecycleDone, releasePath, hangWatchdog)
	lifecycleObserved = observed
	if err != nil {
		t.Fatalf("finish held browser fixture cleanup: %v; output = %s", err, output.String())
	}
	if retained := fullLogsPath(output.Bytes()); retained != browserRoot {
		t.Errorf("interrupted browser logs = %q, want retained process-owned root %q; output = %s", retained, browserRoot, output.String())
	}
	if _, err := os.Stat(browserRoot); err != nil {
		t.Errorf("interrupted browser root %q was not retained: %v", browserRoot, err)
	}
	pidData, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read held Node pid: %v", err)
	}
	nodePID := strings.TrimSpace(string(pidData))
	if exec.Command("kill", "-0", nodePID).Run() == nil {
		t.Errorf("interrupted browser Node pid %s is still alive", nodePID)
	}
}

func TestFrontendToolchainStubHeldNodeHonorsPreexistingRelease(t *testing.T) {
	fixture := newBuildWebFixture(t)
	heldCommand := "scripts/layoutguard/run.mjs"
	readyPath := filepath.Join(fixture.root, "held-node.ready")
	pidPath := filepath.Join(fixture.root, "held-node.pid")
	termPath := filepath.Join(fixture.root, "held-node.term")
	releasePath := filepath.Join(fixture.root, "held-node.release")
	writeTestFile(t, releasePath, nil, 0o644)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(fixture.fakeBin, "node"), heldCommand)
	command.Dir = fixture.root
	command.Env = append(fixture.environment(""),
		"SERF_TEST_NODE_HOLD_COMMAND="+heldCommand,
		"SERF_TEST_NODE_READY="+readyPath,
		"SERF_TEST_NODE_PID="+pidPath,
		"SERF_TEST_NODE_TERM="+termPath,
		"SERF_TEST_NODE_RELEASE="+releasePath,
	)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("held Node ignored a release that existed before startup; output = %s", output)
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 143 {
		t.Fatalf("held Node exit = %v, want status 143 after preexisting release; output = %s", err, output)
	}
	if _, err := os.Stat(readyPath); err != nil {
		t.Fatalf("held Node did not publish readiness before honoring release: %v", err)
	}
	if _, err := os.Stat(pidPath); err != nil {
		t.Fatalf("held Node did not publish its pid before honoring release: %v", err)
	}
	if _, err := os.Stat(termPath); !os.IsNotExist(err) {
		t.Fatalf("held Node recorded TERM without receiving it: stat err = %v", err)
	}
}

func TestHeldBrowserFixtureLifecycleWithoutNode(t *testing.T) {
	lifecycleReader, lifecycleWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create empty lifecycle pipe: %v", err)
	}
	t.Cleanup(func() { _ = lifecycleReader.Close() })
	readyDone, lifecycleDone := observeHeldBrowserFixtureLifecycle(lifecycleReader)
	if err := lifecycleWriter.Close(); err != nil {
		t.Fatalf("close empty lifecycle writer: %v", err)
	}
	if err := <-readyDone; !errors.Is(err, io.EOF) {
		t.Fatalf("readiness without Node = %v, want EOF", err)
	}
	if err := <-lifecycleDone; err != nil {
		t.Fatalf("lifecycle without Node = %v, want nil", err)
	}
}

func TestMakeTestWebBrowserSuccessIsConciseAndRemovesEvidence(t *testing.T) {
	fixture := newBuildWebFixture(t)
	frontendDir := filepath.Join(fixture.root, "cmd", "evener-hub", "frontend")
	writeTestFile(t, filepath.Join(frontendDir, "package-lock.json"), []byte("{}\n"), 0o644)
	writeTestFile(t, filepath.Join(frontendDir, "package.json"), []byte("{}\n"), 0o644)

	command := exec.Command("make", "test-web-browser")
	command.Dir = fixture.root
	command.Env = fixture.environment("")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("make test-web-browser: %v\n%s", err, output)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 3 {
		t.Fatalf("successful browser output has %d nonempty lines, want 3 verdicts; output = %q", len(lines), output)
	}
	for index, guard := range []string{"layoutguard", "overflowguard", "spawnguard"} {
		fields := strings.Fields(lines[index])
		if len(fields) != 2 || fields[0] != "PASS" || fields[1] != "web-"+guard {
			t.Errorf("browser verdict %d fields = %q, want PASS for %s", index, fields, guard)
		}
	}
	browserRoot := browserEvidenceRoot(t, fixture, "scripts/layoutguard/run.mjs")
	if _, err := os.Stat(browserRoot); !os.IsNotExist(err) {
		t.Fatalf("successful browser evidence root %q survived: stat err = %v", browserRoot, err)
	}
}

func TestMakeTestWebBrowserFailureReplaysLogAndRetainsEvidence(t *testing.T) {
	fixture := newBuildWebFixture(t)
	frontendDir := filepath.Join(fixture.root, "cmd", "evener-hub", "frontend")
	writeTestFile(t, filepath.Join(frontendDir, "package-lock.json"), []byte("{}\n"), 0o644)
	writeTestFile(t, filepath.Join(frontendDir, "package.json"), []byte("{}\n"), 0o644)
	failedCommand := "scripts/overflowguard/run.mjs"

	command := exec.Command("make", "test-web-browser")
	command.Dir = fixture.root
	command.Env = append(fixture.environment(""), "SERF_TEST_NODE_FAIL_COMMAND="+failedCommand)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("make test-web-browser succeeded despite injected guard failure; output = %s", output)
	}
	if !strings.Contains(string(output), "browser failure detail: "+failedCommand) {
		t.Fatalf("failed guard log was not replayed; output = %s", output)
	}
	if strings.Contains(string(output), "browser chatter: scripts/layoutguard/run.mjs") {
		t.Fatalf("successful guard chatter leaked into failure output; output = %s", output)
	}
	retained := fullLogsPath(output)
	if retained == "" {
		t.Fatalf("failed browser gate did not name retained evidence; output = %s", output)
	}
	if got := browserEvidenceRoot(t, fixture, failedCommand); got != retained {
		t.Fatalf("named browser evidence root = %q, process-owned root = %q", retained, got)
	}
	for _, relative := range []string{"layoutguard.log", "overflowguard.log", "spawnguard.log", filepath.Join("overflowguard", "tmp")} {
		if _, statErr := os.Stat(filepath.Join(retained, relative)); statErr != nil {
			t.Errorf("retained browser evidence %s: %v", relative, statErr)
		}
	}
}

func TestMakeTestWebRetainsFailedProcessStateWithinTMPDIR(t *testing.T) {
	fixture := newBuildWebFixture(t)
	frontendDir := filepath.Join(fixture.root, "cmd", "evener-hub", "frontend")
	writeTestFile(t, filepath.Join(frontendDir, "package-lock.json"), []byte("{}\n"), 0o644)
	writeTestFile(t, filepath.Join(frontendDir, "package.json"), []byte("{}\n"), 0o644)

	command := exec.Command("make", "test-web")
	command.Dir = fixture.root
	command.Env = append(fixture.environment(""), "SERF_TEST_NPM_FAIL_COMMAND=run test")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("make test-web succeeded despite injected npm failure; output = %s", output)
	}

	retained := fullLogsPath(output)
	if retained == "" {
		t.Fatalf("make test-web did not name retained evidence; output = %s", output)
	}
	if !strings.HasPrefix(retained, fixture.root+string(os.PathSeparator)) {
		t.Fatalf("retained web root = %q, want child of inherited TMPDIR %q", retained, fixture.root)
	}
	for _, relative := range []string{"test.log", filepath.Join("test", "home"), filepath.Join("test", "xdg-cache")} {
		if _, statErr := os.Stat(filepath.Join(retained, relative)); statErr != nil {
			t.Errorf("retained web evidence %s: %v", relative, statErr)
		}
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
		"SERF_TEST_NPM_HOLD_COMMAND=run test",
		"SERF_TEST_NPM_READY="+readyPath,
		"SERF_TEST_NPM_PID="+pidPath,
	)
	var output bytes.Buffer
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
	if err := run.wait(); err == nil {
		t.Fatalf("interrupted make test-web exited zero; output = %s", output.String())
	}

	retained := fullLogsPath(output.Bytes())
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

func TestMakeTestWebInterruptDoesNotSignalReapedCheck(t *testing.T) {
	fixture := newBuildWebFixture(t)
	frontendDir := filepath.Join(fixture.root, "cmd", "evener-hub", "frontend")
	writeTestFile(t, filepath.Join(frontendDir, "package-lock.json"), []byte("{}\n"), 0o644)
	writeTestFile(t, filepath.Join(frontendDir, "package.json"), []byte("{}\n"), 0o644)
	heldReady := filepath.Join(fixture.root, "held-npm.ready")
	heldPID := filepath.Join(fixture.root, "held-npm.pid")
	reapedPID := filepath.Join(fixture.root, "reaped-npm.pid")
	waitedReaped := filepath.Join(fixture.root, "waited-reaped.ready")
	killedReaped := filepath.Join(fixture.root, "killed-reaped")
	recordingShell := filepath.Join(filepath.Dir(fixture.root), "recording-shell")
	writeTestFile(t, recordingShell, []byte(`#!/bin/sh
if [ "${1:-}" = "-c" ]; then
  exec /bin/sh -c '
    wait() {
      command wait "$@"
      wait_status=$?
      tracked_pid=$(cat "$SERF_TEST_NPM_TRACK_PID")
      [ "${1:-}" != "$tracked_pid" ] || : > "$SERF_TEST_SHELL_WAITED_REAPED"
      return "$wait_status"
    }
    kill() {
      tracked_pid=$(cat "$SERF_TEST_NPM_TRACK_PID")
      for kill_arg in "$@"; do
        [ "$kill_arg" != "$tracked_pid" ] || : > "$SERF_TEST_SHELL_KILLED_REAPED"
      done
      command kill "$@"
    }
    eval "$1"
  ' evener-recording-shell "$2"
fi
exec /bin/sh "$@"
`), 0o755)

	command := exec.Command("make", "SHELL="+recordingShell, "test-web")
	command.Dir = fixture.root
	command.Env = append(fixture.environment(""),
		"SERF_TEST_NPM_HOLD_COMMAND=run test",
		"SERF_TEST_NPM_READY="+heldReady,
		"SERF_TEST_NPM_PID="+heldPID,
		"SERF_TEST_NPM_TRACK_COMMAND=run typecheck",
		"SERF_TEST_NPM_TRACK_PID="+reapedPID,
		"SERF_TEST_SHELL_WAITED_REAPED="+waitedReaped,
		"SERF_TEST_SHELL_KILLED_REAPED="+killedReaped,
	)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatalf("start make test-web: %v", err)
	}
	// One waiter, started with the child: both readiness waits race against it,
	// and the interrupt assertion below reads the same result.
	run := startChild(command)
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			<-run.done
		}
	})
	if err := waitForPathOrExit(heldReady, run, readinessTripwire); err != nil {
		t.Fatalf("held npm check did not become ready: %v; output = %s", err, output.String())
	}
	if err := waitForPathOrExit(waitedReaped, run, readinessTripwire); err != nil {
		t.Fatalf("Make did not reap the completed typecheck before waiting on the held check: %v; output = %s", err, output.String())
	}
	if err := exec.Command("kill", "-TERM", strconv.Itoa(command.Process.Pid)).Run(); err != nil {
		t.Fatalf("signal make test-web: %v", err)
	}
	if err := run.wait(); err == nil {
		t.Fatalf("interrupted make test-web exited zero; output = %s", output.String())
	}
	if _, err := os.Stat(killedReaped); !os.IsNotExist(err) {
		t.Fatalf("interrupt signaled a PID after its npm check was reaped: stat err = %v; output = %s", err, output.String())
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
	copyRepositoryFile(t, fixture.repoRoot, fixture.root, "Makefile", 0o644)
	copyRepositoryFile(t, fixture.repoRoot, fixture.root, "scripts/build-runtime-pair.sh", 0o755)
	copyRepositoryFile(t, fixture.repoRoot, fixture.root, "scripts/private-go-home.sh", 0o644)
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
	root := filepath.Join(t.TempDir(), "fixture with spaces")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir fixture root: %v", err)
	}
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
  "$package" "$ldflags" "$HOME" "${XDG_CONFIG_HOME:-}" "${XDG_CACHE_HOME:-}" "${XDG_STATE_HOME:-}" "${GOPATH:-}" "${GOCACHE:-}" >> "$SERF_TEST_GO_LOG"
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
// recipe cd's into, and shadows npm, Node, and git on the fixture PATH so the REAL
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
printf 'npm-env\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$*" "${NODE_DISABLE_COMPILE_CACHE:-}" "${HOME:-}" "${TMPDIR:-}" "${XDG_CONFIG_HOME:-}" "${XDG_CACHE_HOME:-}" "${XDG_STATE_HOME:-}" >> "$SERF_TEST_GO_LOG"
printf 'npm %s\n' "$*" >> "$SERF_TEST_GO_LOG"
if [ "$1" = "ci" ]; then
  mkdir -p node_modules/.bin
  printf '#!/bin/sh\necho "Version 6.0.3"\n' > node_modules/.bin/tsc
  chmod +x node_modules/.bin/tsc
fi
[ "${SERF_TEST_NPM_HOLD_COMMAND:-}" != "$*" ] || {
  printf '%s\n' "$$" > "$SERF_TEST_NPM_PID"
  : > "$SERF_TEST_NPM_READY"
  exec sleep 1000
}
[ "${SERF_TEST_NPM_TRACK_COMMAND:-}" != "$*" ] || printf '%s\n' "$$" > "$SERF_TEST_NPM_TRACK_PID"
[ "${SERF_TEST_NPM_FAIL_COMMAND:-}" = "$*" ] && exit 17
exit 0
`), 0o755)
	writeTestFile(t, filepath.Join(fixture.fakeBin, "node"), []byte(`#!/bin/sh
printf 'node-env\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$*" "${NODE_DISABLE_COMPILE_CACHE:-}" "${HOME:-}" "${TMPDIR:-}" "${XDG_CONFIG_HOME:-}" "${XDG_CACHE_HOME:-}" "${XDG_STATE_HOME:-}" >> "$SERF_TEST_GO_LOG"
printf 'browser chatter: %s\n' "$*"
[ "${SERF_TEST_NODE_HOLD_COMMAND:-}" != "$*" ] || {
  on_term() {
    : > "$SERF_TEST_NODE_TERM"
    while [ ! -f "$SERF_TEST_NODE_RELEASE" ]; do :; done
    exit 143
  }
  trap on_term TERM
  printf '%s\n' "$$" > "$SERF_TEST_NODE_PID"
  : > "$SERF_TEST_NODE_READY"
  [ "${SERF_TEST_NODE_READY_FD:-}" != 3 ] || printf . >&3
  while [ ! -f "$SERF_TEST_NODE_RELEASE" ]; do :; done
  exit 143
}
[ "${SERF_TEST_NODE_FAIL_COMMAND:-}" = "$*" ] && {
  printf 'browser failure detail: %s\n' "$*" >&2
  exit 23
}
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

func waitForPath(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
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

func observeHeldBrowserFixtureLifecycle(reader *os.File) (<-chan error, <-chan error) {
	readyDone := make(chan error, 1)
	lifecycleDone := make(chan error, 1)
	go func() {
		var signal [1]byte
		_, err := io.ReadFull(reader, signal[:])
		readyDone <- err
		if err != nil {
			lifecycleDone <- nil
			return
		}
		_, err = io.Copy(io.Discard, reader)
		lifecycleDone <- err
	}()
	return readyDone, lifecycleDone
}

func cleanupHeldBrowserFixture(command *exec.Cmd, waitDone <-chan error, makeWaited bool, lifecycleDone <-chan error, releasePath string, watchdog time.Duration) (bool, error) {
	var cleanupErrors []error
	lifecycleObserved := false
	if err := os.WriteFile(releasePath, nil, 0o644); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("create release: %w", err))
	}
	if !makeWaited {
		if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("kill Make: %w", err))
		}
		select {
		case <-waitDone:
		case <-time.After(watchdog):
			cleanupErrors = append(cleanupErrors, fmt.Errorf("Make did not exit within cleanup watchdog %s", watchdog))
		}
	}
	select {
	case err := <-lifecycleDone:
		lifecycleObserved = true
		if err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("observe held Node lifecycle: %w", err))
		}
	case <-time.After(watchdog):
		cleanupErrors = append(cleanupErrors, fmt.Errorf("held Node lifecycle did not finish within cleanup watchdog %s", watchdog))
	}
	return lifecycleObserved, errors.Join(cleanupErrors...)
}

func runRuntimePairBuild(fixture runtimeBuildFixture, failPackage string) ([]byte, error) {
	command := exec.Command("sh", filepath.Join(fixture.repoRoot, "scripts", "build-runtime-pair.sh"))
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
			"SERF_TEST_GO_LOG", "SERF_TEST_GO_FAIL_PACKAGE",
			"SERF_TEST_NPM_FAIL_COMMAND", "SERF_TEST_NPM_HOLD_COMMAND", "SERF_TEST_NPM_PID", "SERF_TEST_NPM_READY",
			"SERF_TEST_NPM_TRACK_COMMAND", "SERF_TEST_NPM_TRACK_PID", "SERF_TEST_SHELL_KILLED_REAPED", "SERF_TEST_SHELL_WAITED_REAPED",
			"SERF_TEST_NODE_HOLD_COMMAND", "SERF_TEST_NODE_FAIL_COMMAND", "SERF_TEST_NODE_PID", "SERF_TEST_NODE_READY", "SERF_TEST_NODE_TERM", "SERF_TEST_NODE_RELEASE", "SERF_TEST_NODE_READY_FD":
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
		"SERF_TEST_GO_LOG="+fixture.logPath,
		"SERF_TEST_GO_FAIL_PACKAGE="+failPackage,
	)
}

func browserEvidenceRoot(t *testing.T, fixture runtimeBuildFixture, command string) string {
	t.Helper()
	logData, err := os.ReadFile(fixture.logPath)
	if err != nil {
		t.Fatalf("read browser process log: %v", err)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(logData)), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) == 8 && fields[0] == "node-env" && fields[1] == command {
			return filepath.Dir(filepath.Dir(fields[4]))
		}
	}
	t.Fatalf("browser Node command %q was not recorded; log = %q", command, logData)
	return ""
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
// Makefile:23-29: build-web must run before build-runtime so the evener-hub
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
		if strings.HasPrefix(line, "go-env\t./cmd/evener-hub/\t") {
			hubBuildLine = i
			break
		}
	}
	if hubBuildLine == -1 {
		t.Fatalf("fake go/npm log has no evener-hub go build; log = %q", logData)
	}

	sawNpm := false
	for i, line := range lines {
		if !strings.HasPrefix(line, "npm ") {
			continue
		}
		sawNpm = true
		if i > hubBuildLine {
			t.Fatalf("npm call %q ran after the evener-hub go build; build-web must run before build-runtime (Makefile:23-29); log = %q", line, logData)
		}
	}
	if !sawNpm {
		t.Fatalf("fake go/npm log has no npm invocation; log = %q", logData)
	}
}

// assertNpmBuildPrecedesHubGoBuild pins the dist/install prerequisite graph:
// both now depend on build-web, so a `make -n` dry run must print the vite
// build before the evener-hub go build. make -n also forces the Makefile's
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
		if hubBuildLine == -1 && strings.Contains(line, "./cmd/evener-hub/") {
			hubBuildLine = i
		}
	}
	if npmBuildLine == -1 {
		t.Fatalf("make -n %s has no npm run build line; output = %s", target, output)
	}
	if hubBuildLine == -1 {
		t.Fatalf("make -n %s has no ./cmd/evener-hub/ go build line; output = %s", target, output)
	}
	if npmBuildLine > hubBuildLine {
		t.Fatalf("make -n %s: npm run build (line %d) printed after the evener-hub go build (line %d); %s must build the web first; output = %s", target, npmBuildLine, hubBuildLine, target, output)
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
