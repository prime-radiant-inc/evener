package agent

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// Ordinary missing-report errors still clean up owned work, but only attention
// not presented before that generation ended needs a successor generation.
func TestStableDelegateUpdate_OrdinaryEndAttention(t *testing.T) {
	t.Parallel()
	for _, ending := range []struct {
		name     string
		sentinel error
		retries  int
		response llm.Response
	}{
		{"bare_text", errBareTextWithoutResultTool, maxBareTextRetries, llm.Response{Message: llm.Assistant("unreported work")}},
		{"empty_response", errEmptyResponseExhausted, maxEmptyRetries, llm.Response{Message: llm.Assistant("")}},
	} {
		for _, earlier := range []bool{false, true} {
			schedule := "late_unobserved"
			if earlier {
				schedule = "earlier_observed"
			}
			t.Run(ending.name+"/"+schedule, func(t *testing.T) {
				const middleMarker = "ordinary-middle-task"
				const childMarker = "ordinary-grandchild-task"
				const childReport = "grandchild-result-ordinary-attention"
				adapter := newMessagingAdapter(middleMarker, childMarker)
				adapter.onUnexpected = func(message string) { t.Error(message) }
				client := llm.NewClient()
				client.Register(adapter)

				childHeld, middleHeld, providerHeld := make(chan struct{}), make(chan struct{}), make(chan struct{})
				childRelease, middleRelease, providerRelease := make(chan struct{}), make(chan struct{}), make(chan struct{})
				releaseChild := sync.OnceFunc(func() { close(childRelease) })
				releaseMiddle := sync.OnceFunc(func() { close(middleRelease) })
				releaseProvider := sync.OnceFunc(func() { close(providerRelease) })
				var child *subagent // published by childHeld
				returns := newActivationReturns()
				workspace := t.TempDir()
				cfg := SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 2, ForceRealIO: true, testOnly: testConfig{
					skipGitSnapshot: true, minimalSystemPrompt: true,
					sandboxProber: bwrapCapableProber(workspace), disableDelegateIdleRelease: true,
					subagentAfterFinalStatePublish: func(sub *subagent) {
						if sub.sess.depth == 2 {
							child = sub
							close(childHeld)
							awaitSignal(t, childRelease, "grandchild packet release")
						}
					},
					subagentBeforeGenerationReleased: func(sub *subagent, lease delegateLease) {
						if sub.sess.depth == 1 && lease.generation == 1 {
							close(middleHeld)
							awaitSignal(t, middleRelease, "middle generation release")
						}
					},
				}}
				returns.configure(&cfg)
				root := newSession(t, withClient(client), withDir(workspace), withConfig(cfg))
				t.Cleanup(func() {
					releaseProvider()
					releaseChild()
					releaseMiddle()
					returns.close(t, root)
				})
				adapter.rootID = root.ID()
				var recorder sessionEventRecorder
				root.SetDescendantEventFunc(recorder.record)

				adapter.finiteScript(childMarker, func(context.Context, llm.Request) (llm.Response, error) {
					return finalResponse(childReport), nil
				})
				adapter.finiteScript(middleMarker,
					func(context.Context, llm.Request) (llm.Response, error) {
						return delegateToolResponse("spawn-grandchild", childMarker), nil
					},
					func(context.Context, llm.Request) (llm.Response, error) {
						close(providerHeld)
						awaitSignal(t, providerRelease, "middle presenting request")
						return jobListResponse("before-attention"), nil
					},
				)
				if earlier {
					adapter.finiteScript(middleMarker, func(_ context.Context, req llm.Request) (llm.Response, error) {
						if !requestContainsText(req, childReport) {
							t.Error("original generation request omitted the actual grandchild packet")
						}
						return updateResponse("observed-child", "checking the grandchild result"), nil
					})
				}
				// Exhaust the actual input and its one missing-terminal nudge.
				for range 2 * (ending.retries + 1) {
					adapter.finiteScript(middleMarker, func(context.Context, llm.Request) (llm.Response, error) {
						return ending.response, nil
					})
				}
				if !earlier {
					adapter.finiteScript(middleMarker, func(_ context.Context, req llm.Request) (llm.Response, error) {
						if !requestContainsText(req, childReport) {
							t.Error("successor request omitted the actual grandchild packet")
						}
						return finalResponse("grandchild result handled"), nil
					})
				}

				one := 1
				parent := root.createDelegate(t.Context(), delegateArgs{Task: middleMarker, DelegationAllowance: &one})
				if parent.Err != nil {
					t.Fatal(parent.Err)
				}
				middle := root.subagents.get(parent.ChildSessionID)
				waitForTestSignal(t, providerHeld, "middle request built before delivery")
				waitForTestSignal(t, childHeld, "grandchild before packet publication")
				middleReturn := returns.take(t, middle.sess.ID())
				childReturn := returns.take(t, child.sess.ID())
				tree := realDelegateTree{
					s: root, c: root.delegateController, parent: parent, parentRuntime: middle.sess,
					grandchildID: child.sess.owningDelegateID, grandchildSessionID: child.sess.ID(), grandchildRuntime: child.sess,
				}
				if ids := rootAttentionFold(t, middle.sess).pendingIDs(); len(ids) != 0 {
					t.Fatalf("attention before actual delivery = %v", ids)
				}
				if !earlier {
					releaseProvider()
					waitForTestSignal(t, middleHeld, "ordinary middle ending before generation release")
				}
				beforeDelivery := len(adapter.requestsFor(middleMarker))
				requireOrdinaryAttentionStarts(t, root, parent.DelegateID, false)
				releaseChild()
				waitForTestSignal(t, childReturn.returned, "grandchild actual delivery and activation return")
				attentionID := requireOrdinaryChildCustody(t, tree)
				fold := rootAttentionFold(t, middle.sess)
				if !slices.Equal(fold.pendingIDs(), []string{attentionID}) {
					t.Fatalf("actual delivery pending IDs = %v, want [%s]", fold.pendingIDs(), attentionID)
				}
				assertAttentionResponseReceipts(t, middle.sess, attentionID, 0)
				if got := len(adapter.requestsFor(middleMarker)); got != beforeDelivery {
					t.Fatalf("middle requests changed across held delivery: %d -> %d", beforeDelivery, got)
				}
				requireOrdinaryAttentionStarts(t, root, parent.DelegateID, false)
				if earlier {
					releaseProvider()
					waitForTestSignal(t, middleHeld, "observed middle ending before generation release")
					if resolution, generation := childAttentionResolution(t, middle, attentionID); resolution != delegateAttentionConsumed || generation != 0 {
						t.Fatalf("earlier observation resolution = %q generation %d, want consumed generation 0", resolution, generation)
					}
				}
				middle.mu.Lock()
				runErr, status, gated := middle.err, middle.status, middle.fatalRunGated
				middle.mu.Unlock()
				if !errors.Is(runErr, ending.sentinel) || status != SubagentFailed {
					t.Fatalf("ordinary predecessor = status %s error %v, want failed with %v", status, runErr, ending.sentinel)
				}
				predecessorRequests := len(adapter.requestsFor(middleMarker))
				requireOrdinaryAttentionStarts(t, root, parent.DelegateID, false)
				t.Logf("schedule proved: earlier=%t predecessor_generation=%d requests=%d attention=%s fatal_gate=%t", earlier, middleReturn.lease.generation, predecessorRequests, attentionID, gated)
				releaseMiddle()
				waitForTestSignal(t, middleReturn.returned, "middle predecessor activation return")

				// Keep the original 15s attention-owed gate and routing assertions.
				requireReleasedAncestorUpdateRouting(t, tree, &recorder)
				requireOrdinaryAttentionStarts(t, root, parent.DelegateID, !earlier)
				wantRequests := predecessorRequests
				if !earlier {
					wantRequests++
					wantGeneration := middleReturn.lease.generation + 1
					if gated {
						t.Error("ordinary predecessor latched fatal automatic-drive admission")
					}
					successor := returns.take(t, middle.sess.ID())
					if successor.lease.generation != wantGeneration {
						t.Fatalf("successor generation = %d, want %d", successor.lease.generation, wantGeneration)
					}
					waitForTestSignal(t, successor.returned, "attention successor activation return")
				}
				if got := len(adapter.requestsFor(middleMarker)); got != wantRequests {
					t.Fatalf("middle requests = %d, want %d", got, wantRequests)
				}
				// A leased turn consumes what it presented with a generation-less marker.
				if resolution, generation := childAttentionResolution(t, middle, attentionID); resolution != delegateAttentionConsumed || generation != 0 {
					t.Fatalf("resolution = %q generation %d, want consumed generation 0", resolution, generation)
				}
				if ids := rootAttentionFold(t, middle.sess).pendingIDs(); len(ids) != 0 {
					t.Fatalf("settled attention residue = %v", ids)
				}
				requireOrdinaryAttentionReceipts(t, middle.sess, adapter, middleMarker, attentionID)
			})
		}
	}
}

