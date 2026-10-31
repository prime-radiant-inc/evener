package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	toolpkg "primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func publicationEvents(t *testing.T, root *Session) []delegatestore.Event {
	t.Helper()
	events, err := delegatestore.ReadEvents(delegateResourceStorePath(root.stateDir, root.ID()))
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func publicationOutput(id string) map[string]any {
	return map[string]any{"message": id, "data": map[string]any{"id": id}, "artifacts": []any{id + ".json"}}
}

func publicationCall(id string, args map[string]any) llm.ToolCallData {
	raw, _ := json.Marshal(args)
	return llm.ToolCallData{ID: id, Name: "communicate", Arguments: raw, Type: "function"}
}

func TestNotificationPublication(t *testing.T) {
	t.Parallel()
	for _, readiness := range []string{"", "ready"} {
		t.Run("update/"+readiness, func(t *testing.T) {
			adapter := newMessagingAdapter("PUB-WORKER")
			entered, gate := make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(gate) })
			defer release()
			adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("spawn", "PUB-WORKER") }, func(llm.Request) llm.Response { return finalResponse("root-finished") })
			output := publicationOutput("update-763d")
			call := publicationCall("publication-call", map[string]any{"message": "update-763d", "end_turn": false, "readiness": readiness, "output": output})
			adapter.script("PUB-WORKER", func(llm.Request) llm.Response { return toolCallResponse(call) }, func(llm.Request) llm.Response { close(entered); <-gate; return finalResponse("worker-finished") })
			root := newMessagingRoot(t, adapter, nil)
			if _, err := root.ProcessInput(context.Background(), "start", nil); err != nil {
				t.Fatal(err)
			}
			awaitSignal(t, entered, "update tool batch")
			var updates []delegatestore.Event
			for _, e := range publicationEvents(t, root) {
				if e.UpdatePosted != nil {
					updates = append(updates, e)
				}
			}
			if len(updates) != 1 {
				t.Fatalf("updates=%d, want 1", len(updates))
			}
			raw, _ := json.Marshal(updates[0].UpdatePosted)
			var value struct {
				Communicate *schema.CommunicateInfo       `json:"communicate"`
				Packet      *delegatestore.TerminalPacket `json:"packet"`
			}
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatal(err)
			}
			if value.Communicate == nil || value.Communicate.CallID != call.ID || value.Communicate.Message != "update-763d" || value.Communicate.EndTurn || value.Packet == nil {
				t.Fatalf("captured publication=%s", raw)
			}
			var got any
			if err := json.Unmarshal(value.Packet.StructuredResult, &got); err != nil {
				t.Fatal(err)
			}
			want, _ := json.Marshal(output)
			actual, _ := json.Marshal(got)
			if !bytes.Equal(want, actual) {
				t.Fatalf("structured=%s, want %s", actual, want)
			}
			if len(rootAttentionFold(t, root).order) != 1 {
				t.Fatal("update did not reach owner immediately")
			}
			t.Logf("source seq=%d generation=%d call=%s receiver=%v", updates[0].Seq, updates[0].UpdatePosted.Generation, call.ID, rootAttentionFold(t, root).order)
		})
	}
	t.Run("waiting publishes before target settles", func(t *testing.T) {
		adapter := newMessagingAdapter("WAIT-WORKER", "WAIT-TARGET")
		returns := newActivationReturns()
		workerRequest, targetRequest := make(chan llm.Request, 1), make(chan llm.Request, 1)
		publicationGate := make(chan struct{})
		releasePublication := sync.OnceFunc(func() { close(publicationGate) })
		defer releasePublication()
		adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("spawn", "WAIT-WORKER") }, func(llm.Request) llm.Response { return finalResponse("root-finished") })
		gate := make(chan struct{})
		release := sync.OnceFunc(func() { close(gate) })
		defer release()
		adapter.script("WAIT-TARGET", func(req llm.Request) llm.Response {
			targetRequest <- req
			<-gate
			return finalResponse("target-finished")
		})
		adapter.script("WAIT-WORKER", func(llm.Request) llm.Response { return delegateToolResponse("target", "WAIT-TARGET") }, func(req llm.Request) llm.Response {
			workerRequest <- req
			<-publicationGate
			id := deferredHeadDelegateID.FindString(lastToolResultText(req.Messages))
			return toolCallResponse(publicationCall("wait-publication", map[string]any{"message": "wait-43ac", "end_turn": true, "end_reason": "waiting_on_work", "waiting_on": id, "output": publicationOutput("wait-43ac")}))
		})
		root := newMessagingRoot(t, adapter, returns.configure)
		t.Cleanup(func() {
			releasePublication()
			release()
			returns.close(t, root)
		})
		if _, err := root.ProcessInput(context.Background(), "start", nil); err != nil {
			t.Fatal(err)
		}
		request := <-workerRequest
		target := <-targetRequest
		child := root.getSub(request.SessionID).sess
		activation := returns.take(t, child.ID())
		targetActivation := returns.take(t, target.SessionID)
		activationStillOpen(t, targetActivation.generationDone)
		releasePublication()
		incidentYielded(t, root, child.ID(), activation)
		if !child.Communicated() || len(rootAttentionFold(t, root).order) != 1 {
			t.Fatal("yielded wait did not publish its accepted message to the owner")
		}
		activationStillOpen(t, targetActivation.generationDone)
		for _, e := range publicationEvents(t, root) {
			if u := e.UpdatePosted; u != nil {
				if u.Communicate == nil || u.Communicate.CallID != "wait-publication" || u.Communicate.WaitingOn == "" || !u.Communicate.EndTurn || len(u.Packet.StructuredResult) == 0 {
					t.Fatalf("wait tuple=%+v", u)
				}
				frame := rootAttentionFold(t, root).content[rootAttentionFold(t, root).order[0]].Text()
				if !strings.Contains(frame, u.Communicate.WaitingOn) || !strings.Contains(frame, "wait-43ac.json") {
					t.Fatalf("wait payload missing from receiver: %s", frame)
				}
			}
		}
		for _, e := range publicationEvents(t, root) {
			if e.RunFinished != nil {
				t.Fatal("waiting published terminal outcome")
			}
		}
	})
	t.Run("two calls capture independent tuples", func(t *testing.T) {
		adapter := newMessagingAdapter("TUPLE-WORKER")
		adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("spawn", "TUPLE-WORKER") }, func(llm.Request) llm.Response { return finalResponse("root-finished") })
		adapter.script("TUPLE-WORKER", func(llm.Request) llm.Response {
			return toolCallResponse(
				publicationCall("tuple-update", map[string]any{"message": "first-update", "end_turn": false, "blocker": "old-blocker", "output": publicationOutput("first")}),
				publicationCall("tuple-terminal", map[string]any{"message": "second-terminal", "end_turn": true, "end_reason": "blocked", "blocker": "current-blocker", "output": publicationOutput("second")}),
			)
		})
		root := newMessagingRoot(t, adapter, nil)
		if _, err := root.ProcessInput(context.Background(), "start", nil); err != nil {
			t.Fatal(err)
		}
		// The synchronous root tool admitted the sole terminal child. No sibling
		// or successor can keep this owning sender tail open.
		root.sendersWG.Wait()
		child := root.subagents.sessions()[0]
		sub := root.getSub(child.ID())
		sub.mu.Lock()
		err := sub.err
		sub.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		var updateID string
		var update *delegatestore.UpdatePosted
		for _, e := range publicationEvents(t, root) {
			if e.UpdatePosted != nil {
				update = e.UpdatePosted
				updateID = fmt.Sprintf("delegate:%s/update/%d", e.DelegateID, e.Seq)
			}
			if e.TerminalPrepared != nil {
				p := e.TerminalPrepared
				var metadata delegateTerminalPacketMetadata
				if err := json.Unmarshal(p.Packet.Metadata, &metadata); err != nil {
					t.Fatal(err)
				}
				if metadata.Blocker != "current-blocker" || metadata.Disposition != "blocked" || string(p.Packet.Message) != `"second-terminal"` || !strings.Contains(string(p.Packet.StructuredResult), "second.json") {
					t.Fatalf("terminal tuple=%+v", p)
				}
			}
		}
		if update == nil || update.Communicate.CallID != "tuple-update" || update.Communicate.Blocker != "" || !strings.Contains(string(update.Packet.StructuredResult), "first.json") {
			t.Fatalf("update tuple=%+v", update)
		}
		wantIDs := []string{updateID, delegateAttentionID(child.owningDelegateID + "/delivery/1")}
		if ids := rootAttentionFold(t, root).order; !slices.Equal(ids, wantIDs) {
			t.Fatalf("same-batch publications=%v, want %v", ids, wantIDs)
		}
	})
	t.Run("owning batch and same call retry", testPublicationOwningBatch)
	t.Run("generation turnover discards admission authority", func(t *testing.T) {
		entered, gate := make(chan struct{}), make(chan struct{})
		releaseNext := sync.OnceFunc(func() { close(gate) })
		defer releaseNext()
		root, child, lease, release := publicationRunningChild(t, func(adapter *messagingAdapter) {
			adapter.script("BATCH-WORKER", func(llm.Request) llm.Response { close(entered); <-gate; return finalResponse("generation two") })
		})
		ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease)
		out := publicationExecute(ctx, child, publicationCall("first-generation-done", map[string]any{"message": "generation one", "end_turn": true, "output": publicationOutput("one")}))
		if out.IsError {
			t.Fatalf("done: %+v", out)
		}
		c := root.delegateController
		c.mu.Lock()
		old := c.live[lease.delegateID].binding.evidence
		admitted := old.completionAdmitted
		c.mu.Unlock()
		if !admitted {
			t.Fatal("accepted done did not install generation fence")
		}
		sub := root.getSub(child.ID())
		sub.mu.Lock()
		generationDone := sub.done
		sub.mu.Unlock()
		activationStillOpen(t, generationDone)
		release()
		waitForTestSignal(t, generationDone, "first publication generation")
		root.sendersWG.Wait()
		c.mu.Lock()
		quiesced := !c.durable[lease.delegateID].CurrentRunOpen && c.finalizingLocked(lease.delegateID) == nil
		c.mu.Unlock()
		if !quiesced {
			t.Fatal("first generation retained finalization after its sender returned")
		}
		sub.mu.Lock()
		err := sub.err
		sub.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		call := publicationCall("restart-generation", map[string]any{"to": lease.delegateID, "message": "continue", "max_wait_ms": 0})
		call.Name = "delegate_send"
		out = publicationExecute(context.Background(), root, call)
		if out.IsError {
			t.Fatalf("restart: %+v", out)
		}
		awaitSignal(t, entered, "second generation provider")
		c.mu.Lock()
		current := c.live[lease.delegateID].binding.evidence
		generation := c.live[lease.delegateID].binding.lease.generation
		c.mu.Unlock()
		if generation != 2 || current == old || current.completionAdmitted || current.batch != nil {
			t.Fatalf("generation=%d old=%p current=%+v", generation, old, current)
		}
		if _, _, err := c.PostUpdate(delegateActor{lease: &lease}, schema.CommunicateInfo{Message: "old"}, publicationPacket("old"), ""); !errors.Is(err, errDelegateStaleLease) {
			t.Fatalf("old generation publish=%v", err)
		}
	})
}

