package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func waitResponseFor(id, target string) llm.Response {
	return toolCallResponse(communicateCallArgs(id, map[string]any{"message": "wait-payload-71a9", "end_turn": true, "end_reason": "waiting_on_work", "waiting_on": target}))
}

// Hold a real provider boundary until the test has captured its activation.
// Callers defer release before launching work, including on assertion failures.
type waitBoundary struct {
	entered chan struct{}
	gate    chan struct{}
	release func()
}

func newWaitBoundary() *waitBoundary {
	b := &waitBoundary{entered: make(chan struct{}, 1), gate: make(chan struct{})}
	b.release = sync.OnceFunc(func() { close(b.gate) })
	return b
}

func (b *waitBoundary) hold(t *testing.T) {
	b.entered <- struct{}{}
	awaitSignal(t, b.gate, "wait activation capture")
}

func (b *waitBoundary) take(t *testing.T, returns *activationReturns, id string) *activationReturn {
	t.Helper()
	waitForTestSignal(t, b.entered, "held wait provider")
	return returns.take(t, id)
}

func assertWaitResume(t *testing.T, previous, next *activationReturn) {
	t.Helper()
	if previous == next || previous.sessionID != next.sessionID || previous.delegateID != next.delegateID || previous.lease != next.lease || previous.generationDone != next.generationDone {
		t.Fatalf("wait resume changed generation or reused activation: %+v -> %+v", previous, next)
	}
}

func waitYielded(t *testing.T, parent *Session, receipt *activationReturn, call, target string) {
	t.Helper()
	incidentYielded(t, parent, receipt.sessionID, receipt)
	sub, c := parent.getSub(receipt.sessionID), parent.delegateController
	cp, err := readWaitCheckpoint(sub.sess.TranscriptPath(), receipt.sessionID)
	if err != nil || cp == nil || cp.ExecutionTurnID == "" || cp.SessionID != receipt.sessionID || cp.DelegateID != receipt.delegateID || cp.Generation != receipt.lease.generation || cp.CommunicateCallID != call || cp.WaitingOn != target || cp.ResumedByTurnID != "" {
		t.Fatalf("wait checkpoint=%+v activation=%+v err=%v", cp, receipt, err)
	}
	sub.mu.Lock()
	released, sameDone := !sub.running && !sub.driving && !sub.finalizing, sub.done == receipt.generationDone
	sub.mu.Unlock()
	c.mu.Lock()
	live := c.live[receipt.delegateID]
	yielded := live != nil && live.binding != nil && live.binding.yielded && live.binding.lease == receipt.lease
	c.mu.Unlock()
	if !released || !sameDone || !yielded {
		t.Fatalf("unsafe wait yield: released=%v sameDone=%v yielded=%v", released, sameDone, yielded)
	}
}

func waitShellRun(t *testing.T, jm *jobManager, id string) *runningJob {
	t.Helper()
	jm.mu.Lock()
	run := jm.running[id]
	jm.mu.Unlock()
	if run == nil {
		t.Fatalf("held shell %s has no running receipt", id)
	}
	return run
}

func waitShellFinished(t *testing.T, run *runningJob) {
	t.Helper()
	waitForTestSignal(t, run.done, "real shell durable completion")
	if runningJobCompletion(run.completion.Load()) != runningJobCompletionDurable {
		t.Fatal("shell completion was not durable")
	}
}

func waitProducerFIFO(t *testing.T) (string, func()) {
	t.Helper()
	fifo := filepath.Join(t.TempDir(), "release")
	if out, err := exec.Command("mkfifo", fifo).CombinedOutput(); err != nil {
		t.Fatalf("mkfifo: %v %s", err, out)
	}
	f, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return fifo, sync.OnceFunc(func() { _, _ = f.WriteString("release\n") })
}

func TestFreshRootInputSupersedesFailedWaitCheckpoint(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter()
	adapter.script("root", func(llm.Request) llm.Response { return waitResponseFor("wait-failed-84", "") })
	root := newMessagingRoot(t, adapter, nil)
	if _, err := root.ProcessInput(context.Background(), "input-old-84", nil); err != nil {
		t.Fatal(err)
	}
	path := transcriptPath(root.stateDir, root.ID())
	checkpoint, err := readWaitCheckpoint(path, root.ID())
	if err != nil || checkpoint == nil {
		t.Fatalf("wait checkpoint=%+v err=%v", checkpoint, err)
	}
	// Persist the late failed completion at the existing wait boundary. This
	// historical evidence must forbid resuming the old execution, not new input.
	if _, err := root.recordTranscriptOnlyThrough(schema.Turn{
		Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnFailed},
	}, transcript.DoorSynced, transcript.PlaceInTurn(checkpoint.ExecutionTurnID)); err != nil {
		t.Fatal(err)
	}
	stateDir, client, profile, id := root.stateDir, root.client, root.profile, root.ID()
	root.Close()
	root = nil // No resident runtime or writer survives into restore.
	meta, err := schema.LoadSessionMeta(stateDir, id)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := RestoreSessionFromMetaWithConfig(client, profile, execenv.NewLocalExecutionEnvironment(meta.EnvInfo.WorkingDir), meta, RestoreSessionConfig{
		StateDir: stateDir, ForceRealIO: true, clock: agenttest.NewFakeClock(),
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	before, err := readTranscriptFull(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if safeWaitBoundary(*checkpoint, before.Entries, reopened.resultToolName()) {
		t.Fatal("failed execution became safely resumable")
	}
	if err := reopened.claimWaitResumption(checkpoint.ExecutionTurnID); err == nil {
		t.Fatal("failed execution identity was reusable as a wait resumption")
	}
	adapter.script("root", func(req llm.Request) llm.Response {
		seen := 0
		for _, msg := range req.Messages {
			if msg.Role == llm.RoleUser && msg.Text() == "input-new-84" {
				seen++
			}
		}
		if seen != 1 {
			t.Errorf("fresh input presentations=%d, want 1", seen)
		}
		return finalResponse("reply-new-84")
	})
	reopened.SetClientMutationStartWakeFunc(func() {})
	accepted, err := reopened.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "client-new-84", ExpectedInstanceID: id,
		Input: []appwire.InputItem{{Type: "text", Text: "input-new-84"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := reopened.ProcessClientMutationStart(context.Background(), nil); err != nil {
		t.Fatalf("fresh root input refused: %v", err)
	}
	if got := len(adapter.requestsFor("root")); got != 2 {
		t.Fatalf("scripted requests=%d, want old wait and fresh input", got)
	}
	data, err := readTranscriptFull(path, "")
	if err != nil {
		t.Fatal(err)
	}
	inputs, completions := 0, 0
	for _, entry := range data.Entries[len(before.Entries):] {
		turn := entry.Turn
		if turn.TurnID == checkpoint.ExecutionTurnID || len(turn.ObservedNotificationIDs) != 0 || turn.AttentionResolution != nil {
			t.Fatalf("fresh execution reused failed identity or fabricated observation: %+v", turn)
		}
		if cp := turn.WaitCheckpoint; cp != nil {
			t.Fatalf("failed wait was claimed instead of superseded: %+v", cp)
		}
		if turn.Kind == schema.TurnUserInput {
			inputs++
			if turn.ClientMutationID != "client-new-84" || turn.Message.Text() != "input-new-84" || turn.TurnID != accepted.Turn.ID {
				t.Fatalf("fresh input identity lost: %+v", turn)
			}
		}
		if turn.Kind == schema.TurnCompletion && turn.TurnID == accepted.Turn.ID && turn.Completion != nil && turn.Completion.Status == schema.TurnCompleted {
			completions++
		}
	}
	if inputs != 1 || completions != 1 || accepted.Turn.ID == checkpoint.ExecutionTurnID {
		t.Fatalf("fresh inputs=%d completions=%d turn=%s failed=%s", inputs, completions, accepted.Turn.ID, checkpoint.ExecutionTurnID)
	}
}

func TestFailedWaitSupersessionRequiresSettledBoundary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		before  []schema.Turn
		status  schema.TurnCompletionStatus
		otherID bool
		unknown string
		admit   bool
	}{
		{name: "settled failure", status: schema.TurnFailed, admit: true},
		{name: "failure marker then settled failure", before: []schema.Turn{{Kind: schema.TurnFailure}}, status: schema.TurnFailed, admit: true},
		{name: "unfinished tool before failed completion", before: []schema.Turn{{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "unfinished-84", Name: "read_file"}}}}}}, status: schema.TurnFailed},
		{name: "failure without completion", before: []schema.Turn{{Kind: schema.TurnFailure}}},
		{name: "another execution failed", status: schema.TurnFailed, otherID: true},
		{name: "interrupted completion", status: schema.TurnInterrupted},
		{name: "missing evidence", status: schema.TurnFailed, unknown: "missing"},
		{name: "incomplete evidence", status: schema.TurnFailed, unknown: "truncated"},
		{name: "unavailable durability barrier", status: schema.TurnFailed, unknown: "closed writer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter := newMessagingAdapter()
			adapter.script("root", func(llm.Request) llm.Response { return waitResponseFor("wait-boundary-84", "") })
			root := newMessagingRoot(t, adapter, nil)
			if _, err := root.ProcessInput(context.Background(), "boundary-input-84", nil); err != nil {
				t.Fatal(err)
			}
			path := transcriptPath(root.stateDir, root.ID())
			checkpoint, err := readWaitCheckpoint(path, root.ID())
			if err != nil || checkpoint == nil {
				t.Fatalf("checkpoint=%+v err=%v", checkpoint, err)
			}
			record := func(turn schema.Turn, id string) {
				t.Helper()
				if _, err := root.recordTranscriptOnlyThrough(turn, transcript.DoorSynced, transcript.PlaceInTurn(id)); err != nil {
					t.Fatal(err)
				}
			}
			for _, turn := range tc.before {
				record(turn, checkpoint.ExecutionTurnID)
			}
			if tc.status != "" {
				id := checkpoint.ExecutionTurnID
				if tc.otherID {
					id = "execution-other-84"
				}
				record(schema.Turn{Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: tc.status}}, id)
			}
			switch tc.unknown {
			case "missing":
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = os.Rename(path+".saved", path) }()
			case "truncated":
				file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.WriteString(`{"kind":"entry","turn":`); err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			case "closed writer":
				if err := root.attachedTranscript().Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := root.claimWaitResumption("execution-fresh-84"); (err == nil) != tc.admit {
				t.Fatalf("fresh claim error=%v, want admission=%v", err, tc.admit)
			}
			if tc.unknown == "" {
				retained, err := readWaitCheckpoint(path, root.ID())
				if err != nil || retained == nil || *retained != *checkpoint {
					t.Fatalf("claim erased or resumed failed/unsafe checkpoint: %+v, %v", retained, err)
				}
			}
		})
	}
}

func TestNotificationResumesYieldedWait(t *testing.T) {
	t.Parallel()
	for _, producer := range []string{"child", "shell"} {
		t.Run(producer, func(t *testing.T) {
			adapter := newMessagingAdapter("WAIT-OWNER-71", "WAIT-WORKER-71")
			returns := newActivationReturns()
			initialBoundary, terminalBoundary := newWaitBoundary(), newWaitBoundary()
			defer initialBoundary.release()
			defer terminalBoundary.release()
			gate := make(chan struct{})
			release := sync.OnceFunc(func() { close(gate) })
			defer release()
			var target string
			resumed := make(chan llm.Request, 1)
			adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("owner", "WAIT-OWNER-71") }, func(llm.Request) llm.Response { return finalResponse("root-71") })
			if producer == "child" {
				adapter.script("WAIT-OWNER-71", func(llm.Request) llm.Response { return delegateToolResponse("worker", "WAIT-WORKER-71") })
				adapter.script("WAIT-WORKER-71", func(llm.Request) llm.Response {
					awaitSignal(t, gate, "worker release")
					return finalResponse("worker-payload-71")
				})
			} else {
				fifo, releaseShell := waitProducerFIFO(t)
				adapter.script("WAIT-OWNER-71", func(llm.Request) llm.Response {
					args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("read value < %q", fifo), "mode": "background"})
					return toolCallResponse(llm.ToolCallData{ID: "shell-71", Name: "shell", Arguments: args, Type: "function"})
				})
				bridgeDone := make(chan struct{})
				go func() {
					defer close(bridgeDone)
					<-gate
					releaseShell()
				}()
				defer func() { release(); waitForTestSignal(t, bridgeDone, "shell release bridge") }()
			}
			adapter.script("WAIT-OWNER-71", func(req llm.Request) llm.Response {
				if producer == "child" {
					target = deferredHeadDelegateID.FindString(lastToolResultText(req.Messages))
				} else {
					target = regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages))
				}
				if target == "" {
					t.Error("missing real producer identity")
				}
				initialBoundary.hold(t)
				return waitResponseFor("wait-71", target)
			}, func(req llm.Request) llm.Response {
				resumed <- req
				terminalBoundary.hold(t)
				return finalResponse("resumed-result-71")
			})
			root := newMessagingRoot(t, adapter, returns.configure)
			t.Cleanup(func() { returns.close(t, root) })
			claimFenced := make(chan bool, 1)
			root.SetDescendantRecordedFunc(func(_ string, rec transcript.Record) {
				if cp := rec.Turn.WaitCheckpoint; cp != nil && cp.ResumedByTurnID != "" {
					unlocked := root.delegateController.mu.TryLock()
					if unlocked {
						root.delegateController.mu.Unlock()
					}
					claimFenced <- !unlocked
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if _, err := root.ProcessInput(ctx, "start-71", nil); err != nil {
				t.Fatal(err)
			}
			adapter.waitForRequests(t, "WAIT-OWNER-71", 2)
			owner := root.subagents.sessions()[0]
			sub := root.getSub(owner.ID())
			t.Cleanup(func() {
				if t.Failed() {
					sub.mu.Lock()
					t.Logf("sub running=%v err=%v result=%s", sub.running, sub.err, sub.result)
					sub.mu.Unlock()
					owner.mu.Lock()
					t.Logf("comm=%+v execution=%+v", owner.comm, owner.execution)
					owner.mu.Unlock()
				}
			})
			// No producer can finish until release. The old polling loop never releases
			// sub.running or its model capacity here.
			initial := initialBoundary.take(t, returns, owner.ID())
			initialBoundary.release()
			waitYielded(t, root, initial, "wait-71", target)
			checkpoint, err := readWaitCheckpoint(transcriptPath(root.stateDir, owner.ID()), owner.ID())
			if err != nil || checkpoint == nil || checkpoint.ResumedByTurnID != "" || checkpoint.CommunicateCallID != "wait-71" {
				t.Fatalf("yield checkpoint=%+v err=%v", checkpoint, err)
			}
			generation := delegateAggregateSnapshot(t, root.delegateController, owner.owningDelegateID)
			if generation.Generation != 1 || !generation.CurrentRunOpen || generation.Phase != delegatestore.PhaseRunning || generation.LatestOutcome != nil {
				t.Fatalf("waiting generation=%+v", generation)
			}
			for _, event := range publicationEvents(t, root) {
				if event.DelegateID == owner.owningDelegateID && event.RunFinished != nil {
					t.Fatal("yield wrote RunFinished")
				}
			}
			pending, err := root.pendingNotificationIDs()
			if err != nil {
				t.Fatal(err)
			}
			data, err := readTranscriptFull(transcriptPath(root.stateDir, root.ID()), "")
			if err != nil {
				t.Fatal(err)
			}
			if err := yieldedWaitCustodyError(publicationEvents(t, root), data.Entries, adapter.requestsFor("root"), pending, owner.owningDelegateID, target); err != nil {
				t.Fatal(err)
			}
			root.delegateController.mu.Lock()
			active := root.delegateController.turnsInUse
			root.delegateController.mu.Unlock()
			want := 0
			if producer == "child" {
				want = 1
			}
			if active != want {
				t.Fatalf("active model turns=%d want=%d", active, want)
			}
			release()
			waitForTestSignal(t, terminalBoundary.entered, "producer resumed yielded generation")
			<-resumed
			terminal := returns.take(t, owner.ID())
			assertWaitResume(t, initial, terminal)
			if !<-claimFenced {
				t.Fatal("resume checkpoint append escaped the original lease fence")
			}
			terminalBoundary.release()
			incidentSettled(t, root, owner.ID(), terminal)
			final := delegateAggregateSnapshot(t, root.delegateController, owner.owningDelegateID)
			if final.Generation != 1 {
				t.Fatalf("resumption created generation %d", final.Generation)
			}
			// Check the raw private resumption record, not a status projection.
			claimed, err := readWaitCheckpoint(transcriptPath(root.stateDir, owner.ID()), owner.ID())
			if err != nil || claimed == nil || claimed.ResumedByTurnID == "" || claimed.ResumedByTurnID == checkpoint.ExecutionTurnID {
				t.Fatalf("resume claim=%+v err=%v", claimed, err)
			}
		})
	}
}

