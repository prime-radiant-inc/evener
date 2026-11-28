package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/appwire/appwiretest"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
	"primeradiant.com/evener/rendezvous"
	"primeradiant.com/evener/server"
)

type idempotencyTurnAdapter struct {
	calls atomic.Int32
}

func (a *idempotencyTurnAdapter) Name() string { return "openai" }

func (a *idempotencyTurnAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	a.calls.Add(1)
	args, _ := json.Marshal(map[string]any{
		"message": "ok",
		"next":    "send_and_end_turn",
		"output":  map[string]any{"message": "", "data": map[string]any{}, "artifacts": []string{}},
	})
	return llm.Response{
		Provider: "openai",
		Model:    req.Model,
		Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{{
				Kind:     llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{ID: "c1", Name: "communicate", Arguments: args, Type: "function"},
			}},
		},
		Finish: llm.FinishReason{Reason: llm.FinishReasonToolCalls},
	}, nil
}

func (a *idempotencyTurnAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func localEntry(httpURL, sessionID string) rendezvous.Entry {
	return rendezvous.Entry{
		PID:        os.Getpid(),
		Protocol:   appwire.ProtocolVersion,
		Endpoint:   "ws" + httpURL[len("http"):],
		SourceID:   "local",
		ThreadID:   sessionID,
		SessionID:  sessionID,
		InstanceID: sessionID,
	}
}

func spawnerWritingEntry(t *testing.T, runDir string, entry rendezvous.Entry, spawns *atomic.Int32) *fakeRPCSpawner {
	return &fakeRPCSpawner{spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
		if spawns != nil {
			spawns.Add(1)
		}
		writeRendezvous(t, runDir, entry)
		return entry, nil
	}}
}

func TestThreadStartIdempotencyInvalidClientMutationID(t *testing.T) {
	stateRoot := t.TempDir()
	var spawns atomic.Int32
	spawner := &fakeRPCSpawner{spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
		spawns.Add(1)
		return rendezvous.Entry{}, errors.New("must not spawn")
	}}
	hub := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
		HubStateRoot: stateRoot,
		RunDir:       t.TempDir(),
		Spawner:      spawner,
	}))
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	cwd := t.TempDir()
	for _, badID := range []string{
		strings.Repeat("a", 129),
		" leading",
		"trailing ",
		"\ttab",
		"   ",
		"bad\nnewline",
		"bad\x7fdel",
	} {
		_, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
			ClientMutationID: badID,
			Model:            "openai/gpt-5",
			CWD:              cwd,
		})
		wire, data := wireErrorData(t, err)
		if wire.Code != appwire.CodeInvalidParams || data.EvenerErrorInfo != appwire.ErrorInvalidParams ||
			data.MutationOutcome != appwire.MutationOutcomeNotAccepted || data.ClientMutationID != "" {
			t.Fatalf("badID=%q got code=%d data=%+v, want InvalidParams NotAccepted(\"\")", badID, wire.Code, data)
		}
	}
	if spawns.Load() != 0 {
		t.Fatalf("spawns=%d, want 0", spawns.Load())
	}
	if _, err := os.Stat(filepath.Join(stateRoot, "creations", "state.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("creations/state.json stat err = %v, want not exist", err)
	}
}

func TestThreadStartIdempotencyPreSpawnAndPreExecRefusals(t *testing.T) {
	t.Setenv("EVENER_MODEL", "")
	stateRoot := t.TempDir()
	runDir := t.TempDir()
	sessionID := hubtest.SessionID(t)
	daemon, _ := startSkillInputHubDaemon(t, sessionID, appwire.ThreadCapabilities{Send: true}, nil)
	entry := localEntry(daemon.URL, sessionID)

	var preExecFail atomic.Bool
	preExecFail.Store(true)
	var spawns atomic.Int32
	spawner := &fakeRPCSpawner{spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
		spawns.Add(1)
		if preExecFail.Load() {
			return rendezvous.Entry{}, preExecSpawnError{err: errors.New("binary missing before exec")}
		}
		writeRendezvous(t, runDir, entry)
		return entry, nil
	}}
	hub := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
		HubStateRoot: stateRoot,
		RunDir:       runDir,
		Roster:       hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusIdle}),
		Spawner:      spawner,
	}))
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	cwd := t.TempDir()

	// Pre-spawn refusal: missing CWD
	_, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		ClientMutationID: "mut-missing-cwd",
		Model:            "openai/gpt-5",
		CWD:              filepath.Join(cwd, "does-not-exist"),
	})
	_, data := wireErrorData(t, err)
	if data.MutationOutcome != appwire.MutationOutcomeNotAccepted || data.ClientMutationID != "mut-missing-cwd" {
		t.Fatalf("missing cwd data=%+v", data)
	}

	// Pre-spawn refusal: voice presentation on public RPC
	_, err = appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		ClientMutationID: "mut-voice",
		Model:            "openai/gpt-5",
		CWD:              cwd,
		Input: []appwire.InputItem{{
			Type:              "text",
			Text:              "voice",
			VoicePresentation: &appwire.VoicePresentationContext{Version: 1, DeliveryID: "d1"},
		}},
	})
	_, data = wireErrorData(t, err)
	if data.MutationOutcome != appwire.MutationOutcomeNotAccepted || data.ClientMutationID != "mut-voice" {
		t.Fatalf("voice data=%+v", data)
	}

	// Direct spawnDaemonWithProviderConfig failure before cmd.Start() returns preExecSpawnError.
	_, spawnDirectErr := spawnDaemonWithProviderConfig(t.Context(), filepath.Join(t.TempDir(), "missing-evener"), runDir, hubcore.SpawnRequest{}, time.Second, os.Stderr, nil)
	if _, ok := errors.AsType[preExecSpawnError](spawnDirectErr); !ok {
		t.Fatalf("spawnDaemonWithProviderConfig err = %T (%v), want preExecSpawnError", spawnDirectErr, spawnDirectErr)
	}
	if errors.Unwrap(spawnDirectErr) == nil {
		t.Fatal("preExecSpawnError.Unwrap returned nil")
	}

	// Pre-exec spawn error: HubSpawner.Spawn fails before cmd.Start()
	realSpawner := &HubSpawner{
		Cfg:          Config{SpawnTimeout: time.Second},
		EvenerBinary: filepath.Join(t.TempDir(), "no-such-evener"),
		RunDir:       runDir,
	}
	preExecCfg := testWebConfig(t, hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		RunDir:       runDir,
		Spawner:      realSpawner,
	})
	preExecHub := newHubRPCTestServer(t, preExecCfg)
	defer preExecHub.Close()
	preExecClient := dialHubRPCInitialized(t, preExecHub)
	_, err = appwiretest.ThreadStart(context.Background(), preExecClient, appwire.ThreadStartParams{
		ClientMutationID: "mut-real-preexec",
		Model:            "openai/gpt-5",
		CWD:              cwd,
	})
	_, data = wireErrorData(t, err)
	if data.MutationOutcome != appwire.MutationOutcomeNotAccepted || data.ClientMutationID != "mut-real-preexec" {
		t.Fatalf("real preExecSpawnError data=%+v", data)
	}

	// Pre-exec spawn error rolls back spawning record so fixing cause allows reuse of same ID.
	_, err = appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		ClientMutationID: "mut-preexec-recover",
		Model:            "openai/gpt-5",
		CWD:              cwd,
	})
	_, data = wireErrorData(t, err)
	if data.MutationOutcome != appwire.MutationOutcomeNotAccepted || data.ClientMutationID != "mut-preexec-recover" {
		t.Fatalf("preExec data=%+v", data)
	}
	raw, readErr := os.ReadFile(filepath.Join(stateRoot, "creations", "state.json"))
	if readErr != nil {
		t.Fatalf("read creations state: %v", readErr)
	}
	if strings.Contains(string(raw), "mut-preexec-recover") || strings.Contains(string(raw), "mut-missing-cwd") || strings.Contains(string(raw), "mut-voice") {
		t.Fatalf("journal unexpectedly retained refused records: %s", raw)
	}

	preExecFail.Store(false)
	resp, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		ClientMutationID: "mut-preexec-recover",
		Model:            "openai/gpt-5",
		CWD:              cwd,
	})
	if err != nil {
		t.Fatalf("retry after preExec fix failed: %v", err)
	}
	if resp.ClientMutationID != "mut-preexec-recover" || resp.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("recovered resp = %+v", resp)
	}

	// Pre-exec spawn error whose RollbackSpawning also fails BeforeRename still allows retry once storage recovers.
	var rbStore *hubcore.CreationStore
	rbStore, err = hubcore.NewCreationStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var writeCount atomic.Int32
	rbStore.SetFaultsForTest(func() error {
		if writeCount.Add(1) == 2 { // 1 = Acquire, 2 = RollbackSpawning
			return errors.New("disk full during rollback")
		}
		return nil
	}, nil)
	preExecFail.Store(true)
	rbHub := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
		CreationStore: rbStore,
		RunDir:        runDir,
		Roster:        hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusIdle}),
		Spawner:       spawner,
	}))
	defer rbHub.Close()
	rbClient := dialHubRPCInitialized(t, rbHub)
	_, err = appwiretest.ThreadStart(context.Background(), rbClient, appwire.ThreadStartParams{ClientMutationID: "mut-preexec-rb-fault", Model: "openai/gpt-5", CWD: cwd})
	_, data = wireErrorData(t, err)
	if data.MutationOutcome != appwire.MutationOutcomeNotAccepted {
		t.Fatalf("preExec rollback-fault data=%+v, want NotAccepted", data)
	}
	preExecFail.Store(false)
	rbResp, err := appwiretest.ThreadStart(context.Background(), rbClient, appwire.ThreadStartParams{ClientMutationID: "mut-preexec-rb-fault", Model: "openai/gpt-5", CWD: cwd})
	if err != nil || rbResp.ClientMutationID != "mut-preexec-rb-fault" || rbResp.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("retry after RollbackSpawning BeforeRename failed: resp=%+v err=%v", rbResp, err)
	}

	// Post-exec spawn failure transitions to uncertain + Blocked.
	postSpawner := &fakeRPCSpawner{spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
		return rendezvous.Entry{}, errors.New("daemon exited after start before rendezvous")
	}}
	postHub := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		RunDir:       t.TempDir(),
		Spawner:      postSpawner,
	}))
	defer postHub.Close()
	postClient := dialHubRPCInitialized(t, postHub)
	_, err = appwiretest.ThreadStart(context.Background(), postClient, appwire.ThreadStartParams{
		ClientMutationID: "mut-postexec",
		Model:            "openai/gpt-5",
		CWD:              cwd,
	})
	_, data = wireErrorData(t, err)
	if data.EvenerErrorInfo != appwire.ErrorMutationOutcomeUnknown ||
		data.ClientMutationID != "mut-postexec" ||
		data.MutationOutcome != appwire.MutationOutcomeUnknown ||
		data.RetryDisposition != appwire.RetryDispositionBlocked {
		t.Fatalf("post-exec spawn failure data=%+v, want MutationUnknown+Blocked", data)
	}
}

