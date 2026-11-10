package agent

import (
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
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
