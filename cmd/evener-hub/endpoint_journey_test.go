package hub

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/internal/hubendpoint"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// No TUI or autostart client participates: every hub and restart is explicitly
// owned by this test, while discovery and attachment run as the shipped CLI.
func TestEndpointJourneyDefaultConfigRestart(t *testing.T) {
	root, state := endpointTestRoot(t)
	bin := filepath.Join(liveStackBinaries(t, repoRootForTestMain()), "evener")
	config := endpointTestConfig(t, root, state, "0.0.0.0:0")
	contents, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, []byte("operator_marker = \"TASK8_CONFIG_72c9\"\n")...)
	config = DefaultConfigPath()
	if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, contents, 0600); err != nil {
		t.Fatal(err)
	}
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	providers := fmt.Sprintf("default = \"fake\"\n[providers.fake]\nbase = \"openai-compatible\"\nbase_url = %q\napi_key = \"fixture-only\"\n", provider.BaseURL())
	if err := os.WriteFile(filepath.Join(filepath.Dir(config), "providers.toml"), []byte(providers), 0600); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "projects", "endpoint-journey-0000000000")
	if err := os.MkdirAll(filepath.Join(project, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	id := hubtest.SessionID(t)
	const marker = "TASK8_SESSION_72c9"
	now := time.Unix(1700000000, 0).UTC()
	if err := schema.SaveSessionMeta(project, schema.SessionMeta{ID: id, Name: marker, ProfileID: "fake", Model: fakellm.ModelID, CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: root}}); err != nil {
		t.Fatal(err)
	}
	metaPath := filepath.Join(project, "sessions", id+".meta.json")
	meta, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}

	start := func(generation string) *exec.Cmd {
		t.Helper()
		logPath := filepath.Join(root, generation+".log")
		log, err := os.Create(logPath)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bin, "hub", "--evener", bin)
		cmd.Env = os.Environ() // Includes the TestMain warm marker and pinned Go caches.
		cmd.Stdout, cmd.Stderr = log, log
		if err := cmd.Start(); err != nil {
			_ = log.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
			_ = log.Close()
		})
		_ = awaitHubListening(t, logPath)
		return cmd
	}
	discover := func() hubendpoint.Report {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, bin, "hub", "endpoint", "--json").Output()
		if err != nil {
			t.Fatalf("endpoint subprocess: %v", err)
		}
		report, err := hubendpoint.DecodeReport(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		if report.State != "running" || report.Running == nil {
			t.Fatalf("discovery did not find live generation: %+v", report)
		}
		if report.ConfigPath != config || report.HubStateRoot != state || report.ConfiguredListenAddr != "0.0.0.0:0" || report.ListenSource != "config" || report.Running.RequestedListenAddr != "0.0.0.0:0" {
			t.Fatalf("selection changed: %+v", report)
		}
		_, port, err := net.SplitHostPort(report.Running.BoundAddr)
		if err != nil || port == "0" {
			t.Fatalf("actual binding=%q err=%v", report.Running.BoundAddr, err)
		}
		return report
	}
	read := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, "hub", "attach", "--stdio")
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		output, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		client := appwire.NewClient(appwire.NewStreamTransport(&endpointJourneyStdio{ReadCloser: output, WriteCloser: input}))
		defer func() { _ = client.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
		client.Start(ctx)
		if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatalf("attach initialize: %v", err)
		}
		result, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:" + id})
		if err != nil {
			t.Fatalf("persisted read through attach: %v", err)
		}
		if result.Thread.SessionID != id || result.Thread.Name != marker {
			t.Fatalf("wrong persisted session: %+v", result.Thread)
		}
		_ = client.Close()
		if err := cmd.Wait(); err != nil {
			t.Fatalf("attach exit: %v", err)
		}
	}
	first := start("first")
	before := discover()
	if before.Running.PID != first.Process.Pid {
		t.Fatal("discovered unowned PID")
	}
	read()
	token, err := os.ReadFile(filepath.Join(state, "auth-token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := first.Wait(); err != nil {
		t.Fatalf("selected shutdown: %v", err)
	}
	// Occupy the retired port, making stale-address reuse impossible.
	_, oldPort, _ := net.SplitHostPort(before.Running.BoundAddr)
	holder, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", oldPort))
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	second := start("second")
	after := discover()
	if after.Running.PID != second.Process.Pid || after.Running.PID == before.Running.PID || after.Running.BootID == before.Running.BootID || after.Running.BoundAddr == before.Running.BoundAddr {
		t.Fatal("restart did not rediscover a fresh process and socket")
	}
	read()
	for path, want := range map[string][]byte{config: contents, metaPath: meta, filepath.Join(state, "auth-token"): token} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("restart changed preserved file %s: %v", path, err)
		}
	}
	t.Logf("actual CLI journey: requested=0.0.0.0:0, bound=%s -> %s, authenticated seeded reads=2, config/session/token preserved", before.Running.BoundAddr, after.Running.BoundAddr)
}

