package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
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
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/internal/plugins"
	"primeradiant.com/evener/llm"
	apilog "primeradiant.com/evener/llm/apilog"
	"primeradiant.com/evener/llm/registry"
	"primeradiant.com/evener/rendezvous"
	"primeradiant.com/evener/server"
)

func shutdownServeTestDaemon(ctx context.Context, address, sessionID string) error {
	transport, err := appwire.DialWebSocket(ctx, "ws://"+address+"/rpc", http.DefaultClient)
	if err != nil {
		return err
	}
	client := appwire.NewClient(transport)
	defer client.Close()
	client.Start(context.WithoutCancel(ctx))
	if _, err := client.Initialize(ctx, appwire.InitializeParams{
		ClientInfo: appwire.ClientInfo{Name: "serve-test-shutdown", Version: "test"},
	}); err != nil {
		return err
	}
	threads, err := client.ThreadList(ctx, appwire.ThreadListParams{})
	if err != nil {
		return err
	}
	if len(threads.Data) > 0 {
		sessionID = threads.Data[0].ID
	}
	return client.ThreadShutdown(ctx, appwire.ThreadShutdownParams{Ref: appwire.Ref{SourceID: "local", ThreadID: sessionID}.String()})
}

// A selection that cannot be honoured stops the startup before it does any
// work of its own. Ensuring the config dirs comes first and is not that work:
// it carries the legacy-data guard, which has to see the config root before
// anything — plugin resolution included — creates it.
func TestServePluginSelectionValidationPrecedesStartupHooks(t *testing.T) {
	root := t.TempDir()
	var order []string
	deps := defaultServeDeps()
	deps.resolvePlugins = func(_ context.Context, dirs []string, selected *[]string) (plugins.LaunchPluginResolution, error) {
		order = append(order, "resolve")
		if !reflect.DeepEqual(dirs, []string{root}) || selected == nil || !reflect.DeepEqual(*selected, []string{"missing-plugin"}) {
			t.Fatalf("resolver args = dirs %v selected %v", dirs, selected)
		}
		return plugins.LaunchPluginResolution{SelectionErrors: []plugins.PluginSelectionError{{Name: "missing-plugin", Reason: "no valid plugin candidate"}}}, nil
	}
	deps.ensureConfigDirs = func() error { order = append(order, "ensure-config"); return nil }
	deps.seedMarketplaces = func(context.Context) error { order = append(order, "seed-marketplaces"); return nil }

	err := runServeWithDeps([]string{"--plugin-dir", root, "--enabled-plugins=missing-plugin"}, deps)
	if err == nil || !strings.Contains(err.Error(), "enabled plugin selection is unavailable") {
		t.Fatalf("serve error = %v, want strict selection error", err)
	}
	if !reflect.DeepEqual(order, []string{"ensure-config", "resolve"}) {
		t.Fatalf("startup order = %v, want the config-dir guard and then the resolver", order)
	}
}

