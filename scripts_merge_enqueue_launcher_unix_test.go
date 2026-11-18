//go:build unix

package evener_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/testwait"
)

// resolvePath returns path with every symlink and relative component resolved.
//
// The launcher's scratch library canonicalizes TMPDIR with `cd ... && pwd -P`
// (scripts/lib/scratch-lib.sh), because on macOS $TMPDIR lives behind the
// /var -> /private/var symlink. A case that matched processes by the raw TMPDIR
// would search for a path the launcher never uses: the build's argv carries the
// canonical one, so detection would never fire and cleanup would sweep the wrong
// string, leaving a FIFO-blocked build in its own process group. Both sides go
// through this function instead, so the comparison is path identity, not string
// identity.
func resolvePath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		t.Fatalf("absolutize %s: %v", resolved, err)
	}
	return absolute
}

// mergeEnqueueLauncher is the script under test. These cases drive it as an
// operator does -- a real process, a real signal at its PID or at its process
// group, its real exit status -- because the defects they pin are about process
// lifetime and cancellation, which no in-process seam can show.
//
// The helper is supplied through EVENER_DEV_BIN, the script's own documented
// seam for "run this binary instead of building one". Nothing is put on PATH:
// the fixture stands in for the helper binary the launcher would have built, and
// what these cases pin is the launcher's signal ordering, not the helper's
// decisions (internal/devtool/mergeq pins those, and cmd/evener-dev/mergeq.go
// pins that the helper's own NotifyContext turns a TERM into 130).
//
// Two rules hold in every case here, because a test for a leak must not leak:
//
//   - The launcher starts in its own process group, and t.Cleanup KILLs that
//     group plus every fixture pid the case learned. A failing assertion
//     therefore cannot leave a poller behind on the machine.
//   - Every wait for the launcher is bounded. An unbounded Wait on a
//     launcher that ignored the signal would stall the package until the global
//     timeout, which reads as an unrelated hang instead of the regression.
const mergeEnqueueLauncher = "scripts/ops/merge-enqueue.sh"

// TestMergeEnqueueLauncherLeavesNothingRunningAfterCancellation is the leak the
// review's own reproduction found: three real `evener-dev dev merge-enqueue
// --pr 6499 --wait 570` processes, reparented to PID 1 and still polling more
// than an hour after the tests that started them had finished. Each had a real
// `gh` child. PR 6499 happened to be merged, so the re-enqueue was a no-op; an
// unmerged PR would have been put in the merge queue by a test run.
//
// The property is stated as the operator sees it: after the case ends, no
// process from it survives. Two oracles, both structural rather than textual:
//
//   - the helper the launcher ran is the fixture, proven by the fixture's own
//     marker in its argv. A real helper names `merge-enqueue --repo
//     prime-radiant-inc/evener` and writes no such marker, so this could not
//     pass with the real one.
//   - cleanup leaves nothing behind, checked by scanning for the case's own
//     marker after the group kills rather than by observing that the direct
//     child exited. A kill of the direct child cannot reach a helper that leads
//     a group of its own, so a test tearing down that way leaks the tree. The
//     fixture's grandchild stands in for exactly that shape: it ignores TERM,
//     so only a group signal stops it.
func TestMergeEnqueueLauncherLeavesNothingRunningAfterCancellation(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
trap 'printf "helper-termed\n" >> "$EVENER_FIXTURE_LOG"; exit 130' TERM
"$EVENER_FIXTURE_GRANDCHILD" &
grandchild=$!
printf 'helper-pid=%s;pgid=%s\n' "$$" "$(ps -o pgid= -p "$$" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
printf 'grandchild-pid=%s;pgid=%s\n' "$grandchild" "$(ps -o pgid= -p "$grandchild" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
while :; do sleep 0.05; done
`)
	// A grandchild the fixture does not signal itself: it survives a TERM to
	// the fixture's PID alone, which is exactly the shape that outlived the
	// review's tests.
	grandchild := filepath.Join(f.dir, "grandchild.sh")
	writeTestFile(t, grandchild, []byte(`#!/usr/bin/env bash
trap '' TERM
while :; do sleep 0.05; done
`), 0o755)
	var helperPID int
	var grandchildPID int
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	cmd.Env = append(cmd.Env, "EVENER_FIXTURE_GRANDCHILD="+grandchild)
	runLauncher(t, cmd, f, &helperPID, &grandchildPID)
	helperPID, helperGroup := logPIDAndGroup(t, f, "helper-pid=")
	_, grandchildGroup := logPIDAndGroup(t, f, "grandchild-pid=")
	if grandchildGroup != helperGroup {
		t.Fatalf("the fixture grandchild is in group %d, want the helper's group %d: this case needs it reachable only by a group signal", grandchildGroup, helperGroup)
	}
	// The helper is the fixture, not the real one. EVENER_DEV_BIN is the only
	// way the launcher can have started this pid, and it is a path inside this
	// case's fixture directory.
	helperCmd := commandLineOf(t, helperPID)
	if !strings.Contains(helperCmd, f.path) {
		t.Fatalf("the launcher ran %q, want the fixture helper at %s: a case that starts the real helper can enqueue a real pull request", helperCmd, f.path)
	}
	signalLauncher(t, cmd, syscall.SIGTERM)
	mustExit(t, cmd, f, 130)
	// The helper stopped. That is what the launcher owes: it forwards the
	// cancellation and reaps the helper before reclaiming scratch.
	testwait.Until(t, fmt.Sprintf("helper pid %d to terminate", helperPID), func() bool {
		return pidGone(helperPID)
	})
	// The grandchild is deliberately not asserted here. The launcher does not
	// own a helper's descendants -- the real helper's cancelled context is what
	// stops its `gh` -- so requiring this one to die would be asserting the
	// fixture, not the launcher. What must hold is that nothing from the case
	// outlives the case, which is what cleanup's sweep proves: it runs after
	// the group kills and fails naming every survivor.
}

// commandLineOf returns the full argv of pid, or "" when it is gone.
func commandLineOf(t *testing.T, pid int) string {
	t.Helper()
	for _, proc := range scanProcs(t) {
		if proc.pid == pid {
			return proc.cmd
		}
	}
	return ""
}

// launcherExitDeadline bounds how long a case waits for the launcher to exit.
// It is generous against a loaded host and still far below the package's
// ceiling: the paths under test return in well under a second.
const launcherExitDeadline = 30 * time.Second

// fixtureCheckout is a stand-in checkout the launcher can be pointed at: a copy
// of the script and the libraries it sources, and a Go module whose only
// package under ./cmd/evener-dev/bin writes a sentinel and exits.
//
// The point is that the real helper cannot be built here, let alone started:
// this module has no `dev` subcommand, no merge-queue client and no code that
// could run `gh`. A launcher that reached the build path against *this*
// checkout and then ran the result would run the stub, which writes a file and
// exits.
type fixtureCheckout struct {
	root     string
	script   string
	tmp      string
	sentinel string
}

// newFixtureCheckout copies the launcher and its libraries into a fresh
// directory and writes the fixture module.
//
// tmp is the checkout's TMPDIR: the launcher mints its scratch under it, so the
// scratch path appears in the build's argv and the built helper runs from it.
//
// The build is held open by a FIFO source file rather than by a cold cache. A
// cold build is a race -- how long it takes depends on the host and on what the
// developer's cache already holds -- and the old case lost that race in the one
// direction that matters. A FIFO has no writer, so the go command blocks opening
// it and the build stays in flight until the case signals.
func newFixtureCheckout(t *testing.T, tmp string) fixtureCheckout {
	t.Helper()
	return newFixtureCheckoutNamed(t, tmp, "checkout")
}

// newFixtureCheckoutNamed is newFixtureCheckout with the copy's directory named
// by the caller, so a case can put the checkout on a path with a space in it.
func newFixtureCheckoutNamed(t *testing.T, tmp, name string) fixtureCheckout {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	c := fixtureCheckout{
		root:     root,
		script:   filepath.Join(root, "scripts", "ops", "merge-enqueue.sh"),
		tmp:      tmp,
		sentinel: filepath.Join(root, "helper-ran"),
	}
	for _, file := range []struct{ src, dst string }{
		{mergeEnqueueLauncher, filepath.Join("scripts", "ops", "merge-enqueue.sh")},
		{filepath.Join("scripts", "lib", "scratch-lib.sh"), filepath.Join("scripts", "lib", "scratch-lib.sh")},
		{filepath.Join("scripts", "lib", "gate-scratch-root.sh"), filepath.Join("scripts", "lib", "gate-scratch-root.sh")},
		{filepath.Join("scripts", "lib", "gate-bounded.sh"), filepath.Join("scripts", "lib", "gate-bounded.sh")},
	} {
		body, err := os.ReadFile(file.src)
		if err != nil {
			t.Fatalf("read %s: %v", file.src, err)
		}
		path := filepath.Join(root, file.dst)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", path, err)
		}
		writeTestFile(t, path, body, 0o755)
	}
	if err := os.MkdirAll(filepath.Join(root, "cmd", "evener-dev", "bin"), 0o755); err != nil {
		t.Fatalf("mkdir fixture package: %v", err)
	}
	// go.mod's directive stays below the toolchain's own version: a newer one
	// makes the go command try to download a toolchain, which would need the
	// network this suite must not touch.
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.invalid/fixture\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatalf("write fixture go.mod: %v", err)
	}
	main := "package main\n\nimport \"os\"\n\nfunc main() { _ = os.WriteFile(" + strconv.Quote(c.sentinel) + ", []byte(\"ran\"), 0o644) }\n"
	if err := os.WriteFile(filepath.Join(root, "cmd", "evener-dev", "bin", "main.go"), []byte(main), 0o644); err != nil {
		t.Fatalf("write fixture main: %v", err)
	}
	// A FIFO named *.go: the build has to read every source in the package, so
	// it blocks in open until a writer appears. Nothing ever opens it.
	if err := syscall.Mkfifo(filepath.Join(root, "cmd", "evener-dev", "bin", "hold.go"), 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatalf("mkdir fixture TMPDIR: %v", err)
	}
	// Both paths are resolved once, after they exist, and every later comparison
	// uses the resolved form. The launcher canonicalizes TMPDIR itself with
	// `pwd -P` (scripts/lib/scratch-lib.sh, written for macOS's
	// /var -> /private/var), so the build's argv carries the canonical path. A
	// case comparing the raw one would wait for a path the launcher never uses,
	// then sweep the same unmatched string in cleanup -- which is how a
	// FIFO-blocked build could survive in its own process group.
	c.root = resolvePath(t, root)
	c.script = filepath.Join(c.root, "scripts", "ops", "merge-enqueue.sh")
	c.sentinel = filepath.Join(c.root, "helper-ran")
	c.tmp = resolvePath(t, tmp)
	return c
}

// requireIsolated states this checkout's two isolation facts, so a case that
// drifts back to the repository's own checkout fails at once with the reason
// instead of timing out looking for a build.
//
//   - The script under test is the fixture copy. It is byte-identical to the
//     repository's script, so what runs is the real launcher; only the checkout
//     around it is a stand-in.
//   - The only package ./cmd/evener-dev/bin can find here is the fixture's
//     stub. The repository's helper lives in the repository's module, and this
//     module's go.mod is the only one on the path, so `go build
//     ./cmd/evener-dev/bin` from here cannot produce it.
func (c fixtureCheckout) requireIsolated(t *testing.T) {
	t.Helper()
	rootPrefix := c.root + string(filepath.Separator)
	if !strings.HasPrefix(c.script, rootPrefix) {
		t.Fatalf("the script under test is %s, want a copy under %s: a case that runs the repository's launcher builds the real helper, and the real helper can enqueue a real pull request", c.script, c.root)
	}
	repo, err := os.ReadFile(mergeEnqueueLauncher)
	if err != nil {
		t.Fatalf("read %s: %v", mergeEnqueueLauncher, err)
	}
	copied, err := os.ReadFile(c.script)
	if err != nil {
		t.Fatalf("read %s: %v", c.script, err)
	}
	if !bytes.Equal(repo, copied) {
		t.Fatalf("%s differs from %s: this case would be testing a script nobody ships", c.script, mergeEnqueueLauncher)
	}
	entries, err := os.ReadDir(filepath.Join(c.root, "cmd", "evener-dev", "bin"))
	if err != nil {
		t.Fatalf("read the fixture package: %v", err)
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "main.go", "hold.go":
		default:
			t.Fatalf("the fixture package holds %s: the fixture must contain only its stub, or the build could produce something other than it", entry.Name())
		}
	}
}

