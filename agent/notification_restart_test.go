package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// A crash preserves prefixes of independently synced files. These cases cut
// copies of actual source/receiver writes, not fabricated controller answers.
// Every cut closes the original lifetime and reopens both stores via restore.
func TestNotificationRestartCuts(t *testing.T) {
	t.Parallel()
	for _, cut := range []string{
		"before receiver append", "after append before source ack", "after source ack before wake",
		"after request snapshot before response", "after response sync before receipt index",
		"after wait checkpoint before executor release", "after resume claim before checkpoint",
		"after completed resume",
	} {
		t.Run(cut, func(t *testing.T) { testNotificationShellRestartCut(t, cut) })
	}
	for _, cut := range []string{"watch production before durable end", "watch durable end before custody"} {
		t.Run(cut, func(t *testing.T) { testNotificationWatchRestartCut(t, cut) })
	}
	for _, cut := range []string{"terminal preparation before settlement", "Stop request before completion"} {
		t.Run(cut, func(t *testing.T) { testNotificationDelegateRestartCut(t, cut) })
	}
}

func incidentPendingFacts(t *testing.T, stateDir, sessionID, id string) {
	t.Helper()
	data, err := readTranscriptFull(transcriptPath(stateDir, sessionID), "")
	if err != nil {
		t.Fatal(err)
	}
	accepted := 0
	for _, entry := range data.Entries {
		if entry.Turn.AttentionID == id {
			accepted++
		}
		if slices.Contains(entry.Turn.ObservedNotificationIDs, id) || (entry.Turn.AttentionResolution != nil && entry.Turn.AttentionResolution.AttentionID == id) {
			t.Fatalf("unobserved source %s received successful coverage or consumption", id)
		}
	}
	if accepted != 1 {
		t.Fatalf("source %s accepted=%d want1", id, accepted)
	}
}

