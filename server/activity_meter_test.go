package server

import (
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
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
