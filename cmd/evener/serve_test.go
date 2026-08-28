package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/mcpconfig"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/plugins"
	"primeradiant.com/evener/llm"
	apilog "primeradiant.com/evener/llm/apilog"
	"primeradiant.com/evener/llm/providercfg"
	"primeradiant.com/evener/rendezvous"
	"primeradiant.com/evener/server"
)

func TestServePluginSelectionValidationPrecedesStartupHooks(t *testing.T) {
	root := t.TempDir()
	var order []string
	deps := defaultServeDeps()
	deps.resolvePlugins = func(dirs []string, selected *[]string) (plugins.LaunchPluginResolution, error) {
		order = append(order, "resolve")
		if !reflect.DeepEqual(dirs, []string{root}) || selected == nil || !reflect.DeepEqual(*selected, []string{"missing-plugin"}) {
			t.Fatalf("resolver args = dirs %v selected %v", dirs, selected)
		}
		return plugins.LaunchPluginResolution{SelectionErrors: []plugins.PluginSelectionError{{Name: "missing-plugin", Reason: "no valid plugin candidate"}}}, nil
	}
	deps.ensureConfigDirs = func() error { order = append(order, "ensure-config"); return nil }
	deps.seedMarketplaces = func() error { order = append(order, "seed-marketplaces"); return nil }

	err := runServeWithDeps([]string{"--plugin-dir", root, "--enabled-plugins=missing-plugin"}, deps)
	if err == nil || !strings.Contains(err.Error(), "enabled plugin selection is unavailable") {
		t.Fatalf("serve error = %v, want strict selection error", err)
	}
	if !reflect.DeepEqual(order, []string{"resolve"}) {
		t.Fatalf("startup order = %v, want resolver only", order)
	}
}

