package dev

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// tripwire bounds a wait that should end at once; it only stops a broken gate
// from hanging the test.
const tripwire = 10 * time.Second // TRIPWIRE

// fakeGuard is a started guard whose exit the test decides.
type fakeGuard struct {
	name       string
	exit       chan int
	termOnce   sync.Once
	terminated chan struct{}
}

func (g *fakeGuard) Wait() int { return <-g.exit }

func (g *fakeGuard) Terminate() { g.termOnce.Do(func() { close(g.terminated) }) }

// fakeLauncher starts fakeGuards and records what the gate asked of it.
type fakeLauncher struct {
	mu      sync.Mutex
	guards  map[string]*fakeGuard
	specs   map[string]guardSpec
	started chan string
	// output is what a guard writes to its log when it starts.
	output map[string]string

	buildStatus int
	buildOutput string
	built       bool

	// startErr, when set, is what every Start returns.
	startErr error

	homeEnv []string
	// homeFailure, when set, is what PrivateGoHome writes to the guard's log
	// before it fails.
	homeFailure string
	// homeHold, when set, holds PrivateGoHome until it is closed, and
	// homeEntered is closed once the gate is inside it.
	homeHold    chan struct{}
	homeEntered chan struct{}
}

func newFakeLauncher() *fakeLauncher {
	return &fakeLauncher{
		guards:  map[string]*fakeGuard{},
		specs:   map[string]guardSpec{},
		started: make(chan string, len(browserGuards)),
		output:  map[string]string{},
		homeEnv: []string{"HOME=/private-go-home"},
	}
}

func (l *fakeLauncher) BuildFrontend(_ context.Context, log io.Writer) int {
	l.mu.Lock()
	l.built = true
	l.mu.Unlock()
	_, _ = io.WriteString(log, l.buildOutput)
	return l.buildStatus
}

func (l *fakeLauncher) PrivateGoHome(_ string, log io.Writer) ([]string, error) {
	if l.homeFailure != "" {
		_, _ = io.WriteString(log, l.homeFailure)
		return nil, errors.New("exit status 1")
	}
	if l.homeEntered != nil {
		close(l.homeEntered)
	}
	if l.homeHold != nil {
		<-l.homeHold
	}
	return l.homeEnv, nil
}

func (l *fakeLauncher) Start(spec guardSpec, log *os.File) (guardProcess, error) {
	if l.startErr != nil {
		return nil, l.startErr
	}
	g := &fakeGuard{name: spec.name, exit: make(chan int, 1), terminated: make(chan struct{})}
	l.mu.Lock()
	l.guards[spec.name] = g
	l.specs[spec.name] = spec
	out := l.output[spec.name]
	l.mu.Unlock()
	_, _ = log.WriteString(out)
	l.started <- spec.name
	return g, nil
}

func (l *fakeLauncher) guard(name string) *fakeGuard {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.guards[name]
}

// awaitStart returns the next guard the gate starts.
func (l *fakeLauncher) awaitStart(t *testing.T) *fakeGuard {
	t.Helper()
	select {
	case name := <-l.started:
		return l.guard(name)
	case <-time.After(tripwire):
		t.Fatal("no guard started")
		return nil
	}
}

// assertNoStart fails if the gate starts another guard; the gate has already
// had every chance to, since it starts guards before it waits.
func (l *fakeLauncher) assertNoStart(t *testing.T, why string) {
	t.Helper()
	select {
	case name := <-l.started:
		t.Fatalf("%s started while %s", name, why)
	default:
	}
}

type gateResult struct {
	status int
	keep   bool
}

// testGate is a gate running over a test launcher in the background.
type testGate struct {
	gate    *webGate
	signals chan os.Signal
	stdout  bytes.Buffer
	stderr  bytes.Buffer
	result  chan gateResult
}

func startTestGate(t *testing.T, launcher guardLauncher, slots int, buildFrontend bool) *testGate {
	t.Helper()
	return startTestGateWithSignals(t, launcher, slots, buildFrontend, make(chan os.Signal, 4))
}

