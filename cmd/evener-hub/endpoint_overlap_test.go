package hub

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/hubendpoint"
)

// Both subprocesses run the real startup path. Only the registry's network
// access and background model prefetch are disabled; neither owns the endpoint.
func TestEndpointOverlappingStarterChild(t *testing.T) {
	role := os.Getenv("ENDPOINT_OVERLAP_ROLE")
	if role == "" {
		return
	}
	root := os.Getenv("ENDPOINT_OVERLAP_ROOT")
	state := filepath.Join(root, "hub-state")
	deps := defaultMainDeps()
	deps.loadRegistry = hermeticRegistryLoader
	deps.startLivePrefetch = func(context.Context, *hubcore.ProviderRegistry, *hubAuthController, func(func()), func()) {}
	deps.startLaunchPrefetch = func(context.Context, *WebServer, func(func())) {}
	switch role {
	case "loser":
		loadToken := deps.loadAuthToken
		deps.loadAuthToken = func(stateRoot string) (string, error) {
			if err := os.WriteFile(filepath.Join(root, "loser-reached-credentials"), []byte("reached"), 0600); err != nil {
				return "", err
			}
			return loadToken(stateRoot)
		}
	case "winner":
		deps.serve = func(ctx context.Context, srv hubHTTPServer) error {
			serving := make(chan error, 1)
			go func() { serving <- serveHub(ctx, srv) }()
			record, err := hubendpoint.Read(state)
			if err != nil {
				return err
			}
			origin, err := hubendpoint.ParseOrigin(record.LocalURL)
			if err != nil {
				return err
			}
			conn, err := net.DialTimeout("tcp", record.BoundAddr, 2*time.Second)
			if err != nil {
				return err
			}
			if err := hubendpoint.VerifyLocalConn(ctx, conn, origin, record); err != nil {
				_ = conn.Close()
				return err
			}
			_ = conn.Close()
			_, _ = fmt.Fprintln(os.Stdout, "READY")
			return <-serving
		}
	default:
		t.Fatalf("unexpected child role %q", role)
	}
	// Startup output includes an auth URL; never copy it into test output.
	err := runMain([]string{"--config", filepath.Join(root, "hub.toml")}, io.Discard, deps)
	if role == "loser" {
		if err == nil || !strings.Contains(err.Error(), "flock "+filepath.Join(state, "hub.lock")) {
			t.Fatalf("second startup was not rejected by the held lock: %v", err)
		}
		// A nonzero child exit is the expected refusal, not a fake success.
		t.Fatal("second startup refused by the held flock")
	}
	if err != nil {
		t.Fatalf("winner stopped: %v", err)
	}
}

