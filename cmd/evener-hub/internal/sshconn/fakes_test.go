package sshconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// fakeRunner records every call and delegates to injected functions. Production
// execRunner is deliberately excluded from the default unit tests.
type fakeRunner struct {
	mu               sync.Mutex
	runs             [][]string
	starts           [][]string
	protocolRefusals int
	runFn            func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error)
	startFn          func(ctx context.Context, argv []string, stderr io.Writer) (Stdio, error)
}

func (f *fakeRunner) Run(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
	f.mu.Lock()
	f.runs = append(f.runs, append([]string(nil), argv...))
	f.mu.Unlock()
	if f.runFn == nil {
		return nil, fmt.Errorf("fakeRunner: unexpected Run %v", argv)
	}
	return f.runFn(ctx, argv, stdin)
}

func (f *fakeRunner) Start(ctx context.Context, argv []string, stderr io.Writer) (Stdio, error) {
	f.mu.Lock()
	f.starts = append(f.starts, append([]string(nil), argv...))
	f.mu.Unlock()
	if f.startFn == nil {
		return nil, errors.New("fakeRunner: no startFn")
	}
	return f.startFn(ctx, argv, stderr)
}

func (f *fakeRunner) recordedRuns() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.runs...)
}

func (f *fakeRunner) recordedStarts() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.starts...)
}

// requireProtocolUpgrade makes the remote process reject initialize until a
// successful binary push or installer invocation. Deployment fixtures use this
// positive live evidence rather than a difference in build labels.
func requireProtocolUpgrade(t *testing.T, f *fakeRunner) {
	t.Helper()
	var installed atomic.Bool
	run, start := f.runFn, f.startFn
	f.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		out, err := run(ctx, argv, stdin)
		if err == nil && classifyRemote(argv) == opBinaryInstall {
			installed.Store(true)
		}
		return out, err
	}
	f.startFn = func(ctx context.Context, argv []string, stderr io.Writer) (Stdio, error) {
		if installed.Load() {
			return start(ctx, argv, stderr)
		}
		f.mu.Lock()
		f.protocolRefusals++
		f.mu.Unlock()
		b := newFakeBridge("evener-appwire-v5")
		t.Cleanup(func() { _ = b.stdio.Kill() })
		return b.stdio, nil
	}
}

// fakeStdio is an in-memory Stdio. Stdout is fed by a fakeBridge server; Stdin
// is drained by it.
type fakeStdio struct {
	inW  *io.PipeWriter
	outR *io.PipeReader
	inR  *io.PipeReader
	outW *io.PipeWriter

	// waitErr is what Wait reports, standing in for the child's exit status: a
	// *exec.ExitError with ssh's own 255 is what makes an attach handshake's auth
	// refusal terminal (see exitStatus).
	waitErr  error
	killOnce sync.Once
	waitDone chan struct{}
}

func (s *fakeStdio) Stdin() io.WriteCloser { return s.inW }
func (s *fakeStdio) Stdout() io.ReadCloser { return s.outR }

func (s *fakeStdio) Kill() error {
	s.killOnce.Do(func() {
		_ = s.inW.Close()
		_ = s.outR.Close()
		_ = s.outW.Close()
		_ = s.inR.Close()
		close(s.waitDone)
	})
	return nil
}

func (s *fakeStdio) Wait() error {
	<-s.waitDone
	return s.waitErr
}

// drop simulates the SSH link dying without a deliberate Close: the server stops
// writing, so the controller's read loop sees EOF.
func (s *fakeStdio) drop() {
	_ = s.outW.Close()
	_ = s.inR.Close()
}

// fakeBridge is a minimal in-process AppWire server behind a fakeStdio. It
// answers initialize so Manager.Ensure can complete a real handshake.
type fakeBridge struct {
	stdio        *fakeStdio
	initProtocol string
}

func newFakeBridge(initProtocol string) *fakeBridge {
	inR, inW := io.Pipe()   // controller writes stdin -> server reads
	outR, outW := io.Pipe() // server writes stdout -> controller reads
	b := &fakeBridge{
		stdio:        &fakeStdio{inW: inW, outR: outR, inR: inR, outW: outW, waitDone: make(chan struct{})},
		initProtocol: initProtocol,
	}
	go b.serve(inR, outW)
	return b
}

func protocolMismatchBridge(mismatch bool) Stdio {
	protocol := appwire.ProtocolVersion
	if mismatch {
		protocol = "evener-appwire-v4"
	}
	return newFakeBridge(protocol).stdio
}

