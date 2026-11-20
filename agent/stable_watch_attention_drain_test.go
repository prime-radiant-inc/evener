package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// A watch notice waits on the child's ordinary notification queue until a
// leased handoff reserves the generation that presents it. While the delegate is
// parked in its finalization drain the successor attention drive refuses it, so
// the drain's own turn gate is what presents the notice: the drain runs under
// the run's lease, so its notification turn may carry the notice as ordinary
// steering. Without that gate the model waits for the owned shell to exit before
// receiving the reminder that lets it decide whether to stop the shell.
//
// The assertion is made while the shell is still held, so a turn carrying the
// notice can only come from the drain's own gate.
func TestDrainRunsANotificationTurnForAPendingStableWatchNotice(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		fixture := newOwnedJobDrainFixture(t)
		child := fixture.child.sess

		notice := watchSendDiagnosticNotification("watch:drain-notice", fixture.shellJobID, "watch send failed: delivery_id=drain-notice")
		child.jobManager.enqueueWatchNotifications([]jobNotification{notice})
		if queued := child.peekNotifications(); queued != 1 {
			t.Fatalf("the watch notice left %d item(s) on the ordinary queue, want 1: a notice waits there for the leased handoff", queued)
		}
		fold, err := readDelegateAttentionFold(child.TranscriptPath(), child.id)
		if err != nil {
			t.Fatalf("read child attention fold: %v", err)
		}
		if slices.Contains(fold.order, stableWatchNoticeAttentionID(notice)) {
			t.Fatal("a queued notice already wrote a durable row: it becomes durable only when a reservation presents it")
		}

		// Let the bubble settle: with the shell still held, everything that can
		// run without a new wake has run.
		synctest.Wait()
		requests := fixture.adapter.Requests()
		if !requestsContain(requests, "delivery_id=drain-notice") {
			t.Fatalf("no provider request carried the watch notice while the owned shell was still running (%d request(s)): the drain left the reminder for the shell's exit", len(requests))
		}
		if !requestsContain(requests, "child shell complete") {
			// The shell is still held, so its completion cannot have arrived:
			// reaching here with the notice means the drain ran its own turn.
			t.Log("the notice was presented before the shell completed")
		}

		fixture.env.releaseJob()
		select {
		case <-fixture.runDone:
		case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
			t.Fatal("stable delegate did not finish after its owned shell exited")
		}
	})
}

// A leased turn carries a watch notice: the notice waits on the session's
// ordinary queue, and a turn under a committed generation is allowed to take it
// as ordinary notification steering, because that turn consumes what it
// presents. An unleased turn on the same session is refused instead, so the
// notice reaches the provider exactly once and only inside a generation.
func TestStableWatchAttentionIsAdmittedForALeasedTurn(t *testing.T) {
	t.Parallel()
	child, _, _ := stableWatchSendChild(t)
	// newTestJM discards notifications; the production ingress is the session's
	// own enqueue, which is the rail a watch notice arrives on.
	child.jobManager.enqueue = child.enqueueJobNotificationAndNotify

	notice := watchSendDiagnosticNotification("watch:accept-notice", "job:accept-notice", "watch send failed: delivery_id=accept-notice")
	child.jobManager.enqueueWatchNotifications([]jobNotification{notice})
	if queued := child.peekNotifications(); queued != 1 {
		t.Fatalf("the watch notice left %d item(s) on the ordinary queue, want 1: a notice waits there for the leased handoff", queued)
	}
	if child.hasPendingStableWatchAttention() {
		t.Fatal("a queued notice already reads as durable watch attention: it becomes durable only when a reservation presents it")
	}

	// The unleased attempt hands the notice back untouched: nothing it could
	// carry may present the notice, and consuming it here would lose the tick.
	if child.acceptNotificationInput(context.Background(), "unleased-turn") {
		t.Fatal("an unleased notification drive was admitted for a watch notice it cannot present")
	}
	if queued := child.peekNotifications(); queued != 1 {
		t.Fatalf("the unleased drive left %d item(s) queued, want 1: the notice must stay for the leased drive", queued)
	}
	leased := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, delegateLease{delegateID: child.owningDelegateID, generation: 1})
	if !child.acceptNotificationInput(leased, "leased-turn") {
		t.Fatal("the delegate's own leased turn was not admitted for its watch notice")
	}
	if queued := child.peekNotifications(); queued != 0 {
		t.Fatalf("the leased turn left %d item(s) queued, want 0: the turn that carries the notice consumes it", queued)
	}
	if child.hasAwaitingWatchNotification() {
		t.Fatal("the leased turn left the notice assigned to a request that will never carry it")
	}
	child.mu.Lock()
	history := append([]schema.Turn{}, child.history...)
	child.mu.Unlock()
	presented := 0
	for _, turn := range history {
		if turn.Kind == schema.TurnSteering && strings.Contains(turn.Message.Text(), "delivery_id=accept-notice") {
			presented++
		}
	}
	if presented != 1 {
		t.Fatalf("the notice appears in %d steering turn(s), want 1: the leased turn's reminder is what reaches the provider", presented)
	}
}

