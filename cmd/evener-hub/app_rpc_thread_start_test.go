package hub

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/appwire/appwiretest"
	"primeradiant.com/evener/cmd/evener-hub/internal/fspaths"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/llm/registry"
	"primeradiant.com/evener/rendezvous"
)

type startRelayFailureSource struct {
	startResumeRelaySource
}

func (s *startRelayFailureSource) SubscribeThread(context.Context, appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	return nil, errors.New("subscribe failed after start")
}

func TestHubRPCThreadStartKeepsProviderForModelIDsWithSlashes(t *testing.T) {
	runDir := t.TempDir()
	var got hubcore.SpawnRequest
	spawner := &fakeRPCSpawner{
		spawn: func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
			got = req
			return rendezvous.Entry{
				PID:       106,
				Protocol:  appwire.ProtocolVersion,
				SourceID:  "local",
				ThreadID:  "th_slash_model",
				SessionID: "sess_slash_model",
			}, nil
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Spawner: spawner, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	if _, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		ModelProvider: "openrouter",
		Model:         "deepseek/deepseek-v4-flash",
		CWD:           "/tmp",
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if got.Resolved.Effective.Model != "openrouter/deepseek/deepseek-v4-flash" {
		t.Fatalf("spawn model=%q, want openrouter/deepseek/deepseek-v4-flash", got.Resolved.Effective.Model)
	}
}

func TestHubRPCThreadStartDeliversPromptWhenFirstRosterProbeFails(t *testing.T) {
	for _, fault := range []string{"probe", "listing", "status", "stale-instance"} {
		t.Run(fault, func(t *testing.T) {
			const sessionID = "033snFBSHFr78ZbQQMAeBD"
			daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
			appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
				return appwire.ThreadReadResponse{Thread: appwire.Thread{
					ID:        sessionID,
					SessionID: sessionID,
					Source:    "local",
					Evener: appwire.EvenerThread{
						Ref:          params.Ref,
						Capabilities: appwire.ThreadCapabilities{Send: true},
						Diagnostics: &appwire.EvenerDiagnostics{Delegates: []appwire.EvenerDelegateInfo{
							{ChildSessionID: "active-child", Lifecycle: "running", Status: "running"},
							{ChildSessionID: "idle-child", Lifecycle: "idle", Status: "completed", Resumable: true},
							{ChildSessionID: "closed-child", Lifecycle: "closed", Status: "completed", Terminal: true},
						}},
					},
				}}, nil
			})
			appserver.HandleTyped(daemon.Router(), appwire.MethodThreadList, func(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
				if fault == "status" {
					return appwire.ThreadListResponse{}, appwire.Unavailable("status temporarily unavailable")
				}
				return appwire.ThreadListResponse{Data: []appwire.Thread{{ID: sessionID, SessionID: sessionID, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}}}}, nil
			})
			var gotPrompt string
			var turns int
			appserver.HandleTyped(daemon.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
				gotPrompt = inputTextForTest(params.Input)
				turns++
				return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_1"}}, nil
			})
			daemonHTTP := httptest.NewUnstartedServer(http.HandlerFunc(daemon.ServeWebSocket))
			dropper := &dropFirstConnectionListener{
				Listener: daemonHTTP.Listener,
				dropped:  make(chan struct{}),
			}
			if fault == "probe" {
				daemonHTTP.Listener = dropper
			}
			daemonHTTP.Start()
			defer daemonHTTP.Close()

			runDir := t.TempDir()
			entry := rendezvous.Entry{
				PID:       os.Getpid(),
				Protocol:  appwire.ProtocolVersion,
				Endpoint:  "ws" + strings.TrimPrefix(daemonHTTP.URL, "http"),
				SourceID:  "local",
				ThreadID:  sessionID,
				SessionID: sessionID,
			}
			if fault == "stale-instance" {
				entry.InstanceID = "fresh-instance"
			}
			var spawns int
			spawner := &fakeRPCSpawner{spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
				spawns++
				writeRendezvous(t, runDir, entry)
				if fault == "listing" || fault == "stale-instance" {
					if err := os.WriteFile(filepath.Join(runDir, "1.json"), []byte("{"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return entry, nil
			}}
			roster := hubcore.NewRoster(runDir, &hubcore.StatusProber{})
			if fault == "stale-instance" {
				old := entry
				old.InstanceID = "previous-instance"
				writeRendezvous(t, runDir, old)
				roster.Refresh()
			}
			hub := newHubRPCTestServer(t, hubcore.WebConfig{
				RunDir:  runDir,
				Roster:  roster,
				Spawner: spawner,
				Past:    hubcore.NewPastIndex(""),
			})
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()

			if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			resp, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
				Model: "openai/gpt-5",
				CWD:   "/tmp",
				Input: []appwire.InputItem{{Type: "text", Text: "review the open PRs"}},
			})
			if err != nil {
				t.Fatalf("ThreadStart: %v", err)
			}
			if fault == "probe" {
				select {
				case <-dropper.dropped:
				default:
					t.Fatal("startup test did not drop the first daemon connection")
				}
			}
			if gotPrompt != "review the open PRs" {
				t.Fatalf("prompt=%q, want review the open PRs", gotPrompt)
			}
			if resp.Thread.Evener.Ref != "local:"+sessionID || resp.Turn.ID != "turn_1" {
				t.Fatalf("response=%+v", resp)
			}

			if spawns != 1 || turns != 1 {
				t.Fatalf("spawns=%d turns=%d", spawns, turns)
			}
			if live, ok := roster.Find(sessionID); !ok || live.PID != entry.PID || live.Crashed {
				t.Errorf("spawned daemon is not registered: %+v, present=%v", live, ok)
			}
			if live, _ := roster.Find(sessionID); live.InstanceID != entry.InstanceID {
				t.Errorf("registered instance=%q, want %q", live.InstanceID, entry.InstanceID)
			}
			if state, ok := roster.SubagentState("active-child"); !ok || state != appwire.ThreadStatusActive {
				t.Errorf("active child projection: state=%q live=%v", state, ok)
			}
			if state, ok := roster.SubagentState("idle-child"); !ok || state != appwire.ThreadStatusIdle {
				t.Errorf("idle child projection: state=%q live=%v", state, ok)
			}
			if roster.IsSubagentActive("closed-child") {
				t.Error("closed child published as live")
			}
			if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: resp.Thread.Evener.Ref}); err != nil {
				t.Fatalf("subsequent ThreadRead: %v", err)
			}
			if _, err := client.TurnStart(context.Background(), appwire.TurnStartParams{
				Ref: resp.Thread.Evener.Ref, ClientMutationID: "follow-up", ExpectedInstanceID: sessionID,
				Input: []appwire.InputItem{{Type: "text", Text: "continue review"}},
			}); err != nil {
				t.Fatalf("subsequent TurnStart: %v", err)
			}
			if spawns != 1 || turns != 2 || gotPrompt != "continue review" {
				t.Fatalf("follow-up delivery: spawns=%d turns=%d prompt=%q", spawns, turns, gotPrompt)
			}

		})
	}
}

