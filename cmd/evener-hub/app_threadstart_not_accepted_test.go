package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/plugins"
	"primeradiant.com/evener/rendezvous"
)

// wireErrorData is a failed call's wire error and its standard data, as a
// client decodes them.
func wireErrorData(t *testing.T, err error) (appwire.WireError, appwire.ErrorData) {
	t.Helper()
	if err == nil {
		t.Fatal("the call succeeded, want a refusal")
	}
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("refusal %T is not a wire error: %v", err, err)
	}
	raw, marshalErr := json.Marshal(wire.Data)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var data appwire.ErrorData
	if unmarshalErr := json.Unmarshal(raw, &data); unmarshalErr != nil {
		t.Fatalf("refusal data %s: %v", raw, unmarshalErr)
	}
	return wire, data
}

// A thread/start refused before the hub spawns anything says so: no session
// exists, so a client may start that draft again (#3184). The code, message and
// evenerErrorInfo stay what they were, so a client that doesn't read the
// outcome sees the same refusal.
func TestThreadStartRefusalsBeforeTheSpawnSayNotAccepted(t *testing.T) {
	t.Setenv("EVENER_MODEL", "") // no ambient model, so "model is required" is reachable
	cases := []struct {
		name    string
		spawner bool
		params  func(t *testing.T) appwire.ThreadStartParams
		code    int
		info    appwire.ErrorInfo
	}{
		{
			name:    "no model resolves",
			spawner: true,
			params:  func(t *testing.T) appwire.ThreadStartParams { return appwire.ThreadStartParams{CWD: t.TempDir()} },
			code:    appwire.CodeInvalidParams,
			info:    appwire.ErrorInvalidParams,
		},
		{
			name:    "the working directory is missing",
			spawner: true,
			params: func(t *testing.T) appwire.ThreadStartParams {
				return appwire.ThreadStartParams{Model: "openai/gpt-5", CWD: filepath.Join(t.TempDir(), "missing")}
			},
			code: appwire.CodeInvalidParams,
			info: appwire.ErrorInvalidParams,
		},
		{
			name:    "the hub has no spawner",
			spawner: false,
			params: func(t *testing.T) appwire.ThreadStartParams {
				return appwire.ThreadStartParams{Model: "openai/gpt-5", CWD: t.TempDir()}
			},
			code: appwire.CodeUnavailable,
			info: appwire.ErrorActionUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := hubcore.WebConfig{
				RunDir:           t.TempDir(),
				HubStateRoot:     t.TempDir(),
				LaunchConfigRoot: t.TempDir(),
				Past:             hubcore.NewPastIndex(""),
			}
			if tc.spawner {
				cfg.Spawner = &fakeRPCModelContractSpawner{
					spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
						t.Fatal("a refused start must not spawn")
						return rendezvous.Entry{}, nil
					},
				}
			}
			hub := newHubRPCTestServer(t, cfg)
			defer hub.Close()
			client := dialHubRPC(t, hub)
			defer client.Close()
			if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatalf("Initialize: %v", err)
			}

			_, err := client.ThreadStart(context.Background(), tc.params(t))
			wire, data := wireErrorData(t, err)
			if wire.Code != tc.code || data.EvenerErrorInfo != tc.info {
				t.Fatalf("refusal code=%d info=%q, want %d %q: %v", wire.Code, data.EvenerErrorInfo, tc.code, tc.info, err)
			}
			if data.MutationOutcome != appwire.MutationOutcomeNotAccepted {
				t.Fatalf("mutationOutcome=%q, want %q: %v", data.MutationOutcome, appwire.MutationOutcomeNotAccepted, err)
			}
		})
	}
}

