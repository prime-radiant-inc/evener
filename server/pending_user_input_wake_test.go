package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

// TestSubmitPendingUserInput_DeliveredOnceTheSlotFrees pins that a queued-input
// wake is guaranteed, not best-effort.
//
// The queued-input wake is the ONLY way ProcessPendingUserInput is ever reached:
// cmd/serf/serve.go:1033 runs it in response to a QueuedInput message and
// nothing else calls it. Unlike a durable start — which processNextServeInput
// probes for unconditionally at the top of every loop iteration — there is no
// second path to the work. A dropped wake therefore strands a message the client
// has already been told was accepted.
func TestSubmitPendingUserInput_DeliveredOnceTheSlotFrees(t *testing.T) {
	srv := NewServer(ServerConfig{})

	// Occupy the single slot with unrelated input.
	srv.SubmitContinuation("keep the slot busy")
	srv.SubmitPendingUserInput("session-under-full-slot")

	occupant := <-srv.InputCh()
	if occupant.Kind != agent.EntryContinuation {
		t.Fatalf("first message Kind = %v, want EntryContinuation (the occupant)", occupant.Kind)
	}

	select {
	case msg := <-srv.InputCh():
		if !msg.QueuedInput {
			t.Fatalf("message = %#v, want QueuedInput", msg)
		}
		if msg.SessionID != "session-under-full-slot" {
			t.Fatalf("SessionID = %q, want session-under-full-slot", msg.SessionID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the dropped queued-input wake was never redelivered: a durably accepted message runs never, because nothing polls ProcessPendingUserInput")
	}
}

// TestSubmitPendingUserInput_RepeatedDropsWakeOnce pins that the re-arm
// coalesces, for the same reason SubmitNotification's does.
//
// One wake settles any number of dropped ones: the wake runs the queue head as
// its own turn, and that turn's drain tail runs whatever else accumulated —
// selectDrainNextAction returns runQueued at every tail, and its awaiting branch
// keeps the queued-input rung live while holding every other rung. Re-arming per
// drop would push a burst of turns onto a session that only needed one.
func TestSubmitPendingUserInput_RepeatedDropsWakeOnce(t *testing.T) {
	srv := NewServer(ServerConfig{})

	srv.SubmitContinuation("keep the slot busy")
	for range 5 {
		srv.SubmitPendingUserInput("session-under-full-slot")
	}

	if occupant := <-srv.InputCh(); occupant.Kind != agent.EntryContinuation {
		t.Fatalf("first message Kind = %v, want EntryContinuation (the occupant)", occupant.Kind)
	}
	select {
	case msg := <-srv.InputCh():
		if !msg.QueuedInput {
			t.Fatalf("message = %#v, want QueuedInput", msg)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the dropped queued-input wakes were never redelivered")
	}

	// Negative assertion: nothing to poll for, so this waits a fixed moment and
	// asserts the absence of a second wake.
	time.Sleep(250 * time.Millisecond)
	select {
	case extra := <-srv.InputCh():
		t.Fatalf("a second wake arrived (%#v): repeated drops must coalesce into one re-arm", extra)
	default:
	}
}

// TestSubmitPendingUserInput_DoesNotBlockItsCaller pins the constraint that
// forces the re-arm onto a parked goroutine rather than onto the caller.
//
// The wake is called synchronously from AcceptClientMutationQueue, which runs on
// the AppWire request handler goroutine. A blocking send there would stall the
// client's turn/queue response behind the serve loop.
func TestSubmitPendingUserInput_DoesNotBlockItsCaller(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SubmitContinuation("keep the slot busy")

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		srv.SubmitPendingUserInput("session-under-full-slot")
	}()
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("SubmitPendingUserInput blocked on a full channel; it runs on the AppWire request handler goroutine and must not")
	}
}

// TestQueuedInputRunsAfterItsWakeIsDropped is the kata's acceptance case, driven
// through the real seams rather than through a stand-in for the serve loop.
//
// A message the client queued is durably accepted while the one input slot is
// occupied, so its wake is dropped. The assertions then follow the message the
// rest of the way: the re-armed wake lands, it names this session, and the work
// it names is really there and really runs.
//
// The wake wiring below is the exact callback cmd/serf/serve.go:690-692
// installs, so this test exercises the production seam and not a paraphrase of
// it. There is deliberately no consumer loop here: a hand-written reader that
// branched on message kind would be a second implementation of the serve loop,
// free to drift from the real one while staying green.
func TestQueuedInputRunsAfterItsWakeIsDropped(t *testing.T) {
	const queuedText = "run me even though my wake was dropped"

	adapter := &queuedInputRecordingAdapter{}
	stateDir := t.TempDir()
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := agent.NewSession(
		client,
		provider.NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(stateDir),
		agent.SessionConfig{StateDir: stateDir},
	)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	srv := NewServer(ServerConfig{})
	sess.SetPendingUserInputWakeFunc(func() { srv.SubmitPendingUserInput(sess.ID()) })

	// Occupy the one slot, exactly as a job-completion notification does while a
	// question is pending. That occupant is what makes the wake below drop.
	srv.SubmitContinuation("keep the slot busy")

	if _, err := sess.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "cm-queued-under-full-slot",
		Input:            []appwire.InputItem{{Type: "text", Text: queuedText}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationQueue: %v", err)
	}

	// The serve loop consumes the occupant, freeing the slot.
	if occupant := <-srv.InputCh(); occupant.Kind != agent.EntryContinuation {
		t.Fatalf("first message Kind = %v, want EntryContinuation (the occupant)", occupant.Kind)
	}

	select {
	case msg := <-srv.InputCh():
		if !msg.QueuedInput {
			t.Fatalf("message = %#v, want QueuedInput", msg)
		}
		if msg.SessionID != sess.ID() {
			t.Fatalf("SessionID = %q, want this session %q", msg.SessionID, sess.ID())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the queued message was durably accepted and its wake dropped; nothing ever came back for it, so %q runs never", queuedText)
	}

	_, ran, err := sess.ProcessPendingUserInput(context.Background(), nil)
	if err != nil {
		t.Fatalf("ProcessPendingUserInput: %v", err)
	}
	if !ran {
		t.Fatal("the redelivered wake named no runnable work; it arrived, but too late or for the wrong session")
	}
	if !adapter.sawText(queuedText) {
		t.Fatalf("the turn ran but the model never saw %q; the wake landed and the payload did not", queuedText)
	}
}

// queuedInputRecordingAdapter ends every turn immediately and records the
// request text it was given, so a test can assert the queued payload actually
// reached the model rather than trusting that a turn ran.
type queuedInputRecordingAdapter struct {
	mu   sync.Mutex
	seen []string
}

func (a *queuedInputRecordingAdapter) Name() string { return "openai" }

func (a *queuedInputRecordingAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	var text strings.Builder
	for _, msg := range req.Messages {
		for _, part := range msg.Content {
			text.WriteString(part.Text)
			text.WriteString("\n")
		}
	}
	a.mu.Lock()
	a.seen = append(a.seen, text.String())
	a.mu.Unlock()

	if !requestHasTool(req, "communicate") {
		return llm.Response{
			Provider: a.Name(),
			Model:    req.Model,
			Message:  llm.Assistant(`{"name":"queued input wake"}`),
		}, nil
	}
	args, _ := json.Marshal(map[string]any{
		"message":  "done",
		"end_turn": true,
		"output": map[string]any{
			"message":   "",
			"data":      map[string]any{},
			"artifacts": []string{},
		},
	})
	return llm.Response{
		Provider: a.Name(),
		Model:    req.Model,
		Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{{
				Kind: llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{
					ID:        "communicate-queued-input",
					Name:      "communicate",
					Arguments: args,
					Type:      "function",
				},
			}},
		},
	}, nil
}

func (a *queuedInputRecordingAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *queuedInputRecordingAdapter) sawText(want string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, seen := range a.seen {
		if strings.Contains(seen, want) {
			return true
		}
	}
	return false
}