func TestHubRPCThreadStartPassesExplicitNonInteractive(t *testing.T) {
	runDir := t.TempDir()
	var got hubcore.SpawnRequest
	spawner := &fakeRPCSpawner{
		spawn: func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
			got = req
			return rendezvous.Entry{
				PID:       107,
				Protocol:  appwire.ProtocolVersion,
				SourceID:  "local",
				ThreadID:  "th_noninteractive",
				SessionID: "sess_noninteractive",
			}, nil
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Spawner: spawner, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	nonInteractive := true
	if _, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		Model:          "openai/gpt-5.2",
		CWD:            "/tmp",
		NonInteractive: &nonInteractive,
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if got.Resolved.Effective.NonInteractive == nil || !*got.Resolved.Effective.NonInteractive {
		t.Fatalf("spawn non_interactive = %v, want true", got.Resolved.Effective.NonInteractive)
	}
}

func TestHubRPCThreadStartPassesNonInteractiveLaunchOverride(t *testing.T) {
	runDir := t.TempDir()
	var got hubcore.SpawnRequest
	spawner := &fakeRPCSpawner{
		spawn: func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
			got = req
			return rendezvous.Entry{
				PID:       108,
				Protocol:  appwire.ProtocolVersion,
				SourceID:  "local",
				ThreadID:  "th_noninteractive_override",
				SessionID: "sess_noninteractive_override",
			}, nil
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Spawner: spawner, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	nonInteractive := true
	if _, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		Model: "openai/gpt-5.2",
		CWD:   "/tmp",
		LaunchOverrides: &appwire.LaunchConfigLayer{
			NonInteractive: &nonInteractive,
		},
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if got.Resolved.Effective.NonInteractive == nil || !*got.Resolved.Effective.NonInteractive {
		t.Fatalf("spawn non_interactive = %v, want true", got.Resolved.Effective.NonInteractive)
	}
}

func TestHubRPCThreadStartPropagatesSpawnerStderrAsHubLaunchError(t *testing.T) {
	runDir := t.TempDir()
	spawnErr := strings.Join([]string{
		"daemon spawn failed",
		"process exited before rendezvous",
		"exit status 1",
		`evener serve: session creation: plugin initialization: resolving plugin dir "/Users/jesse/git/superpowers/superpowers": lstat /Users: no such file or directory`,
	}, ": ")
	spawner := &fakeRPCSpawner{
		spawn: func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
			return rendezvous.Entry{}, errors.New(spawnErr)
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Spawner: spawner, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	_, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		ModelProvider: "openai",
		Model:         "gpt-5",
		CWD:           "/tmp",
	})
	assertHubLaunchError(t, err)
	if !strings.Contains(err.Error(), "plugin initialization: resolving plugin dir") {
		t.Fatalf("error did not include daemon stderr: %v", err)
	}
}

func TestValidateLaunchPathRejectsMissingPluginDir(t *testing.T) {
	resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: filepath.Join(t.TempDir(), "missing"), Kind: "dir"})
	if resp.Valid {
		t.Fatalf("valid=%v, want false", resp.Valid)
	}
	if resp.Error == "" {
		t.Fatal("error is empty, want the reason the path is invalid")
	}
}

