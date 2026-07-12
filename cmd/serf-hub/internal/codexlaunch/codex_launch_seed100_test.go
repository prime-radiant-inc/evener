package codexlaunch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
)

type seedRoundTripper func(*http.Request) (*http.Response, error)

func (f seedRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func seedClient(status int, err error) *http.Client {
	return &http.Client{Transport: seedRoundTripper(func(*http.Request) (*http.Response, error) {
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
}

type seedProcess struct {
	cmd       *exec.Cmd
	stdout    io.ReadCloser
	stderr    io.ReadCloser
	stdoutErr error
	stderrErr error
	startErr  error
	waitErr   error
	wait      chan struct{}
	killed    chan struct{}
	killOnce  sync.Once
	dir       string
	env       []string
}

func newSeedProcess(stdout, stderr string) *seedProcess {
	return &seedProcess{
		cmd:    &exec.Cmd{},
		stdout: io.NopCloser(strings.NewReader(stdout)),
		stderr: io.NopCloser(strings.NewReader(stderr)),
		wait:   make(chan struct{}),
		killed: make(chan struct{}),
	}
}

func (p *seedProcess) Cmd() *exec.Cmd                     { return p.cmd }
func (p *seedProcess) SetDir(dir string)                  { p.dir = dir }
func (p *seedProcess) SetEnv(env []string)                { p.env = env }
func (p *seedProcess) StdoutPipe() (io.ReadCloser, error) { return p.stdout, p.stdoutErr }
func (p *seedProcess) StderrPipe() (io.ReadCloser, error) { return p.stderr, p.stderrErr }
func (p *seedProcess) Start() error                       { return p.startErr }
func (p *seedProcess) Wait() error                        { <-p.wait; return p.waitErr }
func (p *seedProcess) Kill() error {
	p.killOnce.Do(func() { close(p.killed); close(p.wait) })
	return nil
}
func (p *seedProcess) Exit() { p.killOnce.Do(func() { close(p.wait) }) }

type seedTicker struct{ ch chan time.Time }

func (t *seedTicker) C() <-chan time.Time { return t.ch }
func (*seedTicker) Stop()                 {}

func useSeedRuntime(l *CodexLauncher, process *seedProcess, ticks int, timedOut bool) {
	l.process = func(string, ...string) launchProcess { return process }
	l.newTicker = func(time.Duration) launchTicker {
		ch := make(chan time.Time, ticks)
		for range ticks {
			ch <- time.Time{}
		}
		return &seedTicker{ch: ch}
	}
	l.withTimeout = func(ctx context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
		waitCtx, cancel := context.WithCancel(ctx)
		if timedOut {
			cancel()
		}
		return waitCtx, cancel
	}
}

func TestSeed100LauncherConfigurationAndCache(t *testing.T) {
	l := NewCodexLauncher([]CodexLaunchConfig{{ID: "  "}, {ID: " named "}})
	if !l.Manages("codex") || !l.Manages("named") || l.Manages("missing") {
		t.Fatal("launcher did not normalize configured IDs")
	}
	if _, err := l.EnsureSource(context.Background(), "missing", nil); err == nil {
		t.Fatal("missing launch configuration succeeded")
	}
	l.configs["bad"] = CodexLaunchConfig{Listen: "http://bad"}
	if _, err := l.EnsureSource(context.Background(), "bad", nil); err == nil {
		t.Fatal("configured launch failure succeeded")
	}

	source := appsource.NewCodexSource(appsource.CodexSourceConfig{ID: "named", Endpoint: "ws://example"}, seedClient(200, nil))
	l.Sources["named"] = source
	alive := make(chan struct{})
	l.Running["named"] = &LaunchedCodex{Exited: alive}
	registry := appsource.NewRegistry()
	got, err := l.EnsureSource(context.Background(), "named", registry)
	if err != nil || got != source {
		t.Fatalf("cached source = %v, %v", got, err)
	}
	if registered, ok := registry.Source("named"); !ok || registered != source {
		t.Fatal("cached source was not restored to registry")
	}

	close(alive)
	if got, ok := l.cachedSourceLocked("named", registry); ok || got != nil {
		t.Fatal("exited source remained cached")
	}
	if _, ok := registry.Source("named"); ok {
		t.Fatal("exited source remained registered")
	}
}

func TestSeed100LaunchArgumentsEnvironmentAndScanning(t *testing.T) {
	tests := []struct {
		binary string
		args   []string
		want   string
	}{
		{"codex", nil, "app-server --listen ws://x"},
		{"codex-app-server", nil, "--listen ws://x"},
		{"codex", []string{"serve", "--listen=ws://y"}, "serve --listen=ws://y"},
	}
	for _, tt := range tests {
		if got := strings.Join(buildCodexLaunchArgs(tt.binary, tt.args, "ws://x"), " "); got != tt.want {
			t.Fatalf("args = %q, want %q", got, tt.want)
		}
	}
	if argsContainFlag([]string{"--listened"}, "--listen") {
		t.Fatal("prefix-only argument was treated as flag")
	}

	t.Setenv("SEED100_BASE", "base")
	env := codexLaunchEnv(map[string]string{"SEED100_OVERRIDE": "value"})
	joined := strings.Join(env, "\n")
	for _, want := range []string{"SEED100_BASE=base", "SEED100_OVERRIDE=value", "SERF_HUB_SPAWNED_CODEX=1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("environment missing %q", want)
		}
	}

	endpoints := make(chan string, 2)
	scanCodexEndpoint(strings.NewReader("noise\n{\"endpoint\":\"ws://one:1\"}\nlisten ws://two:2.\n"), endpoints)
	close(endpoints)
	var got []string
	for endpoint := range endpoints {
		got = append(got, endpoint)
	}
	if strings.Join(got, ",") != "ws://one:1,ws://two:2" {
		t.Fatalf("scanned endpoints = %v", got)
	}
}

func TestSeed100ReadyAndEndpointConversion(t *testing.T) {
	ctx := context.Background()
	if !CodexReady(ctx, seedClient(http.StatusOK, nil), "ws://host:9/app?q=1#frag") {
		t.Fatal("200 readiness response rejected")
	}
	if CodexReady(ctx, seedClient(http.StatusNoContent, nil), "ws://host:9") ||
		CodexReady(ctx, seedClient(0, errors.New("offline")), "ws://host:9") ||
		CodexReady(ctx, seedClient(200, nil), "http://host:9") ||
		CodexReady(ctx, seedClient(200, nil), "://bad") {
		t.Fatal("invalid readiness input accepted")
	}
	if got, err := codexReadyURL("ws://host:9/app?q=1#frag"); err != nil || got != "http://host:9/readyz" {
		t.Fatalf("ready URL = %q, %v", got, err)
	}
	if _, err := codexReadyURL("http://host"); err == nil {
		t.Fatal("unsupported scheme accepted")
	}
	for input, want := range map[string]string{"ws://host:1": "ws://host:1", "ws://host:0": "", "bad": ""} {
		if got := configuredCodexEndpoint(input); got != want {
			t.Fatalf("configured endpoint(%q) = %q", input, got)
		}
	}
}

func TestSeed100ReadyRequestConstructionFailure(t *testing.T) {
	original := newRequestWithContext
	newRequestWithContext = func(context.Context, string, string, io.Reader) (*http.Request, error) {
		return nil, errors.New("request failure")
	}
	t.Cleanup(func() { newRequestWithContext = original })
	if CodexReady(context.Background(), seedClient(http.StatusOK, nil), "ws://host:9") {
		t.Fatal("request construction failure reported ready")
	}
}

func TestSeed100LaunchFailureModes(t *testing.T) {
	l := NewCodexLauncher(nil)
	l.client = seedClient(0, errors.New("not ready"))
	if _, err := l.launchLocked(context.Background(), CodexLaunchConfig{Listen: "http://bad"}); err == nil {
		t.Fatal("non-websocket listen accepted")
	}
	startFailure := newSeedProcess("", "")
	startFailure.startErr = errors.New("missing executable")
	useSeedRuntime(l, startFailure, 0, false)
	if _, err := l.launchLocked(context.Background(), CodexLaunchConfig{Binary: "/definitely/not/a/binary", Listen: "ws://127.0.0.1:1"}); err == nil {
		t.Fatal("missing binary started")
	}
	if _, err := l.launchLocked(context.Background(), CodexLaunchConfig{Binary: "/definitely/not/a/binary"}); err == nil {
		t.Fatal("default listen with missing binary started")
	}
	earlyExit := newSeedProcess("", "")
	earlyExit.Exit()
	useSeedRuntime(l, earlyExit, 0, false)
	if _, err := l.launchLocked(context.Background(), CodexLaunchConfig{Listen: "ws://127.0.0.1:1"}); err == nil || !strings.Contains(err.Error(), "exited before ready") {
		t.Fatalf("early exit error = %v", err)
	}
	timedOut := newSeedProcess("", "")
	useSeedRuntime(l, timedOut, 0, true)
	if _, err := l.launchLocked(context.Background(), CodexLaunchConfig{Listen: "ws://127.0.0.1:1"}); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout error = %v", err)
	}
	select {
	case <-timedOut.killed:
	default:
		t.Fatal("timed out process was not killed")
	}
}

func TestSeed100EndpointDiscoveryAndErroredExit(t *testing.T) {
	l := NewCodexLauncher(nil)
	discovered := newSeedProcess("noise\n{\"endpoint\":\"ws://127.0.0.1:4567\"}\n", "")
	useSeedRuntime(l, discovered, 0, false)
	requests := 0
	l.client = &http.Client{Transport: seedRoundTripper(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	launched, err := l.launchLocked(context.Background(), CodexLaunchConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if launched.endpoint != "ws://127.0.0.1:4567" {
		t.Fatalf("discovered endpoint = %q", launched.endpoint)
	}
	_ = launched.process.Kill()
	<-launched.Exited

	l.client = seedClient(0, errors.New("offline"))
	errorExit := newSeedProcess("", "")
	errorExit.waitErr = errors.New("exit status 7")
	errorExit.Exit()
	useSeedRuntime(l, errorExit, 0, false)
	if _, err := l.launchLocked(context.Background(), CodexLaunchConfig{Listen: "ws://127.0.0.1:1"}); err == nil || !strings.Contains(err.Error(), "exit status") {
		t.Fatalf("errored exit = %v", err)
	}

	retry := newSeedProcess("", "")
	useSeedRuntime(l, retry, 1, false)
	requests = 0
	l.client = &http.Client{Transport: seedRoundTripper(func(*http.Request) (*http.Response, error) {
		requests++
		status := http.StatusServiceUnavailable
		if requests == 2 {
			status = http.StatusOK
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	if _, err := l.launchLocked(context.Background(), CodexLaunchConfig{Listen: "ws://127.0.0.1:9"}); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("readiness requests = %d, want 2", requests)
	}
}

func TestSeed100LaunchPipeFailures(t *testing.T) {
	for _, stderr := range []bool{false, true} {
		l := NewCodexLauncher(nil)
		process := newSeedProcess("", "")
		if stderr {
			process.stderrErr = errors.New("stderr pipe")
		} else {
			process.stdoutErr = errors.New("stdout pipe")
		}
		useSeedRuntime(l, process, 0, false)
		if _, err := l.launchLocked(context.Background(), CodexLaunchConfig{Listen: "ws://127.0.0.1:1"}); err == nil {
			t.Fatalf("stderr=%v: preconfigured pipe succeeded", stderr)
		}
	}
}

func TestSeed100EnsureSourceLaunchAndShutdown(t *testing.T) {
	l := NewCodexLauncher([]CodexLaunchConfig{{ID: "live", Listen: "ws://127.0.0.1:4321"}})
	process := newSeedProcess("", "")
	useSeedRuntime(l, process, 0, false)
	l.client = seedClient(http.StatusOK, nil)
	registry := appsource.NewRegistry()
	source, err := l.EnsureSource(context.Background(), "live", registry)
	if err != nil || source == nil {
		t.Fatalf("EnsureSource = %v, %v", source, err)
	}
	if _, ok := registry.Source("live"); !ok {
		t.Fatal("launched source was not registered")
	}
	if err := l.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(l.Running) != 0 || len(l.Sources) != 0 {
		t.Fatal("shutdown retained launcher state")
	}
}

func TestSeed100Shutdown(t *testing.T) {
	l := NewCodexLauncher(nil)
	exited := make(chan struct{})
	close(exited)
	l.Running["done"] = &LaunchedCodex{Cmd: &execCmdWithoutProcess, Exited: exited}
	if err := l.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	never := make(chan struct{})
	l.Running["stuck"] = &LaunchedCodex{Cmd: &execCmdWithoutProcess, Exited: never}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown error = %v", err)
	}
}

func TestSeed100ProductionRuntimeAdaptersWithoutStartingChild(t *testing.T) {
	l := NewCodexLauncher(nil)
	process := l.process(filepath.Join(t.TempDir(), "missing"))
	process.SetDir("/tmp")
	process.SetEnv([]string{"A=B"})
	if process.Cmd().Dir != "/tmp" || strings.Join(process.Cmd().Env, "") != "A=B" {
		t.Fatal("exec process configuration was not retained")
	}
	if _, err := process.StdoutPipe(); err != nil {
		t.Fatal(err)
	}
	if _, err := process.StderrPipe(); err != nil {
		t.Fatal(err)
	}
	if err := process.Start(); err == nil {
		t.Fatal("missing executable started")
	}
	if err := process.Wait(); err == nil {
		t.Fatal("waiting on an unstarted command succeeded")
	}
	if err := process.Kill(); err != nil {
		t.Fatalf("kill before start: %v", err)
	}

	process.Cmd().Process, _ = os.FindProcess(2147483647)
	_ = process.Kill()
	ticker := l.newTicker(time.Hour)
	_ = ticker.C()
	ticker.Stop()
	waitCtx, cancel := l.withTimeout(context.Background(), time.Hour)
	cancel()
	<-waitCtx.Done()

	exited := make(chan struct{})
	close(exited)
	l.Running["legacy"] = &LaunchedCodex{Cmd: process.Cmd(), Exited: exited}
	if err := l.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

var execCmdWithoutProcess exec.Cmd