// Pending is a presentation state, not a custody receipt: the root's original
// second request can already have observed and consumed the wait publication.
func yieldedWaitCustodyError(events []delegatestore.Event, entries []transcript.Entry, requests []llm.Request, pending []string, owner, target string) error {
	var publication delegatestore.Event
	publications := 0
	for _, event := range events {
		if event.DelegateID == owner && event.UpdatePosted != nil && event.UpdatePosted.Communicate != nil && event.UpdatePosted.Communicate.CallID == "wait-71" {
			publication = event
			publications++
		}
	}
	if publications != 1 {
		return fmt.Errorf("wait publications=%d, want one for owner=%s", publications, owner)
	}
	update := publication.UpdatePosted
	comm := update.Communicate
	if update.Generation != 1 || update.Message != "wait-payload-71a9" || comm.Message != update.Message || !comm.EndTurn || comm.EndReason != "waiting_on_work" || target == "" || comm.WaitingOn != target || update.Packet == nil {
		return fmt.Errorf("wait publication tuple=%+v communicate=%+v target=%s", update, comm, target)
	}
	var payload string
	var metadata delegatestore.UpdateMetadata
	if err := json.Unmarshal(update.Packet.Message, &payload); err != nil {
		return err
	}
	if err := json.Unmarshal(update.Packet.Metadata, &metadata); err != nil {
		return err
	}
	if update.Packet.Kind != delegatestore.PacketUpdate || payload != update.Message || metadata != (delegatestore.UpdateMetadata{CallID: "wait-71", EndTurn: true, Disposition: "waiting_on_work", WaitingOn: target}) {
		return fmt.Errorf("wait source packet=%+v metadata=%+v", update.Packet, metadata)
	}
	deliveryID := fmt.Sprintf("%s/update/%d", owner, publication.Seq)
	id := "delegate:" + deliveryID
	acks := 0
	for _, event := range events {
		if event.DelegateID == owner && event.Seq > publication.Seq && event.DeliveryAcknowledged != nil && event.DeliveryAcknowledged.DeliveryID == deliveryID {
			acks++
		}
	}
	if acks != 1 {
		return fmt.Errorf("wait source acknowledgements=%d for %s", acks, deliveryID)
	}
	accepted, acceptedAt := 0, -1
	var message llm.Message
	for i, entry := range entries {
		if entry.Turn.AttentionID == id {
			accepted++
			acceptedAt, message = i, entry.Turn.Message
		}
	}
	if accepted != 1 || message.Role != llm.RoleUser {
		return fmt.Errorf("wait receiver acceptances=%d for %s", accepted, id)
	}
	var frame struct {
		XMLName  xml.Name
		Delegate string `xml:"delegate_id,attr"`
		Kind     string `xml:"kind,attr"`
		Packet   string `xml:",chardata"`
	}
	if err := xml.Unmarshal([]byte(message.Text()), &frame); err != nil {
		return err
	}
	packet, err := json.Marshal(update.Packet)
	if err != nil {
		return err
	}
	var sourcePacket, receiverPacket any
	if err := json.Unmarshal(packet, &sourcePacket); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(frame.Packet), &receiverPacket); err != nil {
		return err
	}
	if frame.XMLName.Local != "delegate-notification" || frame.Delegate != owner || frame.Kind != "update" || !reflect.DeepEqual(sourcePacket, receiverPacket) {
		return fmt.Errorf("wait receiver packet mismatch for %s", id)
	}
	if slices.Contains(pending, id) {
		return nil
	}
	return yieldedWaitConsumptionError(entries, requests, id, acceptedAt, message)
}

func yieldedWaitConsumptionError(entries []transcript.Entry, requests []llm.Request, id string, acceptedAt int, message llm.Message) error {
	// There are exactly two original root requests. Match coverage to the second
	// successful raw response, not an observed flag or a later unrelated turn.
	if len(requests) != 2 {
		return fmt.Errorf("consumed wait root requests=%d, want two", len(requests))
	}
	for i, request := range requests {
		covered := 0
		for _, msg := range request.Messages {
			if reflect.DeepEqual(msg, message) {
				covered++
			}
		}
		if covered != i {
			return fmt.Errorf("wait request=%d packet coverage=%d, want %d", i+1, covered, i)
		}
	}
	var responses []schema.Turn
	observedAt, consumedAt, consumed := -1, -1, 0
	for i, entry := range entries {
		turn := entry.Turn
		if turn.Kind == schema.TurnAssistant {
			responses = append(responses, turn)
			if slices.Contains(turn.ObservedNotificationIDs, id) {
				observedAt = i
			}
		}
		if resolution := turn.AttentionResolution; resolution != nil && resolution.AttentionID == id {
			if resolution.Disposition != "consumed" {
				return fmt.Errorf("wait resolution=%+v", resolution)
			}
			consumed++
			consumedAt = i
		}
	}
	if len(responses) != 2 || responses[0].TurnID == "" || responses[0].TurnID != responses[1].TurnID || slices.Contains(responses[0].ObservedNotificationIDs, id) || !slices.Equal(responses[1].ObservedNotificationIDs, []string{id}) || acceptedAt >= observedAt || observedAt >= consumedAt || consumed != 1 {
		return fmt.Errorf("wait successful receipts=%+v accepted=%d observed=%d consumed=%d count=%d", responses, acceptedAt, observedAt, consumedAt, consumed)
	}
	return nil
}

func TestParentSteeringResumesYieldedWait(t *testing.T) {
	for _, cold := range []bool{false, true} {
		t.Run(fmt.Sprintf("cold_%v", cold), func(t *testing.T) {
			adapter := newMessagingAdapter("STEER-WAIT-93")
			returns := newActivationReturns()
			initialBoundary := newWaitBoundary()
			defer initialBoundary.release()
			fifo, releaseShell := waitProducerFIFO(t)
			defer releaseShell()
			resumeEntered := make(chan struct{})
			resumed, modelGate := make(chan llm.Request, 1), make(chan struct{})
			releaseModel := sync.OnceFunc(func() { close(modelGate) })
			defer releaseModel()
			adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("steer-owner-93", "STEER-WAIT-93") }, func(llm.Request) llm.Response { return finalResponse("root-93") })
			adapter.script("STEER-WAIT-93", func(llm.Request) llm.Response {
				args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("read value < %q", fifo), "mode": "background"})
				return toolCallResponse(llm.ToolCallData{ID: "steer-shell-93", Name: "shell", Type: "function", Arguments: args})
			}, func(req llm.Request) llm.Response {
				initialBoundary.hold(t)
				return waitResponseFor("steer-wait-93", regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages)))
			}, func(req llm.Request) llm.Response {
				resumed <- req
				close(resumeEntered)
				<-modelGate
				return finalResponse("steering-answered-93")
			})
			root := newMessagingRoot(t, adapter, returns.configure)
			t.Cleanup(func() { returns.close(t, root) })
			if _, err := root.ProcessInput(context.Background(), "start-93", nil); err != nil {
				t.Fatal(err)
			}
			owner := root.subagents.sessions()[0]
			adapter.waitForRequests(t, "STEER-WAIT-93", 2)
			initial := initialBoundary.take(t, returns, owner.ID())
			initialBoundary.release()
			incidentYielded(t, root, owner.ID(), initial)
			cp, err := readWaitCheckpoint(owner.TranscriptPath(), owner.ID())
			if err != nil || cp == nil {
				t.Fatalf("steering checkpoint=%+v err=%v", cp, err)
			}
			waitYielded(t, root, initial, "steer-wait-93", cp.WaitingOn)
			assertNotificationPending(t, owner, []string{})
			receiver := root
			receiverReturns := returns
			var deny atomic.Bool
			if cold {
				receiverReturns = newActivationReturns()
				copyDir := copyWaitingState(t, root.stateDir)
				meta, err := schema.LoadSessionMeta(copyDir, root.ID())
				if err != nil {
					t.Fatal(err)
				}
				deny.Store(true)
				receiver, err = RestoreSessionFromMetaWithConfig(root.client, root.profile, execenv.NewLocalExecutionEnvironment(meta.EnvInfo.WorkingDir), meta, RestoreSessionConfig{
					StateDir: copyDir, ForceRealIO: true, clock: agenttest.NewFakeClock(),
					AcquireSessionOwnership: func(id string) error {
						if deny.Load() && id == owner.ID() {
							return errors.New("fixture ownership unavailable")
						}
						return nil
					},
					testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, disableDelegateIdleRelease: true, subagentActivationReturn: receiverReturns.capture},
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { receiverReturns.close(t, receiver) })
				if receiver.subagentForChild(owner.ID()) != nil {
					t.Fatal("input fixture is not cold")
				}
				deny.Store(false)
			}
			adapter.script("root", func(llm.Request) llm.Response {
				return delegateSendResponse("parent-steer-93", owner.owningDelegateID, "actual-parent-steering-93", 0)
			}, func(req llm.Request) llm.Response {
				var result delegateSendResult
				for _, msg := range req.Messages {
					for _, part := range msg.Content {
						if tr := part.ToolResult; tr != nil && tr.ToolCallID == "parent-steer-93" {
							if err := json.Unmarshal(tr.ToolState, &result); err != nil {
								t.Error(err)
							}
						}
					}
				}
				if result.Action != "steered" || result.DelegateID != owner.owningDelegateID {
					t.Errorf("real delegate_send result=%+v, want steered", result)
				}
				return finalResponse("sent-93")
			})
			if _, err := receiver.ProcessInput(context.Background(), "steer-93", nil); err != nil {
				t.Fatal(err)
			}
			waitForTestSignal(t, resumeEntered, "accepted parent steering resumed before FIFO release")
			req := <-resumed
			terminal := receiverReturns.take(t, owner.ID())
			if cold {
				if terminal.sessionID != initial.sessionID || terminal.delegateID != initial.delegateID || terminal.lease != initial.lease {
					t.Fatalf("cold steering lease changed: %+v -> %+v", initial, terminal)
				}
			} else {
				assertWaitResume(t, initial, terminal)
			}
			{
				seen := 0
				for _, msg := range req.Messages {
					seen += strings.Count(msg.Text(), "actual-parent-steering-93")
				}
				if seen != 1 {
					t.Fatalf("actual steering in resumed request=%d, want1", seen)
				}
			}
			c := receiver.delegateController
			run := delegateAggregateSnapshot(t, c, owner.owningDelegateID)
			if run.Generation != 1 || !run.CurrentRunOpen || run.LatestOutcome != nil || c.generationOwesSteering(owner.owningDelegateID) {
				t.Fatalf("steering not consumed in original open generation: %+v", run)
			}
			data, err := readTranscriptFull(transcriptPath(receiver.stateDir, owner.ID()), "")
			if err != nil {
				t.Fatal(err)
			}
			steers, claims, notifications := 0, 0, 0
			var recoveryIDs []string
			for _, entry := range data.Entries {
				if entry.Turn.Kind == schema.TurnSteering && entry.Turn.SteeringSource == events.SteeringSourceParent && strings.Count(entry.Turn.Message.Text(), "actual-parent-steering-93") == 1 && entry.Turn.StableTurnID != "" {
					steers++
				}
				if cp := entry.Turn.WaitCheckpoint; cp != nil && cp.ResumedByTurnID != "" {
					claims++
				}
				if entry.Turn.AttentionID != "" {
					notifications++
					recoveryIDs = append(recoveryIDs, entry.Turn.AttentionID)
				}
			}
			if steers != 1 || claims != 1 || !cold && notifications != 0 {
				t.Fatalf("durable steers=%d claims=%d notifications=%d", steers, claims, notifications)
			}
			if got := len(adapter.requestsFor("STEER-WAIT-93")); got != 3 {
				t.Fatalf("steering executions before FIFO release=%d want3 total", got)
			}
			if cold {
				t.Logf("cold missing-shell recovery IDs=%v, this is not a steering-only cold wake proof", recoveryIDs)
			}
			releaseShell()
			releaseModel()
			incidentSettled(t, receiver, owner.ID(), terminal)
		})
	}
}

