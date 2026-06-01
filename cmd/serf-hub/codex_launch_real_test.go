package main

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/cmd/serf-hub/internal/codexlaunch"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/internal/appsource"
	"primeradiant.com/serf/internal/appwire"
)

func TestCodexLauncherRealAppServerSmoke(t *testing.T) {
	binary := os.Getenv("SERF_CODEX_APP_SERVER_BINARY")
	if binary == "" {
		t.Skip("set SERF_CODEX_APP_SERVER_BINARY to run real Codex app-server smoke")
	}
	codexHome := t.TempDir()
	launcher := codexlaunch.NewCodexLauncher([]codexlaunch.CodexLaunchConfig{{
		ID:         "codex-real",
		Binary:     binary,
		WorkingDir: t.TempDir(),
		Listen:     "ws://127.0.0.1:0",
		Timeout:    10 * time.Second,
		Args:       realCodexAppServerArgs(binary),
		Env: map[string]string{
			"CODEX_HOME": codexHome,
			"LOG_FORMAT": "json",
		},
	}})
	defer shutdownCodexLauncher(t, launcher)

	sources := appsource.NewRegistry()
	source, err := launcher.EnsureSource(context.Background(), "codex-real", sources)
	if err != nil {
		t.Fatalf("EnsureSource: %v", err)
	}
	if _, ok := sources.Source("codex-real"); !ok {
		t.Fatal("launched source was not registered")
	}

	resp, err := source.StartThread(context.Background(), appwire.ThreadStartParams{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("StartThread: %v", err)
	}
	if resp.Thread.Serf.Ref == "" || resp.Thread.Source != "codex-real" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestHubRPCRealCodexSourceAllowsBlankStart(t *testing.T) {
	binary := os.Getenv("SERF_CODEX_APP_SERVER_BINARY")
	if binary == "" {
		t.Skip("set SERF_CODEX_APP_SERVER_BINARY to run real Codex app-server smoke")
	}
	endpoint, shutdown := startRealCodexAppServer(t, binary)
	defer shutdown()

	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past: hubcore.NewPastIndex(""),
		CodexSources: []appsource.CodexSourceConfig{{
			ID:       "codex-real",
			Endpoint: endpoint,
		}},
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		Harness: "codex-real",
		CWD:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if resp.Thread.Serf.Ref == "" || resp.Thread.Source != "codex-real" || resp.Turn.ID != "" {
		t.Fatalf("resp=%+v", resp)
	}
}

func TestHubRPCRealCodexSourceModelList(t *testing.T) {
	binary := os.Getenv("SERF_CODEX_APP_SERVER_BINARY")
	if binary == "" {
		t.Skip("set SERF_CODEX_APP_SERVER_BINARY to run real Codex app-server smoke")
	}
	endpoint, shutdown := startRealCodexAppServer(t, binary)
	defer shutdown()

	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past: hubcore.NewPastIndex(""),
		CodexSources: []appsource.CodexSourceConfig{{
			ID:       "codex-real",
			Endpoint: endpoint,
		}},
	})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := client.ModelList(context.Background(), appwire.ModelListParams{Harness: "codex-real"})
	if err != nil {
		t.Fatalf("ModelList: %v", err)
	}
	if len(resp.Data) == 0 {
		t.Fatal("real Codex source returned no models")
	}
	for _, model := range resp.Data {
		if model.Provider != "codex-real" {
			t.Fatalf("model provider=%q, want codex-real in %+v", model.Provider, resp.Data)
		}
	}
}

func startRealCodexAppServer(t *testing.T, binary string) (string, func()) {
	t.Helper()
	args := append(realCodexAppServerArgs(binary), "--listen", "ws://127.0.0.1:0")
	cmd := exec.Command(binary, args...)
	cmd.Env = append(os.Environ(),
		"CODEX_HOME="+t.TempDir(),
		"LOG_FORMAT=json",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("StderrPipe: %v", err)
	}
	endpoints := make(chan string, 4)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start codex app-server: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	go scanRealCodexEndpoint(stdout, endpoints)
	go scanRealCodexEndpoint(stderr, endpoints)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var endpoint string
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if endpoint != "" && codexlaunch.CodexReady(ctx, httpTestClient(), endpoint) {
			return endpoint, func() {
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				select {
				case <-exited:
				case <-time.After(2 * time.Second):
					t.Fatalf("codex app-server did not exit after kill")
				}
			}
		}
		select {
		case next := <-endpoints:
			if next != "" {
				endpoint = next
			}
		case err := <-exited:
			t.Fatalf("codex app-server exited before ready: %v", err)
		case <-ticker.C:
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			t.Fatalf("timed out waiting for codex app-server ready")
		}
	}
}

func realCodexAppServerArgs(binary string, extra ...string) []string {
	args := append([]string{}, extra...)
	if strings.Contains(filepath.Base(binary), "codex-app-server") {
		return args
	}
	return append([]string{"app-server"}, args...)
}

func scanRealCodexEndpoint(r io.Reader, endpoints chan<- string) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if endpoint, ok := codexlaunch.ParseCodexEndpoint(scanner.Text()); ok {
			endpoints <- endpoint
		}
	}
}

func httpTestClient() *http.Client {
	return http.DefaultClient
}
