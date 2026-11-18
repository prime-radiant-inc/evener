package agenttest

import (
	"sync"
	"testing"

	"primeradiant.com/evener/llm"
)

// stepAt builds a scripted step that answers with a marker message so tests
// can assert consumption order.
func stepAt(marker string) func(req llm.Request) llm.Response {
	return func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant(marker)} }
}

func TestScriptNextReturnsStepsInOrderThenExhausts(t *testing.T) {
	var s Script[func(req llm.Request) llm.Response]
	s.Replace([]func(req llm.Request) llm.Response{stepAt("one"), stepAt("two")})
	for _, want := range []string{"one", "two"} {
		step, ok := s.Next()
		if !ok {
			t.Fatalf("Next() ok = false, want step %q", want)
		}
		if got := step(llm.Request{}).Message.Text(); got != want {
			t.Errorf("Next() answered %q, want %q", got, want)
		}
	}
	if _, ok := s.Next(); ok {
		t.Error("Next() after exhaustion ok = true, want false")
	}
}

// The cursor is positional and survives a whole-script replacement: the drive
// keeps serving from the same position in the new sequence. This is the
// contract lane fixtures rely on when scripting mid-run turns.
func TestScriptReplacePreservesCursor(t *testing.T) {
	var s Script[func(req llm.Request) llm.Response]
	s.Replace([]func(req llm.Request) llm.Response{stepAt("one"), stepAt("two")})
	if _, ok := s.Next(); !ok {
		t.Fatal("first Next() not ok")
	}
	s.Replace([]func(req llm.Request) llm.Response{stepAt("three"), stepAt("four")})
	step, ok := s.Next()
	if !ok {
		t.Fatal("Next() after Replace not ok")
	}
	if got := step(llm.Request{}).Message.Text(); got != "four" {
		t.Errorf("Next() after Replace answered %q, want the same position in the new sequence %q", got, "four")
	}
}

// Reset replaces AND rewinds: a caller rerunning the same script per
// iteration gets the first step again, unlike Replace.
func TestScriptResetRewindsCursor(t *testing.T) {
	var s Script[func(req llm.Request) llm.Response]
	s.Replace([]func(req llm.Request) llm.Response{stepAt("one"), stepAt("two")})
	if _, ok := s.Next(); !ok {
		t.Fatal("first Next() not ok")
	}
	s.Reset([]func(req llm.Request) llm.Response{stepAt("three"), stepAt("four")})
	step, ok := s.Next()
	if !ok {
		t.Fatal("Next() after Reset not ok")
	}
	if got := step(llm.Request{}).Message.Text(); got != "three" {
		t.Errorf("Next() after Reset answered %q, want the first step of the new sequence %q", got, "three")
	}
}

func TestScriptShiftConsumesFromTheFront(t *testing.T) {
	var s Script[func(req llm.Request) llm.Response]
	s.Append(stepAt("one"), stepAt("two"))
	for _, want := range []string{"one", "two"} {
		step, ok := s.Shift()
		if !ok {
			t.Fatalf("Shift() ok = false, want step %q", want)
		}
		if got := step(llm.Request{}).Message.Text(); got != want {
			t.Errorf("Shift() answered %q, want %q", got, want)
		}
	}
	if s.Len() != 0 {
		t.Errorf("Len() = %d after shifting everything, want 0", s.Len())
	}
}

// TestScriptResistsConcurrentDriveAndReplacement pins the engine's
// synchronization: a drive loop consuming steps while another goroutine
// replaces and appends the script must be race-free under -race.
func TestScriptResistsConcurrentDriveAndReplacement(t *testing.T) {
	var s Script[func(req llm.Request) llm.Response]
	s.Replace([]func(req llm.Request) llm.Response{stepAt("a"), stepAt("b")})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 100 {
			s.Next()
		}
	}()
	go func() {
		defer wg.Done()
		for range 100 {
			s.Replace([]func(req llm.Request) llm.Response{stepAt("x"), stepAt("y")})
			s.Append(stepAt("z"))
		}
	}()
	wg.Wait()
}