// The child is created by the real delegate tool. Only its scripted provider is held.
func publicationRunningChild(t *testing.T, configure ...func(*messagingAdapter)) (*Session, *Session, delegateLease, func()) {
	t.Helper()
	adapter := newMessagingAdapter("BATCH-WORKER")
	entered, gate := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("spawn", "BATCH-WORKER") }, func(llm.Request) llm.Response { return finalResponse("root-finished") })
	adapter.script("BATCH-WORKER", func(llm.Request) llm.Response { close(entered); <-gate; return finalResponse("worker-finished") })
	for _, f := range configure {
		f(adapter)
	}
	root := newMessagingRoot(t, adapter, nil)
	t.Cleanup(release)
	if _, err := root.ProcessInput(context.Background(), "start", nil); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, entered, "running child")
	child := root.subagents.sessions()[0]
	c := root.delegateController
	c.mu.Lock()
	lease := c.live[child.owningDelegateID].binding.lease
	c.mu.Unlock()
	return root, child, lease, release
}

func publicationExecute(ctx context.Context, s *Session, call llm.ToolCallData) toolpkg.ExecResult {
	return s.reg.ExecuteCall(context.WithValue(ctx, ctxToolCallID, call.ID), s.currentEnv(), call)
}

func testPublicationOwningBatch(t *testing.T) {
	root, child, lease, _ := publicationRunningChild(t)
	jobPath := filepath.Join(child.jobManager.dir, "jobs.jsonl")
	original, readErr := os.ReadFile(jobPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if err := os.WriteFile(jobPath, []byte("invalid inventory journal\n"), 0600); err != nil {
		t.Fatal(err)
	}
	restore := sync.OnceFunc(func() {
		if os.IsNotExist(readErr) {
			if err := os.Remove(jobPath); err != nil {
				t.Error(err)
			}
		} else if err := os.WriteFile(jobPath, original, 0600); err != nil {
			t.Error(err)
		}
	})
	defer restore()
	if work, err := child.outstandingDelegateWork(); err == nil || len(work.Unknown) == 0 {
		t.Fatalf("inventory=%+v err=%v", work, err)
	}
	ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease)
	ctx, end, err := child.beginDelegateToolBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer end()
	c := root.delegateController
	claim, continued, err := c.BeginFinalization(lease, delegateSettlementOrdinary)
	if err != nil || continued || claim == nil {
		t.Fatalf("claim=%+v continued=%v err=%v", claim, continued, err)
	}
	defer c.ReleaseSettlementClaim(claim)
	c.mu.Lock()
	blocked := c.finalizationReadyLocked(claim)
	c.mu.Unlock()
	if !errors.Is(blocked, errDelegateTargetBusy) {
		t.Fatalf("live batch finalization=%v", blocked)
	}
	call := publicationCall("same-call-retry", map[string]any{"message": "owning-batch-578c", "end_turn": false, "output": publicationOutput("owning-batch-578c")})
	for i := range 2 {
		out := publicationExecute(ctx, child, call)
		if out.IsError || !strings.Contains(out.Output, `"delivery":"delivered"`) {
			t.Fatalf("attempt %d: %+v", i, out)
		}
	}
	updates := 0
	for _, e := range publicationEvents(t, root) {
		if e.UpdatePosted != nil {
			updates++
		}
	}
	if updates != 1 || len(rootAttentionFold(t, root).order) != 1 {
		t.Fatalf("retry updates=%d receiver=%v", updates, rootAttentionFold(t, root).order)
	}
	end()
	end() // exactly-once close, including a deferred second close
	select {
	case <-claim.batchReady:
	default:
		t.Fatal("batch close did not release settlement")
	}
	c.mu.Lock()
	ready := c.finalizationReadyLocked(claim)
	c.mu.Unlock()
	if ready != nil {
		t.Fatalf("closed batch still fences finalization: %v", ready)
	}
	actor, err := child.delegateActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.PostUpdate(actor, schema.CommunicateInfo{CallID: "closed-batch", Message: "late"}, publicationPacket("late"), ""); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("closed actor publication=%v", err)
	}
	stale := *actor.lease
	stale.generation++
	actor.lease = &stale
	if _, _, err := c.PostUpdate(actor, schema.CommunicateInfo{CallID: "stale", Message: "stale"}, publicationPacket("stale"), ""); !errors.Is(err, errDelegateStaleLease) {
		t.Fatalf("stale actor publication=%v", err)
	}
}

