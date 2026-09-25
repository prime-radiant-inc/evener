package dev

// web-browser-guards runs the real browser-only frontend guards (make
// test-web-browser). They stay out of test-web because jsdom cannot evaluate
// the CSS cascade or browser geometry. It is the port of the scheduling half
// of scripts/web/test-web-browser.sh, which keeps only the load-aware default
// for the slot count and hands off here.
//
// Every guard runs so one missing browser or failing case does not hide the
// remaining guards' verdicts. Verdicts print in a fixed order once all have
// finished, and the exit status is the first nonzero one in that order. A
// failed guard's log is replayed and the scratch is kept and named; a clean
// run removes it.
//
// Interrupts (HUP/INT/TERM, exiting 129/130/143): the gate TERMs every running
// guard but the skill guard and waits for each, so an interruption waits for
// the cleanup each guard owns. The skill guard is waited for but never
// signalled: it is a go test whose driver, Chrome and helper daemons are
// cleaned up by the test binary's own t.Cleanup, which a TERM to go test would
// skip. A second signal exits at once without waiting any further; the skill
// guard's processes may then outlive the gate, so the scratch is kept and
// named for whatever they left.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"primeradiant.com/evener/internal/devtool/procgroup"
	"primeradiant.com/evener/internal/devtool/scratch"
)

// browserGuards is every guard in verdict order.
var browserGuards = []string{"layoutguard", "overflowguard", "shellguard", "spawnguard", "transcriptscrollguard", "retirementguard", "skillguard"}

const (
	skillGuard      = "skillguard"
	retirementGuard = "retirementguard"
	// browserFrontendDir is where the Node guards run, relative to the
	// repository root the gate runs from.
	browserFrontendDir = "cmd/evener-hub/frontend"
)

// guardSpec is how one guard is started: the command, the directory it runs
// in, what it adds to the environment, and whether it first needs a private Go
// home (a guard that runs go test under a private HOME must keep the user's Go
// caches, see scripts/lib/private-go-home.sh).
type guardSpec struct {
	name          string
	argv          []string
	dir           string
	env           []string
	privateGoHome bool
}

// browserGuardSpec is guard's launch contract under root, its own scratch
// directory. Each guard's Vite gets a dep cache of its own: two Vite processes
// optimizing into one cache race (issue #1586), and the guards run side by
// side. Node never writes a compile cache into the shared home.
func browserGuardSpec(guard, root string) guardSpec {
	vite := "BROWSER_GUARD_VITE_CACHE_DIR=" + filepath.Join(root, "vite-cache")
	switch guard {
	case skillGuard:
		// web-skillguard is the full-stack guard: cmd/evener-hub's
		// TestSkillComposerBrowser (browserguard build tag) drives the
		// production web app in real Chrome through a real hub against two
		// real `evener serve` daemons. The TestSkillGuard* unit tests ride
		// along: they need no browser and only this tag compiles them.
		return guardSpec{
			name: guard,
			argv: []string{"go", "test", "-tags", "browserguard", "./cmd/evener-hub", "-run", "^TestSkillComposerBrowser$|^TestSkillGuard", "-count=1"},
			dir:  ".",
			env:  []string{vite},
		}
	case retirementGuard:
		// retirementguard's contract is `npm run retirementguard`: it runs the
		// isolated Go fixture (TestRetirementBrowser), which starts the fixture
		// Hub and drives scripts/retirementguard/run.mjs against it.
		return guardSpec{
			name:          guard,
			argv:          []string{"npm", "run", "retirementguard"},
			dir:           browserFrontendDir,
			env:           []string{"TMPDIR=" + filepath.Join(root, "tmp"), "NODE_DISABLE_COMPILE_CACHE=1", vite},
			privateGoHome: true,
		}
	default:
		return guardSpec{
			name: guard,
			argv: []string{"node", "scripts/" + guard + "/run.mjs"},
			dir:  browserFrontendDir,
			env: []string{
				"HOME=" + filepath.Join(root, "home"),
				"TMPDIR=" + filepath.Join(root, "tmp"),
				"XDG_CONFIG_HOME=" + filepath.Join(root, "xdg-config"),
				"XDG_CACHE_HOME=" + filepath.Join(root, "xdg-cache"),
				"XDG_STATE_HOME=" + filepath.Join(root, "xdg-state"),
				"NODE_DISABLE_COMPILE_CACHE=1",
				vite,
			},
		}
	}
}

// guardDirs are the private roots every guard's scratch directory holds.
var guardDirs = []string{"home", "tmp", "xdg-config", "xdg-cache", "xdg-state", "vite-cache"}

// guardProcess is one started guard.
type guardProcess interface {
	// Wait blocks until the guard exits and returns its shell-style status.
	Wait() int
	// Terminate sends the guard SIGTERM.
	Terminate()
}

