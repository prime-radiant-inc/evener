package hub

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/doctor"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/rendezvous"
)

// The daemon retirement process fixture proves the real process lifecycle end
// to end: a real Hub launches a real daemon, the daemon retires itself on
// proven idleness, the Hub rediscovers or resumes it, and a mutation submitted
// after resume settles exactly once with the same stable turn id and the
// reflected projection.
//
// Everything is real except two boundaries: the LLM is a scripted adapter and
// the clock is a fake. The daemon itself runs the production serve lifecycle
// from a cmd/evener test binary (built with `go test -c`), reached through the
// Hub's external process-launch seam so the real resolved argv/env, rendezvous
// registration, listener, AppWire server, retirement controller and
// non-terminal release all run unchanged. No binary is installed into PATH and
// no production HOME is used: the fixture owns its HOME/XDG roots and cleans up
// through exact process handles.
const (
	daemonRetirementProcessHelperVar = "DAEMON_RETIREMENT_PROCESS_HELPER"
	daemonRetirementProcessEnvFile   = "DAEMON_RETIREMENT_PROCESS_ENV_FILE"
	daemonRetirementProcessCtlFD     = "DAEMON_RETIREMENT_PROCESS_CTL_FD"
	daemonRetirementProcessEvtFD     = "DAEMON_RETIREMENT_PROCESS_EVT_FD"

	// daemonRetirementHubLaunchHelperVar gates the re-executed hub-side helper
	// that launches a daemon from its own process and then exits, so the
	// daemon's survival without any Hub process can be observed for real.
	daemonRetirementHubLaunchHelperVar  = "DAEMON_RETIREMENT_HUB_LAUNCH_HELPER"
	daemonRetirementHubLaunchRequestVar = "DAEMON_RETIREMENT_HUB_LAUNCH_REQUEST"
	// daemonRetirementDetachedHelperVar gates the fixture-owned detached
	// process: it blocks on its inherited pipe until the fixture releases it,
	// proving retirement does not sweep unrelated processes off the machine.
	daemonRetirementDetachedHelperVar = "DAEMON_RETIREMENT_DETACHED_HELPER"

	// daemonRetirementWatchdog is a hang tripwire, never the pacing mechanism:
	// every wait below is released by an inherited-pipe acknowledgement, a
	// process exit, or a settlement event.
	daemonRetirementWatchdog = 90 * time.Second
)

// daemonRetirementProcessEvent mirrors the event line the cmd/evener helper
// writes. The JSON field names are the wire contract between the two test
// binaries.
type daemonRetirementProcessEvent struct {
	Kind      string            `json:"kind"`
	Root      string            `json:"root,omitempty"`
	Name      string            `json:"name,omitempty"`
	Entry     *rendezvous.Entry `json:"entry,omitempty"`
	Now       string            `json:"now,omitempty"`
	Armed     *bool             `json:"armed,omitempty"`
	Remaining int64             `json:"remaining_ns,omitempty"`
	Seq       int               `json:"seq,omitempty"`
	Input     string            `json:"input,omitempty"`
	Fired     *bool             `json:"fired,omitempty"`
	Err       string            `json:"err,omitempty"`
	Phase     string            `json:"phase,omitempty"`
	Timeout   int64             `json:"timeout_millis,omitempty"`
	Blockers  []string          `json:"blockers,omitempty"`
}

type daemonRetirementProcessCommand struct {
	Cmd   string `json:"cmd"`
	Nanos int64  `json:"d_ns,omitempty"`
	Seq   int    `json:"seq,omitempty"`
}

var daemonRetirementHelperBuildOnce struct {
	once sync.Once
	path string
	err  error
}

// daemonRetirementHelperBinary builds the cmd/evener test binary once per test
// binary, into a fixture-owned path under the package's throwaway test root.
// `go test -c` is what makes TestDaemonRetirementProcessHelper invokable as a
// subprocess entry; nothing is installed anywhere a user could see it.
func daemonRetirementHelperBinary(t *testing.T) string {
	t.Helper()
	daemonRetirementHelperBuildOnce.once.Do(func() {
		repoRoot, err := filepath.Abs("../..")
		if err != nil {
			daemonRetirementHelperBuildOnce.err = err
			return
		}
		dir := filepath.Join(testEnv.Root, "daemon-retirement-helper")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			daemonRetirementHelperBuildOnce.err = err
			return
		}
		out := filepath.Join(dir, "evener-serve.test")
		build := exec.Command("go", "test", "-c", "-o", out, "./cmd/evener")
		build.Dir = repoRoot
		if output, buildErr := build.CombinedOutput(); buildErr != nil {
			daemonRetirementHelperBuildOnce.err = fmt.Errorf("build cmd/evener test helper: %w\n%s", buildErr, output)
			return
		}
		daemonRetirementHelperBuildOnce.path = out
	})
	if daemonRetirementHelperBuildOnce.err != nil {
		t.Fatalf("%v", daemonRetirementHelperBuildOnce.err)
	}
	return daemonRetirementHelperBuildOnce.path
}

// daemonRetirementProcessHandle owns one real daemon process and the two
// inherited pipes the fixture drives it through.
type daemonRetirementProcessHandle struct {
	cmd    *exec.Cmd
	pid    int
	kill   func() error
	ctlW   *os.File
	evtR   *os.File
	events *daemonRetirementProcessEvents

	// childCtl/childEvt are the parent's copies of the child's ends. They are
	// closed once the process has started so the event pipe reaches EOF exactly
	// when the daemon exits.
	childCtl *os.File
	childEvt *os.File
	started  bool
	entry    rendezvous.Entry
	// waitErr is filled only for a daemon the fixture started itself (a direct
	// serve); a Hub-launched daemon's Wait belongs to spawnDaemon.
	waitErr chan error
}

func (h *daemonRetirementProcessHandle) afterStart() {
	if h.started {
		return
	}
	h.started = true
	if h.childCtl != nil {
		_ = h.childCtl.Close()
	}
	if h.childEvt != nil {
		_ = h.childEvt.Close()
	}
}

func (h *daemonRetirementProcessHandle) exited() bool {
	select {
	case <-h.events.closed():
		return true
	default:
		return false
	}
}

func (h *daemonRetirementProcessHandle) close() {
	if h.ctlW != nil {
		_ = h.ctlW.Close()
	}
	if h.evtR != nil {
		_ = h.evtR.Close()
	}
}

// daemonRetirementProcessEvents is the fixture-side event stream of one daemon.
// A line reader appends to a history and wakes waiters, so a wait can scan the
// events that already arrived and then block until one does. The wait is a
// pipe/event wait with a watchdog tripwire; it is never a sleep.
type daemonRetirementProcessEvents struct {
	mu      sync.Mutex
	all     []daemonRetirementProcessEvent
	notify  chan struct{}
	stopped chan struct{}
	err     error
}

func newDaemonRetirementProcessEvents() *daemonRetirementProcessEvents {
	return &daemonRetirementProcessEvents{
		notify:  make(chan struct{}, 1),
		stopped: make(chan struct{}),
	}
}

func (e *daemonRetirementProcessEvents) add(ev daemonRetirementProcessEvent) {
	e.mu.Lock()
	e.all = append(e.all, ev)
	e.mu.Unlock()
	select {
	case e.notify <- struct{}{}:
	default:
	}
}

func (e *daemonRetirementProcessEvents) close(err error) {
	e.mu.Lock()
	if e.err == nil {
		e.err = err
	}
	e.mu.Unlock()
	close(e.stopped)
}

func (e *daemonRetirementProcessEvents) closed() <-chan struct{} { return e.stopped }

func (e *daemonRetirementProcessEvents) history() []daemonRetirementProcessEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]daemonRetirementProcessEvent(nil), e.all...)
}

func (e *daemonRetirementProcessEvents) waitFor(what string, pred func(daemonRetirementProcessEvent) bool) (daemonRetirementProcessEvent, error) {
	return e.waitForIndexed(what, func(_ int, ev daemonRetirementProcessEvent) bool { return pred(ev) })
}