// requirePrivateChain creates dir and makes every directory from root down to it
// private, so the launcher's ancestor rule accepts it. Nothing above root is
// touched: root is the test's own temp directory.
//
// `t.TempDir()` inherits the runner's umask, and the umask here is 0002, so the
// test temp directory comes back 0775 and the ancestor rule rejects it --
// correctly, since a group member can replace an entry in it. A case whose
// premise is "a safe root exists" therefore has to manufacture one rather than
// assume the runner's umask.
func requirePrivateChain(t *testing.T, root, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	for probe := dir; ; probe = filepath.Dir(probe) {
		if err := os.Chmod(probe, 0o700); err != nil {
			t.Fatalf("make %s private: %v", probe, err)
		}
		if probe == root || filepath.Dir(probe) == probe {
			return
		}
	}
}

// launcherFixture is a stand-in helper: a script the launcher starts through
// EVENER_DEV_BIN and then signals.
type launcherFixture struct {
	dir    string
	path   string
	log    string
	stdout string
	stderr string
}

// newLauncherFixture writes script as the fake helper and returns its path, the
// file its handlers append to, and the files its output goes to.
func newLauncherFixture(t *testing.T, script string) launcherFixture {
	t.Helper()
	// Resolved, because the launcher canonicalizes the TMPDIR it is handed and
	// the sweep matches against the path the launcher actually used.
	dir := resolvePath(t, t.TempDir())
	f := launcherFixture{
		dir:    dir,
		path:   filepath.Join(dir, "evener-dev"),
		log:    filepath.Join(dir, "log"),
		stdout: filepath.Join(dir, "stdout"),
		stderr: filepath.Join(dir, "stderr"),
	}
	writeTestFile(t, f.path, []byte(script), 0o755)
	return f
}

// lines returns the fixture's handler log, one line per entry. The fixture
// writes values such as the directory it ran from, and a checkout path may hold
// a space: splitting on whitespace would truncate that value at the first one
// and fail a launcher that did the right thing.
func (f launcherFixture) lines(t *testing.T) []string {
	t.Helper()
	body, err := os.ReadFile(f.log)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatalf("read fixture log: %v", err)
	}
	text := strings.TrimSuffix(string(body), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// output returns what the launcher wrote, for a failure message.
func (f launcherFixture) output(t *testing.T) string {
	t.Helper()
	var parts []string
	for _, path := range []string{f.stdout, f.stderr} {
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
			parts = append(parts, filepath.Base(path)+": "+trimmed)
		}
	}
	return strings.Join(parts, " | ")
}

// launcherCmd builds the launcher command with the fixture as its helper.
//
// Its output goes to files, not pipes. A pipe is part of what the defect leaves
// open: an orphaned helper inherits the launcher's stdout, so waiting on a pipe
// blocks until the leaked helper exits and the case that should fail on exit 143
// hangs instead of reporting.
//
// The launcher leads its own process group so a case can deliver Ctrl-C's
// signal the way a tty does -- to the group -- without signalling the test
// binary itself, and so cleanup can KILL the group and everything left in it.
func (f launcherFixture) launcherCmd(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("bash", append([]string{mergeEnqueueLauncher}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = envOverride(os.Environ(),
		"EVENER_DEV_BIN="+f.path,
		"EVENER_FIXTURE_LOG="+f.log,
		"PATH="+os.Getenv("PATH"),
		// TMPDIR is the case's own directory, so a helper the launcher builds
		// and runs carries this case's marker in its argv. That is what lets
		// cleanup find a helper whose PID the case never learned -- including
		// the real one, were a case ever to reach it.
		"TMPDIR="+f.tmpDir(),
	)
	return cmd
}

// tmpDir is the TMPDIR this fixture's launcher runs under: inside the case's own
// directory, so the scratch path the launcher mints names the case.
func (f launcherFixture) tmpDir() string { return filepath.Join(f.dir, "tmp") }

// killGroups KILLs each pid's process group, ignoring ones already gone. It is
// the cleanup path: it must never fail the case it is cleaning up after.
func killGroups(pids ...int) {
	for _, pid := range pids {
		if pid <= 1 {
			continue
		}
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// runLauncher starts cmd with its output redirected, and registers cleanup that
// KILLs the launcher's process group, every fixture pid named so far, and every
// process still naming the fixture directory.
//
// The sweep covers what a pid list cannot: a pid list only holds the processes a
// case managed to learn before its assertion failed, so a helper that published
// nothing, or one the launcher started into a group of its own, is outside every
// list. A tree left that way outlives its test and runs unattended. The fixture directory is unique per case and
// appears in the argv of every process a case starts (the fixture helper by
// path, a build or a real helper by the scratch path under it), so it is the one
// handle that does not depend on the case getting that far.
func runLauncher(t *testing.T, cmd *exec.Cmd, f launcherFixture, fixturePIDs ...*int) {
	t.Helper()
	out, err := os.Create(f.stdout)
	if err != nil {
		t.Fatalf("create launcher stdout: %v", err)
	}
	t.Cleanup(func() { _ = out.Close() })
	errOut, err := os.Create(f.stderr)
	if err != nil {
		t.Fatalf("create launcher stderr: %v", err)
	}
	t.Cleanup(func() { _ = errOut.Close() })
	cmd.Stdout, cmd.Stderr = out, errOut
	// A case may have already put something at the TMPDIR path on purpose -- the
	// no-scratch case uses a regular file so directory creation fails for every
	// caller, root included. Creating the path is only for the cases that did
	// not, and an entry already there is left exactly as the case made it.
	if _, err := os.Lstat(f.tmpDir()); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(f.tmpDir(), 0o755); err != nil {
			t.Fatalf("mkdir fixture TMPDIR: %v", err)
		}
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", mergeEnqueueLauncher, err)
	}
	launcherPID := cmd.Process.Pid
	// Cleanups run last-registered-first, so the sweep is registered first and
	// runs after the kills: it reports what the kills missed.
	t.Cleanup(func() { assertNoSurvivors(t, f.dir) })
	t.Cleanup(func() {
		pids := []int{launcherPID}
		for _, p := range fixturePIDs {
			pids = append(pids, *p)
		}
		killGroups(pids...)
	})
}

// launcherCmdAt is launcherCmd for a copy of the script, which is how the
// build-path case runs the launcher without the real checkout's sources in
// reach.
func (f launcherFixture) launcherCmdAt(t *testing.T, script string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = envOverride(os.Environ(),
		"EVENER_DEV_BIN="+f.path,
		"EVENER_FIXTURE_LOG="+f.log,
		"PATH="+os.Getenv("PATH"),
		// TMPDIR is the case's own directory, so a helper the launcher builds
		// and runs carries this case's marker in its argv. That is what lets
		// cleanup find a helper whose PID the case never learned -- including
		// the real one, were a case ever to reach it.
		"TMPDIR="+f.tmpDir(),
	)
	return cmd
}

// scanProcs returns every process on the machine as pid, parent pid, process
// group and command line.
//
// It is `ps -axo`, the form scripts/lib/gate-bounded.sh already uses, because
// `ps --ppid` is a GNU extension that BSD `ps` -- which is what macOS ships, and
// this repo builds Darwin -- rejects with a usage error. The helper that used
// it turned that error into an empty answer, so on a macOS host the case waited
// for a build it could never see and timed out.
//
// Only the three numeric columns are split on whitespace; everything after them
// is the command line verbatim. Splitting the whole row and rejoining it with
// single spaces rewrote the command, so a path with two spaces in a row -- a
// TMPDIR a case deliberately chose -- became a string that could never appear in
// a process's argv. Readiness and cleanup then agreed with each other and with
// nothing on the machine: the case timed out and the process it was looking for
// survived it.
func scanProcs(t *testing.T) []procLine {
	t.Helper()
	out, err := exec.Command("ps", "-ww", "-axo", "pid=,ppid=,pgid=,command=").Output()
	if err != nil {
		t.Fatalf("ps -axo: %v", err)
	}
	var procs []procLine
	for line := range strings.SplitSeq(string(out), "\n") {
		pid, rest, ok := leadingField(line)
		if !ok {
			continue
		}
		ppid, rest, ok := leadingField(rest)
		if !ok {
			continue
		}
		pgid, cmd, ok := leadingField(rest)
		if !ok {
			continue
		}
		pidNum, pidErr := strconv.Atoi(pid)
		ppidNum, ppidErr := strconv.Atoi(ppid)
		pgidNum, pgidErr := strconv.Atoi(pgid)
		if pidErr != nil || ppidErr != nil || pgidErr != nil {
			continue
		}
		procs = append(procs, procLine{
			pid:  pidNum,
			ppid: ppidNum,
			pgid: pgidNum,
			cmd:  cmd,
		})
	}
	return procs
}

// leadingField splits the first whitespace-delimited field off line and returns
// it with the remainder of the line unchanged, so a later field keeps every byte
// it had. It reports false when the line is blank.
//
// strings.Fields plus a strings.Join would be shorter and would rewrite the
// remainder: two spaces in a row would come back as one, which is the defect
// scanProcs documents.
func leadingField(line string) (field, rest string, ok bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return "", "", false
	}
	end := strings.IndexAny(trimmed, " \t")
	if end < 0 {
		return trimmed, "", true
	}
	return trimmed[:end], strings.TrimLeft(trimmed[end:], " \t"), true
}

// procLine is one row of scanProcs.
type procLine struct {
	pid, ppid, pgid int
	cmd             string
}

// processesNaming returns the processes whose command line contains marker,
// excluding the test binary itself, which is not a process a case started.
//
// The scan's own `ps` child is not excluded and needs no exclusion: it is handed
// no marker, so its argv cannot match.
func processesNaming(t *testing.T, marker string) []procLine {
	t.Helper()
	var found []procLine
	for _, proc := range scanProcs(t) {
		if proc.pid == os.Getpid() || !strings.Contains(proc.cmd, marker) {
			continue
		}
		found = append(found, proc)
	}
	return found
}

// buildIn returns the pid and process group of the process building the fixture
// checkout, or zeros while there is none.
//
// Identification is positive: the launcher builds into scratch under the
// fixture's TMPDIR, so the build's argv names the fixture checkout. "The
// launcher's first child" was the old test, and it identified a scratch
// preflight child as the build on a correct launcher.
func buildIn(t *testing.T, marker string) (pid, group int) {
	t.Helper()
	for _, proc := range processesNaming(t, marker) {
		if strings.Contains(proc.cmd, "go build") {
			return proc.pid, proc.pgid
		}
	}
	return 0, 0
}

// assertNoSurvivors fails when any process still names marker and KILLs it
// first, so a failing case reports a leak without leaving one.
func assertNoSurvivors(t *testing.T, marker string) {
	t.Helper()
	// A zombie is dead and this process cannot reap it: it is another process's
	// exit this one has no business reporting as a leak.
	var survivors []procLine
	for _, proc := range processesNaming(t, marker) {
		if pidGone(proc.pid) {
			continue
		}
		survivors = append(survivors, proc)
	}
	if len(survivors) == 0 {
		return
	}
	var described []string
	for _, proc := range survivors {
		described = append(described, fmt.Sprintf("%d(pgid %d) %s", proc.pid, proc.pgid, proc.cmd))
	}
	for _, proc := range survivors {
		_ = syscall.Kill(-proc.pgid, syscall.SIGKILL)
		_ = syscall.Kill(proc.pid, syscall.SIGKILL)
	}
	t.Fatalf("left behind after cleanup: %s", strings.Join(described, " | "))
}

// waitExit waits for cmd to exit, bounded. It reports the exit code and true,
// or 0 and false when the deadline passed first -- which is a regression, not a
// hang, and the caller says so.
func waitExit(t *testing.T, cmd *exec.Cmd, within time.Duration) (int, bool) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return 0, true
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("wait for the launcher: %v", err)
		}
		return exitErr.ExitCode(), true
	case <-time.After(within):
		return 0, false
	}
}

