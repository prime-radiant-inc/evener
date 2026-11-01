package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
)

type relayLifecycleSource struct {
	thread   appwire.Thread
	canceled chan struct{}
}

type pastFallbackRelaySource struct {
	relayLifecycleSource
	readErr error
}

func (s *pastFallbackRelaySource) ID() string {
	return "local"
}

func (s *pastFallbackRelaySource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{}, s.readErr
}

func (s *relayLifecycleSource) ID() string { return "codex" }

func (s *relayLifecycleSource) ListTurns(context.Context, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	return appwire.ThreadTurnsListResponse{}, nil
}

func (s *relayLifecycleSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	return appwire.ThreadListResponse{Data: []appwire.Thread{s.thread}}, nil
}

func (s *relayLifecycleSource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{Thread: s.thread}, nil
}

func (s *relayLifecycleSource) StartThread(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	return appwire.ThreadStartResponse{}, appwire.Unavailable("relay lifecycle source does not start threads")
}

func (s *relayLifecycleSource) ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	return appwire.ThreadResumeResponse{}, appwire.Unavailable("relay lifecycle source does not resume threads")
}

func (s *relayLifecycleSource) ForkThread(context.Context, appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	return appwire.ThreadForkResponse{}, appwire.Unavailable("relay lifecycle source does not fork threads")
}

func (s *relayLifecycleSource) StartTurn(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	return appwire.TurnStartResponse{}, appwire.Unavailable("relay lifecycle source does not start turns")
}

func (s *relayLifecycleSource) SteerTurn(context.Context, appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
	return appwire.TurnSteerResponse{}, appwire.Unavailable("relay lifecycle source does not steer turns")
}

func (s *relayLifecycleSource) ResolveSandboxEscalation(context.Context, appwire.SandboxEscalationResolveParams) error {
	return appwire.Unavailable("relay lifecycle source does not resolve escalations")
}

func (s *relayLifecycleSource) InterruptTurn(context.Context, appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
	return appwire.TurnInterruptResponse{}, appwire.Unavailable("relay lifecycle source does not interrupt turns")
}

func (s *relayLifecycleSource) QueueTurn(context.Context, appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
	return appwire.TurnQueueResponse{}, appwire.Unavailable("relay lifecycle source does not queue turns")
}

func (s *relayLifecycleSource) DrainAsSteer(context.Context, appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
	return appwire.TurnDrainAsSteerResponse{}, appwire.Unavailable("relay lifecycle source does not drain as steer")
}

func (s *relayLifecycleSource) PromoteQueuedAsSteer(context.Context, appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
	return appwire.TurnPromoteQueuedAsSteerResponse{}, appwire.Unavailable("relay lifecycle source does not promote queued messages")
}

func (s *relayLifecycleSource) CancelQueued(context.Context, appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
	return appwire.TurnCancelQueuedResponse{}, appwire.Unavailable("relay lifecycle source does not cancel queued messages")
}

func (s *relayLifecycleSource) CompactThread(context.Context, appwire.ThreadCompactStartParams) error {
	return appwire.Unavailable("relay lifecycle source does not compact threads")
}

func (s *relayLifecycleSource) ShutdownThread(context.Context, appwire.ThreadShutdownParams) error {
	return appwire.Unavailable("relay lifecycle source does not shut down threads")
}

func (s *relayLifecycleSource) SetThreadModel(context.Context, appwire.ThreadModelSetParams) error {
	return appwire.Unavailable("relay lifecycle source does not set models")
}

func (s *relayLifecycleSource) SetThreadVisionModel(context.Context, appwire.ThreadVisionModelSetParams) error {
	return appwire.Unavailable("relay lifecycle source does not set vision models")
}

func (s *relayLifecycleSource) SetThreadName(context.Context, appwire.ThreadNameSetParams) error {
	return appwire.Unavailable("relay lifecycle source does not set names")
}

func (s *relayLifecycleSource) SetThreadReasoningEffort(context.Context, appwire.ThreadReasoningEffortSetParams) error {
	return appwire.Unavailable("relay lifecycle source does not set reasoning effort")
}

func (s *relayLifecycleSource) GoalSet(context.Context, appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
	return appwire.GoalSetResponse{}, appwire.Unavailable("relay lifecycle source does not set goals")
}