func requireOrdinaryAttentionStarts(t *testing.T, root *Session, delegateID string, successor bool) {
	t.Helper()
	events, err := delegatestore.ReadEvents(delegateResourceStorePath(root.stateDir, root.ID()))
	if err != nil {
		t.Fatal(err)
	}
	var starts []delegatestore.RunStarted
	for _, event := range events {
		if event.Kind == delegatestore.EventDelegateRunStarted && event.DelegateID == delegateID {
			starts = append(starts, *event.RunStarted)
		}
	}
	want := 1
	if successor {
		want++
	}
	if len(starts) != want || starts[0].Generation != 1 || starts[0].Trigger != delegatestore.TriggerInitial {
		t.Fatalf("middle starts = %+v, want initial generation 1 and successor=%t", starts, successor)
	}
	if successor && (starts[1].Generation != 2 || starts[1].Trigger != delegatestore.TriggerAttention) {
		t.Fatalf("successor start = %+v, want attention generation 2", starts[1])
	}
}

func requireOrdinaryChildCustody(t *testing.T, tree realDelegateTree) string {
	t.Helper()
	events, err := delegatestore.ReadEvents(delegateResourceStorePath(tree.s.stateDir, tree.s.ID()))
	if err != nil {
		t.Fatal(err)
	}
	var deliveryID string
	finished, acknowledged := 0, 0
	for _, event := range events {
		if event.DelegateID != tree.grandchildID {
			continue
		}
		switch event.Kind {
		case delegatestore.EventDelegateRunFinished:
			finished++
			deliveryID = event.RunFinished.DeliveryID
		case delegatestore.EventDelegateDeliveryAcknowledged:
			if deliveryID == "" || event.DeliveryAcknowledged.DeliveryID != deliveryID {
				t.Fatalf("acknowledged delivery = %+v, want %s", event.DeliveryAcknowledged, deliveryID)
			}
			acknowledged++
		}
	}
	if finished != 1 || acknowledged != 1 || deliveryID == "" {
		t.Fatalf("grandchild actual terminal delivery = finished:%d acknowledged:%d ID:%q", finished, acknowledged, deliveryID)
	}
	return delegateAttentionID(deliveryID)
}

