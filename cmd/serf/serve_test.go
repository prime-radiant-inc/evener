package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/mcpconfig"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/skill"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
	"primeradiant.com/serf/rendezvous"
)

// TestBuildInitialProfile_ConfigPath verifies that buildInitialProfile resolves
// a custom instance name (e.g. "work" defined in providers.toml) to a profile
// whose ID matches the instance name, not the provider type.
func TestBuildInitialProfile_ConfigPath(t *testing.T) {
	cfg := providercfg.Config{
		Default: "work",
		Instances: []providercfg.InstanceConfig{
			{Name: "work", Type: "openai"},
		},
	}
	profile, err := buildInitialProfile(cfg, cmdutil.ModelRef{Provider: "work", Model: "gpt-4o"}, "")
	if err != nil {
		t.Fatalf("buildInitialProfile: %v", err)
	}
	if profile.ID() != "work" {
		t.Fatalf("profile.ID() = %q, want %q", profile.ID(), "work")
	}
}

// TestBuildInitialProfile_ConfigPathInvalidOutputSchema verifies that an invalid
// --output-schema returns an error.
func TestBuildInitialProfile_ConfigPathInvalidOutputSchema(t *testing.T) {
	cfg := providercfg.Config{
		Default: "work",
		Instances: []providercfg.InstanceConfig{
			{Name: "work", Type: "openai"},
		},
	}
	_, err := buildInitialProfile(cfg, cmdutil.ModelRef{Provider: "work", Model: "gpt-4o"}, "{not json")
	if err == nil {
		t.Fatal("expected error for invalid --output-schema JSON")
	}
	if !strings.Contains(err.Error(), "invalid --output-schema") {
		t.Fatalf("error=%q, want to contain 'invalid --output-schema'", err.Error())
	}
}

// TestBuildInitialProfile_UnknownInstanceError verifies that an unknown
// instance name returns the expected error.
func TestBuildInitialProfile_UnknownInstanceError(t *testing.T) {
	cfg := providercfg.Config{
		Default: "work",
		Instances: []providercfg.InstanceConfig{
			{Name: "work", Type: "openai"},
		},
	}
	_, err := buildInitialProfile(cfg, cmdutil.ModelRef{Provider: "unknown", Model: "gpt-4o"}, "")
	if err == nil {
		t.Fatal("expected error for unknown instance name")
	}
	if !strings.Contains(err.Error(), "unknown instance") {
		t.Fatalf("error=%q, want to contain 'unknown instance'", err.Error())
	}
}

// TestBuildInitialProfile_MaterializedInstance verifies that buildInitialProfile
// resolves a type-named instance (e.g. "openai/gpt-5") through the config path,
// matching the contract that LoadClient materializes a config before callers see it.
func TestBuildInitialProfile_MaterializedInstance(t *testing.T) {
	// Simulate a materialized config where the instance name equals the type name,
	// which is what materializeProvidersConfig produces.
	cfg := providercfg.Config{
		Default: "openai",
		Instances: []providercfg.InstanceConfig{
			{Name: "openai", Type: "openai"},
		},
	}
	profile, err := buildInitialProfile(cfg, cmdutil.ModelRef{Provider: "openai", Model: "gpt-5"}, "")
	if err != nil {
		t.Fatalf("buildInitialProfile: %v", err)
	}
	if profile.ID() != "openai" {
		t.Fatalf("profile.ID() = %q, want %q", profile.ID(), "openai")
	}
}

