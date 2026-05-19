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
	resp, err := source.StartThread(context.Background(), appwire.ThreadStartParams{CWD: "/tmp/project", Input: []appwire.InputItem{{Type: "text", Text: "hello codex"}}})
	if err != nil {
		t.Fatalf("StartThread: %v", err)
	}
	if resp.Thread.Serf.Ref != "codex-managed:th_fake" || resp.Turn.ID != "turn_fake" {
		t.Fatalf("resp=%+v", resp)
	}
}

func TestCodexLauncherRelaunchesAfterManagedProcessExits(t *testing.T) {
	launcher := NewCodexLauncher([]CodexLaunchConfig{fakeCodexLaunchConfig("codex-managed", "ready")})
	defer shutdownCodexLauncher(t, launcher)

	first, err := launcher.EnsureSource(context.Background(), "codex-managed", nil)
	if err != nil {
		t.Fatalf("EnsureSource first: %v", err)
	}
	launcher.mu.Lock()
	launched := launcher.running["codex-managed"]
	launcher.mu.Unlock()
	if launched == nil || launched.cmd.Process == nil {
		t.Fatal("managed codex process was not tracked")
	}
	if err := launched.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill managed codex: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		next, err := launcher.EnsureSource(context.Background(), "codex-managed", nil)
		if err != nil {
			t.Fatalf("EnsureSource after exit: %v", err)
		}
		if next != first {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("EnsureSource kept returning the exited managed Codex source")
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

func TestParseCodexEndpointAcceptsJSONEndpointLine(t *testing.T) {
	endpoint, ok := parseCodexEndpoint(`{"endpoint":"ws://127.0.0.1:1234/rpc"}`)
	if !ok {
		t.Fatal("endpoint not parsed")
	}
	if endpoint != "ws://127.0.0.1:1234/rpc" {
		t.Fatalf("endpoint=%q", endpoint)
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
	appserver.HandleTyped(app.Router(), appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (map[string]any, error) {
		return map[string]any{"data": []map[string]any{{
			"id":            "th_fake",
			"sessionId":     "th_fake",
			"preview":       "fake codex",
			"modelProvider": "openai",
			"status":        map[string]any{"type": "closed"},
			"cwd":           "/tmp/fake-codex",
			"source":        "appServer",
		}}}, nil
	})
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
	appserver.HandleTyped(app.Router(), appwire.MethodThreadResume, func(_ context.Context, params map[string]any) (map[string]any, error) {
		return map[string]any{"thread": map[string]any{
			"id":        params["threadId"],
			"sessionId": params["threadId"],
			"preview":   "fake codex",
			"status":    map[string]any{"type": "idle"},
			"source":    "appServer",
		}}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params map[string]any) (map[string]any, error) {
		return map[string]any{"thread": map[string]any{
			"id":        params["threadId"],
			"sessionId": params["threadId"],
			"preview":   "fake codex",
			"status":    map[string]any{"type": "idle"},
			"source":    "appServer",
		}}, nil
	})
	appserver.HandleTyped(app.Router(), appwire.MethodThreadFork, func(_ context.Context, params map[string]any) (map[string]any, error) {
		threadID, _ := params["threadId"].(string)
		if threadID == "" {
			threadID = "th_fake"
		}
		return map[string]any{"thread": map[string]any{
			"id":        threadID + "_child",
			"sessionId": threadID + "_child",
			"preview":   "fake codex fork",
			"status":    map[string]any{"type": "idle"},
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
	if !wireErrorInfoIs(wire.Data, appwire.ErrorHubLaunch) {
		t.Fatalf("wire error=%+v", wire)
	}
}

func wireErrorInfoIs(data any, want appwire.ErrorInfo) bool {
	switch v := data.(type) {
	case appwire.ErrorData:
		return v.SerfErrorInfo == want
	case map[string]any:
		return v["serfErrorInfo"] == string(want)
	default:
		return false
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