// guardLauncher is the gate's only contact with the outside world, so the
// scheduling and interrupt rules can be tested against guards the test drives.
type guardLauncher interface {
	// BuildFrontend runs the production frontend build, its output in log,
	// and returns its status.
	BuildFrontend(log io.Writer) int
	// PrivateGoHome prepares a private Go home under root and returns the
	// environment that selects it. It runs to completion: the gate never
	// interrupts it, so no half-done copy keeps writing into the scratch after
	// the gate has stopped waiting.
	PrivateGoHome(root string) ([]string, error)
	// Start starts spec with its output in log.
	Start(spec guardSpec, log *os.File) (guardProcess, error)
}

// browserGate is one run of the guards.
type browserGate struct {
	launcher      guardLauncher
	slots         int
	scratch       string
	buildFrontend bool
	signals       <-chan os.Signal
	stdout        io.Writer
	stderr        io.Writer
}

type guardExit struct {
	index  int
	status int
}

// signalStatus is the shell's exit status for a signal: 128 plus its number.
func signalStatus(sig os.Signal) int {
	if s, ok := sig.(syscall.Signal); ok {
		return 128 + int(s)
	}
	return 1
}

// browserGuardSlots reads BROWSER_GUARD_CONCURRENCY: digits only, read as
// decimal (08 is eight, 00 is zero), and at least one, so no value can leave
// the gate with no slot to start a guard in.
func browserGuardSlots(value string) int {
	if value == "" || strings.Trim(value, "0123456789") != "" {
		return 1
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// run runs every guard and returns the gate's exit status, plus whether the
// scratch must be kept (a failure, or an interrupt, keeps it).
func (g *browserGate) run() (int, bool) {
	buildStatus := 0
	buildLog := filepath.Join(g.scratch, "skillguard-build.log")
	if g.buildFrontend {
		// The skill guard serves the embedded dist, so a missing dist is built
		// before any guard starts. A failed build fails only the skill guard:
		// the other guards serve the frontend through their own Vite.
		_, _ = fmt.Fprintln(g.stdout, "building the production frontend for web-skillguard…")
		built := make(chan int, 1)
		go func() {
			log, err := os.Create(buildLog)
			if err != nil {
				_, _ = fmt.Fprintf(g.stderr, "web-browser-guards: %v\n", err)
				built <- 1
				return
			}
			defer log.Close() //nolint:errcheck // the build's status is the result
			built <- g.launcher.BuildFrontend(log)
		}()
		select {
		case buildStatus = <-built:
		case sig := <-g.signals:
			// The build is waited for, as a foreground step would be, unless
			// the operator insists with a second signal, whose status wins.
			select {
			case <-built:
			case sig = <-g.signals:
			}
			return signalStatus(sig), true
		}
	}

	n := len(browserGuards)
	statuses := make([]int, n)
	live := make([]guardProcess, n)
	exits := make(chan guardExit, n)
	next, running, done := 0, 0, 0
	for done < n {
		for running < g.slots && next < n {
			index := next
			next++
			if browserGuards[index] == skillGuard && buildStatus != 0 {
				statuses[index] = buildStatus
				done++
				continue
			}
			spec, log, err := g.prepare(index)
			// A signal that arrived while this guard was being prepared (the
			// retirement guard's private Go home can take a moment) is handled
			// before it starts: no guard starts after an interrupt.
			select {
			case sig := <-g.signals:
				if log != nil {
					_ = log.Close()
				}
				return g.stop(sig, live, exits), true
			default:
			}
			var proc guardProcess
			if err == nil {
				proc, err = g.launcher.Start(spec, log)
				_ = log.Close()
			}
			if err != nil {
				_, _ = fmt.Fprintf(g.stderr, "web-%s: %v\n", browserGuards[index], err)
				statuses[index] = 1
				done++
				continue
			}
			live[index] = proc
			running++
			go func() { exits <- guardExit{index, proc.Wait()} }()
		}
		select {
		case exit := <-exits:
			statuses[exit.index] = exit.status
			live[exit.index] = nil
			running--
			done++
		case sig := <-g.signals:
			return g.stop(sig, live, exits), true
		}
	}

	status := 0
	for i, guard := range browserGuards {
		switch {
		case statuses[i] == 0:
			_, _ = fmt.Fprintf(g.stdout, "PASS  web-%s\n", guard)
			continue
		case guard == skillGuard && buildStatus != 0:
			_, _ = fmt.Fprintf(g.stderr, "FAIL  web-skillguard (frontend build, exit %d)\n", buildStatus)
			g.replay(buildLog)
		default:
			_, _ = fmt.Fprintf(g.stderr, "FAIL  web-%s (exit %d)\n", guard, statuses[i])
			g.replay(filepath.Join(g.scratch, guard+".log"))
		}
		if status == 0 {
			status = statuses[i]
		}
	}
	return status, status != 0
}

// prepare makes guard index's private roots, its private Go home if it needs
// one, and its log file, and returns how to start it. The log is nil on error.
func (g *browserGate) prepare(index int) (guardSpec, *os.File, error) {
	guard := browserGuards[index]
	root := filepath.Join(g.scratch, guard)
	for _, dir := range guardDirs {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			return guardSpec{}, nil, err
		}
	}
	spec := browserGuardSpec(guard, root)
	if spec.privateGoHome {
		env, err := g.launcher.PrivateGoHome(root)
		if err != nil {
			return guardSpec{}, nil, fmt.Errorf("private Go home: %w", err)
		}
		spec.env = append(env, spec.env...)
	}
	log, err := os.Create(filepath.Join(g.scratch, guard+".log"))
	if err != nil {
		return guardSpec{}, nil, err
	}
	return spec, log, nil
}

// stop handles an interrupt: TERM every running guard but the skill guard,
// then wait for them all, unless a second signal says to stop waiting; the
// gate then exits with that signal's status.
func (g *browserGate) stop(sig os.Signal, live []guardProcess, exits <-chan guardExit) int {
	waiting := 0
	for i, proc := range live {
		if proc == nil {
			continue
		}
		waiting++
		if browserGuards[i] != skillGuard {
			proc.Terminate()
		}
	}
	for waiting > 0 {
		select {
		case exit := <-exits:
			live[exit.index] = nil
			waiting--
		case again := <-g.signals:
			return signalStatus(again)
		}
	}
	return signalStatus(sig)
}

func (g *browserGate) replay(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		_, _ = fmt.Fprintf(g.stderr, "web-browser-guards: %v\n", err)
		return
	}
	_, _ = g.stdout.Write(data)
}