// waitForIndexed is waitFor over the event's position in the history, so a
// caller can wait for an event that follows a specific earlier one rather than
// matching a repeated kind from an earlier phase.
func (e *daemonRetirementProcessEvents) waitForIndexed(what string, pred func(int, daemonRetirementProcessEvent) bool) (daemonRetirementProcessEvent, error) {
	deadline := time.Now().Add(daemonRetirementWatchdog)
	for {
		e.mu.Lock()
		for i, ev := range e.all {
			if pred(i, ev) {
				e.mu.Unlock()
				return ev, nil
			}
		}
		history := append([]daemonRetirementProcessEvent(nil), e.all...)
		e.mu.Unlock()
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return daemonRetirementProcessEvent{}, fmt.Errorf("%s: watchdog deadline with events %+v", what, history)
		}
		select {
		case <-e.notify:
		case <-e.stopped:
			// Drain once more: the final event can share the pipe with EOF.
			e.mu.Lock()
			for i, ev := range e.all {
				if pred(i, ev) {
					e.mu.Unlock()
					return ev, nil
				}
			}
			err := e.err
			history := append([]daemonRetirementProcessEvent(nil), e.all...)
			e.mu.Unlock()
			if err != nil {
				return daemonRetirementProcessEvent{}, fmt.Errorf("%s: event stream closed: %w (events %+v)", what, err, history)
			}
			return daemonRetirementProcessEvent{}, fmt.Errorf("%s: event stream closed (events %+v)", what, history)
		case <-time.After(remaining):
		}
	}
}

// readDaemonRetirementProcessEvents decodes the helper's event pipe into the
// shared history until the daemon exits and the pipe reaches EOF.
func readDaemonRetirementProcessEvents(r *os.File) *daemonRetirementProcessEvents {
	events := newDaemonRetirementProcessEvents()
	go func() {
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			var ev daemonRetirementProcessEvent
			if err := json.Unmarshal(line, &ev); err != nil {
				events.add(daemonRetirementProcessEvent{Kind: "decode_error", Err: err.Error()})
				continue
			}
			events.add(ev)
		}
		events.close(scanner.Err())
	}()
	return events
}

// daemonRetirementProcessFixture owns the private roots, the Hub spawner, the
// daemon processes, the clock pipes and the rendezvous root for one lifecycle.
type daemonRetirementProcessFixture struct {
	t *testing.T

	root       string
	home       string
	configHome string
	stateHome  string
	cacheHome  string

	runDir       string
	stateDir     string
	workDir      string
	providers    string
	hubBinary    string
	helperBinary string

	cfg Config
	hub *HubSpawner

	entry     rendezvous.Entry
	sessionID string

	mu             sync.Mutex
	handles        []*daemonRetirementProcessHandle
	params         map[string]appwire.TurnStartParams
	turnMutations  map[string]string
	advanceSeq     int
	effectiveArm   time.Duration
	lastAdvance    daemonRetirementProcessEvent
	haveAdvance    bool
	seamInstalled  bool
	previousSeam   func(binary string, args, env []string) *exec.Cmd
	launchSequence int
}

// newDaemonRetirementProcessFixture is the plan's fixture constructor: a Hub
// with its default configuration launches one real daemon.
func newDaemonRetirementProcessFixture(t *testing.T) *daemonRetirementProcessFixture {
	t.Helper()
	return newDaemonRetirementProcessFixtureWithConfig(t, DefaultConfig())
}

func newDaemonRetirementProcessFixtureWithConfig(t *testing.T, cfg Config) *daemonRetirementProcessFixture {
	t.Helper()
	f := &daemonRetirementProcessFixture{
		t:             t,
		cfg:           cfg,
		params:        map[string]appwire.TurnStartParams{},
		turnMutations: map[string]string{},
	}
	f.setupRoots(t)
	f.installSeam()
	f.hub = f.newSpawner()
	t.Cleanup(func() {
		if err := f.cleanup(); err != nil {
			t.Error(err)
		}
	})
	if err := f.launchInitial(); err != nil {
		t.Fatalf("initial Hub launch: %v", err)
	}
	return f
}

func (f *daemonRetirementProcessFixture) setupRoots(t *testing.T) {
	t.Helper()
	f.helperBinary = daemonRetirementHelperBinary(t)
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("abs repo root: %v", err)
	}
	f.hubBinary = filepath.Join(liveStackBinaries(t, repoRoot), "evener")

	f.root = t.TempDir()
	f.home = filepath.Join(f.root, "home")
	f.configHome = filepath.Join(f.root, "config")
	f.stateHome = filepath.Join(f.root, "state-home")
	f.cacheHome = filepath.Join(f.root, "cache")
	f.runDir = filepath.Join(f.root, "run")
	f.stateDir = filepath.Join(f.root, "sessions-root")
	f.workDir = filepath.Join(f.root, "work")
	for _, dir := range []string{f.home, f.configHome, f.stateHome, f.cacheHome, f.runDir, f.stateDir, f.workDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create fixture dir %s: %v", dir, err)
		}
	}
	// The real launch-check the Hub runs before every spawn resolves the model
	// against this providers.toml; its endpoint is never reached by the contract
	// check, and the daemon's LLM boundary is scripted in the helper.
	f.providers = filepath.Join(f.configHome, "providers.toml")
	providersTOML := "default = \"fixture\"\n\n[providers.fixture]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:1\"\napi_key = \"daemon-retirement-fixture-not-a-secret\"\n"
	if err := os.WriteFile(f.providers, []byte(providersTOML), 0o600); err != nil {
		t.Fatalf("write providers.toml: %v", err)
	}
}

func (f *daemonRetirementProcessFixture) newSpawner() *HubSpawner {
	return &HubSpawner{
		Cfg:                 f.cfg,
		EvenerBinary:        f.hubBinary,
		RunDir:              f.runDir,
		HubToken:            "daemon-retirement-fixture-hub-token",
		ProvidersConfigPath: f.providers,
	}
}

// installSeam replaces the Hub's external process-launch seam. The seam sees
// the real resolved argv/env and passes them to the cmd/evener test binary
// unchanged; only the executable invocation differs.
func (f *daemonRetirementProcessFixture) installSeam() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seamInstalled {
		return
	}
	f.previousSeam = daemonProcessCommand
	f.seamInstalled = true
	daemonProcessCommand = f.launchCommand
}

func (f *daemonRetirementProcessFixture) uninstallSeam() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.seamInstalled {
		return
	}
	daemonProcessCommand = f.previousSeam
	f.seamInstalled = false
}

// launchCommand is the fixture's daemonProcessCommand. It creates the control
// pipes and the env file for the child, records the handle and returns the
// helper command. spawnDaemon owns Start/Wait; the fixture owns the other ends.
func (f *daemonRetirementProcessFixture) launchCommand(_ string, args, env []string) *exec.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	ctlR, ctlW, err := os.Pipe()
	if err != nil {
		f.t.Fatalf("daemon retirement fixture: control pipe: %v", err)
	}
	evtR, evtW, err := os.Pipe()
	if err != nil {
		f.t.Fatalf("daemon retirement fixture: event pipe: %v", err)
	}
	f.launchSequence++
	env = f.daemonEnv(env)
	envFile := filepath.Join(f.root, fmt.Sprintf("daemon-env-%d.json", f.launchSequence))
	if err := writeDaemonRetirementEnvFile(envFile, env); err != nil {
		f.t.Fatalf("daemon retirement fixture: env file: %v", err)
	}
	cmd := daemonRetirementHelperCommand(f.helperBinary, args, env, envFile, ctlR, evtW)
	handle := &daemonRetirementProcessHandle{
		cmd:      cmd,
		ctlW:     ctlW,
		evtR:     evtR,
		events:   readDaemonRetirementProcessEvents(evtR),
		childCtl: ctlR,
		childEvt: evtW,
	}
	handle.kill = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
	f.handles = append(f.handles, handle)
	return cmd
}

func (f *daemonRetirementProcessFixture) daemonEnv(env []string) []string {
	return rewriteDaemonRetirementHome(env, f.home, f.configHome, f.stateHome, f.cacheHome)
}

