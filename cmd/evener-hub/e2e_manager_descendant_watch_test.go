package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
	daemonserver "primeradiant.com/evener/server"
)

// Only the external model is scripted. Both the owned child and the Manager's
// job_watch tool use real sessions, transcript projections and bound hub Watch.
type descendantWatchProvider struct {
	rootID     string
	mu         sync.Mutex
	steps      map[string]int
	childReady chan string
	target     chan string
	release    chan struct{}
	wake       <-chan struct{}
	advanced   chan struct{}
}

type descendantWatchEnvelope struct{ sessionTaskEnvelopeSource }

func (s descendantWatchEnvelope) SessionMeta() schema.SessionMeta { return s.sess.Meta() }

func (*descendantWatchProvider) Name() string { return "openai" }
func (*descendantWatchProvider) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
func (p *descendantWatchProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if req.SessionID == "" {
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "descendant-watch-fixture"}}}}, nil
	}
	p.mu.Lock()
	step := p.steps[req.SessionID]
	p.steps[req.SessionID]++
	p.mu.Unlock()
	if req.SessionID == p.rootID {
		switch step {
		case 0:
			return activityRelayTool("delegate", map[string]any{"prompt": "descendant-watch-input", "delegation_allowance": 0}), nil
		case 1:
			select {
			case ref := <-p.target:
				return activityRelayTool("job_watch", map[string]any{"operation": "create", "hub": map[string]any{"ref": ref, "events": []string{"status", "communicate"}, "completion": true}}), nil
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			}
		case 2:
			select {
			case <-p.wake:
				return activityRelayTool("job_watch", map[string]any{"operation": "list"}), nil
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			}
		case 3:
			close(p.advanced)
		}
	} else if step == 0 {
		select {
		case p.childReady <- req.SessionID:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
		select {
		case <-p.release:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
		return activityRelayTool("communicate", map[string]any{"message": "descendant-watch-result", "end_turn": true, "output": map[string]any{"message": "descendant-watch-result", "data": map[string]any{}, "artifacts": []string{}}}), nil
	}
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

func TestE2E_ManagerOwnedDescendantWatch(t *testing.T) {
	for _, missingGeneration := range []bool{false, true} {
		name := "authoritative"
		if missingGeneration {
			name = "missing-generation-refused"
		}
		t.Run(name, func(t *testing.T) { testManagerOwnedDescendantWatch(t, missingGeneration) })
	}
}

func testManagerOwnedDescendantWatch(t *testing.T, missingGeneration bool) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	stateDir := filepath.Join(t.TempDir(), "project-descendant-watch-0000000000")
	workDir, runDir := t.TempDir(), t.TempDir()
	wakes := make(chan struct{}, 32)
	script := &descendantWatchProvider{steps: map[string]int{}, childReady: make(chan string, 1), target: make(chan string, 1), release: make(chan struct{}), wake: wakes, advanced: make(chan struct{})}
	llmClient := llm.NewClient()
	llmClient.Register(script)
	sess, err := agent.NewSession(llmClient, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), agent.SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1, Manager: true, Sandbox: "off", AgentsDocPath: filepath.Join(workDir, "absent-AGENTS.md")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.Close)
	rootID := sess.ID()
	script.rootID = rootID
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
	boot := appwire.BootGeneration(generation)
	if missingGeneration {
		boot = ""
	}
	daemon.ReplaceAppIdentity(prepared.WithRecordedLength(sess.TranscriptRecordedLength()).WithBootGeneration(boot), nil)
	seedDaemonStatus(daemon, daemonserver.StatusInfo{SessionID: rootID, State: "idle", WorkingDir: workDir})
	daemon.SetThreadEnvelopeSource(descendantWatchEnvelope{sessionTaskEnvelopeSource{sess: sess}})
	daemon.RefreshThreadEnvelope()
	daemon.SetDescendantTranscriptPathFunc(func(id string) string { return filepath.Join(stateDir, "sessions", id+".transcript.jsonl") })
	sess.SetDescendantEventFunc(func(event events.SessionEvent) { daemon.RecordDescendantAppEvent(rootID, event) })
	results := make(chan events.ToolCallEndData, 4)
	sess.ConsumeEventsLossless(func(event events.SessionEvent) {
		daemonserver.BridgeEvent(daemon, event, nil)
		if data, ok := event.Data.(events.ToolCallEndData); ok && data.ToolName == "job_watch" {
			results <- data
		}
	}, func() {})
	sess.SetNotifyFunc(func() {
		select {
		case wakes <- struct{}{}:
		default:
		}
	})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
	t.Cleanup(daemonHTTP.Close)
	entry := rendezvous.Entry{PID: os.Getpid(), Protocol: appwire.ProtocolVersion, Endpoint: daemonHTTP.URL, SourceID: "local", ThreadID: rootID, SessionID: rootID, InstanceID: rootID, WorkspaceRef: "local:" + rootID, WorkingDir: workDir, StateDir: stateDir}
	if _, err := rendezvous.Write(runDir, entry); err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRoster(runDir, &hubcore.StatusProber{})
	if err := roster.RefreshAndWait(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), StateDir: stateDir, RunDir: runDir, Past: hubcore.NewPastIndex(stateDir), Roster: roster, ResumeLocks: hubcore.NewResumeLocks()}
	sources := newHubSourceRegistry(cfg)
	server := newHubAppServer(cfg, sources)
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	var local *appsource.LocalDaemonSource
	for _, source := range sources.All() {
		if source, ok := source.(*appsource.LocalDaemonSource); ok {
			local = source
		}
	}
	if local == nil {
		t.Fatal("missing local source")
	}
	bound := &boundSessionOperations{owner: &sessionOperations{cfg: cfg, sources: sources, server: server}, local: local, binding: appwire.SessionBinding{Ref: "local:" + rootID, InstanceID: rootID, BindingID: "descendant-watch-binding"}}
	sess.SetHubClient(bound)
	done := make(chan error, 1)
	go func() { _, err := sess.ProcessInput(ctx, "descendant-watch-root", nil); done <- err }()
	t.Cleanup(func() { cancel(); <-done; sess.Close() })
	var childID string
	select {
	case childID = <-script.childReady:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	childRef := "local:" + childID
	// Refresh the real roster. Its public list row retains the root daemon's
	// instance while naming the child session and canonical child ref.
	if err := roster.RefreshAndWait(ctx); err != nil {
		t.Fatal(err)
	}
	hubHTTP := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	t.Cleanup(hubHTTP.Close)
	human := dialHubRPC(t, hubHTTP)
	t.Cleanup(func() { _ = human.Close() })
	if _, err := human.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	listed, err := human.ThreadList(ctx, appwire.ThreadListParams{})
	if err != nil {
		t.Fatal(err)
	}
	var child appwire.Thread
	for _, thread := range listed.Data {
		if thread.ID == childID {
			child = thread
		}
	}
	if child.ID != childID || child.Evener.Ref != childRef || child.Evener.InstanceID != rootID {
		t.Fatalf("public child list identity=%+v", child)
	}
	updates, err := bound.Watch(ctx, appwire.SessionWatchRequest{Ref: childRef, Events: []string{"status", "communicate"}, Completion: true})
	if err != nil {
		t.Fatal(err)
	}
	var first appwire.SessionWatchUpdate
	select {
	case first = <-updates:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Do not stop at a missing snapshot field: exercise job_watch's real gate too.
	script.target <- childRef
	var result events.ToolCallEndData
	select {
	case result = <-results:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if missingGeneration {
		if first.Metadata.InstanceID != rootID || first.Metadata.GenerationID != "" || result.Error == "" {
			t.Fatalf("missing generation admitted: metadata=%+v result=%+v", first.Metadata, result)
		}
		page, err := sess.ListActivityWatches(ctx, appwire.SessionActivityListParams{Ref: "local:" + rootID})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Watches) != 0 {
			t.Fatalf("rejected registration installed watches: %+v", page.Watches)
		}
		return
	}
	if result.Error != "" {
		t.Fatalf("Manager job_watch failed: error=%q state=%s; real daemon metadata=%+v", result.Error, result.ToolState, first.Metadata)
	}
	var registration struct {
		WatchID  string `json:"watch_id"`
		Watching bool   `json:"watching"`
	}
	if err := json.Unmarshal(result.ToolState, &registration); err != nil {
		t.Fatal(err)
	}
	if registration.WatchID == "" || !registration.Watching {
		t.Fatalf("watch registration=%s", result.ToolState)
	}
	wantBoot := appwire.BootGeneration(generation) + "@" + rootID
	if first.Snapshot == nil || first.Metadata.Ref != childRef || first.Metadata.InstanceID != rootID || first.Metadata.GenerationID != wantBoot || first.Snapshot.Thread.Evener.InstanceID != child.Evener.InstanceID || first.Snapshot.BootGeneration != wantBoot {
		t.Fatalf("watch/list identity mismatch: metadata=%+v snapshot=%+v list=%+v", first.Metadata, first.Snapshot, child)
	}
	close(script.release)
	communicated := false
	for !communicated {
		select {
		case update, ok := <-updates:
			if !ok {
				t.Fatal("watch ended before child communicate")
			}
			if update.Event == "communicate" && update.Metadata.EndTurn {
				if update.Metadata.Ref != childRef || update.Metadata.InstanceID != rootID || update.Metadata.GenerationID != wantBoot {
					t.Fatalf("child communicate identity=%+v", update.Metadata)
				}
				communicated = true
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	// The actual Manager consumer must also deliver the child's communicate.
	// Completion remains registered, but delegates do not offer end_reason=done.
	// Await its runner wake, then inspect the public receiver-owned watch state.
	select {
	case <-script.advanced:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	journal, err := os.ReadFile(filepath.Join(stateDir, "sessions", rootID, "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	communicateDelivered := false
	lines := bytes.Split(journal, []byte{'\n'})
	for _, line := range lines[:len(lines)-1] {
		var record struct {
			Kind      string `json:"kind"`
			WatchSend *struct {
				Key struct {
					WatchID string `json:"watch_id"`
				} `json:"key"`
				TriggerReason string `json:"trigger_reason"`
			} `json:"watch_send"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record.Kind == "watch_send_delivered" && record.WatchSend != nil && record.WatchSend.Key.WatchID == registration.WatchID && record.WatchSend.TriggerReason == "communicate" {
			communicateDelivered = true
		}
	}
	if !communicateDelivered {
		t.Fatal("Manager has no delivered receipt for child communicate")
	}
	{
		page, err := sess.ListActivityWatches(ctx, appwire.SessionActivityListParams{Ref: "local:" + rootID})
		if err != nil {
			t.Fatal(err)
		}
		for _, watch := range page.Watches {
			if watch.Watch.ID == registration.WatchID && watch.State == appwire.SessionWatchStateArmed && watch.Watch.Deliveries > 0 {
				t.Logf("Manager watch %s delivered child event: child=%s instance=%s boot=%s deliveries=%d", registration.WatchID, childRef, rootID, wantBoot, watch.Watch.Deliveries)
				return
			}
		}
		t.Fatalf("Manager did not consume child watch delivery: %+v", page)
	}
}