func TestCompletionWithLiveChild(t *testing.T) {
	t.Parallel()
	t.Run("aborted reservation permits progress", func(t *testing.T) {
		root, child, lease, _ := publicationRunningChild(t)
		ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease)
		actor, err := child.delegateActor(ctx)
		if err != nil {
			t.Fatal(err)
		}
		c := root.delegateController
		descriptor := delegateAggregateSnapshot(t, c, lease.delegateID).Descriptor
		descriptor.Task = "reserved-child"
		reservation, err := c.ReserveCreate(actor, descriptor)
		if err != nil {
			t.Fatal(err)
		}
		out := publicationExecute(ctx, child, publicationCall("reserved-done", map[string]any{"message": "done", "end_turn": true, "output": publicationOutput("done")}))
		if !out.IsError {
			t.Fatalf("reserved child failed to fence done: %+v", out)
		}
		if err := c.AbortStart(reservation); err != nil {
			t.Fatal(err)
		}
		// The refusal must not install the successful-done fence.
		next, err := c.ReserveCreate(actor, descriptor)
		if err != nil {
			t.Fatalf("refused done fenced subsequent child work: %v", err)
		}
		if err := c.AbortStart(next); err != nil {
			t.Fatal(err)
		}
		out = publicationExecute(ctx, child, publicationCall("after-abort", map[string]any{"message": "now done", "end_turn": true, "output": publicationOutput("now done")}))
		if out.IsError || !strings.Contains(out.Output, `"accepted":true`) {
			t.Fatalf("aborted reservation still fences done: %+v", out)
		}
		if _, err := c.ReserveCreate(actor, descriptor); !errors.Is(err, errDelegateTargetBusy) {
			t.Fatalf("accepted done did not fence child work: %v", err)
		}
	})
	for _, order := range []string{"existing child", "child then done", "done then child"} {
		t.Run(order, func(t *testing.T) {
			adapter := newMessagingAdapter("COORD-PUB", "LIVE-PUB")
			returns := newActivationReturns()
			providerGate, inspectGate := make(chan struct{}), make(chan struct{})
			releaseProvider := sync.OnceFunc(func() { close(providerGate) })
			releaseInspect := sync.OnceFunc(func() { close(inspectGate) })
			defer releaseProvider()
			defer releaseInspect()
			providerRequest, childRequest := make(chan llm.Request, 1), make(chan llm.Request, 1)
			first := func(response llm.Response) func(llm.Request) llm.Response {
				return func(req llm.Request) llm.Response {
					providerRequest <- req
					<-providerGate
					return response
				}
			}
			childGate := make(chan struct{})
			release := sync.OnceFunc(func() { close(childGate) })
			defer release()
			inspected := make(chan llm.Request, 1)
			adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("coordinator", "COORD-PUB") }, func(llm.Request) llm.Response { return finalResponse("root-finished") })
			create := delegateToolResponse("child", "LIVE-PUB").ToolCalls()[0]
			done := publicationCall("premature-done", map[string]any{"message": "done-b83f", "end_turn": true, "output": publicationOutput("done-b83f")})
			switch order {
			case "existing child":
				adapter.script("COORD-PUB", first(toolCallResponse(create)), func(llm.Request) llm.Response { return toolCallResponse(done) })
			case "child then done":
				adapter.script("COORD-PUB", first(toolCallResponse(create, done)))
			default:
				adapter.script("COORD-PUB", first(toolCallResponse(done, create)))
			}
			adapter.script("COORD-PUB", func(req llm.Request) llm.Response {
				inspected <- req
				<-inspectGate
				var target string
				for _, message := range req.Messages {
					for _, part := range message.Content {
						if part.ToolResult != nil && part.ToolResult.ToolCallID == create.ID {
							target = deferredHeadDelegateID.FindString(fmt.Sprint(part.ToolResult.Content))
						}
					}
				}
				return toolCallResponse(publicationCall("valid-wait", map[string]any{"message": "wait-8cc1", "end_turn": true, "end_reason": "waiting_on_work", "waiting_on": target}))
			})
			adapter.script("LIVE-PUB", func(req llm.Request) llm.Response {
				childRequest <- req
				<-childGate
				return finalResponse("child-finished")
			})
			root := newMessagingRoot(t, adapter, returns.configure)
			t.Cleanup(func() {
				releaseProvider()
				releaseInspect()
				release()
				returns.close(t, root)
			})
			if _, err := root.ProcessInput(context.Background(), "start", nil); err != nil {
				t.Fatal(err)
			}
			initial := <-providerRequest
			coordinator := root.getSub(initial.SessionID).sess
			activation := returns.take(t, coordinator.ID())
			activationStillOpen(t, activation.generationDone)
			releaseProvider()
			if order == "done then child" {
				incidentSettled(t, root, coordinator.ID(), activation)
				sub := root.getSub(coordinator.ID())
				sub.mu.Lock()
				err := sub.err
				sub.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				count := 0
				var finished *delegatestore.RunFinished
				var coordinatorID string
				for _, e := range publicationEvents(t, root) {
					if e.Created != nil {
						count++
						coordinatorID = e.DelegateID
					}
					if e.RunFinished != nil {
						finished = e.RunFinished
					}
				}
				if count != 1 {
					t.Fatalf("created=%d, want accepted done to refuse later child creation", count)
				}
				settled := publicationReopen(t, root)[coordinatorID]
				if finished == nil || finished.Outcome.Status != delegatestore.OutcomeCompleted || finished.Disposition != delegatestore.DispositionReported || settled.LatestPacket == nil || settled.LatestPacket.Kind != delegatestore.PacketReported || string(settled.LatestPacket.Message) != `"done-b83f"` {
					t.Fatalf("done-then-child finish=%+v packet=%+v, want completed reported done-b83f", finished, settled.LatestPacket)
				}
				accepted := publicationToolResult(t, coordinator, done.ID)
				var result struct {
					Accepted bool `json:"accepted"`
				}
				if err := json.Unmarshal([]byte(fmt.Sprint(accepted.Content)), &result); err != nil || accepted.IsError || !result.Accepted {
					t.Fatalf("done result=%+v decode=%v, want accepted done", accepted, err)
				}
				refused := publicationToolResult(t, coordinator, create.ID)
				if !refused.IsError {
					t.Fatalf("child result=%+v, want ordinary tool-error refusal", refused)
				}
				t.Logf("coordinator=%s generation=%d outcome=%s packet=%s message=%s done accepted=%t child refused=%t", coordinatorID, finished.Generation, finished.Outcome.Status, settled.LatestPacket.Kind, settled.LatestPacket.Message, result.Accepted, refused.IsError)
				return
			}
			req := <-inspected
			if req.SessionID != coordinator.ID() {
				t.Fatalf("inspection session=%s, want %s", req.SessionID, coordinator.ID())
			}
			var refused bool
			for _, m := range req.Messages {
				for _, p := range m.Content {
					if p.ToolResult != nil && p.ToolResult.ToolCallID == done.ID {
						refused = p.ToolResult.IsError
					}
				}
			}
			if !refused {
				t.Fatal("done with live child claimed acceptance")
			}
			for _, e := range publicationEvents(t, root) {
				if e.TerminalPrepared != nil || e.RunFinished != nil {
					t.Fatal("refused done produced terminal evidence")
				}
			}
			liveRequest := <-childRequest
			liveActivation := returns.take(t, liveRequest.SessionID)
			activationStillOpen(t, liveActivation.generationDone)
			releaseInspect()
			incidentYielded(t, root, coordinator.ID(), activation)
			if !coordinator.Communicated() {
				t.Fatal("yielded coordinator did not accept its waiting publication")
			}
			activationStillOpen(t, liveActivation.generationDone)
			if len(rootAttentionFold(t, root).order) != 1 {
				t.Fatal("valid waiting message did not reach owner")
			}
			release()
		})
	}
}

