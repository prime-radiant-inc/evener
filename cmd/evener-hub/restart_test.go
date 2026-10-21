package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/hubapi"
)

func TestHubRestartCommandWaitsForNewIncarnation(t *testing.T) {
	setBuild(t, "abc1234", "release")
	t.Cleanup(func() { hubUpdateMu = sync.Mutex{} })

	previousSupported := hubRestartSupported
	hubRestartSupported = func() bool { return true }
	t.Cleanup(func() { hubRestartSupported = previousSupported })

	previousPath := hubLaunchEntrypoint
	hubLaunchEntrypoint = writeProbeScript(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	t.Cleanup(func() { hubLaunchEntrypoint = previousPath })
	previousErr := hubLaunchEntrypointErr
	hubLaunchEntrypointErr = nil
	t.Cleanup(func() { hubLaunchEntrypointErr = previousErr })

	root := t.TempDir()
	token, err := hubedge.LoadOrCreateAuthToken(root)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	started := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	scheduled := false
	previousSchedule := scheduleHubRestartOnly
	scheduleHubRestartOnly = func(context.Context, restartPin, string, []string) {
		mu.Lock()
		started = started.Add(time.Second)
		scheduled = true
		mu.Unlock()
	}
	t.Cleanup(func() { scheduleHubRestartOnly = previousSchedule })

	mux := http.NewServeMux()
	appServer := newHubAppServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, appsource.NewRegistry())
	mux.HandleFunc("/rpc", appServer.ServeWebSocket)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		at := started
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(hubapi.HealthResponse{StartedAt: at})
	})
	srv := httptest.NewServer(hubedge.AuthGuard(token)(mux))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	var stdout, stderr bytes.Buffer
	deps := defaultMainDeps()
	deps.loadConfig = func(string, bool) (Config, error) {
		return Config{Addr: addr, HubStateRoot: root}, nil
	}
	deps.stdout = &stdout

	if err := runHubRestart([]string{"--addr", addr, "--timeout", "5s"}, &stderr, deps); err != nil {
		t.Fatalf("runHubRestart: %v (stderr %q)", err, stderr.String())
	}
	if !scheduled {
		t.Fatal("restart was not sent to the hub")
	}
	if !strings.Contains(stdout.String(), "abc1234") {
		t.Fatalf("stdout = %q, want the target build", stdout.String())
	}
}

func TestHubRestartSubcommandDispatch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"restart", "--not-a-flag"}, strings.NewReader(""), &stdout, &stderr); code == 0 {
		t.Fatal("restart with an unknown flag exited 0")
	}
	if stdout.Len() != 0 {
		t.Fatalf("usage error wrote to stdout: %q", stdout.String())
	}
}

func TestHubRestartCommandReportsRefusal(t *testing.T) {
	setBuild(t, "abc1234", "release")
	t.Cleanup(func() { hubUpdateMu = sync.Mutex{} })

	previousSupported := hubRestartSupported
	hubRestartSupported = func() bool { return false }
	t.Cleanup(func() { hubRestartSupported = previousSupported })

	previousPath := hubLaunchEntrypoint
	hubLaunchEntrypoint = writeProbeScript(t, "echo 'evener abc1234 (2026-10-21T00:00:00Z)'")
	t.Cleanup(func() { hubLaunchEntrypoint = previousPath })
	previousErr := hubLaunchEntrypointErr
	hubLaunchEntrypointErr = nil
	t.Cleanup(func() { hubLaunchEntrypointErr = previousErr })

	root := t.TempDir()
	token, err := hubedge.LoadOrCreateAuthToken(root)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	appServer := newHubAppServer(hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, appsource.NewRegistry())
	mux.HandleFunc("/rpc", appServer.ServeWebSocket)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(hubapi.HealthResponse{StartedAt: time.Now().UTC()})
	})
	srv := httptest.NewServer(hubedge.AuthGuard(token)(mux))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	deps := defaultMainDeps()
	deps.loadConfig = func(string, bool) (Config, error) {
		return Config{Addr: addr, HubStateRoot: root}, nil
	}
	deps.stdout = io.Discard
	var stderr bytes.Buffer
	if err := runHubRestart([]string{"--addr", addr, "--timeout", "2s"}, &stderr, deps); err == nil {
		t.Fatal("runHubRestart accepted a refused restart")
	}
}

func TestWaitForNewIncarnationTimesOut(t *testing.T) {
	fixed := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(hubapi.HealthResponse{StartedAt: fixed})
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := waitForNewIncarnation(ctx, addr, "", fixed); err == nil {
		t.Fatal("waitForNewIncarnation accepted an unchanged process")
	}
}

func TestIsAmbiguousRestartFailure(t *testing.T) {
	if !isAmbiguousRestartFailure(appwire.TransportFailureError{}) {
		t.Fatal("transport failure was treated as a refusal")
	}
	if isAmbiguousRestartFailure(appwire.WireError{}) {
		t.Fatal("server refusal was treated as ambiguous")
	}
}