type endpointJourneyStdio struct {
	io.ReadCloser
	io.WriteCloser
}

func (s *endpointJourneyStdio) Close() error { return s.WriteCloser.Close() }

func TestEndpointJourneyExplicitOriginIsolation(t *testing.T) {
	root, state := endpointTestRoot(t)
	bin := filepath.Join(liveStackBinaries(t, repoRootForTestMain()), "evener")
	config := endpointTestConfig(t, root, state, "127.0.0.1:0")
	const token = "TASK8_LOCAL_TOKEN_72c9"
	if err := os.WriteFile(filepath.Join(state, "auth-token"), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(http.StatusNoContent) }))
	defer target.Close()
	for _, origin := range []string{target.URL, strings.Replace(target.URL, "http:", "https:", 1)} {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		cmd := exec.CommandContext(ctx, bin, "hub", "attach", "--stdio", "--config", config, "--hub-url", origin)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		cancel()
		if err == nil || stdout.Len() != 0 {
			t.Fatalf("explicit origin unexpectedly attached: %s", origin)
		}
		if strings.Contains(stderr.String(), token) {
			t.Fatal("credential disclosed")
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("unselected origin received %d requests", requests.Load())
	}
	// A stopped selection must not start a child or mutate state to satisfy attach.
	for _, name := range []string{"hub.lock", "hub-endpoint.json"} {
		if _, err := os.Stat(filepath.Join(state, name)); !os.IsNotExist(err) {
			t.Fatalf("attach created %s: %v", name, err)
		}
	}
	got, err := os.ReadFile(filepath.Join(state, "auth-token"))
	if err != nil || string(got) != token {
		t.Fatal("attach changed selected token")
	}
}

func TestEndpointJourneyBearerOriginIsolation(t *testing.T) {
	const token = "TASK8_REMOTE_TOKEN_72c9"
	var selected, other, downgrade atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downgrade.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer plain.Close()
	unselected := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		other.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer unselected.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer "+token {
			selected.Add(1)
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, plain.URL, http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	origin, err := hubendpoint.ParseOrigin(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := hubendpoint.HTTPClientWithBearer(server.Client(), token, origin, nil)
	if err != nil {
		t.Fatal(err)
	}
	client.Timeout = 5 * time.Second
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || selected.Load() != 1 {
		t.Fatal("selected origin did not authenticate")
	}
	if resp, err = client.Get(unselected.URL); err == nil {
		_ = resp.Body.Close()
		t.Error("unselected origin was contacted")
	}
	if resp, err = client.Get(server.URL + "/redirect"); err == nil {
		_ = resp.Body.Close()
		t.Error("authenticated TLS downgrade was followed")
	}
	if selected.Load() != 2 || other.Load() != 0 || downgrade.Load() != 0 {
		t.Fatalf("request effects selected=%d other=%d downgrade=%d", selected.Load(), other.Load(), downgrade.Load())
	}
}
