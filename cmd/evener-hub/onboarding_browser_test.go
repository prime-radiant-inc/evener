//go:build browserguard

package hub

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
	"primeradiant.com/evener/rendezvous"
)

// TestOnboardingBrowser drives embedded production UI, real AppWire and the
// production HubSpawner/daemon. Only the external HTTP provider is scripted.
func TestOnboardingBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome and production frontend")
	}
	requireFreshDist(t)
	root := t.TempDir()
	repo := skillGuardRepoRoot(t)
	binary := filepath.Join(root, "evener")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/evener")
	build.Dir = repo
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	// Serial: environment is process-wide. Clear inherited access/config, keeping
	// only tool discovery and Go caches. No personal provider or OAuth state.
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		switch key {
		case "PATH", "TMPDIR", "GOCACHE", "GOMODCACHE", "GOROOT", "CGO_ENABLED":
		default:
			t.Setenv(key, "")
		}
	}
	for key, value := range map[string]string{"HOME": filepath.Join(root, "home"), "XDG_CONFIG_HOME": filepath.Join(root, "config"), "XDG_STATE_HOME": filepath.Join(root, "state"), "XDG_CACHE_HOME": filepath.Join(root, "cache"), "EVENER_PROVIDERS_CONFIG": filepath.Join(root, "providers.toml"), "EVENER_CREDENTIALS_CONFIG": filepath.Join(root, "credentials.toml")} {
		t.Setenv(key, value)
	}
	work := filepath.Join(root, "project")
	run := filepath.Join(root, "run")
	for _, dir := range []string{work, run, os.Getenv("HOME")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	var requests []map[string]any
	checks := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/unavailable") {
			http.Error(w, "fixture local discovery unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get("Authorization") != "Bearer onboarding-fixture-key-not-a-secret" {
			t.Error("provider request did not carry the credential saved through the UI")
		}
		if r.URL.Path == "/v1/models" {
			if r.Method != http.MethodGet || r.ContentLength > 0 {
				t.Error("model-list check must be a bodyless GET")
			}
			checks++
			if checks == 1 {
				http.Error(w, `{"error":{"message":"fixture unavailable"}}`, http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o-mini","object":"model","owned_by":"onboarding"},{"id":"gpt-4o","object":"model","owned_by":"onboarding"}]}`)
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected provider route %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		req["receivedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
		requests = append(requests, req)
		if req["stream"] != true {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"fixture","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ONBOARDING_METADATA_72e"},"finish_reason":"stop"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		args, _ := json.Marshal(map[string]any{"message": "ONBOARDING_REPLY_72e", "end_turn": true, "output": map[string]any{"message": "", "data": map[string]any{}, "artifacts": []string{}}})
		chunk, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "onboarding-complete", "type": "function", "function": map[string]any{"name": "communicate", "arguments": string(args)}}}}, "finish_reason": nil}}})
		fmt.Fprintf(w, "data: %s\n\ndata: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", chunk)
	}))
	defer provider.Close()
	t.Setenv("OLLAMA_BASE_URL", provider.URL+"/unavailable")
	t.Setenv("EVENER_OFFLINE", "1")
	// An endpoint definition is not configured access: no key/default/model is
	// seeded. The browser must save access through the real connection dialog.
	config := fmt.Sprintf("[providers.openai]\nprotocol = \"openai-chat\"\nbase_url = %q\n", provider.URL+"/v1")
	if err := os.WriteFile(os.Getenv("EVENER_PROVIDERS_CONFIG"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	reg := hubcore.NewProviderRegistry(func(opts ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		return cmdutil.LoadRegistry(append(opts, registry.WithOffline(true))...)
	})
	if err := reg.Reload(); err != nil {
		t.Fatal(err)
	}
	_, store, err := cmdutil.LoadRegistry(registry.WithOffline(true))
	if err != nil {
		t.Fatal(err)
	}
	state := cmdutil.DefaultStateRoot()
	roster := hubcore.NewRoster(run, &hubcore.StatusProber{Timeout: time.Second})
	past := hubcore.NewPastIndex(filepath.Join(state, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	const token = "onboarding-disposable-auth"
	spawner := &HubSpawner{EvenerBinary: binary, RunDir: run, HubToken: token, Registry: reg, ProvidersConfigPath: os.Getenv("EVENER_PROVIDERS_CONFIG"), CredentialsPath: os.Getenv("EVENER_CREDENTIALS_CONFIG")}
	web := newWebServer(hubcore.WebConfig{AuthToken: token, HubStateRoot: filepath.Join(root, "hub"), LaunchConfigRoot: filepath.Join(root, "launch"), RunDir: run, StateDir: state, Past: past, Roster: roster, Spawner: spawner, Registry: reg, CredsStore: store, ProvidersConfigPath: spawner.ProvidersConfigPath, CredentialsPath: spawner.CredentialsPath}, nil)
	past.SetOnChange(func() { web.navigation.Invalidate(navigationChangeHint{}) })
	roster.SetOnChange(func() { web.navigation.Invalidate(navigationChangeHint{}) })
	hub := httptest.NewServer(web.Handler())
	defer hub.Close()
	// Observe actual child launch commands so every spawned process is reaped on
	// every failure path, without touching unrelated rendezvous/service roots.
	oldCommand := daemonProcessCommand
	var children []*exec.Cmd
	var childMu sync.Mutex
	daemonProcessCommand = func(bin string, args, env []string) *exec.Cmd {
		cmd := oldCommand(bin, args, env)
		childMu.Lock()
		children = append(children, cmd)
		childMu.Unlock()
		return cmd
	}
	var stopOnce sync.Once
	stopChildren := func() {
		stopOnce.Do(func() {
			childMu.Lock()
			defer childMu.Unlock()
			for _, cmd := range children {
				if cmd.Process == nil {
					continue
				}
				_ = cmd.Process.Signal(syscall.SIGTERM)
				deadline := time.Now().Add(10 * time.Second)
				for !errors.Is(cmd.Process.Signal(syscall.Signal(0)), os.ErrProcessDone) {
					if time.Now().After(deadline) {
						_ = cmd.Process.Kill()
						t.Errorf("daemon %d failed graceful shutdown", cmd.Process.Pid)
						break
					}
					time.Sleep(10 * time.Millisecond) // production owns cmd.Wait, observe its reaping
				}
			}
		})
	}
	defer func() { stopChildren(); daemonProcessCommand = oldCommand }()

	// The driver's checkpoint is a fixture channel, not a frontend mock. It asks
	// Go to inspect the real provider log and rendezvous before Start.
	checkpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		entries, err := rendezvous.List(run)
		if err != nil || len(entries) != 0 || len(requests) != 0 {
			http.Error(w, "session or generation before Start", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]int{"checks": checks, "sessions": len(entries), "generations": len(requests)})
	}))
	defer checkpoint.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "scripts/onboardingguard/run.mjs", "--url", hubedge.AuthURLFor(hub.URL, token), "--artifact-dir", root, "--work", work, "--checkpoint", checkpoint.URL)
	cmd.Dir = filepath.Join(repo, "cmd/evener-hub/frontend")
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 15 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	}
	t.Logf("browser: %s", out)
	stopChildren()
	if len(children) != 2 {
		t.Fatalf("spawned %d daemons, want task and blank sessions", len(children))
	}
	mu.Lock()
	defer mu.Unlock()
	if checks < 2 {
		t.Errorf("model-list requests=%d, want failure and retry", checks)
	}
	var start time.Time
	for _, milestone := range skillGuardReadMilestones(t, filepath.Join(root, "milestones.jsonl")) {
		if milestone.Milestone == "before-start" {
			start, err = time.Parse(time.RFC3339Nano, milestone.At)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if start.IsZero() {
		t.Fatal("missing explicit Start milestone")
	}
	var turns []map[string]any
	for _, req := range requests {
		received, err := time.Parse(time.RFC3339Nano, req["receivedAt"].(string))
		if err != nil || received.Before(start) {
			t.Fatal("generation preceded explicit Start")
		}
		if req["stream"] == true {
			turns = append(turns, req)
		}
	}
	if len(turns) != 1 {
		t.Fatalf("streamed user turns=%d, want 1 (total generations=%d)", len(turns), len(requests))
	}
	if turns[0]["model"] != "gpt-4o-mini" {
		t.Errorf("model=%v", turns[0]["model"])
	}
	encoded, _ := json.Marshal(turns[0]["messages"])
	if !strings.Contains(string(encoded), "ONBOARDING_TASK_72e") {
		t.Error("provider request lost draft")
	}

	var completed, empty int
	err = filepath.WalkDir(state, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".transcript.jsonl") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 4096), 4*1024*1024)
		var input, reply, finished bool
		userTurns := 0
		for scanner.Scan() {
			var record struct {
				Kind string      `json:"kind"`
				Turn schema.Turn `json:"turn"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
				return err
			}
			if record.Kind != "entry" {
				continue
			}
			if record.Turn.Kind == schema.TurnUserInput {
				userTurns++
				input = input || record.Turn.Message.Text() == "ONBOARDING_TASK_72e"
			}
			if c := record.Turn.Communicate; c != nil {
				reply = reply || (c.Message == "ONBOARDING_REPLY_72e" && c.EndTurn)
			}
			if c := record.Turn.Completion; c != nil {
				finished = finished || c.Status == schema.TurnCompleted
			}
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		if input && reply && finished && userTurns == 1 {
			completed++
		}
		if userTurns == 0 && !reply && !finished {
			empty++
		}
		return nil
	})
	if err != nil || completed != 1 || empty != 1 {
		t.Fatalf("durable completed=%d blank=%d err=%v", completed, empty, err)
	}

	t.Logf("verified model-list retry (%d requests), no generation/session before Start, one routed generation and durable input/reply", checks)
}