func daemonRetirementHelperCommand(helper string, args, env []string, envFile string, ctlR, evtW *os.File) *exec.Cmd {
	helperArgs := append([]string{"-test.run=^TestDaemonRetirementProcessHelper$"}, args...)
	cmd := exec.Command(helper, helperArgs...)
	cmd.Env = append(append([]string{}, env...),
		daemonRetirementProcessHelperVar+"=1",
		daemonRetirementProcessEnvFile+"="+envFile,
		fmt.Sprintf("%s=%d", daemonRetirementProcessCtlFD, 3),
		fmt.Sprintf("%s=%d", daemonRetirementProcessEvtFD, 4),
	)
	cmd.ExtraFiles = []*os.File{ctlR, evtW}
	return cmd
}

func writeDaemonRetirementEnvFile(path string, env []string) error {
	values := map[string]string{}
	for _, kv := range env {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		values[key] = value
	}
	data, err := json.Marshal(values)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (f *daemonRetirementProcessFixture) launchRequest() hubcore.SpawnRequest {
	return hubcore.SpawnRequest{
		Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{
			Model: "fixture/gpt-test",
		}},
		WorkingDir: f.workDir,
		StateDir:   f.stateDir,
		RunDir:     f.runDir,
		Provider:   "fixture",
	}
}

func (f *daemonRetirementProcessFixture) launchInitial() error {
	before := f.handleCount()
	ctx, cancel := context.WithTimeout(context.Background(), daemonRetirementWatchdog)
	defer cancel()
	entry, err := f.hub.Spawn(ctx, f.launchRequest())
	if err != nil {
		return err
	}
	return f.adoptLaunch(before, entry)
}

// adoptLaunch binds the just-launched handle to the rendezvous entry the Hub
// resolved and closes the child-side pipe ends.
func (f *daemonRetirementProcessFixture) adoptLaunch(before int, entry rendezvous.Entry) error {
	handle, err := f.handleAt(before)
	if err != nil {
		return err
	}
	handle.afterStart()
	handle.entry = entry
	f.entry = entry
	f.sessionID = entry.SessionID
	if f.sessionID == "" {
		return errors.New("launched daemon published no session id")
	}
	return nil
}

func (f *daemonRetirementProcessFixture) handleCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.handles)
}

func (f *daemonRetirementProcessFixture) handleAt(index int) (*daemonRetirementProcessHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index < 0 || index >= len(f.handles) {
		return nil, fmt.Errorf("no daemon handle at index %d (have %d)", index, len(f.handles))
	}
	return f.handles[index], nil
}

func (f *daemonRetirementProcessFixture) currentHandle() (*daemonRetirementProcessHandle, error) {
	return f.handleAt(f.handleCount() - 1)
}

// advance drives the daemon's fake clock and waits for the pipe
// acknowledgement, which carries the post-command armed state.
func (f *daemonRetirementProcessFixture) advance(d time.Duration) error {
	handle, err := f.currentHandle()
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.advanceSeq++
	seq := f.advanceSeq
	f.mu.Unlock()
	if err := writeDaemonRetirementProcessCommand(handle.ctlW, daemonRetirementProcessCommand{
		Cmd: "advance", Nanos: int64(d), Seq: seq,
	}); err != nil {
		return err
	}
	ack, err := handle.events.waitFor(fmt.Sprintf("advance(%s) acknowledgement", d), func(ev daemonRetirementProcessEvent) bool {
		return ev.Kind == "advanced" && ev.Seq == seq
	})
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.lastAdvance = ack
	f.haveAdvance = true
	f.mu.Unlock()
	return nil
}

// advanceFired reports whether the last advance delivered the armed timer, so
// a test can prove the deadline did not move without waiting on wall time.
func (f *daemonRetirementProcessFixture) advanceFired() (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.haveAdvance || f.lastAdvance.Fired == nil {
		return false, errors.New("no advance acknowledgement recorded")
	}
	return *f.lastAdvance.Fired, nil
}

// awaitEvaluation waits for the daemon's real retirement evaluation to arm its
// idle timer and records the effective interval.
func (f *daemonRetirementProcessFixture) awaitEvaluation() error {
	handle, err := f.currentHandle()
	if err != nil {
		return err
	}
	ev, err := handle.events.waitFor("retirement evaluation", func(ev daemonRetirementProcessEvent) bool {
		return ev.Kind == "armed"
	})
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.effectiveArm = time.Duration(ev.Remaining)
	f.mu.Unlock()
	return nil
}

// awaitStartup waits for the daemon's root to be published.
func (f *daemonRetirementProcessFixture) awaitStartup() error {
	handle, err := f.currentHandle()
	if err != nil {
		return err
	}
	_, err = handle.events.waitFor("retirement root publication", func(ev daemonRetirementProcessEvent) bool {
		return ev.Kind == "beat" && ev.Name == "root_published"
	})
	return err
}