func TestThreadStartIdempotencyFastCheapMissingCredentialPreflightRetry(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	clearProviderKeysFromEnvironment(t)
	t.Setenv("EVENER_MODEL", "")
	t.Setenv("EVENER_OFFLINE", "1")

	fakeLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-test","object":"model"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer fakeLLM.Close()

	root := t.TempDir()
	providersPath := filepath.Join(root, "providers.toml")
	credsPath := filepath.Join(root, "credentials.toml")
	stateRoot := filepath.Join(root, "hub")
	launchConfigRoot := filepath.Join(root, "launch")
	runDir := filepath.Join(root, "run")
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVENER_PROVIDERS_CONFIG", providersPath)
	t.Setenv("EVENER_CREDENTIALS_CONFIG", credsPath)
	providersTOML := fmt.Sprintf("default = \"fake\"\n[providers.fake]\nbase = \"openai-compatible\"\nbase_url = %q\napi_key = \"fake-primary-key\"\ndefault_model = \"gpt-test\"\n", fakeLLM.URL+"/v1")
	if err := os.WriteFile(providersPath, []byte(providersTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := credentials.LoadStore(credsPath)
	if err != nil {
		t.Fatal(err)
	}
	reg := hubcore.NewProviderRegistry(func(opts ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		return cmdutil.LoadRegistry(append(opts, registry.WithOffline(true), registry.WithoutCache())...)
	})
	if err := reg.Reload(); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(liveStackBinaries(t, repoRootForTestMain()), "evener")
	spawner := &HubSpawner{
		Cfg:                 DefaultConfig(),
		EvenerBinary:        binary,
		RunDir:              runDir,
		HubToken:            "generated-token",
		Registry:            reg,
		ProvidersConfigPath: providersPath,
		CredentialsPath:     credsPath,
	}
	hub := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
		HubStateRoot:        stateRoot,
		LaunchConfigRoot:    launchConfigRoot,
		RunDir:              runDir,
		StateDir:            filepath.Join(root, "state"),
		Registry:            reg,
		CredsStore:          store,
		ProvidersConfigPath: providersPath,
		CredentialsPath:     credsPath,
		Spawner:             spawner,
	}))
	defer hub.Close()
	t.Cleanup(func() {
		entries, _ := rendezvous.List(runDir)
		for _, e := range entries {
			if e.PID > 0 {
				if p, err := os.FindProcess(e.PID); err == nil {
					_ = p.Kill()
				}
			}
		}
	})
	client := dialHubRPCInitialized(t, hub)

	params := appwire.ThreadStartParams{
		ClientMutationID: "mut-fastcheap-cred",
		Model:            "fake/gpt-test",
		CWD:              cwd,
		LaunchOverrides: &appwire.LaunchConfigLayer{
			FastCheapModel: "anthropic/claude-haiku-4-5-20251001",
		},
	}
	_, err = appwiretest.ThreadStart(context.Background(), client, params)
	_, data := wireErrorData(t, err)
	if data.EvenerErrorInfo != appwire.ErrorHubLaunch ||
		data.MutationOutcome != appwire.MutationOutcomeNotAccepted ||
		data.ClientMutationID != params.ClientMutationID {
		t.Fatalf("missing fast/cheap credential data=%+v, want HubLaunch+NotAccepted(%q)", data, params.ClientMutationID)
	}
	if entries, listErr := rendezvous.List(runDir); listErr == nil && len(entries) != 0 {
		t.Fatalf("unexpectedly spawned daemon before credential check: %+v", entries)
	}
	if raw, readErr := os.ReadFile(filepath.Join(stateRoot, "creations", "state.json")); readErr == nil && strings.Contains(string(raw), params.ClientMutationID) {
		t.Fatalf("journal unexpectedly retained pre-exec refused key: %s", raw)
	}

	// Global launch.toml fast_cheap_model is also validated pre-exec before spawn.
	if err := os.MkdirAll(launchConfigRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(launchConfigRoot, "launch.toml"), []byte("fast_cheap_model = \"groq/llama-3.3-70b-versatile\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		ClientMutationID: "mut-fastcheap-global-layer",
		Model:            "fake/gpt-test",
		CWD:              cwd,
	})
	_, globalData := wireErrorData(t, err)
	if globalData.EvenerErrorInfo != appwire.ErrorHubLaunch ||
		globalData.MutationOutcome != appwire.MutationOutcomeNotAccepted ||
		globalData.ClientMutationID != "mut-fastcheap-global-layer" {
		t.Fatalf("global layer missing fast/cheap credential data=%+v, want HubLaunch+NotAccepted", globalData)
	}
	if err := os.Remove(filepath.Join(launchConfigRoot, "launch.toml")); err != nil {
		t.Fatal(err)
	}

	var authResp appwire.AuthStatusResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerAuthApiKeySet, appwire.AuthApiKeySetParams{
		Provider: "anthropic",
		Value:    "sk-ant-recovered-test",
	}, &authResp); err != nil {
		t.Fatalf("evener/auth/apiKey/set: %v", err)
	}
	if authResp.ActiveSource != "store" {
		t.Fatalf("auth activeSource=%q, want store", authResp.ActiveSource)
	}

	resp, err := appwiretest.ThreadStart(context.Background(), client, params)
	if err != nil {
		t.Fatalf("retry after adding fast/cheap credential failed: %v", err)
	}
	if resp.ClientMutationID != params.ClientMutationID || resp.Disposition != appwire.MutationDispositionApplied || resp.Thread.ID == "" {
		t.Fatalf("retry resp = %+v, want Applied for %q", resp, params.ClientMutationID)
	}
	if err := client.ThreadShutdown(context.Background(), appwire.ThreadShutdownParams{Ref: resp.Thread.Evener.Ref}); err != nil {
		t.Fatalf("shutdown spawned thread: %v", err)
	}
}

func TestThreadStartIdempotencyCorruptJournalFailSoft(t *testing.T) {
	stateRoot := t.TempDir()
	statePath := filepath.Join(stateRoot, "creations", "state.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	const corrupt = `{corrupt-creation-journal`
	if err := os.WriteFile(statePath, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}

	rt := &hubRuntime{hubStateRoot: stateRoot, stderr: os.Stderr}
	if err := rt.openDurableState(); err != nil {
		t.Fatalf("openDurableState failed on corrupt creations/state.json: %v", err)
	}
	if rt.creationStore != nil || rt.creationStoreErr == nil {
		t.Fatalf("openDurableState store=%p err=%v, want nil store and non-nil creationStoreErr", rt.creationStore, rt.creationStoreErr)
	}

	runDir := t.TempDir()
	sessionID := hubtest.SessionID(t)
	daemon, _ := startSkillInputHubDaemon(t, sessionID, appwire.ThreadCapabilities{Send: true}, nil)
	entry := localEntry(daemon.URL, sessionID)
	var spawns atomic.Int32
	spawner := spawnerWritingEntry(t, runDir, entry, &spawns)
	hub, web := newHubRPCTestServerWithWeb(t, testWebConfig(t, hubcore.WebConfig{
		HubStateRoot: stateRoot,
		RunDir:       runDir,
		Roster:       hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusIdle}),
		Spawner:      spawner,
	}))
	if web.cfg.CreationStore != nil || web.creationStoreErr == nil {
		t.Fatalf("web creationStore=%p err=%v, want nil store and retained creationStoreErr", web.cfg.CreationStore, web.creationStoreErr)
	}
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	initResp, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion})
	if err != nil {
		t.Fatalf("Initialize on corrupt creation store: %v", err)
	}
	if initResp.Features.ThreadStartIdempotency {
		t.Fatal("Features.ThreadStartIdempotency must be false when creations/state.json is corrupt")
	}

	cwd := t.TempDir()
	_, err = appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		ClientMutationID: "mut-corrupt-keyed",
		Model:            "openai/gpt-5",
		CWD:              cwd,
	})
	_, data := wireErrorData(t, err)
	if data.EvenerErrorInfo != appwire.ErrorMutationOutcomeUnknown ||
		data.ClientMutationID != "mut-corrupt-keyed" ||
		data.MutationOutcome != appwire.MutationOutcomeUnknown ||
		data.RetryDisposition != appwire.RetryDispositionBlocked {
		t.Fatalf("keyed call on corrupt store data=%+v, want MutationUnknown+Blocked", data)
	}
	if spawns.Load() != 0 {
		t.Fatalf("spawns=%d after keyed refusal, want 0", spawns.Load())
	}

	// Keyless thread/start still succeeds.
	resp, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		Model: "openai/gpt-5",
		CWD:   cwd,
	})
	if err != nil {
		t.Fatalf("keyless thread/start failed on corrupt creation store: %v", err)
	}
	if resp.Thread.ID != sessionID || resp.ClientMutationID != "" || resp.Disposition != "" {
		t.Fatalf("unexpected keyless resp=%+v", resp)
	}

	afterBytes, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterBytes) != corrupt {
		t.Fatalf("corrupt file was modified: %q", afterBytes)
	}
}

