package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/internal/appserver"
)

const (
	pathological39ID   = "simulator-pathological-39"
	pathological39Ref  = "simulator-scripted:" + pathological39ID
	pathological500ID  = "simulator-variable-500"
	pathological500Ref = "simulator-scripted:" + pathological500ID
	fixtureToken       = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

var (
	ErrPairingURLConsumed = errors.New("fixture pairing URL already consumed")
	ErrStaleGeneration    = errors.New("stale projection generation")
)

type ScriptedRequest struct {
	Method           string
	ThreadID         string
	ClientMutationID string
}

type ScriptedNotification struct {
	Method string
	Params any
}

type ScriptedResponse struct {
	Result        any
	Notifications []ScriptedNotification
}

type SimulatorScriptedProvider interface {
	ProviderName() string
	Next(context.Context, ScriptedRequest) (ScriptedResponse, error)
}

type deterministicSimulatorProvider struct {
	mu            sync.Mutex
	turnSequence  uint64
	notifications []string
	server        *appserver.Server
}

func NewDeterministicSimulatorProvider() *deterministicSimulatorProvider {
	return &deterministicSimulatorProvider{}
}

func (p *deterministicSimulatorProvider) ProviderName() string { return "simulator-scripted" }

func (p *deterministicSimulatorProvider) Next(_ context.Context, request ScriptedRequest) (ScriptedResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.turnSequence++
	turnID := appwire.ClientMutationTurnID(p.turnSequence)
	receipt := appwire.MutationReceipt{
		ClientMutationID: request.ClientMutationID,
		Disposition:      appwire.MutationDispositionApplied,
		ThreadID:         pathological39ID,
		TurnID:           turnID,
		ProjectionState:  appwire.MutationProjectionReflected,
	}
	if request.Method == appwire.MethodTurnQueue {
		receipt.TurnID = ""
		receipt.QueueEntryIDs = []string{fmt.Sprintf("queue-%d", p.turnSequence)}
	}
	notifications := []ScriptedNotification{
		{Method: appwire.NotifyTurnStarted, Params: map[string]any{"threadId": pathological39ID, "turn": map[string]any{"id": turnID, "status": appwire.TurnStatusInProgress}}},
		{Method: appwire.NotifyItemStarted, Params: map[string]any{"threadId": pathological39ID, "turnId": turnID, "item": map[string]any{"id": "stream-agent", "type": "agentMessage", "status": "inProgress"}}},
		{Method: appwire.NotifyAgentMessageDelta, Params: map[string]any{"threadId": pathological39ID, "turnId": turnID, "itemId": "stream-agent", "delta": "scripted stream"}},
		{Method: appwire.NotifyReasoningSummaryDelta, Params: map[string]any{"threadId": pathological39ID, "turnId": turnID, "itemId": "stream-reasoning", "delta": "scripted reasoning"}},
		{Method: appwire.NotifyToolOutputDelta, Params: map[string]any{"threadId": pathological39ID, "turnId": turnID, "itemId": "stream-tool", "delta": "scripted tool output"}},
		{Method: "item/question/created", Params: map[string]any{"threadId": pathological39ID, "turnId": turnID, "question": "Continue?"}},
		{Method: appwire.NotifyItemCompleted, Params: map[string]any{"threadId": pathological39ID, "turnId": turnID, "item": map[string]any{"id": "stream-agent", "type": "agentMessage", "text": "scripted stream", "status": "completed"}}},
		{Method: appwire.NotifyTurnCompleted, Params: map[string]any{"threadId": pathological39ID, "turn": map[string]any{"id": turnID, "status": appwire.TurnStatusCompleted}}},
	}
	for _, notification := range notifications {
		p.notifications = append(p.notifications, notification.Method)
		if p.server != nil {
			p.server.Broadcast(pathological39ID, notification.Method, notification.Params)
		}
	}

	var result any
	switch request.Method {
	case appwire.MethodTurnStart:
		result = appwire.TurnStartResponse{Turn: appwire.Turn{ID: turnID, ItemsView: "full", Status: appwire.TurnStatusInProgress}, Receipt: receipt}
	case appwire.MethodTurnSteer:
		result = appwire.TurnSteerResponse{Receipt: receipt}
	case appwire.MethodTurnQueue:
		result = appwire.TurnQueueResponse{Receipt: receipt}
	case appwire.MethodTurnInterrupt:
		result = appwire.TurnInterruptResponse{Receipt: receipt}
	default:
		return ScriptedResponse{}, appwire.MethodNotFound(request.Method)
	}
	return ScriptedResponse{Result: result, Notifications: notifications}, nil
}

func (p *deterministicSimulatorProvider) notificationMethods() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.notifications...)
}