// awaitSettled waits until the daemon's own published lifecycle reports a
// settled resident with an armed deadline and no blockers. It is a readiness
// precondition for a test that then advances virtual time: session startup
// work can arm, disarm and re-arm while it settles, and the deadline the test
// means to probe is the one after that churn. The loop condition-watches the
// daemon's published state with the watchdog as a tripwire, never as pacing.
func (f *daemonRetirementProcessFixture) awaitSettled() error {
	deadline := time.Now().Add(daemonRetirementWatchdog)
	var last string
	for {
		if status, err := f.daemonStatus(); err == nil {
			last = fmt.Sprintf("phase=%q deadline=%q blockers=%+v", status.Lifecycle.Phase, status.Lifecycle.Deadline, status.Lifecycle.Blockers)
			if status.Lifecycle.Phase == "resident" && status.Lifecycle.Deadline != "" && len(status.Lifecycle.Blockers) == 0 {
				return nil
			}
		} else {
			last = err.Error()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("daemon never settled: %s", last)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitDaemonExit waits for the current daemon process to exit. The event pipe
// reaches EOF exactly when the process ends, so this is a process-exit wait.
func (f *daemonRetirementProcessFixture) waitDaemonExit() error {
	handle, err := f.currentHandle()
	if err != nil {
		return err
	}
	select {
	case <-handle.events.closed():
		return nil
	case <-time.After(daemonRetirementWatchdog):
		return fmt.Errorf("daemon pid %d never exited; events %+v", handle.entry.PID, handle.events.history())
	}
}

// waitEvent waits for one named retirement beat on the current daemon.
func (f *daemonRetirementProcessFixture) waitEvent(name string) error {
	handle, err := f.currentHandle()
	if err != nil {
		return err
	}
	_, err = handle.events.waitFor("retirement beat "+name, func(ev daemonRetirementProcessEvent) bool {
		return ev.Kind == "beat" && ev.Name == name
	})
	return err
}

// waitKind waits for one event kind on the current daemon.
func (f *daemonRetirementProcessFixture) waitKind(kind string) error {
	handle, err := f.currentHandle()
	if err != nil {
		return err
	}
	_, err = handle.events.waitFor("daemon event "+kind, func(ev daemonRetirementProcessEvent) bool {
		return ev.Kind == kind
	})
	return err
}

// waitTurnSettled releases the turn's held provider call and awaits both the
// real turn-ended settlement event and the durable reflected journal state.
func (f *daemonRetirementProcessFixture) waitTurnSettled(turnID string) error {
	handle, err := f.currentHandle()
	if err != nil {
		return err
	}
	f.mu.Lock()
	mutationID := f.turnMutations[turnID]
	params, ok := f.params[mutationID]
	f.mu.Unlock()
	if !ok {
		return fmt.Errorf("no retained params for turn %q", turnID)
	}
	text := params.Input[0].Text
	ev, err := handle.events.waitFor("scripted provider invocation for "+turnID, func(ev daemonRetirementProcessEvent) bool {
		return ev.Kind == "provider" && strings.TrimSpace(ev.Input) == strings.TrimSpace(text)
	})
	if err != nil {
		return err
	}
	if err := writeDaemonRetirementProcessCommand(handle.ctlW, daemonRetirementProcessCommand{Cmd: "release", Seq: ev.Seq}); err != nil {
		return err
	}
	if err := f.waitKind("settled"); err != nil {
		return err
	}
	return f.awaitReflected(mutationID)
}

// awaitReflected waits for the delivered mutation's journal record to reach
// its durable terminal, reflected state. The wait is on the reflected state
// itself; the settlement event above proves the turn ended.
func (f *daemonRetirementProcessFixture) awaitReflected(mutationID string) error {
	var last string
	deadline := time.Now().Add(daemonRetirementWatchdog)
	for {
		report, err := f.mutationReport(mutationID)
		if err == nil {
			for _, record := range report.Journal {
				if record.ClientMutationID != mutationID {
					continue
				}
				if record.OperationState == "terminal" && len(report.PendingExecutions) == 0 {
					return nil
				}
				last = fmt.Sprintf("%+v pending=%+v", record, report.PendingExecutions)
			}
		} else {
			last = err.Error()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("mutation %q never reached reflected settlement: %s", mutationID, last)
		}
		<-time.After(2 * time.Millisecond)
	}
}

// restartHub rebuilds the Hub over the same private roots and re-runs
// discovery from the rendezvous directory, exactly as a restarted Hub process
// would: a live owner is rediscovered by identity, and an owner that has
// genuinely exited is left for the resume path.
func (f *daemonRetirementProcessFixture) restartHub() error {
	f.hub = f.newSpawner()
	f.entry = rendezvous.Entry{}
	if f.sessionID == "" {
		return errors.New("restartHub before any root identity was known")
	}
	entries, err := rendezvous.List(f.runDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.SessionID == "" {
			continue
		}
		if daemonRetirementProcessAlive(entry.PID) {
			f.entry = entry
			f.sessionID = entry.SessionID
			return nil
		}
	}
	return nil
}

func daemonRetirementProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// ownerLive reports whether the current entry names a live daemon process.
func (f *daemonRetirementProcessFixture) ownerLive() bool {
	if f.entry.SessionID == "" || f.entry.PID == 0 {
		return false
	}
	handle, err := f.currentHandle()
	if err == nil && handle.entry.PID == f.entry.PID && handle.exited() {
		return false
	}
	return daemonRetirementProcessAlive(f.entry.PID)
}

// ensureOwner resolves a live owner: the current daemon if it is still
// running, otherwise the Hub's real resume path.
func (f *daemonRetirementProcessFixture) ensureOwner() error {
	if f.ownerLive() {
		return nil
	}
	before := f.handleCount()
	ctx, cancel := context.WithTimeout(context.Background(), daemonRetirementWatchdog)
	defer cancel()
	entry, err := f.hub.Resume(ctx, hubcore.ResumeRequest{
		SessionID:  f.sessionID,
		WorkingDir: f.workDir,
		StateDir:   f.stateDir,
		RunDir:     f.runDir,
		Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{
			Model: "fixture/gpt-test",
		}},
		Provider: "fixture",
	})
	if err != nil {
		return err
	}
	return f.adoptLaunch(before, entry)
}

// startMutation submits the retained TurnStartParams for id through a real
// AppWire client to the live owner, resolving retirement exactly as the Hub
// does: a retired owner is waited out and resumed, then the original request is
// retried verbatim.
func (f *daemonRetirementProcessFixture) startMutation(id string) (appwire.TurnStartResponse, error) {
	f.mu.Lock()
	if _, ok := f.params[id]; !ok {
		f.params[id] = appwire.TurnStartParams{
			Ref:                "local:" + f.sessionID,
			ClientMutationID:   id,
			ExpectedInstanceID: f.sessionID,
			Input:              []appwire.InputItem{{Type: "text", Text: "daemon-retirement-mutation:" + id}},
		}
	}
	params := f.params[id]
	f.mu.Unlock()

	for range 3 {
		if err := f.ensureOwner(); err != nil {
			return appwire.TurnStartResponse{}, err
		}
		client, err := f.dialDaemon()
		if err != nil {
			return appwire.TurnStartResponse{}, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), daemonRetirementWatchdog)
		response, err := client.TurnStart(ctx, params)
		cancel()
		_ = client.Close()
		if err == nil {
			if response.Turn.ID != "" {
				f.mu.Lock()
				f.turnMutations[response.Turn.ID] = id
				f.mu.Unlock()
			}
			return response, nil
		}
		if isLifecycleRetiringError(err) || isSessionUnavailableError(err) {
			if err := f.waitDaemonExit(); err != nil {
				return appwire.TurnStartResponse{}, err
			}
			f.entry = rendezvous.Entry{}
			f.sessionID = params.ExpectedInstanceID
			continue
		}
		return appwire.TurnStartResponse{}, err
	}
	return appwire.TurnStartResponse{}, fmt.Errorf("mutation %q never reached a live owner", id)
}

func (f *daemonRetirementProcessFixture) dialDaemon() (*appwire.Client, error) {
	header := http.Header{}
	if f.entry.HubToken != "" {
		header.Set("Authorization", "Bearer "+f.entry.HubToken)
	}
	ctx, cancel := context.WithTimeout(context.Background(), daemonRetirementWatchdog)
	defer cancel()
	transport, err := appwire.DialWebSocketWithHeaders(ctx, f.entry.Endpoint, nil, header)
	if err != nil {
		return nil, err
	}
	client := appwire.NewClient(transport)
	client.Start(context.Background())
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

// daemonStatus reads the daemon's own published lifecycle diagnostics.
func (f *daemonRetirementProcessFixture) daemonStatus() (appwire.DaemonStatusResponse, error) {
	client, err := f.dialDaemon()
	if err != nil {
		return appwire.DaemonStatusResponse{}, err
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return client.DaemonStatus(ctx, appwire.DaemonStatusParams{})
}

// mutationReport parses the daemon's durable client-mutation store through the
// production doctor reader.
func (f *daemonRetirementProcessFixture) mutationReport(mutationID string) (doctor.MutationReport, error) {
	_ = mutationID
	return doctor.Mutations(f.stateDir, "local:"+f.sessionID)
}

// transcriptDoc parses the session's primary transcript through the production
// transcript decoders, for the independent original-record oracle.
func (f *daemonRetirementProcessFixture) transcriptTurns() ([]schema.Turn, error) {
	data, err := os.ReadFile(filepath.Join(f.stateDir, "sessions", f.sessionID+".transcript.jsonl"))
	if err != nil {
		return nil, err
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) == 0 {
		return nil, errors.New("empty transcript")
	}
	if _, err := transcript.DecodeHeader(lines[0]); err != nil {
		return nil, err
	}
	turns := make([]schema.Turn, 0, len(lines)-1)
	for _, line := range lines[1:] {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		entry, err := transcript.DecodeEntry(line)
		if err != nil {
			return nil, err
		}
		turns = append(turns, entry.Turn)
	}
	return turns, nil
}

// assertSingleExecution independently reads the original mutation journal and
// transcript through their production parsers and compares them with the
// scripted provider's recorded invocations.
func (f *daemonRetirementProcessFixture) assertSingleExecution(t *testing.T, mutationID, turnID string) {
	t.Helper()
	params, ok := f.params[mutationID]
	if !ok {
		t.Fatalf("no retained original params for mutation %q", mutationID)
	}
	text := params.Input[0].Text

	report, err := doctor.Mutations(f.stateDir, "local:"+f.sessionID)
	if err != nil {
		t.Fatalf("read mutation journal: %v", err)
	}
	var matched *doctor.MutationRecordView
	for i := range report.Journal {
		record := report.Journal[i]
		if record.ClientMutationID != mutationID {
			continue
		}
		matched = &record
		break
	}
	if matched == nil {
		t.Fatalf("mutation %q absent from the journal: %+v", mutationID, report.Journal)
	}
	if matched.StableTurnID != turnID || turnID == "" {
		t.Fatalf("journal stable turn = %q, want %q", matched.StableTurnID, turnID)
	}
	if matched.OperationState != "terminal" || matched.ExecutionState == "rejected" {
		t.Fatalf("journal record = %+v, want a terminal accepted execution", matched)
	}
	for _, pending := range report.PendingExecutions {
		if pending.ClientMutationID == mutationID {
			t.Fatalf("mutation %q still has a pending execution after settlement: %+v", mutationID, pending)
		}
	}

	turns, err := f.transcriptTurns()
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	var userInputs []schema.Turn
	for _, turn := range turns {
		if turn.Kind != schema.TurnUserInput {
			continue
		}
		if turn.ClientMutationID != mutationID {
			continue
		}
		userInputs = append(userInputs, turn)
	}
	if len(userInputs) != 1 {
		t.Fatalf("transcript user inputs for %q = %d, want exactly 1", mutationID, len(userInputs))
	}
	input := userInputs[0]
	if input.StableTurnID != turnID {
		t.Fatalf("transcript stable turn = %q, want %q", input.StableTurnID, turnID)
	}
	if got := strings.TrimSpace(input.Message.Text()); got != strings.TrimSpace(text) {
		t.Fatalf("transcript input = %q, want the retained original %q", got, text)
	}

	// The fixture IPC provider invocations are the execution oracle: exactly one
	// non-naming invocation carried this input. Naming work never reports.
	handle, err := f.currentHandle()
	if err != nil {
		t.Fatalf("current daemon handle: %v", err)
	}
	executions := 0
	for _, ev := range handle.events.history() {
		if ev.Kind != "provider" {
			continue
		}
		if strings.TrimSpace(ev.Input) == strings.TrimSpace(text) {
			executions++
		}
	}
	if executions != 1 {
		t.Fatalf("scripted provider executions for %q = %d, want exactly 1; events %+v", text, executions, handle.events.history())
	}
}

// cleanup closes every fixture-owned process and pipe exactly, and restores
// the launch seam. It never scans for processes by name.
func (f *daemonRetirementProcessFixture) cleanup() error {
	f.uninstallSeam()
	f.mu.Lock()
	handles := append([]*daemonRetirementProcessHandle(nil), f.handles...)
	f.mu.Unlock()
	var errs []error
	for _, handle := range handles {
		if !handle.exited() && handle.kill != nil {
			if err := handle.kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				errs = append(errs, fmt.Errorf("kill daemon pid %d: %w", handle.entry.PID, err))
			}
		}
		if handle.started && !handle.exited() {
			select {
			case <-handle.events.closed():
			case <-time.After(30 * time.Second):
				errs = append(errs, fmt.Errorf("daemon pid %d did not exit during cleanup", handle.entry.PID))
			}
		}
		handle.close()
	}
	return errors.Join(errs...)
}

func writeDaemonRetirementProcessCommand(w *os.File, cmd daemonRetirementProcessCommand) error {
	if w == nil {
		return errors.New("daemon retirement fixture: control pipe is closed")
	}
	data, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}

// --- fixture variants used by the additional tests -------------------------

// newDaemonRetirementProcessFixtureNoInitialLaunch builds the fixture roots,
// the Hub spawner and the launch seam without launching a daemon, for tests
// that launch their own (a direct serve, or a daemon whose launcher exits).
func newDaemonRetirementProcessFixtureNoInitialLaunch(t *testing.T, cfg Config) *daemonRetirementProcessFixture {
	t.Helper()
	f := &daemonRetirementProcessFixture{
		t:             t,
		cfg:           cfg,
		params:        map[string]appwire.TurnStartParams{},
		turnMutations: map[string]string{},
	}
	f.setupRoots(t)
	f.installSeam()
	f.hub = f.newSpawner()
	t.Cleanup(func() {
		if err := f.cleanup(); err != nil {
			t.Error(err)
		}
	})
	return f
}

// startDirectServe launches the real serve lifecycle in the helper process
// directly — no Hub — so an explicit --daemon-idle-timeout can be observed.
func (f *daemonRetirementProcessFixture) startDirectServe(t *testing.T, timeoutArgs ...string) (*daemonRetirementProcessHandle, error) {
	t.Helper()
	ctlR, ctlW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	evtR, evtW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.launchSequence++
	sequence := f.launchSequence
	f.mu.Unlock()
	env := f.daemonEnv(os.Environ())
	envFile := filepath.Join(f.root, fmt.Sprintf("daemon-env-%d.json", sequence))
	if err := writeDaemonRetirementEnvFile(envFile, env); err != nil {
		return nil, err
	}
	args := []string{
		"serve",
		"--addr", "127.0.0.1:0",
		"--dir", f.workDir,
		"--state-dir", f.stateDir,
		"--run-dir", f.runDir,
		"--model", "openai/gpt-test",
	}
	args = append(args, timeoutArgs...)
	cmd := daemonRetirementHelperCommand(f.helperBinary, args, env, envFile, ctlR, evtW)
	handle := &daemonRetirementProcessHandle{
		cmd:      cmd,
		ctlW:     ctlW,
		evtR:     evtR,
		events:   readDaemonRetirementProcessEvents(evtR),
		childCtl: ctlR,
		childEvt: evtW,
		waitErr:  make(chan error, 1),
	}
	handle.kill = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	handle.afterStart()
	go func() { handle.waitErr <- cmd.Wait() }()
	ev, err := handle.events.waitFor("direct serve rendezvous registration", func(ev daemonRetirementProcessEvent) bool {
		return ev.Kind == "registered" && ev.Entry != nil
	})
	if err != nil {
		return nil, err
	}
	handle.entry = *ev.Entry
	f.mu.Lock()
	f.handles = append(f.handles, handle)
	f.entry = *ev.Entry
	f.sessionID = ev.Entry.SessionID
	f.mu.Unlock()
	return handle, nil
}

// directExitCode waits for a direct serve helper to exit and reports its code.
func (f *daemonRetirementProcessFixture) directExitCode(handle *daemonRetirementProcessHandle) (int, error) {
	if handle.waitErr == nil {
		return -1, errors.New("handle was not started by the fixture")
	}
	select {
	case err := <-handle.waitErr:
		if err == nil {
			return 0, nil
		}
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			return exitErr.ExitCode(), nil
		}
		return -1, err
	case <-time.After(daemonRetirementWatchdog):
		return -1, fmt.Errorf("direct serve pid %d never exited", handle.entry.PID)
	}
}

// armedEvents returns every arm/disarm event of the current daemon.
func (f *daemonRetirementProcessFixture) armedEvents() ([]daemonRetirementProcessEvent, error) {
	handle, err := f.currentHandle()
	if err != nil {
		return nil, err
	}
	var out []daemonRetirementProcessEvent
	for _, ev := range handle.events.history() {
		if ev.Kind == "armed" || ev.Kind == "disarmed" {
			out = append(out, ev)
		}
	}
	return out, nil
}

// assertNoClaim proves the daemon consumed no retirement claim, and is still
// resident, without waiting on a clock.
func (f *daemonRetirementProcessFixture) assertNoClaim() error {
	handle, err := f.currentHandle()
	if err != nil {
		return err
	}
	if handle.exited() {
		return errors.New("daemon exited without a retirement claim")
	}
	for _, ev := range handle.events.history() {
		if ev.Kind == "beat" && ev.Name == "claim_consumed" {
			return errors.New("daemon consumed a retirement claim")
		}
	}
	return nil
}

// --- direct process lifecycle tests ----------------------------------------

// TestDaemonRetirementProcessDirectZeroStaysResident proves an explicit
// `--daemon-idle-timeout 0s` on a directly-launched serve arms no timer and
// stays resident across arbitrary fake advancement.
func TestDaemonRetirementProcessDirectZeroStaysResident(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a real daemon")
	}
	f := newDaemonRetirementProcessFixtureNoInitialLaunch(t, DefaultConfig())
	if _, err := f.startDirectServe(t, "--daemon-idle-timeout", "0s"); err != nil {
		t.Fatal(err)
	}
	if err := f.awaitStartup(); err != nil {
		t.Fatal(err)
	}
	if arms, err := f.armedEvents(); err != nil {
		t.Fatal(err)
	} else if len(arms) != 0 {
		t.Fatalf("explicit 0s armed a retirement timer: %+v", arms)
	}
	if err := f.advance(72 * time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := f.assertNoClaim(); err != nil {
		t.Fatal(err)
	}
	status, err := f.daemonStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.Lifecycle.TimeoutMillis != 0 {
		t.Fatalf("daemon diagnostics TimeoutMillis = %d, want 0 (disabled)", status.Lifecycle.TimeoutMillis)
	}
}

// TestDaemonRetirementProcessHubZeroStaysResident proves the same for a
// Hub-configured `daemon_idle_timeout = "0s"`: the spawned daemon arms no timer
// and remains resident.
func TestDaemonRetirementProcessHubZeroStaysResident(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a real hub + daemon")
	}
	cfg := DefaultConfig()
	cfg.DaemonIdleTimeout = 0
	f := newDaemonRetirementProcessFixtureNoInitialLaunch(t, cfg)
	if err := f.launchInitial(); err != nil {
		t.Fatal(err)
	}
	if err := f.awaitStartup(); err != nil {
		t.Fatal(err)
	}
	if arms, err := f.armedEvents(); err != nil {
		t.Fatal(err)
	} else if len(arms) != 0 {
		t.Fatalf("Hub 0s armed a retirement timer: %+v", arms)
	}
	if err := f.advance(72 * time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := f.assertNoClaim(); err != nil {
		t.Fatal(err)
	}
	status, err := f.daemonStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.Lifecycle.TimeoutMillis != 0 {
		t.Fatalf("daemon diagnostics TimeoutMillis = %d, want 0 (disabled)", status.Lifecycle.TimeoutMillis)
	}
}

// TestDaemonRetirementProcessDirectPositiveExits proves a positive direct
// configuration exits the real helper process with code zero and leaves no
// terminal lifecycle record.
func TestDaemonRetirementProcessDirectPositiveExits(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a real daemon")
	}
	f := newDaemonRetirementProcessFixtureNoInitialLaunch(t, DefaultConfig())
	handle, err := f.startDirectServe(t, "--daemon-idle-timeout", "1h")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.awaitEvaluation(); err != nil {
		t.Fatal(err)
	}
	if err := f.awaitSettled(); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	effective := f.effectiveArm
	f.mu.Unlock()
	if effective != time.Hour {
		t.Fatalf("direct serve armed %v, want the explicit 1h", effective)
	}
	transcriptPath := filepath.Join(f.stateDir, "sessions", f.sessionID+".transcript.jsonl")
	before, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.advance(time.Hour); err != nil {
		t.Fatal(err)
	}
	code, err := f.directExitCode(handle)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("retired helper process exit code = %d, want 0", code)
	}
	if _, err := schema.LoadSessionMeta(f.stateDir, f.sessionID); err != nil {
		t.Fatalf("retirement left the session unrestorable: %v", err)
	}
	after, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("retirement appended terminal lifecycle evidence to the transcript:\n%s", after)
	}
}

