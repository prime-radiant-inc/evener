// webGate runs a web gate's checks side by side (make test-web's typecheck,
// test and lint; make test-web-browser's browser guards): every check runs so
// one failure does not hide another's verdict, verdicts print in a fixed order
// once all have finished, and the exit status is the first nonzero one in that
// order. A failed check's log is replayed and the scratch is kept and named; a
// clean run removes it.
//
// Interrupts (HUP/INT/TERM, exiting 129/130/143): the gate TERMs every running
// check, except those a gate names as never signalled, and waits for each, so an
// interruption waits for the cleanup each check owns. A second signal exits at
// once, with that signal's status, without waiting any further; the scratch is
// then kept and named for whatever the unfinished checks left.

package dev

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	baseprocgroup "primeradiant.com/evener/execsupport/procgroup"
	"primeradiant.com/evener/internal/devtool/procgroup"
	"primeradiant.com/evener/internal/devtool/scratch"
)

// frontendDir is where the web checks run, relative to the repository root the
// gates run from.
const frontendDir = "cmd/evener-hub/frontend"

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
	// group starts the guard in its own process group: Terminate then TERMs
	// the whole tree, and anything left in the group when the guard exits is
	// killed. For a check run through npm, which exits on TERM without
	// stopping its script's children.
	group bool
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
	// and returns its status. Cancelling ctx asks the build to stop; it still
	// returns only once the build has exited.
	BuildFrontend(ctx context.Context, log io.Writer) int
	// PrivateGoHome prepares a private Go home under root, its diagnostics in
	// log, and returns the environment that selects it. It runs to
	// completion: the gate never interrupts it, so no half-done copy keeps
	// writing into the scratch after the gate has stopped waiting.
	PrivateGoHome(root string, log io.Writer) ([]string, error)
	// Start starts spec with its output in log.
	Start(spec guardSpec, log *os.File) (guardProcess, error)
}

