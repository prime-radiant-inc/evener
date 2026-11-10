package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	rootID       string
	mu           sync.Mutex
	steps        map[string]int
	childReady   chan string
	target       chan string
	release      chan struct{}
	terminal     chan struct{}
	lateTerminal bool
	wake         <-chan struct{}
	advanced     chan struct{}
	advancedOnce sync.Once
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
		default:
			// Every later call is the one acknowledgement the Manager makes of a
			// wake. Which turn runs it is the session's choice: a managed-job
			// notification landing between the list round and the next one makes
			// the user turn yield to an EntryNotification turn, so the same
			// acknowledgement can be the user turn's fourth call, the notification
			// turn's first, or the turn this harness forces after the completion.
			// Scripting an exact turn count made the provider answer the model's
			// retry budget with an unexpected-step error when the yield landed.
			if step > 8 {
				return llm.Response{}, fmt.Errorf("unexpected Manager model call step %d", step)
			}
			p.advancedOnce.Do(func() { close(p.advanced) })
		}
	} else {
		switch step {
		case 0:
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
			if !p.lateTerminal {
				return activityRelayTool("communicate", map[string]any{"message": "descendant-watch-result", "end_turn": true, "output": map[string]any{"message": "descendant-watch-result", "data": map[string]any{}, "artifacts": []string{}}}), nil
			}
			return activityRelayTool("communicate", map[string]any{"message": "descendant-watch-update", "end_turn": false, "output": map[string]any{"message": "", "data": map[string]any{}, "artifacts": []string{}}}), nil
		case 1:
			if !p.lateTerminal {
				return llm.Response{}, fmt.Errorf("unexpected child model call step %d", step)
			}
			select {
			case <-p.terminal:
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			}
			return activityRelayTool("communicate", map[string]any{"message": "descendant-watch-result", "end_turn": true, "output": map[string]any{"message": "descendant-watch-result", "data": map[string]any{}, "artifacts": []string{}}}), nil
		default:
			return llm.Response{}, fmt.Errorf("unexpected child model call step %d", step)
		}
	}
	return activityRelayTool("communicate", map[string]any{"message": "descendant-watch-manager-idle", "end_turn": true, "output": map[string]any{"message": "", "data": map[string]any{}, "artifacts": []string{}}}), nil
}

func TestE2E_ManagerOwnedDescendantWatch(t *testing.T) {
	for _, tc := range []struct {
		name              string
		missingGeneration bool
		lateTerminal      bool
	}{
		{name: "authoritative"},
		{name: "late-terminal", lateTerminal: true},
		{name: "missing-generation-refused", missingGeneration: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testManagerOwnedDescendantWatch(t, tc.missingGeneration, tc.lateTerminal)
		})
	}
}