func (s *relayLifecycleSource) NotesHumanSet(context.Context, appwire.NotesHumanSetParams) (appwire.NotesHumanSetResponse, error) {
	return appwire.NotesHumanSetResponse{}, appwire.Unavailable("relay lifecycle source does not set notes")
}

func (s *relayLifecycleSource) UrlsRemove(context.Context, appwire.UrlsRemoveParams) (appwire.UrlsRemoveResponse, error) {
	return appwire.UrlsRemoveResponse{}, appwire.Unavailable("relay lifecycle source does not remove urls")
}

func (s *relayLifecycleSource) ClearThread(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	return appwire.ThreadClearResponse{}, appwire.Unavailable("relay lifecycle source does not clear threads")
}

func (s *relayLifecycleSource) ListModels(context.Context, appwire.ModelListParams) (appwire.ModelListResponse, error) {
	return appwire.ModelListResponse{}, appwire.Unavailable("relay lifecycle source does not list models")
}

func (s *relayLifecycleSource) ListTasks(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
	return appwire.TaskListResponse{}, appwire.Unavailable("relay lifecycle source does not list tasks")
}

func (s *relayLifecycleSource) ListJobs(context.Context, appwire.JobsListParams) (appwire.JobsListResponse, error) {
	return appwire.JobsListResponse{}, appwire.Unavailable("relay lifecycle source does not list jobs")
}

func (s *relayLifecycleSource) JobOutput(context.Context, appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
	return appwire.JobsOutputResponse{}, appwire.Unavailable("relay lifecycle source does not read job output")
}

func (s *relayLifecycleSource) JobGet(context.Context, appwire.JobsGetParams) (appwire.JobsGetResponse, error) {
	return appwire.JobsGetResponse{}, appwire.Unavailable("relay lifecycle source does not get jobs")
}

func (s *relayLifecycleSource) SubscribeThread(ctx context.Context, _ appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	out := make(chan appwire.Notification)
	go func() {
		defer close(out)
		<-ctx.Done()
		close(s.canceled)
	}()
	return out, nil
}

type relayBroadcastSource struct {
	relayLifecycleSource
	id            string
	thread        appwire.Thread
	notifications chan appwire.Notification
	subscribed    chan struct{}
	canceled      chan struct{}
}

func inputTextForTest(input []appwire.InputItem) string {
	for _, item := range input {
		if item.Text != "" {
			return item.Text
		}
	}
	return ""
}

func (s *relayBroadcastSource) ID() string {
	if s.id != "" {
		return s.id
	}
	return "codex"
}

func (s *relayBroadcastSource) ListTurns(context.Context, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	return appwire.ThreadTurnsListResponse{}, nil
}

func (s *relayBroadcastSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	return appwire.ThreadListResponse{Data: []appwire.Thread{s.thread}}, nil
}

func (s *relayBroadcastSource) ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	return appwire.ThreadReadResponse{Thread: s.thread}, nil
}

func (s *relayBroadcastSource) SubscribeThread(ctx context.Context, _ appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	out := make(chan appwire.Notification, 4)
	if s.subscribed != nil {
		s.subscribed <- struct{}{}
	}
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				select {
				case s.canceled <- struct{}{}:
				default:
				}
				return
			case notification := <-s.notifications:
				select {
				case out <- notification:
				case <-ctx.Done():
					select {
					case s.canceled <- struct{}{}:
					default:
					}
					return
				}
			}
		}
	}()
	return out, nil
}

type startResumeRelaySource struct {
	relayBroadcastSource
}

func (s *startResumeRelaySource) StartThread(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	return appwire.ThreadStartResponse{
		Thread: s.thread,
		Turn:   appwire.Turn{ID: "turn_start"},
	}, nil
}

func (s *startResumeRelaySource) ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	return appwire.ThreadResumeResponse{Thread: s.thread}, nil
}

func expectRelaySubscription(t *testing.T, subscribed <-chan struct{}) {
	t.Helper()
	select {
	case <-subscribed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relay subscription")
	}
}