// webGate is one run of a gate's checks.
type webGate struct {
	// name prefixes the gate's own diagnostics.
	name string
	// checks are the checks in verdict order; spec says how to start one
	// under its private root.
	checks []string
	spec   func(check, root string) guardSpec
	// needsBuild is the check that needs the production frontend build,
	// which runs first when buildFrontend is set; a failed build fails only
	// that check. Empty for a gate with no build.
	needsBuild string
	// unsignalled are the checks an interrupt waits for but never signals.
	unsignalled []string

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

// run runs every guard and returns the gate's exit status, plus whether the
// scratch must be kept (a failure, or an interrupt, keeps it).
func (g *webGate) run() (int, bool) {
	buildStatus := 0
	buildLog := filepath.Join(g.scratch, g.needsBuild+"-build.log")
	if g.buildFrontend {
		// The skill guard serves the embedded dist, so a missing dist is built
		// before any guard starts. A failed build fails only the skill guard:
		// the other guards serve the frontend through their own Vite.
		_, _ = fmt.Fprintf(g.stdout, "building the production frontend for web-%s…\n", g.needsBuild)
		built := make(chan int, 1)
		ctx, stopBuild := context.WithCancel(context.Background())
		defer stopBuild()
		go func() {
			log, err := os.Create(buildLog)
			if err != nil {
				_, _ = fmt.Fprintf(g.stderr, "%s: %v\n", g.name, err)
				built <- 1
				return
			}
			defer log.Close() //nolint:errcheck // the build's status is the result
			built <- g.launcher.BuildFrontend(ctx, log)
		}()
		select {
		case buildStatus = <-built:
		case sig := <-g.signals:
			// The build is waited for, as a foreground step would be. If the
			// operator insists with a second signal, whose status wins, the
			// build is stopped, and still waited for: an abandoned build
			// would keep writing after the gate had gone.
			select {
			case <-built:
				sig = latestSignal(sig, g.signals)
			case sig = <-g.signals:
				stopBuild()
				<-built
			}
			return signalStatus(sig), true
		}
	}

	n := len(g.checks)
	statuses := make([]int, n)
	live := make([]guardProcess, n)
	exits := make(chan guardExit, n)
	next, running, done := 0, 0, 0
	for done < n {
		for running < g.slots && next < n {
			index := next
			next++
			if g.checks[index] == g.needsBuild && buildStatus != 0 {
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
			}
			if log != nil {
				_ = log.Close()
			}
			if err != nil {
				_, _ = fmt.Fprintf(g.stderr, "web-%s: %v\n", g.checks[index], err)
				statuses[index] = 1
				done++
				continue
			}
			live[index] = proc
			running++
			go func() { exits <- guardExit{index, proc.Wait()} }()
		}
		if done == n {
			// The last guard was accounted for without starting (a failed
			// build, or a start that failed); nothing is running to wait on.
			break
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
	for i, guard := range g.checks {
		switch {
		case statuses[i] == 0:
			_, _ = fmt.Fprintf(g.stdout, "PASS  web-%s\n", guard)
			continue
		case guard == g.needsBuild && buildStatus != 0:
			_, _ = fmt.Fprintf(g.stderr, "FAIL  web-%s (frontend build, exit %d)\n", guard, buildStatus)
			g.replay(buildLog)
		default:
			_, _ = fmt.Fprintf(g.stderr, "FAIL  web-%s (exit %d)\n", guard, statuses[i])
			g.replay(filepath.Join(g.scratch, guard+".log"))
		}
		if status == 0 {
			status = statuses[i]
		}
	}
	// An interrupt that landed after the last check finished, while the
	// verdicts printed, still makes this an interrupted run: its status, and
	// the evidence kept rather than removed.
	select {
	case sig := <-g.signals:
		return signalStatus(latestSignal(sig, g.signals)), true
	default:
	}
	return status, status != 0
}

// prepare makes guard index's private roots, its log file, and its private Go
// home if it needs one, and returns how to start it. The log is returned
// whenever it was created, error or not: a failed setup's diagnostics are in
// it, for the verdict to replay.
func (g *webGate) prepare(index int) (guardSpec, *os.File, error) {
	guard := g.checks[index]
	root := filepath.Join(g.scratch, guard)
	for _, dir := range guardDirs {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			return guardSpec{}, nil, err
		}
	}
	log, err := os.Create(filepath.Join(g.scratch, guard+".log"))
	if err != nil {
		return guardSpec{}, nil, err
	}
	spec := g.spec(guard, root)
	if spec.privateGoHome {
		env, err := g.launcher.PrivateGoHome(root, log)
		if err != nil {
			return guardSpec{}, log, fmt.Errorf("private Go home: %w", err)
		}
		spec.env = append(env, spec.env...)
	}
	return spec, log, nil
}

// stop handles an interrupt: TERM every running check the gate does not name
// in unsignalled, then wait for them all, unless a second signal says to stop
// waiting; the gate then exits with that signal's status.
func (g *webGate) stop(sig os.Signal, live []guardProcess, exits <-chan guardExit) int {
	waiting := 0
	for i, proc := range live {
		if proc == nil {
			continue
		}
		waiting++
		if !slices.Contains(g.unsignalled, g.checks[i]) {
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
	return signalStatus(latestSignal(sig, g.signals))
}

// latestSignal is sig, or a second signal already waiting in signals: when a
// wait's last event and a second interrupt are ready together, select may take
// either, and the second interrupt's status must still win.
func latestSignal(sig os.Signal, signals <-chan os.Signal) os.Signal {
	select {
	case again := <-signals:
		return again
	default:
		return sig
	}
}

func (g *webGate) replay(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		_, _ = fmt.Fprintf(g.stderr, "%s: %v\n", g.name, err)
		return
	}
	_, _ = g.stdout.Write(data)
}

// runWebGate runs gate in a fresh scratch named after scratchPrefix, with the
// process's HUP/INT/TERM delivered to it, and returns its exit status. The
// scratch is removed after a clean run and kept and named otherwise.
func runWebGate(gate *webGate, scratchPrefix string) int {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	dir, err := scratch.Acquire(scratchPrefix, os.Stderr)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%s: %v\n", gate.name, err)
		return 1
	}
	gate.launcher = execGuardLauncher{}
	gate.scratch = dir.Path()
	gate.signals = signals
	gate.stdout, gate.stderr = os.Stdout, os.Stderr
	status, keep := gate.run()
	if keep {
		dir.KeepOnFailure()
	}
	dir.Release()
	return status
}

// groupDrainGrace is how long a grouped guard's leftovers get, once its leader
// has exited, to wind down on a TERM before they are KILLed.
const groupDrainGrace = 5 * time.Second

// execGuardLauncher starts the real guards.
type execGuardLauncher struct{}

func (execGuardLauncher) BuildFrontend(ctx context.Context, log io.Writer) int {
	// Not bound to ctx: exec would KILL it on cancel, where runStoppable TERMs
	// the whole tree and lets it wind down.
	cmd := exec.CommandContext(context.Background(), "npm", "run", "build")
	cmd.Dir = frontendDir
	cmd.Env = append(os.Environ(), "NODE_DISABLE_COMPILE_CACHE=1")
	cmd.Stdout, cmd.Stderr = log, log
	if err := runStoppable(ctx, cmd, groupDrainGrace); err != nil && cmd.ProcessState == nil {
		_, _ = fmt.Fprintf(log, "npm run build: %v\n", err)
		return 1
	}
	return procgroup.ExitCode(cmd.ProcessState)
}

// runStoppable runs cmd in its own process group until it exits. Cancelling
// ctx TERMs the whole group, and once cmd exits whatever it left in the group
// is drained (drainProcessGroup), so it returns only when the tree is gone:
// npm, for one, exits on TERM without stopping its script's children.
func runStoppable(ctx context.Context, cmd *exec.Cmd, grace time.Duration) error {
	if err := procgroup.Start(cmd); err != nil {
		return err
	}
	pgid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		procgroup.Terminate(pgid)
		err = <-done
	}
	drainProcessGroup(pgid, grace)
	return err
}

// PrivateGoHome runs scripts/lib/private-go-home.sh, the one definition of the
// private Go home that the gate scripts share, and returns the variables it
// exported.
func (execGuardLauncher) PrivateGoHome(root string, log io.Writer) ([]string, error) {
	const script = `. scripts/lib/private-go-home.sh && evener_prepare_private_go_home "$1" && env -0`
	cmd := exec.CommandContext(context.Background(), "bash", "-c", script, "private-go-home", root)
	cmd.Stderr = log
	out, err := cmd.Output()
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
	start := cmd.Start
	if spec.group {
		start = func() error { return procgroup.Start(cmd) }
	}
	if err := start(); err != nil {
		return nil, err
	}
	return &execGuard{cmd: cmd, group: spec.group, reaped: make(chan struct{})}, nil
}

type execGuard struct {
	cmd    *exec.Cmd
	group  bool
	reaped chan struct{}
}

func (p *execGuard) Wait() int {
	_ = p.cmd.Wait()
	close(p.reaped)
	if p.group {
		p.drainGroup()
	}
	return procgroup.ExitCode(p.cmd.ProcessState)
}

// drainGroup stops whatever a grouped guard left behind once its leader has
// exited (drainProcessGroup).
func (p *execGuard) drainGroup() { drainProcessGroup(p.cmd.Process.Pid, groupDrainGrace) }

// drainProcessGroup stops whatever a process group's leader left behind once
// the leader has exited: a TERM, then up to grace for the group to empty, then
// a KILL for anything still there. After an interrupt the group already has
// its TERM, and this is what waits for the tree (vitest and its workers, say)
// to act on it rather than killing it the moment npm exits. Nothing announces
// an empty group, so it is polled.
func drainProcessGroup(pgid int, grace time.Duration) {
	if !groupAlive(pgid) {
		return
	}
	procgroup.Terminate(pgid)
	deadline := time.Now().Add(grace)
	for groupAlive(pgid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	// Only a group still there is KILLed: once it has emptied, its id is free
	// for another process group to take.
	if groupAlive(pgid) {
		baseprocgroup.KillGroupAfterReap(pgid)
	}
}

// Terminate TERMs the guard: through its pidfd-backed process handle, which
// refuses a reaped process, or, for a grouped guard, its whole process group.
// A group is signalled by id, so one whose leader is already reaped is left
// alone, as procgroup.Stop does: the id may name another group by now.
func (p *execGuard) Terminate() {
	if !p.group {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
		return
	}
	select {
	case <-p.reaped:
	default:
		procgroup.Terminate(p.cmd.Process.Pid)
	}
}
