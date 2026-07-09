package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/provenance"
)

// These tests pin the scoping rule for concrete-job-targeted session-event
// watches and the inform+breaker policy on watcher-notification deliveries
// (PRI-2525). Non-lifecycle event payloads carry no job identity, and the only
// session whose events reach a jobManager's evaluator today is the watcher's
// own — so before identity scoping, a job-targeted event watch could only echo
// the watcher's own tool calls back at it labeled as job activity, and the
// no-send notification path delivered those echoes without self-influence
// classification.

// A job-targeted watch must not match the watcher's own tool events: the
// envelope's originating session is the watcher, not the watched job.
func TestJobTargetWatchDoesNotMatchWatcherOwnToolEvents(t *testing.T) {
	snap := watchEventSnapshot{
		target:          "job_watched",
		targetActive:    true,
		targetSessionID: "sess_child",
		eventKinds:      map[events.EventKind]bool{events.EventToolCallEnd: true},
		eventFilter:     &watchEventFilter{ToolName: "communicate", Status: "ok"},
		watchID:         "watch_scope_1",
		generation:      "g1",
	}
	ev := events.SessionEvent{
		Kind:      events.EventToolCallEnd,
		SessionID: "sess_watcher",
		Data:      events.ToolCallEndData{ToolName: "communicate"},
	}
	if dec := evaluateWatchEvent(snap, ev); dec.matched {
		t.Fatalf("job-targeted watch matched a watcher-origin communicate TOOL_CALL_END: %+v", dec)
	}
	// An event that cannot prove its origin does not fire either.
	ev.SessionID = ""
	if dec := evaluateWatchEvent(snap, ev); dec.matched {
		t.Fatalf("job-targeted watch matched an identity-less tool event: %+v", dec)
	}
}

// The spec's promised semantics: an event originating from the watched job's
// child session matches its job-targeted watch.
func TestJobTargetWatchMatchesWatchedJobSessionEvents(t *testing.T) {
	snap := watchEventSnapshot{
		target:          "job_watched",
		targetActive:    true,
		targetSessionID: "sess_child",
		eventKinds:      map[events.EventKind]bool{events.EventToolCallEnd: true},
		eventFilter:     &watchEventFilter{ToolName: "communicate", Status: "ok"},
		watchID:         "watch_scope_2",
		generation:      "g1",
	}
	ev := events.SessionEvent{
		Kind:      events.EventToolCallEnd,
		SessionID: "sess_child",
		Data:      events.ToolCallEndData{ToolName: "communicate"},
	}
	if dec := evaluateWatchEvent(snap, ev); !dec.matched {
		t.Fatalf("job-targeted watch must match the watched job's own session events: %+v", dec)
	}
}

// Job lifecycle events carry a JobID and stay matchable on a job target
// regardless of envelope identity.
func TestJobTargetWatchStillMatchesJobLifecycle(t *testing.T) {
	snap := watchEventSnapshot{
		target:       "job_watched",
		targetActive: true,
		eventKinds:   map[events.EventKind]bool{events.EventJobFinished: true},
		watchID:      "watch_scope_3",
		generation:   "g1",
	}
	ev := events.SessionEvent{
		Kind:      events.EventJobFinished,
		SessionID: "sess_watcher",
		Data:      events.JobFinishedData{JobID: "job_watched"},
	}
	if dec := evaluateWatchEvent(snap, ev); !dec.matched {
		t.Fatalf("job-targeted watch must still match its own job's lifecycle event: %+v", dec)
	}
}

// Session-target watches (self/parent observers) keep tool-event semantics:
// firing on the session's own events is their intended meaning.
func TestSessionTargetWatchStillMatchesToolEvents(t *testing.T) {
	snap := watchEventSnapshot{
		target:       runtimeMessageAliasCaller,
		targetActive: true,
		eventKinds:   map[events.EventKind]bool{events.EventToolCallEnd: true},
		eventFilter:  &watchEventFilter{ToolName: "read_file", Status: "ok"},
		watchID:      "watch_scope_4",
		generation:   "g1",
	}
	ev := events.SessionEvent{
		Kind:      events.EventToolCallEnd,
		SessionID: "sess_watcher",
		Data:      events.ToolCallEndData{ToolName: "read_file"},
	}
	if dec := evaluateWatchEvent(snap, ev); !dec.matched {
		t.Fatalf("session-target watch must keep matching its own tool events: %+v", dec)
	}
}