func TestAlreadyObservedTargetCannotStrandWait(t *testing.T) {
	t.Parallel()
	t.Run("completion after validation", testWaitTargetCompletesBeforeCheckpoint)
	for _, producer := range []string{"child", "shell"} {
		t.Run(producer, func(t *testing.T) {
			adapter := newMessagingAdapter("STALE-WORKER-19")
			returns := newActivationReturns()
			var target string
			gate := make(chan struct{})
			release := sync.OnceFunc(func() { close(gate) })
			defer release()
			if producer == "child" {
				adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("worker-19", "STALE-WORKER-19") }, func(req llm.Request) llm.Response {
					target = deferredHeadDelegateID.FindString(lastToolResultText(req.Messages))
					return finalResponse("initial-19")
				})
				adapter.script("STALE-WORKER-19", func(llm.Request) llm.Response { <-gate; return finalResponse("terminal-payload-19") })
			} else {
				fifo, releaseShell := waitProducerFIFO(t)
				bridgeDone := make(chan struct{})
				go func() { defer close(bridgeDone); <-gate; releaseShell() }()
				defer func() { release(); waitForTestSignal(t, bridgeDone, "stale-target shell release bridge") }()
				adapter.script("root", func(llm.Request) llm.Response {
					args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("read value < %q", fifo), "mode": "background"})
					return toolCallResponse(llm.ToolCallData{ID: "shell-19", Name: "shell", Arguments: args, Type: "function"})
				}, func(req llm.Request) llm.Response {
					target = regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages))
					return finalResponse("initial-19")
				})
			}
			root := newMessagingRoot(t, adapter, returns.configure)
			t.Cleanup(func() { returns.close(t, root) })
			if _, err := root.ProcessInput(context.Background(), "start-19", nil); err != nil {
				t.Fatal(err)
			}
			var childReceipt *activationReturn
			var shellReceipt *runningJob
			if producer == "child" {
				childReceipt = returns.take(t, root.subagents.sessions()[0].ID())
			} else {
				shellReceipt = waitShellRun(t, root.jobManager, target)
			}
			release()
			if childReceipt != nil {
				incidentSettled(t, root, childReceipt.sessionID, childReceipt)
			} else {
				waitShellFinished(t, shellReceipt)
			}
			if ids, err := root.pendingNotificationIDs(); err != nil || len(ids) != 1 {
				t.Fatalf("real producer custody=%v err=%v", ids, err)
			}
			adapter.script("root", func(llm.Request) llm.Response { return finalResponse("observed-19") })
			if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			assertNotificationPending(t, root, []string{})
			// Completion has been durably observed before attempting to reuse it.
			adapter.script("root", func(llm.Request) llm.Response { return waitResponseFor("stale-wait-19", target) }, func(req llm.Request) llm.Response {
				var found *llm.ToolResultData
				for _, msg := range req.Messages {
					for _, part := range msg.Content {
						if part.ToolResult != nil && part.ToolResult.ToolCallID == "stale-wait-19" {
							found = part.ToolResult
						}
					}
				}
				if found == nil || found.IsError {
					t.Errorf("stale wait is not an ordinary refused result: %+v", found)
				} else {
					var result struct {
						Accepted    bool            `json:"accepted"`
						Reason      string          `json:"reason"`
						TargetState json.RawMessage `json:"target_state"`
					}
					if err := json.Unmarshal([]byte(fmt.Sprint(found.Content)), &result); err != nil || result.Accepted || result.Reason != "stale_wait_target" || len(result.TargetState) == 0 {
						t.Errorf("stale target state=%s err=%v", found.Content, err)
					}
					if producer == "child" {
						var state struct {
							Phase      string `json:"phase"`
							Generation int    `json:"generation"`
							Outcome    struct {
								Status string `json:"status"`
							} `json:"outcome"`
						}
						if err := json.Unmarshal(result.TargetState, &state); err != nil || state.Phase != "idle" || state.Generation != 1 || state.Outcome.Status != "completed" {
							t.Errorf("actual child terminal state=%s err=%v", result.TargetState, err)
						}
					} else {
						var state struct {
							JobID  string `json:"job_id"`
							Status string `json:"status"`
						}
						if err := json.Unmarshal(result.TargetState, &state); err != nil || state.JobID != target || state.Status != "completed" {
							t.Errorf("actual shell terminal state=%s err=%v", result.TargetState, err)
						}
					}
				}
				return finalResponse("refused-19")
			})
			if _, err := root.ProcessInput(context.Background(), "try-wait-19", nil); err != nil {
				t.Fatal(err)
			}
			checkpoint, err := readWaitCheckpoint(transcriptPath(root.stateDir, root.ID()), root.ID())
			if err != nil || checkpoint != nil {
				t.Fatalf("stale wait checkpoint=%+v err=%v", checkpoint, err)
			}
			assertNotificationPending(t, root, []string{})
			data, err := readTranscriptFull(transcriptPath(root.stateDir, root.ID()), "")
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range data.Entries {
				if entry.Turn.Communicate != nil && entry.Turn.Communicate.CallID == "stale-wait-19" {
					t.Fatal("refused wait was published")
				}
			}
		})
	}
}

func testWaitTargetCompletesBeforeCheckpoint(t *testing.T) {
	adapter := newMessagingAdapter("RACE-WAIT-26", "RACE-CHILD-26")
	returns := newActivationReturns()
	terminalBoundary := newWaitBoundary()
	defer terminalBoundary.release()
	gate, recorded, checkpointGate := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	unblock := sync.OnceFunc(func() { close(checkpointGate) })
	defer release()
	defer unblock()
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("race-owner", "RACE-WAIT-26") }, func(llm.Request) llm.Response { return finalResponse("root-26") })
	adapter.script("RACE-WAIT-26", func(llm.Request) llm.Response { return delegateToolResponse("race-child", "RACE-CHILD-26") }, func(req llm.Request) llm.Response {
		return waitResponseFor("race-wait-26", deferredHeadDelegateID.FindString(lastToolResultText(req.Messages)))
	}, func(llm.Request) llm.Response { terminalBoundary.hold(t); return finalResponse("race-result-26") })
	adapter.script("RACE-CHILD-26", func(llm.Request) llm.Response { <-gate; return finalResponse("race-child-result-26") })
	root := newMessagingRoot(t, adapter, returns.configure)
	t.Cleanup(func() { returns.close(t, root) })
	root.SetDescendantRecordedFunc(func(_ string, rec transcript.Record) {
		if rec.Turn.Communicate != nil && rec.Turn.Communicate.CallID == "race-wait-26" {
			close(recorded)
			<-checkpointGate
		}
	})
	if _, err := root.ProcessInput(context.Background(), "start-26", nil); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, recorded, "accepted wait publication before checkpoint")
	owner := root.subagents.sessions()[0]
	child := owner.subagents.sessions()[0]
	initial := returns.take(t, owner.ID())
	childReceipt := returns.take(t, child.ID())
	targetSettled := make(chan struct{})
	settledChild := make(chan *subagent, 1)
	updateSessionTestConfig(child, func(cfg *testConfig) {
		cfg.subagentBeforeGenerationAnnounced = func(a *subagent) { settledChild <- a; close(targetSettled) }
	})
	release()
	// Owner publication holds its append lock, so result delivery cannot finish
	// until unblock. This hook follows durable FinishGeneration, the original oracle.
	waitForTestSignal(t, targetSettled, "target durable settlement before checkpoint")
	if got := <-settledChild; got != owner.getSub(child.ID()) || got.sess.ID() != childReceipt.sessionID || got.sess.owningDelegateID != childReceipt.delegateID {
		t.Fatal("settlement signal belongs to another child")
	}
	if run := delegateAggregateSnapshot(t, root.delegateController, child.owningDelegateID); run.CurrentRunOpen || run.Generation != childReceipt.lease.generation {
		t.Fatalf("target settlement before checkpoint=%+v", run)
	}
	cp, err := readWaitCheckpoint(transcriptPath(root.stateDir, owner.ID()), owner.ID())
	if err != nil || cp != nil {
		t.Fatalf("checkpoint preceded target completion: %+v %v", cp, err)
	}
	unblock()
	incidentSettled(t, owner, child.ID(), childReceipt)
	waitForTestSignal(t, terminalBoundary.entered, "race accepted wait third response")
	terminal := returns.take(t, owner.ID())
	assertWaitResume(t, initial, terminal)
	terminalBoundary.release()
	incidentSettled(t, root, owner.ID(), terminal)
	if len(adapter.requestsFor("RACE-WAIT-26")) != 3 {
		t.Fatal("completion lost between validation and checkpoint")
	}
	cp, err = readWaitCheckpoint(transcriptPath(root.stateDir, owner.ID()), owner.ID())
	if err != nil || cp == nil || cp.ResumedByTurnID == "" {
		t.Fatalf("accepted race wait not claimed: %+v %v", cp, err)
	}
}