func TestRunServe_BareModelRejected(t *testing.T) {
	err := runServe([]string{"--model", "gpt-5.2"})
	if err == nil {
		t.Fatal("expected error for bare model")
	}
	if got := err.Error(); got != `model "gpt-5.2" must use provider/model` {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestRunServe_MissingModel verifies runServe returns an error when no
// --model flag is set and SERF_MODEL is unset.
func TestRunServe_MissingModel(t *testing.T) {
	old := os.Getenv("SERF_MODEL")
	if err := os.Unsetenv("SERF_MODEL"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if old != "" {
			os.Setenv("SERF_MODEL", old)
		}
	}()

	err := runServe(nil)
	if err == nil {
		t.Fatal("expected error for missing model")
	}
	if got := err.Error(); got != "no model: use --model provider/model or set SERF_MODEL=provider/model" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrintServeEnvVars_IncludesOpenAIResponsesContinuation(t *testing.T) {
	var b strings.Builder
	printServeEnvVars(&b)
	if !strings.Contains(b.String(), envvars.SERFOpenAIResponsesContinuation.Name) {
		t.Fatalf("serve env help missing %s: %s", envvars.SERFOpenAIResponsesContinuation.Name, b.String())
	}
}

func TestServe_WritesAndRemovesRendezvousFile(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	if os.Getenv("SERF_LIVE_TESTS") != "1" {
		t.Skip("set SERF_LIVE_TESTS=1 to run live serve integration test")
	}
	if os.Getenv("OPENAI_API_KEY") == "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("requires an LLM API key for serve startup")
	}

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	args := []string{
		"--model", os.Getenv("SERF_TEST_PROVIDER") + "/" + os.Getenv("SERF_TEST_MODEL"),
		"--addr", "127.0.0.1:0",
		"--dir", t.TempDir(),
	}
	if os.Getenv("SERF_TEST_PROVIDER") == "" || os.Getenv("SERF_TEST_MODEL") == "" {
		t.Skip("set SERF_TEST_PROVIDER and SERF_TEST_MODEL to run this test")
	}

	done := make(chan error, 1)
	go func() {
		done <- runServe(args)
	}()

	runDir := filepath.Join(tmpHome, ".serf", "run")
	pid := os.Getpid()
	target := filepath.Join(runDir, strconv.Itoa(pid)+".json")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(target); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("rendezvous file %s was not created: %v", target, err)
	}

	entries, err := rendezvous.List(runDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("rendezvous.List returned err=%v entries=%v", err, entries)
	}
	if entries[0].SpawnedBy != "user" {
		t.Errorf("SpawnedBy: got %q, want %q", entries[0].SpawnedBy, "user")
	}
	if entries[0].Address == "" {
		t.Error("Address should not be empty")
	}

	resp, err := http.Post("http://"+entries[0].Address+"/shutdown", "", nil)
	if err != nil {
		t.Fatalf("post /shutdown: %v", err)
	}
	resp.Body.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runServe did not exit after /shutdown")
	}

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("rendezvous file should be removed, stat err=%v", err)
	}
}