// Done fences child work in its own input, not work a later admitted input owns.
func TestCompletionAdmissionRetiresOnSameGenerationInput(t *testing.T) {
	adapter := newMessagingAdapter("CONTINUATION-COORD", "CONTINUATION-CHILD")
	returns := newActivationReturns()
	providerEntered, providerGate := make(chan struct{}), make(chan struct{})
	doneExecuted, batchGate := make(chan struct{}), make(chan struct{})
	childGate, inspectGate := make(chan struct{}), make(chan struct{})
	continuationGate := make(chan struct{})
	releaseContinuation := sync.OnceFunc(func() { close(continuationGate) })
	releaseProvider := sync.OnceFunc(func() { close(providerGate) })
	releaseBatch := sync.OnceFunc(func() { close(batchGate) })
	releaseChild := sync.OnceFunc(func() { close(childGate) })
	releaseInspect := sync.OnceFunc(func() { close(inspectGate) })
	defer releaseProvider()
	defer releaseBatch()
	defer releaseChild()
	defer releaseInspect()
	defer releaseContinuation()
	done := publicationCall("continuation-done", map[string]any{"message": "done-before-steer", "end_turn": true, "output": publicationOutput("done-before-steer")})
	create := delegateToolResponse("continuation-create", "CONTINUATION-CHILD").ToolCalls()[0]
	continuation, inspected := make(chan llm.Request, 1), make(chan llm.Request, 1)
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("coordinator", "CONTINUATION-COORD") }, func(llm.Request) llm.Response { return finalResponse("root-finished") })
	adapter.script("CONTINUATION-COORD", func(llm.Request) llm.Response {
		close(providerEntered)
		<-providerGate
		return toolCallResponse(done)
	}, func(req llm.Request) llm.Response {
		continuation <- req
		<-continuationGate
		return toolCallResponse(create)
	}, func(req llm.Request) llm.Response {
		inspected <- req
		<-inspectGate
		target := deferredHeadDelegateID.FindString(lastToolResultText(req.Messages))
		return toolCallResponse(publicationCall("continuation-wait", map[string]any{"message": "waiting for follow-up child", "end_turn": true, "end_reason": "waiting_on_work", "waiting_on": target}))
	})
	adapter.script("CONTINUATION-CHILD", func(llm.Request) llm.Response { <-childGate; return finalResponse("follow-up finished") })
	root := newMessagingRoot(t, adapter, returns.configure)
	t.Cleanup(func() {
		releaseProvider()
		releaseBatch()
		releaseContinuation()
		releaseInspect()
		releaseChild()
		returns.close(t, root)
	})
	if _, err := root.ProcessInput(context.Background(), "start", nil); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, providerEntered, "coordinator initial provider")
	coordinator := root.subagents.sessions()[0]
	activation := returns.take(t, coordinator.ID())
	activationStillOpen(t, activation.generationDone)
	c := root.delegateController
	c.mu.Lock()
	lease := c.live[coordinator.owningDelegateID].binding.lease
	c.mu.Unlock()
	// Install only an observer while this provider is held, not admission state.
	holdDone := sync.OnceFunc(func() { close(doneExecuted); <-batchGate })
	coordinator.cfg.testOnly.execToolCheckpoint = func(checkpoint string) {
		if checkpoint == "after_execute" {
			holdDone()
		}
	}
	releaseProvider()
	awaitSignal(t, doneExecuted, "accepted done tool")
	if !coordinator.Communicated() {
		t.Fatal("initial done was not accepted")
	}
	c.mu.Lock()
	admitted := c.live[lease.delegateID].binding.evidence.completionAdmitted
	c.mu.Unlock()
	if !admitted {
		t.Fatal("initial done did not install child admission fence")
	}
	for _, e := range publicationEvents(t, root) {
		if (e.Created != nil && e.DelegateID != lease.delegateID) || e.RunFinished != nil {
			t.Fatalf("unexpected child or settlement before steering: %+v", e)
		}
	}
	steer := delegateSendResponse("continue-owner-steer", lease.delegateID, "follow-up-74ac, create the requested child", 0).ToolCalls()[0]
	result := publicationExecute(context.Background(), root, steer)
	var steered sendMessageResult
	if err := json.Unmarshal(result.ToolState, &steered); err != nil || result.IsError || steered.Action != "steered" {
		t.Fatalf("owner steer=%+v decode=%v, want steering into running generation", result, err)
	}
	releaseBatch()
	req := <-continuation
	if !requestContainsText(req, "follow-up-74ac") {
		t.Fatal("continuation request did not carry admitted owner steering")
	}
	releaseContinuation()
	inspection := <-inspected
	if inspection.SessionID != coordinator.ID() {
		t.Fatalf("inspection session=%s, want %s", inspection.SessionID, coordinator.ID())
	}
	c.mu.Lock()
	next := c.live[lease.delegateID].binding.lease
	c.mu.Unlock()
	if next != lease {
		t.Fatalf("continuation changed generation lease: before=%+v after=%+v", lease, next)
	}
	accepted := publicationToolResult(t, coordinator, done.ID)
	var doneResult struct {
		Accepted bool `json:"accepted"`
	}
	if err := json.Unmarshal([]byte(fmt.Sprint(accepted.Content)), &doneResult); err != nil || accepted.IsError || !doneResult.Accepted {
		t.Fatalf("initial done result=%+v decode=%v", accepted, err)
	}
	created := publicationToolResult(t, coordinator, create.ID)
	if created.IsError {
		t.Fatalf("same-generation continuation child create refused: lease=%+v result=%+v", next, created)
	}
	starts, children := 0, 0
	for _, e := range publicationEvents(t, root) {
		if e.DelegateID == lease.delegateID && e.RunStarted != nil {
			starts++
		}
		if e.Created != nil && e.Created.Descriptor.ParentDelegateID == lease.delegateID {
			children++
		}
	}
	if starts != 1 || children != 1 {
		t.Fatalf("coordinator starts=%d children=%d, want same-generation child creation", starts, children)
	}
	releaseInspect()
	incidentYielded(t, root, coordinator.ID(), activation)
	t.Logf("coordinator=%s generation=%d lease unchanged, RunStarted=%d accepted done, admitted owner steer carried, child Created=%d", lease.delegateID, lease.generation, starts, children)
}