func TestThreadStartIdempotencyCompletedReplayAfterDroppedResponse(t *testing.T) {
	stateRoot := t.TempDir()
	runDir := t.TempDir()
	sessionID := hubtest.SessionID(t)
	var startTurns atomic.Int32
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "local",
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener:    appwire.EvenerThread{Ref: "local:" + sessionID, InstanceID: sessionID, Capabilities: appwire.ThreadCapabilities{Send: true}},
			Turns:     []appwire.Turn{{ID: "turn_0", Items: []appwire.ThreadItem{{Type: "userMessage", Text: "should strip"}}}},
		}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		startTurns.Add(1)
		return appwire.TurnStartResponse{
			Turn:    appwire.Turn{ID: "turn_1", Status: "inProgress"},
			Receipt: appwire.MutationReceipt{ClientMutationID: params.ClientMutationID, Disposition: appwire.MutationDispositionApplied, ThreadID: sessionID, TurnID: "turn_1"},
		}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	entry := localEntry(daemonHTTP.URL, sessionID)
	var spawns atomic.Int32
	spawner := spawnerWritingEntry(t, runDir, entry, &spawns)
	hub := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
		HubStateRoot: stateRoot,
		RunDir:       runDir,
		Roster:       hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusActive}),
		Spawner:      spawner,
	}))
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	if !client.Features().ThreadStartIdempotency {
		t.Fatal("expected ThreadStartIdempotency feature flag")
	}

	params := appwire.ThreadStartParams{
		ClientMutationID: "mut-replay-1",
		Model:            "openai/gpt-5",
		CWD:              t.TempDir(),
		Input:            []appwire.InputItem{{Type: "text", Text: "first turn prompt"}},
	}
	first, err := appwiretest.ThreadStart(context.Background(), client, params)
	if err != nil {
		t.Fatalf("first ThreadStart: %v", err)
	}
	if first.ClientMutationID != "mut-replay-1" || first.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("first resp idempotency fields = %+v", first)
	}

	replay, err := appwiretest.ThreadStart(context.Background(), client, params)
	if err != nil {
		t.Fatalf("replay ThreadStart: %v", err)
	}
	if replay.ClientMutationID != "mut-replay-1" || replay.Disposition != appwire.MutationDispositionReplayed {
		t.Fatalf("replay metadata = %+v", replay)
	}
	if replay.Thread.ID != first.Thread.ID || replay.Thread.Evener.Ref != first.Thread.Evener.Ref ||
		replay.Thread.Turns != nil || replay.Turn.ID != "turn_1" || replay.Turn.Status != "inProgress" {
		t.Fatalf("replay payload mismatch: first=%+v replay=%+v", first, replay)
	}
	if spawns.Load() != 1 || startTurns.Load() != 1 {
		t.Fatalf("spawns=%d startTurns=%d, want 1 and 1", spawns.Load(), startTurns.Load())
	}
}

func TestThreadStartIdempotencyTransientFailureAndRealDaemonTurn1Dedup(t *testing.T) {
	stateRoot := t.TempDir()
	runDir := t.TempDir()
	sessionStateDir := t.TempDir()
	workDir := t.TempDir()

	adapter := &idempotencyTurnAdapter{}
	llmClient := llm.NewClient()
	llmClient.Register(adapter)
	llmClient.Register(&retirementNamerAdapter{})
	sess, err := agent.NewSession(
		llmClient,
		provider.WithCheapModel(provider.NewOpenAIProfile("gpt-5.2"), retirementNamerProvider+"/namer"),
		execenv.NewLocalExecutionEnvironment(workDir),
		agent.SessionConfig{StateDir: sessionStateDir, Sandbox: "off", AgentsDocPath: filepath.Join(workDir, "absent-AGENTS.md")},
	)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sessionID := sess.ID()

	var wakeWG sync.WaitGroup
	var failTurnOnce atomic.Bool
	failTurnOnce.Store(true)
	var seenTurnMutationIDs []string
	var seenInstanceIDs []string
	var mu sync.Mutex
	var injectPostExecTransient atomic.Bool

	daemonSrv := server.NewServer(server.ServerConfig{StateDir: sessionStateDir})
	setAppIdentity(t, daemonSrv, "local", sessionID)
	daemonSrv.SetState(appwire.ThreadStatusIdle)
	daemonSrv.SetRetrySafeTurnFunctions(server.RetrySafeTurnFunctions{
		Start: func(params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			mu.Lock()
			seenTurnMutationIDs = append(seenTurnMutationIDs, params.ClientMutationID)
			seenInstanceIDs = append(seenInstanceIDs, params.ExpectedInstanceID)
			mu.Unlock()
			if failTurnOnce.Swap(false) {
				return appwire.TurnStartResponse{}, appwire.RetryableSessionUnavailable("transient turn outage")
			}
			resp, err := sess.AcceptClientMutationStart(params)
			if err != nil {
				return appwire.TurnStartResponse{}, err
			}
			if injectPostExecTransient.Swap(false) {
				return appwire.TurnStartResponse{}, appwire.RetryableSessionUnavailable("dropped reply after daemon accepted turn 1")
			}
			return resp, nil
		},
	})
	sess.SetClientMutationStartWakeFunc(func() {
		wakeWG.Go(func() {
			_, _, _ = sess.ProcessClientMutationStart(context.Background(), nil)
		})
	})
	eventsDrained := make(chan struct{})
	sess.ConsumeEventsLossless(func(ev events.SessionEvent) {
		server.BridgeEvent(daemonSrv, ev, nil)
	}, func() { close(eventsDrained) })
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemonSrv.AppServer().ServeWebSocket))
	t.Cleanup(func() {
		daemonHTTP.Close()
		daemonSrv.Close()
		sess.Close()
		<-eventsDrained
		wakeWG.Wait()
	})

	entry := localEntry(daemonHTTP.URL, sessionID)
	entry.Endpoint += "/rpc"
	entry.StateDir = sessionStateDir
	var spawns atomic.Int32
	spawner := spawnerWritingEntry(t, runDir, entry, &spawns)
	hub := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
		HubStateRoot: stateRoot,
		RunDir:       runDir,
		Roster:       hubcore.NewRoster(runDir, &hubcore.StatusProber{}),
		Spawner:      spawner,
	}))
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	params := appwire.ThreadStartParams{
		ClientMutationID: "mut-partial-real-daemon",
		Model:            "openai/gpt-5",
		CWD:              workDir,
		Input:            []appwire.InputItem{{Type: "text", Text: "run turn one exactly once"}},
	}
	// Attempt 1: Spawn succeeds, StartTurn fails with RetryableSessionUnavailable.
	_, err = appwiretest.ThreadStart(context.Background(), client, params)
	wire := requireWireError(t, err, appwire.CodeUnavailable)
	raw, _ := json.Marshal(wire.Data)
	var partial appwire.ThreadStartPartialErrorData
	if err := json.Unmarshal(raw, &partial); err != nil {
		t.Fatalf("unmarshal partial: %v", err)
	}
	if partial.CreatedRef != "local:"+sessionID || partial.CreatedInstanceID != sessionID || partial.ClientMutationID != "mut-partial-real-daemon" {
		t.Fatalf("partial identity = %+v", partial)
	}
	if partial.RetryDisposition == appwire.RetryDispositionBlocked {
		t.Fatalf("retryable partial error must NOT stamp RetryDispositionBlocked: %+v", partial)
	}

	// Attempt 2: Skips Spawn, real daemon executes Turn 1 once, then drops reply transiently (leaving spawned on disk).
	injectPostExecTransient.Store(true)
	_, err = appwiretest.ThreadStart(context.Background(), client, params)
	wire2 := requireWireError(t, err, appwire.CodeUnavailable)
	raw2, _ := json.Marshal(wire2.Data)
	var partial2 appwire.ThreadStartPartialErrorData
	if err := json.Unmarshal(raw2, &partial2); err != nil {
		t.Fatalf("unmarshal partial2: %v", err)
	}
	if partial2.RetryDisposition == appwire.RetryDispositionBlocked || partial2.ClientMutationID != params.ClientMutationID {
		t.Fatalf("post-daemon transient partial2 = %+v", partial2)
	}

	// Attempt 3: Skips Spawn, replays StartTurn with identical InitialTurnMutationID -> real daemon deduplicates Turn 1.
	resp, err := appwiretest.ThreadStart(context.Background(), client, params)
	if err != nil {
		t.Fatalf("retry after partial failure failed: %v", err)
	}
	if spawns.Load() != 1 {
		t.Fatalf("spawns = %d, want 1", spawns.Load())
	}
	if resp.Turn.ID == "" || resp.Thread.ID != sessionID {
		t.Fatalf("retry resp = %+v", resp)
	}

	// Direct daemon call with the exact same InitialTurnMutationID confirms Turn 1 was journaled & deduplicated once.
	mu.Lock()
	ids := append([]string(nil), seenTurnMutationIDs...)
	insts := append([]string(nil), seenInstanceIDs...)
	mu.Unlock()
	if len(ids) != 3 || ids[0] == "" || ids[0] != ids[1] || ids[1] != ids[2] || insts[0] != sessionID || insts[1] != sessionID || insts[2] != sessionID {
		t.Fatalf("turn mutation ids=%v instances=%v, want identical trio across retries", ids, insts)
	}
	replayTurn, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		Ref:                "local:" + sessionID,
		ClientMutationID:   ids[0],
		ExpectedInstanceID: sessionID,
		Input:              params.Input,
	})
	if err != nil {
		t.Fatalf("daemon dedup check: %v", err)
	}
	if replayTurn.Receipt.Disposition != appwire.MutationDispositionReplayed || replayTurn.Turn.ID != resp.Turn.ID {
		t.Fatalf("daemon did not dedup InitialTurnMutationID: %+v vs %+v", replayTurn, resp.Turn)
	}
	if calls := adapter.calls.Load(); calls > 1 {
		t.Fatalf("adapter executed %d times, want at most 1", calls)
	}
}

func TestThreadStartIdempotencyTransientReadFailurePartialErrorAndRetry(t *testing.T) {
	stateRoot := t.TempDir()
	runDir := t.TempDir()
	sessionID := hubtest.SessionID(t)
	var turnCalls, spawns atomic.Int32

	daemon2 := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon2", SourceID: "local"})
	var r2Calls atomic.Int32
	appserver.HandleTyped(daemon2.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		if r2Calls.Add(1) == 1 {
			return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: "transient-unconfirmed"}}, nil
		}
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: sessionID, SessionID: sessionID, Source: "local",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener: appwire.EvenerThread{Ref: "local:" + sessionID, InstanceID: sessionID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		}}, nil
	})
	appserver.HandleTyped(daemon2.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		turnCalls.Add(1)
		return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_1", Status: "inProgress"}}, nil
	})
	daemon2HTTP := httptest.NewServer(http.HandlerFunc(daemon2.ServeWebSocket))
	defer daemon2HTTP.Close()
	entry2 := localEntry(daemon2HTTP.URL, sessionID)
	spawner2 := spawnerWritingEntry(t, runDir, entry2, &spawns)
	roster := hubcore.NewRoster(runDir, failedRPCProber{}).SetProcessIdentity(func(rendezvous.Entry) hubcore.ProcessIdentity { return hubcore.ProcessOwnsEntry })
	hub := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
		HubStateRoot: stateRoot,
		RunDir:       runDir,
		Roster:       roster,
		Spawner:      spawner2,
	}))
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	params := appwire.ThreadStartParams{
		ClientMutationID: "mut-read-fail",
		Model:            "openai/gpt-5",
		CWD:              t.TempDir(),
		Input:            []appwire.InputItem{{Type: "text", Text: "go"}},
	}
	_, err := appwiretest.ThreadStart(context.Background(), client, params)
	if err == nil {
		t.Fatal("expected attempt 1 ReadThread confirmation failure")
	}
	wire := requireWireError(t, err, appwire.CodeUnavailable)
	raw, _ := json.Marshal(wire.Data)
	var partial appwire.ThreadStartPartialErrorData
	_ = json.Unmarshal(raw, &partial)
	if partial.CreatedRef != "local:"+sessionID || partial.ClientMutationID != "mut-read-fail" || partial.RetryDisposition == appwire.RetryDispositionBlocked {
		t.Fatalf("read failure partial = %+v", partial)
	}

	resp, err := appwiretest.ThreadStart(context.Background(), client, params)
	if err != nil {
		t.Fatalf("retry after ReadThread failure: %v", err)
	}
	if spawns.Load() != 1 || turnCalls.Load() != 1 || resp.Turn.ID != "turn_1" {
		t.Fatalf("spawns=%d turnCalls=%d resp=%+v", spawns.Load(), turnCalls.Load(), resp)
	}
}