func testHubSubscribedReadReplacedOwner(t *testing.T, sameEndpoint bool, method string, replacement http.HandlerFunc) {
	root := t.TempDir()
	sessionID := buildRPCParentSession(t, filepath.Join(root, "projects", "upgrade-0000000000"))
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))
	daemon := appserver.NewServer(appserver.ServerConfig{ServerName: "daemon", SourceID: "local"})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadList, func(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{Data: []appwire.Thread{{ID: sessionID, SessionID: sessionID, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}}}}, nil
	})
	appserver.HandleTyped(daemon.Router(), appwire.MethodThreadRead, func(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{ID: sessionID, SessionID: sessionID, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}}}, nil
	})
	var handlerMu sync.RWMutex
	serve := http.HandlerFunc(daemon.ServeWebSocket)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerMu.RLock()
		handler := serve
		handlerMu.RUnlock()
		handler(w, r)
	}))
	defer peer.Close()
	runDir := t.TempDir()
	entry := rendezvous.Entry{PID: os.Getpid(), Protocol: appwire.ProtocolVersion, Endpoint: "ws" + strings.TrimPrefix(peer.URL, "http"), SourceID: "local", ThreadID: sessionID, SessionID: sessionID}
	writeRendezvous(t, runDir, entry)
	roster := liveClaimRoster(runDir, &hubcore.StatusProber{})
	roster.Refresh()
	if !roster.HasConfirmedEntry(entry) {
		t.Fatal("owner not confirmed")
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: runDir, Roster: roster, Past: past})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	entry.Protocol = "evener-appwire-v4"
	if sameEndpoint {
		handlerMu.Lock()
		serve = replacement
		handlerMu.Unlock()
	} else {
		peer.Close()
		entry.Endpoint = protocolMismatchPeer(t)
	}
	writeRendezvous(t, runDir, entry)
	if method != appwire.MethodThreadRead {
		params := map[string]any{"ref": "local:" + sessionID}
		switch method {
		case appwire.MethodTurnStart:
			params["clientMutationId"] = "replacement-send"
			params["expectedInstanceId"] = sessionID
			params["input"] = []appwire.InputItem{{Type: "text", Text: "keep this message"}}
		case appwire.MethodThreadModelSet:
			params["modelProvider"], params["model"] = "test", "test-model"
		case appwire.MethodThreadVisionModelSet:
			params["visionModel"] = "test/vision"
		case appwire.MethodGoalSet:
			params["objective"] = "test goal"
		}
		var response any
		err := client.Request(context.Background(), method, params, &response)
		if !isDaemonRestartRequiredError(err) {
			t.Fatalf("error=%v", err)
		}
		if method == appwire.MethodTurnStart {
			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatal(err)
			}
			data, ok := wire.Data.(map[string]any)
			if !ok || data["clientMutationId"] != "replacement-send" || data["mutationOutcome"] != "unknown" || data["retryDisposition"] != "blocked" {
				t.Fatalf("mutation outcome=%+v", wire.Data)
			}
		}
		return
	}
	response, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true, Subscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	if response.Thread.Status.Type != appwire.ThreadStatusRestartRequired {
		t.Fatalf("status=%s", response.Thread.Status.Type)
	}
	if response.Thread.Evener.Capabilities.Send || len(response.Thread.Turns) != 2 {
		t.Fatalf("thread=%+v", response.Thread)
	}
}

type fakeRPCSpawner struct {
	spawn        func(context.Context, hubcore.SpawnRequest) (rendezvous.Entry, error)
	resume       func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error)
	launchModels func(context.Context) ([]appwire.ModelDescriptor, error)
}

type failedRPCProber struct{}

func (failedRPCProber) Probe(rendezvous.Entry) hubcore.ProbeResult {
	return hubcore.ProbeResult{}
}

type fakeRPCModelContractSpawner struct {
	fakeRPCSpawner
	contract appwire.ModelListResponse
	err      error
}

func (f *fakeRPCModelContractSpawner) ListLaunchModelContract(context.Context) (appwire.ModelListResponse, error) {
	if f.err != nil {
		return appwire.ModelListResponse{}, f.err
	}
	return f.contract, nil
}

func (f *fakeRPCSpawner) Spawn(ctx context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
	if f.spawn != nil {
		return f.spawn(ctx, req)
	}
	return rendezvous.Entry{}, appwire.Unavailable("spawn not configured")
}

func (f *fakeRPCSpawner) Resume(ctx context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
	if f.resume != nil {
		return f.resume(ctx, req)
	}
	return rendezvous.Entry{}, appwire.Unavailable("resume not configured")
}