// mustExit waits for cmd and fails with the fixture output when it did not exit
// in time or exited with a status other than want.
func mustExit(t *testing.T, cmd *exec.Cmd, f launcherFixture, want int) {
	t.Helper()
	code, exited := waitExit(t, cmd, launcherExitDeadline)
	if !exited {
		t.Fatalf("launcher still running after %s, want exit %d: the signal did not stop it; %s",
			launcherExitDeadline, want, f.output(t))
	}
	if code != want {
		t.Fatalf("launcher exit %d, want %d; %s", code, want, f.output(t))
	}
}

// signalLauncher sends sig to the launcher's own PID -- the signal the script
// documents for cancelling a background run.
func signalLauncher(t *testing.T, cmd *exec.Cmd, sig syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(cmd.Process.Pid, sig); err != nil {
		t.Fatalf("signal the launcher: %v", err)
	}
}

// signalLauncherGroup sends sig to the launcher's process group, which is how a
// terminal delivers Ctrl-C: every process in the foreground group gets it.
func signalLauncherGroup(t *testing.T, cmd *exec.Cmd, sig syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil {
		t.Fatalf("signal the launcher's process group: %v", err)
	}
}

// pidGone reports whether the pid's process has terminated. A zombie counts:
// the process is dead, and its parent may not have reaped it yet.
func pidGone(pid int) bool {
	state, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return true
	}
	trimmed := strings.TrimSpace(string(state))
	return trimmed == "" || strings.HasPrefix(trimmed, "Z")
}

// logPID reads the pid a fixture wrote under prefix, waiting for it to appear.
func logPID(t *testing.T, f launcherFixture, prefix string) int {
	t.Helper()
	var pid int
	testwait.Until(t, "the fixture to publish "+prefix, func() bool {
		pid = pidFromLines(f.lines(t), prefix)
		return pid != 0
	})
	return pid
}

// waitForLine waits until the fixture has recorded want, and fails naming what
// it saw instead. Failing here rather than proceeding is what stops a case from
// signalling a fixture that has not installed its handler yet.
func waitForLine(t *testing.T, f launcherFixture, want string) {
	t.Helper()
	testwait.UntilSeen(t, "the fixture to record "+want, func() (bool, string) {
		lines := f.lines(t)
		return slices.Contains(lines, want), strings.Join(lines, " ")
	})
}

// pidFromLines returns the pid recorded under prefix, or 0.
func pidFromLines(lines []string, prefix string) int {
	for _, line := range lines {
		rest, ok := strings.CutPrefix(line, prefix)
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(rest)
		if err != nil {
			continue
		}
		return pid
	}
	return 0
}

// logValue returns the value the fixture recorded under prefix, or "" when it
// recorded none. The value is the rest of the line verbatim: a path may hold a
// space, and a helper that logged one is not a helper that logged nothing.
func logValue(t *testing.T, f launcherFixture, prefix string) string {
	t.Helper()
	for _, line := range f.lines(t) {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			return rest
		}
	}
	return ""
}

// logPIDAndGroup reads the "pid=N;pgid=G" line a fixture wrote under prefix,
// waiting for it to appear, and returns both.
func logPIDAndGroup(t *testing.T, f launcherFixture, prefix string) (pid, group int) {
	t.Helper()
	testwait.UntilSeen(t, "the fixture to publish "+prefix, func() (bool, string) {
		lines := f.lines(t)
		for _, line := range lines {
			rest, ok := strings.CutPrefix(line, prefix)
			if !ok {
				continue
			}
			pidText, groupText, ok := strings.Cut(rest, ";pgid=")
			if !ok {
				continue
			}
			parsedPID, err := strconv.Atoi(pidText)
			if err != nil {
				continue
			}
			parsedGroup, err := strconv.Atoi(groupText)
			if err != nil {
				continue
			}
			pid, group = parsedPID, parsedGroup
			return true, ""
		}
		return false, strings.Join(lines, " ")
	})
	return pid, group
}

// TestMergeEnqueueLauncherInterruptsAColdBuildInItsOwnGroup pins the launcher's
// cold-build properties. Three invariants hold here, and the case is written so
// it cannot pass without them:
//
//   - The tracked child leads the process group that holds the build's tree, so
//     one signal reaches the build and every compiler it forks, including one
//     forked after stop_command's PID snapshot. The build runs under bash job
//     control for this (scripts/lib/gate-bounded.sh's run_bounded is the same
//     pattern); a background subshell whose child leads a group of its own would
//     make `$!` name a wrapper, and a compiler reparented when `go build` exits
//     would keep the caller's pipes open past the launcher's 130.
//     positively by the fixture's own scratch path in its argv rather than as
//     "the launcher's first child", which is a probe binary during scratch
//     preflight.
//   - No process here can reach a merge-queue helper. The script runs from a
//     fixture checkout whose only buildable package writes a sentinel and exits,
//     and requireIsolated fails the case if the script under test is not that
//     copy. A test that reached the real helper could put a real pull request in
//     the real merge queue from a `go test` run.
//
// The build is held open by a FIFO source file rather than a cold cache: a cold
// build is a race against the host, and this case has to observe the build
// before signalling it.
func TestMergeEnqueueLauncherInterruptsAColdBuildInItsOwnGroup(t *testing.T) {
	t.Parallel()
	c := newFixtureCheckout(t, filepath.Join(t.TempDir(), "tmp"))
	// The marker names the build: the launcher builds into scratch under the
	// fixture's TMPDIR, so the build's argv carries that path and no other
	// process on the machine does. That is positive identification -- the old
	// case asked for "the launcher's first child", which during scratch
	// preflight is whichever probe binary happens to be running.
	marker := c.tmp
	c.requireIsolated(t)
	// The checkout's scratch is outside launcherFixture's directory, so this
	// case sweeps its own markers. Registered before runLauncher, so it runs
	// after the group kills: a build the launcher leaked is reported here
	// rather than left running on the machine.
	for _, m := range []string{c.root, c.tmp} {
		t.Cleanup(func() { assertNoSurvivors(t, m) })
	}
	f := newLauncherFixture(t, "#!/usr/bin/env bash\nexit 0\n")
	cmd := f.launcherCmdAt(t, c.script, "--pr", "6499", "--repo", "example.invalid/none")
	// No EVENER_DEV_BIN: this case has to reach the build path. TMPDIR points at
	// the fixture, GOWORK=off keeps the build inside the fixture module even when
	// the fixture sits under a checkout that has a go.work, and GOENV=off drops
	// the developer's saved go env. A saved GOFLAGS such as -modfile=... would
	// otherwise make the build exit before it reaches the FIFO, and the case
	// would wait on a process that had already failed.
	cmd.Env = envOverride(cmd.Env, "TMPDIR="+c.tmp, "GOWORK=off", "GOENV=off")
	cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool {
		return strings.HasPrefix(kv, "EVENER_DEV_BIN=")
	})
	runLauncher(t, cmd, f)
	launcherPID := cmd.Process.Pid
	var buildPID, buildGroup int
	testwait.UntilSeen(t, "the launcher's build to appear", func() (bool, string) {
		buildPID, buildGroup = buildIn(t, marker)
		if buildPID == 0 {
			return false, "no process names the fixture scratch yet"
		}
		return true, ""
	})
	// The tracked child leads the group: kill -TERM -$child reaches the build
	// and every compiler it forks, including one forked after this moment.
	if buildGroup != buildPID {
		t.Fatalf("the build child %d is in group %d: stop_command cannot signal the build's tree by group, so a compiler forked after its PID walk escapes", buildPID, buildGroup)
	}
	if buildGroup == launcherPID {
		t.Fatalf("the build shares the launcher's group %d, so a group stop would signal the launcher too", buildGroup)
	}
	signalLauncher(t, cmd, syscall.SIGTERM)
	mustExit(t, cmd, f, 130)
	testwait.Until(t, fmt.Sprintf("build pid %d to terminate", buildPID), func() bool {
		return pidGone(buildPID)
	})
	// The stub never ran: the build was cancelled, so there was nothing to
	// start. The sentinel is what distinguishes "the fixture's helper was
	// cancelled" from "something else ran", and no merge-queue helper exists in
	// this checkout to run at all.
	if _, err := os.Stat(c.sentinel); err == nil {
		t.Fatalf("the fixture helper ran: the build finished after all, so this case did not exercise cancellation")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat fixture sentinel: %v", err)
	}
}

