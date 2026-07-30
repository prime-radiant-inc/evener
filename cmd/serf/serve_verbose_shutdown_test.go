package main

import (
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/server"
)

// TestVerboseTeeOutlivesTheBridgeDrain reproduces the shutdown crash and pins
// the ordering that prevents it.
//
// serve.go's `--verbose` tee is closed when serve returns. The bridge drain is a
// separate goroutine that keeps delivering a closed session's BUFFERED TAIL to
// the observer, and the tee's non-blocking send does not protect a closed
// channel: `case t.ch <- ev` panics with "send on closed channel" regardless of
// the select. The panic lands on the drain goroutine, so it takes the process
// down and every defer registered before the tee's -- cancel, the API log close,
// the client close -- never runs. The daemon exits with a crash dump instead of
// shutting down.
//
// The ordering under test is that the tee outlives every drain, established by
// waiting on the drain's completion signal rather than assuming it has finished.
// Without the fix this test panics; the panic is on another goroutine, so it
// cannot be recovered here -- it takes the whole test binary down, which is
// precisely how loud this failure deserves to be.
func TestVerboseTeeOutlivesTheBridgeDrain(t *testing.T) {
	for range 20 {
		verboseTeeShutdownRound(t)
	}
}

func verboseTeeShutdownRound(t *testing.T) {
	t.Helper()
	sess, err := agent.NewSession(llm.NewClient(), provider.NewOpenAIProfile("gpt-5.4-mini"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()), agent.SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	tee := newVerboseEventTee(newDiscardWriter(), verboseEventTeeBuffer)
	drain := make(chan struct{})
	defaultServeDeps().bridge(server.NewServer(server.ServerConfig{}), sess, tee.observe,
		func() { close(drain) })

	// Leave a tail in the buffer that the drain has not delivered yet, which is
	// the state a real daemon is in when a turn was in flight at shutdown.
	for range 20 {
		sess.SetReasoningEffort("high")
	}

	// serve.go's shutdown order: close the session, then let serve return and
	// run `defer tee.close()`.
	sess.Close()
	// Budgeted, not a bare park: this branch's own rule. An unbudgeted receive
	// here made two drain-signal mutations hang the package instead of naming
	// themselves.
	select {
	case <-drain:
	case <-time.After(30 * time.Second):
		t.Fatal("the bridge drain never reported completion; the tee cannot be closed safely")
	}
	tee.close()
}

type discardWriter struct{ mu sync.Mutex }

func newDiscardWriter() *discardWriter { return &discardWriter{} }

func (d *discardWriter) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(p), nil
}

// TestBridgeDrainSignalsCompletion pins the signal the ordering above depends
// on: the drain must report when it has finished, or a closer can only guess.
func TestBridgeDrainSignalsCompletion(t *testing.T) {
	sess, err := agent.NewSession(llm.NewClient(), provider.NewOpenAIProfile("gpt-5.4-mini"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()), agent.SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	var delivered int
	var mu sync.Mutex
	drain := make(chan struct{})
	defaultServeDeps().bridge(server.NewServer(server.ServerConfig{}), sess, func(events.SessionEvent) {
		mu.Lock()
		delivered++
		mu.Unlock()
	}, func() { close(drain) })

	select {
	case <-drain:
		t.Fatal("drain reported completion while the session was still open")
	default:
	}

	sess.SetReasoningEffort("high")
	sess.Close()

	select {
	case <-drain:
	case <-time.After(30 * time.Second):
		t.Fatal("drain never reported completion after Close")
	}

	// Completion must mean DRAINED, not merely stopped: everything the session
	// emitted has reached the observer by the time the signal fires.
	mu.Lock()
	defer mu.Unlock()
	if delivered == 0 {
		t.Fatal("drain signalled completion having delivered nothing")
	}
}