func TestThreadStartIdempotencyHubRestartRecovery(t *testing.T) {
	workDir := t.TempDir()

	t.Run("restart after completed replays without spawn", func(t *testing.T) {
		stateRoot := t.TempDir()
		runDir := t.TempDir()
		sessionID := hubtest.SessionID(t)
		daemon, _ := startSkillInputHubDaemon(t, sessionID, appwire.ThreadCapabilities{Send: true}, nil)
		entry := localEntry(daemon.URL, sessionID)
		var spawns atomic.Int32
		spawner := spawnerWritingEntry(t, runDir, entry, &spawns)
		params := appwire.ThreadStartParams{
			ClientMutationID: "mut-restart-completed",
			Model:            "openai/gpt-5",
			CWD:              workDir,
		}
		hub1 := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
			HubStateRoot: stateRoot, RunDir: runDir,
			Roster: hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusIdle}), Spawner: spawner,
		}))
		c1 := dialHubRPCInitialized(t, hub1)
		first, err := appwiretest.ThreadStart(context.Background(), c1, params)
		if err != nil {
			t.Fatalf("hub1 ThreadStart: %v", err)
		}
		_ = c1.Close()
		hub1.Close()

		// Hub 2 opens the same HubStateRoot with a spawner that must never run.
		hub2 := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
			HubStateRoot: stateRoot, RunDir: t.TempDir(), Spawner: spawner,
		}))
		defer hub2.Close()
		c2 := dialHubRPCInitialized(t, hub2)
		replay, err := appwiretest.ThreadStart(context.Background(), c2, params)
		if err != nil {
			t.Fatalf("hub2 replay ThreadStart: %v", err)
		}
		if spawns.Load() != 1 || replay.Disposition != appwire.MutationDispositionReplayed || replay.Thread.ID != first.Thread.ID {
			t.Fatalf("spawns=%d replay=%+v first=%+v", spawns.Load(), replay, first)
		}
	})

	t.Run("restart after spawned with initial input resumes via hubThreadAutoResume and finishes turn 1", func(t *testing.T) {
		stateRoot := t.TempDir()
		projRoot := t.TempDir()
		proj, err := identifier.ResolveProject(workDir)
		if err != nil {
			t.Fatal(err)
		}
		sessionStateDir := filepath.Join(projRoot, "projects", proj.ID)
		sessionID := buildRPCSessionWithWorkingDir(t, sessionStateDir, hubtest.SessionID(t), workDir)

		var turnCalls atomic.Int32
		var gotTurnID, gotInstID string
		resumedDaemon := appserver.NewServer(appserver.ServerConfig{ServerName: "resumed", SourceID: "local"})
		appserver.HandleTyped(resumedDaemon.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: sessionID, SessionID: sessionID, Source: "local", CWD: workDir,
				Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
				Evener: appwire.EvenerThread{Ref: "local:" + sessionID, InstanceID: sessionID, Capabilities: appwire.ThreadCapabilities{Send: true}},
			}}, nil
		})
		appserver.HandleTyped(resumedDaemon.Router(), appwire.MethodTurnStart, func(_ context.Context, p appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			turnCalls.Add(1)
			gotTurnID, gotInstID = p.ClientMutationID, p.ExpectedInstanceID
			return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_resumed", Status: "inProgress"}}, nil
		})
		resumedHTTP := httptest.NewServer(http.HandlerFunc(resumedDaemon.ServeWebSocket))
		defer resumedHTTP.Close()

		// Seed CreationStore in stateRoot directly as spawned (simulating crash after CommitSpawned).
		store1, err := hubcore.NewCreationStore(stateRoot)
		if err != nil {
			t.Fatal(err)
		}
		params := appwire.ThreadStartParams{
			ClientMutationID: "mut-restart-spawned-input",
			Model:            "openai/gpt-5",
			CWD:              workDir,
			Input:            []appwire.InputItem{{Type: "text", Text: "hello"}},
		}
		hash, err := hubcore.CreationPayloadHash(params)
		if err != nil {
			t.Fatal(err)
		}
		acq, err := store1.Acquire(t.Context(), params.ClientMutationID, hash, true, "turn-mut-persisted")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store1.CommitSpawned(params.ClientMutationID, "local:"+sessionID, sessionID, sessionID); err != nil {
			t.Fatal(err)
		}
		store1.ReleaseOwner(acq.Owner, appwire.ThreadStartResponse{}, nil)

		runDir2 := t.TempDir()
		var spawns, resumes atomic.Int32
		spawner2 := &fakeRPCSpawner{
			spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
				spawns.Add(1)
				return rendezvous.Entry{}, errors.New("must not re-spawn")
			},
			resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
				resumes.Add(1)
				entry := localEntry(resumedHTTP.URL, req.SessionID)
				writeRendezvous(t, runDir2, entry)
				return entry, nil
			},
		}
		hub2 := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
			HubStateRoot: stateRoot,
			RunDir:       runDir2,
			Past:         newRebuiltPastIndex(t, filepath.Join(projRoot, "projects", "*")),
			Roster:       hubcore.NewRoster(runDir2, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusIdle}),
			Spawner:      spawner2,
		}))
		defer hub2.Close()
		c2 := dialHubRPCInitialized(t, hub2)
		resp, err := appwiretest.ThreadStart(context.Background(), c2, params)
		if err != nil {
			t.Fatalf("hub2 spawned-input retry: %v", err)
		}
		if spawns.Load() != 0 || resumes.Load() != 1 || turnCalls.Load() != 1 {
			t.Fatalf("spawns=%d resumes=%d turnCalls=%d", spawns.Load(), resumes.Load(), turnCalls.Load())
		}
		if gotTurnID != "turn-mut-persisted" || gotInstID != sessionID || resp.Turn.ID != "turn_resumed" {
			t.Fatalf("gotTurnID=%q gotInstID=%q resp=%+v", gotTurnID, gotInstID, resp)
		}
	})

	t.Run("restart after spawned with empty input reads via pastThreadReadResponse", func(t *testing.T) {
		stateRoot := t.TempDir()
		projRoot := t.TempDir()
		proj, err := identifier.ResolveProject(workDir)
		if err != nil {
			t.Fatal(err)
		}
		sessionStateDir := filepath.Join(projRoot, "projects", proj.ID)
		sessionID := buildRPCSessionWithWorkingDir(t, sessionStateDir, hubtest.SessionID(t), workDir)

		store1, err := hubcore.NewCreationStore(stateRoot)
		if err != nil {
			t.Fatal(err)
		}
		params := appwire.ThreadStartParams{
			ClientMutationID: "mut-restart-spawned-empty",
			Model:            "openai/gpt-5",
			CWD:              workDir,
		}
		hash, err := hubcore.CreationPayloadHash(params)
		if err != nil {
			t.Fatal(err)
		}
		acq, err := store1.Acquire(t.Context(), params.ClientMutationID, hash, false, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store1.CommitSpawned(params.ClientMutationID, "local:"+sessionID, sessionID, sessionID); err != nil {
			t.Fatal(err)
		}
		store1.ReleaseOwner(acq.Owner, appwire.ThreadStartResponse{}, nil)

		var spawns, resumes atomic.Int32
		spawner2 := &fakeRPCSpawner{
			spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
				spawns.Add(1)
				return rendezvous.Entry{}, errors.New("no spawn")
			},
			resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
				resumes.Add(1)
				return rendezvous.Entry{}, errors.New("no resume")
			},
		}
		hub2 := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
			HubStateRoot: stateRoot,
			RunDir:       t.TempDir(),
			Past:         newRebuiltPastIndex(t, filepath.Join(projRoot, "projects", "*")),
			Spawner:      spawner2,
		}))
		defer hub2.Close()
		c2 := dialHubRPCInitialized(t, hub2)
		resp, err := appwiretest.ThreadStart(context.Background(), c2, params)
		if err != nil {
			t.Fatalf("hub2 spawned-empty retry: %v", err)
		}
		if spawns.Load() != 0 || resumes.Load() != 0 || resp.Thread.ID != sessionID || resp.ClientMutationID != params.ClientMutationID {
			t.Fatalf("spawns=%d resumes=%d resp=%+v", spawns.Load(), resumes.Load(), resp)
		}
	})

	t.Run("restart during spawning fails closed as uncertain", func(t *testing.T) {
		stateRoot := t.TempDir()
		store1, err := hubcore.NewCreationStore(stateRoot)
		if err != nil {
			t.Fatal(err)
		}
		params := appwire.ThreadStartParams{
			ClientMutationID: "mut-restart-spawning",
			Model:            "openai/gpt-5",
			CWD:              workDir,
		}
		hash, _ := hubcore.CreationPayloadHash(params)
		if _, err := store1.Acquire(t.Context(), params.ClientMutationID, hash, false, ""); err != nil {
			t.Fatal(err)
		}
		hub2 := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
			HubStateRoot: stateRoot,
			RunDir:       t.TempDir(),
			Spawner:      &fakeRPCSpawner{},
		}))
		defer hub2.Close()
		c2 := dialHubRPCInitialized(t, hub2)
		_, err = appwiretest.ThreadStart(context.Background(), c2, params)
		_, data := wireErrorData(t, err)
		if data.MutationOutcome != appwire.MutationOutcomeUnknown || data.RetryDisposition != appwire.RetryDispositionBlocked {
			t.Fatalf("spawning restart data=%+v, want Unknown+Blocked", data)
		}
	})
}

