package codexlaunch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
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
	if _, err := l.launchLocked(context.Background(), CodexLaunchConfig{Binary: "/definitely/not/a/binary", Listen: "ws://127.0.0.1:1"}); err == nil {
		t.Fatal("missing binary started")
	}
	if _, err := l.launchLocked(context.Background(), CodexLaunchConfig{Binary: "/definitely/not/a/binary"}); err == nil {
		t.Fatal("default listen with missing binary started")
	}
	if _, err := l.launchLocked(context.Background(), CodexLaunchConfig{Binary: os.Args[0], Args: []string{"-test.run=TestSeed100HelperExit", "--"}, Listen: "ws://127.0.0.1:1", Timeout: 10 * time.Second}); err == nil || !strings.Contains(err.Error(), "exited before ready") {
		t.Fatalf("early exit error = %v", err)
	}
	if _, err := l.launchLocked(context.Background(), CodexLaunchConfig{Binary: os.Args[0], Args: []string{"-test.run=TestSeed100HelperBlock", "--"}, Listen: "ws://127.0.0.1:1", Timeout: time.Nanosecond}); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestSeed100EndpointDiscoveryAndErroredExit(t *testing.T) {
	l := NewCodexLauncher(nil)
	requests := 0
	l.client = &http.Client{Transport: seedRoundTripper(func(*http.Request) (*http.Response, error) {
		requests++
		status := http.StatusServiceUnavailable
		if requests > 1 {
			status = http.StatusOK
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	launched, err := l.launchLocked(context.Background(), CodexLaunchConfig{Binary: os.Args[0], Args: []string{"-test.run=TestSeed100HelperEndpoint", "--"}, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if launched.endpoint != "ws://127.0.0.1:4567" {
		t.Fatalf("discovered endpoint = %q", launched.endpoint)
	}
	_ = launched.Cmd.Process.Kill()
	<-launched.Exited

	l.client = seedClient(0, errors.New("offline"))
	if _, err := l.launchLocked(context.Background(), CodexLaunchConfig{Binary: os.Args[0], Args: []string{"-test.run=TestSeed100HelperErrorExit", "--"}, Listen: "ws://127.0.0.1:1", Timeout: time.Second}); err == nil || !strings.Contains(err.Error(), "exit status") {
		t.Fatalf("errored exit = %v", err)
	}
}

func TestSeed100LaunchPipeFailures(t *testing.T) {
	for _, stderr := range []bool{false, true} {
		l := NewCodexLauncher(nil)
		l.command = func(string, ...string) *exec.Cmd {
			cmd := exec.Command(os.Args[0], "-test.run=TestSeed100HelperExit")
			if stderr {
				cmd.Stderr = io.Discard
			} else {
				cmd.Stdout = io.Discard
			}
			return cmd
		}
		if _, err := l.launchLocked(context.Background(), CodexLaunchConfig{Listen: "ws://127.0.0.1:1"}); err == nil {
			t.Fatalf("stderr=%v: preconfigured pipe succeeded", stderr)
		}
	}
}

func TestSeed100EnsureSourceLaunchAndShutdown(t *testing.T) {
	l := NewCodexLauncher([]CodexLaunchConfig{{ID: "live", Binary: os.Args[0], Args: []string{"-test.run=TestSeed100HelperBlock", "--"}, Listen: "ws://127.0.0.1:4321", Timeout: time.Second}})
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

func TestSeed100HelperExit(t *testing.T) {}

func TestSeed100HelperEndpoint(t *testing.T) {
	if os.Getenv("SERF_HUB_SPAWNED_CODEX") == "1" {
		_, _ = os.Stdout.WriteString("noise\n{\"endpoint\":\"ws://127.0.0.1:4567\"}\n")
		select {}
	}
}

func TestSeed100HelperErrorExit(t *testing.T) {
	if os.Getenv("SERF_HUB_SPAWNED_CODEX") == "1" {
		os.Exit(7)
	}
}

func TestSeed100HelperBlock(t *testing.T) {
	if os.Getenv("SERF_HUB_SPAWNED_CODEX") == "1" {
		select {}
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

var execCmdWithoutProcess exec.Cmd
