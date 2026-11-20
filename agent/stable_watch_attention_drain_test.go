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
	"primeradiant.com/evener/llm"
)

// A stable delegate's watch notice is durable attention, so it never reaches the
// notification queue and peekNotifications cannot see it. The successor
// attention drive refuses a delegate that is already finalizing, and the
// parent's drive skips a child whose only undelivered work is watch attention.
// So while the delegate is parked in its finalization drain, the drain's own
// turn gate is the only thing that can present the notice -- without it the
// model waits for the owned shell to exit before receiving the reminder that
// lets it decide whether to stop the shell.
//
// The assertion is made while the shell is still held, so a turn carrying the
// notice can only come from the drain's own gate. The fold check proves the
// notice was recorded as durable attention whatever the drain has since done
// with it: the gate may already have woken and consumed it by the time the
// bubble settles.
func TestDrainRunsANotificationTurnForAPendingStableWatchNotice(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		fixture := newOwnedJobDrainFixture(t)
		child := fixture.child.sess

		notice := watchSendDiagnosticNotification("watch:drain-notice", fixture.shellJobID, "watch send failed: delivery_id=drain-notice")
		child.jobManager.enqueueWatchNotifications([]jobNotification{notice})
		if queued := child.peekNotifications(); queued != 0 {
			t.Fatalf("the watch notice left %d item(s) on the ordinary queue, want 0: the ingress did not take the durable rail", queued)
		}
		fold, err := readDelegateAttentionFold(child.TranscriptPath(), child.id)
		if err != nil {
			t.Fatalf("read child attention fold: %v", err)
		}
		if !slices.Contains(fold.order, stableWatchNoticeAttentionID(notice)) {
			t.Fatal("the enqueued notice was not recorded as durable attention")
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

// A leased turn is admitted for a watch notice already resolved into the
// session's transcript: the notice is in the history the request projects, and
// the turn consumes what it presents. Without an admission rule for durable
// watch attention the leased turn stands down and the row is presented by
// nobody, because only a leased generation may run it.
func TestStableWatchAttentionIsAdmittedForALeasedTurn(t *testing.T) {
	t.Parallel()
	child, _, _ := stableWatchSendChild(t)
	// newTestJM discards notifications; the production ingress is the session's
	// own enqueue, which is what routes a stable child's watch notice onto the
	// durable rail.
	child.jobManager.enqueue = child.enqueueJobNotificationAndNotify

	notice := watchSendDiagnosticNotification("watch:accept-notice", "job:accept-notice", "watch send failed: delivery_id=accept-notice")
	child.jobManager.enqueueWatchNotifications([]jobNotification{notice})
	if queued := child.peekNotifications(); queued != 0 {
		t.Fatalf("the watch notice left %d item(s) on the ordinary queue, want 0: the ingress did not take the durable rail", queued)
	}
	if !child.hasPendingStableWatchAttention() {
		t.Fatal("the enqueued notice does not read as pending watch attention")
	}

	if child.acceptNotificationInput(context.Background(), "unleased-turn") {
		t.Fatal("an unleased notification drive was admitted for a watch notice it cannot consume")
	}
	leased := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, delegateLease{delegateID: child.owningDelegateID, generation: 1})
	if !child.acceptNotificationInput(leased, "leased-turn") {
		t.Fatal("the delegate's own leased turn was not admitted for its watch notice")
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
// it already declined to wait for, and presenting it after the answer would
// reopen a paid model turn on a run nothing is waiting on. The notice is still
// recorded durably; it is resolved as discarded rather than consumed.
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

		fold, err := readDelegateAttentionFold(child.TranscriptPath(), child.id)
		if err != nil {
			t.Fatalf("read child attention fold: %v", err)
		}
		var noticeID string
		for _, id := range fold.order {
			if isStableWatchAttentionID(id) {
				noticeID = id
			}
		}
		if noticeID == "" {
			t.Fatal("the self watch's notice was not recorded as durable attention")
		}
		if got, resolved := fold.resolutions[noticeID]; !resolved || got != delegateAttentionDiscarded {
			t.Fatalf("pre-entry watch notice disposition = %q (resolved %v), want discarded", got, resolved)
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

// A caller watch send states an obligation the model still owes an answer, so
// the terminal cutoff keeps its durable row pending and the drain's turn gate
// still owes its presentation. Only notices that carry news are discarded.
func TestTerminalStableWatchCutoffKeepsWatchSendObligation(t *testing.T) {
	t.Parallel()
	child, jm, _ := stableWatchSendChild(t)

	cfg, key, deliveryID := installCallerSendWatchWithCurrentFrame(t, jm, "caller frame")
	jm.mu.Lock()
	state := *cfg.pending[key]
	jm.mu.Unlock()
	child.enqueueJobNotificationAndNotify(watchSendTokenNotification("", state))

	declinable := watchSendDiagnosticNotification("watch:declinable", "job_declinable", "declinable notice")
	child.enqueueJobNotificationAndNotify(declinable)
	declinableID := stableWatchNoticeAttentionID(declinable)

	fold, err := readDelegateAttentionFold(child.TranscriptPath(), child.id)
	if err != nil {
		t.Fatalf("read child attention fold before the cutoff: %v", err)
	}
	if !slices.Contains(fold.order, declinableID) {
		t.Fatal("the notice was not recorded as durable attention")
	}

	child.discardTerminalStableWatchPending()

	pending := child.pendingStableWatchAttentionIDs()
	if len(pending) != 1 {
		t.Fatalf("pending watch attention after the cutoff = %v, want only the caller send's row", pending)
	}
	if !strings.HasSuffix(pending[0], "send:"+deliveryID) {
		t.Fatalf("surviving pending row = %q, want the caller send's delivery identity %q", pending[0], "send:"+deliveryID)
	}
	fold, err = readDelegateAttentionFold(child.TranscriptPath(), child.id)
	if err != nil {
		t.Fatalf("read child attention fold after the cutoff: %v", err)
	}
	if got := fold.resolutions[declinableID]; got != delegateAttentionDiscarded {
		t.Fatalf("declinable notice disposition = %q, want discarded", got)
	}
	if !child.hasUndrivenStableWatchAttention(nil) {
		t.Fatal("the drain's turn gate no longer owes the caller send's presentation")
	}
	leased := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, delegateLease{delegateID: child.owningDelegateID, generation: 1})
	if !child.acceptNotificationInput(leased, "leased-turn") {
		t.Fatal("no leased turn is admitted for the caller send the cutoff kept")
	}
}