// Copy only this test's generated state, never alter a live writer's input.
func copyWaitingState(t *testing.T, source string) string {
	t.Helper()
	dest := t.TempDir()
	if err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		to := filepath.Join(dest, rel)
		if entry.IsDir() {
			return os.MkdirAll(to, 0o700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(to, data, 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	return dest
}

func restoreWaitingCopy(t *testing.T, root *Session, stateDir string) *Session {
	t.Helper()
	meta, err := schema.LoadSessionMeta(stateDir, root.ID())
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreSessionFromMetaWithConfig(root.client, root.profile, execenv.NewLocalExecutionEnvironment(meta.EnvInfo.WorkingDir), meta, RestoreSessionConfig{
		StateDir: stateDir, ForceRealIO: true,
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, disableDelegateIdleRelease: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restored.Close)
	return restored
}

func restoreWaitingJoinCopy(t *testing.T, root *Session, stateDir string, returns *activationReturns) *Session {
	t.Helper()
	meta, err := schema.LoadSessionMeta(stateDir, root.ID())
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreSessionFromMetaWithConfig(root.client, root.profile, execenv.NewLocalExecutionEnvironment(meta.EnvInfo.WorkingDir), meta, RestoreSessionConfig{
		StateDir: stateDir, ForceRealIO: true,
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, disableDelegateIdleRelease: true, subagentActivationReturn: returns.capture},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { returns.close(t, restored) })
	return restored
}

func TestWaitCheckpointBeforeCompletionMarkerReopen(t *testing.T) {
	adapter := newMessagingAdapter()
	adapter.script("root", func(llm.Request) llm.Response { return waitResponseFor("root-wait-88", "") })
	root := newMessagingRoot(t, adapter, nil)
	if _, err := root.ProcessInput(context.Background(), "root-wait-88", nil); err != nil {
		t.Fatal(err)
	}
	cp, err := readWaitCheckpoint(transcriptPath(root.stateDir, root.ID()), root.ID())
	if err != nil || cp == nil {
		t.Fatalf("real root wait checkpoint=%+v err=%v", cp, err)
	}
	copyDir := copyWaitingState(t, root.stateDir)
	path := transcriptPath(copyDir, root.ID())
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cut []byte
	for _, line := range bytes.SplitAfter(raw, []byte{'\n'}) {
		cut = append(cut, line...)
		entry, err := transcript.DecodeEntry(bytes.TrimSpace(line))
		if err == nil && entry.Turn.WaitCheckpoint != nil {
			break
		}
	}
	if err := os.WriteFile(path, cut, 0o600); err != nil {
		t.Fatal(err)
	}
	restored := restoreWaitingCopy(t, root, copyDir)
	data, err := readTranscriptFull(path, "")
	if err != nil {
		t.Fatal(err)
	}
	var status schema.TurnCompletionStatus
	for _, entry := range data.Entries {
		if entry.Turn.TurnID == cp.ExecutionTurnID && entry.Turn.Completion != nil {
			status = entry.Turn.Completion.Status
		}
	}
	if status != schema.TurnCompleted {
		t.Fatalf("safe wait accounting repaired as %q, want completed", status)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	adapter.script("root", func(llm.Request) llm.Response { close(entered); <-release; return finalResponse("resumed-88") })
	done := make(chan error, 1)
	go func() { _, err := restored.ProcessInput(context.Background(), "resume-88", nil); done <- err }()
	awaitSignal(t, entered, "resumed model request")
	claimed, err := readWaitCheckpoint(path, root.ID())
	if err != nil || claimed.ResumedByTurnID == "" {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	interruptedCopy := copyWaitingState(t, copyDir)
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_ = restoreWaitingCopy(t, root, interruptedCopy)
	data, err = readTranscriptFull(transcriptPath(interruptedCopy, root.ID()), "")
	if err != nil {
		t.Fatal(err)
	}
	status = ""
	for _, entry := range data.Entries {
		if entry.Turn.TurnID == claimed.ResumedByTurnID && entry.Turn.Completion != nil {
			status = entry.Turn.Completion.Status
		}
	}
	if status != schema.TurnInterrupted {
		t.Fatalf("resumed activation crash repaired as %q, want interrupted", status)
	}
}

func TestMissingWaitProducerNotifiesOnce(t *testing.T) {
	adapter := newMessagingAdapter("COLD-WAIT-37")
	returns := newActivationReturns()
	initialBoundary := newWaitBoundary()
	defer initialBoundary.release()
	fifo, releaseShell := waitProducerFIFO(t)
	defer releaseShell()
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("cold-owner", "COLD-WAIT-37") }, func(llm.Request) llm.Response { return finalResponse("root-37") })
	adapter.script("COLD-WAIT-37", func(llm.Request) llm.Response {
		return toolCallResponse(llm.ToolCallData{ID: "unfinished-37", Name: "task_list", Type: "function", Arguments: json.RawMessage(`{"add":[{"type":"implement","description":"unfinished task","prompt":"leave this incomplete"}]}`)})
	}, func(llm.Request) llm.Response {
		args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("read value < %q", fifo), "mode": "background"})
		return toolCallResponse(llm.ToolCallData{ID: "shell-37", Name: "shell", Type: "function", Arguments: args})
	}, func(req llm.Request) llm.Response {
		initialBoundary.hold(t)
		return waitResponseFor("cold-wait-37", regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages)))
	})
	root := newMessagingRoot(t, adapter, returns.configure)
	t.Cleanup(func() { returns.close(t, root) })
	if _, err := root.ProcessInput(context.Background(), "start-37", nil); err != nil {
		t.Fatal(err)
	}
	owner := root.subagents.sessions()[0]
	initial := initialBoundary.take(t, returns, owner.ID())
	initialBoundary.release()
	incidentYielded(t, root, owner.ID(), initial)
	cp, err := readWaitCheckpoint(transcriptPath(root.stateDir, owner.ID()), owner.ID())
	if err != nil || cp == nil || cp.WaitingOn == "" {
		t.Fatalf("missing original wait identity: %+v %v", cp, err)
	}
	waitYielded(t, root, initial, "cold-wait-37", cp.WaitingOn)
	interruptionID := "wait-interrupted:" + owner.ID() + ":" + cp.ExecutionTurnID + ":" + cp.CommunicateCallID
	copyDir := copyWaitingState(t, root.stateDir)
	jobPath := filepath.Join(jobsDir(copyDir, owner.ID()), "jobs.jsonl")
	if err := os.Remove(jobPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(jobsDir(copyDir, root.ID()), "jobs.jsonl")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	responseEntered := make(chan struct{})
	entered, gate := make(chan llm.Request, 1), make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	adapter.script("COLD-WAIT-37", func(req llm.Request) llm.Response {
		entered <- req
		close(responseEntered)
		<-gate
		return finalResponse("cold-result-37")
	})
	root.Close()
	restoredReturns := newActivationReturns()
	restored := restoreWaitingJoinCopy(t, root, copyDir, restoredReturns)
	waitForTestSignal(t, responseEntered, "cold wait resumed")
	req := <-entered
	terminal := restoredReturns.take(t, owner.ID())
	if terminal.sessionID != initial.sessionID || terminal.delegateID != initial.delegateID || terminal.lease != initial.lease {
		t.Fatalf("cold wait lease changed: %+v -> %+v", initial, terminal)
	}
	{
		if req.SessionID != owner.ID() {
			t.Fatal("cold interruption used another receiver")
		}
		// The private interruption ID binds this notification to the original
		// wait checkpoint. Its actual injected payload must reach this request.
		ids := incidentPresentedIDs(t, copyDir, owner.ID(), req, interruptionID)
		if !slices.Equal(ids, []string{interruptionID}) {
			t.Fatalf("cold interruption payloads=%v", ids)
		}
	}

	before := delegateAggregateSnapshot(t, restored.delegateController, owner.owningDelegateID)
	if before.Generation != 1 || !before.CurrentRunOpen {
		t.Fatalf("cold resumed generation=%+v", before)
	}
	release()
	incidentSettled(t, restored, owner.ID(), terminal)
	if requests := len(adapter.requestsFor("COLD-WAIT-37")); requests != 4 {
		t.Fatalf("model requests=%d want exactly4", requests)
	}
	resumedOwner := restored.getSub(owner.ID())
	if resumedOwner == nil {
		t.Fatal("resumed owner missing")
	}
	if summary := resumedOwner.sess.getOrCreateTaskStore().Summary(); summary.Total != 1 || summary.Done != 0 {
		t.Fatalf("unfinished task was lost or completed: %+v", summary)
	}
	for _, event := range publicationEvents(t, restored) {
		if event.DelegateID == owner.owningDelegateID && event.RunStarted != nil && event.RunStarted.Generation != 1 {
			t.Fatal("cold resume replaced generation")
		}
	}
	data, err := readTranscriptFull(transcriptPath(copyDir, owner.ID()), "")
	if err != nil {
		t.Fatal(err)
	}
	interruptions := 0
	for _, entry := range data.Entries {
		if strings.HasPrefix(entry.Turn.AttentionID, "wait-interrupted:") {
			interruptions++
			incidentNotificationFacts(t, copyDir, owner.ID(), entry.Turn.AttentionID)
		}
	}
	if interruptions != 1 {
		t.Fatalf("interruption records=%d want1", interruptions)
	}
	incidentCoverage(t, copyDir, owner.ID(), map[string][]string{"cold-result-37": {interruptionID}})
}

func TestNotificationBatchCapacityRelease(t *testing.T) {
	a := newMessagingAdapter("REAL-CAP-WAIT-11", "REAL-CAP-BUSY-11")
	returns := newActivationReturns()
	yieldGate, terminalGate := make(chan struct{}), make(chan struct{})
	releaseYield := sync.OnceFunc(func() { close(yieldGate) })
	releaseTerminal := sync.OnceFunc(func() { close(terminalGate) })
	defer releaseYield()
	defer releaseTerminal()
	fifo, releaseShell := waitProducerFIFO(t)
	defer releaseShell()
	gate := make(chan struct{})
	releaseBusy := sync.OnceFunc(func() { close(gate) })
	defer releaseBusy()
	a.script("root", func(llm.Request) llm.Response { return delegateToolResponse("real-cap-owner-11", "REAL-CAP-WAIT-11") }, func(llm.Request) llm.Response { return finalResponse("real-cap-root-11") })
	var jobID string
	resumed := make(chan struct{}, 1)
	a.script("REAL-CAP-WAIT-11", func(llm.Request) llm.Response { return incidentShellResponse("real-cap-shell-11", fifo) }, func(req llm.Request) llm.Response {
		jobID = regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages))
		awaitSignal(t, yieldGate, "capacity yield activation capture")
		return waitResponseFor("real-cap-wait-11", jobID)
	}, func(req llm.Request) llm.Response {
		if err := incidentJobPayload(req, jobID, "completed", "exit_zero"); err != nil {
			t.Error(err)
		}
		resumed <- struct{}{}
		awaitSignal(t, terminalGate, "capacity terminal activation capture")
		return finalResponse("real-cap-observed-11")
	})
	a.script("REAL-CAP-BUSY-11", func(llm.Request) llm.Response { <-gate; return finalResponse("real-cap-busy-result-11") })
	root := newMessagingRoot(t, a, func(cfg *SessionConfig) {
		returns.configure(cfg)
		cfg.MaxConcurrentDelegateTurns = 1
		cfg.clock = agenttest.NewFakeClock() // Capacity release, not a timer, must wake the receiver.
	})
	t.Cleanup(func() { returns.close(t, root) })
	if _, err := root.ProcessInput(context.Background(), "real-cap-start-11", nil); err != nil {
		t.Fatal(err)
	}
	owner := root.subagents.sessions()[0]
	a.waitForRequests(t, "REAL-CAP-WAIT-11", 2)
	initial := returns.take(t, owner.ID())
	releaseYield()
	incidentYielded(t, root, owner.ID(), initial)
	a.script("root", func(llm.Request) llm.Response { return delegateToolResponse("real-cap-busy-11", "REAL-CAP-BUSY-11") }, func(llm.Request) llm.Response { return finalResponse("real-cap-busy-root-11") })
	if _, err := root.ProcessInput(context.Background(), "real-cap-busy-start-11", nil); err != nil {
		t.Fatal(err)
	}
	a.waitForRequests(t, "REAL-CAP-BUSY-11", 1)
	shell := waitShellRun(t, owner.jobManager, jobID)
	releaseShell()
	waitShellFinished(t, shell)
	if delivered := producerSourceEvents(t, owner.jobManager, jobstore.EventJobNotificationDelivered); len(delivered) != 1 {
		t.Fatalf("real shell custody while capacity full=%+v", delivered)
	}
	finished := producerSourceEvents(t, owner.jobManager, jobstore.EventJobFinished)
	if len(finished) != 1 {
		t.Fatalf("terminal shell facts=%+v", finished)
	}
	id := stableShellAttentionID(jobID, finished[0].TerminalGen)
	incidentPendingFacts(t, root.stateDir, owner.ID(), id)
	if len(a.requestsFor("REAL-CAP-WAIT-11")) != 2 {
		t.Fatal("full batch capacity admitted a receiver")
	}
	releaseBusy()
	awaitSignal(t, resumed, "real capacity release notification")
	terminal := returns.take(t, owner.ID())
	if terminal.lease != initial.lease || terminal.generationDone != initial.generationDone {
		t.Fatalf("capacity release replaced the yielded generation: %+v -> %+v", initial, terminal)
	}
	releaseTerminal()
	incidentSettled(t, root, owner.ID(), terminal)
	incidentNotificationFacts(t, root.stateDir, owner.ID(), id)
	incidentCoverage(t, root.stateDir, owner.ID(), map[string][]string{"real-cap-observed-11": {id}})
	if len(a.requestsFor("REAL-CAP-WAIT-11")) != 3 {
		t.Fatal("capacity release ran more than one successor")
	}
}

func TestYieldedWaitCapacityReleaseAndStop(t *testing.T) {
	adapter := newMessagingAdapter("CAP-WAIT-52", "CAP-BUSY-52")
	returns := newActivationReturns()
	initialBoundary, againBoundary := newWaitBoundary(), newWaitBoundary()
	defer initialBoundary.release()
	defer againBoundary.release()
	fifo, releaseShell := waitProducerFIFO(t)
	defer releaseShell()
	busyGate := make(chan struct{})
	releaseBusy := sync.OnceFunc(func() { close(busyGate) })
	defer releaseBusy()
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("cap-owner", "CAP-WAIT-52") }, func(llm.Request) llm.Response { return finalResponse("root-52") })
	var target string
	resumed := make(chan struct{}, 1)
	adapter.script("CAP-WAIT-52", func(llm.Request) llm.Response {
		args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("read value < %q", fifo), "mode": "background"})
		return toolCallResponse(llm.ToolCallData{ID: "shell-52", Name: "shell", Type: "function", Arguments: args})
	}, func(req llm.Request) llm.Response {
		target = regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages))
		initialBoundary.hold(t)
		return waitResponseFor("cap-wait-52", target)
	}, func(llm.Request) llm.Response {
		resumed <- struct{}{}
		againBoundary.hold(t)
		return waitResponseFor("cap-wait-again-52", target)
	})
	adapter.script("CAP-BUSY-52", func(llm.Request) llm.Response { <-busyGate; return finalResponse("busy-result-52") })
	root := newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
		returns.configure(cfg)
		cfg.MaxConcurrentDelegateTurns = 1
		cfg.clock = agenttest.NewFakeClock() // Never advance, retries cannot mask a missed release wake.
	})
	t.Cleanup(func() { returns.close(t, root) })
	if _, err := root.ProcessInput(context.Background(), "start-52", nil); err != nil {
		t.Fatal(err)
	}
	owner := root.subagents.sessions()[0]
	initial := initialBoundary.take(t, returns, owner.ID())
	initialBoundary.release()
	waitYielded(t, root, initial, "cap-wait-52", target)
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("cap-busy", "CAP-BUSY-52") }, func(llm.Request) llm.Response { return finalResponse("root-busy-52") })
	if _, err := root.ProcessInput(context.Background(), "busy-52", nil); err != nil {
		t.Fatal(err)
	}
	adapter.waitForRequests(t, "CAP-BUSY-52", 1)
	if err := root.delegateController.enqueueNotification(owner.ID(), notification{ID: "unrelated-cap-52", Message: llm.UserMachinery("unrelated-cap-payload-52")}); err != nil {
		t.Fatal(err)
	}
	root.delegateController.mu.Lock()
	active := root.delegateController.turnsInUse
	root.delegateController.mu.Unlock()
	if active != 1 || len(adapter.requestsFor("CAP-WAIT-52")) != 2 {
		t.Fatalf("capacity refusal active=%d requests=%d", active, len(adapter.requestsFor("CAP-WAIT-52")))
	}
	releaseBusy()
	awaitSignal(t, resumed, "capacity release wakes yielded receiver")
	root.delegateController.mu.Lock()
	admissionOrder := root.delegateController.attention[owner.owningDelegateID].driveTurn
	root.delegateController.mu.Unlock()
	if admissionOrder == 0 {
		t.Fatal("same-generation admission did not advance existing fairness order")
	}
	again := againBoundary.take(t, returns, owner.ID())
	assertWaitResume(t, initial, again)
	againBoundary.release()
	waitYielded(t, root, again, "cap-wait-again-52", target)
	root.delegateController.mu.Lock()
	active = root.delegateController.turnsInUse
	root.delegateController.mu.Unlock()
	if active != 0 {
		t.Fatalf("second yield capacity=%d want0", active)
	}
	result, plan, mutations, err := root.delegateController.StopSubtreeAndDrive(context.Background(), rootDelegateActor(root.ID()), owner.owningDelegateID)
	if err != nil {
		t.Fatal(err)
	}
	executeDelegateCancelPlan(plan)
	if err := root.executeDelegateMutationPlans(mutations); err != nil {
		t.Fatal(err)
	}
	if result.id != owner.owningDelegateID || result.requestSeq == 0 || result.outcome != "cancelled_by_request" || result.done == nil {
		t.Fatalf("Stop admission=%+v", result)
	}
	waitForTestSignal(t, result.done, "Stop settled yielded generation")
	stopped := returns.take(t, owner.ID())
	assertWaitResume(t, again, stopped)
	incidentSettled(t, root, owner.ID(), stopped)
	root.delegateController.mu.Lock()
	active = root.delegateController.turnsInUse
	root.delegateController.mu.Unlock()
	if active != 0 {
		t.Fatalf("Stop double released capacity=%d", active)
	}
}