// startTestGateWithSignals takes the signal channel, so a test can pass an
// unbuffered one: a send on it completes only once the gate receives it.
func startTestGateWithSignals(t *testing.T, launcher guardLauncher, slots int, buildFrontend bool, signals chan os.Signal) *testGate {
	t.Helper()
	return startGate(t, newBrowserGate(slots, buildFrontend), launcher, signals)
}

// startGate runs gate over launcher in the background, in a scratch of its
// own, as runWebGate would run it for real.
func startGate(t *testing.T, gate *webGate, launcher guardLauncher, signals chan os.Signal) *testGate {
	t.Helper()
	tg := &testGate{gate: gate, signals: signals, result: make(chan gateResult, 1)}
	gate.launcher = launcher
	gate.scratch = t.TempDir()
	gate.signals = signals
	gate.stdout, gate.stderr = &tg.stdout, &tg.stderr
	go func() {
		status, keep := tg.gate.run()
		tg.result <- gateResult{status, keep}
	}()
	return tg
}

func (tg *testGate) await(t *testing.T) gateResult {
	t.Helper()
	select {
	case r := <-tg.result:
		return r
	case <-time.After(tripwire):
		t.Fatal("the gate did not finish")
		return gateResult{}
	}
}

func (tg *testGate) assertRunning(t *testing.T, why string) {
	t.Helper()
	select {
	case r := <-tg.result:
		t.Fatalf("the gate returned %+v while %s", r, why)
	default:
	}
}

// startAll starts and returns every guard, in order, with slots for all.
func startAll(t *testing.T, launcher *fakeLauncher) []*fakeGuard {
	t.Helper()
	guards := make([]*fakeGuard, len(browserGuards))
	for i := range browserGuards {
		guards[i] = launcher.awaitStart(t)
	}
	return guards
}

func TestBrowserGateSuccessIsConcise(t *testing.T) {
	launcher := newFakeLauncher()
	launcher.output["layoutguard"] = "browser chatter\n"
	tg := startTestGate(t, launcher, len(browserGuards), false)
	for _, g := range startAll(t, launcher) {
		g.exit <- 0
	}
	r := tg.await(t)
	if r.status != 0 || r.keep {
		t.Fatalf("result = %+v, want status 0 and the scratch removed", r)
	}
	var want strings.Builder
	for _, guard := range browserGuards {
		want.WriteString("PASS  web-" + guard + "\n")
	}
	if tg.stdout.String() != want.String() || tg.stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q; want only the seven verdicts", tg.stdout.String(), tg.stderr.String())
	}
}

// The slot count is a ceiling, and a finished guard's slot goes to the next
// guard at once, whichever guard finished. The test always finishes the guard
// started last, so with two or more slots the first guard stays held for the
// whole run and every later guard still gets its turn.
func TestBrowserGateRunsGuardsInItsSlots(t *testing.T) {
	for _, slots := range []int{1, 2, 3, len(browserGuards)} {
		t.Run(strconv.Itoa(slots), func(t *testing.T) {
			launcher := newFakeLauncher()
			tg := startTestGate(t, launcher, slots, false)
			var live []*fakeGuard
			for range slots {
				live = append(live, launcher.awaitStart(t))
			}
			for started := slots; started < len(browserGuards); started++ {
				launcher.assertNoStart(t, "every slot was taken")
				last := live[len(live)-1]
				live = live[:len(live)-1]
				last.exit <- 0
				live = append(live, launcher.awaitStart(t))
			}
			if slots > 1 && live[0].name != browserGuards[0] {
				t.Fatalf("the first guard did not stay held: live = %v", live)
			}
			for _, g := range live {
				g.exit <- 0
			}
			if r := tg.await(t); r.status != 0 {
				t.Fatalf("status = %d, want 0; stderr = %s", r.status, tg.stderr.String())
			}
		})
	}
}