// TestMergeEnqueueLauncherForwardsSigtermToAPrebuiltHelper pins that the
// prebuilt path is interruptible: the traps are installed before either launch
// path, so a SIGTERM at the launcher's PID stops the helper and exits 130. A
// handler installed after the helper starts leaves the default action, which
// kills the launcher with 143 while the helper polls until the wait expires,
// holding the caller's output open for the rest of it.
//
// The fixture publishes its pid only after its TERM handler is installed, so a
// correct launcher cannot be failed by signalling a fixture that is still
// setting up.
func TestMergeEnqueueLauncherForwardsSigtermToAPrebuiltHelper(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
trap 'printf "helper-termed\n" >> "$EVENER_FIXTURE_LOG"; exit 130' TERM
printf 'helper-pid=%s\n' "$$" >> "$EVENER_FIXTURE_LOG"
while :; do sleep 0.05; done
`)
	var helperPID int
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	runLauncher(t, cmd, f, &helperPID)
	helperPID = logPID(t, f, "helper-pid=")
	signalLauncher(t, cmd, syscall.SIGTERM)
	mustExit(t, cmd, f, 130)
	// The helper ran its own TERM handler: the launcher forwarded the signal
	// rather than dying on the shell's default action.
	waitForLine(t, f, "helper-termed")
	// And it is gone by the time the launcher exits: a forwarded signal that
	// left the poller running would still hold the caller's output open.
	testwait.Until(t, fmt.Sprintf("helper pid %d to terminate", helperPID), func() bool {
		return pidGone(helperPID)
	})
}

// TestMergeEnqueueLauncherRunsTheHelperInItsOwnProcessGroup pins that the helper
// leads a group of its own. Sharing the launcher's group would let a terminal
// Ctrl-C reach the helper's `gh` before the helper's own handler cancelled the
// context, and the helper would then read the interrupted call as an ordinary
// GitHub failure and print a drop verdict with re-enqueue advice for a wait the
// operator cancelled.
//
// The assertion is the process-group structure, not a log line, because a log
// line cannot honestly observe this. A bash fixture forked with `&` from a
// non-interactive shell has SIGINT forced to SIG_IGN before its first command
// runs (SigIgn 6, measured), so `trap ... INT` in the fixture never installs and
// a "did gh receive the SIGINT?" oracle passes whether or not the defect is
// present. What the fix changes, and what this pins, is that the helper leads a
// group of its own: the terminal's foreground group holds the launcher alone, so
// the launcher is the only thing a Ctrl-C touches and the helper's cancelled
// context is what stops `gh`.
//
// It also pins that the helper still ends: the launcher forwards TERM after
// taking the SIGINT, so an operator's Ctrl-C stops the wait.
// TestMergeEnqueueLauncherPublishesItsGroupFileInPrivateScratch pins where the
// file the helper publishes its live `gh` group in is allowed to live.
//
// The handle's name cannot be secret -- the helper has to be told it -- so the
// only thing that keeps another local user from pre-creating that name as a link
// to a file the operator can write is the directory: a `mktemp -d` directory
// only this user can enter, created atomically, not a name directly under a
// shared TMPDIR. A launcher that opens the predictable path itself truncates the
// operator's file once per GitHub call, and the operator never learns why.
//
// The oracle is the handle's own directory: its permission bits, and that it is
// not TMPDIR itself. The second subtest is the other half of the same
// requirement -- if private scratch cannot be created at all, the prebuilt path
// must still run. Hardening that turned into a new failure mode would be worse
// than the hole it closed.
func TestMergeEnqueueLauncherPublishesItsGroupFileInPrivateScratch(t *testing.T) {
	t.Parallel()
	const script = `#!/usr/bin/env bash
set -uo pipefail
printf 'handle=%s\n' "${EVENER_MERGEQ_GH_GROUP_FILE:-}" >> "$EVENER_FIXTURE_LOG"
printf 'helper-pid=%s\n' "$$" >> "$EVENER_FIXTURE_LOG"
printf 'helper-started\n' >> "$EVENER_FIXTURE_LOG"
while [ ! -e "${EVENER_FIXTURE_LOG}.release" ]; do sleep 0.02; done
`
	t.Run("private scratch", func(t *testing.T) {
		f := newLauncherFixture(t, script)
		// Declared before the launcher starts and handed to runLauncher, so the
		// cleanup kills this helper's group as soon as its pid is known. The
		// helper blocks until released, and an assertion that fails first would
		// otherwise leave it polling: a test that leaks the thing it is testing
		// for is worse than the defect, because it hides the next one.
		var helperPID int
		cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
		runLauncher(t, cmd, f, &helperPID)
		helperPID = logPID(t, f, "helper-pid=")
		waitForLine(t, f, "helper-started")
		handle := logValue(t, f, "handle=")
		if handle == "" {
			t.Fatalf("the launcher named no group file for the prebuilt helper; %s", f.output(t))
		}
		dir := filepath.Dir(handle)
		if dir == f.tmpDir() {
			t.Fatalf("the group file is %s directly under TMPDIR: its name is predictable, so on a shared TMPDIR that permits symlink traversal another user can plant a link the launcher truncates through", handle)
		}
		// Read while the helper is still running: the launcher removes the
		// directory with the handle, so after exit there is nothing to stat.
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat the group file's directory %s: %v", dir, err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("the group file's directory %s has mode %04o, want 0700: another user can create names in it", dir, perm)
		}
		if err := os.WriteFile(f.log+".release", nil, 0o600); err != nil {
			t.Fatalf("release the fixture helper: %v", err)
		}
		mustExit(t, cmd, f, 0)
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the private directory %s survived the launcher: stat returned %v", dir, err)
		}
	})
	t.Run("no private scratch", func(t *testing.T) {
		f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
printf 'handle=%s\n' "${EVENER_MERGEQ_GH_GROUP_FILE:-}" >> "$EVENER_FIXTURE_LOG"
printf 'helper-ran\n' >> "$EVENER_FIXTURE_LOG"
`)
		// TMPDIR is a regular FILE, so `mktemp -d` fails there whatever the
		// caller's privileges are. Refusing by mode does not work: Linux running as
		// root has DAC override and creates the directory inside a 0500 one
		// anyway, so the subtest passed or failed on who ran it. All three roots
		// are denied, because the launcher tries them in turn: leaving
		// $XDG_RUNTIME_DIR usable would let it mint the handle there and the case
		// would be about a different fallback. The prebuilt path must still start
		// the helper -- it publishes nothing to a handle it could not mint, and it
		// does not fail the run. This is the fallback the high finding requires.
		tmp := f.tmpDir()
		if err := os.MkdirAll(filepath.Dir(tmp), 0o755); err != nil {
			t.Fatalf("create the fixture TMPDIR's parent: %v", err)
		}
		if err := os.WriteFile(tmp, []byte("not a directory\n"), 0o600); err != nil {
			t.Fatalf("write the fixture TMPDIR as a file: %v", err)
		}
		cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
		denied := filepath.Join(f.dir, "not-a-directory")
		if err := os.WriteFile(denied, []byte("not a directory\n"), 0o600); err != nil {
			t.Fatalf("write the denied root: %v", err)
		}
		cmd.Env = envOverride(cmd.Env,
			"XDG_RUNTIME_DIR="+denied,
			"HOME="+filepath.Join(denied, "home"),
		)
		runLauncher(t, cmd, f)
		mustExit(t, cmd, f, 0)
		waitForLine(t, f, "helper-ran")
		if handle := logValue(t, f, "handle="); handle != "" {
			t.Fatalf("the launcher named %s as the group file, but no private scratch could be created there", handle)
		}
	})
}