// TestDaemonRetirementProcessAdmittedWorkResetsInterval proves an admitted turn
// resets the complete interval: the post-settlement arm is the full timeout,
// not the remainder.
func TestDaemonRetirementProcessAdmittedWorkResetsInterval(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a real hub + daemon")
	}
	f := newDaemonRetirementProcessFixture(t)
	if err := f.awaitEvaluation(); err != nil {
		t.Fatal(err)
	}
	if err := f.awaitSettled(); err != nil {
		t.Fatal(err)
	}
	interval := f.cfg.DaemonIdleTimeout
	if err := f.advance(interval / 2); err != nil {
		t.Fatal(err)
	}
	response, err := f.startMutation("retirement-process-reset")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.waitKind("disarmed"); err != nil {
		t.Fatalf("admitted work never disarmed the idle timer: %v", err)
	}
	if err := f.waitTurnSettled(response.Turn.ID); err != nil {
		t.Fatal(err)
	}
	// The re-arm is an event: wait for the arm that follows the disarmed timer
	// rather than racing the session's lease release.
	rearm, err := f.waitArmAfterDisarm()
	if err != nil {
		t.Fatal(err)
	}
	if rearm != interval {
		t.Fatalf("post-settlement arm = %v, want the complete interval %v", rearm, interval)
	}
	if err := f.advance(interval / 2); err != nil {
		t.Fatal(err)
	}
	if fired, err := f.advanceFired(); err != nil {
		t.Fatal(err)
	} else if fired {
		t.Fatal("the reset interval expired before its complete interval elapsed")
	}
	if err := f.assertNoClaim(); err != nil {
		t.Fatalf("the reset interval did not start at settlement: %v", err)
	}
	if err := f.advance(interval / 2); err != nil {
		t.Fatal(err)
	}
	if err := f.waitEvent("claim_consumed"); err != nil {
		t.Fatalf("the reset interval never expired: %v", err)
	}
}