// A plain error from a check before the spawn goes out as the internal error it
// always did, marked not accepted like the rest: every refusal the local
// checks make, they make before any session exists.
func TestThreadStartMarksAPlainErrorBeforeTheSpawnNotAccepted(t *testing.T) {
	resolve := hubResolveLaunch
	hubResolveLaunch = func(string, string, launchconfig.Layer) (launchconfig.Resolved, error) {
		return launchconfig.Resolved{}, errors.New("launch.toml: unreadable")
	}
	t.Cleanup(func() { hubResolveLaunch = resolve })
	cfg := hubcore.WebConfig{LaunchConfigRoot: t.TempDir(), Spawner: &recordingSpawner{}}

	_, err := hubThreadStart(context.Background(), cfg, appsource.NewRegistry(), appwire.ThreadStartParams{
		Model: "openai/gpt-5",
		CWD:   t.TempDir(),
	})
	wire, data := wireErrorData(t, err)
	if wire.Code != appwire.CodeInternalError || data.EvenerErrorInfo != appwire.ErrorInternal {
		t.Fatalf("refusal code=%d info=%q, want an internal error: %v", wire.Code, data.EvenerErrorInfo, err)
	}
	if data.MutationOutcome != appwire.MutationOutcomeNotAccepted {
		t.Fatalf("mutationOutcome=%q, want %q: %v", data.MutationOutcome, appwire.MutationOutcomeNotAccepted, err)
	}
}

// A thread/start refused after the spawn doesn't say not accepted: its session
// already exists (the initial input's skill check runs against the spawned
// session's own read), so a client must not start that draft again.
func TestThreadStartRefusalAfterTheSpawnLeavesItsOutcomeOpen(t *testing.T) {
	sessionID := hubtest.SessionID(t)
	daemon, _ := startSkillInputHubDaemon(t, sessionID, appwire.ThreadCapabilities{Send: true}, nil)
	runDir := t.TempDir()
	entry := rendezvous.Entry{
		PID:        404,
		Protocol:   appwire.ProtocolVersion,
		Endpoint:   "ws" + daemon.URL[len("http"):],
		SourceID:   "local",
		ThreadID:   sessionID,
		SessionID:  sessionID,
		WorkingDir: "/tmp",
		Model:      "gpt-5",
	}
	spawned := 0
	spawner := &fakeRPCSpawner{spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
		spawned++
		writeRendezvous(t, runDir, entry)
		return entry, nil
	}}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir:  runDir,
		Roster:  hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusActive}),
		Spawner: spawner,
		Past:    hubcore.NewPastIndex(""),
	})
	defer hub.Close()
	client := dialSkillInputHub(t, hub)
	defer client.Close()

	_, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
		Model: "openai/gpt-5",
		CWD:   "/tmp",
		Input: skillInputHubSelection(),
	})
	wire, data := wireErrorData(t, err)
	if spawned != 1 {
		t.Fatalf("spawned=%d, want the refusal to come after the spawn", spawned)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("refusal code=%d, want invalidParams: %v", wire.Code, err)
	}
	if data.MutationOutcome != "" {
		t.Fatalf("mutationOutcome=%q, want none for a start whose session exists: %v", data.MutationOutcome, err)
	}
}

// refusingStartSource is another host's source that refuses every start.
type refusingStartSource struct{ *scriptedAppSource }

func (s *refusingStartSource) StartThread(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	return appwire.ThreadStartResponse{}, appwire.InvalidParams("the other host refused it")
}

