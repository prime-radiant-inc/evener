package transcript

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func newPlacementWriter(t *testing.T) *Writer {
	t.Helper()
	w, _ := newRecordWriter(t)
	return w
}

func recordPlaced(t *testing.T, w *Writer, turn schema.Turn, p Placement) schema.Turn {
	t.Helper()
	r, err := w.Record(turn, RecordOptions{Door: DoorBuffered, Place: p})
	if err != nil || !r.Recorded {
		t.Fatalf("record: %+v, %v", r, err)
	}
	return r.Turn
}

func completionTurn() schema.Turn {
	return schema.NewTurn(schema.TurnCompletion, llm.Message{Role: llm.RoleUser})
}

func TestPlacementFollowsTheRunningExecutionGapAndPrelude(t *testing.T) {
	w := newPlacementWriter(t)
	rec := func(p Placement) schema.Turn {
		t.Helper()
		turn := recordPlaced(t, w, steeringTurn("x"), p)
		if turn.Format != schema.TurnFormatIdentity {
			t.Fatalf("format marker missing: %+v", turn)
		}
		return turn
	}
	p1, p2 := rec(PlaceSession), rec(PlaceSession)
	if p1.TurnID != appwire.SystemPreludeTurnID || p1.TurnKind != schema.TurnSpanPrelude || p2.TurnID != p1.TurnID || p2.TurnKind != "" {
		t.Fatalf("prelude = %+v, %+v", p1, p2)
	}
	w.BeginExecution("turn_m1", false)
	if w.RunningTurnID() != "turn_m1" {
		t.Fatalf("running = %q", w.RunningTurnID())
	}
	e1, e2 := rec(PlaceSession), rec(PlaceAsync)
	if e1.TurnID != "turn_m1" || e1.TurnKind != schema.TurnSpanExecution || e2.TurnID != "turn_m1" || e2.TurnKind != "" {
		t.Fatalf("execution = %+v, %+v", e1, e2)
	}
	done := recordPlaced(t, w, completionTurn(), PlaceCompletion)
	if done.TurnID != "turn_m1" || done.TurnKind != "" || w.RunningTurnID() != "" {
		t.Fatalf("completion = %+v, running %q", done, w.RunningTurnID())
	}
	g1, g2 := rec(PlaceSession), rec(PlaceSession)
	if !strings.HasPrefix(g1.TurnID, "t_") || g1.TurnKind != schema.TurnSpanGap || g2.TurnID != g1.TurnID || g2.TurnKind != "" {
		t.Fatalf("gap = %+v, %+v", g1, g2)
	}
	d := rec(PlaceAsync)
	if !strings.HasPrefix(d.TurnID, "t_") || d.TurnID == g1.TurnID || d.TurnKind != schema.TurnSpanDelivery {
		t.Fatalf("idle async = %+v", d)
	}
	g3 := rec(PlaceSession)
	if g3.TurnID == g1.TurnID || g3.TurnKind != schema.TurnSpanGap {
		t.Fatalf("a delivery entry must close the gap: %+v", g3)
	}
	if again := rec(PlaceSession); again.TurnID != g3.TurnID || again.TurnKind != "" {
		t.Fatalf("second gap entry = %+v", again)
	}
	explicit := rec(PlaceInTurn("turn_m7"))
	if explicit.TurnID != "turn_m7" || explicit.TurnKind != "" {
		t.Fatalf("explicit = %+v", explicit)
	}
	if g4 := rec(PlaceSession); g4.TurnID == g3.TurnID || g4.TurnKind != schema.TurnSpanGap {
		t.Fatalf("an entry of another turn must close the gap: %+v", g4)
	}
	w.BeginExecution("turn_m8", false)
	if e := rec(PlaceSession); e.TurnID != "turn_m8" || e.TurnKind != schema.TurnSpanExecution {
		t.Fatalf("second execution = %+v", e)
	}
	if p := rec(PlaceSession); p.TurnID == appwire.SystemPreludeTurnID {
		t.Fatal("an execution must end the prelude")
	}
}