// waitArmAfterDisarm waits for the first arm that follows the most recent
// disarmed timer, and reports its interval.
func (f *daemonRetirementProcessFixture) waitArmAfterDisarm() (time.Duration, error) {
	handle, err := f.currentHandle()
	if err != nil {
		return 0, err
	}
	history := handle.events.history()
	disarmAt := -1
	for i, ev := range history {
		if ev.Kind == "disarmed" {
			disarmAt = i
		}
	}
	if disarmAt < 0 {
		return 0, errors.New("no disarmed timer to follow")
	}
	ev, err := handle.events.waitForIndexed("re-arm after admitted work", func(i int, ev daemonRetirementProcessEvent) bool {
		return i > disarmAt && ev.Kind == "armed"
	})
	if err != nil {
		return 0, err
	}
	return time.Duration(ev.Remaining), nil
}

// TestDaemonRetirementProcessReadsDoNotResetInterval proves reads, probes and
// subscriptions do not reset the idle interval: the original deadline still
// fires exactly one full interval after the initial evaluation.
func TestDaemonRetirementProcessReadsDoNotResetInterval(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a real hub + daemon")
	}
	f := newDaemonRetirementProcessFixture(t)
	if err := f.awaitEvaluation(); err != nil {
		t.Fatal(err)
	}
	if err := f.awaitSettled(); err != nil {
		t.Fatal(err)
	}
	client, err := f.dialDaemon()
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	interval := f.cfg.DaemonIdleTimeout
	if err := f.advance(interval / 2); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for i := range 3 {
		if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:" + f.sessionID}); err != nil {
			t.Fatalf("thread/read %d: %v", i, err)
		}
		if _, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:" + f.sessionID, Subscribe: true}); err != nil {
			t.Fatalf("subscribed thread/read %d: %v", i, err)
		}
		if _, err := client.DaemonStatus(ctx, appwire.DaemonStatusParams{}); err != nil {
			t.Fatalf("daemon/status %d: %v", i, err)
		}
	}
	arms, err := f.armedEvents()
	if err != nil {
		t.Fatal(err)
	}
	// Reads borrow runtime resources but take no mutation lease, so they do not
	// move the deadline. Startup churn can arm and disarm while the session
	// settles, so the decisive evidence is the deadline itself: the advance to
	// the original deadline must deliver the timer. A read that reset the
	// interval would have pushed the deadline past this point.
	if err := f.advance(interval / 2); err != nil {
		t.Fatal(err)
	}
	if fired, err := f.advanceFired(); err != nil {
		t.Fatal(err)
	} else if !fired {
		t.Fatalf("reads moved the idle deadline: the original deadline did not expire; arms=%+v", arms)
	}
	if err := f.waitEvent("claim_consumed"); err != nil {
		t.Fatalf("reads reset the idle deadline: %v", err)
	}
}

// TestDaemonRetirementProcessRestartedHubRediscoversOrResumes proves a
// restarted Hub rediscovers the live owner by identity and, once that owner has
// genuinely exited, resumes the offline root.
func TestDaemonRetirementProcessRestartedHubRediscoversOrResumes(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a real hub + daemon")
	}
	f := newDaemonRetirementProcessFixture(t)
	original := f.entry
	if err := f.restartHub(); err != nil {
		t.Fatal(err)
	}
	if f.entry.SessionID != original.SessionID || f.entry.PID != original.PID {
		t.Fatalf("restarted Hub did not rediscover the live owner: %+v, want %+v", f.entry, original)
	}
	if err := f.awaitEvaluation(); err != nil {
		t.Fatal(err)
	}
	if err := f.awaitSettled(); err != nil {
		t.Fatal(err)
	}
	if err := f.advance(f.cfg.DaemonIdleTimeout); err != nil {
		t.Fatal(err)
	}
	if err := f.waitDaemonExit(); err != nil {
		t.Fatal(err)
	}
	if err := f.restartHub(); err != nil {
		t.Fatal(err)
	}
	if f.entry.SessionID != "" {
		t.Fatalf("restarted Hub rediscovered a confirmed-dead owner: %+v", f.entry)
	}
	response, err := f.startMutation("restart-resume")
	if err != nil {
		t.Fatal(err)
	}
	if response.Receipt.Disposition != appwire.MutationDispositionApplied || response.Turn.ID == "" {
		t.Fatalf("resumed-offline-root mutation = %+v, want one applied turn", response)
	}
	if f.entry.SessionID != original.SessionID {
		t.Fatalf("resumed root identity = %q, want %q", f.entry.SessionID, original.SessionID)
	}
	if f.entry.PID == original.PID {
		t.Fatalf("offline root was not resumed by a new process (pid %d)", original.PID)
	}
	if err := f.waitTurnSettled(response.Turn.ID); err != nil {
		t.Fatal(err)
	}
}

