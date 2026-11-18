package agenttest

import (
	"slices"
	"sync"

	"primeradiant.com/evener/llm"
)

// Script is a mutex-owned sequence of scripted steps. Every read and write of
// the sequence goes through its methods, so no adapter holding a Script can
// expose a directly assignable steps field for a test (or a future refactor)
// to race on. Two consumption models are supported: Next advances a cursor
// and keeps the script (steps may be replaced mid-run, the cursor survives),
// while Shift consumes steps from the front.
type Script[T any] struct {
	mu    sync.Mutex
	steps []T
	i     int
}

// Next returns the step at the cursor and advances it, keeping the script.
// ok is false once the cursor is past the last step.
func (s *Script[T]) Next() (step T, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.i >= len(s.steps) {
		return step, false
	}
	step = s.steps[s.i]
	s.i++
	return step, true
}

// Shift removes and returns the first step. ok is false when no step remains.
func (s *Script[T]) Shift() (step T, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.steps) == 0 {
		return step, false
	}
	step = s.steps[0]
	s.steps = s.steps[1:]
	return step, true
}

// Replace swaps the whole script. The cursor survives: a drive that already
// consumed k steps answers the new script's step k+1.
//
// Replace takes its own copy of the steps. A caller that spreads one slice
// into two scripts — newFakeAdapter("a", steps...), newFakeAdapter("b",
// steps...) — would otherwise leave both sharing one backing array, so a
// SetAt through one script would change the step the other serves, and a
// concurrent Next on the other would race that write under a different mutex.
func (s *Script[T]) Replace(steps []T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps = slices.Clone(steps)
}

// Reset swaps the whole script and rewinds the cursor to the first step.
// Unlike Replace, which keeps a mid-run drive's position, Reset starts the
// sequence over: a caller rerunning the same script per iteration gets the
// first step again. Like Replace, it takes its own copy of the steps.
func (s *Script[T]) Reset(steps []T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps = slices.Clone(steps)
	s.i = 0
}

// Append adds steps to the end of the script.
func (s *Script[T]) Append(steps ...T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps = append(s.steps, steps...)
}

// Len reports the scripted step count.
func (s *Script[T]) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.steps)
}

// At returns the step at index without moving the cursor. ok is false when
// index is out of range. It pairs with SetAt for tests that capture a step and
// wrap it with extra assertions.
func (s *Script[T]) At(index int) (step T, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < 0 || index >= len(s.steps) {
		return step, false
	}
	return s.steps[index], true
}

// SetAt replaces the step at index in place, leaving the cursor alone. ok is
// false when index is out of range.
func (s *Script[T]) SetAt(index int, step T) (ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < 0 || index >= len(s.steps) {
		return false
	}
	s.steps[index] = step
	return true
}

// Cursor reports how many steps Next has consumed.
func (s *Script[T]) Cursor() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.i
}

// TryState reports the cursor and the step count together without waiting for
// a writer. ok is false when a replacement holds the mutex, which tells a
// reader that must never block to treat the script as busy and try again.
func (s *Script[T]) TryState() (cursor, steps int, ok bool) {
	if !s.mu.TryLock() {
		return 0, 0, false
	}
	defer s.mu.Unlock()
	return s.i, len(s.steps), true
}

// HoldForTest takes the script's mutex and holds it until release runs, so a
// test can stand for the replacement whose hold a reader that must not block
// has to report instead of wait on. Test seam only: no shipping code calls it.
func (s *Script[T]) HoldForTest() (release func()) {
	s.mu.Lock()
	return s.mu.Unlock
}

// RepeatSteps returns n copies of step, for a script that answers the same way
// several times in a row. Passing the result with ... keeps the call site from
// repeating one closure textually n times, which is what gocritic's dupOption
// flags in a variadic constructor.
func RepeatSteps(step func(req llm.Request) llm.Response, n int) []func(req llm.Request) llm.Response {
	if n <= 0 {
		return nil
	}
	steps := make([]func(req llm.Request) llm.Response, n)
	for i := range steps {
		steps[i] = step
	}
	return steps
}