func TestMergeEnqueueLauncherRunsTheHelperInItsOwnProcessGroup(t *testing.T) {
	t.Parallel()
	gh := filepath.Join(t.TempDir(), "gh.sh")
	writeTestFile(t, gh, []byte(`#!/usr/bin/env bash
trap 'printf "gh-termed\n" >> "$EVENER_FIXTURE_LOG"; exit 0' TERM
printf 'gh-pid=%s;pgid=%s\n' "$$" "$(ps -o pgid= -p "$$" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
while :; do sleep 0.05; done
`), 0o755)
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
trap 'printf "helper-interrupted\n" >> "$EVENER_FIXTURE_LOG"; kill -TERM "$gh" 2>/dev/null; exit 130' INT TERM
"$EVENER_FIXTURE_GH" &
gh=$!
printf 'helper-pid=%s;pgid=%s\n' "$$" "$(ps -o pgid= -p "$$" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
while :; do sleep 0.05; done
`)
	var helperPID, ghPID int
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	cmd.Env = append(cmd.Env, "EVENER_FIXTURE_GH="+gh)
	runLauncher(t, cmd, f, &helperPID, &ghPID)
	launcherGroup := cmd.Process.Pid
	helperPID, helperGroup := logPIDAndGroup(t, f, "helper-pid=")
	ghPID, ghGroup := logPIDAndGroup(t, f, "gh-pid=")
	if helperGroup == launcherGroup {
		t.Fatalf("the helper runs in the launcher's process group (%d): a terminal Ctrl-C would reach its gh directly, which is what lets an interrupted wait report a drop", launcherGroup)
	}
	// The helper owns its `gh`: one signal from the launcher reaches both, and
	// the helper's cancelled context is what ends the call.
	if ghGroup != helperGroup {
		t.Fatalf("the helper's gh is in group %d, want the helper's own group %d", ghGroup, helperGroup)
	}
	signalLauncherGroup(t, cmd, syscall.SIGINT)
	mustExit(t, cmd, f, 130)
	waitForLine(t, f, "helper-interrupted")
	testwait.Until(t, fmt.Sprintf("fixture gh pid %d to terminate", ghPID), func() bool {
		return pidGone(ghPID)
	})
	testwait.Until(t, fmt.Sprintf("helper pid %d to terminate", helperPID), func() bool {
		return pidGone(helperPID)
	})
}

// TestMergeEnqueueLauncherForcedStopTakesTheHelpersGHCallsWithIt pins the second
// half of cancellation: a helper that does not act on the launcher's SIGTERM
// must not leave its `gh` call running after the launcher has reported 130.
//
// The client starts `gh` in a process group of its own (gh.go, so one signal
// reaches the CLI and anything a wrapper forked). That is what makes an
// interrupted wait stop promptly, and it is also why a group signal at the
// helper no longer reaches the call: the helper's group is the only one
// stop_command signals. A helper that never runs its handler therefore leaves a
// live enqueue call behind a launcher that has already returned 130.
//
// The fixture states that shape structurally. The helper ignores TERM, so the
// grace period expires and the launcher's forced stop is the only thing that can
// reach the call, and its `gh` leads a group of its own and ignores TERM as
// well, so only the forced KILL ends it. The assertion is that the call is gone
// after the launcher exits, which a TERM at the helper's group cannot satisfy.
func TestMergeEnqueueLauncherForcedStopTakesTheHelpersGHCallsWithIt(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
trap '' TERM
set -m
"$EVENER_FIXTURE_GH" &
gh=$!
set +m
printf 'helper-pid=%s;pgid=%s\n' "$$" "$(ps -o pgid= -p "$$" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
printf 'gh-pid=%s;pgid=%s\n' "$gh" "$(ps -o pgid= -p "$gh" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
while :; do sleep 0.05; done
`)
	// The call stands in for one that has not acted on TERM: a real `gh` may
	// exit on it, and this case is about the signal that reaches the group
	// whether or not it does.
	gh := filepath.Join(f.dir, "gh.sh")
	writeTestFile(t, gh, []byte(`#!/usr/bin/env bash
trap '' TERM
while :; do sleep 0.05; done
`), 0o755)
	var helperPID, ghPID int
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	cmd.Env = append(cmd.Env,
		"EVENER_FIXTURE_GH="+gh,
		// The grace has to expire for this case to reach the forced stop, and the
		// stopper's own grace bounds it. Both are the launcher's own knobs, so the
		// case shrinks them instead of spending its ceiling on them.
		"EVENER_HELPER_TERM_GRACE_SECONDS=0",
		"EVENER_STOP_TREE_GRACE_SECONDS=1",
	)
	runLauncher(t, cmd, f, &helperPID, &ghPID)
	helperPID, helperGroup := logPIDAndGroup(t, f, "helper-pid=")
	ghPID, ghGroup := logPIDAndGroup(t, f, "gh-pid=")
	if ghGroup != ghPID {
		t.Fatalf("the fixture's gh is in group %d, want a group of its own led by %d: the case has to reproduce the shape the client creates", ghGroup, ghPID)
	}
	if ghGroup == helperGroup {
		t.Fatalf("the fixture's gh shares the helper's group %d, so this case would prove nothing about the group the forced stop has to name", helperGroup)
	}
	if pidGone(ghPID) {
		t.Fatalf("the fixture's gh %d exited before the launcher was signalled", ghPID)
	}
	signalLauncher(t, cmd, syscall.SIGTERM)
	mustExit(t, cmd, f, 130)
	// The launcher reported the interrupt, so nothing it started may still be
	// submitting: a call left in its own group would outlive this line.
	testwait.Until(t, fmt.Sprintf("the helper's gh pid %d to be stopped before the launcher exits 130", ghPID), func() bool {
		return pidGone(ghPID)
	})
}

// TestMergeEnqueueLauncherDoesNotBuildUnderAReplaceableRoot pins the high
// finding on 7ef64c0d67: the launcher must not build and then exec a helper from
// a directory whose entry in its parent another local user can replace.
//
// `mktemp -d` gives the scratch mode 0700, which protects its contents. It says
// nothing about the entry: the parent's owner can rename a 0700 directory away
// whatever its mode is. So on a TMPDIR that is group- or other-writable and not
// sticky, an attacker replaces the whole directory after the build, and the `-x`
// test and the exec both resolve the attacker's file. The launcher then runs it
// with the operator's credentials and its `gh` token.
//
// The oracle is where the scratch was minted, read from the build's own argv:
// the case gives the launcher a replaceable TMPDIR and a safe root, and requires
// the scratch to be under the safe one. The second subtest is the requirement's
// other half -- with no safe root anywhere, the run still happens, because a
// launcher that refuses to merge on an ordinary host is worse than the window.
func TestMergeEnqueueLauncherDoesNotBuildUnderAReplaceableRoot(t *testing.T) {
	t.Parallel()
	t.Run("a safe root is used", func(t *testing.T) {
		// TMPDIR is replaceable: group- and other-writable, no sticky bit. mktemp's
		// own directories are 0700, so the mode is set explicitly -- the case has
		// to look like a shared one for there to be an entry to swap. The checkout
		// is the fixture, and its helper stub is `exit 0`, so this never reaches
		// the real merge helper however the scratch resolves.
		c := newFixtureCheckoutNamed(t, filepath.Join(t.TempDir(), "tmp"), "checkout")
		c.requireIsolated(t)
		if err := os.Chmod(c.tmp, 0o777); err != nil {
			t.Fatalf("make the fixture TMPDIR replaceable: %v", err)
		}
		safeRoot := t.TempDir()
		safe := filepath.Join(safeRoot, "runtime")
		requirePrivateChain(t, safeRoot, safe)
		f := newLauncherFixture(t, "#!/usr/bin/env bash\nexit 0\n")
		cmd := f.launcherCmdAt(t, c.script, "--pr", "6499", "--repo", "example.invalid/none")
		cmd.Env = envOverride(cmd.Env,
			"TMPDIR="+c.tmp, "XDG_RUNTIME_DIR="+safe,
			"GOWORK=off", "GOENV=off",
		)
		cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool {
			return strings.HasPrefix(kv, "EVENER_DEV_BIN=")
		})
		var buildPID int
		// Registered before the launcher starts, because the assertion that fails
		// here is the one that leaves a build behind: the FIFO keeps the build
		// blocked forever, so a case that fails without killing it leaks a
		// `go build` in a process group of its own. That is the defect this whole
		// round is about, and a test for it must not commit it.
		//
		// The sweep by this case's own directory, not only by the recorded pid:
		// the branch that records nothing is exactly the branch the red takes, so
		// a pid list alone would miss the build there and report a clean cleanup
		// while leaving it running.
		t.Cleanup(func() { assertNoSurvivors(t, safe) })
		t.Cleanup(func() { assertNoSurvivors(t, c.root) })
		t.Cleanup(func() { assertNoSurvivors(t, c.tmp) })
		t.Cleanup(func() { killGroups(buildPID) })
		// The build blocks on the fixture's FIFO, so its argv is observable while
		// it is in flight: that is where the scratch path it will write to is
		// named, and reading it there is what proves where the scratch was minted
		// without waiting for the build to finish.
		runLauncher(t, cmd, f)
		testwait.UntilSeen(t, "the fixture build to appear", func() (bool, string) {
			buildPID, _ = buildIn(t, safe)
			if buildPID != 0 {
				return true, ""
			}
			if pid, _ := buildIn(t, c.tmp); pid != 0 {
				// Recorded too, so cleanup can stop it: this is the branch a
				// regression takes, and its build must not outlive the failure.
				buildPID = pid
				return false, "the build names the replaceable TMPDIR, not the safe root"
			}
			return false, "no build names either root yet"
		})
		// Explicit: nothing was minted under the replaceable root. A scratch left
		// there is the directory an attacker swaps.
		entries, err := os.ReadDir(c.tmp)
		if err != nil {
			t.Fatalf("read the fixture TMPDIR: %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("the launcher minted scratch under the replaceable TMPDIR (%v): its entry can be replaced by another user", entries)
		}
		// The case is not finished when its assertions are. The launcher it
		// started is still waiting on the FIFO-blocked build, and returning here
		// would leave cleanup to stop a process the case never observed finishing
		// -- judged by whatever the sweep happened to see. So the run is signalled
		// and reaped, exactly as the cold-build case does: 130 is the interrupt
		// this signal means, and the build it was blocked on has to be gone too.
		signalLauncher(t, cmd, syscall.SIGTERM)
		mustExit(t, cmd, f, 130)
		testwait.Until(t, fmt.Sprintf("build pid %d to terminate", buildPID), func() bool {
			return pidGone(buildPID)
		})
	})
	t.Run("a private leaf under a replaceable ancestor is not a trusted root", func(t *testing.T) {
		// The finding this pins: the leaf's mode says nothing about its entry. A
		// per-user TMPDIR of 0700 beneath a shared directory without the sticky
		// bit passes any check that looks only at the leaf, and another user can
		// still replace the shared directory above it and move where every path
		// below resolves -- including the freshly built helper the launcher is
		// about to execute.
		shared := filepath.Join(t.TempDir(), "shared")
		if err := os.MkdirAll(shared, 0o755); err != nil {
			t.Fatalf("create the shared ancestor: %v", err)
		}
		if err := os.Chmod(shared, 0o777); err != nil {
			t.Fatalf("make the ancestor replaceable: %v", err)
		}
		c := newFixtureCheckoutNamed(t, filepath.Join(shared, "tmp"), "checkout")
		c.requireIsolated(t)
		if err := os.Chmod(c.tmp, 0o700); err != nil {
			t.Fatalf("make the fixture TMPDIR private: %v", err)
		}
		safeRoot := t.TempDir()
		safe := filepath.Join(safeRoot, "runtime")
		requirePrivateChain(t, safeRoot, safe)
		f := newLauncherFixture(t, "#!/usr/bin/env bash\nexit 0\n")
		cmd := f.launcherCmdAt(t, c.script, "--pr", "6499", "--repo", "example.invalid/none")
		cmd.Env = envOverride(cmd.Env,
			"TMPDIR="+c.tmp, "XDG_RUNTIME_DIR="+safe,
			"GOWORK=off", "GOENV=off",
		)
		cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool {
			return strings.HasPrefix(kv, "EVENER_DEV_BIN=")
		})
		var buildPID int
		t.Cleanup(func() { assertNoSurvivors(t, safe) })
		t.Cleanup(func() { assertNoSurvivors(t, c.root) })
		t.Cleanup(func() { assertNoSurvivors(t, shared) })
		t.Cleanup(func() { killGroups(buildPID) })
		runLauncher(t, cmd, f)
		testwait.UntilSeen(t, "the fixture build to appear", func() (bool, string) {
			buildPID, _ = buildIn(t, safe)
			if buildPID != 0 {
				return true, ""
			}
			if pid, _ := buildIn(t, shared); pid != 0 {
				buildPID = pid
				return false, "the build ran under the replaceable ancestor, so the leaf's mode was taken as the answer"
			}
			return false, "no build names either root yet"
		})
		if entries, err := os.ReadDir(c.tmp); err != nil {
			t.Fatalf("read the fixture TMPDIR: %v", err)
		} else if len(entries) != 0 {
			t.Fatalf("the launcher minted scratch under a directory whose ancestor another user can replace (%v)", entries)
		}
		signalLauncher(t, cmd, syscall.SIGTERM)
		mustExit(t, cmd, f, 130)
		testwait.Until(t, fmt.Sprintf("build pid %d to terminate", buildPID), func() bool {
			return pidGone(buildPID)
		})
	})
	t.Run("no safe root still runs", func(t *testing.T) {
		f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
printf 'helper-ran\n' >> "$EVENER_FIXTURE_LOG"
`)
		tmp := f.tmpDir()
		if err := os.MkdirAll(tmp, 0o755); err != nil {
			t.Fatalf("create the fixture TMPDIR: %v", err)
		}
		if err := os.Chmod(tmp, 0o777); err != nil {
			t.Fatalf("make the fixture TMPDIR replaceable: %v", err)
		}
		cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
		// No safe root to fall back to: an unusable XDG_RUNTIME_DIR and a HOME
		// whose cache cannot be created either.
		cmd.Env = envOverride(cmd.Env,
			"XDG_RUNTIME_DIR="+filepath.Join(f.dir, "no-such-runtime"),
			"HOME="+filepath.Join(f.dir, "no-such-home"),
		)
		runLauncher(t, cmd, f)
		mustExit(t, cmd, f, 0)
		waitForLine(t, f, "helper-ran")
	})
}

// TestMergeEnqueueLauncherDoesNotTouchACallerOwnedGroupFile pins the medium: an
// inherited variable must not make the prebuilt path treat a caller's directory
// as scratch.
//
// The launcher used to read `work` from the environment when naming the file the
// helper publishes its `gh` group in. A caller with `work` set for their own
// reasons therefore had a gh-group file in that directory truncated at launch
// and removed at exit -- data loss in a directory this tool never created. The
// scratch is passed explicitly now, and the prebuilt path mints its own.
func TestMergeEnqueueLauncherDoesNotTouchACallerOwnedGroupFile(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
printf 'helper-pid=%s\n' "$$" >> "$EVENER_FIXTURE_LOG"
printf 'handle=%s\n' "${EVENER_MERGEQ_GH_GROUP_FILE:-}" >> "$EVENER_FIXTURE_LOG"
`)
	// A caller's own directory with a file at the name the launcher would use,
	// holding bytes that are not the launcher's to touch.
	owned := filepath.Join(f.dir, "caller-work")
	if err := os.MkdirAll(owned, 0o755); err != nil {
		t.Fatalf("create the caller's directory: %v", err)
	}
	handle := filepath.Join(owned, "gh-group")
	const original = "the caller's own bytes\n"
	if err := os.WriteFile(handle, []byte(original), 0o600); err != nil {
		t.Fatalf("write the caller's file: %v", err)
	}
	var helperPID int
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	cmd.Env = append(cmd.Env, "work="+owned)
	runLauncher(t, cmd, f, &helperPID)
	mustExit(t, cmd, f, 0)
	if body, err := os.ReadFile(handle); err != nil {
		t.Fatalf("the caller's file is gone: the launcher treated a directory it did not create as scratch (%v)", err)
	} else if string(body) != original {
		t.Fatalf("the caller's file now holds %q: the launcher truncated it", body)
	}
	if got := logValue(t, f, "handle="); got == handle {
		t.Fatalf("the launcher named the caller's %s as the group file", handle)
	}
}