func TestAttentionWaitPreservesSiblingDriveCapacity(t *testing.T) {
	adapter := newMessagingAdapter("DRIVE-WAIT-72", "DRIVE-BUSY-72")
	returns := newActivationReturns()
	ownerInitialBoundary, busyInitialBoundary, attentionBoundary := newWaitBoundary(), newWaitBoundary(), newWaitBoundary()
	defer ownerInitialBoundary.release()
	defer busyInitialBoundary.release()
	defer attentionBoundary.release()
	shellResultBoundary := newWaitBoundary()
	defer shellResultBoundary.release()
	shellResult := make(chan llm.Request, 1)
	fifo, releaseShell := waitProducerFIFO(t)
	defer releaseShell()
	busyGate := make(chan struct{})
	releaseBusy := sync.OnceFunc(func() { close(busyGate) })
	defer releaseBusy()
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("drive-owner", "DRIVE-WAIT-72") }, func(llm.Request) llm.Response { return finalResponse("root-72") })
	adapter.script("DRIVE-WAIT-72", func(llm.Request) llm.Response { ownerInitialBoundary.hold(t); return finalResponse("initial-owner-72") }, func(llm.Request) llm.Response {
		args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("read value < %q", fifo), "mode": "background"})
		return toolCallResponse(llm.ToolCallData{ID: "shell-72", Name: "shell", Type: "function", Arguments: args})
	}, func(req llm.Request) llm.Response {
		attentionBoundary.hold(t)
		return waitResponseFor("drive-wait-72", regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages)))
	}, func(req llm.Request) llm.Response {
		shellResult <- req
		shellResultBoundary.hold(t)
		return finalResponse("stopped-shell-observed-72")
	})
	adapter.script("DRIVE-BUSY-72", func(llm.Request) llm.Response { busyInitialBoundary.hold(t); return finalResponse("initial-busy-72") }, func(llm.Request) llm.Response { <-busyGate; return finalResponse("busy-drive-72") })
	root := newMessagingRoot(t, adapter, returns.configure)
	t.Cleanup(func() { returns.close(t, root) })
	if _, err := root.ProcessInput(context.Background(), "start-72", nil); err != nil {
		t.Fatal(err)
	}
	owner := root.subagents.sessions()[0]
	ownerInitial := ownerInitialBoundary.take(t, returns, owner.ID())
	ownerInitialBoundary.release()
	incidentSettled(t, root, owner.ID(), ownerInitial)
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("drive-busy", "DRIVE-BUSY-72") }, func(llm.Request) llm.Response { return finalResponse("root-busy-72") })
	if _, err := root.ProcessInput(context.Background(), "busy-72", nil); err != nil {
		t.Fatal(err)
	}
	var busy *Session
	for _, candidate := range root.subagents.sessions() {
		if candidate.ID() != owner.ID() {
			busy = candidate
		}
	}
	if busy == nil {
		t.Fatal("busy sibling missing")
	}
	busyInitial := busyInitialBoundary.take(t, returns, busy.ID())
	busyInitialBoundary.release()
	incidentSettled(t, root, busy.ID(), busyInitial)
	if err := root.delegateController.enqueueNotification(owner.ID(), notification{ID: "drive-owner-72", Message: llm.UserMachinery("drive-owner-payload-72")}); err != nil {
		t.Fatal(err)
	}
	attention := attentionBoundary.take(t, returns, owner.ID())
	if attention.lease.generation != 2 || attention.generationDone == ownerInitial.generationDone {
		t.Fatalf("attention did not start generation two: %+v", attention)
	}
	attentionBoundary.release()
	incidentYielded(t, root, owner.ID(), attention)
	cp, err := readWaitCheckpoint(owner.TranscriptPath(), owner.ID())
	if err != nil || cp == nil {
		t.Fatalf("attention checkpoint=%+v err=%v", cp, err)
	}
	waitYielded(t, root, attention, "drive-wait-72", cp.WaitingOn)
	shell := waitShellRun(t, owner.jobManager, cp.WaitingOn)
	t.Cleanup(func() { waitShellFinished(t, shell) })
	if run := delegateAggregateSnapshot(t, root.delegateController, owner.owningDelegateID); run.Generation != 2 || run.Trigger != delegatestore.TriggerAttention || !run.CurrentRunOpen {
		t.Fatalf("attention generation=%+v", run)
	}
	if err := root.delegateController.enqueueNotification(busy.ID(), notification{ID: "drive-busy-72", Message: llm.UserMachinery("drive-busy-payload-72")}); err != nil {
		t.Fatal(err)
	}
	adapter.waitForRequests(t, "DRIVE-BUSY-72", 2)
	result, plan, mutations, err := root.delegateController.StopSubtreeAndDrive(context.Background(), rootDelegateActor(root.ID()), owner.owningDelegateID)
	if err != nil {
		t.Fatal(err)
	}
	executeDelegateCancelPlan(plan)
	if err := root.executeDelegateMutationPlans(mutations); err != nil {
		t.Fatal(err)
	}
	if result.id != owner.owningDelegateID || result.requestSeq == 0 || result.outcome != "cancelled_by_request" || result.done == nil {
		t.Fatalf("attention Stop admission=%+v", result)
	}
	awaitSignal(t, result.done, "yielded attention generation Stop")
	stopped := returns.take(t, owner.ID())
	assertWaitResume(t, attention, stopped)
	incidentSettled(t, root, owner.ID(), stopped)
	root.delegateController.mu.Lock()
	turns, drives := root.delegateController.turnsInUse, root.delegateController.drivesInUse
	root.delegateController.mu.Unlock()
	if turns != 0 || drives != 1 {
		t.Fatalf("Stop stole sibling drive capacity: turns=%d drives=%d, want0,1", turns, drives)
	}
	busyDrive := returns.take(t, busy.ID())
	if busyDrive.lease.generation != 2 || busyDrive.generationDone == busyInitial.generationDone {
		t.Fatalf("busy drive reused initial generation: %+v", busyDrive)
	}
	releaseBusy()
	incidentSettled(t, root, busy.ID(), busyDrive)
	// Subtree Stop killed OWNER's real shell. Once BUSY returns its drive
	// slot, that terminal input starts a distinct OWNER attention generation.
	// Join its scripted response rather than polling past provider exhaustion.
	completion := shellResultBoundary.take(t, returns, owner.ID())
	if completion.sessionID != owner.ID() || completion.delegateID != owner.owningDelegateID || completion.lease.generation != 3 || completion.generationDone == stopped.generationDone {
		t.Fatalf("shell result reused stopped generation: %+v", completion)
	}
	if run := delegateAggregateSnapshot(t, root.delegateController, owner.owningDelegateID); run.Generation != 3 || run.Trigger != delegatestore.TriggerAttention || !run.CurrentRunOpen {
		t.Fatalf("shell result generation=%+v", run)
	}
	waitShellFinished(t, shell)
	records, err := owner.jobManager.store.Load()
	if err != nil || records[cp.WaitingOn] == nil {
		t.Fatalf("stopped shell record=%+v err=%v", records[cp.WaitingOn], err)
	}
	record := records[cp.WaitingOn]
	if record.Status != jobstore.StatusCommandKilled || !strings.HasPrefix(record.Reason, "killed_by_signal:") || record.TerminalGen == "" {
		t.Fatalf("subtree Stop shell result=%+v", record)
	}
	if err := incidentJobPayload(<-shellResult, cp.WaitingOn, string(record.Status), record.Reason); err != nil {
		t.Fatal(err)
	}
	shellResultBoundary.release()
	incidentSettled(t, root, owner.ID(), completion)
	root.delegateController.mu.Lock()
	drives = root.delegateController.drivesInUse
	root.delegateController.mu.Unlock()
	if drives != 0 {
		t.Fatalf("sibling drive capacity after return=%d", drives)
	}
}

func TestDirectUserStopResumeHandoff(t *testing.T) {
	for _, order := range []string{"stop_selected", "stop_tail", "resume_admitted"} {
		t.Run(order, func(t *testing.T) {
			stopFirst := order != "resume_admitted"
			adapter := newMessagingAdapter("HANDOFF-WAIT-94")
			returns := newActivationReturns()
			initialBoundary := newWaitBoundary()
			defer initialBoundary.release()
			fifo, releaseShell := waitProducerFIFO(t)
			defer releaseShell()
			adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("handoff-owner", "HANDOFF-WAIT-94") }, func(llm.Request) llm.Response { return finalResponse("root-94") })
			adapter.script("HANDOFF-WAIT-94", func(llm.Request) llm.Response {
				args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("read value < %q", fifo), "mode": "background"})
				return toolCallResponse(llm.ToolCallData{ID: "handoff-shell-94", Name: "shell", Type: "function", Arguments: args})
			}, func(req llm.Request) llm.Response {
				initialBoundary.hold(t)
				return waitResponseFor("handoff-wait-94", regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages)))
			})
			root := newMessagingRoot(t, adapter, func(cfg *SessionConfig) { returns.configure(cfg); cfg.clock = agenttest.NewFakeClock() })
			t.Cleanup(func() { returns.close(t, root) })
			if _, err := root.ProcessInput(context.Background(), "start-94", nil); err != nil {
				t.Fatal(err)
			}
			owner := root.subagents.sessions()[0]
			sub := root.getSub(owner.ID())
			initial := initialBoundary.take(t, returns, owner.ID())
			initialBoundary.release()
			incidentYielded(t, root, owner.ID(), initial)
			cp, err := readWaitCheckpoint(owner.TranscriptPath(), owner.ID())
			if err != nil || cp == nil {
				t.Fatalf("handoff checkpoint=%+v err=%v", cp, err)
			}
			waitYielded(t, root, initial, "handoff-wait-94", cp.WaitingOn)
			// This is the same guard the attention driver owns when it calls
			// resumeYieldedDelegate. Drive that handoff directly so Stop ordering
			// does not depend on a scheduler or a fabricated incoming notification.
			if !root.takeSendDriveGuard(sub) {
				t.Fatal("could not acquire existing drive guard")
			}
			defer releaseDriveGuard(sub)
			sub.mu.Lock()
			originalDone := sub.done
			sub.mu.Unlock()
			executorEntered, executorGate := make(chan struct{}, 1), make(chan struct{})
			releaseExecutor := sync.OnceFunc(func() { close(executorGate) })
			defer releaseExecutor()
			updateSessionTestConfig(owner, func(cfg *testConfig) {
				cfg.subagentRunIteration = func(*subagent, int) {
					executorEntered <- struct{}{}
					<-executorGate
				}
			})
			cancelSelected, cancelGate := make(chan struct{}), make(chan struct{})
			selected := sync.OnceFunc(func() { close(cancelSelected) })
			releaseCancel := sync.OnceFunc(func() { close(cancelGate) })
			defer releaseCancel()
			c := root.delegateController
			c.mu.Lock()
			binding := c.live[owner.owningDelegateID].binding
			lease, baseCancel := binding.lease, binding.cancel
			binding.cancel = func() {
				selected()
				<-cancelGate
				baseCancel()
			}
			c.mu.Unlock()
			type stopResult struct {
				done chan struct{}
				err  error
			}
			stopped := make(chan stopResult, 1)
			stopFinished := make(chan struct{})
			stopStarted := false
			stop := func() {
				defer close(stopFinished)
				done, err := sub.requestRunCancel(true)
				stopped <- stopResult{done, err}
			}
			t.Cleanup(func() {
				releaseCancel()
				releaseExecutor()
				if stopStarted {
					waitForTestSignal(t, stopFinished, "handoff Stop operation cleanup")
				}
			})
			takeStop := func() stopResult {
				waitForTestSignal(t, stopFinished, "direct Stop cancellation admission")
				return <-stopped
			}
			if stopFirst {
				stopStarted = true
				go stop()
				awaitSignal(t, cancelSelected, "direct Stop selected yielded cancellation after guard")
			}
			if order == "stop_tail" {
				releaseCancel()
				awaitSignal(t, executorEntered, "canceled tail already owns execution")
			}
			handled, resumed := root.resumeYieldedDelegate(sub)
			if !handled || resumed == stopFirst {
				t.Errorf("same-generation handoff handled=%v resumed=%v, stopFirst=%v", handled, resumed, stopFirst)
			}
			if !stopFirst {
				awaitSignal(t, executorEntered, "resumed executor before direct Stop")
				stopStarted = true
				go stop()
				// A running executor's exact cancellation is installed by handoff.
			} else if resumed {
				awaitSignal(t, executorEntered, "unexpected resumed executor held before input")
			}
			sub.mu.Lock()
			currentDone := sub.done
			sub.mu.Unlock()
			if currentDone != originalDone {
				t.Error("same-generation resumption replaced admitted done channel")
			}
			releaseCancel()
			if stopFirst && !resumed && order != "stop_tail" {
				awaitSignal(t, executorEntered, "canceled tail owns only execution")
			}
			// Both orderings have admitted user Stop before either executor can
			// read input. Inspect the flags actually consumed by finalization.
			stoppedActivation := returns.take(t, owner.ID())
			assertWaitResume(t, initial, stoppedActivation)
			var admitted stopResult
			if !stopFirst {
				admitted = takeStop()
			}
			sub.mu.Lock()
			cancelled, user := sub.cancelRequested, sub.userStopRequested
			sub.mu.Unlock()
			if !cancelled || !user {
				t.Errorf("handoff lost cancellation ownership: requested=%v user=%v", cancelled, user)
			}
			releaseExecutor()
			if stopFirst {
				admitted = takeStop()
			}
			if admitted.err != nil || admitted.done != originalDone {
				t.Errorf("Stop admitted done=%p original=%p err=%v", admitted.done, originalDone, admitted.err)
			}
			waitForTestSignal(t, admitted.done, "admitted direct Stop done")
			incidentSettled(t, root, owner.ID(), stoppedActivation)
			checkpoint, err := readWaitCheckpoint(transcriptPath(root.stateDir, owner.ID()), owner.ID())
			if err != nil || checkpoint == nil || checkpoint.ResumedByTurnID != "" {
				t.Errorf("Stop before executor input wrote resumption claim=%+v err=%v", checkpoint, err)
			}
			sub.mu.Lock()
			status, attributed := sub.status, sub.userStopRequested
			sub.mu.Unlock()
			if status != SubagentCancelled || !attributed {
				t.Errorf("actual final state status=%s userStopRequested=%v", status, attributed)
			}
			starts, finishes := 0, 0
			for _, event := range publicationEvents(t, root) {
				if event.DelegateID != lease.delegateID {
					continue
				}
				if event.RunStarted != nil {
					starts++
				}
				if event.RunFinished != nil {
					finishes++
					if event.RunFinished.Generation != lease.generation || event.RunFinished.Outcome.Status != delegatestore.OutcomeCancelled {
						t.Errorf("direct Stop handoff outcome=%+v", event.RunFinished)
					}
				}
			}
			c.mu.Lock()
			turns, drives := c.turnsInUse, c.drivesInUse
			c.mu.Unlock()
			if starts != 1 || finishes != 1 || turns != 0 || drives != 0 || len(adapter.requestsFor("HANDOFF-WAIT-94")) != 2 {
				t.Errorf("handoff starts=%d finishes=%d capacity=%d,%d modelRequests=%d", starts, finishes, turns, drives, len(adapter.requestsFor("HANDOFF-WAIT-94")))
			}
		})
	}
}