func TestValidateLaunchPathAcceptsExecutableCommand(t *testing.T) {
	resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: "sh", Kind: "command"})
	if !resp.Valid {
		t.Fatalf("valid=false error=%q", resp.Error)
	}
	if resp.Path == "" || !filepath.IsAbs(resp.Path) {
		t.Fatalf("resolved command path=%q, want absolute", resp.Path)
	}
}

func TestValidateLaunchPathAcceptsMissingOutputFileWithWritableParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: path, Kind: "output-file"})
	if !resp.Valid {
		t.Fatalf("valid=false error=%q", resp.Error)
	}
	if resp.Path != path {
		t.Fatalf("path=%q, want %q", resp.Path, path)
	}
}

func TestValidateLaunchPathRejectsOutputFileWithMissingParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "trace.jsonl")
	resp := fspaths.ValidateLaunchPath(appwire.PathValidateParams{Path: path, Kind: "output-file"})
	if resp.Valid {
		t.Fatalf("valid=true, want false")
	}
	if resp.Error == "" {
		t.Fatal("error is empty, want the reason the path is invalid")
	}
}

func TestHubRPCThreadStartRejectsModelOutsideEvenerLaunchContractBeforeSpawn(t *testing.T) {
	runDir := t.TempDir()
	var spawnCalled bool
	spawner := &fakeRPCSpawner{
		spawn: func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
			spawnCalled = true
			return rendezvous.Entry{}, nil
		},
		launchModels: func(context.Context) ([]appwire.ModelDescriptor, error) {
			return []appwire.ModelDescriptor{{Provider: "openai", Model: "gpt-5"}}, nil
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:  runDir,
		Spawner: spawner,
		Past:    hubcore.NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	_, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		ModelProvider: "openai",
		Model:         "gpt-stale",
		CWD:           "/tmp",
	})
	assertHubLaunchError(t, err)
	if spawnCalled {
		t.Fatal("spawn was called for a model outside the Evener launch contract")
	}
}