// Every refusal a thread/start meets before the spawn is marked not accepted,
// whichever check makes it, and none of them spawns.
func TestEveryThreadStartRefusalBeforeTheSpawnIsMarkedNotAccepted(t *testing.T) {
	t.Setenv("EVENER_MODEL", "")
	tooManyImages := make([]appwire.InputItem, hubcore.SendMaxImageItems+1)
	for i := range tooManyImages {
		tooManyImages[i] = appwire.InputItem{Type: "image", Name: "shot.png"}
	}
	enabled := []string{"superpowers"}
	cases := []struct {
		name    string
		ctx     func(t *testing.T) context.Context
		sources func() *appsource.Registry
		spawner hubcore.Spawner
		plugins func(context.Context) (plugins.LaunchPluginResolution, error)
		params  appwire.ThreadStartParams
	}{
		{name: "invalid input items", params: appwire.ThreadStartParams{Model: "openai/gpt-5", Input: tooManyImages}},
		{name: "an unknown source", params: appwire.ThreadStartParams{Source: "missing"}},
		{
			name: "a harness naming a host",
			sources: func() *appsource.Registry {
				sources := appsource.NewRegistry()
				sources.Add(&scriptedAppSource{id: "remote-a"})
				return sources
			},
			params: appwire.ThreadStartParams{Harness: "remote-a", CWD: "/tmp"},
		},
		{
			name: "a remote-originated start routed off this hub",
			ctx: func(*testing.T) context.Context {
				return withHostRoutingOrigin(context.Background(), hostRoutingOriginBridge)
			},
			params: appwire.ThreadStartParams{Harness: "not-a-host", CWD: "/tmp"},
		},
		{
			name: "a model the launch harness doesn't report",
			spawner: &fakeRPCModelContractSpawner{contract: appwire.ModelListResponse{
				Data: []appwire.ModelDescriptor{{Provider: "ollama", Model: "local"}},
			}},
			params: appwire.ThreadStartParams{ModelProvider: "anthropic", Model: "claude-test", CWD: "/tmp"},
		},
		{
			name: "a plugin selection that doesn't resolve",
			plugins: func(context.Context) (plugins.LaunchPluginResolution, error) {
				return plugins.LaunchPluginResolution{SelectionErrors: []plugins.PluginSelectionError{{Name: "superpowers", Reason: "not installed"}}}, nil
			},
			params: appwire.ThreadStartParams{Model: "openai/gpt-5", CWD: "/tmp", LaunchOverrides: &appwire.LaunchConfigLayer{EnabledPlugins: &enabled}},
		},
		{
			name: "a caller gone before the spawn",
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			plugins: func(context.Context) (plugins.LaunchPluginResolution, error) {
				return plugins.LaunchPluginResolution{}, nil
			},
			params: appwire.ThreadStartParams{Model: "openai/gpt-5", CWD: "/tmp"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.plugins != nil {
				resolve := hubResolvePlugins
				hubResolvePlugins = func(ctx context.Context, _ string, _ []string, _ *[]string, _ *plugins.Manager) (plugins.LaunchPluginResolution, error) {
					return tc.plugins(ctx)
				}
				t.Cleanup(func() { hubResolvePlugins = resolve })
			}
			ctx := context.Background()
			if tc.ctx != nil {
				ctx = tc.ctx(t)
			}
			sources := appsource.NewRegistry()
			if tc.sources != nil {
				sources = tc.sources()
			}
			spawner := tc.spawner
			if spawner == nil {
				spawner = &fakeRPCSpawner{spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
					t.Fatal("a refused start must not spawn")
					return rendezvous.Entry{}, nil
				}}
			}
			cfg := hubcore.WebConfig{LaunchConfigRoot: t.TempDir(), PluginRoot: t.TempDir(), Spawner: spawner}

			_, err := hubThreadStart(ctx, cfg, sources, tc.params)
			_, data := wireErrorData(t, err)
			if data.MutationOutcome != appwire.MutationOutcomeNotAccepted || data.RetryDisposition != appwire.RetryDispositionNone {
				t.Fatalf("outcome=%q retry=%q, want not accepted with no retry: %v", data.MutationOutcome, data.RetryDisposition, err)
			}
		})
	}
}

// Another host's refusal is that host's to mark: this hub passes it on as it
// came.
func TestThreadStartPassesAnotherHostsRefusalOnUnmarked(t *testing.T) {
	sources := appsource.NewRegistry()
	sources.Add(&refusingStartSource{scriptedAppSource: &scriptedAppSource{id: "remote-a"}})
	_, err := hubThreadStart(context.Background(), hubcore.WebConfig{}, sources, appwire.ThreadStartParams{Source: "remote-a"})
	wire, data := wireErrorData(t, err)
	if wire.Message != "the other host refused it" || data.MutationOutcome != "" {
		t.Fatalf("refusal=%q outcome=%q, want the other host's refusal as it came: %v", wire.Message, data.MutationOutcome, err)
	}
}