func TestDirectUserStopYieldedWait(t *testing.T) {
	adapter := newMessagingAdapter("USER-WAIT-63", "USER-CHILD-63")
	returns := newActivationReturns()
	initialBoundary, successorBoundary := newWaitBoundary(), newWaitBoundary()
	defer initialBoundary.release()
	defer successorBoundary.release()
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("user-owner", "USER-WAIT-63") }, func(llm.Request) llm.Response { return finalResponse("root-63") })
	adapter.script("USER-WAIT-63", func(llm.Request) llm.Response { return delegateToolResponse("user-child", "USER-CHILD-63") }, func(req llm.Request) llm.Response {
		initialBoundary.hold(t)
		return waitResponseFor("user-wait-63", deferredHeadDelegateID.FindString(lastToolResultText(req.Messages)))
	})
	adapter.script("USER-WAIT-63", func(llm.Request) llm.Response { successorBoundary.hold(t); return finalResponse("done") })
	adapter.script("USER-CHILD-63", func(llm.Request) llm.Response { <-gate; return finalResponse("child-result-63") })
	root := newMessagingRoot(t, adapter, returns.configure)
	t.Cleanup(func() { returns.close(t, root) })
	if _, err := root.ProcessInput(context.Background(), "start-63", nil); err != nil {
		t.Fatal(err)
	}
	owner := root.subagents.sessions()[0]
	sub := root.getSub(owner.ID())
	initial := initialBoundary.take(t, returns, owner.ID())
	initialBoundary.release()
	incidentYielded(t, root, owner.ID(), initial)
	checkpoint, err := readWaitCheckpoint(owner.TranscriptPath(), owner.ID())
	if err != nil || checkpoint == nil {
		t.Fatalf("Stop checkpoint=%+v err=%v", checkpoint, err)
	}
	waitYielded(t, root, initial, "user-wait-63", checkpoint.WaitingOn)
	sub.mu.Lock()
	originalDone := sub.done
	sub.mu.Unlock()
	outcome, err := root.StopDelegateRun(owner.owningDelegateID)
	if err != nil || outcome != appwire.DelegateStopStopping {
		t.Fatalf("direct Stop=%s err=%v", outcome, err)
	}
	stopped := returns.take(t, owner.ID())
	assertWaitResume(t, initial, stopped)
	waitForTestSignal(t, originalDone, "direct Stop original done")
	incidentSettled(t, root, owner.ID(), stopped)
	if again, err := root.StopDelegateRun(owner.owningDelegateID); err != nil || again != appwire.DelegateStopNotRunning {
		t.Fatalf("repeated Stop=%s err=%v", again, err)
	}
	cp, err := readWaitCheckpoint(transcriptPath(root.stateDir, owner.ID()), owner.ID())
	if err != nil || cp.ResumedByTurnID != "" {
		t.Fatalf("Stop admitted a resumed execution: %+v %v", cp, err)
	}
	child := owner.subagents.sessions()[0]
	if !delegateAggregateSnapshot(t, root.delegateController, child.owningDelegateID).CurrentRunOpen {
		t.Fatal("direct Stop settled live child")
	}
	root.delegateController.mu.Lock()
	active := root.delegateController.turnsInUse
	root.delegateController.mu.Unlock()
	if active != 1 {
		t.Fatalf("direct Stop stole child capacity=%d want1", active)
	}
	finishes := 0
	for _, event := range publicationEvents(t, root) {
		if event.DelegateID == owner.owningDelegateID && event.RunFinished != nil {
			finishes++
			if event.RunFinished.Outcome.Status != delegatestore.OutcomeCancelled {
				t.Fatalf("direct Stop outcome=%+v", event.RunFinished.Outcome)
			}
		}
	}
	if finishes != 1 {
		t.Fatalf("direct Stop finishes=%d want1", finishes)
	}
	release()
	successor := successorBoundary.take(t, returns, owner.ID())
	if successor.lease.generation != 2 || successor.generationDone == initial.generationDone {
		t.Fatalf("successor reused stopped generation: %+v", successor)
	}
	successorBoundary.release()
	incidentSettled(t, root, owner.ID(), successor)
	if requests := len(adapter.requestsFor("USER-WAIT-63")); requests != 3 {
		t.Fatalf("retained child result model requests=%d want3", requests)
	}
	sub.mu.Lock()
	newDone, cancelled, user := sub.done, sub.cancelRequested, sub.userStopRequested
	sub.mu.Unlock()
	if newDone == originalDone || cancelled || user {
		t.Fatalf("successor did not reset generation-owned state: oldDone=%p newDone=%p cancelled=%v user=%v", originalDone, newDone, cancelled, user)
	}
}