// newSilentBridge models a hub whose AppWire handshake never answers: it drains
// the framed request stream and never writes a response, so a client's
// Initialize can only end at its own deadline.
func newSilentBridge() *fakeBridge {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	b := &fakeBridge{
		stdio: &fakeStdio{inW: inW, outR: outR, inR: inR, outW: outW, waitDone: make(chan struct{})},
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := inR.Read(buf); err != nil {
				return
			}
		}
	}()
	return b
}

// exitStatus returns a real *exec.ExitError carrying code, the fixture for a
// child that exited with that status. ssh(1) exits 255 when ssh itself fails
// (a refused connection, an authentication refusal) and otherwise reports the
// remote command's own status.
func exitStatus(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != code {
		t.Fatalf("building an exit-status-%d fixture: %v", code, err)
	}
	return err
}

func (b *fakeBridge) serve(inR *io.PipeReader, outW *io.PipeWriter) {
	srv := appwire.NewStreamTransport(&stdioReadWriter{in: outW, out: inR})
	for {
		msg, err := srv.Recv(context.Background())
		if err != nil {
			return
		}
		if msg.Request != nil && msg.Request.Method == appwire.MethodInitialize {
			resp := appwire.InitializeResponse{ProtocolVersion: b.initProtocol, SourceID: "local"}
			if err := srv.Send(context.Background(), appwire.ResponseMessage(msg.Request.ID, resp)); err != nil {
				return
			}
		}
	}
}

// floodNotifications writes n AppWire notification frames to the bridge's
// stdout, so a client that never drains its bounded notification buffer overflows.
// An error means the stream broke first (the client tears it down on overflow);
// the caller only cares that the frames it did write arrived, so it is reported
// rather than fatal.
func (b *fakeBridge) floodNotifications(n int) error {
	srv := appwire.NewStreamTransport(&stdioReadWriter{in: b.stdio.outW, out: b.stdio.inR})
	for i := range n {
		if err := srv.Send(context.Background(), appwire.NotificationMessage("noop", map[string]int{"seq": i})); err != nil {
			return err
		}
	}
	return nil
}

// goodLaunchCheck is the launch-check answer a healthy host gives. Its protocol
// is appwire.ProtocolVersion itself, not a literal, so a protocol bump cannot
// leave this shared fixture speaking a stale version (which reads as an
// old-protocol host and refuses every attach that uses it).
var goodLaunchCheck = `{"hub_endpoint_schema":1,"protocol":"` + appwire.ProtocolVersion + `","version":"dev","launch_flags":["api-log"]}`

// remoteOp names what a command run on the fake host asks of it. The mapping
// from command text to operation lives in classifyRemote alone, so a test says
// "the launch check answers X" and a change to a command's wording is fixed in
// one place instead of in every runFn that matched part of it.
type remoteOp string

const (
	opUnknown       remoteOp = ""
	opUnameKernel   remoteOp = "uname -s"
	opUnameArch     remoteOp = "uname -m"
	opUserID        remoteOp = "id -u"
	opEnvironment   remoteOp = "environment probe"
	opHubEndpoint   remoteOp = "hub endpoint"
	opLaunchCheck   remoteOp = "launch-check"
	opHealth        remoteOp = "api/health"
	opListenerProbe remoteOp = "lsof -ti"
	opBinaryInstall remoteOp = "binary push or install"

	opRelaunch        remoteOp = "relaunch hub"
	opUserName        remoteOp = "user lookup"
	opProcessFiles    remoteOp = "lsof -p"
	opProcessAlive    remoteOp = "kill -s 0"
	opProcessKill     remoteOp = "kill"
	opProcessArgv     remoteOp = "process argv"
	opProcessCmdline  remoteOp = "process cmdline"
	opSystemdUnits    remoteOp = "systemctl list-units"
	opSystemdRestart  remoteOp = "systemctl restart"
	opSystemdStart    remoteOp = "systemctl start"
	opLaunchdList     remoteOp = "launchctl list"
	opLaunchdKick     remoteOp = "launchctl kickstart"
	opInstallDirProbe remoteOp = "test -d"
	opExecutableProbe remoteOp = "test -x"
	opResolveExec     remoteOp = "evener_resolve"
	opCommandLookup   remoteOp = "command -v evener"
)

