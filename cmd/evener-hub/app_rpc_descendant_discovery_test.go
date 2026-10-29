package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
	daemonserver "primeradiant.com/evener/server"
)

// Only model decisions are scripted. The child, transcript, roster probe,
// read capture and delivered notifications are produced by real Evener owners.
type childDiscoveryProvider struct {
	rootID  string
	mu      sync.Mutex
	steps   map[string]int
	ready   chan string
	release chan struct{}
}

func (*childDiscoveryProvider) Name() string { return "openai" }
func (*childDiscoveryProvider) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
func (p *childDiscoveryProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if req.SessionID == "" {
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "discovery-fixture"}}}}, nil
	}
	p.mu.Lock()
	step := p.steps[req.SessionID]
	p.steps[req.SessionID]++
	p.mu.Unlock()
	if req.SessionID == p.rootID && step == 0 {
		return activityRelayTool("delegate", map[string]any{"prompt": "child-discovery-input", "delegation_allowance": 0}), nil
	}
	if req.SessionID != p.rootID && step == 0 {
		p.ready <- req.SessionID
		select {
		case <-p.release:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "child-discovery-output"}}}}, nil
	}
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

func TestHubChildDiscoveryFirstSubscribedRead(t *testing.T) {
	testHubChildDiscovery(t, "live")
}

func TestHubChildDiscoveryRecoveryBoundaries(t *testing.T) {
	for _, scenario := range []string{"absent", "owner-replaced", "source-removed", "transport-lost", "deleted", "cancelled", "remote"} {
		t.Run(scenario, func(t *testing.T) { testHubChildDiscovery(t, scenario) })
	}
}

