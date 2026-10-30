package hub

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
	daemonserver "primeradiant.com/evener/server"
)

// Only the external provider is scripted. Both reads cross the actual runtime
// binding and the hub's existing discovery/read owners.
type managerReadAdapter struct {
	requests []llm.Request
	ref      string
	step     int
	watch    bool
}
type managerNamedAdapter struct {
	*managerReadAdapter
	name string
}

func (a *managerNamedAdapter) Name() string { return a.name }
func (*managerReadAdapter) Name() string    { return "openai" }
func (*managerReadAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
func (a *managerReadAdapter) Complete(_ context.Context, r llm.Request) (llm.Response, error) {
	if len(r.Tools) == 0 {
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "Manager read test"}}}}, nil
	}
	raw, _ := json.Marshal(r)
	var captured llm.Request
	_ = json.Unmarshal(raw, &captured)
	a.requests = append(a.requests, captured)
	a.step++
	if a.watch && a.step == 1 {
		return activityRelayTool("job_watch", map[string]any{"operation": "create", "hub": map[string]any{"ref": a.ref, "events": []string{"status"}, "completion": true}, "intent": "Watch this session's recorded completion"}), nil
	}
	if a.watch {
		return activityRelayTool("communicate", map[string]any{"message": "watch registered", "end_turn": true}), nil
	}
	switch a.step {
	case 1:
		return activityRelayTool("job_list", map[string]any{"hub": map[string]any{"mode": "discovery"}}), nil
	case 2:
		return activityRelayTool("job_status", map[string]any{"hub": map[string]any{"ref": a.ref}}), nil
	default:
		return activityRelayTool("communicate", map[string]any{"message": "read complete", "end_turn": true}), nil
	}
}

type snapshotBoundarySource struct {
	*appsource.LocalDaemonSource
	afterRead func()
}

func (s *snapshotBoundarySource) AcquireRelaySession(ref appwire.Ref) (appsource.RelaySessionRoutePublicationLease, error) {
	lease, err := s.LocalDaemonSource.AcquireRelaySession(ref)
	if err != nil {
		return nil, err
	}
	return &snapshotBoundaryLease{RelaySessionRoutePublicationLease: lease, afterRead: s.afterRead}, nil
}

type snapshotBoundaryLease struct {
	appsource.RelaySessionRoutePublicationLease
	afterRead func()
}

func (l *snapshotBoundaryLease) ReadWithRoutePublication(ctx context.Context, params appwire.ThreadReadParams, routes *appsource.RelayRoutePublication) (appsource.RelayReadResult, error) {
	read, err := l.RelaySessionRoutePublicationLease.ReadWithRoutePublication(ctx, params, routes)
	if err == nil {
		l.afterRead()
	}
	return read, err
}