// At reads a step positionally without consuming it, so a test can capture the
// scripted answer it is about to wrap and still expect that answer to be served.
func TestScriptAtReadsWithoutMovingTheCursor(t *testing.T) {
	var s Script[func(req llm.Request) llm.Response]
	s.Replace([]func(req llm.Request) llm.Response{stepAt("one"), stepAt("two"), stepAt("three"), stepAt("four")})
	// Consume a step first: with the cursor at zero, "did not move" and
	// "rewound to zero" are the same observation, so the assertion could not
	// tell the contract from its opposite.
	if _, ok := s.Next(); !ok {
		t.Fatal("first Next() not ok")
	}
	step, ok := s.At(2)
	if !ok {
		t.Fatal("At(2) ok = false, want true")
	}
	if got := step(llm.Request{}).Message.Text(); got != "three" {
		t.Errorf("At(2) answered %q, want %q", got, "three")
	}
	if got := s.Cursor(); got != 1 {
		t.Errorf("Cursor() after At = %d, want the surviving 1", got)
	}
	// The pending step is what Next serves: a rewind to zero would answer
	// "one" here, re-running a step the drive already consumed.
	pending, ok := s.Next()
	if !ok {
		t.Fatal("Next() ok = false, want the pending second step")
	}
	if got := pending(llm.Request{}).Message.Text(); got != "two" {
		t.Errorf("Next() after At answered %q, want the pending step %q", got, "two")
	}
	// And the step At read is still served in its own turn.
	read, ok := s.Next()
	if !ok {
		t.Fatal("Next() ok = false, want the step At read")
	}
	if got := read(llm.Request{}).Message.Text(); got != "three" {
		t.Errorf("Next() answered %q, want the step At read %q", got, "three")
	}
}

func TestScriptAtRejectsOutOfRangeIndexes(t *testing.T) {
	var s Script[func(req llm.Request) llm.Response]
	s.Replace([]func(req llm.Request) llm.Response{stepAt("only")})
	for _, index := range []int{-1, 1, 100} {
		if _, ok := s.At(index); ok {
			t.Errorf("At(%d) ok = true, want false", index)
		}
	}
}

// SetAt replaces a step in place and leaves the cursor alone, which is what
// lets a test wrap the answer at a position it has already read with At.
func TestScriptSetAtReplacesWithoutMovingTheCursor(t *testing.T) {
	var s Script[func(req llm.Request) llm.Response]
	s.Replace([]func(req llm.Request) llm.Response{stepAt("one"), stepAt("two"), stepAt("three"), stepAt("four")})
	// Advance past the middle before replacing: at cursor zero, "left the
	// cursor alone" and "rewound it to zero" read identically, and only the
	// second would silently re-run steps the drive already consumed.
	if _, ok := s.Next(); !ok {
		t.Fatal("first Next() not ok")
	}
	if !s.SetAt(2, stepAt("replaced")) {
		t.Fatal("SetAt(2) ok = false, want true")
	}
	if got := s.Cursor(); got != 1 {
		t.Errorf("Cursor() after SetAt = %d, want the surviving 1", got)
	}
	// The pending step still comes next, in order.
	pending, ok := s.Next()
	if !ok {
		t.Fatal("Next() ok = false, want the pending second step")
	}
	if got := pending(llm.Request{}).Message.Text(); got != "two" {
		t.Errorf("Next() after SetAt answered %q, want the pending step %q", got, "two")
	}
	// Then the replacement, at the position it was written to.
	replaced, ok := s.Next()
	if !ok {
		t.Fatal("Next() ok = false, want the replaced step")
	}
	if got := replaced(llm.Request{}).Message.Text(); got != "replaced" {
		t.Errorf("Next() answered %q, want the replacement %q", got, "replaced")
	}
	// Nothing past the replacement moved either.
	last, ok := s.Next()
	if !ok {
		t.Fatal("Next() ok = false, want the final step")
	}
	if got := last(llm.Request{}).Message.Text(); got != "four" {
		t.Errorf("Next() answered %q, want the untouched final step %q", got, "four")
	}
}

func TestScriptSetAtRejectsOutOfRangeIndexes(t *testing.T) {
	var s Script[func(req llm.Request) llm.Response]
	s.Replace([]func(req llm.Request) llm.Response{stepAt("only")})
	for _, index := range []int{-1, 1, 100} {
		if s.SetAt(index, stepAt("unused")) {
			t.Errorf("SetAt(%d) ok = true, want false", index)
		}
	}
}

// Cursor counts the steps Next consumed; Replace keeps it and Reset rewinds it,
// matching the two consumption contracts.
func TestScriptCursorTracksNext(t *testing.T) {
	var s Script[func(req llm.Request) llm.Response]
	if got := s.Cursor(); got != 0 {
		t.Errorf("Cursor() on an empty script = %d, want 0", got)
	}
	s.Replace([]func(req llm.Request) llm.Response{stepAt("one"), stepAt("two")})
	if _, ok := s.Next(); !ok {
		t.Fatal("first Next() not ok")
	}
	if got := s.Cursor(); got != 1 {
		t.Errorf("Cursor() after one Next = %d, want 1", got)
	}
	s.Replace([]func(req llm.Request) llm.Response{stepAt("x"), stepAt("y")})
	if got := s.Cursor(); got != 1 {
		t.Errorf("Cursor() after Replace = %d, want the surviving 1", got)
	}
	s.Reset([]func(req llm.Request) llm.Response{stepAt("fresh")})
	if got := s.Cursor(); got != 0 {
		t.Errorf("Cursor() after Reset = %d, want 0", got)
	}
}