// oneShotTerminalWatchFixture is a one-shot stable delegate that arms a no-send
// self watch on communicate, then ends its turn with a terminal communicate
// while its own background shell is still running.
type oneShotTerminalWatchFixture struct {
	child      *subagent
	parent     *Session
	delegateID string
	adapter    *fakeAdapter
	env        *heldJobEnvironment
	shellJobID string
	runDone    <-chan struct{}
}

func newOneShotTerminalWatchFixture(t *testing.T) *oneShotTerminalWatchFixture {
	t.Helper()

	idleReported := make(chan struct{})
	drainClock := newOwnedJobDrainClock()
	adapter := newFakeAdapter("openai", func(llm.Request) llm.Response {
		return agenttest.ToolCallResponse(
			llm.ToolCallData{
				ID:        "owned-shell",
				Name:      "shell",
				Arguments: json.RawMessage(`{"command":"owned shell","mode":"background"}`),
				Type:      "function",
			},
			llm.ToolCallData{
				ID:        "terminal-self-watch",
				Name:      "job_watch",
				Arguments: json.RawMessage(`{"operation":"create","source":"self","events":["communicate"]}`),
				Type:      "function",
			},
		)
	}, func(llm.Request) llm.Response {
		close(idleReported)
		return agenttest.FinalResponse("waiting for owned shell")
	}, func(req llm.Request) llm.Response {
		// Drain turns answer whatever the drain presented without claiming a
		// new report: bare text leaves the accepted one standing.
		_ = req
		return llm.Response{Message: llm.Assistant("acknowledged")}
	})
	client := llm.NewClient()
	client.Register(adapter)
	env := newHeldJobEnvironment(t.TempDir())
	parent, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), env, SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		TurnEndsProcess:  true,
		clock:            drainClock,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { parent.Close() })
	t.Cleanup(env.releaseJob)

	result := parent.createDelegate(context.Background(), delegateArgs{
		Task: "run an owned background shell",
	})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	_, childID, err := decodeRef(result.TranscriptRef)
	if err != nil {
		t.Fatalf("decodeRef(%q): %v", result.TranscriptRef, err)
	}
	child := parent.subagents.get(childID)
	if child == nil {
		t.Fatalf("subagent %s not found", childID)
	}

	select {
	case <-env.started:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("child shell did not start")
	}
	shellJobID := liveOwnedShellJobID(t, child.sess.jobManager)
	// A live unfired watch on the held shell excuses it from the one-shot
	// undisposed-background-job announcement ladder, which otherwise runs turns
	// of its own for a held background shell. With that ladder out of the
	// picture, any drain turn taken while the shell is held is a turn for
	// durable watch attention.
	if _, err := child.sess.jobManager.configureWatch(watchArgs{
		Target:      shellJobID,
		OutputMatch: "output that never appears",
	}); err != nil {
		t.Fatalf("arm an output_match watch on the held shell: %v", err)
	}
	select {
	case <-idleReported:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("child did not report its interim result")
	}
	select {
	case <-drainClock.drainEntered:
	case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
		t.Fatal("subagent did not enter its owned job-tree drain")
	}

	child.mu.Lock()
	runDone := child.done
	child.mu.Unlock()

	return &oneShotTerminalWatchFixture{
		child:      child,
		parent:     parent,
		delegateID: result.DelegateID,
		adapter:    adapter,
		env:        env,
		shellJobID: shellJobID,
		runDone:    runDone,
	}
}