func TestSessionToolsWatchSnapshotHandoff(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	const ref = "local:manager-workspace"
	const instance = "manager-instance"
	daemon := daemonserver.NewServer(daemonserver.ServerConfig{HubToken: "test-token"})
	defer daemon.Close()
	identity, err := daemonserver.PrepareAppIdentityForRef("local", instance, ref, "")
	if err != nil {
		t.Fatal(err)
	}
	daemon.ReplaceAppIdentity(identity, nil)
	upstream := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
	defer upstream.Close()
	entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: upstream.URL, SourceID: "local", ThreadID: instance, SessionID: instance, WorkspaceRef: ref, InstanceID: instance, HubToken: "test-token"}
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: entry, SessionID: instance, Manager: true}}
	}, upstream.Client())
	sources := appsource.NewRegistry()
	// Change state only after the real upstream snapshot is captured, before
	// its canonical handoff commits or the Manager watch starts listening.
	sources.Add(&snapshotBoundarySource{LocalDaemonSource: source, afterRead: func() {
		daemon.SetProcessingTurn("turn-at-snapshot-boundary")
	}})
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex(""), HubID: "hub-test"}
	server := appserver.NewServer(appserver.ServerConfig{})
	defer server.Shutdown(ctx)
	relays := newHubRelay(server, cfg, sources)
	owner := &sessionOperations{cfg: cfg, sources: sources, relays: relays, server: server}
	appserver.HandleTyped(server.Router(), appwire.MethodThreadRead, owner.Read)
	binding := &boundSessionOperations{owner: owner, local: source, binding: appwire.SessionBinding{Ref: ref, InstanceID: instance, HubID: "hub-test", BindingID: "binding"}}
	parsed, err := appwire.ParseRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	observer, err := source.AcquireRelaySession(parsed)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	notifications, err := observer.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	observed := make(chan struct{}, 1)
	go func() {
		for delivery := range notifications {
			delivery.Acknowledge()
			if delivery.Notification.Method == appwire.NotifyThreadStatusChanged {
				select {
				case observed <- struct{}{}:
				default:
				}
			}
		}
	}()
	updates, err := binding.Watch(ctx, appwire.SessionWatchRequest{Ref: ref, Events: []string{"status"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case update := <-updates:
		if update.Snapshot == nil {
			t.Fatalf("missing initial snapshot: %+v", update)
		}
		if update.Snapshot.Thread.Status.Type == appwire.ThreadStatusActive {
			t.Fatal("initial snapshot already contains the boundary transition")
		}
	case <-ctx.Done():
		t.Fatal("missing initial snapshot")
	}
	select {
	case <-observed:
	case <-ctx.Done():
		t.Fatal("concurrent observer lost transition")
	}
	select {
	case update := <-updates:
		if update.Notification == nil || update.Notification.Method != appwire.NotifyThreadStatusChanged {
			t.Fatalf("missing boundary status: %+v", update)
		}
		var status appwire.ThreadStatusChangedParams
		if err := json.Unmarshal(update.Notification.Params, &status); err != nil || status.Status.Type != appwire.ThreadStatusActive || status.Ref != ref {
			t.Fatalf("boundary status=%+v err=%v", status, err)
		}
	case <-ctx.Done():
		t.Fatal("snapshot-to-listener transition lost without a second transition")
	}
	if n := daemon.AppServer().SubscriberCount(ref); n != 1 {
		t.Fatalf("upstream subscriptions=%d, want one canonical subscription", n)
	}
}

func TestSessionToolsAutomaticBindingLifecycleAndReconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	const ref = "local:manager-workspace"
	const instance = "manager-instance"
	type connection struct {
		ctx     context.Context
		client  appwire.SessionHubClient
		binding appwire.SessionBinding
	}
	bound := make(chan connection, 8)
	daemon := daemonserver.NewServer(daemonserver.ServerConfig{HubToken: "test-token"})
	defer daemon.Close()
	identity, err := daemonserver.PrepareAppIdentityForRef("local", instance, ref, "")
	if err != nil {
		t.Fatal(err)
	}
	daemon.ReplaceAppIdentity(identity, nil)
	daemon.AppServer().InstallSessionTools(func(life context.Context, binding appwire.SessionBinding, client appwire.SessionHubClient) error {
		bound <- connection{life, client, binding}
		return nil
	})
	sockets := make(chan net.Conn, 8)
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
	upstream.Config.ConnState = func(c net.Conn, state http.ConnState) {
		if state == http.StateHijacked {
			sockets <- c
		}
	}
	upstream.Start()
	defer upstream.Close()
	entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: upstream.URL, SourceID: "local", ThreadID: instance, SessionID: instance, WorkspaceRef: ref, InstanceID: instance, HubToken: "test-token"}
	// The roster entry carries the session's own start-time Manager property;
	// there is no hub-side enable to cycle.
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: entry, SessionID: instance, Manager: true}}
	}, upstream.Client())
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex(""), HubID: "hub-test", Roster: hubcore.NewRosterWithEntries(hubcore.LiveEntry{Entry: entry, SessionID: instance, Status: "active", Manager: true})}
	web := NewWebServer(cfg)
	hubServer := newHubAppServerWithNavigation(cfg, sources, web.navigation, web.resolveTopLevelSessionRef)
	hubHTTP := httptest.NewServer(http.HandlerFunc(hubServer.ServeWebSocket))
	defer hubHTTP.Close()
	human := dialHubRPC(t, hubHTTP)
	defer human.Close()
	if _, err := human.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	life, stop := context.WithCancel(ctx)
	defer stop()
	var workers sync.WaitGroup
	startSessionTools(life, sources, func(f func()) { workers.Go(f) })
	next := func() connection {
		t.Helper()
		select {
		case c := <-bound:
			return c
		case <-ctx.Done():
			t.Fatal("automatic binding did not arrive")
			return connection{}
		}
	}
	first := next()
	if first.binding.Ref != ref || first.binding.InstanceID != instance {
		t.Fatalf("discovered binding=%+v", first.binding)
	}
	request := appwire.SessionOperationRequest{Discover: &appwire.ThreadListParams{}}
	response, err := first.client.Invoke(ctx, request)
	if err != nil || response.Discover == nil {
		t.Fatalf("manager discovery=%+v err=%v", response, err)
	}
	for _, watch := range []appwire.SessionWatchRequest{{Ref: ref, Events: []string{"unknown"}}} {
		if _, err := first.client.Watch(ctx, watch); err == nil {
			t.Fatalf("unsupported observation accepted: %+v", watch)
		}
	}
	watchCtx, clear := context.WithCancel(ctx)
	updates, err := first.client.Watch(watchCtx, appwire.SessionWatchRequest{Ref: ref, Events: []string{"status"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case update := <-updates:
		if update.Snapshot == nil || update.Metadata.Ref != ref {
			t.Fatalf("initial observation=%+v", update)
		}
	case <-ctx.Done():
		t.Fatal("no initial observation")
	}
	if n := daemon.AppServer().SubscriberCount(ref); n == 0 {
		t.Fatal("status watch has no upstream subscription")
	}
	daemon.SetProcessingTurn("turn-status")
	select {
	case update := <-updates:
		if update.Notification == nil || update.Notification.Method != appwire.NotifyThreadStatusChanged {
			t.Fatalf("live observation=%+v", update)
		}
	case <-ctx.Done():
		t.Fatal("no live status observation")
	}
	clear()
	select {
	case _, ok := <-updates:
		if ok {
			t.Fatal("cancelled consumer remained open")
		}
	case <-ctx.Done():
		t.Fatal("cancelled observation did not close")
	}
	updates, err = first.client.Watch(ctx, appwire.SessionWatchRequest{Ref: ref, Events: []string{"status"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-updates:
	case <-ctx.Done():
		t.Fatal("no second initial observation")
	}
	// Break the physical websocket without changing discovery identity.
	select {
	case socket := <-sockets:
		_ = socket.Close()
	case <-ctx.Done():
		t.Fatal("no physical websocket")
	}
	select {
	case _, ok := <-updates:
		if ok {
			t.Fatal("disconnected consumer remained open")
		}
	case <-ctx.Done():
		t.Fatal("disconnected observation did not close")
	}
	second := next()
	if second.binding.BindingID == first.binding.BindingID {
		t.Fatal("reconnect reused binding ID")
	}
	if _, err = first.client.State(ctx); err == nil {
		t.Fatal("disconnected binding remains usable")
	}
	if _, err = second.client.State(ctx); err != nil {
		t.Fatalf("reconnected state: %v", err)
	}
	stop()
	drained := make(chan struct{})
	go func() { workers.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-ctx.Done():
		t.Fatal("session-tools shutdown did not drain")
	}
	select {
	case <-second.ctx.Done():
	case <-ctx.Done():
		t.Fatal("shutdown left binding alive")
	}
	// A new hub process reconstructs its sources and binds the same Manager.
	restarted := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: entry, SessionID: instance, Manager: true}}
	}, upstream.Client())
	restartedSources := appsource.NewRegistry()
	restartedSources.Add(restarted)
	_ = newHubAppServerWithNavigation(cfg, restartedSources, web.navigation, web.resolveTopLevelSessionRef)
	restartLife, restartStop := context.WithCancel(ctx)
	defer restartStop()
	var restartWorkers sync.WaitGroup
	startSessionTools(restartLife, restartedSources, func(f func()) { restartWorkers.Go(f) })
	third := next()
	restored, err := third.client.State(ctx)
	if err != nil || !restored.Manager || restored.Ref != ref {
		t.Fatalf("hub restart state=%+v err=%v", restored, err)
	}
	if _, err := third.client.Invoke(ctx, request); err != nil {
		t.Fatalf("restarted binding lost manager discovery: %v", err)
	}
	restartStop()
	restartWorkers.Wait()
}

func TestManagerAdmissionRealToolRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	adapter := &managerReadAdapter{}
	client := llm.NewClient()
	client.Register(adapter)
	client.Register(&managerNamedAdapter{managerReadAdapter: adapter, name: "anthropic"})
	stateDir, workDir := t.TempDir(), t.TempDir()
	session, err := agent.NewSession(client, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), agent.SessionConfig{StateDir: stateDir, Sandbox: "off", Manager: true, ResolveProfile: func(ref string) (*provider.Profile, error) { return provider.Resolve(provider.EmbeddedRegistry(), ref) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	ref := "local:" + session.WorkspaceID()
	adapter.ref = ref
	daemon := daemonserver.NewServer(daemonserver.ServerConfig{HubToken: "test-token"})
	t.Cleanup(daemon.Close)
	identity, err := daemonserver.PrepareAppIdentityForRef("local", session.ID(), ref, session.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	daemon.ReplaceAppIdentity(identity.WithBootGeneration("7@manager"), nil)
	daemon.UpdateSessionInfo(session.ID(), "", "")
	daemon.SetState("idle")
	daemon.SetWorkingDir(workDir)
	daemon.SetSessionTools(func() *agent.Session { return session })
	daemon.WireTranscriptHistory(session)
	sockets := make(chan net.Conn, 8)
	reconnectReady := make(chan struct{})
	var releaseOnce sync.Once
	releaseReconnect := func() { releaseOnce.Do(func() { close(reconnectReady) }) }
	defer releaseReconnect()
	var connections atomic.Int32
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if connections.Add(1) > 1 {
			select {
			case <-reconnectReady:
			case <-r.Context().Done():
				return
			}
		}
		daemon.AppServer().ServeWebSocket(w, r)
	}))
	upstream.Config.ConnState = func(c net.Conn, state http.ConnState) {
		if state == http.StateHijacked {
			sockets <- c
		}
	}
	upstream.Start()
	t.Cleanup(upstream.Close)
	entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: upstream.URL, SourceID: "local", ThreadID: session.ID(), SessionID: session.ID(), WorkspaceRef: ref, InstanceID: session.ID(), HubToken: "test-token", WorkingDir: workDir, StateDir: stateDir}
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: entry, SessionID: session.ID(), Manager: true}}
	}, upstream.Client())
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex(""), HubID: "hub-test", Roster: hubcore.NewRosterWithEntries(hubcore.LiveEntry{Entry: entry, SessionID: session.ID(), Status: "active", Manager: true})}
	web := NewWebServer(cfg)
	_ = newHubAppServerWithNavigation(cfg, sources, web.navigation, web.resolveTopLevelSessionRef)
	// Establish the real canonical relay, not a test implementation of the hub interface.
	parsed, _ := appwire.ParseRef(ref)
	lease, err := source.AcquireRelaySession(parsed)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	read, err := lease.Read(ctx, appwire.ThreadReadParams{Ref: ref, Subscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	if !read.Handoff.Commit() {
		t.Fatal("initial relay snapshot was not committed")
	}
	adapter.step = 0
	adapter.requests = nil
	if _, err = session.ProcessInput(ctx, "discover sessions and read this session", nil); err != nil {
		t.Fatal(err)
	}
	if len(adapter.requests) != 3 {
		t.Fatalf("provider requests=%d, want two tool calls plus final", len(adapter.requests))
	}
	assertManagerRequestSurface(t, adapter.requests[0], true)
	var results []appwire.SessionOperationResponse
	var contents []any
	for _, message := range adapter.requests[2].Messages {
		for _, part := range message.Content {
			if part.ToolResult != nil {
				contents = append(contents, part.ToolResult)
				var response appwire.SessionOperationResponse
				raw, _ := json.Marshal(part.ToolResult.Content)
				if text, ok := part.ToolResult.Content.(string); ok {
					raw = []byte(text)
				}
				if json.Unmarshal(raw, &response) == nil && response.Actor.Kind == "manager" {
					results = append(results, response)
				}
			}
		}
	}
	if len(results) != 2 {
		raw, _ := json.Marshal(contents)
		t.Fatalf("expected actual Manager discovery and status results, got %d: %s", len(results), raw)
	}
	if results[0].Discover == nil || len(results[0].Discover.Sources) != 1 || results[0].Discover.Sources[0].ID != "local" {
		t.Fatalf("discovery=%+v", results[0])
	}
	foundRef := false
	for _, thread := range results[0].Discover.Threads.Data {
		if thread.Evener.Ref == ref && thread.Evener.InstanceID == session.ID() {
			foundRef = true
		}
	}
	if !foundRef {
		t.Fatalf("discovery did not return source-qualified live ref: %+v", results[0].Discover.Threads)
	}
	if results[1].Read == nil || results[1].Read.Thread.Evener.Ref != ref || results[1].Read.Thread.Evener.InstanceID != session.ID() {
		t.Fatalf("status=%+v", results[1])
	}
	if len(results[1].FollowUps) != 1 || results[1].FollowUps[0].Tool != "job_status" || results[1].FollowUps[0].Arguments.Hub.Ref != ref {
		t.Fatalf("status follow-up=%+v", results[1].FollowUps)
	}
	for _, model := range []string{"gpt-5", "anthropic/claude-opus-4-6"} {
		if err = session.SetModel(model); err != nil {
			t.Fatal(err)
		}
		if _, err = session.ProcessInput(ctx, "capture switched request", nil); err != nil {
			t.Fatal(err)
		}
		assertManagerRequestSurface(t, adapter.requests[len(adapter.requests)-1], true)
	}
	// Hold only the next transport handshake while capturing the disconnected
	// request, then let the existing relay reconnect normally.
	select {
	case socket := <-sockets:
		_ = socket.Close()
	case <-ctx.Done():
		t.Fatal("no runtime socket")
	}
	if _, err = session.ProcessInput(ctx, "capture disconnected request", nil); err != nil {
		t.Fatal(err)
	}
	// The start-time property keeps the Manager surface even while the hub
	// transport is down; only the operations fail.
	assertManagerRequestSurface(t, adapter.requests[len(adapter.requests)-1], true)
	releaseReconnect()
	reconnected, err := lease.Read(ctx, appwire.ThreadReadParams{Ref: ref, Subscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reconnected.Handoff.Commit() {
		t.Fatal("reconnect snapshot not committed")
	}
	if _, err = session.ProcessInput(ctx, "capture reconnected request", nil); err != nil {
		t.Fatal(err)
	}
	assertManagerRequestSurface(t, adapter.requests[len(adapter.requests)-1], true)
	adapter.watch, adapter.step = true, 0
	adapter.requests = nil
	if _, err = session.ProcessInput(ctx, "install Manager watch over real connection", nil); err != nil {
		t.Fatal(err)
	}
	if len(adapter.requests) < 2 {
		t.Fatalf("watch provider requests=%d, want tool call and result", len(adapter.requests))
	}
	registered := false
	for _, request := range adapter.requests[1:] {
		for _, message := range request.Messages {
			for _, part := range message.Content {
				if part.ToolResult != nil && part.ToolResult.Name == "job_watch" {
					var payload map[string]any
					if json.Unmarshal(part.ToolResult.ToolState, &payload) == nil {
						id, _ := payload["watch_id"].(string)
						watching, _ := payload["watching"].(bool)
						registered = registered || !part.ToolResult.IsError && id != "" && watching
					}
				}
			}
		}
	}
	if !registered {
		var results []any
		for _, request := range adapter.requests[1:] {
			for _, message := range request.Messages {
				for _, part := range message.Content {
					if part.ToolResult != nil && part.ToolResult.Name == "job_watch" {
						results = append(results, part.ToolResult)
					}
				}
			}
		}
		encoded, _ := json.Marshal(results)
		t.Fatalf("scripted runtime job_watch has no successful registration: %s", encoded)
	}
}

func TestSessionToolsRuntimeIdentityReplacement(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	workDir := t.TempDir()
	newRuntime := func(workspace string) (*agent.Session, *managerReadAdapter) {
		t.Helper()
		adapter := &managerReadAdapter{}
		client := llm.NewClient()
		client.Register(adapter)
		session, err := agent.NewSession(client, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), agent.SessionConfig{WorkspaceID: workspace, StateDir: t.TempDir(), Sandbox: "off", Manager: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(session.Close)
		adapter.ref = "local:" + session.WorkspaceID()
		return session, adapter
	}
	first, firstAdapter := newRuntime("")
	ref := "local:" + first.WorkspaceID()
	var current atomic.Pointer[agent.Session]
	current.Store(first)
	daemon := daemonserver.NewServer(daemonserver.ServerConfig{HubToken: "test-token"})
	defer daemon.Close()
	daemon.SetSessionTools(current.Load)
	setIdentity := func(session *agent.Session) {
		t.Helper()
		identity, err := daemonserver.PrepareAppIdentityForRef("local", session.ID(), ref, session.TranscriptPath())
		if err != nil {
			t.Fatal(err)
		}
		daemon.ReplaceAppIdentity(identity, nil)
		daemon.UpdateSessionInfo(session.ID(), "", "")
		daemon.SetState("idle")
		daemon.SetWorkingDir(workDir)
		daemon.WireTranscriptHistory(session)
	}
	setIdentity(first)
	sockets := make(chan struct{}, 4)
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
	upstream.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateHijacked {
			sockets <- struct{}{}
		}
	}
	upstream.Start()
	defer upstream.Close()
	var entry atomic.Pointer[rendezvous.Entry]
	setEntry := func(session *agent.Session) {
		entry.Store(&rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: upstream.URL, SourceID: "local", ThreadID: session.ID(), SessionID: session.ID(), WorkspaceRef: ref, InstanceID: session.ID(), HubToken: "test-token", WorkingDir: workDir})
	}
	setEntry(first)
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		e := *entry.Load()
		return []appsource.LocalDaemonEntry{{Entry: e, SessionID: e.SessionID, Manager: true}}
	}, upstream.Client())
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex(""), HubID: "hub-test"}
	web := NewWebServer(cfg)
	_ = newHubAppServerWithNavigation(cfg, sources, web.navigation, web.resolveTopLevelSessionRef)
	life, stop := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() { stop(); workers.Wait() }()
	startSessionTools(life, sources, func(f func()) { workers.Go(f) })
	parsed, _ := appwire.ParseRef(ref)
	lease, err := source.AcquireRelaySession(parsed)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	read := func() {
		t.Helper()
		snapshot, err := lease.Read(ctx, appwire.ThreadReadParams{Ref: ref})
		if err != nil {
			t.Fatal(err)
		}
		if !snapshot.Handoff.Commit() {
			t.Fatal("snapshot commit failed")
		}
	}
	read()
	select {
	case <-sockets:
	case <-ctx.Done():
		t.Fatal("initial transport missing")
	}
	if _, err := first.ProcessInput(ctx, "read through original binding", nil); err != nil {
		t.Fatal(err)
	}
	assertManagerRequestSurface(t, firstAdapter.requests[0], true)
	replacement, replacementAdapter := newRuntime(first.WorkspaceID())
	if replacement.ID() == first.ID() {
		t.Fatal("runtime identity was not replaced")
	}
	current.Store(replacement)
	setIdentity(replacement)
	setEntry(replacement)
	// The old runtime keeps its Manager surface: the start-time property never
	// leaves a session. Its binding is superseded below, and the supersede tests
	// cover the admission failure on a dead binding against real sockets.
	if _, err := first.ProcessInput(ctx, "capture obsolete binding", nil); err != nil {
		t.Fatal(err)
	}
	assertManagerRequestSurface(t, firstAdapter.requests[len(firstAdapter.requests)-1], true)
	source.ReconcileSessionTools()
	select {
	case <-sockets:
	case <-ctx.Done():
		t.Fatal("replacement did not reconnect")
	}
	// The canonical owner reconnects with the replacement identity. A read is
	// the handshake completion barrier, not a substituted Manager client.
	read()
	if _, err := replacement.ProcessInput(ctx, "discover and read replacement", nil); err != nil {
		t.Fatal(err)
	}
	assertManagerRequestSurface(t, replacementAdapter.requests[0], true)
	var useful bool
	for _, message := range replacementAdapter.requests[len(replacementAdapter.requests)-1].Messages {
		for _, part := range message.Content {
			if part.ToolResult == nil {
				continue
			}
			text, ok := part.ToolResult.Content.(string)
			if !ok {
				continue
			}
			var response appwire.SessionOperationResponse
			if json.Unmarshal([]byte(text), &response) == nil && response.Read != nil && response.Read.Thread.Evener.InstanceID == replacement.ID() {
				useful = true
			}
		}
	}
	if !useful {
		t.Fatal("replacement did not complete a useful Manager status read")
	}
}