func (f *fakeRPCSpawner) ListLaunchModels(ctx context.Context) ([]appwire.ModelDescriptor, error) {
	if f.launchModels != nil {
		return f.launchModels(ctx)
	}
	return nil, appwire.Unavailable("launch model contract not configured")
}

func buildRPCParentSession(t *testing.T, stateDir string) string {
	t.Helper()
	return buildRPCParentSessionWithWorkingDir(t, stateDir, t.TempDir())
}

func buildRPCParentSessionWithWorkingDir(t *testing.T, stateDir, workingDir string) string {
	t.Helper()
	return buildRPCSessionWithWorkingDir(t, stateDir, "02wMz5Txv1C3Hut0M8GCeB", workingDir)
}

func buildRPCSessionWithWorkingDir(t *testing.T, stateDir, parentID, workingDir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	writer, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", parentID+".transcript.jsonl"), transcript.Header{
		SessionID:  parentID,
		CreatedAt:  time.Now().UTC(),
		ProfileID:  "openai",
		Model:      "gpt-5",
		WorkingDir: workingDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first task")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("first reply")),
		schema.NewTurn(schema.TurnUserInput, llm.User("second task")),
	} {
		if err := writer.Append(turn); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	indexPastTranscriptForTest(t, stateDir, parentID)
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:             parentID,
		ProfileID:      "openai",
		Model:          "gpt-5",
		EnvInfo:        schema.EnvironmentInfo{WorkingDir: workingDir},
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		TurnCount:      2,
		OriginalPrompt: "second task",
	}); err != nil {
		t.Fatal(err)
	}
	return parentID
}

func dialHubRPC(t *testing.T, hub *httptest.Server) *appwire.Client {
	t.Helper()
	transport, err := appwire.DialWebSocket(context.Background(), "ws"+hub.URL[len("http"):]+"/rpc", hub.Client())
	if err != nil {
		t.Fatalf("dial hub rpc: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.Background())
	return client
}

// startDaemonRoster serves a test daemon app server over WebSocket, registers
// it as the local source's live rendezvous entry in a fresh run directory,
// and returns the daemon's HTTP server, the roster refreshed over that
// directory, and the directory itself. The HTTP server closes at test end.
func startDaemonRoster(t *testing.T, daemon *appserver.Server, pid int, threadID, sessionID string) (*httptest.Server, *hubcore.Roster, string) {
	t.Helper()
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.ServeWebSocket))
	t.Cleanup(daemonHTTP.Close)
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       pid,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws" + daemonHTTP.URL[len("http"):],
		SourceID:  "local",
		ThreadID:  threadID,
		SessionID: sessionID,
	})
	roster := hubcore.NewRoster(runDir, nil)
	roster.Refresh()
	return daemonHTTP, roster, runDir
}

func newHubRPCTestServer(t *testing.T, cfg hubcore.WebConfig) *httptest.Server {
	t.Helper()
	srv, _ := newHubRPCTestServerWithWeb(t, cfg)
	return srv
}

// newTestCredentialsStore loads a credentials store over a file of this test's
// own, so writes through evener/auth/apiKey never reach the developer's
// credentials.toml or another test's.
func newTestCredentialsStore(t *testing.T) *credentials.Store {
	t.Helper()
	credsPath := filepath.Join(t.TempDir(), "credentials.toml")
	store, err := credentials.LoadStore(credsPath)
	if err != nil {
		t.Fatalf("LoadStore(%s): %v", credsPath, err)
	}
	return store
}

// newHubRPCTestServerWithWeb behaves like newHubRPCTestServer but also
// returns the constructed *WebServer, for tests that need to wire an
// onChange hook on one of its cfg stores (e.g. past.SetOnChange, mirroring
// runMain's composed evener/tree/changed wiring in main.go) before the server
// starts serving requests.
// rosterOverRunDir is the roster a test hub gets when it names a rendezvous
// directory and no roster: the production shape (main.go wires one over the
// run dir with the status prober), refreshed once so the files already there
// are listed. A test that adds daemons later refreshes it again.
func rosterOverRunDir(t *testing.T, runDir string) *hubcore.Roster {
	t.Helper()
	roster := hubcore.NewRoster(runDir, &hubcore.StatusProber{})
	roster.Refresh()
	return roster
}

