package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// A transport fault belongs to the addressed receiver, not every executor
// sharing its controller. Removing paced retry must also fail this test.
func TestNotificationReplayFailureDoesNotAbortHealthyExecution(t *testing.T) {
	t.Parallel()
	clock := agenttest.NewFakeClock()
	adapter := newMessagingAdapter()
	adapter.script("root", func(llm.Request) llm.Response { return finalResponse("healthy-result-41") })
	var unavailable atomic.Bool
	unavailable.Store(true)
	var coldPath string
	root := newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
		cfg.clock = clock
		cfg.testOnly.delegateAttentionOpenWriter = func(path, sessionID string) (*transcript.Writer, []transcript.Entry, error) {
			if path == coldPath && unavailable.Load() {
				return nil, nil, errInjectedTranscriptWrite
			}
			return transcript.OpenWriterForSession(path, sessionID)
		}
	})
	eventsDone := captureSessionEvents(root)
	t.Cleanup(func() {
		root.Close()
		warnings := warningEvents(<-eventsDone)
		if len(warnings) != 1 || warnings[0].Message != "replay delegate deliveries: injected transcript write failure" {
			t.Errorf("warnings = %+v, want only the injected receiver replay transport failure", warnings)
		}
	})
	c := root.delegateController
	ownerID := "dlg_unavailable_owner"
	owner := stableToolDescriptor(root, ownerID, "")
	owner.ChildSessionID = identifier.MustNewSessionID()
	owner.TranscriptRef = "local:" + owner.ChildSessionID
	coldPath = transcriptPath(root.stateDir, owner.ChildSessionID)
	source := delegateControllerCreatedEvent("dlg_unavailable_source", ownerID)
	source.Created.Descriptor.OwnerSessionID = owner.ChildSessionID
	source.Created.Descriptor.ChildSessionID = identifier.MustNewSessionID()
	source.Created.Descriptor.TranscriptRef = "local:" + source.Created.Descriptor.ChildSessionID
	for _, descriptor := range []delegatestore.Descriptor{owner, source.Created.Descriptor} {
		w, err := transcript.NewWriter(transcriptPath(root.stateDir, descriptor.ChildSessionID), transcript.Header{
			SessionID: descriptor.ChildSessionID, ParentSessionID: descriptor.OwnerSessionID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	c.mu.Lock()
	_, err := c.appendLocked(
		delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: ownerID, Created: &delegatestore.DelegateCreated{Descriptor: owner}},
		source,
	)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// Owner-input admission keeps the receiver cold and unobserved. It must
	// exclude execution restoration without blocking canonical transport.
	reservation, err := c.ReserveStart(rootDelegateActor(root.ID()), ownerID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.AbortStart(reservation); err != nil {
			t.Errorf("release receiver input admission: %v", err)
		}
	})
	seedDelegateControllerDelivery(t, c, source.DelegateID)
	if err := root.replayDelegateDeliveries(); !errors.Is(err, errInjectedTranscriptWrite) {
		t.Fatalf("unavailable receiver transport = %v", err)
	}
	if len(pendingDeliveryIDs(c, source.DelegateID)) != 1 {
		t.Fatal("failed receiver lost its source obligation")
	}
	if _, err := root.ProcessInput(context.Background(), "healthy-input-41", nil); err != nil {
		t.Fatalf("unrelated receiver aborted healthy execution: %v", err)
	}
	if got := len(adapter.requestsFor("root")); got != 1 {
		t.Fatalf("healthy provider requests = %d, want 1", got)
	}
	if got := pendingDeliveryIDs(c, source.DelegateID); len(got) != 1 {
		t.Fatalf("healthy execution changed unavailable receiver's source debt: %v", got)
	}
	unavailable.Store(false)
	clock.Advance(jobNotificationRetryMaxDelay)
	clock.Drain()
	if got := pendingDeliveryIDs(c, source.DelegateID); len(got) != 0 {
		t.Fatalf("source did not recover without another input: %v", got)
	}
	fold, err := readDelegateAttentionFold(coldPath, owner.ChildSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fold.order) != 1 || fold.order[0] != "delegate:dlg_unavailable_source/delivery/1" || len(fold.pendingIDs()) != 1 {
		t.Fatalf("recovered receiver custody = order:%v pending:%v", fold.order, fold.pendingIDs())
	}
	if len(fold.observed) != 0 {
		t.Fatalf("recovered receiver was observed without execution: %v", fold.observed)
	}
}

func TestRenamedResultToolWaitResumes(t *testing.T) {
	t.Parallel()
	for _, cold := range []bool{false, true} {
		name := "resident"
		if cold {
			name = "restored"
		}
		t.Run(name, func(t *testing.T) {
			adapter := newMessagingAdapter()
			adapter.script("root", func(llm.Request) llm.Response {
				return notificationToolCall("renamed-wait-42", "respond", map[string]any{
					"message": "wait-payload-42", "end_turn": true, "end_reason": "waiting_on_work",
				})
			}, func(req llm.Request) llm.Response {
				calls, results := 0, 0
				for _, message := range req.Messages {
					for _, part := range message.Content {
						if call := part.ToolCall; call != nil && call.ID == "renamed-wait-42" && call.Name == "respond" {
							calls++
						}
						if result := part.ToolResult; result != nil && result.ToolCallID == "renamed-wait-42" && result.Name == "respond" && !result.IsError {
							results++
						}
					}
				}
				if calls != 1 || results != 1 {
					t.Errorf("resumed configured-tool pairs = calls:%d results:%d", calls, results)
				}
				return notificationToolCall("renamed-done-42", "respond", map[string]any{"message": "result-payload-42", "end_turn": true})
			})
			root := newMessagingRoot(t, adapter, func(cfg *SessionConfig) { cfg.ResultToolName = "respond" })
			if _, err := root.ProcessInput(context.Background(), "wait-input-42", nil); err != nil {
				t.Fatal(err)
			}
			checkpoint, err := readWaitCheckpoint(root.TranscriptPath(), root.ID())
			if err != nil || checkpoint == nil || checkpoint.CommunicateCallID != "renamed-wait-42" || checkpoint.ResumedByTurnID != "" {
				t.Fatalf("configured-tool wait checkpoint = %+v, %v", checkpoint, err)
			}
			receiver := root
			if cold {
				receiver = restoreWaitingCopy(t, root, copyWaitingState(t, root.stateDir))
			}
			if _, err := receiver.ProcessInput(context.Background(), "resume-input-42", nil); err != nil {
				t.Fatalf("configured-tool wait could not resume: %v", err)
			}
			resumed, err := readWaitCheckpoint(receiver.TranscriptPath(), receiver.ID())
			if err != nil || resumed == nil || resumed.ExecutionTurnID != checkpoint.ExecutionTurnID || resumed.ResumedByTurnID == "" || resumed.ResumedByTurnID == checkpoint.ExecutionTurnID {
				t.Fatalf("configured-tool resumption claim = %+v, %v", resumed, err)
			}
			if got := len(adapter.requestsFor("root")); got != 2 {
				t.Fatalf("configured-tool provider requests = %d, want 2", got)
			}
		})
	}
}