type PathologicalThread struct {
	ID            string
	Ref           string
	SystemPrelude string
	Items         []appwire.ThreadItem
	Thread        appwire.Thread
}

type SimulatorFixtureHub struct {
	server    *appserver.Server
	provider  *deterministicSimulatorProvider
	stateRoot string
	threads   [2]PathologicalThread

	mu                 sync.Mutex
	pairingURLConsumed bool
	acceptedGeneration uint64
	manifestPath       string
	httpServer         *http.Server
	listener           net.Listener
}

func newSimulatorFixtureHub(stateRoot string) *SimulatorFixtureHub {
	provider := NewDeterministicSimulatorProvider()
	server := appserver.NewServer(appserver.ServerConfig{
		ServerName: "mobile-simulator-fixture",
		Version:    "fixture-v1",
		SourceID:   "simulator-scripted",
		Features: appwire.FeatureSet{
			ThreadList: true, ThreadTurnsList: true, TurnStart: true,
			TurnSteer: true, Tasks: true,
		},
	})
	threads := pathologicalThreads()
	registerSimulatorMethods(server.Router(), provider, threads)
	provider.server = server
	return NewSimulatorFixtureHub(server, provider, stateRoot)
}

func NewSimulatorFixtureHub(server *appserver.Server, provider *deterministicSimulatorProvider, stateRoot string) *SimulatorFixtureHub {
	return &SimulatorFixtureHub{server: server, provider: provider, stateRoot: stateRoot, threads: pathologicalThreads()}
}

func registerSimulatorMethods(router *appserver.Router, provider SimulatorScriptedProvider, threads [2]PathologicalThread) {
	byID := func(id, ref string) (PathologicalThread, bool) {
		for _, thread := range threads {
			if id == thread.ID || ref == thread.Ref {
				return thread, true
			}
		}
		return PathologicalThread{}, false
	}
	appserver.HandleTyped(router, appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{Data: []appwire.Thread{threads[0].Thread, threads[1].Thread}}, nil
	})
	appserver.HandleTyped(router, appwire.MethodThreadRead, func(_ context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		thread, ok := byID(params.ThreadID, params.Ref)
		if !ok {
			return appwire.ThreadReadResponse{}, appwire.InvalidParams("unknown fixture thread")
		}
		return appwire.ThreadReadResponse{Thread: thread.Thread, OlderCursor: olderCursor(thread)}, nil
	})
	appserver.HandleTyped(router, appwire.MethodThreadTurnsList, func(_ context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		thread, ok := byID(params.ThreadID, params.Ref)
		if !ok {
			return appwire.ThreadTurnsListResponse{}, appwire.InvalidParams("unknown fixture thread")
		}
		return appwire.ThreadTurnsListResponse{Data: append([]appwire.Turn(nil), thread.Thread.Turns...)}, nil
	})
	appserver.HandleTyped(router, appwire.MethodTurnStart, func(ctx context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		response, err := provider.Next(ctx, ScriptedRequest{Method: appwire.MethodTurnStart, ThreadID: params.ThreadID, ClientMutationID: params.ClientMutationID})
		if err != nil {
			return appwire.TurnStartResponse{}, err
		}
		return response.Result.(appwire.TurnStartResponse), nil
	})
	appserver.HandleTyped(router, appwire.MethodTurnSteer, func(ctx context.Context, params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
		response, err := provider.Next(ctx, ScriptedRequest{Method: appwire.MethodTurnSteer, ThreadID: params.ThreadID, ClientMutationID: params.ClientMutationID})
		if err != nil {
			return appwire.TurnSteerResponse{}, err
		}
		return response.Result.(appwire.TurnSteerResponse), nil
	})
	appserver.HandleTyped(router, appwire.MethodTurnQueue, func(ctx context.Context, params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
		response, err := provider.Next(ctx, ScriptedRequest{Method: appwire.MethodTurnQueue, ThreadID: pathological39ID, ClientMutationID: params.ClientMutationID})
		if err != nil {
			return appwire.TurnQueueResponse{}, err
		}
		return response.Result.(appwire.TurnQueueResponse), nil
	})
	appserver.HandleTyped(router, appwire.MethodTurnInterrupt, func(ctx context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
		response, err := provider.Next(ctx, ScriptedRequest{Method: appwire.MethodTurnInterrupt, ThreadID: params.ThreadID, ClientMutationID: params.ClientMutationID})
		if err != nil {
			return appwire.TurnInterruptResponse{}, err
		}
		return response.Result.(appwire.TurnInterruptResponse), nil
	})
	appserver.HandleTyped(router, appwire.MethodEvenerTasksList, func(_ context.Context, _ appwire.TaskListParams) (appwire.TaskListResponse, error) {
		return appwire.TaskListResponse{Data: []map[string]any{{"id": 1, "description": "Fixture task", "status": "in_progress"}}}, nil
	})
	appserver.HandleTyped(router, appwire.MethodEvenerJobsList, func(_ context.Context, _ json.RawMessage) (appwire.JobsListResponse, error) {
		return appwire.JobsListResponse{Data: []map[string]any{{"id": "fixture-job", "status": "completed"}}}, nil
	})
	appserver.HandleTyped(router, appwire.MethodEvenerMobilePairing, func(_ context.Context, params appwire.MobilePairingParams) (appwire.MobilePairingResponse, error) {
		return appwire.MobilePairingResponse{AuthURL: hubedge.AuthURLFor(params.Origin, fixtureToken)}, nil
	})
}