func TestBrowserGuardSlots(t *testing.T) {
	for value, want := range map[string]int{"": 1, "0": 1, "00": 1, "08": 8, "7": 7, "-1": 1, "x": 1, "2a": 1} {
		if got := browserGuardSlots(value); got != want {
			t.Errorf("browserGuardSlots(%q) = %d, want %d", value, got, want)
		}
	}
}

// The first nonzero status in verdict order is the gate's, not the first to
// arrive; only failing guards' logs are replayed.
func TestBrowserGateFailureReplaysOnlyFailingLogsAndKeepsTheScratch(t *testing.T) {
	launcher := newFakeLauncher()
	launcher.output["layoutguard"] = "browser chatter\n"
	launcher.output["overflowguard"] = "overflow failure detail\n"
	launcher.output["spawnguard"] = "spawn failure detail\n"
	tg := startTestGate(t, launcher, len(browserGuards), false)
	guards := startAll(t, launcher)
	guards[3].exit <- 5 // spawnguard fails first
	guards[1].exit <- 4 // overflowguard later, but earlier in verdict order
	for i, g := range guards {
		if i != 1 && i != 3 {
			g.exit <- 0
		}
	}
	r := tg.await(t)
	if r.status != 4 || !r.keep {
		t.Fatalf("result = %+v, want overflowguard's 4 and the scratch kept", r)
	}
	for _, want := range []string{"overflow failure detail", "spawn failure detail"} {
		if !strings.Contains(tg.stdout.String(), want) {
			t.Errorf("failing log %q was not replayed; stdout = %s", want, tg.stdout.String())
		}
	}
	if strings.Contains(tg.stdout.String(), "browser chatter") {
		t.Errorf("a passing guard's log was replayed; stdout = %s", tg.stdout.String())
	}
	for _, want := range []string{"FAIL  web-overflowguard (exit 4)", "FAIL  web-spawnguard (exit 5)"} {
		if !strings.Contains(tg.stderr.String(), want) {
			t.Errorf("stderr lacks %q: %s", want, tg.stderr.String())
		}
	}
	for _, kept := range []string{"layoutguard.log", "overflowguard.log", filepath.Join("overflowguard", "tmp")} {
		if _, err := os.Stat(filepath.Join(tg.gate.scratch, kept)); err != nil {
			t.Errorf("kept evidence %s: %v", kept, err)
		}
	}
}

func TestBrowserGateBuildFailureFailsOnlyTheSkillGuard(t *testing.T) {
	launcher := newFakeLauncher()
	launcher.buildStatus = 17
	launcher.buildOutput = "vite build exploded\n"
	tg := startTestGate(t, launcher, len(browserGuards), true)
	for range len(browserGuards) - 1 {
		launcher.awaitStart(t).exit <- 0
	}
	r := tg.await(t)
	if r.status != 17 {
		t.Fatalf("status = %d, want the build's 17", r.status)
	}
	if launcher.guard(skillGuard) != nil {
		t.Fatal("the skill guard started without a built frontend")
	}
	if !strings.Contains(tg.stderr.String(), "FAIL  web-skillguard (frontend build, exit 17)") || !strings.Contains(tg.stdout.String(), "vite build exploded") {
		t.Fatalf("stdout = %s\nstderr = %s", tg.stdout.String(), tg.stderr.String())
	}
	for _, guard := range browserGuards[:len(browserGuards)-1] {
		if !strings.Contains(tg.stdout.String(), "PASS  web-"+guard+"\n") {
			t.Errorf("web-%s did not reach its verdict", guard)
		}
	}
}

// signalOnFirstWrite queues a signal the first time the gate writes, which is
// when it starts printing verdicts: every guard has finished by then.
type signalOnFirstWrite struct {
	bytes.Buffer
	signals chan os.Signal
	queue   []os.Signal
	sent    bool
}

func (w *signalOnFirstWrite) Write(p []byte) (int, error) {
	if !w.sent {
		w.sent = true
		for _, sig := range w.queue {
			w.signals <- sig
		}
	}
	return w.Buffer.Write(p)
}