func TestColdDirectUserStopYieldedWait(t *testing.T) {
	for _, test := range []struct {
		name                     string
		pending, failedRead      bool
		resident, concurrentStop bool
		refuseCancel             bool
		activeSibling            bool
	}{
		{name: "cold_empty"},
		{name: "cold_empty_active_sibling", activeSibling: true},
		{name: "cold_pending", pending: true},
		{name: "cold_read_repair", pending: true, failedRead: true},
		{name: "cold_concurrent_stop", pending: true, concurrentStop: true},
		{name: "cold_admission_retry", pending: true, refuseCancel: true},
		{name: "resident_pending", pending: true, resident: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := newMessagingAdapter("COLD-STOP-84", "COLD-CHILD-84", "COLD-BUSY-84")
			returns := newActivationReturns()
			initialBoundary, againBoundary, childBoundary, successorBoundary, terminalBoundary := newWaitBoundary(), newWaitBoundary(), newWaitBoundary(), newWaitBoundary(), newWaitBoundary()
			defer initialBoundary.release()
			defer againBoundary.release()
			defer childBoundary.release()
			defer successorBoundary.release()
			defer terminalBoundary.release()
			fifo, releaseShell := waitProducerFIFO(t)
			defer releaseShell()
			successor := make(chan llm.Request, 1)
			childRecovery := make(chan llm.Request, 1)
			childGate := make(chan struct{})
			releaseChild := sync.OnceFunc(func() { close(childGate) })
			defer releaseChild()
			busyGate := make(chan struct{})
			releaseBusy := sync.OnceFunc(func() { close(busyGate) })
			defer releaseBusy()
			adapter.script("COLD-BUSY-84", func(llm.Request) llm.Response { <-busyGate; return finalResponse("cold-busy-result-84") })
			adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("cold-stop", "COLD-STOP-84") }, func(llm.Request) llm.Response { return finalResponse("root-84") })
			adapter.script("COLD-STOP-84", func(llm.Request) llm.Response { return delegateToolResponse("cold-child", "COLD-CHILD-84") }, func(req llm.Request) llm.Response {
				initialBoundary.hold(t)
				return waitResponseFor("cold-stop-wait-84", deferredHeadDelegateID.FindString(lastToolResultText(req.Messages)))
			}, func(req llm.Request) llm.Response {
				// The child's own wait publication is real unrelated input to its parent.
				var childID string
				for _, msg := range req.Messages {
					if id := deferredHeadDelegateID.FindString(msg.Text()); id != "" {
						childID = id
					}
				}
				againBoundary.hold(t)
				return waitResponseFor("cold-stop-wait-again-84", childID)
			}, func(req llm.Request) llm.Response {
				successor <- req
				successorBoundary.hold(t)
				if !test.pending {
					return finalResponse("cold-stop-retained-child-result-84")
				}
				var childID string
				for _, msg := range req.Messages {
					if id := deferredHeadDelegateID.FindString(msg.Text()); id != "" {
						childID = id
					}
				}
				return waitResponseFor("cold-stop-successor-wait-84", childID)
			}, func(llm.Request) llm.Response {
				terminalBoundary.hold(t)
				return finalResponse("cold-stop-retained-child-result-84")
			})
			adapter.script("COLD-CHILD-84", func(llm.Request) llm.Response {
				args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("read value < %q", fifo), "mode": "background"})
				return toolCallResponse(llm.ToolCallData{ID: "cold-shell-84", Name: "shell", Type: "function", Arguments: args})
			}, func(req llm.Request) llm.Response {
				childBoundary.hold(t)
				return waitResponseFor("cold-child-wait-84", regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages)))
			}, func(req llm.Request) llm.Response {
				childRecovery <- req
				<-childGate
				return finalResponse("cold-stop-retained-child-payload-84")
			})
			root := newMessagingRoot(t, adapter, returns.configure)
			t.Cleanup(func() { returns.close(t, root) })
			if _, err := root.ProcessInput(context.Background(), "start-84", nil); err != nil {
				t.Fatal(err)
			}
			owner := root.subagents.sessions()[0]
			sub := root.getSub(owner.ID())
			initial := initialBoundary.take(t, returns, owner.ID())
			initialBoundary.release()
			incidentYielded(t, root, owner.ID(), initial)
			initialCP, err := readWaitCheckpoint(owner.TranscriptPath(), owner.ID())
			if err != nil || initialCP == nil {
				t.Fatalf("initial cold Stop checkpoint=%+v err=%v", initialCP, err)
			}
			waitYielded(t, root, initial, "cold-stop-wait-84", initialCP.WaitingOn)
			child := owner.subagents.sessions()[0]
			childInitial := childBoundary.take(t, returns, child.ID())
			childBoundary.release()
			incidentYielded(t, owner, child.ID(), childInitial)
			childCP, err := readWaitCheckpoint(child.TranscriptPath(), child.ID())
			if err != nil || childCP == nil {
				t.Fatalf("cold child checkpoint=%+v err=%v", childCP, err)
			}
			waitYielded(t, owner, childInitial, "cold-child-wait-84", childCP.WaitingOn)
			shellID := childCP.WaitingOn
			shell := waitShellRun(t, child.jobManager, shellID)
			t.Cleanup(func() { waitShellFinished(t, shell) })
			records, err := child.jobManager.store.Load()
			if err != nil || records[shellID] == nil || records[shellID].Status != jobstore.StatusRunning || records[shellID].OwnerSessionID != child.ID() {
				t.Fatalf("original held child shell=%+v err=%v", records[shellID], err)
			}
			again := againBoundary.take(t, returns, owner.ID())
			assertWaitResume(t, initial, again)
			againBoundary.release()
			waitYielded(t, root, again, "cold-stop-wait-again-84", child.owningDelegateID)
			// Keep the original executor out while accepting real addressed custody,
			// then work only on an exact copy of that persisted state.
			sub.mu.Lock()
			sub.driving = true
			sub.mu.Unlock()
			defer releaseDriveGuard(sub)
			if test.pending {
				if err := root.delegateController.enqueueNotification(owner.ID(), notification{ID: "pending-cold-stop-84", Message: llm.UserMachinery("pending-cold-stop-payload-84")}); err != nil {
					t.Fatal(err)
				}
			}
			copyDir := root.stateDir
			if !test.resident {
				copyDir = copyWaitingState(t, root.stateDir)
			}
			meta, err := schema.LoadSessionMeta(copyDir, root.ID())
			if err != nil {
				t.Fatal(err)
			}
			var denyRestore atomic.Bool
			denyRestore.Store(true)
			restored := root
			restoredReturns := returns
			if !test.resident {
				restoredReturns = newActivationReturns()
				restored, err = RestoreSessionFromMetaWithConfig(root.client, root.profile, execenv.NewLocalExecutionEnvironment(meta.EnvInfo.WorkingDir), meta, RestoreSessionConfig{
					StateDir: copyDir, ForceRealIO: true, clock: agenttest.NewFakeClock(),
					AcquireSessionOwnership: func(id string) error {
						if denyRestore.Load() && (id == owner.ID() || id == child.ID()) {
							return errors.New("fixture ownership unavailable")
						}
						return nil
					},
					testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, disableDelegateIdleRelease: true, subagentActivationReturn: restoredReturns.capture},
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { restoredReturns.close(t, restored) })
				if resident := restored.subagentForChild(owner.ID()); resident != nil {
					t.Fatal("fixture did not remain cold behind ownership refusal")
				}
			}
			fold, err := readExistingDelegateAttentionFold(transcriptPath(copyDir, owner.ID()), owner.ID())
			if err != nil || (len(fold.pendingIDs()) != 0) != test.pending {
				t.Fatalf("cold pending evidence=%v err=%v", fold.pendingIDs(), err)
			}
			c := restored.delegateController
			var userStopStates atomic.Int32
			captureAttribution := func(a *subagent) {
				a.mu.Lock()
				attributed := a.status == SubagentCancelled && a.userStopRequested && a.cancelRequested
				a.mu.Unlock()
				if attributed {
					userStopStates.Add(1)
				}
			}
			if test.resident {
				updateSessionTestConfig(owner, func(cfg *testConfig) { cfg.subagentAfterFinalStatePublish = captureAttribution })
			}
			if !test.resident {
				restored.delegateRestoreBeforeSideEffects = func(target *Session) {
					if target.ID() != owner.ID() {
						return
					}
					updateSessionTestConfig(target, func(cfg *testConfig) { cfg.subagentAfterFinalStatePublish = captureAttribution })
					// The normal callback exists and the lease is already attached,
					// but gate.add has not installed its wrapper yet.
					if err := target.requestNotificationWake(); err != nil {
						t.Error(err)
					}
					current := restored.subagentForChild(target.ID())
					current.mu.Lock()
					guarded, running := current.driving, current.running
					current.mu.Unlock()
					if !guarded || running || len(adapter.requestsFor("COLD-STOP-84")) != 3 {
						t.Errorf("pre-gate callback escaped guard: guarded=%v running=%v", guarded, running)
					}
					if test.concurrentStop {
						if outcome, err := restored.StopDelegateRun(owner.owningDelegateID); err != nil || outcome != appwire.DelegateStopStopping {
							t.Errorf("concurrent Stop=%s err=%v", outcome, err)
						}
						if delegateAggregateSnapshot(t, c, owner.owningDelegateID).CurrentRunOpen {
							t.Error("concurrent Stop reached an attached wait without its yielded cancellation tail")
						}
					}
					if test.refuseCancel {
						c.mu.Lock()
						c.closing = true
						c.mu.Unlock()
					}
				}
			}
			if test.failedRead {
				c.mu.Lock()
				c.attentionOpen = func(string, string) (*transcript.Writer, []transcript.Entry, error) {
					return nil, nil, errors.New("cold wait proof unavailable")
				}
				c.mu.Unlock()
				if outcome, err := restored.StopDelegateRun(owner.owningDelegateID); err == nil || outcome != "" {
					t.Fatalf("unreadable live wait Stop=%s err=%v, want retryable error", outcome, err)
				}
				c.mu.Lock()
				c.attentionOpen = nil
				c.mu.Unlock()
			}
			var busyID string
			var busyReceipt *activationReturn
			if test.activeSibling {
				adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("cold-busy", "COLD-BUSY-84") }, func(llm.Request) llm.Response { return finalResponse("root-busy-84") })
				if _, err := restored.ProcessInput(context.Background(), "busy-84", nil); err != nil {
					t.Fatal(err)
				}
				adapter.waitForRequests(t, "COLD-BUSY-84", 1)
				for _, candidate := range restored.subagents.sessions() {
					if candidate.ID() != owner.ID() {
						busyID = candidate.owningDelegateID
						busyReceipt = restoredReturns.take(t, candidate.ID())
					}
				}
				if busyID == "" {
					t.Fatal("active sibling missing")
				}
			}
			denyRestore.Store(false)
			beforeCalls := len(adapter.requestsFor("COLD-STOP-84"))
			outcome, err := restored.StopDelegateRun(owner.owningDelegateID)
			if test.refuseCancel {
				if err != nil || outcome != appwire.DelegateStopNotRunning {
					t.Fatalf("refused cancellation=%s err=%v", outcome, err)
				}
				current := restored.subagentForChild(owner.ID())
				current.mu.Lock()
				guarded := current.driving
				current.mu.Unlock()
				if guarded {
					t.Fatal("failed cancellation orphaned the restored drive guard")
				}
				c.mu.Lock()
				c.closing = false
				c.mu.Unlock()
				outcome, err = restored.StopDelegateRun(owner.owningDelegateID)
			}
			wantOutcome := appwire.DelegateStopStopping
			if test.concurrentStop {
				wantOutcome = appwire.DelegateStopNotRunning // the other Stop already settled this lease
			}
			if err != nil || outcome != wantOutcome {
				t.Fatalf("cold direct Stop=%s err=%v, want stopping", outcome, err)
			}
			stopped := restoredReturns.take(t, owner.ID())
			if stopped.sessionID != owner.ID() || stopped.delegateID != owner.owningDelegateID || stopped.lease != again.lease || test.resident && stopped.generationDone != again.generationDone {
				t.Fatalf("cold Stop activation=%+v original=%+v", stopped, again)
			}
			waitForTestSignal(t, stopped.generationDone, "cold Stop admitted generation")
			waitForTestSignal(t, stopped.returned, "cold Stop exact canceled tail")
			if test.resident && releaseDriveGuard(sub) {
				c.requestSessionWake(owner.ID())
			}
			wantStarts, wantCalls := 1, beforeCalls
			if test.pending {
				wantStarts, wantCalls = 2, beforeCalls+1
				waitForTestSignal(t, successorBoundary.entered, "retained pending input successor")
				successorActivation := restoredReturns.take(t, owner.ID())
				if successorActivation.lease.generation != 2 || successorActivation.generationDone == stopped.generationDone {
					t.Fatalf("successor reused stopped done: %+v", successorActivation)
				}
				{
					req := <-successor
					found := 0
					for _, msg := range req.Messages {
						found += strings.Count(msg.Text(), "pending-cold-stop-payload-84")
					}
					if found != 1 {
						t.Fatalf("successor canonical payload count=%d want1", found)
					}
				}
				successorBoundary.release()
				waitYielded(t, restored, successorActivation, "cold-stop-successor-wait-84", child.owningDelegateID)
			} else {
				if again, err := restored.StopDelegateRun(owner.owningDelegateID); err != nil || again != appwire.DelegateStopNotRunning {
					t.Fatalf("repeated cold Stop=%s err=%v", again, err)
				}
			}
			if got := len(adapter.requestsFor("COLD-STOP-84")); got != wantCalls {
				t.Fatalf("cold Stop model requests=%d want%d", got, wantCalls)
			}
			data, err := readTranscriptFull(transcriptPath(copyDir, owner.ID()), "")
			if err != nil {
				t.Fatal(err)
			}
			observations := 0
			for _, entry := range data.Entries {
				if cp := entry.Turn.WaitCheckpoint; cp != nil && cp.Generation == 1 && cp.ResumedByTurnID != "" && cp.CommunicateCallID == "cold-stop-wait-again-84" {
					t.Fatalf("cold Stop resumed final generation1 wait=%+v", cp)
				}
				for _, id := range entry.Turn.ObservedNotificationIDs {
					if id == "pending-cold-stop-84" {
						observations++
					}
				}
			}
			wantObservations := 0
			if test.pending {
				wantObservations = 1
			}
			if observations != wantObservations {
				t.Fatalf("durable canonical observations=%d want%d", observations, wantObservations)
			}
			childRun := delegateAggregateSnapshot(t, c, child.owningDelegateID)
			if !childRun.CurrentRunOpen || childRun.Generation != 1 || childRun.LatestOutcome != nil {
				t.Fatalf("cold direct Stop changed descendant=%+v", childRun)
			}
			starts, finishes, preparedCount := 0, 0, 0
			for _, event := range publicationEvents(t, restored) {
				if event.DelegateID != owner.owningDelegateID {
					continue
				}
				if prepared := event.TerminalPrepared; prepared != nil && prepared.Generation == 1 {
					preparedCount++
				}
				if event.RunStarted != nil {
					starts++
				}
				if event.RunFinished != nil {
					finishes++
					if event.RunFinished.Generation != 1 || event.RunFinished.Outcome.Status != delegatestore.OutcomeCancelled {
						t.Fatalf("cold direct Stop attribution=%+v", event.RunFinished)
					}
				}
			}
			if starts != wantStarts || finishes != 1 || preparedCount != 1 || userStopStates.Load() != 1 {
				t.Fatalf("cold direct Stop starts=%d finishes=%d prepared=%d userStopStates=%d, want%d,1,1,1", starts, finishes, preparedCount, userStopStates.Load(), wantStarts)
			}
			wantTurns := 0
			if test.activeSibling {
				wantTurns = 1
				if !delegateAggregateSnapshot(t, c, busyID).CurrentRunOpen {
					t.Fatal("cold direct Stop ended unrelated sibling")
				}
			}
			var childTerminal *activationReturn
			if !test.resident {
				// Root bootstrap repairs the copied live shell into addressed
				// child input even when the OWNER has no pending input. Stop's
				// capacity release may therefore resume this exact child lease.
				adapter.waitForRequests(t, "COLD-CHILD-84", 3)
				request := <-childRecovery
				childTerminal = restoredReturns.take(t, child.ID())
				if childTerminal.sessionID != child.ID() || childTerminal.delegateID != child.owningDelegateID || childTerminal.lease != childInitial.lease || childTerminal.generationDone == childInitial.generationDone {
					t.Fatalf("restored child recovery identity=%+v original=%+v", childTerminal, childInitial)
				}
				if err := incidentJobPayload(request, shellID, "stopped", "runtime_lost"); err != nil {
					t.Fatal(err)
				}
				recovered, err := readTranscriptFull(transcriptPath(copyDir, child.ID()), "")
				if err != nil {
					t.Fatal(err)
				}
				claims := 0
				for _, entry := range recovered.Entries {
					cp := entry.Turn.WaitCheckpoint
					if cp == nil || cp.ResumedByTurnID == "" {
						continue
					}
					claims++
					if cp.SessionID != childCP.SessionID || cp.DelegateID != childCP.DelegateID || cp.Generation != childCP.Generation || cp.ExecutionTurnID != childCP.ExecutionTurnID || cp.CommunicateCallID != childCP.CommunicateCallID || cp.WaitingOn != shellID || cp.ResumedByTurnID == cp.ExecutionTurnID {
						t.Fatalf("restored child recovery claim=%+v original=%+v", cp, childCP)
					}
				}
				if claims != 1 {
					t.Fatalf("restored child resumption claims=%d want1", claims)
				}
				current := restored.subagentForChild(child.ID())
				current.mu.Lock()
				exactActive := current.running && !current.driving && current.done == childTerminal.generationDone
				current.mu.Unlock()
				c.mu.Lock()
				live := c.live[child.owningDelegateID]
				exactBinding := live != nil && live.runtime == current.sess && live.binding != nil && !live.binding.yielded && live.binding.lease == childTerminal.lease
				c.mu.Unlock()
				if !exactActive || !exactBinding {
					t.Fatalf("restored child recovery active=%v exactBinding=%v", exactActive, exactBinding)
				}
				select {
				case <-childTerminal.generationDone:
					t.Fatal("held restored child generation closed")
				default:
				}
				wantTurns++
			}
			c.mu.Lock()
			turns, drives := c.turnsInUse, c.drivesInUse
			c.mu.Unlock()
			if turns != wantTurns || drives != 0 {
				t.Fatalf("cold Stop capacity: turns=%d drives=%d want%d,0", turns, drives, wantTurns)
			}
			if !test.pending {
				if test.activeSibling {
					releaseBusy()
					incidentSettled(t, restored, busyReceipt.sessionID, busyReceipt)
				}
				return // no synthetic input is added merely to make Stop execute
			}
			releaseShell()
			adapter.waitForRequests(t, "COLD-CHILD-84", 3)
			if test.resident {
				<-childRecovery
				childTerminal = restoredReturns.take(t, child.ID())
			}
			if childTerminal.lease != childInitial.lease || test.resident && childTerminal.generationDone != childInitial.generationDone {
				t.Fatalf("retained child lease changed: %+v", childTerminal)
			}
			releaseChild()
			terminal := terminalBoundary.take(t, restoredReturns, owner.ID())
			if terminal.lease.generation != 2 || terminal.generationDone == stopped.generationDone {
				t.Fatalf("retained-result activation reused stopped generation: %+v", terminal)
			}
			terminalBoundary.release()
			incidentSettled(t, restored, owner.ID(), terminal)
			incidentSettled(t, restored.subagentForChild(owner.ID()).sess, child.ID(), childTerminal)
			childRun = delegateAggregateSnapshot(t, c, child.owningDelegateID)
			if childRun.Generation != 1 || childRun.CurrentRunOpen || childRun.LatestOutcome == nil || childRun.LatestOutcome.Status != delegatestore.OutcomeCompleted {
				t.Fatalf("retained child outcome=%+v", childRun)
			}
			if ids, err := restored.subagentForChild(owner.ID()).sess.pendingNotificationIDs(); err != nil || len(ids) != 0 {
				t.Fatalf("retained source obligations not observed: ids=%v err=%v", ids, err)
			}
		})
	}
}

func TestColdDirectUserStopIdle(t *testing.T) {
	adapter := newMessagingAdapter("COLD-IDLE-85")
	returns := newActivationReturns()
	terminalBoundary := newWaitBoundary()
	defer terminalBoundary.release()
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("cold-idle", "COLD-IDLE-85") }, func(llm.Request) llm.Response { return finalResponse("root-85") })
	adapter.script("COLD-IDLE-85", func(llm.Request) llm.Response { terminalBoundary.hold(t); return finalResponse("idle-85") })
	root := newMessagingRoot(t, adapter, returns.configure)
	t.Cleanup(func() { returns.close(t, root) })
	if _, err := root.ProcessInput(context.Background(), "start-85", nil); err != nil {
		t.Fatal(err)
	}
	owner := root.subagents.sessions()[0]
	terminal := terminalBoundary.take(t, returns, owner.ID())
	terminalBoundary.release()
	incidentSettled(t, root, owner.ID(), terminal)
	copyDir := copyWaitingState(t, root.stateDir)
	meta, err := schema.LoadSessionMeta(copyDir, root.ID())
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreSessionFromMetaWithConfig(root.client, root.profile, execenv.NewLocalExecutionEnvironment(meta.EnvInfo.WorkingDir), meta, RestoreSessionConfig{
		StateDir: copyDir, ForceRealIO: true, clock: agenttest.NewFakeClock(),
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, disableDelegateIdleRelease: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restored.Close)
	if restored.subagentForChild(owner.ID()) != nil {
		t.Fatal("idle fixture unexpectedly resident")
	}
	if outcome, err := restored.StopDelegateRun(owner.owningDelegateID); err != nil || outcome != appwire.DelegateStopNotRunning {
		t.Fatalf("cold idle Stop=%s err=%v want notRunning", outcome, err)
	}
	if got := len(adapter.requestsFor("COLD-IDLE-85")); got != 1 {
		t.Fatalf("cold idle Stop requests=%d want1", got)
	}
}

