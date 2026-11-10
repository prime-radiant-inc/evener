package agenttest

import "sync"

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
func (s *Script[T]) Replace(steps []T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps = steps
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