func TestVerbatimPlacementWritesTheTurnAsGiven(t *testing.T) {
	w := newPlacementWriter(t)
	copied := steeringTurn("copy")
	copied.TurnID, copied.Format, copied.TurnKind = "turn_m9", schema.TurnFormatIdentity, schema.TurnSpanExecution
	if got := recordPlaced(t, w, copied, PlaceVerbatim); got.TurnID != "turn_m9" || got.TurnKind != schema.TurnSpanExecution {
		t.Fatalf("verbatim = %+v", got)
	}
	if got := recordPlaced(t, w, steeringTurn("legacy copy"), PlaceVerbatim); got.Format != 0 || got.TurnID != "" || got.TurnKind != "" {
		t.Fatalf("verbatim stamped a legacy copy: %+v", got)
	}
	// Copies leave the prelude and gap state alone.
	if got := recordPlaced(t, w, steeringTurn("startup"), PlaceSession); got.TurnKind != schema.TurnSpanPrelude {
		t.Fatalf("after verbatim copies = %+v", got)
	}
}

func TestCompletionWithNoRunningExecutionRecordsNothing(t *testing.T) {
	w := newPlacementWriter(t)
	r, err := w.Record(completionTurn(), RecordOptions{Door: DoorDurable, Place: PlaceCompletion})
	if err != nil || r.Recorded {
		t.Fatalf("completion without an execution = %+v, %v", r, err)
	}
}

func TestReopenedExecutionDoesNotRestampTurnKind(t *testing.T) {
	w := newPlacementWriter(t)
	w.BeginExecution("turn_m3", true)
	reopen := schema.NewTurn(schema.TurnReopen, llm.Message{Role: llm.RoleUser})
	if got := recordPlaced(t, w, reopen, PlaceSession); got.TurnID != "turn_m3" || got.TurnKind != "" {
		t.Fatalf("reopen marker = %+v", got)
	}
}

func TestResumedTailIsNotInThePrelude(t *testing.T) {
	path := newSharedFileTranscript(t)
	w := openSharedFileWriter(t, path)
	defer w.Close() //nolint:errcheck // fixture
	if got := recordPlaced(t, w, steeringTurn("after resume"), PlaceSession); got.TurnKind != schema.TurnSpanGap {
		t.Fatalf("resume startup entry = %+v; want a gap turn", got)
	}
}

func TestAsyncRecordAfterCompletionTakesADeliveryTurn(t *testing.T) {
	w := newPlacementWriter(t)
	w.BeginExecution("turn_m4", false)
	recordPlaced(t, w, steeringTurn("in turn"), PlaceSession)
	recordPlaced(t, w, completionTurn(), PlaceCompletion)
	late, err := w.Record(steeringTurn("late attention"), RecordOptions{Door: DoorSynced, Place: PlaceAsync})
	if err != nil || late.Turn.TurnID == "turn_m4" || late.Turn.TurnKind != schema.TurnSpanDelivery {
		t.Fatalf("late async write = %+v, %v", late.Turn, err)
	}
}

func TestColdWriterOnASharedFileTakesADeliveryTurnMidExecution(t *testing.T) {
	path := newSharedFileTranscript(t)
	session := openSharedFileWriter(t, path)
	defer session.Close() //nolint:errcheck // fixture
	session.BeginExecution("turn_m5", false)
	recordPlaced(t, session, steeringTurn("running"), PlaceSession)
	cold := openSharedFileWriter(t, path)
	r, err := cold.Record(steeringTurn("cold"), RecordOptions{Door: DoorSynced, Place: PlaceDelivery})
	if closeErr := cold.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil || r.Turn.TurnKind != schema.TurnSpanDelivery || r.Turn.TurnID == "turn_m5" {
		t.Fatalf("cold = %+v, %v", r.Turn, err)
	}
	if after := recordPlaced(t, session, steeringTurn("still running"), PlaceSession); after.TurnID != "turn_m5" || after.TurnKind != "" {
		t.Fatalf("the execution span must continue after a cold write: %+v", after)
	}
}

func TestPlacementStateAdvancesOnlyForRecordedEntries(t *testing.T) {
	w := newPlacementWriter(t)
	w.BeginExecution("turn_m6", false)
	w.mu.Lock()
	w.poisoned = true // every append now fails without recording
	w.mu.Unlock()
	if r, err := w.Record(steeringTurn("lost"), RecordOptions{Place: PlaceSession}); err == nil || r.Recorded {
		t.Fatalf("poisoned append = %+v, %v", r, err)
	}
	w.mu.Lock()
	w.poisoned = false
	w.mu.Unlock()
	if got := recordPlaced(t, w, steeringTurn("first recorded"), PlaceSession); got.TurnKind != schema.TurnSpanExecution {
		t.Fatalf("the first recorded entry must carry the TurnKind the lost one never recorded: %+v", got)
	}
}