func TestHubRPCThreadStartAllowsModelWhenProviderDoesNotEnumerateLaunchModels(t *testing.T) {
	runDir := t.TempDir()
	var got hubcore.SpawnRequest
	spawner := &fakeRPCModelContractSpawner{
		contract: appwire.ModelListResponse{
			Data: []appwire.ModelDescriptor{{Provider: "ollama", Model: "local"}},
			Diagnostics: []appwire.ModelListDiagnostic{{
				Provider: "openai",
				Source:   "provider",
				Title:    "Provider error",
				Message:  "HTTP 403",
			}},
		},
	}
	spawner.spawn = func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
		got = req
		return rendezvous.Entry{
			PID:       107,
			Protocol:  appwire.ProtocolVersion,
			SourceID:  "local",
			ThreadID:  "th_non_enumerable_model",
			SessionID: "sess_non_enumerable_model",
		}, nil
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Spawner: spawner, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	if _, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		ModelProvider: "openai",
		Model:         "gpt-5.5",
		CWD:           "/tmp",
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if got.Resolved.Effective.Model != "openai/gpt-5.5" {
		t.Fatalf("spawn model=%q, want openai/gpt-5.5", got.Resolved.Effective.Model)
	}
}

func TestHubRPCThreadStartAllowsModelWhenProviderHasLaunchDiagnostic(t *testing.T) {
	runDir := t.TempDir()
	var got hubcore.SpawnRequest
	spawner := &fakeRPCModelContractSpawner{
		contract: appwire.ModelListResponse{
			Diagnostics: []appwire.ModelListDiagnostic{{
				Provider: "openai",
				Source:   "provider",
				Title:    "Provider error",
				Message:  "HTTP 403",
			}},
		},
	}
	spawner.spawn = func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
		got = req
		return rendezvous.Entry{
			PID:       108,
			Protocol:  appwire.ProtocolVersion,
			SourceID:  "local",
			ThreadID:  "th_degraded_model",
			SessionID: "sess_degraded_model",
		}, nil
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Spawner: spawner, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	if _, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		ModelProvider: "openai",
		Model:         "gpt-5.5",
		CWD:           "/tmp",
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if got.Resolved.Effective.Model != "openai/gpt-5.5" {
		t.Fatalf("spawn model=%q, want openai/gpt-5.5", got.Resolved.Effective.Model)
	}
}

func TestHubRPCThreadStartRejectsProviderMissingFromDegradedLaunchContract(t *testing.T) {
	runDir := t.TempDir()
	var spawnCalled bool
	spawner := &fakeRPCModelContractSpawner{
		contract: appwire.ModelListResponse{
			Data: []appwire.ModelDescriptor{{Provider: "ollama", Model: "local"}},
			Diagnostics: []appwire.ModelListDiagnostic{{
				Provider: "openai",
				Source:   "provider",
				Title:    "Provider error",
				Message:  "HTTP 403",
			}},
		},
	}
	spawner.spawn = func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
		spawnCalled = true
		return rendezvous.Entry{}, nil
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Spawner: spawner, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	_, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		ModelProvider: "anthropic",
		Model:         "claude-test",
		CWD:           "/tmp",
	})
	assertHubLaunchError(t, err)
	if spawnCalled {
		t.Fatal("spawn was called for provider missing from degraded launch contract")
	}
}