// An interrupt that lands while the verdicts print, after every guard has
// finished, still makes the run an interrupted one: its status, and the
// evidence kept rather than removed.
func TestBrowserGateHonorsAnInterruptDuringTheVerdicts(t *testing.T) {
	launcher := newFakeLauncher()
	signals := make(chan os.Signal, 4)
	tg := &testGate{gate: newBrowserGate(len(browserGuards), false), signals: signals, result: make(chan gateResult, 1)}
	stdout := &signalOnFirstWrite{signals: signals, queue: []os.Signal{syscall.SIGINT, syscall.SIGTERM}}
	tg.gate.launcher, tg.gate.scratch, tg.gate.signals = launcher, t.TempDir(), signals
	tg.gate.stdout, tg.gate.stderr = stdout, &tg.stderr
	go func() {
		status, keep := tg.gate.run()
		tg.result <- gateResult{status, keep}
	}()
	for _, g := range startAll(t, launcher) {
		g.exit <- 0
	}
	// Two interrupts landed; the second's status wins, as it does mid-run.
	if r := tg.await(t); r.status != 143 || !r.keep {
		t.Fatalf("result = %+v, want the second interrupt's 143 and the evidence kept", r)
	}
}

func TestBrowserGateRunsEveryGuardAfterASuccessfulBuild(t *testing.T) {
	launcher := newFakeLauncher()
	tg := startTestGate(t, launcher, len(browserGuards), true)
	for _, g := range startAll(t, launcher) {
		g.exit <- 0
	}
	if r := tg.await(t); r.status != 0 || r.keep {
		t.Fatalf("result = %+v, want status 0 and the scratch removed", r)
	}
	if !launcher.built {
		t.Fatal("the gate did not build the missing frontend")
	}
	var want strings.Builder
	want.WriteString("building the production frontend for web-skillguard…\n")
	for _, guard := range browserGuards {
		want.WriteString("PASS  web-" + guard + "\n")
	}
	if tg.stdout.String() != want.String() {
		t.Fatalf("stdout = %q, want %q", tg.stdout.String(), want.String())
	}
}

// Only a regular dist/index.html counts as a built frontend, as the shell's
// -f test did: anything else there is rebuilt over.
func TestFrontendBuiltNeedsARegularIndex(t *testing.T) {
	frontend := t.TempDir()
	index := filepath.Join(frontend, "dist", "index.html")
	if frontendBuilt(frontend) {
		t.Fatal("a frontend with no dist counts as built")
	}
	if err := os.MkdirAll(index, 0o755); err != nil {
		t.Fatal(err)
	}
	if frontendBuilt(frontend) {
		t.Fatal("a directory named index.html counts as a built frontend")
	}
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index, []byte("<html></html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !frontendBuilt(frontend) {
		t.Fatal("a regular dist/index.html does not count as built")
	}
}

// The gate finishes when the last guard is accounted for without starting:
// with one slot and a failed build, the skill guard is failed after every
// other guard has run, and nothing is left running to wait on.
func TestBrowserGateFinishesWhenTheLastGuardNeverStarts(t *testing.T) {
	launcher := newFakeLauncher()
	launcher.buildStatus = 17
	tg := startTestGate(t, launcher, 1, true)
	for range len(browserGuards) - 1 {
		launcher.awaitStart(t).exit <- 0
	}
	if r := tg.await(t); r.status != 17 {
		t.Fatalf("status = %d, want the build's 17", r.status)
	}
}

func TestBrowserGateFinishesWhenNoGuardStarts(t *testing.T) {
	launcher := newFakeLauncher()
	launcher.startErr = errors.New("exec: no such file")
	tg := startTestGate(t, launcher, 1, false)
	if r := tg.await(t); r.status != 1 {
		t.Fatalf("status = %d, want 1", r.status)
	}
	if n := strings.Count(tg.stderr.String(), "FAIL  web-"); n != len(browserGuards) {
		t.Fatalf("%d FAIL verdicts, want one per guard; stderr = %s", n, tg.stderr.String())
	}
}