// classifyRemote returns the operation argv performs on the host, or
// opUnknown for a command no fake host answers.
func classifyRemote(argv []string) remoteOp {
	joined := strings.Join(argv, " ")
	switch {
	case strings.Contains(joined, "lsof -ti"):
		return opListenerProbe
	case strings.Contains(joined, `mv "$tmp"`) || strings.Contains(joined, "evener-install.XXXXXX"):
		return opBinaryInstall
	case strings.Contains(joined, "nohup"):
		return opRelaunch
	case strings.Contains(joined, "lsof -p"):
		return opProcessFiles
	case strings.Contains(joined, "kill -s 0"):
		return opProcessAlive
	case strings.Contains(joined, "kill -- "):
		return opProcessKill
	case strings.Contains(joined, "-ww -o "):
		return opProcessArgv
	case strings.Contains(joined, "/cmdline"):
		return opProcessCmdline
	case strings.Contains(joined, "systemctl restart"):
		return opSystemdRestart
	case strings.Contains(joined, "systemctl start"):
		return opSystemdStart
	case strings.Contains(joined, "list-units"):
		return opSystemdUnits
	case strings.Contains(joined, "launchctl list"):
		return opLaunchdList
	case strings.Contains(joined, "launchctl kickstart"):
		return opLaunchdKick
	case strings.Contains(joined, "if test -x"):
		return opExecutableProbe
	case strings.Contains(joined, "test -d"):
		return opInstallDirProbe
	case strings.Contains(joined, "command -v evener"):
		return opCommandLookup
	case strings.Contains(joined, "evener_resolve"):
		return opResolveExec
	case strings.HasSuffix(joined, "id -un"), strings.Contains(joined, "ps -o user="):
		return opUserName
	case strings.HasSuffix(joined, "hub endpoint --json"):
		return opHubEndpoint
	case strings.HasSuffix(joined, "uname -s"):
		return opUnameKernel
	case strings.HasSuffix(joined, "uname -m"):
		return opUnameArch
	case strings.Contains(joined, "XDG_STATE_HOME"):
		return opEnvironment
	case strings.HasSuffix(joined, "id -u"):
		return opUserID
	case strings.Contains(joined, "launch-check"):
		return opLaunchCheck
	case strings.Contains(joined, "api/health"):
		return opHealth
	default:
		return opUnknown
	}
}

// requireNoRemoteOps fails the test when the fake host was asked to perform any
// of ops.
func requireNoRemoteOps(t *testing.T, fr *fakeRunner, what string, ops ...remoteOp) {
	t.Helper()
	for _, argv := range fr.recordedRuns() {
		if slices.Contains(ops, classifyRemote(argv)) {
			t.Fatalf("%s: %v", what, argv)
		}
	}
}

// resolveTarget returns the path an opResolveExec command resolves.
func resolveTarget(argv []string) string {
	joined := strings.Join(argv, " ")
	return strings.TrimSpace(joined[strings.LastIndex(joined, "evener_resolve ")+len("evener_resolve "):])
}

// resolveAnswers answers an opResolveExec command from a table of the real
// path each target resolves to. A target the table lacks fails to resolve, as
// a missing file does on a host.
func resolveAnswers(argv []string, resolved map[string]string) ([]byte, error) {
	target := resolveTarget(argv)
	if real, ok := resolved[target]; ok {
		return []byte(real + "\n"), nil
	}
	return nil, fmt.Errorf("resolver: %s: no such file or directory", target)
}

