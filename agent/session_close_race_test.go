package agent

import (
	"sync"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// TestSession_Close_NoRaceWithConcurrentEmit covers the PRI-1939 residual: a
// caller-owned goroutine emitting (e.g. Enqueue's EventQueueChanged, or the
// ProcessInput loop) concurrently with Close() closing the events channel. The
// session cannot join caller-owned goroutines, so before the fix emit() relied
// on a defer-recover() to swallow the resulting send-on-closed-channel — a real
// DATA RACE the detector flags. After the fix, emit and close are mutually
// excluded by eventsMu, so the send never races the close and recover() is gone.
// This hammers emit() directly (the mechanism under test) for a reliable repro.
func TestSession_Close_NoRaceWithConcurrentEmit(t *testing.T) {
	for i := 0; i < 20; i++ {
		dir := t.TempDir()
		c := llm.NewClient()
		c.Register(&fakeAdapter{
			name: "openai",
			steps: []func(req llm.Request) llm.Response{
				func(req llm.Request) llm.Response { return finalResponse("done") },
			},
		})
		sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				sess.emit(events.EventWarning, events.WarningData{Message: "concurrent"})
			}
		}()
		// Close concurrently with the caller-owned emits.
		sess.Close()
		wg.Wait()
	}
}