func newHubRPCTestServerWithWeb(t *testing.T, cfg hubcore.WebConfig) (*httptest.Server, *WebServer) {
	t.Helper()
	if cfg.Roster == nil && cfg.RunDir != "" {
		cfg.Roster = rosterOverRunDir(t, cfg.RunDir)
	}
	defaultTestRoots(t, &cfg)
	// credentials.toml gets the same treatment: the auth controller keeps its
	// OAuth records in the registry's state root, but the store it writes
	// keys to follows no root, so without a default parallel tests calling
	// evener/auth/apiKey/set or apiKey/clear would share the process-wide
	// XDG-derived store and overwrite each other's keys. CredentialsPath is
	// the same file, as in production (main.go loads the store from
	// cmdutil.CredentialsPath and hands children that path).
	if cfg.CredsStore == nil {
		// A caller that brings its own registry has already chosen where that
		// registry resolves credentials from, and a store minted here would
		// not be it: evener/auth/apiKey/set would write a file the registry
		// never reads, so Reload and every status answer would keep reporting
		// the credential the registry's own source holds.
		if cfg.Registry != nil {
			t.Fatalf("newHubRPCTestServerWithWeb: an injected Registry must come with the CredsStore it resolves from; without it evener/auth/apiKey/set writes a store the registry never reads")
		}
		cfg.CredsStore = newTestCredentialsStore(t)
	}
	if cfg.CredentialsPath == "" {
		cfg.CredentialsPath = cfg.CredsStore.Path()
	}
	if cfg.Registry == nil {
		// Every auth and instance answer comes from the registry, so a hub
		// fixture without one answers nothing. newTestRegistry loads it
		// straight from this server's own store and state root - offline,
		// uncached, no user layer and no ambient environment - so the store
		// the auth handlers write is the one the registry resolves from,
		// stray OAuth records come from this server's state root, and nothing
		// reaches the developer's providers.toml or credentials.toml.
		// cmdutil.LoadRegistry will not do: it loads the process-wide
		// credentials.toml before any caller option applies, so the fixture
		// failed whenever that file was malformed, had loose permissions or
		// was rewritten by another test.
		cfg.Registry = newTestRegistry(t, cfg.HubStateRoot, "", cfg.CredsStore, nil)
	}
	// The past index's sessions get their first transcript-index sidecar built
	// here, once (see indexPastTranscriptForTest). Seeders own this for the
	// sessions they write; this loop is the safety net for a test that seeds
	// its transcript some other way, so its tests read a session's content
	// rather than the first index of it.
	if cfg.Past != nil {
		for _, entry := range cfg.Past.All() {
			indexPastTranscriptForTest(t, entry.StateDir, entry.Meta.ID)
		}
	}
	return startHubRPCTestServer(t, cfg)
}

// startHubRPCTestServer serves cfg exactly as given, with none of
// newHubRPCTestServerWithWeb's per-test defaults. It is for the rare test
// whose subject is an unset root: an empty LaunchConfigRoot means "none" to
// the handlers, and the fixture would fill it in.
func startHubRPCTestServer(t *testing.T, cfg hubcore.WebConfig) (*httptest.Server, *WebServer) {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	cfg.HubAddr = srv.Listener.Addr().String()
	web := NewWebServer(cfg)
	srv.Config.Handler = web.Handler()
	srv.Start()
	return srv, web
}

func (s *relayLifecycleSource) ThreadActivityRead(context.Context, appwire.SessionActivityReadParams) (appwire.SessionActivitySummary, error) {
	return appwire.SessionActivitySummary{}, appwire.Unavailable("test source does not read session activity")
}

func (s *relayLifecycleSource) ThreadDelegatesList(context.Context, appwire.SessionActivityListParams) (appwire.SessionDelegatesResponse, error) {
	return appwire.SessionDelegatesResponse{}, appwire.Unavailable("test source does not read session activity")
}

func (s *relayLifecycleSource) ThreadJobsList(context.Context, appwire.SessionActivityListParams) (appwire.SessionJobsResponse, error) {
	return appwire.SessionJobsResponse{}, appwire.Unavailable("test source does not read session activity")
}

func (s *relayLifecycleSource) ThreadWatchesList(context.Context, appwire.SessionActivityListParams) (appwire.SessionWatchesResponse, error) {
	return appwire.SessionWatchesResponse{}, appwire.Unavailable("test source does not read session activity")
}