// runWebBrowserGuards is `evener dev web-browser-guards`, run from the
// repository root.
func runWebBrowserGuards(args []string) int {
	if len(args) != 0 {
		_, _ = fmt.Fprintln(os.Stderr, "usage: evener dev web-browser-guards (BROWSER_GUARD_CONCURRENCY sets the slots)")
		return 2
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	dir, err := scratch.Acquire("evener-test-web-browser", os.Stderr)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "web-browser-guards: %v\n", err)
		return 1
	}
	gate := &browserGate{
		launcher:      execGuardLauncher{},
		slots:         browserGuardSlots(os.Getenv("BROWSER_GUARD_CONCURRENCY")),
		scratch:       dir.Path(),
		buildFrontend: !frontendBuilt(browserFrontendDir),
		signals:       signals,
		stdout:        os.Stdout,
		stderr:        os.Stderr,
	}
	status, keep := gate.run()
	if keep {
		dir.KeepOnFailure()
	}
	dir.Release()
	return status
}

// frontendBuilt reports whether frontend holds a built dist/index.html: a
// regular file, so nothing else at that path passes for a build.
func frontendBuilt(frontend string) bool {
	info, err := os.Stat(filepath.Join(frontend, "dist", "index.html"))
	return err == nil && info.Mode().IsRegular()
}

// execGuardLauncher starts the real guards.
type execGuardLauncher struct{}

func (execGuardLauncher) BuildFrontend(log io.Writer) int {
	cmd := exec.CommandContext(context.Background(), "npm", "run", "build")
	cmd.Dir = browserFrontendDir
	cmd.Env = append(os.Environ(), "NODE_DISABLE_COMPILE_CACHE=1")
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Run(); err != nil && cmd.ProcessState == nil {
		_, _ = fmt.Fprintf(log, "npm run build: %v\n", err)
		return 1
	}
	return procgroup.ExitCode(cmd.ProcessState)
}

// PrivateGoHome runs scripts/lib/private-go-home.sh, the one definition of the
// private Go home that the gate scripts share, and returns the variables it
// exported.
func (execGuardLauncher) PrivateGoHome(root string) ([]string, error) {
	const script = `. scripts/lib/private-go-home.sh && evener_prepare_private_go_home "$1" && env -0`
	out, err := exec.CommandContext(context.Background(), "bash", "-c", script, "private-go-home", root).Output()
	if err != nil {
		return nil, err
	}
	return exportedBy(os.Environ(), strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")), nil
}

// exportedBy is the entries of after that before lacks: what a step set or
// changed. bash's own bookkeeping (PWD, SHLVL, _) is left out, since it
// describes the setup shell, not the guard.
func exportedBy(before, after []string) []string {
	had := make(map[string]bool, len(before))
	for _, entry := range before {
		had[entry] = true
	}
	var changed []string
	for _, entry := range after {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || had[entry] {
			continue
		}
		switch name {
		case "PWD", "OLDPWD", "SHLVL", "_":
			continue
		}
		changed = append(changed, entry)
	}
	return changed
}

func (execGuardLauncher) Start(spec guardSpec, log *os.File) (guardProcess, error) {
	cmd := exec.CommandContext(context.Background(), spec.argv[0], spec.argv[1:]...)
	cmd.Dir = spec.dir
	cmd.Env = append(os.Environ(), spec.env...)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return execGuard{cmd}, nil
}

type execGuard struct{ cmd *exec.Cmd }

func (p execGuard) Wait() int {
	_ = p.cmd.Wait()
	return procgroup.ExitCode(p.cmd.ProcessState)
}

func (p execGuard) Terminate() { _ = p.cmd.Process.Signal(syscall.SIGTERM) }