func publicationToolResult(t *testing.T, s *Session, callID string) llm.ToolResultData {
	t.Helper()
	_, entries, _, err := readTranscript(transcriptPath(s.stateDir, s.ID()), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		for _, part := range entry.Turn.Message.Content {
			if part.ToolResult != nil && part.ToolResult.ToolCallID == callID {
				return *part.ToolResult
			}
		}
	}
	t.Fatalf("session %s has no persisted result for tool call %s", s.ID(), callID)
	return llm.ToolResultData{}
}

func TestNotificationPublicationFinalizationJoinsBothFences(t *testing.T) {
	for _, first := range []string{"quiet", "batch"} {
		t.Run(first, func(t *testing.T) {
			root, child, lease, _ := publicationRunningChild(t)
			c := root.delegateController
			ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease)
			_, end, err := child.beginDelegateToolBatch(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer end()
			quiet, err := c.BeginQuietAttention(root, lease, time.Now().Add(2*delegateQuietWindow))
			if err != nil || quiet == nil {
				t.Fatalf("quiet=%+v err=%v", quiet, err)
			}
			claim, _, err := c.BeginFinalization(lease, delegateSettlementTerminal)
			if err != nil {
				t.Fatal(err)
			}
			defer c.ReleaseSettlementClaim(claim)
			check := func(want error) {
				t.Helper()
				c.mu.Lock()
				err := c.finalizationReadyLocked(claim)
				c.mu.Unlock()
				if !errors.Is(err, want) {
					t.Fatalf("ready=%v want=%v", err, want)
				}
			}
			check(errDelegateTargetBusy)
			if first == "quiet" {
				if err := c.CompleteQuietAttention(quiet, false); err != nil {
					t.Fatal(err)
				}
				check(errDelegateTargetBusy)
				end()
			} else {
				end()
				check(errDelegateTargetBusy)
				if err := c.CompleteQuietAttention(quiet, false); err != nil {
					t.Fatal(err)
				}
			}
			check(nil)
		})
	}
}