// A private Go home that fails to set up fails only its guard, and the
// setup's own output is what the verdict replays.
func TestBrowserGateReplaysAFailedGuardSetup(t *testing.T) {
	launcher := newFakeLauncher()
	launcher.homeFailure = "cp: cannot create regular file: No space left on device\n"
	tg := startTestGate(t, launcher, len(browserGuards), false)
	for range len(browserGuards) - 1 {
		launcher.awaitStart(t).exit <- 0
	}
	r := tg.await(t)
	if r.status != 1 || !r.keep {
		t.Fatalf("result = %+v, want 1 and the scratch kept", r)
	}
	if launcher.guard(retirementGuard) != nil {
		t.Fatal("the retirement guard started without its private Go home")
	}
	if !strings.Contains(tg.stdout.String(), "No space left on device") {
		t.Errorf("the setup's output was not replayed; stdout = %s", tg.stdout.String())
	}
	if !strings.Contains(tg.stderr.String(), "FAIL  web-retirementguard (exit 1)") || strings.Contains(tg.stderr.String(), "no such file") {
		t.Errorf("stderr = %s", tg.stderr.String())
	}
}

func TestBrowserGateBuildsOnlyAMissingFrontend(t *testing.T) {
	launcher := newFakeLauncher()
	tg := startTestGate(t, launcher, len(browserGuards), false)
	for _, g := range startAll(t, launcher) {
		g.exit <- 0
	}
	tg.await(t)
	if launcher.built {
		t.Fatal("the gate rebuilt a frontend that was already built")
	}
}

// An interrupt TERMs every running guard except the skill guard, and waits for
// all of them, the skill guard included, before the gate exits.
// waitedNotSignalled are the guards an interrupt waits for but never TERMs:
// each is a go test (the retirement guard's behind npm) whose driver, Chrome
// and helper daemons are cleaned up by the test binary's own t.Cleanup, which
// a TERM would skip, and a TERM to npm alone would orphan.
var waitedNotSignalled = []string{retirementGuard, skillGuard}

// An interrupt TERMs every running guard except the go-test guards, and waits
// for all of them before the gate exits.
func TestBrowserGateInterruptTermsTheNodeGuardsAndWaitsForTheGoTestGuards(t *testing.T) {
	launcher := newFakeLauncher()
	tg := startTestGate(t, launcher, len(browserGuards), false)
	guards := startAll(t, launcher)
	tg.signals <- syscall.SIGTERM
	for _, g := range guards {
		if slices.Contains(waitedNotSignalled, g.name) {
			continue
		}
		select {
		case <-g.terminated:
		case <-time.After(tripwire):
			t.Fatalf("the interrupted gate never TERMed %s", g.name)
		}
		g.exit <- 143
	}
	for _, name := range waitedNotSignalled {
		tg.assertRunning(t, name+" was still running")
		launcher.guard(name).exit <- 0
	}
	if r := tg.await(t); r.status != 143 || !r.keep {
		t.Fatalf("result = %+v, want 143 and the scratch kept", r)
	}
	// Checked once the gate has returned, when its TERM pass has certainly run.
	for _, name := range waitedNotSignalled {
		select {
		case <-launcher.guard(name).terminated:
			t.Fatalf("the interrupted gate signalled %s; its go test's t.Cleanup would be skipped", name)
		default:
		}
	}
}

func TestBrowserGateSecondInterruptStopsWaiting(t *testing.T) {
	launcher := newFakeLauncher()
	tg := startTestGate(t, launcher, len(browserGuards), false)
	guards := startAll(t, launcher)
	tg.signals <- syscall.SIGINT
	for _, g := range guards {
		if !slices.Contains(waitedNotSignalled, g.name) {
			<-g.terminated
			g.exit <- 130
		}
	}
	tg.assertRunning(t, "the skill guard was still running")
	// The operator insisting with a different signal exits with that one's
	// status, as the shell trap it replaces did.
	tg.signals <- syscall.SIGTERM
	if r := tg.await(t); r.status != 143 || !r.keep {
		t.Fatalf("result = %+v, want the second signal's 143 and the scratch kept for the skill guard's leftovers", r)
	}
}