// TestHubRPCThreadStartAllowsIntentionallySkippedLaunchProvider: a provider
// the launch contract never enumerated is still launchable when the registry
// holds the instance (spec §11.3) — an endpoint that lists no models is
// configured, not broken.
func TestHubRPCThreadStartAllowsIntentionallySkippedLaunchProvider(t *testing.T) {
	runDir := t.TempDir()
	var got hubcore.SpawnRequest
	spawner := &fakeRPCModelContractSpawner{
		contract: appwire.ModelListResponse{
			Data: []appwire.ModelDescriptor{{Provider: "ollama", Model: "local"}},
			Diagnostics: []appwire.ModelListDiagnostic{{
				Provider: "openai",
				Source:   "provider",
				Title:    "Provider error",
				Message:  "HTTP 403",
			}},
		},
	}
	spawner.spawn = func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
		got = req
		return rendezvous.Entry{PID: 301, ThreadID: "th_orclaude", SessionID: "th_orclaude"}, nil
	}
	credsStore := newTestCredentialsStore(t)
	reg := newSpawnGateRegistry(t, t.TempDir(), map[string]string{"OPENROUTER_API_KEY": "k"}, map[string]registry.Provider{
		"orclaude": {Base: "openrouter", Protocol: registry.ProtocolAnthropic},
	}, credsStore)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Spawner: spawner, Past: hubcore.NewPastIndex(""), Registry: reg, CredsStore: credsStore})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	resp, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		ModelProvider: "orclaude",
		Model:         "anthropic/claude-3-5-sonnet",
		CWD:           "/tmp",
	})
	if err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if got.Resolved.Effective.Model != "orclaude/anthropic/claude-3-5-sonnet" {
		t.Fatalf("spawn model=%q", got.Resolved.Effective.Model)
	}
	if resp.Thread.Evener.Ref != "local:th_orclaude" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestHubRPCThreadStartRejectsMalformedModelBeforeSpawn(t *testing.T) {
	runDir := t.TempDir()
	var spawnCalled bool
	spawner := &fakeRPCSpawner{
		spawn: func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
			spawnCalled = true
			return rendezvous.Entry{}, nil
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:  runDir,
		Spawner: spawner,
		Past:    hubcore.NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	_, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		Model: "openrouter",
		CWD:   "/tmp",
	})
	if err == nil {
		t.Fatal("ThreadStart succeeded for malformed model")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error %T does not preserve WireError: %v", err, err)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("wire=%+v", wire)
	}
	if spawnCalled {
		t.Fatal("spawn was called for malformed model")
	}
}

func TestThreadStart_LaunchOverridesApplied(t *testing.T) {
	runDir := t.TempDir()
	var got hubcore.SpawnRequest
	spawner := &fakeRPCSpawner{
		spawn: func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
			got = req
			return rendezvous.Entry{
				PID:       200,
				Protocol:  appwire.ProtocolVersion,
				SourceID:  "local",
				ThreadID:  "th_overrides",
				SessionID: "sess_overrides",
			}, nil
		},
	}
	maxRounds := 7
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:       runDir,
		HubStateRoot: t.TempDir(),
		Spawner:      spawner,
		Past:         hubcore.NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	if _, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		Model: "openai/gpt-5",
		CWD:   "/tmp",
		LaunchOverrides: &appwire.LaunchConfigLayer{
			SkillsDirs: []string{"/per-launch"},
			MaxRounds:  &maxRounds,
		},
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	eff := got.Resolved.Effective
	if !slices.Contains(eff.SkillsDirs, "/per-launch") {
		t.Errorf("SkillsDirs = %v, want /per-launch", eff.SkillsDirs)
	}
	if eff.MaxRounds == nil || *eff.MaxRounds != 7 {
		t.Errorf("MaxRounds = %v, want 7", eff.MaxRounds)
	}
	// Legacy scalar wins: model comes from params.Model, not launchOverrides.
	if eff.Model != "openai/gpt-5" {
		t.Errorf("Model = %q, want openai/gpt-5", eff.Model)
	}
}