// cannedRun returns a runFn answering the standard preflight commands, with
// optional per-operation overrides consulted first.
func cannedRun(overrides map[remoteOp][]byte) func(context.Context, []string, io.Reader) ([]byte, error) {
	return func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		op := classifyRemote(argv)
		if out, ok := overrides[op]; ok {
			return append([]byte(nil), out...), nil
		}
		switch op {
		case opHubEndpoint:
			return []byte(`{"schema_version":1,"state":"running","config_path":"/home/dev/.config/evener/hub.toml","hub_state_root":"/home/dev/.local/state/evener","configured_listen_addr":"127.0.0.1:9180","listen_source":"default","running":{"schema_version":1,"boot_id":"canned-boot","pid":4242,"started_at":"2026-10-15T00:00:00Z","config_path":"/home/dev/.config/evener/hub.toml","hub_state_root":"/home/dev/.local/state/evener","requested_listen_addr":"127.0.0.1:9180","listen_source":"default","bound_addr":"127.0.0.1:9180","local_url":"http://127.0.0.1:9180","loopback_url":"http://127.0.0.1:9180","version":"dev"}}`), nil
		case opUnameKernel:
			return []byte("Linux\n"), nil
		case opUnameArch:
			return []byte("x86_64\n"), nil
		case opEnvironment:
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case opUserID:
			return []byte("1000\n"), nil
		case opLaunchCheck:
			return []byte(goodLaunchCheck), nil
		case opHealth:
			// A running hub matching the on-disk build, so Ensure attaches
			// without deploying, restarting, or taking the first-attach
			// bootstrap start. Tests that need "nothing answered" use their own
			// runFn or a health override.
			return []byte(`{"version":"dev","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}
}

func testRegistry(t *testing.T, hosts ...hostreg.Host) *hostreg.Registry {
	t.Helper()
	reg, err := hostreg.New(hosts)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	return reg
}

// writeStageBinary is a BuildBinary seam that writes a stand-in binary to the
// staging path the deploy opened.
func writeStageBinary(_ context.Context, _, _, out string) error {
	return os.WriteFile(out, []byte("staged-binary"), 0o755)
}

// deployRunner answers the whole ensure sequence for a linux host whose evener is
// installed at /opt/evener/bin/evener: preflight, deploy target resolution and
// push, a systemd restart, and /api/health. launch answers successive
// launch-check calls by index (the last answer repeats), and health answers
// successive health probes.
func deployRunner(t *testing.T, launch func(call int) ([]byte, error), health func(call int) ([]byte, error)) *fakeRunner {
	t.Helper()
	return deployRunnerFor(t, "linux", "amd64", launch, health)
}

// deployRunnerFor is deployRunner for a host on an explicit target: its uname
// answers are the ones preflight's mapOS/mapArch turn back into goos/goarch, so a
// test can put the host on the very target the controller itself runs on
// (runtime.GOOS/runtime.GOARCH) instead of the linux/amd64 pair deployRunner
// hardcodes — the own-executable dispatch compares the host's facts against the
// controller's own target, so a hardcoded pair makes such a test pass only on
// the machine that happens to match it. A target preflight itself cannot
// represent (mapOS and mapArch accept Linux/Darwin and amd64/arm64 only, so no
// host facts for another target can exist) skips: the test cannot be built here,
// and running it against facts the controller could never read would test
// nothing.
func deployRunnerFor(t *testing.T, goos, goarch string, launch func(call int) ([]byte, error), health func(call int) ([]byte, error)) *fakeRunner {
	fr, _ := deployRunnerForFixture(t, goos, goarch, launch, health)
	return fr
}

func deployRunnerForFixture(t *testing.T, goos, goarch string, launch func(call int) ([]byte, error), health func(call int) ([]byte, error)) (*fakeRunner, *selectedProcessFixture) {
	t.Helper()
	osName, ok := map[string]string{"linux": "Linux", "darwin": "Darwin"}[goos]
	if !ok {
		t.Skipf("deployRunnerFor: preflight's mapOS cannot round-trip GOOS %q, so no host facts for it exist and this test cannot run on this platform", goos)
	}
	archName, ok := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[goarch]
	if !ok {
		t.Skipf("deployRunnerFor: preflight's mapArch cannot round-trip GOARCH %q, so no host facts for it exist and this test cannot run on this platform", goarch)
	}
	launchCalls, healthCalls := 0, 0
	fr := &fakeRunner{
		runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
			switch classifyRemote(argv) {
			case opUnameKernel:
				return []byte(osName + "\n"), nil
			case opUnameArch:
				return []byte(archName + "\n"), nil
			case opEnvironment:
				return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
			case opUserID:
				return []byte("1000\n"), nil
			case opLaunchCheck:
				out, err := launch(launchCalls)
				launchCalls++
				return out, err
			case opInstallDirProbe:
				return nil, nil
			case opExecutableProbe:
				return nil, nil // This fixture's installed executable is runnable.
			case opResolveExec:
				return []byte("/opt/evener/bin/evener\n"), nil
			case opSystemdUnits:
				return []byte("evener-hub.service loaded active running Evener Hub\n"), nil
			case opSystemdRestart, opSystemdStart:
				return nil, nil
			case opHealth:
				out, err := health(healthCalls)
				healthCalls++
				return out, err
			case opBinaryInstall:
				if stdin != nil {
					_, _ = io.ReadAll(stdin)
				}
				return nil, nil
			default:
				return nil, fmt.Errorf("unexpected remote command: %v", argv)
			}
		},
		startFn: goodStartFn(t),
	}
	fixture := modelSelectedProcess(t, fr, "/opt/evener/bin/evener", "", "")
	fixture.darwin = goos == "darwin"
	initialStart := ""
	fixture.observe = func(f *selectedProcessFixture) {
		if health == nil {
			f.stopped = true
			return
		}
		out, err := health(healthCalls)
		if f.mutated {
			healthCalls++
		}
		if err != nil {
			f.stopped = true
			return
		}
		var state struct {
			Version string `json:"version"`
			SHA     string `json:"backend_git_sha"`
			Started string `json:"started_at"`
		}
		if err := json.Unmarshal(out, &state); err != nil {
			t.Fatalf("fixture health state: %v", err)
		}
		f.stopped = false
		f.before, f.after, f.sha = state.Version, state.Version, state.SHA
		if state.Started != "" {
			if initialStart == "" {
				initialStart = state.Started
			}
			f.mutated = state.Started != initialStart && state.Started != "0001-01-01T00:00:00Z"
			started, err := time.Parse(time.RFC3339Nano, state.Started)
			if err != nil {
				t.Fatal(err)
			}
			f.reportedStart = &started
		}
	}
	return fr, fixture
}

// registeredTestHost supplies the registry generation to fixtures written before
// lifecycle recovery retained the full registration. Nonzero generations remain
// untouched so stale-registration negative controls still exercise rejection.
func registeredTestHost(t *testing.T, m *Manager, host hostreg.Host) hostreg.Host {
	t.Helper()
	if host.Generation != 0 {
		return host
	}
	current, ok := m.reg.Get(host.Name)
	if !ok || !current.Equal(host) {
		t.Fatalf("fixture registration does not match %q", host.Name)
	}
	return current
}

// modelHealthProbe is the /api/health read that health-only fixtures answer;
// modelLegacySelectedProcess issues it to learn the build such a fixture serves.
const modelHealthProbe = "curl -q --noproxy '*' -fsS http://127.0.0.1:9180/api/health"

// modelLegacySelectedProcess adds the selected lock/process/service evidence
// omitted by fixtures written for the old health-only manager. The existing
// fake still supplies the served build and all mutation effects.
func modelLegacySelectedProcess(t *testing.T, fr *fakeRunner, host hostreg.Host) *selectedProcessFixture {
	t.Helper()
	base := fr.runFn
	exe := host.EvenerPath
	if exe == "" {
		exe = "/opt/evener/bin/evener"
	}
	f := modelSelectedProcess(t, fr, exe, "", "")
	if host.EvenerPath == "" {
		f.endpointAlias = "evener hub endpoint --json"
	}
	f.observe = func(f *selectedProcessFixture) {
		out, err := base(t.Context(), rawCommandArgv(Options{}, host, modelHealthProbe), nil)
		if err != nil {
			f.stopped = true
			return
		}
		var health struct {
			Version string    `json:"version"`
			SHA     string    `json:"backend_git_sha"`
			Started time.Time `json:"started_at"`
		}
		if json.Unmarshal(out, &health) != nil || health.Version == "" {
			f.stopped = true
			return
		}
		f.stopped = false
		f.before, f.after, f.sha = health.Version, health.Version, health.SHA
		if !health.Started.IsZero() {
			f.reportedStart = &health.Started
		}
	}
	return f
}

// setPendingRestart seeds a command that restarts a serving hub.
func (m *Manager) setPendingRestart(name, remote, expectedVersion, expectedGitSHA string, replaced hubIdentity, binding *effectBinding) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pendingRestarts[name] = pendingRestartState{command: remote, expectedVersion: expectedVersion, expectedGitSHA: expectedGitSHA, replaced: replaced, binding: binding}
}

// setPendingStart seeds a command that starts a hub where nothing was serving,
// keeping any known original predecessor.
func (m *Manager) setPendingStart(name, remote, expectedVersion, expectedGitSHA string, binding *effectBinding) {
	m.mu.Lock()
	defer m.mu.Unlock()
	replaced := m.pendingRestarts[name].replaced
	m.pendingRestarts[name] = pendingRestartState{command: remote, expectedVersion: expectedVersion, expectedGitSHA: expectedGitSHA, replaced: replaced, start: true, binding: binding}
}

// Answers the fake runners give a port probe; the production restart path no
// longer issues one.
const (
	noListenerMarker      = "__sshconn_no_listener__"
	listenerPresentMarker = "__sshconn_listener_present__"
)