// RepeatSteps repeats one step in order, which is how a script that answers the
// same way several times stays readable at the call site.
func TestRepeatStepsRepeatsTheSameStepInOrder(t *testing.T) {
	step := stepAt("same")
	steps := RepeatSteps(step, 3)
	if len(steps) != 3 {
		t.Fatalf("RepeatSteps(_, 3) len = %d, want 3", len(steps))
	}
	for i, got := range steps {
		if got == nil {
			t.Fatalf("RepeatSteps step %d is nil", i)
		}
		if text := got(llm.Request{}).Message.Text(); text != "same" {
			t.Errorf("RepeatSteps step %d answered %q, want %q", i, text, "same")
		}
	}
}

// Two scripts built from one step slice must own their slots. Retaining the
// caller's backing array would let SetAt through one script change the step the
// other serves, and a concurrent Next on the other would race that write under
// a different mutex. RepeatSteps hands one slice to both, so this is the shape
// every converted fixture that spreads repeats can hit.
func TestScriptsFromOneStepSliceDoNotShareSlots(t *testing.T) {
	shared := RepeatSteps(stepAt("original"), 2)
	var first, second Script[func(req llm.Request) llm.Response]
	first.Replace(shared)
	second.Replace(shared)
	if !first.SetAt(0, stepAt("replaced")) {
		t.Fatal("SetAt(0) ok = false, want true")
	}
	// The second script still serves the step its caller gave it.
	step, ok := second.Next()
	if !ok {
		t.Fatal("second Next() ok = false, want the original step")
	}
	if got := step(llm.Request{}).Message.Text(); got != "original" {
		t.Errorf("second script answered %q, want the original step %q; the two scripts share one backing array", got, "original")
	}
	// And the caller's own slice is untouched too, so a third script built
	// from it later cannot inherit the write.
	if got := shared[0](llm.Request{}).Message.Text(); got != "original" {
		t.Errorf("the caller's slice element 0 is %q, want %q; Replace wrote through to it", got, "original")
	}
	// Reset owns its slots the same way.
	var third, fourth Script[func(req llm.Request) llm.Response]
	third.Reset(shared)
	fourth.Reset(shared)
	// Write the slot the other script serves next, or the check below would
	// read a different element and pass whatever Reset did.
	if !third.SetAt(0, stepAt("reset-replaced")) {
		t.Fatal("SetAt(0) ok = false, want true")
	}
	step, ok = fourth.Next()
	if !ok {
		t.Fatal("fourth Next() ok = false, want the original step")
	}
	if got := step(llm.Request{}).Message.Text(); got != "original" {
		t.Errorf("script built with Reset answered %q, want the original step %q; Reset shares one backing array", got, "original")
	}
	if got := shared[0](llm.Request{}).Message.Text(); got != "original" {
		t.Errorf("the caller's slice element 0 is %q after Reset, want %q", got, "original")
	}
}

// A non-positive count yields no steps: a caller scripting zero repeats gets an
// empty script rather than a panic.
func TestRepeatStepsRejectsNonPositiveCounts(t *testing.T) {
	for _, n := range []int{0, -1} {
		if steps := RepeatSteps(stepAt("unused"), n); len(steps) != 0 {
			t.Errorf("RepeatSteps(_, %d) len = %d, want 0", n, len(steps))
		}
	}
}

// A repeated step feeds the engine exactly like a literal list of copies.
func TestScriptAcceptsRepeatedSteps(t *testing.T) {
	var s Script[func(req llm.Request) llm.Response]
	s.Replace(RepeatSteps(stepAt("again"), 2))
	if s.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", s.Len())
	}
	for range 2 {
		step, ok := s.Next()
		if !ok {
			t.Fatal("Next() ok = false, want a repeated step")
		}
		if got := step(llm.Request{}).Message.Text(); got != "again" {
			t.Errorf("Next() answered %q, want %q", got, "again")
		}
	}
	if _, ok := s.Next(); ok {
		t.Error("Next() after the repeats ok = true, want false")
	}
}