// TestDaemonRetirementProcessStaleRendezvousCleanupKeepsReplacement proves a
// stale incarnation's cleanup cannot remove the live replacement's rendezvous
// record: the production RemoveIfOwned re-reads under the ownership lock and
// refuses a mismatched identity.
func TestDaemonRetirementProcessStaleRendezvousCleanupKeepsReplacement(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a real hub + daemon")
	}
	f := newDaemonRetirementProcessFixture(t)
	if err := f.awaitEvaluation(); err != nil {
		t.Fatal(err)
	}
	if err := f.awaitSettled(); err != nil {
		t.Fatal(err)
	}
	if err := f.advance(f.cfg.DaemonIdleTimeout); err != nil {
		t.Fatal(err)
	}
	if err := f.waitDaemonExit(); err != nil {
		t.Fatal(err)
	}
	// Force the Hub's resume path so a real replacement publishes a record.
	f.entry = rendezvous.Entry{}
	response, err := f.startMutation("stale-cleanup")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.waitTurnSettled(response.Turn.ID); err != nil {
		t.Fatal(err)
	}
	replacement := f.entry
	if replacement.PID == 0 || replacement.SessionID == "" {
		t.Fatalf("replacement did not publish a rendezvous record: %+v", replacement)
	}
	stale := replacement
	stale.StartedAt = stale.StartedAt.Add(-time.Hour)
	stale.Endpoint = "ws://127.0.0.1:1/rpc"
	if err := rendezvous.RemoveIfOwned(f.runDir, stale); err == nil {
		t.Fatal("a stale incarnation's cleanup removed the live replacement's rendezvous record")
	}
	entries, err := rendezvous.List(f.runDir)
	if err != nil {
		t.Fatal(err)
	}
	var found *rendezvous.Entry
	for i := range entries {
		if entries[i].PID == replacement.PID {
			found = &entries[i]
		}
	}
	if found == nil {
		t.Fatalf("replacement record for pid %d is gone from %s: %+v", replacement.PID, f.runDir, entries)
	}
	if found.Endpoint != replacement.Endpoint || found.SessionID != replacement.SessionID || !found.StartedAt.Equal(replacement.StartedAt) {
		t.Fatalf("replacement record was rewritten by stale cleanup: %+v, want %+v", *found, replacement)
	}
	if !daemonRetirementProcessAlive(replacement.PID) {
		t.Fatalf("replacement process pid %d is gone", replacement.PID)
	}
}

// TestDaemonRetirementProcessDetachedProcessSurvives proves retirement is a
// per-process decision: an unrelated fixture-owned detached process survives
// it and is released only through its own pipe handle.
func TestDaemonRetirementProcessDetachedProcessSurvives(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a real hub + daemon")
	}
	f := newDaemonRetirementProcessFixture(t)
	detached, release := f.startDetachedProcess(t)
	if err := f.awaitEvaluation(); err != nil {
		t.Fatal(err)
	}
	if err := f.awaitSettled(); err != nil {
		t.Fatal(err)
	}
	if err := f.advance(f.cfg.DaemonIdleTimeout); err != nil {
		t.Fatal(err)
	}
	if err := f.waitDaemonExit(); err != nil {
		t.Fatal(err)
	}
	if err := detached.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("fixture-owned detached process pid %d did not survive retirement: %v", detached.Process.Pid, err)
	}
	if err := release.Close(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- detached.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("detached helper exit: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("detached helper did not exit after its release handle closed")
	}
}

func (f *daemonRetirementProcessFixture) startDetachedProcess(t *testing.T) (*exec.Cmd, *os.File) {
	t.Helper()
	releaseR, releaseW, err := os.Pipe()
	if err != nil {
		t.Fatalf("detached process pipe: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^TestDaemonRetirementDetachedFixtureProcess$")
	cmd.Env = append(os.Environ(), daemonRetirementDetachedHelperVar+"=1")
	cmd.ExtraFiles = []*os.File{releaseR}
	cmd.SysProcAttr = daemonSysProcAttr()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start detached fixture process: %v", err)
	}
	_ = releaseR.Close()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd, releaseW
}

// --- a Hub process that launches a daemon and then exits -------------------

type daemonRetirementHubLaunchRequest struct {
	HelperBinary string `json:"helper_binary"`
	HubBinary    string `json:"hub_binary"`
	Providers    string `json:"providers"`
	RunDir       string `json:"run_dir"`
	StateDir     string `json:"state_dir"`
	WorkDir      string `json:"work_dir"`
	Home         string `json:"home"`
	ConfigHome   string `json:"config_home"`
	StateHome    string `json:"state_home"`
	CacheHome    string `json:"cache_home"`
	EnvFile      string `json:"env_file"`
	ResultPath   string `json:"result_path"`
	Model        string `json:"model"`
	TimeoutNanos int64  `json:"timeout_nanos"`
}

type daemonRetirementHubLaunchResult struct {
	Entry rendezvous.Entry `json:"entry"`
	Err   string           `json:"err,omitempty"`
}

// TestDaemonRetirementProcessHubExitStillRetires proves the daemon owns its
// retirement: a Hub process launches it, exits, and the daemon still retires on
// its own clock.
func TestDaemonRetirementProcessHubExitStillRetires(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a real hub + daemon")
	}
	f := newDaemonRetirementProcessFixtureNoInitialLaunch(t, DefaultConfig())
	if err := f.spawnFromStandaloneHubProcess(t); err != nil {
		t.Fatal(err)
	}
	if err := f.awaitEvaluation(); err != nil {
		t.Fatal(err)
	}
	if err := f.awaitSettled(); err != nil {
		t.Fatal(err)
	}
	if err := f.advance(f.cfg.DaemonIdleTimeout); err != nil {
		t.Fatal(err)
	}
	if err := f.waitDaemonExit(); err != nil {
		t.Fatalf("daemon did not retire after its Hub process exited: %v", err)
	}
}

func (f *daemonRetirementProcessFixture) spawnFromStandaloneHubProcess(t *testing.T) error {
	t.Helper()
	ctlR, ctlW, err := os.Pipe()
	if err != nil {
		return err
	}
	evtR, evtW, err := os.Pipe()
	if err != nil {
		return err
	}
	req := daemonRetirementHubLaunchRequest{
		HelperBinary: f.helperBinary,
		HubBinary:    f.hubBinary,
		Providers:    f.providers,
		RunDir:       f.runDir,
		StateDir:     f.stateDir,
		WorkDir:      f.workDir,
		Home:         f.home,
		ConfigHome:   f.configHome,
		StateHome:    f.stateHome,
		CacheHome:    f.cacheHome,
		EnvFile:      filepath.Join(f.root, "hub-launch-env.json"),
		ResultPath:   filepath.Join(f.root, "hub-launch-result.json"),
		Model:        "fixture/gpt-test",
		TimeoutNanos: int64(f.cfg.DaemonIdleTimeout),
	}
	reqPath := filepath.Join(f.root, "hub-launch-request.json")
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if err := os.WriteFile(reqPath, data, 0o600); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "-test.run=^TestDaemonRetirementHubLaunchHelper$")
	cmd.Env = append(os.Environ(),
		daemonRetirementHubLaunchHelperVar+"=1",
		daemonRetirementHubLaunchRequestVar+"="+reqPath,
	)
	cmd.ExtraFiles = []*os.File{ctlR, evtW}
	logFile := filepath.Join(f.root, "hub-launch-helper.log")
	log, err := os.Create(logFile)
	if err != nil {
		return err
	}
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		return err
	}
	_ = ctlR.Close()
	_ = evtW.Close()
	// Wait for the Hub process to genuinely exit before the daemon clock moves.
	waitErr := cmd.Wait()
	_ = log.Close()
	if waitErr != nil {
		body, _ := os.ReadFile(logFile)
		return fmt.Errorf("standalone Hub launch helper: %w\n%s", waitErr, body)
	}
	resultData, err := os.ReadFile(req.ResultPath)
	if err != nil {
		body, _ := os.ReadFile(logFile)
		return fmt.Errorf("standalone Hub launch result: %w\n%s", err, body)
	}
	var result daemonRetirementHubLaunchResult
	if err := json.Unmarshal(resultData, &result); err != nil {
		return err
	}
	if result.Err != "" {
		return errors.New(result.Err)
	}
	if result.Entry.PID == 0 || result.Entry.SessionID == "" {
		return fmt.Errorf("standalone Hub launch returned no live entry: %+v", result.Entry)
	}
	handle := &daemonRetirementProcessHandle{
		pid:     result.Entry.PID,
		ctlW:    ctlW,
		evtR:    evtR,
		events:  readDaemonRetirementProcessEvents(evtR),
		started: true,
		entry:   result.Entry,
	}
	handle.kill = func() error { return daemonRetirementKillPID(result.Entry.PID) }
	f.mu.Lock()
	f.handles = append(f.handles, handle)
	f.entry = result.Entry
	f.sessionID = result.Entry.SessionID
	f.mu.Unlock()
	return nil
}