func TestThreadStartIdempotencyCoalescingAndCancelledWaiter(t *testing.T) {
	stateRoot := t.TempDir()
	runDir := t.TempDir()
	sessionID := hubtest.SessionID(t)
	var turnCalls, spawns atomic.Int32
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: sessionID, SessionID: sessionID, Source: "local",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener: appwire.EvenerThread{Ref: "local:" + sessionID, InstanceID: sessionID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(_ context.Context, _ appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		turnCalls.Add(1)
		return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_1", Status: "inProgress"}}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()
	entry := localEntry(daemonHTTP.URL, sessionID)

	spawnEntered := make(chan struct{})
	releaseSpawn := make(chan struct{})
	spawner := &fakeRPCSpawner{spawn: func(ctx context.Context, _ hubcore.SpawnRequest) (rendezvous.Entry, error) {
		spawns.Add(1)
		close(spawnEntered)
		select {
		case <-releaseSpawn:
		case <-ctx.Done():
			return rendezvous.Entry{}, ctx.Err()
		}
		writeRendezvous(t, runDir, entry)
		return entry, nil
	}}
	var waiters atomic.Int32
	bothWaiting := make(chan struct{})
	store, err := hubcore.NewCreationStoreForTest(stateRoot, func() {
		if waiters.Add(1) == 2 {
			close(bothWaiting)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	hub := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
		HubStateRoot:  stateRoot,
		CreationStore: store,
		RunDir:        runDir,
		Roster:        hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusActive}),
		Spawner:       spawner,
	}))
	defer hub.Close()

	cOwner := dialHubRPCInitialized(t, hub)
	cWaiterCancel := dialHubRPCInitialized(t, hub)
	cWaiterLive := dialHubRPCInitialized(t, hub)

	params := appwire.ThreadStartParams{
		ClientMutationID: "mut-coalesce-1",
		Model:            "openai/gpt-5",
		CWD:              t.TempDir(),
		Input:            []appwire.InputItem{{Type: "text", Text: "go"}},
	}

	ownerDone := make(chan error, 1)
	go func() {
		_, err := appwiretest.ThreadStart(context.Background(), cOwner, params)
		ownerDone <- err
	}()
	<-spawnEntered

	waiterCancelCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterCancelErr := make(chan error, 1)
	go func() {
		_, err := appwiretest.ThreadStart(waiterCancelCtx, cWaiterCancel, params)
		waiterCancelErr <- err
	}()
	waiterLiveResp := make(chan appwire.ThreadStartResponse, 1)
	waiterLiveErr := make(chan error, 1)
	go func() {
		resp, err := appwiretest.ThreadStart(context.Background(), cWaiterLive, params)
		waiterLiveResp <- resp
		waiterLiveErr <- err
	}()
	<-bothWaiting

	// Cancel waiter; must return ctx.Err() promptly without blocking on releaseSpawn.
	cancelWaiter()
	if err := <-waiterCancelErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter err = %v, want context.Canceled", err)
	}

	// Owner's detached spawn finishes and commits completed; live waiter gets replayed result.
	close(releaseSpawn)
	if err := <-ownerDone; err != nil {
		t.Fatalf("owner err = %v", err)
	}
	if err := <-waiterLiveErr; err != nil {
		t.Fatalf("live waiter err = %v", err)
	}
	gotWaiter := <-waiterLiveResp
	if gotWaiter.Disposition != appwire.MutationDispositionReplayed || gotWaiter.Thread.ID != sessionID {
		t.Fatalf("live waiter resp = %+v", gotWaiter)
	}
	if spawns.Load() != 1 || turnCalls.Load() != 1 {
		t.Fatalf("spawns=%d turnCalls=%d, want 1 and 1", spawns.Load(), turnCalls.Load())
	}
}

func TestThreadStartIdempotencyPayloadConflictAndTargetDeleted(t *testing.T) {
	stateRoot := t.TempDir()
	runDir := t.TempDir()
	sessionID := hubtest.SessionID(t)
	daemon, _ := startSkillInputHubDaemon(t, sessionID, appwire.ThreadCapabilities{Send: true}, nil)
	entry := localEntry(daemon.URL, sessionID)
	delStore, err := hubcore.NewDeletionStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	spawner := spawnerWritingEntry(t, runDir, entry, nil)
	hub := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
		HubStateRoot:  stateRoot,
		RunDir:        runDir,
		DeletionStore: delStore,
		Roster:        hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusIdle}),
		Spawner:       spawner,
	}))
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	cwd := t.TempDir()
	params := appwire.ThreadStartParams{
		ClientMutationID: "mut-conflict-del",
		Model:            "openai/gpt-5",
		CWD:              cwd,
	}
	if _, err := appwiretest.ThreadStart(context.Background(), client, params); err != nil {
		t.Fatalf("initial start: %v", err)
	}

	// Payload mismatch
	changed := params
	changed.Model = "openai/gpt-5-mini"
	_, err = appwiretest.ThreadStart(context.Background(), client, changed)
	_, data := wireErrorData(t, err)
	if data.EvenerErrorInfo != appwire.ErrorConflictingMutationID || data.MutationOutcome != appwire.MutationOutcomeNotAccepted || data.ClientMutationID != params.ClientMutationID {
		t.Fatalf("conflict data = %+v", data)
	}

	// Deleted session on completed record
	proj, err := identifier.ResolveProject(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := delStore.BeginSession(proj.ID, hubcore.DeletionTarget{Ref: "local:" + sessionID, ThreadID: sessionID}); err != nil {
		t.Fatal(err)
	}
	_, err = appwiretest.ThreadStart(context.Background(), client, params)
	_, data = wireErrorData(t, err)
	if data.MutationOutcome != appwire.MutationOutcomeTargetDeleted || data.ClientMutationID != params.ClientMutationID {
		t.Fatalf("deleted completed data = %+v, want MutationOutcomeTargetDeleted", data)
	}

	// Deleted session on spawned record
	store, err := hubcore.NewCreationStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	spawnedID := hubtest.SessionID(t)
	delStore2, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := delStore2.BeginSession(proj.ID, hubcore.DeletionTarget{Ref: "local:" + spawnedID, ThreadID: spawnedID}); err != nil {
		t.Fatal(err)
	}
	spawnedParams := appwire.ThreadStartParams{ClientMutationID: "mut-spawned-del", Model: "openai/gpt-5", CWD: cwd}
	h, _ := hubcore.CreationPayloadHash(spawnedParams)
	acq, _ := store.Acquire(t.Context(), spawnedParams.ClientMutationID, h, false, "")
	_, _ = store.CommitSpawned(spawnedParams.ClientMutationID, "local:"+spawnedID, spawnedID, spawnedID)
	store.ReleaseOwner(acq.Owner, appwire.ThreadStartResponse{}, nil)
	hubDelSpawned := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
		CreationStore: store, DeletionStore: delStore2, RunDir: t.TempDir(), Spawner: spawner,
	}))
	defer hubDelSpawned.Close()
	cDel := dialHubRPCInitialized(t, hubDelSpawned)
	_, err = appwiretest.ThreadStart(context.Background(), cDel, spawnedParams)
	_, data = wireErrorData(t, err)
	if data.MutationOutcome != appwire.MutationOutcomeTargetDeleted || data.ClientMutationID != spawnedParams.ClientMutationID {
		t.Fatalf("deleted spawned data = %+v, want MutationOutcomeTargetDeleted", data)
	}

	// Completed key retried after its original CWD is deleted still replays, and a missing CWD with mismatched payload returns ConflictingMutationID (not InvalidParams).
	ephemeralWorkDir := filepath.Join(t.TempDir(), "ephemeral-cwd")
	if err := os.MkdirAll(ephemeralWorkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	delStore3, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hubEphemeral := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
		HubStateRoot:  t.TempDir(),
		RunDir:        runDir,
		DeletionStore: delStore3,
		Roster:        hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusIdle}),
		Spawner:       spawner,
	}))
	defer hubEphemeral.Close()
	cEph := dialHubRPCInitialized(t, hubEphemeral)
	ephParams := appwire.ThreadStartParams{
		ClientMutationID: "mut-deleted-cwd",
		Model:            "openai/gpt-5",
		CWD:              ephemeralWorkDir,
	}
	if _, err := appwiretest.ThreadStart(context.Background(), cEph, ephParams); err != nil {
		t.Fatalf("initial start with ephemeralWorkDir: %v", err)
	}
	if err := os.Remove(ephemeralWorkDir); err != nil {
		t.Fatal(err)
	}
	ephReplay, err := appwiretest.ThreadStart(context.Background(), cEph, ephParams)
	if err != nil {
		t.Fatalf("replay after CWD deleted failed: %v", err)
	}
	if ephReplay.Disposition != appwire.MutationDispositionReplayed || ephReplay.Thread.ID != sessionID {
		t.Fatalf("ephemeral CWD replay resp = %+v", ephReplay)
	}
	badConflict := ephParams
	badConflict.CWD = filepath.Join(t.TempDir(), "never-existed-cwd")
	_, err = appwiretest.ThreadStart(context.Background(), cEph, badConflict)
	_, data = wireErrorData(t, err)
	if data.EvenerErrorInfo != appwire.ErrorConflictingMutationID || data.MutationOutcome != appwire.MutationOutcomeNotAccepted {
		t.Fatalf("conflict with nonexistent CWD data = %+v, want ErrorConflictingMutationID", data)
	}
}

func TestThreadStartIdempotencyRemoteOfflineNotAccepted(t *testing.T) {
	stateRoot := t.TempDir()
	hub := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
		HubStateRoot: stateRoot,
		RunDir:       t.TempDir(),
		RemoteHosts:  []hostreg.Host{{Name: "remote-a", SSH: "remote-a.example"}},
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
			t.Fatal("offline host must not dial on thread/start")
			return nil, nil
		},
		RemoteHostClientIfAttached: func(string) (*appwire.Client, bool) {
			return nil, false
		},
		RemoteHostOnline: func(string) bool { return false },
	}))
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	_, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		ClientMutationID: "mut-remote-offline",
		Source:           "remote-a",
		Model:            "openai/gpt-5",
		CWD:              "/remote/work",
	})
	wire, data := wireErrorData(t, err)
	if wire.Code != appwire.CodeUnavailable || data.EvenerErrorInfo != appwire.ErrorSessionUnavailable ||
		data.MutationOutcome != appwire.MutationOutcomeNotAccepted || data.ClientMutationID != "mut-remote-offline" {
		t.Fatalf("offline remote data=%+v wire=%+v", data, wire)
	}
	if _, statErr := os.Stat(filepath.Join(stateRoot, "creations", "state.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("offline remote unexpectedly wrote creations/state.json: %v", statErr)
	}
}