// TestMergeEnqueueLauncherStopsAHelperAWrapperLeftBehind pins the case where the
// launched PID is not the helper: EVENER_DEV_BIN names a wrapper that starts the
// Go helper as a child and exits on TERM without forwarding it.
//
// The launcher's graceful stop is a TERM at the launched PID, which is the
// wrapper. When the wrapper exits on that TERM the launcher used to treat the
// cancellation as done, because the only thing it checked was whether the traced
// child was still running. The Go helper it started is in the group recorded at
// launch and keeps polling -- or finishes submitting an enqueue -- after the
// launcher has reported 130.
//
// The recorded group is the handle that survives: the child is reaped, so `ps`
// assertions are needed. The launcher has to exit 130 because the operator asked
// it to stop, and the helper has to be gone because 130 means the enqueue
// stopped rather than merely that this process did.
func TestMergeEnqueueLauncherStopsAHelperAWrapperLeftBehind(t *testing.T) {
	t.Parallel()
	// The fixture IS the wrapper: EVENER_DEV_BIN points at it, so it is the
	// launcher's tracked child. It starts a stand-in for the Go helper in its own
	// group, records both, and exits on TERM without forwarding it -- the exit
	// that makes the launcher believe the cancellation is complete.
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
"$EVENER_FIXTURE_HELPER" &
inner=$!
printf 'wrapper-pid=%s;pgid=%s\n' "$$" "$(ps -o pgid= -p "$$" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
printf 'inner-pid=%s;pgid=%s\n' "$inner" "$(ps -o pgid= -p "$inner" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
trap 'exit 0' TERM
while :; do sleep 0.05; done
`)
	inner := filepath.Join(f.dir, "helper.sh")
	writeTestFile(t, inner, []byte(`#!/usr/bin/env bash
trap '' TERM
while :; do sleep 0.05; done
`), 0o755)
	var innerPID int
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	// The grace is long enough for the wrapper to act on TERM and exit, which is
	// the sequencing this case needs: the launcher's check must happen after the
	// wrapper is gone, or it would be testing the ordinary path. One second still
	// leaves the case well inside its ceiling, with the stopper's own grace at
	// one second as well.
	cmd.Env = append(cmd.Env,
		"EVENER_FIXTURE_HELPER="+inner,
		"EVENER_HELPER_TERM_GRACE_SECONDS=1",
		"EVENER_STOP_TREE_GRACE_SECONDS=1",
	)
	runLauncher(t, cmd, f, &innerPID)
	_, helperGroup := logPIDAndGroup(t, f, "wrapper-pid=")
	innerPID, innerGroup := logPIDAndGroup(t, f, "inner-pid=")
	if innerGroup != helperGroup {
		t.Fatalf("the inner stand-in is in group %d, want the wrapper's group %d: the recorded group is the only handle to it", innerGroup, helperGroup)
	}
	signalLauncher(t, cmd, syscall.SIGTERM)
	mustExit(t, cmd, f, 130)
	// The helper the wrapper started must be gone: 130 has to mean the enqueue
	// stopped, and a wrapper's exit says nothing about its child.
	testwait.Until(t, fmt.Sprintf("the helper the wrapper left behind (pid %d) to be stopped before the launcher exits 130", innerPID), func() bool {
		return pidGone(innerPID)
	})
}

// TestMergeEnqueueLauncherStopsACallItsWrapperLeftUnpublished pins the third
// handle a forced stop needs.
//
// An EVENER_DEV_BIN wrapper that exits on TERM while the Go helper it started is
// between starting `gh` and publishing that call's group leaves the call in
// neither place the launcher used to look: the tracked pid is gone, so a
// descendant walk from it finds nothing, and the handle file is not written yet,
// so the published group names nothing. The helper it left behind is alive in
// the group recorded at launch, and that group is the only route to the call --
// so the walk has to start from the group's surviving members, not just from the
// dead wrapper.
//
// The fixture is exactly that shape, with the call in a group of its own so it is
// reachable only through the helper and not by a group kill of the recorded one.
func TestMergeEnqueueLauncherStopsACallItsWrapperLeftUnpublished(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
"$EVENER_FIXTURE_HELPER" &
inner=$!
printf 'wrapper-pid=%s;pgid=%s\n' "$$" "$(ps -o pgid= -p "$$" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
printf 'inner-pid=%s;pgid=%s\n' "$inner" "$(ps -o pgid= -p "$inner" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
trap 'exit 0' TERM
while :; do sleep 0.05; done
`)
	call := filepath.Join(f.dir, "gh-call.sh")
	writeTestFile(t, call, []byte(`#!/usr/bin/env bash
trap '' TERM
while :; do sleep 0.05; done
`), 0o755)
	// The stand-in helper: it starts the call in a group of its own, exactly as
	// GHClient does (SysProcAttr leads a group, and the group id is the child's
	// pid), records it, and never writes EVENER_MERGEQ_GH_GROUP_FILE -- the
	// window between starting the call and publishing it.
	inner := filepath.Join(f.dir, "helper.sh")
	writeTestFile(t, inner, []byte(`#!/usr/bin/env bash
trap '' TERM
set -m
`+shellquote.Literal(call)+` &
call=$!
set +m
printf 'call-pid=%s;pgid=%s\n' "$call" "$(ps -o pgid= -p "$call" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
while :; do sleep 0.05; done
`), 0o755)
	var innerPID int
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	cmd.Env = append(cmd.Env,
		"EVENER_FIXTURE_HELPER="+inner,
		"EVENER_HELPER_TERM_GRACE_SECONDS=1",
		"EVENER_STOP_TREE_GRACE_SECONDS=1",
	)
	runLauncher(t, cmd, f, &innerPID)
	_, helperGroup := logPIDAndGroup(t, f, "wrapper-pid=")
	innerPID, innerGroup := logPIDAndGroup(t, f, "inner-pid=")
	callPID, callGroup := logPIDAndGroup(t, f, "call-pid=")
	if innerGroup != helperGroup {
		t.Fatalf("the inner helper is in group %d, want the wrapper's group %d: the recorded group is the only handle to it", innerGroup, helperGroup)
	}
	if callGroup == helperGroup || callGroup == innerPID {
		t.Fatalf("the call is in group %d: this case needs it outside both the helper's group and the launcher's tree", callGroup)
	}
	t.Cleanup(func() { killGroups(callPID) })
	if pidGone(callPID) {
		t.Fatalf("the fixture call %d exited before the launcher was signalled", callPID)
	}
	if pidGone(innerPID) {
		t.Fatalf("the fixture helper %d exited before the launcher was signalled", innerPID)
	}
	// The wrapper is still alive here, and it is the signal that makes it exit:
	// the launcher TERMs the tracked pid, the wrapper exits without forwarding,
	// the grace expires, and the forced stop is what has to find the call.
	signalLauncher(t, cmd, syscall.SIGTERM)
	mustExit(t, cmd, f, 130)
	testwait.Until(t, fmt.Sprintf("the unpublished call pid %d to be stopped before the launcher exits 130", callPID), func() bool {
		return pidGone(callPID)
	})
}