func daemonRetirementKillPID(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGKILL)
}

// TestDaemonRetirementHubLaunchHelper is the re-executed hub-side half: it
// constructs the real HubSpawner with the launch seam pointed at the cmd/evener
// helper, launches the daemon, writes the rendezvous entry back, and exits —
// taking the Hub process with it.
func TestDaemonRetirementHubLaunchHelper(t *testing.T) {
	if os.Getenv(daemonRetirementHubLaunchHelperVar) == "" {
		t.Skip("re-executed hub-side helper for TestDaemonRetirementProcessHubExitStillRetires")
	}
	raw, err := os.ReadFile(os.Getenv(daemonRetirementHubLaunchRequestVar))
	if err != nil {
		t.Fatalf("read hub launch request: %v", err)
	}
	var req daemonRetirementHubLaunchRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("decode hub launch request: %v", err)
	}
	previous := daemonProcessCommand
	daemonProcessCommand = func(_ string, args, env []string) *exec.Cmd {
		env = rewriteDaemonRetirementHome(env, req.Home, req.ConfigHome, req.StateHome, req.CacheHome)
		if err := writeDaemonRetirementEnvFile(req.EnvFile, env); err != nil {
			t.Errorf("write child env file: %v", err)
		}
		return daemonRetirementHelperCommand(req.HelperBinary, args, env, req.EnvFile,
			os.NewFile(3, "daemon-retirement-ctl"), os.NewFile(4, "daemon-retirement-evt"))
	}
	defer func() { daemonProcessCommand = previous }()

	cfg := DefaultConfig()
	cfg.DaemonIdleTimeout = time.Duration(req.TimeoutNanos)
	spawner := &HubSpawner{
		Cfg:                 cfg,
		EvenerBinary:        req.HubBinary,
		RunDir:              req.RunDir,
		HubToken:            "daemon-retirement-fixture-hub-token",
		ProvidersConfigPath: req.Providers,
	}
	ctx, cancel := context.WithTimeout(context.Background(), daemonRetirementWatchdog)
	defer cancel()
	entry, spawnErr := spawner.Spawn(ctx, hubcore.SpawnRequest{
		Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{
			Model: req.Model,
		}},
		WorkingDir: req.WorkDir,
		StateDir:   req.StateDir,
		RunDir:     req.RunDir,
		Provider:   "fixture",
	})
	result := daemonRetirementHubLaunchResult{Entry: entry}
	if spawnErr != nil {
		result.Err = spawnErr.Error()
	}
	data, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatalf("encode hub launch result: %v", marshalErr)
	}
	if err := os.WriteFile(req.ResultPath, data, 0o600); err != nil {
		t.Fatalf("write hub launch result: %v", err)
	}
	if spawnErr != nil {
		t.Fatalf("standalone Hub spawn: %v", spawnErr)
	}
}

// TestDaemonRetirementDetachedFixtureProcess is the fixture-owned detached
// process: it blocks on its inherited pipe until the fixture closes it.
func TestDaemonRetirementDetachedFixtureProcess(t *testing.T) {
	if os.Getenv(daemonRetirementDetachedHelperVar) == "" {
		t.Skip("fixture-owned detached process helper")
	}
	reader := os.NewFile(3, "detached-release")
	if reader == nil {
		t.Fatal("detached helper: inherited release pipe missing")
	}
	buf := make([]byte, 1)
	_, _ = reader.Read(buf)
}

func rewriteDaemonRetirementHome(env []string, home, configHome, stateHome, cacheHome string) []string {
	out := make([]string, 0, len(env)+4)
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		switch key {
		case "HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME":
			continue
		}
		out = append(out, kv)
	}
	return append(out,
		"HOME="+home,
		"XDG_CONFIG_HOME="+configHome,
		"XDG_STATE_HOME="+stateHome,
		"XDG_CACHE_HOME="+cacheHome,
	)
}

// --- Step 1: the smallest actual process expiry test -----------------------

// TestDaemonRetirementHubLaunchExpiresAndResumes is the plan's end-to-end
// process test: a real Hub-launched daemon expires on the Hub's default idle
// deadline, the Hub resumes the offline root, and a mutation submitted after
// resume settles exactly once with the original replay identity.
func TestDaemonRetirementHubLaunchExpiresAndResumes(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs a real hub + daemon")
	}
	f := newDaemonRetirementProcessFixture(t)
	original := f.entry.SessionID
	firstPID := f.entry.PID
	if f.cfg.DaemonIdleTimeout != DefaultConfig().DaemonIdleTimeout {
		t.Fatalf("fixture Hub timeout = %v, want the Hub default %v", f.cfg.DaemonIdleTimeout, DefaultConfig().DaemonIdleTimeout)
	}
	if err := f.awaitEvaluation(); err != nil {
		t.Fatal(err)
	}
	if err := f.awaitSettled(); err != nil {
		t.Fatal(err)
	}
	// The daemon's effective interval comes from the Hub's default config, not
	// a per-test hard-coded controller.
	f.mu.Lock()
	effective := f.effectiveArm
	f.mu.Unlock()
	if effective != f.cfg.DaemonIdleTimeout {
		t.Fatalf("daemon armed %v, want the Hub default %v", effective, f.cfg.DaemonIdleTimeout)
	}
	status, err := f.daemonStatus()
	if err != nil {
		t.Fatalf("daemon status before retirement: %v", err)
	}
	if status.Lifecycle.TimeoutMillis != f.cfg.DaemonIdleTimeout.Milliseconds() {
		t.Fatalf("daemon diagnostics TimeoutMillis = %d, want the Hub default %d", status.Lifecycle.TimeoutMillis, f.cfg.DaemonIdleTimeout.Milliseconds())
	}
	if err := f.advance(time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := f.waitDaemonExit(); err != nil {
		t.Fatal(err)
	}
	if _, err := schema.LoadSessionMeta(f.stateDir, original); err != nil {
		t.Fatal(err)
	}
	response, err := f.startMutation("retirement-process-resume")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.waitTurnSettled(response.Turn.ID); err != nil {
		t.Fatal(err)
	}
	replay, err := f.startMutation("retirement-process-resume")
	if err != nil {
		t.Fatal(err)
	}
	if response.Receipt.Disposition != appwire.MutationDispositionApplied ||
		replay.Receipt.Disposition != appwire.MutationDispositionReplayed ||
		response.Turn.ID == "" || replay.Turn.ID != response.Turn.ID ||
		response.Receipt.TurnID != response.Turn.ID || replay.Receipt.TurnID != response.Turn.ID ||
		response.Receipt.ProjectionState != appwire.MutationProjectionPending ||
		replay.Receipt.ProjectionState != appwire.MutationProjectionReflected {
		t.Fatalf("resume identity/receipt/projection: %#v %#v", response, replay)
	}
	if f.entry.SessionID != original {
		t.Fatal("replacement changed saved root identity")
	}
	if f.entry.PID == firstPID {
		t.Fatalf("resume reused the retired daemon pid %d", firstPID)
	}
	f.assertSingleExecution(t, "retirement-process-resume", response.Turn.ID)
}