func TestWaitResumeClaimSerializesStop(t *testing.T) {
	for _, claimFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("claim_first=%v", claimFirst), func(t *testing.T) {
			adapter := newMessagingAdapter("FENCE-WAIT-81")
			returns := newActivationReturns()
			initialBoundary := newWaitBoundary()
			defer initialBoundary.release()
			fifo, releaseShell := waitProducerFIFO(t)
			defer releaseShell()
			adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("fence-owner", "FENCE-WAIT-81") }, func(llm.Request) llm.Response { return finalResponse("root-81") })
			modelGate := make(chan struct{})
			releaseModel := sync.OnceFunc(func() { close(modelGate) })
			defer releaseModel()
			adapter.script("FENCE-WAIT-81", func(llm.Request) llm.Response {
				args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("read value < %q", fifo), "mode": "background"})
				return toolCallResponse(llm.ToolCallData{ID: "shell-81", Name: "shell", Type: "function", Arguments: args})
			}, func(req llm.Request) llm.Response {
				initialBoundary.hold(t)
				return waitResponseFor("fence-wait-81", regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages)))
			}, func(llm.Request) llm.Response { <-modelGate; return finalResponse("fence-result-81") })
			root := newMessagingRoot(t, adapter, returns.configure)
			t.Cleanup(func() { returns.close(t, root) })
			claimRecorded := make(chan struct{})
			claimEntered, claimGate := make(chan bool, 1), make(chan struct{})
			releaseClaim := sync.OnceFunc(func() { close(claimGate) })
			defer releaseClaim()
			root.SetDescendantRecordedFunc(func(_ string, rec transcript.Record) {
				if cp := rec.Turn.WaitCheckpoint; cp != nil && cp.ResumedByTurnID != "" {
					unlocked := root.delegateController.mu.TryLock()
					if unlocked {
						root.delegateController.mu.Unlock()
					}
					claimEntered <- !unlocked
					close(claimRecorded)
					<-claimGate
				}
			})
			if _, err := root.ProcessInput(context.Background(), "start-81", nil); err != nil {
				t.Fatal(err)
			}
			owner := root.subagents.sessions()[0]
			initial := initialBoundary.take(t, returns, owner.ID())
			initialBoundary.release()
			incidentYielded(t, root, owner.ID(), initial)
			cp, err := readWaitCheckpoint(owner.TranscriptPath(), owner.ID())
			if err != nil || cp == nil {
				t.Fatalf("fence checkpoint=%+v err=%v", cp, err)
			}
			waitYielded(t, root, initial, "fence-wait-81", cp.WaitingOn)
			if claimFirst {
				if err := root.delegateController.enqueueNotification(owner.ID(), notification{ID: "fence-message-81", Message: llm.UserMachinery("fence-payload-81")}); err != nil {
					t.Fatal(err)
				}
				waitForTestSignal(t, claimRecorded, "resume claim reached writer with controller lock held")
				if !<-claimEntered {
					t.Error("claim append is not serialized with Stop")
				}
			}
			stopStarted, stopDone := make(chan struct{}), make(chan struct{})
			t.Cleanup(func() { releaseClaim(); releaseModel(); waitForTestSignal(t, stopDone, "Stop contender cleanup") })
			go func() {
				close(stopStarted)
				result, plan, mutations, err := root.delegateController.StopSubtreeAndDrive(context.Background(), rootDelegateActor(root.ID()), owner.owningDelegateID)
				if err != nil {
					t.Error(err)
					close(stopDone)
					return
				}
				executeDelegateCancelPlan(plan)
				if err := root.executeDelegateMutationPlans(mutations); err != nil {
					t.Error(err)
				}
				releaseModel()
				<-result.done
				close(stopDone)
			}()
			awaitSignal(t, stopStarted, "Stop contender started")
			releaseClaim()
			waitForTestSignal(t, stopDone, "Stop settles exact generation")
			stopped := returns.take(t, owner.ID())
			assertWaitResume(t, initial, stopped)
			incidentSettled(t, root, owner.ID(), stopped)
			data, err := readTranscriptFull(transcriptPath(root.stateDir, owner.ID()), "")
			if err != nil {
				t.Fatal(err)
			}
			claims, finishes := 0, 0
			for _, entry := range data.Entries {
				if cp := entry.Turn.WaitCheckpoint; cp != nil && cp.ResumedByTurnID != "" {
					claims++
				}
			}
			for _, event := range publicationEvents(t, root) {
				if event.DelegateID == owner.owningDelegateID && event.RunFinished != nil {
					finishes++
					if event.RunFinished.Generation != 1 || event.RunFinished.Outcome.Status != delegatestore.OutcomeStopped {
						t.Fatalf("Stop outcome=%+v", event.RunFinished)
					}
				}
			}
			wantClaims := 0
			if claimFirst {
				wantClaims = 1
			}
			if claims != wantClaims || finishes != 1 {
				t.Fatalf("claims=%d want%d, stopped outcomes=%d want1", claims, wantClaims, finishes)
			}
		})
	}
}

func TestUnrelatedNotificationResumesWait(t *testing.T) {
	adapter := newMessagingAdapter("OTHER-WAIT-94", "OTHER-CHILD-94")
	returns := newActivationReturns()
	initialBoundary, resumedBoundary := newWaitBoundary(), newWaitBoundary()
	defer initialBoundary.release()
	defer resumedBoundary.release()
	fifo, releaseShell := waitProducerFIFO(t)
	defer releaseShell()
	updateGate, finishGate := make(chan struct{}), make(chan struct{})
	releaseUpdate := sync.OnceFunc(func() { close(updateGate) })
	releaseChild := sync.OnceFunc(func() { close(finishGate) })
	defer releaseUpdate()
	defer releaseChild()
	resumed := make(chan struct{}, 1)
	var jobID string
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("other-owner", "OTHER-WAIT-94") }, func(llm.Request) llm.Response { return finalResponse("root-94") })
	adapter.script("OTHER-WAIT-94", func(llm.Request) llm.Response {
		args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("read value < %q", fifo), "mode": "background"})
		return toolCallResponse(llm.ToolCallData{ID: "shell-94", Name: "shell", Type: "function", Arguments: args})
	}, func(req llm.Request) llm.Response {
		jobID = regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages))
		return delegateToolResponse("other-child", "OTHER-CHILD-94")
	}, func(llm.Request) llm.Response {
		initialBoundary.hold(t)
		return waitResponseFor("other-wait-94", jobID)
	}, func(llm.Request) llm.Response {
		resumed <- struct{}{}
		resumedBoundary.hold(t)
		return waitResponseFor("other-wait-again-94", jobID)
	})
	adapter.script("OTHER-CHILD-94", func(llm.Request) llm.Response {
		<-updateGate
		return updateResponse("other-update-94", "other-child-payload-94")
	}, func(llm.Request) llm.Response { <-finishGate; return finalResponse("other-child-result-94") })
	root := newMessagingRoot(t, adapter, returns.configure)
	t.Cleanup(func() { returns.close(t, root) })
	if _, err := root.ProcessInput(context.Background(), "start-94", nil); err != nil {
		t.Fatal(err)
	}
	owner := root.subagents.sessions()[0]
	initial := initialBoundary.take(t, returns, owner.ID())
	initialBoundary.release()
	waitYielded(t, root, initial, "other-wait-94", jobID)
	releaseUpdate()
	awaitSignal(t, resumed, "unrelated child update resumes shell wait")
	again := resumedBoundary.take(t, returns, owner.ID())
	assertWaitResume(t, initial, again)
	resumedBoundary.release()
	waitYielded(t, root, again, "other-wait-again-94", jobID)
	journal, err := retirementJobJournal(filepath.Join(jobsDir(root.stateDir, owner.ID()), "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	job := jobstore.Fold(journal)[jobID]
	if job == nil || job.Status.IsTerminal() {
		t.Fatalf("unrelated notification claimed waited job finished: %+v", job)
	}
	if run := delegateAggregateSnapshot(t, root.delegateController, owner.owningDelegateID); run.Generation != 1 || !run.CurrentRunOpen {
		t.Fatalf("unrelated notification changed generation: %+v", run)
	}
	data, err := readTranscriptFull(transcriptPath(root.stateDir, owner.ID()), "")
	if err != nil {
		t.Fatal(err)
	}
	claimed := 0
	for _, entry := range data.Entries {
		if cp := entry.Turn.WaitCheckpoint; cp != nil && cp.CommunicateCallID == "other-wait-94" && cp.ResumedByTurnID != "" {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("unrelated update resume claims=%d want1", claimed)
	}
	releaseChild()
	releaseShell()
}

func TestColdWaitingAncestorRestoresOnlyAddressedDescendant(t *testing.T) {
	adapter := newMessagingAdapter("TREE-WAIT-45", "TREE-CHILD-45")
	returns := newActivationReturns()
	initialBoundary, againBoundary, childBoundary, terminalBoundary := newWaitBoundary(), newWaitBoundary(), newWaitBoundary(), newWaitBoundary()
	defer initialBoundary.release()
	defer againBoundary.release()
	defer childBoundary.release()
	defer terminalBoundary.release()
	fifo, releaseShell := waitProducerFIFO(t)
	defer releaseShell()
	childGate := make(chan struct{})
	releaseChild := sync.OnceFunc(func() { close(childGate) })
	defer releaseChild()
	var childID string
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("tree-owner", "TREE-WAIT-45") }, func(llm.Request) llm.Response { return finalResponse("root-45") })
	adapter.script("TREE-WAIT-45", func(llm.Request) llm.Response { return delegateToolResponse("tree-child", "TREE-CHILD-45") }, func(req llm.Request) llm.Response {
		childID = deferredHeadDelegateID.FindString(lastToolResultText(req.Messages))
		initialBoundary.hold(t)
		return waitResponseFor("tree-wait-45", childID)
	}, func(llm.Request) llm.Response {
		againBoundary.hold(t)
		return waitResponseFor("tree-wait-again-45", childID)
	})
	adapter.script("TREE-CHILD-45", func(llm.Request) llm.Response {
		<-childGate
		args, _ := json.Marshal(map[string]any{"command": fmt.Sprintf("read value < %q", fifo), "mode": "background"})
		return toolCallResponse(llm.ToolCallData{ID: "shell-45", Name: "shell", Type: "function", Arguments: args})
	}, func(req llm.Request) llm.Response {
		childBoundary.hold(t)
		return waitResponseFor("tree-child-wait-45", regexp.MustCompile(`job_[A-Za-z0-9_]{10,}`).FindString(lastToolResultText(req.Messages)))
	})
	root := newMessagingRoot(t, adapter, returns.configure)
	t.Cleanup(func() { returns.close(t, root) })
	if _, err := root.ProcessInput(context.Background(), "start-45", nil); err != nil {
		t.Fatal(err)
	}
	owner := root.subagents.sessions()[0]
	initial := initialBoundary.take(t, returns, owner.ID())
	initialBoundary.release()
	waitYielded(t, root, initial, "tree-wait-45", childID)
	releaseChild()
	child := owner.subagents.sessions()[0]
	childInitial := childBoundary.take(t, returns, child.ID())
	childBoundary.release()
	incidentYielded(t, owner, child.ID(), childInitial)
	childCP, err := readWaitCheckpoint(child.TranscriptPath(), child.ID())
	if err != nil || childCP == nil {
		t.Fatalf("child checkpoint=%+v err=%v", childCP, err)
	}
	waitYielded(t, owner, childInitial, "tree-child-wait-45", childCP.WaitingOn)
	adapter.waitForRequests(t, "TREE-WAIT-45", 3)
	again := againBoundary.take(t, returns, owner.ID())
	assertWaitResume(t, initial, again)
	againBoundary.release()
	waitYielded(t, root, again, "tree-wait-again-45", childID)
	copyDir := copyWaitingState(t, root.stateDir)
	entered, finish := make(chan struct{}), make(chan struct{})
	releaseFinish := sync.OnceFunc(func() { close(finish) })
	defer releaseFinish()
	adapter.script("TREE-CHILD-45", func(llm.Request) llm.Response {
		close(entered)
		<-finish
		return finalResponse("restored-child-result-45")
	})
	adapter.script("TREE-WAIT-45", func(llm.Request) llm.Response {
		terminalBoundary.hold(t)
		return finalResponse("restored-ancestor-result-45")
	})
	restoredReturns := newActivationReturns()
	restored := restoreWaitingJoinCopy(t, root, copyDir, restoredReturns)
	// The server installs the outer input owner after bootstrap returns.
	restored.SetNotifyFunc(func() {})
	awaitSignal(t, entered, "cold addressed descendant resume")
	if requests := len(adapter.requestsFor("TREE-WAIT-45")); requests != 3 {
		t.Fatalf("ancestor model ran to restore child, requests=%d want3", requests)
	}
	for _, id := range []string{owner.owningDelegateID, child.owningDelegateID} {
		run := delegateAggregateSnapshot(t, restored.delegateController, id)
		if run.Generation != 1 || !run.CurrentRunOpen || run.LatestOutcome != nil {
			t.Fatalf("restored open wait=%+v", run)
		}
	}
	releaseFinish()
	terminal := terminalBoundary.take(t, restoredReturns, owner.ID())
	if terminal.sessionID != again.sessionID || terminal.delegateID != again.delegateID || terminal.lease != again.lease {
		t.Fatalf("cold ancestor lease changed: %+v -> %+v", again, terminal)
	}
	terminalBoundary.release()
	incidentSettled(t, restored, owner.ID(), terminal)
	starts := map[string]int{}
	for _, event := range publicationEvents(t, restored) {
		if event.RunStarted != nil {
			starts[event.DelegateID]++
		}
	}
	if starts[owner.owningDelegateID] != 1 || starts[child.owningDelegateID] != 1 {
		t.Fatalf("replacement RunStarted events=%v", starts)
	}
}
