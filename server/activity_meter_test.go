package server

import (
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// activityTestClock is a hand-advanced clock for the meter.
type activityTestClock struct{ now time.Time }

func (c *activityTestClock) Now() time.Time { return c.now }

// activityTestStart is a ten-second slot boundary (1_800_000_000 is a multiple
// of ten), so each test knows which slot an event lands in.
var activityTestStart = time.Unix(1_800_000_000, 0).UTC()

func startedMeter() (*activityMeter, *activityTestClock) {
	clock := &activityTestClock{now: activityTestStart}
	meter := &activityMeter{now: clock.Now}
	meter.restart()
	return meter, clock
}

// An item finishing (a user message, an assistant message, a tool call, a
// delivered communicate) and each tool output delta count toward the newest
// bar; streaming and bookkeeping events do not (spec 16.4: "transcript items
// and tool output events").
func TestActivityMeterCountsFinishedItemsAndToolOutputInTheNewestMinute(t *testing.T) {
	meter, _ := startedMeter()
	for _, kind := range []events.EventKind{
		events.EventUserInput,
		events.EventAssistantTextEnd,
		events.EventToolCallEnd,
		events.EventCommunicate,
		events.EventToolCallOutputDelta,
		events.EventToolCallOutputDelta,
		events.EventAssistantTextDelta,
		events.EventToolCallStart,
		events.EventExecutionStarted,
		events.EventReasoningSummaryDelta,
		events.EventTaskUpdated,
		events.EventModelRetry,
	} {
		meter.observe(kind)
	}
	if got, want := meter.snapshot().Minutes, []int{0, 0, 0, 0, 0, 0, 6}; !reflect.DeepEqual(got, want) {
		t.Fatalf("minutes = %v, want %v", got, want)
	}
}

// Each bar is a sliding minute ending at the read: an event moves to the next
// older bar sixty seconds after it happened and leaves the meter after seven
// minutes. A quiet minute is zero.
func TestActivityMeterSlidesAnEventThroughOlderBarsAndDropsItAfterSevenMinutes(t *testing.T) {
	meter, clock := startedMeter()
	meter.observe(events.EventUserInput)
	for _, step := range []struct {
		after time.Duration
		want  []int
	}{
		{59 * time.Second, []int{0, 0, 0, 0, 0, 0, 1}},
		{60 * time.Second, []int{0, 0, 0, 0, 0, 1, 0}},
		{3*time.Minute + 30*time.Second, []int{0, 0, 0, 1, 0, 0, 0}},
		{6*time.Minute + 59*time.Second, []int{1, 0, 0, 0, 0, 0, 0}},
		{7 * time.Minute, []int{0, 0, 0, 0, 0, 0, 0}},
	} {
		clock.now = activityTestStart.Add(step.after)
		if got := meter.snapshot().Minutes; !reflect.DeepEqual(got, step.want) {
			t.Fatalf("after %s: minutes = %v, want %v", step.after, got, step.want)
		}
	}
}

// A slot reused on a later lap of the ring counts only its own events.
func TestActivityMeterReusedSlotForgetsItsEarlierLap(t *testing.T) {
	meter, clock := startedMeter()
	meter.observe(events.EventUserInput)
	meter.observe(events.EventUserInput)
	clock.now = activityTestStart.Add(7 * time.Minute) // the same ring position, one lap later
	meter.observe(events.EventToolCallOutputDelta)
	if got, want := meter.snapshot().Minutes, []int{0, 0, 0, 0, 0, 0, 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("minutes = %v, want %v", got, want)
	}
}

// The quiet clock starts when the meter starts, and any transcript motion
// (streaming included) moves it; bookkeeping does not.
func TestActivityMeterQuietClockMovesOnTranscriptMotionOnly(t *testing.T) {
	meter, clock := startedMeter()
	if got, want := meter.snapshot().LastActivityAt, activityTestStart.UnixMilli(); got != want {
		t.Fatalf("a meter nothing has moved reports %d, want its start %d", got, want)
	}
	clock.now = activityTestStart.Add(90 * time.Second)
	meter.observe(events.EventTaskUpdated)
	if got, want := meter.snapshot().LastActivityAt, activityTestStart.UnixMilli(); got != want {
		t.Fatalf("a status change moved the quiet clock to %d, want %d", got, want)
	}
	meter.observe(events.EventReasoningSummaryDelta)
	if got, want := meter.snapshot().LastActivityAt, clock.now.UnixMilli(); got != want {
		t.Fatalf("a model thinking left the quiet clock at %d, want %d", got, want)
	}
	if got, want := meter.snapshot().Minutes, []int{0, 0, 0, 0, 0, 0, 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("streaming counted toward a bar: minutes = %v", got)
	}
}

// A clock that steps backward (a resync, a corrected wall clock) must not
// un-mark motion that already happened: the quiet clock only moves forward.
func TestActivityMeterQuietClockNeverMovesBackward(t *testing.T) {
	meter, clock := startedMeter()
	clock.now = activityTestStart.Add(90 * time.Second)
	meter.observe(events.EventUserInput)
	later := clock.now
	clock.now = activityTestStart.Add(30 * time.Second) // a step backward
	meter.observe(events.EventUserInput)
	if got, want := meter.snapshot().LastActivityAt, later.UnixMilli(); got != want {
		t.Fatalf("a clock step backward moved the quiet clock to %d, want it to stay at %d", got, want)
	}
}

func TestActivityMeterReadsNothingBeforeItStarts(t *testing.T) {
	if got := (&activityMeter{}).snapshot(); got != nil {
		t.Fatalf("an unstarted meter reported %+v, want nil", got)
	}
}

// toolCallStart is a root tool call carrying the intent the agent promotes to
// the event's description (session_tools.go).
func toolCallStart(description string) events.SessionEvent {
	return events.SessionEvent{
		Kind:      events.EventToolCallStart,
		SessionID: "root",
		Data:      events.ToolCallStartData{ToolName: "read_file", ArgumentsJSON: "{}", Description: description},
	}
}

// A Working row says what the session last set out to do: the intent of the
// newest tool call the root itself started.
func TestActivityMeterNamesTheRootsNewestToolIntent(t *testing.T) {
	meter, _ := startedMeter()
	meter.noteIntent(toolCallStart("Reading the board's row tests."))
	if got, want := meter.snapshot().LatestIntent, "Reading the board's row tests."; got != want {
		t.Fatalf("latest intent = %q, want %q", got, want)
	}
	meter.noteIntent(toolCallStart("Editing the why line."))
	if got, want := meter.snapshot().LatestIntent, "Editing the why line."; got != want {
		t.Fatalf("after a second call the intent = %q, want %q", got, want)
	}
}

// A call that states no intent is not news: the line keeps the last one it was
// given rather than going blank mid-turn.
func TestActivityMeterKeepsTheIntentWhenAToolCallStatesNone(t *testing.T) {
	meter, _ := startedMeter()
	meter.noteIntent(toolCallStart("Reading the board's row tests."))
	meter.noteIntent(toolCallStart(""))
	if got, want := meter.snapshot().LatestIntent, "Reading the board's row tests."; got != want {
		t.Fatalf("latest intent = %q, want the earlier one %q", got, want)
	}
}

// Motion alone never words a row: only noteIntent sets the intent, and the
// descendant path observes without noting it, so a subagent's calls cannot put
// their words on the root's row.
func TestActivityMeterLeavesTheIntentToRootToolCalls(t *testing.T) {
	meter, _ := startedMeter()
	meter.observe(events.EventToolCallStart)
	if got := meter.snapshot().LatestIntent; got != "" {
		t.Fatalf("motion alone set the intent to %q, want none", got)
	}
}

// A new turn is new work: the row shows this turn's intent, not the words of a
// turn that finished an hour ago.
func TestActivityMeterDropsTheIntentWhenATurnStarts(t *testing.T) {
	meter, _ := startedMeter()
	meter.noteIntent(toolCallStart("Reading the board's row tests."))
	meter.noteIntent(events.SessionEvent{Kind: events.EventExecutionStarted, SessionID: "root"})
	if got := meter.snapshot().LatestIntent; got != "" {
		t.Fatalf("intent after a turn started = %q, want none", got)
	}
}

// The intent is a row's why line, so it travels as one bounded line: runs of
// whitespace become single spaces, and text past the bound is cut.
func TestActivityMeterBoundsTheIntentToOneLine(t *testing.T) {
	meter, _ := startedMeter()
	meter.noteIntent(toolCallStart("Reading\nthe   board's row tests."))
	if got, want := meter.snapshot().LatestIntent, "Reading the board's row tests."; got != want {
		t.Fatalf("latest intent = %q, want one line %q", got, want)
	}
	meter.noteIntent(toolCallStart(strings.Repeat("x", appwire.MaxIntentRunes+50)))
	if got := meter.snapshot().LatestIntent; utf8.RuneCountInString(got) > appwire.MaxIntentRunes {
		t.Fatalf("intent runs %d runes, want at most %d", utf8.RuneCountInString(got), appwire.MaxIntentRunes)
	}
}

// A replaced identity is a different session: its meter starts clean, intent
// included.
func TestActivityMeterRestartDropsTheIntent(t *testing.T) {
	meter, _ := startedMeter()
	meter.noteIntent(toolCallStart("Reading the board's row tests."))
	meter.restart()
	if got := meter.snapshot().LatestIntent; got != "" {
		t.Fatalf("intent after restart = %q, want none", got)
	}
}
