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