func TestServePassesResolvedPluginDirsToSessionConfig(t *testing.T) {
	installServeScriptedProvider(t, &scriptedProvider{name: "openai"})
	selectedDir := t.TempDir()
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func(context.Context) error { return nil }
	deps.resolvePlugins = func(context.Context, []string, *[]string) (plugins.LaunchPluginResolution, error) {
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
	if _, err := plugins.NewManager(hubRoot).ResolveForLaunch(context.Background(), nil, &selected); err != nil {
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
	deps.seedMarketplaces = func(context.Context) error { return nil }

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

// serveTestClient is a client on a hermetic registry carrying one custom
// instance, "work", behind openai.
func serveTestClient(t *testing.T) *llm.Client {
	t.Helper()
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{"work": {Base: "openai", APIKey: "test"}}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return llm.NewClient(llm.WithRegistry(r))
}

// TestBuildInitialProfile_ConfigPath verifies that buildInitialProfile resolves
// a custom instance name (e.g. "work" defined in providers.toml) to a profile
// whose ID matches the instance name, not the provider id behind it.
func TestBuildInitialProfile_ConfigPath(t *testing.T) {
	profile, err := buildInitialProfile(serveTestClient(t), cmdutil.ModelRef{Provider: "work", Model: "gpt-4o"}, "")
	if err != nil {
		t.Fatalf("buildInitialProfile: %v", err)
	}
	if profile.ID() != "work" || profile.ProviderID() != "openai" {
		t.Fatalf("profile = %s/%s, want work/openai", profile.ID(), profile.ProviderID())
	}
}

// TestBuildInitialProfile_ConfigPathInvalidOutputSchema verifies that an invalid
// --output-schema returns an error.
func TestBuildInitialProfile_ConfigPathInvalidOutputSchema(t *testing.T) {
	_, err := buildInitialProfile(serveTestClient(t), cmdutil.ModelRef{Provider: "work", Model: "gpt-4o"}, "{not json")
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
	_, err := buildInitialProfile(serveTestClient(t), cmdutil.ModelRef{Provider: "unknown", Model: "gpt-4o"}, "")
	if err == nil {
		t.Fatal("expected error for unknown instance name")
	}
	if !strings.Contains(err.Error(), "unknown instance") {
		t.Fatalf("error=%q, want to contain 'unknown instance'", err.Error())
	}
}

// TestBuildInitialProfile_CuratedInstance verifies that a curated implicit id
// resolves the same way a configured instance does.
func TestBuildInitialProfile_CuratedInstance(t *testing.T) {
	profile, err := buildInitialProfile(serveTestClient(t), cmdutil.ModelRef{Provider: "openai", Model: "gpt-5"}, "")
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

	if err := shutdownServeTestDaemon(context.Background(), entries[0].Address, entries[0].SessionID); err != nil {
		t.Fatalf("thread/shutdown: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runServe did not exit after thread/shutdown")
	}

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("rendezvous file should be removed, stat err=%v", err)
	}
}

func TestRunServeNonInteractiveFlagControlsPromptAddendum(t *testing.T) {
	oldLoadClient := serveLoadClient
	serveLoadClient = func(string) (*llm.Client, error) {
		client := llm.NewClient()
		client.Register(serveLoggingAdapter{})
		return client, nil
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
			if err := shutdownServeTestDaemon(context.Background(), entry.Address, entry.SessionID); err != nil {
				t.Fatalf("thread/shutdown: %v", err)
			}

			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("runServe: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("runServe did not exit after thread/shutdown")
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
	serveLoadClient = func(string) (*llm.Client, error) {
		client := llm.NewClient()
		client.Register(adapter)
		return client, nil
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
		ClientMutationID:   "shutdown-in-flight",
		ExpectedInstanceID: entry.SessionID,
		Ref:                ref,
		Input:              []appwire.InputItem{{Type: "text", Text: "stay busy until shutdown"}},
	}); err != nil {
		t.Fatalf("TurnStart: %v", err)
	}

	select {
	case <-adapter.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("fake adapter was not called")
	}

	if err := shutdownServeTestDaemon(context.Background(), entry.Address, entry.SessionID); err != nil {
		t.Fatalf("thread/shutdown: %v", err)
	}

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
	serveLoadClient = func(string) (*llm.Client, error) {
		client := llm.NewClient()
		client.Register(serveLoggingAdapter{called: called})
		return client, nil
	}
	t.Cleanup(func() {
		serveLoadClient = oldLoadClient
	})

	client, closeAPILog, err := newServeLLMClient(stateDir, nil)
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
	serveLoadClient = func(string) (*llm.Client, error) {
		client := llm.NewClient()
		client.Register(serveLoggingAdapter{})
		return client, nil
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
			deps.seedMarketplaces = func(context.Context) error { return nil }
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

// Resolving plugins can wait on the plugin store lock, so the wait has to be
// one an interrupt ends. That means the signal-derived context exists before
// plugin resolution rather than after it, when the daemon starts listening.
func TestServeResolvesPluginsOnTheSignalContext(t *testing.T) {
	deps := defaultServeDeps()
	var stopSignals context.CancelFunc
	deps.notifyContext = func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		next, stop := context.WithCancel(ctx)
		stopSignals = stop
		return next, stop
	}
	var resolveCtx context.Context
	deps.resolvePlugins = func(ctx context.Context, _ []string, _ *[]string) (plugins.LaunchPluginResolution, error) {
		resolveCtx = ctx
		return plugins.LaunchPluginResolution{SelectionErrors: []plugins.PluginSelectionError{{Name: "missing-plugin", Reason: "no valid plugin candidate"}}}, nil
	}
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func(context.Context) error { return nil }

	err := runServeWithDeps([]string{"--enabled-plugins=missing-plugin"}, deps)
	if err == nil || !strings.Contains(err.Error(), "enabled plugin selection is unavailable") {
		t.Fatalf("serve error = %v, want the selection error that stops this startup", err)
	}
	if resolveCtx == nil {
		t.Fatal("plugin resolution never ran")
	}
	if stopSignals == nil {
		t.Fatal("the interrupt handler was not installed before plugin resolution")
	}
	stopSignals()
	if resolveCtx.Err() == nil {
		t.Error("plugin resolution ran on a context an interrupt cannot reach")
	}
}

// An interrupt that arrives during startup ends the startup. The steps between
// resolving plugins and binding the listener are the slow ones — seeding
// marketplaces takes the same store lock a plugin install holds, probing the
// login shell PATH and provisioning the sandbox run subprocesses — and
// net.ListenConfig.Listen on a literal address binds happily with a cancelled
// context, so without an explicit read of the context the daemon finishes
// startup, binds, shuts down again and exits 0 with nothing said.
func TestServeStopsStartupOnAnInterrupt(t *testing.T) {
	tests := []struct {
		name string
		// step names the gate the interrupt has to trip, so each arm proves
		// its own gate rather than being caught by a later one.
		step string
		arm  func(t *testing.T, deps *serveDeps, interrupt func())
	}{
		{
			name: "seeding marketplaces",
			step: "seeding default marketplaces",
			arm: func(t *testing.T, deps *serveDeps, interrupt func()) {
				var seedCtx context.Context
				deps.seedMarketplaces = func(ctx context.Context) error {
					interrupt()
					seedCtx = ctx
					return nil
				}
				t.Cleanup(func() {
					if seedCtx == nil || seedCtx.Err() == nil {
						t.Errorf("seeding ran on %v, want the context an interrupt cancels", seedCtx)
					}
				})
			},
		},
		{
			name: "probing the login shell PATH",
			step: "probing the login shell PATH",
			arm: func(_ *testing.T, deps *serveDeps, interrupt func()) {
				// The probe takes no context, so an interrupt during it (or
				// during the profile work just before it) is only noticed by
				// the gate that follows it.
				applyCheap := deps.applyCheap
				deps.applyCheap = func(profile *provider.Profile, cheap string, client *llm.Client) (*provider.Profile, error) {
					interrupt()
					return applyCheap(profile, cheap, client)
				}
			},
		},
		{
			name: "provisioning the sandbox",
			step: "provisioning the sandbox",
			arm: func(t *testing.T, deps *serveDeps, interrupt func()) {
				provisionServeScratchThatMustBeDisposed(t, deps, interrupt)
			},
		},
		{
			name: "creating the session",
			step: "creating the session",
			arm: func(t *testing.T, deps *serveDeps, interrupt func()) {
				var sess *agent.Session
				newSession := deps.newSession
				deps.newSession = func(client *llm.Client, profile *provider.Profile, env execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
					created, err := newSession(client, profile, env, cfg)
					sess = created
					interrupt()
					return created, err
				}
				// The session is live by the time this gate reads the context,
				// so ending the startup has to take it down: a returned-from
				// startup that leaves a session running leaks its environment
				// and its child processes.
				t.Cleanup(func() {
					if sess == nil {
						t.Error("the session was never created")
						return
					}
					if state := sess.State(); state != agent.SessionClosed {
						t.Errorf("session state = %v, want %v after the startup ended", state, agent.SessionClosed)
					}
				})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installServeScriptedProvider(t, &scriptedProvider{name: "openai"})
			deps := defaultServeDeps()
			var stopSignals context.CancelFunc
			deps.notifyContext = func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
				next, stop := context.WithCancel(ctx)
				stopSignals = stop
				return next, stop
			}
			deps.ensureConfigDirs = func() error { return nil }
			deps.seedMarketplaces = func(context.Context) error { return nil }
			listened := false
			deps.listen = func(context.Context, string, string) (net.Listener, error) {
				listened = true
				return nil, errors.New("a listener was bound after the interrupt")
			}
			tt.arm(t, &deps, func() { stopSignals() })

			err := runServeWithDeps([]string{
				"--model", "openai/gpt-test", "--dir", t.TempDir(), "--state-dir", t.TempDir(),
			}, deps)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("serve error = %v, want the interrupt that ended startup", err)
			}
			if want := "interrupted while " + tt.step; !strings.Contains(err.Error(), want) {
				t.Errorf("serve error = %q, want it to say %q", err, want)
			}
			if listened {
				t.Error("bound a listener for a startup an interrupt had already ended")
			}
		})
	}
}

// serveScratchThatMustBeDisposed gives env the owned session scratch a
// sandboxed startup provisions — a write-blocked off policy takes that path
// without needing a kernel backend this host may not have — and holds the
// startup to disposing of it. Nothing releases the directory or the flock
// lease under it until a session owns the environment and its Close does, so
// every way out before that hand-off owes them.
func serveScratchThatMustBeDisposed(t *testing.T, env *execenv.LocalExecutionEnvironment) error {
	t.Helper()
	if err := env.EnableSandbox(&sandbox.ResolvedPolicy{Mode: sandbox.ModeOff, WriteBlocked: true}); err != nil {
		return err
	}
	scratch := env.SessionScratchDir()
	// Registered before the assertion so it runs after it: a failing test must
	// not leave the scratch behind either.
	t.Cleanup(env.DisposeSandboxScratch)
	t.Cleanup(func() {
		if scratch == "" {
			t.Error("provisioning left no session scratch to dispose")
			return
		}
		if _, err := os.Stat(scratch); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("session scratch %s survived the abandoned startup: stat err = %v", scratch, err)
		}
	})
	return nil
}

// provisionServeScratchThatMustBeDisposed is serveScratchThatMustBeDisposed as
// a startup's sandbox-provisioning step, running then once the scratch is in
// place.
func provisionServeScratchThatMustBeDisposed(t *testing.T, deps *serveDeps, then func()) {
	t.Helper()
	deps.provisionSandbox = func(env *execenv.LocalExecutionEnvironment, _ *agent.SessionConfig, _ string) error {
		if err := serveScratchThatMustBeDisposed(t, env); err != nil {
			return err
		}
		then()
		return nil
	}
}

// A session that was never built never takes the environment over, so the
// startup that provisioned its scratch is the one that owes its disposal.
func TestServeDisposesTheSandboxScratchWhenNoSessionIsCreated(t *testing.T) {
	installServeScriptedProvider(t, &scriptedProvider{name: "openai"})
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func(context.Context) error { return nil }
	provisionServeScratchThatMustBeDisposed(t, &deps, func() {})
	deps.newSession = func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, agent.SessionConfig) (*agent.Session, error) {
		return nil, errors.New("no session today")
	}
	deps.listen = func(context.Context, string, string) (net.Listener, error) {
		t.Error("bound a listener for a startup that has no session")
		return nil, errors.New("a listener was bound without a session")
	}

	err := runServeWithDeps([]string{
		"--model", "openai/gpt-test", "--dir", t.TempDir(), "--state-dir", t.TempDir(),
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "session creation") {
		t.Fatalf("serve error = %v, want the session-creation failure", err)
	}
}

// A resume provisions the environment's sandbox from the session's PERSISTED
// mode inside the restore (provisionRestoredSandbox), and the restore can
// still fail after that — env.Initialize, the transcript, the artifact store —
// with no session built to own what was provisioned.
func TestServeDisposesTheSandboxScratchWhenRestoreFails(t *testing.T) {
	installServeScriptedProvider(t, &scriptedProvider{name: "openai"})
	stateDir := t.TempDir()
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sessionID, ProfileID: "openai", Model: "gpt-test",
		CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(2, 0).UTC(),
	}); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	deps := defaultServeDeps()
	deps.ensureConfigDirs = func() error { return nil }
	deps.seedMarketplaces = func(context.Context) error { return nil }
	deps.restoreSession = func(_ *llm.Client, _ *provider.Profile, env execenv.ExecutionEnvironment, _ schema.SessionMeta, _ agent.RestoreSessionConfig) (*agent.Session, error) {
		local, ok := env.(*execenv.LocalExecutionEnvironment)
		if !ok {
			t.Fatalf("restore got a %T, want the local environment serve built", env)
		}
		if err := serveScratchThatMustBeDisposed(t, local); err != nil {
			return nil, err
		}
		return nil, errors.New("restore failed after the sandbox was provisioned")
	}
	deps.listen = func(context.Context, string, string) (net.Listener, error) {
		t.Error("bound a listener for a resume that never restored")
		return nil, errors.New("a listener was bound without a session")
	}

	err := runServeWithDeps([]string{
		"--resume", sessionID, "--dir", stateDir, "--state-dir", stateDir, "--run-dir", t.TempDir(),
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "restore session") {
		t.Fatalf("serve error = %v, want the restore failure", err)
	}
}