// No guard starts after an interrupt: one that is already pending when the
// gate starts leaves every guard unstarted.
func TestBrowserGateStartsNoGuardAfterAPendingInterrupt(t *testing.T) {
	launcher := newFakeLauncher()
	signals := make(chan os.Signal, 4)
	signals <- syscall.SIGHUP // queued before the gate runs, so it is pending from the start
	tg := startTestGateWithSignals(t, launcher, len(browserGuards), false, signals)
	if r := tg.await(t); r.status != 129 || !r.keep {
		t.Fatalf("result = %+v, want 129 and the scratch kept", r)
	}
	launcher.assertNoStart(t, "the gate was interrupted")
}

// With one slot, an interrupt while the first guard runs stops the gate once
// that guard has cleaned up; the guards still waiting never start.
func TestBrowserGateStartsNoWaitingGuardAfterAnInterrupt(t *testing.T) {
	launcher := newFakeLauncher()
	tg := startTestGate(t, launcher, 1, false)
	first := launcher.awaitStart(t)
	tg.signals <- syscall.SIGHUP
	select {
	case <-first.terminated:
	case <-time.After(tripwire):
		t.Fatal("the interrupted gate never TERMed the running guard")
	}
	first.exit <- 129
	if r := tg.await(t); r.status != 129 {
		t.Fatalf("status = %d, want 129", r.status)
	}
	launcher.assertNoStart(t, "the gate was interrupted")
}

// An interrupt during the retirement guard's private Go home setup waits for
// the setup, then stops: the guard never starts, and the setup is never cut
// short to keep writing into the scratch after the gate has moved on.
func TestBrowserGateInterruptDuringGuardSetupWaitsAndNeverStartsTheGuard(t *testing.T) {
	launcher := newFakeLauncher()
	launcher.homeHold = make(chan struct{})
	launcher.homeEntered = make(chan struct{})
	tg := startTestGate(t, launcher, len(browserGuards), false)
	var earlier []*fakeGuard
	for range 5 {
		earlier = append(earlier, launcher.awaitStart(t))
	}
	select {
	case <-launcher.homeEntered:
	case <-time.After(tripwire):
		t.Fatal("the gate never prepared the retirement guard's Go home")
	}
	tg.signals <- syscall.SIGTERM
	tg.assertRunning(t, "the setup was still running")
	close(launcher.homeHold)
	for _, g := range earlier {
		<-g.terminated
		g.exit <- 143
	}
	if r := tg.await(t); r.status != 143 {
		t.Fatalf("status = %d, want 143", r.status)
	}
	if launcher.guard(retirementGuard) != nil || launcher.guard(skillGuard) != nil {
		t.Fatal("a guard started after the interrupt")
	}
}

// An interrupt during the frontend build waits for the build, as a foreground
// step would be, then stops without starting a guard.
func TestBrowserGateInterruptDuringTheBuildWaitsForIt(t *testing.T) {
	launcher := &blockingBuildLauncher{fakeLauncher: newFakeLauncher(), entered: make(chan struct{}), hold: make(chan struct{}), stopped: make(chan struct{})}
	tg := startTestGateWithSignals(t, launcher, len(browserGuards), true, make(chan os.Signal))
	select {
	case <-launcher.entered:
	case <-time.After(tripwire):
		t.Fatal("the gate never started the build")
	}
	tg.signals <- syscall.SIGTERM // taken by the gate once this send completes
	// A gate still waiting for the build takes a second signal; one that
	// returned instead has nobody left to take it.
	select {
	case tg.signals <- syscall.SIGINT:
	case r := <-tg.result:
		t.Fatalf("the gate returned %+v while the build was still running", r)
	}
	// The operator insisting stops the build rather than abandoning it: the
	// gate asks it to stop, then still waits for it to exit.
	select {
	case <-launcher.stopped:
	case <-time.After(tripwire):
		t.Fatal("a second interrupt did not stop the build")
	}
	tg.assertRunning(t, "the stopped build had not exited yet")
	close(launcher.hold)
	if r := tg.await(t); r.status != 130 || !r.keep {
		t.Fatalf("result = %+v, want the second signal's 130 and the scratch kept", r)
	}
	launcher.assertNoStart(t, "the gate was interrupted during the build")
}

