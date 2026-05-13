package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/internal/appserver"
	"primeradiant.com/serf/internal/appwire"
)

func TestCodexLauncherLaunchesProcessAndWaitsForReady(t *testing.T) {
	launcher := NewCodexLauncher([]CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")})
	defer shutdownCodexLauncher(t, launcher)

	source, err := launcher.EnsureSource(context.Background(), "codex-managed", nil)
	if err != nil {
		t.Fatalf("EnsureSource: %v", err)
	}
	resp, err := source.StartThread(context.Background(), appwire.ThreadStartParams{CWD: "/tmp/project", Prompt: "hello codex"})
	if err != nil {
		t.Fatalf("StartThread: %v", err)
	}
	if resp.Thread.Serf.Ref != "codex-managed:th_fake" || resp.Turn.ID != "turn_fake" {
		t.Fatalf("resp=%+v", resp)
	}
}

func TestCodexLauncherEarlyExitReturnsStructuredDiagnostic(t *testing.T) {
	launcher := NewCodexLauncher([]CodexLaunchConfig{fakeCodexLaunchConfig("codex-exit", "exit")})
	defer shutdownCodexLauncher(t, launcher)

	_, err := launcher.EnsureSource(context.Background(), "codex-exit", nil)
	assertHubLaunchError(t, err)
	if !strings.Contains(err.Error(), "exited before ready") {
		t.Fatalf("error=%v", err)
	}
}

func TestCodexLauncherTimeoutReturnsStructuredDiagnostic(t *testing.T) {
	cfg := fakeCodexLaunchConfig("codex-timeout", "silent")
	cfg.Timeout = 50 * time.Millisecond
	launcher := NewCodexLauncher([]CodexLaunchConfig{cfg})
	defer shutdownCodexLauncher(t, launcher)

	_, err := launcher.EnsureSource(context.Background(), "codex-timeout", nil)
	assertHubLaunchError(t, err)
	if !strings.Contains(err.Error(), "timed out waiting for ready") {
		t.Fatalf("error=%v", err)
	}
}

func TestCodexLauncherMissingBinaryReturnsStructuredDiagnostic(t *testing.T) {
	launcher := NewCodexLauncher([]CodexLaunchConfig{{
		ID:     "codex-missing",
		Binary: "/tmp/serf-no-such-codex-binary",
		Listen: "ws://127.0.0.1:0",
	}})
	defer shutdownCodexLauncher(t, launcher)

	_, err := launcher.EnsureSource(context.Background(), "codex-missing", nil)
	assertHubLaunchError(t, err)
	if !strings.Contains(err.Error(), "start codex app-server") {
		t.Fatalf("error=%v", err)
	}
}

func TestFakeCodexAppServerHelper(t *testing.T) {
	if os.Getenv("SERF_FAKE_CODEX_APP_SERVER") == "" {
		return
	}
	mode := os.Getenv("SERF_FAKE_CODEX_APP_SERVER")
	switch mode {
	case "exit":
		fmt.Fprintln(os.Stderr, "fake codex exited before ready")
		os.Exit(42)
	case "silent":
		for {
			time.Sleep(time.Hour)
		}
	case "ready":
	default:
		fmt.Fprintf(os.Stderr, "unknown fake codex mode %q\n", mode)
		os.Exit(2)
	}

	listenURL := "ws://127.0.0.1:0"
	for i, arg := range os.Args {
		if arg == "--listen" && i+1 < len(os.Args) {
			listenURL = os.Args[i+1]
			break
		}
	}
	addr := strings.TrimPrefix(listenURL, "ws://")
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(2)
	}
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "fake-codex", SourceID: "codex-managed"})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadStart, func(_ context.Context, params map[string]any) (map[string]any, error) {
		return map[string]any{"thread": map[string]any{
			"id":        "th_fake",
			"sessionId": "th_fake",
			"preview":   "fake codex",
			"status":    map[string]any{"type": "idle"},
			"cwd":       params["cwd"],
			"source":    "appServer",
		}}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, func(_ context.Context, params map[string]any) (map[string]any, error) {
		return map[string]any{"turn": map[string]any{
			"id":        "turn_fake",
			"items":     []any{},
			"itemsView": "full",
			"status":    "inProgress",
		}}, nil
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/", app.ServeWebSocket)
	fmt.Fprintf(os.Stderr, "codex app-server (WebSockets)\n  listening on: ws://%s\n  readyz: http://%s/readyz\n", listener.Addr(), listener.Addr())
	_ = (&http.Server{Handler: mux}).Serve(listener)
	os.Exit(0)
}

func fakeCodexLaunchConfig(id, mode string) CodexLaunchConfig {
	exe, err := os.Executable()
	if err != nil {
		panic(err)
	}
	return CodexLaunchConfig{
		ID:      id,
		Binary:  exe,
		Args:    []string{"-test.run=TestFakeCodexAppServerHelper", "--"},
		Listen:  "ws://127.0.0.1:0",
		Timeout: 5 * time.Second,
		Env:     map[string]string{"SERF_FAKE_CODEX_APP_SERVER": mode},
	}
}

func assertHubLaunchError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error %T is not appwire.WireError: %v", err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok || data.SerfErrorInfo != appwire.ErrorHubLaunch {
		t.Fatalf("wire error=%+v", wire)
	}
}

func shutdownCodexLauncher(t *testing.T, launcher *CodexLauncher) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := launcher.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown launcher: %v", err)
	}
}