func testManagerOwnedDescendantWatch(t *testing.T, missingGeneration, lateTerminal bool) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	stateDir := filepath.Join(t.TempDir(), "project-descendant-watch-0000000000")
	workDir, runDir := t.TempDir(), t.TempDir()
	wakes := make(chan struct{}, 32)
	notified := make(chan struct{}, 1)
	script := &descendantWatchProvider{steps: map[string]int{}, childReady: make(chan string, 1), target: make(chan string, 1), release: make(chan struct{}), terminal: make(chan struct{}), lateTerminal: lateTerminal, wake: wakes, advanced: make(chan struct{})}
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
	daemon.SetSessionTools(func() *agent.Session { return sess })
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
		select {
		case notified <- struct{}{}:
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
	cfg := testWebConfig(t, hubcore.WebConfig{StateDir: stateDir, RunDir: runDir, Past: hubcore.NewPastIndex(stateDir), Roster: roster, ResumeLocks: hubcore.NewResumeLocks()})
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
	var turns sync.WaitGroup
	turns.Go(func() {
		_, err := sess.ProcessInput(ctx, "descendant-watch-root", nil)
		done <- err
	})
	t.Cleanup(func() { cancel(); turns.Wait(); sess.Close() })
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
	awaitChildCommunicate := func(endTurn bool) {
		for {
			select {
			case update, ok := <-updates:
				if !ok {
					t.Fatalf("watch ended before the child communicate (end_turn=%v)", endTurn)
				}
				if update.Event == "communicate" && update.Metadata.EndTurn == endTurn {
					if update.Metadata.Ref != childRef || update.Metadata.InstanceID != rootID || update.Metadata.GenerationID != wantBoot {
						t.Fatalf("child communicate identity=%+v", update.Metadata)
					}
					return
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	// Discard any wake from before the child produced its communicate.
	select {
	case <-notified:
	default:
	}
	close(script.release)
	awaitChildCommunicate(!lateTerminal)
	// The child's communicate wakes the actual Manager consumer. Let the
	// Manager's turn finish; in the late-terminal variant the accepted terminal
	// follows and is delivered by a later notification turn, the one the
	// serving loop's guaranteed wake runs.
	select {
	case <-script.advanced:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Manager turn: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if lateTerminal {
		// Discard the first communicate's wake: the wait below is for the
		// accepted terminal's own notification wake.
		select {
		case <-notified:
		default:
		}
		close(script.terminal)
		awaitChildCommunicate(true)
	}
	// The terminal send and the watch clear are one durable batch
	// (endHubWatch), so an ended watch already owns its pending completion
	// frame; the wake below only has to deliver it.
	awaitActivityRelayCondition(ctx, t, func() bool {
		page, err := sess.ListActivityWatches(ctx, appwire.SessionActivityListParams{Ref: "local:" + rootID})
		if err != nil {
			t.Fatal(err)
		}
		return acceptanceWatchCompleted(page, registration.WatchID)
	}, "completed manager watch")
	select {
	case <-notified:
	case <-ctx.Done():
		t.Fatalf("Manager notification wake for accepted child completion never arrived: %v", ctx.Err())
	}
	// The serving loop turns that wake into an EntryNotification turn. This
	// harness drives the Manager directly and has no serving loop, so run it
	// here; without it the completion frame stays pending and its receipt is
	// never observable.
	if _, err := sess.ProcessInputKind(ctx, "", nil, agent.EntryNotification); err != nil {
		t.Fatalf("Manager notification wake: %v", err)
	}
	journalPath := filepath.Join(stateDir, "sessions", rootID, "jobs.jsonl")
	// The watch has already ended, so its terminal pending send is durable and
	// only the Manager's receipt can still appear. The public delivery counter is
	// not the completion contract; the journaled receipt is. The accepted
	// terminal can supersede the communicate event's wake, so that receipt is
	// order-dependent: the communicate frame itself was asserted on the
	// observation stream above, and the completion receipt is the wake the
	// Manager must always receive.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !managerWatchReceipt(journalPath, registration.WatchID, "completed: accepted terminal done") {
		select {
		case <-ctx.Done():
			dumpCtx, cancelDump := context.WithTimeout(context.Background(), time.Second)
			page, err := sess.ListActivityWatches(dumpCtx, appwire.SessionActivityListParams{Ref: "local:" + rootID})
			cancelDump()
			if err != nil {
				t.Fatalf("waiting for completed watch receipt: %v; list watches: %v; journal=%s", ctx.Err(), err, managerWatchJournalSnapshot(journalPath, registration.WatchID))
			}
			t.Fatalf("waiting for completed watch receipt: %v; watches=%+v; journal=%s", ctx.Err(), page.Watches, managerWatchJournalSnapshot(journalPath, registration.WatchID))
		case <-ticker.C:
		}
	}
	page, err := sess.ListActivityWatches(ctx, appwire.SessionActivityListParams{Ref: "local:" + rootID})
	if err != nil {
		t.Fatal(err)
	}
	for _, watch := range page.Watches {
		if watch.Watch.ID == registration.WatchID {
			if watch.State != appwire.SessionWatchStateEnded || watch.Watch.EndReason != "completed" {
				t.Fatalf("completed Manager watch state=%s reason=%q", watch.State, watch.Watch.EndReason)
			}
			t.Logf("Manager watch %s delivered child event and completion: child=%s instance=%s boot=%s deliveries=%d", registration.WatchID, childRef, rootID, wantBoot, watch.Watch.Deliveries)
			return
		}
	}
	t.Fatalf("Manager did not consume child watch delivery: %+v", page)
}

type watchSendJournalRecord struct {
	Kind          string
	DeliveryID    string
	TriggerReason string
	UpdateSeq     uint64
}

// watchSendJournalRecords decodes every watch-send record for watchID. The
// journal is read whole: an unterminated final line is still a record.
func watchSendJournalRecords(journalPath, watchID string) ([]watchSendJournalRecord, error) {
	journal, err := os.ReadFile(journalPath)
	if err != nil {
		return nil, err
	}
	var rows []watchSendJournalRecord
	for line := range bytes.SplitSeq(journal, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var record struct {
			Kind      string `json:"kind"`
			WatchSend *struct {
				Key struct {
					WatchID string `json:"watch_id"`
				} `json:"key"`
				DeliveryID    string `json:"delivery_id"`
				TriggerReason string `json:"trigger_reason"`
				UpdateSeq     uint64 `json:"update_seq"`
			} `json:"watch_send"`
		}
		if json.Unmarshal(line, &record) != nil || record.WatchSend == nil || record.WatchSend.Key.WatchID != watchID {
			continue
		}
		rows = append(rows, watchSendJournalRecord{
			Kind:          record.Kind,
			DeliveryID:    record.WatchSend.DeliveryID,
			TriggerReason: record.WatchSend.TriggerReason,
			UpdateSeq:     record.WatchSend.UpdateSeq,
		})
	}
	return rows, nil
}

func managerWatchReceipt(journalPath, watchID, triggerReason string) bool {
	rows, err := watchSendJournalRecords(journalPath, watchID)
	if err != nil {
		return false
	}
	for _, row := range rows {
		if row.Kind == "watch_send_delivered" && row.TriggerReason == triggerReason {
			return true
		}
	}
	return false
}

// managerWatchJournalSnapshot lists every watch-send record for watchID, so a
// missing completion receipt names what was recorded instead.
func managerWatchJournalSnapshot(journalPath, watchID string) string {
	rows, err := watchSendJournalRecords(journalPath, watchID)
	if err != nil {
		return "read journal: " + err.Error()
	}
	if len(rows) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, fmt.Sprintf("%s delivery=%s seq=%d trigger=%q", row.Kind, row.DeliveryID, row.UpdateSeq, row.TriggerReason))
	}
	return strings.Join(parts, "; ")
}