// Match real provider requests and successful responses to synced assistant
// receipts, and require exactly one custody turn and one consumed resolution.
func requireOrdinaryAttentionReceipts(t *testing.T, middle *Session, adapter *messagingAdapter, key, attentionID string) {
	t.Helper()
	data, err := readTranscriptFull(middle.TranscriptPath(), "")
	if err != nil {
		t.Fatal(err)
	}
	accepted, consumed := 0, 0
	var frame string
	for _, entry := range data.Entries {
		if entry.Turn.AttentionID == attentionID {
			accepted++
			frame = entry.Turn.Message.Text()
		}
		if resolution := entry.Turn.AttentionResolution; resolution != nil && resolution.AttentionID == attentionID && resolution.Disposition == string(delegateAttentionConsumed) {
			consumed++
		}
	}
	if accepted != 1 || consumed != 1 || frame == "" {
		t.Fatalf("attention transitions = accepted:%d consumed:%d frame:%q", accepted, consumed, frame)
	}
	requests := adapter.requestsFor(key)
	adapter.mu.Lock()
	responses := slices.Clone(adapter.answered[key])
	adapter.mu.Unlock()
	if len(requests) != len(responses) {
		t.Fatalf("actual requests/responses = %d/%d", len(requests), len(responses))
	}
	presented := make(map[string]bool)
	for i, request := range requests {
		if requestContainsText(request, frame) {
			if request.SessionID != middle.ID() || responses[i].ID == "" {
				t.Fatalf("presenting request/response identity = %q/%q", request.SessionID, responses[i].ID)
			}
			presented[responses[i].ID] = true
		}
	}
	if len(presented) == 0 {
		t.Fatal("no actual provider request presented the accepted attention frame")
	}
	assertAttentionResponseReceipts(t, middle, attentionID, len(presented))
	for _, entry := range data.Entries {
		if entry.Turn.Kind == schema.TurnAssistant && slices.Contains(entry.Turn.ObservedNotificationIDs, attentionID) {
			if !presented[entry.Turn.ResponseID] {
				t.Fatalf("assistant observation %q has no matched successful presenting request", entry.Turn.ResponseID)
			}
			delete(presented, entry.Turn.ResponseID)
		}
	}
	if len(presented) != 0 {
		t.Fatalf("presented responses without synced assistant observations: %v", presented)
	}
}