// blockingBuildLauncher holds the frontend build until released.
// It closes stopped when the gate asks it to stop, and still returns only once
// hold is closed, like a build that takes a moment to wind down.
type blockingBuildLauncher struct {
	*fakeLauncher
	entered chan struct{}
	hold    chan struct{}
	stopped chan struct{}
}

func (l *blockingBuildLauncher) BuildFrontend(ctx context.Context, _ io.Writer) int {
	close(l.entered)
	select {
	case <-ctx.Done():
		close(l.stopped)
	case <-l.hold:
		return 0
	}
	<-l.hold
	return 143
}

// Every guard is launched with private roots inside its own scratch directory,
// Node's compile cache off, and a Vite dep cache no other guard shares; the
// retirement guard also gets the private Go home.
func TestBrowserGateLaunchesEachGuardContained(t *testing.T) {
	launcher := newFakeLauncher()
	tg := startTestGate(t, launcher, len(browserGuards), false)
	for _, g := range startAll(t, launcher) {
		g.exit <- 0
	}
	tg.await(t)
	vites := map[string]string{}
	for _, guard := range browserGuards {
		spec := launcher.specs[guard]
		env := map[string]string{}
		for _, entry := range spec.env {
			k, v, _ := strings.Cut(entry, "=")
			env[k] = v
		}
		root := filepath.Join(tg.gate.scratch, guard)
		vite := env["BROWSER_GUARD_VITE_CACHE_DIR"]
		if !strings.HasPrefix(vite, root+string(os.PathSeparator)) {
			t.Errorf("%s Vite cache = %q, want inside %s", guard, vite, root)
		} else if other, ok := vites[vite]; ok {
			t.Errorf("%s shares its Vite cache with %s", guard, other)
		}
		vites[vite] = guard
		if guard == skillGuard {
			if !slices.Equal(spec.argv[:4], []string{"go", "test", "-tags", "browserguard"}) {
				t.Errorf("skill guard argv = %q", spec.argv)
			}
			continue
		}
		if env["NODE_DISABLE_COMPILE_CACHE"] != "1" {
			t.Errorf("%s NODE_DISABLE_COMPILE_CACHE = %q, want 1", guard, env["NODE_DISABLE_COMPILE_CACHE"])
		}
		names := []string{"TMPDIR"}
		if guard == retirementGuard {
			if env["HOME"] != "/private-go-home" {
				t.Errorf("retirement guard HOME = %q, want the private Go home's", env["HOME"])
			}
		} else {
			names = append(names, "HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME")
		}
		for _, name := range names {
			if !strings.HasPrefix(env[name], root+string(os.PathSeparator)) {
				t.Errorf("%s %s = %q, want inside %s", guard, name, env[name], root)
			}
		}
	}
}

func TestExportedByKeepsOnlyWhatTheStepSet(t *testing.T) {
	before := []string{"HOME=/home/a", "PATH=/bin", "PWD=/repo"}
	after := []string{"HOME=/private", "PATH=/bin", "GOCACHE=/cache", "PWD=/elsewhere", "SHLVL=2", "_=/usr/bin/env"}
	got := exportedBy(before, after)
	if want := []string{"HOME=/private", "GOCACHE=/cache"}; !slices.Equal(got, want) {
		t.Fatalf("exportedBy = %q, want %q", got, want)
	}
}