func olderCursor(thread PathologicalThread) string {
	if len(thread.Items) > 48 {
		return "fixture-older"
	}
	return ""
}

func pathologicalThreads() [2]PathologicalThread {
	return [2]PathologicalThread{makePathologicalThread(39, pathological39ID, pathological39Ref), makePathologicalThread(500, pathological500ID, pathological500Ref)}
}

func makePathologicalThread(count int, id, ref string) PathologicalThread {
	prelude := strings.Repeat("S", 44_700)
	items := make([]appwire.ThreadItem, 0, count)
	items = append(items, appwire.ThreadItem{Type: "systemMessage", ID: id + "-system", TurnID: appwire.SystemPreludeTurnID, Text: prelude, EventKind: appwire.ThreadItemEventKindSystemPrompt})
	for i := 1; i < count; i++ {
		typeName := "agentMessage"
		text := fmt.Sprintf("Fixture response %03d %s", i, strings.Repeat("variable-height ", i%7))
		if i%4 == 0 {
			typeName = "userMessage"
			text = fmt.Sprintf("Fixture user message %03d", i)
		}
		if i%9 == 0 {
			typeName = "reasoning"
			text = fmt.Sprintf("Fixture reasoning %03d", i)
		}
		if i%13 == 0 {
			typeName = "commandExecution"
			text = ""
		}
		item := appwire.ThreadItem{Type: typeName, ID: fmt.Sprintf("%s-item-%03d", id, i), TurnID: fmt.Sprintf("turn_%d", i), Text: text, Status: "completed", TranscriptEntryIndex: i}
		if typeName == "commandExecution" {
			item.ToolName = "read_file"
			item.Output = fmt.Sprintf("safe fixture output %03d", i)
			item.Description = "Read fixture evidence"
		}
		items = append(items, item)
	}
	turns := make([]appwire.Turn, 0, len(items))
	for _, item := range items {
		turnID := item.TurnID
		turns = append(turns, appwire.Turn{ID: turnID, Items: []appwire.ThreadItem{item}, ItemsView: "full", Status: appwire.TurnStatusCompleted})
	}
	now := int64(1_800_000_000_000)
	thread := appwire.Thread{
		ID: id, SessionID: id, ProjectID: "simulator-fixture-project", ProjectPath: "/fixture/project",
		Preview: fmt.Sprintf("Simulator fixture with %d items", count), ModelProvider: "simulator-scripted",
		CreatedAt: now, UpdatedAt: now, Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
		CWD: "/fixture/project", CLIVersion: "fixture", Source: "simulator-scripted", Name: fmt.Sprintf("Pathological %d", count),
		Turns: turns, Evener: appwire.EvenerThread{Ref: ref, Kind: "session", Profile: "simulator-scripted", Capabilities: appwire.ThreadCapabilities{Send: true, Steer: true, Interrupt: true, Queue: true}},
	}
	return PathologicalThread{ID: id, Ref: ref, SystemPrelude: prelude, Items: items, Thread: thread}
}

func (h *SimulatorFixtureHub) RegisteredMethods() []string {
	methods := h.server.Router().Methods()
	order := map[string]int{}
	for i, method := range []string{appwire.MethodInitialize, appwire.MethodThreadList, appwire.MethodThreadRead, appwire.MethodThreadTurnsList, appwire.MethodTurnStart, appwire.MethodTurnSteer, appwire.MethodTurnQueue, appwire.MethodTurnInterrupt, appwire.MethodEvenerTasksList, appwire.MethodEvenerJobsList, appwire.MethodEvenerMobilePairing} {
		order[method] = i
	}
	sort.Slice(methods, func(i, j int) bool { return order[methods[i]] < order[methods[j]] })
	return methods
}