func TestHubRPCThreadStartUsesGlobalLaunchDefaultModel(t *testing.T) {
	runDir := t.TempDir()
	// stateRoot and launchRoot are deliberately distinct: HubStateRoot and
	// LaunchConfigRoot must resolve independently. Aliasing them (as a
	// previous version of this test did) would let a regression that wires
	// ThreadStart's launch resolution back onto HubStateRoot pass silently,
	// since both fields would coincidentally point at the same directory.
	stateRoot := t.TempDir()
	launchRoot := t.TempDir()
	cwd := t.TempDir()
	c := newHubLaunchController(launchRoot, false)
	if _, err := c.SetLayer(context.Background(), appwire.LaunchConfigSetLayerParams{
		CWD:    cwd,
		Layer:  "global",
		Config: appwire.LaunchConfigLayer{Model: "openai/gpt-5"},
	}); err != nil {
		t.Fatalf("SetLayer: %v", err)
	}
	var got hubcore.SpawnRequest
	spawner := &fakeRPCModelContractSpawner{
		spawn: func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
			got = req
			return rendezvous.Entry{
				PID:       201,
				Protocol:  appwire.ProtocolVersion,
				SourceID:  "local",
				ThreadID:  "th_default_model",
				SessionID: "sess_default_model",
			}, nil
		},
		contract: appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{
			Provider: "openai",
			Model:    "gpt-5",
		}}},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:           runDir,
		HubStateRoot:     stateRoot,
		LaunchConfigRoot: launchRoot,
		Spawner:          spawner,
		Past:             hubcore.NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	if _, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		CWD: cwd,
	}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if got.Resolved.Effective.Model != "openai/gpt-5" {
		t.Errorf("Model = %q, want openai/gpt-5", got.Resolved.Effective.Model)
	}
	if got.Provider != "openai" {
		t.Errorf("Provider = %q, want openai", got.Provider)
	}
}

