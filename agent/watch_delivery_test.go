package agent

import (
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
)

// installCallerCommunicateWatch arms a caller-send communicate watch on a fresh
// session. Frames carry the Communicate message, so a test tells fires apart by
// the sentinel it sends.
func installCallerCommunicateWatch(t *testing.T) (*Session, *jobManager, watchKey) {
	t.Helper()
	s := newTestSession(t)
	jm := s.jobManager
	installWatchBelowValidation(t, jm, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"communicate"},
		Send:   &watchSendArgs{To: runtimeMessageAliasCaller, Message: "observe"},
	})
	key := watchKey{VisibleSessionID: jm.sessionID, Target: runtimeMessageAliasCaller, SendTo: runtimeMessageAliasCaller}
	return s, jm, key
}

func fireCommunicate(jm *jobManager, msg string) {
	onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: msg})
}

// watchFrameTurns returns the steering turns the model received that mention
// sentinel, in transcript order.
func watchFrameTurns(s *Session, sentinel string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, turn := range s.history {
		if turn.Kind == schema.TurnSteering && strings.Contains(turn.Message.Text(), sentinel) {
			out = append(out, turn.Message.Text())
		}
	}
	return out
}

// A watch has no lifetime delivery cap: it keeps delivering, and stays armed,
// well past the 50 condition fires that used to end it.
func TestWatchSurvivesBeyondFiftyDeliveries(t *testing.T) {
	t.Parallel()
	s, jm, key := installCallerCommunicateWatch(t)
	const fires = 60
	for i := range fires {
		fireCommunicate(jm, fmt.Sprintf("survive-%03d", i))
		drainAndAccept(t, s)
	}
	delivered := 0
	for i := range fires {
		delivered += len(watchFrameTurns(s, fmt.Sprintf("survive-%03d", i)))
	}
	jm.mu.Lock()
	live := jm.watches[key] != nil
	jm.mu.Unlock()
	if delivered != fires || !live {
		t.Fatalf("after %d fires: delivered=%d live=%t, want every fire delivered and the watch still armed", fires, delivered, live)
	}
	if jm.hasPendingWatchSends() {
		t.Fatal("every frame settled, so nothing may remain pending")
	}
}

// A frame that reached the transcript but whose delivered record failed to
// persist is settled on the retry, never rendered to the model again.
func TestWatchSettleIsIdempotentAcrossRetry(t *testing.T) {
	t.Parallel()
	s, jm, _ := installCallerCommunicateWatch(t)
	const sentinel = "settle-retry-sentinel"
	attempts := failAppendN(jm, jobstore.EventWatchSendDelivered, 1)

	fireCommunicate(jm, sentinel)
	drainAndAccept(t, s) // the frame reaches the transcript, then settle fails
	if attempts.Load() != 1 {
		t.Fatalf("settle attempts = %d, want the injected failure to have been hit once", attempts.Load())
	}
	if !jm.hasPendingWatchSends() {
		t.Fatal("a failed settle leaves the frame pending")
	}
	drainAndAccept(t, s) // the rail retries
	drainAndAccept(t, s)

	if got := len(watchFrameTurns(s, sentinel)); got != 1 {
		t.Fatalf("frame delivered to the transcript %d times, want exactly once", got)
	}
	if jm.hasPendingWatchSends() {
		t.Fatal("the retry must settle the frame it already delivered")
	}
}

// A no-send self watch on communicate can feed itself: each notification turn
// that replies through communicate stamps the notification's lineage on that
// event (acceptNotificationInput adopts it), which fires the watch again. The
// runaway guard stops that chain at runawaySelfInfluenceDepth fires and keeps
// the watch armed: a communicate from outside the chain still fires it.
func TestNoSendSelfWatchLoopStopsAtTheRunawayDepth(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var queued []jobNotification
	jm.enqueue = func(n jobNotification) { queued = append(queued, n) }
	installWatchBelowValidation(t, jm, watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}})
	communicate := func(p *provenance.Causal) {
		jm.onSessionEvent(events.SessionEvent{Kind: events.EventCommunicate, SessionID: jm.sessionID, Data: events.CommunicateData{Message: "reply"}, Provenance: p})
	}
	deliveries := func() int {
		jm.mu.Lock()
		defer jm.mu.Unlock()
		for _, cfg := range jm.watches {
			return cfg.deliveries
		}
		t.Fatal("the watch is no longer armed")
		return 0
	}

	communicate(nil) // the user's own message starts the chain
	turns := 0
	for len(queued) > 0 && turns < 4*runawaySelfInfluenceDepth {
		n := queued[0]
		queued = queued[1:]
		communicate(n.Provenance) // the notification turn replies
		turns++
	}
	if len(queued) != 0 {
		t.Fatalf("the self-feeding chain was still running after %d notification turns", turns)
	}
	if got := deliveries(); got != runawaySelfInfluenceDepth {
		t.Fatalf("the chain delivered %d fires, want it stopped at the runaway depth %d", got, runawaySelfInfluenceDepth)
	}

	communicate(nil) // a fresh message outside the chain
	if got := deliveries(); got != runawaySelfInfluenceDepth+1 {
		t.Fatalf("a communicate outside the chain delivered %d more fires, want 1", got-runawaySelfInfluenceDepth)
	}
}

// A caller frame the model already has, whose delivered record keeps failing to
// persist, must not keep the notification rail busy: after a bounded number of
// attempts it leaves the pending set (so no boundary re-enqueues it), and the
// model still saw it exactly once.
func TestTranscribedFrameWithFailingSettleStopsRetrying(t *testing.T) {
	t.Parallel()
	s, jm, _ := installCallerCommunicateWatch(t)
	const sentinel = "settle-always-fails"
	attempts := failAppendN(jm, jobstore.EventWatchSendDelivered, 1000)

	fireCommunicate(jm, sentinel)
	const passes = 20
	for range passes {
		drainAndAccept(t, s)
	}
	if got := attempts.Load(); got >= passes {
		t.Fatalf("delivered-record attempts = %d over %d passes, want a bounded number", got, passes)
	}
	if jm.hasPendingWatchSends() {
		t.Fatal("the transcribed frame is still pending, so every boundary re-enqueues it")
	}
	if got := len(watchFrameTurns(s, sentinel)); got != 1 {
		t.Fatalf("frame reached the model %d times, want exactly once", got)
	}
}