// Install stays permissive (loop-guard posture): event watches on concrete job
// targets are accepted; identity scoping happens at match time, not create time.
func TestJobTargetEventWatchStillInstallsAtCreate(t *testing.T) {
	err := validateWatchEventArgs(watchArgs{
		Target:      "job_watched",
		Events:      []string{"assistant.tool"},
		EventFilter: &watchEventFilter{ToolName: "communicate", Status: "ok"},
	})
	if err != nil {
		t.Fatalf("expected install-permissive accept, got: %v", err)
	}
}

// A self-influenced watcher-notification frame carries the disengage notice —
// the same inform+breaker policy the send path applies. Without classification
// on the no-send path, the only shape job_watch's public arguments can express
// delivered unclassified echoes.
func TestNotificationPathCarriesSelfInfluenceNotice(t *testing.T) {
	jm := newTestJM(t)
	var got []jobNotification
	jm.enqueue = func(n jobNotification) { got = append(got, n) }

	installWatchBelowValidation(t, jm, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"assistant.tool"},
	})
	watchID, generation := singleWatchIdentity(t, jm)

	jm.onSessionEvent(events.SessionEvent{
		Kind:       events.EventToolCallEnd,
		SessionID:  jm.sessionID,
		Data:       events.ToolCallEndData{ToolName: "read_file"},
		Provenance: provenance.WithWatch(nil, watchID, generation, "wd_prior", jm.sessionID, ""),
	})

	if len(got) != 1 {
		t.Fatalf("want one watcher notification, got %d", len(got))
	}
	if got[0].Notice == "" {
		t.Fatal("self-influenced notification frame must carry the disengage notice")
	}
	if !strings.Contains(formatJobNotificationBlock(got[0], notificationExcerpt{}), got[0].Notice) {
		t.Fatal("rendered notification block must include the notice")
	}
	// A frame with no self-influence carries no notice.
	got = nil
	jm.onSessionEvent(events.SessionEvent{
		Kind:      events.EventToolCallEnd,
		SessionID: jm.sessionID,
		Data:      events.ToolCallEndData{ToolName: "read_file"},
	})
	if len(got) != 1 {
		t.Fatalf("want one watcher notification, got %d", len(got))
	}
	if got[0].Notice != "" {
		t.Fatalf("un-influenced frame must carry no notice, got %q", got[0].Notice)
	}
}

// Once the delivered-prior chain of a watch is runaway-deep, the notification
// path drops the delivery and auto-clears the watch — mirroring the send
// path's fuse in recordWatchSend.
func TestNotificationPathRunawayFuseClearsWatch(t *testing.T) {
	jm := newTestJM(t)
	var got []jobNotification
	jm.enqueue = func(n jobNotification) { got = append(got, n) }

	installWatchBelowValidation(t, jm, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"assistant.tool"},
	})
	watchID, generation := singleWatchIdentity(t, jm)

	var chain *provenance.Causal
	jm.mu.Lock()
	for i := 0; i < runawaySelfInfluenceDepth; i++ {
		deliveryID := "wd_runaway_" + strings.Repeat("x", i+1)
		chain = provenance.WithWatch(chain, watchID, generation, deliveryID, jm.sessionID, "")
		jm.deliveredWatchSendIDs[deliveryID] = struct{}{}
	}
	jm.mu.Unlock()

	jm.onSessionEvent(events.SessionEvent{
		Kind:       events.EventToolCallEnd,
		SessionID:  jm.sessionID,
		Data:       events.ToolCallEndData{ToolName: "read_file"},
		Provenance: chain,
	})

	var clearNotice bool
	for _, n := range got {
		if strings.HasPrefix(n.Reason, "event: ") {
			t.Fatalf("runaway-deep echo must not deliver a watch frame: %+v", n)
		}
		if strings.Contains(n.Reason, "runaway self-influence") {
			clearNotice = true
		}
	}
	if !clearNotice {
		t.Fatal("runaway clear must tell the model why the watch ended")
	}
	if jm.watchCount() != 0 {
		t.Fatalf("runaway watch must auto-clear; watchCount = %d", jm.watchCount())
	}
}

// singleWatchIdentity returns the (watchID, generation) of the jobManager's
// only installed watch.
func singleWatchIdentity(t *testing.T, jm *jobManager) (string, string) {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if len(jm.watches) != 1 {
		t.Fatalf("want exactly one watch installed, got %d", len(jm.watches))
	}
	for _, cfg := range jm.watches {
		return cfg.watchID, cfg.generation
	}
	return "", ""
}