func TestNotificationOwnerHeldObserverEligible(t *testing.T) {
	a := newMessagingAdapter("HELD-OBSERVER-11")
	returns := newActivationReturns()
	yieldGate := make(chan struct{})
	releaseYield := sync.OnceFunc(func() { close(yieldGate) })
	defer releaseYield()
	a.script("root", func(llm.Request) llm.Response {
		return delegateToolCall("held-observer-11", map[string]any{"prompt": "HELD-OBSERVER-11", "watch_parent": true})
	}, func(llm.Request) llm.Response { return finalResponse("held-owner-initial-11") })
	a.script("HELD-OBSERVER-11", func(llm.Request) llm.Response {
		return toolCallResponse(llm.ToolCallData{ID: "held-observer-watch-11", Name: "job_watch", Type: "function", Arguments: json.RawMessage(`{"operation":"create","source":"parent","events":["assistant.tool"],"event_filter":{"tool_name":"ask_user","status":"ok"},"note":"observer-7a32bd90"}`)})
	}, func(req llm.Request) llm.Response {
		watchID := regexp.MustCompile(`watch_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages))
		if watchID == "" {
			t.Errorf("watch tool result omitted identity: %s", lastToolResultText(req.Messages))
		}
		awaitSignal(t, yieldGate, "observer activation capture")
		return waitResponseFor("observer-wait-11", "watch:"+watchID)
	}, func(req llm.Request) llm.Response {
		return finalResponse("observer-report-9a087bc1")
	})
	root := newMessagingRoot(t, a, returns.configure)
	t.Cleanup(func() { returns.close(t, root) })
	if _, err := root.ProcessInput(context.Background(), "held-observer-start-11", nil); err != nil {
		t.Fatal(err)
	}
	observer := root.subagents.sessions()[0]
	a.waitForRequests(t, "HELD-OBSERVER-11", 2)
	initial := returns.take(t, observer.ID())
	releaseYield()
	incidentYielded(t, root, observer.ID(), initial)
	a.script("root", func(llm.Request) llm.Response {
		return toolCallResponse(askUserCall("held-question-11", askUserArgsValid()))
	})
	if _, err := root.ProcessInput(context.Background(), "held-question-start-11", nil); err != nil {
		t.Fatal(err)
	}
	if root.askPendingCount() != 1 {
		t.Fatal("owner question was not held")
	}
	before := len(a.requestsFor("root"))
	var reportID string
	waitForCondition(t, 3*time.Second, "independent observer delivery while owner held", func() bool {
		events := publicationEvents(t, root)
		var delivery string
		for _, e := range events {
			if e.DelegateID == observer.owningDelegateID && e.RunFinished != nil && e.RunFinished.Generation == 1 {
				delivery = e.RunFinished.DeliveryID
			}
		}
		for _, e := range events {
			if delivery != "" && e.DelegateID == observer.owningDelegateID && e.DeliveryAcknowledged != nil && e.DeliveryAcknowledged.DeliveryID == delivery {
				reportID = "delegate:" + e.DeliveryAcknowledged.DeliveryID
				return true
			}
		}
		return false
	})
	if len(a.requestsFor("root")) != before || root.askPendingCount() != 1 {
		t.Fatal("observer report bypassed owner's human hold")
	}
	if len(a.requestsFor("HELD-OBSERVER-11")) != 3 {
		t.Fatal("observer did not run exactly one independent successor")
	}
	produced := producerSourceEvents(t, root.jobManager, jobstore.EventWatchSendPending)
	if len(produced) != 1 || produced[0].WatchSend == nil {
		t.Fatalf("parent watch source=%+v", produced)
	}
	state := produced[0].WatchSend
	req := a.requestsFor("HELD-OBSERVER-11")[2]
	fields := incidentWatchFields(req, state.Key.WatchID)
	args, _ := json.Marshal(askUserArgsValid())
	if fields["event.kind"] != "assistant.tool" || fields["event.tool_name"] != "ask_user" || fields["event.call_id"] != "held-question-11" || fields["event.status"] != "ok" || fields["event.arguments_json"] != string(args) {
		t.Fatalf("observer omitted actual parent ask_user event: fields=%v request=%+v", fields, req.Messages)
	}
	incidentNotificationFacts(t, root.stateDir, observer.ID(), stableWatchAttentionID(*state))
	incidentCoverage(t, root.stateDir, observer.ID(), map[string][]string{"observer-report-9a087bc1": {stableWatchAttentionID(*state)}})
	incidentPendingFacts(t, root.stateDir, root.ID(), reportID)
}

func TestNotificationUnobservedReportAcrossCompaction(t *testing.T) {
	a := newMessagingAdapter("COMPACT-WORKER-11")
	root, _ := newCompactableSession(t)
	root.client.Register(a)
	a.rootID = root.ID()
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	a.script("root", func(llm.Request) llm.Response { return delegateToolResponse("compact-worker-11", "COMPACT-WORKER-11") }, func(llm.Request) llm.Response { return finalResponse("compact-root-initial-11") })
	a.script("COMPACT-WORKER-11", func(llm.Request) llm.Response { <-gate; return finalResponse("unobserved-report-629da8c3") })
	if _, err := root.ProcessInput(context.Background(), "compact-start-11", nil); err != nil {
		t.Fatal(err)
	}
	worker := root.subagents.sessions()[0]
	release()
	var delivery string
	waitForCondition(t, 3*time.Second, "real report receiver custody", func() bool {
		for _, e := range publicationEvents(t, root) {
			if e.DelegateID == worker.owningDelegateID && e.DeliveryAcknowledged != nil {
				delivery = e.DeliveryAcknowledged.DeliveryID
				return true
			}
		}
		return false
	})
	id := "delegate:" + delivery
	incidentPendingFacts(t, root.stateDir, root.ID(), id)
	compactAfterAttention(t, root)
	incidentPendingFacts(t, root.stateDir, root.ID(), id)
	a.script("root", func(req llm.Request) llm.Response {
		if !incidentDelegatePayload(req, worker.owningDelegateID, "unobserved-report-629da8c3") {
			t.Error("designated post-compaction request omitted source or report")
		}
		return finalResponse("compact-report-consumed-11")
	})
	wake, hint := newDrainWake()
	root.SetNotifyFunc(hint)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := root.DrainJobTree(ctx, wake); err != nil {
		t.Fatal(err)
	}
	if len(a.requestsFor("root")) != 3 {
		t.Fatal("compacted pending report did not cause exactly one useful request")
	}
	incidentNotificationFacts(t, root.stateDir, root.ID(), id)
	incidentCoverage(t, root.stateDir, root.ID(), map[string][]string{"compact-report-consumed-11": {id}})
}

// truncateRestartTranscript retains complete real records before the selected
// boundary. It operates only on a test-owned closed copy.
func truncateRestartTranscript(t *testing.T, path string, stop func(schema.Turn) bool) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var kept []byte
	found := false
	for _, line := range bytes.SplitAfter(raw, []byte{'\n'}) {
		entry, err := transcript.DecodeEntry(bytes.TrimSpace(line))
		if err == nil && stop(entry.Turn) {
			found = true
			break
		}
		kept = append(kept, line...)
	}
	if !found {
		t.Fatalf("no real transcript boundary in %s", path)
	}
	if err := os.WriteFile(path, kept, 0o600); err != nil {
		t.Fatal(err)
	}
}

func truncateRestartSourceBeforeAck(t *testing.T, path, jobID string) {
	t.Helper()
	truncateRestartJobSource(t, path, func(e jobstore.Event) bool {
		return e.Kind == jobstore.EventJobNotificationDelivered && e.JobID == jobID
	})
}

func truncateRestartJobSource(t *testing.T, path string, stop func(jobstore.Event) bool) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var kept []byte
	found := false
	for _, line := range bytes.SplitAfter(raw, []byte{'\n'}) {
		var e jobstore.Event
		if json.Unmarshal(bytes.TrimSpace(line), &e) == nil && stop(e) {
			found = true
			break
		}
		kept = append(kept, line...)
	}
	if !found {
		t.Fatalf("no real source boundary in %s", path)
	}
	if err := os.WriteFile(path, kept, 0o600); err != nil {
		t.Fatal(err)
	}
}

func truncateRestartDelegateSource(t *testing.T, path string, kind delegatestore.EventKind) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var kept []byte
	found := false
	for _, line := range bytes.SplitAfter(raw, []byte{'\n'}) {
		var batch struct {
			Events []delegatestore.Event `json:"events"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(line), &batch); err == nil {
			for i, e := range batch.Events {
				if e.Kind == kind {
					if i != 0 {
						t.Fatal("requested crash cut would split an atomic source batch")
					}
					found = true
				}
			}
		}
		if found {
			break
		}
		kept = append(kept, line...)
	}
	if !found {
		t.Fatalf("no real delegate boundary %s", kind)
	}
	if err := os.WriteFile(path, kept, 0o600); err != nil {
		t.Fatal(err)
	}
}

func testNotificationDelegateRestartCut(t *testing.T, cut string) {
	const key, payload = "RESTART-TERMINAL-11", "packet-5c8b439f"
	a := newMessagingAdapter(key)
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	a.script("root", func(llm.Request) llm.Response { return delegateToolResponse("restart-terminal-11", key) }, func(llm.Request) llm.Response { return finalResponse("started-terminal-11") })
	a.script(key, func(llm.Request) llm.Response { <-gate; return finalResponse(payload) })
	root := newMessagingRoot(t, a, nil)
	if _, err := root.ProcessInput(context.Background(), "start-terminal-11", nil); err != nil {
		t.Fatal(err)
	}
	a.waitForRequests(t, key, 1)
	child := root.subagents.sessions()[0]
	if cut == "Stop request before completion" {
		a.script("root", func(llm.Request) llm.Response {
			args, _ := json.Marshal(map[string]any{"target": child.owningDelegateID, "max_wait_ms": 0})
			return toolCallResponse(llm.ToolCallData{ID: "restart-stop-11", Name: "job_stop", Type: "function", Arguments: args})
		}, func(llm.Request) llm.Response { return finalResponse("stop-accepted-11") })
		if _, err := root.ProcessInput(context.Background(), "stop-11", nil); err != nil {
			t.Fatal(err)
		}
	} else {
		release()
		waitForCondition(t, 3*time.Second, "terminal source settled", func() bool {
			return !delegateAggregateSnapshot(t, root.delegateController, child.owningDelegateID).CurrentRunOpen
		})
		waitForCondition(t, 3*time.Second, "terminal receiver custody", func() bool { return producerRecordCount(t, root) == 1 })
	}
	copied := copyWaitingState(t, root.stateDir)
	if cut == "terminal preparation before settlement" {
		truncateRestartDelegateSource(t, delegateResourceStorePath(copied, root.ID()), delegatestore.EventDelegateRunFinished)
		truncateRestartTranscript(t, transcriptPath(copied, root.ID()), func(turn schema.Turn) bool { return turn.AttentionID != "" })
	}
	release()
	root.Close()
	beforeRoot, beforeChild := len(a.requestsFor("root")), len(a.requestsFor(key))
	var received llm.Request
	a.script("root", func(req llm.Request) llm.Response { received = req; return finalResponse("terminal-observed-11") })
	restored := restoreWaitingCopy(t, root, copied)
	wake, hint := newDrainWake()
	restored.SetNotifyFunc(hint)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := restored.DrainJobTree(ctx, wake); err != nil {
		t.Fatal(err)
	}
	if len(a.requestsFor("root")) != beforeRoot+1 || len(a.requestsFor(key)) != beforeChild {
		t.Fatal("restart should repair source and run only the owed receiver")
	}
	starts, prepared, finished, stops, completed, acks := 0, 0, 0, 0, 0, 0
	var delivery string
	for _, e := range publicationEvents(t, restored) {
		if e.DelegateID != child.owningDelegateID {
			continue
		}
		if e.RunStarted != nil {
			starts++
		}
		if e.TerminalPrepared != nil {
			prepared++
		}
		if e.RunFinished != nil {
			finished++
			delivery = e.RunFinished.DeliveryID
			want := delegatestore.OutcomeCompleted
			if cut == "Stop request before completion" {
				want = delegatestore.OutcomeStopped
			}
			if e.RunFinished.Generation != 1 || e.RunFinished.Outcome.Status != want {
				t.Fatalf("repaired outcome=%+v", e.RunFinished)
			}
		}
		if e.SubtreeStopRequested != nil {
			stops++
			if !e.SubtreeStopRequested.PreserveNotifications {
				t.Fatal("new Stop lost notification preservation policy")
			}
		}
		if e.SubtreeStopCompleted != nil {
			completed++
		}
		if e.DeliveryAcknowledged != nil {
			acks++
		}
	}
	if starts != 1 || finished != 1 || acks != 1 || delivery == "" {
		t.Fatalf("starts=%d prepared=%d finishes=%d custody=%d delivery=%s", starts, prepared, finished, acks, delivery)
	}
	if cut == "Stop request before completion" {
		if stops != 1 || completed != 1 {
			t.Fatalf("Stop requested=%d completed=%d", stops, completed)
		}
	} else if prepared != 1 || !incidentDelegatePayload(received, child.owningDelegateID, payload) {
		t.Fatal("immutable prepared packet not delivered")
	}
	if !incidentDelegatePayload(received, child.owningDelegateID, "") {
		t.Fatal("designated receiver request omitted exact repaired delegate identity")
	}
	incidentNotificationFacts(t, copied, root.ID(), "delegate:"+delivery)
	incidentCoverage(t, copied, root.ID(), map[string][]string{"terminal-observed-11": incidentPresentedIDs(t, copied, root.ID(), received, "delegate:"+delivery)}, "delegate:"+delivery)
}

func testNotificationShellRestartCut(t *testing.T, cut string) {
	a := newMessagingAdapter()
	fifo, releaseShell := waitProducerFIFO(t)
	defer releaseShell()
	var jobID string
	waiting := cut == "after wait checkpoint before executor release" || cut == "after resume claim before checkpoint" || cut == "after completed resume"
	a.script("root", func(llm.Request) llm.Response { return incidentShellResponse("restart-shell-11", fifo) }, func(req llm.Request) llm.Response {
		jobID = regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages))
		if waiting {
			return waitResponseFor("restart-wait-11", jobID)
		}
		return finalResponse("initial-restart-11")
	})
	root := newMessagingRoot(t, a, nil)
	if _, err := root.ProcessInput(context.Background(), "restart-start-11", nil); err != nil {
		t.Fatal(err)
	}
	if jobID == "" {
		t.Fatal("real shell identity missing")
	}
	var copied string
	var attempt llm.Request
	if cut == "after wait checkpoint before executor release" {
		copied = copyWaitingState(t, root.stateDir)
		path := transcriptPath(copied, root.ID())
		seen := false
		truncateRestartTranscript(t, path, func(turn schema.Turn) bool {
			if seen {
				return true
			}
			seen = turn.WaitCheckpoint != nil
			return false
		})
	} else {
		releaseShell()
		waitForCondition(t, 3*time.Second, "real shell receiver custody", func() bool { return producerRecordCount(t, root) == 1 })
		waitForCondition(t, 3*time.Second, "real shell source acknowledgement", func() bool {
			return len(producerSourceEvents(t, root.jobManager, jobstore.EventJobNotificationDelivered)) == 1
		})
		source := producerSourceEvents(t, root.jobManager, jobstore.EventJobFinished)
		if len(source) != 1 || source[0].JobID != jobID {
			t.Fatalf("source completion=%+v", source)
		}
		id := "shell:" + jobID + ":" + source[0].TerminalGen
		switch cut {
		case "after request snapshot before response", "after resume claim before checkpoint":
			entered, gate := make(chan llm.Request, 1), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(gate) })
			defer unblock()
			a.script("root", func(req llm.Request) llm.Response {
				entered <- req
				<-gate
				return finalResponse("attempt-response-11")
			})
			done := make(chan error, 1)
			go func() { _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); done <- err }()
			select {
			case attempt = <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("request snapshot missing")
			}
			if err := incidentJobPayload(attempt, jobID, string(source[0].Status), source[0].Reason); err != nil {
				t.Fatal(err)
			}
			copied = copyWaitingState(t, root.stateDir)
			for _, entry := range notificationRawEntries(t, transcriptPath(copied, root.ID())) {
				if slices.Contains(entry.Turn.ObservedNotificationIDs, id) {
					t.Fatal("attempt or claim manufactured successful coverage")
				}
			}
			if cut == "after resume claim before checkpoint" {
				cp, err := readWaitCheckpoint(transcriptPath(copied, root.ID()), root.ID())
				if err != nil || cp == nil || cp.ResumedByTurnID == "" {
					t.Fatalf("claim not durable before provider: %+v %v", cp, err)
				}
			}
			unblock()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		case "after response sync before receipt index", "after completed resume":
			a.script("root", func(req llm.Request) llm.Response { attempt = req; return finalResponse("response-synced-11") })
			if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			if err := incidentJobPayload(attempt, jobID, string(source[0].Status), source[0].Reason); err != nil {
				t.Fatal(err)
			}
			copied = copyWaitingState(t, root.stateDir)
			if cut == "after response sync before receipt index" {
				truncateRestartTranscript(t, transcriptPath(copied, root.ID()), func(turn schema.Turn) bool {
					return turn.AttentionResolution != nil && turn.AttentionResolution.AttentionID == id
				})
			}
		default:
			copied = copyWaitingState(t, root.stateDir)
			if cut == "before receiver append" {
				truncateRestartTranscript(t, transcriptPath(copied, root.ID()), func(turn schema.Turn) bool { return turn.AttentionID == id })
			}
			if cut != "after source ack before wake" {
				truncateRestartSourceBeforeAck(t, filepath.Join(jobsDir(copied, root.ID()), "jobs.jsonl"), jobID)
			}
		}
	}
	root.Close()
	releaseShell()
	before := len(a.requestsFor("root"))
	observed := make(chan llm.Request, 1)
	a.script("root", func(req llm.Request) llm.Response { observed <- req; return finalResponse("recovered-11") })
	restored := restoreWaitingCopy(t, root, copied)
	wake, hint := newDrainWake()
	restored.SetNotifyFunc(hint)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := restored.DrainJobTree(ctx, wake); err != nil {
		t.Fatal(err)
	}
	want := 1
	if cut == "after response sync before receipt index" || cut == "after completed resume" {
		want = 0
	}
	if got := len(a.requestsFor("root")) - before; got != want {
		t.Fatalf("new requests=%d want%d, historical attempted request is not successful coverage", got, want)
	}
	var recovered llm.Request
	if want == 1 {
		recovered = <-observed
		if recovered.SessionID != root.ID() {
			t.Fatal("wrong recovered receiver")
		}
	}
	events := producerSourceEvents(t, restored.jobManager, jobstore.EventJobFinished)
	if len(events) != 1 || events[0].JobID != jobID {
		t.Fatalf("reopened source terminal facts=%+v", events)
	}
	id := "shell:" + jobID + ":" + events[0].TerminalGen
	response := "response-synced-11"
	if want == 1 {
		response = "recovered-11"
		if err := incidentJobPayload(recovered, jobID, string(events[0].Status), events[0].Reason); err != nil {
			t.Fatal(err)
		}
	}
	incidentNotificationFacts(t, copied, root.ID(), id)
	incidentCoverage(t, copied, root.ID(), map[string][]string{response: {id}})
	acks := producerSourceEvents(t, restored.jobManager, jobstore.EventJobNotificationDelivered)
	if len(acks) != 1 || acks[0].JobID != jobID {
		t.Fatalf("source custody acks=%+v", acks)
	}
	if waiting {
		data, err := readTranscriptFull(transcriptPath(copied, root.ID()), "")
		if err != nil {
			t.Fatal(err)
		}
		claims := 0
		var claimed string
		for _, entry := range data.Entries {
			if cp := entry.Turn.WaitCheckpoint; cp != nil && cp.ResumedByTurnID != "" {
				claims++
				claimed = cp.ResumedByTurnID
			}
		}
		if claims != 1 {
			t.Fatalf("resume claims=%d want1", claims)
		}
		if cut == "after resume claim before checkpoint" || cut == "after completed resume" {
			wantStatus := schema.TurnInterrupted
			if cut == "after completed resume" {
				wantStatus = schema.TurnCompleted
			}
			completions := 0
			for _, entry := range data.Entries {
				if entry.Turn.TurnID == claimed && entry.Turn.Completion != nil {
					completions++
					if entry.Turn.Completion.Status != wantStatus {
						t.Fatalf("claim completion=%s want%s", entry.Turn.Completion.Status, wantStatus)
					}
				}
			}
			if completions != 1 {
				t.Fatalf("claim completions=%d want1", completions)
			}
			restored.Close()
			again := restoreWaitingCopy(t, root, copied)
			wake, hint := newDrainWake()
			again.SetNotifyFunc(hint)
			if _, err := again.DrainJobTree(ctx, wake); err != nil {
				t.Fatal(err)
			}
			completions = 0
			for _, entry := range notificationRawEntries(t, transcriptPath(copied, root.ID())) {
				if entry.Turn.TurnID == claimed && entry.Turn.Completion != nil {
					completions++
					if entry.Turn.Completion.Status != wantStatus {
						t.Fatal("second reopen rewrote completion")
					}
				}
			}
			if completions != 1 || len(a.requestsFor("root"))-before != want {
				t.Fatalf("second reopen completions=%d new requests=%d", completions, len(a.requestsFor("root"))-before)
			}
		}
	}
	t.Logf("cut=%s reopened source=%s receiver=%s new successful requests=%d", cut, filepath.Join(jobsDir(copied, root.ID()), "jobs.jsonl"), transcriptPath(copied, root.ID()), want)
}