func TestRunServeNonInteractiveFlagControlsPromptAddendum(t *testing.T) {
	oldLoadClient := serveLoadClient
	serveLoadClient = func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
		client := llm.NewClient()
		client.Register(serveLoggingAdapter{})
		cfg := providercfg.Config{
			Default: "openai",
			Instances: []providercfg.InstanceConfig{
				{Name: "openai", Type: "openai"},
			},
		}
		return client, cfg, true, nil
	}
	t.Cleanup(func() {
		serveLoadClient = oldLoadClient
	})

	for _, tc := range []struct {
		name string
		flag bool
		want bool
	}{
		{name: "default interactive", flag: false, want: false},
		{name: "explicit non-interactive", flag: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runDir := t.TempDir()
			stateDir := t.TempDir()
			args := []string{
				"--model", "openai/gpt-5.2",
				"--addr", "127.0.0.1:0",
				"--dir", t.TempDir(),
				"--state-dir", stateDir,
				"--run-dir", runDir,
			}
			if tc.flag {
				args = append(args, "--non-interactive")
			}

			done := make(chan error, 1)
			go func() {
				done <- runServe(args)
			}()

			entry := waitForServeTestRendezvous(t, runDir)
			resp, err := http.Post("http://"+entry.Address+"/shutdown", "", nil)
			if err != nil {
				t.Fatalf("post /shutdown: %v", err)
			}
			resp.Body.Close()

			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("runServe: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("runServe did not exit after /shutdown")
			}

			path := filepath.Join(stateDir, "sessions", entry.SessionID+".transcript.jsonl")
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("open transcript: %v", err)
			}
			var header transcript.Header
			derr := json.NewDecoder(f).Decode(&header)
			f.Close()
			if derr != nil {
				t.Fatalf("decode transcript header: %v", derr)
			}
			got := strings.Contains(header.SystemPrompt, "Non-interactive mode")
			if got != tc.want {
				t.Fatalf("non-interactive prompt addendum present=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunServeShutdownWaitsForInFlightInput(t *testing.T) {
	adapter := &shutdownBlockingAdapter{
		entered:   make(chan struct{}, 1),
		cancelled: make(chan struct{}, 1),
		release:   make(chan struct{}),
	}
	var releaseOnce sync.Once
	releaseAdapter := func() {
		releaseOnce.Do(func() {
			close(adapter.release)
		})
	}
	t.Cleanup(releaseAdapter)

	oldLoadClient := serveLoadClient
	serveLoadClient = func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
		client := llm.NewClient()
		client.Register(adapter)
		cfg := providercfg.Config{
			Default: "openai",
			Instances: []providercfg.InstanceConfig{
				{Name: "openai", Type: "openai"},
			},
		}
		return client, cfg, true, nil
	}
	t.Cleanup(func() {
		serveLoadClient = oldLoadClient
	})

	runDir := t.TempDir()
	args := []string{
		"--model", "openai/gpt-5.2",
		"--addr", "127.0.0.1:0",
		"--dir", t.TempDir(),
		"--state-dir", t.TempDir(),
		"--run-dir", runDir,
	}

	done := make(chan error, 1)
	go func() {
		done <- runServe(args)
	}()

	entry := waitForServeTestRendezvous(t, runDir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	transport, err := appwire.DialWebSocket(ctx, "ws://"+entry.Address+"/rpc", http.DefaultClient)
	if err != nil {
		t.Fatalf("DialWebSocket: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.WithoutCancel(ctx))
	defer client.Close()

	if _, err := client.Initialize(ctx, appwire.InitializeParams{
		ClientInfo: appwire.ClientInfo{Name: "serve-shutdown-test", Version: "test"},
	}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	ref := appwire.Ref{SourceID: "local", ThreadID: entry.SessionID}.String()
	if _, err := client.TurnStart(ctx, appwire.TurnStartParams{
		Ref:   ref,
		Input: []appwire.InputItem{{Type: "text", Text: "stay busy until shutdown"}},
	}); err != nil {
		t.Fatalf("TurnStart: %v", err)
	}

	select {
	case <-adapter.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("fake adapter was not called")
	}

	resp, err := http.Post("http://"+entry.Address+"/shutdown", "", nil)
	if err != nil {
		t.Fatalf("post /shutdown: %v", err)
	}
	resp.Body.Close()

	select {
	case <-adapter.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight input was not cancelled by shutdown")
	}

	select {
	case err := <-done:
		t.Fatalf("runServe returned before in-flight input exited: %v", err)
	case <-time.After(250 * time.Millisecond):
	}

	releaseAdapter()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServe: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runServe did not exit after in-flight input was released")
	}
}

func waitForServeTestRendezvous(t *testing.T, runDir string) rendezvous.Entry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := rendezvous.List(runDir)
		for _, entry := range entries {
			if entry.Address != "" && entry.SessionID != "" {
				return entry
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no rendezvous entry in %s", runDir)
	return rendezvous.Entry{}
}

func TestServeClient_APILogWritesJSONL(t *testing.T) {
	stateDir := t.TempDir()
	called := make(chan struct{}, 1)

	oldLoadClient := serveLoadClient
	serveLoadClient = func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
		client := llm.NewClient()
		client.Register(serveLoggingAdapter{called: called})
		return client, providercfg.Config{}, false, nil
	}
	t.Cleanup(func() {
		serveLoadClient = oldLoadClient
	})

	client, _, _, closeAPILog, err := newServeLLMClient(stateDir, nil)
	if err != nil {
		t.Fatalf("newServeLLMClient: %v", err)
	}
	_, err = client.Complete(llm.WithAPILogContext(context.Background(), "serve-session", 4), llm.Request{
		Provider: "openai",
		Model:    "gpt-test",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := closeAPILog(); err != nil {
		t.Fatalf("closeAPILog: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(stateDir, "api.jsonl"))
	if err != nil {
		t.Fatalf("read api.jsonl: %v", err)
	}
	for _, want := range []string{`"provider":"openai"`, `"model":"gpt-test"`, `"session_id":"serve-session"`, `"round":4`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("api.jsonl missing %s:\n%s", want, string(data))
		}
	}
	select {
	case <-called:
	default:
		t.Fatal("fake adapter was not called")
	}
}

type serveLoggingAdapter struct {
	called chan<- struct{}
}

func (a serveLoggingAdapter) Name() string { return "openai" }

func (a serveLoggingAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	select {
	case a.called <- struct{}{}:
	default:
	}
	return llm.Response{
		Provider: req.Provider,
		Model:    req.Model,
		Message:  llm.Assistant("ok"),
		Finish:   llm.FinishReason{Reason: llm.FinishReasonStop},
		Usage:    llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	}, nil
}

func (a serveLoggingAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, io.EOF
}

type shutdownBlockingAdapter struct {
	entered   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
}

func (a *shutdownBlockingAdapter) Name() string { return "openai" }

func (a *shutdownBlockingAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	select {
	case a.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	select {
	case a.cancelled <- struct{}{}:
	default:
	}
	<-a.release
	return llm.Response{
		Provider: req.Provider,
		Model:    req.Model,
		Message:  llm.Assistant("ok"),
		Finish:   llm.FinishReason{Reason: llm.FinishReasonStop},
		Usage:    llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	}, ctx.Err()
}

func (a *shutdownBlockingAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func TestAgentToServerDetailedStatus_Empty(t *testing.T) {
	got := agentToServerDetailedStatus(agent.DetailedStatus{})
	if len(got.Tools) != 0 {
		t.Errorf("Tools = %d, want 0", len(got.Tools))
	}
	if len(got.MCP) != 0 {
		t.Errorf("MCP = %d, want 0", len(got.MCP))
	}
	if len(got.Skills) != 0 {
		t.Errorf("Skills = %d, want 0", len(got.Skills))
	}
	if len(got.Plugins) != 0 {
		t.Errorf("Plugins = %d, want 0", len(got.Plugins))
	}
	if len(got.Hooks) != 0 {
		t.Errorf("Hooks = %d, want 0", len(got.Hooks))
	}
	if len(got.Jobs) != 0 {
		t.Errorf("Jobs = %d, want 0", len(got.Jobs))
	}
	if len(got.Agents) != 0 {
		t.Errorf("Agents = %d, want 0", len(got.Agents))
	}
}

func TestAgentToServerDetailedStatus_Partial(t *testing.T) {
	exitCode := 42
	ds := agent.DetailedStatus{
		Tools:   []agent.ToolInfo{{Name: "shell", Source: "core"}},
		MCP:     []mcpconfig.ServerInfo{{Name: "test-server", Tools: []string{"tool1"}}},
		Skills:  []skill.SkillMeta{{Name: "test-skill", Description: "A test skill"}},
		Plugins: []agent.PluginInfo{{Name: "test-plugin", Version: "1.0.0", SkillCount: 2, AgentCount: 1, HookCount: 3, MCPCount: 0}},
		Hooks:   map[plugin.HookEvent]int{"PreToolUse": 1},
		Jobs:    []agent.JobStatusInfo{{JobID: "job1", JobType: "delegate", Status: "done", Reason: "finished", ExitCode: &exitCode, TranscriptRef: "ref1", OutputBytes: 100}},
		Agents:  []string{"explorer", "default"},
	}
	got := agentToServerDetailedStatus(ds)

	if len(got.Tools) != 1 || got.Tools[0].Name != "shell" {
		t.Errorf("Tools = %v, want 1 shell", got.Tools)
	}
	if len(got.MCP) != 1 || got.MCP[0].Name != "test-server" {
		t.Errorf("MCP = %v, want 1 test-server", got.MCP)
	}
	if len(got.Skills) != 1 || got.Skills[0].Name != "test-skill" {
		t.Errorf("Skills = %v, want 1 test-skill", got.Skills)
	}
	if len(got.Plugins) != 1 || got.Plugins[0].Name != "test-plugin" {
		t.Errorf("Plugins = %v, want 1 test-plugin", got.Plugins)
	}
	if len(got.Hooks) != 1 || got.Hooks["PreToolUse"] != 1 {
		t.Errorf("Hooks = %v, want PreToolUse=1", got.Hooks)
	}
	if len(got.Jobs) != 1 || got.Jobs[0].JobID != "job1" || *got.Jobs[0].ExitCode != 42 {
		t.Errorf("Jobs = %v, want 1 job1 with exit code 42", got.Jobs)
	}
	if len(got.Agents) != 2 {
		t.Errorf("Agents = %v, want 2", got.Agents)
	}
}

func TestRunServe_ResumeNonexistent(t *testing.T) {
	oldLoadClient := serveLoadClient
	serveLoadClient = func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
		client := llm.NewClient()
		client.Register(serveLoggingAdapter{})
		cfg := providercfg.Config{
			Default: "openai",
			Instances: []providercfg.InstanceConfig{
				{Name: "openai", Type: "openai"},
			},
		}
		return client, cfg, true, nil
	}
	t.Cleanup(func() { serveLoadClient = oldLoadClient })

	err := runServe([]string{
		"--model", "openai/gpt-test",
		"--resume", "NONEXISTENT",
		"--dir", t.TempDir(),
		"--state-dir", t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error for nonexistent resume session")
	}
	if !strings.Contains(err.Error(), "NONEXISTENT") {
		t.Fatalf("error = %v, want to mention NONEXISTENT", err)
	}
}