func TestServePassesResolvedPluginDirsToSessionConfig(t *testing.T) {
	installServeScriptedProvider(t, &scriptedProvider{name: "openai"})
	selectedDir := t.TempDir()
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func() error { return nil }
	deps.resolvePlugins = func([]string, *[]string) (plugins.LaunchPluginResolution, error) {
		return plugins.LaunchPluginResolution{SelectedDirs: []string{selectedDir}}, nil
	}
	var got []string
	deps.provisionSandbox = func(_ *execenv.LocalExecutionEnvironment, cfg *agent.SessionConfig, _ string) error {
		got = append([]string(nil), cfg.PluginDirs...)
		return errors.New("stop after config")
	}
	err := runServeWithDeps([]string{
		"--model", "openai/gpt-test", "--dir", t.TempDir(), "--state-dir", t.TempDir(),
		"--enabled-plugins=alpha", "--plugin-dir", selectedDir,
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "stop after config") {
		t.Fatalf("serve error = %v", err)
	}
	if !reflect.DeepEqual(got, []string{selectedDir}) {
		t.Fatalf("session plugin dirs = %v, want %v", got, []string{selectedDir})
	}
}

func TestServePluginRootFlagUsesHubValidatedRegistryAfterDisablement(t *testing.T) {
	hubRoot := filepath.Join(t.TempDir(), "hub-root")
	hubInstalledDir := filepath.Join(hubRoot, "installed-alpha")
	writeTask3Plugin(t, hubInstalledDir, "alpha")
	if err := plugins.SaveRegistry(filepath.Join(hubRoot, "installed_plugins.json"), plugins.Registry{
		Plugins: map[string][]plugins.InstallEntry{
			"alpha@acme": {{
				InstallPath: hubInstalledDir,
				Version:     "1.0.0",
				Enabled:     true,
				Source:      plugins.Source{Kind: plugins.SourceDirectory, Path: hubInstalledDir},
			}},
		},
	}); err != nil {
		t.Fatalf("SaveRegistry(enabled): %v", err)
	}
	selected := []string{"alpha"}
	if _, err := plugins.NewManager(hubRoot).ResolveForLaunch(nil, &selected); err != nil {
		t.Fatalf("hub validation ResolveForLaunch: %v", err)
	}
	if err := plugins.SaveRegistry(filepath.Join(hubRoot, "installed_plugins.json"), plugins.Registry{
		Plugins: map[string][]plugins.InstallEntry{
			"alpha@acme": {{
				InstallPath: hubInstalledDir,
				Version:     "1.0.0",
				Enabled:     false,
				Source:      plugins.Source{Kind: plugins.SourceDirectory, Path: hubInstalledDir},
			}},
		},
	}); err != nil {
		t.Fatalf("SaveRegistry(disabled): %v", err)
	}

	xdgConfigHome := filepath.Join(t.TempDir(), "ambient-config")
	ambientRoot := filepath.Join(xdgConfigHome, "evener", "plugins")
	ambientInstalledDir := filepath.Join(ambientRoot, "installed-alpha")
	writeTask3Plugin(t, ambientInstalledDir, "alpha")
	if err := plugins.SaveRegistry(filepath.Join(ambientRoot, "installed_plugins.json"), plugins.Registry{
		Plugins: map[string][]plugins.InstallEntry{
			"alpha@acme": {{
				InstallPath: ambientInstalledDir,
				Version:     "9.9.9",
				Enabled:     true,
				Source:      plugins.Source{Kind: plugins.SourceDirectory, Path: ambientInstalledDir},
			}},
		},
	}); err != nil {
		t.Fatalf("SaveRegistry(ambient): %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdgConfigHome)

	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func() error { return nil }

	err := runServeWithDeps([]string{
		"--model", "openai/gpt-test",
		"--dir", t.TempDir(),
		"--state-dir", t.TempDir(),
		"--plugin-root", hubRoot,
		"--enabled-plugins=alpha",
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "enabled plugin selection is unavailable: alpha: no valid plugin candidate") {
		t.Fatalf("serve error = %v, want disabled hub-root selection failure", err)
	}
}

func TestAgentToServerDetailedStatus_DelegatesLossless(t *testing.T) {
	valid, resumable := true, false
	running, quiet, duration := int64(100), int64(40), int64(60)
	in := agent.DelegateStatusInfo{
		DelegateID: "dlg_bridge", OwnerSessionID: "owner", RootSessionID: "root", ChildSessionID: "child", TranscriptRef: "local:child",
		ParentDelegateID: "dlg_parent", Type: "delegate", Lifecycle: "idle", Phase: "idle", Status: "idle", Outcome: "exhausted",
		Reason: "tool_round_budget_exhausted", Terminal: true, Resumable: true, NeedsAttention: true, ProjectionRevision: 9,
		Task: "inspect", Description: "inspect carefully", AgentType: "explorer", RequestedModel: "openai/gpt-5",
		ResolvedProfileID: "openai", ResolvedModel: "gpt-5", Model: "gpt-5", ReasoningEffort: "high",
		OriginTurnID: "turn", OriginToolCallID: "call", OriginItemID: "item", RunStartedAt: "start", RunEndedAt: "end", LatestActivityAt: "latest",
		RunningForMS: &running, QuietForMS: &quiet, DurationMS: &duration, PacketKind: "reported", Message: json.RawMessage("null"),
		StructuredResult: json.RawMessage("null"), StructuredValid: &valid, StructuredReason: "valid null", Warnings: []string{"warning"}, Diagnostics: []string{"diagnostic"},
		ExhaustionBudget: "max_tool_rounds_per_input", ExhaustionLimit: 4, ExhaustionResumable: &resumable,
		DelegationAllowance: 2, ParentWatchGranted: true, Usage: &appwire.EvenerUsage{InputTokens: 11, OutputTokens: 7, CacheReadTokens: 3, TotalTokens: 18},
		Worktree: &appwire.JobActivityWorktree{Path: "/tmp/lane", Branch: "delegate/lane", HeadSHA: "abc", Ahead: 2, Dirty: true},
	}
	out := agentToServerDetailedStatus(agent.DetailedStatus{Delegates: []agent.DelegateStatusInfo{in}, TurnSlots: &agent.TurnSlotOccupancy{InUse: 3, Cap: 50, Jobs: 2, Drives: 1}})
	if len(out.Delegates) != 1 {
		t.Fatalf("delegates = %+v, want one", out.Delegates)
	}
	want := server.DelegateStatusInfo{
		DelegateID: in.DelegateID, OwnerSessionID: in.OwnerSessionID, RootSessionID: in.RootSessionID, ChildSessionID: in.ChildSessionID, TranscriptRef: in.TranscriptRef,
		ParentDelegateID: in.ParentDelegateID, Type: in.Type, Lifecycle: in.Lifecycle, Phase: in.Phase, Status: in.Status, Outcome: in.Outcome,
		Reason: in.Reason, Terminal: in.Terminal, Resumable: in.Resumable, NeedsAttention: in.NeedsAttention, NotResumableReason: in.NotResumableReason, ProjectionRevision: in.ProjectionRevision,
		Task: in.Task, Description: in.Description, AgentType: in.AgentType, RequestedModel: in.RequestedModel, ResolvedProfileID: in.ResolvedProfileID,
		ResolvedModel: in.ResolvedModel, Model: in.Model, ReasoningEffort: in.ReasoningEffort, OriginTurnID: in.OriginTurnID,
		OriginToolCallID: in.OriginToolCallID, OriginItemID: in.OriginItemID, RunStartedAt: in.RunStartedAt, RunEndedAt: in.RunEndedAt,
		LatestActivityAt: in.LatestActivityAt, RunningForMS: in.RunningForMS, QuietForMS: in.QuietForMS, DurationMS: in.DurationMS,
		PacketKind: in.PacketKind, Message: in.Message, StructuredResult: in.StructuredResult, StructuredValid: in.StructuredValid,
		StructuredReason: in.StructuredReason, Warnings: in.Warnings, Diagnostics: in.Diagnostics, ExhaustionBudget: in.ExhaustionBudget,
		ExhaustionLimit: in.ExhaustionLimit, ExhaustionResumable: in.ExhaustionResumable, DelegationAllowance: in.DelegationAllowance,
		ParentWatchGranted: in.ParentWatchGranted, Usage: in.Usage, Worktree: in.Worktree,
	}
	if !reflect.DeepEqual(out.Delegates[0], want) {
		t.Fatalf("delegate bridge lost fields:\ngot=%+v\nwant=%+v", out.Delegates[0], want)
	}
	if out.TurnSlots == nil || out.TurnSlots.InUse != 3 || out.TurnSlots.Cap != 50 || out.TurnSlots.Jobs != 2 || out.TurnSlots.Drives != 1 {
		t.Fatalf("turn slots = %+v", out.TurnSlots)
	}
}

func TestProcessNextServeInputClaimsDurableStartAfterCoalescedWake(t *testing.T) {
	input := make(chan server.InputMessage, 1)
	input <- server.InputMessage{Text: "already queued"}
	durableStart := false
	var processed []string
	process := func(msg server.InputMessage) bool {
		if msg.ClientMutationStart {
			if !durableStart {
				return false
			}
			if msg.SessionID != "session-1" {
				t.Fatalf("durable session = %q, want session-1", msg.SessionID)
			}
			processed = append(processed, "durable")
			durableStart = false
			return true
		}
		processed = append(processed, msg.Text)
		durableStart = true
		input <- server.InputMessage{Text: "later queued"}
		return true
	}

	if !processNextServeInput(context.Background(), input, "session-1", process) {
		t.Fatal("first input iteration stopped")
	}
	if !processNextServeInput(context.Background(), input, "session-1", process) {
		t.Fatal("durable input iteration stopped")
	}
	if len(processed) != 2 || processed[0] != "already queued" || processed[1] != "durable" {
		t.Fatalf("processed = %#v, want queued input then durable start", processed)
	}
	select {
	case msg := <-input:
		if msg.Text != "later queued" {
			t.Fatalf("remaining input = %#v", msg)
		}
	default:
		t.Fatal("durable start did not take priority over the full wake channel")
	}
}

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
// --model flag is set and EVENER_MODEL is unset.
func TestRunServe_MissingModel(t *testing.T) {
	old := os.Getenv("EVENER_MODEL")
	if err := os.Unsetenv("EVENER_MODEL"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if old != "" {
			os.Setenv("EVENER_MODEL", old)
		}
	}()

	err := runServe(nil)
	if err == nil {
		t.Fatal("expected error for missing model")
	}
	if got := err.Error(); got != "no model: use --model provider/model or set EVENER_MODEL=provider/model" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrintServeEnvVars_IncludesOpenAIResponsesContinuation(t *testing.T) {
	var b strings.Builder
	printServeEnvVars(&b)
	if !strings.Contains(b.String(), envvars.EVENEROpenAIResponsesContinuation.Name) {
		t.Fatalf("serve env help missing %s: %s", envvars.EVENEROpenAIResponsesContinuation.Name, b.String())
	}
}

func TestServe_WritesAndRemovesRendezvousFile(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	if os.Getenv("EVENER_LIVE_TESTS") != "1" {
		t.Skip("set EVENER_LIVE_TESTS=1 to run live serve integration test")
	}
	if os.Getenv("OPENAI_API_KEY") == "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("requires an LLM API key for serve startup")
	}

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	// TestMain already points XDG_STATE_HOME at its own package-wide throwaway
	// root; clear it here so the rendezvous dir this test inspects resolves
	// under tmpHome (the HOME fallback) rather than that shared root.
	t.Setenv("XDG_STATE_HOME", "")

	args := []string{
		"--model", os.Getenv("EVENER_TEST_PROVIDER") + "/" + os.Getenv("EVENER_TEST_MODEL"),
		"--addr", "127.0.0.1:0",
		"--dir", t.TempDir(),
	}
	if os.Getenv("EVENER_TEST_PROVIDER") == "" || os.Getenv("EVENER_TEST_MODEL") == "" {
		t.Skip("set EVENER_TEST_PROVIDER and EVENER_TEST_MODEL to run this test")
	}

	done := make(chan error, 1)
	go func() {
		done <- runServe(args)
	}()

	runDir := filepath.Join(tmpHome, ".local", "state", "evener", "run")
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
		ClientMutationID: "shutdown-in-flight",
		Ref:              ref,
		Input:            []appwire.InputItem{{Type: "text", Text: "stay busy until shutdown"}},
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

// waitForServeTestRendezvous waits for the serve process to write its
// rendezvous file and returns the ready entry. It uses filesystem
// notifications instead of sleep-polling so it responds immediately when the
// file appears. This is integration-test infrastructure: the server must
// start within 5 seconds.
func waitForServeTestRendezvous(t *testing.T, runDir string) rendezvous.Entry {
	t.Helper()

	// Ensure the run directory exists before attaching the watcher.
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("waitForServeTestRendezvous: mkdir %s: %v", runDir, err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("waitForServeTestRendezvous: new watcher: %v", err)
	}
	defer watcher.Close()

	if err := watcher.Add(runDir); err != nil {
		t.Fatalf("waitForServeTestRendezvous: watch %s: %v", runDir, err)
	}

	// findEntry scans the run dir for a ready rendezvous entry.
	findEntry := func() (rendezvous.Entry, bool) {
		entries, _ := rendezvous.List(runDir)
		for _, e := range entries {
			if e.Address != "" && e.SessionID != "" {
				return e, true
			}
		}
		return rendezvous.Entry{}, false
	}

	// Check once after installing the watcher to close the TOCTOU window: the
	// file may have been written between goroutine start and watcher.Add.
	if e, ok := findEntry(); ok {
		return e
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-watcher.Events:
			if !ok {
				t.Fatal("waitForServeTestRendezvous: watcher closed unexpectedly")
			}
			if e, found := findEntry(); found {
				return e
			}
		case werr := <-watcher.Errors:
			t.Fatalf("waitForServeTestRendezvous: watcher error: %v", werr)
		case <-deadline:
			t.Fatalf("no rendezvous entry in %s after 5s", runDir)
			return rendezvous.Entry{}
		}
	}
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
	_, err = client.Complete(llm.WithAPILogContext(context.Background(), "serve-session"), llm.Request{
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

	path := filepath.Join(stateDir, "sessions", "serve-session.api.jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open sessions/serve-session.api.jsonl: %v", err)
	}
	defer f.Close()
	decoder := apilog.NewDecoder(f, 1<<20)
	first, err := decoder.Next()
	if err != nil {
		t.Fatalf("decode attempt: %v", err)
	}
	attempt, ok := first.(apilog.APIAttemptRecord)
	if !ok || attempt.ProviderInstance != "openai" || attempt.RequestModel != "gpt-test" || attempt.Request.Model != "gpt-test" {
		t.Fatalf("canonical attempt = %+v", first)
	}
	second, err := decoder.Next()
	if err != nil {
		t.Fatalf("decode settlement: %v", err)
	}
	settlement, ok := second.(apilog.APIAttemptGroupSettlement)
	if !ok || settlement.AttemptGroupID != attempt.AttemptGroupID || settlement.FinalAttemptCount != 1 {
		t.Fatalf("canonical settlement = %+v", second)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "api.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("frozen project-level api.jsonl was written (stat err=%v)", err)
	}
	select {
	case <-called:
	default:
		t.Fatal("fake adapter was not called")
	}
}

func TestRunServeClearReleasesOldSessionAPILogRoute(t *testing.T) {
	stateDir := t.TempDir()
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func() error { return nil }
	deps.newClient = func(string, io.Writer) (*llm.Client, providercfg.Config, bool, func() error, error) {
		client := llm.NewClient()
		client.Register(serveLoggingAdapter{})
		cfg := providercfg.Config{
			Default: "openai",
			Instances: []providercfg.InstanceConfig{
				{Name: "openai", Type: "openai"},
			},
		}
		return client, cfg, true, func() error { return nil }, nil
	}
	var logger *llm.APILogger
	deps.attachAPILogger = func(client *llm.Client, stateDir string, _ io.Writer) (func(string) error, func() error, error) {
		var err error
		logger, err = llm.NewSessionAPILogger(stateDir)
		if err != nil {
			return nil, nil, err
		}
		client.Use(logger)
		return logger.ReserveSession, logger.Close, nil
	}

	runDir := t.TempDir()
	workDir := t.TempDir()
	done := make(chan error, 1)
	go func() {
		done <- runServeWithDeps([]string{
			"--model", "openai/gpt-test",
			"--addr", "127.0.0.1:0",
			"--dir", workDir,
			"--state-dir", stateDir,
			"--run-dir", runDir,
			"--no-project-prompts",
		}, deps)
	}()

	entry := waitForServeTestRendezvous(t, runDir)
	t.Cleanup(func() {
		resp, err := http.Post("http://"+entry.Address+"/shutdown", "", nil)
		if err == nil {
			resp.Body.Close()
		}
	})
	if err := logger.ReserveSession(entry.SessionID); err != nil {
		t.Fatalf("ReserveSession old route: %v", err)
	}
	resp, err := http.Post("http://"+entry.Address+"/clear", "", nil)
	if err != nil {
		t.Fatalf("POST /clear: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /clear status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}

	reopened, err := llm.NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger after clear: %v", err)
	}
	defer reopened.Close() //nolint:errcheck
	if err := reopened.ReserveSession(entry.SessionID); err != nil {
		t.Fatalf("old session API-log route remained owned after /clear: %v", err)
	}

	shutdownResp, err := http.Post("http://"+entry.Address+"/shutdown", "", nil)
	if err != nil {
		t.Fatalf("POST /shutdown: %v", err)
	}
	shutdownResp.Body.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServeWithDeps: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runServeWithDeps did not exit after shutdown")
	}
}

type serveLoggingAdapter struct {
	called chan<- struct{}
}

func (a serveLoggingAdapter) Name() string { return "openai" }

func (a serveLoggingAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	select {
	case a.called <- struct{}{}:
	default:
	}
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	attempt := llm.BeginAPIAttempt(ctx, llm.APIAttemptMeta{
		ProviderInstance: req.Provider,
		RequestModel:     req.Model,
		Method:           http.MethodPost,
		Endpoint:         "https://example.test/v1/responses",
		RequestBody:      []byte(`{"input":"hi"}`),
		StartedAt:        startedAt,
	})
	resp := llm.Response{
		Provider: req.Provider,
		Model:    req.Model,
		Message:  llm.Assistant("ok"),
		Finish:   llm.FinishReason{Reason: llm.FinishReasonStop},
		Usage:    llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	}
	attempt.Complete(llm.APIAttemptResult{
		StatusCode:   http.StatusOK,
		ResponseBody: []byte(`{"output":"ok"}`),
		Response:     &resp,
		Outcome:      apilog.AttemptSuccess,
		FinishedAt:   startedAt.Add(time.Millisecond),
	})
	return resp, nil
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
	if len(got.HookEvents) != 0 {
		t.Errorf("HookEvents = %d, want 0", len(got.HookEvents))
	}
	if len(got.Jobs) != 0 {
		t.Errorf("Jobs = %d, want 0", len(got.Jobs))
	}
	if len(got.Agents) != 0 {
		t.Errorf("Agents = %d, want 0", len(got.Agents))
	}
}

func TestAgentToServerDetailedStatus_PreservesPluginPresence(t *testing.T) {
	got := agentToServerDetailedStatus(agent.DetailedStatus{Plugins: []agent.PluginInfo{}})
	if got.Plugins == nil {
		t.Fatal("explicit empty Plugins became nil")
	}

	legacy := agentToServerDetailedStatus(agent.DetailedStatus{})
	if legacy.Plugins != nil {
		t.Fatalf("nil Plugins became non-nil: %#v", legacy.Plugins)
	}
}

func TestAgentToServerDetailedStatus_Partial(t *testing.T) {
	// Every field gets a distinct value so a transposed or dropped mapping
	// (e.g. SkillCount swapped with AgentCount, or Skills.Description dropped)
	// produces a detectable mismatch rather than silently passing.
	exitCode := 42
	ds := agent.DetailedStatus{
		Tools:      []agent.ToolInfo{{Name: "shell", Source: "core"}},
		MCP:        []mcpconfig.ServerInfo{{Name: "test-server", Tools: []string{"tool1", "tool2"}, Status: "degraded", Error: "boom"}},
		Skills:     []skill.SkillMeta{{Name: "test-skill", Description: "A test skill"}},
		Plugins:    []agent.PluginInfo{{Name: "test-plugin", Version: "1.0.0", SkillCount: 2, AgentCount: 3, HookCount: 4, MCPCount: 5}},
		HookEvents: []agent.HookEventStatus{{Event: plugin.HookPreToolUse, Count: 1}, {Event: plugin.HookPostToolUse, Count: 7}},
		Jobs:       []agent.JobStatusInfo{{JobID: "job1", JobType: "delegate", Status: "done", Reason: "finished", ExitCode: &exitCode, TranscriptRef: "ref1", OutputBytes: 100}},
		Agents:     []string{"explorer", "default"},
	}
	got := agentToServerDetailedStatus(ds)

	if len(got.Tools) != 1 {
		t.Fatalf("Tools = %v, want 1", got.Tools)
	}
	if got.Tools[0].Name != "shell" || got.Tools[0].Source != "core" {
		t.Errorf("Tools[0] = %+v, want {Name:shell Source:core}", got.Tools[0])
	}

	if len(got.MCP) != 1 {
		t.Fatalf("MCP = %v, want 1", got.MCP)
	}
	if got.MCP[0].Name != "test-server" {
		t.Errorf("MCP[0].Name = %q, want test-server", got.MCP[0].Name)
	}
	if len(got.MCP[0].Tools) != 2 || got.MCP[0].Tools[0] != "tool1" || got.MCP[0].Tools[1] != "tool2" {
		t.Errorf("MCP[0].Tools = %v, want [tool1 tool2]", got.MCP[0].Tools)
	}
	if got.MCP[0].Status != "degraded" {
		t.Errorf("MCP[0].Status = %q, want degraded", got.MCP[0].Status)
	}
	if got.MCP[0].Error != "boom" {
		t.Errorf("MCP[0].Error = %q, want boom", got.MCP[0].Error)
	}

	if len(got.Skills) != 1 {
		t.Fatalf("Skills = %v, want 1", got.Skills)
	}
	if got.Skills[0].Name != "test-skill" || got.Skills[0].Description != "A test skill" {
		t.Errorf("Skills[0] = %+v, want {Name:test-skill Description:A test skill}", got.Skills[0])
	}

	if len(got.Plugins) != 1 {
		t.Fatalf("Plugins = %v, want 1", got.Plugins)
	}
	p := got.Plugins[0]
	if p.Name != "test-plugin" || p.Version != "1.0.0" ||
		p.SkillCount != 2 || p.AgentCount != 3 || p.HookCount != 4 || p.MCPCount != 5 {
		t.Errorf("Plugins[0] = %+v, want {Name:test-plugin Version:1.0.0 SkillCount:2 AgentCount:3 HookCount:4 MCPCount:5}", p)
	}

	if len(got.HookEvents) != 2 {
		t.Fatalf("HookEvents = %d, want 2", len(got.HookEvents))
	}
	for _, he := range got.HookEvents {
		switch he.Event {
		case "PreToolUse":
			if he.Count != 1 {
				t.Errorf("HookEvents PreToolUse = %d, want 1", he.Count)
			}
		case "PostToolUse":
			if he.Count != 7 {
				t.Errorf("HookEvents PostToolUse = %d, want 7", he.Count)
			}
		}
	}

	if len(got.Jobs) != 1 {
		t.Fatalf("Jobs = %v, want 1", got.Jobs)
	}
	job := got.Jobs[0]
	if job.JobID != "job1" || job.JobType != "delegate" || job.Status != "done" ||
		job.Reason != "finished" || job.TranscriptRef != "ref1" || job.OutputBytes != 100 {
		t.Errorf("Jobs[0] = %+v, want job1/delegate/done/finished/ref1/100", job)
	}
	if job.ExitCode == nil || *job.ExitCode != 42 {
		t.Errorf("Jobs[0].ExitCode = %v, want 42", job.ExitCode)
	}

	if len(got.Agents) != 2 || got.Agents[0] != "explorer" || got.Agents[1] != "default" {
		t.Errorf("Agents = %v, want [explorer default]", got.Agents)
	}
}

func TestAgentToServerDetailedStatus_Exhaustion(t *testing.T) {
	resumable := true
	got := agentToServerDetailedStatus(agent.DetailedStatus{Jobs: []agent.JobStatusInfo{{
		JobID:            "job_exhausted",
		JobType:          "delegate",
		Status:           "exhausted",
		Reason:           "tool_round_budget_exhausted",
		ExhaustionBudget: "max_tool_rounds_per_input",
		ExhaustionLimit:  1,
		Resumable:        &resumable,
	}}})
	if len(got.Jobs) != 1 {
		t.Fatalf("jobs = %+v, want one exhausted job", got.Jobs)
	}
	job := got.Jobs[0]
	if job.Status != "exhausted" || job.Reason != "tool_round_budget_exhausted" ||
		job.ExhaustionBudget != "max_tool_rounds_per_input" || job.ExhaustionLimit != 1 ||
		job.Resumable == nil || !*job.Resumable {
		t.Fatalf("job = %+v", job)
	}
	raw, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("marshal server job status: %v", err)
	}
	encoded := string(raw)
	if !strings.Contains(encoded, `"exhaustion_budget":"max_tool_rounds_per_input"`) || !strings.Contains(encoded, `"exhaustion_limit":1`) {
		t.Fatalf("server diagnostic JSON = %s", encoded)
	}
	if strings.Contains(encoded, "exhaustionBudget") || strings.Contains(encoded, "exhaustionLimit") {
		t.Fatalf("server diagnostic used AppWire camelCase: %s", encoded)
	}
}

// TestEvenerUsageFromLLM_ZeroReturnsNil pins the WS2 A7 helper: an llm.Usage
// with every total at zero (a fresh session, an old daemon that never seeded
// usage, or a Codex thread) maps to a nil *appwire.EvenerUsage, so the status
// row hides the usage cluster rather than rendering ↑0 ↓0.
func TestEvenerUsageFromLLM_ZeroReturnsNil(t *testing.T) {
	if got := evenerUsageFromLLM(llm.Usage{}); got != nil {
		t.Fatalf("evenerUsageFromLLM(zero) = %+v, want nil", got)
	}
}

// TestEvenerUsageFromLLM_MapsTotals pins the field mapping, including
// CacheReadTokens dereferencing the *int pointer field (distinct values on
// every field so a transposed mapping is detectable).
func TestEvenerUsageFromLLM_MapsTotals(t *testing.T) {
	cacheRead := 5
	got := evenerUsageFromLLM(llm.Usage{
		InputTokens:     10,
		OutputTokens:    20,
		TotalTokens:     30,
		CacheReadTokens: &cacheRead,
	})
	want := &appwire.EvenerUsage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 5, TotalTokens: 30}
	if got == nil || *got != *want {
		t.Fatalf("evenerUsageFromLLM = %+v, want %+v", got, want)
	}
}

// TestEvenerUsageFromLLM_NonZeroCacheReadOnlyStillReturns pins the "any of the
// four totals nonzero" gate: CacheReadTokens alone (input/output/total all
// zero, e.g. a fully cache-served turn) must not be hidden.
func TestEvenerUsageFromLLM_NonZeroCacheReadOnlyStillReturns(t *testing.T) {
	cacheRead := 7
	got := evenerUsageFromLLM(llm.Usage{CacheReadTokens: &cacheRead})
	if got == nil || got.CacheReadTokens != 7 {
		t.Fatalf("evenerUsageFromLLM(cache-read-only) = %+v, want CacheReadTokens=7", got)
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

func TestServeResumeRunningReservesBeforeRestore(t *testing.T) {
	adapter := &scriptedProvider{name: "openai"}
	installServeScriptedProvider(t, adapter)
	tests := []struct {
		name string
		flag func(string) []string
	}{
		{name: "resume", flag: func(id string) []string { return []string{"--resume", id} }},
		{name: "resume-last", flag: func(string) []string { return []string{"--resume-last"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stateDir := t.TempDir()
			const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
			meta := schema.SessionMeta{
				ID: sessionID, ProfileID: "openai", Model: "gpt-test",
				CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(2, 0).UTC(),
			}
			if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
				t.Fatalf("SaveSessionMeta: %v", err)
			}
			sessionsDir := filepath.Join(stateDir, "sessions")
			transcriptPath := filepath.Join(sessionsDir, sessionID+".transcript.jsonl")
			if err := os.WriteFile(transcriptPath, []byte("transcript sentinel\n"), 0o600); err != nil {
				t.Fatalf("write transcript sentinel: %v", err)
			}
			jobsDir := filepath.Join(sessionsDir, sessionID)
			if err := os.Mkdir(jobsDir, 0o700); err != nil {
				t.Fatalf("mkdir jobs: %v", err)
			}
			jobsPath := filepath.Join(jobsDir, "jobs.jsonl")
			if err := os.WriteFile(jobsPath, []byte("jobs sentinel\n"), 0o600); err != nil {
				t.Fatalf("write jobs sentinel: %v", err)
			}
			apiPath := filepath.Join(sessionsDir, sessionID+".api.jsonl")
			owner, err := llm.NewAPILogger(apiPath)
			if err != nil {
				t.Fatalf("NewAPILogger owner: %v", err)
			}
			before := readResumeArtifacts(t, stateDir, sessionID, transcriptPath, jobsPath, apiPath)

			restoreCalled := false
			deps := defaultServeDeps()
			deps.ensureConfigDirs = func() error { return nil }
			deps.seedMarketplaces = func() error { return nil }
			deps.restoreSession = func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, schema.SessionMeta, agent.RestoreSessionConfig) (*agent.Session, error) {
				restoreCalled = true
				return nil, errors.New("restore reached")
			}
			args := []string{"--dir", stateDir, "--state-dir", stateDir, "--run-dir", t.TempDir()}
			args = append(args, tt.flag(sessionID)...)
			serveErr := runServeWithDeps(args, deps)
			if closeErr := owner.Close(); closeErr != nil {
				t.Fatalf("owner Close: %v", closeErr)
			}
			if serveErr == nil || !strings.Contains(serveErr.Error(), "already running") || !strings.Contains(serveErr.Error(), "send work") || !strings.Contains(serveErr.Error(), "fork") {
				t.Fatalf("serve error = %v, want live-session or fork guidance", serveErr)
			}
			if restoreCalled {
				t.Fatal("RestoreSessionFromMetaWithConfig was called before API-log ownership")
			}
			if got := len(adapter.Requests()); got != 0 {
				t.Fatalf("provider requests = %d, want 0", got)
			}
			after := readResumeArtifacts(t, stateDir, sessionID, transcriptPath, jobsPath, apiPath)
			if !equalResumeArtifacts(before, after) {
				t.Fatalf("resume lock conflict mutated session artifacts:\n before=%q\n  after=%q", before, after)
			}
		})
	}
}