func TestThreadStartIdempotencyRemoteCapableForwardsAndDedups(t *testing.T) {
	remoteSessionID := hubtest.SessionID(t)
	var remoteSpawns atomic.Int32
	var seenMutationIDs []string
	var mu sync.Mutex

	remoteServer := appserver.NewServer(appserver.ServerConfig{
		ServerName: "remote-capable",
		SourceID:   "local",
		Features:   appwire.FeatureSet{ThreadStartIdempotency: true, TurnStart: true},
	})
	appserver.HandleTyped(remoteServer.Router(), appwire.MethodThreadStart, func(_ context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
		mu.Lock()
		seenMutationIDs = append(seenMutationIDs, params.ClientMutationID)
		mu.Unlock()
		n := remoteSpawns.Add(1)
		disp := appwire.MutationDispositionApplied
		if n > 1 {
			remoteSpawns.Add(-1)
			disp = appwire.MutationDispositionReplayed
		}
		return appwire.ThreadStartResponse{
			Thread: appwire.Thread{
				ID:        remoteSessionID,
				SessionID: remoteSessionID,
				Source:    "local",
				Evener:    appwire.EvenerThread{Ref: "local:" + remoteSessionID, InstanceID: remoteSessionID},
			},
			Turn:             appwire.Turn{ID: "turn_1", Status: "inProgress"},
			ClientMutationID: params.ClientMutationID,
			Disposition:      disp,
		}, nil
	})
	remoteHTTP := httptest.NewServer(http.HandlerFunc(remoteServer.ServeWebSocket))
	defer remoteHTTP.Close()
	remoteClient := dialHubRPCInitialized(t, remoteHTTP)

	localHub := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
		HubStateRoot:               t.TempDir(),
		RunDir:                     t.TempDir(),
		RemoteHosts:                []hostreg.Host{{Name: "remote-a", SSH: "remote-a.example"}},
		RemoteHostClient:           func(context.Context, string) (*appwire.Client, error) { return remoteClient, nil },
		RemoteHostClientIfAttached: func(string) (*appwire.Client, bool) { return remoteClient, true },
	}))
	defer localHub.Close()
	client := dialHubRPCInitialized(t, localHub)

	params := appwire.ThreadStartParams{
		ClientMutationID: "mut-remote-cap",
		Source:           "remote-a",
		Model:            "openai/gpt-5",
		CWD:              "/remote/work",
		Input:            []appwire.InputItem{{Type: "text", Text: "remote prompt"}},
	}
	first, err := appwiretest.ThreadStart(context.Background(), client, params)
	if err != nil {
		t.Fatalf("first remote start: %v", err)
	}
	if first.Thread.Evener.Ref != "remote-a:"+remoteSessionID || first.ClientMutationID != "mut-remote-cap" || first.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("first remote resp = %+v", first)
	}
	replay, err := appwiretest.ThreadStart(context.Background(), client, params)
	if err != nil {
		t.Fatalf("retry remote start: %v", err)
	}
	if replay.Thread.Evener.Ref != "remote-a:"+remoteSessionID || replay.ClientMutationID != "mut-remote-cap" || replay.Disposition != appwire.MutationDispositionReplayed {
		t.Fatalf("replay remote resp = %+v", replay)
	}
	mu.Lock()
	defer mu.Unlock()
	if remoteSpawns.Load() != 1 || len(seenMutationIDs) != 2 || seenMutationIDs[0] != "mut-remote-cap" || seenMutationIDs[1] != "mut-remote-cap" {
		t.Fatalf("remoteSpawns=%d seenMutationIDs=%v", remoteSpawns.Load(), seenMutationIDs)
	}
}

type gatedSendTransport struct {
	appwire.Transport
	firstStarted chan struct{}
	once         sync.Once
	gate         atomic.Bool
	release      chan struct{}
}

func (g *gatedSendTransport) Send(ctx context.Context, msg appwire.Message) error {
	if g.gate.Load() {
		g.once.Do(func() { close(g.firstStarted) })
		<-g.release
	}
	return g.Transport.Send(ctx, msg)
}

func TestThreadStartIdempotencyRemoteCapableErrorsAndNotSent(t *testing.T) {
	remoteSessionID := hubtest.SessionID(t)
	var remoteStartCalls atomic.Int32
	var mode atomic.Int32 // 0 = partial error with createdRef, 1 = succeed
	remoteServer := appserver.NewServer(appserver.ServerConfig{
		ServerName: "remote-capable-errors",
		SourceID:   "local",
		Features:   appwire.FeatureSet{ThreadStartIdempotency: true, TurnStart: true},
	})
	appserver.HandleTyped(remoteServer.Router(), appwire.MethodThreadStart, func(_ context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
		remoteStartCalls.Add(1)
		if mode.Load() == 0 {
			return appwire.ThreadStartResponse{}, appwire.WireError{
				Code:    appwire.CodeUnavailable,
				Message: "turn 1 interrupted on remote",
				Data: appwire.ThreadStartPartialErrorData{
					ErrorData: appwire.ErrorData{
						EvenerErrorInfo:  appwire.ErrorSessionUnavailable,
						ClientMutationID: params.ClientMutationID,
						MutationOutcome:  appwire.MutationOutcomeUnknown,
						RetryDisposition: appwire.RetryDispositionBlocked,
					},
					CreatedRef:        "local:" + remoteSessionID,
					CreatedInstanceID: "inst-cap-partial",
				},
			}
		}
		return appwire.ThreadStartResponse{
			Thread: appwire.Thread{
				ID:        remoteSessionID,
				SessionID: remoteSessionID,
				Source:    "local",
				Evener:    appwire.EvenerThread{Ref: "local:" + remoteSessionID, InstanceID: remoteSessionID},
			},
			Turn:             appwire.Turn{ID: "turn_1", Status: "inProgress"},
			ClientMutationID: params.ClientMutationID,
			Disposition:      appwire.MutationDispositionApplied,
		}, nil
	})
	remoteHTTP := httptest.NewServer(http.HandlerFunc(remoteServer.ServeWebSocket))
	defer remoteHTTP.Close()

	remoteClient := dialHubRPCInitialized(t, remoteHTTP)
	rawSlotTransport, err := appwire.DialWebSocket(t.Context(), "ws"+remoteHTTP.URL[len("http"):], remoteHTTP.Client())
	if err != nil {
		t.Fatalf("dial slot websocket: %v", err)
	}
	slotTransport := &gatedSendTransport{Transport: rawSlotTransport, firstStarted: make(chan struct{}), release: make(chan struct{})}
	slotClient := appwire.NewClient(slotTransport)
	slotClient.Start(t.Context())
	var releaseOnce sync.Once
	releaseSlot := func() { releaseOnce.Do(func() { close(slotTransport.release) }) }
	t.Cleanup(func() {
		releaseSlot()
		_ = slotClient.Close()
	})
	mustInitializeHubClient(t, slotClient)
	slotTransport.gate.Store(true)
	go func() { _ = slotClient.Notify(t.Context(), appwire.NotifyEvenerAuthUpdated, map[string]string{}) }()
	<-slotTransport.firstStarted

	var useWedged atomic.Bool
	hub := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		RunDir:       t.TempDir(),
		RemoteHosts:  []hostreg.Host{{Name: "remote-a", SSH: "remote-a.example"}},
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
			if useWedged.Load() {
				return slotClient, nil
			}
			return remoteClient, nil
		},
		RemoteHostClientIfAttached: func(string) (*appwire.Client, bool) {
			if useWedged.Load() {
				return slotClient, true
			}
			return remoteClient, true
		},
	}))
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	// 1. Capable remote error fields and translated CreatedRef pass through intact.
	_, err = appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		ClientMutationID: "mut-cap-partial",
		Source:           "remote-a",
		Model:            "openai/gpt-5",
		CWD:              "/remote/work",
	})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("err = %T (%v), want WireError", err, err)
	}
	rawData, merr := json.Marshal(wire.Data)
	if merr != nil {
		t.Fatal(merr)
	}
	var partial appwire.ThreadStartPartialErrorData
	if uerr := json.Unmarshal(rawData, &partial); uerr != nil {
		t.Fatal(uerr)
	}
	if wire.Code != appwire.CodeUnavailable ||
		partial.EvenerErrorInfo != appwire.ErrorSessionUnavailable ||
		partial.ClientMutationID != "mut-cap-partial" ||
		partial.MutationOutcome != appwire.MutationOutcomeUnknown ||
		partial.RetryDisposition != appwire.RetryDispositionBlocked ||
		partial.CreatedRef != "remote-a:"+remoteSessionID ||
		partial.CreatedInstanceID != "inst-cap-partial" {
		t.Fatalf("capable remote partial error = code:%d data:%+v (raw=%s)", wire.Code, partial, rawData)
	}

	// 2. RequestNotSentError on capable remote returns NotAccepted(id), allowing retry to succeed.
	useWedged.Store(true)
	origDetached := threadStartDetachedTimeout
	threadStartDetachedTimeout = 100 * time.Millisecond
	t.Cleanup(func() { threadStartDetachedTimeout = origDetached })
	_, err = appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		ClientMutationID: "mut-cap-notsent",
		Source:           "remote-a",
		Model:            "openai/gpt-5",
		CWD:              "/remote/work",
	})
	threadStartDetachedTimeout = origDetached
	releaseSlot()
	wireNotSent, data := wireErrorData(t, err)
	if wireNotSent.Code != appwire.CodeUnavailable ||
		data.MutationOutcome != appwire.MutationOutcomeNotAccepted ||
		data.RetryDisposition != appwire.RetryDispositionNone ||
		data.ClientMutationID != "mut-cap-notsent" ||
		remoteStartCalls.Load() != 1 {
		t.Fatalf("capable remote RequestNotSentError = wire:%+v data:%+v calls:%d, want CodeUnavailable+NotAccepted+RetryNone", wireNotSent, data, remoteStartCalls.Load())
	}

	mode.Store(1)
	useWedged.Store(false)
	resp, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		ClientMutationID: "mut-cap-notsent",
		Source:           "remote-a",
		Model:            "openai/gpt-5",
		CWD:              "/remote/work",
	})
	if err != nil {
		t.Fatalf("retry after RequestNotSentError failed: %v", err)
	}
	if resp.ClientMutationID != "mut-cap-notsent" || resp.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("recovered resp = %+v", resp)
	}
}