// A failure at or after the spawn leaves its outcome open: a session may exist.
func TestThreadStartFailuresFromTheSpawnOnLeaveTheOutcomeOpen(t *testing.T) {
	t.Run("the spawn fails", func(t *testing.T) {
		cfg := hubcore.WebConfig{LaunchConfigRoot: t.TempDir(), PluginRoot: t.TempDir(), Spawner: &fakeRPCSpawner{
			spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
				return rendezvous.Entry{}, errors.New("exec: the daemon exited")
			},
		}}
		_, err := hubThreadStart(context.Background(), cfg, appsource.NewRegistry(), appwire.ThreadStartParams{Model: "openai/gpt-5", CWD: "/tmp"})
		wire, data := wireErrorData(t, err)
		if wire.Code != appwire.CodeUnavailable || data.MutationOutcome != "" {
			t.Fatalf("code=%d outcome=%q, want a launch error with its outcome open: %v", wire.Code, data.MutationOutcome, err)
		}
	})

	t.Run("the roster refresh after the spawn fails", func(t *testing.T) {
		refresh := hubRosterRefresh
		hubRosterRefresh = func(context.Context, *hubcore.Roster) error { return errors.New("roster unreadable") }
		t.Cleanup(func() { hubRosterRefresh = refresh })
		runDir := t.TempDir()
		cfg := hubcore.WebConfig{
			LaunchConfigRoot: t.TempDir(),
			PluginRoot:       t.TempDir(),
			Roster:           hubcore.NewRoster(runDir, fakeProber{}),
			// No endpoint: the spawn can't be used without the roster.
			Spawner: &fakeRPCSpawner{spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
				return rendezvous.Entry{PID: 405}, nil
			}},
		}
		_, err := hubThreadStart(context.Background(), cfg, appsource.NewRegistry(), appwire.ThreadStartParams{Model: "openai/gpt-5", CWD: "/tmp"})
		wire, data := wireErrorData(t, err)
		if wire.Message != "roster unreadable" || data.MutationOutcome != "" {
			t.Fatalf("refusal=%q outcome=%q, want the roster failure with its outcome open: %v", wire.Message, data.MutationOutcome, err)
		}
	})

	t.Run("the initial turn start fails", func(t *testing.T) {
		sessionID := hubtest.SessionID(t)
		app := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
		appserver.HandleTyped(app.Router(), appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
			return appwire.ThreadReadResponse{Thread: appwire.Thread{
				ID: sessionID, SessionID: sessionID, Source: "local",
				Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
				Evener: appwire.EvenerThread{Ref: "local:" + sessionID, Capabilities: appwire.ThreadCapabilities{Send: true}},
			}}, nil
		})
		appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, func(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			return appwire.TurnStartResponse{}, appwire.InvalidParams("the session refused its first turn")
		})
		daemon := httptest.NewServer(http.HandlerFunc(app.ServeWebSocket))
		t.Cleanup(daemon.Close)
		runDir := t.TempDir()
		entry := rendezvous.Entry{
			PID: 406, Protocol: appwire.ProtocolVersion, Endpoint: "ws" + daemon.URL[len("http"):],
			SourceID: "local", ThreadID: sessionID, SessionID: sessionID, WorkingDir: "/tmp", Model: "gpt-5",
		}
		hub := newHubRPCTestServer(t, hubcore.WebConfig{
			RunDir: runDir,
			Roster: hubcore.NewRoster(runDir, fakeProber{sessionID: sessionID, status: appwire.ThreadStatusActive}),
			Spawner: &fakeRPCSpawner{spawn: func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error) {
				writeRendezvous(t, runDir, entry)
				return entry, nil
			}},
			Past: hubcore.NewPastIndex(""),
		})
		defer hub.Close()
		client := dialSkillInputHub(t, hub)
		defer client.Close()

		_, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{
			Model: "openai/gpt-5", CWD: "/tmp",
			Input: []appwire.InputItem{{Type: "text", Text: "go"}},
		})
		wire, data := wireErrorData(t, err)
		if wire.Code != appwire.CodeInvalidParams || data.MutationOutcome != "" {
			t.Fatalf("code=%d outcome=%q, want the turn's refusal with its outcome open: %v", wire.Code, data.MutationOutcome, err)
		}
	})
}