func endpointOverlapEnv(root, role string) []string {
	env := []string{
		"HOME=" + filepath.Join(root, "home"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
		"XDG_STATE_HOME=" + filepath.Join(root, "state"),
		"XDG_CACHE_HOME=" + filepath.Join(root, "cache"),
		"TMPDIR=" + filepath.Join(root, "tmp"),
		"PATH=" + os.Getenv("PATH"),
		"ENDPOINT_OVERLAP_ROLE=" + role,
		"ENDPOINT_OVERLAP_ROOT=" + root,
	}
	// TestMain already warmed the binaries. Keep that evidence and the pinned
	// Go paths rather than rebuilding or downloading modules under fixture HOME.
	for _, key := range []string{"EVENER_HOST_TEMP_BASES", liveStackWarmEnv, "GOCACHE", "GOPATH", "GOMODCACHE", "GOROOT", "GOTOOLCHAIN"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func TestEndpointChildEnvironmentsPreserveWarmCaches(t *testing.T) {
	values := map[string]string{
		liveStackWarmEnv: "already-warmed",
		"GOCACHE":        filepath.Join(t.TempDir(), "build-cache"),
		"GOPATH":         filepath.Join(t.TempDir(), "go"),
		"GOMODCACHE":     filepath.Join(t.TempDir(), "module-cache"),
		"GOROOT":         filepath.Join(t.TempDir(), "toolchain"),
		"GOTOOLCHAIN":    "local",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	t.Run("fixture", func(t *testing.T) {
		endpointTestRoot(t)
		for key, want := range values {
			if got := os.Getenv(key); got != want {
				t.Errorf("fixture %s = %q, want %q", key, got, want)
			}
		}
	})
	for name, build := range map[string]func(string) []string{
		"overlap": func(root string) []string { return endpointOverlapEnv(root, "winner") },
		"crash":   endpointCrashEnv,
	} {
		t.Run(name, func(t *testing.T) {
			env := envMap(build(t.TempDir()))
			for key, want := range values {
				if got := env[key]; got != want {
					t.Errorf("child %s = %q, want %q", key, got, want)
				}
			}
		})
	}
}

func TestEndpointOverlappingRealStarters(t *testing.T) {
	root, state := endpointTestRoot(t)
	t.Cleanup(func() {
		if _, err := os.Lstat(filepath.Join(root, "home", "go", "pkg", "mod")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("child created a fixture module cache: %v", err)
		}
	})
	path := endpointTestConfig(t, root, state, "127.0.0.1:0")
	if err := os.MkdirAll(filepath.Join(root, "tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	const disposableToken = "disposable-overlap-test-token"
	tokenPath := filepath.Join(state, "auth-token")
	if err := os.WriteFile(tokenPath, []byte(disposableToken+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	winner := exec.Command(os.Args[0], "-test.run=^TestEndpointOverlappingStarterChild$")
	winner.Env = endpointOverlapEnv(root, "winner")
	winner.Stderr = io.Discard // The real startup log contains a disposable auth URL.
	pipe, err := winner.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := winner.Start(); err != nil {
		t.Fatal(err)
	}
	winnerDone := make(chan error, 1)
	go func() { winnerDone <- winner.Wait() }()
	winnerReaped := false
	t.Cleanup(func() {
		if !winnerReaped {
			_ = winner.Process.Kill()
			<-winnerDone
		}
	})
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(pipe).ReadString('\n')
		if err == nil && line != "READY\n" {
			err = fmt.Errorf("unexpected readiness marker %q", line)
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("winner never served verified health: %v", err)
		}
	case err := <-winnerDone:
		winnerReaped = true
		t.Fatalf("winner exited before serving: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("winner readiness timed out")
	}

	initial := endpointCommand(t, path)
	if initial.State != "running" || initial.Running == nil || initial.Running.PID != winner.Process.Pid {
		t.Fatalf("first process does not own the running endpoint: %+v", initial)
	}
	before, err := os.ReadFile(filepath.Join(state, "hub-endpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Lstat(filepath.Join(state, "hub-endpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	beforeToken, err := os.ReadFile(tokenPath)
	if err != nil || string(beforeToken) != disposableToken+"\n" {
		t.Fatalf("winner did not retain the disposable token: %v", err)
	}
	beforeTokenInfo, err := os.Lstat(tokenPath)
	if err != nil {
		t.Fatal(err)
	}

	// The loser is launched only after the first child's real listener answered
	// health, and the winner remains alive until the loser has exited.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	loser := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestEndpointOverlappingStarterChild$")
	loser.Env = endpointOverlapEnv(root, "loser")
	output, err := loser.CombinedOutput()
	if _, statErr := os.Lstat(filepath.Join(root, "loser-reached-credentials")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("loser reached credential loading: %v", statErr)
	}
	if ctx.Err() != nil {
		t.Fatalf("second real startup did not exit while first owner held the lock: %v", ctx.Err())
	}
	if err == nil || !strings.Contains(string(output), "second startup refused by the held flock") {
		t.Fatalf("second startup did not lose at the lock: exit %v, output %q", err, output)
	}
	current, err := os.ReadFile(filepath.Join(state, "hub-endpoint.json"))
	if err != nil || !bytes.Equal(before, current) {
		t.Fatalf("loser changed winner's published generation: %v", err)
	}
	currentInfo, err := os.Lstat(filepath.Join(state, "hub-endpoint.json"))
	if err != nil || !os.SameFile(beforeInfo, currentInfo) {
		t.Fatalf("loser replaced winner's record inode: %v", err)
	}
	currentToken, err := os.ReadFile(tokenPath)
	if err != nil || !bytes.Equal(beforeToken, currentToken) {
		t.Fatalf("loser changed winner's credential file: %v", err)
	}
	currentTokenInfo, err := os.Lstat(tokenPath)
	if err != nil || !os.SameFile(beforeTokenInfo, currentTokenInfo) {
		t.Fatalf("loser replaced winner's credential inode: %v", err)
	}
	select {
	case err := <-winnerDone:
		winnerReaped = true
		t.Fatalf("winner exited while competing startup ran: %v", err)
	default:
	}
	// Independently check the actual HTTP handler after the competing process
	// exited, not just the discovery command's interpretation of metadata.
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 2 * time.Second}
	response, err := client.Get(initial.Running.LocalURL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var health struct {
		BootID  string `json:"boot_id"`
		HubAddr string `json:"hub_addr"`
	}
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || health.BootID != initial.Running.BootID || health.HubAddr != initial.Running.BoundAddr {
		t.Fatalf("winner no longer serves its original identity: status=%d health=%+v", response.StatusCode, health)
	}
	if err := winner.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-winnerDone:
		winnerReaped = true
		if err != nil {
			t.Fatalf("winner graceful exit: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("winner shutdown timed out")
	}
	if _, err := os.Lstat(filepath.Join(state, "hub-endpoint.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("winner did not retire record on shutdown: %v", err)
	}
}