func TestThreadStartIdempotencyRemoteCapablePostSendDropAndRetry(t *testing.T) {
	remoteSessionID := hubtest.SessionID(t)
	var spawns atomic.Int32
	received := make(chan struct{})
	dropDone := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-dropDone:
		default:
			close(dropDone)
		}
	})

	remoteServer := appserver.NewServer(appserver.ServerConfig{
		ServerName: "remote-capable-drop",
		SourceID:   "local",
		Features:   appwire.FeatureSet{ThreadStartIdempotency: true, TurnStart: true},
	})
	appserver.HandleTyped(remoteServer.Router(), appwire.MethodThreadStart, func(_ context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
		n := spawns.Add(1)
		disp := appwire.MutationDispositionApplied
		if n > 1 {
			spawns.Add(-1)
			disp = appwire.MutationDispositionReplayed
		} else {
			close(received)
			<-dropDone
		}
		return appwire.ThreadStartResponse{
			Thread: appwire.Thread{
				ID:        remoteSessionID,
				SessionID: remoteSessionID,
				Source:    "local",
				Evener:    appwire.EvenerThread{Ref: "local:" + remoteSessionID, InstanceID: remoteSessionID},
			},
			Turn:             appwire.Turn{ID: "turn_1", Status: "inProgress"},
			ClientMutationID: params.ClientMutationID,
			Disposition:      disp,
		}, nil
	})
	remoteHTTP := httptest.NewServer(http.HandlerFunc(remoteServer.ServeWebSocket))
	defer remoteHTTP.Close()

	raw1, err := appwire.DialWebSocket(t.Context(), "ws"+remoteHTTP.URL[len("http"):], remoteHTTP.Client())
	if err != nil {
		t.Fatalf("dial remote 1: %v", err)
	}
	remoteClient1 := appwire.NewClient(raw1)
	remoteClient1.Start(t.Context())
	defer remoteClient1.Close()
	mustInitializeHubClient(t, remoteClient1)

	remoteClient2 := dialHubRPCInitialized(t, remoteHTTP)
	var mu sync.Mutex
	attached := remoteClient1

	hub := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		RunDir:       t.TempDir(),
		RemoteHosts:  []hostreg.Host{{Name: "remote-a", SSH: "remote-a.example"}},
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
			mu.Lock()
			defer mu.Unlock()
			return attached, nil
		},
		RemoteHostClientIfAttached: func(string) (*appwire.Client, bool) {
			mu.Lock()
			defer mu.Unlock()
			return attached, true
		},
	}))
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	params := appwire.ThreadStartParams{
		ClientMutationID: "mut-cap-drop",
		Source:           "remote-a",
		Model:            "openai/gpt-5",
		CWD:              "/remote/work",
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := appwiretest.ThreadStart(context.Background(), client, params)
		errCh <- err
	}()
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("remote never received thread/start")
	}
	_ = raw1.Close()
	close(dropDone)

	var firstErr error
	select {
	case firstErr = <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("controller thread/start did not return after transport drop")
	}
	wire, data := wireErrorData(t, firstErr)
	if wire.Code != appwire.CodeInternalError ||
		data.EvenerErrorInfo != appwire.ErrorMutationOutcomeUnknown ||
		data.ClientMutationID != "mut-cap-drop" ||
		data.MutationOutcome != appwire.MutationOutcomeUnknown ||
		data.RetryDisposition != appwire.RetryDispositionAutomatic {
		t.Fatalf("post-send drop error = wire:%+v data:%+v, want MutationOutcomeUnknown+Automatic", wire, data)
	}

	mu.Lock()
	attached = remoteClient2
	mu.Unlock()
	replay, err := appwiretest.ThreadStart(context.Background(), client, params)
	if err != nil {
		t.Fatalf("retry after post-send drop failed: %v", err)
	}
	if replay.Thread.Evener.Ref != "remote-a:"+remoteSessionID || replay.ClientMutationID != "mut-cap-drop" || replay.Disposition != appwire.MutationDispositionReplayed || spawns.Load() != 1 {
		t.Fatalf("replay resp = %+v spawns=%d, want Replayed and 1 spawn", replay, spawns.Load())
	}
}

func TestThreadStartIdempotencyOlderRemoteRefusesKeyedAllowsKeyless(t *testing.T) {
	remoteSessionID := hubtest.SessionID(t)
	var remoteCalls atomic.Int32

	remoteServer := appserver.NewServer(appserver.ServerConfig{
		ServerName: "remote-legacy",
		SourceID:   "local",
		Features:   appwire.FeatureSet{ThreadList: true, TurnStart: true}, // ThreadStartIdempotency == false
	})
	appserver.HandleTyped(remoteServer.Router(), appwire.MethodThreadStart, func(_ context.Context, _ appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
		remoteCalls.Add(1)
		return appwire.ThreadStartResponse{
			Thread: appwire.Thread{
				ID:        remoteSessionID,
				SessionID: remoteSessionID,
				Source:    "local",
				Evener:    appwire.EvenerThread{Ref: "local:" + remoteSessionID, InstanceID: remoteSessionID},
			},
			Turn: appwire.Turn{ID: "turn_1", Status: "inProgress"},
		}, nil
	})
	remoteHTTP := httptest.NewServer(http.HandlerFunc(remoteServer.ServeWebSocket))
	defer remoteHTTP.Close()
	remoteClient := dialHubRPCInitialized(t, remoteHTTP)

	stateRoot := t.TempDir()
	localHub := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
		HubStateRoot:               stateRoot,
		RunDir:                     t.TempDir(),
		RemoteHosts:                []hostreg.Host{{Name: "remote-a", SSH: "remote-a.example"}},
		RemoteHostClient:           func(context.Context, string) (*appwire.Client, error) { return remoteClient, nil },
		RemoteHostClientIfAttached: func(string) (*appwire.Client, bool) { return remoteClient, true },
	}))
	defer localHub.Close()
	client := dialHubRPCInitialized(t, localHub)

	// 1. Keyed thread/start against an older remote hub is refused pre-send without touching local creations/state.json.
	_, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		ClientMutationID: "mut-older-remote",
		Source:           "remote-a",
		Model:            "openai/gpt-5",
		CWD:              "/remote/work",
	})
	wire, data := wireErrorData(t, err)
	if wire.Code != appwire.CodeInvalidParams ||
		data.EvenerErrorInfo != appwire.ErrorInvalidParams ||
		data.ClientMutationID != "mut-older-remote" ||
		data.MutationOutcome != appwire.MutationOutcomeNotAccepted ||
		data.RetryDisposition != appwire.RetryDispositionNone {
		t.Fatalf("older remote keyed refusal = wire:%+v data:%+v", wire, data)
	}
	if remoteCalls.Load() != 0 {
		t.Fatalf("remoteCalls = %d after keyed refusal on older remote, want 0", remoteCalls.Load())
	}
	if _, statErr := os.Stat(filepath.Join(stateRoot, "creations", "state.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("older remote keyed refusal unexpectedly wrote creations/state.json: %v", statErr)
	}

	// 2. Keyless thread/start against that same older remote hub still succeeds.
	resp, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		Source: "remote-a",
		Model:  "openai/gpt-5",
		CWD:    "/remote/work",
	})
	if err != nil {
		t.Fatalf("keyless older remote start failed: %v", err)
	}
	if resp.Thread.Evener.Ref != "remote-a:"+remoteSessionID || resp.ClientMutationID != "" || resp.Disposition != "" || remoteCalls.Load() != 1 {
		t.Fatalf("keyless older remote resp = %+v, remoteCalls=%d", resp, remoteCalls.Load())
	}
}

func TestThreadStartIdempotencyCommitSpawnedAfterRenameRetainsSpawnedAndFinishesTurn1(t *testing.T) {
	stateRoot := t.TempDir()
	runDir := t.TempDir()
	sessionID := hubtest.SessionID(t)
	var turnCalls atomic.Int32
	var gotTurnMutationID string

	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, _ appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: sessionID, SessionID: sessionID, Source: "local",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener: appwire.EvenerThread{Ref: "local:" + sessionID, InstanceID: sessionID, Capabilities: appwire.ThreadCapabilities{Send: true}},
		}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		turnCalls.Add(1)
		gotTurnMutationID = params.ClientMutationID
		return appwire.TurnStartResponse{
			Turn:    appwire.Turn{ID: "turn_1", Status: "inProgress"},
			Receipt: appwire.MutationReceipt{ClientMutationID: params.ClientMutationID, Disposition: appwire.MutationDispositionApplied, ThreadID: sessionID, TurnID: "turn_1"},
		}, nil
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	defer daemonHTTP.Close()

	store, err := hubcore.NewCreationStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	var writeCount atomic.Int32
	store.SetFaultsForTest(nil, func() error {
		if writeCount.Add(1) == 2 { // 1 = Acquire (spawning), 2 = CommitSpawned (spawned)
			return errors.New("dir fsync failed after rename")
		}
		return nil
	})

	entry := localEntry(daemonHTTP.URL, sessionID)
	var spawns atomic.Int32
	hub := newHubRPCTestServer(t, testWebConfig(t, hubcore.WebConfig{
		HubStateRoot:  stateRoot,
		CreationStore: store,
		RunDir:        runDir,
		Roster:        hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusIdle}),
		Spawner:       spawnerWritingEntry(t, runDir, entry, &spawns),
	}))
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)

	params := appwire.ThreadStartParams{
		ClientMutationID: "mut-spawned-fsync",
		Model:            "openai/gpt-5",
		CWD:              t.TempDir(),
		Input:            []appwire.InputItem{{Type: "text", Text: "finish turn 1"}},
	}
	_, err = appwiretest.ThreadStart(context.Background(), client, params)
	wire := requireWireError(t, err, appwire.CodeUnavailable)
	raw, _ := json.Marshal(wire.Data)
	var partial appwire.ThreadStartPartialErrorData
	_ = json.Unmarshal(raw, &partial)
	if partial.CreatedRef != "local:"+sessionID || partial.CreatedInstanceID != sessionID || partial.ClientMutationID != params.ClientMutationID || partial.RetryDisposition == appwire.RetryDispositionBlocked {
		t.Fatalf("CommitSpawned AfterRename partial = %+v (raw=%s)", partial, raw)
	}
	resp, err := appwiretest.ThreadStart(context.Background(), client, params)
	if err != nil {
		t.Fatalf("retry after CommitSpawned AfterRename failed: %v", err)
	}
	if spawns.Load() != 1 || turnCalls.Load() != 1 || gotTurnMutationID == "" || resp.Turn.ID != "turn_1" {
		t.Fatalf("spawns=%d turnCalls=%d turnMut=%q resp=%+v", spawns.Load(), turnCalls.Load(), gotTurnMutationID, resp)
	}

	// BeforeRename on CommitSpawned marks uncertain (blocking retry) while still preserving createdRef/createdInstanceId.
	var beforeCount atomic.Int32
	store.SetFaultsForTest(func() error {
		if beforeCount.Add(1) == 2 { // 1 = Acquire (spawning), 2 = CommitSpawned (spawned)
			return errors.New("rename failed before publish")
		}
		return nil
	}, nil)
	paramsBefore := params
	paramsBefore.ClientMutationID = "mut-spawned-beforerename"
	_, err = appwiretest.ThreadStart(context.Background(), client, paramsBefore)
	wireBefore := requireWireError(t, err, appwire.CodeInternalError)
	rawBefore, _ := json.Marshal(wireBefore.Data)
	var partialBefore appwire.ThreadStartPartialErrorData
	_ = json.Unmarshal(rawBefore, &partialBefore)
	if partialBefore.CreatedRef != "local:"+sessionID || partialBefore.CreatedInstanceID != sessionID ||
		partialBefore.MutationOutcome != appwire.MutationOutcomeUnknown || partialBefore.RetryDisposition != appwire.RetryDispositionBlocked {
		t.Fatalf("CommitSpawned BeforeRename partial = %+v (raw=%s)", partialBefore, rawBefore)
	}
	_, retryBeforeErr := appwiretest.ThreadStart(context.Background(), client, paramsBefore)
	_, retryBeforeData := wireErrorData(t, retryBeforeErr)
	if retryBeforeData.MutationOutcome != appwire.MutationOutcomeUnknown || retryBeforeData.RetryDisposition != appwire.RetryDispositionBlocked || spawns.Load() != 2 {
		t.Fatalf("retry after CommitSpawned BeforeRename data=%+v spawns=%d", retryBeforeData, spawns.Load())
	}
}

