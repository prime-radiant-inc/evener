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
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
	"primeradiant.com/evener/rendezvous"
)

// This is a real DOM driver test, separate from the production-app E2E below.
func TestOnboardingEvidenceButtonLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome")
	}
	cmd := exec.CommandContext(t.Context(), "node", "frontend/scripts/onboardingguard/button-browser.mjs", t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("button lifecycle: %v\n%s", err, out)
	}
}

// TestOnboardingBrowser drives embedded production UI, real AppWire and the
// production HubSpawner/daemon. Only the external HTTP provider is scripted.
func TestOnboardingBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Chrome and production frontend")
	}
	requireFreshDist(t)
	// Read before isolation. Only video and sanitized visual evidence escape TempDir.
	recordVideo := os.Getenv("EVENER_ONBOARDING_VIDEO")
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
		receivedAt := time.Now()
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
		req["receivedAt"] = receivedAt.UTC().Format(time.RFC3339Nano)
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
	var launches []time.Time
	var childMu sync.Mutex
	daemonProcessCommand = func(bin string, args, env []string) *exec.Cmd {
		attempted := time.Now()
		cmd := oldCommand(bin, args, env)
		childMu.Lock()
		children = append(children, cmd)
		launches = append(launches, attempted)
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
				killed := false
				for !errors.Is(cmd.Process.Signal(syscall.Signal(0)), os.ErrProcessDone) {
					if time.Now().After(deadline) {
						if killed {
							t.Errorf("daemon %d not reaped after kill", cmd.Process.Pid)
							break
						}
						killed = true
						deadline = time.Now().Add(5 * time.Second)
						_ = cmd.Process.Kill()
						t.Errorf("daemon %d failed graceful shutdown", cmd.Process.Pid)
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
		childMu.Lock()
		attempts := len(launches)
		childMu.Unlock()
		if err != nil || len(entries) != 0 || len(requests) != 0 || attempts != 0 {
			http.Error(w, "session or generation before Start", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]int{"checks": checks, "sessions": len(entries), "generations": len(requests)})
	}))
	defer checkpoint.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "scripts/onboardingguard/run.mjs", "--url", hubedge.AuthURLFor(hub.URL, token), "--artifact-dir", root, "--work", work, "--checkpoint", checkpoint.URL)
	if recordVideo != "" {
		cmd.Args = append(cmd.Args, "--record-video", recordVideo)
	}
	cmd.Dir = filepath.Join(repo, "cmd/evener-hub/frontend")
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 15 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	}
	t.Logf("browser: %s", out)
	entries, err := rendezvous.List(run)
	if err != nil {
		t.Fatal(err)
	}
	stopChildren()
	if len(children) != 2 {
		t.Fatalf("spawned %d daemons, want task and blank sessions", len(children))
	}
	mu.Lock()
	defer mu.Unlock()
	if checks < 2 {
		t.Errorf("model-list requests=%d, want failure and retry", checks)
	}
	var starts []time.Time
	var refs []string
	for _, milestone := range skillGuardReadMilestones(t, filepath.Join(root, "milestones.jsonl")) {
		switch milestone.Milestone {
		case "start-activated":
			var detail struct {
				At float64 `json:"activatedAt"`
			}
			if err := json.Unmarshal(milestone.Detail, &detail); err != nil {
				t.Fatal(err)
			}
			starts = append(starts, time.Unix(0, int64(detail.At*1e6)))
		case "session-observed":
			var detail struct {
				Ref string `json:"ref"`
			}
			if err := json.Unmarshal(milestone.Detail, &detail); err != nil {
				t.Fatal(err)
			}
			refs = append(refs, detail.Ref)
		}
	}
	if len(refs) != 2 || refs[0] == refs[1] {
		t.Fatalf("browser session identities: %v", refs)
	}
	var generations []time.Time
	var turns []map[string]any
	for _, req := range requests {
		received, err := time.Parse(time.RFC3339Nano, req["receivedAt"].(string))
		if err != nil {
			t.Fatal(err)
		}
		generations = append(generations, received)
		if req["stream"] == true {
			turns = append(turns, req)
		}
	}
	if err := onboardingStarts(starts, launches, generations); err != nil {
		t.Fatal(err)
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

	for i, ref := range refs {
		var entry rendezvous.Entry
		for _, candidate := range entries {
			if candidate.PID == children[i].Process.Pid {
				entry = candidate
			}
		}
		if entry.SessionID == "" {
			t.Fatal("spawned daemon missing rendezvous metadata")
		}
		path := filepath.Join(entry.StateDir, "sessions", entry.SessionID+".transcript.jsonl")
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 4096), 4*1024*1024)
		var input, reply, finished bool
		userTurns := 0
		var header transcript.Header
		for scanner.Scan() {
			var record struct {
				Kind string      `json:"kind"`
				Turn schema.Turn `json:"turn"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			if record.Kind == "header" {
				if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
					t.Fatal(err)
				}
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
			t.Fatal(err)
		}
		if err := onboardingIdentity(ref, work, entry, header); err != nil {
			t.Fatalf("launch %d: %v", i, err)
		}
		if i == 0 && !(input && reply && finished && userTurns == 1) {
			t.Fatal("active task session missing completed input/reply")
		}
		if i == 1 && (userTurns != 0 || reply || finished) {
			t.Fatal("active blank session contains user work")
		}
	}

	t.Logf("verified model-list retry (%d requests), no generation/session before Start, one routed generation and durable input/reply", checks)
}

var errOnboardingIdentity = errors.New("onboarding identity mismatch")
var errOnboardingDirectory = errors.New("onboarding directory mismatch")
var errOnboardingStart = errors.New("onboarding operation before activation")

func onboardingIdentity(ref, cwd string, entry rendezvous.Entry, header transcript.Header) error {
	if ref != "local:"+entry.SessionID || header.SessionID != entry.SessionID {
		return errOnboardingIdentity
	}
	if entry.WorkingDir != cwd || header.WorkingDir != cwd {
		return errOnboardingDirectory
	}
	return nil
}
func onboardingStarts(starts, launches, generations []time.Time) error {
	if len(starts) != 2 || len(launches) != 2 || !starts[1].After(starts[0]) {
		return errOnboardingStart
	}
	for i, launch := range launches {
		if launch.Before(starts[i]) || (i == 0 && !launch.Before(starts[1])) {
			return errOnboardingStart
		}
	}
	for _, generation := range generations {
		if generation.Before(starts[0]) {
			return errOnboardingStart
		}
	}
	return nil
}

func TestOnboardingEvidenceRejectsMisrouting(t *testing.T) {
	entry := rendezvous.Entry{SessionID: "fixture-a", WorkingDir: "/fixture/project"}
	header := transcript.Header{SessionID: "fixture-a", WorkingDir: "/fixture/project"}
	for _, tc := range []struct {
		name, ref, cwd string
		want           error
	}{
		{"matching", "local:fixture-a", "/fixture/project", nil},
		{"wrong-session", "local:fixture-b", "/fixture/project", errOnboardingIdentity},
		{"wrong-directory", "local:fixture-a", "/fixture/other", errOnboardingDirectory},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := onboardingIdentity(tc.ref, tc.cwd, entry, header); !errors.Is(err, tc.want) {
				t.Fatalf("rejection=%v want=%v", err, tc.want)
			}
		})
	}
}
func TestOnboardingEvidenceRejectsPreStartObservation(t *testing.T) {
	at := time.Unix(100, 0)
	starts := []time.Time{at, at.Add(time.Second)}
	for _, tc := range []struct {
		name                  string
		launches, generations []time.Time
		want                  error
	}{
		{"authorized", []time.Time{at.Add(time.Millisecond), at.Add(2 * time.Second)}, []time.Time{at.Add(time.Millisecond)}, nil},
		// No rendezvous is supplied: an attempt alone must be rejected.
		{"pre-rendezvous-launch", []time.Time{at.Add(-time.Millisecond), at.Add(2 * time.Second)}, nil, errOnboardingStart},
		{"second-before-activation", []time.Time{at.Add(time.Millisecond), at.Add(time.Second - time.Millisecond)}, nil, errOnboardingStart},
		{"generation-before-activation", []time.Time{at.Add(time.Millisecond), at.Add(2 * time.Second)}, []time.Time{at.Add(-time.Millisecond)}, errOnboardingStart},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := onboardingStarts(starts, tc.launches, tc.generations); !errors.Is(err, tc.want) {
				t.Fatalf("rejection=%v want=%v", err, tc.want)
			}
		})
	}
}
