package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// TestConditionWatchNote_AcceptedAndSummarised proves a note is the watch's own
// prose payload rather than a timer field: an output_match create carries one,
// and list and inspect both label the watch with it.
func TestConditionWatchNote_AcceptedAndSummarised(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	res, err := jm.configureWatch(watchArgs{Operation: "create", Target: rec.JobID, OutputMatch: "ready", Note: "why I care"})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if res.Note != "why I care" {
		t.Fatalf("create result note = %q, want %q", res.Note, "why I care")
	}
	live := jm.liveWatchSummaries()
	if len(live) != 1 || !strings.Contains(live[0].Condition, "note: why I care") {
		t.Fatalf("list condition = %+v, want it to carry the note", live)
	}
	inspect := jm.inspectWatchByID(res.WatchID)
	if !strings.Contains(inspect.Condition, "note: why I care") {
		t.Fatalf("inspect condition = %q, want it to carry the note", inspect.Condition)
	}
}

// TestConditionWatchNote_RidesTheFiredNotification proves the note reaches the
// model the same way a timer note does: on the notification the match enqueues,
// rendered in the block body.
func TestConditionWatchNote_RidesTheFiredNotification(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	if _, err := jm.configureWatch(watchArgs{Operation: "create", Target: rec.JobID, OutputMatch: "ready", Note: "deploy gate"}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	jm.feedJobOutput(rec.JobID, []byte("server ready\n"), 13)
	if len(notified) != 1 {
		t.Fatalf("match must fire once; got %d: %+v", len(notified), notified)
	}
	if notified[0].Note != "deploy gate" {
		t.Fatalf("notification note = %q, want %q", notified[0].Note, "deploy gate")
	}
	block := formatJobNotificationBlock(notified[0], notificationExcerpt{}, false)
	if !strings.Contains(block, "Note: deploy gate") {
		t.Fatalf("block lacks the note:\n%s", block)
	}
}

// TestSessionConditionWatchNote_RidesTheFiredNotification covers the
// session-target rail, whose watch block is rendered by its own branch.
func TestSessionConditionWatchNote_RidesTheFiredNotification(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	var notified []jobNotification
	jm.enqueue = func(n jobNotification) { notified = append(notified, n) }

	installWatchBelowValidation(t, jm, watchArgs{Target: "caller", Events: []string{"communicate"}, Note: "tool audit"})
	onSessionEventKD(jm, events.EventCommunicate, nil)

	if len(notified) != 1 {
		t.Fatalf("event must fire once; got %d: %+v", len(notified), notified)
	}
	if notified[0].Note != "tool audit" {
		t.Fatalf("notification note = %q, want %q", notified[0].Note, "tool audit")
	}
	block := formatJobNotificationBlock(notified[0], notificationExcerpt{}, false)
	if !strings.Contains(block, "Watch event triggered:") || !strings.Contains(block, "Note: tool audit") {
		t.Fatalf("session watch block lacks the note:\n%s", block)
	}
}

// TestStableReceiverWatchNote_RidesTheDurableFrame covers the stable-receiver
// rail (source="parent" / source="dlg_..."), whose fire is delivered as a watch
// frame rather than a rendered notification body. The note has to travel in the
// frame itself, because the frame is the whole payload the receiver reads.
func TestStableReceiverWatchNote_RidesTheDurableFrame(t *testing.T) {
	fixture := newStableWatchRuntimeBase(t, nil)
	if _, err := jobWatchToolWithContext(context.Background(), fixture.root, map[string]any{
		"operation": "create",
		"source":    "dlg_source",
		"events":    []any{"communicate"},
		"note":      "watching for the handoff",
	}, 4096); err != nil {
		t.Fatalf("create noted stable delegate watch: %v", err)
	}

	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "handoff"})

	frame := fixture.requireOnePending(t).state.Frame
	if !strings.Contains(frame, "note: watching for the handoff") {
		t.Fatalf("stable-receiver frame lacks the note:\n%s", frame)
	}
}