// TestMergeEnqueueLauncherReportsAMissingLibraryAsALauncherFailure pins the exit
// code for a checkout whose scripts/lib is a directory but whose contents cannot
// be sourced.
//
// The sources run before the EXIT trap, so a `.` that failed used to end the
// script under `set -e` with the shell's own status. Exit 1 means "the queue
// dropped your PR", which a caller retries, and nothing had asked GitHub
// anything. The contract says a launcher that could not run the helper is 2, so
// each source is checked and routed through launcher_failed, which names the
// library it could not read.
func TestMergeEnqueueLauncherReportsAMissingLibraryAsALauncherFailure(t *testing.T) {
	t.Parallel()
	c := newFixtureCheckout(t, filepath.Join(t.TempDir(), "tmp"))
	// One library is missing, which is what a partial checkout or a bad copy
	// looks like. scripts/lib itself stays a directory, so the root-resolution
	// guard passes and the source is what fails.
	if err := os.Remove(filepath.Join(c.root, "scripts", "lib", "gate-bounded.sh")); err != nil {
		t.Fatalf("remove the fixture library: %v", err)
	}
	f := newLauncherFixture(t, "#!/usr/bin/env bash\nexit 0\n")
	cmd := f.launcherCmdAt(t, c.script, "--pr", "6499", "--repo", "example.invalid/none")
	cmd.Env = envOverride(cmd.Env, "TMPDIR="+c.tmp, "GOWORK=off", "GOENV=off")
	runLauncher(t, cmd, f)
	// Exit 2, not 1: the launcher never got as far as a decision, so the caller
	// must not read this as a queue outcome.
	mustExit(t, cmd, f, 2)
	// And it says which library, so the operator does not have to guess.
	if out := f.output(t); !strings.Contains(out, "gate-bounded.sh") {
		t.Fatalf("the launcher did not name the missing library: %s", out)
	}
}

// TestMergeEnqueueLauncherStopsACallWhoseWrapperExited pins the forced stop
// against the shape that defeats a descendant walk: the helper's `gh` is a
// wrapper that starts the real CLI in the wrapper's own process group, publishes
// that group, and then exits.
//
// Once the wrapper exits, the CLI is reparented out of the helper's tree, so
// nothing walking descendants from the helper can find it, while it is still
// submitting the enqueue. The launcher has to hold the group id itself, which is
// what the helper publishes through EVENER_MERGEQ_GH_GROUP_FILE. The helper here
// ignores TERM, so the grace expires and only the forced stop can end the call.
//
// The CLI ignores TERM as well, so a signal at the wrong group -- or one that
// never arrives -- is visible as survival rather than as a lucky exit. The
// assertion is that the CLI is gone after the launcher exits 130, which is the
// property the operator relies on: "interrupted" means nothing is submitting.
func TestMergeEnqueueLauncherStopsACallWhoseWrapperExited(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
trap '' TERM
# The wrapper runs in the background and exits immediately: the real CLI is
# reparented out of this tree, exactly as a gh wrapper without exec leaves it.
#
# The wrapper gets a group of its own, and the handle names it, which is what
# internal/devtool/mergeq does with SysProcAttr: gh leads its own group, and the
# group id is the wrapper's pid.
set -m
"$EVENER_FIXTURE_WRAPPER" &
wrapper=$!
set +m
printf '%s\n' "$wrapper" > "$EVENER_MERGEQ_GH_GROUP_FILE"
printf 'helper-pid=%s;pgid=%s\n' "$$" "$(ps -o pgid= -p "$$" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
while :; do sleep 0.05; done
`)
	cli := filepath.Join(f.dir, "gh-cli.sh")
	writeTestFile(t, cli, []byte(`#!/usr/bin/env bash
trap '' TERM
printf 'cli-pid=%s;pgid=%s\n' "$$" "$(ps -o pgid= -p "$$" | tr -d ' ')" >> "$EVENER_FIXTURE_LOG"
while :; do sleep 0.05; done
`), 0o755)
	// The wrapper inherits the group the helper put it in, starts the CLI in that
	// same group -- no `exec`, no new group, exactly as a gh wrapper leaves the
	// real CLI -- and exits. The CLI keeps the group id after the reparent, which
	// is the only handle that still names it.
	wrapper := filepath.Join(f.dir, "gh")
	writeTestFile(t, wrapper, []byte(`#!/usr/bin/env bash
set -uo pipefail
`+shellquote.Literal(cli)+` &
# Give the CLI a moment to be scheduled, then leave it behind: this is the exit
# that makes a descendant walk from the helper useless.
sleep 0.1
exit 0
`), 0o755)
	var helperPID int
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	cmd.Env = append(cmd.Env,
		"EVENER_FIXTURE_WRAPPER="+wrapper,
		"EVENER_HELPER_TERM_GRACE_SECONDS=0",
		"EVENER_STOP_TREE_GRACE_SECONDS=1",
	)
	runLauncher(t, cmd, f, &helperPID)
	helperPID, helperGroup := logPIDAndGroup(t, f, "helper-pid=")
	cliPID, cliGroup := logPIDAndGroup(t, f, "cli-pid=")
	if cliGroup == helperGroup {
		t.Fatalf("the fixture CLI is in the helper's group %d: this case needs it outside the helper's tree", helperGroup)
	}
	// The wrapper has to be gone before the signal, or this case would be testing
	// the walk it exists to show is insufficient.
	testwait.Until(t, "the wrapper to exit, leaving its CLI behind", func() bool {
		for _, proc := range processesNaming(t, wrapper) {
			if strings.Contains(proc.cmd, wrapper) && proc.pid != cliPID {
				return false
			}
		}
		return true
	})
	if pidGone(cliPID) {
		t.Fatalf("the fixture CLI %d exited before the launcher was signalled", cliPID)
	}
	signalLauncher(t, cmd, syscall.SIGTERM)
	mustExit(t, cmd, f, 130)
	testwait.Until(t, fmt.Sprintf("the reparented CLI pid %d to be stopped before the launcher exits 130", cliPID), func() bool {
		return pidGone(cliPID)
	})
}

// TestMergeEnqueueLauncherFindsABuildUnderAScratchPathWithTwoSpaces pins that
// the process scan keeps a command line verbatim. It is the cold-build case run
// with a TMPDIR holding two consecutive spaces.
//
// Two spaces is what separates "the path is present" from "the path survives a
// re-render": a scan that split the whole row on whitespace and rejoined it with
// single spaces produced a string that no process's argv can contain, so
// readiness waited for a build it could not see and the sweep looked for
// survivors under a path nothing was running under. Both then agreed with each
// other and with nothing on the machine -- the case timed out, and the
// FIFO-blocked build it failed to find kept running in its own process group
// after the test exited.
//
// Two oracles, both about the real processes: the build the case signalled is
// gone after the launcher exits 130, and cleanup's scan over the same marker
// reports no survivor. Cleanup is what would have hidden the leak, so it has to
// see what the machine sees.
func TestMergeEnqueueLauncherFindsABuildUnderAScratchPathWithTwoSpaces(t *testing.T) {
	t.Parallel()
	c := newFixtureCheckoutNamed(t, filepath.Join(t.TempDir(), "tmp  dir"), "checkout")
	// The marker is the scratch path the build's argv carries. Two spaces in it
	// make a re-rendered command line unmatchable.
	marker := c.tmp
	c.requireIsolated(t)
	for _, m := range []string{c.root, c.tmp} {
		t.Cleanup(func() { assertNoSurvivors(t, m) })
	}
	f := newLauncherFixture(t, "#!/usr/bin/env bash\nexit 0\n")
	cmd := f.launcherCmdAt(t, c.script, "--pr", "6499", "--repo", "example.invalid/none")
	cmd.Env = envOverride(cmd.Env, "TMPDIR="+c.tmp, "GOWORK=off", "GOENV=off")
	cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool {
		return strings.HasPrefix(kv, "EVENER_DEV_BIN=")
	})
	runLauncher(t, cmd, f)
	var buildPID int
	testwait.UntilSeen(t, "the launcher's build to appear under a scratch path with two spaces", func() (bool, string) {
		buildPID, _ = buildIn(t, marker)
		if buildPID == 0 {
			return false, "no process names the fixture scratch yet"
		}
		return true, ""
	})
	signalLauncher(t, cmd, syscall.SIGTERM)
	mustExit(t, cmd, f, 130)
	testwait.Until(t, fmt.Sprintf("build pid %d to terminate", buildPID), func() bool {
		return pidGone(buildPID)
	})
	// Explicit, because this is the half of the finding that does not show up as
	// a failure: the build has to be dead, not merely unreported.
	if survivors := processesNaming(t, marker); len(survivors) > 0 {
		t.Fatalf("a build survived the launcher: %v", survivors)
	}
}

// TestMergeEnqueueLauncherRunsFromACheckoutPathWithSpaces pins that a checkout
// directory with a space in it changes nothing.
//
// The launcher quotes every path it builds, so a space is ordinary to it. The
// fixture log is what used to break: entries are values such as the directory the
// helper ran from, and splitting the log on whitespace truncated such a value at
// its first space, so a correct launcher failed a comparison against the path it
// really used. Two things are pinned here: the launcher exits 0 from a spaced
// path, and the value the fixture recorded comes back whole.
func TestMergeEnqueueLauncherRunsFromACheckoutPathWithSpaces(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
printf 'helper-ran=%s\n' "$PWD" >> "$EVENER_FIXTURE_LOG"
exit 0
`)
	// The space is in the checkout path, which is what this case is about. TMPDIR
	// stays free of one: a spaced scratch is a different question, and this case
	// must fail only for its own reason.
	c := newFixtureCheckoutNamed(t, filepath.Join(t.TempDir(), "tmp"), "checkout with spaces")
	c.requireIsolated(t)
	// The prebuilt path, so the helper that records its directory is the fixture
	// and not the fixture checkout's stub: start_helper changes into the checkout
	// root before launching, and the value it records is the one the log must
	// carry whole.
	cmd := f.launcherCmdAt(t, c.script, "--pr", "6499", "--repo", "example.invalid/none")
	cmd.Env = envOverride(cmd.Env, "TMPDIR="+c.tmp, "GOWORK=off", "GOENV=off")
	runLauncher(t, cmd, f)
	mustExit(t, cmd, f, 0)
	var ran string
	testwait.Until(t, "the helper to record the directory it ran from", func() bool {
		for _, line := range f.lines(t) {
			if rest, ok := strings.CutPrefix(line, "helper-ran="); ok {
				ran = rest
				return true
			}
		}
		return false
	})
	if ran != c.root {
		t.Fatalf("helper ran from %q, want the checkout root %q", ran, c.root)
	}
}