// A one-shot delegate that ends its turn with a terminal communicate has
// declared the run over. A watch notice that arrived before that answer is news
// it already declined to wait for: the drain's entry cutoff drops it from the
// queue, and presenting it after the answer would reopen a paid model turn on a
// run nothing is waiting on.
//
// A notice that arrives after the drain has entered is untouched by the cutoff
// and still reaches the model, and the accepted report survives the drain turns
// that follow.
func TestOneShotTerminalCommunicateSelfWatchDoesNotReopenTurn(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		fixture := newOneShotTerminalWatchFixture(t)
		child := fixture.child.sess

		synctest.Wait()
		requests := fixture.adapter.Requests()
		if len(requests) != 2 {
			extra := ""
			if len(requests) > 2 {
				extra = requestText(requests[2])
			}
			t.Fatalf("provider requests while the owned shell is still held = %d, want the initial and pre-drain turns only: the terminal communicate reopened a turn whose request read %q", len(requests), extra)
		}

		// The cutoff dropped the pre-answer notice from the queue and wrote no
		// row for it: the run it would have woken already declared itself over.
		if queued := child.peekNotifications(); queued != 0 {
			t.Fatalf("%d watch notification(s) survived the terminal cutoff, want 0", queued)
		}
		fold, err := readDelegateAttentionFold(child.TranscriptPath(), child.id)
		if err != nil {
			t.Fatalf("read child attention fold: %v", err)
		}
		for _, id := range fold.order {
			if isStableWatchAttentionID(id) {
				t.Fatalf("the terminal cutoff left durable watch row %q for news the model declined to wait for", id)
			}
		}

		// The cutoff runs once at drain entry, so a notice delivered to the drain
		// afterwards is still presented.
		child.jobManager.enqueueWatchNotifications([]jobNotification{
			watchSendDiagnosticNotification("watch:post-entry", "job_post_entry", "post-entry watch notice"),
		})
		synctest.Wait()
		requests = fixture.adapter.Requests()
		if !requestsContain(requests[2:], "post-entry watch notice") {
			t.Fatalf("no provider request carried the watch notice delivered during the drain (%d request(s))", len(requests))
		}

		fixture.env.releaseJob()
		select {
		case <-fixture.runDone:
		case <-time.After(30 * time.Second): // TRIPWIRE: real signal from a background goroutine/job; 30s only fires on a genuine hang.
			t.Fatal("stable delegate did not finish after its owned shell exited")
		}
		// The drained run's report is what the delegate settled with. A drain
		// turn that ends in bare text leaves it standing, so the canonical
		// packet still carries the model's own answer rather than a
		// housekeeping acknowledgement.
		packet := loadDelegateTerminalPacket(t, fixture.parent, fixture.delegateID)
		var message string
		if err := json.Unmarshal(packet.Message, &message); err != nil {
			t.Fatalf("decode canonical terminal message: %v", err)
		}
		if strings.TrimSpace(message) != "waiting for owned shell" {
			t.Fatalf("canonical terminal message = %q, want the accepted report preserved", message)
		}
	})
}
