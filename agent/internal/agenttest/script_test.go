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
	s.Replace([]func(req llm.Request) llm.Response{stepAt("one"), stepAt("two")})
	step, ok := s.At(1)
	if !ok {
		t.Fatal("At(1) ok = false, want true")
	}
	if got := step(llm.Request{}).Message.Text(); got != "two" {
		t.Errorf("At(1) answered %q, want %q", got, "two")
	}
	if s.Cursor() != 0 {
		t.Errorf("Cursor() after At = %d, want 0", s.Cursor())
	}
	first, ok := s.Next()
	if !ok {
		t.Fatal("Next() ok = false, want the first step")
	}
	if got := first(llm.Request{}).Message.Text(); got != "one" {
		t.Errorf("Next() after At answered %q, want the first step %q", got, "one")
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
	s.Replace([]func(req llm.Request) llm.Response{stepAt("one"), stepAt("two")})
	if !s.SetAt(0, stepAt("replaced")) {
		t.Fatal("SetAt(0) ok = false, want true")
	}
	if s.Cursor() != 0 {
		t.Errorf("Cursor() after SetAt = %d, want 0", s.Cursor())
	}
	step, ok := s.Next()
	if !ok {
		t.Fatal("Next() ok = false, want the replaced step")
	}
	if got := step(llm.Request{}).Message.Text(); got != "replaced" {
		t.Errorf("Next() after SetAt answered %q, want %q", got, "replaced")
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