func assertManagerRequestSurface(t *testing.T, request llm.Request, enabled bool) {
	t.Helper()
	found := 0
	control := 0
	for _, definition := range request.Tools {
		if definition.Name == "session_control" {
			control++
			if !enabled {
				t.Fatal("session_control exposed while Manager is disabled")
			}
			properties, ok := definition.Parameters["properties"].(map[string]any)
			if !ok {
				t.Fatal("session_control has no typed properties")
			}
			action, ok := properties["action"].(map[string]any)
			if !ok {
				t.Fatalf("session_control action schema = %#v", properties["action"])
			}
			values, ok := action["enum"].([]any)
			if !ok || len(values) != 20 || values[0] != "approval" || values[19] != "seen" {
				t.Fatalf("session_control actions = %#v, want all 20 human controls", action)
			}
			continue
		}
		if definition.Name != "job_list" && definition.Name != "job_status" {
			continue
		}
		found++
		_, hasHub := definition.Parameters["properties"].(map[string]any)["hub"]
		if hasHub != enabled {
			t.Fatalf("%s hub=%v want %v", definition.Name, hasHub, enabled)
		}
	}
	if found != 2 {
		t.Fatalf("implemented job tools=%d, want 2", found)
	}
	if enabled && control != 1 {
		t.Fatalf("Manager request has %d session_control tools, want one", control)
	}
}