func newRemoteFeatureHubClient(t *testing.T, name string, features appwire.FeatureSet) *appwire.Client {
	t.Helper()
	srv := appserver.NewServer(appserver.ServerConfig{ServerName: name, SourceID: "local", Features: features})
	httpSrv := httptest.NewServer(http.HandlerFunc(srv.ServeWebSocket))
	t.Cleanup(httpSrv.Close)
	return dialHubRPCInitialized(t, httpSrv)
}

func TestThreadStartIdempotencyManifestSourcesCapability(t *testing.T) {
	capableClient := newRemoteFeatureHubClient(t, "remote-capable", appwire.FeatureSet{ThreadStartIdempotency: true})
	olderClient := newRemoteFeatureHubClient(t, "remote-older", appwire.FeatureSet{ThreadStartIdempotency: false})

	var dials atomic.Int32
	web := NewWebServer(hubcore.WebConfig{
		HubAddr:       "127.0.0.1:9180",
		LocalHostName: "controller",
		RemoteHosts: []hostreg.Host{
			{Name: "remote-capable", SSH: "remote-capable.example"},
			{Name: "remote-older", SSH: "remote-older.example"},
			{Name: "remote-offline", SSH: "remote-offline.example"},
		},
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
			dials.Add(1)
			return nil, errors.New("apiTreeSources must never dial")
		},
		RemoteHostClientIfAttached: func(host string) (*appwire.Client, bool) {
			switch host {
			case "remote-capable":
				return capableClient, true
			case "remote-older":
				return olderClient, true
			default:
				return nil, false
			}
		},
		RemoteHostOnline: func(host string) bool {
			return host != "remote-offline"
		},
	})
	sources := web.apiTreeSources()
	byID := make(map[string]hubapi.Source, len(sources))
	for _, src := range sources {
		byID[src.ID] = src
	}
	if !byID["local"].ThreadStartIdempotency {
		t.Fatalf("local source ThreadStartIdempotency = false, want true: %+v", byID["local"])
	}
	if !byID["remote-capable"].ThreadStartIdempotency || !byID["remote-capable"].Online {
		t.Fatalf("remote-capable = %+v, want Online=true ThreadStartIdempotency=true", byID["remote-capable"])
	}
	if byID["remote-older"].ThreadStartIdempotency || !byID["remote-older"].Online {
		t.Fatalf("remote-older = %+v, want Online=true ThreadStartIdempotency=false", byID["remote-older"])
	}
	if byID["remote-offline"].ThreadStartIdempotency || byID["remote-offline"].Online {
		t.Fatalf("remote-offline = %+v, want Online=false ThreadStartIdempotency=false", byID["remote-offline"])
	}
	if dials.Load() != 0 {
		t.Fatalf("apiTreeSources dialed %d times, want 0", dials.Load())
	}

	// Projection preserves ThreadStartIdempotency across clone + Manifest().
	inputs := navigationBuildInputsFromTreeSnapshot("generation", 1, hubcore.Tree{}, sources, hubapi.AttentionSummary{}, nil, nil, nil, nil, nil)
	projection, err := buildNavigationProjection(t.Context(), inputs)
	if err != nil {
		t.Fatalf("buildNavigationProjection: %v", err)
	}
	manifest := projection.Manifest()
	if len(manifest.Sources) != len(sources) || !manifest.Sources[0].ThreadStartIdempotency {
		t.Fatalf("projection.Manifest().Sources = %+v, want ThreadStartIdempotency preserved", manifest.Sources)
	}

	// Corrupt creation store reports local ThreadStartIdempotency=false.
	corruptRoot := t.TempDir()
	statePath := filepath.Join(corruptRoot, "creations", "state.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte(`{corrupt`), 0o600); err != nil {
		t.Fatal(err)
	}
	corruptWeb := NewWebServer(hubcore.WebConfig{HubAddr: "127.0.0.1:9180", HubStateRoot: corruptRoot})
	corruptSources := corruptWeb.apiTreeSources()
	if len(corruptSources) != 1 || corruptSources[0].ID != "local" || corruptSources[0].ThreadStartIdempotency {
		t.Fatalf("corrupt store local source = %+v, want ThreadStartIdempotency=false", corruptSources)
	}
}

func TestThreadStartIdempotencyManifestInvalidatesOnRemoteCapabilityChange(t *testing.T) {
	capableClient := newRemoteFeatureHubClient(t, "remote-capable", appwire.FeatureSet{ThreadStartIdempotency: true})
	olderClient := newRemoteFeatureHubClient(t, "remote-older", appwire.FeatureSet{ThreadStartIdempotency: false})

	var mu sync.Mutex
	var online bool
	var attachedClient *appwire.Client
	hosts := []hostreg.Host{{Name: "remote-a", SSH: "remote-a.example"}}
	cfg := hubcore.WebConfig{
		HubAddr:     "127.0.0.1:9180",
		RemoteHosts: hosts,
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
			mu.Lock()
			defer mu.Unlock()
			online = true
			attachedClient = capableClient
			return capableClient, nil
		},
		RemoteHostClientIfAttached: func(host string) (*appwire.Client, bool) {
			if host != "remote-a" {
				return nil, false
			}
			mu.Lock()
			defer mu.Unlock()
			return attachedClient, attachedClient != nil
		},
		RemoteHostOnline: func(host string) bool {
			if host != "remote-a" {
				return false
			}
			mu.Lock()
			defer mu.Unlock()
			return online
		},
	}
	web := NewWebServer(cfg)
	initial, err := web.navigation.readV3(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}, nil)
	if err != nil {
		t.Fatalf("initial manifest readV3: %v", err)
	}
	rev1 := initial.Response.Revision
	if got := web.apiTreeSources()[1]; got.Online || got.ThreadStartIdempotency {
		t.Fatalf("before attach remote-a = %+v, want offline and keyless", got)
	}

	// Explicit attach flips remote-a to online + capable and invalidates manifest.
	hostReg, err := hostreg.New(hosts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hostAttachAndInvalidateSources(t.Context(), web.cfg, web.sources, hostReg, web.navigation, appwire.HostAttachParams{Host: "remote-a"}); err != nil {
		t.Fatalf("hostAttachAndInvalidateSources: %v", err)
	}
	web.navigation.refreshPending(t.Context())
	pubs1 := web.navigation.DrainPublications()
	if len(pubs1) != 1 || !hasNavigationTarget(pubs1[0].Targets, appwire.NavigationTargetManifest, "") {
		t.Fatalf("attach publications = %+v, want manifest invalidation", pubs1)
	}
	afterAttach, err := web.navigation.readV3(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}, nil)
	if err != nil {
		t.Fatalf("after attach manifest readV3: %v", err)
	}
	if afterAttach.Response.Revision <= rev1 {
		t.Fatalf("manifest revision after attach = %d, want > %d", afterAttach.Response.Revision, rev1)
	}
	var attachedManifest hubapi.NavigationManifest
	var snap1 hubapi.NavigationSnapshot
	if err := json.Unmarshal(afterAttach.Response.Data, &snap1); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(snap1.Metadata, &attachedManifest); err != nil {
		t.Fatal(err)
	}
	if !attachedManifest.Sources[1].Online || !attachedManifest.Sources[1].ThreadStartIdempotency {
		t.Fatalf("attached manifest source = %+v, want Online=true ThreadStartIdempotency=true", attachedManifest.Sources[1])
	}

	// Reattach with an older remote client + SSH invalidation flips ThreadStartIdempotency back to false and advances manifest revision.
	mu.Lock()
	attachedClient = olderClient
	mu.Unlock()
	hubSSHStateInvalidation(func() { web.navigation.Invalidate(navigationChangeHint{Sources: true}) }, nil)(sshconn.Event{Host: "remote-a", Kind: sshconn.EventAttached})
	web.navigation.refreshPending(t.Context())
	pubs2 := web.navigation.DrainPublications()
	if len(pubs2) != 1 || !hasNavigationTarget(pubs2[0].Targets, appwire.NavigationTargetManifest, "") {
		t.Fatalf("reattach publications = %+v, want manifest invalidation", pubs2)
	}
	afterReattach, err := web.navigation.readV3(t.Context(), navigationResourceKey{Kind: navigationResourceManifest}, nil)
	if err != nil {
		t.Fatalf("after reattach manifest readV3: %v", err)
	}
	if afterReattach.Response.Revision <= afterAttach.Response.Revision {
		t.Fatalf("manifest revision after capability flip = %d, want > %d", afterReattach.Response.Revision, afterAttach.Response.Revision)
	}
	var reattachedManifest hubapi.NavigationManifest
	var snap2 hubapi.NavigationSnapshot
	if err := json.Unmarshal(afterReattach.Response.Data, &snap2); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(snap2.Metadata, &reattachedManifest); err != nil {
		t.Fatal(err)
	}
	if !reattachedManifest.Sources[1].Online || reattachedManifest.Sources[1].ThreadStartIdempotency {
		t.Fatalf("reattached manifest source = %+v, want Online=true ThreadStartIdempotency=false", reattachedManifest.Sources[1])
	}
}