func TestTerminalNotificationFollowsSettlement(t *testing.T) {
	t.Parallel()
	for _, disposition := range []string{"done", "blocked", "later error"} {
		t.Run(disposition, func(t *testing.T) {
			adapter := newMessagingAdapter("TERMINAL-PUB")
			held, gate := make(chan *subagent, 1), make(chan struct{})
			release := sync.OnceFunc(func() { close(gate) })
			defer release()
			adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("spawn", "TERMINAL-PUB") },
				func(llm.Request) llm.Response { return finalResponse("root-finished") },
				// The notification response earns the receipt asserted below, not an unscripted adapter answer.
				func(llm.Request) llm.Response { return finalResponse("done") })
			output := publicationOutput("terminal-993f")
			adapter.script("TERMINAL-PUB", func(llm.Request) llm.Response {
				reason := disposition
				if reason == "later error" {
					reason = "done"
				}
				return toolCallResponse(publicationCall("accepted-terminal", map[string]any{"message": "terminal-993f", "end_turn": true, "end_reason": reason, "blocker": "blocker-9f7b", "output": output}))
			})
			root := newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
				cfg.testOnly.subagentAfterFinalStatePublish = func(a *subagent) { held <- a; <-gate }
			})
			t.Cleanup(func() {
				release()
				root.Close()
				root.sendersWG.Wait()
			})
			if _, err := root.ProcessInput(context.Background(), "start", nil); err != nil {
				t.Fatal(err)
			}
			var sub *subagent
			select {
			case sub = <-held:
			// TRIPWIRE: the scripted child sends its actual held settlement, only a hang reaches this guard.
			case <-time.After(30 * time.Second):
				t.Fatal("settlement boundary not reached")
			}
			sub.mu.Lock()
			generationDone := sub.done
			sub.mu.Unlock()
			c := root.delegateController
			c.mu.Lock()
			lease := c.live[sub.sess.owningDelegateID].binding.lease
			c.mu.Unlock()
			activationStillOpen(t, generationDone)
			events := publicationEvents(t, root)
			prepared, finished := 0, 0
			for _, e := range events {
				if e.TerminalPrepared != nil {
					prepared++
				}
				if e.RunFinished != nil {
					finished++
				}
			}
			if prepared != 1 || finished != 0 || len(rootAttentionFold(t, root).order) != 0 {
				t.Fatalf("prepared=%d finished=%d receiver=%v", prepared, finished, rootAttentionFold(t, root).order)
			}
			reopened := publicationReopen(t, root)
			aggregate := reopened[sub.sess.owningDelegateID]
			if aggregate.PreparedTerminal == nil || !aggregate.CurrentRunOpen || len(aggregate.PendingDeliveries) != 0 {
				t.Fatalf("reopened preparation=%+v", aggregate)
			}
			// Exercise the real packet constructor after an accepted call, with a later
			// failure rather than a fabricated communicated/report inventory.
			failed := sub.stableDelegateFinish("diagnostic-1fc2", errors.New("fault-d381"))
			var message string
			_ = json.Unmarshal(failed.packet.Message, &message)
			want, _ := json.Marshal(output)
			if failed.outcome != delegatestore.OutcomeFailed || message != "terminal-993f" || string(failed.packet.StructuredResult) != string(want) {
				t.Fatalf("later failure lost accepted report: outcome=%s packet=%+v", failed.outcome, failed.packet)
			}
			if disposition == "later error" {
				// Inject the actual late outcome at the controller boundary while the
				// real runner is held. Its later finish must not publish a second item.
				plans, err := c.FinishGeneration(lease, failed)
				if err != nil {
					t.Fatal(err)
				}
				if err := root.executeDelegateMutationPlans(plans); err != nil {
					t.Fatal(err)
				}
			}
			release()
			waitForTestSignal(t, generationDone, "captured terminal publication generation")
			root.sendersWG.Wait()
			sub.mu.Lock()
			err := sub.err
			sub.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			ids := rootAttentionFold(t, root).order
			if !slices.Equal(ids, []string{delegateAttentionID(sub.sess.owningDelegateID + "/delivery/1")}) {
				t.Fatalf("receiver=%v", ids)
			}
			events = publicationEvents(t, root)
			finished = 0
			for _, e := range events {
				if e.RunFinished != nil {
					finished++
					if e.RunFinished.Generation != 1 {
						t.Fatal("wrong generation")
					}
				}
			}
			if finished != 1 {
				t.Fatalf("finished=%d", finished)
			}
			if !strings.Contains(rootAttentionFold(t, root).content[ids[0]].Text(), "terminal-993f") {
				t.Fatal("accepted payload missing from receiver")
			}
			settled := publicationReopen(t, root)[sub.sess.owningDelegateID]
			if disposition == "later error" && (settled.LatestOutcome.Status != delegatestore.OutcomeFailed || settled.LatestPacket.Kind != delegatestore.PacketTerminalError || !strings.Contains(string(settled.LatestPacket.StructuredResult), "terminal-993f.json")) {
				t.Fatalf("reopened failure=%+v", settled)
			}
			if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			requests := adapter.requestsFor("root")
			if !requestContainsText(requests[len(requests)-1], "terminal-993f.json") {
				t.Fatal("terminal structured output absent from parent request")
			}
			if rootAttentionFold(t, root).resolutions[ids[0]] != delegateAttentionConsumed {
				t.Fatal("parent did not observe terminal item")
			}
			t.Logf("prepared=1, finished=%d, generation=1, receiver=%v", finished, ids)
		})
	}
}

func publicationReopen(t *testing.T, root *Session) delegatestore.State {
	t.Helper()
	raw, err := os.ReadFile(delegateResourceStorePath(root.stateDir, root.ID()))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := delegatestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = delegatestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state, err := delegatestore.Fold(events)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func publicationPacket(message string) delegatestore.TerminalPacket {
	raw, _ := json.Marshal(message)
	return delegatestore.TerminalPacket{Kind: delegatestore.PacketUpdate, Message: raw}
}