// TestMergeEnqueueLauncherResolvesItsRootUnderCDPATH pins that the root resolves
// to one path: `cd` prints the directory it moved to when a CDPATH entry
// matches, so the root would otherwise capture two lines. Everything that path is used for then
// failed -- the scratch library would not load from a path with a newline in it,
// and `cd "$root"` before the helper fails.
//
// CDPATH is the checkout itself, which is what makes the relative
// `scripts/ops/...` invocation match. The claim is that the run still works: the
// root resolves to one path, the helper starts from it, and the launcher exits 0.
func TestMergeEnqueueLauncherResolvesItsRootUnderCDPATH(t *testing.T) {
	t.Parallel()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	f := newLauncherFixture(t, `#!/usr/bin/env bash
set -uo pipefail
printf 'helper-ran=%s\n' "$PWD" >> "$EVENER_FIXTURE_LOG"
exit 0
`)
	cmd := f.launcherCmd(t, "--pr", "6499", "--repo", "example.invalid/none")
	cmd.Env = envOverride(cmd.Env, "CDPATH="+wd)
	runLauncher(t, cmd, f)
	mustExit(t, cmd, f, 0)
	// The helper ran from the resolved root, not from a path with a newline in
	// it: start_helper changes directory before launching.
	var ran string
	testwait.Until(t, "the helper to record the directory it ran from", func() bool {
		for _, line := range f.lines(t) {
			if rest, ok := strings.CutPrefix(line, "helper-ran="); ok {
				ran = rest
				return true
			}
		}
		return false
	})
	if ran != wd {
		t.Fatalf("helper ran from %q, want the checkout root %q", ran, wd)
	}
}

// TestMergeEnqueueLauncherLeavesNoCallBehindWhenForcedCleanupRuns pins that the
// forced stop leaves nothing that can still submit.
//
// The forced stop freezes the helper before it takes its inventory of `gh`
// groups, because a census of a tree that is free to fork is not a census: a
// call started after the read and before the kill would be in no list and would
// keep submitting after the launcher returned 130.
//
// The consequence for testing it is worth stating plainly. With the freeze in
// place a call the fixture tries to start during the forced stop never starts at
// all, so there is no red this case can produce against the pre-freeze code by
// asking "did the late call die" -- the late call does not exist to die. What
// this case still pins is the observable that matters to an operator: after the
// launcher exits 130, no process naming this case's directory is left, whether
// it was the first call, the helper, or one the helper was about to start. The
// freeze's own guarantee is covered by the code path it makes atomic and by the
// forced-stop cases above.
func TestMergeEnqueueLauncherLeavesNoCallBehindWhenForcedCleanupRuns(t *testing.T) {
	t.Parallel()
	f := newLauncherFixture(t, "#!/usr/bin/env bash\n"+
		"set -uo pipefail\n"+
		"trap '' TERM\n"+
		"printf 'helper-pid=%s;pgid=%s\\n' \"$$\" \"$(ps -o pgid= -p \"$$\" | tr -d ' ')\" >> \"$EVENER_FIXTURE_LOG\"\n"+
		"\"$EVENER_FIXTURE_GH\" &\n"+
		"gh=$!\n"+
		"printf 'gh-pid=%s;pgid=%s\\n' \"$gh\" \"$(ps -o pgid= -p \"$gh\" | tr -d ' ')\" >> \"$EVENER_FIXTURE_LOG\"\n"+
		"while :; do sleep 0.05; done\n")
	// A `gh` that ignores TERM and never finishes: only the forced KILL ends it.
	gh := filepath.Join(f.dir, "gh.sh")
	writeTestFile(t, gh, []byte(`#!/usr/bin/env bash
trap '' TERM
while :; do sleep 0.05; done
`), 0o755)
	var helperPID, ghPID int
	cmd := f.launcherCmd(t, "--pr", "6499", "--wait", "570", "--repo", "example.invalid/none")
	cmd.Env = append(cmd.Env,
		"EVENER_FIXTURE_GH="+gh,
		"EVENER_HELPER_TERM_GRACE_SECONDS=1",
		"EVENER_STOP_TREE_GRACE_SECONDS=1",
	)
	runLauncher(t, cmd, f, &helperPID, &ghPID)
	helperPID, _ = logPIDAndGroup(t, f, "helper-pid=")
	ghPID, ghGroup := logPIDAndGroup(t, f, "gh-pid=")
	if pidGone(ghPID) {
		t.Fatalf("the fixture's gh %d exited before the launcher was signalled", ghPID)
	}
	signalLauncher(t, cmd, syscall.SIGTERM)
	mustExit(t, cmd, f, 130)
	// The call must be gone, and nothing else this case started may be alive.
	testwait.Until(t, fmt.Sprintf("the call in group %d to be stopped before the launcher exits 130", ghGroup), func() bool {
		return pidGone(ghPID)
	})
	for _, proc := range processesNaming(t, f.dir) {
		if pidGone(proc.pid) {
			continue
		}
		t.Fatalf("a process outlived the launcher's forced stop: %d(pgid %d) %s", proc.pid, proc.pgid, proc.cmd)
	}
}

// TestMergeEnqueueLauncherRefusesANonNumericWaitBeforeBuilding pins that a bad
// --wait is a usage error at once.
//
// It used to reach the helper, which put a scratch mint and a full `go build`
// between the operator's typo and the answer. On a cold or noexec host the build
// then failed first, and its exit 2 named the build while the real problem was
// the argument. The parser is still the helper's -- overflow, leading zeros and
// the duration bound are its business (#5304) -- but "not a number" is answerable
// here, and the pre-port launcher answered it here.
//
// The oracle is that nothing was built: a launcher that answered correctly but
// built first would still have spent minutes on the typo.
func TestMergeEnqueueLauncherRefusesANonNumericWaitBeforeBuilding(t *testing.T) {
	t.Parallel()
	c := newFixtureCheckoutNamed(t, filepath.Join(t.TempDir(), "tmp"), "checkout")
	c.requireIsolated(t)
	f := newLauncherFixture(t, "#!/usr/bin/env bash\nprintf 'helper-ran\\n' >> \"$EVENER_FIXTURE_LOG\"\n")
	cmd := f.launcherCmdAt(t, c.script, "--pr", "6499", "--wait", "nope", "--repo", "example.invalid/none")
	cmd.Env = envOverride(cmd.Env, "TMPDIR="+c.tmp, "GOWORK=off", "GOENV=off")
	cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool {
		return strings.HasPrefix(kv, "EVENER_DEV_BIN=")
	})
	runLauncher(t, cmd, f)
	// Usage, not 1: nothing was decided, so a caller must not read this as a
	// queue outcome and must not retry.
	mustExit(t, cmd, f, 2)
	// Nothing was built: the answer came before the build, not after it.
	if survivors := processesNaming(t, c.tmp); len(survivors) > 0 {
		t.Fatalf("the launcher built before refusing the argument: %v", survivors)
	}
	for _, line := range f.lines(t) {
		if strings.HasPrefix(line, "helper-ran") {
			t.Fatalf("the launcher started the helper for a usage error: %v", f.lines(t))
		}
	}
}

// TestMergeEnqueueLauncherHelpPrintsTheWholeContract pins that --help prints the
// script's contract block: every comment line above `set -euo pipefail`, and
// nothing else.
//
// The oracle is structural rather than textual. --help used to be a numbered
// line range over the header, so every paragraph added to the contract moved the
// end and an un-updated range silently truncated it -- it stopped mid-sentence
// through one review round. Comparing the printed lines against the header's own
// comment block says the whole contract came out without restating any of it, so
// the help text stays free to change wording.
func TestMergeEnqueueLauncherHelpPrintsTheWholeContract(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("bash", mergeEnqueueLauncher, "--help").CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("--help exit = %v, want the usage status 2", err)
	}
	body, err := os.ReadFile(mergeEnqueueLauncher)
	if err != nil {
		t.Fatalf("read the launcher: %v", err)
	}
	// The contract is the leading comment block, stopping at the first line that
	// is not a comment. Its last line is the exit-code paragraph's final sentence.
	var contract []string
	for _, line := range strings.Split(string(body), "\n")[1:] {
		if !strings.HasPrefix(line, "#") {
			break
		}
		contract = append(contract, strings.TrimPrefix(strings.TrimPrefix(line, "#"), " "))
	}
	if len(contract) == 0 {
		t.Fatal("the launcher has no leading comment block to print")
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if strings.Join(got, "\n") != strings.Join(contract, "\n") {
		t.Fatalf("--help printed %d lines, the contract block is %d:\n%s", len(got), len(contract), out)
	}
}