func (h *SimulatorFixtureHub) ProviderName() string { return h.provider.ProviderName() }
func (h *SimulatorFixtureHub) PathologicalThreads() (PathologicalThread, PathologicalThread) {
	return h.threads[0], h.threads[1]
}
func (h *SimulatorFixtureHub) ReadLiveProviderEnvironment() bool { return false }
func (h *SimulatorFixtureHub) NotificationMethods() []string     { return h.provider.notificationMethods() }

func (h *SimulatorFixtureHub) ConsumePairingURL(origin string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pairingURLConsumed {
		return "", ErrPairingURLConsumed
	}
	h.pairingURLConsumed = true
	return hubedge.AuthURLFor(origin, fixtureToken), nil
}

func (h *SimulatorFixtureHub) AcceptProjection(generation uint64, threadID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if generation <= h.acceptedGeneration {
		return ErrStaleGeneration
	}
	if threadID != pathological39ID && threadID != pathological500ID {
		return errors.New("unknown fixture thread")
	}
	h.acceptedGeneration = generation
	return nil
}

func (h *SimulatorFixtureHub) AcceptedGeneration() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.acceptedGeneration
}

func (h *SimulatorFixtureHub) Dispatch(ctx context.Context, method string, params any) (any, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return h.server.Router().Dispatch(ctx, appwire.Request{ID: appwire.NewIntID(1), Method: method, Params: raw})
}

type SimulatorReadyManifest struct {
	Status                 string `json:"status"`
	Provider               string `json:"provider"`
	Origin                 string `json:"origin,omitempty"`
	AuthURL                string `json:"authUrl,omitempty"`
	Pathological39Items    int    `json:"pathological39Items"`
	Pathological500Items   int    `json:"pathological500Items"`
	SystemPreludeUTF8Bytes int    `json:"systemPreludeUtf8Bytes"`
}

func (h *SimulatorFixtureHub) WriteReadyManifest(path string, manifest SimulatorReadyManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	h.mu.Lock()
	h.manifestPath = path
	h.mu.Unlock()
	return nil
}

func (h *SimulatorFixtureHub) Start() (SimulatorReadyManifest, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return SimulatorReadyManifest{}, err
	}
	origin := "http://" + listener.Addr().String()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"mobile_api_version":1}`))
	})
	mux.HandleFunc("/auth", hubedge.HandleAuth(fixtureToken))
	mux.HandleFunc("/rpc", h.server.ServeWebSocket)
	h.httpServer = &http.Server{Handler: hubedge.AuthGuard(fixtureToken)(mux), ReadHeaderTimeout: 5 * time.Second}
	h.listener = listener
	go func() { _ = h.httpServer.Serve(listener) }()
	return SimulatorReadyManifest{Status: "ready", Provider: h.ProviderName(), Origin: origin, AuthURL: hubedge.AuthURLFor(origin, fixtureToken), Pathological39Items: 39, Pathological500Items: 500, SystemPreludeUTF8Bytes: 44_700}, nil
}

func (h *SimulatorFixtureHub) Shutdown(ctx context.Context) error {
	h.mu.Lock()
	manifestPath := h.manifestPath
	h.manifestPath = ""
	h.mu.Unlock()
	var shutdownErr error
	if h.httpServer != nil {
		shutdownErr = h.httpServer.Shutdown(ctx)
	} else if h.listener != nil {
		shutdownErr = h.listener.Close()
	}
	if manifestPath != "" {
		if err := os.Remove(manifestPath); err != nil && !errors.Is(err, os.ErrNotExist) && shutdownErr == nil {
			shutdownErr = err
		}
	}
	return shutdownErr
}

func main() {
	outputDir := flag.String("output-dir", "", "absolute fixture output directory")
	flag.Parse()
	if *outputDir == "" || !filepath.IsAbs(*outputDir) {
		fmt.Fprintln(os.Stderr, "--output-dir must be absolute")
		os.Exit(2)
	}
	hub := newSimulatorFixtureHub(*outputDir)
	manifest, err := hub.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	manifestPath := filepath.Join(*outputDir, "fixture-hub-ready.json")
	if err := hub.WriteReadyManifest(manifestPath, manifest); err != nil {
		_ = hub.Shutdown(context.Background())
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(manifestPath)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	<-ctx.Done()
	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := hub.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