// readChildRetryingIndexContention follows the typed locked answer's automatic
// retry disposition: the child's daemon extends the transcript index on its
// own projection goroutine, so a fallback read can meet that writer mid-extension.
// Each attempt starts with fresh counters, so the final read still has to
// match its scenario exactly.
func readChildRetryingIndexContention(ctx context.Context, t *testing.T, client *appwire.Client, params appwire.ThreadReadParams, resetCounters func()) (appwire.ThreadReadResponse, error) {
	t.Helper()
	for {
		resetCounters()
		read, err := client.ThreadRead(ctx, params)
		if err == nil {
			return read, nil
		}
		wire, ok := wireErrorFromError(err)
		if !ok || !wireErrorInfoIs(wire.Data, appwire.ErrorTranscriptIndexLocked) {
			return read, err
		}
		select {
		case <-ctx.Done():
			return read, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func testHubChildDiscovery(t *testing.T, scenario string) {
	// Serial: observes the shared refresh function, without replacing its work.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stateDir := filepath.Join(t.TempDir(), "project-discovery-0000000000")
	workDir, runDir := t.TempDir(), t.TempDir()
	providerScript := &childDiscoveryProvider{steps: map[string]int{}, ready: make(chan string, 1), release: make(chan struct{})}
	llmClient := llm.NewClient()
	llmClient.Register(providerScript)
	sess, err := agent.NewSession(llmClient, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), agent.SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1, Sandbox: "off", AgentsDocPath: filepath.Join(workDir, "absent-AGENTS.md")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.Close)
	rootID := sess.ID()
	providerScript.rootID = rootID
	daemon := daemonserver.NewServer(daemonserver.ServerConfig{})
	t.Cleanup(daemon.Close)
	t.Cleanup(func() { _ = daemon.AppServer().Shutdown(context.Background()) })
	prepared, err := daemonserver.PrepareAppIdentityForRef("local", rootID, "local:"+rootID, sess.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	generation, err := schema.NextBootGeneration(stateDir, rootID, 0)
	if err != nil {
		t.Fatal(err)
	}
	daemon.WireTranscriptHistory(sess)
	daemon.ReplaceAppIdentity(prepared.WithRecordedLength(sess.TranscriptRecordedLength()).WithBootGeneration(appwire.BootGeneration(generation)), nil)
	seedDaemonStatus(daemon, daemonserver.StatusInfo{SessionID: rootID, State: "idle", WorkingDir: workDir})
	daemon.SetDescendantTranscriptPathFunc(func(id string) string { return filepath.Join(stateDir, "sessions", id+".transcript.jsonl") })
	sess.SetDescendantEventFunc(func(event events.SessionEvent) { daemon.RecordDescendantAppEvent(rootID, event) })
	sess.ConsumeEventsLossless(func(event events.SessionEvent) { daemonserver.BridgeEvent(daemon, event, nil) }, func() {})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
	t.Cleanup(daemonHTTP.Close)
	entry := rendezvous.Entry{PID: os.Getpid(), Protocol: appwire.ProtocolVersion, Endpoint: daemonHTTP.URL, SourceID: "local", ThreadID: rootID, SessionID: rootID, WorkspaceRef: "local:" + rootID, WorkingDir: workDir, StateDir: stateDir}
	registration, err := rendezvous.Write(runDir, entry)
	if err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRoster(runDir, &hubcore.StatusProber{})
	if err := roster.RefreshAndWait(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), StateDir: stateDir, RunDir: runDir, Past: hubcore.NewPastIndex(stateDir), Roster: roster, ResumeLocks: hubcore.NewResumeLocks()}
	cfg.DeletionStore, err = hubcore.NewDeletionStore(cfg.HubStateRoot)
	if err != nil {
		t.Fatal(err)
	}
	sources := newHubSourceRegistry(cfg)
	// Observe calls while retaining all real relay operations and their results.
	var reads atomic.Int32
	cfg.RelayHooks.BeforeReadThread = func() { reads.Add(1) }
	server := newHubAppServer(cfg, sources)
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	hubHTTP := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	t.Cleanup(hubHTTP.Close)
	client := dialHubRPC(t, hubHTTP)
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	rootRead, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:" + rootID, IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := sess.ProcessInput(ctx, "child-discovery-root", nil); done <- err }()
	t.Cleanup(func() { cancel(); <-done; sess.Close() })
	childID := <-providerScript.ready
	for _, row := range roster.List() {
		if slices.Contains(row.RunningSubagentIDs, childID) {
			t.Fatal("child already in initial roster")
		}
	}
	reads.Store(0)
	readCtx, cancelRead := context.WithCancel(ctx)
	defer cancelRead()
	var refreshes atomic.Int32
	previousRefresh := hubRosterRefresh
	hubRosterRefresh = func(ctx context.Context, r *hubcore.Roster) error {
		refreshes.Add(1)
		if scenario == "absent" {
			// A retried read finds the registration already gone.
			if err := os.Remove(registration); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		if err := previousRefresh(ctx, r); err != nil {
			return err
		}
		switch scenario {
		case "cancelled":
			cancelRead()
		case "deleted":
			_, err := cfg.DeletionStore.Begin("project-discovery-0000000000", []hubcore.DeletionTarget{{Ref: "local:" + childID, ThreadID: childID}})
			return err
		case "source-removed":
			sources.Remove("local")
		case "owner-replaced":
			// Use the daemon's real identity replacement after discovery has
			// published the old owner, not a fabricated read failure.
			replacement, err := agent.NewSession(llmClient, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), agent.SessionConfig{StateDir: stateDir, Sandbox: "off"})
			if err != nil {
				return err
			}
			defer replacement.Close()
			next, err := daemonserver.PrepareAppIdentityForRef("local", replacement.ID(), "local:"+rootID, replacement.TranscriptPath())
			if err != nil {
				return err
			}
			daemon.WireTranscriptHistory(replacement)
			daemon.ReplaceAppIdentity(next.WithRecordedLength(replacement.TranscriptRecordedLength()).WithBootGeneration("2"), nil)
		case "transport-lost":
			return daemon.AppServer().Shutdown(context.Background())
		}
		return nil
	}
	t.Cleanup(func() { hubRosterRefresh = previousRefresh })
	params := appwire.ThreadReadParams{Ref: "local:" + childID, Subscribe: true, IncludeTurns: true, ItemLimit: 40, RequestGeneration: 17}
	var read appwire.ThreadReadResponse
	if scenario == "cancelled" {
		raw, marshalErr := json.Marshal(params)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		_, err = server.Router().Dispatch(readCtx, appwire.Request{Method: appwire.MethodThreadRead, Params: raw})
	} else {
		if scenario == "remote" {
			params.Ref = "unattached:" + childID
		}
		read, err = readChildRetryingIndexContention(readCtx, t, client, params, func() { refreshes.Store(0); reads.Store(0) })
	}
	if scenario == "remote" {
		if err == nil || refreshes.Load() != 0 || reads.Load() != 0 {
			t.Fatalf("remote read borrowed local recovery: err=%v refreshes=%d reads=%d", err, refreshes.Load(), reads.Load())
		}
		return
	}
	if scenario == "cancelled" || scenario == "deleted" {
		valid := errors.Is(err, context.Canceled)
		if scenario == "deleted" {
			valid = isTargetDeletedError(err)
		}
		if !valid || refreshes.Load() != 1 || reads.Load() != 1 {
			t.Fatalf("%s: err=%v refreshes=%d reads=%d", scenario, err, refreshes.Load(), reads.Load())
		}
		t.Logf("%s preserves typed refusal, refreshes=1 reads=1", scenario)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if scenario != "live" {
		wantReads := int32(2)
		if scenario == "absent" || scenario == "source-removed" {
			wantReads = 1
		}
		if read.BootGeneration != appwire.DaemonlessBootGeneration || read.Thread.ID != childID || read.Thread.Evener.Ref != "local:"+childID || read.Snapshot == nil || len(read.Thread.Turns) == 0 || refreshes.Load() != 1 || reads.Load() != wantReads {
			t.Fatalf("%s fallback generation=%s id=%s ref=%s snapshot=%v turns=%d refreshes=%d reads=%d", scenario, read.BootGeneration, read.Thread.ID, read.Thread.Evener.Ref, read.Snapshot, len(read.Thread.Turns), refreshes.Load(), reads.Load())
		}
		t.Logf("%s preserves saved child history, refreshes=%d reads=%d", scenario, refreshes.Load(), reads.Load())
		return
	}
	found := false
	for _, row := range roster.List() {
		found = found || slices.Contains(row.RunningSubagentIDs, childID)
	}
	t.Logf("discovery found child=%s, refreshes=%d, first generation=%s, root generation=%s", childID, refreshes.Load(), read.BootGeneration, rootRead.BootGeneration)
	// The live daemon currently leaves Authoritative unset (finishAppThreadRead).
	// Its boot identity and recorded snapshot, not that flag, prove a live read.
	if read.BootGeneration != rootRead.BootGeneration+"@"+rootID || read.BootGeneration == "" || read.Snapshot == nil || read.Thread.Evener.MutationStateAuthoritative || read.Thread.ID != childID || read.Thread.Evener.Ref != "local:"+childID || read.RequestGeneration != 17 {
		t.Fatalf("first subscribed child read must retain live identity: generation=%s root=%s authoritative=%v id=%s ref=%s request=%d", read.BootGeneration, rootRead.BootGeneration, read.Authoritative, read.Thread.ID, read.Thread.Evener.Ref, read.RequestGeneration)
	}
	if !found || refreshes.Load() != 1 || reads.Load() != 2 {
		t.Fatalf("discovery found=%v refreshes=%d reads=%d", found, refreshes.Load(), reads.Load())
	}
	close(providerScript.release)
	for note := range client.Notifications() {
		if note.Method != appwire.NotifyHistoryUpdated {
			continue
		}
		var event appwire.HistoryUpdatedParams
		if err := json.Unmarshal(note.Params, &event); err != nil {
			t.Fatal(err)
		}
		foundOutput := false
		for _, item := range event.Items {
			foundOutput = foundOutput || item.Text == "child-discovery-output"
		}
		if !foundOutput {
			continue
		}
		if event.ThreadID != childID || event.Ref != "local:"+childID || event.BootGeneration != read.BootGeneration {
			t.Fatalf("wrong child delivery identity: %+v", event)
		}
		t.Logf("delivered child-qualified item: thread=%s ref=%s text=%s", event.ThreadID, event.Ref, "child-discovery-output")
		if got := server.SubscriberCount("local:" + childID); got != 1 {
			t.Fatalf("child subscribers=%d, want one", got)
		}
		if _, err := client.ThreadUnsubscribe(ctx, appwire.ThreadUnsubscribeParams{Ref: "local:" + childID}); err != nil {
			t.Fatal(err)
		}
		if got := server.SubscriberCount("local:" + childID); got != 0 {
			t.Fatalf("child unsubscribe retained %d subscribers", got)
		}
		return
	}
	t.Fatal("subscription closed without the child output")
}