func TestHubRPCThreadStartRelaysReturnedSourceThread(t *testing.T) {
	source := &startResumeRelaySource{
		id: "codex",
		thread: appwire.Thread{
			ID:        "th_start_relay",
			SessionID: "th_start_relay",
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:th_start_relay", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 4),
		subscribed:    make(chan struct{}, 1),
		canceled:      make(chan struct{}, 1),
	}
	srv := newWebSourceTestServer(t, source)

	client := dialHubRPCInitialized(t, srv)
	// Source, not Harness, names the host source: a harness naming a registered
	// source is now refused (TestHubThreadStartRefusesHarnessNamingHostSource).
	resp, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{Source: "codex", CWD: "/work", Input: []appwire.InputItem{{Type: "text", Text: "hello"}}})
	if err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if resp.Thread.Evener.Ref != "codex:th_start_relay" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
	expectRelaySubscription(t, source.subscribed)

	source.notifications <- appwire.Notification{
		Method: appwire.NotifyOverlayDelta,
		Params: testRawJSON(t, appwire.OverlayDeltaParams{
			ThreadID: "th_start_relay",
			Ref:      "codex:th_start_relay",
			Delta:    "after start",
		}),
	}
	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyOverlayDelta {
			t.Fatalf("method=%q", got.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for start relay notification")
	}
}

func TestHubRPCThreadStartReturnsThreadWhenPostStartRelayFails(t *testing.T) {
	source := &startRelayFailureSource{
		id: "codex",
		thread: appwire.Thread{
			ID:        "th_start_relay_fail",
			SessionID: "th_start_relay_fail",
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:th_start_relay_fail", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
	}
	srv := newWebSourceTestServer(t, source)

	client := dialHubRPCInitialized(t, srv)
	// Source, not Harness, names the host source: a harness naming a registered
	// source is now refused (TestHubThreadStartRefusesHarnessNamingHostSource).
	resp, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{Source: "codex", CWD: "/work", Input: []appwire.InputItem{{Type: "text", Text: "hello"}}})
	if err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if resp.Thread.Evener.Ref != "codex:th_start_relay_fail" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
	select {
	case got := <-client.Notifications():
		if got.Method != appwire.NotifyWarning {
			t.Fatalf("method=%q, want warning", got.Method)
		}
		if !strings.Contains(string(got.Params), "subscribe failed after start") || !strings.Contains(string(got.Params), `"source":"hub"`) {
			t.Fatalf("warning params=%s", got.Params)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relay warning")
	}
}

type dropFirstConnectionListener struct {
	net.Listener
	once    sync.Once
	dropped chan struct{}
}

func (l *dropFirstConnectionListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		dropped := false
		l.once.Do(func() {
			dropped = true
			_ = conn.Close()
			close(l.dropped)
		})
		if dropped {
			continue
		}
		return conn, nil
	}
}

func TestHubRPCThreadStartEnvModelSatisfiesRequiredGate(t *testing.T) {
	// A spawn with no model in any launch layer still launches when
	// EVENER_MODEL is set for the hub process: the agent's own fallback
	// chain (flag > env > none) would use it. The "model is required"
	// gate must not reject what the child would have run with anyway —
	// and the resolve RPC's "(default)" label already names that model,
	// so the gate and the label must agree.
	runDir := t.TempDir()
	launchRoot := t.TempDir()
	t.Setenv("EVENER_MODEL", "openai/gpt-5")
	var got hubcore.SpawnRequest
	spawner := &fakeRPCModelContractSpawner{
		spawn: func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
			got = req
			return rendezvous.Entry{
				PID:       202,
				Protocol:  appwire.ProtocolVersion,
				SourceID:  "local",
				ThreadID:  "th_env_model",
				SessionID: "sess_env_model",
			}, nil
		},
		contract: appwire.ModelListResponse{Data: []appwire.ModelDescriptor{{
			Provider: "openai",
			Model:    "gpt-5",
		}}},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:           runDir,
		HubStateRoot:     t.TempDir(),
		LaunchConfigRoot: launchRoot,
		Spawner:          spawner,
		Past:             hubcore.NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	if _, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		CWD: t.TempDir(),
	}); err != nil {
		t.Fatalf("ThreadStart should accept env-model spawn: %v", err)
	}
	if got.Resolved.Effective.Model != "openai/gpt-5" {
		t.Errorf("Model = %q, want env model threaded into spawn", got.Resolved.Effective.Model)
	}
}

func TestHubRPCThreadStartEmptyModelRejected(t *testing.T) {
	// The counterpart to TestHubRPCThreadStartEnvModelSatisfiesRequiredGate:
	// with no model in any layer, no EVENER_MODEL env, and no per-launch
	// override, the spawn gate rejects with "model is required". This guards
	// the branch the env path exists to satisfy.
	t.Setenv("EVENER_MODEL", "") // ensure ambient env cannot leak a model in
	runDir := t.TempDir()
	launchRoot := t.TempDir()
	spawner := &fakeRPCModelContractSpawner{
		spawn: func(_ context.Context, _ hubcore.SpawnRequest) (rendezvous.Entry, error) {
			t.Fatal("spawner should not be called when model is empty")
			return rendezvous.Entry{}, nil
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:           runDir,
		HubStateRoot:     t.TempDir(),
		LaunchConfigRoot: launchRoot,
		Spawner:          spawner,
		Past:             hubcore.NewPastIndex(""),
	})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	_, err := appwiretest.ThreadStart(context.Background(), client, appwire.ThreadStartParams{
		CWD: t.TempDir(),
	})
	if err == nil {
		t.Fatal("ThreadStart should fail when model resolves to empty")
	}
	if wire, _ := wireErrorData(t, err); wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("error = %v, want the invalid-params refusal for a missing model", err)
	}
}
